package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/levmv/golems/brevity/internal/source"
)

const browserCommandTimeout = 2 * time.Minute

type BrowserCommand struct {
	name string
	args []string
}

type BrowserCommandResponse struct {
	OK          bool   `json:"ok"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	Text        string `json:"text,omitempty"`
	ContentType string `json:"content_type,omitempty"`

	NeedsHuman bool   `json:"needs_human,omitempty"`
	Reason     string `json:"reason,omitempty"`
	BrowserURL string `json:"browser_url,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

func NewBrowserCommand(name string, args ...string) *BrowserCommand {
	return &BrowserCommand{name: name, args: args}
}

func (c *BrowserCommand) Fetch(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := validateHTTPURL(rawURL)
	if err != nil {
		return source.Document{}, err
	}
	if strings.TrimSpace(c.name) == "" {
		return source.Document{}, fmt.Errorf("browser fetch command is empty")
	}

	cmdCtx, cancel := context.WithTimeout(ctx, browserCommandTimeout)
	defer cancel()

	args := append(append([]string{}, c.args...), parsed.String())
	cmd := exec.CommandContext(cmdCtx, c.name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, runErr := cmd.Output()
	resp, parseErr := parseBrowserCommandResponse(out)
	if parseErr == nil {
		return responseToDocument(parsed.String(), resp)
	}
	if runErr != nil {
		if stderr.Len() > 0 {
			return source.Document{}, fmt.Errorf("browser fetch command failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return source.Document{}, fmt.Errorf("browser fetch command failed: %w", runErr)
	}
	return source.Document{}, parseErr
}

func parseBrowserCommandResponse(out []byte) (BrowserCommandResponse, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return BrowserCommandResponse{}, fmt.Errorf("browser fetch command returned empty output")
	}

	var resp BrowserCommandResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return BrowserCommandResponse{}, fmt.Errorf("decode browser fetch command JSON: %w", err)
	}
	return resp, nil
}

func responseToDocument(originalURL string, resp BrowserCommandResponse) (source.Document, error) {
	if resp.NeedsHuman {
		return source.Document{}, &NeedsHumanError{
			URL:        firstNonEmpty(resp.URL, originalURL),
			SessionID:  resp.SessionID,
			BrowserURL: resp.BrowserURL,
			Reason:     resp.Reason,
		}
	}
	if !resp.OK {
		if strings.TrimSpace(resp.Error) != "" {
			return source.Document{}, fmt.Errorf("browser fetch command returned error: %s", resp.Error)
		}
		return source.Document{}, fmt.Errorf("browser fetch command returned ok=false")
	}

	text := trimRunes(strings.TrimSpace(resp.Text), maxSourceChars)
	if text == "" {
		return source.Document{}, fmt.Errorf("browser fetch command returned empty text")
	}

	finalURL := firstNonEmpty(resp.URL, originalURL)
	contentType := firstNonEmpty(resp.ContentType, "text/markdown; source=browser-command")
	return source.Document{
		URL:         originalURL,
		FinalURL:    finalURL,
		Title:       strings.TrimSpace(resp.Title),
		Text:        text,
		ContentType: contentType,
		FetchedAt:   time.Now(),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
