package llm

import "context"

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
	RoleAI     Role = "assistant"
	RoleTool   Role = "tool"
)

type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolID    string     `json:"tool_id,omitempty"`
	Meta      any        `json:"meta,omitempty"`
}

type Request struct {
	Model              string    `json:"model"`
	Messages           []Message `json:"messages"`
	Temperature        *float32  `json:"temperature,omitempty"`
	MaxTokens          *int      `json:"max_tokens,omitempty"`
	ProviderExtensions any       `json:"provider_extensions,omitempty"` // Escape hatch
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Response struct {
	Content          string       `json:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall   `json:"tool_calls,omitempty"`
	FinishReason     FinishReason `json:"finish_reason"`
	Usage            Usage        `json:"usage"`
	Raw              any          `json:"-"` // Keep original response accessible
}

type StreamChunk struct {
	Text             string
	ReasoningContent string
	FinishReason     FinishReason
}

type Stream interface {
	Recv() (StreamChunk, error)
	Usage() Usage // Returns the total usage (valid after EOF)
	Close() error
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"     // Normal completion
	FinishReasonLength        FinishReason = "length"   // Hit token limit
	FinishReasonToolUse       FinishReason = "tool_use" // Model wants to call a tool
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonUnknown       FinishReason = "unknown"
)

type Client interface {
	Chat(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (Stream, error)
}

type Logger interface {
	Debug(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
}

type UsageTracker interface {
	RecordUsage(model string, usage Usage)
}
