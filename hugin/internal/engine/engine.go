package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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

type Engine struct {
	cfg *config.Config
	db  *storage.DB
	log logger.Logger
}

func New(cfg *config.Config, db *storage.DB, log logger.Logger) *Engine {
	return &Engine{cfg: cfg, db: db, log: log}
}

func (e *Engine) RunCheck(ctx context.Context, checkID string) error {
	execRunner := runner.New()
	defer execRunner.Close()
	return e.runCheck(ctx, checkID, execRunner)
}

func (e *Engine) runCheck(ctx context.Context, checkID string, execRunner *runner.Runner) error {
	check := e.cfg.FindCheck(checkID)
	if check == nil {
		return fmt.Errorf("check '%s' not found in configuration", checkID)
	}

	target, ok := e.cfg.Targets[check.Target]
	if !ok {
		return fmt.Errorf("check '%s' references unknown target '%s'", checkID, check.Target)
	}
	e.log.Info("Executing check '%s' on target '%s' (%s)", checkID, check.Target, target.Host)

	ctx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()

	start := time.Now()
	output, execErr := execRunner.Execute(ctx, *check, target)
	durationMs := time.Since(start).Milliseconds()

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

	if err := e.runAnalysis(check, checkID, runID, output); err != nil {
		e.log.Error("Analysis failed: %v", err)
	}
	return nil
}

