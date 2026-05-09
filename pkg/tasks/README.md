# tasks

`pkg/tasks` is a durable, database-driven task queue for this monorepo.

Applications enqueue work once with a kind, payload, schedule, and optional
group. Task definitions and runtime state live in the store; the queue loop
claims due rows and dispatches them to a handler.

```go
store := tasks.NewMemoryStore()
handler := tasks.HandlerFunc(func(ctx context.Context, task tasks.Task) error {
	switch task.Kind {
	case "assistant.reminder":
		payload, err := tasks.DecodeJSON[struct {
			Text string `json:"text"`
		}](task)
		if err != nil {
			return err
		}
		return sendReminder(ctx, payload.Text)
	default:
		return fmt.Errorf("unknown task kind: %s", task.Kind)
	}
})

q, err := tasks.New(store, handler, tasks.Options{MaxConcurrent: 8})
if err != nil {
	return err
}

payload, err := tasks.JSONPayload(map[string]string{"text": "Call John"})
if err != nil {
	return err
}
_, err = q.Enqueue(ctx, tasks.Enqueue{
	Kind:     "assistant.reminder",
	Payload:  payload,
	Schedule: tasks.Once(time.Now().Add(time.Hour)),
	Group:    "user:user_42",
})
```

`RunOnce` returns operational errors only. `RunLoop` reports operational errors
through `Options.OnError`, sleeps for `PollInterval`, and keeps going until its
context is cancelled. Handler failures are recorded on the task row, retried
according to policy, and can be observed with `Options.OnFailure`.

Handlers can return `tasks.Discard` or `tasks.Discardf` when a claimed task is
obsolete. The queue deletes the claimed row without treating it as a failure and
without retrying it.

## Schedules

Supported persisted schedules:

- `tasks.Once(at)` for one-off tasks.
- `tasks.Every(interval)` for recurring tasks first due at enqueue time plus
  the interval.
- `tasks.EveryFrom(start, interval)` for anchored recurring tasks.
- `tasks.Cron(expr, timezone)` for five-field cron schedules.
- `tasks.CronFrom(expr, timezone, firstRunAt)` for cron tasks that need an
  explicit first run before following the cron cadence.

Successful one-off tasks are deleted. Recurring success resets attempts and
advances to the next natural future occurrence. Missed recurring intervals are
skipped; the queue runs one claimed occurrence at a time.

## Retries And DLQ

Attempts count handler failures, not claims. A claimed task that is abandoned
during shutdown remains locked until its lease expires and can be retried without
burning an attempt. Handler failures retry with exponential backoff until
`MaxAttempts`; exhausted tasks remain stored with `next_run_at = NULL` and the
last error for inspection or manual rescheduling.

The default retry policy is 3 attempts, 30s initial backoff, and 15m max backoff.

## SQLite

Durable SQLite storage is provided by `pkg/tasks/sqlite`:

```go
if err := tasksqlite.EnsureSchema(ctx, db, tasksqlite.Options{}); err != nil {
	return err
}
store, err := tasksqlite.New(db, tasksqlite.Options{})
```

Applications still own the database handle, driver import, migrations, backups,
and retention policy.
