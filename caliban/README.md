# Caliban

A long-lived personal AI assistant. Caliban holds durable conversations
reachable over Telegram and a web UI, backed by file-based memory, a sandboxed
shell, reminders and scheduled turns, and silent self-maintenance — it compacts
its own context and periodically reflects on its persona without being asked.

Because it drives a real shell from a semi-trusted model, Caliban's safety rests
on **isolation, not a permission prompt**: a hardened systemd unit confines the
host and a per-command Landlock sandbox keeps the shell away from Caliban's own
secrets. This boundary is the deployment — see [Deploy](#deploy).

## Build & run

Requirements: Go 1.26+ and Node (for bundling the web assets).

```sh
make test     # go test ./... + webapp typecheck
make build    # test, bundle web assets, build ./caliban
make run      # run against a local config (CONFIG=dev-config.json by default)
make inspect  # print a conversation's context/compaction diagnostics
make clean    # remove the binary and embedded web assets
```

The binary dispatches subcommands:

```
caliban serve                Run the assistant: Telegram/web transports and the task queue.
caliban inspect-context      Print a conversation's context state (summary, tail, compaction).
caliban set-web-password     Set or change the web UI password.
caliban generate-vapid-keys  Print a new Web Push VAPID key pair.
```

The config path defaults to `/etc/golems/caliban.json`; override with
`-config`, e.g. `caliban serve -config dev-config.json`.

## Configuration

All settings — including secrets — live in one `config.json`, kept outside the
workspace and the scrubbed shell environment. See
[`deploy/config.json.example`](deploy/config.json.example) for a full sample;
for local development, copy it to `dev-config.json` (gitignored).

Key fields:

- `db_path`, `workspace_path` — SQLite database and the git-backed workspace the
  shell operates in.
- `providers` — per-provider `api_key` (and optional `base_url`), e.g. DeepSeek
  or OpenRouter.
- `models.main` / `models.cheap` — the main reasoning model and a cheaper model
  for lightweight passes.
- `telegram` — bot `token`, `chat_id`, and the `conversation_id` it maps to.
- `web` — listen `addr` (empty disables the web transport), password `auth`, and
  Web Push (`vapid_*`/`subject`).
- `shell` — `timeout_seconds`, `max_output_bytes`, and `sandbox` (`require` to
  enforce the per-command Landlock sandbox).
- `context` — token budgets that drive compaction (`tail_budget_tokens`,
  `keep_recent_tokens`).
- `timezone`, `log_level`, `max_tool_iterations`.

## Transports

- **Telegram** — long-polled bot bound to a chat and conversation.
- **Web UI** — served on `web.addr` behind password auth, with optional Web Push
  notifications. Set the password with `caliban set-web-password` and generate
  push keys with `caliban generate-vapid-keys`.

## Tools & skills

The agent works through a small tool set: file-based memory, a sandboxed shell,
reminders and scheduled turns, managed background tasks, and trusted external
runners.

Skills follow a progressive-disclosure pattern: built-in skills are embedded in
the binary (from `internal/tools/builtin_skills/`), each a directory with a
`SKILL.md` carrying YAML `name`/`description` frontmatter. The system prompt
receives only the compact skill catalog; the agent reads a skill's full body
with `skill_read` when a task matches. Current skills:

- **runners** — use trusted external agent runners (`agy`, `codex`, `claude`,
  `pi`) through the `runner_*` tools for second opinions or independent
  investigation.
- **background-tasks** — start, inspect, and stop managed long-running
  shell/runner jobs.

## Background tasks

`shell` accepts `run_in_background: true` and returns a task id immediately
instead of blocking. Manage jobs with `task_list`, `task_output` (incremental,
by stored byte offset), and `task_stop`. Output is written to log files next to
the database; each job runs in its own process group and is killed as a group on
stop, timeout, or shutdown. Jobs do not survive a restart — tasks left running
by a previous process are reconciled to `lost` on startup.

## Self-maintenance

Between user turns Caliban does quiet upkeep: it compacts a conversation's
context when it grows past the configured budgets, and runs periodic persona
reflection. These passes are silent and never interrupt the user.

## Deploy

Caliban runs a shell driven by a semi-trusted model, so its security model is
**isolation, not a permission prompt**. Two layers enforce that:

1. The **systemd unit** ([`deploy/caliban.service`](deploy/caliban.service)) is
   the host boundary — a hardened sandbox (`ProtectSystem=strict`, no new
   privileges, dropped capabilities, a syscall allowlist, restricted address
   families, and resource caps). Secrets are passed via `LoadCredential`, not a
   world-readable path; persistent state lives under `StateDirectory=caliban`.
2. The per-command **Landlock sandbox** (`shell.sandbox=require`) stops the shell
   from reading Caliban's own secrets.

Deploy over SSH with:

```sh
make deploy CAL_HOST=<ssh-host> [CAL_CONFIG=path/to/config.json]
```

This builds, copies the binary, installs the unit, and (optionally) installs the
config as `/etc/golems/caliban.json` (keep it mode `0600 root:root`). The system
`caliban` user must exist:

```sh
useradd --system --no-create-home --shell /usr/sbin/nologin caliban
```

See [`deploy/deploy.sh`](deploy/deploy.sh) for the steps it runs.

## Layout

```
main.go            subcommand dispatch; `serve` wires config → store → engine → transports
internal/engine    the agent run loop, context assembly, compaction, free-time
internal/store     SQLite persistence (conversations, runs, messages, tasks)
internal/tools     shell, memory, reminders, background tasks, runners, skills
internal/telegram  Telegram transport
internal/web       web transport (serves the bundled webapp)
webapp/            web UI sources, bundled into the binary at build time
deploy/            systemd unit, config example, deploy script
```
