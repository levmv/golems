package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

// scriptModel is a golem.Model that returns one scripted text reply per Stream
// call and records the requests it received.
type scriptModel struct {
	mu        sync.Mutex
	replies   []string
	calls     int
	streamErr error
	requests  []llm.Request
}

func (m *scriptModel) Chat(_ context.Context, req llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	reply := "ok"
	if m.calls < len(m.replies) {
		reply = m.replies[m.calls]
	}
	m.calls++
	return &llm.Response{
		Content:      reply,
		FinishReason: llm.FinishReasonStop,
		Usage:        llm.Usage{PromptTokens: 10, CompletionTokens: 3},
	}, nil
}

func (m *scriptModel) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	reply := "ok"
	if m.calls < len(m.replies) {
		reply = m.replies[m.calls]
	}
	m.calls++
	return &scriptStream{chunks: []llm.StreamChunk{
		{Text: reply, FinishReason: llm.FinishReasonStop},
	}, usage: llm.Usage{PromptTokens: 10, CompletionTokens: 3}}, nil
}

// requestCount counts Stream attempts, including ones that returned an error
// (m.calls only advances on the success path).
func (m *scriptModel) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *scriptModel) request(n int) (llm.Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 0 || n >= len(m.requests) {
		return llm.Request{}, false
	}
	return m.requests[n], true
}

func (m *scriptModel) lastRequest() (llm.Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return llm.Request{}, false
	}
	return m.requests[len(m.requests)-1], true
}

type scriptStream struct {
	chunks []llm.StreamChunk
	usage  llm.Usage
	idx    int
}

func (s *scriptStream) Recv() (llm.StreamChunk, error) {
	if s.idx >= len(s.chunks) {
		return llm.StreamChunk{}, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}
func (s *scriptStream) Usage() llm.Usage { return s.usage }
func (s *scriptStream) Close() error     { return nil }

var errModel = errors.New("model exploded")

// blockingModel gates each Stream call: it announces the call's index on started
// and blocks until the test sends on release, letting a test hold a run open
// while it appends another user message.
type blockingModel struct {
	mu       sync.Mutex
	calls    int
	replies  []string
	requests []llm.Request
	started  chan int
	release  chan struct{}
}

func (m *blockingModel) Chat(context.Context, llm.Request) (*llm.Response, error) {
	return nil, errors.New("blockingModel: Chat not used")
}

func (m *blockingModel) request(n int) (llm.Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 0 || n >= len(m.requests) {
		return llm.Request{}, false
	}
	return m.requests[n], true
}

func (m *blockingModel) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	m.mu.Lock()
	n := m.calls
	m.calls++
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	m.started <- n
	<-m.release
	reply := "ok"
	if n < len(m.replies) {
		reply = m.replies[n]
	}
	return &scriptStream{chunks: []llm.StreamChunk{
		{Text: reply, FinishReason: llm.FinishReasonStop},
	}, usage: llm.Usage{PromptTokens: 10, CompletionTokens: 3}}, nil
}

// runEngine builds an engine over fresh temp store/workspace and the given model
// and runs it under a cancelable context.
func runEngine(t *testing.T, model golem.Model) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	eng, err := New(Config{
		Store:       st,
		Workspace:   ws,
		Main:        model,
		MainModelID: "fake/model",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Start(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done // wait for in-flight runs (incl. git commit) before TempDir cleanup
		st.Close()
	})
	return eng, st
}

func waitForLastRole(t *testing.T, st *store.Store, role llm.Role) store.Message {
	return waitForLastRoleInConversation(t, st, 1, role)
}

func waitForLastRoleInConversation(t *testing.T, st *store.Store, conversationID int64, role llm.Role) store.Message {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m, ok, err := st.LastMessage(context.Background(), conversationID)
		if err != nil {
			t.Fatalf("LastMessage: %v", err)
		}
		if ok && m.Role == role {
			return m
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for last message role %s", role)
	return store.Message{}
}

func TestStartReadyAllowsConfiguredActiveConversationSubmissions(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	model := &scriptModel{replies: []string{"web reply"}}
	eng, err := New(Config{
		Store:       st,
		Workspace:   ws,
		Main:        model,
		MainModelID: "fake/model",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("EnsureConversation(2): %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- eng.StartReady(runCtx, ready) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("StartReady: %v", err)
		}
	})

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("engine did not become ready")
	}
	if err := eng.SubmitUserMessage(ctx, 2, "hello from web", "web"); err != nil {
		t.Fatalf("SubmitUserMessage after ready: %v", err)
	}
	reply := waitForLastRoleInConversation(t, st, 2, llm.RoleAI)
	if reply.Content.Text != "web reply" {
		t.Fatalf("unexpected web reply: %q", reply.Content.Text)
	}
}

func TestSimpleTurnPersistsRunAndReply(t *testing.T) {
	model := &scriptModel{replies: []string{"2 plus 2 is 4."}}
	eng, st := runEngine(t, model)
	if err := eng.SubmitUserMessage(context.Background(), 1, "what's 2+2?", "telegram"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}

	reply := waitForLastRole(t, st, llm.RoleAI)
	if reply.Content.Text != "2 plus 2 is 4." {
		t.Fatalf("unexpected reply: %q", reply.Content.Text)
	}
	if reply.RunID == nil {
		t.Fatal("assistant message missing run_id")
	}

	// The whole transcript: one user + one assistant message.
	all, _ := st.MessagesAfter(context.Background(), 1, 0)
	if len(all) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(all))
	}
	if all[0].Role != llm.RoleUser || all[0].Source != "telegram" {
		t.Fatalf("unexpected user message: %+v", all[0])
	}
}

