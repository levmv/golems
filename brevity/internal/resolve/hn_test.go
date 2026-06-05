package resolve

import (
	"net/url"
	"strings"
	"testing"
)

func TestHNMatch(t *testing.T) {
	resolver := NewHN(nil)
	parsed, err := url.Parse("https://news.ycombinator.com/item?id=123")
	if err != nil {
		t.Fatal(err)
	}
	if !resolver.Match(parsed) {
		t.Fatal("expected HN item URL to match")
	}

	parsed, err = url.Parse("https://news.ycombinator.com/news")
	if err != nil {
		t.Fatal(err)
	}
	if resolver.Match(parsed) {
		t.Fatal("expected non-item HN URL not to match")
	}
}

func TestRenderCommentKeepsTreeShape(t *testing.T) {
	var sb strings.Builder
	renderComment(&sb, hnComment{
		By:   "alice",
		Text: "root\ncomment",
		Children: []hnComment{{
			By:   "bob",
			Text: "reply",
		}},
	}, 0)

	out := sb.String()
	if !strings.Contains(out, "- alice: root comment") {
		t.Fatalf("missing root comment: %q", out)
	}
	if !strings.Contains(out, "  - bob: reply") {
		t.Fatalf("missing child comment: %q", out)
	}
}
