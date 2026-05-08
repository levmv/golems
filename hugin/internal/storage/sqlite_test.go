package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/golems/hugin/internal/models"
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

func TestRunAnalysisPersistence(t *testing.T) {
	db := newTestDB(t)
	runID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:  "disk",
		Status: models.StatusOK,
		Metrics: map[string]any{
			"used_pct": 72.5,
		},
	}, 123)
	if err != nil {
		t.Fatalf("InsertRun returned error: %v", err)
	}

	analysis := RunAnalysis{
		Severity:    "normal",
		ShouldAlert: false,
		Summary:     "Disk usage is stable",
		Evidence:    "used_pct is within the configured context",
		Model:       "openai/gpt-test",
		CreatedAt:   time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
	if err := db.UpdateRunAnalysis(runID, analysis); err != nil {
		t.Fatalf("UpdateRunAnalysis returned error: %v", err)
	}
	got, err := db.RunAnalysis(runID)
	if err != nil {
		t.Fatalf("RunAnalysis returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected persisted analysis, got nil")
	}
	if got.Severity != analysis.Severity || got.Summary != analysis.Summary || got.Model != analysis.Model {
		t.Fatalf("unexpected analysis: %+v", got)
	}
	if got.ShouldAlert {
		t.Fatalf("expected should_alert false, got %+v", got)
	}
	if !got.CreatedAt.Equal(analysis.CreatedAt) {
		t.Fatalf("expected created_at %s, got %s", analysis.CreatedAt, got.CreatedAt)
	}
}

func TestIncidentNotificationState(t *testing.T) {
	db := newTestDB(t)
	notifiedAt := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	if err := db.CreateIncident("inc-disk-1", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	inc, err := db.ActiveIncident("disk")
	if err != nil {
		t.Fatalf("ActiveIncident returned error: %v", err)
	}
	if inc.LastNotifiedAt != nil {
		t.Fatalf("expected nil last_notified_at before notification, got %s", *inc.LastNotifiedAt)
	}
	if err := db.MarkIncidentNotified("inc-disk-1", notifiedAt); err != nil {
		t.Fatalf("MarkIncidentNotified returned error: %v", err)
	}
	inc, err = db.ActiveIncident("disk")
	if err != nil {
		t.Fatalf("second ActiveIncident returned error: %v", err)
	}
	if inc.LastNotifiedAt == nil || !inc.LastNotifiedAt.Equal(notifiedAt) {
		t.Fatalf("expected last_notified_at %s, got %+v", notifiedAt, inc.LastNotifiedAt)
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
