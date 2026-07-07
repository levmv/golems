package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/tools"
	"github.com/levmv/golems/pkg/tasks"
)

type runConversationIDKey struct{}
type runIDKey struct{}

func withRunConversationID(ctx context.Context, conversationID int64) context.Context {
	return context.WithValue(ctx, runConversationIDKey{}, conversationID)
}

func withRunContext(ctx context.Context, conversationID, runID int64) context.Context {
	ctx = withRunConversationID(ctx, conversationID)
	ctx = tools.WithRunInfo(ctx, conversationID, runID)
	return context.WithValue(ctx, runIDKey{}, runID)
}

func conversationIDFromContext(ctx context.Context) int64 {
	if id, ok := ctx.Value(runConversationIDKey{}).(int64); ok && id > 0 {
		return id
	}
	return mainConversationID
}

func runIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(runIDKey{}).(int64)
	return id, ok && id > 0
}

// Task group and kinds for caliban's scheduled work.
const (
	TaskGroup           = "caliban"
	KindReminderDeliver = "reminder.deliver"
	KindAgentTurn       = "agent.turn"

	reminderMaxAttempts        = 5 // reminders retry on notify failure (delivery guarantee)
	agentTurnMaxAttempts       = 1 // the run isn't under retry; D2's invariant re-runs after a crash
	scheduledTurnNotifyTimeout = 15 * time.Second
)

// ReminderPayload is the stored payload for reminder.deliver tasks.
type ReminderPayload struct {
	ConversationID int64  `json:"conversation_id"`
	Text           string `json:"text"`
}

// AgentTurnPayload is the stored payload for agent.turn tasks.
type AgentTurnPayload struct {
	ConversationID int64  `json:"conversation_id"`
	Prompt         string `json:"prompt"`
}

// Notify pushes to the current run's conversation. It is the sink for the notify
// tool; reminder delivery uses NotifyConversation because task contexts do not
// carry run-local metadata.
func (e *Engine) Notify(ctx context.Context, text string) error {
	return e.NotifyConversation(ctx, conversationIDFromContext(ctx), text)
}

