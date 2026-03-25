package llm

import "sync"

// BasicUsageTracker is a thread-safe implementation of the UsageTracker interface.
type BasicUsageTracker struct {
	mu      sync.Mutex
	ByModel map[string]Usage
	Total   Usage
}

func NewUsageTracker() *BasicUsageTracker {
	return &BasicUsageTracker{
		ByModel: make(map[string]Usage),
	}
}

// Add safely aggregates usage stats.
func (t *BasicUsageTracker) RecordUsage(model string, u Usage) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.Total.PromptTokens += u.PromptTokens
	t.Total.CompletionTokens += u.CompletionTokens
	t.Total.CachedTokens += u.CachedTokens
	t.Total.TotalTokens += u.TotalTokens

	modelUsage := t.ByModel[model]
	modelUsage.PromptTokens += u.PromptTokens
	modelUsage.CompletionTokens += u.CompletionTokens
	modelUsage.CachedTokens += u.CachedTokens
	modelUsage.TotalTokens += u.TotalTokens
	t.ByModel[model] = modelUsage
}
