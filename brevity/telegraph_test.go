package main

import (
	"testing"

	"github.com/levmv/golems/brevity/internal/source"
)

func TestMarkdownishToTelegraphBuildsBasicNodes(t *testing.T) {
	nodes := markdownishToTelegraph("## Главное\n\n- первое\n- второе\n\nАбзац.")
	if len(nodes) != 3 {
		t.Fatalf("expected 3 top-level nodes, got %d", len(nodes))
	}
	if nodes[0].Tag != "h3" {
		t.Fatalf("expected h3, got %q", nodes[0].Tag)
	}
	if nodes[1].Tag != "ul" || len(nodes[1].Children) != 2 {
		t.Fatalf("expected ul with 2 items, got %#v", nodes[1])
	}
	if nodes[2].Tag != "p" {
		t.Fatalf("expected paragraph, got %q", nodes[2].Tag)
	}
}

func TestTelegraphContentOnlyIncludesFullSummary(t *testing.T) {
	nodes := telegraphContent(source.Document{FinalURL: "https://example.com"}, Summary{
		ShortSummary: "short summary should stay in Telegram",
		FullSummary:  "Full summary paragraph.",
	})

	text := telegraphNodesText(nodes)
	if containsText(text, "short summary should stay in Telegram") {
		t.Fatalf("telegraph content should not include short summary: %q", text)
	}
	if !containsText(text, "Full summary paragraph.") {
		t.Fatalf("telegraph content should include full summary: %q", text)
	}
}

func telegraphNodesText(nodes []telegraphNode) []string {
	var out []string
	var walk func(telegraphNode)
	walk = func(node telegraphNode) {
		if node.Text != "" {
			out = append(out, node.Text)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	return out
}

func containsText(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
