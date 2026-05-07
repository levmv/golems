# schedule

`pkg/schedule` is a small generic scheduler core for this monorepo.

It intentionally does not know about Hugin checks, assistant reminders, chats, SSH, LLMs, or Telegram. It only coordinates time, atomic run claims, run records, and global dispatch.

## Model

Applications provide `JobSpec` values:

```go
schedule.JobSpec{
	ID:             "check:disk_web1",
	Kind:           "hugin.check",
	Ref:            "disk_web1",
	Group:          "target:web1",
	Trigger:        schedule.Cron("*/15 * * * *"),
	Timeout:        10 * time.Second,
	InitialRun:     true,
}
```

The scheduler treats `ID`, `Kind`, and `Ref` as routing metadata. The real payload lives in the application database or configuration.

For an assistant reminder:

```go
schedule.JobSpec{
	ID:      "reminder:rem_123",
	Kind:    "assistant.reminder",
	Ref:     "rem_123",
	Group:   "user:user_42",
	Trigger: schedule.At(dueAt),
	Timeout: 30 * time.Second,
}
```

The assistant runner uses `Kind` and `Ref` to load the reminder payload from its own store.

## Runner

```go
runner := schedule.RunnerFunc(func(ctx context.Context, job schedule.Job) error {
	switch job.Spec.Kind {
	case "hugin.check":
		return engine.RunCheck(ctx, job.Spec.Ref)
	case "assistant.reminder":
		return reminders.Send(ctx, job.Spec.Ref)
	default:
		return fmt.Errorf("unknown job kind: %s", job.Spec.Kind)
	}
})
```

Runner behavior can be wrapped with middleware:

```go
runner = schedule.Chain(
	runner,
	schedule.GroupConcurrency(1),
)
```

This is how Hugin can run checks in parallel globally while avoiding more than one check per target at a time. The scheduler core does not need to know what a "target" is.
Jobs with an empty `Group` are not limited by `GroupConcurrency`; set a group explicitly when jobs should share a per-group limit.

## Cron Mode

For cron or systemd timers, load specs and run due jobs once:

```go
s := schedule.New(store, runner, schedule.Options{
	MaxConcurrent: 8,
})

err := s.RunDue(ctx, specs)
```

`MaxConcurrent` limits total in-flight jobs. Per-target or per-user limits belong in runner middleware.

## Daemon Mode

Daemon mode should use the same scheduler and runner through `RunLoop`:

```go
err := s.RunLoop(ctx, time.Minute, func(ctx context.Context) ([]schedule.JobSpec, error) {
	return loadSpecs(ctx)
}, func(err error) {
	log.Warn("scheduled run failed: %v", err)
})
if err != nil && !errors.Is(err, context.Canceled) {
	return err
}
```

`RunLoop` runs one pass immediately, then repeats at the given interval until the context is cancelled. Load and run errors are passed to the callback and do not stop the loop.

If the application needs custom loop behavior, it can still call `RunDue` directly:

```go
for {
	specs, err := loadSpecs(ctx)
	if err == nil {
		err = s.RunDue(ctx, specs)
	}
	if err != nil {
		log.Warn("scheduled run failed: %v", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Minute):
	}
}
```

The difference between cron mode and daemon mode should be orchestration only. Job semantics should stay in the shared runner.

## Due Time

`DueAt` is the scheduled time. `StartedAt` is the actual execution time.

For first runs:

- `At(dueAt)` runs when `dueAt` is at or before now.
- `InitialRun: true` creates one bootstrap occurrence due on the first pass when no run history exists.
- For recurring `Every`/`Cron` jobs, prefer `InitialRun: true` until the package grows durable next-run state or explicit schedule anchors.

Recurring jobs advance from the previous `DueAt`, not `StartedAt`, so slow execution does not drift the schedule:

```text
Every 5m
DueAt:     12:00, 12:05, 12:10
StartedAt: 12:00:03, 12:05:42, 12:10:08
```

For missed recurring runs, the scheduler runs at most one occurrence: the latest scheduled `DueAt` at or before now. It does not catch up every missed occurrence.

Run claims are occurrence-based. Initial bootstrap jobs use the deterministic occurrence key `initial`; scheduled jobs use job ID plus `DueAt`. Stores should enforce uniqueness on `(job_id, occurrence_key)`, for example with `INSERT ... ON CONFLICT DO NOTHING`. If the atomic claim fails, another process already claimed or ran that occurrence.

This is an at-most-once coordination model. If a process claims an occurrence and crashes before the runner performs the external work, that occurrence remains claimed as `running` and is not retried by the scheduler core.

If a runner returns an error or panics, the run is marked failed and the error is returned from `RunDue` / `RunJobs`.

## Cron Semantics

`Cron` supports five-field expressions with `*`, single numbers, comma lists, ranges, and `*/N` steps. Month is `1-12`; day of week is `0-7`, where `0` and `7` both mean Sunday.

When both day-of-month and day-of-week are restricted, both fields must match. This is stricter than some cron implementations that treat those two fields as OR.

## Store Ownership

`Store` is intentionally only the coordination boundary. Applications can keep job payloads in their own database, config files, or JSON files, then map those records to `JobSpec`.

Durable stores should enforce uniqueness on `(job_id, occurrence_key)`. In-memory storage is useful for tests and single-process experiments; app-owned SQLite adapters are usually clearer than a package-owned database because the application already controls schema, migrations, backup, and retention.
