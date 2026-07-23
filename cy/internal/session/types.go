package session

import (
	"encoding/json"
	"time"

	"github.com/levmv/golems/pkg/llm"
)

type RecordType string

const (
	RecordSessionStarted      RecordType = "session_started"
	RecordModelChanged        RecordType = "model_changed"
	RecordUserMessage         RecordType = "user_message"
	RecordAssistantMessage    RecordType = "assistant_message"
	RecordToolResult          RecordType = "tool_result"
	RecordRunFinished         RecordType = "run_finished"
	RecordCompactionCompleted RecordType = "compaction_completed"
)

// Record is one append-only session event. Sequence numbers are sufficient to
// detect a damaged or reordered local journal; records do not need their own
// globally unique identity.
type Record struct {
	Seq       uint64          `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Type      RecordType      `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type SessionStarted struct {
	Workspace string `json:"workspace"`
	Model     string `json:"model"`
}

type ModelChanged struct {
	Model string `json:"model"`
}

type UserMessage struct {
	RunID   string `json:"run_id"`
	Content string `json:"content"`
}

type AssistantMessage struct {
	RunID     string         `json:"run_id"`
	Content   string         `json:"content"`
	Reasoning string         `json:"reasoning,omitempty"`
	ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"`
	Usage     llm.Usage      `json:"usage"`
}

type ToolResult struct {
	RunID      string `json:"run_id"`
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	Meta       any    `json:"meta,omitempty"`
}

type RunFinished struct {
	RunID string `json:"run_id"`
}

type CompactionCompleted struct {
	CoveredThroughSeq uint64    `json:"covered_through_seq"`
	FirstVerbatimSeq  uint64    `json:"first_verbatim_seq"`
	Summary           string    `json:"summary"`
	Usage             llm.Usage `json:"usage"`
}

func DecodePayload[T any](record Record) (T, error) {
	var payload T
	err := json.Unmarshal(record.Payload, &payload)
	return payload, err
}
