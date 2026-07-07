package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/llm"
)

type fakeMemoryStore struct {
	title   string
	body    string
	summary string
	result  MemoryUpsertResult
	err     error
}

func (s *fakeMemoryStore) UpsertMemory(title, body, summary string) (MemoryUpsertResult, error) {
	s.title = title
	s.body = body
	s.summary = summary
	return s.result, s.err
}

func TestMemoryUpsertTool(t *testing.T) {
	store := &fakeMemoryStore{result: MemoryUpsertResult{Path: "memory/fact.md", Created: true}}
	tool := Memory(store)[0]

	out := runToolForTest(t, tool, memoryUpsertArgs{
		Title:   "User preference",
		Body:    "The user prefers concise replies.",
		Summary: "Prefers concise replies",
	})

	if out != "memory created: memory/fact.md" {
		t.Fatalf("unexpected output: %q", out)
	}
	if store.title != "User preference" || store.body != "The user prefers concise replies." || store.summary != "Prefers concise replies" {
		t.Fatalf("unexpected store args: %+v", store)
	}
}

func TestMemoryUpsertToolRequiresBody(t *testing.T) {
	tool := Memory(&fakeMemoryStore{})[0]
	raw, err := json.Marshal(memoryUpsertArgs{Title: "Fact"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Run(context.Background(), llm.ToolCall{
		Function: llm.ToolFunction{Name: tool.Definition.Function.Name, Arguments: string(raw)},
	})
	if err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Fatalf("expected body error, got %v", err)
	}
}
