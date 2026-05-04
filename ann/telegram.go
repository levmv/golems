package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/telegram"
)

var localTelegramBus = NewLocalMessageBus()

type TelegramGateway struct {
	engine           *Engine
	tgBot            *telegram.Bot
	botID            string
	botName          string
	unsubscribeLocal func()
}

// StartTelegramBot initializes the bot, returning an http.Handler for webhooks
// or starting a background polling loop if no URL is provided.
func StartTelegramBot(ctx context.Context, tgCfg *TelegramConfig, eng *Engine, webhookURL, botID, botName string) (*TelegramGateway, http.Handler, error) {
	secret := generateSecret()

	authMiddleware := func(c *telegram.Context) (bool, string) {
		if c.Update.Message == nil || c.SenderID == 0 || c.ChatID == 0 {
			return false, "no user info"
		}

		userIDStr := strconv.FormatInt(c.SenderID, 10)
		chatIDStr := strconv.FormatInt(c.ChatID, 10)

		if slices.Contains(tgCfg.AllowedUsers, userIDStr) || slices.Contains(tgCfg.AllowedUsers, chatIDStr) {
			return true, "whitelisted"
		}
		if strings.HasPrefix(c.Update.Message.Text, "/start") {
			_ = c.Reply(fmt.Sprintf("Your ID is %s. Ask the admin to whitelist it.", userIDStr))
		}
		return false, "not whitelisted"
	}

	tgBot, err := telegram.New(
		tgCfg.Token,
		secret,
		telegram.WithLogger(Log),
		telegram.WithAuthFunc(authMiddleware),
	)
	if err != nil {
		return nil, nil, err
	}

	gw := &TelegramGateway{
		engine:  eng,
		tgBot:   tgBot,
		botID:   botID,
		botName: botName,
	}
	gw.unsubscribeLocal = localTelegramBus.Subscribe("tg", botID, gw.handleLocalBotMessage)
	gw.tgBot.OnText(gw.handleMessage)

	if webhookURL != "" {
		Log.Info("Setting Webhook to: %s", webhookURL)
		_, err = gw.tgBot.SetWebhook(ctx, &telegram.SetWebhookParams{
			URL:         webhookURL,
			SecretToken: secret,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to set webhook: %w", err)
		}
		go gw.tgBot.Start(ctx)
		return gw, gw.tgBot.WebhookHandler(), nil
	}

	Log.Info("Starting Polling (No Webhook URL provided)")
	go gw.tgBot.StartPolling(ctx)

	return gw, nil, nil
}

func (g *TelegramGateway) Send(ctx context.Context, sKey SessionKey, text string) error {
	return g.send(ctx, sKey, text, true)
}

func (g *TelegramGateway) SendWithoutBroadcast(ctx context.Context, sKey SessionKey, text string) error {
	return g.send(ctx, sKey, text, false)
}

func (g *TelegramGateway) send(ctx context.Context, sKey SessionKey, text string, broadcast bool) error {
	chatID, err := strconv.ParseInt(sKey.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid sessionID format: %w", err)
	}
	if _, err = g.tgBot.SendChunked(ctx, chatID, text); err != nil {
		return err
	}
	if broadcast {
		g.publishLocalMessage(ctx, sKey, text)
	}
	return nil
}

func (g *TelegramGateway) StartTyping(ctx context.Context, sKey SessionKey) func() {
	chatID, err := strconv.ParseInt(sKey.ChatID, 10, 64)
	if err != nil {
		return func() {}
	}

	typing := g.tgBot.StartTypingIndicator(ctx, chatID)
	return func() { typing.Stop() }
}

func (g *TelegramGateway) handleMessage(ctx *telegram.Context) error {
	msg := ctx.Update.Message
	if msg == nil || msg.Text == "" {
		return nil
	}

	if msg.Chat.Type == "private" {
		return g.handlePrivateMessage(ctx)
	}

	return g.handleGroupMessage(ctx)
}

func (g *TelegramGateway) handlePrivateMessage(ctx *telegram.Context) error {
	sKey := SessionKey{
		Platform: "tg",
		Type:     SessionTypePrivate,
		ChatID:   strconv.FormatInt(ctx.ChatID, 10),
	}

	return g.executeBotReply(ctx, sKey, extractUserMessage(ctx.Update.Message))
}

func (g *TelegramGateway) handleGroupMessage(ctx *telegram.Context) error {
	msg := ctx.Update.Message
	chatIDStr := strconv.FormatInt(ctx.ChatID, 10)

	sKey := SessionKey{
		Platform: "tg",
		Type:     SessionTypeGroup,
		ChatID:   chatIDStr,
	}
	userMsg := extractUserMessage(msg)

	botUsername := g.tgBot.Me.Username
	botID := g.tgBot.Me.ID

	isMention := botUsername != "" && strings.Contains(msg.Text, "@"+botUsername)

	isReplyToBot := msg.ReplyToMessage != nil &&
		msg.ReplyToMessage.From != nil &&
		msg.ReplyToMessage.From.ID == botID

	shouldReply := isMention || isReplyToBot

	if !shouldReply {
		return g.engine.ObserveMessage(context.Background(), sKey, userMsg)
	}

	go func() {
		if err := g.executeBotReply(ctx, sKey, userMsg); err != nil {
			Log.Error("Bot reply failed for %s: %v", sKey, err)
		}
	}()

	return nil
}

func (g *TelegramGateway) executeBotReply(ctx context.Context, sKey SessionKey, userMsg Message) error {
	stopTyping := g.StartTyping(ctx, sKey)
	defer stopTyping()

	// Engine handles debounce. Use WithoutCancel so HTTP timeouts don't kill the LLM generation.
	text, err := g.engine.ProcessMessage(context.WithoutCancel(ctx), sKey, userMsg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			Log.Info("Session %s generation canceled by a new message", sKey)
			return nil
		}
		Log.Error("Engine error for %s: %v", sKey, err)
		return err
	}

	if text != "" {
		return g.Send(context.WithoutCancel(ctx), sKey, text)
	}
	return nil
}

