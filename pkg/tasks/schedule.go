package tasks

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type ScheduleKind string

const (
	ScheduleOnce  ScheduleKind = "once"
	ScheduleEvery ScheduleKind = "every"
	ScheduleCron  ScheduleKind = "cron"
)

// Schedule is a persistable task schedule.
type Schedule struct {
	Kind     ScheduleKind  `json:"kind"`
	At       time.Time     `json:"at,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	Start    time.Time     `json:"start,omitempty"`
	CronExpr string        `json:"cron,omitempty"`
	Timezone string        `json:"timezone,omitempty"`
}

func Once(at time.Time) Schedule {
	return Schedule{Kind: ScheduleOnce, At: at.UTC()}
}

func Every(interval time.Duration) Schedule {
	return Schedule{Kind: ScheduleEvery, Interval: interval}
}

func EveryFrom(start time.Time, interval time.Duration) Schedule {
	return Schedule{Kind: ScheduleEvery, Start: start.UTC(), Interval: interval}
}

func Cron(expr, timezone string) Schedule {
	return Schedule{Kind: ScheduleCron, CronExpr: expr, Timezone: timezone}
}

func CronFrom(expr, timezone string, firstRunAt time.Time) Schedule {
	return Schedule{Kind: ScheduleCron, CronExpr: expr, Timezone: timezone, Start: firstRunAt.UTC()}
}

func (s Schedule) Validate() error {
	switch s.Kind {
	case ScheduleOnce:
		if s.At.IsZero() {
			return fmt.Errorf("once schedule time is required")
		}
	case ScheduleEvery:
		if s.Interval <= 0 {
			return fmt.Errorf("every interval must be positive")
		}
	case ScheduleCron:
		if strings.TrimSpace(s.CronExpr) == "" {
			return fmt.Errorf("cron expression is required")
		}
		loc, err := location(s.Timezone)
		if err != nil {
			return fmt.Errorf("timezone %q: %w", s.Timezone, err)
		}
		schedule, err := parseCron(s.CronExpr)
		if err != nil {
			return err
		}
		if _, err := schedule.next(time.Now().In(loc)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown schedule kind %q", s.Kind)
	}
	return nil
}

func (s Schedule) recurring() bool {
	return s.Kind == ScheduleEvery || s.Kind == ScheduleCron
}

func (s Schedule) initialNextRun(now time.Time) (time.Time, error) {
	now = now.UTC()
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	switch s.Kind {
	case ScheduleOnce:
		return s.At.UTC(), nil
	case ScheduleEvery:
		if !s.Start.IsZero() {
			return s.Start.UTC(), nil
		}
		return now.Add(s.Interval).UTC(), nil
	case ScheduleCron:
		loc, err := location(s.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		if !s.Start.IsZero() {
			return s.Start.UTC(), nil
		}
		parsed, err := parseCron(s.CronExpr)
		if err != nil {
			return time.Time{}, err
		}
		next, err := parsed.next(now.In(loc))
		if err != nil {
			return time.Time{}, err
		}
		return next.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unknown schedule kind %q", s.Kind)
	}
}

func (s Schedule) nextAfterRun(dueAt, now time.Time) (*time.Time, error) {
	if !s.recurring() {
		return nil, nil
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	now = now.UTC()
	switch s.Kind {
	case ScheduleEvery:
		dueAt = dueAt.UTC()
		next := dueAt.Add(s.Interval)
		for !next.After(now) {
			missed := now.Sub(dueAt)/s.Interval + 1
			next = dueAt.Add(missed * s.Interval)
			if !next.After(now) {
				next = next.Add(s.Interval)
			}
		}
		return &next, nil
	case ScheduleCron:
		loc, err := location(s.Timezone)
		if err != nil {
			return nil, err
		}
		parsed, err := parseCron(s.CronExpr)
		if err != nil {
			return nil, err
		}
		anchor := dueAt.In(loc)
		now = now.In(loc)
		if now.After(anchor) {
			anchor = now
		}
		next, err := parsed.next(anchor)
		if err != nil {
			return nil, err
		}
		next = next.UTC()
		return &next, nil
	default:
		return nil, nil
	}
}

func location(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

type cronSchedule struct {
	minutes              []int
	hours                []int
	days                 []int
	months               []int
	weekdays             []int
	dayOfMonthRestricted bool
	dayOfWeekRestricted  bool
}

type cronField struct {
	values     []int
	restricted bool
}

const cronSearchYears = 8

func parseCron(expr string) (cronSchedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronSchedule{}, fmt.Errorf("invalid cron expression: expected 5 fields, got %d", len(parts))
	}

	minutes, err := parseCronField(parts[0], 0, 59, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("minute field: %w", err)
	}
	hours, err := parseCronField(parts[1], 0, 23, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("hour field: %w", err)
	}
	days, err := parseCronField(parts[2], 1, 31, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-month field: %w", err)
	}
	months, err := parseCronField(parts[3], 1, 12, false)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("month field: %w", err)
	}
	weekdays, err := parseCronField(parts[4], 0, 7, true)
	if err != nil {
		return cronSchedule{}, fmt.Errorf("day-of-week field: %w", err)
	}

	return cronSchedule{
		minutes:              minutes.values,
		hours:                hours.values,
		days:                 days.values,
		months:               months.values,
		weekdays:             weekdays.values,
		dayOfMonthRestricted: days.restricted,
		dayOfWeekRestricted:  weekdays.restricted,
	}, nil
}

func (s cronSchedule) next(after time.Time) (time.Time, error) {
	if after.IsZero() {
		return time.Time{}, fmt.Errorf("cron schedule requires an anchor time")
	}

	loc := after.Location()
	anchor := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc)
	candidate := anchor.Add(time.Minute)
	deadline := candidate.AddDate(cronSearchYears, 0, 0)

	for day := time.Date(candidate.Year(), candidate.Month(), candidate.Day(), 0, 0, 0, 0, loc); !day.After(deadline); day = day.AddDate(0, 0, 1) {
		if !containsInt(s.months, int(day.Month())) || !s.dayMatches(day) {
			continue
		}
		for _, hour := range s.hours {
			for _, minute := range s.minutes {
				next := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
				if next.Before(candidate) || next.After(deadline) {
					continue
				}
				return next, nil
			}
		}
	}

	return time.Time{}, fmt.Errorf("no matching time found within %d years", cronSearchYears)
}

func (s cronSchedule) dayMatches(day time.Time) bool {
	dayOfMonthMatches := containsInt(s.days, day.Day())
	dayOfWeekMatches := containsInt(s.weekdays, int(day.Weekday()))

	switch {
	case s.dayOfMonthRestricted && s.dayOfWeekRestricted:
		return dayOfMonthMatches || dayOfWeekMatches
	case s.dayOfMonthRestricted:
		return dayOfMonthMatches
	case s.dayOfWeekRestricted:
		return dayOfWeekMatches
	default:
		return true
	}
}

func parseCronField(expr string, min, max int, sundaySeven bool) (cronField, error) {
	values := make(map[int]struct{})
	if expr == "*" {
		for i := min; i <= max; i++ {
			values[normalizeCronValue(i, sundaySeven)] = struct{}{}
		}
		out := sortedCronValues(values)
		return cronField{values: out, restricted: len(out) < cronDomainSize(min, max, sundaySeven)}, nil
	}

	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return cronField{}, fmt.Errorf("empty field part")
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("invalid step %q", part)
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
				return cronField{}, fmt.Errorf("invalid range %q", part)
			}
			if start < min || end > max || start > end {
				return cronField{}, fmt.Errorf("range %q outside %d-%d", part, min, max)
			}
			for i := start; i <= end; i++ {
				values[normalizeCronValue(i, sundaySeven)] = struct{}{}
			}
			continue
		}

		n, err := strconv.Atoi(part)
		if err != nil {
			return cronField{}, fmt.Errorf("invalid value %q", part)
		}
		if n < min || n > max {
			return cronField{}, fmt.Errorf("value %q outside %d-%d", part, min, max)
		}
		values[normalizeCronValue(n, sundaySeven)] = struct{}{}
	}

	out := sortedCronValues(values)
	if len(out) == 0 {
		return cronField{}, fmt.Errorf("field has no values")
	}
	return cronField{values: out, restricted: len(out) < cronDomainSize(min, max, sundaySeven)}, nil
}

func normalizeCronValue(value int, sundaySeven bool) int {
	if sundaySeven && value == 7 {
		return 0
	}
	return value
}

func sortedCronValues(values map[int]struct{}) []int {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func cronDomainSize(min, max int, sundaySeven bool) int {
	size := max - min + 1
	if sundaySeven {
		size--
	}
	return size
}

func containsInt(values []int, want int) bool {
	_, ok := slices.BinarySearch(values, want)
	return ok
}
