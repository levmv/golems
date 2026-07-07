package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/levmv/golems/pkg/llm"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caliban.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsRecordedAndReopenable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "caliban.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id = '0001_initial'`).Scan(&count); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 0001_initial recorded once, got %d", count)
	}
	s.Close()

	// Reopening the same database re-runs nothing (0001 already recorded) and the
	// data survives.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected migrations recorded, got %d", count)
	}
	if _, ok, err := s2.LastMessage(ctx, 1); err != nil || ok {
		t.Fatalf("conversation 1 should persist and be empty: ok=%v err=%v", ok, err)
	}
}

func TestObsoleteMigrationRecordsAreIgnored(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "caliban.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"0002_web_push_subscriptions",
		"0003_background_tasks",
		"0004_web_auth",
		"0005_conversation_uuid",
		"0006_message_created_at_millis",
		"0007_remaining_timestamps_to_millis",
	} {
		if _, err := s.db.Exec(`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, id, unixMillis(nowUTC())); err != nil {
			t.Fatalf("insert obsolete migration %s: %v", id, err)
		}
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen with obsolete migration records: %v", err)
	}
	defer s2.Close()
	if _, err := s2.Conversation(ctx, mainConversationID); err != nil {
		t.Fatalf("read conversation after reopen: %v", err)
	}
}

func TestWebAuthPasswordAndSessions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	if _, ok, err := s.WebAuthPasswordHash(ctx); err != nil || ok {
		t.Fatalf("empty web auth password: ok=%v err=%v", ok, err)
	}
	if err := s.SetWebAuthPasswordHash(ctx, "hash-1"); err != nil {
		t.Fatalf("SetWebAuthPasswordHash: %v", err)
	}
	hash, ok, err := s.WebAuthPasswordHash(ctx)
	if err != nil || !ok || hash != "hash-1" {
		t.Fatalf("web auth password = %q ok=%v err=%v", hash, ok, err)
	}
	if err := s.SetWebAuthPasswordHash(ctx, "hash-2"); err != nil {
		t.Fatalf("SetWebAuthPasswordHash replace: %v", err)
	}
	hash, ok, err = s.WebAuthPasswordHash(ctx)
	if err != nil || !ok || hash != "hash-2" {
		t.Fatalf("replaced web auth password = %q ok=%v err=%v", hash, ok, err)
	}

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if err := s.CreateWebSession(ctx, WebSession{
		TokenHash:  "token-hash",
		UserAgent:  "test-agent",
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	sess, ok, err := s.WebSession(ctx, "token-hash")
	if err != nil || !ok || sess.UserAgent != "test-agent" || !sess.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("WebSession = %+v ok=%v err=%v", sess, ok, err)
	}
	touched := now.Add(10 * time.Minute)
	if err := s.TouchWebSession(ctx, "token-hash", touched); err != nil {
		t.Fatalf("TouchWebSession: %v", err)
	}
	sess, ok, err = s.WebSession(ctx, "token-hash")
	if err != nil || !ok || !sess.LastSeenAt.Equal(touched) {
		t.Fatalf("touched WebSession = %+v ok=%v err=%v", sess, ok, err)
	}
	deleted, err := s.DeleteWebSession(ctx, "token-hash")
	if err != nil || !deleted {
		t.Fatalf("DeleteWebSession deleted=%v err=%v", deleted, err)
	}
	if _, ok, err := s.WebSession(ctx, "token-hash"); err != nil || ok {
		t.Fatalf("deleted WebSession ok=%v err=%v", ok, err)
	}

	if err := s.CreateWebSession(ctx, WebSession{TokenHash: "expired", ExpiresAt: now.Add(-time.Minute)}); err != nil {
		t.Fatalf("CreateWebSession expired: %v", err)
	}
	if err := s.CreateWebSession(ctx, WebSession{TokenHash: "live", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("CreateWebSession live: %v", err)
	}
	n, err := s.DeleteExpiredWebSessions(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredWebSessions = %d err=%v", n, err)
	}
	if _, ok, err := s.WebSession(ctx, "live"); err != nil || !ok {
		t.Fatalf("live WebSession ok=%v err=%v", ok, err)
	}
}

func TestCreateRunRejectsMissingConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, 2, "user", "m", 0); err == nil {
		t.Fatal("expected error creating a run for a nonexistent conversation")
	}
}

func TestAppendMessageRejectsMissingConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.AppendMessage(ctx, Message{ConversationID: 99, Role: llm.RoleUser, Content: Content{Text: "hi"}}); err == nil {
		t.Fatal("expected error appending to a nonexistent conversation")
	}
}

func TestFinishRunRejectsDoubleFinish(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	run, _ := s.CreateRun(ctx, 1, "user", "m", 0)
	if err := s.FinishRun(ctx, run.ID, "done", llm.Usage{}, ""); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	if err := s.FinishRun(ctx, run.ID, "done", llm.Usage{}, ""); err == nil {
		t.Fatal("expected error finishing an already-finished run")
	}
}

func TestAppendSummaryRejectsCrossConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureConversation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	m, err := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "in conv 1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Summarizing conversation 2 through a message that belongs to conversation 1
	// must be rejected.
	if _, err := s.AppendSummary(ctx, Summary{ConversationID: 2, ThroughMessageID: m.ID, Content: "x"}); err == nil {
		t.Fatal("expected cross-conversation summary to be rejected")
	}
	// The same through-message in its own conversation is fine.
	if _, err := s.AppendSummary(ctx, Summary{ConversationID: 1, ThroughMessageID: m.ID, Content: "ok"}); err != nil {
		t.Fatalf("same-conversation summary rejected: %v", err)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "caliban.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open with missing parent dir: %v", err)
	}
	defer s.Close()
	if _, err := s.EnsureMainConversation(context.Background()); err != nil {
		t.Fatalf("EnsureMainConversation: %v", err)
	}
}

func TestEnsureMainConversationIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	c1, err := s.EnsureMainConversation(ctx)
	if err != nil {
		t.Fatalf("EnsureMainConversation: %v", err)
	}
	if c1.ID != 1 || c1.Status != "active" || c1.ParentRunID != nil {
		t.Fatalf("unexpected main conversation: %+v", c1)
	}
	requireUUIDv7(t, c1.UUID)
	if c1.CreatedAt.IsZero() {
		t.Fatal("created_at not set")
	}

	c2, err := s.EnsureMainConversation(ctx)
	if err != nil {
		t.Fatalf("second EnsureMainConversation: %v", err)
	}
	if c2.ID != c1.ID || c2.UUID != c1.UUID || !c2.CreatedAt.Equal(c1.CreatedAt) {
		t.Fatalf("second ensure created a different conversation: %+v vs %+v", c2, c1)
	}

	active, err := s.ActiveConversations(ctx)
	if err != nil {
		t.Fatalf("ActiveConversations: %v", err)
	}
	if len(active) != 1 || active[0].ID != 1 {
		t.Fatalf("expected one active conversation, got %+v", active)
	}
}

func TestEnsureAndCreateConversations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	web, err := s.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatalf("EnsureConversation(2): %v", err)
	}
	if web.ID != 2 || web.Status != "active" {
		t.Fatalf("unexpected web conversation: %+v", web)
	}
	requireUUIDv7(t, web.UUID)
	again, err := s.EnsureConversation(ctx, 2)
	if err != nil {
		t.Fatalf("EnsureConversation(2) again: %v", err)
	}
	if again.ID != web.ID || again.UUID != web.UUID || !again.CreatedAt.Equal(web.CreatedAt) {
		t.Fatalf("ensure not idempotent: %+v vs %+v", again, web)
	}

	created, err := s.CreateConversation(ctx)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if created.ID == 0 || created.ID == 2 || created.Status != "active" {
		t.Fatalf("unexpected created conversation: %+v", created)
	}
	requireUUIDv7(t, created.UUID)
	byUUID, ok, err := s.ConversationByUUID(ctx, created.UUID)
	if err != nil || !ok || byUUID.ID != created.ID {
		t.Fatalf("ConversationByUUID = %+v ok=%v err=%v", byUUID, ok, err)
	}
}

func TestCreateChildConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, 1, "user", "model", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateChildConversation(ctx, run.ID)
	if err != nil {
		t.Fatalf("CreateChildConversation: %v", err)
	}
	if child.ID == 0 || child.ParentRunID == nil || *child.ParentRunID != run.ID || child.Status != "active" {
		t.Fatalf("unexpected child conversation: %+v", child)
	}
	active, err := s.ActiveConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != 1 {
		t.Fatalf("child conversations should not be top-level active conversations: %+v", active)
	}
	if _, err := s.CreateChildConversation(ctx, run.ID+999); err == nil {
		t.Fatal("expected missing parent run to be rejected")
	}
}

func requireUUIDv7(t *testing.T, value string) {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("invalid uuid %q: %v", value, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("uuid %q version = %d, want 7", value, parsed.Version())
	}
}

func TestPushSubscriptionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureConversation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	ps := PushSubscription{
		Endpoint:       "https://push.example/sub-1",
		ConversationID: 2,
		P256DH:         "p256dh",
		Auth:           "auth",
		UserAgent:      "test-agent",
	}
	if err := s.UpsertPushSubscription(ctx, ps); err != nil {
		t.Fatalf("UpsertPushSubscription: %v", err)
	}
	list, err := s.PushSubscriptions(ctx, 2)
	if err != nil {
		t.Fatalf("PushSubscriptions: %v", err)
	}
	if len(list) != 1 || list[0].Endpoint != ps.Endpoint || list[0].P256DH != ps.P256DH || list[0].Auth != ps.Auth {
		t.Fatalf("unexpected subscriptions: %+v", list)
	}

	ps.P256DH = "new-p256dh"
	if err := s.UpsertPushSubscription(ctx, ps); err != nil {
		t.Fatalf("second UpsertPushSubscription: %v", err)
	}
	list, err = s.PushSubscriptions(ctx, 2)
	if err != nil {
		t.Fatalf("PushSubscriptions after update: %v", err)
	}
	if len(list) != 1 || list[0].P256DH != "new-p256dh" {
		t.Fatalf("subscription not updated: %+v", list)
	}

	deleted, err := s.DeletePushSubscription(ctx, ps.Endpoint)
	if err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	if !deleted {
		t.Fatal("expected subscription to be deleted")
	}
	list, err = s.PushSubscriptions(ctx, 2)
	if err != nil {
		t.Fatalf("PushSubscriptions after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no subscriptions after delete, got %+v", list)
	}
}

func TestPushSubscriptionRejectsMissingConversation(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	err := s.UpsertPushSubscription(ctx, PushSubscription{
		Endpoint:       "https://push.example/sub-1",
		ConversationID: 99,
		P256DH:         "p256dh",
		Auth:           "auth",
	})
	if err == nil {
		t.Fatal("expected missing conversation error")
	}
}

func TestRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	run, err := s.CreateRun(ctx, 1, "user", "deepseek/deepseek-chat", 0)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.ID == 0 || run.Status != "running" {
		t.Fatalf("unexpected run: %+v", run)
	}

	usage := llm.Usage{PromptTokens: 100, CompletionTokens: 42}
	if err := s.FinishRun(ctx, run.ID, "done", usage, ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	got := loadRun(t, s, run.ID)
	if got.Status != "done" || got.InputTokens != 100 || got.OutputTokens != 42 {
		t.Fatalf("finished run mismatch: %+v", got)
	}
	if got.Error != "" {
		t.Fatalf("expected empty error, got %q", got.Error)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
}

func TestFailRunAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	run, _ := s.CreateRun(ctx, 1, "user", "m", 0)
	usage := llm.Usage{PromptTokens: 7, CompletionTokens: 3}
	failure := Message{
		ConversationID: 1,
		RunID:          &run.ID,
		Role:           llm.RoleAI,
		Content:        Content{Text: "(run failed: boom)"},
	}
	stored, err := s.FailRun(ctx, run.ID, 1, 0, usage, "boom", failure)
	if err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	if stored.ID == 0 || stored.CreatedAt.IsZero() {
		t.Fatalf("stored failure message not populated: %+v", stored)
	}

	got := loadRun(t, s, run.ID)
	if got.Status != "failed" || got.Error != "boom" || got.InputTokens != 7 || got.OutputTokens != 3 {
		t.Fatalf("run not failed as expected: %+v", got)
	}

	last, ok, err := s.LastMessage(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("LastMessage: ok=%v err=%v", ok, err)
	}
	if last.Role != llm.RoleAI || last.Content.Text != "(run failed: boom)" || last.RunID == nil || *last.RunID != run.ID {
		t.Fatalf("unexpected trailing failure message: %+v", last)
	}
}

