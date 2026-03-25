/*
Package main implements a lightweight, in-memory Telegram chatbot using the OpenAI API.

This bot is intentionally designed to be very simple and minimal, serving a small group
of friends and family (up to 10-20 users).

All chat history and token usage stats are stored purely in memory. There is no persistent
storage (no databases, no disk saving), and state is intentionally lost on restart.
*/
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/openai"
	"github.com/levmv/golems/pkg/telegram"
)

type UsageStats struct {
	PromptTokens     int
	CachedTokens     int
	CompletionTokens int
	TotalTokens      int
}

type ChatMessage struct {
	OpenAI      openai.ChatCompletionMessage
	TelegramIDs []int64
}

type ChatSession struct {
	mu           sync.Mutex
	Messages     []ChatMessage
	Stats        UsageStats
	LastActive   time.Time
	useReasoning bool
	cancelLLM    context.CancelFunc
}

var (
	log       logger.Logger
	oai       *openai.Client
	adminID   int64
	whitelist []int64

	sessions   = make(map[int64]*ChatSession)
	sessionsMu sync.RWMutex
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log = logger.Default()
	oai = openai.NewClientWithConfig(openai.DeepSeekConfig(os.Getenv("OPENAI_API_KEY")))
	adminID, _ = strconv.ParseInt(strings.TrimSpace(os.Getenv("TELEGRAM_ADMIN")), 10, 64)
	whitelist = parseWhitelist(os.Getenv("TELEGRAM_WHITELIST"))

	bot, err := telegram.New(
		os.Getenv("TELEGRAM_BOT_TOKEN"),
		os.Getenv("WEBHOOK_SECRET_TOKEN"),
		// telegram.WithDebug(),
		telegram.WithLogger(log),
		telegram.WithAuthFunc(authMiddleware),
	)
	if err != nil {
		log.Info("Failed to create bot: %v", err)
		return
	}

	bot.OnCommand("start", func(c *telegram.Context) error { return nil })
	bot.OnCommand("think", handleThink)
	bot.OnCommand("reset", handleReset)
	bot.OnCommand("stats", handleStats)

	bot.OnText(handleUpdate)

	bot.SetMyCommands(ctx, telegram.SetMyCommandsRequest{
		Commands: []telegram.BotCommand{
			{Command: "think", Description: "Think mode till reset"},
			{Command: "reset", Description: "Finish conversation"},
			{Command: "stats", Description: "View token usage stats"},
		},
	})

	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		log.Info("Starting bot in polling mode")
		bot.StartPolling(ctx)
		return
	}

	_, err = bot.SetWebhook(ctx, &telegram.SetWebhookParams{
		URL:         webhookURL,
		SecretToken: os.Getenv("WEBHOOK_SECRET_TOKEN"),
	})
	if err != nil {
		log.Info("Failed to set webhook: %v", err)
	}

	listenAddr := ":" + cmp.Or(os.Getenv("PORT"), "8443")
	go func() {
		log.Info("Starting HTTP server on %s", listenAddr)
		if err := http.ListenAndServe(listenAddr, bot.WebhookHandler()); err != nil {
			log.Info("HTTP server error: %v", err)
			cancel()
		}
	}()
	bot.Start(ctx)
	log.Info("Shutting down bot...")
}

func authMiddleware(c *telegram.Context) (bool, string) {
	if c.Update.Message == nil || c.Update.Message.From == nil {
		return false, "no user info"
	}

	userID := c.Update.Message.From.ID

	if slices.Contains(whitelist, userID) {
		return true, "whitelisted"
	}

	if c.Command == "start" {
		_ = c.Reply(fmt.Sprintf("Your user ID is %d. Ask the owner to whitelist it.", userID))
	}

	return false, "not whitelisted"
}

