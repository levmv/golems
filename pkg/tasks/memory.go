package tasks

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

type MemoryStore struct {
	mu    sync.Mutex
	tasks map[string]Task
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tasks: make(map[string]Task)}
}

func (s *MemoryStore) Enqueue(ctx context.Context, task Task) error {
	if err := task.validate(); err != nil {
		return err
	}
	task.Payload = normalizePayload(task.Payload)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[task.ID]; ok {
		return fmt.Errorf("%w: task %q already exists", ErrInvalid, task.ID)
	}
	s.tasks[task.ID] = task.clone()
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return task.clone(), nil
}

func (s *MemoryStore) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Task, 0)
	for _, task := range s.tasks {
		if filter.Kind != "" && task.Kind != filter.Kind {
			continue
		}
		if filter.Group != "" && task.Group != filter.Group {
			continue
		}
		out = append(out, task.clone())
	}
	sortTasksByNextRun(out)
	return out, nil
}

func (s *MemoryStore) Delete(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.tasks[id]; !ok {
		return false, nil
	}
	delete(s.tasks, id)
	return true, nil
}

func (s *MemoryStore) Reschedule(ctx context.Context, id string, schedule Schedule, nextRunAt time.Time, updatedAt time.Time) (Task, bool, error) {
	if id == "" {
		return Task{}, false, fmt.Errorf("%w: task ID is required", ErrInvalid)
	}
	if nextRunAt.IsZero() {
		return Task{}, false, fmt.Errorf("%w: next run time is required", ErrInvalid)
	}
	if err := schedule.Validate(); err != nil {
		return Task{}, false, fmt.Errorf("%w: schedule: %v", ErrInvalid, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[id]
	if !ok {
		return Task{}, false, nil
	}
	next := nextRunAt.UTC()
	updatedAt = updatedAt.UTC()
	task.Schedule = schedule
	task.NextRunAt = &next
	task.LockedAt = nil
	task.LockToken = ""
	task.Attempts = 0
	task.UpdatedAt = updatedAt
	task.LastError = ""
	s.tasks[id] = task.clone()
	return task.clone(), true, nil
}

func (s *MemoryStore) ClaimDue(ctx context.Context, now time.Time, leaseDuration time.Duration, limit int, token string) ([]Task, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: lock token is required", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now = now.UTC()
	due := make([]Task, 0)
	for _, task := range s.tasks {
		if !claimable(task, now, leaseDuration) {
			continue
		}
		due = append(due, task.clone())
	}
	sortTasksByNextRun(due)
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}

	claimed := make([]Task, 0, len(due))
	for _, task := range due {
		task.LockedAt = &now
		task.LockToken = token
		task.LastStartedAt = &now
		task.LastFinishedAt = nil
		task.LastError = ""
		task.UpdatedAt = now
		s.tasks[task.ID] = task.clone()
		claimed = append(claimed, task.clone())
	}
	return claimed, nil
}

func (s *MemoryStore) Finish(ctx context.Context, finish Finish) (bool, error) {
	if finish.LockToken == "" {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[finish.ID]
	if !ok || task.LockToken == "" || task.LockToken != finish.LockToken {
		return false, nil
	}
	finishedAt := finish.FinishedAt.UTC()
	task.LockedAt = nil
	task.LockToken = ""
	task.LastFinishedAt = &finishedAt
	task.LastError = finish.Error
	task.NextRunAt = cloneTimePtr(finish.NextRunAt)
	task.UpdatedAt = finishedAt
	if finish.ResetAttempts {
		task.Attempts = 0
	} else if finish.IncrementAttempts {
		task.Attempts++
	}
	s.tasks[finish.ID] = task.clone()
	return true, nil
}

func (s *MemoryStore) NextRunAt(ctx context.Context, now time.Time, leaseDuration time.Duration) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now = now.UTC()
	var next *time.Time
	for _, task := range s.tasks {
		if task.NextRunAt == nil {
			continue
		}
		candidate := task.NextRunAt.UTC()
		if !candidate.After(now) && activeLock(task, now, leaseDuration) {
			candidate = task.LockedAt.Add(leaseDuration).UTC()
		}
		if next == nil || candidate.Before(*next) {
			next = &candidate
		}
	}
	return cloneTimePtr(next), nil
}

func claimable(task Task, now time.Time, leaseDuration time.Duration) bool {
	if task.NextRunAt == nil || task.NextRunAt.After(now) {
		return false
	}
	return !activeLock(task, now, leaseDuration)
}

func activeLock(task Task, now time.Time, leaseDuration time.Duration) bool {
	if task.LockToken == "" || task.LockedAt == nil {
		return false
	}
	if leaseDuration <= 0 {
		return true
	}
	return task.LockedAt.Add(leaseDuration).After(now)
}

func (s *MemoryStore) DeleteClaimed(ctx context.Context, id string, lockToken string) (bool, error) {
	if lockToken == "" {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[id]
	if !ok || task.LockToken == "" || task.LockToken != lockToken {
		return false, nil
	}
	delete(s.tasks, id)
	return true, nil
}

func sortTasksByNextRun(tasks []Task) {
	slices.SortStableFunc(tasks, compareTasksByNextRun)
}

func compareTasksByNextRun(left, right Task) int {
	switch {
	case left.NextRunAt == nil && right.NextRunAt == nil:
		return cmp.Compare(left.ID, right.ID)
	case left.NextRunAt == nil:
		return 1
	case right.NextRunAt == nil:
		return -1
	case left.NextRunAt.Before(*right.NextRunAt):
		return -1
	case right.NextRunAt.Before(*left.NextRunAt):
		return 1
	default:
		return cmp.Compare(left.ID, right.ID)
	}
}
