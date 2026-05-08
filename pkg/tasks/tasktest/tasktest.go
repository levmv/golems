package tasktest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/tasks"
)

type NewStore func(t *testing.T) tasks.Store

func Run(t *testing.T, newStore NewStore) {
	t.Helper()

	t.Run("enqueue get and claim due in next run order", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		early := now.Add(-2 * time.Minute)
		late := now.Add(-time.Minute)
		future := now.Add(time.Hour)

		mustEnqueue(t, store, task("future", future))
		mustEnqueue(t, store, task("late", late))
		mustEnqueue(t, store, task("early", early))

		got, err := store.Get(ctx, "early")
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if string(got.Payload) != "payload:early" || got.Metadata["source"] != "test" {
			t.Fatalf("unexpected persisted task: %+v", got)
		}

		claimed, err := store.ClaimDue(ctx, now, time.Minute, 0, "token")
		if err != nil {
			t.Fatalf("ClaimDue returned error: %v", err)
		}
		assertTaskIDs(t, claimed, []string{"early", "late"})
		for _, task := range claimed {
			if task.LockToken != "token" || task.LockedAt == nil || !task.LockedAt.Equal(now) {
				t.Fatalf("expected lock fields on claimed task, got %+v", task)
			}
			if task.Attempts != 0 || task.LastStartedAt == nil || !task.LastStartedAt.Equal(now) {
				t.Fatalf("expected running claim metadata, got %+v", task)
			}
		}
	})

	t.Run("enqueue normalizes empty payload", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()

		job := task("empty-payload", now)
		job.Payload = nil
		mustEnqueue(t, store, job)

		got, err := store.Get(ctx, "empty-payload")
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if string(got.Payload) != "null" {
			t.Fatalf("expected empty payload to be normalized to JSON null, got %q", string(got.Payload))
		}
	})

	t.Run("claim due respects active lease and limit", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		lease := time.Minute

		mustEnqueue(t, store, task("late", now.Add(-time.Minute)))
		mustEnqueue(t, store, task("early", now.Add(-2*time.Minute)))
		mustEnqueue(t, store, task("now", now))

		claimed, err := store.ClaimDue(ctx, now, lease, 2, "token-a")
		if err != nil {
			t.Fatalf("ClaimDue returned error: %v", err)
		}
		assertTaskIDs(t, claimed, []string{"early", "late"})

		claimed, err = store.ClaimDue(ctx, now, lease, 0, "token-b")
		if err != nil {
			t.Fatalf("second ClaimDue returned error: %v", err)
		}
		assertTaskIDs(t, claimed, []string{"now"})
	})

	t.Run("next run uses lock expiry for locked due task", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		lease := 5 * time.Minute

		mustEnqueue(t, store, task("locked", now))
		mustEnqueue(t, store, task("future", now.Add(10*time.Minute)))
		if claimed, err := store.ClaimDue(ctx, now, lease, 0, "token"); err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDue returned claimed=%+v err=%v", claimed, err)
		}

		next, err := store.NextRunAt(ctx, now, lease)
		if err != nil {
			t.Fatalf("NextRunAt returned error: %v", err)
		}
		assertTimePtr(t, next, now.Add(lease))

		claimed, err := store.ClaimDue(ctx, now.Add(lease), lease, 0, "new-token")
		if err != nil {
			t.Fatalf("ClaimDue after lease expiry returned error: %v", err)
		}
		assertTaskIDs(t, claimed, []string{"locked"})
		if claimed[0].Attempts != 0 {
			t.Fatalf("expected reclaim to preserve attempts at 0, got %+v", claimed[0])
		}
	})

	t.Run("stale finish cannot update reclaimed task", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		lease := time.Minute
		reclaimAt := now.Add(2 * lease)

		mustEnqueue(t, store, task("job-1", now))
		if claimed, err := store.ClaimDue(ctx, now, lease, 0, "old-token"); err != nil || len(claimed) != 1 {
			t.Fatalf("old ClaimDue returned claimed=%+v err=%v", claimed, err)
		}
		if claimed, err := store.ClaimDue(ctx, reclaimAt, lease, 0, "new-token"); err != nil || len(claimed) != 1 {
			t.Fatalf("reclaim ClaimDue returned claimed=%+v err=%v", claimed, err)
		}

		ok, err := store.Finish(ctx, tasks.Finish{
			ID:                "job-1",
			LockToken:         "old-token",
			Error:             "stale failure",
			FinishedAt:        reclaimAt,
			NextRunAt:         nil,
			IncrementAttempts: true,
		})
		if err != nil {
			t.Fatalf("stale Finish returned error: %v", err)
		}
		if ok {
			t.Fatal("expected stale Finish to lose")
		}

		nextRun := reclaimAt.Add(time.Hour)
		ok, err = store.Finish(ctx, tasks.Finish{
			ID:            "job-1",
			LockToken:     "new-token",
			FinishedAt:    reclaimAt.Add(time.Second),
			NextRunAt:     &nextRun,
			ResetAttempts: true,
		})
		if err != nil {
			t.Fatalf("fresh Finish returned error: %v", err)
		}
		if !ok {
			t.Fatal("expected fresh Finish to win")
		}

		got, err := store.Get(ctx, "job-1")
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if got.Attempts != 0 || got.LastError != "" {
			t.Fatalf("unexpected task after fresh finish: %+v", got)
		}
		assertTimePtr(t, got.NextRunAt, nextRun)
	})

	t.Run("finish persists retry and dead states", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		retry := now.Add(30 * time.Second)

		job := task("job-1", now)
		job.MaxAttempts = 2
		mustEnqueue(t, store, job)
		claimed, err := store.ClaimDue(ctx, now, time.Minute, 0, "token-1")
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDue returned claimed=%+v err=%v", claimed, err)
		}
		ok, err := store.Finish(ctx, tasks.Finish{
			ID:                "job-1",
			LockToken:         "token-1",
			Error:             "try again",
			FinishedAt:        now,
			NextRunAt:         &retry,
			IncrementAttempts: true,
		})
		if err != nil || !ok {
			t.Fatalf("Finish retry returned ok=%v err=%v", ok, err)
		}
		got, err := store.Get(ctx, "job-1")
		if err != nil {
			t.Fatalf("Get after retry returned error: %v", err)
		}
		if got.Attempts != 1 || got.LastError != "try again" || got.LockToken != "" {
			t.Fatalf("unexpected retry state: %+v", got)
		}
		assertTimePtr(t, got.NextRunAt, retry)

		claimed, err = store.ClaimDue(ctx, retry, time.Minute, 0, "token-2")
		if err != nil || len(claimed) != 1 {
			t.Fatalf("retry ClaimDue returned claimed=%+v err=%v", claimed, err)
		}
		ok, err = store.Finish(ctx, tasks.Finish{
			ID:                "job-1",
			LockToken:         "token-2",
			Error:             "poison",
			FinishedAt:        retry,
			IncrementAttempts: true,
		})
		if err != nil || !ok {
			t.Fatalf("Finish dead returned ok=%v err=%v", ok, err)
		}
		got, err = store.Get(ctx, "job-1")
		if err != nil {
			t.Fatalf("Get after dead returned error: %v", err)
		}
		if !got.Exhausted() || got.NextRunAt != nil || got.LastError != "poison" {
			t.Fatalf("unexpected dead state: %+v", got)
		}
	})

	t.Run("reschedule clears lock attempts and stale finish", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()
		next := now.Add(time.Hour)

		mustEnqueue(t, store, task("job-1", now))
		if claimed, err := store.ClaimDue(ctx, now, time.Minute, 0, "token"); err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDue returned claimed=%+v err=%v", claimed, err)
		}
		got, ok, err := store.Reschedule(ctx, "job-1", tasks.Once(next), next, now.Add(time.Second))
		if err != nil || !ok {
			t.Fatalf("Reschedule returned task=%+v ok=%v err=%v", got, ok, err)
		}
		if got.LockToken != "" || got.LockedAt != nil || got.Attempts != 0 {
			t.Fatalf("unexpected rescheduled task: %+v", got)
		}
		assertTimePtr(t, got.NextRunAt, next)

		ok, err = store.Finish(ctx, tasks.Finish{
			ID:         "job-1",
			LockToken:  "token",
			FinishedAt: now.Add(2 * time.Second),
		})
		if err != nil {
			t.Fatalf("stale Finish returned error: %v", err)
		}
		if ok {
			t.Fatal("expected stale Finish after reschedule to lose")
		}
	})

	t.Run("delete claimed is token fenced", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()

		mustEnqueue(t, store, task("job-1", now))
		if claimed, err := store.ClaimDue(ctx, now, time.Minute, 0, "token"); err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimDue returned claimed=%+v err=%v", claimed, err)
		}

		ok, err := store.DeleteClaimed(ctx, "job-1", "stale-token")
		if err != nil {
			t.Fatalf("stale DeleteClaimed returned error: %v", err)
		}
		if ok {
			t.Fatal("expected stale DeleteClaimed to lose")
		}

		ok, err = store.DeleteClaimed(ctx, "job-1", "token")
		if err != nil {
			t.Fatalf("fresh DeleteClaimed returned error: %v", err)
		}
		if !ok {
			t.Fatal("expected fresh DeleteClaimed to win")
		}
		_, err = store.Get(ctx, "job-1")
		if !errors.Is(err, tasks.ErrNotFound) {
			t.Fatalf("expected ErrNotFound after DeleteClaimed, got %v", err)
		}
	})

	t.Run("delete removes task", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		now := testTime()

		mustEnqueue(t, store, task("job-1", now))
		ok, err := store.Delete(ctx, "job-1")
		if err != nil || !ok {
			t.Fatalf("Delete returned ok=%v err=%v", ok, err)
		}
		_, err = store.Get(ctx, "job-1")
		if !errors.Is(err, tasks.ErrNotFound) {
			t.Fatalf("expected ErrNotFound after delete, got %v", err)
		}
	})
}

func task(id string, nextRunAt time.Time) tasks.Task {
	now := testTime()
	next := nextRunAt.UTC()
	return tasks.Task{
		ID:          id,
		Kind:        "test.task",
		Payload:     []byte("payload:" + id),
		Schedule:    tasks.Once(next),
		Group:       "group:" + id,
		Timeout:     time.Second,
		MaxAttempts: 3,
		Metadata:    map[string]string{"source": "test"},
		NextRunAt:   &next,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func mustEnqueue(t *testing.T, store tasks.Store, task tasks.Task) {
	t.Helper()
	if err := store.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("Enqueue(%s) returned error: %v", task.ID, err)
	}
}

func assertTaskIDs(t *testing.T, got []tasks.Task, want []string) {
	t.Helper()
	ids := make([]string, 0, len(got))
	for _, task := range got {
		ids = append(ids, task.ID)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("expected task IDs %v, got %v", want, ids)
	}
}

func assertTimePtr(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil {
		t.Fatalf("expected time %s, got nil", want)
	}
	if !got.Equal(want) {
		t.Fatalf("expected time %s, got %s", want, *got)
	}
}

func testTime() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}
