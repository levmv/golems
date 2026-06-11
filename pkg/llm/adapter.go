package llm

import (
	"context"
	"fmt"
	"io"

	"github.com/levmv/golems/pkg/openai"
)

type openAIAdapter struct {
	client *openai.Client
}

func newOpenAIAdapter(client *openai.Client) *openAIAdapter {
	return &openAIAdapter{
		client: client,
	}
}

func mapMessages(msgs []Message) []openai.ChatCompletionMessage {
	res := make([]openai.ChatCompletionMessage, len(msgs))
	for i, m := range msgs {
		res[i] = openai.ChatCompletionMessage{
			Role:       openai.Role(m.Role),
			Content:    m.Content,
			ToolCalls:  mapToolCallsToOpenAI(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		}
	}
	return res
}

func mapToolCallsToOpenAI(calls []ToolCall) []openai.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]openai.ToolCall, len(calls))
	for i, tc := range calls {
		toolType := openai.ToolType(tc.Type)
		if toolType == "" {
			toolType = openai.ToolTypeFunction
		}
		out[i] = openai.ToolCall{
			ID:   tc.ID,
			Type: toolType,
			Function: openai.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return out
}

func mapToolCallsFromOpenAI(calls []openai.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(calls))
	for i, tc := range calls {
		out[i] = ToolCall{
			ID:   tc.ID,
			Type: string(tc.Type),
			Function: ToolFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return out
}

func mapTools(tools []Tool) []openai.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openai.Tool, len(tools))
	for i, tool := range tools {
		toolType := openai.ToolType(tool.Type)
		if toolType == "" {
			toolType = openai.ToolTypeFunction
		}
		out[i] = openai.Tool{
			Type: toolType,
			Function: &openai.FunctionDefinition{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
				Strict:      tool.Function.Strict,
			},
		}
	}
	return out
}

func mapToolChoice(choice *ToolChoice) (any, error) {
	if choice == nil {
		return nil, nil
	}

	mode := choice.Mode
	if mode == "" && choice.Name != "" {
		mode = ToolChoiceFunction
	}

	switch mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		if choice.Name != "" {
			return nil, fmt.Errorf("%w: tool choice %q must not include function name %q", ErrInvalidRequest, mode, choice.Name)
		}
		return string(mode), nil
	case ToolChoiceFunction:
		if choice.Name == "" {
			return nil, fmt.Errorf("%w: tool choice function requires a name", ErrInvalidRequest)
		}
		return openai.ToolChoice{
			Type: openai.ToolTypeFunction,
			Function: &openai.ToolFunction{
				Name: choice.Name,
			},
		}, nil
	default:
		return nil, fmt.Errorf("%w: unknown tool choice mode %q", ErrInvalidRequest, mode)
	}
}

func mapUsage(u openai.Usage) Usage {
	usage := Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		usage.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	return usage
}

func mapFinishReason(oaiReason openai.FinishReason) FinishReason {
	switch oaiReason {
	case openai.FinishReasonStop:
		return FinishReasonStop
	case openai.FinishReasonLength:
		return FinishReasonLength
	case openai.FinishReasonToolCalls:
		return FinishReasonToolUse
	case openai.FinishReasonContentFilter:
		return FinishReasonContentFilter
	case openai.FinishReasonNull, "":
		return "" // Mid-stream, not finished yet
	default:
		return FinishReasonUnknown
	}
}

func (a *openAIAdapter) buildBaseRequest(req *Request) (openai.ChatCompletionRequest, error) {
	toolChoice, err := mapToolChoice(req.ToolChoice)
	if err != nil {
		return openai.ChatCompletionRequest{}, err
	}

	oaiReq := openai.ChatCompletionRequest{
		Model:             req.Model,
		Messages:          mapMessages(req.Messages),
		Tools:             mapTools(req.Tools),
		ToolChoice:        toolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
	}

	if req.Temperature != nil {
		oaiReq.Temperature = req.Temperature
	}
	if req.MaxTokens != nil {
		oaiReq.MaxTokens = *req.MaxTokens
	}

	if ext, ok := req.ProviderExtensions.(openai.ChatCompletionRequestExtensions); ok {
		oaiReq.ChatCompletionRequestExtensions = ext
	}
	if apply, ok := req.ProviderExtensions.(func(*openai.ChatCompletionRequest)); ok {
		apply(&oaiReq)
	}

	return oaiReq, nil
}

