package murm

import (
	"time"

	"github.com/levmv/golems/pkg/golem"
)

// Translator converts a single golem run's stream into murm-ui stream events.
// It is stateful and not safe for concurrent use: create one per run and feed
// golem events in order via Feed.
//
// golem has no message-boundary events, so the translator synthesizes them:
// message_start (assistant, iteration N) is emitted lazily on the first
// assistant content after a boundary; a tool result ends the tool phase and the
// next assistant content starts iteration N+1.
type Translator struct {
	run  string
	emit func(StreamEvent)
	now  func() int64

	iteration        int
	assistantStarted bool
	toolPhase        bool // a tool result was emitted; next assistant content bumps iteration
}

// NewTranslator creates a translator for the run identified by runID (the
// client-generated user message id). emit receives the translated events.
func NewTranslator(runID string, emit func(StreamEvent)) *Translator {
	return &Translator{
		run:  runID,
		emit: emit,
		now:  func() int64 { return time.Now().UnixMilli() },
	}
}

// Feed translates one golem event, emitting zero or more murm events.
func (t *Translator) Feed(ev golem.StreamEvent) {
	switch ev.Kind {
	case golem.EventTextDelta:
		t.ensureAssistant()
		t.emit(StreamEvent{
			Type:      EventTextDelta,
			MessageID: t.assistantID(),
			BlockID:   textBlockID(t.run, t.iteration),
			Delta:     ev.Text,
		})
	case golem.EventReasoningDelta:
		t.ensureAssistant()
		t.emit(StreamEvent{
			Type:      EventReasoningDelta,
			MessageID: t.assistantID(),
			BlockID:   reasoningBlockID(t.run, t.iteration),
			Delta:     ev.Text,
		})
	case golem.EventToolCall:
		t.ensureAssistant()
		t.emit(StreamEvent{
			Type:      EventToolCallStart,
			MessageID: t.assistantID(),
			Block: &ContentBlock{
				ID:         toolCallBlockID(t.run, ev.Step.ToolCallID),
				Type:       BlockToolCall,
				ToolCallID: ev.Step.ToolCallID,
				Name:       ev.Step.ToolName,
				ArgsText:   ev.Step.Arguments,
				Status:     StatusRunning,
			},
		})
	case golem.EventToolResult:
		t.emitToolOutcome(ev.Step.ToolCallID, ev.Step.Result, StatusComplete, false)
	case golem.EventToolError:
		t.emitToolOutcome(ev.Step.ToolCallID, ev.Step.Error, StatusError, true)
	case golem.EventAttemptReset:
		if !t.assistantStarted {
			return
		}
		// murm-ui deliberately has no provider-specific retry event, but a
		// message_start carrying a non-empty block list replaces the current
		// streamed blocks. Reset to one empty text block, then let the next
		// attempt reuse the stable message and block ids.
		t.emit(StreamEvent{Type: EventMessageStart, Message: &Message{
			ID:   t.assistantID(),
			Role: RoleAssistant,
			Blocks: []ContentBlock{{
				ID:   textBlockID(t.run, t.iteration),
				Type: BlockText,
			}},
			RunID: t.run,
		}})
	case golem.EventDone:
		t.emit(StreamEvent{
			Type:      EventUsage,
			Input:     ev.Usage.PromptTokens,
			Output:    ev.Usage.CompletionTokens,
			Total:     ev.Usage.TotalTokens,
			CacheRead: ev.Usage.CachedTokens,
		})
		t.emit(StreamEvent{Type: EventFinish, Reason: mapFinishReason(ev.FinishReason)})
	}
}

// Finish emits a terminal finish event with an explicit reason (e.g. aborted on
// client disconnect, or error on a run failure). Use when the golem stream did
// not reach EventDone.
func (t *Translator) Finish(reason FinishReason) {
	t.emit(StreamEvent{Type: EventFinish, Reason: reason})
}

// emitToolOutcome closes a tool call (status complete/error), opens a tool-role
// message, and emits its result block — the OpenAI-shape convention from the
// contract. It marks the tool phase so the next assistant content bumps the
// iteration.
func (t *Translator) emitToolOutcome(toolCallID, output, status string, isError bool) {
	t.emit(StreamEvent{
		Type:      EventToolCallDelta,
		MessageID: t.assistantID(),
		BlockID:   toolCallBlockID(t.run, toolCallID),
		Status:    status,
	})
	toolMsgID := toolMessageID(t.run, toolCallID)
	t.emit(StreamEvent{
		Type: EventMessageStart,
		Message: &Message{
			ID:        toolMsgID,
			Role:      RoleTool,
			Blocks:    []ContentBlock{},
			RunID:     t.run,
			CreatedAt: t.now(),
		},
	})
	t.emit(StreamEvent{
		Type:      EventToolResult,
		MessageID: toolMsgID,
		Block: &ContentBlock{
			ID:         toolResultBlockID(t.run, toolCallID),
			Type:       BlockToolResult,
			ToolCallID: toolCallID,
			OutputText: output,
			IsError:    isError,
		},
	})
	t.toolPhase = true
}

// ensureAssistant starts (lazily) the assistant message for the current
// iteration, bumping the iteration first if a tool phase just ended.
func (t *Translator) ensureAssistant() {
	if t.toolPhase {
		t.iteration++
		t.assistantStarted = false
		t.toolPhase = false
	}
	if t.assistantStarted {
		return
	}
	t.assistantStarted = true
	t.emit(StreamEvent{
		Type: EventMessageStart,
		Message: &Message{
			ID:        t.assistantID(),
			Role:      RoleAssistant,
			Blocks:    []ContentBlock{},
			RunID:     t.run,
			CreatedAt: t.now(),
		},
	})
}

func (t *Translator) assistantID() string { return assistantMessageID(t.run, t.iteration) }
