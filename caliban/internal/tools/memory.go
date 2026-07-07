package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

// MemoryStore is the workspace capability behind durable memory writes.
type MemoryStore interface {
	UpsertMemory(title, body, summary string) (MemoryUpsertResult, error)
}

// MemoryUpsertResult is the subset of workspace.MemoryUpsertResult this tool
// needs. Keeping the shape local avoids importing workspace into tools.
type MemoryUpsertResult struct {
	Path         string
	Created      bool
	IndexUpdated bool
}

type memoryUpsertArgs struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Summary string `json:"summary,omitempty"`
}

// Memory returns tools for durable user memory.
func Memory(store MemoryStore) []golem.Tool {
	return []golem.Tool{
		golem.FunctionTool("memory_upsert",
			"Create or update one durable user memory fact. Use when the user explicitly asks you to remember something, "+
				"or when a stable user-specific preference, commitment, project decision, or long-lived fact should persist beyond the chat tail. "+
				"Do not ask the user for filenames or slugs; provide a short title and complete body.",
			jsonschema.Obj(
				jsonschema.Required("title", jsonschema.Str{
					Description: "Short human-readable title for this memory. Reuse the same title to update the same fact.",
				}),
				jsonschema.Required("body", jsonschema.Str{
					Description: "Complete durable memory content in Markdown. Include the full updated fact, not a patch.",
				}),
				jsonschema.Optional("summary", jsonschema.Str{
					Description: "One short MEMORY.md index sentence. If omitted, the body is summarized by truncation.",
				}),
			),
			func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
				var args memoryUpsertArgs
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
				if strings.TrimSpace(args.Title) == "" {
					return golem.ToolResult{}, fmt.Errorf("title is required")
				}
				if strings.TrimSpace(args.Body) == "" {
					return golem.ToolResult{}, fmt.Errorf("body is required")
				}
				res, err := store.UpsertMemory(args.Title, args.Body, args.Summary)
				if err != nil {
					return golem.ToolResult{}, err
				}
				action := "updated"
				if res.Created {
					action = "created"
				}
				return golem.ToolResult{Content: fmt.Sprintf("memory %s: %s", action, res.Path)}, nil
			}),
	}
}
