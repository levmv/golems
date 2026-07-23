package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/golem"
)

func TestDescribeToolCallUsesBuiltInArguments(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{name: "read", tool: "read", args: `{"path":"cy/main.go","offset":201,"limit":50}`, want: "read  cy/main.go:201+50"},
		{name: "grep", tool: "grep", args: `{"pattern":"func \\(m \\*Model\\)","path":"cy","include":"*.go"}`, want: `grep  "func \\(m \\*Model\\)" · cy · *.go`},
		{name: "glob", tool: "glob", args: `{"pattern":"**/*_test.go","path":"cy"}`, want: "glob  **/*_test.go · cy"},
		{name: "edit", tool: "edit", args: `{"path":"cy/main.go","old_text":"before","new_text":"after"}`, want: "edit  cy/main.go"},
		{name: "write", tool: "write", args: `{"path":"cy/new.go","content":"package main"}`, want: "write  cy/new.go"},
		{name: "bash", tool: "bash", args: `{"command":"go test\n./cy/...","workdir":"src"}`, want: "$ go test ↵ ./cy/... · in src"},
		{name: "bash spacing", tool: "bash", args: `{"command":"printf 'a  b'"}`, want: "$ printf 'a  b'"},
		{name: "job", tool: "job", args: `{"action":"output","job_id":"job-123"}`, want: "job  output job-123"},
		{name: "web search", tool: "web_search", args: `{"query":"Go durable agents"}`, want: `web  "Go durable agents"`},
		{name: "web fetch", tool: "web_fetch", args: `{"url":"https://example.com/docs?q=private#part"}`, want: "fetch  https://example.com/docs?…"},
		{name: "Hacker News feed", tool: "hacker_news", args: `{"view":"top","limit":10}`, want: "hn  top · 10"},
		{name: "Hacker News search", tool: "hacker_news", args: `{"view":"search","query":"SQLite replication","limit":10}`, want: `hn  search "SQLite replication"`},
		{name: "Hacker News thread", tool: "hacker_news", args: `{"view":"thread","item":"123"}`, want: "hn  thread 123"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := describeToolCall(test.tool, test.args).Text; got != test.want {
				t.Fatalf("description = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTUIAggregatesConsecutiveReadsFromSameDirectory(t *testing.T) {
	model := cyTUIModel{}
	for _, file := range []string{"cy/main.go", "cy/config.go", "cy/process.go"} {
		model.applyStreamEvent(golem.StreamEvent{
			Kind: golem.EventToolCall,
			Step: golem.Step{ToolName: "read", Arguments: `{"path":"` + file + `"}`},
		})
	}

	if len(model.blocks) != 1 {
		t.Fatalf("blocks = %#v, want one read group", model.blocks)
	}
	if got := model.blocks[0].text; got != "read  cy/ → main.go, config.go, process.go" {
		t.Fatalf("read group = %q", got)
	}

	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolCall,
		Step: golem.Step{ToolName: "read", Arguments: `{"path":"pkg/llm/model.go"}`},
	})
	if len(model.blocks) != 2 || model.blocks[1].text != "read  pkg/llm/model.go" {
		t.Fatalf("different directory was merged: %#v", model.blocks)
	}
}

func TestTUIShowsBashCommandInsteadOfToolName(t *testing.T) {
	model := cyTUIModel{}
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolCall,
		Step: golem.Step{ToolName: "bash", Arguments: `{"command":"go test ./cy/..."}`},
	})
	if len(model.blocks) != 1 || model.blocks[0].text != "$ go test ./cy/..." {
		t.Fatalf("bash block = %#v", model.blocks)
	}
}

func TestTUIUpdatesBashCallWithCompletionStatus(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = nil
	model.resize(100, 24)
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolCall,
		Step: golem.Step{ToolName: "bash", ToolCallID: "call-1", Arguments: `{"command":"go test ./cy/..."}`},
	})
	zero := 0
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolResult,
		Step: golem.Step{ToolName: "bash", ToolCallID: "call-1", Meta: processResultMeta{
			Type: processResultMetaType, JobID: "job-123", Status: jobCompleted,
			ExitCode: &zero, DurationMillis: 1250, OutputBytes: 2048,
		}},
	})

	if len(model.blocks) != 1 || model.blocks[0].processResult == nil {
		t.Fatalf("bash block = %#v", model.blocks)
	}
	rendered := strings.Join(model.renderTranscriptLines(), "\n")
	for _, want := range []string{"✓", "$ go test ./cy/...", "1.2s · exit 0 · 2.0 KB"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered process missed %q: %q", want, rendered)
		}
	}
}

func TestTUIShowsElapsedTimeWhileBashRuns(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = nil
	model.resize(100, 24)
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolCall,
		Step: golem.Step{ToolName: "bash", ToolCallID: "call-1", Arguments: `{"command":"sleep 5"}`},
	})
	model.updatePendingToolDurations(model.blocks[0].toolStartedAt.Add(2500 * time.Millisecond))
	rendered := strings.Join(model.renderTranscriptLines(), "\n")
	if !strings.Contains(rendered, "◌") || !strings.Contains(rendered, "$ sleep 5  2.5s") {
		t.Fatalf("pending process = %q", rendered)
	}
}

func TestTUISuppressesDuplicateBackgroundCompletionNotice(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = []screenBlock{{
		kind:          screenBlockTool,
		text:          "$ make test",
		processResult: &processResultMeta{Type: processResultMetaType, JobID: "job-123", Status: jobCompleted},
	}}
	model.applyStreamEvent(golem.StreamEvent{Kind: golem.EventStatus, Text: "Background job job-123 completed: status=completed, exit_code=0."})
	if len(model.blocks) != 1 {
		t.Fatalf("duplicate completion was rendered: %#v", model.blocks)
	}
}

func TestTUICollapsesToolIterationLimitErrors(t *testing.T) {
	model := cyTUIModel{}
	for _, tool := range []string{"read", "grep"} {
		model.applyStreamEvent(golem.StreamEvent{
			Kind: golem.EventToolError,
			Step: golem.Step{ToolName: tool, Error: "tool iteration limit reached after 128 iterations"},
		})
	}
	if len(model.blocks) != 1 || !strings.Contains(model.blocks[0].text, "requesting a final answer without more tools") {
		t.Fatalf("tool limit blocks = %#v", model.blocks)
	}
}
