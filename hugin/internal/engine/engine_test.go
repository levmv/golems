package engine

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/tasks"
)

func TestSyncCheckTasksReconcilesConfigWithTaskQueue(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	cfg := testConfig()
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))
	store, err := db.TaskStore()
	if err != nil {
		t.Fatalf("TaskStore returned error: %v", err)
	}
	countingStore := &countingTaskStore{
		Store:        store,
		deleteCounts: make(map[string]int),
	}
	q := mustTaskQueue(t, countingStore)

	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("syncCheckTasks returned error: %v", err)
	}
	taskID := checkTaskID("disk")
	task, err := store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if task.Kind != checkJobKind || task.Group != "web" || task.Schedule.CronExpr != "*/5 * * * *" {
		t.Fatalf("unexpected task after first sync: %+v", task)
	}
	createdAt := task.CreatedAt

	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("second syncCheckTasks returned error: %v", err)
	}
	task, err = store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("second Get returned error: %v", err)
	}
	if !task.CreatedAt.Equal(createdAt) {
		t.Fatalf("unchanged sync should preserve existing task, created_at changed from %s to %s", createdAt, task.CreatedAt)
	}

	if ok, err := q.Delete(ctx, taskID); err != nil || !ok {
		t.Fatalf("Delete existing task returned ok=%v err=%v", ok, err)
	}
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("repair syncCheckTasks returned error: %v", err)
	}
	task, err = store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("repair Get returned error: %v", err)
	}
	if task.Kind != checkJobKind {
		t.Fatalf("expected missing task to be repaired, got %+v", task)
	}

	countingStore.resetDeleteCounts()
	cfg.Checks[0].Schedule = "*/10 * * * *"
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("changed syncCheckTasks returned error: %v", err)
	}
	if got := countingStore.deleteCount(taskID); got != 1 {
		t.Fatalf("changed sync should delete deterministic task ID once, got %d deletes", got)
	}
	task, err = store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("changed Get returned error: %v", err)
	}
	if task.Schedule.CronExpr != "*/10 * * * *" {
		t.Fatalf("expected updated schedule, got %+v", task.Schedule)
	}

	if task.Timeout != 0 {
		t.Fatalf("scheduled task timeout = %s, want no task-level timeout", task.Timeout)
	}

	countingStore.resetDeleteCounts()
	cfg.Checks[0].Timeout = 2 * time.Second
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("timeout syncCheckTasks returned error: %v", err)
	}
	if got := countingStore.deleteCount(taskID); got != 0 {
		t.Fatalf("timeout change should not replace scheduled task, got %d deletes", got)
	}
	task, err = store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("timeout Get returned error: %v", err)
	}
	if task.Timeout != 0 {
		t.Fatalf("scheduled task timeout = %s, want no task-level timeout", task.Timeout)
	}

	countingStore.resetDeleteCounts()
	cfg.Targets["db"] = config.Target{Host: "db.example.test"}
	cfg.Checks[0].Target = "db"
	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("target syncCheckTasks returned error: %v", err)
	}
	if got := countingStore.deleteCount(taskID); got != 1 {
		t.Fatalf("target change should delete deterministic task ID once, got %d deletes", got)
	}
	task, err = store.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("target Get returned error: %v", err)
	}
	if task.Group != "db" {
		t.Fatalf("expected updated target group, got %q", task.Group)
	}
}

func TestRunDueDiscardsRemovedCheckTask(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	cfg := testConfig()
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))
	store, err := db.TaskStore()
	if err != nil {
		t.Fatalf("TaskStore returned error: %v", err)
	}
	q := mustTaskQueue(t, store)

	if err := eng.syncCheckTasks(ctx, q); err != nil {
		t.Fatalf("syncCheckTasks returned error: %v", err)
	}
	taskID := checkTaskID("disk")
	if _, err := store.Get(ctx, taskID); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	cfg.Checks = nil

	if err := eng.RunDue(ctx); err != nil {
		t.Fatalf("RunDue returned error: %v", err)
	}
	_, err = store.Get(ctx, taskID)
	if !errors.Is(err, tasks.ErrNotFound) {
		t.Fatalf("expected removed check task to be discarded, got %v", err)
	}
}

func TestNewCachesAnalysisModelConfiguration(t *testing.T) {
	const tokenEnv = "HUGIN_TEST_LLM_TOKEN"
	t.Setenv(tokenEnv, "test-token")

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-test",
		APIKeyEnv: tokenEnv,
	}
	eng := New(cfg, nil, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))

	if err := os.Unsetenv(tokenEnv); err != nil {
		t.Fatalf("Unsetenv returned error: %v", err)
	}
	if _, err := eng.model(); err != nil {
		t.Fatalf("expected cached model after env was unset, got %v", err)
	}
}

func TestBuildModelUsesProviderAwareTokenFallback(t *testing.T) {
	clearEngineLLMEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai-token")

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "deepseek",
		Model:    "deepseek-test",
	}
	if _, err := buildModel(cfg); err == nil {
		t.Fatal("buildModel() error = nil, want missing deepseek token error")
	}

	t.Setenv("DEEPSEEK_API_KEY", "deepseek-token")
	if _, err := buildModel(cfg); err != nil {
		t.Fatalf("buildModel() with DEEPSEEK_API_KEY error = %v", err)
	}
}

