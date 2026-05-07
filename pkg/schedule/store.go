package schedule

import (
	"context"
	"time"
)

// Store persists scheduler coordination state.
type Store interface {
	LastRun(ctx context.Context, jobID string) (*RunRecord, error)
	LastRuns(ctx context.Context, jobIDs []string) (map[string]RunRecord, error)
	TryCreateRun(ctx context.Context, record RunRecord) (RunRecord, bool, error)
	FinishRun(ctx context.Context, runID string, status RunStatus, message string, finishedAt time.Time) error
}
