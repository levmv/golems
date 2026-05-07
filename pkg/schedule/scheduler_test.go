package schedule

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDueFirstRunIsImmediate(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{
			ID:         "job-1",
			Kind:       "test",
			Ref:        "one",
			Trigger:    Every(time.Hour),
			InitialRun: true,
		},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	if !jobs[0].DueAt.Equal(now) {
		t.Fatalf("expected due time %s, got %s", now, jobs[0].DueAt)
	}
}

func TestFutureAtTriggerIsNotDueImmediately(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{
			ID:      "reminder-1",
			Kind:    "assistant.reminder",
			Ref:     "rem-1",
			Trigger: At(now.Add(2 * time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no due jobs, got %d", len(jobs))
	}
}

func TestPastAtTriggerIsDueOnFirstRun(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{
			ID:      "reminder-1",
			Kind:    "assistant.reminder",
			Ref:     "rem-1",
			Trigger: At(now.Add(-2 * time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	if !jobs[0].DueAt.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("expected reminder due time to be preserved, got %s", jobs[0].DueAt)
	}
}

func TestRecurringFirstRunWaitsForInitialDueTime(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{
			ID:      "job-1",
			Kind:    "test",
			Ref:     "one",
			Trigger: Every(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no due jobs, got %d", len(jobs))
	}
}

func TestDueRejectsDuplicateJobIDs(t *testing.T) {
	store := NewMemoryStore()
	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{})

	_, err := s.Due(context.Background(), []JobSpec{
		{ID: "job-1", Trigger: Every(time.Hour), InitialRun: true},
		{ID: "job-1", Trigger: Every(time.Hour), InitialRun: true},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for duplicate job ID, got %v", err)
	}
}

func TestRunDueSerializesByGroup(t *testing.T) {
	store := NewMemoryStore()
	started := make(chan string, 2)
	release := make(chan struct{})

	var mu sync.Mutex
	activeByGroup := map[string]int{}
	maxActiveByGroup := map[string]int{}

	runner := RunnerFunc(func(ctx context.Context, job Job) error {
		mu.Lock()
		activeByGroup[job.Spec.Group]++
		if activeByGroup[job.Spec.Group] > maxActiveByGroup[job.Spec.Group] {
			maxActiveByGroup[job.Spec.Group] = activeByGroup[job.Spec.Group]
		}
		mu.Unlock()

		started <- job.Spec.ID

		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}

		mu.Lock()
		activeByGroup[job.Spec.Group]--
		mu.Unlock()
		return nil
	})

	s := New(store, Chain(runner, GroupConcurrency(1)), Options{
		MaxConcurrent: 2,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.RunDue(context.Background(), []JobSpec{
			{ID: "job-1", Kind: "test", Ref: "one", Group: "same", Trigger: Every(time.Hour), InitialRun: true},
			{ID: "job-2", Kind: "test", Ref: "two", Group: "same", Trigger: Every(time.Hour), InitialRun: true},
		})
	}()

	<-started

	select {
	case id := <-started:
		t.Fatalf("second same-group job started before first finished: %s", id)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	if err := <-errCh; err != nil {
		t.Fatalf("RunDue returned error: %v", err)
	}

	if maxActiveByGroup["same"] != 1 {
		t.Fatalf("expected max active in group to be 1, got %d", maxActiveByGroup["same"])
	}
}

func TestGroupConcurrencyDoesNotLimitEmptyGroup(t *testing.T) {
	store := NewMemoryStore()
	started := make(chan string, 2)
	release := make(chan struct{})

	var mu sync.Mutex
	active := 0
	maxActive := 0

	runner := RunnerFunc(func(ctx context.Context, job Job) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		started <- job.Spec.ID

		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}

		mu.Lock()
		active--
		mu.Unlock()
		return nil
	})

	s := New(store, Chain(runner, GroupConcurrency(1)), Options{
		MaxConcurrent: 2,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.RunDue(context.Background(), []JobSpec{
			{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
			{ID: "job-2", Kind: "test", Ref: "two", Trigger: Every(time.Hour), InitialRun: true},
		})
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("expected both empty-group jobs to start")
		}
	}

	close(release)

	if err := <-errCh; err != nil {
		t.Fatalf("RunDue returned error: %v", err)
	}

	if maxActive != 2 {
		t.Fatalf("expected empty-group jobs to run concurrently, max active was %d", maxActive)
	}
}

func TestRunLoopRunsImmediatelyUntilContextCancelled(t *testing.T) {
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	var loads atomic.Int64
	var runs atomic.Int64

	runner := RunnerFunc(func(ctx context.Context, job Job) error {
		runs.Add(1)
		cancel()
		return nil
	})

	s := New(store, runner, Options{})
	err := s.RunLoop(ctx, time.Hour, func(ctx context.Context) ([]JobSpec, error) {
		loads.Add(1)
		return []JobSpec{
			{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
		}, nil
	}, nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("expected 1 load, got %d", loads.Load())
	}
	if runs.Load() != 1 {
		t.Fatalf("expected 1 run, got %d", runs.Load())
	}
}

func TestRunRecordsFailureAfterContextCancellation(t *testing.T) {
	store := &contextCheckingStore{MemoryStore: NewMemoryStore()}
	ctx, cancel := context.WithCancel(context.Background())

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		cancel()
		return ctx.Err()
	}), Options{})

	err := s.RunDue(ctx, []JobSpec{
		{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from runner, got %v", err)
	}
	if store.finishSawCanceled.Load() {
		t.Fatal("FinishRun received a cancelled context")
	}

	last, err := store.LastRun(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("LastRun returned error: %v", err)
	}
	if last.Status != RunFailed {
		t.Fatalf("expected failed status, got %s", last.Status)
	}
}

func TestInitialRunDoesNotExecuteStaleInitialJobTwice(t *testing.T) {
	store := NewMemoryStore()
	now1 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	now2 := now1.Add(time.Second)
	var runs atomic.Int64

	runner := RunnerFunc(func(ctx context.Context, job Job) error {
		runs.Add(1)
		return nil
	})

	s1 := New(store, runner, Options{Now: func() time.Time { return now1 }})
	s2 := New(store, runner, Options{Now: func() time.Time { return now2 }})
	specs := []JobSpec{
		{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
	}

	jobs1, err := s1.Due(context.Background(), specs)
	if err != nil {
		t.Fatalf("Due 1 returned error: %v", err)
	}
	jobs2, err := s2.Due(context.Background(), specs)
	if err != nil {
		t.Fatalf("Due 2 returned error: %v", err)
	}

	if err := s1.RunJobs(context.Background(), jobs1); err != nil {
		t.Fatalf("RunJobs 1 returned error: %v", err)
	}
	if err := s2.RunJobs(context.Background(), jobs2); err != nil {
		t.Fatalf("RunJobs 2 returned error: %v", err)
	}

	if got := runs.Load(); got != 1 {
		t.Fatalf("expected 1 run, got %d", got)
	}
}

func TestStaleDueJobIsSkippedAfterAnotherSchedulerRunsIt(t *testing.T) {
	store := NewMemoryStore()
	lastDue := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 6, 12, 6, 0, 0, time.UTC)
	var runs atomic.Int64

	mustClaimRun(t, store, RunRecord{
		JobID:         "job-1",
		OccurrenceKey: "seed",
		DueAt:         lastDue,
		StartedAt:     lastDue,
		Status:        RunSucceeded,
	})

	runner := RunnerFunc(func(ctx context.Context, job Job) error {
		runs.Add(1)
		return nil
	})
	s := New(store, runner, Options{Now: func() time.Time { return now }})
	specs := []JobSpec{{ID: "job-1", Trigger: Every(5 * time.Minute)}}

	jobs1, err := s.Due(context.Background(), specs)
	if err != nil {
		t.Fatalf("Due 1 returned error: %v", err)
	}
	jobs2, err := s.Due(context.Background(), specs)
	if err != nil {
		t.Fatalf("Due 2 returned error: %v", err)
	}

	if err := s.RunJobs(context.Background(), jobs1); err != nil {
		t.Fatalf("RunJobs 1 returned error: %v", err)
	}
	if err := s.RunJobs(context.Background(), jobs2); err != nil {
		t.Fatalf("RunJobs 2 returned error: %v", err)
	}

	if got := runs.Load(); got != 1 {
		t.Fatalf("expected 1 run, got %d", got)
	}
}

func TestRunnerPanicIsRecordedAsFailedRun(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		panic("boom")
	}), Options{Now: func() time.Time { return now }})

	err := s.RunDue(context.Background(), []JobSpec{
		{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
	})
	if err == nil {
		t.Fatal("expected panic to be returned as an error")
	}

	last, err := store.LastRun(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("LastRun returned error: %v", err)
	}
	if last.Status != RunFailed {
		t.Fatalf("expected failed status, got %s", last.Status)
	}
	if last.Error == "" {
		t.Fatal("expected panic message in run error")
	}
}

func TestRunnerErrorIsRecordedAsFailedRun(t *testing.T) {
	store := NewMemoryStore()
	wantErr := errors.New("runner failed")
	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return wantErr
	}), Options{})

	err := s.RunDue(context.Background(), []JobSpec{
		{ID: "job-1", Kind: "test", Ref: "one", Trigger: Every(time.Hour), InitialRun: true},
	})
	if err == nil {
		t.Fatal("expected runner error")
	}

	last, err := store.LastRun(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("LastRun returned error: %v", err)
	}
	if last.Status != RunFailed {
		t.Fatalf("expected failed status, got %s", last.Status)
	}
}

