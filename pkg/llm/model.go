package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Model represents a bound provider, model ID, and default execution parameters.
//
// Decorator order is significant. Calls such as
// model.WithRetries(...).WithUsageTracking(...).WithLogging(...) log and track one
// logical call, while placing logging or tracking before retries observes each
// attempt.
type Model struct {
	client          Client
	modelID         string
	temperature     *float32
	maxTokens       *int
	reasoningEffort string
}

// Model creates a new model handle from a string like "openai/gpt-4o".
func (r *Registry) Model(uri string) (Model, error) {
	parts := strings.SplitN(uri, "/", 2)
	if len(parts) != 2 {
		return Model{}, fmt.Errorf("invalid model uri: %s (expected provider/model)", uri)
	}

	provider, modelID := parts[0], parts[1]
	client, ok := r.providers[provider]
	if !ok {
		return Model{}, fmt.Errorf("provider %s not found in router", provider)
	}

	return Model{
		client:  client,
		modelID: modelID,
	}, nil
}

func (m Model) WithTemperature(t float32) Model {
	m.temperature = &t
	return m
}

func (m Model) WithMaxTokens(mt int) Model {
	m.maxTokens = &mt
	return m
}

// WithReasoningEffort sets the provider-neutral reasoning effort applied to
// requests which do not override it. An empty value preserves provider
// defaults and emits no additional wire field.
func (m Model) WithReasoningEffort(effort string) Model {
	m.reasoningEffort = strings.ToLower(strings.TrimSpace(effort))
	return m
}

// WithRetries retries Chat failures and failures while opening a Stream. Errors
// after Stream returns must be handled by the stream-consuming layer; agent
// runtimes should prefer golem.Requester, which can retry a complete stream.
func (m Model) WithRetries(maxRetries int, baseDelay time.Duration) Model {
	if maxRetries <= 0 {
		return m
	}

	m.client = &retryClient{
		client:     m.client,
		maxRetries: maxRetries,
		baseDelay:  baseDelay,
	}
	return m
}

// WithLogging wraps the model execution to log requests, responses, and durations.
func (m Model) WithLogging(logger Logger) Model {
	if logger == nil {
		return m
	}
	m.client = &loggingClient{
		client: m.client,
		logger: logger,
	}
	return m
}

// WithUsageTracking automatically aggregates token usage for successful requests.
func (m Model) WithUsageTracking(tracker UsageTracker) Model {
	if tracker == nil {
		return m
	}
	m.client = &usageClient{
		client:  m.client,
		tracker: tracker,
	}
	return m
}

// Chat executes the request using the Model's defaults, allowing Request overrides.
func (m Model) Chat(ctx context.Context, req Request) (*Response, error) {
	return m.client.Chat(ctx, m.buildRequest(req))
}

func (m Model) Stream(ctx context.Context, req Request) (Stream, error) {
	return m.client.Stream(ctx, m.buildRequest(req))
}

func (m Model) buildRequest(req Request) *Request {
	temp := m.temperature
	if req.Temperature != nil {
		temp = req.Temperature
	}

	maxTokens := m.maxTokens
	if req.MaxTokens != nil {
		maxTokens = req.MaxTokens
	}
	reasoningEffort := m.reasoningEffort
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		reasoningEffort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	}

	return &Request{
		Model:              m.modelID,
		Messages:           req.Messages,
		Temperature:        temp,
		MaxTokens:          maxTokens,
		ReasoningEffort:    reasoningEffort,
		Tools:              req.Tools,
		ToolChoice:         req.ToolChoice,
		ParallelToolCalls:  req.ParallelToolCalls,
		ProviderExtensions: req.ProviderExtensions,
	}
}
