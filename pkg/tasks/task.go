package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound = errors.New("tasks: not found")
	ErrInvalid  = errors.New("tasks: invalid")
)

// Task is one durable, scheduler-owned task definition and queue cursor.
type Task struct {
	ID             string
	Kind           string
	Payload        []byte
	Schedule       Schedule
	Group          string
	Timeout        time.Duration
	MaxAttempts    int
	Metadata       map[string]string
	NextRunAt      *time.Time
	LockedAt       *time.Time
	LockToken      string
	Attempts       int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastStartedAt  *time.Time
	LastFinishedAt *time.Time
	LastError      string
}

func (t Task) Exhausted() bool {
	return t.NextRunAt == nil && t.LastError != "" && t.Attempts >= t.MaxAttempts
}

// Enqueue describes a task to persist. If ID is empty, Queue generates one.
type Enqueue struct {
	ID          string
	Kind        string
	Payload     []byte
	Schedule    Schedule
	Group       string
	Timeout     time.Duration
	MaxAttempts int
	Metadata    map[string]string
}

// Handler executes a claimed task.
type Handler interface {
	HandleTask(ctx context.Context, task Task) error
}

type HandlerFunc func(ctx context.Context, task Task) error

func (f HandlerFunc) HandleTask(ctx context.Context, task Task) error {
	return f(ctx, task)
}

type Finish struct {
	ID                string
	LockToken         string
	Error             string
	FinishedAt        time.Time
	NextRunAt         *time.Time
	ResetAttempts     bool
	IncrementAttempts bool
}

type Failure struct {
	Task      Task
	Err       error
	Exhausted bool
}

func JSONPayload(v any) ([]byte, error) {
	return json.Marshal(v)
}

func DecodeJSON[T any](task Task) (T, error) {
	var out T
	if err := json.Unmarshal(task.Payload, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (t Task) Validate() error {
	return t.validate()
}

func (t Task) clone() Task {
	t.Payload = cloneBytes(t.Payload)
	t.Metadata = cloneMetadata(t.Metadata)
	t.NextRunAt = cloneTimePtr(t.NextRunAt)
	t.LockedAt = cloneTimePtr(t.LockedAt)
	t.LastStartedAt = cloneTimePtr(t.LastStartedAt)
	t.LastFinishedAt = cloneTimePtr(t.LastFinishedAt)
	return t
}

func (t Task) validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: task ID is required", ErrInvalid)
	}
	if t.Kind == "" {
		return fmt.Errorf("%w: task kind is required", ErrInvalid)
	}
	if err := t.Schedule.Validate(); err != nil {
		return fmt.Errorf("%w: schedule: %v", ErrInvalid, err)
	}
	if t.Timeout < 0 {
		return fmt.Errorf("%w: timeout cannot be negative", ErrInvalid)
	}
	if t.MaxAttempts <= 0 {
		return fmt.Errorf("%w: max attempts must be positive", ErrInvalid)
	}
	if t.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created time is required", ErrInvalid)
	}
	if t.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: updated time is required", ErrInvalid)
	}
	return nil
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

func normalizePayload(in []byte) []byte {
	if len(in) == 0 {
		return []byte("null")
	}
	return cloneBytes(in)
}

func cloneMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := t.UTC()
	return &out
}
