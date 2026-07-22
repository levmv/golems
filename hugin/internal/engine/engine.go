package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/levmv/golems/hugin/internal/analysis"
	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/incidents"
	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/hugin/internal/notifier"
	"github.com/levmv/golems/hugin/internal/runner"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/tasks"
)

const checkJobKind = "hugin.check"
const analysisIssueSuffix = ":analysis"
const defaultAnalysisTimeout = 2 * time.Minute

type Engine struct {
	cfg               *config.Config
	db                *storage.DB
	log               logger.Logger
	analysisModel     llm.Model
	analysisModelErr  error
	analysisModelName string
	ntf               notifier.Notifier
}

// analysisPipelineError means collector output was already persisted, but a
// later analysis/incident step failed. Direct runs surface the error; scheduled
// runs deliberately advance to the next occurrence instead of executing the
// collector again.
type analysisPipelineError struct {
	runID int64
	cause error
}

func (e *analysisPipelineError) Error() string {
	return fmt.Sprintf("run %d analysis pipeline failed: %v", e.runID, e.cause)
}

func (e *analysisPipelineError) Unwrap() error {
	return e.cause
}

func New(cfg *config.Config, db *storage.DB, log logger.Logger) *Engine {
	model, modelErr := buildModel(cfg)
	ntf := notifier.FromConfig(cfg, log)
	return &Engine{
		cfg:               cfg,
		db:                db,
		log:               log,
		analysisModel:     model,
		analysisModelErr:  modelErr,
		analysisModelName: analysisModelName(cfg),
		ntf:               ntf,
	}
}

func (e *Engine) RunCheck(ctx context.Context, checkID string) error {
	execRunner := runner.New()
	defer execRunner.Close()
	return e.runCheck(ctx, checkID, execRunner)
}

func (e *Engine) runCheck(ctx context.Context, checkID string, execRunner *runner.Runner) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	check := e.cfg.FindCheck(checkID)
	if check == nil {
		return fmt.Errorf("check '%s' not found in configuration", checkID)
	}

	target, ok := e.cfg.Targets[check.Target]
	if !ok {
		return fmt.Errorf("check '%s' references unknown target '%s'", checkID, check.Target)
	}
	e.log.Info("Executing check '%s' on target '%s' (%s)", checkID, check.Target, target.Host)

	execCtx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()

	start := time.Now()
	output, execErr := execRunner.Execute(execCtx, *check, target)
	durationMs := time.Since(start).Milliseconds()

	if err := ctx.Err(); err != nil {
		e.log.Info("Check '%s' canceled during execution", checkID)
		return err
	}

	if execErr != nil {
		e.log.Error("Execution failed after %dms: %v", durationMs, execErr)
		for _, err := range output.Errors {
			e.log.Warn("  Collector Error [%s]: %s", err.Code, err.Message)
		}
	} else {
		e.log.Info("Execution successful in %dms. Status: %s", durationMs, output.Status)
		for k, v := range output.Metrics {
			e.log.Debug("  Metric %s: %v", k, v)
		}
	}

	runID, err := e.db.InsertRun(checkID, output, durationMs)
	if err != nil {
		return fmt.Errorf("failed to save run: %w", err)
	}
	e.log.Debug("Run saved (id=%d)", runID)

	if err := e.runAnalysis(ctx, check, checkID, runID, output); err != nil {
		e.log.Error("Analysis failed: %v", err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if markErr := e.db.MarkRunAnalysisPipelineFailed(runID, err); markErr != nil {
			e.log.Error("Failed to record analysis pipeline failure for run %d: %v", runID, markErr)
		}
		return &analysisPipelineError{runID: runID, cause: err}
	}
	if err := ctx.Err(); err != nil {
		e.log.Info("Check '%s' canceled after execution", checkID)
		return err
	}
	return nil
}

