package llm

import (
	"context"

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
		role := string(m.Role)
		if m.Role == RoleAI {
			role = openai.RoleAssistant
		}

		var toolCalls []openai.ToolCall
		if len(m.ToolCalls) > 0 {
			toolCalls = make([]openai.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				toolCalls[j] = openai.ToolCall{
					ID:   tc.ID,
					Type: openai.ToolType(tc.Type),
					Function: openai.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}

		res[i] = openai.ChatCompletionMessage{
			Role:       role,
			Content:    m.Content,
			ToolCalls:  toolCalls,
			ToolCallID: m.ToolID,
		}
	}
	return res
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

func (a *openAIAdapter) buildBaseRequest(req *Request) openai.ChatCompletionRequest {
	oaiReq := openai.ChatCompletionRequest{
		Model:    req.Model,
		Messages: mapMessages(req.Messages),
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

	return oaiReq
}

func (a *openAIAdapter) Chat(ctx context.Context, req *Request) (*Response, error) {
	oaiReq := a.buildBaseRequest(req)

	resp, err := a.client.CreateChatCompletion(ctx, oaiReq)
	if err != nil {
		return nil, err
	}

	var choice openai.ChatCompletionChoice
	if len(resp.Choices) > 0 {
		choice = resp.Choices[0]
	}

	var outTools []ToolCall
	if len(choice.Message.ToolCalls) > 0 {
		outTools = make([]ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			outTools[i] = ToolCall{
				ID:   tc.ID,
				Type: string(tc.Type),
				Function: ToolFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	}

	return &Response{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ToolCalls:        outTools,
		FinishReason:     mapFinishReason(choice.FinishReason),
		Usage:            mapUsage(resp.Usage),
		Raw:              resp,
	}, nil
}

func (a *openAIAdapter) Stream(ctx context.Context, req *Request) (Stream, error) {
	oaiReq := a.buildBaseRequest(req)

	// Instruct OpenAI to return usage data on the final chunk
	oaiReq.StreamOptions = &openai.StreamOptions{
		IncludeUsage: true,
	}

	stream, err := a.client.CreateChatCompletionStream(ctx, oaiReq)
	if err != nil {
		return nil, err
	}

	return &streamAdapter{
		stream: stream,
	}, nil
}

type streamAdapter struct {
	stream *openai.ChatCompletionStream
	usage  Usage
}

func (s *streamAdapter) Recv() (StreamChunk, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		return StreamChunk{}, err
	}

	chunk := StreamChunk{}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		chunk.Text = choice.Delta.Content
		chunk.ReasoningContent = choice.Delta.ReasoningContent
		chunk.FinishReason = mapFinishReason(choice.FinishReason)
	}

	if resp.Usage != nil {
		s.usage = mapUsage(*resp.Usage)
	}

	return chunk, nil
}

func (s *streamAdapter) Usage() Usage {
	return s.usage
}

func (s *streamAdapter) Close() error {
	return s.stream.Close()
}
