# Hugin

**Hugin** is a lightweight, SSH-based monitoring tool with AI-first analysis and low-noise alerting.

It is designed for small personal infrastructure: homeservers, VPS machines, small websites, side projects, and custom operational scripts.

Hugin runs locally, connects to remote machines over SSH (or runs directly on localhost), executes allowlisted collector scripts, stores the results, gives the data and local context to an LLM, and alerts you only when the situation looks meaningful. 

The core idea is simple:

> Collect facts with scripts. Give the facts, history, and local notes to an AI analyst. Let it decide whether you should care.

---

## Why Hugin?

Traditional monitoring often starts with thresholds and rules:
```text
if disk_used > 95% then critical
if rps > 100 then warning
if memory_used > 90% then alert
```

That works, but it quickly becomes noisy and brittle for small personal setups. Real infrastructure often needs local knowledge:
```text
/var is normally around 80-86% full on this server. 
That is okay if it is stable. 
Alert only if it starts growing quickly or free space becomes very low.
```

Hugin is built around this kind of operator knowledge. Instead of writing many hardcoded rules, you write simple collectors and notes. The LLM receives current data, recent history, previous incidents, and your notes, then decides whether the situation is normal, suspicious, or urgent. 

If the LLM is unavailable, Hugin treats that as an operational problem of its own instead of guessing with a second rules engine.

---

## Design Goals

- **Flexible Execution (Cron or Daemon)**
  - Hugin can act statelessly. You can run `hugin run-due` via a standard system cron job or systemd timer.
  - Alternatively, use `hugin daemon` for a persistent, long-running background process.
- **Fast & Efficient**
  - Checks are executed concurrently.
  - SSH connections are pooled and multiplexed. Multiple checks on the same server share a single connection, making execution extremely fast and reducing audit log noise.
- **AI-first Analysis**
  - The LLM is the primary judgment engine.
  - If AI analysis is unavailable, Hugin opens a separate analysis incident instead of silently accepting or inventing a rule-based decision.
- **Agentless Remote & Local Execution**
  - Remote servers only need SSH and collector scripts.
  - The local machine (where Hugin runs) can be monitored directly, bypassing SSH.
- **Safe by default**
  - SSH keys should be restricted to allowlisted collector commands.
- **Script-friendly**
  - Collectors can be written in Bash, Python, Go, or anything else. A collector only needs to print JSON to stdout.
- **Local-first storage**
  - SQLite manages runs, metrics, LLM decisions, alerts, incidents, and user notes. Built-in data retention policies prevent database bloat.
- **Low-noise alerting**
  - Hugin tracks incident state and alert cooldowns.

---

## Non-Goals

Hugin is not trying to replace Prometheus, Grafana, Alertmanager, Datadog, or a full observability platform. 

It is intended to be a small personal monitoring assistant for situations where you already have custom scripts, want contextual analysis instead of endless thresholds, and want a tool that fits gracefully on a single homeserver.

---

## Core Concepts

### Target
A machine Hugin monitors. This can be a remote machine accessed via SSH, or `localhost` executed directly.

### Collector
A remote or local command that gathers facts and prints JSON. Collectors should be dumb. They measure and summarize, but do not decide whether the situation is urgent.

### Check
A configured scheduled execution of a collector against a target.

### Run
One execution of a check, including timestamps, raw output, parsed metrics, LLM analysis, and the alert decision.

### Note
Human-provided local context that helps the LLM make better decisions. **Notes are stored exclusively in SQLite and managed via the CLI**, keeping your YAML config clean and preventing state-drift.

### Incident
An ongoing abnormal condition. Hugin groups repeated abnormal runs into one incident instead of sending a new alert every time.

---

## Collector Contract

Collectors return structured JSON to stdout.

Minimal example:
```json
{
  "check": "disk",
  "status": "ok",
  "metrics": {
    "root_used_pct": 71.2,
    "var_used_pct": 84.8
  }
}
```

**Structured Errors:**
If a collector fails, it must provide a structured `errors` array so the LLM knows exactly why it failed, preventing hallucinations.
```json
{
  "check": "db_backup",
  "status": "error",
  "errors": [
    {
      "code": "FILE_NOT_FOUND",
      "message": "Backup manifest /var/backups/latest.json is missing."
    }
  ]
}
```

Recommended fields:
```text
check         string, collector/check name
status        ok | error | partial
metrics       object with numeric/string/bool values ONLY
errors        optional list of structured error objects (code, message)
window        optional collection window, such as 15m or 1h
```

---

## AI-First Analysis

Hugin builds a compact case file for the LLM containing current metrics, recent history bounds, and your CLI-provided operator notes. The LLM responds with strict JSON outlining `severity`, `should_alert`, `summary`, and `evidence`.

**Analysis availability:**
Hugin does not include a fallback expression language. Collectors report facts, and the AI makes judgments. If AI analysis cannot run, Hugin opens an urgent analysis incident for that check so the failure is visible and cooldown-controlled.
```yaml
    analysis:
      mode: ai
      include_history: 7d
```

---

## CLI Usage

```bash
# Execute a single check manually and analyze results
hugin run disk_web1

# Run all scheduled checks that are currently due (ideal for cron / systemd timers)
hugin run-due

# Run continuously in the background (ideal for docker / systemd services)
hugin daemon

# Add an operator note (stored in SQLite)
hugin note disk_web1 "80-86% disk usage is normal if stable."

# Show recent runs
hugin runs disk_web1

# Manually resolve an incident
hugin resolve inc-disk_web1-1712345678

# Validate configuration
hugin validate

# Clean up old runs (14-day retention)
hugin cleanup
```

---

## Example Configuration (`hugin.yaml`)

```yaml
app:
  data_dir: /var/lib/hugin
  timezone: Europe/Amsterdam

llm:
  provider: openai
  model: gpt-4o-mini
  temperature: 0
  max_input_runs: 50

targets:
  web1:
    host: web1.example.com
    user: hugin
    key: ~/.ssh/hugin_web1
  local:
    host: localhost

checks:
  - id: disk_web1
    target: web1
    command: /usr/local/bin/collect_disk.sh
    schedule: "*/15 * * * *"
    timeout: 10s
    analysis:
      mode: ai
      include_history: 7d
    alert:
      cooldown: 2h
      repeat_after: 12h
      notify_on_resolved: true

  - id: local_health
    target: local
    command: hugin_health_check
    schedule: "*/5 * * * *"
    timeout: 5s
    analysis:
      mode: ai
    alert:
      cooldown: 1h

# Note: Operator notes are managed via the CLI (e.g., `hugin note web1 disk "Normal if stable"`) 
# and are intentionally excluded from the YAML to prevent state-drift.

notifiers:
  telegram:
    enabled: true
    bot_token_env: HUGIN_TELEGRAM_TOKEN
    chat_id_env: HUGIN_TELEGRAM_CHAT_ID
```