func TestCoverageCursorDrivesDueInput(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// No messages: nothing due.
	if _, ok, err := s.NextDueInput(ctx, 1); err != nil || ok {
		t.Fatalf("empty conversation should have no due input: ok=%v err=%v", ok, err)
	}

	u1, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u1"}})
	u2, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u2"}})

	// The newest uncovered user message is the due input (u1 is history, coalesced).
	due, ok, err := s.NextDueInput(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("NextDueInput: ok=%v err=%v", ok, err)
	}
	if due.ID != u2.ID {
		t.Fatalf("due input = %d, want newest user %d", due.ID, u2.ID)
	}

	// A run answers u2: CompleteRun appends the reply, finishes the run, and
	// advances the cursor past both u1 and u2 in one shot.
	run, _ := s.CreateRun(ctx, 1, "user", "m", u2.ID)
	reply := Message{ConversationID: 1, RunID: &run.ID, Role: llm.RoleAI, Content: Content{Text: "answer"}}
	stored, err := s.CompleteRun(ctx, run.ID, 1, u2.ID, []Message{reply}, llm.Usage{PromptTokens: 5, CompletionTokens: 2})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if len(stored) != 1 || stored[0].ID == 0 || stored[0].CreatedAt.IsZero() || stored[0].Content.Text != "answer" {
		t.Fatalf("CompleteRun stored output = %+v", stored)
	}
	if got := loadRun(t, s, run.ID); got.Status != "done" {
		t.Fatalf("run not done: %+v", got)
	}
	if cur, _ := s.CoveredThrough(ctx, 1); cur != u2.ID {
		t.Fatalf("cursor = %d, want %d", cur, u2.ID)
	}
	// With u1 and u2 covered, nothing is due even though u1 < u2 < cursor.
	if _, ok, _ := s.NextDueInput(ctx, 1); ok {
		t.Fatalf("no input should be due after covering through u2")
	}

	// A message arriving after the run (the dropped-message case) is due again.
	u3, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u3"}})
	due, ok, _ = s.NextDueInput(ctx, 1)
	if !ok || due.ID != u3.ID {
		t.Fatalf("u3 should be due, got ok=%v id=%d", ok, due.ID)
	}

	// The cursor never moves backwards.
	if err := s.MarkCovered(ctx, 1, u1.ID); err != nil {
		t.Fatalf("MarkCovered: %v", err)
	}
	if cur, _ := s.CoveredThrough(ctx, 1); cur != u2.ID {
		t.Fatalf("cursor moved backwards to %d", cur)
	}
}

// MessagesForInput must order history causally: a reply that was appended after
// a newer user message still belongs before that newer message (right after the
// input it answered), and a causally-later message is excluded.
func TestMessagesForInputLogicalOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	u1, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u1"}})
	u2, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u2"}})
	// Run answering u1 lands its reply physically after u2 (the mid-run case).
	r1, _ := s.CreateRun(ctx, 1, "user", "m", u1.ID)
	a1, _ := s.AppendMessage(ctx, Message{ConversationID: 1, RunID: &r1.ID, Role: llm.RoleAI, Content: Content{Text: "a1"}})
	// A still-newer user message and its reply must be excluded from u2's window.
	u3, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "u3"}})

	got, err := s.MessagesForInput(ctx, 1, 0, u2.ID)
	if err != nil {
		t.Fatalf("MessagesForInput: %v", err)
	}
	gotIDs := make([]int64, len(got))
	for i, m := range got {
		gotIDs[i] = m.ID
	}
	want := []int64{u1.ID, a1.ID, u2.ID} // a1 sits after u1, before u2; u3 excluded
	if len(gotIDs) != len(want) {
		t.Fatalf("got ids %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("order mismatch: got %v, want %v", gotIDs, want)
		}
	}
	_ = u3
}

