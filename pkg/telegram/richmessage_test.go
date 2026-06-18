package telegram

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRichMessageWorksAsTextInput(t *testing.T) {
	raw := []byte(`{
		"update_id": 1,
		"message": {
			"message_id": 10,
			"date": 1,
			"chat": {"id": 42, "type": "private"},
			"rich_message": {
				"blocks": [
					{"type": "paragraph", "text": ["hello ", {"type": "bold", "text": "world"}]}
				]
			}
		}
	}`)

	var upd Update
	if err := json.Unmarshal(raw, &upd); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	c := &Context{Update: &upd}

	if got := c.Text(); got != "hello world" {
		t.Fatalf("Text() = %q, want %q", got, "hello world")
	}
	if !(&route{routeType: RouteText}).match(c) {
		t.Fatal("RouteText did not match rich-only message")
	}
}

func TestSendRichMarkdownFallsBackOnBadRequest(t *testing.T) {
	client := &recordingClient{responses: []string{
		`{"ok":false,"error_code":400,"description":"Bad Request: can't parse rich message"}`,
		`{"ok":true,"result":{"message_id":11,"date":1,"chat":{"id":42,"type":"private"},"text":"fallback"}}`,
	}}
	b := testBot(client)

	msgs, err := b.SendRichMarkdown(t.Context(), 42, "# bad rich")
	if err != nil {
		t.Fatalf("SendRichMarkdown returned error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want fallback result only", len(msgs))
	}
	if got := client.methods(); strings.Join(got, ",") != "sendRichMessage,sendMessage" {
		t.Fatalf("methods = %v, want sendRichMessage then sendMessage", got)
	}
}

func TestSendRichMarkdownDoesNotFallBackOnRateLimit(t *testing.T) {
	client := &recordingClient{responses: []string{
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`,
	}}
	b := testBot(client)

	if _, err := b.SendRichMarkdown(t.Context(), 42, "# rate limited"); err == nil {
		t.Fatal("SendRichMarkdown returned nil error")
	}
	if got := client.methods(); strings.Join(got, ",") != "sendRichMessage" {
		t.Fatalf("methods = %v, want only sendRichMessage", got)
	}
}

type recordingClient struct {
	responses []string
	requests  []string
}

func (c *recordingClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req.URL.Path)
	body := `{"ok":true,"result":true}`
	if len(c.responses) > 0 {
		body = c.responses[0]
		c.responses = c.responses[1:]
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}, nil
}

func (c *recordingClient) methods() []string {
	out := make([]string, 0, len(c.requests))
	for _, path := range c.requests {
		if idx := strings.LastIndex(path, "/"); idx != -1 {
			out = append(out, path[idx+1:])
			continue
		}
		out = append(out, path)
	}
	return out
}

func testBot(client HttpClient) *Bot {
	return &Bot{
		url:    "https://api.telegram.org",
		token:  "123:test",
		client: client,
		logger: defaultLogger{},
	}
}
