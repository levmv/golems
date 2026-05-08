package engine

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/tasks"
)

func TestSyncCheckTasksReconcilesConfigWithTaskQueue(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	cfg := testConfig()
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))
	store, err := db.TaskStore()
	if err != nil {
		t.Fatalf("TaskStore returned error: %v", err)
	}
	q := mustTaskQueue(t, store)

	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("syncCheckTasks returned error: %v", err)
	}
	ref := onlyCheckTaskRef(t, db)
	if ref.CheckID != "disk" || ref.TaskID != checkTaskID("disk") {
		t.Fatalf("unexpected ref after first sync: %+v", ref)
	}
	task, err := store.Get(ctx, ref.TaskID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if task.Kind != checkJobKind || task.Group != "web" || task.Schedule.CronExpr != "*/5 * * * *" {
		t.Fatalf("unexpected task after first sync: %+v", task)
	}
	createdAt := task.CreatedAt

	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("second syncCheckTasks returned error: %v", err)
	}
	task, err = store.Get(ctx, ref.TaskID)
	if err != nil {
		t.Fatalf("second Get returned error: %v", err)
	}
	if !task.CreatedAt.Equal(createdAt) {
		t.Fatalf("unchanged sync should preserve existing task, created_at changed from %s to %s", createdAt, task.CreatedAt)
	}

	if ok, err := q.Delete(ctx, ref.TaskID); err != nil || !ok {
		t.Fatalf("Delete existing task returned ok=%v err=%v", ok, err)
	}
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("repair syncCheckTasks returned error: %v", err)
	}
	task, err = store.Get(ctx, ref.TaskID)
	if err != nil {
		t.Fatalf("repair Get returned error: %v", err)
	}
	if task.Kind != checkJobKind {
		t.Fatalf("expected missing task to be repaired, got %+v", task)
	}

	cfg.Checks[0].Schedule = "*/10 * * * *"
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("changed syncCheckTasks returned error: %v", err)
	}
	changedRef := onlyCheckTaskRef(t, db)
	if changedRef.Fingerprint == ref.Fingerprint {
		t.Fatalf("expected fingerprint to change, got %q", changedRef.Fingerprint)
	}
	task, err = store.Get(ctx, changedRef.TaskID)
	if err != nil {
		t.Fatalf("changed Get returned error: %v", err)
	}
	if task.Schedule.CronExpr != "*/10 * * * *" {
		t.Fatalf("expected updated schedule, got %+v", task.Schedule)
	}

	cfg.Checks = nil
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("removed syncCheckTasks returned error: %v", err)
	}
	refs, err := db.CheckTaskRefs(ctx)
	if err != nil {
		t.Fatalf("CheckTaskRefs returned error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected refs to be removed, got %+v", refs)
	}
	_, err = store.Get(ctx, changedRef.TaskID)
	if !errors.Is(err, tasks.ErrNotFound) {
		t.Fatalf("expected task to be removed, got %v", err)
	}
}

func testConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Timezone:            "UTC",
			MaxConcurrentChecks: 2,
		},
		Targets: map[string]config.Target{
			"web": {Host: "example.test"},
		},
		Checks: []config.Check{
			{
				ID:       "disk",
				Target:   "web",
				Command:  "true",
				Schedule: "*/5 * * * *",
				Timeout:  time.Second,
			},
		},
	}
}

func mustTaskQueue(t *testing.T, store tasks.Store) *tasks.Queue {
	t.Helper()
	q, err := tasks.New(store, tasks.HandlerFunc(func(ctx context.Context, task tasks.Task) error {
		return nil
	}), tasks.Options{})
	if err != nil {
		t.Fatalf("tasks.New returned error: %v", err)
	}
	return q
}

func onlyCheckTaskRef(t *testing.T, db *storage.DB) storage.CheckTaskRef {
	t.Helper()
	refs, err := db.CheckTaskRefs(context.Background())
	if err != nil {
		t.Fatalf("CheckTaskRefs returned error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected one ref, got %+v", refs)
	}
	return refs[0]
}
