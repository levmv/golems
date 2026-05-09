package incidents

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/hugin/internal/analysis"
	"github.com/levmv/golems/hugin/internal/storage"
)

// Manager tracks incident lifecycle: creation, updates, cooldowns, resolution.
type Manager struct {
	db *storage.DB
}

func New(db *storage.DB) *Manager {
	return &Manager{db: db}
}

// Event describes what happened after processing an analysis result.
type Event struct {
	Type     EventType
	CheckID  string
	Incident *storage.IncidentRecord
}

type EventType string

const (
	EventNone     EventType = "none"
	EventCreated  EventType = "created"
	EventUpdated  EventType = "updated"
	EventResolved EventType = "resolved"
)

// Process evaluates an analysis result against the current incident state.
// It handles creation, cooldown, and resolution.
func (m *Manager) Process(checkID string, result *analysis.Result, runID int64, cooldown, repeatAfter time.Duration) (Event, error) {
	now := time.Now().UTC()
	active, err := m.db.ActiveIncident(checkID)
	if err != nil {
		return Event{}, fmt.Errorf("failed to check active incident: %w", err)
	}

	// Case 1: Situation is normal — no alert, resolve any active incident
	if result.Severity == analysis.SeverityNormal {
		if active != nil {
			if err := m.db.ResolveIncident(active.ID, "Auto-resolved: situation returned to normal"); err != nil {
				return Event{}, fmt.Errorf("failed to resolve incident: %w", err)
			}
			active.Status = "resolved"
			return Event{Type: EventResolved, CheckID: checkID, Incident: active}, nil
		}
		return Event{Type: EventNone, CheckID: checkID}, nil
	}

	// Case 2: Abnormal but no active incident. Only open a new incident when the
	// analyst explicitly asks to alert.
	if active == nil {
		if !result.ShouldAlert {
			return Event{Type: EventNone, CheckID: checkID}, nil
		}
		id := fmt.Sprintf("inc-%s-%d", checkID, now.Unix())
		evidence := incidentEvidence(result)
		if err := m.db.CreateIncident(id, checkID, string(result.Severity), result.Summary, evidence, runID, runID); err != nil {
			return Event{}, fmt.Errorf("failed to create incident: %w", err)
		}
		firstRunID := runID
		lastRunID := runID
		return Event{
			Type:    EventCreated,
			CheckID: checkID,
			Incident: &storage.IncidentRecord{
				ID:         id,
				CheckID:    checkID,
				Status:     "active",
				Severity:   string(result.Severity),
				Summary:    result.Summary,
				Evidence:   evidence,
				FirstRunID: &firstRunID,
				LastRunID:  &lastRunID,
				CreatedAt:  now,
			},
		}, nil
	}

	// Case 3: Active incident exists. Keep its latest run fresh even if the
	// analyst decides the continuing abnormal state does not warrant a new alert.
	severity := string(result.Severity)
	evidence := incidentEvidence(result)
	escalated := severityRank(severity) > severityRank(active.Severity)
	if err := m.db.UpdateIncident(active.ID, severity, result.Summary, evidence, runID); err != nil {
		return Event{}, fmt.Errorf("failed to update incident: %w", err)
	}
	active.Severity = severity
	active.Summary = result.Summary
	active.Evidence = evidence
	active.LastRunID = &runID

	if result.ShouldAlert && escalated {
		return Event{Type: EventUpdated, CheckID: checkID, Incident: active}, nil
	}

	if shouldNotifyActive(now, active.LastNotifiedAt, cooldown, repeatAfter) {
		return Event{Type: EventUpdated, CheckID: checkID, Incident: active}, nil
	}

	return Event{Type: EventNone, CheckID: checkID, Incident: active}, nil
}

func shouldNotifyActive(now time.Time, lastNotifiedAt *time.Time, cooldown, repeatAfter time.Duration) bool {
	if lastNotifiedAt == nil {
		return true
	}

	sinceNotification := now.Sub(*lastNotifiedAt)
	if cooldown > 0 && sinceNotification < cooldown {
		return false
	}
	if repeatAfter > 0 && sinceNotification >= repeatAfter {
		return true
	}
	return false
}

func incidentEvidence(result *analysis.Result) string {
	return truncate(result.Summary+"\n"+result.Evidence, 2000)
}

func severityRank(severity string) int {
	switch analysis.Severity(severity) {
	case analysis.SeverityUrgent:
		return 3
	case analysis.SeveritySuspicious:
		return 2
	case analysis.SeverityNormal:
		return 1
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return "..."
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
