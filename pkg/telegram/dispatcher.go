package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf16"
)

// Start starts the main update loop.
func (b *Bot) Start(ctx context.Context) {
	if b.webhookSecretToken == "" {
		b.logger.Error("webhook secret token not configured; set webhookSecretToken")
		return
	}
	b.logger.Info("Bot started in webhook mode")
	// Block and run the dispatcher
	b.waitUpdates(ctx)
}

// StartPolling starts the bot in long-polling mode (only for local dev).
func (b *Bot) StartPolling(ctx context.Context) {
	// Delete the webhook first, otherwise Telegram blocks getUpdates
	_, err := b.DeleteWebhook(ctx, &DeleteWebhookParams{DropPendingUpdates: false})
	if err != nil {
		b.logger.Error("failed to delete webhook before polling: %v", err)
	}

	go b.waitUpdates(ctx)

	b.logger.Info("Bot started in long-polling mode")

	var offset int64 = 0
	for {
		select {
		case <-ctx.Done():
			if offset > 0 {
				b.logger.Info("Acknowledging final updates before shutdown (offset: %d)", offset)
				shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				var ignore []Update
				_ = b.rawRequest(shutdownCtx, "getUpdates", &GetUpdatesParams{
					Offset:  offset,
					Limit:   1,
					Timeout: 0,
				}, &ignore)
			}
			return
		default:
		}

		var updates []Update
		err = b.rawRequest(ctx, "getUpdates", &GetUpdatesParams{
			Offset:  offset,
			Timeout: 30, // Wait up to 30 seconds for a new message
		}, &updates)
		if err != nil {
			b.logger.Error("getUpdates failed: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second * 3): // Sleep briefly on error to avoid spamming the API
			}
			continue
		}

		for _, upd := range updates {
			// Update the offset to confirm we received this message
			if upd.ID >= offset {
				offset = upd.ID + 1
			}

			select {
			case b.updates <- &upd:
			case <-ctx.Done():
				return
			}
		}
	}
}

// WebhookHandler returns HTTP handler for webhook.
func (b *Bot) WebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		headerToken := req.Header.Get("X-Telegram-Bot-Api-Secret-Token")

		if subtle.ConstantTimeCompare([]byte(b.webhookSecretToken), []byte(headerToken)) != 1 {
			b.logger.Error("invalid webhook secret token")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Limit payload to 10MB to prevent memory exhaustion
		body, err := io.ReadAll(io.LimitReader(req.Body, 10<<20))
		if err != nil {
			b.logger.Error("failed to read request body: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		update := &Update{}
		if err = json.Unmarshal(body, update); err != nil {
			b.logger.Error("failed to decode update: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		if b.isDebug {
			b.logger.Debug("webhook request: %s", string(body))
		}

		// Non-blocking send. Webhook always returns 200 OK fast.
		select {
		case b.updates <- update:
		default:
			b.logger.Error("global update channel full, dropping update")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
	}
}

func (b *Bot) waitUpdates(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case upd := <-b.updates:
			b.dispatchUpdate(ctx, upd)
		}
	}
}

// dispatchUpdate routes the update to a specific chat's goroutine.
func (b *Bot) dispatchUpdate(ctx context.Context, upd *Update) {
	userID, chatID := extractIDs(upd)

	if chatID == 0 {
		b.logger.Debug("ignored update with no Chat ID: %+v", upd)
		return
	}
	cmd := extractCommand(upd)

	c := &Context{
		Context:  ctx,
		Bot:      b,
		Update:   upd,
		ChatID:   chatID,
		SenderID: userID,
		Command:  cmd,
	}

	if b.authFunc != nil {
		allow, reason := b.authFunc(c)
		if !allow {
			if b.isDebug {
				b.logger.Warn("auth denied: %s (user=%d chat=%d cmd=%q)", reason, c.SenderID, c.ChatID, c.Command)
			}
			return
		}
	}

	b.chatQueuesMx.Lock()
	defer b.chatQueuesMx.Unlock()

	ch, exists := b.chatQueues[chatID]
	if !exists {
		// Create a buffered channel for this specific chat
		ch = make(chan *Context, 100)
		b.chatQueues[chatID] = ch

		// Start a dedicated worker for this chat
		go b.chatWorker(ctx, chatID, ch)
	}

	// Send to chat's queue. If user is spamming faster than LLM replies,
	// and buffer fills up, drop the message.
	select {
	case ch <- c:
	default:
		b.logger.Error("queue for chat %d is full, dropping update", chatID)
	}
}

// chatWorker processes updates sequentially for ONE specific chat.
func (b *Bot) chatWorker(ctx context.Context, chatID int64, ch <-chan *Context) {
	if b.isDebug {
		b.logger.Debug("started worker for chat %d", chatID)
	}

	// Idle timer: shut down worker if no messages for 10 minutes
	idleDuration := 10 * time.Minute
	idleTimer := time.NewTimer(idleDuration)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case c := <-ch:
			b.safeProcessUpdate(c)
			idleTimer.Reset(idleDuration)

		case <-idleTimer.C:
			// Inactivity timeout reached. Safely clean up.
			b.chatQueuesMx.Lock()

			if len(ch) > 0 {
				b.chatQueuesMx.Unlock()
				idleTimer.Reset(idleDuration)
				continue
			}

			delete(b.chatQueues, chatID)
			b.chatQueuesMx.Unlock()

			if b.isDebug {
				b.logger.Debug("worker for chat %d stopped due to inactivity", chatID)
			}
			return
		}
	}
}

// safeProcessUpdate wraps processUpdate with a panic recovery.
func (b *Bot) safeProcessUpdate(c *Context) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			b.logger.Error("PANIC recovered in handler: %v\nStack trace:\n%s", r, stack)
		}
	}()

	h := b.findHandler(c)
	if err := h(c); err != nil {
		b.logger.Error("Handler error for chat %d: %v", c.ChatID, err)
	}
}

func extractIDs(upd *Update) (userID, chatID int64) {
	switch {
	case upd.Message != nil:
		if upd.Message.From != nil {
			userID = upd.Message.From.ID
		}
		chatID = upd.Message.Chat.ID
	case upd.EditedMessage != nil:
		if upd.EditedMessage.From != nil {
			userID = upd.EditedMessage.From.ID
		}
		chatID = upd.EditedMessage.Chat.ID
	case upd.CallbackQuery != nil:
		userID = upd.CallbackQuery.From.ID
		if upd.CallbackQuery.Message != nil {
			chatID = upd.CallbackQuery.Message.Chat.ID
		}
	}
	return userID, chatID
}

// extractCommand parses a bot command from a message text or caption.
// Returns the command without the leading slash or bot username (e.g., "start").
func extractCommand(upd *Update) string {
	if upd == nil || upd.Message == nil {
		return ""
	}

	text := upd.Message.Text
	entities := upd.Message.Entities

	// Fallback to caption if it's a media message
	if text == "" {
		text = upd.Message.Caption
		entities = upd.Message.CaptionEntities
	}

	if text == "" {
		return ""
	}

	for _, e := range entities {
		// A command must start at the very beginning of the message (offset == 0)
		if e.Type == "bot_command" && e.Offset == 0 {
			cmd := toUTF16Len(text, e.Offset+1, e.Length-1)

			// Strip the bot username if present (e.g., "/start@MyCoolBot" -> "start")
			if idx := strings.Index(cmd, "@"); idx != -1 {
				cmd = cmd[:idx]
			}
			return cmd
		}
	}

	return ""
}

// toUTF16Len extracts a substring using UTF-16 code units (as Telegram uses).
func toUTF16Len(s string, offset, length int) string {
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
