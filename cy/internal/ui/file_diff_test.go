package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	toolruntime "github.com/levmv/golems/cy/internal/tools"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const maxDiffDisplayLines = toolruntime.MaxDiffDisplayLines

func buildFileChangeMeta(path, operation string, old, updated []byte) fileChangeMeta {
	return toolruntime.BuildFileChangeMeta(path, operation, old, updated)
}

func TestTUIUpdatesEditCallWithRenderedDiff(t *testing.T) {
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = nil
	model.resize(100, 24)
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolCall,
		Step: golem.Step{ToolName: "edit", ToolCallID: "call-1", Arguments: `{"path":"note.txt"}`},
	})
	change := buildFileChangeMeta("note.txt", "edited", []byte("one\nold\nthree\n"), []byte("one\nnew\nthree\n"))
	model.applyStreamEvent(golem.StreamEvent{
		Kind: golem.EventToolResult,
		Step: golem.Step{ToolName: "edit", ToolCallID: "call-1", Meta: change},
	})

	if len(model.blocks) != 1 || model.blocks[0].text != "edited  note.txt" || model.blocks[0].fileChange == nil {
		t.Fatalf("diff block = %#v", model.blocks)
	}
	rendered := strings.Join(model.renderTranscriptLines(), "\n")
	for _, want := range []string{"edited  note.txt  +1 −1", "@@ -1,3 +1,3 @@", "− 2   │ old", "+   2 │ new"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered diff missed %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "[tool]") {
		t.Fatalf("rendered diff kept generic tool label: %q", rendered)
	}
}

func TestTUILimitsLargeDiffPreview(t *testing.T) {
	var updated strings.Builder
	for index := 0; index < maxDiffDisplayLines+20; index++ {
		fmt.Fprintf(&updated, "line %d\n", index)
	}
	change := buildFileChangeMeta("generated.txt", "created", nil, []byte(updated.String()))
	if !change.Truncated || change.Additions != maxDiffDisplayLines+20 {
		t.Fatalf("large change = %#v", change)
	}

	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = []screenBlock{{kind: screenBlockTool, text: "created  generated.txt", fileChange: &change}}
	model.resize(100, 100)
	rendered := strings.Join(model.renderTranscriptLines(), "\n")
	if !strings.Contains(rendered, "diff preview limited") || !strings.Contains(rendered, fmt.Sprintf("+%d", maxDiffDisplayLines+20)) {
		t.Fatalf("large diff preview = %q", rendered)
	}
}

func TestTUIRestoresStructuredDiffFromHistory(t *testing.T) {
	change := buildFileChangeMeta("note.txt", "edited", []byte("old\n"), []byte("new\n"))
	agent, err := golem.New(golem.Config{
		Model: historyOnlyModel{},
		History: []llm.Message{
			{Role: llm.RoleAI, ToolCalls: []llm.ToolCall{{ID: "call-1", Function: llm.ToolFunction{Name: "edit", Arguments: `{"path":"note.txt"}`}}}},
			{Role: llm.RoleTool, ToolCallID: "call-1", Content: "updated", Meta: change},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stateful := historyTestAgent{history: agent.History()}
	model := newCyTUIModel(context.Background(), stateful, Config{}, ".", nil)
	found := false
	for _, block := range model.blocks {
		if block.text == "edited  note.txt" && block.fileChange != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored blocks = %#v", model.blocks)
	}
}

type historyOnlyModel struct{}

func (historyOnlyModel) Chat(context.Context, llm.Request) (*llm.Response, error) {
	return nil, nil
}

func (historyOnlyModel) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

type historyTestAgent struct {
	screenAgentStub
	history []llm.Message
}

func (a historyTestAgent) SessionHistory() ([]llm.Message, error) { return a.history, nil }
func (historyTestAgent) SessionUsage() (llm.Usage, error)         { return llm.Usage{}, nil }

func TestRenderedDiffSanitizesTerminalEscapes(t *testing.T) {
	change := buildFileChangeMeta("note.txt", "edited", []byte("safe\n"), []byte("\x1b]8;;https://example.com\aunsafe\x1b]8;;\a\n"))
	model := newCyTUIModel(context.Background(), screenAgentStub{}, Config{}, ".", nil)
	model.blocks = []screenBlock{{kind: screenBlockTool, text: "edited  note.txt", fileChange: &change}}
	model.resize(100, 24)
	if rendered := strings.Join(model.renderTranscriptLines(), "\n"); strings.Contains(rendered, "\x1b]") {
		t.Fatalf("rendered diff contains terminal escape: %q", rendered)
	}
}
