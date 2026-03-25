# Telegram Go Bot API 

Simple Telegram Bot API wrapper for Go. 

**Key Features:**
- **Per-Chat Concurrency:** Updates are processed concurrently across different chats, but *sequentially* within a single chat.
- **LLM Ready:** Built-in standard-Markdown to Telegram-HTML conversion, automatic message chunking for large outputs (>4000 chars), and live message streaming.
- **Robust:** Built-in panic recovery per handler, automatic fallback for malformed markdown, and automated typing indicators.

---

## 🚀 Quick Start

```go
package main

import (
	"context"
	"log"
	"net/http"
	"github.com/levmv/golems/pkg/telegram"
)

func main() {
	// Initialize bot
	bot, err := telegram.New("YOUR_BOT_TOKEN", "YOUR_WEBHOOK_SECRET", telegram.WithDebug())
	if err != nil {
		log.Fatal(err)
	}

	// Register handlers
	bot.OnCommand("start", func(c *telegram.Context) error {
		return c.Reply("Hello!")
	})

	bot.OnText(func(c *telegram.Context) error {
		// Auto-chunks long text and converts standard Markdown to HTML
		_, err := c.ReplyChunked("**Bold text** and some standard markdown")
		return err
	})

	ctx := context.Background()
	
	// Option A: Long-polling (Local Dev)
	bot.StartPolling(ctx) 
	
	// Option B: Webhook (Production)
	// http.HandleFunc("/webhook", bot.WebhookHandler())
	// go http.ListenAndServe(":8080", nil)
	// bot.Start(ctx) 
}
```

---

## 📚 API Reference

### 1. Initialization & Options
```go
bot, err := telegram.New(token string, webhookSecret string, options ...telegram.Option)
```
**Options:**
- `telegram.WithDebug()`: Enables verbose request logging.
- `telegram.WithLogger(logger Logger)`: Inject custom logger.
- `telegram.WithAuthFunc(f AuthFunc)`: Global middleware `func(c *telegram.Context) (allow bool, reason string)`.
- `telegram.WithHTTPClient(client HttpClient)`: Custom HTTP client.
- `telegram.WithServerURL(url string)`: Custom Telegram Bot API server URL.

### 2. Handlers & Routing
The router exact-matches commands or types. Handlers receive a `*telegram.Context`.
```go
bot.OnCommand("start", handler)
bot.OnText(handler)             // Matches ANY text that is NOT a command
bot.OnCallback("data", handler) // Matches specific callback_data, or "" for all
bot.OnPhoto(handler)
bot.OnDocument(handler)
bot.OnEditedMessage(handler)
bot.OnAny(handler)              // Catch-all
```

### 3. Context (`*telegram.Context`)
Wraps `context.Context` and the incoming update.
- `c.Bot`: Access to the parent `*Bot` instance.
- `c.Update`: Raw `*telegram.Update`.
- `c.ChatID` (`int64`): ID of the current chat.
- `c.SenderID` (`int64`): ID of the user who sent the message.
- `c.Command` (`string`): Extracted command without the slash or bot name (e.g., `start`).
- `c.Text() string`: Returns message text (handles standard and edited messages).
- `c.Reply(text string) error`: Sends standard text back to `c.ChatID`.
- `c.ReplyChunked(text string) ([]*telegram.Message, error)`: Safely parses standard LLM Markdown, converts to HTML, and splits into multiple messages if length > 4000 chars.

### 4. LLM Streaming & Long Responses

**Message Chunking:**
LLMs output standard Markdown and long texts. Telegram requires HTML or MarkdownV2 and limits to 4096 characters.
```go
// Safely converts Markdown -> HTML, avoids breaking code blocks, and splits >4000 chars.
messages, err := c.ReplyChunked("... very long LLM output ...") 
```

**Live Message Streaming:**
For token-by-token streaming. *Note: Uses `sendMessageDraft` API extension. Throttles network requests to max 1 per 500ms automatically.*
```go
draft := bot.StartMessageStream(c, c.ChatID, 12345) // 12345 = arbitrary draft ID
defer draft.Close(c) // Closes draft, finalizes markdown, and converts to regular message

draft.Write(c, "Tokens ")
draft.Write(c, "arriving ")
draft.Write(c, "live...")
```

**Typing Indicators:**
Run in a goroutine/defer while LLM is thinking.
```go
indicator := bot.StartTypingIndicator(ctx, chatID)
defer indicator.Stop() // Stops the recurring typing action
```

### 5. Sending Files
Files are handled via the `InputFile` struct. No manual multipart building required.
```go
// Available constructors:
telegram.FileFromDisk("path/to/file.jpg")
telegram.FileFromBytes("image.png", byteSlice)
telegram.FileFromReader("data.csv", ioReader)
telegram.FileFromID("AgADBAAD...") // Existing Telegram file_id
telegram.FileFromURL("https://...")

// Usage:
_, err := bot.SendPhoto(ctx, &telegram.SendPhotoParams{
    ChatID: chatID,
    Photo:  telegram.FileFromDisk("cat.jpg"),
})
```
*Note: Downloading files is done via `data, err := bot.DownloadFile(ctx, fileID)`.*

### 6. Keyboards
```go
// Inline Keyboard
markup := telegram.NewInlineKeyboardMarkup().AddRow(
    telegram.NewInlineKeyboardButton("Click Me").WithCallback("btn_click"),
    telegram.NewInlineKeyboardButton("Google").WithURL("https://google.com"),
)

c.Bot.SendMessage(c, &telegram.SendMessageParams{
    ChatID:      c.ChatID,
    Text:        "Choose an option:",
    ReplyMarkup: markup,
})

// Reply Keyboard (Custom Keyboard)
replyMarkup := telegram.NewReplyKeyboardMarkup([][]telegram.KeyboardButton{
    {telegram.NewKeyboardButton("Option 1"), telegram.NewKeyboardButton("Option 2")},
})
```

### 7. Major Bot Methods
```go
func (b *Bot) SendMessage(ctx context.Context, params *SendMessageParams) (*Message, error)
func (b *Bot) EditMessageText(ctx context.Context, params *EditMessageTextParams) (*Message, error)
func (b *Bot) AnswerCallbackQuery(ctx context.Context, params *AnswerCallbackQueryParams) (bool, error)
func (b *Bot) SendPhoto(ctx context.Context, params *SendPhotoParams) (*Message, error)
func (b *Bot) SendDocument(ctx context.Context, params *SendDocumentParams) (*Message, error)
func (b *Bot) DeleteMessages(ctx context.Context, chatID int64, messageIDs []int) (bool, error)
func (b *Bot) SetMyCommands(ctx context.Context, req SetMyCommandsRequest) (bool, error)
```

### 8. Common Markdown Utils
- `telegram.LLMMarkdownToHTML(text string) string`: Converts standard LLM markdown to Telegram-safe HTML. Preserves ```` ``` ```` fenced code blocks.
- `telegram.EscapeMarkdown(text string) string`: Escapes standard text for Telegram's strict `MarkdownV2`.