func getSession(chatID int64) *ChatSession {
	sessionsMu.RLock()
	sess, ok := sessions[chatID]
	sessionsMu.RUnlock()

	if ok {
		return sess
	}

	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	if sess, ok = sessions[chatID]; ok {
		return sess
	}

	sess = &ChatSession{
		Messages:   []ChatMessage{{OpenAI: systemPrompt(), TelegramIDs: nil}},
		LastActive: time.Now(),
	}
	sessions[chatID] = sess

	return sess
}

func (s *ChatSession) AddUsage(u *openai.Usage) {
	if u == nil {
		return
	}
	s.Stats.PromptTokens += u.PromptTokens
	s.Stats.CompletionTokens += u.CompletionTokens
	s.Stats.TotalTokens += u.TotalTokens
	if u.PromptTokensDetails != nil {
		s.Stats.CachedTokens += u.PromptTokensDetails.CachedTokens
	}
}

func handleUpdate(ctx *telegram.Context) error {
	sess := getSession(ctx.ChatID)

	sess.mu.Lock()

	if time.Since(sess.LastActive) > 6*time.Hour {
		log.Info("Auto-resetting session for %d due to inactivity", ctx.ChatID)
		sess.Messages = []ChatMessage{{OpenAI: systemPrompt(), TelegramIDs: nil}}
		sess.useReasoning = false
	}

	if sess.cancelLLM != nil {
		sess.cancelLLM()
	}

	lastIdx := len(sess.Messages) - 1
	if lastIdx >= 0 && sess.Messages[lastIdx].OpenAI.Role == openai.RoleUser {
		// Concatenate text to the existing user message
		sess.Messages[lastIdx].OpenAI.Content += "\n\n" + ctx.Update.Message.Text
		sess.Messages[lastIdx].TelegramIDs = append(sess.Messages[lastIdx].TelegramIDs, ctx.Update.Message.ID)
	} else {
		// Add as a brand-new user message
		sess.Messages = append(sess.Messages, ChatMessage{
			OpenAI: openai.ChatCompletionMessage{
				Role:    openai.RoleUser,
				Content: ctx.Update.Message.Text,
			},
			TelegramIDs: []int64{ctx.Update.Message.ID},
		})
	}
	sess.LastActive = time.Now()

	llmCtx, cancel := context.WithCancel(context.Background())
	sess.cancelLLM = cancel

	reqMessages := make([]openai.ChatCompletionMessage, 0, len(sess.Messages))
	for _, m := range sess.Messages {
		reqMessages = append(reqMessages, m.OpenAI)
	}
	useReasoning := sess.useReasoning

	sess.mu.Unlock()

	go func() {
		defer cancel()

		typing := ctx.Bot.StartTypingIndicator(ctx, ctx.ChatID)
		defer typing.Stop()

		req := openai.ChatCompletionRequest{
			Model:    openai.DeepSeekChat,
			Messages: reqMessages,
		}
		if useReasoning {
			req.Model = openai.DeepSeekReasoner
		}

		resp, err := oai.CreateChatCompletion(llmCtx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				log.Info("LLM request canceled for chat %d", ctx.ChatID)
				return
			}
			log.Info("Create chat completion error: %v", err)
			_, _ = ctx.Bot.SendMessage(context.Background(), &telegram.SendMessageParams{
				ChatID: ctx.ChatID,
				Text:   fmt.Sprintf("Error calling model: %v", err),
			})
			return
		}

		assistantContent := resp.Choices[0].Message.Content

		sentMsgs, err := ctx.ReplyChunked(assistantContent)
		if err != nil {
			log.Info("Failed to send chunked message to Telegram: %v", err)
		}

		var botMsgIDs []int64
		for _, m := range sentMsgs {
			if m != nil {
				botMsgIDs = append(botMsgIDs, m.ID)
			}
		}

		sess.mu.Lock()
		defer sess.mu.Unlock()

		if llmCtx.Err() != nil {
			log.Info("LLM response discarded for chat %d (superseded by new message)", ctx.ChatID)
			return
		}

		sess.Messages = append(sess.Messages, ChatMessage{
			OpenAI: openai.ChatCompletionMessage{
				Role:    openai.RoleAssistant,
				Content: assistantContent,
			},
			TelegramIDs: botMsgIDs,
		})
		sess.AddUsage(&resp.Usage)
		sess.LastActive = time.Now()
		sess.cancelLLM = nil
	}()

	return nil
}

