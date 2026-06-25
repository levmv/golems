package golem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

const (
	DefaultName               = "golem"
	DefaultMaxHistoryMessages = 40
	DefaultMaxToolIterations  = 8
	UnlimitedHistoryMessages  = -1
	UnlimitedToolIterations   = -1
	DefaultSystemPrompt       = `You are Golem, a compact CLI agent. Be direct, practical, and curious. Help the user think and act, keep enough context from the conversation, and ask for clarification only when it is needed.`
	toolLimitFinalPrompt      = "Tool call limit reached. Do not call any more tools; answer with what you have, and be explicit about anything you could not verify."
)

var ErrEmptyInput = errors.New("empty input")

type Model interface {
	Chat(ctx context.Context, req llm.Request) (*llm.Response, error)
	Stream(ctx context.Context, req llm.Request) (llm.Stream, error)
}

type ToolFunc func(ctx context.Context, call llm.ToolCall) (ToolResult, error)

type ToolResult struct {
	Content string
	Meta    any
}

type Tool struct {
	Definition llm.Tool
	Run        ToolFunc
}

func FunctionTool(name, description string, parameters jsonschema.Schema, run ToolFunc) Tool {
	return Tool{
		Definition: llm.Tool{
			Type: llm.ToolTypeFunction,
			Function: llm.ToolDefinition{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		},
		Run: run,
	}
}

type Config struct {
	Model        Model
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
	name               string
	systemPrompt       string
	maxHistoryMessages int
	tools              []llm.Tool
	toolExecutors      map[string]ToolFunc
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
	return cloneMessages(t.messages)
}

type StreamEventKind string

const (
	EventTextDelta      StreamEventKind = "text_delta"
	EventReasoningDelta StreamEventKind = "reasoning_delta"
	EventToolCall       StreamEventKind = "tool_call"
	EventToolResult     StreamEventKind = "tool_result"
	EventToolError      StreamEventKind = "tool_error"
	// EventDone is emitted once when the turn completes; its Text is the final
	// reply (== Turn.Reply), so consumers need not re-derive it from text deltas.
	EventDone StreamEventKind = "done"
)

type StreamEvent struct {
	Kind         StreamEventKind
	Text         string
	Step         Step
	Usage        llm.Usage
	FinishReason llm.FinishReason
}

type StreamFunc func(StreamEvent)

func New(cfg Config) (*Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("golem: model is required")
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

	maxToolIterations := cfg.MaxToolIterations
	if maxToolIterations == 0 {
		maxToolIterations = DefaultMaxToolIterations
	}
	if maxToolIterations < 0 {
		maxToolIterations = UnlimitedToolIterations
	}

	agent := &Agent{
		model:              cfg.Model,
		name:               name,
		systemPrompt:       systemPrompt,
		maxHistoryMessages: maxHistory,
		toolExecutors:      make(map[string]ToolFunc),
		toolChoice:         cloneToolChoice(cfg.ToolChoice),
		parallelToolCalls:  cloneBool(cfg.ParallelToolCalls),
		maxToolIterations:  maxToolIterations,
		messages:           cloneMessages(cfg.History),
	}

	for _, tool := range cfg.Tools {
		if err := agent.Use(tool); err != nil {
			return nil, err
		}
	}

	return agent, nil
}

func (a *Agent) Use(tool Tool) error {
	definition, run, err := normalizeTool(tool)
	if err != nil {
		return err
	}
	name := definition.Function.Name

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.toolExecutors == nil {
		a.toolExecutors = make(map[string]ToolFunc)
	}
	if _, exists := a.toolExecutors[name]; exists {
		return fmt.Errorf("golem: duplicate tool %q", name)
	}

	a.tools = append(a.tools, definition)
	a.toolExecutors[name] = run
	return nil
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
	return cloneMessages(a.messages)
}

func (a *Agent) SetHistory(messages []llm.Message) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = cloneMessages(messages)
}

