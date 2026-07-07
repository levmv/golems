package engine

import (
	"context"
	"errors"
	"time"

	"github.com/levmv/golems/pkg/tasks"
)

// KindSubagentPrune is the task kind for the periodic subagent-history cleanup.
const KindSubagentPrune = "subagent.prune"

const (
	// subagentPruneTaskID is the fixed id of the single recurring prune task.
	subagentPruneTaskID = "subagent-prune"
	// subagentPruneCron runs the prune once a day, inside the server's uptime
	// window (~09:00–01:00), so it runs that day rather than waiting for the next
	// boot. Timing is not load-bearing — pruning is idempotent and catch-up-safe.
	subagentPruneCron = "30 12 * * *"
	// subagentRetention is how long a delegated child conversation's transcript is
	// kept (for debugging a delegation) before it is pruned. The durable output —
	// the final text — already lives in the parent's tool result, so the child is
	// disposable; this window only preserves post-hoc inspection.
	subagentRetention = 7 * 24 * time.Hour
)

// ensureSubagentPruneSchedule registers the single recurring subagent-prune task.
// Unlike reflection/free-time it is pure housekeeping with no feature gate, so it
// is always scheduled when a queue exists. Idempotent; clears an exhausted prior
// task (mirrors ensureReflectionSchedule).
func (e *Engine) ensureSubagentPruneSchedule(ctx context.Context) {
	if e.tasks == nil {
		return
	}
	existing, err := e.tasks.Get(ctx, subagentPruneTaskID)
	switch {
	case err == nil:
		if !existing.Exhausted() {
			return
		}
		if _, derr := e.tasks.Delete(ctx, subagentPruneTaskID); derr != nil {
			e.logf(warnLevel, "delete exhausted subagent-prune task: %v", derr)
			return
		}
	case !errors.Is(err, tasks.ErrNotFound):
		e.logf(warnLevel, "subagent-prune schedule check: %v", err)
		return
	}
	if _, err := e.tasks.Enqueue(ctx, tasks.Enqueue{
		ID:          subagentPruneTaskID,
		Kind:        KindSubagentPrune,
		Schedule:    tasks.Cron(subagentPruneCron, e.loc.String()),
		Group:       TaskGroup,
		MaxAttempts: 1,
	}); err != nil {
		e.logf(warnLevel, "enqueue subagent-prune: %v", err)
		return
	}
	e.logf(infoLevel, "scheduled daily subagent-history prune (cron %q tz %s, keep %s)",
		subagentPruneCron, e.loc.String(), subagentRetention)
}

// HandleSubagentPrune deletes delegated child conversations older than the
// retention window. Failures are logged and swallowed (return nil): the recurring
// cron must not be killed by an exhausted attempt (see ensureReflectionSchedule),
// and a transient prune failure simply retries tomorrow.
func (e *Engine) HandleSubagentPrune(ctx context.Context, _ tasks.Task) error {
	cutoff := time.Now().Add(-subagentRetention)
	n, err := e.store.PruneChildConversations(ctx, cutoff)
	if err != nil {
		e.logf(warnLevel, "subagent-prune: %v", err)
		return nil
	}
	if n > 0 {
		e.logf(infoLevel, "subagent-prune: removed %d child conversation(s) older than %s", n, subagentRetention)
	}
	return nil
}
