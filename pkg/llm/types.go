package llm

import (
	"context"
	"time"

	"github.com/levmv/golems/pkg/jsonschema"
)

type Role string

const (
	RoleSystem Role = "system"
	RoleUser   Role = "user"
	RoleAI     Role = "assistant"
	RoleTool   Role = "tool"
)

type Message struct {
	Role             Role       `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"-"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Meta             any        `json:"meta,omitempty"`
	// CreatedAt is when the message was produced: for an assistant message when
	// the model finished it, for a tool message when the tool returned. Set by
	// the producing layer (the golem agent loop); zero when nothing stamped it.
	// Never sent to the provider — the adapter maps wire fields explicitly.
	CreatedAt time.Time `json:"-"`
}

type Request struct {
	// Model is set by Model.Chat and Model.Stream from the bound model handle.
	// Callers using Model directly can leave this empty; any value is ignored.
	Model              string      `json:"model"`
	Messages           []Message   `json:"messages"`
	Temperature        *float32    `json:"temperature,omitempty"`
	MaxTokens          *int        `json:"max_tokens,omitempty"`
	Tools              []Tool      `json:"tools,omitempty"`
	ToolChoice         *ToolChoice `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool       `json:"parallel_tool_calls,omitempty"`
	ProviderExtensions any         `json:"provider_extensions,omitempty"` // Escape hatch
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		PromptTokens:     u.PromptTokens + other.PromptTokens,
		CompletionTokens: u.CompletionTokens + other.CompletionTokens,
		CachedTokens:     u.CachedTokens + other.CachedTokens,
		TotalTokens:      u.TotalTokens + other.TotalTokens,
	}
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
	// ToolCalls is emitted as a complete snapshot when FinishReason is FinishReasonToolUse.
	// Consumers should keep the latest non-empty snapshot rather than append chunks.
	ToolCalls    []ToolCall
	FinishReason FinishReason
}

type Stream interface {
	Recv() (StreamChunk, error)
	// Usage returns the total usage after Recv returns io.EOF. Before EOF, it may be zero.
	Usage() Usage
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

type ToolType string

const (
	ToolTypeFunction ToolType = "function"
)

type Tool struct {
	Type     ToolType       `json:"type"`
	Function ToolDefinition `json:"function"`
}

type ToolDefinition struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parameters  jsonschema.Schema `json:"parameters"`
	Strict      bool              `json:"strict,omitempty"`
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceFunction ToolChoiceMode = "function"
)

type ToolChoice struct {
	Mode ToolChoiceMode `json:"mode"`
	Name string         `json:"name,omitempty"`
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
