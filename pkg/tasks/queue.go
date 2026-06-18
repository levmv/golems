package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type Options struct {
	MaxConcurrent int
	ClaimLimit    int
	LeaseDuration time.Duration
	FinishTimeout time.Duration
	PollInterval  time.Duration
	Retry         RetryPolicy
	Now           func() time.Time
	NewID         func() string
	NewToken      func() string
	OnFailure     func(Failure)
	OnError       func(error)
}

const (
	defaultMaxAttempts    = 3
	defaultInitialBackoff = 30 * time.Second
	defaultMaxBackoff     = 15 * time.Minute
	defaultLeaseDuration  = 5 * time.Minute
	defaultFinishTimeout  = 5 * time.Second
	defaultPollInterval   = time.Minute
)

type Queue struct {
	store   Store
	handler Handler
	opts    Options
	wake    chan struct{}
}

func New(store Store, handler Handler, opts Options) (*Queue, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if handler == nil {
		return nil, fmt.Errorf("%w: handler is required", ErrInvalid)
	}
	applyOptionDefaults(&opts)
	return &Queue{
		store:   store,
		handler: handler,
		opts:    opts,
		wake:    make(chan struct{}, 1),
	}, nil
}

func applyOptionDefaults(opts *Options) {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.ClaimLimit <= 0 {
		opts.ClaimLimit = opts.MaxConcurrent
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = defaultLeaseDuration
	}
	if opts.FinishTimeout <= 0 {
		opts.FinishTimeout = defaultFinishTimeout
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.Retry.MaxAttempts <= 0 {
		opts.Retry.MaxAttempts = defaultMaxAttempts
	}
	if opts.Retry.InitialBackoff <= 0 {
		opts.Retry.InitialBackoff = defaultInitialBackoff
	}
	if opts.Retry.MaxBackoff <= 0 {
		opts.Retry.MaxBackoff = defaultMaxBackoff
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = randomID
	}
	if opts.NewToken == nil {
		opts.NewToken = randomToken
	}
}

func (q *Queue) Enqueue(ctx context.Context, req Enqueue) (Task, error) {
	now := q.opts.Now().UTC()
	if req.ID == "" {
		req.ID = q.opts.NewID()
	}
	if req.ID == "" {
		return Task{}, fmt.Errorf("%w: generated task ID is empty", ErrInvalid)
	}
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = q.opts.Retry.MaxAttempts
	}
	next, err := req.Schedule.initialNextRun(now)
	if err != nil {
		return Task{}, fmt.Errorf("%w: schedule: %v", ErrInvalid, err)
	}
	task := Task{
		ID:          req.ID,
		Kind:        req.Kind,
		Payload:     normalizePayload(req.Payload),
		Schedule:    req.Schedule,
		Group:       req.Group,
		Timeout:     req.Timeout,
		MaxAttempts: maxAttempts,
		Metadata:    cloneMetadata(req.Metadata),
		NextRunAt:   &next,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := task.validate(); err != nil {
		return Task{}, err
	}
	if err := q.store.Enqueue(ctx, task); err != nil {
		return Task{}, err
	}
	q.signalWake()
	return task.clone(), nil
}

func (q *Queue) Get(ctx context.Context, id string) (Task, error) {
	if id == "" {
		return Task{}, fmt.Errorf("%w: task ID is required", ErrInvalid)
	}
	return q.store.Get(ctx, id)
}

// List returns tasks matching filter, ordered by next_run_at ascending with
// exhausted tasks last.
func (q *Queue) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	return q.store.List(ctx, filter)
}

func (q *Queue) Delete(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: task ID is required", ErrInvalid)
	}
	ok, err := q.store.Delete(ctx, id)
	if err == nil && ok {
		q.signalWake()
	}
	return ok, err
}

func (q *Queue) Reschedule(ctx context.Context, id string, schedule Schedule) (Task, error) {
	if id == "" {
		return Task{}, fmt.Errorf("%w: task ID is required", ErrInvalid)
	}
	now := q.opts.Now().UTC()
	next, err := schedule.initialNextRun(now)
	if err != nil {
		return Task{}, fmt.Errorf("%w: schedule: %v", ErrInvalid, err)
	}
	task, ok, err := q.store.Reschedule(ctx, id, schedule, next, now)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, ErrNotFound
	}
	q.signalWake()
	return task, nil
}

func (q *Queue) RunOnce(ctx context.Context) error {
	now := q.opts.Now().UTC()
	token := q.opts.NewToken()
	if token == "" {
		return fmt.Errorf("%w: generated claim token is empty", ErrInvalid)
	}
	tasks, err := q.store.ClaimDue(ctx, now, q.opts.LeaseDuration, q.opts.ClaimLimit, token)
	if err != nil {
		return fmt.Errorf("claim due tasks: %w", err)
	}
	return q.runClaimed(ctx, tasks)
}

func (q *Queue) RunLoop(ctx context.Context) error {
	for {
		err := q.RunOnce(ctx)
		if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return ctx.Err()
		}
		if err != nil {
			q.notifyError(err)
			if err := q.sleepUntil(ctx, q.opts.Now().UTC().Add(q.opts.PollInterval)); err != nil {
				return err
			}
			continue
		}

		now := q.opts.Now().UTC()
		next, err := q.store.NextRunAt(ctx, now, q.opts.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			q.notifyError(fmt.Errorf("next run: %w", err))
			if err := q.sleepUntil(ctx, now.Add(q.opts.PollInterval)); err != nil {
				return err
			}
			continue
		}
		if next != nil && !next.After(now) {
			continue
		}

		sleepUntil := now.Add(q.opts.PollInterval)
		if next != nil && next.Before(sleepUntil) {
			sleepUntil = *next
		}
		if err := q.sleepUntil(ctx, sleepUntil); err != nil {
			return err
		}
	}
}

