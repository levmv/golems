# Cy

Cy is a terminal coding agent built on the reusable `pkg/golem` and `pkg/llm`
packages. It can inspect and edit a workspace, run managed Bash commands, keep
resumable sessions, fetch public pages, and optionally use configured web
search providers.

## Install

Install the latest published release on Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/levmv/golems/main/cy/install.sh | sh
```

## Quick start

Run from the repository:

```bash
go run ./cy
```

Or install the binary:

```bash
go install ./cy
cy
```

The default model is `deepseek/deepseek-v4-flash`. Cy can open the interactive
UI without an API key; use the provider picker or `/login` to add one. For
automation, provider environment variables take precedence over stored keys:

```bash
export DEEPSEEK_API_KEY="..."
# OPENAI_API_KEY and OPENROUTER_API_KEY are also supported.
# TAVILY_API_KEY, EXA_API_KEY, and FIRECRAWL_API_KEY configure web services.
```

Credentials can also be managed from a terminal:

```bash
cy login deepseek
cy logout deepseek
cy login                 # show provider status
```

`/login` groups model providers separately from optional services. Tavily and
Exa enable web search; Firecrawl and Exa add richer page-fetch fallbacks. Keys
take effect without restarting Cy:

```bash
cy login tavily
cy login exa
cy login firecrawl
# or use TAVILY_API_KEY / EXA_API_KEY / FIRECRAWL_API_KEY for automation
```

List known/recent models or save a new default with:

```bash
cy model
cy model openrouter/moonshotai/kimi-k3
```

Selected custom models remain in the list. The built-in suggestions stay short:
the two DeepSeek defaults, OpenRouter's free router, and three rolling `latest`
aliases. In the interactive `/model` picker, Up/Down selects a model and
Left/Right selects its reasoning effort when Cy knows the model supports it.
`default` sends no effort override; the current DeepSeek V4 entries also offer
`high`.

Local Ollama models do not require a stored key:

```bash
cy -model ollama/your-model-name
```

## Usage

With no prompt, Cy starts its full-screen interactive UI. A positional prompt
or piped input runs one turn and exits:

```bash
cy "Review the current changes"
printf '%s\n' "Summarize this repository" | cy
```

One-shot stdout contains only the final assistant reply. `-v` writes compact
tool activity, retries, and usage to stderr:

```bash
result=$(cy "Return the package list as JSON")
cy -v "Run the focused tests"
```

Use `-json` to receive one versioned JSON object after a successful turn. It
contains the final reply, usage, finish reason, and a session ID when the run is
saved. With `-v`, progress still goes to stderr:

```bash
cy -json "Inspect the workspace and report what you checked" | jq -r .reply
```

Interactive sessions are durable. Ordinary one-shot runs use temporary state;
add `-save-session` when the result may need a follow-up:

```bash
cy -save-session "Investigate the flaky test"
cy resume 01234567
cy resume 01234567 "Continue the investigation"
```

Session IDs may be abbreviated to a unique prefix.

## Interactive commands

```text
/help
/clear
/resume [session-id-or-prefix]
/usage
/context
/compact [focus]
/login [provider]
/logout <provider>
/model [provider/model]
/profile [read-only|edit|full]
/exit
```

Typing `/` opens command completion. Use Up/Down to select, Tab to complete,
and Enter to run. Shift+Enter inserts a newline. At the first or last editor
line, Up/Down browses input history. PageUp/PageDown, Ctrl+Up/Ctrl+Down,
Ctrl+Home/Ctrl+End, and the mouse wheel scroll the transcript. Hold Shift for
terminal-native mouse selection and copying.

While Cy is working, Enter queues input for the next model boundary. Ctrl+C
cancels active work and exits when idle; Escape cancels and restores any queued
text that was not delivered.

## Configuration

Flags override environment variables, which override saved defaults.

| Flag | Environment | Default | Purpose |
| --- | --- | --- | --- |
| `-model` | `CY_MODEL` | `deepseek/deepseek-v4-flash` | Model URI in `provider/model` form |
| `-root` | `CY_ROOT` | `.` | Workspace available to tools |
| `-home` | `CY_HOME` | `~/.cy` | Credentials, settings, sessions, and tool state |
| `-profile` | `CY_PROFILE` | `full` | Tool capability profile |
| `-sandbox` | `CY_SANDBOX` | `auto` | Bash sandbox policy: `auto`, `require`, or `off` |

Other useful flags are `-v`, `-json`, `-save-session`, and `-version`. Run
`cy -help` for the complete list.

Cy keeps saved model/profile defaults, recent model choices, and discovered
model context windows in `config.json`, and API keys in the private `auth.json`.
Both live under `CY_HOME`, which defaults to `~/.cy`. Sessions and tool state use
subdirectories of the same home.

Color can be controlled independently:

```bash
export CY_COLOR=always   # auto, always, or never
export NO_COLOR=1
```

## Tools and capability profiles

Cy provides these workspace tools:

- `read`, `grep`, and `glob` for bounded discovery;
- `edit` and `write` for atomic UTF-8 file changes;
- `bash` and `job` for managed commands and their output;
- `web_fetch` for public pages, with configured extraction services preferred
  and a direct reader available as the no-configuration fallback;
- `hacker_news` for current feeds and bounded discussion threads;
- `web_search` when at least one search service credential is configured.

Paths are resolved beneath the workspace root. Absolute paths, `..` escapes,
and symlinks that resolve outside the workspace are rejected.

Profiles remove whole tool classes from the model-visible catalog:

| Profile | Available tools |
| --- | --- |
| `full` | Files, Bash/jobs, and available web tools |
| `edit` | Read/write files and available web tools; no Bash |
| `read-only` | Read/search and available web tools; no writes or Bash |

`web_fetch` resolves Hacker News discussion URLs through its dedicated reader.
For other pages it tries configured extraction backends as Firecrawl → Exa,
then falls back to the bounded stateless HTTP reader. Search supports Tavily and
Exa, tried as Tavily → Exa. Service endpoints are built in, so only API keys are
required:

```bash
cy login tavily
cy login exa
cy login firecrawl
# or use environment overrides:
export TAVILY_API_KEY="..."
export EXA_API_KEY="..."
export FIRECRAWL_API_KEY="..."
```

The first successful non-empty provider response wins.

Fetched pages and search results are bounded and marked as untrusted. Fetching
rejects URL credentials, non-HTTP schemes, loopback, private, and link-local
destinations, including redirects and DNS resolutions.

`hacker_news` needs no account and supports `top`, `new`, `best`, `show`,
`search`, and `thread` views. Feeds and threads use the official Hacker News
API; full-text story search uses the public Algolia-powered HN Search API and
can be ordered by relevance or date. Feed and search results include ranking
metadata and timestamps; thread results preserve a bounded ranked comment
tree. Fetching a `news.ycombinator.com/item?id=...` URL through
`web_fetch` uses the same thread reader automatically. The linked article is
returned as `article_url` and is fetched separately only when the agent needs
its contents.

## Sessions and context

Sessions are append-only JSONL journals under `$CY_HOME/sessions`. `/resume`
shows sessions for the current workspace, while `/clear` starts a new session
without deleting the previous one. A session permits only one writer at a
time. Journals are private files, but they contain prompts, model responses,
tool calls, and tool results verbatim and must be treated as secret-bearing
artifacts when copied, shared, or backed up.

Cy loads the applicable `AGENTS.md` chain from the workspace root to the current
working directory when building a session runtime. Context budgeting includes
the system prompt, project instructions, tool schemas, summaries, history, and
pending input. OpenRouter context windows are discovered from its model
metadata API on first use and cached; selecting a model explicitly refreshes
the value. Dynamic `~...-latest` aliases and the free router are also refreshed
at startup because their backing models can change. If lookup and the built-in
catalog cannot resolve a model, Cy uses a visibly estimated 128K window.

`/context` shows the current budget. `/compact [focus]` records an additive
summary while preserving recent complete conversation and tool blocks. Before
automatic compaction, Cy checks whether shortening large tool results outside
the recent verbatim tail is sufficient; the provider sees a stable head/tail
representation while the full result remains in the append-only journal. If
that is not enough, normal compaction runs immediately. Cy also retries
transient provider and stream failures within a bounded retry budget;
authentication, policy, invalid-request, and missing-model failures stop
immediately.

## Security

On Linux, the default `CY_SANDBOX=auto` attempts to run Bash under Landlock.
`require` fails startup when the sandbox is unavailable; `off` uses the ambient
user filesystem permissions. Tool processes receive a scrubbed environment and
a workspace-specific tool home, so provider credentials are not inherited.

Landlock restricts filesystem access only. It does not make commands
semantically safe and does not block network access; the interactive startup
line reports the effective sandbox and network state.

## Development

The root `cy` package contains CLI wiring and session lifecycle. The main
internals are separated by responsibility:

- `internal/engine` — model/tool execution and context management;
- `internal/session` — durable journals and replay;
- `internal/state` — credentials and saved defaults;
- `internal/tools` — workspace, process, and sandbox tools;
- `internal/ui` — console rendering and the interactive TUI.

Run the focused checks with:

```bash
go test ./cy/...
go test -race ./cy/...
go vet ./cy/...
```