func (e *Engine) RunDue(ctx context.Context) error {
	store, err := e.db.TaskStore()
	if err != nil {
		return fmt.Errorf("failed to initialize task store: %w", err)
	}

	execRunner := runner.New()
	defer execRunner.Close()

	var failures []tasks.Failure
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
		return e.runCheck(ctx, payload.CheckID, execRunner)
	})

	handler = tasks.Chain(handler, tasks.GroupConcurrency(1))
	q, err := tasks.New(store, handler, tasks.Options{
		MaxConcurrent: e.cfg.App.MaxConcurrentChecks,
		OnFailure: func(failure tasks.Failure) {
			failures = append(failures, failure)
			if failure.Exhausted {
				e.log.Error("Scheduled check task %s exhausted attempts: %v", failure.Task.ID, failure.Err)
				return
			}
			e.log.Warn("Scheduled check task %s failed: %v", failure.Task.ID, failure.Err)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to initialize task queue: %w", err)
	}

	if err := e.syncCheckTasks(ctx, q); err != nil {
		return fmt.Errorf("failed to sync scheduled check tasks: %w", err)
	}

	if err := q.RunOnce(ctx); err != nil {
		return err
	}
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

type checkTaskPayload struct {
	CheckID string `json:"check_id"`
}

type desiredCheckTask struct {
	ref     storage.CheckTaskRef
	enqueue tasks.Enqueue
}

func (e *Engine) syncCheckTasks(ctx context.Context, q *tasks.Queue) error {
	refs, err := e.db.CheckTaskRefs(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]storage.CheckTaskRef, len(refs))
	for _, ref := range refs {
		existing[ref.CheckID] = ref
	}

	desired := checkTasks(e.cfg, time.Now().UTC())
	for checkID, task := range desired {
		current, ok := existing[checkID]
		if ok && current.Fingerprint == task.ref.Fingerprint {
			if _, err := q.Get(ctx, current.TaskID); err == nil {
				continue
			} else if !errors.Is(err, tasks.ErrNotFound) {
				return fmt.Errorf("load existing task for check %q: %w", checkID, err)
			}
		}
		if ok {
			if _, err := q.Delete(ctx, current.TaskID); err != nil {
				return fmt.Errorf("delete stale task for check %q: %w", checkID, err)
			}
		}
		if _, err := q.Delete(ctx, task.ref.TaskID); err != nil {
			return fmt.Errorf("delete duplicate task for check %q: %w", checkID, err)
		}
		if _, err := q.Enqueue(ctx, task.enqueue); err != nil {
			return fmt.Errorf("enqueue task for check %q: %w", checkID, err)
		}
		if err := e.db.UpsertCheckTaskRef(ctx, task.ref); err != nil {
			return fmt.Errorf("record task ref for check %q: %w", checkID, err)
		}
	}

	for _, ref := range refs {
		if _, ok := desired[ref.CheckID]; ok {
			continue
		}
		if _, err := q.Delete(ctx, ref.TaskID); err != nil {
			return fmt.Errorf("delete task for removed check %q: %w", ref.CheckID, err)
		}
		if err := e.db.DeleteCheckTaskRef(ctx, ref.CheckID); err != nil {
			return fmt.Errorf("delete task ref for removed check %q: %w", ref.CheckID, err)
		}
	}
	return nil
}

func checkTasks(cfg *config.Config, seedAt time.Time) map[string]desiredCheckTask {
	out := make(map[string]desiredCheckTask, len(cfg.Checks))
	for _, check := range cfg.Checks {
		taskID := checkTaskID(check.ID)
		payload, _ := tasks.JSONPayload(checkTaskPayload{CheckID: check.ID})
		fingerprint := checkTaskFingerprint(check, cfg.App.Timezone)
		out[check.ID] = desiredCheckTask{
			ref: storage.CheckTaskRef{
				CheckID:     check.ID,
				TaskID:      taskID,
				Fingerprint: fingerprint,
			},
			enqueue: tasks.Enqueue{
				ID:       taskID,
				Kind:     checkJobKind,
				Payload:  payload,
				Group:    check.Target,
				Schedule: tasks.CronFrom(check.Schedule, cfg.App.Timezone, seedAt),
				Timeout:  check.Timeout,
				Metadata: map[string]string{
					"check_id":    check.ID,
					"target":      check.Target,
					"fingerprint": fingerprint,
				},
			},
		}
	}
	return out
}

func checkTaskID(checkID string) string {
	return "hugin.check:" + checkID
}

func checkTaskFingerprint(check config.Check, timezone string) string {
	data, _ := json.Marshal(struct {
		CheckID   string `json:"check_id"`
		Target    string `json:"target"`
		Schedule  string `json:"schedule"`
		Timezone  string `json:"timezone"`
		TimeoutNS int64  `json:"timeout_ns"`
	}{
		CheckID:   check.ID,
		Target:    check.Target,
		Schedule:  check.Schedule,
		Timezone:  timezone,
		TimeoutNS: int64(check.Timeout),
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) runAnalysis(check *config.Check, checkID string, runID int64, output *models.CollectorOutput) error {
	historyWindow := parseHistoryWindow(check)
	history, err := e.db.RunsSince(checkID, time.Now().Add(-historyWindow), e.cfg.LLM.MaxInputRuns)
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

	modelName := e.analysisModelName()
	model, err := e.buildModel()
	if err != nil {
		e.log.Warn("LLM configuration failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(check, checkID, runID, modelName, err)
	}

	analyzer := analysis.New(model, e.log)
	target := e.cfg.Targets[check.Target]
	result, err := analyzer.Analyze(context.Background(), analysis.Input{
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
		e.log.Warn("LLM analysis failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(check, checkID, runID, modelName, err)
	}
	if err := e.recordRunAnalysis(runID, modelName, result, nil); err != nil {
		return err
	}
	if err := e.resolveAnalysisUnavailable(check, checkID, runID); err != nil {
		return err
	}

	e.log.Info("Analysis: severity=%s should_alert=%v summary=%s", result.Severity, result.ShouldAlert, result.Summary)
	e.log.Debug("Evidence: %s", result.Evidence)

	return e.processIncident(check, checkID, result, runID)
}

func (e *Engine) processAnalysisUnavailable(check *config.Check, checkID string, runID int64, modelName string, cause error) error {
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

	return e.processIncident(check, analysisIssueCheckID(checkID), result, runID)
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

func (e *Engine) resolveAnalysisUnavailable(check *config.Check, checkID string, runID int64) error {
	result := &analysis.Result{
		Severity:    analysis.SeverityNormal,
		ShouldAlert: false,
		Summary:     fmt.Sprintf("%s AI analysis recovered", checkID),
		Evidence:    "AI analysis completed successfully on this run.",
	}
	return e.processIncident(check, analysisIssueCheckID(checkID), result, runID)
}

func (e *Engine) processIncident(check *config.Check, checkID string, result *analysis.Result, runID int64) error {
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
		ntf := notifier.FromConfig(e.cfg, e.log)
		if err := notifyEvent(ntf, event); err != nil {
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

func (e *Engine) buildModel() (llm.Model, error) {
	provider := e.cfg.LLM.Provider
	token := ""
	if e.cfg.LLM.APIKeyEnv != "" {
		token = os.Getenv(e.cfg.LLM.APIKeyEnv)
	}
	if token == "" {
		token = os.Getenv("HUGIN_LLM_TOKEN")
	}
	if token == "" && provider == "deepseek" {
		token = os.Getenv("DEEPSEEK_API_KEY")
	}
	if token == "" {
		token = os.Getenv("OPENAI_API_KEY")
	}
	if token == "" {
		return llm.Model{}, fmt.Errorf("no LLM API token found. Set HUGIN_LLM_TOKEN, DEEPSEEK_API_KEY, or OPENAI_API_KEY")
	}

	reg := llm.NewRegistry().WithProvider(provider, token)
	m, err := reg.Model(provider + "/" + e.cfg.LLM.Model)
	if err != nil {
		return llm.Model{}, err
	}

	if e.cfg.LLM.Temperature > 0 {
		m = m.WithTemperature(e.cfg.LLM.Temperature)
	}
	return m, nil
}

func (e *Engine) analysisModelName() string {
	if e.cfg.LLM.Provider == "" && e.cfg.LLM.Model == "" {
		return ""
	}
	return e.cfg.LLM.Provider + "/" + e.cfg.LLM.Model
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

func notifyEvent(ntf notifier.Notifier, event incidents.Event) error {
	ctx := context.Background()
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