func TestBuildTurnMessagesPreservesReasoningPerAssistantMessage(t *testing.T) {
	call := llm.ToolCall{
		ID:   "call_1",
		Type: string(llm.ToolTypeFunction),
		Function: llm.ToolFunction{
			Name:      "read_file",
			Arguments: `{"path":"README.md"}`,
		},
	}
	base := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "read", CreatedAt: base},
		{
			Role:             llm.RoleAI,
			ReasoningContent: "need tool",
			ToolCalls:        []llm.ToolCall{call},
			CreatedAt:        base.Add(200 * time.Millisecond),
		},
		{Role: llm.RoleTool, Content: "README contents", ToolCallID: "call_1", CreatedAt: base.Add(900 * time.Millisecond)},
		{
			Role:             llm.RoleAI,
			Content:          "done",
			ReasoningContent: "final thought",
			CreatedAt:        base.Add(1300 * time.Millisecond),
		},
	}

	out := buildTurnMessages(1, 2, msgs)
	if len(out) != 3 {
		t.Fatalf("messages len = %d, want 3: %#v", len(out), out)
	}
	if out[0].Content.Reasoning != "need tool" {
		t.Fatalf("first assistant reasoning = %q, want need tool", out[0].Content.Reasoning)
	}
	if out[1].Content.Reasoning != "" {
		t.Fatalf("tool reasoning = %q, want empty", out[1].Content.Reasoning)
	}
	if out[2].Content.Reasoning != "final thought" {
		t.Fatalf("final assistant reasoning = %q, want final thought", out[2].Content.Reasoning)
	}
	// The user input is skipped; each kept row carries the real per-step time
	// golem produced it, not one shared save instant.
	if !out[0].CreatedAt.Equal(base.Add(200*time.Millisecond)) ||
		!out[1].CreatedAt.Equal(base.Add(900*time.Millisecond)) ||
		!out[2].CreatedAt.Equal(base.Add(1300*time.Millisecond)) {
		t.Fatalf("per-step timestamps not preserved: %v, %v, %v",
			out[0].CreatedAt, out[1].CreatedAt, out[2].CreatedAt)
	}
}

func TestTrailingMultiUserMessages(t *testing.T) {
	model := &scriptModel{replies: []string{"got both"}}
	eng, st := runEngine(t, model)

	ctx := context.Background()
	// Two user messages land before any run completes; the engine should treat
	// the last as input and keep the first in history.
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Source: "telegram", Content: store.Content{Text: "first line"}}); err != nil {
		t.Fatal(err)
	}
	if err := eng.SubmitUserMessage(ctx, 1, "second line", "telegram"); err != nil {
		t.Fatal(err)
	}

	waitForLastRole(t, st, llm.RoleAI)

	req, ok := model.lastRequest()
	if !ok {
		t.Fatal("model received no request")
	}
	// Request messages: system + user(first) + user(second). Find the two users.
	var users []string
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			users = append(users, m.Content)
		}
	}
	if len(users) != 2 || users[0] != "first line" || users[1] != "second line" {
		t.Fatalf("unexpected user messages in request: %v", users)
	}
}

// A user message that arrives while a run is in flight must still get answered.
// The reply to the first message lands physically after the second user message;
// driving runs from the coverage cursor (not the transcript tail) keeps the
// second message due instead of burying it under that reply.
func TestUserMessageDuringRunIsNotDropped(t *testing.T) {
	model := &blockingModel{replies: []string{"a1", "a2"}, started: make(chan int), release: make(chan struct{})}
	eng, st := runEngine(t, model)
	ctx := context.Background()

	if err := eng.SubmitUserMessage(ctx, 1, "u1", "telegram"); err != nil {
		t.Fatal(err)
	}
	// Run 1 has started and is blocked inside the model call.
	if n := recvStart(t, model); n != 0 {
		t.Fatalf("expected first run, got call %d", n)
	}
	// u2 arrives mid-run; its reply for u1 will be appended after it.
	if err := eng.SubmitUserMessage(ctx, 1, "u2", "telegram"); err != nil {
		t.Fatal(err)
	}
	model.release <- struct{}{} // let run 1 finish

	// The worker must start a second run for u2 rather than treat the assistant
	// reply at the tail as "nothing due".
	if n := recvStart(t, model); n != 1 {
		t.Fatalf("u2 was dropped: expected a second run, got call %d", n)
	}
	// Run 2 answers u2; its context must include the reply to u1 (a1), which was
	// appended physically after u2, placed before u2 in causal order — not cut by
	// a physical-id window.
	if req, ok := model.request(1); ok {
		var seq []string
		for _, m := range req.Messages {
			if m.Role == llm.RoleUser || m.Role == llm.RoleAI {
				seq = append(seq, string(m.Role)+":"+m.Content)
			}
		}
		idx := func(want string) int {
			for i, s := range seq {
				if s == want {
					return i
				}
			}
			return -1
		}
		a1, u2 := idx("assistant:a1"), idx("user:u2")
		if a1 < 0 {
			t.Fatalf("run 2 context missing the reply to u1: %v", seq)
		}
		if u2 < 0 || a1 > u2 {
			t.Fatalf("run 2 context out of causal order (a1 must precede u2): %v", seq)
		}
	}

	model.release <- struct{}{} // let run 2 finish

	waitForCovered(t, st, 1)
	all, _ := st.MessagesAfter(ctx, 1, 0)
	var users, replies []string
	for _, m := range all {
		switch m.Role {
		case llm.RoleUser:
			users = append(users, m.Content.Text)
		case llm.RoleAI:
			replies = append(replies, m.Content.Text)
		}
	}
	if len(users) != 2 || users[0] != "u1" || users[1] != "u2" {
		t.Fatalf("unexpected user messages: %v", users)
	}
	if len(replies) != 2 || replies[0] != "a1" || replies[1] != "a2" {
		t.Fatalf("unexpected replies: %v", replies)
	}
}

