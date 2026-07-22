package llm

import "testing"

func TestEstimateTextTokensAccountsForNonASCII(t *testing.T) {
	if got := EstimateTextTokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII estimate = %d, want 2", got)
	}
	if got := EstimateTextTokens("привет"); got != 6 {
		t.Fatalf("Cyrillic estimate = %d, want 6", got)
	}
}

func TestEstimateMessageTokensIncludesReasoningAndTools(t *testing.T) {
	plain := EstimateMessageTokens(Message{Content: "answer"})
	rich := EstimateMessageTokens(Message{
		Content:          "answer",
		ReasoningContent: "thinking",
		ToolCalls: []ToolCall{{
			Function: ToolFunction{Name: "read", Arguments: `{"path":"x"}`},
		}},
	})
	if rich <= plain {
		t.Fatalf("rich estimate = %d, plain = %d", rich, plain)
	}
}
