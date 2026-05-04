package main

import (
	"context"
	"sync"
	"time"
)

type LocalBotMessage struct {
	SourceBotID   string
	SourceBotName string
	Session       SessionKey
	Text          string
	SentAt        time.Time
}

type LocalMessageHandler func(context.Context, LocalBotMessage) error

type localMessageSubscriber struct {
	botID    string
	platform string
	handler  LocalMessageHandler
}

// LocalMessageBus lets bots running in the same process hear each other's
// outbound messages, bypassing Telegram's bot-to-bot update restriction.
type LocalMessageBus struct {
	mu          sync.RWMutex
	subscribers map[string]localMessageSubscriber
}

func NewLocalMessageBus() *LocalMessageBus {
	return &LocalMessageBus{
		subscribers: make(map[string]localMessageSubscriber),
	}
}

func (b *LocalMessageBus) Subscribe(platform, botID string, handler LocalMessageHandler) func() {
	if b == nil || platform == "" || botID == "" || handler == nil {
		return func() {}
	}

	key := platform + "\x00" + botID

	b.mu.Lock()
	b.subscribers[key] = localMessageSubscriber{
		botID:    botID,
		platform: platform,
		handler:  handler,
	}
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.subscribers, key)
		b.mu.Unlock()
	}
}

func (b *LocalMessageBus) Publish(ctx context.Context, msg LocalBotMessage) {
	if b == nil || msg.Text == "" {
		return
	}
	if msg.SentAt.IsZero() {
		msg.SentAt = time.Now()
	}

	b.mu.RLock()
	recipients := make([]localMessageSubscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if sub.platform != msg.Session.Platform || sub.botID == msg.SourceBotID {
			continue
		}
		recipients = append(recipients, sub)
	}
	b.mu.RUnlock()

	for _, sub := range recipients {
		if err := sub.handler(ctx, msg); err != nil {
			Log.Error("Local bus delivery failed from %s to %s in %s: %v", msg.SourceBotID, sub.botID, msg.Session, err)
		}
	}
}
