package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/llm"
)

// Result is the structured output of an AI analysis.
type Result struct {
	Severity    Severity `json:"severity"`
	ShouldAlert bool     `json:"should_alert"`
	Summary     string   `json:"summary"`
	Evidence    string   `json:"evidence"`
}

// Severity classifies the alert level.
type Severity string

const (
	SeverityNormal     Severity = "normal"
	SeveritySuspicious Severity = "suspicious"
	SeverityUrgent     Severity = "urgent"
)

// Input bundles everything the LLM needs to make a decision.
type Input struct {
	CheckID        string
	Current        *models.CollectorOutput
	History        []storage.RunRecord
	Notes          []string
	ActiveIncident *storage.IncidentRecord
	IncludeHistory time.Duration
}

// Analyzer uses an LLM to evaluate monitoring data.
type Analyzer struct {
	model  llm.Model
	logger llm.Logger
}

// New creates a new Analyzer.
func New(model llm.Model, logger llm.Logger) *Analyzer {
	return &Analyzer{model: model, logger: logger}
}

// Analyze evaluates the current state using the LLM and returns a decision.
func (a *Analyzer) Analyze(ctx context.Context, in Input) (*Result, error) {
	prompt := buildPrompt(in)

	a.logger.Debug("Analysis prompt for %s: %s", in.CheckID, truncate(prompt, 500))

	resp, err := a.model.Chat(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	a.logger.Debug("Analysis response for %s: %s", in.CheckID, truncate(resp.Content, 500))

	var result Result
	if err := json.Unmarshal([]byte(resp.Content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w (raw: %s)", err, truncate(resp.Content, 200))
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("invalid LLM analysis result: %w", err)
	}

	return &result, nil
}

// Validate checks the analyzer contract before incident processing trusts it.
func (r *Result) Validate() error {
	if r == nil {
		return fmt.Errorf("analysis result is nil")
	}
	switch r.Severity {
	case SeverityNormal, SeveritySuspicious, SeverityUrgent:
	default:
		return fmt.Errorf("unknown severity %q", r.Severity)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

const systemPrompt = `You are Hugin, an infrastructure monitoring analyst. Your job is to evaluate monitoring data and decide whether to alert the operator.

You receive:
- Current metrics from a collector script
- Recent history of the same metrics (min/max/avg over the lookback window)
- Operator notes with local knowledge about what's normal
- Any active incident context

Your response MUST be valid JSON with these fields:
{
  "severity": "normal" | "suspicious" | "urgent",
  "should_alert": true | false,
  "summary": "one-line human-readable summary of the situation",
  "evidence": "brief bullet points explaining your reasoning"
}

Rules:
- "normal" means everything is within expected ranges. Do NOT alert.
- "suspicious" means something unusual is happening but not critical. Consider alerting if persistent.
- "urgent" means immediate action is needed. MUST alert.
- Use operator notes to understand what's normal. Trust them over generic thresholds.
- If the collector itself errored, evaluate based on the structured errors.
- If the situation is already covered by an active incident, only alert if it worsened.
- Prefer fewer alerts over more. Avoid noise.`

func buildPrompt(in Input) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("## Check: %s\n", in.CheckID))
	b.WriteString(fmt.Sprintf("## Time: %s\n\n", time.Now().UTC().Format(time.RFC3339)))

	// Current state
	b.WriteString("## Current State\n")
	b.WriteString(fmt.Sprintf("Status: %s\n", in.Current.Status))
	if in.Current.Window != "" {
		b.WriteString(fmt.Sprintf("Window: %s\n", in.Current.Window))
	}
	if len(in.Current.Metrics) > 0 {
		b.WriteString("Metrics:\n")
		for k, v := range in.Current.Metrics {
			b.WriteString(fmt.Sprintf("  %s: %v\n", k, v))
		}
	}
	if len(in.Current.Errors) > 0 {
		b.WriteString("Errors:\n")
		for _, e := range in.Current.Errors {
			b.WriteString(fmt.Sprintf("  [%s] %s\n", e.Code, e.Message))
		}
	}
	b.WriteString("\n")

	// History summary
	if len(in.History) > 0 {
		b.WriteString("## Recent History\n")
		b.WriteString(fmt.Sprintf("Runs in window: %d\n", len(in.History)))
		historySummary := computeHistorySummary(in.History)
		if len(historySummary) > 0 {
			b.WriteString("Metric ranges (min / avg / max):\n")
			for k, s := range historySummary {
				b.WriteString(fmt.Sprintf("  %s: %.2f / %.2f / %.2f\n", k, s.Min, s.Avg, s.Max))
			}
		}
		// Status distribution
		statusCounts := make(map[string]int)
		for _, r := range in.History {
			statusCounts[r.Status]++
		}
		b.WriteString("Status distribution:\n")
		for status, count := range statusCounts {
			b.WriteString(fmt.Sprintf("  %s: %d\n", status, count))
		}
		b.WriteString("\n")
	}

	// Operator notes
	if len(in.Notes) > 0 {
		b.WriteString("## Operator Notes\n")
		for _, n := range in.Notes {
			b.WriteString(fmt.Sprintf("- %s\n", n))
		}
		b.WriteString("\n")
	}

	// Active incident
	if in.ActiveIncident != nil {
		b.WriteString("## Active Incident\n")
		b.WriteString(fmt.Sprintf("ID: %s\n", in.ActiveIncident.ID))
		b.WriteString(fmt.Sprintf("Severity: %s\n", in.ActiveIncident.Severity))
		b.WriteString(fmt.Sprintf("Summary: %s\n", in.ActiveIncident.Summary))
		b.WriteString(fmt.Sprintf("Started: %s\n", in.ActiveIncident.CreatedAt.Format(time.RFC3339)))
		b.WriteString("\n")
	}

	return b.String()
}

type metricSummary struct {
	Min, Avg, Max float64
}

func computeHistorySummary(runs []storage.RunRecord) map[string]metricSummary {
	// Collect all numeric metrics across runs
	metrics := make(map[string][]float64)
	for _, r := range runs {
		for k, v := range r.Metrics {
			f := toFloat(v)
			metrics[k] = append(metrics[k], f)
		}
	}

	result := make(map[string]metricSummary)
	for k, vals := range metrics {
		if len(vals) == 0 {
			continue
		}
		sum := 0.0
		min := vals[0]
		max := vals[0]
		for _, v := range vals {
			sum += v
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		result[k] = metricSummary{
			Min: min,
			Avg: sum / float64(len(vals)),
			Max: max,
		}
	}
	return result
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
