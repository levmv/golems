package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

func reflectionEngine(t *testing.T, model *scriptModel) (*Engine, *store.Store, *workspace.Workspace) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.EnsureMainConversation(ctx); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(Config{Store: st, Workspace: ws, Main: model, MainModelID: "fake/main"})
	if err != nil {
		t.Fatal(err)
	}
	return eng, st, ws
}

func seedInteraction(t *testing.T, st *store.Store, user, assistant string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleUser, Source: "telegram", Content: store.Content{Text: user}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 1, Role: llm.RoleAI, Content: store.Content{Text: assistant}}); err != nil {
		t.Fatal(err)
	}
}

func TestReflectionUpdatesPersonaOnChange(t *testing.T) {
	ctx := context.Background()
	const newPersona = "I am Caliban. I keep replies terse and skip pleasantries, as this user prefers."
	model := &scriptModel{replies: []string{newPersona}}
	eng, st, ws := reflectionEngine(t, model)
	seedInteraction(t, st, "stop being so chatty, just answer", "Understood.")

	if err := eng.HandleReflection(ctx, tasks.Task{Kind: KindReflection}); err != nil {
		t.Fatalf("HandleReflection: %v", err)
	}

	got, err := ws.Persona()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != newPersona {
		t.Fatalf("persona not updated:\n got: %q\nwant: %q", strings.TrimSpace(got), newPersona)
	}
	if model.requestCount() != 1 {
		t.Fatalf("expected 1 model call, got %d", model.requestCount())
	}
}

// TestReflectionReadsNonMainConversation is the regression for the bug where
// reflection only ever read the hardcoded Telegram conversation (id 1): a
// web-only user (conversation 2) left conversation 1 empty, so every pass found
// no activity and silently no-oped. Reflection must sample all user conversations.
func TestReflectionReadsNonMainConversation(t *testing.T) {
	ctx := context.Background()
	const newPersona = "I am Caliban. This user works with me over the web."
	model := &scriptModel{replies: []string{newPersona}}
	eng, st, ws := reflectionEngine(t, model)
	// The user talks only in the web conversation (id 2); the Telegram main (id 1) is empty.
	if _, err := st.EnsureConversation(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 2, Role: llm.RoleUser, Source: "web", Content: store.Content{Text: "keep it terse"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(ctx, store.Message{ConversationID: 2, Role: llm.RoleAI, Content: store.Content{Text: "Will do."}}); err != nil {
		t.Fatal(err)
	}

	if err := eng.HandleReflection(ctx, tasks.Task{Kind: KindReflection}); err != nil {
		t.Fatalf("HandleReflection: %v", err)
	}
	if model.requestCount() != 1 {
		t.Fatalf("reflection should read the web conversation; model calls = %d", model.requestCount())
	}
	if got, _ := ws.Persona(); strings.TrimSpace(got) != newPersona {
		t.Fatalf("persona not updated from web activity: %q", strings.TrimSpace(got))
	}
}

func TestReflectionNoChangeKeepsPersona(t *testing.T) {
	ctx := context.Background()
	const original = "I am Caliban, a careful assistant."
	model := &scriptModel{replies: []string{reflectionNoChange}}
	eng, st, ws := reflectionEngine(t, model)
	if err := ws.WritePersona(original); err != nil {
		t.Fatal(err)
	}
	seedInteraction(t, st, "what's the weather", "I can't check live weather.")

	if err := eng.HandleReflection(ctx, tasks.Task{Kind: KindReflection}); err != nil {
		t.Fatalf("HandleReflection: %v", err)
	}

	got, err := ws.Persona()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got) != original {
		t.Fatalf("persona changed on NO_CHANGE: %q", strings.TrimSpace(got))
	}
	if model.requestCount() != 1 {
		t.Fatalf("expected 1 model call, got %d", model.requestCount())
	}
}

func TestReflectionSkipsWhenNoActivity(t *testing.T) {
	ctx := context.Background()
	model := &scriptModel{replies: []string{"should not be used"}}
	eng, _, ws := reflectionEngine(t, model) // no seeded messages

	if err := eng.HandleReflection(ctx, tasks.Task{Kind: KindReflection}); err != nil {
		t.Fatalf("HandleReflection: %v", err)
	}
	if model.requestCount() != 0 {
		t.Fatalf("model should not be called with no activity, got %d calls", model.requestCount())
	}
	if got, _ := ws.Persona(); got != "" {
		t.Fatalf("persona should stay empty, got %q", got)
	}
}

func TestReflectionIgnoresOversizedUpdate(t *testing.T) {
	ctx := context.Background()
	huge := strings.Repeat("x", maxPersonaBytes+1)
	model := &scriptModel{replies: []string{huge}}
	eng, st, ws := reflectionEngine(t, model)
	if err := ws.WritePersona("original"); err != nil {
		t.Fatal(err)
	}
	seedInteraction(t, st, "hello", "hi")

	if err := eng.HandleReflection(ctx, tasks.Task{Kind: KindReflection}); err != nil {
		t.Fatalf("HandleReflection: %v", err)
	}
	if got, _ := ws.Persona(); strings.TrimSpace(got) != "original" {
		t.Fatalf("oversized update should be ignored, persona is now %d bytes", len(got))
	}
}

func TestEnsureReflectionScheduleIsIdempotentAndHidden(t *testing.T) {
	ctx := context.Background()
	queue, err := tasks.New(tasks.NewMemoryStore(),
		tasks.HandlerFunc(func(context.Context, tasks.Task) error { return nil }), tasks.Options{})
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.EnsureMainConversation(ctx)
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/main", Tasks: queue})
	if err != nil {
		t.Fatal(err)
	}

	eng.ensureReflectionSchedule(ctx)
	eng.ensureReflectionSchedule(ctx) // second call must not error or duplicate

	got, err := queue.Get(ctx, reflectionTaskID)
	if err != nil {
		t.Fatalf("reflection task not scheduled: %v", err)
	}
	if got.Kind != KindReflection {
		t.Fatalf("unexpected kind %q", got.Kind)
	}

	// Reflection shares the caliban task group but must never surface to the user.
	scheduled, err := eng.ListScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 0 {
		t.Fatalf("reflection leaked into user-facing scheduled list: %+v", scheduled)
	}
}
