package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

// KindCompaction is the task kind for folding old transcript into the summary.
const KindCompaction = "compaction"

// CompactionPayload identifies the conversation to compact.
type CompactionPayload struct {
	ConversationID int64 `json:"conversation_id"`
}

const compactionSummarizerPrompt = `You maintain a rolling summary of a long, ongoing conversation between a user
and their personal assistant. You are given the previous summary (if any) and
the next chunk of transcript that is about to scroll out of the model's window.

Produce an updated summary that folds the chunk into the previous summary.
Preserve: decisions, facts about the user, commitments and open threads, and
anything the user would expect remembered later. Drop pleasantries and
mechanical tool chatter. Be concise and factual; write prose, not a transcript.
Output only the summary text.`

// maybeScheduleCompaction enqueues a compaction task when the post-summary tail
// has grown past the budget. It is cheap to call after every run: it does one
// token estimate and enqueues at most one pending task per conversation.
func (e *Engine) maybeScheduleCompaction(ctx context.Context, conversationID int64) {
	if e.tasks == nil || e.cheap == nil {
		return
	}
	tail, _, err := e.tailAfterSummary(ctx, conversationID)
	if err != nil {
		e.logf(warnLevel, "conversation %d: compaction check: %v", conversationID, err)
		return
	}
	tailTokens := estMessagesTokens(tail)
	if tailTokens <= e.tailTok {
		return
	}
	id := fmt.Sprintf("compaction-%d", conversationID)
	if existing, err := e.tasks.Get(ctx, id); err == nil {
		if !existing.Exhausted() {
			return // a compaction is already pending
		}
		// A prior compaction exhausted its retries; the queue keeps the dead row,
		// which would otherwise suppress every future compaction for this
		// conversation. Clear it so a fresh one can be enqueued.
		if _, err := e.tasks.Delete(ctx, id); err != nil {
			e.logf(warnLevel, "delete exhausted compaction task: %v", err)
			return
		}
	}
	payload, err := json.Marshal(CompactionPayload{ConversationID: conversationID})
	if err != nil {
		e.logf(warnLevel, "marshal compaction payload: %v", err)
		return
	}
	_, err = e.tasks.Enqueue(ctx, tasks.Enqueue{
		ID:          id,
		Kind:        KindCompaction,
		Payload:     payload,
		Schedule:    tasks.Once(time.Now()),
		Group:       TaskGroup,
		MaxAttempts: 3,
	})
	if err != nil {
		e.logf(warnLevel, "enqueue compaction: %v", err)
		return
	}
	e.logf(infoLevel, "conversation %d: scheduled compaction (tail_est=%d budget=%d msgs=%d)",
		conversationID, tailTokens, e.tailTok, len(tail))
}

// HandleCompaction folds the oldest part of the tail into the rolling summary
// using the cheap model. It is idempotent: if the tail is already within budget
// (a prior compaction handled it), it does nothing.
func (e *Engine) HandleCompaction(ctx context.Context, t tasks.Task) error {
	if e.cheap == nil {
		return tasks.Discard("compaction: no cheap model configured")
	}
	payload, err := tasks.DecodeJSON[CompactionPayload](t)
	if err != nil {
		return tasks.Discardf("decode compaction payload: %v", err)
	}
	conversationID := payload.ConversationID

	tail, prev, err := e.tailAfterSummary(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("load tail: %w", err)
	}
	tailTokens := estMessagesTokens(tail)
	if tailTokens <= e.tailTok {
		return nil // already within budget
	}

	// Keep the most recent messages verbatim (keepTok); fold the rest.
	fold := foldPoint(tail, e.keepTok)

	// Never fold past the coverage cursor. A summary advances the window's
	// starting point (assembleContext loads messages after through_message_id),
	// so folding an unanswered user message would drop it from the very run that
	// is about to answer it. Folding only covered messages keeps uncovered input
	// visible to the worker.
	cover, err := e.store.CoveredThrough(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("covered-through: %w", err)
	}
	for fold > 0 && tail[fold-1].ID > cover {
		fold--
	}
	if fold <= 0 {
		e.logf(infoLevel, "conversation %d: compaction skipped; no covered messages safe to fold (tail_est=%d budget=%d cover=%d)",
			conversationID, tailTokens, e.tailTok, cover)
		return nil // nothing safe to fold yet
	}
	folded := tail[:fold]
	throughID := folded[len(folded)-1].ID
	keptTokens := estMessagesTokens(tail[fold:])
	e.logf(infoLevel, "compacting conversation %d through message %d (%d/%d msgs, tail_est=%d kept_est=%d budget=%d)",
		conversationID, throughID, len(folded), len(tail), tailTokens, keptTokens, e.tailTok)

	return e.runMaintenance(ctx, conversationID, e.cheapModelID, func(ctx context.Context) (llm.Usage, error) {
		summary, usage, err := e.summarize(ctx, prev, folded)
		if err != nil {
			return usage, fmt.Errorf("summarize: %w", err)
		}
		if _, err := e.store.AppendSummary(ctx, store.Summary{
			ConversationID:   conversationID,
			ThroughMessageID: throughID,
			Content:          summary,
		}); err != nil {
			return usage, fmt.Errorf("append summary: %w", err)
		}
		e.logf(infoLevel, "compacted conversation %d through message %d (%d msgs folded, summary_tokens in=%d out=%d)",
			conversationID, throughID, len(folded), usage.PromptTokens, usage.CompletionTokens)
		return usage, nil
	})
}

