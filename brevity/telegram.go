package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	sourcefetch "github.com/levmv/golems/brevity/internal/fetch"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/telegram"
)

var rawURLRe = regexp.MustCompile(`https?://[^\s<>"']+`)

type TelegramAdapter struct {
	bot           *telegram.Bot
	service       *Service
	whitelist     []int64
	webhookSecret string
	log           logger.Logger
}

func NewTelegramAdapter(cfg Config, service *Service, log logger.Logger) (*TelegramAdapter, error) {
	secret := cfg.WebhookSecretToken
	if secret == "" {
		secret = generateSecret()
	}

	adapter := &TelegramAdapter{
		service:       service,
		whitelist:     cfg.TelegramWhitelist,
		webhookSecret: secret,
		log:           log,
	}

	bot, err := telegram.New(
		cfg.TelegramBotToken,
		secret,
		telegram.WithLogger(log),
		telegram.WithAuthFunc(adapter.auth),
	)
	if err != nil {
		return nil, err
	}
	adapter.bot = bot
	adapter.registerHandlers()
	return adapter, nil
}

func (a *TelegramAdapter) Start(ctx context.Context, cfg Config) error {
	a.setCommands(ctx)

	if cfg.WebhookURL == "" {
		a.log.Info("Starting Brevity in polling mode")
		a.bot.StartPolling(ctx)
		return nil
	}

	_, err := a.bot.SetWebhook(ctx, &telegram.SetWebhookParams{
		URL:         cfg.WebhookURL,
		SecretToken: a.webhookSecret,
	})
	if err != nil {
		return fmt.Errorf("set webhook: %w", err)
	}

	listenAddr := ":" + cfg.Port
	server := &http.Server{Addr: listenAddr, Handler: a.bot.WebhookHandler()}
	go func() {
		a.log.Info("Starting Brevity webhook server on %s", listenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.log.Error("webhook HTTP server error: %v", err)
		}
	}()

	a.bot.Start(ctx)
	return server.Shutdown(context.Background())
}

func (a *TelegramAdapter) setCommands(ctx context.Context) {
	if _, err := a.bot.SetMyCommands(ctx, telegram.SetMyCommandsRequest{Commands: brevityCommands()}); err != nil {
		a.log.Warn("failed to set Telegram commands: %v", err)
	}
}

func brevityCommands() []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "start", Description: "Как пользоваться ботом"},
		{Command: "help", Description: "Помощь"},
		{Command: "id", Description: "Показать ID чата"},
		{Command: "s", Description: "Сделать саммари ссылки"},
		{Command: "summary", Description: "Сделать саммари ссылки"},
	}
}

func (a *TelegramAdapter) registerHandlers() {
	a.bot.OnCommand("start", a.handleHelp)
	a.bot.OnCommand("help", a.handleHelp)
	a.bot.OnCommand("id", a.handleID)
	a.bot.OnCommand("s", a.handleText)
	a.bot.OnCommand("summary", a.handleText)
	a.bot.OnText(a.handleText)
}

func (a *TelegramAdapter) auth(c *telegram.Context) (bool, string) {
	if slices.Contains(a.whitelist, c.ChatID) {
		return true, "chat whitelisted"
	}
	if c.SenderID != 0 && slices.Contains(a.whitelist, c.SenderID) {
		return true, "sender whitelisted"
	}
	if c.SenderID == 0 {
		return false, "no sender"
	}
	if c.Command == "start" || c.Command == "id" {
		_ = c.Reply(idMessage(c) + "\nДобавь нужный ID в TELEGRAM_WHITELIST.")
	}
	return false, "not whitelisted"
}

func (a *TelegramAdapter) handleHelp(c *telegram.Context) error {
	return c.Reply("Пришли ссылку на статью или страницу, а я верну короткое саммари и ссылку на подробную версию.")
}

func (a *TelegramAdapter) handleID(c *telegram.Context) error {
	return c.Reply(idMessage(c))
}

func idMessage(c *telegram.Context) string {
	chatType := ""
	if c.Update.Message != nil {
		chatType = c.Update.Message.Chat.Type
	}
	if chatType == "" {
		chatType = "unknown"
	}
	return fmt.Sprintf("Chat ID: %d\nChat type: %s\nYour user ID: %d", c.ChatID, chatType, c.SenderID)
}

