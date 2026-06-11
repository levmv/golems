package llm

import (
	"errors"
	"testing"

	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/openai"
)

func TestBuildBaseRequestMapsTools(t *testing.T) {
	parallel := false
	topP := float32(0.8)
	req := &Request{
		Model: "gpt-test",
		Messages: []Message{{
			Role:       RoleTool,
			Content:    "file contents",
			ToolCallID: "call_1",
		}},
		Tools: []Tool{{
			Type: ToolTypeFunction,
			Function: ToolDefinition{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: jsonschema.Object(map[string]jsonschema.Schema{
					"path": jsonschema.String("Path under root"),
				}, "path").NoAdditionalProperties(),
				Strict: true,
			},
		}},
		ToolChoice:        &ToolChoice{Mode: ToolChoiceFunction, Name: "read_file"},
		ParallelToolCalls: &parallel,
		ProviderExtensions: func(oaiReq *openai.ChatCompletionRequest) {
			oaiReq.TopP = &topP
			oaiReq.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			}
		},
	}

	oaiReq, err := (&openAIAdapter{}).buildBaseRequest(req)
	if err != nil {
		t.Fatalf("buildBaseRequest() error = %v", err)
	}

	if oaiReq.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", oaiReq.Model)
	}
	if len(oaiReq.Messages) != 1 || oaiReq.Messages[0].ToolCallID != "call_1" {
		t.Fatalf("messages = %#v", oaiReq.Messages)
	}
	if len(oaiReq.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(oaiReq.Tools))
	}

	tool := oaiReq.Tools[0]
	if tool.Type != openai.ToolTypeFunction {
		t.Fatalf("tool type = %q, want function", tool.Type)
	}
	if tool.Function == nil {
		t.Fatal("tool function is nil")
	}
	if tool.Function.Name != "read_file" || !tool.Function.Strict {
		t.Fatalf("tool function = %#v", tool.Function)
	}
	if tool.Function.Parameters == nil {
		t.Fatal("tool parameters are nil")
	}

	choice, ok := oaiReq.ToolChoice.(openai.ToolChoice)
	if !ok {
		t.Fatalf("tool choice type = %T, want openai.ToolChoice", oaiReq.ToolChoice)
	}
	if choice.Function == nil {
		t.Fatal("choice function is nil")
	}
	if choice.Function.Name != "read_file" {
		t.Fatalf("choice function = %#v", choice.Function)
	}
	if oaiReq.ParallelToolCalls != &parallel {
		t.Fatal("parallel tool calls pointer was not forwarded")
	}
	if oaiReq.TopP != &topP {
		t.Fatal("provider extension did not set top_p")
	}
	if oaiReq.ResponseFormat == nil || oaiReq.ResponseFormat.Type != openai.ChatCompletionResponseFormatTypeJSONObject {
		t.Fatalf("response format = %#v", oaiReq.ResponseFormat)
	}
}

func TestMapToolChoiceModes(t *testing.T) {
	tests := []struct {
		name   string
		choice *ToolChoice
		want   any
	}{
		{name: "auto", choice: &ToolChoice{Mode: ToolChoiceAuto}, want: "auto"},
		{name: "none", choice: &ToolChoice{Mode: ToolChoiceNone}, want: "none"},
		{name: "required", choice: &ToolChoice{Mode: ToolChoiceRequired}, want: "required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapToolChoice(tt.choice)
			if err != nil {
				t.Fatalf("mapToolChoice() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("mapToolChoice() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMapToolChoiceRejectsInvalidChoices(t *testing.T) {
	tests := []struct {
		name   string
		choice *ToolChoice
	}{
		{name: "unknown mode", choice: &ToolChoice{Mode: ToolChoiceMode("invalid")}},
		{name: "function without name", choice: &ToolChoice{Mode: ToolChoiceFunction}},
		{name: "mode with name", choice: &ToolChoice{Mode: ToolChoiceRequired, Name: "read_file"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mapToolChoice(tt.choice)
			if err == nil {
				t.Fatal("mapToolChoice() error = nil, want error")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("mapToolChoice() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestStreamAdapterAccumulatesToolCallDeltas(t *testing.T) {
	idx := 0
	stream := &streamAdapter{}

	stream.mergeToolCallDeltas([]openai.ToolCall{{
		Index: &idx,
		ID:    "call_1",
		Type:  openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "read_file",
			Arguments: `{"pa`,
		},
	}})
	stream.mergeToolCallDeltas([]openai.ToolCall{{
		Index: &idx,
		Function: openai.FunctionCall{
			Arguments: `th":"README.md"}`,
		},
	}})

	calls := stream.snapshotToolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.ID != "call_1" {
		t.Fatalf("id = %q, want call_1", call.ID)
	}
	if call.Type != string(ToolTypeFunction) {
		t.Fatalf("type = %q, want function", call.Type)
	}
	if call.Function.Name != "read_file" {
		t.Fatalf("name = %q, want read_file", call.Function.Name)
	}
	if call.Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("arguments = %q", call.Function.Arguments)
	}
}

func TestStreamAdapterAppendsMissingIndexDeltaToLastCall(t *testing.T) {
	stream := &streamAdapter{}

	stream.mergeToolCallDeltas([]openai.ToolCall{{
		ID:   "call_1",
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "read_file",
			Arguments: `{"pa`,
		},
	}})
	stream.mergeToolCallDeltas([]openai.ToolCall{{
		Function: openai.FunctionCall{
			Arguments: `th":"README.md"}`,
		},
	}})

	calls := stream.snapshotToolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	if calls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("arguments = %q", calls[0].Function.Arguments)
	}
}

func TestStreamAdapterStartsNewCallWhenMissingIndexDeltaHasNewID(t *testing.T) {
	stream := &streamAdapter{}

	stream.mergeToolCallDeltas([]openai.ToolCall{{
		ID:   "call_1",
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "first",
			Arguments: `{"a":1}`,
		},
	}})
	stream.mergeToolCallDeltas([]openai.ToolCall{{
		ID:   "call_2",
		Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{
			Name:      "second",
			Arguments: `{"b":2}`,
		},
	}})

	calls := stream.snapshotToolCalls()
	if len(calls) != 2 {
		t.Fatalf("calls len = %d, want 2", len(calls))
	}
	if calls[0].ID != "call_1" || calls[1].ID != "call_2" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestChatEmptyChoicesFinishReasonUnknown(t *testing.T) {
	if got := finishReasonFromChoices(nil); got != FinishReasonUnknown {
		t.Fatalf("finish reason = %q, want unknown", got)
	}
}
