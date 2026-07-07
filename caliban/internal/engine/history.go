package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/tools"
)

// SearchHistory implements tools.HistorySearcher for the current run's
// conversation. Outside a run it falls back to the main conversation, matching
// scheduling and notification behavior.
func (e *Engine) SearchHistory(ctx context.Context, query string, limit int) ([]tools.HistorySearchResult, error) {
	msgs, err := e.store.SearchMessages(ctx, conversationIDFromContext(ctx), query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tools.HistorySearchResult, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, tools.HistorySearchResult{
			ID:        m.ID,
			Role:      string(m.Role),
			Source:    m.Source,
			Text:      historySearchText(m),
			CreatedAt: m.CreatedAt,
		})
	}
	return out, nil
}

func historySearchText(m store.Message) string {
	var parts []string
	if text := strings.TrimSpace(m.Content.Text); text != "" {
		parts = append(parts, text)
	}
	for _, tc := range m.Content.ToolCalls {
		name := tc.Function.Name
		if name == "" {
			name = "tool"
		}
		parts = append(parts, fmt.Sprintf("tool call %s(%s)", name, truncate(tc.Function.Arguments, 240)))
	}
	if len(parts) == 0 && m.Content.ToolCallID != "" {
		parts = append(parts, "tool result for "+m.Content.ToolCallID)
	}
	return strings.Join(parts, "\n")
}
