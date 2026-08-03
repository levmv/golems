package fetch

import (
	"strings"
	"testing"
)

func TestParseJinaFrontmatter(t *testing.T) {
	title, finalURL, text := parseJinaFrontmatter("---\ntitle: \"Example\"\nurl: \"https://example.com/a\"\n---\n\n# Body\nText")
	if title != "Example" {
		t.Fatalf("unexpected title: %q", title)
	}
	if finalURL != "https://example.com/a" {
		t.Fatalf("unexpected url: %q", finalURL)
	}
	if !strings.Contains(text, "# Body") {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestParseJinaPlainHeader(t *testing.T) {
	title, finalURL, text := parseJinaFrontmatter("Title: Example\nURL Source: https://example.com/a\n\nBody")
	if title != "Example" {
		t.Fatalf("unexpected title: %q", title)
	}
	if finalURL != "https://example.com/a" {
		t.Fatalf("unexpected url: %q", finalURL)
	}
	if text != "Body" {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestParseJinaPlainHeaderStopsAtUnknownField(t *testing.T) {
	title, finalURL, text := parseJinaFrontmatter("Title: Example\nAuthor: Alice\nBody")
	if title != "Example" {
		t.Fatalf("unexpected title: %q", title)
	}
	if finalURL != "" {
		t.Fatalf("unexpected url: %q", finalURL)
	}
	if text != "Author: Alice\nBody" {
		t.Fatalf("unexpected text: %q", text)
	}
}
