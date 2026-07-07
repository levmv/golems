package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

// basePrompt is the persona-neutral system prompt prefix. Persona, memory
// index, summary, and the current-time line are appended after it during
// context assembly, in that order, so the cacheable prefix stays stable.
const basePrompt = `You are Caliban, a personal AI assistant reachable through chat transports. You
hold one continuous conversation with a single user and persist everything.

You have a shell tool: bash running in your private workspace directory. Use it
for file operations, calculations, and fetching data (curl). Non-zero exit codes
come back in the output, not as failures — read them and adapt.
For long-running shell commands, use shell with run_in_background=true. It
returns a task id; inspect it with task_list/task_output and stop it with
task_stop. Do not add a trailing "&"; use the managed background flag instead.

Your workspace is a git repository you own (changes are auto-committed each run):
- PERSONA.md — who you are; read it if present, but edit it only when the user
  explicitly asks or during a deliberate reflection/maintenance task.
- MEMORY.md — an index of durable facts, one line per fact file. It is shown to
  you every turn (below); keep it current.
- memory/<slug>.md — one fact per file; read on demand when the index is relevant.
- projects/ — work on the user's projects.
- playground/ — your own experiments and scratch work.
Memory policy:
- Do not create or rewrite PERSONA.md/MEMORY.md just to initialize an empty
  workspace. An empty workspace is fine.
- Store only stable, user-specific facts, preferences, commitments, project
  decisions, or explicit "remember this" requests.
- Use memory_upsert to create or update durable memory. Do not ask the user for
  filenames or slugs; choose a short title, write the complete updated fact in
  the body, and optionally provide one short summary for the index.
- Reuse the same semantic title when updating an existing fact. Use shell only
  to read existing memory/project files when you need more context.
- Do not store generic facts about yourself being an AI assistant, this software
  being built, or the current conversation unless the user explicitly asks you
  to remember that.
- If unsure whether something is worth remembering, ask or leave it in the
  transcript. Don't store what you can re-derive.

You can schedule things: schedule_reminder fires a stored message at a time
(plain "remind me" requests); schedule_turn injects a prompt so you act on it
later with fresh context; list_scheduled and cancel_scheduled manage them. Use
the current time below to compute "in 10 minutes" / "tomorrow at 9". After
scheduling, confirm to the user in plain words what and when. notify pushes a
message out of band — only inside a scheduled turn, not in a normal reply. Do
not call notify just to announce that a scheduled turn finished; the system sends
its own short completion alert.

You can delegate isolated grunt work to a child conversation with delegate and
delegate_continue. Use it when exploration would add noisy intermediate context
to the main chat. It is blocking: wait for the returned result, then decide what
to tell the user.

Use history_search to find older raw transcript messages in the current
conversation when the visible summary, memory index, and recent tail are not
enough.

Detailed operating procedures are available through skill_list and skill_read.
Read the matching skill before non-trivial use of trusted runners, background
tasks, or other specialized workflows.

You can use trusted external agent runners with runner_list, runner_models, and
runner_run. Prefer runners for second opinions, independent investigation, or
tasks explicitly asking for agy/codex/claude/pi. These are not shell commands:
do not use shell to inspect runner credentials, config, cache, history, or logs.

Conventions:
- Confirm before any outward-facing or hard-to-reverse action (messaging third
  parties, spending, deleting your own memory). Ask in chat first.
- Keep replies concise and direct; this is a chat, not a document.`