func TestMessagesTail(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure main: %v", err)
	}
	if _, err := s.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("ensure second: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := s.AppendMessage(ctx, Message{
			ConversationID: 1,
			Role:           llm.RoleUser,
			Content:        Content{Text: "m" + strconv.Itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendMessage(ctx, Message{ConversationID: 2, Role: llm.RoleUser, Content: Content{Text: "other"}}); err != nil {
		t.Fatal(err)
	}

	got, hasMore, err := s.MessagesTail(ctx, 1, 3)
	if err != nil {
		t.Fatalf("MessagesTail: %v", err)
	}
	if !hasMore || len(got) != 3 {
		t.Fatalf("len=%d hasMore=%v, want 3 true", len(got), hasMore)
	}
	for i, m := range got {
		want := "m" + strconv.Itoa(i+3)
		if m.Content.Text != want {
			t.Fatalf("message %d = %q, want %q", i, m.Content.Text, want)
		}
	}

	got, hasMore, err = s.MessagesTail(ctx, 1, 10)
	if err != nil {
		t.Fatalf("MessagesTail large: %v", err)
	}
	if hasMore || len(got) != 5 {
		t.Fatalf("len=%d hasMore=%v, want 5 false", len(got), hasMore)
	}
}

func TestMessagesTailExpandsToRunInput(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure main: %v", err)
	}
	if _, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Content:        Content{Text: "older question"},
	}); err != nil {
		t.Fatal(err)
	}
	user, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Content:        Content{Text: "inspect the workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, 1, "user", "model", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := []Message{
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content: Content{ToolCalls: []llm.ToolCall{{
				ID: "call_1",
				Function: llm.ToolFunction{
					Name:      "shell",
					Arguments: `{"command":"find . -maxdepth 1"}`,
				},
			}}},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleTool,
			Content:        Content{Text: ".\n./README.md", ToolCallID: "call_1"},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content:        Content{Text: "Workspace has README.md."},
		},
	}
	if _, err := s.CompleteRun(ctx, run.ID, 1, user.ID, out, llm.Usage{}); err != nil {
		t.Fatal(err)
	}

	got, hasMore, err := s.MessagesTail(ctx, 1, 2)
	if err != nil {
		t.Fatalf("MessagesTail: %v", err)
	}
	if !hasMore {
		t.Fatalf("hasMore = false, want true for older message before expanded run")
	}
	if len(got) != 4 {
		t.Fatalf("expanded tail len = %d, want 4: roles=%v texts=%v", len(got), msgRoles(got), msgTexts(got))
	}
	if got[0].ID != user.ID || got[0].Role != llm.RoleUser {
		t.Fatalf("expanded tail should start with run input, got %+v", got[0])
	}
	for i, m := range got[1:] {
		if m.RunID == nil || *m.RunID != run.ID {
			t.Fatalf("message %d has run_id %+v, want %d", i+1, m.RunID, run.ID)
		}
	}
}