func (a *TelegramAdapter) handleText(c *telegram.Context) error {
	link := ExtractFirstURL(c.Update.Message)
	if link == "" {
		if c.Command == "" && c.Update.Message != nil && c.Update.Message.Chat.Type != "private" {
			return nil
		}
		return c.Reply("Не вижу ссылку. Пришли URL вида https://example.com/article.")
	}

	statusMsg, _ := c.Bot.SendMessage(c, &telegram.SendMessageParams{
		ChatID: c.ChatID,
		Text:   "Принял. Читаю...",
	})
	deleteStatus := func(ctx context.Context) {
		if statusMsg == nil || statusMsg.ID == 0 {
			return
		}
		if _, err := c.Bot.DeleteMessages(ctx, c.ChatID, []int{int(statusMsg.ID)}); err != nil {
			a.log.Warn("failed to delete status message chat=%d message=%d: %v", c.ChatID, statusMsg.ID, err)
		}
	}

	typing := c.Bot.StartTypingIndicator(c, c.ChatID)
	defer typing.Stop()

	replyCtx := context.WithoutCancel(c)
	result, err := a.service.SummarizeURL(replyCtx, link)
	deleteStatus(replyCtx)
	if err != nil {
		if human, ok := sourcefetch.AsNeedsHuman(err); ok {
			a.log.Info("manual browser action required for chat=%d url=%s: %v", c.ChatID, link, err)
			return a.sendNeedsHuman(replyCtx, c.ChatID, human)
		}
		a.log.Error("brevity failed for chat=%d url=%s: %v", c.ChatID, link, err)
		return c.Reply("Не получилось сделать саммари: " + err.Error())
	}
	if result.PublishErr != nil {
		a.log.Warn("telegraph publish failed for url=%s: %v", link, result.PublishErr)
	}
	return a.sendResult(replyCtx, c.ChatID, result)
}

func (a *TelegramAdapter) sendNeedsHuman(ctx context.Context, chatID int64, human *sourcefetch.NeedsHumanError) error {
	reason := strings.TrimSpace(human.Reason)
	if reason == "" {
		reason = "страница просит ручное действие в браузере"
	}

	text := "Нужно ручное действие: " + reason + ".\n\nОткрой браузерную сессию, пройди проверку, потом пришли эту ссылку еще раз."
	markup := telegram.NewInlineKeyboardMarkup()
	if strings.TrimSpace(human.BrowserURL) != "" {
		markup.AddRow(telegram.NewInlineKeyboardButton("Открыть браузер").WithURL(human.BrowserURL))
	}

	params := &telegram.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}
	if len(markup.InlineKeyboard) > 0 {
		params.ReplyMarkup = markup
	}
	_, err := a.bot.SendMessage(ctx, params)
	return err
}

func (a *TelegramAdapter) sendResult(ctx context.Context, chatID int64, result *Result) error {
	text := formatShortTelegramPost(result)
	markup := telegram.NewInlineKeyboardMarkup()
	var buttons []telegram.InlineKeyboardButton
	if result.PublishedURL != "" {
		buttons = append(buttons, telegram.NewInlineKeyboardButton("Подробнее").WithURL(result.PublishedURL))
	}
	if result.Source.FinalURL != "" {
		buttons = append(buttons, telegram.NewInlineKeyboardButton("Источник").WithURL(result.Source.FinalURL))
	}
	if len(buttons) > 0 {
		markup.AddRow(buttons...)
	}

	params := &telegram.SendMessageParams{
		ChatID:                chatID,
		Text:                  telegram.LLMMarkdownToHTML(text),
		ParseMode:             telegram.ParseModeHTML,
		DisableWebPagePreview: result.PublishedURL == "",
	}
	if len(markup.InlineKeyboard) > 0 {
		params.ReplyMarkup = markup
	}

	if _, err := a.bot.SendMessage(ctx, params); err != nil {
		return err
	}

	if result.PublishedURL == "" {
		if result.PublishErr != nil {
			_, _ = a.bot.SendMessage(ctx, &telegram.SendMessageParams{
				ChatID: chatID,
				Text:   "Полную версию не удалось опубликовать на Telegraph, отправляю ее сообщением ниже.",
			})
		}
		_, err := a.bot.SendChunked(ctx, chatID, result.Summary.FullSummary)
		return err
	}
	return nil
}

func formatShortTelegramPost(result *Result) string {
	var sb strings.Builder
	sb.WriteString("**")
	sb.WriteString(result.Summary.Title)
	sb.WriteString("**\n\n")
	sb.WriteString(strings.TrimSpace(result.Summary.ShortSummary))
	if result.PublishedURL != "" {
		sb.WriteString("\n\n[Подробное саммари](")
		sb.WriteString(result.PublishedURL)
		sb.WriteString(")")
	}
	return sb.String()
}

func ExtractFirstURL(msg *telegram.Message) string {
	if msg == nil {
		return ""
	}

	for _, entity := range msg.Entities {
		if entity.Type == "text_link" && entity.URL != "" {
			if normalized := normalizeCandidateURL(entity.URL); normalized != "" {
				return normalized
			}
		}
	}

	for _, entity := range msg.Entities {
		if entity.Type == "url" {
			if normalized := normalizeCandidateURL(entitySubstring(msg.Text, entity.Offset, entity.Length)); normalized != "" {
				return normalized
			}
		}
	}

	if match := rawURLRe.FindString(msg.Text); match != "" {
		return normalizeCandidateURL(match)
	}
	return ""
}

func normalizeCandidateURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,;:!?)]}\"'")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return ""
	}
	return parsed.String()
}

func entitySubstring(s string, offset, length int) string {
	if offset < 0 || length <= 0 {
		return ""
	}
	encoded := utf16.Encode([]rune(s))
	if offset >= len(encoded) {
		return ""
	}
	end := min(offset+length, len(encoded))
	return string(utf16.Decode(encoded[offset:end]))
}

func generateSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(int64(len(b)), 36)
	}
	return hex.EncodeToString(b[:])
}