func recvStart(t *testing.T, m *blockingModel) int {
	t.Helper()
	select {
	case n := <-m.started:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a run to start")
		return -1
	}
}

// waitForCovered blocks until the conversation's coverage cursor reaches the
// newest user message, i.e. every user message has been answered.
func waitForCovered(t *testing.T, st *store.Store, conversationID int64) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		due, ok, err := st.NextDueInput(ctx, conversationID)
		if err != nil {
			t.Fatalf("NextDueInput: %v", err)
		}
		if !ok {
			return
		}
		_ = due
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for all user messages to be covered")
}

func TestRestartInvariantFiresPendingRun(t *testing.T) {
	// Simulate a crash: a user message persisted, no reply yet. Open the store,
	// seed the message, then start a fresh engine over the same DB.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "c.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Source: "telegram", Content: store.Content{Text: "are you there?"}}); err != nil {
		t.Fatal(err)
	}

	model := &scriptModel{replies: []string{"yes, recovered"}}
	ws, _ := workspace.Open(dir)
	eng, err := New(Config{Store: st, Workspace: ws, Main: model, MainModelID: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { eng.Start(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; st.Close() })

	reply := waitForLastRole(t, st, llm.RoleAI)
	if reply.Content.Text != "yes, recovered" {
		t.Fatalf("unexpected recovery reply: %q", reply.Content.Text)
	}
}

func TestRunFailureAppendsMessageAndStops(t *testing.T) {
	model := &scriptModel{streamErr: errModel}
	eng, st := runEngine(t, model)

	if err := eng.SubmitUserMessage(context.Background(), 1, "trigger failure", "telegram"); err != nil {
		t.Fatal(err)
	}

	reply := waitForLastRole(t, st, llm.RoleAI)
	if got := reply.Content.Text; got == "" || got[:13] != "(run failed: " {
		t.Fatalf("expected failure message, got %q", got)
	}

	// The worker must not loop: give it a moment, then assert exactly one Stream
	// call and one failure message.
	time.Sleep(100 * time.Millisecond)
	if c := model.requestCount(); c != 1 {
		t.Fatalf("expected exactly 1 model call, got %d (worker looping)", c)
	}
	all, _ := st.MessagesAfter(context.Background(), 1, 0)
	if len(all) != 2 {
		t.Fatalf("expected user + one failure message, got %d", len(all))
	}
}

// stubNotifier captures out-of-band pushes.
type stubNotifier struct {
	mu   sync.Mutex
	sent []notifiedText
}

type notifiedText struct {
	conversationID int64
	text           string
}

func (n *stubNotifier) Notify(_ context.Context, conversationID int64, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, notifiedText{conversationID: conversationID, text: text})
	return nil
}

func (n *stubNotifier) texts() []notifiedText {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifiedText(nil), n.sent...)
}

type scheduledCompletionNotifier struct {
	normal    chan notifiedText
	scheduled chan notifiedText
}

func (n *scheduledCompletionNotifier) Notify(_ context.Context, conversationID int64, text string) error {
	n.normal <- notifiedText{conversationID: conversationID, text: text}
	return nil
}

func (n *scheduledCompletionNotifier) NotifyScheduledTurn(_ context.Context, conversationID int64, reply string) error {
	n.scheduled <- notifiedText{conversationID: conversationID, text: reply}
	return nil
}

func TestHandleReminderDeliverAppendsAndNotifies(t *testing.T) {
	model := &scriptModel{}
	eng, st := runEngine(t, model)
	notifier := &stubNotifier{}
	eng.AddNotifier(notifier)

	payload, _ := json.Marshal(ReminderPayload{ConversationID: 1, Text: "drink water"})
	err := eng.HandleReminderDeliver(context.Background(), tasks.Task{Kind: KindReminderDeliver, Payload: payload})
	if err != nil {
		t.Fatalf("HandleReminderDeliver: %v", err)
	}

	last, ok, _ := st.LastMessage(context.Background(), 1)
	if !ok || last.Role != store.RoleEvent || last.Source != "reminder" || last.Content.Text != "drink water" {
		t.Fatalf("unexpected reminder message: %+v", last)
	}
	if last.RunID != nil {
		t.Fatal("reminder message should have no run_id")
	}
	if got := notifier.texts(); len(got) != 1 || got[0].conversationID != 1 || got[0].text != "⏰ drink water" {
		t.Fatalf("notifier got %v", got)
	}

	// A reminder must not trigger a model run.
	time.Sleep(50 * time.Millisecond)
	if model.requestCount() != 0 {
		t.Fatalf("reminder should not run the model, got %d calls", model.requestCount())
	}
}

