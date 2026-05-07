package schedule

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

type Options struct {
	MaxConcurrent int
	Now           func() time.Time
}

const finishTimeout = 5 * time.Second

type Scheduler struct {
	store  Store
	runner Runner
	opts   Options
}

type LoadSpecsFunc func(ctx context.Context) ([]JobSpec, error)

func New(store Store, runner Runner, opts Options) *Scheduler {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Scheduler{
		store:  store,
		runner: runner,
		opts:   opts,
	}
}

// Due returns jobs that should run at now.
func (s *Scheduler) Due(ctx context.Context, specs []JobSpec) ([]Job, error) {
	now := s.opts.Now()
	due := make([]Job, 0)
	ids := make([]string, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))

	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("validate %q: %w", spec.ID, err)
		}
		if _, ok := seen[spec.ID]; ok {
			return nil, fmt.Errorf("validate %q: %w", spec.ID, fmt.Errorf("%w: duplicate job ID %q", ErrInvalid, spec.ID))
		}
		seen[spec.ID] = struct{}{}
		ids = append(ids, spec.ID)
	}

	lastRuns, err := s.store.LastRuns(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("last runs: %w", err)
	}

	for _, spec := range specs {
		loc, err := location(spec.Timezone)
		if err != nil {
			return nil, fmt.Errorf("job %q timezone: %w", spec.ID, err)
		}

		last, ok := lastRuns[spec.ID]
		if !ok {
			if spec.InitialRun {
				due = append(due, Job{Spec: spec, DueAt: now.In(loc), OccurrenceKey: "initial"})
				continue
			}

			next, ok := spec.Trigger.Next(time.Time{})
			if ok && !next.In(loc).After(now.In(loc)) {
				dueAt := next.In(loc)
				due = append(due, Job{Spec: spec, DueAt: dueAt, OccurrenceKey: dueAtOccurrenceKey(dueAt)})
			}
			continue
		}

		due = append(due, s.dueAfter(spec, last.DueAt.In(loc), now.In(loc))...)
	}

	return due, nil
}

// RunDue runs all currently due jobs with a global concurrency limit. It
// returns a joined error if one or more jobs fail.
func (s *Scheduler) RunDue(ctx context.Context, specs []JobSpec) error {
	jobs, err := s.Due(ctx, specs)
	if err != nil {
		return err
	}
	return s.RunJobs(ctx, jobs)
}

// RunLoop repeatedly loads specs and runs due jobs until ctx is cancelled.
// Errors are passed to onError and do not stop the loop.
func (s *Scheduler) RunLoop(ctx context.Context, interval time.Duration, load LoadSpecsFunc, onError func(error)) error {
	if load == nil {
		return fmt.Errorf("schedule: load specs function is required")
	}
	if interval <= 0 {
		interval = time.Minute
	}

	runOnce := func() {
		specs, err := load(ctx)
		if err == nil {
			err = s.RunDue(ctx, specs)
		}
		if err != nil && onError != nil && !errors.Is(err, context.Canceled) {
			onError(err)
		}
	}

	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Scheduler) RunJobs(ctx context.Context, jobs []Job) error {
	if len(jobs) == 0 {
		return nil
	}

	global := make(chan struct{}, s.opts.MaxConcurrent)

	var wg sync.WaitGroup
	errs := make(chan error, len(jobs))

	for _, job := range jobs {
		wg.Add(1)
		go func(job Job) {
			defer wg.Done()

			select {
			case global <- struct{}{}:
				defer func() { <-global }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}

			if err := s.runOne(ctx, job); err != nil {
				errs <- err
			}
		}(job)
	}

	wg.Wait()
	close(errs)

	var joined error
	for err := range errs {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (s *Scheduler) runOne(ctx context.Context, job Job) error {
	startedAt := s.opts.Now()
	run, claimed, err := s.store.TryCreateRun(ctx, RunRecord{
		JobID:         job.Spec.ID,
		OccurrenceKey: occurrenceKey(job),
		Kind:          job.Spec.Kind,
		Ref:           job.Spec.Ref,
		Group:         job.Spec.Group,
		DueAt:         job.DueAt,
		StartedAt:     startedAt,
		Status:        RunRunning,
	})
	if err != nil {
		return fmt.Errorf("claim run for %q: %w", job.Spec.ID, err)
	}
	if !claimed {
		return nil
	}

	job.RunID = run.ID
	runCtx := ctx
	cancel := func() {}
	if job.Spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, job.Spec.Timeout)
	}
	defer cancel()

	runErr := s.runSafely(runCtx, job)
	finishedAt := s.opts.Now()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer finishCancel()
	if runErr != nil {
		msg := runErr.Error()
		if err := s.store.FinishRun(finishCtx, run.ID, RunFailed, msg, finishedAt); err != nil {
			return errors.Join(runErr, fmt.Errorf("finish failed run %q: %w", run.ID, err))
		}
		return fmt.Errorf("run %q: %w", job.Spec.ID, runErr)
	}

	if err := s.store.FinishRun(finishCtx, run.ID, RunSucceeded, "", finishedAt); err != nil {
		return fmt.Errorf("finish run %q: %w", run.ID, err)
	}
	return nil
}

func occurrenceKey(job Job) string {
	if job.OccurrenceKey != "" {
		return job.OccurrenceKey
	}
	return dueAtOccurrenceKey(job.DueAt)
}

func (s *Scheduler) runSafely(ctx context.Context, job Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in job %s: %v\n%s", job.Spec.ID, r, debug.Stack())
		}
	}()
	return s.runner.Run(ctx, job)
}

func dueAtOccurrenceKey(dueAt time.Time) string {
	return fmt.Sprintf("%d", dueAt.UTC().UnixNano())
}

func (s *Scheduler) dueAfter(spec JobSpec, after, now time.Time) []Job {
	next, ok := spec.Trigger.Next(after)
	if !ok || next.After(now) {
		return nil
	}

	latest := next
	for {
		candidate, ok := spec.Trigger.Next(latest)
		if !ok || candidate.After(now) {
			break
		}
		latest = candidate
	}
	return []Job{{Spec: spec, DueAt: latest}}
}

func location(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}
