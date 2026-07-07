package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

func TestBuildScheduleRequiresExactlyOne(t *testing.T) {
	loc := time.UTC
	cases := []scheduleArgs{
		{},                                    // none
		{At: "2026-06-16 09:00", Every: "5m"}, // two
		{Every: "5m", Cron: "0 9 * * *"},      // two
	}
	for i, args := range cases {
		if _, err := buildSchedule(args, loc); err == nil {
			t.Fatalf("case %d: expected error for %+v", i, args)
		}
	}
}

func TestBuildScheduleEvery(t *testing.T) {
	if _, err := buildSchedule(scheduleArgs{Every: "30s"}, time.UTC); err == nil {
		t.Fatal("expected error for sub-minute interval")
	}
	sc, err := buildSchedule(scheduleArgs{Every: "90m"}, time.UTC)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	if sc.Kind != tasks.ScheduleEvery || sc.Interval != 90*time.Minute {
		t.Fatalf("unexpected schedule: %+v", sc)
	}
}

func TestBuildScheduleAtLocalTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	// 09:00 Amsterdam in June (CEST, +2) is 07:00 UTC.
	sc, err := buildSchedule(scheduleArgs{At: "2026-06-16 09:00"}, loc)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	if sc.Kind != tasks.ScheduleOnce {
		t.Fatalf("expected once, got %+v", sc)
	}
	if got := sc.At.UTC(); got.Hour() != 7 || got.Minute() != 0 {
		t.Fatalf("expected 07:00 UTC, got %s", got)
	}
}

func TestBuildScheduleAtRFC3339(t *testing.T) {
	sc, err := buildSchedule(scheduleArgs{At: "2026-06-16T09:00:00Z"}, time.UTC)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	if sc.At.Hour() != 9 {
		t.Fatalf("unexpected at: %s", sc.At)
	}
}

func TestBuildScheduleAtTimezoneSuffixes(t *testing.T) {
	want := time.Date(2026, 6, 18, 18, 9, 0, 0, time.UTC)
	cases := []string{
		"2026-06-18 21:09 +03",
		"2026-06-18 21:09 +0300",
		"2026-06-18 21:09 +03:00",
	}
	for _, at := range cases {
		sc, err := buildSchedule(scheduleArgs{At: at}, time.UTC)
		if err != nil {
			t.Fatalf("buildSchedule(%q): %v", at, err)
		}
		if got := sc.At.UTC(); !got.Equal(want) {
			t.Fatalf("buildSchedule(%q): expected %s, got %s", at, want, got)
		}
	}
}

func TestBuildScheduleAtCurrentTimezoneSuffix(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	sc, err := buildSchedule(scheduleArgs{At: "2026-06-18 21:09 MSK"}, loc)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	want := time.Date(2026, 6, 18, 18, 9, 0, 0, time.UTC)
	if got := sc.At.UTC(); !got.Equal(want) {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestBuildScheduleAtLocalTimezoneSuffix(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	sc, err := buildSchedule(scheduleArgs{At: "2026-06-16 09:00 CEST"}, loc)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	want := time.Date(2026, 6, 16, 7, 0, 0, 0, time.UTC)
	if got := sc.At.UTC(); !got.Equal(want) {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestBuildScheduleRejectsUnknownTimezoneSuffix(t *testing.T) {
	if _, err := buildSchedule(scheduleArgs{At: "2026-06-18 21:09 Europe/Moscow"}, time.UTC); err == nil {
		t.Fatal("expected IANA suffix to be rejected")
	}
	if _, err := buildSchedule(scheduleArgs{At: "2026-06-18 21:09 CST"}, time.UTC); err == nil {
		t.Fatal("expected ambiguous abbreviation to be rejected")
	}
}

func TestBuildScheduleCron(t *testing.T) {
	sc, err := buildSchedule(scheduleArgs{Cron: "0 9 * * 1-5"}, time.UTC)
	if err != nil {
		t.Fatalf("buildSchedule: %v", err)
	}
	if sc.Kind != tasks.ScheduleCron || sc.CronExpr != "0 9 * * 1-5" {
		t.Fatalf("unexpected cron: %+v", sc)
	}
	if _, err := buildSchedule(scheduleArgs{Cron: "not a cron"}, time.UTC); err == nil {
		t.Fatal("expected error for malformed cron")
	}
}

// fakeScheduler records calls for the tool round-trip tests.
type fakeScheduler struct {
	reminders []string
	lastSched tasks.Schedule
	cancelled string
}

func (f *fakeScheduler) ScheduleReminder(_ context.Context, text string, sc tasks.Schedule) (string, error) {
	f.reminders = append(f.reminders, text)
	f.lastSched = sc
	return "rem-1", nil
}
func (f *fakeScheduler) ScheduleTurn(_ context.Context, _ string, sc tasks.Schedule) (string, error) {
	f.lastSched = sc
	return "turn-1", nil
}
func (f *fakeScheduler) ListScheduled(context.Context) ([]tasks.Task, error) { return nil, nil }
func (f *fakeScheduler) CancelScheduled(_ context.Context, id string) (bool, error) {
	f.cancelled = id
	return true, nil
}

func callTool(t *testing.T, tool golem.Tool, args map[string]any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := tool.Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: string(raw)}})
	return res.Content, err
}

func findTool(tools []golem.Tool, name string) golem.Tool {
	for _, tl := range tools {
		if tl.Definition.Function.Name == name {
			return tl
		}
	}
	return golem.Tool{}
}

func TestScheduleReminderToolRoundTrip(t *testing.T) {
	f := &fakeScheduler{}
	toolset := Scheduling(f, time.UTC)

	out, err := callTool(t, findTool(toolset, "schedule_reminder"), map[string]any{
		"text":  "call mom",
		"every": "24h",
	})
	if err != nil {
		t.Fatalf("schedule_reminder: %v", err)
	}
	if !strings.Contains(out, "rem-1") {
		t.Fatalf("expected id in output, got %q", out)
	}
	if len(f.reminders) != 1 || f.reminders[0] != "call mom" {
		t.Fatalf("scheduler not called correctly: %+v", f.reminders)
	}
	if f.lastSched.Interval != 24*time.Hour {
		t.Fatalf("unexpected schedule: %+v", f.lastSched)
	}
}

func TestScheduleReminderToolValidationError(t *testing.T) {
	f := &fakeScheduler{}
	toolset := Scheduling(f, time.UTC)
	// No schedule field → tool error, scheduler untouched.
	if _, err := callTool(t, findTool(toolset, "schedule_reminder"), map[string]any{"text": "x"}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(f.reminders) != 0 {
		t.Fatal("scheduler should not be called on validation error")
	}
}

func TestCancelScheduledTool(t *testing.T) {
	f := &fakeScheduler{}
	toolset := Scheduling(f, time.UTC)
	out, err := callTool(t, findTool(toolset, "cancel_scheduled"), map[string]any{"id": "rem-1"})
	if err != nil {
		t.Fatalf("cancel_scheduled: %v", err)
	}
	if f.cancelled != "rem-1" || !strings.Contains(out, "rem-1") {
		t.Fatalf("cancel not wired: cancelled=%q out=%q", f.cancelled, out)
	}
}
