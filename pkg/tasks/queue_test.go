package tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEnqueuePersistsPayloadAndJSONHelpers(t *testing.T) {
	now := testNow()
	store := NewMemoryStore()
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		Now: func() time.Time { return now },
	})

	payload, err := JSONPayload(struct {
		Message string `json:"message"`
	}{Message: "hello"})
	if err != nil {
		t.Fatalf("JSONPayload returned error: %v", err)
	}
	task, err := q.Enqueue(context.Background(), Enqueue{
		ID:       "task-1",
		Kind:     "assistant.reminder",
		Payload:  payload,
		Schedule: Once(now),
		Group:    "user:1",
		Metadata: map[string]string{"trace": "abc"},
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if task.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("unexpected enqueued task: %+v", task)
	}

	got, err := q.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	decoded, err := DecodeJSON[struct {
		Message string `json:"message"`
	}](got)
	if err != nil {
		t.Fatalf("DecodeJSON returned error: %v", err)
	}
	if decoded.Message != "hello" || got.Metadata["trace"] != "abc" {
		t.Fatalf("unexpected decoded task payload/metadata: decoded=%+v task=%+v", decoded, got)
	}
}

func TestEnqueueNormalizesEmptyPayloadForJSONHelpers(t *testing.T) {
	now := testNow()
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		Now: func() time.Time { return now },
	})

	if _, err := q.Enqueue(context.Background(), Enqueue{
		ID:       "task-1",
		Kind:     "assistant.noop",
		Schedule: Once(now),
	}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	got, err := q.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if string(got.Payload) != "null" {
		t.Fatalf("expected nil payload to be normalized to JSON null, got %q", string(got.Payload))
	}
	decoded, err := DecodeJSON[struct {
		Message string `json:"message"`
	}](got)
	if err != nil {
		t.Fatalf("DecodeJSON returned error for normalized payload: %v", err)
	}
	if decoded.Message != "" {
		t.Fatalf("expected zero-value decoded struct, got %+v", decoded)
	}
}

func TestRunOnceOneOffSuccessDeletesTask(t *testing.T) {
	now := testNow()
	store := NewMemoryStore()
	var handled []string
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error {
		handled = append(handled, task.ID)
		return nil
	}), Options{Now: func() time.Time { return now }})

	if _, err := q.Enqueue(context.Background(), Enqueue{ID: "task-1", Kind: "test.once", Schedule: Once(now)}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if len(handled) != 1 {
		t.Fatalf("expected one handled task, got %v", handled)
	}
	_, err := q.Get(context.Background(), "task-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected successful one-off to be deleted, got %v", err)
	}
}

func TestRunOnceRecurringSuccessAdvancesAndResetsAttempts(t *testing.T) {
	now := testNow()
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		Now: func() time.Time { return now },
	})

	if _, err := q.Enqueue(context.Background(), Enqueue{
		ID:          "task-1",
		Kind:        "test.every",
		Schedule:    EveryFrom(now, time.Hour),
		MaxAttempts: 2,
	}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	got, err := q.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	wantNext := now.Add(time.Hour)
	if got.Attempts != 0 || got.NextRunAt == nil || !got.NextRunAt.Equal(wantNext) {
		t.Fatalf("unexpected recurring task: %+v", got)
	}
}

func TestRunOnceFailureRetriesThenDead(t *testing.T) {
	now := testNow()
	var failures []Failure
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error {
		return fmt.Errorf("boom")
	}), Options{
		Now: func() time.Time { return now },
		OnFailure: func(failure Failure) {
			failures = append(failures, failure)
		},
		Retry: RetryPolicy{
			MaxAttempts:    2,
			InitialBackoff: 10 * time.Second,
			MaxBackoff:     time.Minute,
		},
	})

	if _, err := q.Enqueue(context.Background(), Enqueue{ID: "task-1", Kind: "test.fail", Schedule: Once(now)}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned handler failure as error: %v", err)
	}
	if len(failures) != 1 || failures[0].Exhausted {
		t.Fatalf("unexpected first failure callbacks: %+v", failures)
	}
	got, err := q.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	retryAt := now.Add(10 * time.Second)
	if got.Attempts != 1 || got.NextRunAt == nil || !got.NextRunAt.Equal(retryAt) || got.LastError != "boom" {
		t.Fatalf("unexpected retry state: %+v", got)
	}

	now = retryAt
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned dead handler failure as error: %v", err)
	}
	if len(failures) != 2 || !failures[1].Exhausted {
		t.Fatalf("unexpected second failure callbacks: %+v", failures)
	}
	got, err = q.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get after dead returned error: %v", err)
	}
	if !got.Exhausted() || got.NextRunAt != nil || got.Attempts != 2 {
		t.Fatalf("unexpected dead task: %+v", got)
	}
}

