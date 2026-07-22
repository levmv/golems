package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

// KindReflection is the task kind for the periodic self-reflection pass.
const KindReflection = "reflection"

const (
	// reflectionTaskID is the fixed id of the single recurring reflection task.
	reflectionTaskID = "reflection-self"
	// reflectionCron runs reflection once a day at a quiet local hour (00:45).
	reflectionCron = "45 0 * * *"
	// reflectionWindowMessages bounds how much recent transcript reflection reads.
	reflectionWindowMessages = 120
	// reflectionNoChange is the sentinel the model returns to leave the persona as-is.
	reflectionNoChange = "NO_CHANGE"
	// maxPersonaBytes caps a self-written persona. Persona is identity, not a
	// notebook; a wildly oversized "update" is treated as a model mistake and
	// dropped rather than overwriting PERSONA.md.
	maxPersonaBytes = 8192
)

const reflectionPrompt = `You are performing a quiet, periodic self-reflection for Caliban, a personal AI
assistant. This is not a conversation with the user; nothing you write here is
shown to them. Your only job is to decide whether your durable self-description
should change.

You are given your current self-description (PERSONA.md) and a sample of recent
activity. Consider only stable, identity-level signal: how you actually work
with this user, your voice and tone, recurring preferences for how you should
behave, and standing corrections the user has made about how you operate.

Be very conservative. The default is no change. Only revise when the recent
activity shows a clear, repeated, lasting signal that your current description
gets wrong or is missing. Do not react to a single message, a one-off mood, or a
transient task. Never invent traits the evidence does not support.

Keep PERSONA.md short and high-signal: who you are, how you work with this user,
your voice. It is identity, not a notebook. Durable facts ABOUT the user and
project decisions do NOT belong here — those are separate memory. Do not copy
private user details into the persona.

If nothing should change, reply with exactly: NO_CHANGE
Otherwise reply with the complete new PERSONA.md content (not a diff), preserving
everything still true and folding in only what genuinely changed.`

// ensureReflectionSchedule registers the single recurring self-reflection task,
// if scheduling is enabled and it is not already scheduled. It is called once at
// Start; the cron schedule lives in code (not config), so the task is created on
// first boot and left alone thereafter. An exhausted prior task is cleared and
// recreated, mirroring compaction, so a one-time failure does not silence
// reflection forever.
func (e *Engine) ensureReflectionSchedule(ctx context.Context) {
	if e.tasks == nil {
		return
	}
	existing, err := e.tasks.Get(ctx, reflectionTaskID)
	switch {
	case err == nil:
		if !existing.Exhausted() {
			return // already scheduled
		}
		if _, derr := e.tasks.Delete(ctx, reflectionTaskID); derr != nil {
			e.logf(warnLevel, "delete exhausted reflection task: %v", derr)
			return
		}
	case !errors.Is(err, tasks.ErrNotFound):
		e.logf(warnLevel, "reflection schedule check: %v", err)
		return
	}

	if _, err := e.tasks.Enqueue(ctx, tasks.Enqueue{
		ID:          reflectionTaskID,
		Kind:        KindReflection,
		Schedule:    tasks.Cron(reflectionCron, e.loc.String()),
		Group:       TaskGroup,
		MaxAttempts: 1,
	}); err != nil {
		e.logf(warnLevel, "enqueue reflection: %v", err)
		return
	}
	e.logf(infoLevel, "scheduled daily self-reflection (cron %q tz %s)", reflectionCron, e.loc.String())
}

// HandleReflection runs one self-reflection pass. It is deliberately
// fault-tolerant: any failure is logged and swallowed (return nil) so the
// recurring cron advances to the next day. Returning an error would let the
// queue exhaust the task's single attempt and set NextRunAt nil, permanently
// killing the schedule (see tasks.Queue.finishFailed) — the wrong outcome for a
// best-effort maintenance pass that naturally retries tomorrow.
func (e *Engine) HandleReflection(ctx context.Context, _ tasks.Task) error {
	if err := e.reflect(ctx); err != nil {
		e.logf(warnLevel, "self-reflection: %v", err)
	}
	return nil
}

