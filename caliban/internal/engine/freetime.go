package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/tasks"
)

// freeTimeEnabled gates free-time entirely. It is OFF: free-time is built and
// tested but not turned on. When false, no free-time conversation is created and
// no free-time task is scheduled, so nothing ever runs; the handler, tool scope,
// and run profile still exist so flipping this to true is the only change needed
// to enable it. There is deliberately no config option — this is an experiment
// that is either promoted (then a flag) or deleted.
const freeTimeEnabled = false

// KindFreeTime is the task kind that kicks off a free-time session.
const KindFreeTime = "freetime"

const (
	// freeTimeConversationUUID is the reserved, code-owned identity of the private
	// free-time conversation. No transport owns it, so everything done there is
	// silent by construction; its transcript is a private journal. It is keyed by a
	// fixed sentinel uuid, not a fixed int id, because delegation creates child
	// conversations with auto-increment ids and would collide with any low reserved
	// number; the conversation's actual int id is resolved from this uuid at Start.
	// Hand-authored reserved UUIDv7 (version nibble 7, RFC4122 variant byte 8).
	freeTimeConversationUUID = "00000000-0000-7000-8000-000000000003"
	// freeTimeTaskID is the fixed id of the single recurring free-time task.
	freeTimeTaskID = "freetime-self"
	// freeTimeCron fires free-time once a day at 11:11 local. It must fall inside
	// the server's uptime window (~09:00–01:00); a downtime slot would only fire
	// late at the next boot.
	freeTimeCron = "11 11 * * *"
)

// freeTimePrompt re-frames the standard system prompt for unattended self-
// directed work. It is appended after the normal prompt (persona, memory, the
// free-time conversation's own rolling summary), so free-time acts in character.
const freeTimePrompt = `This is free time. No user is waiting, and nothing here is shown to anyone unless
you deliberately record it. Use the time as you wish: explore an idea, build
something of your own in playground/, tidy or extend your notes, or follow a
curiosity from earlier conversations. (projects/ is for the user's work, not free
time.)

You are working alone in your private workspace. Continue only while it is
genuinely useful — it is completely fine to do a little and stop. Only your files
and memory persist, so write down anything worth keeping. Do not contact the user
and do not schedule anything.`

// freeTimeKickPrompt is the turn injected to start a free-time session; the
// guidance lives in freeTimePrompt above.
const freeTimeKickPrompt = "Free time. Spend it however you find worthwhile."

// runProfile selects the tool set, prompt suffix, and tool-iteration ceiling for
// a run. Everything except the free-time conversation uses the defaults, so this
// is inert for user/telegram/web/scheduled runs.
type runProfile struct {
	tools             []golem.Tool
	maxToolIterations int
	promptSuffix      string
}

// systemPrompt appends the profile's suffix (if any) to the assembled prompt.
func (p runProfile) systemPrompt(base string) string {
	if p.promptSuffix == "" {
		return base
	}
	return base + "\n\n" + p.promptSuffix
}

func (e *Engine) runProfile(conversationID int64) runProfile {
	if e.freeTimeConvID != 0 && conversationID == e.freeTimeConvID {
		return runProfile{
			tools:             e.freeTimeTools(),
			maxToolIterations: e.maxToolIter,
			promptSuffix:      freeTimePrompt,
		}
	}
	return runProfile{tools: e.tools, maxToolIterations: e.maxToolIter}
}