func TestMessagesBefore(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure main: %v", err)
	}
	if _, err := s.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("ensure second: %v", err)
	}
	var ids []int64
	for i := 1; i <= 5; i++ {
		m, err := s.AppendMessage(ctx, Message{
			ConversationID: 1,
			Role:           llm.RoleUser,
			Content:        Content{Text: "m" + strconv.Itoa(i)},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	// A newer row in another conversation must not leak into the cursor page.
	if _, err := s.AppendMessage(ctx, Message{ConversationID: 2, Role: llm.RoleUser, Content: Content{Text: "other"}}); err != nil {
		t.Fatal(err)
	}

	// Page of 2 below m5 -> [m3, m4], more remain (m1, m2).
	got, hasMore, err := s.MessagesBefore(ctx, 1, ids[4], 2)
	if err != nil {
		t.Fatalf("MessagesBefore: %v", err)
	}
	if !hasMore || len(got) != 2 || got[0].Content.Text != "m3" || got[1].Content.Text != "m4" {
		t.Fatalf("got=%v hasMore=%v, want [m3 m4] true", msgTexts(got), hasMore)
	}

	// Everything below m3 fits in one page -> [m1, m2], no more.
	got, hasMore, err = s.MessagesBefore(ctx, 1, ids[2], 5)
	if err != nil {
		t.Fatalf("MessagesBefore exhausted: %v", err)
	}
	if hasMore || len(got) != 2 || got[0].Content.Text != "m1" || got[1].Content.Text != "m2" {
		t.Fatalf("got=%v hasMore=%v, want [m1 m2] false", msgTexts(got), hasMore)
	}

	// Nothing older than the first message.
	got, hasMore, err = s.MessagesBefore(ctx, 1, ids[0], 3)
	if err != nil {
		t.Fatalf("MessagesBefore empty: %v", err)
	}
	if hasMore || len(got) != 0 {
		t.Fatalf("got=%v hasMore=%v, want [] false", msgTexts(got), hasMore)
	}
}

func TestMessagesBeforeExpandsToRunInput(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure main: %v", err)
	}
	user, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Content:        Content{Text: "inspect the workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, 1, "user", "model", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := []Message{
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content: Content{ToolCalls: []llm.ToolCall{{
				ID: "call_1",
				Function: llm.ToolFunction{
					Name:      "shell",
					Arguments: `{"command":"find . -maxdepth 1"}`,
				},
			}}},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleTool,
			Content:        Content{Text: ".\n./README.md", ToolCallID: "call_1"},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content:        Content{Text: "Workspace has README.md."},
		},
	}
	if _, err := s.CompleteRun(ctx, run.ID, 1, user.ID, out, llm.Usage{}); err != nil {
		t.Fatal(err)
	}
	next, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Content:        Content{Text: "next question"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, hasMore, err := s.MessagesBefore(ctx, 1, next.ID, 2)
	if err != nil {
		t.Fatalf("MessagesBefore: %v", err)
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false for fully expanded first run")
	}
	if len(got) != 4 {
		t.Fatalf("expanded page len = %d, want 4: roles=%v texts=%v", len(got), msgRoles(got), msgTexts(got))
	}
	if got[0].ID != user.ID || got[0].Role != llm.RoleUser {
		t.Fatalf("expanded page should start with run input, got %+v", got[0])
	}
	for i, m := range got[1:] {
		if m.RunID == nil || *m.RunID != run.ID {
			t.Fatalf("message %d has run_id %+v, want %d", i+1, m.RunID, run.ID)
		}
	}
}

func msgTexts(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content.Text
	}
	return out
}

func msgRoles(msgs []Message) []llm.Role {
	out := make([]llm.Role, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func TestSearchMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure main: %v", err)
	}
	if _, err := s.EnsureConversation(ctx, 2); err != nil {
		t.Fatalf("ensure second: %v", err)
	}

	want, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Source: "web", Content: Content{Text: "Caliban PWA push notifications are working."}})
	_, _ = s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleAI, Content: Content{Text: "Unrelated answer."}})
	_, _ = s.AppendMessage(ctx, Message{ConversationID: 2, Role: llm.RoleUser, Source: "web", Content: Content{Text: "Caliban in another conversation."}})
	percent, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "Progress is 100% literal."}})

	got, err := s.SearchMessages(ctx, 1, "caliban", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got) != 1 || got[0].ID != want.ID {
		t.Fatalf("unexpected caliban matches: %+v", got)
	}

	got, err = s.SearchMessages(ctx, 1, "100%", 10)
	if err != nil {
		t.Fatalf("SearchMessages percent: %v", err)
	}
	if len(got) != 1 || got[0].ID != percent.ID {
		t.Fatalf("unexpected percent matches: %+v", got)
	}

	got, err = s.SearchMessages(ctx, 1, "web", 10)
	if err != nil {
		t.Fatalf("SearchMessages source: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("source should not be searched, got %+v", got)
	}
}

func TestFailRunAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	u1, _ := s.AppendMessage(ctx, Message{ConversationID: 1, Role: llm.RoleUser, Content: Content{Text: "boom please"}})
	run, _ := s.CreateRun(ctx, 1, "user", "m", u1.ID)
	failure := Message{ConversationID: 1, RunID: &run.ID, Role: llm.RoleAI, Content: Content{Text: "(run failed: boom)"}}
	if _, err := s.FailRun(ctx, run.ID, 1, u1.ID, llm.Usage{}, "boom", failure); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	// A failed run still covers its input, so the worker does not loop on it.
	if cur, _ := s.CoveredThrough(ctx, 1); cur != u1.ID {
		t.Fatalf("failed run did not advance cursor: %d", cur)
	}
	if _, ok, _ := s.NextDueInput(ctx, 1); ok {
		t.Fatalf("poison input should be covered, not due")
	}
}

