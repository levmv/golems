package engine

import (
	"context"
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
	jobschedule "github.com/levmv/golems/pkg/schedule"
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
	output, execErr := runner.Execute(ctx, *check, target)
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

	if check.Analysis.Mode != "ai" {
		e.log.Debug("Analysis mode is '%s', skipping AI analysis", check.Analysis.Mode)
		return nil
	}

	if err := e.runAnalysis(check, checkID, runID, output); err != nil {
		e.log.Error("Analysis failed: %v", err)
	}
	return nil
}

func (e *Engine) RunDue(ctx context.Context) error {
	var runner jobschedule.Runner = jobschedule.RunnerFunc(func(ctx context.Context, job jobschedule.Job) error {
		if job.Spec.Kind != checkJobKind {
			return fmt.Errorf("unknown scheduled job kind: %s", job.Spec.Kind)
		}
		return e.RunCheck(ctx, job.Spec.Ref)
	})
	runner = jobschedule.Chain(runner, jobschedule.GroupConcurrency(1))
	s := jobschedule.New(e.db, runner, jobschedule.Options{
		MaxConcurrent: e.cfg.App.MaxConcurrentChecks,
	})

	jobs, err := s.Due(ctx, jobSpecs(e.cfg))
	if err != nil {
		return fmt.Errorf("failed to determine due checks: %w", err)
	}

	if len(jobs) == 0 {
		e.log.Info("No checks are due")
		return nil
	}

	e.log.Info("Running %d due check(s)", len(jobs))
	if err := s.RunJobs(ctx, jobs); err != nil {
		return fmt.Errorf("one or more scheduled checks failed: %w", err)
	}
	return nil
}

func jobSpecs(cfg *config.Config) []jobschedule.JobSpec {
	specs := make([]jobschedule.JobSpec, 0, len(cfg.Checks))
	for _, check := range cfg.Checks {
		specs = append(specs, jobschedule.JobSpec{
			ID:         check.ID,
			Kind:       checkJobKind,
			Ref:        check.ID,
			Group:      check.Target,
			Trigger:    jobschedule.Cron(check.Schedule),
			Timezone:   cfg.App.Timezone,
			Timeout:    check.Timeout,
			InitialRun: true,
			Metadata: map[string]string{
				"target": check.Target,
			},
		})
	}
	return specs
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

	model, err := e.buildModel()
	if err != nil {
		e.log.Warn("LLM configuration failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(check, checkID, runID, err)
	}

	analyzer := analysis.New(model, e.log)
	result, err := analyzer.Analyze(context.Background(), analysis.Input{
		CheckID:        checkID,
		Current:        output,
		History:        history,
		Notes:          notes,
		ActiveIncident: incident,
		IncludeHistory: historyWindow,
	})
	if err != nil {
		e.log.Warn("LLM analysis failed; opening analysis incident: %v", err)
		return e.processAnalysisUnavailable(check, checkID, runID, err)
	}
	if err := e.resolveAnalysisUnavailable(check, checkID, runID); err != nil {
		return err
	}

	e.log.Info("Analysis: severity=%s should_alert=%v summary=%s", result.Severity, result.ShouldAlert, result.Summary)
	e.log.Debug("Evidence: %s", result.Evidence)

	return e.processIncident(check, checkID, result, runID)
}

func (e *Engine) processAnalysisUnavailable(check *config.Check, checkID string, runID int64, cause error) error {
	result := &analysis.Result{
		Severity:    analysis.SeverityUrgent,
		ShouldAlert: true,
		Summary:     fmt.Sprintf("%s AI analysis unavailable", checkID),
		Evidence:    fmt.Sprintf("AI analysis failed for check %q: %v", checkID, cause),
	}

	e.log.Info("Analysis: severity=%s should_alert=%v summary=%s", result.Severity, result.ShouldAlert, result.Summary)
	e.log.Debug("Evidence: %s", result.Evidence)

	return e.processIncident(check, analysisIssueCheckID(checkID), result, runID)
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
		}
	}

	return nil
}

func analysisIssueCheckID(checkID string) string {
	return checkID + analysisIssueSuffix
}

func (e *Engine) buildModel() (llm.Model, error) {
	provider := e.cfg.LLM.Provider
	token := os.Getenv("HUGIN_LLM_TOKEN")
	if provider == "deepseek" {
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

func parseHistoryWindow(check *config.Check) time.Duration {
	if check == nil {
		return 7 * 24 * time.Hour
	}
	return parseDuration(check.Analysis.IncludeHistory, 7*24*time.Hour)
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