func (a *Agent) Usage() llm.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

// Reply executes one complete turn. Agent turns are serialized, so concurrent
// Reply and Stream calls observe each other's committed history in call order.
func (a *Agent) Reply(ctx context.Context, input string) (*Turn, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrEmptyInput
	}

	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	state := a.prepareTurn(input)
	turn, err := a.replyLoop(ctx, input, state)
	if err != nil {
		return nil, err
	}
	a.commitTurn(*turn)
	return turn, nil
}

// Stream executes one complete turn while emitting streaming events. Agent turns
// are serialized, so concurrent Reply and Stream calls observe each other's
// committed history in call order.
func (a *Agent) Stream(ctx context.Context, input string, emit StreamFunc) (*Turn, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ErrEmptyInput
	}

	a.turnMu.Lock()
	defer a.turnMu.Unlock()

	state := a.prepareTurn(input)
	turn, err := a.streamLoop(ctx, input, state, emit)
	if err != nil {
		return nil, err
	}
	a.commitTurn(*turn)
	if emit != nil {
		emit(StreamEvent{
			Kind:         EventDone,
			Text:         turn.Reply,
			Usage:        turn.Usage,
			FinishReason: turn.FinishReason,
		})
	}
	return turn, nil
}

type turnState struct {
	messages          []llm.Message
	tools             []llm.Tool
	toolExecutors     map[string]ToolFunc
	toolChoice        *llm.ToolChoice
	parallelToolCalls *bool
	maxToolIterations int
}

func (a *Agent) prepareTurn(input string) turnState {
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
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: input,
	})

	tools := make([]llm.Tool, len(a.tools))
	copy(tools, a.tools)

	executors := make(map[string]ToolFunc, len(a.toolExecutors))
	for name, run := range a.toolExecutors {
		executors[name] = run
	}

	return turnState{
		messages:          messages,
		tools:             tools,
		toolExecutors:     executors,
		toolChoice:        cloneToolChoice(a.toolChoice),
		parallelToolCalls: cloneBool(a.parallelToolCalls),
		maxToolIterations: a.maxToolIterations,
	}
}

func (a *Agent) replyLoop(ctx context.Context, input string, state turnState) (*Turn, error) {
	messages := state.messages
	turnMessages := []llm.Message{{Role: llm.RoleUser, Content: input, CreatedAt: time.Now()}}
	var steps []Step
	var reasoning strings.Builder
	var usage llm.Usage
	var finishReason llm.FinishReason
	toolIterations := 0

	for {
		resp, err := a.model.Chat(ctx, llm.Request{
			Messages:          messages,
			Tools:             state.tools,
			ToolChoice:        state.toolChoice,
			ParallelToolCalls: state.parallelToolCalls,
		})
		if err != nil {
			return nil, err
		}

		usage = addUsage(usage, resp.Usage)
		appendReasoning(&reasoning, resp.ReasoningContent)
		finishReason = resp.FinishReason

		assistantMsg := llm.Message{
			Role:             llm.RoleAI,
			Content:          strings.TrimSpace(resp.Content),
			ReasoningContent: strings.TrimSpace(resp.ReasoningContent),
			ToolCalls:        resp.ToolCalls,
			CreatedAt:        time.Now(),
		}

		if len(resp.ToolCalls) == 0 {
			messages = append(messages, assistantMsg)
			turnMessages = append(turnMessages, assistantMsg)
			return &Turn{
				Input:        input,
				Reply:        strings.TrimSpace(resp.Content),
				Reasoning:    strings.TrimSpace(reasoning.String()),
				Steps:        steps,
				Usage:        usage,
				FinishReason: finishReason,
				messages:     turnMessages,
			}, nil
		}

		if state.maxToolIterations >= 0 && toolIterations >= state.maxToolIterations {
			steps = append(steps, toolLimitSteps(resp.ToolCalls, state.maxToolIterations)...)

			resp, err := a.finalReply(ctx, messages, state)
			if err != nil {
				return nil, err
			}

			usage = addUsage(usage, resp.Usage)
			appendReasoning(&reasoning, resp.ReasoningContent)
			finishReason = resp.FinishReason

			assistantMsg := llm.Message{
				Role:             llm.RoleAI,
				Content:          strings.TrimSpace(resp.Content),
				ReasoningContent: strings.TrimSpace(resp.ReasoningContent),
				CreatedAt:        time.Now(),
			}
			messages = append(messages, assistantMsg)
			turnMessages = append(turnMessages, assistantMsg)

			return &Turn{
				Input:        input,
				Reply:        strings.TrimSpace(resp.Content),
				Reasoning:    strings.TrimSpace(reasoning.String()),
				Steps:        steps,
				Usage:        usage,
				FinishReason: finishReason,
				messages:     turnMessages,
			}, nil
		}
		toolIterations++

		messages = append(messages, assistantMsg)
		turnMessages = append(turnMessages, assistantMsg)

		toolMessages, toolSteps, err := executeToolCalls(ctx, resp.ToolCalls, state.toolExecutors, nil)
		if err != nil {
			return nil, err
		}
		steps = append(steps, toolSteps...)
		messages = append(messages, toolMessages...)
		turnMessages = append(turnMessages, toolMessages...)
	}
}

