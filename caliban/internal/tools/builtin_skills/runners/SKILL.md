---
name: runners
description: Use trusted external agent runners agy, codex, claude, or pi for second opinions, independent repository investigation, model-specific checks, or tasks explicitly asking Caliban to invoke another agent/CLI.
---

# Trusted Runners

Use `runner_list` first when runner availability is uncertain. Use `runner_models` only for runners that advertise model listing support.

Use `runner_run` instead of `shell` for agy, codex, claude, or pi. These CLIs run with their normal authentication state but through a semantic wrapper that limits workspace access and avoids arbitrary argv.

Caliban sets runner-native non-interactive/approval flags where practical. Do not try to invoke these CLIs through `shell` just to add permission flags.

Default to `workspace_access: "read_only"` for investigation, review, model comparison, and second opinions. Use `workspace_access: "read_write"` only when the user asked the external runner to modify files or when the task clearly needs edits.

Use `run_in_background: true` for work that may take more than a short interactive turn. Read results with `task_output`, inspect status with `task_list`, and stop runaway work with `task_stop`.

Use `session: "continue"` only when the user asks to continue that runner's latest session or the task depends on previous runner context. Use an exact session id only when it is known from runner output or the user provides it. Omit `session` for a fresh task.

Do not use shell to inspect runner credentials, auth files, config, cache, history, logs, or session storage. Do not ask a runner to print its own secrets. If runner authentication is missing or expired, report that plainly.

When reporting a runner result, make clear which runner produced it and distinguish the runner's claims from Caliban's own verification.
