package murm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func collect(run string) (*Translator, *[]StreamEvent) {
	var got []StreamEvent
	tr := NewTranslator(run, func(ev StreamEvent) { got = append(got, ev) })
	tr.now = func() int64 { return 1000 } // deterministic timestamps
	return tr, &got
}

func TestTranslatorToolUsingTurn(t *testing.T) {
	tr, got := collect("msg_user_1")

	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: "Let me check."})
	tr.Feed(golem.StreamEvent{Kind: golem.EventToolCall, Step: golem.Step{
		ToolName: "shell", ToolCallID: "call_1", Arguments: `{"command":"ls"}`,
	}})
	tr.Feed(golem.StreamEvent{Kind: golem.EventToolResult, Step: golem.Step{
		ToolCallID: "call_1", Result: "README.md",
	}})
	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: "Done."})
	tr.Feed(golem.StreamEvent{Kind: golem.EventDone,
		Usage:        llm.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		FinishReason: llm.FinishReasonStop,
	})

	events := *got
	wantTypes := []string{
		EventMessageStart, // assistant iter 0
		EventTextDelta,
		EventToolCallStart,
		EventToolCallDelta, // status complete
		EventMessageStart,  // tool role
		EventToolResult,
		EventMessageStart, // assistant iter 1 (after tool phase)
		EventTextDelta,
		EventUsage,
		EventFinish,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d:\n%s", len(events), len(wantTypes), dump(events))
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event %d type = %q, want %q\n%s", i, events[i].Type, want, dump(events))
		}
	}

	// ids and key fields
	if m := events[0].Message; m.ID != "msg_msg_user_1_assistant_0" || m.Role != RoleAssistant || m.Blocks == nil {
		t.Fatalf("assistant message_start wrong: %+v", m)
	}
	if e := events[1]; e.MessageID != "msg_msg_user_1_assistant_0" || e.BlockID != "block_msg_user_1_assistant_0_text" || e.Delta != "Let me check." {
		t.Fatalf("text_delta wrong: %+v", e)
	}
	if b := events[2].Block; b.ID != "block_msg_user_1_tool_call_call_1" || b.Name != "shell" || b.ArgsText != `{"command":"ls"}` || b.Status != StatusRunning {
		t.Fatalf("tool_call_start wrong: %+v", b)
	}
	if e := events[3]; e.BlockID != "block_msg_user_1_tool_call_call_1" || e.Status != StatusComplete {
		t.Fatalf("tool_call_delta wrong: %+v", e)
	}
	if m := events[4].Message; m.ID != "msg_msg_user_1_tool_call_1" || m.Role != RoleTool {
		t.Fatalf("tool message_start wrong: %+v", m)
	}
	if b := events[5].Block; b.ID != "block_msg_user_1_tool_result_call_1" || b.OutputText != "README.md" || b.IsError {
		t.Fatalf("tool_result wrong: %+v", b)
	}
	if m := events[6].Message; m.ID != "msg_msg_user_1_assistant_1" {
		t.Fatalf("second assistant message_start wrong: %+v", m)
	}
	if e := events[7]; e.MessageID != "msg_msg_user_1_assistant_1" || e.BlockID != "block_msg_user_1_assistant_1_text" {
		t.Fatalf("second text_delta wrong: %+v", e)
	}
	if e := events[8]; e.Input != 100 || e.Output != 20 || e.Total != 120 {
		t.Fatalf("usage wrong: %+v", e)
	}
	if events[9].Reason != FinishStop {
		t.Fatalf("finish reason = %q, want stop", events[9].Reason)
	}
}

func TestTranslatorToolError(t *testing.T) {
	tr, got := collect("r")
	tr.Feed(golem.StreamEvent{Kind: golem.EventToolCall, Step: golem.Step{ToolName: "x", ToolCallID: "c1"}})
	tr.Feed(golem.StreamEvent{Kind: golem.EventToolError, Step: golem.Step{ToolCallID: "c1", Error: "boom"}})

	events := *got
	// message_start(assistant), tool_call_start, tool_call_delta(error), message_start(tool), tool_result(isError)
	if len(events) != 5 {
		t.Fatalf("got %d events:\n%s", len(events), dump(events))
	}
	if events[2].Status != StatusError {
		t.Fatalf("tool_call_delta status = %q, want error", events[2].Status)
	}
	if b := events[4].Block; !b.IsError || b.OutputText != "boom" {
		t.Fatalf("tool_result should carry the error: %+v", b)
	}
}

func TestTranslatorPlainTextTurn(t *testing.T) {
	tr, got := collect("r")
	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: "hi"})
	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: " there"})
	tr.Feed(golem.StreamEvent{Kind: golem.EventDone, FinishReason: llm.FinishReasonStop})

	events := *got
	// one message_start, two text_deltas into the same block, usage, finish
	if len(events) != 5 {
		t.Fatalf("got %d events:\n%s", len(events), dump(events))
	}
	if events[1].BlockID != events[2].BlockID {
		t.Fatalf("text deltas should share a block: %q vs %q", events[1].BlockID, events[2].BlockID)
	}
}

func TestTranslatorResetsPartialModelAttempt(t *testing.T) {
	tr, got := collect("r")
	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: "partial"})
	tr.Feed(golem.StreamEvent{Kind: golem.EventAttemptReset})
	tr.Feed(golem.StreamEvent{Kind: golem.EventTextDelta, Text: "complete"})

	events := *got
	if len(events) != 4 {
		t.Fatalf("got %d events:\n%s", len(events), dump(events))
	}
	reset := events[2]
	if reset.Type != EventMessageStart || reset.Message == nil || len(reset.Message.Blocks) != 1 {
		t.Fatalf("reset event = %+v", reset)
	}
	block := reset.Message.Blocks[0]
	if block.Type != BlockText || block.Text != "" || block.ID != events[1].BlockID {
		t.Fatalf("reset block = %+v, first delta = %+v", block, events[1])
	}
	if events[3].BlockID != block.ID || events[3].Delta != "complete" {
		t.Fatalf("replacement delta = %+v", events[3])
	}
}

func TestMessageStartMarshalsEmptyBlocksArray(t *testing.T) {
	ev := StreamEvent{Type: EventMessageStart, Message: &Message{
		ID: "m1", Role: RoleAssistant, Blocks: []ContentBlock{}, RunID: "r",
	}}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"blocks":[]`) {
		t.Fatalf("blocks should marshal as [], got: %s", s)
	}
	if !strings.Contains(s, `"type":"message_start"`) {
		t.Fatalf("missing type: %s", s)
	}
}

func dump(events []StreamEvent) string {
	var b strings.Builder
	for i, e := range events {
		fmt := e.Type
		b.WriteString("  [")
		b.WriteByte(byte('0' + i))
		b.WriteString("] ")
		b.WriteString(fmt)
		b.WriteByte('\n')
	}
	return b.String()
}