// executeRun creates a run record, assembles context, executes one golem turn,
// and persists the result. input is the user message the run answers (the newest
// uncovered one); its source decides the initiator and its id is the run's
// coverage point. Success and failure both advance the conversation's cursor
// past input, so the worker never re-runs it.
func (e *Engine) executeRun(ctx context.Context, conversationID int64, input store.Message) {
	initiator := "user"
	switch input.Source {
	case "schedule":
		initiator = "schedule"
	case "freetime":
		initiator = "freetime"
	}

	run, err := e.store.CreateRun(ctx, conversationID, initiator, e.modelID, input.ID)
	if err != nil {
		e.logf(errorLevel, "conversation %d: create run: %v", conversationID, err)
		return
	}
	e.logf(infoLevel, "run %d started (conv %d, %s)", run.ID, conversationID, initiator)
	publishTranscript := shouldPublishRunTranscript(input.Source)
	if publishTranscript {
		e.emit(Event{ConversationID: conversationID, RunID: run.ID, Message: &input})
	}
	// Version any workspace files the agent touched this run (no-op if clean).
	defer func() {
		if err := e.workspace.Commit(fmt.Sprintf("run %d", run.ID)); err != nil {
			e.logf(warnLevel, "run %d: commit workspace: %v", run.ID, err)
		}
	}()

	prompt, history, inputText, err := e.assembleContext(ctx, conversationID, input)
	if err != nil {
		e.failRun(ctx, conversationID, run.ID, input, llm.Usage{}, err)
		return
	}

	profile := e.runProfile(conversationID)
	agent, err := golem.New(golem.Config{
		Model:              e.main,
		SystemPrompt:       profile.systemPrompt(prompt),
		History:            history,
		Tools:              profile.tools,
		MaxHistoryMessages: golem.UnlimitedHistoryMessages,
		MaxToolIterations:  profile.maxToolIterations,
	})
	if err != nil {
		e.failRun(ctx, conversationID, run.ID, input, llm.Usage{}, fmt.Errorf("build agent: %w", err))
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, defaultRunTimeout)
	defer cancel()
	runCtx = withRunContext(runCtx, conversationID, run.ID)

	turn, err := agent.Stream(runCtx, inputText, func(ev golem.StreamEvent) {
		e.logEvent(run.ID, ev)
		e.emit(Event{ConversationID: conversationID, RunID: run.ID, Ev: ev})
	})
	if err != nil {
		e.failRun(ctx, conversationID, run.ID, input, agent.Usage(), err)
		return
	}
	e.logf(infoLevel, "run %d done (tokens in=%d out=%d)", run.ID, turn.Usage.PromptTokens, turn.Usage.CompletionTokens)

	out := buildTurnMessages(conversationID, run.ID, turn.Messages())
	// One transaction: append the reply, finish the run, advance the cursor past
	// input. A failure here records the run failed (and still covers input) so
	// the worker doesn't loop.
	storedOut, err := e.store.CompleteRun(ctx, run.ID, conversationID, input.ID, out, turn.Usage)
	if err != nil {
		e.logf(errorLevel, "conversation %d run %d: complete run: %v", conversationID, run.ID, err)
		e.failRun(ctx, conversationID, run.ID, input, turn.Usage, err)
		return
	}
	if publishTranscript {
		e.emitPersistedRunMessages(conversationID, run.ID, storedOut)
	}
	if initiator == "schedule" {
		e.notifyScheduledTurn(ctx, conversationID, turn.Reply)
	}
	e.maybeScheduleCompaction(ctx, conversationID)
}

func shouldPublishRunTranscript(source string) bool {
	return source == "schedule"
}

func (e *Engine) emitPersistedRunMessages(conversationID, runID int64, msgs []store.Message) {
	for i := range msgs {
		e.emit(Event{ConversationID: conversationID, RunID: runID, Message: &msgs[i]})
	}
}