func TestFailRunningRuns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	r1, _ := s.CreateRun(ctx, 1, "user", "m", 0)
	r2, _ := s.CreateRun(ctx, 1, "user", "m", 0)
	if err := s.FinishRun(ctx, r2.ID, "done", llm.Usage{}, ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	n, err := s.FailRunningRuns(ctx)
	if err != nil {
		t.Fatalf("FailRunningRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 run failed, got %d", n)
	}
	if got := loadRun(t, s, r1.ID); got.Status != "failed" || got.Error != "interrupted by restart" {
		t.Fatalf("r1 not failed: %+v", got)
	}
	if got := loadRun(t, s, r2.ID); got.Status != "done" {
		t.Fatalf("r2 should stay done: %+v", got)
	}
}

func TestAppendAndReadMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if _, ok, err := s.LastMessage(ctx, 1); err != nil || ok {
		t.Fatalf("empty conversation: ok=%v err=%v", ok, err)
	}

	run, _ := s.CreateRun(ctx, 1, "user", "m", 0)

	user, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Source:         "telegram",
		Content:        Content{Text: "what's 2+2?"},
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if user.ID == 0 || user.CreatedAt.IsZero() {
		t.Fatalf("append did not set id/created_at: %+v", user)
	}
	// An assistant tool-call turn plus the tool result, in one tx.
	appended, err := s.AppendMessages(ctx, []Message{
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content: Content{
				Reasoning: "need a calculator",
				ToolCalls: []llm.ToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: llm.ToolFunction{Name: "shell", Arguments: `{"command":"echo $((2+2))"}`},
				}},
			},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleTool,
			Content:        Content{Text: "4\n", ToolCallID: "call_1"},
		},
		{
			ConversationID: 1,
			RunID:          &run.ID,
			Role:           llm.RoleAI,
			Content:        Content{Text: "It's 4."},
		},
	})
	if err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	if len(appended) != 3 {
		t.Fatalf("expected 3 appended, got %d", len(appended))
	}

	last, ok, err := s.LastMessage(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("LastMessage: ok=%v err=%v", ok, err)
	}
	if last.Role != llm.RoleAI || last.Content.Text != "It's 4." {
		t.Fatalf("unexpected last message: %+v", last)
	}

	after, err := s.MessagesAfter(ctx, 1, user.ID)
	if err != nil {
		t.Fatalf("MessagesAfter: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("expected 3 messages after user, got %d", len(after))
	}
	// Verify the tool-call round-trips through JSON intact.
	toolCallMsg := after[0]
	if toolCallMsg.Content.Reasoning != "need a calculator" {
		t.Fatalf("reasoning lost: %+v", toolCallMsg.Content)
	}
	if len(toolCallMsg.Content.ToolCalls) != 1 || toolCallMsg.Content.ToolCalls[0].Function.Name != "shell" {
		t.Fatalf("tool call lost: %+v", toolCallMsg.Content)
	}
	if got := after[1]; got.Content.ToolCallID != "call_1" || got.RunID == nil || *got.RunID != run.ID {
		t.Fatalf("tool result lost fields: %+v", got)
	}

	all, err := s.MessagesAfter(ctx, 1, 0)
	if err != nil {
		t.Fatalf("MessagesAfter(0): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 total messages, got %d", len(all))
	}
}