func TestRunOnceDiscardDeletesClaimedTaskWithoutFailure(t *testing.T) {
	now := testNow()
	var failures []Failure
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error {
		return Discardf("task %s is obsolete", task.ID)
	}), Options{
		Now: func() time.Time { return now },
		OnFailure: func(failure Failure) {
			failures = append(failures, failure)
		},
	})

	if _, err := q.Enqueue(context.Background(), Enqueue{
		ID:       "task-1",
		Kind:     "test.discard",
		Schedule: EveryFrom(now, time.Hour),
	}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failure callbacks for discarded task, got %+v", failures)
	}
	_, err := q.Get(context.Background(), "task-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected discarded task to be deleted, got %v", err)
	}
}

func TestExpiredLeaseReclaimDoesNotExhaustWithoutHandlerFailure(t *testing.T) {
	now := testNow()
	store := NewMemoryStore()
	var calls int
	var failures []Failure
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error {
		calls++
		return nil
	}), Options{
		Now:           func() time.Time { return now },
		LeaseDuration: time.Minute,
		OnFailure: func(failure Failure) {
			failures = append(failures, failure)
		},
		Retry: RetryPolicy{
			MaxAttempts:    1,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	if _, err := q.Enqueue(context.Background(), Enqueue{ID: "task-1", Kind: "test.crash", Schedule: Once(now)}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if claimed, err := store.ClaimDue(context.Background(), now, time.Minute, 0, "crashed-worker"); err != nil || len(claimed) != 1 {
		t.Fatalf("manual ClaimDue returned claimed=%+v err=%v", claimed, err)
	}

	now = now.Add(2 * time.Minute)
	if err := q.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected handler to run after lease reclaim, got %d calls", calls)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failure callbacks, got %+v", failures)
	}
	_, err := q.Get(context.Background(), "task-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected successful reclaimed one-off to be deleted, got %v", err)
	}
}

func TestRunOnceCancellationDoesNotFailUnstartedOrCanceledTasks(t *testing.T) {
	now := testNow()
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	var failures []Failure
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error {
		cancel()
		return ctx.Err()
	}), Options{
		MaxConcurrent: 1,
		ClaimLimit:    3,
		Now:           func() time.Time { return now },
		OnFailure: func(failure Failure) {
			failures = append(failures, failure)
		},
		Retry: RetryPolicy{
			MaxAttempts:    1,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		if _, err := q.Enqueue(context.Background(), Enqueue{ID: id, Kind: "test.cancel", Schedule: Once(now), MaxAttempts: 1}); err != nil {
			t.Fatalf("Enqueue(%s) returned error: %v", id, err)
		}
	}

	if err := q.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failure callbacks on shutdown cancellation, got %+v", failures)
	}
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		got, err := q.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get(%s) returned error: %v", id, err)
		}
		if got.Attempts != 0 || got.LastError != "" || got.NextRunAt == nil {
			t.Fatalf("expected %s to remain retryable without failure state, got %+v", id, got)
		}
	}
}

func TestRunOnceTimeoutAndPanicAreFailures(t *testing.T) {
	now := testNow()
	var timeoutFailures []Failure
	timeoutQueue := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error {
		<-ctx.Done()
		return ctx.Err()
	}), Options{
		Now: func() time.Time { return now },
		OnFailure: func(failure Failure) {
			timeoutFailures = append(timeoutFailures, failure)
		},
		Retry: RetryPolicy{
			MaxAttempts:    1,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	if _, err := timeoutQueue.Enqueue(context.Background(), Enqueue{
		ID:       "timeout",
		Kind:     "test.timeout",
		Schedule: Once(now),
		Timeout:  time.Millisecond,
	}); err != nil {
		t.Fatalf("timeout Enqueue returned error: %v", err)
	}
	if err := timeoutQueue.RunOnce(context.Background()); err != nil {
		t.Fatalf("timeout RunOnce returned error: %v", err)
	}
	if len(timeoutFailures) != 1 || !timeoutFailures[0].Exhausted {
		t.Fatalf("expected timeout exhausted callback, got %+v", timeoutFailures)
	}

	var panicFailures []Failure
	panicQueue := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error {
		panic("bad payload")
	}), Options{
		Now: func() time.Time { return now },
		OnFailure: func(failure Failure) {
			panicFailures = append(panicFailures, failure)
		},
		Retry: RetryPolicy{
			MaxAttempts:    1,
			InitialBackoff: time.Second,
			MaxBackoff:     time.Second,
		},
	})
	if _, err := panicQueue.Enqueue(context.Background(), Enqueue{ID: "panic", Kind: "test.panic", Schedule: Once(now)}); err != nil {
		t.Fatalf("panic Enqueue returned error: %v", err)
	}
	if err := panicQueue.RunOnce(context.Background()); err != nil {
		t.Fatalf("panic RunOnce returned error: %v", err)
	}
	if len(panicFailures) != 1 || !panicFailures[0].Exhausted {
		t.Fatalf("expected panic exhausted callback, got %+v", panicFailures)
	}
}

