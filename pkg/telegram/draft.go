package telegram

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TODO: maybe add callback "on first token"?
type MessageDraft struct {
	ChatID      int64
	DraftID     int64
	CurrentText string
	mu          sync.Mutex
	bot         *Bot
	closed      bool
	lastUpdate  time.Time
}

type SendMessageDraftRequest struct {
	ChatID  int64  `json:"chat_id"`
	DraftID int64  `json:"draft_id"`
	Text    string `json:"text"`
}

func (b *Bot) SendMessageDraft(ctx context.Context, req SendMessageDraftRequest) (bool, error) {
	var result bool
	err := b.rawRequest(ctx, "sendMessageDraft", req, &result)
	return result, err
}

func (b *Bot) StartMessageStream(ctx context.Context, chatID int64, draftID int64) *MessageDraft {
	return &MessageDraft{
		ChatID:     chatID,
		DraftID:    draftID,
		bot:        b,
		lastUpdate: time.Now(),
	}
}

func (d *MessageDraft) Write(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return errors.New("draft already closed")
	}

	d.CurrentText += text

	// THROTTLE: Only update Telegram every 500ms.
	if time.Since(d.lastUpdate) < 500*time.Millisecond {
		return nil
	}

	display := d.CurrentText
	if len(display) > 4000 {
		display = "...\n" + display[len(display)-3990:]
	}

	req := SendMessageDraftRequest{
		ChatID:  d.ChatID,
		DraftID: d.DraftID,
		Text:    display,
		// No ParseMode here! We stream raw text so broken markdown doesn't crash it.
	}

	_, err := d.bot.SendMessageDraft(ctx, req)
	if err == nil {
		d.lastUpdate = time.Now()
	}
	return err
}

func (d *MessageDraft) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return nil
	}
	d.closed = true

	_, err := d.bot.SendChunked(ctx, d.ChatID, d.CurrentText)

	_, _ = d.bot.SendMessageDraft(ctx, SendMessageDraftRequest{
		ChatID:  d.ChatID,
		DraftID: d.DraftID,
		Text:    "", // Empty text deletes the draft
	})

	return err
}
