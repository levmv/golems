// Package telegram is the Telegram transport: a long-poll loop on pkg/telegram
// restricted to one allow-listed chat. It translates incoming messages into
// engine runs, renders run stream events back into Telegram messages, and
// delivers out-of-band pushes. The transport talks only to engine.
package telegram

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/telegram"
)

const (
	defaultConversationID = 1
	typingInterval        = 5 * time.Second
)

// Config wires the transport.
type Config struct {
	Token          string
	ChatID         int64 // single allow-listed chat
	ConversationID int64 // default 1
	Engine         *engine.Engine
	Logger         telegram.Logger
}

// Transport bridges a Telegram bot and the engine.
type Transport struct {
	bot    *telegram.Bot
	chatID int64
	convID int64
	engine *engine.Engine
	log    telegram.Logger

	ctx context.Context // set in Run; used by event/notify sends

	// I/O seams: the real bot in New, fakeable in tests. emitText sends the
	// LLM's markdown through Telegram's rich-message API; emitTyping shows the
	// typing indicator once.
	emitText   func(ctx context.Context, text string) error
	emitTyping func(ctx context.Context) error

	mu          sync.Mutex
	typing      map[int64]chan struct{} // run id -> stop channel for its typing pinger
	warnedID    map[int64]bool          // offender chat ids already logged
	unsubscribe func()
}

func New(cfg Config) (*Transport, error) {
	convID := cfg.ConversationID
	if convID == 0 {
		convID = defaultConversationID
	}
	t := &Transport{
		chatID:   cfg.ChatID,
		convID:   convID,
		engine:   cfg.Engine,
		log:      cfg.Logger,
		typing:   make(map[int64]chan struct{}),
		warnedID: make(map[int64]bool),
	}

	bot, err := telegram.New(cfg.Token, "",
		telegram.WithLogger(cfg.Logger),
		telegram.WithAuthFunc(t.authorize),
	)
	if err != nil {
		return nil, err
	}
	t.bot = bot
	t.emitText = func(ctx context.Context, text string) error {
		_, err := bot.SendRichMarkdown(ctx, t.chatID, text)
		return err
	}
	t.emitTyping = func(ctx context.Context) error {
		_, err := bot.SendChatAction(ctx, t.chatID, telegram.ActionTyping)
		return err
	}
	bot.OnAny(t.onUpdate)
	return t, nil
}

// Run subscribes to engine events and runs the long-poll loop until ctx is done.
func (t *Transport) Run(ctx context.Context) error {
	t.ctx = ctx
	t.unsubscribe = t.engine.Subscribe(t.onEvent)
	defer t.unsubscribe()
	t.bot.StartPolling(ctx)
	return nil
}

// authorize restricts the bot to the configured chat, logging each offender once.
func (t *Transport) authorize(c *telegram.Context) (bool, string) {
	if c.ChatID == t.chatID {
		return true, ""
	}
	t.mu.Lock()
	if !t.warnedID[c.ChatID] {
		t.warnedID[c.ChatID] = true
		if t.log != nil {
			t.log.Warn("ignoring update from unauthorized chat %d", c.ChatID)
		}
	}
	t.mu.Unlock()
	return false, ""
}

// onUpdate submits text messages to the engine; anything else gets a polite nudge.
func (t *Transport) onUpdate(c *telegram.Context) error {
	text := strings.TrimSpace(c.Text())
	if text == "" {
		_, err := c.ReplyRich("I can only handle text messages for now.")
		return err
	}
	return t.engine.SubmitUserMessage(c, t.convID, text, "telegram")
}

// onEvent runs in the engine's goroutine and must not block. It drives the
// typing indicator for the duration of a run and, on completion, sends the run's
// final reply — which EventDone carries authoritatively, so the transport does
// not re-derive it from streamed deltas.
func (t *Transport) onEvent(ev engine.Event) {
	if ev.ConversationID != t.convID {
		return
	}
	if ev.Message != nil {
		return
	}
	if ev.Ev.Kind == golem.EventDone {
		t.finishRun(ev.RunID, ev.Ev.Text)
		return
	}
	// Any pre-done event means the run is working: keep the typing indicator up.
	t.ensureTyping(ev.RunID)
}

// ensureTyping starts the typing pinger for a run on the first event seen for it.
func (t *Transport) ensureTyping(runID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.typing[runID]; ok {
		return
	}
	stop := make(chan struct{})
	t.typing[runID] = stop
	go t.pingTyping(stop)
}

func (t *Transport) finishRun(runID int64, reply string) {
	t.mu.Lock()
	stop := t.typing[runID]
	delete(t.typing, runID)
	t.mu.Unlock()
	if stop != nil {
		close(stop)
	}

	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}
	// Send off the engine goroutine; network IO must not block delivery.
	go t.send(reply)
}

// pingTyping shows the typing indicator until the run finishes (it expires after
// a few seconds, so it must be refreshed).
func (t *Transport) pingTyping(stop <-chan struct{}) {
	_ = t.emitTyping(t.ctx)
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.ctx.Done():
			return
		case <-ticker.C:
			_ = t.emitTyping(t.ctx)
		}
	}
}

// Notify pushes text to the chat outside the reply flow (engine.Notifier).
func (t *Transport) Notify(ctx context.Context, conversationID int64, text string) error {
	if conversationID != t.convID {
		return nil
	}
	return t.emitText(ctx, text)
}

func (t *Transport) send(text string) {
	if err := t.emitText(t.ctx, text); err != nil && t.log != nil {
		t.log.Error("send reply: %v", err)
	}
}
