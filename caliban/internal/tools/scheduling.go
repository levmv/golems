package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

const localTimeLayout = "2006-01-02 15:04"

// Scheduler is the engine capability the scheduling tools depend on. The engine
// satisfies it; tools never import engine.
type Scheduler interface {
	ScheduleReminder(ctx context.Context, text string, schedule tasks.Schedule) (id string, err error)
	ScheduleTurn(ctx context.Context, prompt string, schedule tasks.Schedule) (id string, err error)
	ListScheduled(ctx context.Context) ([]tasks.Task, error)
	CancelScheduled(ctx context.Context, id string) (bool, error)
}

type scheduleArgs struct {
	Text   string `json:"text,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	At     string `json:"at,omitempty"`
	Every  string `json:"every,omitempty"`
	Cron   string `json:"cron,omitempty"`
}

type cancelArgs struct {
	ID string `json:"id"`
}

// Scheduling returns the four scheduling tools. loc is the user's timezone, used
// to interpret `at` wall-clock times and cron expressions and to render `next`.
func Scheduling(s Scheduler, loc *time.Location) []golem.Tool {
	return []golem.Tool{
		scheduleTool(s, loc, "schedule_reminder", "text",
			"Schedule a reminder: at the given time, a stored message is pushed to the user. "+
				"No model call — cheap and reliable. Use this for plain 'remind me' requests.",
			func(ctx context.Context, text string, sc tasks.Schedule) (string, error) {
				return s.ScheduleReminder(ctx, text, sc)
			}),
		scheduleTool(s, loc, "schedule_turn", "prompt",
			"Schedule an agent turn: at the given time, the given prompt is injected as a turn "+
				"and you act on it with current context. Use when the reminder needs reasoning or fresh data.",
			func(ctx context.Context, prompt string, sc tasks.Schedule) (string, error) {
				return s.ScheduleTurn(ctx, prompt, sc)
			}),
		listScheduledTool(s, loc),
		cancelScheduledTool(s),
	}
}

func scheduleTool(s Scheduler, loc *time.Location, name, contentField, description string,
	run func(ctx context.Context, content string, sc tasks.Schedule) (string, error)) golem.Tool {

	schema := jsonschema.Obj(
		jsonschema.Required(contentField, jsonschema.Str{
			Description: "The " + contentField + " to deliver.",
		}),
		jsonschema.Optional("at", jsonschema.Str{
			Description: "One-off time: RFC 3339, or 'YYYY-MM-DD HH:MM' in the user's timezone.",
		}),
		jsonschema.Optional("every", jsonschema.Str{
			Description: "Recurring interval as a Go duration (>= 1m), e.g. '90m', '24h'.",
		}),
		jsonschema.Optional("cron", jsonschema.Str{
			Description: "Recurring 5-field cron expression in the user's timezone, e.g. '0 9 * * 1-5'.",
		}),
	)
	return golem.FunctionTool(name, description, schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			var args scheduleArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			content := args.Text
			if content == "" {
				content = args.Prompt
			}
			if strings.TrimSpace(content) == "" {
				return golem.ToolResult{}, fmt.Errorf("%s is required", contentField)
			}
			schedule, err := buildSchedule(args, loc)
			if err != nil {
				return golem.ToolResult{}, err
			}
			id, err := run(ctx, content, schedule)
			if err != nil {
				return golem.ToolResult{}, err
			}
			return golem.ToolResult{Content: fmt.Sprintf("scheduled (id: %s)", id)}, nil
		})
}

// buildSchedule turns the at/every/cron arguments into a tasks.Schedule,
// requiring exactly one and reporting friendly hints on malformed input.
func buildSchedule(args scheduleArgs, loc *time.Location) (tasks.Schedule, error) {
	set := 0
	for _, v := range []string{args.At, args.Every, args.Cron} {
		if strings.TrimSpace(v) != "" {
			set++
		}
	}
	if set != 1 {
		return tasks.Schedule{}, fmt.Errorf("provide exactly one of 'at', 'every', or 'cron'")
	}

	switch {
	case args.At != "":
		t, err := parseAt(args.At, loc)
		if err != nil {
			return tasks.Schedule{}, err
		}
		return tasks.Once(t), nil
	case args.Every != "":
		d, err := time.ParseDuration(args.Every)
		if err != nil {
			return tasks.Schedule{}, fmt.Errorf("invalid 'every' duration %q: %v", args.Every, err)
		}
		if d < time.Minute {
			return tasks.Schedule{}, fmt.Errorf("'every' must be at least 1m, got %s", d)
		}
		return tasks.Every(d), nil
	default:
		sc := tasks.Cron(args.Cron, loc.String())
		if err := sc.Validate(); err != nil {
			return tasks.Schedule{}, fmt.Errorf("invalid 'cron' expression %q: %v", args.Cron, err)
		}
		return sc, nil
	}
}

func parseAt(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(localTimeLayout, s, loc); err == nil {
		return t, nil
	}
	if t, ok := parseAtWithZoneSuffix(s, loc); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid 'at' time %q: use RFC 3339 or 'YYYY-MM-DD HH:MM'", s)
}

func parseAtWithZoneSuffix(s string, loc *time.Location) (time.Time, bool) {
	fields := strings.Fields(s)
	if len(fields) != 3 {
		return time.Time{}, false
	}
	base := fields[0] + " " + fields[1]
	zone := fields[2]

	if t, err := time.ParseInLocation(localTimeLayout, base, loc); err == nil {
		if abbr, _ := t.Zone(); strings.EqualFold(zone, abbr) {
			return t, true
		}
	}

	for _, layout := range []string{
		"2006-01-02 15:04 -07",
		"2006-01-02 15:04 -0700",
		"2006-01-02 15:04 -07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func listScheduledTool(s Scheduler, loc *time.Location) golem.Tool {
	return golem.FunctionToolWithEffect(golem.ToolEffectRead, "list_scheduled",
		"List the currently scheduled reminders and agent turns, soonest first.",
		jsonschema.Obj(),
		func(ctx context.Context, _ llm.ToolCall) (golem.ToolResult, error) {
			list, err := s.ListScheduled(ctx)
			if err != nil {
				return golem.ToolResult{}, err
			}
			return golem.ToolResult{Content: renderSchedule(list, loc)}, nil
		})
}

func cancelScheduledTool(s Scheduler) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("id", jsonschema.Str{Description: "The scheduled task id to cancel."}),
	)
	return golem.FunctionTool("cancel_scheduled",
		"Cancel a scheduled reminder or agent turn by its id.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			var args cancelArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.ID) == "" {
				return golem.ToolResult{}, fmt.Errorf("id is required")
			}
			ok, err := s.CancelScheduled(ctx, args.ID)
			if err != nil {
				return golem.ToolResult{}, err
			}
			if !ok {
				return golem.ToolResult{Content: fmt.Sprintf("no scheduled task with id %s", args.ID)}, nil
			}
			return golem.ToolResult{Content: "cancelled " + args.ID}, nil
		})
}

// renderSchedule formats tasks as a compact text table for the model.
func renderSchedule(list []tasks.Task, loc *time.Location) string {
	if len(list) == 0 {
		return "(nothing scheduled)"
	}
	var b strings.Builder
	for _, t := range list {
		next := "exhausted"
		if t.NextRunAt != nil {
			next = t.NextRunAt.In(loc).Format(localTimeLayout)
		}
		fmt.Fprintf(&b, "%s  %s  %s  next=%s  %q\n",
			t.ID, t.Kind, describeSchedule(t.Schedule), next, truncate(payloadText(t), 80))
	}
	return strings.TrimRight(b.String(), "\n")
}

func describeSchedule(sc tasks.Schedule) string {
	switch sc.Kind {
	case tasks.ScheduleOnce:
		return "once"
	case tasks.ScheduleEvery:
		return "every " + sc.Interval.String()
	case tasks.ScheduleCron:
		return "cron(" + sc.CronExpr + ")"
	default:
		return string(sc.Kind)
	}
}

// payloadText extracts the human-facing text from a reminder/agent-turn payload.
func payloadText(t tasks.Task) string {
	var p struct {
		Text   string `json:"text"`
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(t.Payload, &p)
	if p.Text != "" {
		return p.Text
	}
	return p.Prompt
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