func (e *Engine) RunDue(ctx context.Context) error {
	scheduled, err := e.newScheduledCheckQueue(scheduledCheckQueueOptions{
		collectFailures: true,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := scheduled.Close(); err != nil {
			e.log.Warn("Failed to close scheduled runner: %v", err)
		}
	}()

	if err := e.syncCheckTasks(ctx, scheduled.queue); err != nil {
		return fmt.Errorf("failed to sync scheduled check tasks: %w", err)
	}

	if err := scheduled.queue.RunOnce(ctx); err != nil {
		return err
	}
	failures := scheduled.failuresSnapshot()
	if len(failures) == 0 {
		e.log.Info("Scheduled checks processed")
		return nil
	}
	var joined error
	for _, failure := range failures {
		joined = errors.Join(joined, failure.Err)
	}
	return fmt.Errorf("one or more scheduled checks failed: %w", joined)
}

func (e *Engine) RunDaemon(ctx context.Context) error {
	scheduled, err := e.newScheduledCheckQueue(scheduledCheckQueueOptions{
		onError: func(err error) {
			e.log.Error("Scheduled check loop error: %v", err)
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := scheduled.Close(); err != nil {
			e.log.Warn("Failed to close scheduled runner: %v", err)
		}
	}()

	if err := e.syncCheckTasks(ctx, scheduled.queue); err != nil {
		return fmt.Errorf("failed to sync scheduled check tasks: %w", err)
	}

	e.log.Info("Hugin daemon started")
	if err := scheduled.queue.RunLoop(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			e.log.Info("Hugin daemon stopped")
			return nil
		}
		return err
	}
	e.log.Info("Hugin daemon stopped")
	return nil
}

func (e *Engine) SyncSchedule(ctx context.Context) error {
	scheduled, err := e.newScheduledCheckQueue(scheduledCheckQueueOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err := scheduled.Close(); err != nil {
			e.log.Warn("Failed to close scheduled runner: %v", err)
		}
	}()

	return e.syncCheckTasks(ctx, scheduled.queue)
}

type scheduledCheckQueueOptions struct {
	collectFailures bool
	onError         func(error)
}

type scheduledCheckQueue struct {
	queue      *tasks.Queue
	execRunner *runner.Runner
	mu         sync.Mutex
	failures   []tasks.Failure
}

func (q *scheduledCheckQueue) Close() error {
	if q == nil || q.execRunner == nil {
		return nil
	}
	return q.execRunner.Close()
}

func (q *scheduledCheckQueue) addFailure(failure tasks.Failure) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failures = append(q.failures, failure)
}

func (q *scheduledCheckQueue) failuresSnapshot() []tasks.Failure {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]tasks.Failure(nil), q.failures...)
}

func (e *Engine) newScheduledCheckQueue(opts scheduledCheckQueueOptions) (*scheduledCheckQueue, error) {
	store, err := e.db.TaskStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize task store: %w", err)
	}

	scheduled := &scheduledCheckQueue{
		execRunner: runner.New(),
	}
	var handler tasks.Handler = tasks.HandlerFunc(func(ctx context.Context, task tasks.Task) error {
		if task.Kind != checkJobKind {
			return fmt.Errorf("unknown scheduled task kind: %s", task.Kind)
		}
		payload, err := tasks.DecodeJSON[checkTaskPayload](task)
		if err != nil {
			return fmt.Errorf("decode check task payload: %w", err)
		}
		if payload.CheckID == "" {
			return fmt.Errorf("check task payload is missing check_id")
		}
		if e.cfg.FindCheck(payload.CheckID) == nil {
			return tasks.Discardf("hugin check %q is no longer in configuration", payload.CheckID)
		}
		err = e.runCheck(ctx, payload.CheckID, scheduled.execRunner)
		var pipelineErr *analysisPipelineError
		if errors.As(err, &pipelineErr) {
			e.log.Error("Scheduled %v; advancing to the next occurrence without rerunning the collector", pipelineErr)
			return nil
		}
		return err
	})

	handler = tasks.Chain(handler, tasks.GroupConcurrency(1))
	q, err := tasks.New(store, handler, tasks.Options{
		MaxConcurrent: e.cfg.App.MaxConcurrentChecks,
		OnFailure: func(failure tasks.Failure) {
			if opts.collectFailures {
				scheduled.addFailure(failure)
			}
			if failure.Exhausted {
				e.log.Error("Scheduled check task %s exhausted attempts: %v", failure.Task.ID, failure.Err)
				return
			}
			e.log.Warn("Scheduled check task %s failed: %v", failure.Task.ID, failure.Err)
		},
		OnError: opts.onError,
	})
	if err != nil {
		_ = scheduled.Close()
		return nil, fmt.Errorf("failed to initialize task queue: %w", err)
	}
	scheduled.queue = q
	return scheduled, nil
}

