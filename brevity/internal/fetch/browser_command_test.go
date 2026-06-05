package fetch

import (
	"errors"
	"strings"
	"testing"
)

func TestBrowserCommandResponseToDocument(t *testing.T) {
	doc, err := responseToDocument("https://example.com/a", BrowserCommandResponse{
		OK:    true,
		Title: "Example",
		URL:   "https://example.com/final",
		Text:  "Body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Example" || doc.FinalURL != "https://example.com/final" || doc.Text != "Body" {
		t.Fatalf("unexpected doc: %#v", doc)
	}
}

func TestBrowserCommandResponseNeedsHuman(t *testing.T) {
	_, err := responseToDocument("https://example.com/a", BrowserCommandResponse{
		NeedsHuman: true,
		Reason:     "captcha",
		BrowserURL: "https://browser/session/1",
		SessionID:  "1",
	})
	var human *NeedsHumanError
	if !errors.As(err, &human) {
		t.Fatalf("expected NeedsHumanError, got %v", err)
	}
	if human.BrowserURL != "https://browser/session/1" || human.SessionID != "1" {
		t.Fatalf("unexpected human error: %#v", human)
	}
}

func TestParseBrowserCommandResponse(t *testing.T) {
	resp, err := parseBrowserCommandResponse([]byte(`{"ok":true,"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Text != "hello" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestParseBrowserCommandResponseRejectsPlainText(t *testing.T) {
	_, err := parseBrowserCommandResponse([]byte("hello"))
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}