func (g *TelegramGateway) publishLocalMessage(ctx context.Context, sKey SessionKey, text string) {
	localTelegramBus.Publish(ctx, LocalBotMessage{
		SourceBotID:   g.botID,
		SourceBotName: g.botName,
		Session:       sKey,
		Text:          text,
		SentAt:        time.Now(),
	})
}

func (g *TelegramGateway) handleLocalBotMessage(ctx context.Context, event LocalBotMessage) error {
	msg := Message{
		Role:      llm.RoleUser,
		Content:   event.Text,
		Name:      event.SourceBotName,
		Timestamp: event.SentAt,
	}
	if msg.Name == "" {
		msg.Name = event.SourceBotID
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	if g.shouldReplyToLocalMessage(event) {
		go func() {
			if err := g.executeBotReply(context.WithoutCancel(ctx), event.Session, msg); err != nil {
				Log.Error("Local bot reply failed for %s after message from %s: %v", event.Session, event.SourceBotID, err)
			}
		}()
		return nil
	}

	return g.engine.ObserveMessage(ctx, event.Session, msg)
}

func (g *TelegramGateway) shouldReplyToLocalMessage(event LocalBotMessage) bool {
	if event.Session.Type != SessionTypeGroup {
		return false
	}
	botUsername := g.tgBot.Me.Username
	return botUsername != "" && strings.Contains(event.Text, "@"+botUsername)
}

func extractUserMessage(msg *telegram.Message) Message {
	return Message{
		Role:      llm.RoleUser,
		Content:   msg.Text,
		Name:      extractSenderName(msg.From),
		Timestamp: time.Unix(int64(msg.Date), 0),
		ReplyTo:   extractReplyInfo(msg.ReplyToMessage),
	}
}

// extractSenderName returns the best available name from a Telegram User.
func extractSenderName(user *telegram.User) string {
	if user == nil {
		return "unknown"
	}
	name := user.FirstName
	if name == "" {
		name = user.Username
	}
	if name == "" {
		return "unknown"
	}
	return name
}

// extractReplyInfo safely builds a ReplyInfo struct if the message is a reply.
func extractReplyInfo(replyMsg *telegram.Message) *ReplyInfo {
	if replyMsg == nil {
		return nil
	}

	replyText := replyMsg.Text
	if replyText == "" {
		replyText = replyMsg.Caption
	}

	return &ReplyInfo{
		Name: extractSenderName(replyMsg.From),
		Text: replyText,
	}
}

func generateSecret() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
