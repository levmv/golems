package schedule

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type MemoryStore struct {
	mu     sync.Mutex
	nextID int64
	runs   map[string]RunRecord
	claims map[string]string
	seq    map[string]int64
	latest map[string]RunRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		runs:   make(map[string]RunRecord),
		claims: make(map[string]string),
		seq:    make(map[string]int64),
		latest: make(map[string]RunRecord),
	}
}

func (s *MemoryStore) LastRun(ctx context.Context, jobID string) (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.latest[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	return &rec, nil
}

func (s *MemoryStore) TryCreateRun(ctx context.Context, record RunRecord) (RunRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := claimKey(record.JobID, record.OccurrenceKey)
	if _, ok := s.claims[key]; ok {
		return RunRecord{}, false, nil
	}

	s.nextID++
	record.ID = fmt.Sprintf("run-%d", s.nextID)
	s.runs[record.ID] = record
	s.claims[key] = record.ID
	s.seq[record.ID] = s.nextID
	s.updateLatestLocked(record)
	return record, true, nil
}

func (s *MemoryStore) LastRuns(ctx context.Context, jobIDs []string) (map[string]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]RunRecord, len(jobIDs))
	for _, id := range jobIDs {
		if rec, ok := s.latest[id]; ok {
			result[id] = rec
		}
	}
	return result, nil
}

func (s *MemoryStore) FinishRun(ctx context.Context, runID string, status RunStatus, message string, finishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.runs[runID]
	if !ok {
		return ErrNotFound
	}
	record.Status = status
	record.Error = message
	record.FinishedAt = &finishedAt
	s.runs[runID] = record
	s.updateLatestLocked(record)
	return nil
}

func (s *MemoryStore) updateLatestLocked(record RunRecord) {
	latest, ok := s.latest[record.JobID]
	if !ok || s.latestBeforeLocked(latest, record) {
		s.latest[record.JobID] = record
		return
	}
	if latest.ID == record.ID {
		s.latest[record.JobID] = record
	}
}

func (s *MemoryStore) latestBeforeLocked(a, b RunRecord) bool {
	if !a.DueAt.Equal(b.DueAt) {
		return a.DueAt.Before(b.DueAt)
	}
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.Before(b.StartedAt)
	}
	return s.seq[a.ID] < s.seq[b.ID]
}

func claimKey(jobID, occurrenceKey string) string {
	return jobID + "\x00" + occurrenceKey
}