func TestRescheduleAndDelete(t *testing.T) {
	now := testNow()
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		Now: func() time.Time { return now },
	})
	if _, err := q.Enqueue(context.Background(), Enqueue{ID: "task-1", Kind: "test", Schedule: Once(now.Add(time.Hour))}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	rescheduled, err := q.Reschedule(context.Background(), "task-1", Once(now))
	if err != nil {
		t.Fatalf("Reschedule returned error: %v", err)
	}
	if rescheduled.NextRunAt == nil || !rescheduled.NextRunAt.Equal(now) {
		t.Fatalf("unexpected rescheduled task: %+v", rescheduled)
	}
	ok, err := q.Delete(context.Background(), "task-1")
	if err != nil || !ok {
		t.Fatalf("Delete returned ok=%v err=%v", ok, err)
	}
	_, err = q.Get(context.Background(), "task-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestGroupConcurrencyMiddleware(t *testing.T) {
	now := testNow()
	started := make(chan string, 3)
	release := make(chan struct{})

	var mu sync.Mutex
	active := 0
	maxActive := 0
	handler := Chain(HandlerFunc(func(ctx context.Context, task Task) error {
		mu.Lock()
		active++
		maxActive = max(maxActive, active)
		mu.Unlock()

		started <- task.ID
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}

		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}), GroupConcurrency(1))

	q := mustQueue(t, NewMemoryStore(), handler, Options{
		MaxConcurrent: 3,
		Now:           func() time.Time { return now },
	})
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		if _, err := q.Enqueue(context.Background(), Enqueue{ID: id, Kind: "test", Group: "same", Schedule: Once(now)}); err != nil {
			t.Fatalf("Enqueue(%s) returned error: %v", id, err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		err := q.RunOnce(context.Background())
		errCh <- err
	}()

	waitStarted(t, started)
	select {
	case id := <-started:
		t.Fatalf("second same-group task started before release: %s", id)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := waitErr(t, errCh); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if maxActive != 1 {
		t.Fatalf("expected max active 1, got %d", maxActive)
	}
}

func TestRunLoopReportsOperationalErrorsAndKeepsLoopPolicy(t *testing.T) {
	claimErr := errors.New("temporary claim failure")
	store := &transientStore{Store: NewMemoryStore(), claimErr: claimErr}
	ctx, cancel := context.WithCancel(context.Background())
	var got []error
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		PollInterval: time.Hour,
		OnError: func(err error) {
			got = append(got, err)
			cancel()
		},
	})

	err := q.RunLoop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after OnError cancellation, got %v", err)
	}
	if len(got) != 1 || !errors.Is(got[0], claimErr) {
		t.Fatalf("expected OnError to receive claim error, got %+v", got)
	}
}

func TestRunLoopReportsNextRunErrorsAndKeepsLoopPolicy(t *testing.T) {
	nextErr := errors.New("temporary next-run failure")
	store := &transientStore{Store: NewMemoryStore(), nextErr: nextErr}
	ctx, cancel := context.WithCancel(context.Background())
	var got []error
	q := mustQueue(t, store, HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{
		PollInterval: time.Hour,
		OnError: func(err error) {
			got = append(got, err)
			cancel()
		},
	})

	err := q.RunLoop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after OnError cancellation, got %v", err)
	}
	if len(got) != 1 || !errors.Is(got[0], nextErr) {
		t.Fatalf("expected OnError to receive next-run error, got %+v", got)
	}
}

type transientStore struct {
	Store
	claimErr error
	nextErr  error
}

func (s *transientStore) ClaimDue(ctx context.Context, now time.Time, leaseDuration time.Duration, limit int, token string) ([]Task, error) {
	if s.claimErr != nil {
		err := s.claimErr
		s.claimErr = nil
		return nil, err
	}
	return s.Store.ClaimDue(ctx, now, leaseDuration, limit, token)
}

func (s *transientStore) NextRunAt(ctx context.Context, now time.Time, leaseDuration time.Duration) (*time.Time, error) {
	if s.nextErr != nil {
		err := s.nextErr
		s.nextErr = nil
		return nil, err
	}
	return s.Store.NextRunAt(ctx, now, leaseDuration)
}

func mustQueue(t *testing.T, store Store, handler Handler, opts Options) *Queue {
	t.Helper()
	q, err := New(store, handler, opts)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return q
}

func waitStarted(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case id := <-started:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task to start")
		return ""
	}
}

func waitErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RunOnce")
		return nil
	}
}

func testNow() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}