type checkTaskPayload struct {
	CheckID string `json:"check_id"`
}

func (e *Engine) syncCheckTasks(ctx context.Context, q *tasks.Queue) error {
	desired := checkTasks(e.cfg, time.Now().UTC())
	for checkID, task := range desired {
		current, err := q.Get(ctx, task.ID)
		if err == nil {
			if scheduledCheckTaskMatches(current, task) {
				continue
			}
			if _, err := q.Delete(ctx, current.ID); err != nil {
				return fmt.Errorf("delete stale task for check %q: %w", checkID, err)
			}
		} else if !errors.Is(err, tasks.ErrNotFound) {
			return fmt.Errorf("load existing task for check %q: %w", checkID, err)
		}
		if _, err := q.Enqueue(ctx, task); err != nil {
			return fmt.Errorf("enqueue task for check %q: %w", checkID, err)
		}
	}
	return nil
}

func scheduledCheckTaskMatches(task tasks.Task, desired tasks.Enqueue) bool {
	if task.ID != desired.ID || task.Kind != desired.Kind || task.Group != desired.Group || task.Timeout != desired.Timeout {
		return false
	}
	payload, err := tasks.DecodeJSON[checkTaskPayload](task)
	if err != nil || payload.CheckID == "" {
		return false
	}
	desiredPayload, err := decodeCheckTaskPayload(desired.Payload)
	if err != nil || payload.CheckID != desiredPayload.CheckID {
		return false
	}
	return sameScheduledCheckSchedule(task.Schedule, desired.Schedule)
}

func decodeCheckTaskPayload(payload []byte) (checkTaskPayload, error) {
	task := tasks.Task{Payload: payload}
	return tasks.DecodeJSON[checkTaskPayload](task)
}

func sameScheduledCheckSchedule(current tasks.Schedule, desired tasks.Schedule) bool {
	if current.Kind != desired.Kind {
		return false
	}
	switch desired.Kind {
	case tasks.ScheduleCron:
		// CronFrom.Start is the first materialization seed; changing it during
		// sync should not replace an otherwise unchanged durable task.
		return current.CronExpr == desired.CronExpr && current.Timezone == desired.Timezone
	case tasks.ScheduleEvery:
		return current.Interval == desired.Interval
	case tasks.ScheduleOnce:
		return current.At.Equal(desired.At)
	default:
		return false
	}
}

func checkTasks(cfg *config.Config, seedAt time.Time) map[string]tasks.Enqueue {
	out := make(map[string]tasks.Enqueue, len(cfg.Checks))
	for _, check := range cfg.Checks {
		taskID := CheckTaskID(check.ID)
		payload, _ := tasks.JSONPayload(checkTaskPayload{CheckID: check.ID})
		out[check.ID] = tasks.Enqueue{
			ID:       taskID,
			Kind:     checkJobKind,
			Payload:  payload,
			Group:    check.Target,
			Schedule: tasks.CronFrom(check.Schedule, cfg.App.Timezone, seedAt),
			Metadata: map[string]string{
				"check_id": check.ID,
				"target":   check.Target,
				"schedule": check.Schedule,
				"timezone": cfg.App.Timezone,
			},
		}
	}
	return out
}

