package incidents

import (
	"fmt"
	"time"

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

	// Case 2: Severity is suspicious/urgent but should_alert is false —
	// don't resolve, just suppress re-notification (already covered by active incident)
	if !result.ShouldAlert {
		return Event{Type: EventNone, CheckID: checkID, Incident: active}, nil
	}

	// Case 3: Should alert — create or update incident

	// If no active incident, create one
	if active == nil {
		id := fmt.Sprintf("inc-%s-%d", checkID, time.Now().Unix())
		evidence := truncate(result.Summary+"\n"+result.Evidence, 2000)
		if err := m.db.CreateIncident(id, checkID, string(result.Severity), result.Summary, evidence, runID, runID); err != nil {
			return Event{}, fmt.Errorf("failed to create incident: %w", err)
		}
		return Event{
			Type:     EventCreated,
			CheckID:  checkID,
			Incident: &storage.IncidentRecord{ID: id, CheckID: checkID, Status: "active", Severity: string(result.Severity), Summary: result.Summary},
		}, nil
	}

	// Active incident exists — update it
	if err := m.db.UpdateIncidentRun(active.ID, runID); err != nil {
		return Event{}, fmt.Errorf("failed to update incident: %w", err)
	}

	// Check cooldown — don't re-notify if within cooldown
	if time.Since(active.CreatedAt) < cooldown {
		return Event{Type: EventNone, CheckID: checkID, Incident: active}, nil
	}

	// Check repeat interval
	if repeatAfter > 0 && time.Since(active.CreatedAt) >= repeatAfter {
		return Event{Type: EventUpdated, CheckID: checkID, Incident: active}, nil
	}

	return Event{Type: EventNone, CheckID: checkID, Incident: active}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
