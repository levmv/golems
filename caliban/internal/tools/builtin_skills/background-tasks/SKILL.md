---
name: background-tasks
description: Manage long-running shell commands or runner invocations with Caliban's background task tools, including starting managed jobs, checking incremental output, waiting for completion, and stopping stuck tasks.
---

# Background Tasks

Use managed background tasks for commands or runner jobs that may take several minutes, stream substantial output, or need follow-up after the current response starts.

For shell commands, call `shell` with `run_in_background: true`. Do not append `&`, `nohup`, `disown`, or similar shell job-control constructs.

For external agents, call `runner_run` with `run_in_background: true`.

After starting a task, keep the returned `task_id`. Use `task_output` to read output. Use its returned offset for incremental reads when polling. Use `block: true` when waiting briefly for more output is useful.

Use `task_list` to inspect active or recent work. Use `task_stop` when a task is clearly stuck, no longer needed, or running the wrong command.

Summarize long outputs instead of pasting them wholesale. If a task fails, report the exit status and the relevant final output.
