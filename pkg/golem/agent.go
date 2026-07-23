package golem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/levmv/golems/pkg/llm"
)

const (
	DefaultName               = "golem"
	DefaultMaxHistoryMessages = 40
	DefaultMaxToolIterations  = 8
	UnlimitedHistoryMessages  = -1
	UnlimitedToolIterations   = -1
	DefaultSystemPrompt       = `You are Golem, a compact CLI agent. Be direct, practical, and curious. Help the user think and act, keep enough context from the conversation, and ask for clarification only when it is needed.`
)

var ErrEmptyInput = errors.New("empty input")

type Model interface {
	Chat(ctx context.Context, req llm.Request) (*llm.Response, error)
	Stream(ctx context.Context, req llm.Request) (llm.Stream, error)
}

type Config struct {
	Model Model
	// Request optionally replaces direct model execution for each step. Use a
	// Requester here when the application needs retries or request bookkeeping.
	Request      ModelRequestFunc
	Name         string
	SystemPrompt string
	// MaxHistoryMessages limits prior messages sent to the model. Zero uses the
	// default; negative means unlimited.
	MaxHistoryMessages int
	// History seeds the agent's committed conversation state.
	History           []llm.Message
	Tools             []Tool
	ToolChoice        *llm.ToolChoice
	ParallelToolCalls *bool
	// MaxToolIterations limits model->tool cycles in one turn. Zero uses the
	// default; negative means unlimited.
	MaxToolIterations int
}

type Agent struct {
	turnMu             sync.Mutex
	mu                 sync.Mutex
	model              Model
	request            ModelRequestFunc
	name               string
	systemPrompt       string
	maxHistoryMessages int
	toolSet            *ToolSet
	toolChoice         *llm.ToolChoice
	parallelToolCalls  *bool
	maxToolIterations  int
	messages           []llm.Message
	usage              llm.Usage
}

type StepKind string

const (
	StepToolCall   StepKind = "tool_call"
	StepToolResult StepKind = "tool_result"
	StepToolError  StepKind = "tool_error"
)

type Step struct {
	Kind       StepKind
	ToolName   string
	ToolCallID string
	Arguments  string
	Result     string
	Error      string
	Meta       any
}

type Turn struct {
	Input        string
	Reply        string
	Reasoning    string
	Steps        []Step
	Usage        llm.Usage
	FinishReason llm.FinishReason

	messages []llm.Message
}

// Messages returns the turn's messages (the user input followed by the assistant
// and tool messages it produced). Each carries the time it was produced in
// llm.Message.CreatedAt: an assistant message when the model finished it, a tool
// message when the tool returned — so consumers that persist a transcript can
// record when each step actually happened rather than when the turn was saved.
// The messages are cloned, so callers may retain or mutate them freely.
func (t *Turn) Messages() []llm.Message {
	return llm.CloneMessages(t.messages)
}

type StreamEventKind string

const (
	EventTextDelta      StreamEventKind = "text_delta"
	EventReasoningDelta StreamEventKind = "reasoning_delta"
	EventToolCall       StreamEventKind = "tool_call"
	EventToolResult     StreamEventKind = "tool_result"
	EventToolError      StreamEventKind = "tool_error"
	EventModelRetry     StreamEventKind = "model_retry"
	EventStatus         StreamEventKind = "status"
	EventAttemptReset   StreamEventKind = "attempt_reset"
	// EventDone is emitted once when the turn completes; its Text is the final
	// reply (== Turn.Reply), so consumers need not re-derive it from text deltas.
	EventDone StreamEventKind = "done"
)

type StreamEvent struct {
	Kind         StreamEventKind
	Text         string
	RetryKey     string
	Step         Step
	Usage        llm.Usage
	FinishReason llm.FinishReason
}

type StreamFunc func(StreamEvent)

func New(cfg Config) (*Agent, error) {
	if cfg.Model == nil && cfg.Request == nil {
		return nil, errors.New("golem: model or request function is required")
	}
	toolSet, err := NewToolSet(cfg.Tools)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = DefaultName
	}

	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = DefaultSystemPrompt
	}

	maxHistory := cfg.MaxHistoryMessages
	if maxHistory == 0 {
		maxHistory = DefaultMaxHistoryMessages
	}
	if maxHistory < 0 {
		maxHistory = 0
	}

	maxToolIterations := normalizeMaxToolIterations(cfg.MaxToolIterations)

	agent := &Agent{
		model:              cfg.Model,
		request:            cfg.Request,
		name:               name,
		systemPrompt:       systemPrompt,
		maxHistoryMessages: maxHistory,
		toolSet:            toolSet,
		toolChoice:         llm.CloneToolChoice(cfg.ToolChoice),
		parallelToolCalls:  cloneBool(cfg.ParallelToolCalls),
		maxToolIterations:  maxToolIterations,
		messages:           llm.CloneMessages(cfg.History),
	}

	return agent, nil
}