func TestHandleReminderDeliverPublishesMessageEvent(t *testing.T) {
	eng, _ := runEngine(t, &scriptModel{})
	events := make(chan Event, 1)
	cancel := eng.Subscribe(func(ev Event) {
		if ev.Message != nil {
			events <- ev
		}
	})
	defer cancel()

	payload, _ := json.Marshal(ReminderPayload{ConversationID: 1, Text: "drink water"})
	if err := eng.HandleReminderDeliver(context.Background(), tasks.Task{Kind: KindReminderDeliver, Payload: payload}); err != nil {
		t.Fatalf("HandleReminderDeliver: %v", err)
	}

	select {
	case ev := <-events:
		if ev.ConversationID != 1 || ev.Message == nil || ev.Message.Source != "reminder" || ev.Message.Content.Text != "drink water" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message event")
	}
}

// A fired reminder is recorded as an event row, not an assistant message, so it
// is not dropped by the user-boundary trim and the model sees it on the next
// turn as a visible event.
func TestReminderEventReachesModel(t *testing.T) {
	model := &scriptModel{replies: []string{"noted"}}
	eng, st := runEngine(t, model)
	ctx := context.Background()

	payload, _ := json.Marshal(ReminderPayload{ConversationID: 1, Text: "drink water"})
	if err := eng.HandleReminderDeliver(ctx, tasks.Task{Kind: KindReminderDeliver, Payload: payload}); err != nil {
		t.Fatalf("HandleReminderDeliver: %v", err)
	}
	if err := eng.SubmitUserMessage(ctx, 1, "ok thanks", "telegram"); err != nil {
		t.Fatal(err)
	}
	waitForLastRole(t, st, llm.RoleAI)

	req, ok := model.lastRequest()
	if !ok {
		t.Fatal("model received no request")
	}
	var sawReminder bool
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "[reminder fired] drink water") {
			sawReminder = true
		}
	}
	if !sawReminder {
		t.Fatalf("model did not see the fired reminder in context: %+v", req.Messages)
	}
}

// failingNotifier fails while shouldFail is set, recording only delivered texts.
type failingNotifier struct {
	mu         sync.Mutex
	shouldFail bool
	sent       []string
}

func (n *failingNotifier) Notify(_ context.Context, _ int64, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.shouldFail {
		return errors.New("notify failed")
	}
	n.sent = append(n.sent, text)
	return nil
}

func (n *failingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

func (n *failingNotifier) setFail(v bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.shouldFail = v
}

// A reminder whose notify fails is retried by the queue; the retry must not
// append the reminder to the transcript a second time (idempotency).
func TestHandleReminderDeliverRetryDoesNotDuplicate(t *testing.T) {
	model := &scriptModel{}
	eng, st := runEngine(t, model)
	notifier := &failingNotifier{shouldFail: true}
	eng.AddNotifier(notifier)

	payload, _ := json.Marshal(ReminderPayload{ConversationID: 1, Text: "stretch"})

	// First attempt: notify fails, so the handler returns an error for retry.
	err := eng.HandleReminderDeliver(context.Background(), tasks.Task{Kind: KindReminderDeliver, Payload: payload, Attempts: 0})
	if err == nil {
		t.Fatal("expected error when notify fails")
	}
	// Retry (Attempts > 0): notify now succeeds; the append must be skipped.
	notifier.setFail(false)
	if err := eng.HandleReminderDeliver(context.Background(), tasks.Task{Kind: KindReminderDeliver, Payload: payload, Attempts: 1}); err != nil {
		t.Fatalf("retry HandleReminderDeliver: %v", err)
	}

	msgs, _ := st.MessagesAfter(context.Background(), 1, 0)
	reminders := 0
	for _, m := range msgs {
		if m.Source == "reminder" {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("expected exactly 1 reminder message, got %d", reminders)
	}
	if notifier.count() != 1 {
		t.Fatalf("expected 1 delivered notification, got %d", notifier.count())
	}
}

// SubmitUserMessage to a conversation with no worker lane must error and persist
// nothing, rather than silently storing a message that never runs.
func TestSubmitUserMessageNoWorkerErrors(t *testing.T) {
	eng, st := runEngine(t, &scriptModel{})

	const orphanConv = 999
	if err := eng.SubmitUserMessage(context.Background(), orphanConv, "hello?", "telegram"); err == nil {
		t.Fatal("expected error submitting to a conversation with no worker")
	}
	msgs, _ := st.MessagesAfter(context.Background(), orphanConv, 0)
	if len(msgs) != 0 {
		t.Fatalf("expected no persisted messages, got %d", len(msgs))
	}
}

func TestTrimToBudgetAlignsToUserBoundary(t *testing.T) {
	big := strings.Repeat("x", 400) // ~100 est tokens each
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: big},                                      // 0
		{Role: llm.RoleAI, Content: big, ToolCalls: []llm.ToolCall{{ID: "c1"}}}, // 1
		{Role: llm.RoleTool, ToolCallID: "c1", Content: big},                    // 2
		{Role: llm.RoleTool, ToolCallID: "c1b", Content: big},                   // 3 (multiple results)
		{Role: llm.RoleAI, Content: "earlier reply"},                            // 4
		{Role: llm.RoleUser, Content: "u2"},                                     // 5
		{Role: llm.RoleAI, ToolCalls: []llm.ToolCall{{ID: "c2"}}},               // 6
		{Role: llm.RoleTool, ToolCallID: "c2", Content: "res"},                  // 7
		{Role: llm.RoleUser, Content: "current input"},                          // 8 (tail)
	}

	// A budget that forces trimming the first user turn and its tool block. The
	// cut lands inside that block; the window must skip forward to the next user
	// turn rather than open on a dangling tool result or orphaned tool call.
	got := trimToBudget(msgs, 60)
	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAI, llm.RoleTool, llm.RoleUser}
	if len(got) != len(wantRoles) {
		t.Fatalf("expected %d messages, got %d: %+v", len(wantRoles), len(got), got)
	}
	for i, r := range wantRoles {
		if got[i].Role != r {
			t.Fatalf("message %d role = %s, want %s", i, got[i].Role, r)
		}
	}
	if got[0].Content != "u2" {
		t.Fatalf("window should start at u2, got %q", got[0].Content)
	}
	if got[len(got)-1].Content != "current input" {
		t.Fatalf("trailing input not retained: %q", got[len(got)-1].Content)
	}
}

