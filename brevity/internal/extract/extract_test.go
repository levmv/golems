package extract

import (
	"strings"
	"testing"
)

func TestReadableTextPrefersArticle(t *testing.T) {
	raw := `<html><head><title>Original title</title></head><body><nav>menu</nav><article><h1>Hello</h1><p>Main text&nbsp;here.</p></article></body></html>`

	result := ReadableText(raw, "text/html")
	if result.Title != "Original title" {
		t.Fatalf("unexpected title: %q", result.Title)
	}
	if !strings.Contains(result.Text, "Hello") || !strings.Contains(result.Text, "Main text here.") {
		t.Fatalf("expected article text, got %q", result.Text)
	}
	if strings.Contains(result.Text, "menu") {
		t.Fatalf("expected navigation to be dropped, got %q", result.Text)
	}
}
