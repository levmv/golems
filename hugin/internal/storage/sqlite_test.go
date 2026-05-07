package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/schedule"
)

func TestScheduleRunClaimLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	dueAt := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	startedAt := dueAt.Add(time.Second)

	_, err := db.LastRun(ctx, "job-1")
	if !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("expected missing LastRun to return ErrNotFound, got %v", err)
	}

	record := schedule.RunRecord{
		JobID:         "job-1",
		OccurrenceKey: "2026-05-07T12:00:00Z",
		Kind:          "hugin.check",
		Ref:           "disk",
		Group:         "local",
		DueAt:         dueAt,
		StartedAt:     startedAt,
		Status:        schedule.RunRunning,
	}

	claimed, ok, err := db.TryCreateRun(ctx, record)
	if err != nil {
		t.Fatalf("TryCreateRun returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected first TryCreateRun to claim")
	}
	if claimed.ID == "" {
		t.Fatalf("expected claimed run ID")
	}

	duplicate, ok, err := db.TryCreateRun(ctx, record)
	if err != nil {
		t.Fatalf("duplicate TryCreateRun returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected duplicate TryCreateRun not to claim: %#v", duplicate)
	}

	finishedAt := startedAt.Add(2 * time.Second)
	if err := db.FinishRun(ctx, claimed.ID, schedule.RunFailed, "collector failed", finishedAt); err != nil {
		t.Fatalf("FinishRun returned error: %v", err)
	}

	last, err := db.LastRun(ctx, "job-1")
	if err != nil {
		t.Fatalf("LastRun returned error: %v", err)
	}
	if last.ID != claimed.ID {
		t.Fatalf("expected LastRun ID %q, got %q", claimed.ID, last.ID)
	}
	if last.Status != schedule.RunFailed {
		t.Fatalf("expected failed status, got %q", last.Status)
	}
	if last.Error != "collector failed" {
		t.Fatalf("expected error message to be stored, got %q", last.Error)
	}
	if last.FinishedAt == nil || !last.FinishedAt.Equal(finishedAt) {
		t.Fatalf("expected finished_at %s, got %#v", finishedAt, last.FinishedAt)
	}
}

func TestLastRunsSkipsMissingJobs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	_, ok, err := db.TryCreateRun(ctx, schedule.RunRecord{
		JobID:         "job-1",
		OccurrenceKey: "initial",
		DueAt:         now,
		StartedAt:     now,
		Status:        schedule.RunRunning,
	})
	if err != nil {
		t.Fatalf("TryCreateRun returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected TryCreateRun to claim")
	}

	runs, err := db.LastRuns(ctx, []string{"job-1", "missing"})
	if err != nil {
		t.Fatalf("LastRuns returned error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d: %#v", len(runs), runs)
	}
	if _, ok := runs["job-1"]; !ok {
		t.Fatalf("expected job-1 in LastRuns result: %#v", runs)
	}
	if _, ok := runs["missing"]; ok {
		t.Fatalf("did not expect missing job in LastRuns result: %#v", runs)
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