func TestMemoryStoreLatestRunUsesNumericRunOrder(t *testing.T) {
	store := NewMemoryStore()
	dueAt := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		mustClaimRun(t, store, RunRecord{
			JobID:         "job-1",
			OccurrenceKey: fmt.Sprintf("occ-%d", i),
			DueAt:         dueAt,
			StartedAt:     dueAt,
			Status:        RunSucceeded,
		})
	}

	last, err := store.LastRun(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("LastRun returned error: %v", err)
	}
	if last.ID != "run-10" {
		t.Fatalf("expected run 10 to be latest, got %s", last.ID)
	}
}

func TestAtTriggerRunsOnce(t *testing.T) {
	at := time.Date(2026, 5, 6, 18, 30, 0, 0, time.UTC)
	trigger := At(at)

	next, ok := trigger.Next(time.Time{})
	if !ok {
		t.Fatal("expected first next time")
	}
	if !next.Equal(at) {
		t.Fatalf("expected %s, got %s", at, next)
	}

	if _, ok := trigger.Next(at); ok {
		t.Fatal("expected no next time after one-shot trigger has passed")
	}
}

func TestCronStepUsesFieldMinimum(t *testing.T) {
	next, ok := Cron("0 0 */15 * *").Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("expected next cron time")
	}
	want := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected day-of-month step to use field minimum: want %s, got %s", want, next)
	}

	next, ok = Cron("0 0 1 */3 *").Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("expected next cron time")
	}
	want = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected month step to use field minimum: want %s, got %s", want, next)
	}
}