func TestBuildModelAllowsOllamaWithoutToken(t *testing.T) {
	clearEngineLLMEnv(t)

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "ollama",
		Model:    "llama-test",
	}
	if _, err := buildModel(cfg); err != nil {
		t.Fatalf("buildModel() with ollama error = %v", err)
	}
}

func TestRunDueCancellationLeavesClaimedTaskUnfinished(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	started := filepath.Join(t.TempDir(), "started")
	cfg := testConfig()
	cfg.Targets["web"] = config.Target{Type: "local", Host: "localhost"}
	cfg.Checks[0].Command = "touch " + strconv.Quote(started) + "; exec sleep 5"
	cfg.Checks[0].Timeout = 30 * time.Second
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunDue(runCtx)
	}()

	waitForFile(t, started)
	cancel()
	if err := waitEngineErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	store, err := db.TaskStore()
	if err != nil {
		t.Fatalf("TaskStore returned error: %v", err)
	}
	task, err := store.Get(ctx, checkTaskID("disk"))
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if task.LockedAt == nil || task.LockToken == "" {
		t.Fatalf("expected canceled task to remain claimed until lease expiry, got %+v", task)
	}
	if task.Attempts != 0 || task.LastError != "" || task.NextRunAt == nil {
		t.Fatalf("expected cancellation to avoid failure/success state, got %+v", task)
	}
	runs, err := db.RecentRuns("disk", 20)
	if err != nil {
		t.Fatalf("RecentRuns returned error: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected canceled execution not to persist a run, got %+v", runs)
	}
}

func TestRunAnalysisCancellationDoesNotOpenAnalysisUnavailableIncident(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	cfg := testConfig()
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))
	output := &models.CollectorOutput{
		Check:   "disk",
		Status:  models.StatusOK,
		Metrics: map[string]any{"ok": true},
	}
	runID, err := db.InsertRun("disk", output, 10)
	if err != nil {
		t.Fatalf("InsertRun returned error: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancel()
	if err := eng.runAnalysis(runCtx, &cfg.Checks[0], "disk", runID, output); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got, err := db.RunAnalysis(runID); err != nil || got != nil {
		t.Fatalf("expected no analysis record on cancellation, got analysis=%+v err=%v", got, err)
	}
	if _, err := db.Incident(analysisIssueCheckID("disk")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected no analysis-unavailable incident, got %v", err)
	}
}

func TestRunDaemonReturnsNilOnCancellation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "hugin.db"))
	if err != nil {
		t.Fatalf("storage.New returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	})

	cfg := testConfig()
	cfg.Targets["web"] = config.Target{Type: "local", Host: "localhost"}
	cfg.Checks[0].Command = `printf '%s\n' '{"check":"disk","status":"ok","metrics":{"ok":true}}'`
	eng := New(cfg, db, logger.New(logger.Config{Out: io.Discard, Err: io.Discard}))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunDaemon(runCtx)
	}()

	waitForRuns(t, db, "disk", 1)
	cancel()
	if err := waitEngineErr(t, errCh); err != nil {
		t.Fatalf("expected graceful daemon cancellation to return nil, got %v", err)
	}
}

func testConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Timezone:            "UTC",
			MaxConcurrentChecks: 2,
		},
		Targets: map[string]config.Target{
			"web": {Host: "example.test"},
		},
		Checks: []config.Check{
			{
				ID:       "disk",
				Target:   "web",
				Command:  "true",
				Schedule: "*/5 * * * *",
				Timeout:  time.Second,
			},
		},
	}
}

func clearEngineLLMEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{"HUGIN_LLM_TOKEN", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(env, "")
	}
}

func waitForRuns(t *testing.T, db *storage.DB, checkID string, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		runs, err := db.RecentRuns(checkID, want)
		if err != nil {
			t.Fatalf("RecentRuns returned error: %v", err)
		}
		if len(runs) >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d run(s) for %s", want, checkID)
		case <-tick.C:
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}

func waitEngineErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for engine")
		return nil
	}
}

func mustTaskQueue(t *testing.T, store tasks.Store) *tasks.Queue {
	t.Helper()
	q, err := tasks.New(store, tasks.HandlerFunc(func(ctx context.Context, task tasks.Task) error {
		return nil
	}), tasks.Options{})
	if err != nil {
		t.Fatalf("tasks.New returned error: %v", err)
	}
	return q
}

type countingTaskStore struct {
	tasks.Store
	mu           sync.Mutex
	deleteCounts map[string]int
}

func (s *countingTaskStore) Delete(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	s.deleteCounts[id]++
	s.mu.Unlock()
	return s.Store.Delete(ctx, id)
}

func (s *countingTaskStore) resetDeleteCounts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.deleteCounts)
}

func (s *countingTaskStore) deleteCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCounts[id]
}