// logEvent traces the agent's tool activity: calls and results at Debug (set
// log_level=debug to see them), tool errors at Warn. Text deltas are skipped to
// avoid spam — the final reply is persisted to the transcript anyway.
func (e *Engine) logEvent(runID int64, ev golem.StreamEvent) {
	switch ev.Kind {
	case golem.EventToolCall:
		e.logf(debugLevel, "run %d tool call %s(%s)", runID, ev.Step.ToolName, truncate(ev.Step.Arguments, 200))
	case golem.EventToolResult:
		e.logf(debugLevel, "run %d tool result %s: %s", runID, ev.Step.ToolName, truncate(ev.Step.Result, 200))
	case golem.EventToolError:
		e.logf(warnLevel, "run %d tool error %s: %s", runID, ev.Step.ToolName, ev.Step.Error)
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// failRun records the run as failed, appends a visible failure message, and
// advances the conversation's cursor past the answered input — all in one store
// transaction (see store.FailRun). Advancing the cursor stops the worker from
// re-running the same input; committing it with the failure message keeps the
// transcript and cursor in agreement.
func (e *Engine) failRun(ctx context.Context, conversationID, runID int64, input store.Message, usage llm.Usage, cause error) {
	e.logf(errorLevel, "conversation %d run %d failed: %v", conversationID, runID, cause)
	failure := store.Message{
		ConversationID: conversationID,
		RunID:          &runID,
		Role:           llm.RoleAI,
		Content:        store.Content{Text: fmt.Sprintf("(run failed: %v)", cause)},
	}
	storedFailure, err := e.store.FailRun(ctx, runID, conversationID, input.ID, usage, cause.Error(), failure)
	if err != nil {
		e.logf(errorLevel, "conversation %d run %d: persist failure: %v", conversationID, runID, err)
		return
	}
	if shouldPublishRunTranscript(input.Source) {
		e.emit(Event{ConversationID: conversationID, RunID: runID, Message: &storedFailure})
	}
	text := failure.Content.Text
	e.emit(Event{ConversationID: conversationID, RunID: runID, Ev: golem.StreamEvent{Kind: golem.EventTextDelta, Text: text}})
	e.emit(Event{ConversationID: conversationID, RunID: runID, Ev: golem.StreamEvent{Kind: golem.EventDone, Text: text, Usage: usage, FinishReason: llm.FinishReasonUnknown}})
}

// buildTurnMessages turns the messages a golem turn produced into store rows.
// The leading user row (the input, persisted on submit) is skipped; assistant
// and tool rows are returned with run_id set and CreatedAt carrying the real
// wall-clock time golem produced each one (an assistant message when the model
// finished it, a tool message when the tool returned). Persisting these real
// times — instead of letting every row default to the save instant — is what
// lets the UI show a meaningful work duration per step.
func buildTurnMessages(conversationID, runID int64, msgs []llm.Message) []store.Message {
	i := 0
	for i < len(msgs) && msgs[i].Role == llm.RoleUser {
		i++ // skip the input message(s), already persisted on submit
	}

	var out []store.Message
	for _, m := range msgs[i:] {
		c := store.Content{Text: m.Content}
		switch m.Role {
		case llm.RoleAI:
			c.ToolCalls = m.ToolCalls
			c.Reasoning = strings.TrimSpace(m.ReasoningContent)
		case llm.RoleTool:
			c.ToolCallID = m.ToolCallID
		}
		out = append(out, store.Message{
			ConversationID: conversationID,
			RunID:          &runID,
			Role:           m.Role,
			Content:        c,
			CreatedAt:      m.CreatedAt,
		})
	}
	return out
}

// assembleContext builds the system prompt, the history seed, and the input text
// for one run. input is the user message the run answers. History is the
// post-summary transcript up to the input in conversational order, budget-
// trimmed. Messages causally after the input — a newer user message, or the
// reply to one that landed physically later — are excluded so they stay for the
// next run; an earlier reply that landed after a newer user message is still
// included, in its causal place, rather than cut by a physical-id window.
func (e *Engine) assembleContext(ctx context.Context, conversationID int64, input store.Message) (prompt string, history []llm.Message, inputText string, err error) {
	summary, hasSummary, err := e.store.LatestSummary(ctx, conversationID)
	if err != nil {
		return "", nil, "", fmt.Errorf("load summary: %w", err)
	}
	var afterID int64
	if hasSummary {
		afterID = summary.ThroughMessageID
	}
	msgs, err := e.store.MessagesForInput(ctx, conversationID, afterID, input.ID)
	if err != nil {
		return "", nil, "", fmt.Errorf("load transcript tail: %w", err)
	}

	llmMsgs := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		llmMsgs = append(llmMsgs, toLLMMessage(m))
	}
	if len(llmMsgs) == 0 {
		return "", nil, "", fmt.Errorf("no messages to run over")
	}
	llmMsgs = trimToBudget(llmMsgs, e.tailTok)

	tail := llmMsgs[len(llmMsgs)-1]
	if tail.Role != llm.RoleUser {
		return "", nil, "", fmt.Errorf("newest message is %s, not user", tail.Role)
	}
	inputText = tail.Content
	history = llmMsgs[:len(llmMsgs)-1]

	prompt, err = e.systemPrompt(summary, hasSummary)
	if err != nil {
		return "", nil, "", err
	}
	return prompt, history, inputText, nil
}

// toLLMMessage projects a stored transcript row into the model-facing message.
// Reasoning is intentionally not replayed. Event rows (a fired reminder) are not
// model output; they map to a user-visible event line so the model sees them and
// so context trimming can safely open a window on them.
func toLLMMessage(m store.Message) llm.Message {
	if m.Role == store.RoleEvent {
		return llm.Message{Role: llm.RoleUser, Content: "[reminder fired] " + m.Content.Text}
	}
	return llm.Message{
		Role:       m.Role,
		Content:    m.Content.Text,
		ToolCalls:  m.Content.ToolCalls,
		ToolCallID: m.Content.ToolCallID,
	}
}

func (e *Engine) systemPrompt(summary store.Summary, hasSummary bool) (string, error) {
	var b strings.Builder
	b.WriteString(basePrompt)

	if catalog := strings.TrimSpace(e.skillCatalog); catalog != "" {
		b.WriteString("\n\n## Builtin skills\n\n")
		b.WriteString(catalog)
	}

	persona, err := e.workspace.Persona()
	if err != nil {
		return "", fmt.Errorf("load persona: %w", err)
	}
	if persona = strings.TrimSpace(persona); persona != "" {
		b.WriteString("\n\n")
		b.WriteString(persona)
	}

	index, err := e.workspace.MemoryIndex()
	if err != nil {
		return "", fmt.Errorf("load memory index: %w", err)
	}
	if index = strings.TrimSpace(index); index != "" {
		b.WriteString("\n\n## Memory index\n\n")
		b.WriteString(index)
	}

	if hasSummary && strings.TrimSpace(summary.Content) != "" {
		b.WriteString("\n\n## Summary of earlier conversation\n\n")
		b.WriteString(strings.TrimSpace(summary.Content))
	}

	now := time.Now().In(e.loc)
	b.WriteString("\n\nCurrent time: ")
	b.WriteString(now.Format("Monday, 2 January 2006, 15:04 MST -07:00"))
	return b.String(), nil
}

// estTokens approximates a message's token cost with the len/4 chars heuristic,
// plus a small per-message overhead.
func estTokens(m llm.Message) int {
	n := len(m.Content)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	n += len(m.ToolCallID)
	return n/4 + 4
}

// trimToBudget drops messages from the front until the estimated token total is
// within budget, then advances the start to the next user message so the window
// always opens on a clean turn boundary. The trailing message (the input) is
// always retained.
//
// A budget cut can land inside a tool block — an assistant message issuing tool
// calls, or one of its tool results — whose earlier turns were trimmed away.
// Starting history there is malformed: a dangling tool result, or tool calls
// with no preceding user turn. Providers reject this (Anthropic requires the
// first message to be a user turn), and it surfaces as intermittent adapter
// failures. Aligning to a user boundary keeps every user -> assistant(tool_calls)
// -> tool block intact.
//
// This mirrors the intent of pkg/golem's toolBlockStart, but resolves forward to
// a user boundary instead of extending the block backwards: caliban trims to a
// hard token budget and must not grow the window back over it.
func trimToBudget(msgs []llm.Message, budget int) []llm.Message {
	if budget <= 0 || len(msgs) <= 1 {
		return msgs
	}
	total := 0
	for _, m := range msgs {
		total += estTokens(m)
	}
	start := 0
	for start < len(msgs)-1 && total > budget {
		total -= estTokens(msgs[start])
		start++
	}
	for start < len(msgs)-1 && msgs[start].Role != llm.RoleUser {
		start++
	}
	return msgs[start:]
}