func TestTrimToBudgetRetainsInputUnderTinyBudget(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("a", 400)},
		{Role: llm.RoleAI, Content: strings.Repeat("b", 400)},
		{Role: llm.RoleUser, Content: "the input"},
	}
	got := trimToBudget(msgs, 1)
	if len(got) != 1 || got[0].Role != llm.RoleUser || got[0].Content != "the input" {
		t.Fatalf("tiny budget must retain just the user input, got %+v", got)
	}
}

func TestTrimToBudgetAlignsLeadingBlockEvenWhenFitting(t *testing.T) {
	// Simulates a post-summary window that opens mid tool-block (foldPoint can
	// leave a leading assistant/tool). Even within budget, history must not begin
	// on a non-user turn.
	msgs := []llm.Message{
		{Role: llm.RoleAI, ToolCalls: []llm.ToolCall{{ID: "c1"}}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "res"},
		{Role: llm.RoleUser, Content: "u"},
		{Role: llm.RoleAI, Content: "reply"},
		{Role: llm.RoleUser, Content: "input"},
	}
	got := trimToBudget(msgs, 100000)
	if len(got) != 3 || got[0].Role != llm.RoleUser || got[0].Content != "u" {
		t.Fatalf("window must align to the first user turn, got %+v", got)
	}
}

func TestFoldPointAlignsKeptTailToUserTurn(t *testing.T) {
	msgs := []store.Message{
		{Role: llm.RoleUser, Content: store.Content{Text: strings.Repeat("old ", 400)}},
		{Role: llm.RoleAI, Content: store.Content{ToolCalls: []llm.ToolCall{{ID: "c1"}}}},
		{Role: llm.RoleTool, Content: store.Content{Text: "res", ToolCallID: "c1"}},
		{Role: llm.RoleAI, Content: store.Content{Text: "done"}},
		{Role: llm.RoleUser, Content: store.Content{Text: "fresh input"}},
	}
	k := foldPoint(msgs, 100)
	if k != 4 || msgs[k].Role != llm.RoleUser {
		t.Fatalf("kept tail should start at fresh user turn, k=%d role=%s", k, msgs[k].Role)
	}
}

