// Package murm provides Go backend support for murm-ui: the wire format and a
// translator from golem.StreamEvent to murm-ui stream events, plus an SSE
// writer and a minimal run endpoint. It owns the wire contract and translation;
// routing, auth, and persistence stay app-level behind small interfaces.
//
// The wire types mirror murm-ui/src/core/types.ts. murm-ui uses tagged unions;
// Go uses one flat struct per union with a Type discriminator and omitempty on
// variant fields — wire-only, no behavior, so a flat struct beats interfaces.
//
// See docs/pkg-murm-plan.md and murm-ui/backend-authoritative-chat.md.
package murm

// Role mirrors murm-ui Role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Block type discriminators.
const (
	BlockText       = "text"
	BlockReasoning  = "reasoning"
	BlockToolCall   = "tool_call"
	BlockToolResult = "tool_result"
	BlockArtifact   = "artifact"
	BlockFile       = "file"
)

// Tool call statuses.
const (
	StatusStreaming = "streaming"
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusComplete  = "complete"
	StatusError     = "error"
)

// ContentBlock is a flat mirror of murm-ui's ContentBlock union. Only the fields
// relevant to a block's Type are set; the rest are omitted.
type ContentBlock struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// text / reasoning
	Text          string `json:"text,omitempty"`
	Encrypted     bool   `json:"encrypted,omitempty"`
	EncryptedText string `json:"encryptedText,omitempty"`

	// tool_call
	ToolCallID string `json:"toolCallId,omitempty"`
	Name       string `json:"name,omitempty"`
	ArgsText   string `json:"argsText,omitempty"`
	Status     string `json:"status,omitempty"`

	// tool_result
	OutputText string `json:"outputText,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
}

// TokenUsage mirrors murm-ui TokenUsage.
type TokenUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Total      int `json:"total"`
	CacheRead  int `json:"cacheRead,omitempty"`
	CacheWrite int `json:"cacheWrite,omitempty"`
}

// Message mirrors murm-ui Message. Blocks is always emitted (as [] when empty)
// so message_start carries a concrete, not null, array.
type Message struct {
	ID        string         `json:"id"`
	Role      Role           `json:"role"`
	Blocks    []ContentBlock `json:"blocks"`
	RunID     string         `json:"runId,omitempty"`
	CreatedAt int64          `json:"createdAt,omitempty"`
	UpdatedAt int64          `json:"updatedAt,omitempty"`
	Ephemeral bool           `json:"ephemeral,omitempty"`
	Usage     *TokenUsage    `json:"usage,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// FinishReason mirrors murm-ui FinishReason.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolUse       FinishReason = "tool_use"
	FinishContentFilter FinishReason = "content_filter"
	FinishError         FinishReason = "error"
	FinishAborted       FinishReason = "aborted"
)

// StreamEvent type discriminators.
const (
	EventMessageStart   = "message_start"
	EventTextDelta      = "text_delta"
	EventReasoningDelta = "reasoning_delta"
	EventToolCallStart  = "tool_call_start"
	EventToolCallDelta  = "tool_call_delta"
	EventToolResult     = "tool_result"
	EventArtifact       = "artifact"
	EventUsage          = "usage"
	EventFinish         = "finish"
)

// StreamEvent is a flat mirror of murm-ui's StreamEvent union. The set of
// populated fields depends on Type.
type StreamEvent struct {
	Type string `json:"type"`

	// message_start
	Message *Message `json:"message,omitempty"`

	// text_delta / reasoning_delta / tool_call_delta target a message+block
	MessageID string `json:"messageId,omitempty"`
	BlockID   string `json:"blockId,omitempty"`
	Delta     string `json:"delta,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`

	// tool_call_start / tool_result / artifact carry a full block
	Block *ContentBlock `json:"block,omitempty"`

	// tool_call_delta
	Name      string `json:"name,omitempty"`
	ArgsDelta string `json:"argsDelta,omitempty"`
	Status    string `json:"status,omitempty"`

	// usage
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	Total      int `json:"total,omitempty"`
	CacheRead  int `json:"cacheRead,omitempty"`
	CacheWrite int `json:"cacheWrite,omitempty"`

	// finish
	Reason FinishReason `json:"reason,omitempty"`
}

// RunRequest is the POST /api/chats/{chatId}/runs body.
type RunRequest struct {
	Input             string `json:"input"`
	ClientMessageID   string `json:"clientMessageId"`
	ClientTextBlockID string `json:"clientTextBlockId,omitempty"`
	Stream            bool   `json:"stream"`
}
