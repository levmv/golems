package incidents

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/levmv/golems/hugin/internal/analysis"
	"github.com/levmv/golems/hugin/internal/storage"
)

func TestProcessUsesLastNotifiedAtForRepeatNotifications(t *testing.T) {
	db := newTestDB(t)
	manager := New(db)
	now := time.Now().UTC()

	if err := db.CreateIncident("inc-disk-1", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	if err := db.MarkIncidentNotified("inc-disk-1", now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("MarkIncidentNotified returned error: %v", err)
	}

	event, err := manager.Process("disk", urgentResult(), 2, 10*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if event.Type != EventNone {
		t.Fatalf("expected repeat notification to be suppressed, got %+v", event)
	}

	if err := db.MarkIncidentNotified("inc-disk-1", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("second MarkIncidentNotified returned error: %v", err)
	}
	event, err = manager.Process("disk", urgentResult(), 3, 10*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("second Process returned error: %v", err)
	}
	if event.Type != EventUpdated {
		t.Fatalf("expected repeat notification update, got %+v", event)
	}
}

func TestProcessRetriesNotificationWhenIncidentWasNeverNotified(t *testing.T) {
	db := newTestDB(t)
	manager := New(db)

	if err := db.CreateIncident("inc-disk-1", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	event, err := manager.Process("disk", urgentResult(), 2, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if event.Type != EventUpdated {
		t.Fatalf("expected update for never-notified incident, got %+v", event)
	}
}

func urgentResult() *analysis.Result {
	return &analysis.Result{
		Severity:    analysis.SeverityUrgent,
		ShouldAlert: true,
		Summary:     "Disk full",
		Evidence:    "free space is very low",
	}
}

func newTestDB(t *testing.T) *storage.DB {
	t.Helper()

	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})
	return db
}
