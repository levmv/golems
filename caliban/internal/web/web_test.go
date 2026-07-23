package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

type submitCall struct {
	conv   int64
	text   string
	source string
}

type fakeEngine struct {
	mu        sync.Mutex
	sub       func(engine.Event)
	submitted []submitCall
}

func (f *fakeEngine) SubmitUserMessage(_ context.Context, conv int64, text, source string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitted = append(f.submitted, submitCall{conv, text, source})
	return nil
}

func (f *fakeEngine) Subscribe(fn func(engine.Event)) func() {
	f.mu.Lock()
	f.sub = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.sub = nil
		f.mu.Unlock()
	}
}

func (f *fakeEngine) emit(ev engine.Event) {
	f.mu.Lock()
	fn := f.sub
	f.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func (f *fakeEngine) hasSub() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sub != nil
}

func (f *fakeEngine) lastSubmit() (submitCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.submitted) == 0 {
		return submitCall{}, false
	}
	return f.submitted[len(f.submitted)-1], true
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSubmitForwardsToEngine(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chats/1/runs", "application/json",
		strings.NewReader(`{"input":"hello","clientMessageId":"m1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	got, ok := fake.lastSubmit()
	if !ok || got.conv != 1 || got.text != "hello" || got.source != "web" {
		t.Fatalf("unexpected submit: %+v ok=%v", got, ok)
	}
}

func TestSubmitValidatesInput(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chats/1/runs", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if _, ok := fake.lastSubmit(); ok {
		t.Fatal("engine should not be called on invalid input")
	}
}

func TestSubmitRejectsOversizedBody(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	body := `{"input":"` + strings.Repeat("x", maxSubmitBodyBytes) + `"}`
	resp, err := http.Post(srv.URL+"/api/chats/1/runs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if _, ok := fake.lastSubmit(); ok {
		t.Fatal("engine should not be called for an oversized body")
	}
}

func TestSubmitRejectsTrailingJSON(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chats/1/runs", "application/json", strings.NewReader(`{"input":"hello"} {}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if _, ok := fake.lastSubmit(); ok {
		t.Fatal("engine should not be called for trailing JSON")
	}
}

func TestServesEmbeddedPWA(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Caliban") || !strings.Contains(string(body), "/app.js") {
		t.Fatalf("index does not look like the PWA shell:\n%s", string(body))
	}

	resp, err = http.Get(srv.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app.js status = %d", resp.StatusCode)
	}
}

func TestMainChatAliasIsNotAccepted(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	srv := httptest.NewServer(New(Config{Engine: &fakeEngine{}, Store: st}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chats/main")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestNumericChatIDRejectedWhenStoreEnabled(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	srv := httptest.NewServer(New(Config{Engine: &fakeEngine{}, Store: st}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/chats/2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAuthRequiresLoginAndSetsSessionCookie(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.SetWebAuthPasswordHash(ctx, hash); err != nil {
		t.Fatalf("SetWebAuthPasswordHash: %v", err)
	}
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	srv := httptest.NewServer(New(Config{
		Engine: &fakeEngine{},
		Store:  st,
		Auth:   AuthConfig{Enabled: true},
	}).Handler())
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := client.Get(srv.URL + "/api/chats")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api status = %d, want 401", resp.StatusCode)
	}

	resp, err = client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("root status/location = %d %q, want redirect to login", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = client.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"wrong-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong login status = %d, want 401", resp.StatusCode)
	}

	resp, err = client.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"password":"correct-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("unexpected login cookies: %+v", cookies)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/chats", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookies[0])
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated api status = %d, want 200", resp.StatusCode)
	}
}

func TestAuthRejectsCrossOriginMutations(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := st.SetWebAuthPasswordHash(ctx, hash); err != nil {
		t.Fatalf("SetWebAuthPasswordHash: %v", err)
	}
	conv, err := st.EnsureConversation(ctx, 1)
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{
		Engine: fake,
		Store:  st,
		Auth:   AuthConfig{Enabled: true},
	}).Handler())
	defer srv.Close()
	cookie := loginCookie(t, srv, "correct-password")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/chats/"+conv.UUID+"/runs", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", resp.StatusCode)
	}
	if _, ok := fake.lastSubmit(); ok {
		t.Fatal("engine should not be called for cross-origin mutation")
	}

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/chats/"+conv.UUID+"/runs", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(cookie)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("same-origin status = %d, want 202", resp.StatusCode)
	}
}