func TestHandleAgentTurnTriggersRun(t *testing.T) {
	model := &scriptModel{replies: []string{"morning digest ready"}}
	eng, st := runEngine(t, model)
	notifier := &scheduledCompletionNotifier{
		normal:    make(chan notifiedText, 1),
		scheduled: make(chan notifiedText, 1),
	}
	eng.AddNotifier(notifier)
	messageEvents := make(chan Event, 4)
	cancel := eng.Subscribe(func(ev Event) {
		if ev.Message != nil {
			messageEvents <- ev
		}
	})
	defer cancel()

	payload, _ := json.Marshal(AgentTurnPayload{ConversationID: 1, Prompt: "summarize my day"})
	if err := eng.HandleAgentTurn(context.Background(), tasks.Task{Kind: KindAgentTurn, Payload: payload}); err != nil {
		t.Fatalf("HandleAgentTurn: %v", err)
	}

	reply := waitForLastRole(t, st, llm.RoleAI)
	if reply.Content.Text != "morning digest ready" {
		t.Fatalf("unexpected reply: %q", reply.Content.Text)
	}
	// The injected turn must be recorded as a user message with schedule source.
	all, _ := st.MessagesAfter(context.Background(), 1, 0)
	if all[0].Role != llm.RoleUser || all[0].Source != "schedule" || all[0].Content.Text != "summarize my day" {
		t.Fatalf("unexpected scheduled user message: %+v", all[0])
	}
	var scheduledRunID int64
	select {
	case ev := <-messageEvents:
		if ev.RunID == 0 || ev.Message.Role != llm.RoleUser || ev.Message.Source != "schedule" || ev.Message.Content.Text != "summarize my day" {
			t.Fatalf("scheduled input event = %+v", ev)
		}
		scheduledRunID = ev.RunID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled input event")
	}
	select {
	case ev := <-messageEvents:
		if ev.RunID != scheduledRunID || ev.Message.RunID == nil || *ev.Message.RunID != scheduledRunID ||
			ev.Message.Role != llm.RoleAI || ev.Message.Content.Text != "morning digest ready" {
			t.Fatalf("scheduled reply event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled reply event")
	}
	select {
	case got := <-notifier.scheduled:
		if got.conversationID != 1 || got.text != "morning digest ready" {
			t.Fatalf("scheduled completion notification = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduled completion notification")
	}
	select {
	case got := <-notifier.normal:
		t.Fatalf("scheduled completion should not use normal notify path: %+v", got)
	default:
	}
}

func TestSchedulerOverQueue(t *testing.T) {
	ctx := context.Background()
	queue, err := tasks.New(tasks.NewMemoryStore(), tasks.HandlerFunc(
		func(context.Context, tasks.Task) error { return nil }), tasks.Options{})
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/model", Tasks: queue})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id, err := eng.ScheduleReminder(ctx, "standup", tasks.Cron("0 9 * * 1-5", "UTC"))
	if err != nil {
		t.Fatalf("ScheduleReminder: %v", err)
	}
	reminderTask, err := queue.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get reminder task: %v", err)
	}
	var reminderPayload ReminderPayload
	if err := json.Unmarshal(reminderTask.Payload, &reminderPayload); err != nil {
		t.Fatalf("decode reminder payload: %v", err)
	}
	if reminderPayload.ConversationID != 1 {
		t.Fatalf("default scheduled reminder conversation = %d, want 1", reminderPayload.ConversationID)
	}

	webID, err := eng.ScheduleReminder(withRunConversationID(ctx, 2), "web reminder", tasks.Every(time.Hour))
	if err != nil {
		t.Fatalf("ScheduleReminder(web): %v", err)
	}
	webTask, err := queue.Get(ctx, webID)
	if err != nil {
		t.Fatalf("Get web reminder task: %v", err)
	}
	var webPayload ReminderPayload
	if err := json.Unmarshal(webTask.Payload, &webPayload); err != nil {
		t.Fatalf("decode web reminder payload: %v", err)
	}
	if webPayload.ConversationID != 2 {
		t.Fatalf("web scheduled reminder conversation = %d, want 2", webPayload.ConversationID)
	}

	turnID, err := eng.ScheduleTurn(ctx, "review", tasks.Every(24*time.Hour))
	if err != nil {
		t.Fatalf("ScheduleTurn: %v", err)
	}

	list, err := eng.ListScheduled(ctx)
	if err != nil {
		t.Fatalf("ListScheduled: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 scheduled, got %d", len(list))
	}
	for _, task := range list {
		if task.Group != TaskGroup {
			t.Fatalf("task not in caliban group: %+v", task)
		}
	}

	ok, err := eng.CancelScheduled(ctx, id)
	if err != nil || !ok {
		t.Fatalf("CancelScheduled(reminder): ok=%v err=%v", ok, err)
	}
	if _, err := eng.CancelScheduled(ctx, turnID); err != nil {
		t.Fatalf("CancelScheduled(turn): %v", err)
	}
	if _, err := eng.CancelScheduled(ctx, webID); err != nil {
		t.Fatalf("CancelScheduled(web reminder): %v", err)
	}
	list, _ = eng.ListScheduled(ctx)
	if len(list) != 0 {
		t.Fatalf("expected nothing scheduled after cancel, got %d", len(list))
	}
}

func TestDelegateCreatesChildConversationAndPersistsResult(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	model := &scriptModel{replies: []string{"child result"}}
	eng, err := New(Config{Store: st, Workspace: ws, Main: model, MainModelID: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	parentRun, err := st.CreateRun(ctx, 1, "user", "fake/model", 0)
	if err != nil {
		t.Fatal(err)
	}

	childID, reply, err := eng.Delegate(withRunContext(ctx, 1, parentRun.ID), "inspect the workspace")
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if reply != "child result" {
		t.Fatalf("reply = %q", reply)
	}
	child, err := st.Conversation(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID == nil || *child.ParentRunID != parentRun.ID {
		t.Fatalf("child conversation not tied to parent run: %+v", child)
	}
	msgs, err := st.MessagesAfter(ctx, childID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected child user + assistant messages, got %+v", msgs)
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Source != "delegate" || msgs[0].Content.Text != "inspect the workspace" {
		t.Fatalf("unexpected child prompt: %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleAI || msgs[1].Content.Text != "child result" {
		t.Fatalf("unexpected child reply: %+v", msgs[1])
	}
	if due, ok, err := st.NextDueInput(ctx, childID); err != nil || ok {
		t.Fatalf("delegated input should be covered: ok=%v due=%+v err=%v", ok, due, err)
	}
	req, ok := model.lastRequest()
	if !ok || !strings.Contains(req.Messages[0].Content, "delegated worker") {
		t.Fatalf("child prompt not used: %+v", req.Messages)
	}
}

func TestDelegateContinueUsesChildHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	model := &scriptModel{replies: []string{"first result", "second result"}}
	eng, err := New(Config{Store: st, Workspace: ws, Main: model, MainModelID: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	parentRun, err := st.CreateRun(ctx, 1, "user", "fake/model", 0)
	if err != nil {
		t.Fatal(err)
	}
	runCtx := withRunContext(ctx, 1, parentRun.ID)
	childID, _, err := eng.Delegate(runCtx, "first task")
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	reply, err := eng.DelegateContinue(runCtx, childID, "follow up")
	if err != nil {
		t.Fatalf("DelegateContinue: %v", err)
	}
	if reply != "second result" {
		t.Fatalf("reply = %q", reply)
	}
	req, ok := model.request(1)
	if !ok {
		t.Fatal("missing second child request")
	}
	var seq []string
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser || m.Role == llm.RoleAI {
			seq = append(seq, string(m.Role)+":"+m.Content)
		}
	}
	want := []string{"user:first task", "assistant:first result", "user:follow up"}
	if !equalStrings(seq, want) {
		t.Fatalf("unexpected child history: got %v want %v", seq, want)
	}
}

func TestDelegateRejectsNestedDelegation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	parentRun, _ := st.CreateRun(ctx, 1, "user", "fake/model", 0)
	child, err := st.CreateChildConversation(ctx, parentRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := st.CreateRun(ctx, child.ID, "agent", "fake/model", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.Delegate(withRunContext(ctx, child.ID, childRun.ID), "nested"); err == nil {
		t.Fatal("expected nested delegation to be rejected")
	}
}

func TestDelegateChildToolsExcludeRoutingSideEffects(t *testing.T) {
	queue, err := tasks.New(tasks.NewMemoryStore(), tasks.HandlerFunc(
		func(context.Context, tasks.Task) error { return nil }), tasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/model", Tasks: queue})
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range eng.childTools() {
		switch tool.Definition.Function.Name {
		case "delegate", "delegate_continue", "notify",
			"schedule_reminder", "schedule_turn", "list_scheduled", "cancel_scheduled":
			t.Fatalf("child agent got side-effect routing tool %q", tool.Definition.Function.Name)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The user-facing scheduling tools must not see or cancel internal maintenance
// tasks (compaction), even though they share the caliban task group.
func TestSchedulingToolsIgnoreMaintenance(t *testing.T) {
	ctx := context.Background()
	queue, err := tasks.New(tasks.NewMemoryStore(), tasks.HandlerFunc(
		func(context.Context, tasks.Task) error { return nil }), tasks.Options{})
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/model", Tasks: queue})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := eng.ScheduleReminder(ctx, "standup", tasks.Every(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An internal compaction task, enqueued directly as the engine would.
	compID := "compaction-1"
	if _, err := queue.Enqueue(ctx, tasks.Enqueue{
		ID: compID, Kind: KindCompaction, Group: TaskGroup,
		Schedule: tasks.Once(time.Now()), Payload: mustJSON(t, CompactionPayload{ConversationID: 1}),
	}); err != nil {
		t.Fatal(err)
	}

	list, err := eng.ListScheduled(ctx)
	if err != nil {
		t.Fatalf("ListScheduled: %v", err)
	}
	if len(list) != 1 || list[0].Kind != KindReminderDeliver {
		t.Fatalf("list should contain only the reminder, got %+v", list)
	}

	// Cancelling the maintenance task by id must be refused and leave it in place.
	ok, err := eng.CancelScheduled(ctx, compID)
	if err != nil {
		t.Fatalf("CancelScheduled: %v", err)
	}
	if ok {
		t.Fatal("compaction task should not be cancellable via the user tool")
	}
	if _, err := queue.Get(ctx, compID); err != nil {
		t.Fatalf("compaction task was deleted: %v", err)
	}
}

func TestCompactionFoldsTailIntoSummary(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}

	// Seed a transcript that clearly exceeds a tiny budget.
	var lastUser int64
	for i := 0; i < 20; i++ {
		u, err := st.AppendMessage(ctx, store.Message{
			ConversationID: 1, Role: llm.RoleUser, Source: "telegram",
			Content: store.Content{Text: strings.Repeat("x", 400)},
		})
		if err != nil {
			t.Fatal(err)
		}
		lastUser = u.ID
		if _, err := st.AppendMessage(ctx, store.Message{
			ConversationID: 1, Role: llm.RoleAI,
			Content: store.Content{Text: strings.Repeat("y", 400)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// These turns were all answered: compaction only folds covered messages.
	if err := st.MarkCovered(ctx, 1, lastUser); err != nil {
		t.Fatal(err)
	}

	cheap := &scriptModel{replies: []string{"User chatted at length; nothing important decided."}}
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{
		Store: st, Workspace: ws,
		Main: &scriptModel{}, MainModelID: "fake/main",
		Cheap: cheap, CheapModelID: "fake/cheap",
		TailBudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.HandleCompaction(ctx, tasks.Task{
		Kind:    KindCompaction,
		Payload: mustJSON(t, CompactionPayload{ConversationID: 1}),
	}); err != nil {
		t.Fatalf("HandleCompaction: %v", err)
	}

	sm, ok, err := st.LatestSummary(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("expected a summary: ok=%v err=%v", ok, err)
	}
	if sm.Content == "" || sm.ThroughMessageID == 0 {
		t.Fatalf("unexpected summary: %+v", sm)
	}

	// After folding, the remaining tail must be within budget.
	tail, prev, err := eng.tailAfterSummary(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if prev == "" {
		t.Fatal("summary text not picked up as previous")
	}
	if got := estMessagesTokens(tail); got > 1000 {
		t.Fatalf("tail still over budget after compaction: %d tokens", got)
	}

	// The cheap model summarized exactly once.
	if cheap.requestCount() != 1 {
		t.Fatalf("expected 1 cheap-model call, got %d", cheap.requestCount())
	}
}

func TestCompactionNoopWhenWithinBudget(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	st.EnsureMainConversation(ctx)
	st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Content: store.Content{Text: "hi"}})

	cheap := &scriptModel{}
	ws, _ := workspace.Open(t.TempDir())
	eng, _ := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, Cheap: cheap, CheapModelID: "fake/cheap", TailBudgetTokens: 1000})

	if err := eng.HandleCompaction(ctx, tasks.Task{Kind: KindCompaction, Payload: mustJSON(t, CompactionPayload{ConversationID: 1})}); err != nil {
		t.Fatalf("HandleCompaction: %v", err)
	}
	if _, ok, _ := st.LatestSummary(ctx, 1); ok {
		t.Fatal("should not have created a summary when within budget")
	}
	if cheap.requestCount() != 0 {
		t.Fatal("cheap model should not be called when within budget")
	}
}

func TestNewAppliesDefaultsAndClampsKeep(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	st.EnsureMainConversation(ctx)
	ws, _ := workspace.Open(t.TempDir())

	// Unset knobs fall back to the engine defaults.
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}})
	if err != nil {
		t.Fatal(err)
	}
	if eng.tailTok != defaultTailBudgetTokens || eng.keepTok != defaultKeepRecentTokens || eng.maxToolIter != defaultMaxToolIterations {
		t.Fatalf("defaults: tail=%d keep=%d maxTool=%d", eng.tailTok, eng.keepTok, eng.maxToolIter)
	}

	// keep must stay below the trigger; an over-large keep is clamped to budget/2
	// so a compaction always reduces the tail under budget (no re-fire loop).
	eng2, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, TailBudgetTokens: 1000, KeepRecentTokens: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if eng2.keepTok != 500 {
		t.Fatalf("keep clamp: got %d, want 500", eng2.keepTok)
	}
}

// Compaction must not fold a user message that has not been answered yet: the
// summary moves the window's start, and an uncovered input folded away would be
// lost from the run about to answer it.
func TestCompactionDoesNotFoldUncoveredInput(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	st.EnsureMainConversation(ctx)

	// Many covered turns, then a final uncovered user message (just arrived).
	var lastCovered int64
	for i := 0; i < 20; i++ {
		u, _ := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Content: store.Content{Text: strings.Repeat("x", 400)}})
		st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleAI, Content: store.Content{Text: strings.Repeat("y", 400)}})
		lastCovered = u.ID
	}
	st.MarkCovered(ctx, 1, lastCovered)
	uncovered, _ := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Content: store.Content{Text: "unanswered"}})

	cheap := &scriptModel{replies: []string{"folded summary"}}
	ws, _ := workspace.Open(t.TempDir())
	eng, _ := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, Cheap: cheap, CheapModelID: "fake/cheap", TailBudgetTokens: 1000})

	if err := eng.HandleCompaction(ctx, tasks.Task{Kind: KindCompaction, Payload: mustJSON(t, CompactionPayload{ConversationID: 1})}); err != nil {
		t.Fatalf("HandleCompaction: %v", err)
	}
	sm, ok, _ := st.LatestSummary(ctx, 1)
	if !ok {
		t.Fatal("expected a summary")
	}
	if sm.ThroughMessageID >= uncovered.ID {
		t.Fatalf("summary folded through %d, past the uncovered input %d", sm.ThroughMessageID, uncovered.ID)
	}
	// The uncovered message is still due after compaction.
	due, ok, _ := st.NextDueInput(ctx, 1)
	if !ok || due.ID != uncovered.ID {
		t.Fatalf("uncovered input lost after compaction: ok=%v id=%d", ok, due.ID)
	}
}

// An exhausted compaction task must not permanently suppress future compaction:
// the dead row is cleared and a fresh task enqueued.
func TestMaybeScheduleCompactionReplacesExhausted(t *testing.T) {
	ctx := context.Background()
	// A failing handler with MaxAttempts 1 lets us exhaust a task in one RunOnce.
	// The engine never runs this queue; it only enqueues onto it.
	queue, err := tasks.New(tasks.NewMemoryStore(), tasks.HandlerFunc(
		func(context.Context, tasks.Task) error { return errors.New("boom") }),
		tasks.Options{Retry: tasks.RetryPolicy{MaxAttempts: 1}})
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(func() { st.Close() })
	st.EnsureMainConversation(ctx)

	// Seed and exhaust a compaction task for conversation 1.
	id := "compaction-1"
	if _, err := queue.Enqueue(ctx, tasks.Enqueue{
		ID: id, Kind: KindCompaction, Group: TaskGroup,
		Schedule: tasks.Once(time.Now()), MaxAttempts: 1,
		Payload: mustJSON(t, CompactionPayload{ConversationID: 1}),
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got, err := queue.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after exhaust: %v", err)
	}
	if !got.Exhausted() {
		t.Fatalf("task should be exhausted: %+v", got)
	}

	// A transcript over budget so maybeScheduleCompaction wants to enqueue.
	var lastUser int64
	for i := 0; i < 20; i++ {
		u, _ := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Content: store.Content{Text: strings.Repeat("x", 400)}})
		st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleAI, Content: store.Content{Text: strings.Repeat("y", 400)}})
		lastUser = u.ID
	}
	st.MarkCovered(ctx, 1, lastUser)

	cheap := &scriptModel{}
	ws, _ := workspace.Open(t.TempDir())
	eng, _ := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, Cheap: cheap, CheapModelID: "fake/cheap", TailBudgetTokens: 1000, Tasks: queue})

	eng.maybeScheduleCompaction(ctx, 1)

	fresh, err := queue.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after reschedule: %v", err)
	}
	if fresh.Exhausted() {
		t.Fatalf("compaction task still exhausted; not rescheduled: %+v", fresh)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