func TestValidateRejectsInvalidTrigger(t *testing.T) {
	spec := JobSpec{
		ID:      "bad-cron",
		Trigger: Cron("not cron"),
	}

	if err := spec.Validate(); err == nil {
		t.Fatal("expected invalid cron to fail validation")
	}
}

func TestIntervalAnchorsToLastDueAt(t *testing.T) {
	store := NewMemoryStore()
	lastDue := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 6, 12, 6, 0, 0, time.UTC)

	mustClaimRun(t, store, RunRecord{
		JobID:         "job-1",
		OccurrenceKey: "seed",
		Kind:          "test",
		Ref:           "one",
		DueAt:         lastDue,
		StartedAt:     lastDue.Add(4 * time.Minute),
		Status:        RunSucceeded,
	})

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{
			ID:      "job-1",
			Kind:    "test",
			Ref:     "one",
			Trigger: Every(5 * time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	want := lastDue.Add(5 * time.Minute)
	if !jobs[0].DueAt.Equal(want) {
		t.Fatalf("expected next due %s, got %s", want, jobs[0].DueAt)
	}
}

func TestDueReturnsLatestMissedOccurrence(t *testing.T) {
	store := NewMemoryStore()
	lastDue := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 6, 12, 16, 0, 0, time.UTC)

	mustClaimRun(t, store, RunRecord{
		JobID:         "job-1",
		OccurrenceKey: "seed",
		DueAt:         lastDue,
		StartedAt:     lastDue,
		Status:        RunSucceeded,
	})

	s := New(store, RunnerFunc(func(ctx context.Context, job Job) error {
		return nil
	}), Options{Now: func() time.Time { return now }})

	jobs, err := s.Due(context.Background(), []JobSpec{
		{ID: "job-1", Trigger: Every(5 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("Due returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	want := time.Date(2026, 5, 6, 12, 15, 0, 0, time.UTC)
	if !jobs[0].DueAt.Equal(want) {
		t.Fatalf("expected latest due %s, got %s", want, jobs[0].DueAt)
	}
}

func mustClaimRun(t *testing.T, store *MemoryStore, record RunRecord) RunRecord {
	t.Helper()

	run, claimed, err := store.TryCreateRun(context.Background(), record)
	if err != nil {
		t.Fatalf("TryCreateRun returned error: %v", err)
	}
	if !claimed {
		t.Fatalf("TryCreateRun did not claim run for job %q occurrence %q", record.JobID, record.OccurrenceKey)
	}
	return run
}

type contextCheckingStore struct {
	*MemoryStore
	finishSawCanceled atomic.Bool
}

func (s *contextCheckingStore) FinishRun(ctx context.Context, runID string, status RunStatus, message string, finishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		s.finishSawCanceled.Store(true)
		return err
	}
	return s.MemoryStore.FinishRun(ctx, runID, status, message, finishedAt)
}
