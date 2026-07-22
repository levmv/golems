package llm

import "unicode/utf8"

// EstimateTextTokens returns a conservative tokenizer-independent estimate.
// ASCII prose and code average roughly four bytes per token; non-ASCII runes
// receive one token each so Cyrillic and CJK are not treated as nearly free.
func EstimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	nonASCII := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return (ascii+3)/4 + nonASCII
}

// EstimateMessageTokens includes visible and reasoning text, tool calls, tool
// result linkage, and a small provider-format overhead.
func EstimateMessageTokens(message Message) int {
	tokens := EstimateTextTokens(message.Content) + EstimateTextTokens(message.ReasoningContent) + 6
	for _, call := range message.ToolCalls {
		tokens += EstimateTextTokens(call.Function.Name) + EstimateTextTokens(call.Function.Arguments) + 8
	}
	if message.ToolCallID != "" {
		tokens += EstimateTextTokens(message.ToolCallID) + 2
	}
	return tokens
}