func (a *Agent) Use(tool Tool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toolSet == nil {
		a.toolSet, _ = NewToolSet(nil)
	}
	return a.toolSet.add(tool)
}

func (a *Agent) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.name
}

func (a *Agent) Reset() {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = nil
	a.usage = llm.Usage{}
}

func (a *Agent) History() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return llm.CloneMessages(a.messages)
}

func (a *Agent) SetHistory(messages []llm.Message) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = llm.CloneMessages(messages)
}

func (a *Agent) Usage() llm.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

// Reply executes one complete turn. Agent turns are serialized, so concurrent
// Reply and Stream calls observe each other's committed history in call order.
func (a *Agent) Reply(ctx context.Context, input string) (*Turn, error) {
	return a.run(ctx, input, false, nil)
}

// Stream executes one complete turn while emitting streaming events. Agent turns
// are serialized, so concurrent Reply and Stream calls observe each other's
// committed history in call order.
func (a *Agent) Stream(ctx context.Context, input string, emit StreamFunc) (*Turn, error) {
	return a.run(ctx, input, true, emit)
}

func (a *Agent) run(ctx context.Context, input string, stream bool, emit StreamFunc) (*Turn, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrEmptyInput
	}

	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	cfg := a.prepareTurn(input)
	cfg.Stream = stream
	cfg.Emit = emit
	cfg.Runtime.Complete = func(turn *Turn) error {
		a.commitTurn(*turn)
		return nil
	}
	cfg.Runtime.Fail = func(usage llm.Usage, cause error) error {
		a.commitUsage(usage)
		return cause
	}
	return RunTurn(ctx, cfg)
}

func (a *Agent) prepareTurn(input string) TurnConfig {
	a.mu.Lock()
	defer a.mu.Unlock()

	history := trimHistory(a.messages, a.maxHistoryMessages)
	messages := make([]llm.Message, 0, len(history)+2)
	if a.systemPrompt != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleSystem,
			Content: a.systemPrompt,
		})
	}
	// Seed prior-turn history without reasoning. A turn's chain-of-thought is
	// echoed back only within its own tool loop (some providers, e.g. DeepSeek
	// thinking mode, require that for tool-calling turns); replaying it across
	// turns is unnecessary and costs input tokens. Retained history
	// (a.messages / History()) keeps reasoning intact for consumers that
	// persist it — only the model-facing copy is stripped.
	for _, m := range history {
		m.ReasoningContent = ""
		messages = append(messages, m)
	}
	toolSet := a.toolSet.clone()

	return TurnConfig{
		Model:             a.model,
		Input:             input,
		InitialContext:    messages,
		Tools:             toolSet,
		ToolChoice:        llm.CloneToolChoice(a.toolChoice),
		ParallelToolCalls: cloneBool(a.parallelToolCalls),
		MaxToolIterations: a.maxToolIterations,
		Runtime:           TurnRuntime{Request: a.request},
	}
}

func trimHistory(messages []llm.Message, maxMessages int) []llm.Message {
	start := 0
	if maxMessages > 0 && len(messages) > maxMessages {
		start = len(messages) - maxMessages
		start = toolBlockStart(messages, start)
	}

	out := make([]llm.Message, len(messages)-start)
	copy(out, messages[start:])
	return out
}

func toolBlockStart(messages []llm.Message, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}

	for start > 0 && messages[start].Role == llm.RoleTool {
		start--
	}

	if len(messages[start].ToolCalls) > 0 && start > 0 && messages[start-1].Role == llm.RoleUser {
		return start - 1
	}
	return start
}

func (a *Agent) commitTurn(turn Turn) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.messages = append(a.messages, turn.messages...)
	a.usage = a.usage.Add(turn.Usage)
}

func (a *Agent) commitUsage(usage llm.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage = a.usage.Add(usage)
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func FormatUsage(u llm.Usage) string {
	return fmt.Sprintf("prompt=%d cached=%d completion=%d total=%d",
		u.PromptTokens,
		u.CachedTokens,
		u.CompletionTokens,
		u.TotalTokens,
	)
}
