package tasks

import (
	"context"
	"time"
)

// ListFilter narrows a List query. Empty fields match everything.
type ListFilter struct {
	Kind  string // optional, exact match
	Group string // optional, exact match
}

// Store persists task definitions, queue cursors, leases, and last outcomes.
type Store interface {
	Enqueue(ctx context.Context, task Task) error
	Get(ctx context.Context, id string) (Task, error)
	// List returns tasks matching filter, ordered by next_run_at ascending with
	// exhausted (NULL next_run_at) tasks last, ties broken by ID.
	List(ctx context.Context, filter ListFilter) ([]Task, error)
	Delete(ctx context.Context, id string) (bool, error)
	DeleteClaimed(ctx context.Context, id string, lockToken string) (bool, error)
	Reschedule(ctx context.Context, id string, schedule Schedule, nextRunAt time.Time, updatedAt time.Time) (Task, bool, error)
	ClaimDue(ctx context.Context, now time.Time, leaseDuration time.Duration, limit int, token string) ([]Task, error)
	Finish(ctx context.Context, finish Finish) (bool, error)
	NextRunAt(ctx context.Context, now time.Time, leaseDuration time.Duration) (*time.Time, error)
}
