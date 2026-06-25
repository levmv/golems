package murm

import (
	"testing"
	"time"
)

func TestMessageFromTranscriptAssistantBlocks(t *testing.T) {
	created := time.Unix(10, 123000000)
	got := MessageFromTranscript(TranscriptMessage{
		ID:        "42",
		Role:      RoleAssistant,
		RunID:     "7",
		Text:      "done",
		Reasoning: "thinking",
		ToolCalls: []TranscriptToolCall{{
			ID:        "call_1",
			Name:      "shell",
			Arguments: `{"command":"ls"}`,
		}},
		CreatedAt: created,
	})

	if got.ID != "msg_db_42" || got.Role != RoleAssistant || got.RunID != "7" {
		t.Fatalf("unexpected message identity: %+v", got)
	}
	if got.CreatedAt != created.UnixMilli() || got.UpdatedAt != created.UnixMilli() {
		t.Fatalf("unexpected timestamps: %+v", got)
	}
	if len(got.Blocks) != 3 {
		t.Fatalf("got %d blocks: %+v", len(got.Blocks), got.Blocks)
	}
	if got.Blocks[0].Type != BlockReasoning || got.Blocks[0].Text != "thinking" {
		t.Fatalf("bad reasoning block: %+v", got.Blocks[0])
	}
	if got.Blocks[1].Type != BlockText || got.Blocks[1].Text != "done" {
		t.Fatalf("bad text block: %+v", got.Blocks[1])
	}
	if got.Blocks[2].Type != BlockToolCall || got.Blocks[2].Status != StatusComplete || got.Blocks[2].Name != "shell" {
		t.Fatalf("bad tool block: %+v", got.Blocks[2])
	}
}

func TestMessageFromTranscriptToolResult(t *testing.T) {
	got := MessageFromTranscript(TranscriptMessage{
		ID:   "43",
		Role: RoleTool,
		Text: "raw output should come from result",
		ToolResult: &TranscriptToolResult{
			ToolCallID: "call_1",
			OutputText: "README.md",
			IsError:    true,
		},
	})

	if len(got.Blocks) != 1 {
		t.Fatalf("got %d blocks: %+v", len(got.Blocks), got.Blocks)
	}
	block := got.Blocks[0]
	if block.Type != BlockToolResult || block.ToolCallID != "call_1" || block.OutputText != "README.md" || !block.IsError {
		t.Fatalf("bad result block: %+v", block)
	}
}