func (q *Queue) runClaimed(ctx context.Context, tasks []Task) error {
	if len(tasks) == 0 {
		return nil
	}

	workerCount := min(q.opts.MaxConcurrent, len(tasks))
	taskCh := make(chan Task)
	results := make(chan runOutcome, len(tasks))

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				if ctx.Err() != nil {
					results <- runOutcome{}
					continue
				}
				results <- runOutcome{err: q.runOne(ctx, task)}
			}
		}()
	}

sendLoop:
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			break sendLoop
		case taskCh <- task:
		}
	}
	close(taskCh)
	wg.Wait()
	close(results)

	var err error
	for outcome := range results {
		err = errors.Join(err, outcome.err)
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return err
}

func (q *Queue) runOne(ctx context.Context, task Task) error {
	if task.Attempts > task.MaxAttempts {
		return q.finishFailed(ctx, task, fmt.Errorf("max attempts exceeded"))
	}

	runCtx := ctx
	cancel := func() {}
	if task.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, task.Timeout)
	}
	defer cancel()

	runErr := q.runSafely(runCtx, task)
	if runErr != nil {
		if ctx.Err() != nil && errors.Is(runErr, ctx.Err()) {
			return ctx.Err()
		}
		if errors.Is(runErr, ErrDiscard) {
			return q.discardClaimed(ctx, task)
		}
		return q.finishFailed(ctx, task, runErr)
	}
	return q.finishSucceeded(ctx, task)
}

func (q *Queue) discardClaimed(ctx context.Context, task Task) error {
	finishCtx, cancel := q.finishContext(ctx)
	defer cancel()

	ok, err := q.store.DeleteClaimed(finishCtx, task.ID, task.LockToken)
	if err != nil {
		return fmt.Errorf("delete discarded task %q: %w", task.ID, err)
	}
	if ok {
		q.signalWake()
	}
	return nil
}

func (q *Queue) finishSucceeded(ctx context.Context, task Task) error {
	now := q.opts.Now().UTC()
	next, err := task.Schedule.nextAfterRun(dueAt(task), now)
	if err != nil {
		return q.finishFailed(ctx, task, fmt.Errorf("advance schedule: %w", err))
	}

	finishCtx, cancel := q.finishContext(ctx)
	defer cancel()

	if next == nil {
		ok, err := q.store.DeleteClaimed(finishCtx, task.ID, task.LockToken)
		if err != nil {
			return fmt.Errorf("delete successful one-off task %q: %w", task.ID, err)
		}
		if !ok {
			return nil
		}
		return nil
	}

	ok, err := q.store.Finish(finishCtx, Finish{
		ID:            task.ID,
		LockToken:     task.LockToken,
		FinishedAt:    now,
		NextRunAt:     next,
		ResetAttempts: true,
	})
	if err != nil {
		return fmt.Errorf("finish successful task %q: %w", task.ID, err)
	}
	if !ok {
		return nil
	}
	return nil
}

func (q *Queue) finishFailed(ctx context.Context, task Task, runErr error) error {
	now := q.opts.Now().UTC()
	attempts := task.Attempts + 1
	exhausted := false
	next := cloneTimePtr(retryAt(now, attempts, q.opts.Retry))
	if attempts >= task.MaxAttempts {
		exhausted = true
		next = nil
	}

	finishCtx, cancel := q.finishContext(ctx)
	defer cancel()
	ok, err := q.store.Finish(finishCtx, Finish{
		ID:                task.ID,
		LockToken:         task.LockToken,
		Error:             runErr.Error(),
		FinishedAt:        now,
		NextRunAt:         next,
		IncrementAttempts: true,
	})
	if err != nil {
		return fmt.Errorf("finish failed task %q: %w", task.ID, err)
	}
	if !ok {
		return nil
	}

	task.LockedAt = nil
	task.LockToken = ""
	task.LastFinishedAt = &now
	task.LastError = runErr.Error()
	task.NextRunAt = cloneTimePtr(next)
	task.Attempts = attempts
	q.notifyFailure(Failure{Task: task.clone(), Err: runErr, Exhausted: exhausted})
	return nil
}

func retryAt(now time.Time, attempt int, policy RetryPolicy) *time.Time {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := policy.InitialBackoff
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= policy.MaxBackoff {
			backoff = policy.MaxBackoff
			break
		}
	}
	next := now.Add(backoff).UTC()
	return &next
}

func dueAt(task Task) time.Time {
	if task.NextRunAt == nil {
		return time.Time{}
	}
	return task.NextRunAt.UTC()
}

func (q *Queue) runSafely(ctx context.Context, task Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in task %s: %v\n%s", task.ID, r, debug.Stack())
		}
	}()
	return q.handler.HandleTask(ctx, task.clone())
}

func (q *Queue) finishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), q.opts.FinishTimeout)
}

func (q *Queue) notifyFailure(failure Failure) {
	if q.opts.OnFailure == nil {
		return
	}
	q.opts.OnFailure(failure)
}

func (q *Queue) notifyError(err error) {
	if q.opts.OnError == nil {
		return
	}
	q.opts.OnError(err)
}

func (q *Queue) signalWake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) sleepUntil(ctx context.Context, until time.Time) error {
	delay := until.Sub(q.opts.Now().UTC())
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-q.wake:
		return nil
	case <-timer.C:
		return nil
	}
}

type runOutcome struct {
	err error
}

func randomID() string {
	return "task_" + randomHex(16)
}

func randomToken() string {
	return randomHex(16)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
