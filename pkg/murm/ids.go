package murm

import (
	"fmt"

	"github.com/levmv/golems/pkg/llm"
)

// Deterministic id scheme (from the contract doc), so replay/reconnect never
// creates duplicate blocks. run is the client-generated user message id.

func assistantMessageID(run string, iteration int) string {
	return fmt.Sprintf("msg_%s_assistant_%d", run, iteration)
}

func toolMessageID(run, toolCallID string) string {
	return fmt.Sprintf("msg_%s_tool_%s", run, toolCallID)
}

func toolCallBlockID(run, toolCallID string) string {
	return fmt.Sprintf("block_%s_tool_call_%s", run, toolCallID)
}

func toolResultBlockID(run, toolCallID string) string {
	return fmt.Sprintf("block_%s_tool_result_%s", run, toolCallID)
}

func textBlockID(run string, iteration int) string {
	return fmt.Sprintf("block_%s_assistant_%d_text", run, iteration)
}

func reasoningBlockID(run string, iteration int) string {
	return fmt.Sprintf("block_%s_assistant_%d_reasoning", run, iteration)
}

// mapFinishReason maps an llm.FinishReason to a murm FinishReason. Unknown maps
// to stop; aborted and error are produced by the caller, not from llm.
func mapFinishReason(r llm.FinishReason) FinishReason {
	switch r {
	case llm.FinishReasonStop:
		return FinishStop
	case llm.FinishReasonLength:
		return FinishLength
	case llm.FinishReasonToolUse:
		return FinishToolUse
	case llm.FinishReasonContentFilter:
		return FinishContentFilter
	default:
		return FinishStop
	}
}
