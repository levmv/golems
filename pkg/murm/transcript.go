package murm

import (
	"strconv"
	"time"
)

// TranscriptMessage is a neutral stored-message shape that can be rendered as a
// murm-ui Message. Apps adapt their durable transcript rows to this type without
// leaking app-specific storage packages into murm.
type TranscriptMessage struct {
	ID         string
	Role       Role
	RunID      string
	Text       string
	Reasoning  string
	ToolCalls  []TranscriptToolCall
	ToolResult *TranscriptToolResult
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Meta       map[string]any
}

// TranscriptToolCall is a stored assistant tool call.
type TranscriptToolCall struct {
	ID        string
	Name      string
	Arguments string
	Status    string
}

// TranscriptToolResult is a stored tool result block.
type TranscriptToolResult struct {
	ToolCallID string
	OutputText string
	IsError    bool
}

// MessageFromTranscript converts a durable transcript message into the block
// shape murm-ui renders. It intentionally does not know how to load chats,
// submit runs, or persist edits; those are app-level policies.
func MessageFromTranscript(tm TranscriptMessage) Message {
	msgID := prefixedID("msg_db_", tm.ID)
	updatedAt := tm.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = tm.CreatedAt
	}
	msg := Message{
		ID:        msgID,
		Role:      tm.Role,
		Blocks:    []ContentBlock{},
		RunID:     tm.RunID,
		CreatedAt: unixMillis(tm.CreatedAt),
		UpdatedAt: unixMillis(updatedAt),
		Meta:      tm.Meta,
	}
	blockPrefix := "block_db_" + tm.ID
	if tm.Reasoning != "" {
		msg.Blocks = append(msg.Blocks, ContentBlock{
			ID:   blockPrefix + "_reasoning",
			Type: BlockReasoning,
			Text: tm.Reasoning,
		})
	}
	if tm.Text != "" && tm.ToolResult == nil {
		msg.Blocks = append(msg.Blocks, ContentBlock{
			ID:   blockPrefix + "_text",
			Type: BlockText,
			Text: tm.Text,
		})
	}
	for i, tc := range tm.ToolCalls {
		status := tc.Status
		if status == "" {
			status = StatusComplete
		}
		msg.Blocks = append(msg.Blocks, ContentBlock{
			ID:         blockPrefix + "_tool_call_" + strconv.Itoa(i),
			Type:       BlockToolCall,
			ToolCallID: tc.ID,
			Name:       tc.Name,
			ArgsText:   tc.Arguments,
			Status:     status,
		})
	}
	if tm.ToolResult != nil {
		msg.Blocks = append(msg.Blocks, ContentBlock{
			ID:         blockPrefix + "_tool_result",
			Type:       BlockToolResult,
			ToolCallID: tm.ToolResult.ToolCallID,
			OutputText: tm.ToolResult.OutputText,
			IsError:    tm.ToolResult.IsError,
		})
	}
	return msg
}

func prefixedID(prefix, id string) string {
	if id == "" {
		return prefix + "0"
	}
	return prefix + id
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
