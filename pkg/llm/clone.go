package llm

// CloneMessages copies a message slice and its mutable tool-call slices. Meta
// and Raw-style opaque values remain shared because their concrete types are
// application-defined.
func CloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, len(messages))
	for index, message := range messages {
		out[index] = message
		out[index].ToolCalls = CloneToolCalls(message.ToolCalls)
	}
	return out
}

func CloneToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(calls))
	copy(out, calls)
	return out
}

func CloneToolChoice(choice *ToolChoice) *ToolChoice {
	if choice == nil {
		return nil
	}
	out := *choice
	return &out
}
