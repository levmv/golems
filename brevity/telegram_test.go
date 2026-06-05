package main

import (
	"testing"

	"github.com/levmv/golems/pkg/telegram"
)

func TestExtractFirstURLFromMentionedMessage(t *testing.T) {
	msg := &telegram.Message{Text: "@brevitybot https://example.com/article"}
	got := ExtractFirstURL(msg)
	if got != "https://example.com/article" {
		t.Fatalf("unexpected URL: %q", got)
	}
}

func TestExtractFirstURLFromCommandMessage(t *testing.T) {
	msg := &telegram.Message{Text: "/s@brevitybot https://example.com/article"}
	got := ExtractFirstURL(msg)
	if got != "https://example.com/article" {
		t.Fatalf("unexpected URL: %q", got)
	}
}

func TestAuthAllowsWhitelistedChatWithoutSender(t *testing.T) {
	adapter := &TelegramAdapter{whitelist: []int64{-100123}}
	allowed, reason := adapter.auth(&telegram.Context{ChatID: -100123})
	if !allowed {
		t.Fatalf("expected whitelisted chat to pass, reason=%s", reason)
	}
}
