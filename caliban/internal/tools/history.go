package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

const (
	defaultHistorySearchLimit = 8
	maxHistorySearchLimit     = 20
)

// HistorySearcher is the engine capability behind transcript search.
type HistorySearcher interface {
	SearchHistory(ctx context.Context, query string, limit int) ([]HistorySearchResult, error)
}

// HistorySearchResult is one transcript search hit projected for tool output.
type HistorySearchResult struct {
	ID        int64
	Role      string
	Source    string
	Text      string
	CreatedAt time.Time
}

type historySearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// History returns tools for searching the current conversation transcript.
func History(searcher HistorySearcher) []golem.Tool {
	return []golem.Tool{
		golem.FunctionToolWithEffect(golem.ToolEffectRead, "history_search",
			"Search older raw transcript messages in the current conversation when the compacted summary or memory index is not enough. "+
				"Use concise substring queries; results are newest first.",
			jsonschema.Obj(
				jsonschema.Required("query", jsonschema.Str{
					Description: "Plain substring to search for in the current conversation transcript.",
				}),
				jsonschema.Optional("limit", jsonschema.Int{
					Description: "Maximum number of matches to return. Defaults to 8, maximum 20.",
				}),
			),
			func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
				var args historySearchArgs
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
				query := strings.TrimSpace(args.Query)
				if query == "" {
					return golem.ToolResult{}, fmt.Errorf("query is required")
				}
				limit := args.Limit
				if limit <= 0 {
					limit = defaultHistorySearchLimit
				}
				if limit > maxHistorySearchLimit {
					limit = maxHistorySearchLimit
				}
				matches, err := searcher.SearchHistory(ctx, query, limit)
				if err != nil {
					return golem.ToolResult{}, err
				}
				return golem.ToolResult{Content: formatHistorySearch(matches)}, nil
			}),
	}
}

func formatHistorySearch(matches []HistorySearchResult) string {
	if len(matches) == 0 {
		return "no history matches"
	}
	var b strings.Builder
	for _, m := range matches {
		role := m.Role
		if m.Source != "" {
			role += "/" + m.Source
		}
		fmt.Fprintf(&b, "- #%d %s %s: %s\n", m.ID, m.CreatedAt.Format("2006-01-02 15:04"), role, compactHistorySnippet(m.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func compactHistorySnippet(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" {
		return "(empty)"
	}
	const max = 360
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