func CheckTaskID(checkID string) string {
	return "hugin.check:" + checkID
}

func (e *Engine) runAnalysis(ctx context.Context, check *config.Check, checkID string, runID int64, output *models.CollectorOutput) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	historyWindow := parseHistoryWindow(check)
	history, err := e.db.RunsSinceExcluding(checkID, time.Now().Add(-historyWindow), runID, e.cfg.LLM.MaxInputRuns)
	if err != nil {
		return fmt.Errorf("failed to fetch history: %w", err)
	}
	e.log.Debug("Fetched %d historical runs for analysis", len(history))

	notes, err := e.db.Notes(checkID)
	if err != nil {
		e.log.Warn("Failed to fetch notes: %v", err)
		notes = nil
	}

	incident, err := e.db.ActiveIncident(checkID)
	if err != nil {
		e.log.Warn("Failed to fetch active incident: %v", err)
		incident = nil
	}

	model, err := e.model()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		e.log.Warn("LLM configuration failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(ctx, check, checkID, runID, e.analysisModelName, err)
	}

	analyzer := analysis.New(model, e.log)
	target := e.cfg.Targets[check.Target]
	analysisCtx, cancel := context.WithTimeout(ctx, defaultAnalysisTimeout)
	defer cancel()

	result, err := analyzer.Analyze(analysisCtx, analysis.Input{
		CheckID:        checkID,
		Current:        output,
		History:        history,
		TargetContext:  target.Context,
		CheckContext:   check.Context,
		Notes:          notes,
		ActiveIncident: incident,
		HistoryWindow:  historyWindow,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		e.log.Warn("LLM analysis failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(ctx, check, checkID, runID, e.analysisModelName, err)
	}
	if err := e.recordRunAnalysis(runID, e.analysisModelName, result, nil); err != nil {
		return err
	}
	if err := e.resolveAnalysisUnavailable(ctx, check, checkID, runID); err != nil {
		return err
	}

	e.log.Info("Analysis: severity=%s should_alert=%v summary=%s", result.Severity, result.ShouldAlert, result.Summary)
	e.log.Debug("Evidence: %s", result.Evidence)

	return e.processIncident(ctx, check, checkID, result, runID)
}

func (e *Engine) processAnalysisUnavailable(ctx context.Context, check *config.Check, checkID string, runID int64, modelName string, cause error) error {
	result := &analysis.Result{
		Severity:    analysis.SeverityUrgent,
		ShouldAlert: true,
		Summary:     fmt.Sprintf("%s AI analysis unavailable", checkID),
		Evidence:    fmt.Sprintf("AI analysis failed for check %q: %v", checkID, cause),
	}
	if err := e.recordRunAnalysis(runID, modelName, result, cause); err != nil {
		return err
	}

	e.log.Info("Analysis: severity=%s should_alert=%v summary=%s", result.Severity, result.ShouldAlert, result.Summary)
	e.log.Debug("Evidence: %s", result.Evidence)

	return e.processIncident(ctx, check, analysisIssueCheckID(checkID), result, runID)
}

func (e *Engine) recordRunAnalysis(runID int64, modelName string, result *analysis.Result, cause error) error {
	record := storage.RunAnalysis{
		Severity:    string(result.Severity),
		ShouldAlert: result.ShouldAlert,
		Summary:     result.Summary,
		Evidence:    result.Evidence,
		Model:       modelName,
		CreatedAt:   time.Now().UTC(),
	}
	if cause != nil {
		record.Error = cause.Error()
	}
	if err := e.db.UpdateRunAnalysis(runID, record); err != nil {
		return fmt.Errorf("failed to save analysis result: %w", err)
	}
	return nil
}

func (e *Engine) resolveAnalysisUnavailable(ctx context.Context, check *config.Check, checkID string, runID int64) error {
	analysisCheckID := analysisIssueCheckID(checkID)
	active, err := e.db.ActiveIncident(analysisCheckID)
	if err != nil {
		return fmt.Errorf("failed to check active analysis incident: %w", err)
	}
	if active == nil {
		return nil
	}
	result := &analysis.Result{
		Severity:    analysis.SeverityNormal,
		ShouldAlert: false,
		Summary:     fmt.Sprintf("%s AI analysis recovered", checkID),
		Evidence:    "AI analysis completed successfully on this run.",
	}
	return e.processIncident(ctx, check, analysisCheckID, result, runID)
}

func (e *Engine) processIncident(ctx context.Context, check *config.Check, checkID string, result *analysis.Result, runID int64) error {
	im := incidents.New(e.db)
	event, err := im.Process(checkID, result, runID, check.Alert.Cooldown, check.Alert.RepeatAfter)
	if err != nil {
		return fmt.Errorf("incident processing failed: %w", err)
	}

	if event.Type != incidents.EventNone {
		if event.Type == incidents.EventResolved && !check.Alert.NotifyOnResolved {
			e.log.Debug("Resolution notification suppressed by check '%s' config", checkID)
			return nil
		}
		if err := notifyEvent(ctx, e.ntf, event); err != nil {
			e.log.Error("Failed to send notification: %v", err)
			return nil
		}
		if err := e.db.MarkIncidentNotified(event.Incident.ID, time.Now().UTC()); err != nil {
			e.log.Error("Failed to update notification state: %v", err)
		}
	}

	return nil
}

func analysisIssueCheckID(checkID string) string {
	return checkID + analysisIssueSuffix
}

func (e *Engine) model() (llm.Model, error) {
	if e.analysisModelErr != nil {
		return llm.Model{}, e.analysisModelErr
	}
	return e.analysisModel, nil
}

func buildModel(cfg *config.Config) (llm.Model, error) {
	provider := cfg.LLM.Provider
	token := ""
	for _, env := range config.LLMTokenEnvCandidates(cfg.LLM) {
		if value := os.Getenv(env); value != "" {
			token = value
			break
		}
	}
	if token == "" && config.LLMProviderNeedsToken(provider) {
		return llm.Model{}, fmt.Errorf("no LLM API token found for provider %q. Set one of: %s", provider, strings.Join(config.LLMTokenEnvCandidates(cfg.LLM), ", "))
	}

	reg := llm.NewRegistry().WithProvider(provider, token)
	m, err := reg.Model(provider + "/" + cfg.LLM.Model)
	if err != nil {
		return llm.Model{}, err
	}

	if cfg.LLM.Temperature > 0 {
		m = m.WithTemperature(cfg.LLM.Temperature)
	}
	return m, nil
}

func analysisModelName(cfg *config.Config) string {
	if cfg.LLM.Provider == "" && cfg.LLM.Model == "" {
		return ""
	}
	return cfg.LLM.Provider + "/" + cfg.LLM.Model
}

func parseHistoryWindow(check *config.Check) time.Duration {
	if check == nil {
		return 7 * 24 * time.Hour
	}
	return parseDuration(check.Analysis.History, 7*24*time.Hour)
}

func parseDuration(s string, defaultDur time.Duration) time.Duration {
	if s == "" {
		return defaultDur
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return defaultDur
}

func notifyEvent(ctx context.Context, ntf notifier.Notifier, event incidents.Event) error {
	inc := *event.Incident
	switch event.Type {
	case incidents.EventCreated:
		return ntf.NotifyCreated(ctx, inc)
	case incidents.EventUpdated:
		return ntf.NotifyUpdated(ctx, inc)
	case incidents.EventResolved:
		return ntf.NotifyResolved(ctx, inc)
	default:
		return nil
	}
}