// TestMessageTimestampMillisecondPrecision guards the reason messages.created_at
// is stored as unix millis rather than seconds: sub-second timing must survive
// the store round-trip so UIs can show work durations finer than "0s".
func TestMessageTimestampMillisecondPrecision(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	want := time.Date(2026, 6, 22, 10, 30, 0, 123_000_000, time.UTC) // .123s
	appended, err := s.AppendMessage(ctx, Message{
		ConversationID: 1,
		Role:           llm.RoleUser,
		Content:        Content{Text: "hi"},
		CreatedAt:      want,
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	last, ok, err := s.LastMessage(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("LastMessage: ok=%v err=%v", ok, err)
	}
	for _, got := range []time.Time{appended.CreatedAt, last.CreatedAt} {
		if !got.Equal(want) {
			t.Fatalf("timestamp lost sub-second precision: got %v, want %v", got, want)
		}
	}
}

func TestLatestSummary(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if _, ok, err := s.LatestSummary(ctx, 1); err != nil || ok {
		t.Fatalf("expected no summary: ok=%v err=%v", ok, err)
	}

	for i, body := range []string{"older", "newest"} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO summaries (conversation_id, through_message_id, content, created_at)
			 VALUES (?, ?, ?, ?)`,
			1, int64(10+i), body, unixMillis(nowUTC()))
		if err != nil {
			t.Fatalf("insert summary: %v", err)
		}
	}

	sm, ok, err := s.LatestSummary(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("LatestSummary: ok=%v err=%v", ok, err)
	}
	if sm.Content != "newest" || sm.ThroughMessageID != 11 {
		t.Fatalf("expected newest summary, got %+v", sm)
	}
}

func loadRun(t *testing.T, s *Store, id int64) Run {
	t.Helper()
	var (
		r          Run
		errMsg     *string
		createdAt  int64
		finishedAt *int64
	)
	err := s.db.QueryRow(
		`SELECT id, conversation_id, initiator, model, status, input_tokens, output_tokens, error, created_at, finished_at
		 FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.ConversationID, &r.Initiator, &r.Model, &r.Status,
			&r.InputTokens, &r.OutputTokens, &errMsg, &createdAt, &finishedAt)
	if err != nil {
		t.Fatalf("loadRun %d: %v", id, err)
	}
	r.CreatedAt = fromUnixMilli(createdAt)
	if errMsg != nil {
		r.Error = *errMsg
	}
	if finishedAt != nil {
		ft := fromUnixMilli(*finishedAt)
		r.FinishedAt = &ft
	}
	return r
}

func TestPruneChildConversations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}

	// A run in the main conversation parents the delegation child conversations.
	run, err := s.CreateRun(ctx, 1, "agent", "m", 0)
	if err != nil {
		t.Fatal(err)
	}
	oldChild, err := s.CreateChildConversation(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	recentChild, err := s.CreateChildConversation(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Give the old child a message + a summary, then backdate it past the cutoff.
	if _, err := s.AppendMessage(ctx, Message{ConversationID: oldChild.ID, Role: llm.RoleUser, Content: Content{Text: "search please"}}); err != nil {
		t.Fatal(err)
	}
	msg, err := s.AppendMessage(ctx, Message{ConversationID: oldChild.ID, Role: llm.RoleAI, Content: Content{Text: "found it"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendSummary(ctx, Summary{ConversationID: oldChild.ID, ThroughMessageID: msg.ID, Content: "summary"}); err != nil {
		t.Fatal(err)
	}
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour).UTC().UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE conversations SET created_at=? WHERE id=?`, thirtyDaysAgo, oldChild.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneChildConversations(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d conversations, want 1", n)
	}

	// Old child fully gone; recent child and the main conversation untouched.
	if _, err := s.Conversation(ctx, oldChild.ID); err == nil {
		t.Fatal("old child conversation should be deleted")
	}
	if _, err := s.Conversation(ctx, recentChild.ID); err != nil {
		t.Fatalf("recent child conversation must survive: %v", err)
	}
	if _, err := s.Conversation(ctx, 1); err != nil {
		t.Fatalf("main conversation must survive: %v", err)
	}
	if c := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE conversation_id=?`, oldChild.ID); c != 0 {
		t.Fatalf("old child messages not pruned: %d left", c)
	}
	if c := countRows(t, s, `SELECT COUNT(*) FROM summaries WHERE conversation_id=?`, oldChild.ID); c != 0 {
		t.Fatalf("old child summaries not pruned: %d left", c)
	}
	// Runs are kept for cost history even though their conversation is gone.
	if c := countRows(t, s, `SELECT COUNT(*) FROM runs WHERE id=?`, run.ID); c != 1 {
		t.Fatalf("run must survive prune for cost history, got %d", c)
	}
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}
