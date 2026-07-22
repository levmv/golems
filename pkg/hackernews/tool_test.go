package hackernews

import (
	"context"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func TestToolReadsFeedsAndMarksOutputUntrusted(t *testing.T) {
	client, closeServer := testClient(t, []int64{1}, map[int64]apiItem{
		1: {ID: 1, Type: "story", Title: "Current story", URL: "https://example.com/story", Score: 12, Descendants: 4},
	})
	defer closeServer()
	tool := NewTool(client)
	if tool.Definition.Function.Name != "hacker_news" || tool.Effect != golem.ToolEffectExternal {
		t.Fatalf("tool = %#v", tool)
	}
	result, err := tool.Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: `{"view":"top","limit":1}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "UNTRUSTED HACKER NEWS DATA") || !strings.Contains(result.Content, "Current story") || !strings.Contains(result.Content, "hn_url: https://news.ycombinator.com/item?id=1") {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestToolSearchesStories(t *testing.T) {
	client, closeServer := testSearchClient(t, searchResponse{Hits: []searchHit{
		{ObjectID: "123", Author: "alice", Title: "SQLite replication", Points: 20, NumComments: 5},
	}}, nil)
	defer closeServer()

	result, err := NewTool(client).Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: `{"view":"search","query":" SQLite replication ","sort":"date","limit":1}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "UNTRUSTED HACKER NEWS DATA") || !strings.Contains(result.Content, "query: SQLite replication") || !strings.Contains(result.Content, "sort: date") || !strings.Contains(result.Content, "SQLite replication") {
		t.Fatalf("result = %q", result.Content)
	}
	meta, ok := result.Meta.(map[string]any)
	if !ok || meta["view"] != ViewSearch || meta["sort"] != SearchSortDate {
		t.Fatalf("meta = %#v", result.Meta)
	}
}

func TestToolRequiresViewSpecificArguments(t *testing.T) {
	tool := NewTool(NewClient())
	if _, err := tool.Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: `{"view":"thread"}`}}); err == nil {
		t.Fatal("thread without item unexpectedly succeeded")
	}
	if _, err := tool.Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: `{"view":"search"}`}}); err == nil {
		t.Fatal("search without query unexpectedly succeeded")
	}
}
