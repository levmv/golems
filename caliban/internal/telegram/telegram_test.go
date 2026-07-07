package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
)

// fakeIO captures the transport's outbound calls so tests can drive the event
// state machine without a real bot.
type fakeIO struct {
	sent  chan string
	typed chan struct{}
}

func newTestTransport() (*Transport, *fakeIO) {
	io := &fakeIO{sent: make(chan string, 8), typed: make(chan struct{}, 64)}
	t := &Transport{
		chatID:   1,
		convID:   defaultConversationID,
		ctx:      context.Background(),
		typing:   make(map[int64]chan struct{}),
		warnedID: make(map[int64]bool),
		emitText: func(_ context.Context, text string) error {
			io.sent <- text
			return nil
		},
		emitTyping: func(_ context.Context) error {
			select {
			case io.typed <- struct{}{}:
			default:
			}
			return nil
		},
	}
	return t, io
}

func done(runID int64, reply string) engine.Event {
	return engine.Event{ConversationID: defaultConversationID, RunID: runID,
		Ev: golem.StreamEvent{Kind: golem.EventDone, Text: reply}}
}

func delta(runID int64, text string) engine.Event {
	return engine.Event{ConversationID: defaultConversationID, RunID: runID,
		Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: text}}
}

func waitSend(t *testing.T, io *fakeIO) string {
	t.Helper()
	select {
	case s := <-io.sent:
		return s
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a send")
		return ""
	}
}

func expectNoSend(t *testing.T, io *fakeIO) {
	t.Helper()
	select {
	case s := <-io.sent:
		t.Fatalf("unexpected send: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}

// The reply sent is the one EventDone carries, not anything re-derived from the
// streamed deltas or tool-call preamble.
func TestDoneSendsAuthoritativeReply(t *testing.T) {
	tr, io := newTestTransport()
	tr.onEvent(delta(7, "thinking out loud"))
	tr.onEvent(engine.Event{ConversationID: defaultConversationID, RunID: 7,
		Ev: golem.StreamEvent{Kind: golem.EventToolCall}})
	tr.onEvent(delta(7, "more preamble"))
	tr.onEvent(done(7, "the final answer"))

	if got := waitSend(t, io); got != "the final answer" {
		t.Fatalf("sent %q, want the EventDone reply", got)
	}
	if _, ok := tr.typing[7]; ok {
		t.Fatal("typing state not cleaned up after done")
	}
}

// An empty final reply produces no message.
func TestEmptyReplyNotSent(t *testing.T) {
	tr, io := newTestTransport()
	tr.onEvent(delta(1, "preamble only"))
	tr.onEvent(done(1, "   "))
	expectNoSend(t, io)
}

// The typing indicator starts on the first event of a run and its pinger is
// stopped (state removed) when the run is done.
func TestTypingLifecycle(t *testing.T) {
	tr, io := newTestTransport()
	tr.onEvent(delta(3, "x"))
	select {
	case <-io.typed:
	case <-time.After(time.Second):
		t.Fatal("typing indicator never sent")
	}
	tr.mu.Lock()
	_, running := tr.typing[3]
	tr.mu.Unlock()
	if !running {
		t.Fatal("typing pinger not registered")
	}
	tr.onEvent(done(3, "done"))
	waitSend(t, io)
	tr.mu.Lock()
	_, stillRunning := tr.typing[3]
	tr.mu.Unlock()
	if stillRunning {
		t.Fatal("typing pinger not stopped after done")
	}
}

// Events for any conversation other than the main chat are ignored.
func TestNonMainConversationIgnored(t *testing.T) {
	tr, io := newTestTransport()
	tr.onEvent(engine.Event{ConversationID: defaultConversationID + 1, RunID: 9,
		Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: "hi"}})
	tr.onEvent(engine.Event{ConversationID: defaultConversationID + 1, RunID: 9,
		Ev: golem.StreamEvent{Kind: golem.EventDone, Text: "reply"}})
	expectNoSend(t, io)
	if len(tr.typing) != 0 {
		t.Fatal("non-main conversation started a typing pinger")
	}
}

func TestPersistedMessageEventIgnored(t *testing.T) {
	tr, io := newTestTransport()
	msg := store.Message{ID: 1, ConversationID: defaultConversationID, Role: store.RoleEvent, Source: "reminder"}
	tr.onEvent(engine.Event{ConversationID: defaultConversationID, Message: &msg})
	expectNoSend(t, io)
	if len(tr.typing) != 0 {
		t.Fatal("persisted message event started a typing pinger")
	}
}
