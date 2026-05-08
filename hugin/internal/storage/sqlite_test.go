package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/tasks"
)

func TestTaskStoreIsInitializedInHuginDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	next := now

	store, err := db.TaskStore()
	if err != nil {
		t.Fatalf("TaskStore returned error: %v", err)
	}

	if err := store.Enqueue(ctx, tasks.Task{
		ID:          "task-1",
		Kind:        "hugin.check",
		Payload:     []byte(`{"check_id":"disk"}`),
		Schedule:    tasks.Once(now),
		Group:       "target:web",
		Timeout:     time.Second,
		MaxAttempts: 3,
		NextRunAt:   &next,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	claimed, err := store.ClaimDue(ctx, now, time.Minute, 0, "token")
	if err != nil {
		t.Fatalf("ClaimDue returned error: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected one claimed task, got %d", len(claimed))
	}

	nextRun := now.Add(time.Hour)
	ok, err := store.Finish(ctx, tasks.Finish{
		ID:         "task-1",
		LockToken:  "token",
		FinishedAt: now.Add(time.Second),
		NextRunAt:  &nextRun,
	})
	if err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected Finish to update claimed job")
	}
}

func TestCheckTaskRefs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ref := CheckTaskRef{CheckID: "disk", TaskID: "hugin.check:disk", Fingerprint: "first"}
	if err := db.UpsertCheckTaskRef(ctx, ref); err != nil {
		t.Fatalf("UpsertCheckTaskRef returned error: %v", err)
	}
	refs, err := db.CheckTaskRefs(ctx)
	if err != nil {
		t.Fatalf("CheckTaskRefs returned error: %v", err)
	}
	if len(refs) != 1 || refs[0] != ref {
		t.Fatalf("unexpected refs: %+v", refs)
	}

	ref.Fingerprint = "second"
	if err := db.UpsertCheckTaskRef(ctx, ref); err != nil {
		t.Fatalf("second UpsertCheckTaskRef returned error: %v", err)
	}
	refs, err = db.CheckTaskRefs(ctx)
	if err != nil {
		t.Fatalf("second CheckTaskRefs returned error: %v", err)
	}
	if len(refs) != 1 || refs[0] != ref {
		t.Fatalf("unexpected refs after upsert: %+v", refs)
	}

	if err := db.DeleteCheckTaskRef(ctx, ref.CheckID); err != nil {
		t.Fatalf("DeleteCheckTaskRef returned error: %v", err)
	}
	refs, err = db.CheckTaskRefs(ctx)
	if err != nil {
		t.Fatalf("final CheckTaskRefs returned error: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected refs to be deleted, got %+v", refs)
	}
}

func newTestDB(t *testing.T) *DB {
	t.Helper()

	db, err := New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return db
}