func handleThink(ctx *telegram.Context) error {
	sess := getSession(ctx.ChatID)

	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.useReasoning = true

	return ctx.Reply("🧠 Deep thinking mode enabled. I will reason through your next prompts.")
}

func handleReset(ctx *telegram.Context) error {
	sess := getSession(ctx.ChatID)

	sess.mu.Lock()
	defer sess.mu.Unlock()

	// Abort any currently running LLM requests
	if sess.cancelLLM != nil {
		sess.cancelLLM()
		sess.cancelLLM = nil
	}

	sess.Messages = []ChatMessage{{OpenAI: systemPrompt(), TelegramIDs: nil}}
	sess.useReasoning = false

	return ctx.Reply("Conversation reset.")
}

func handleStats(ctx *telegram.Context) error {
	userID := ctx.Update.Message.From.ID

	if userID == adminID {
		return sendAdminStats(ctx)
	}

	sess := getSession(ctx.ChatID)

	sess.mu.Lock()
	stats := sess.Stats
	msgs := len(sess.Messages)
	sess.mu.Unlock()

	resp := fmt.Sprintf(
		"*Your Session Stats*\n\n"+
			"Messages in context: %d\n"+
			"Total Tokens: %d\n"+
			"Prompt Tokens: %d\n"+
			"Cached Tokens: %d\n"+
			"Completion Tokens: %d",
		msgs, stats.TotalTokens, stats.PromptTokens, stats.CachedTokens, stats.CompletionTokens,
	)

	_, err := ctx.Bot.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:    ctx.ChatID,
		Text:      resp,
		ParseMode: "Markdown",
	})
	return err
}

func sendAdminStats(ctx *telegram.Context) error {
	type userStat struct {
		ChatID int64
		Msgs   int
		Stats  UsageStats
	}

	sessionsMu.RLock()
	list := make([]userStat, 0, len(sessions))
	for id, s := range sessions {
		s.mu.Lock()
		list = append(list, userStat{
			ChatID: id,
			Msgs:   len(s.Messages),
			Stats:  s.Stats,
		})
		s.mu.Unlock()
	}
	sessionsMu.RUnlock()

	slices.SortFunc(list, func(a, b userStat) int {
		return cmp.Compare(b.Stats.TotalTokens, a.Stats.TotalTokens)
	})

	if len(list) == 0 {
		return ctx.Reply("No active sessions found.")
	}

	var sb strings.Builder
	sb.WriteString("*Admin Stats (Top Spenders)*\n\n")

	for i := 0; i < min(10, len(list)); i++ {
		p := list[i]
		sb.WriteString(fmt.Sprintf("%d) Chat: `%d`\n", i+1, p.ChatID))
		sb.WriteString(fmt.Sprintf("Tokens: %d (Cached: %d)\n", p.Stats.TotalTokens, p.Stats.CachedTokens))
		sb.WriteString(fmt.Sprintf("Messages: %d\n\n", p.Msgs))
	}

	_, err := ctx.Bot.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:    ctx.ChatID,
		Text:      sb.String(),
		ParseMode: "Markdown",
	})
	return err
}

func systemPrompt() openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{
		Role:    openai.RoleSystem,
		Content: `You are helpful AI assistant. Be concise and correct. You writing in Telegram chat.`,
	}
}

func parseWhitelist(env string) []int64 {
	var whitelist []int64
	for p := range strings.SplitSeq(env, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			whitelist = append(whitelist, id)
		}
	}
	return whitelist
}
