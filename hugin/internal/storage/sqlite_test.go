package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
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

func TestSQLiteBusyTimeoutAppliesToEveryConnection(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	first, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatalf("first Conn returned error: %v", err)
	}
	defer first.Close()

	second, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatalf("second Conn returned error: %v", err)
	}
	defer second.Close()

	want := int(sqliteBusyTimeout.Milliseconds())
	for _, tc := range []struct {
		name string
		conn *sql.Conn
	}{
		{name: "first", conn: first},
		{name: "second", conn: second},
	} {
		var got int
		if err := tc.conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&got); err != nil {
			t.Fatalf("%s busy_timeout query returned error: %v", tc.name, err)
		}
		if got != want {
			t.Fatalf("%s connection busy_timeout = %d, want %d", tc.name, got, want)
		}
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

func TestRunAnalysisUsesCreatedAtAsSentinel(t *testing.T) {
	db := newTestDB(t)
	runID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"used_pct": 72.5},
	}, 123)
	if err != nil {
		t.Fatalf("InsertRun returned error: %v", err)
	}
	if _, err := db.sql.Exec(
		`UPDATE runs SET analysis_severity = ?, analysis_should_alert = ?, analysis_summary = ? WHERE id = ?`,
		"normal", false, "stale partial analysis", runID,
	); err != nil {
		t.Fatalf("partial analysis update returned error: %v", err)
	}

	got, err := db.RunAnalysis(runID)
	if err != nil {
		t.Fatalf("RunAnalysis returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil without analysis_created_at, got %+v", got)
	}
}

func TestRunAnalysisPipelineFailurePersistence(t *testing.T) {
	db := newTestDB(t)
	runID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"used_pct": 72.5},
	}, 123)
	if err != nil {
		t.Fatalf("InsertRun returned error: %v", err)
	}

	if err := db.MarkRunAnalysisPipelineFailed(runID, errors.New("incident update failed")); err != nil {
		t.Fatalf("MarkRunAnalysisPipelineFailed returned error: %v", err)
	}
	got, err := db.RunAnalysis(runID)
	if err != nil {
		t.Fatalf("RunAnalysis returned error: %v", err)
	}
	if got == nil || got.PipelineError != "incident update failed" || got.PipelineFailedAt == nil {
		t.Fatalf("unexpected pipeline failure: %+v", got)
	}

	analysis := RunAnalysis{
		Severity:  "normal",
		Summary:   "Recovered",
		Model:     "openai/gpt-test",
		CreatedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
	if err := db.UpdateRunAnalysis(runID, analysis); err != nil {
		t.Fatalf("UpdateRunAnalysis returned error: %v", err)
	}
	got, err = db.RunAnalysis(runID)
	if err != nil {
		t.Fatalf("RunAnalysis after recovery returned error: %v", err)
	}
	if got == nil || got.PipelineError != "" || got.PipelineFailedAt != nil {
		t.Fatalf("successful analysis did not clear pipeline failure: %+v", got)
	}
}

func TestRunsSinceExcludingOmitsCurrentRun(t *testing.T) {
	db := newTestDB(t)
	since := time.Now().Add(-time.Hour)

	previousID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"used_pct": 70.0},
	}, 100)
	if err != nil {
		t.Fatalf("InsertRun previous returned error: %v", err)
	}
	currentID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"used_pct": 80.0},
	}, 100)
	if err != nil {
		t.Fatalf("InsertRun current returned error: %v", err)
	}

	runs, err := db.RunsSinceExcluding("disk", since, currentID, 10)
	if err != nil {
		t.Fatalf("RunsSinceExcluding returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != previousID {
		t.Fatalf("expected only previous run %d, got %+v", previousID, runs)
	}
}

func TestInsertRunReturnsMetricsMarshalError(t *testing.T) {
	db := newTestDB(t)
	_, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"bad": math.Inf(1)},
	}, 10)
	if err == nil {
		t.Fatal("InsertRun error = nil, want metrics marshal error")
	}
}

func TestRecentRunsReturnsCorruptJSONError(t *testing.T) {
	db := newTestDB(t)
	runID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"used_pct": 70.0},
	}, 100)
	if err != nil {
		t.Fatalf("InsertRun returned error: %v", err)
	}
	if _, err := db.sql.Exec(`UPDATE runs SET metrics = ? WHERE id = ?`, "{bad json", runID); err != nil {
		t.Fatalf("corrupt metrics update returned error: %v", err)
	}

	_, err = db.RecentRuns("disk", 1)
	if err == nil {
		t.Fatal("RecentRuns error = nil, want corrupt JSON error")
	}
	if !strings.Contains(err.Error(), "decode run") || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("RecentRuns error = %v", err)
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

