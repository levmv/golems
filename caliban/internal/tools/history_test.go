package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/llm"
)

type fakeHistorySearcher struct {
	query string
	limit int
}

func (s *fakeHistorySearcher) SearchHistory(_ context.Context, query string, limit int) ([]HistorySearchResult, error) {
	s.query = query
	s.limit = limit
	return []HistorySearchResult{{
		ID:        12,
		Role:      "user",
		Source:    "web",
		Text:      "We discussed Caliban PWA push notifications.",
		CreatedAt: time.Date(2026, 6, 19, 12, 30, 0, 0, time.UTC),
	}}, nil
}

func TestHistorySearchTool(t *testing.T) {
	searcher := &fakeHistorySearcher{}
	tool := History(searcher)[0]

	out := runToolForTest(t, tool, historySearchArgs{Query: " Caliban PWA ", Limit: 50})

	if searcher.query != "Caliban PWA" || searcher.limit != maxHistorySearchLimit {
		t.Fatalf("unexpected search args: query=%q limit=%d", searcher.query, searcher.limit)
	}
	if !strings.Contains(out, "#12 2026-06-19 12:30 user/web") || !strings.Contains(out, "push notifications") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestHistorySearchToolRequiresQuery(t *testing.T) {
	tool := History(&fakeHistorySearcher{})[0]
	raw, err := json.Marshal(historySearchArgs{Query: " "})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Run(context.Background(), llm.ToolCall{
		Function: llm.ToolFunction{Name: tool.Definition.Function.Name, Arguments: string(raw)},
	})
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("expected query error, got %v", err)
	}
}
