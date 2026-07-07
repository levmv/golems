package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/tasks"
)

func TestEnsureSubagentPruneScheduleIsIdempotentAndHidden(t *testing.T) {
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

	eng.ensureSubagentPruneSchedule(ctx)
	eng.ensureSubagentPruneSchedule(ctx) // second call must not error or duplicate

	got, err := queue.Get(ctx, subagentPruneTaskID)
	if err != nil {
		t.Fatalf("prune task not scheduled: %v", err)
	}
	if got.Kind != KindSubagentPrune {
		t.Fatalf("unexpected kind %q", got.Kind)
	}

	// Housekeeping task must never surface in the user-facing scheduled list.
	scheduled, err := eng.ListScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 0 {
		t.Fatalf("prune task leaked into user-facing scheduled list: %+v", scheduled)
	}
}
