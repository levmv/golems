package tasks

import (
	"context"
	"time"
)

// Store persists task definitions, queue cursors, leases, and last outcomes.
type Store interface {
	Enqueue(ctx context.Context, task Task) error
	Get(ctx context.Context, id string) (Task, error)
	Delete(ctx context.Context, id string) (bool, error)
	DeleteClaimed(ctx context.Context, id string, lockToken string) (bool, error)
	Reschedule(ctx context.Context, id string, schedule Schedule, nextRunAt time.Time, updatedAt time.Time) (Task, bool, error)
	ClaimDue(ctx context.Context, now time.Time, leaseDuration time.Duration, limit int, token string) ([]Task, error)
	Finish(ctx context.Context, finish Finish) (bool, error)
	NextRunAt(ctx context.Context, now time.Time, leaseDuration time.Duration) (*time.Time, error)
}
