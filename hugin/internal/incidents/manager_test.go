package incidents

import (
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

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

func TestProcessRepeatsActiveIncidentWhenStillAbnormalWithoutFreshAlert(t *testing.T) {
	db := newTestDB(t)
	manager := New(db)
	now := time.Now().UTC()

	if err := db.CreateIncident("inc-disk-1", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	if err := db.MarkIncidentNotified("inc-disk-1", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("MarkIncidentNotified returned error: %v", err)
	}

	event, err := manager.Process("disk", abnormalNoAlertResult(), 2, 10*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if event.Type != EventUpdated {
		t.Fatalf("expected repeat update for active abnormal incident, got %+v", event)
	}
	if event.Incident == nil || event.Incident.LastRunID == nil || *event.Incident.LastRunID != 2 {
		t.Fatalf("expected last run to be updated in event, got %+v", event.Incident)
	}
}

func TestProcessSuppressesActiveAbnormalIncidentBeforeRepeatAfter(t *testing.T) {
	db := newTestDB(t)
	manager := New(db)
	now := time.Now().UTC()

	if err := db.CreateIncident("inc-disk-1", "disk", "urgent", "Disk full", "evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	if err := db.MarkIncidentNotified("inc-disk-1", now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("MarkIncidentNotified returned error: %v", err)
	}

	event, err := manager.Process("disk", abnormalNoAlertResult(), 2, 10*time.Minute, time.Hour)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if event.Type != EventNone {
		t.Fatalf("expected active abnormal incident to be suppressed before repeat_after, got %+v", event)
	}
}

func TestProcessEscalatesActiveIncidentDetailsImmediately(t *testing.T) {
	db := newTestDB(t)
	manager := New(db)
	now := time.Now().UTC()

	if err := db.CreateIncident("inc-disk-1", "disk", "suspicious", "Disk growing", "old evidence", 1, 1); err != nil {
		t.Fatalf("CreateIncident returned error: %v", err)
	}
	if err := db.MarkIncidentNotified("inc-disk-1", now.Add(-5*time.Minute)); err != nil {
		t.Fatalf("MarkIncidentNotified returned error: %v", err)
	}

	event, err := manager.Process("disk", urgentResult(), 2, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if event.Type != EventUpdated {
		t.Fatalf("expected immediate update for escalated incident, got %+v", event)
	}
	if event.Incident == nil {
		t.Fatal("expected updated incident in event")
	}
	if event.Incident.Severity != "urgent" || event.Incident.Summary != "Disk full" {
		t.Fatalf("expected updated event details, got %+v", event.Incident)
	}

	inc, err := db.ActiveIncident("disk")
	if err != nil {
		t.Fatalf("ActiveIncident returned error: %v", err)
	}
	if inc.Severity != "urgent" || inc.Summary != "Disk full" {
		t.Fatalf("expected persisted incident details to be updated, got %+v", inc)
	}
	if inc.LastRunID == nil || *inc.LastRunID != 2 {
		t.Fatalf("expected persisted last run id 2, got %+v", inc.LastRunID)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("абвгд", 5)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %q", got)
	}
	if got != "аб..." {
		t.Fatalf("expected truncate to cut at rune boundary, got %q", got)
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

func abnormalNoAlertResult() *analysis.Result {
	return &analysis.Result{
		Severity:    analysis.SeverityUrgent,
		ShouldAlert: false,
		Summary:     "Disk still full",
		Evidence:    "active incident already covers the low free space",
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