func TestActiveIncidentsReturnsOpenIncidentsOldestFirst(t *testing.T) {
	db := newTestDB(t)

	if err := db.CreateIncident("inc-second", "memory", "suspicious", "Memory high", "evidence", 2, 2); err != nil {
		t.Fatalf("CreateIncident second returned error: %v", err)
	}
	if err := db.CreateIncident("inc-first", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident first returned error: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour).UTC()
	if _, err := db.sql.Exec(`UPDATE incidents SET created_at = ? WHERE id = ?`, oldTime, "inc-first"); err != nil {
		t.Fatalf("update incident time returned error: %v", err)
	}
	if err := db.ResolveIncident("inc-second", "done"); err != nil {
		t.Fatalf("ResolveIncident returned error: %v", err)
	}

	incidents, err := db.ActiveIncidents()
	if err != nil {
		t.Fatalf("ActiveIncidents returned error: %v", err)
	}
	if len(incidents) != 1 || incidents[0].ID != "inc-first" {
		t.Fatalf("expected only inc-first active, got %+v", incidents)
	}
}

func TestNotesCanBeListedAndDeleted(t *testing.T) {
	db := newTestDB(t)

	if err := db.AddNote("disk", "first"); err != nil {
		t.Fatalf("AddNote first returned error: %v", err)
	}
	if err := db.AddNote("disk", "second"); err != nil {
		t.Fatalf("AddNote second returned error: %v", err)
	}
	if err := db.AddNote("memory", "other"); err != nil {
		t.Fatalf("AddNote other returned error: %v", err)
	}

	records, err := db.NoteRecords("disk")
	if err != nil {
		t.Fatalf("NoteRecords returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records len = %d, want 2: %+v", len(records), records)
	}
	if records[0].Content != "first" || records[1].Content != "second" {
		t.Fatalf("records = %+v", records)
	}

	contents, err := db.Notes("disk")
	if err != nil {
		t.Fatalf("Notes returned error: %v", err)
	}
	if len(contents) != 2 || contents[0] != "first" || contents[1] != "second" {
		t.Fatalf("contents = %+v", contents)
	}

	if err := db.DeleteNote(records[0].ID); err != nil {
		t.Fatalf("DeleteNote returned error: %v", err)
	}
	records, err = db.NoteRecords("disk")
	if err != nil {
		t.Fatalf("second NoteRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].Content != "second" {
		t.Fatalf("records after delete = %+v", records)
	}
	if err := db.DeleteNote(records[0].ID + 1000); err != sql.ErrNoRows {
		t.Fatalf("DeleteNote missing error = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteOldRunsIgnoresNullIncidentRunReferences(t *testing.T) {
	db := newTestDB(t)
	oldRunID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:  "disk",
		Status: models.StatusOK,
	}, 10)
	if err != nil {
		t.Fatalf("InsertRun old returned error: %v", err)
	}
	protectedRunID, err := db.InsertRun("disk", &models.CollectorOutput{
		Check:  "disk",
		Status: models.StatusOK,
	}, 10)
	if err != nil {
		t.Fatalf("InsertRun protected returned error: %v", err)
	}

	oldTime := time.Now().Add(-48 * time.Hour).UTC()
	if _, err := db.sql.Exec(`UPDATE runs SET created_at = ? WHERE id IN (?, ?)`, oldTime, oldRunID, protectedRunID); err != nil {
		t.Fatalf("update run times returned error: %v", err)
	}
	if _, err := db.sql.Exec(
		`INSERT INTO incidents (id, check_id, status, severity, summary, created_at)
		 VALUES (?, ?, 'active', ?, ?, ?)`,
		"inc-null", "disk", "urgent", "incident with null run refs", oldTime,
	); err != nil {
		t.Fatalf("insert null incident returned error: %v", err)
	}
	if err := db.CreateIncident("inc-protected", "disk", "urgent", "protected", "evidence", protectedRunID, protectedRunID); err != nil {
		t.Fatalf("CreateIncident protected returned error: %v", err)
	}

	deleted, err := db.DeleteOldRuns(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteOldRuns returned error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one deleted run, got %d", deleted)
	}
	if runExists(t, db, oldRunID) {
		t.Fatalf("expected old unreferenced run %d to be deleted", oldRunID)
	}
	if !runExists(t, db, protectedRunID) {
		t.Fatalf("expected active incident run %d to be preserved", protectedRunID)
	}
}

func runExists(t *testing.T, db *DB, id int64) bool {
	t.Helper()

	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("count run %d returned error: %v", id, err)
	}
	return count > 0
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