func (a *Agent) streamLoop(ctx context.Context, input string, state turnState, emit StreamFunc) (*Turn, error) {
	messages := state.messages
	turnMessages := []llm.Message{{Role: llm.RoleUser, Content: input, CreatedAt: time.Now()}}
	var steps []Step
	var finalReply string
	var reasoning strings.Builder
	var usage llm.Usage
	var finishReason llm.FinishReason
	toolIterations := 0

	for {
		stream, err := a.model.Stream(ctx, llm.Request{
			Messages:          messages,
			Tools:             state.tools,
			ToolChoice:        state.toolChoice,
			ParallelToolCalls: state.parallelToolCalls,
		})
		if err != nil {
			return nil, err
		}
		if stream == nil {
			return nil, errors.New("golem: model returned nil stream")
		}

		streamReply, streamReasoning, toolCalls, streamFinishReason, err := consumeStream(stream, emit)
		if err != nil {
			return nil, err
		}

		usage = addUsage(usage, stream.Usage())
		appendReasoning(&reasoning, streamReasoning)
		finishReason = streamFinishReason

		assistantMsg := llm.Message{
			Role:             llm.RoleAI,
			Content:          strings.TrimSpace(streamReply),
			ReasoningContent: strings.TrimSpace(streamReasoning),
			ToolCalls:        toolCalls,
			CreatedAt:        time.Now(),
		}

		if len(toolCalls) == 0 {
			messages = append(messages, assistantMsg)
			turnMessages = append(turnMessages, assistantMsg)
			finalReply = streamReply
			break
		}

		if state.maxToolIterations >= 0 && toolIterations >= state.maxToolIterations {
			limitSteps := toolLimitSteps(toolCalls, state.maxToolIterations)
			steps = append(steps, limitSteps...)
			if emit != nil {
				for _, step := range limitSteps {
					emit(StreamEvent{Kind: EventToolError, Step: step})
				}
			}

			resp, err := a.finalReply(ctx, messages, state)
			if err != nil {
				return nil, err
			}

			reply := strings.TrimSpace(resp.Content)
			if emit != nil && resp.Content != "" {
				emit(StreamEvent{Kind: EventTextDelta, Text: resp.Content})
			}
			usage = addUsage(usage, resp.Usage)
			appendReasoning(&reasoning, resp.ReasoningContent)
			finishReason = resp.FinishReason

			assistantMsg := llm.Message{
				Role:             llm.RoleAI,
				Content:          reply,
				ReasoningContent: strings.TrimSpace(resp.ReasoningContent),
				CreatedAt:        time.Now(),
			}
			messages = append(messages, assistantMsg)
			turnMessages = append(turnMessages, assistantMsg)
			finalReply = reply
			break
		}
		toolIterations++

		messages = append(messages, assistantMsg)
		turnMessages = append(turnMessages, assistantMsg)

		toolMessages, toolSteps, err := executeToolCalls(ctx, toolCalls, state.toolExecutors, emit)
		if err != nil {
			return nil, err
		}
		steps = append(steps, toolSteps...)
		messages = append(messages, toolMessages...)
		turnMessages = append(turnMessages, toolMessages...)
	}

	return &Turn{
		Input:        input,
		Reply:        strings.TrimSpace(finalReply),
		Reasoning:    strings.TrimSpace(reasoning.String()),
		Steps:        steps,
		Usage:        usage,
		FinishReason: finishReason,
		messages:     turnMessages,
	}, nil
}

