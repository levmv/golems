package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type atTrigger struct {
	at time.Time
}

// At returns a one-shot trigger.
func At(t time.Time) Trigger {
	return atTrigger{at: t}
}

func (t atTrigger) Next(after time.Time) (time.Time, bool) {
	if after.IsZero() || t.at.After(after) {
		return t.at, true
	}
	return time.Time{}, false
}

func (t atTrigger) Validate() error {
	if t.at.IsZero() {
		return fmt.Errorf("at trigger time is required")
	}
	return nil
}

type everyTrigger struct {
	interval time.Duration
}

// Every returns a recurring interval trigger.
func Every(interval time.Duration) Trigger {
	return everyTrigger{interval: interval}
}

func (t everyTrigger) Next(after time.Time) (time.Time, bool) {
	if t.interval <= 0 {
		return time.Time{}, false
	}
	if after.IsZero() {
		return time.Time{}, false
	}
	return after.Add(t.interval), true
}

func (t everyTrigger) Validate() error {
	if t.interval <= 0 {
		return fmt.Errorf("every trigger interval must be positive")
	}
	return nil
}

type cronTrigger struct {
	expr string
}

// Cron returns a small five-field cron trigger.
//
// Supported syntax per field: *, single number, comma lists, ranges, and */N
// steps. Month is 1-12. Day of week is 0-7, where 0 and 7 both mean Sunday.
// When both day-of-month and day-of-week are restricted, both must match.
func Cron(expr string) Trigger {
	return cronTrigger{expr: expr}
}

func (t cronTrigger) Next(after time.Time) (time.Time, bool) {
	next, err := nextCron(t.expr, after)
	if err != nil {
		return time.Time{}, false
	}
	return next, true
}

func (t cronTrigger) Validate() error {
	_, err := nextCron(t.expr, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return err
}

func nextCron(expr string, after time.Time) (time.Time, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return time.Time{}, fmt.Errorf("invalid cron expression: expected 5 fields, got %d", len(parts))
	}

	if after.IsZero() {
		return time.Time{}, fmt.Errorf("cron trigger requires an anchor time")
	}

	minutes, err := parseCronField(parts[0], 0, 59, false)
	if err != nil {
		return time.Time{}, fmt.Errorf("minute field: %w", err)
	}
	hours, err := parseCronField(parts[1], 0, 23, false)
	if err != nil {
		return time.Time{}, fmt.Errorf("hour field: %w", err)
	}
	days, err := parseCronField(parts[2], 1, 31, false)
	if err != nil {
		return time.Time{}, fmt.Errorf("day-of-month field: %w", err)
	}
	months, err := parseCronField(parts[3], 1, 12, false)
	if err != nil {
		return time.Time{}, fmt.Errorf("month field: %w", err)
	}
	weekdays, err := parseCronField(parts[4], 0, 7, true)
	if err != nil {
		return time.Time{}, fmt.Errorf("day-of-week field: %w", err)
	}

	loc := after.Location()
	anchor := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc)
	candidate := anchor.Add(time.Minute)
	deadline := candidate.Add(366 * 24 * time.Hour)

	for day := time.Date(candidate.Year(), candidate.Month(), candidate.Day(), 0, 0, 0, 0, loc); !day.After(deadline); day = day.AddDate(0, 0, 1) {
		if !containsInt(months, int(day.Month())) ||
			!containsInt(days, day.Day()) ||
			!containsInt(weekdays, int(day.Weekday())) {
			continue
		}

		for _, hour := range hours {
			for _, minute := range minutes {
				next := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
				if next.Before(candidate) || next.After(deadline) {
					continue
				}
				return next, nil
			}
		}
	}

	return time.Time{}, fmt.Errorf("no matching time found within one year")
}

func parseCronField(expr string, min, max int, sundaySeven bool) ([]int, error) {
	values := make(map[int]struct{})

	if expr == "*" {
		for i := min; i <= max; i++ {
			values[normalizeCronValue(i, sundaySeven)] = struct{}{}
		}
		return sortedCronValues(values), nil
	}

	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty field part")
		}

		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			for i := min; i <= max; i++ {
				if (i-min)%step == 0 {
					values[normalizeCronValue(i, sundaySeven)] = struct{}{}
				}
			}
			continue
		}

		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(bounds[0])
			end, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			if start < min || end > max || start > end {
				return nil, fmt.Errorf("range %q outside %d-%d", part, min, max)
			}
			for i := start; i <= end; i++ {
				values[normalizeCronValue(i, sundaySeven)] = struct{}{}
			}
			continue
		}

		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q", part)
		}
		if n < min || n > max {
			return nil, fmt.Errorf("value %q outside %d-%d", part, min, max)
		}
		values[normalizeCronValue(n, sundaySeven)] = struct{}{}
	}

	out := sortedCronValues(values)
	if len(out) == 0 {
		return nil, fmt.Errorf("field has no allowed values")
	}
	return out, nil
}

func sortedCronValues(values map[int]struct{}) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func containsInt(values []int, target int) bool {
	i := sort.SearchInts(values, target)
	return i < len(values) && values[i] == target
}

func normalizeCronValue(v int, sundaySeven bool) int {
	if sundaySeven && v == 7 {
		return 0
	}
	return v
}