// summarize asks the cheap model to fold the messages into the previous summary.
func (e *Engine) summarize(ctx context.Context, prev string, folded []store.Message) (string, llm.Usage, error) {
	var b strings.Builder
	if strings.TrimSpace(prev) != "" {
		b.WriteString("## Previous summary\n\n")
		b.WriteString(strings.TrimSpace(prev))
		b.WriteString("\n\n")
	}
	b.WriteString("## Transcript chunk to fold in\n\n")
	for _, m := range folded {
		b.WriteString(renderForSummary(m))
	}

	resp, err := e.cheap.Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: compactionSummarizerPrompt},
			{Role: llm.RoleUser, Content: b.String()},
		},
	})
	if err != nil {
		return "", llm.Usage{}, err
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", resp.Usage, fmt.Errorf("model returned empty summary")
	}
	return out, resp.Usage, nil
}

// renderForSummary turns a stored message into a compact text line for the
// summarizer. Tool calls/results are noted briefly, not dumped verbatim.
func renderForSummary(m store.Message) string {
	switch m.Role {
	case llm.RoleUser:
		return "User: " + m.Content.Text + "\n"
	case llm.RoleAI:
		var b strings.Builder
		if m.Content.Text != "" {
			b.WriteString("Assistant: " + m.Content.Text + "\n")
		}
		for _, tc := range m.Content.ToolCalls {
			b.WriteString("Assistant called " + tc.Function.Name + "\n")
		}
		return b.String()
	case llm.RoleTool:
		return "Tool result: " + truncate(m.Content.Text, 200) + "\n"
	case store.RoleEvent:
		return "Reminder fired: " + m.Content.Text + "\n"
	default:
		return ""
	}
}

// tailAfterSummary returns the messages after the latest summary and that
// summary's text ("" if none).
func (e *Engine) tailAfterSummary(ctx context.Context, conversationID int64) ([]store.Message, string, error) {
	summary, has, err := e.store.LatestSummary(ctx, conversationID)
	if err != nil {
		return nil, "", err
	}
	var afterID int64
	if has {
		afterID = summary.ThroughMessageID
	}
	msgs, err := e.store.MessagesAfter(ctx, conversationID, afterID)
	if err != nil {
		return nil, "", err
	}
	return msgs, summary.Content, nil
}

// foldPoint returns the index splitting msgs into folded (msgs[:k]) and kept
// (msgs[k:]), keeping the most recent messages up to keepTokens. It aligns the
// boundary so the kept portion opens on a clean user turn instead of a dangling
// tool result or assistant tool-call block.
func foldPoint(msgs []store.Message, keepTokens int) int {
	total := 0
	k := len(msgs)
	for k > 0 {
		t := estContentTokens(msgs[k-1].Content)
		if total+t > keepTokens {
			break
		}
		total += t
		k--
	}
	// Don't let the kept portion start on a tool result orphaned from its call.
	for k < len(msgs) && msgs[k].Role == llm.RoleTool {
		k++
	}
	// Providers are stricter about history that starts mid-turn. If the compacted
	// tail opens on an assistant/tool block, fold forward to the next user turn.
	for k < len(msgs)-1 && msgs[k].Role != llm.RoleUser {
		k++
	}
	if k >= len(msgs) {
		// Everything is recent enough to keep; fold all but the last message so
		// we still make progress when a single chunk exceeds the budget.
		return len(msgs) - 1
	}
	return k
}

func estContentTokens(c store.Content) int {
	n := len(c.Text)
	for _, tc := range c.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	n += len(c.ToolCallID)
	return n/4 + 4
}

func estMessagesTokens(msgs []store.Message) int {
	total := 0
	for _, m := range msgs {
		total += estContentTokens(m.Content)
	}
	return total
}
