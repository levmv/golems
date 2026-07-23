package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const childAgentPrompt = `You are a delegated worker for Caliban.

You are working in an isolated child conversation. Complete the task given by
the parent assistant, keep intermediate exploration concise, and return a clear
final answer. Do not message the user directly. If you cannot verify something,
say so explicitly.`

// Delegate creates a child conversation tied to the current run and executes
// one blocking child-agent turn. The child trace is persisted in its own
// conversation; only the returned final text enters the parent tool result.
func (e *Engine) Delegate(ctx context.Context, prompt string) (int64, string, error) {
	parentRunID, ok := runIDFromContext(ctx)
	if !ok {
		return 0, "", fmt.Errorf("delegate is only available during an agent run")
	}
	parentConversationID := conversationIDFromContext(ctx)
	parent, err := e.store.Conversation(ctx, parentConversationID)
	if err != nil {
		return 0, "", fmt.Errorf("load parent conversation: %w", err)
	}
	if parent.ParentRunID != nil {
		return 0, "", fmt.Errorf("subagents cannot delegate further")
	}

	child, err := e.store.CreateChildConversation(ctx, parentRunID)
	if err != nil {
		return 0, "", err
	}
	reply, err := e.runDelegatedTurn(ctx, child.ID, prompt)
	if err != nil {
		return child.ID, "", err
	}
	return child.ID, reply, nil
}

// DelegateContinue appends a blocking turn to an existing child conversation.
func (e *Engine) DelegateContinue(ctx context.Context, conversationID int64, prompt string) (string, error) {
	parentRunID, ok := runIDFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("delegate_continue is only available during an agent run")
	}
	child, err := e.store.Conversation(ctx, conversationID)
	if err != nil {
		return "", fmt.Errorf("load child conversation: %w", err)
	}
	if child.ParentRunID == nil {
		return "", fmt.Errorf("conversation %d is not a child conversation", conversationID)
	}
	if *child.ParentRunID != parentRunID {
		return "", fmt.Errorf("conversation %d does not belong to this run", conversationID)
	}
	return e.runDelegatedTurn(ctx, conversationID, prompt)
}

func (e *Engine) runDelegatedTurn(ctx context.Context, conversationID int64, prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("delegate prompt is required")
	}
	input, err := e.store.AppendMessage(ctx, store.Message{
		ConversationID: conversationID,
		Role:           llm.RoleUser,
		Source:         "delegate",
		Content:        store.Content{Text: prompt},
	})
	if err != nil {
		return "", fmt.Errorf("append delegated prompt: %w", err)
	}

	run, err := e.store.CreateRun(ctx, conversationID, "agent", e.modelID, input.ID)
	if err != nil {
		return "", fmt.Errorf("create delegated run: %w", err)
	}
	e.logf(infoLevel, "delegated run %d started (conv %d)", run.ID, conversationID)

	childCtx, cancel := context.WithTimeout(ctx, defaultRunTimeout)
	defer cancel()
	childCtx = withRunContext(childCtx, conversationID, run.ID)

	promptText, history, inputText, err := e.assembleDelegatedContext(childCtx, conversationID, input)
	if err != nil {
		return "", e.failDelegatedTurn(ctx, conversationID, run.ID, input, llm.Usage{}, err)
	}
	agent, err := golem.New(golem.Config{
		Request:            e.mainRequester.Request,
		SystemPrompt:       promptText,
		History:            history,
		Tools:              e.childTools(),
		MaxHistoryMessages: golem.UnlimitedHistoryMessages,
		MaxToolIterations:  e.maxToolIter,
	})
	if err != nil {
		cause := fmt.Errorf("build delegated agent: %w", err)
		return "", e.failDelegatedTurn(ctx, conversationID, run.ID, input, llm.Usage{}, cause)
	}
	turn, err := agent.Reply(childCtx, inputText)
	if err != nil {
		return "", e.failDelegatedTurn(ctx, conversationID, run.ID, input, agent.Usage(), err)
	}
	out := buildTurnMessages(conversationID, run.ID, turn.Messages())
	if _, err := e.store.CompleteRun(ctx, run.ID, conversationID, input.ID, out, turn.Usage); err != nil {
		return "", e.failDelegatedTurn(ctx, conversationID, run.ID, input, turn.Usage, err)
	}
	e.logf(infoLevel, "delegated run %d done (tokens in=%d out=%d)", run.ID, turn.Usage.PromptTokens, turn.Usage.CompletionTokens)
	return strings.TrimSpace(turn.Reply), nil
}

func (e *Engine) failDelegatedTurn(
	ctx context.Context,
	conversationID, runID int64,
	input store.Message,
	usage llm.Usage,
	cause error,
) error {
	if err := e.failRun(ctx, conversationID, runID, input, usage, cause); err != nil {
		return fmt.Errorf("%w; additionally: %v", cause, err)
	}
	return cause
}

func (e *Engine) assembleDelegatedContext(ctx context.Context, conversationID int64, input store.Message) (string, []llm.Message, string, error) {
	prompt, history, inputText, err := e.assembleContext(ctx, conversationID, input)
	if err != nil {
		return "", nil, "", err
	}
	return prompt + "\n\n" + childAgentPrompt, history, inputText, nil
}

func (e *Engine) childTools() []golem.Tool {
	out := make([]golem.Tool, 0, len(e.tools))
	for _, tool := range e.tools {
		name := tool.Definition.Function.Name
		switch name {
		case "delegate", "delegate_continue",
			"notify",
			"schedule_reminder", "schedule_turn", "list_scheduled", "cancel_scheduled":
			continue
		}
		out = append(out, tool)
	}
	return out
}