// NotifyConversation fans an out-of-band push to notifiers that own the target
// conversation. It stops at the first error so the tasks queue can retry.
func (e *Engine) NotifyConversation(ctx context.Context, conversationID int64, text string) error {
	e.mu.Lock()
	notifiers := make([]Notifier, len(e.notifiers))
	copy(notifiers, e.notifiers)
	e.mu.Unlock()
	for _, n := range notifiers {
		if err := n.Notify(ctx, conversationID, text); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) notifyScheduledTurn(ctx context.Context, conversationID int64, reply string) {
	e.mu.Lock()
	notifiers := make([]Notifier, len(e.notifiers))
	copy(notifiers, e.notifiers)
	e.mu.Unlock()

	notifyCtx, cancel := context.WithTimeout(ctx, scheduledTurnNotifyTimeout)
	defer cancel()
	for _, n := range notifiers {
		scheduled, ok := n.(scheduledTurnNotifier)
		if !ok {
			continue
		}
		if err := scheduled.NotifyScheduledTurn(notifyCtx, conversationID, reply); err != nil {
			e.logf(warnLevel, "scheduled turn notify: %v", err)
		}
	}
}

// ScheduleReminder enqueues a reminder.deliver task into the current run's
// conversation, falling back to the legacy main conversation outside a run.
func (e *Engine) ScheduleReminder(ctx context.Context, text string, schedule tasks.Schedule) (string, error) {
	payload, err := json.Marshal(ReminderPayload{ConversationID: conversationIDFromContext(ctx), Text: text})
	if err != nil {
		return "", fmt.Errorf("marshal reminder payload: %w", err)
	}
	return e.enqueue(ctx, KindReminderDeliver, payload, schedule, reminderMaxAttempts)
}

// ScheduleTurn enqueues an agent.turn task into the current run's conversation,
// falling back to the legacy main conversation outside a run.
func (e *Engine) ScheduleTurn(ctx context.Context, prompt string, schedule tasks.Schedule) (string, error) {
	payload, err := json.Marshal(AgentTurnPayload{ConversationID: conversationIDFromContext(ctx), Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("marshal agent-turn payload: %w", err)
	}
	return e.enqueue(ctx, KindAgentTurn, payload, schedule, agentTurnMaxAttempts)
}

func (e *Engine) enqueue(ctx context.Context, kind string, payload []byte, schedule tasks.Schedule, maxAttempts int) (string, error) {
	if e.tasks == nil {
		return "", fmt.Errorf("scheduling is not enabled")
	}
	task, err := e.tasks.Enqueue(ctx, tasks.Enqueue{
		Kind:        kind,
		Payload:     payload,
		Schedule:    schedule,
		Group:       TaskGroup,
		MaxAttempts: maxAttempts,
	})
	if err != nil {
		return "", err
	}
	return task.ID, nil
}

// userSchedulable reports whether a task kind is one the user/model created
// through the scheduling tools, as opposed to internal maintenance work
// (compaction) that happens to share the caliban task group. The scheduling
// tools must only see and touch user-facing tasks.
func userSchedulable(kind string) bool {
	return kind == KindReminderDeliver || kind == KindAgentTurn
}

// ListScheduled returns the user's scheduled reminders and agent turns, soonest
// first. Internal maintenance tasks (compaction) share the task group but are
// filtered out — they are not user-facing and must not appear to the model.
func (e *Engine) ListScheduled(ctx context.Context) ([]tasks.Task, error) {
	if e.tasks == nil {
		return nil, fmt.Errorf("scheduling is not enabled")
	}
	all, err := e.tasks.List(ctx, tasks.ListFilter{Group: TaskGroup})
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, t := range all {
		if userSchedulable(t.Kind) {
			out = append(out, t)
		}
	}
	return out, nil
}

// CancelScheduled deletes a user-scheduled task by id. The bool is false if no
// such task existed. It refuses to delete internal maintenance tasks: the model
// could otherwise cancel compaction by guessing its id and damage the machinery
// that keeps the transcript usable.
func (e *Engine) CancelScheduled(ctx context.Context, id string) (bool, error) {
	if e.tasks == nil {
		return false, fmt.Errorf("scheduling is not enabled")
	}
	task, err := e.tasks.Get(ctx, id)
	if errors.Is(err, tasks.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if task.Group != TaskGroup || !userSchedulable(task.Kind) {
		return false, nil // not a user-cancellable task; treat as "no such task"
	}
	return e.tasks.Delete(ctx, id)
}

// HandleReminderDeliver delivers a stored reminder: it appends the firing into
// the transcript (so the model sees it fired) and notifies the user. A notify
// failure is returned so the tasks queue retries — that is the delivery
// guarantee.
//
// The reminder is recorded as a store.RoleEvent row, not an assistant message:
// a fired reminder is an external event, not model output. That keeps it from
// being treated as an assistant turn — context trimming can safely open a window
// on it, and it does not advance the conversation's coverage cursor (it is not a
// user message and does not, by itself, start a run).
//
// The queue is at-least-once, so the handler must be idempotent: a notify
// failure retries the whole task. We append to the transcript only on the
// firing's first attempt (Attempts == 0) and re-notify on retries, so a flaky
// push does not duplicate the transcript row. Attempts resets to 0 when a
// recurring task advances to its next firing, so each firing still records once.
// This leaves one narrow window: a crash after append but before the queue
// records the failure re-runs at Attempts == 0 and double-appends. Closing that
// would need a transactional per-firing dedup key in pkg/tasks; at-least-once
// delivery can still push the notification more than once regardless.
func (e *Engine) HandleReminderDeliver(ctx context.Context, t tasks.Task) error {
	payload, err := tasks.DecodeJSON[ReminderPayload](t)
	if err != nil {
		return tasks.Discardf("decode reminder payload: %v", err)
	}
	if t.Attempts == 0 {
		msg, err := e.store.AppendMessage(ctx, store.Message{
			ConversationID: payload.ConversationID,
			Role:           store.RoleEvent,
			Source:         "reminder",
			Content:        store.Content{Text: payload.Text},
		})
		if err != nil {
			return fmt.Errorf("append reminder message: %w", err)
		}
		e.emit(Event{ConversationID: payload.ConversationID, Message: &msg})
	}
	return e.NotifyConversation(ctx, payload.ConversationID, "⏰ "+payload.Text)
}

// HandleAgentTurn injects a scheduled prompt as a user turn and kicks the worker.
// It is fire-and-forget: the run is not under the task's retry policy (D2's
// invariant re-runs it after a crash anyway).
func (e *Engine) HandleAgentTurn(ctx context.Context, t tasks.Task) error {
	payload, err := tasks.DecodeJSON[AgentTurnPayload](t)
	if err != nil {
		return tasks.Discardf("decode agent-turn payload: %v", err)
	}
	if err := e.SubmitUserMessage(ctx, payload.ConversationID, payload.Prompt, "schedule"); err != nil {
		return fmt.Errorf("submit scheduled turn: %w", err)
	}
	return nil
}
