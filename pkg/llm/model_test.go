package llm

import (
	"context"
	"io"
	"testing"

	"github.com/levmv/golems/pkg/jsonschema"
)

type captureClient struct {
	chatReq   *Request
	streamReq *Request
}

func (c *captureClient) Chat(_ context.Context, req *Request) (*Response, error) {
	c.chatReq = req
	return &Response{}, nil
}

func (c *captureClient) Stream(_ context.Context, req *Request) (Stream, error) {
	c.streamReq = req
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (StreamChunk, error) { return StreamChunk{}, io.EOF }
func (emptyStream) Usage() Usage               { return Usage{} }
func (emptyStream) Close() error               { return nil }

func TestModelChatForwardsToolFields(t *testing.T) {
	client := &captureClient{}
	model := Model{client: client, modelID: "gpt-test"}
	parallel := false
	choice := &ToolChoice{Mode: ToolChoiceRequired}
	extensions := struct {
		Value string
	}{Value: "x"}
	tools := []Tool{{
		Type: ToolTypeFunction,
		Function: ToolDefinition{
			Name: "read_file",
			Parameters: jsonschema.Object(map[string]jsonschema.Schema{
				"path": jsonschema.String("Path"),
			}, "path"),
		},
	}}

	_, err := model.Chat(context.Background(), Request{
		Model:              "caller-model",
		Messages:           []Message{{Role: RoleUser, Content: "hi"}},
		Tools:              tools,
		ToolChoice:         choice,
		ParallelToolCalls:  &parallel,
		ProviderExtensions: extensions,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if client.chatReq == nil {
		t.Fatal("client did not receive request")
	}
	if client.chatReq.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", client.chatReq.Model)
	}
	if len(client.chatReq.Tools) != 1 || client.chatReq.Tools[0].Function.Name != "read_file" {
		t.Fatalf("tools = %#v", client.chatReq.Tools)
	}
	if client.chatReq.ToolChoice != choice {
		t.Fatalf("tool choice was not forwarded")
	}
	if client.chatReq.ParallelToolCalls != &parallel {
		t.Fatalf("parallel tool calls was not forwarded")
	}
	if client.chatReq.ProviderExtensions != extensions {
		t.Fatalf("provider extensions = %#v, want %#v", client.chatReq.ProviderExtensions, extensions)
	}
}

func TestModelDefaultReasoningEffort(t *testing.T) {
	client := &captureClient{}
	model := (Model{client: client, modelID: "reasoner"}).WithReasoningEffort(" HIGH ")
	if _, err := model.Chat(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if client.chatReq == nil || client.chatReq.ReasoningEffort != "high" {
		t.Fatalf("request = %#v", client.chatReq)
	}
}

func TestModelStreamForwardsToolFields(t *testing.T) {
	client := &captureClient{}
	model := Model{client: client, modelID: "gpt-test"}
	choice := &ToolChoice{Mode: ToolChoiceNone}

	stream, err := model.Stream(context.Background(), Request{
		Messages:   []Message{{Role: RoleUser, Content: "hi"}},
		ToolChoice: choice,
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	_ = stream.Close()

	if client.streamReq == nil {
		t.Fatal("client did not receive stream request")
	}
	if client.streamReq.Model != "gpt-test" {
		t.Fatalf("model = %q, want gpt-test", client.streamReq.Model)
	}
	if client.streamReq.ToolChoice != choice {
		t.Fatalf("tool choice was not forwarded")
	}
}