// reflect loads the current persona and recent activity, asks the main model
// whether the persona should evolve, and writes it back only on a real change.
// The model call and the persona write run under a silent maintenance run so the
// cost is attributed and audited without touching the transcript.
func (e *Engine) reflect(ctx context.Context) error {
	current, err := e.workspace.Persona()
	if err != nil {
		return fmt.Errorf("load persona: %w", err)
	}
	activity, err := e.recentActivity(ctx)
	if err != nil {
		return fmt.Errorf("gather activity: %w", err)
	}
	if strings.TrimSpace(activity) == "" {
		// Still log: a maintenance pass that decided to do nothing must be
		// visible, or the feature looks dead when it is merely quiet.
		e.logf(infoLevel, "self-reflection: no recent user activity; skipped")
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, defaultRunTimeout)
	defer cancel()
	return e.runMaintenance(runCtx, mainConversationID, e.modelID, func(ctx context.Context) (llm.Usage, error) {
		updated, usage, err := e.reflectOnce(ctx, current, activity)
		if err != nil {
			return usage, err
		}
		if updated == "" {
			e.logf(infoLevel, "self-reflection: no change (tokens in=%d out=%d)", usage.PromptTokens, usage.CompletionTokens)
			return usage, nil
		}
		if err := e.workspace.WritePersona(updated); err != nil {
			return usage, fmt.Errorf("write persona: %w", err)
		}
		if err := e.workspace.Commit("self-reflection: update persona"); err != nil {
			e.logf(warnLevel, "self-reflection: commit persona: %v", err)
		}
		e.logf(infoLevel, "self-reflection: persona updated (tokens in=%d out=%d)", usage.PromptTokens, usage.CompletionTokens)
		return usage, nil
	})
}

// reflectOnce runs the single conservative model call. It returns the new
// persona text, or "" when the model declined to change anything (the sentinel,
// an empty reply, or an over-budget reply that is treated as a mistake).
func (e *Engine) reflectOnce(ctx context.Context, current, activity string) (string, llm.Usage, error) {
	var b strings.Builder
	b.WriteString("## Current PERSONA.md\n\n")
	if strings.TrimSpace(current) == "" {
		b.WriteString("(empty — you have not written a self-description yet)\n")
	} else {
		b.WriteString(strings.TrimSpace(current))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(activity)

	resp, err := e.mainRequester.Request(ctx, 0, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: reflectionPrompt},
			{Role: llm.RoleUser, Content: b.String()},
		},
	}, false, nil)
	if err != nil {
		return "", llm.Usage{}, err
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" || strings.EqualFold(strings.TrimRight(out, ". \n"), reflectionNoChange) {
		return "", resp.Usage, nil
	}
	if len(out) > maxPersonaBytes {
		e.logf(warnLevel, "self-reflection: persona update too large (%d bytes); ignoring", len(out))
		return "", resp.Usage, nil
	}
	return out, resp.Usage, nil
}

// recentActivity renders recent activity for reflection input. It samples every
// user-facing conversation (all active top-level conversations except the private
// free-time journal), so reflection sees the user wherever they actually talk —
// Telegram, web, or both — instead of one hardcoded conversation. It is built as
// additive sections so reflection can later weigh the agent's own free-time work
// as a second source; for now there is the single section below, the recent
// user<->assistant transcript across those conversations.
func (e *Engine) recentActivity(ctx context.Context) (string, error) {
	ids, err := e.userConversationIDs(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, id := range ids {
		msgs, _, err := e.store.MessagesTail(ctx, id, reflectionWindowMessages)
		if err != nil {
			return "", err
		}
		if len(msgs) == 0 {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("## Recent interactions\n\n")
		}
		for _, m := range msgs {
			b.WriteString(renderForSummary(m))
		}
	}
	return b.String(), nil
}

// userConversationIDs lists the active top-level conversations that represent the
// user — every transport's conversation — excluding the reserved free-time journal
// (matched by its sentinel uuid), whose private reasoning must never shape the
// persona. Subagent conversations are children (parent_run_id set) and are already
// absent from ActiveConversations.
func (e *Engine) userConversationIDs(ctx context.Context) ([]int64, error) {
	convs, err := e.store.ActiveConversations(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(convs))
	for _, c := range convs {
		if c.UUID == freeTimeConversationUUID {
			continue
		}
		ids = append(ids, c.ID)
	}
	return ids, nil
}