func loginCookie(t *testing.T, srv *httptest.Server, password string) *http.Cookie {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"password":`+strconv.Quote(password)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status = %d body=%s", resp.StatusCode, string(body))
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login did not set session cookie")
	return nil
}

func TestPushConfigDisabled(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/push/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got pushConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.PublicKey != "" {
		t.Fatalf("unexpected push config: %+v", got)
	}
}

func TestSavePushSubscription(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEngine{}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Config{
		Engine: fake,
		Store:  st,
		Push:   PushConfig{VAPIDPublicKey: "pub", VAPIDPrivateKey: "priv", Subject: "admin@example.com"},
	}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/chats/"+conv.UUID+"/push-subscriptions", "application/json",
		strings.NewReader(`{"endpoint":"https://push.example/sub","keys":{"p256dh":"dh","auth":"auth"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	subs, err := st.PushSubscriptions(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/sub" || subs[0].P256DH != "dh" || subs[0].Auth != "auth" {
		t.Fatalf("unexpected subscriptions: %+v", subs)
	}
}

func TestNotifySendsPushAndDeletesExpiredSubscriptions(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEngine{}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://push.example/live", "https://push.example/gone"} {
		if err := st.UpsertPushSubscription(ctx, store.PushSubscription{
			Endpoint: endpoint, ConversationID: 2, P256DH: "dh", Auth: "auth",
		}); err != nil {
			t.Fatal(err)
		}
	}
	tr := New(Config{
		Engine: fake,
		Store:  st,
		Push:   PushConfig{VAPIDPublicKey: "pub", VAPIDPrivateKey: "priv", Subject: "admin@example.com"},
	})
	var sent []string
	tr.sendPush = func(_ context.Context, sub store.PushSubscription, payload pushPayload) (int, error) {
		if payload.Title != "\u23f0" {
			t.Fatalf("unexpected payload title: %+v", payload)
		}
		if payload.Body != "stand up" {
			t.Fatalf("unexpected payload: %+v", payload)
		}
		if strings.Contains(sub.Endpoint, "gone") {
			return http.StatusGone, nil
		}
		sent = append(sent, sub.Endpoint)
		return http.StatusCreated, nil
	}

	if err := tr.Notify(ctx, 2, "\u23f0 stand up"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(sent) != 1 || sent[0] != "https://push.example/live" {
		t.Fatalf("unexpected sends: %+v", sent)
	}
	subs, err := st.PushSubscriptions(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Endpoint != "https://push.example/live" {
		t.Fatalf("expired subscription not deleted: %+v", subs)
	}
}

func TestNotifyScheduledTurnSendsPreviewPush(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPushSubscription(ctx, store.PushSubscription{
		Endpoint: "https://push.example/live", ConversationID: 2, P256DH: "dh", Auth: "auth",
	}); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{
		Engine: &fakeEngine{},
		Store:  st,
		Push:   PushConfig{VAPIDPublicKey: "pub", VAPIDPrivateKey: "priv", Subject: "admin@example.com"},
	})
	var payloads []pushPayload
	tr.sendPush = func(_ context.Context, sub store.PushSubscription, payload pushPayload) (int, error) {
		if sub.Endpoint != "https://push.example/live" {
			t.Fatalf("unexpected subscription: %+v", sub)
		}
		payloads = append(payloads, payload)
		return http.StatusCreated, nil
	}

	reply := "  Проверил логи:\nсбоев нет, но очередь выросла. " + strings.Repeat("x", 300)
	if err := tr.NotifyScheduledTurn(ctx, 2, reply); err != nil {
		t.Fatalf("NotifyScheduledTurn: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(payloads))
	}
	payload := payloads[0]
	if payload.Title != scheduledTurnPushNotification {
		t.Fatalf("title = %q, want %q", payload.Title, scheduledTurnPushNotification)
	}
	if strings.Contains(payload.Body, "\n") {
		t.Fatalf("preview should be one line: %q", payload.Body)
	}
	if !strings.HasPrefix(payload.Body, "Проверил логи: сбоев нет") {
		t.Fatalf("preview did not keep reply start: %q", payload.Body)
	}
	if len([]rune(payload.Body)) > scheduledTurnPushBodyMaxRunes {
		t.Fatalf("preview len = %d, want <= %d", len([]rune(payload.Body)), scheduledTurnPushBodyMaxRunes)
	}
	if !strings.HasPrefix(payload.Tag, "caliban-scheduled-2-") || payload.URL != "/" {
		t.Fatalf("unexpected payload metadata: %+v", payload)
	}
}

func TestEventsStreamTranslatesRun(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/chats/1/events?client_run_id=client-run-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(resp.Body)
		out <- string(b)
	}()

	// Wait until the handler has subscribed before emitting.
	deadline := time.Now().Add(2 * time.Second)
	for !fake.hasSub() {
		if time.Now().After(deadline) {
			t.Fatal("handler did not subscribe")
		}
		time.Sleep(2 * time.Millisecond)
	}

	fake.emit(engine.Event{ConversationID: 1, RunID: 7, Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: "hi web"}})
	// An event for a different conversation must be ignored.
	fake.emit(engine.Event{ConversationID: 2, RunID: 9, Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: "other chat"}})
	fake.emit(engine.Event{ConversationID: 1, RunID: 7, Ev: golem.StreamEvent{Kind: golem.EventDone, Usage: llm.Usage{TotalTokens: 5}, FinishReason: llm.FinishReasonStop}})

	time.Sleep(50 * time.Millisecond)
	cancel()
	s := <-out

	for _, want := range []string{
		"event: message_start",
		"event: text_delta",
		"hi web",
		`"messageId":"msg_client-run-1_assistant_0"`,
		`"runId":"client-run-1"`,
		"event: finish",
		`"reason":"stop"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SSE output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "other chat") {
		t.Fatalf("events from another conversation leaked:\n%s", s)
	}
}

func TestGetChatSetsRunIDOnAnsweredUserMessages(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEngine{}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.AppendMessage(ctx, store.Message{
		ConversationID: 2,
		Role:           llm.RoleUser,
		Source:         "web",
		Content:        store.Content{Text: "list files"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, 2, "user", "model", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	toolCall := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleAI,
		Content: store.Content{ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "shell",
				Arguments: `{"command":"ls"}`,
			},
		}}},
	}
	toolResult := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleTool,
		Content:        store.Content{Text: "README.md", ToolCallID: "call_1"},
	}
	final := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleAI,
		Content:        store.Content{Text: "Done."},
	}
	if _, err := st.CompleteRun(ctx, run.ID, 2, user.ID, []store.Message{toolCall, toolResult, final}, llm.Usage{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(Config{Engine: fake, Store: st}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/chats/" + conv.UUID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var chat struct {
		Messages []struct {
			Role  string `json:"role"`
			RunID string `json:"runId"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(chat.Messages))
	}
	for i, msg := range chat.Messages {
		if msg.RunID != strconv.FormatInt(run.ID, 10) {
			t.Fatalf("message %d (%s) runId = %q, want %d", i, msg.Role, msg.RunID, run.ID)
		}
	}
}