func (a *openAIAdapter) Chat(ctx context.Context, req *Request) (*Response, error) {
	oaiReq, err := a.buildBaseRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.CreateChatCompletion(ctx, oaiReq)
	if err != nil {
		return nil, wrapOpenAIError(err)
	}

	var choice openai.ChatCompletionChoice
	if len(resp.Choices) > 0 {
		choice = resp.Choices[0]
	}

	return &Response{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ToolCalls:        mapToolCallsFromOpenAI(choice.Message.ToolCalls),
		FinishReason:     finishReasonFromChoices(resp.Choices),
		Usage:            mapUsage(resp.Usage),
		Raw:              resp,
	}, nil
}

func finishReasonFromChoices(choices []openai.ChatCompletionChoice) FinishReason {
	if len(choices) == 0 {
		return FinishReasonUnknown
	}
	return mapFinishReason(choices[0].FinishReason)
}

func (a *openAIAdapter) Stream(ctx context.Context, req *Request) (Stream, error) {
	oaiReq, err := a.buildBaseRequest(req)
	if err != nil {
		return nil, err
	}

	// Instruct OpenAI to return usage data on the final chunk
	oaiReq.StreamOptions = &openai.StreamOptions{
		IncludeUsage: true,
	}

	stream, err := a.client.CreateChatCompletionStream(ctx, oaiReq)
	if err != nil {
		return nil, wrapOpenAIError(err)
	}

	return &streamAdapter{
		stream: stream,
	}, nil
}

type streamAdapter struct {
	stream    *openai.ChatCompletionStream
	usage     Usage
	toolCalls []ToolCall
}

func (s *streamAdapter) Recv() (StreamChunk, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		if err == io.EOF {
			return StreamChunk{}, err
		}
		return StreamChunk{}, wrapOpenAIError(err)
	}

	chunk := StreamChunk{}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		chunk.Text = choice.Delta.Content
		chunk.ReasoningContent = choice.Delta.ReasoningContent
		if len(choice.Delta.ToolCalls) > 0 {
			s.mergeToolCallDeltas(choice.Delta.ToolCalls)
		}
		chunk.FinishReason = mapFinishReason(choice.FinishReason)
		if chunk.FinishReason == FinishReasonToolUse {
			chunk.ToolCalls = s.snapshotToolCalls()
		}
	}

	if resp.Usage != nil {
		s.usage = mapUsage(*resp.Usage)
	}

	return chunk, nil
}

func (s *streamAdapter) mergeToolCallDeltas(deltas []openai.ToolCall) {
	for _, delta := range deltas {
		idx := len(s.toolCalls)
		if delta.Index != nil {
			idx = *delta.Index
		} else if len(s.toolCalls) > 0 {
			idx = len(s.toolCalls) - 1
			if delta.ID != "" && s.toolCalls[idx].ID != "" && s.toolCalls[idx].ID != delta.ID {
				idx = len(s.toolCalls)
			}
		}
		if idx < 0 {
			continue
		}

		for len(s.toolCalls) <= idx {
			s.toolCalls = append(s.toolCalls, ToolCall{Type: string(ToolTypeFunction)})
		}

		call := &s.toolCalls[idx]
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Type != "" {
			call.Type = string(delta.Type)
		}
		if call.Type == "" {
			call.Type = string(ToolTypeFunction)
		}
		if delta.Function.Name != "" {
			call.Function.Name += delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			call.Function.Arguments += delta.Function.Arguments
		}
	}
}

func (s *streamAdapter) snapshotToolCalls() []ToolCall {
	if len(s.toolCalls) == 0 {
		return nil
	}

	out := make([]ToolCall, 0, len(s.toolCalls))
	for _, call := range s.toolCalls {
		if call.ID == "" && call.Function.Name == "" && call.Function.Arguments == "" {
			continue
		}
		if call.Type == "" {
			call.Type = string(ToolTypeFunction)
		}
		out = append(out, call)
	}
	return out
}

func (s *streamAdapter) Usage() Usage {
	return s.usage
}

func (s *streamAdapter) Close() error {
	return s.stream.Close()
}
