# Hugin Roadmap

Hugin should stay a small local AI operator for personal infrastructure: scripts
collect facts, SQLite keeps memory, and the LLM makes the judgment with enough
context to explain itself.

## Principles

- Keep `run`, `run-due`, and daemon mode on the same execution path.
- Prefer visible state over clever behavior: runs, analysis decisions, incidents,
  notes, and task state should be inspectable.
- Keep collectors dumb. They measure and report structured facts; Hugin decides
  whether the facts matter.
- Avoid a second rule engine. If AI analysis is unavailable, treat that as its
  own operational incident.
- Keep package boundaries modest and concrete until repeated pain asks for more.

## Current Shape

```text
config -> engine -> runner -> runs -> AI analysis -> incidents -> notifier
```

Scheduled checks are durable `pkg/tasks` rows with deterministic IDs. Hugin
stores run history in `runs`, incident state in `incidents`, and ad-hoc operator
notes in `notes`.

Stable knowledge belongs in YAML `context` on targets and checks. Temporary
operator observations belong in CLI-managed notes.

## Done

- `hugin daemon` runs the same scheduled check path as `run-due` through
  `pkg/tasks.RunLoop`.
- Graceful shutdown returns without recording canceled checks as completed or
  failed scheduled tasks.
- A sample systemd service unit lives in `hugin/contrib/systemd/hugin.service`.
- Scheduled task sync no longer keeps a separate Hugin manifest table. Current
  checks are reconciled directly by deterministic task ID, and removed checks are
  discarded lazily through `pkg/tasks`.
- `pkg/tasks` supports `Discard`/`Discardf` for obsolete claimed work that should
  be deleted without retries or failure callbacks.

## Next

1. Add minimal operator status.

- Add `hugin status` if it helps validate daemon/scheduler decisions: active
  incidents, last run per check, and next scheduled run.
- Keep deeper inspection commands out of the critical path unless implementation
  work proves that they would clarify storage or analysis boundaries.

2. Tighten analysis failure handling.

- Retry brief/transient LLM failures before opening an analysis-unavailable
  incident.
- Add focused tests for analysis-unavailable incident creation and recovery.
- Make analysis persistence easy to inspect from the CLI.
- Decide how to resolve or hide analysis-unavailable incidents when their parent
  check is removed from config.

3. Harden SSH execution.

- Replace `ssh.InsecureIgnoreHostKey` with known-hosts validation.
- Add tests around SSH reuse, timeout behavior, and session cleanup.
- Consider per-target session limits only after real usage shows pressure.

4. Improve incident and notification ergonomics.

- Test notification cooldown/repeat/resolution behavior end to end.
- Decide whether `last_notified_at` is enough or whether notification history is
  worth the extra table.
- Make manual resolution and auto-resolution easy to audit from CLI output.
- Make manual `resolve` idempotent and apply `notify_on_resolved` consistently
  for analysis-unavailable incidents.
- Decide how aggressively failed notifications should retry before
  `repeat_after`.
- Consider separating incident state transitions from notification routing if
  cooldown/repeat rules keep growing.

5. Reduce new-host setup friction.

- Explore `hugin deploy <target>` for bootstrapping a host from config.
- Generate or select an SSH key for Hugin access.
- Install bundled collectors into a fixed remote directory.
- Add the public key to the remote host with a restricted forced command.
- Consider a small remote wrapper that only executes allowlisted scripts from
  the collector directory and rejects arbitrary commands.
- Keep manual setup documented as the fallback path; deploy should save typing,
  not hide what security choices were made.

6. Add deeper operator inspection commands.

- `hugin why <check_id>` or `hugin inspect run <id>`: collector output, context,
  notes, history summary, and persisted analysis result.
- `hugin incidents`: active and recent incidents.

7. Revisit atomicity after the core CLI settles.

- Decide whether run insert, analysis persistence, incident update, and
  notification-state update need transaction boundaries.
- Reuse notifier construction across a batch if notifier setup becomes expensive.

## Later

- Add an advisory `hugin doctor` or `hugin review-config` command that can use
  AI to review configuration, collector contracts, schedules, alert settings,
  and missing context. Keep normal `hugin validate` deterministic, fast, and
  offline.
- Add history-aware check review, for example `hugin review-check <check_id>`,
  once real run and incident data exists.

## Not Now

- A Prometheus-compatible metrics system.
- A deterministic fallback expression language.
- A full plugin system for collectors.
- A large migration framework before there are production databases.