// freeTimeTools is the main tool set minus everything that reaches the user, fans
// out, or spends money outside caliban's own (DeepSeek) accounting:
//   - notify / schedule_* — free-time must not contact the user or schedule work;
//   - delegate / delegate_continue — avoid subagent fan-out (24x the run's calls);
//   - runner_* — external paid agents, invisible to caliban's token accounting.
//
// What remains is inward, bounded work: shell (+ background tasks) to actually
// build things, memory to keep what it learns, history_search to read the main
// chat, and skills. To let free-time use runners later, restrict them to "agy"
// (free quota) via an allow-list on RunnerTools rather than re-adding them here.
func (e *Engine) freeTimeTools() []golem.Tool {
	out := make([]golem.Tool, 0, len(e.tools))
	for _, tool := range e.tools {
		switch tool.Definition.Function.Name {
		case "notify",
			"schedule_reminder", "schedule_turn", "list_scheduled", "cancel_scheduled",
			"delegate", "delegate_continue",
			"runner_list", "runner_models", "runner_run":
			continue
		}
		out = append(out, tool)
	}
	return out
}

// ensureFreeTimeConversation resolves (creating if needed) the reserved private
// conversation by its sentinel uuid and caches its int id, so Start gives it a
// worker and runProfile can recognize it. Must run before ActiveConversations is
// read and before any worker is spawned. No-op when free-time is disabled — the
// id stays 0, so runProfile never matches a real conversation (in particular not a
// delegation child that happened to take the old reserved id).
func (e *Engine) ensureFreeTimeConversation(ctx context.Context) error {
	if !freeTimeEnabled {
		return nil
	}
	conv, err := e.store.EnsureConversationByUUID(ctx, freeTimeConversationUUID)
	if err != nil {
		return fmt.Errorf("ensure free-time conversation: %w", err)
	}
	e.freeTimeConvID = conv.ID
	e.logf(infoLevel, "free-time conversation ready (id %d uuid %s)", conv.ID, conv.UUID)
	return nil
}

// ensureFreeTimeSchedule registers the single recurring free-time task, mirroring
// ensureReflectionSchedule (idempotent, clears an exhausted prior task). No-op
// when free-time is disabled or scheduling is unavailable.
func (e *Engine) ensureFreeTimeSchedule(ctx context.Context) {
	if !freeTimeEnabled || e.tasks == nil {
		return
	}
	existing, err := e.tasks.Get(ctx, freeTimeTaskID)
	switch {
	case err == nil:
		if !existing.Exhausted() {
			return
		}
		if _, derr := e.tasks.Delete(ctx, freeTimeTaskID); derr != nil {
			e.logf(warnLevel, "delete exhausted free-time task: %v", derr)
			return
		}
	case !errors.Is(err, tasks.ErrNotFound):
		e.logf(warnLevel, "free-time schedule check: %v", err)
		return
	}

	if _, err := e.tasks.Enqueue(ctx, tasks.Enqueue{
		ID:          freeTimeTaskID,
		Kind:        KindFreeTime,
		Schedule:    tasks.Cron(freeTimeCron, e.loc.String()),
		Group:       TaskGroup,
		MaxAttempts: 1,
	}); err != nil {
		e.logf(warnLevel, "enqueue free-time: %v", err)
		return
	}
	e.logf(infoLevel, "scheduled daily free-time (cron %q tz %s)", freeTimeCron, e.loc.String())
}

// HandleFreeTime kicks off a free-time session by injecting a turn into the
// private conversation; the conversation worker then runs it with the free-time
// run profile. Like reflection, failures are logged and swallowed (return nil):
// the run itself is owned by the worker, not the task, and a recurring cron task
// must not be killed by an exhausted attempt (see tasks.Queue.finishFailed).
func (e *Engine) HandleFreeTime(ctx context.Context, _ tasks.Task) error {
	conv, ok, err := e.store.ConversationByUUID(ctx, freeTimeConversationUUID)
	if err != nil {
		e.logf(warnLevel, "free-time: resolve conversation: %v", err)
		return nil
	}
	if !ok {
		e.logf(warnLevel, "free-time: conversation not found (uuid %s)", freeTimeConversationUUID)
		return nil
	}
	if err := e.SubmitUserMessage(ctx, conv.ID, freeTimeKickPrompt, "freetime"); err != nil {
		e.logf(warnLevel, "free-time: %v", err)
	}
	return nil
}
