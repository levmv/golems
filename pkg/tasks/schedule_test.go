package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScheduleInitialNextRun(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 7, 0, 0, time.UTC)
	tests := []struct {
		name     string
		schedule Schedule
		want     time.Time
	}{
		{
			name:     "once",
			schedule: Once(now.Add(-time.Hour)),
			want:     now.Add(-time.Hour),
		},
		{
			name:     "every",
			schedule: Every(5 * time.Minute),
			want:     now.Add(5 * time.Minute),
		},
		{
			name:     "every from",
			schedule: EveryFrom(now.Add(time.Hour), 30*time.Minute),
			want:     now.Add(time.Hour),
		},
		{
			name:     "cron",
			schedule: Cron("*/15 * * * *", "UTC"),
			want:     time.Date(2026, 5, 8, 12, 15, 0, 0, time.UTC),
		},
		{
			name:     "cron from",
			schedule: CronFrom("*/15 * * * *", "UTC", now),
			want:     now,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.schedule.initialNextRun(now)
			if err != nil {
				t.Fatalf("initialNextRun returned error: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestScheduleNextAfterRunSkipsMissedOccurrences(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 7, 0, 0, time.UTC)

	next, err := Every(30*time.Minute).nextAfterRun(time.Date(2026, 5, 8, 11, 0, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatalf("Every.nextAfterRun returned error: %v", err)
	}
	want := time.Date(2026, 5, 8, 12, 30, 0, 0, time.UTC)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}

	next, err = Cron("*/15 * * * *", "UTC").nextAfterRun(time.Date(2026, 5, 8, 11, 45, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatalf("Cron.nextAfterRun returned error: %v", err)
	}
	want = time.Date(2026, 5, 8, 12, 15, 0, 0, time.UTC)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}

	next, err = Every(time.Minute).nextAfterRun(time.Date(2025, 11, 8, 12, 0, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatalf("Every.nextAfterRun for long gap returned error: %v", err)
	}
	want = time.Date(2026, 5, 8, 12, 8, 0, 0, time.UTC)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}

	next, err = Cron("* * * * *", "UTC").nextAfterRun(time.Date(2025, 11, 8, 12, 0, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatalf("Cron.nextAfterRun for long gap returned error: %v", err)
	}
	want = time.Date(2026, 5, 8, 12, 8, 0, 0, time.UTC)
	if next == nil || !next.Equal(want) {
		t.Fatalf("expected %s, got %v", want, next)
	}
}

func TestScheduleValidation(t *testing.T) {
	tests := []Schedule{
		{},
		Once(time.Time{}),
		Every(0),
		Cron("not cron", "UTC"),
		Cron("* * * * *", "Invalid/Timezone"),
	}
	for _, schedule := range tests {
		if err := schedule.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", schedule)
		}
	}
}

func TestQueueRejectsInvalidSchedule(t *testing.T) {
	q := mustQueue(t, NewMemoryStore(), HandlerFunc(func(ctx context.Context, task Task) error { return nil }), Options{})
	_, err := q.Enqueue(context.Background(), Enqueue{ID: "bad", Kind: "test", Schedule: Every(0)})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