func TestGetChatReturnsBoundedTail(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEngine{}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := st.AppendMessage(ctx, store.Message{
			ConversationID: 2,
			Role:           llm.RoleUser,
			Source:         "web",
			Content:        store.Content{Text: "message " + strconv.Itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(New(Config{Engine: fake, Store: st}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/chats/" + conv.UUID + "?limit=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var chat struct {
		Messages []struct {
			Blocks []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
		HasMoreMessages         bool   `json:"hasMoreMessages"`
		NextOlderMessagesCursor string `json:"nextOlderMessagesCursor"`
		MessageLimit            int    `json:"messageLimit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 3 || !chat.HasMoreMessages || chat.MessageLimit != 3 || chat.NextOlderMessagesCursor == "" {
		t.Fatalf("unexpected tail metadata: %+v", chat)
	}
	if got := chat.Messages[0].Blocks[0].Text; got != "message 3" {
		t.Fatalf("first message = %q, want message 3", got)
	}
}

func TestGetChatTailExpandsToRunInput(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{
		ConversationID: 2,
		Role:           llm.RoleUser,
		Source:         "web",
		Content:        store.Content{Text: "older question"},
	}); err != nil {
		t.Fatal(err)
	}
	user, err := st.AppendMessage(ctx, store.Message{
		ConversationID: 2,
		Role:           llm.RoleUser,
		Source:         "web",
		Content:        store.Content{Text: "list files"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, 2, "user", "model", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	toolCall := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleAI,
		Content: store.Content{ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: llm.ToolFunction{
				Name:      "shell",
				Arguments: `{"command":"ls"}`,
			},
		}}},
	}
	toolResult := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleTool,
		Content:        store.Content{Text: "README.md", ToolCallID: "call_1"},
	}
	final := store.Message{
		ConversationID: 2,
		RunID:          &run.ID,
		Role:           llm.RoleAI,
		Content:        store.Content{Text: "Done."},
	}
	if _, err := st.CompleteRun(ctx, run.ID, 2, user.ID, []store.Message{toolCall, toolResult, final}, llm.Usage{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(Config{Engine: &fakeEngine{}, Store: st}).Handler())
	defer srv.Close()

	var chat struct {
		Messages []struct {
			Role   string `json:"role"`
			RunID  string `json:"runId"`
			Blocks []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
		HasMoreMessages         bool   `json:"hasMoreMessages"`
		NextOlderMessagesCursor string `json:"nextOlderMessagesCursor"`
		MessageLimit            int    `json:"messageLimit"`
	}
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?limit=2", &chat)
	if len(chat.Messages) != 4 || !chat.HasMoreMessages || chat.MessageLimit != 2 {
		t.Fatalf("unexpected expanded tail metadata: %+v", chat)
	}
	if chat.NextOlderMessagesCursor != strconv.FormatInt(user.ID, 10) {
		t.Fatalf("next older cursor = %q, want %d", chat.NextOlderMessagesCursor, user.ID)
	}
	if chat.Messages[0].Role != "user" || chat.Messages[0].RunID != strconv.FormatInt(run.ID, 10) {
		t.Fatalf("first message should be the run input with run id, got %+v", chat.Messages[0])
	}
	if got := chat.Messages[0].Blocks[0].Text; got != "list files" {
		t.Fatalf("first message text = %q, want list files", got)
	}
	for i, msg := range chat.Messages[1:] {
		if msg.RunID != strconv.FormatInt(run.ID, 10) {
			t.Fatalf("message %d runId = %q, want %d", i+1, msg.RunID, run.ID)
		}
	}
}

func TestGetChatPreservesUserMessageSourceMeta(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{
		ConversationID: 2,
		Role:           llm.RoleUser,
		Source:         "schedule",
		Content:        store.Content{Text: "summarize my day"},
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(Config{Engine: &fakeEngine{}, Store: st}).Handler())
	defer srv.Close()

	var chat struct {
		Messages []struct {
			Role string         `json:"role"`
			Meta map[string]any `json:"meta"`
		} `json:"messages"`
	}
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID, &chat)
	if len(chat.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(chat.Messages))
	}
	if chat.Messages[0].Role != "user" || chat.Messages[0].Meta["source"] != "schedule" {
		t.Fatalf("scheduled source was not preserved: %+v", chat.Messages[0])
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestGetChatPaginatesOlderMessages(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEngine{}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := st.AppendMessage(ctx, store.Message{
			ConversationID: 2,
			Role:           llm.RoleUser,
			Source:         "web",
			Content:        store.Content{Text: "message " + strconv.Itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(New(Config{Engine: fake, Store: st}).Handler())
	defer srv.Close()

	// The tail gives us the opaque cursor to page back from.
	var tail struct {
		NextOlderMessagesCursor string `json:"nextOlderMessagesCursor"`
	}
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?limit=3", &tail)
	if tail.NextOlderMessagesCursor == "" {
		t.Fatal("missing nextOlderMessagesCursor")
	}

	type page struct {
		Messages []struct {
			Blocks []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
		HasMore                 bool   `json:"hasMore"`
		NextOlderMessagesCursor string `json:"nextOlderMessagesCursor"`
	}

	// Page back using the cursor from the tail: "message 2", and a cursor remains.
	var p1 page
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?before="+tail.NextOlderMessagesCursor+"&limit=1", &p1)
	if len(p1.Messages) != 1 || !p1.HasMore || p1.Messages[0].Blocks[0].Text != "message 2" || p1.NextOlderMessagesCursor == "" {
		t.Fatalf("first older page = %+v", p1)
	}

	// Following the page's own cursor reaches the last message; nothing older remains.
	var p2 page
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?before="+p1.NextOlderMessagesCursor+"&limit=5", &p2)
	if len(p2.Messages) != 1 || p2.HasMore || p2.Messages[0].Blocks[0].Text != "message 1" || p2.NextOlderMessagesCursor != "" {
		t.Fatalf("exhausting older page = %+v", p2)
	}
}

func TestGetChatOlderPageCapsTargetLimit(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	conv, err := st.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 120; i++ {
		if _, err := st.AppendMessage(ctx, store.Message{
			ConversationID: 2,
			Role:           llm.RoleUser,
			Source:         "web",
			Content:        store.Content{Text: "message " + strconv.Itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(Config{Engine: &fakeEngine{}, Store: st}).Handler())
	defer srv.Close()

	var tail struct {
		NextOlderMessagesCursor string `json:"nextOlderMessagesCursor"`
	}
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?limit=1", &tail)
	if tail.NextOlderMessagesCursor == "" {
		t.Fatal("missing nextOlderMessagesCursor")
	}
	var page struct {
		Messages []struct {
			Blocks []struct {
				Text string `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
		HasMore bool `json:"hasMore"`
	}
	getJSON(t, srv.URL+"/api/chats/"+conv.UUID+"?before="+tail.NextOlderMessagesCursor+"&limit=100", &page)
	if len(page.Messages) != maxOlderChatPageTargetLimit || !page.HasMore {
		t.Fatalf("older page len=%d hasMore=%v, want %d true", len(page.Messages), page.HasMore, maxOlderChatPageTargetLimit)
	}
	if got := page.Messages[0].Blocks[0].Text; got != "message 40" {
		t.Fatalf("first capped older message = %q, want message 40", got)
	}
}

func TestEventsStreamEmitsPersistedMessages(t *testing.T) {
	fake := &fakeEngine{}
	srv := httptest.NewServer(New(Config{Engine: fake}).Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/chats/1/events?scope=messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(resp.Body)
		out <- string(b)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !fake.hasSub() {
		if time.Now().After(deadline) {
			t.Fatal("handler did not subscribe")
		}
		time.Sleep(2 * time.Millisecond)
	}

	created := time.Unix(1_700_000_000, 0)
	runID := int64(7)
	fake.emit(engine.Event{ConversationID: 1, RunID: runID, Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: "run text"}})
	scheduledPrompt := store.Message{
		ID:             41,
		ConversationID: 1,
		Role:           llm.RoleUser,
		Source:         "schedule",
		Content:        store.Content{Text: "scheduled prompt"},
		CreatedAt:      created,
	}
	fake.emit(engine.Event{ConversationID: 1, RunID: runID, Message: &scheduledPrompt})
	scheduledReply := store.Message{
		ID:             42,
		ConversationID: 1,
		RunID:          &runID,
		Role:           llm.RoleAI,
		Content:        store.Content{Text: "scheduled answer"},
		CreatedAt:      created.Add(time.Second),
	}
	fake.emit(engine.Event{ConversationID: 1, RunID: runID, Message: &scheduledReply})
	msg := store.Message{
		ID:             43,
		ConversationID: 1,
		Role:           store.RoleEvent,
		Source:         "reminder",
		Content:        store.Content{Text: "stand up"},
		CreatedAt:      created,
	}
	fake.emit(engine.Event{ConversationID: 1, Message: &msg})
	fake.emit(engine.Event{ConversationID: 2, Message: &store.Message{ID: 44, Role: store.RoleEvent, Content: store.Content{Text: "other chat"}}})

	time.Sleep(50 * time.Millisecond)
	cancel()
	s := <-out

	for _, want := range []string{
		"event: message_start",
		`"id":"msg_db_41"`,
		`"role":"user"`,
		`"runId":"7"`,
		`"source":"schedule"`,
		"scheduled prompt",
		`"id":"msg_db_42"`,
		`"role":"assistant"`,
		"scheduled answer",
		`"id":"msg_db_43"`,
		`"role":"system"`,
		`"source":"reminder"`,
		"stand up",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SSE output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "run text") || strings.Contains(s, "other chat") {
		t.Fatalf("wrong event leaked:\n%s", s)
	}
}
