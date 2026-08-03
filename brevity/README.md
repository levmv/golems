# Brevity

Telegram bot that accepts a URL and returns two Russian summaries:

- a short version suitable for one Telegram post;
- a fuller version published to Telegra.ph, with the link attached to the short post.

The core service is intentionally small and not tied to Telegram: fetch URL -> extract readable text -> summarize -> optionally publish.

## Configuration

Runtime environment is kept to things that are actually secrets or deployment-specific:

```bash
export TELEGRAM_BOT_TOKEN="123:abc"
export TELEGRAM_WHITELIST="123456789,-1001234567890"

export BREVITY_MODEL="deepseek/deepseek-v4-flash"
export DEEPSEEK_API_KEY="..."

export TELEGRAPH_ACCESS_TOKEN="..."
export TELEGRAPH_AUTHOR_NAME="Brevity"
```

Optional deployment knobs:

```bash
export WEBHOOK_URL="https://example.com/brevity"
export WEBHOOK_SECRET_TOKEN="random-secret"
export PORT="8443"
```

Without `WEBHOOK_URL`, the bot runs in polling mode.

Model temperature, token limits, fetch limits, and timeouts are constants in code for now. They can become config later when there is a real reason.

## Telegraph Account

Create a Telegraph token once:

```bash
go run ./brevity -create-telegraph-account -telegraph-short-name Brevity
```

Save the printed `TELEGRAPH_ACCESS_TOKEN` in the bot environment. If the token is not configured, Brevity still works, but sends the full summary back to Telegram instead of publishing it.

## Fetching

Default reading goes through `internal/resolve`. Site-specific resolvers can recognize URLs before fetching. The fallback fetch path is direct HTTP plus `internal/extract.Regex`, then Jina Reader if the direct result is empty/thin.

There are also code-level fallback hooks for external tools. `externalFetcherCommand` is a plain-text stdout reader:

```go
var externalFetcherCommand = []string{"reader", "--format", "text"}
```

Brevity tries the built-in HTTP fetcher first, then Jina Reader, then external commands if configured and the result still looks too thin.

For browser automation with human-in-the-loop CAPTCHA handling, set `browserFetcherCommand` instead. It must return JSON:

```json
{
  "ok": true,
  "title": "Article title",
  "url": "https://example.com/final",
  "text": "Readable page text"
}
```

If manual action is needed, return:

```json
{
  "ok": false,
  "needs_human": true,
  "reason": "captcha",
  "browser_url": "https://brevity.example.com/browser/session/abc",
  "session_id": "abc"
}
```

In that case the Telegram bot sends a button to open the browser session. After solving the challenge, send the same article URL again.

Hacker News item URLs are handled by `internal/resolve.HN`: Brevity fetches the linked article, loads a bounded slice of HN comments through the official HN API, and turns both into one synthetic document for the summarizer.

## Run

```bash
go run ./brevity
```

Then send the bot a URL. If your user is not in `TELEGRAM_WHITELIST`, `/start` will show your Telegram user ID.