func (a *Agent) finalReply(ctx context.Context, messages []llm.Message, state turnState) (*llm.Response, error) {
	// Auto, not None: under tool_choice=none some providers stop parsing the
	// model's native tool tokens, so a model that still tries to call a tool
	// (e.g. DeepSeek) leaks raw tool markup into Content. With auto the provider
	// parses any call into structural ToolCalls — which the callers ignore — so
	// Content stays clean. The prompt below is what actually steers toward an
	// answer; the choice mode only controls whether stray calls leak as text.
	toolChoice := llm.ToolChoice{Mode: llm.ToolChoiceAuto}
	finalMessages := make([]llm.Message, 0, len(messages)+1)
	finalMessages = append(finalMessages, messages...)
	finalMessages = append(finalMessages, llm.Message{
		Role:    llm.RoleUser,
		Content: toolLimitFinalPrompt,
	})

	return a.model.Chat(ctx, llm.Request{
		Messages:          finalMessages,
		Tools:             state.tools,
		ToolChoice:        &toolChoice,
		ParallelToolCalls: state.parallelToolCalls,
	})
}

func toolLimitSteps(calls []llm.ToolCall, maxIterations int) []Step {
	steps := make([]Step, 0, len(calls))
	err := fmt.Sprintf("tool iteration limit reached after %d iterations", maxIterations)
	for _, call := range calls {
		steps = append(steps, Step{
			Kind:       StepToolError,
			ToolName:   call.Function.Name,
			ToolCallID: call.ID,
			Arguments:  call.Function.Arguments,
			Error:      err,
		})
	}
	return steps
}

func consumeStream(stream llm.Stream, emit StreamFunc) (string, string, []llm.ToolCall, llm.FinishReason, error) {
	if stream == nil {
		return "", "", nil, "", errors.New("golem: model returned nil stream")
	}
	defer func() {
		_ = stream.Close()
	}()

	var reply strings.Builder
	var reasoning strings.Builder
	var toolCalls []llm.ToolCall
	var finishReason llm.FinishReason

	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", nil, "", err
		}

		if chunk.Text != "" {
			reply.WriteString(chunk.Text)
			if emit != nil {
				emit(StreamEvent{Kind: EventTextDelta, Text: chunk.Text})
			}
		}
		if chunk.ReasoningContent != "" {
			reasoning.WriteString(chunk.ReasoningContent)
			if emit != nil {
				emit(StreamEvent{Kind: EventReasoningDelta, Text: chunk.ReasoningContent})
			}
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}

	return reply.String(), reasoning.String(), toolCalls, finishReason, nil
}

// executeToolCalls runs the model's tool calls in order, returning the tool
// messages and steps. Each tool message's CreatedAt marks when the tool returned
// (or when its error was determined) — the real moment of that step, for
// transcript persistence.
func executeToolCalls(ctx context.Context, calls []llm.ToolCall, executors map[string]ToolFunc, emit StreamFunc) ([]llm.Message, []Step, error) {
	messages := make([]llm.Message, 0, len(calls))
	steps := make([]Step, 0, len(calls)*2)

	// Tool calls are executed sequentially even if the request allowed the model
	// to emit parallel tool calls.
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		name := call.Function.Name
		callStep := Step{
			Kind:       StepToolCall,
			ToolName:   name,
			ToolCallID: call.ID,
			Arguments:  call.Function.Arguments,
		}
		steps = append(steps, callStep)
		if emit != nil {
			emit(StreamEvent{Kind: EventToolCall, Step: callStep})
		}

		run, ok := executors[name]
		if !ok {
			msg, step := toolErrorMessage(call, fmt.Errorf("unknown tool %q", name))
			msg.CreatedAt = time.Now()
			messages = append(messages, msg)
			steps = append(steps, step)
			if emit != nil {
				emit(StreamEvent{Kind: EventToolError, Step: step})
			}
			continue
		}

		result, err := run(ctx, call)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, ctxErr
			}
			msg, step := toolErrorMessage(call, err)
			msg.CreatedAt = time.Now()
			messages = append(messages, msg)
			steps = append(steps, step)
			if emit != nil {
				emit(StreamEvent{Kind: EventToolError, Step: step})
			}
			continue
		}

		step := Step{
			Kind:       StepToolResult,
			ToolName:   name,
			ToolCallID: call.ID,
			Arguments:  call.Function.Arguments,
			Result:     result.Content,
		}
		messages = append(messages, llm.Message{
			Role:       llm.RoleTool,
			Content:    result.Content,
			ToolCallID: call.ID,
			Meta:       result.Meta,
			CreatedAt:  time.Now(),
		})
		steps = append(steps, step)
		if emit != nil {
			emit(StreamEvent{Kind: EventToolResult, Step: step})
		}
	}

	return messages, steps, nil
}

func toolErrorMessage(call llm.ToolCall, err error) (llm.Message, Step) {
	content := fmt.Sprintf("tool %s error: %v", call.Function.Name, err)
	step := Step{
		Kind:       StepToolError,
		ToolName:   call.Function.Name,
		ToolCallID: call.ID,
		Arguments:  call.Function.Arguments,
		Error:      err.Error(),
	}
	return llm.Message{
		Role:       llm.RoleTool,
		Content:    content,
		ToolCallID: call.ID,
	}, step
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
	a.usage = addUsage(a.usage, turn.Usage)
}

func normalizeTool(tool Tool) (llm.Tool, ToolFunc, error) {
	definition := tool.Definition
	if definition.Type == "" {
		definition.Type = llm.ToolTypeFunction
	}

	definition.Function.Name = strings.TrimSpace(definition.Function.Name)
	if definition.Function.Name == "" {
		return llm.Tool{}, nil, errors.New("golem: tool name is required")
	}
	if tool.Run == nil {
		return llm.Tool{}, nil, fmt.Errorf("golem: tool %q run function is required", definition.Function.Name)
	}

	return definition, tool.Run, nil
}

func appendReasoning(builder *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString(text)
}

func cloneToolChoice(choice *llm.ToolChoice) *llm.ToolChoice {
	if choice == nil {
		return nil
	}
	out := *choice
	return &out
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneMessages(messages []llm.Message) []llm.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]llm.Message, len(messages))
	for i, msg := range messages {
		out[i] = msg
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = make([]llm.ToolCall, len(msg.ToolCalls))
			copy(out[i].ToolCalls, msg.ToolCalls)
		}
	}
	return out
}

func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		CachedTokens:     a.CachedTokens + b.CachedTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}

func FormatUsage(u llm.Usage) string {
	return fmt.Sprintf("prompt=%d cached=%d completion=%d total=%d",
		u.PromptTokens,
		u.CachedTokens,
		u.CompletionTokens,
		u.TotalTokens,
	)
}
