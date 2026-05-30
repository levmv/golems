package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/deploy"
	"github.com/levmv/golems/hugin/internal/doctor"
	"github.com/levmv/golems/hugin/internal/engine"
	"github.com/levmv/golems/hugin/internal/notifier"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/tasks"
)

func main() {
	log := logger.Default()

	configPath := flag.String("config", "hugin.yaml", "Path to configuration file")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	if *debug {
		log = logger.New(logger.Config{Level: logger.LevelDebug})
	}

	args := flag.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.App.DataDir, 0755); err != nil {
		log.Error("Failed to create data directory: %v", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(cfg.App.DataDir, "hugin.db")
	db, err := storage.New(dbPath)
	if err != nil {
		log.Error("Failed to initialize database: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	eng := engine.New(cfg, db, log)

	switch command {
	case "run":
		if len(args) < 2 {
			log.Error("Usage: hugin run <check_id>")
			os.Exit(1)
		}
		handleRun(eng, args[1], log)

	case "note":
		if len(args) < 3 {
			log.Error("Usage: hugin note <check_id> <content>")
			os.Exit(1)
		}
		handleNote(db, args[1], strings.Join(args[2:], " "), log)

	case "runs":
		checkID, limit, err := parseRunsArgs(args[1:])
		if err != nil {
			log.Error("Usage: hugin runs <check_id> [--last N]: %v", err)
			os.Exit(1)
		}
		handleRuns(db, checkID, limit, log)

	case "resolve":
		if len(args) < 2 {
			log.Error("Usage: hugin resolve <incident_id> [--note <msg>]")
			os.Exit(1)
		}
		note, err := parseResolveNote(args[2:])
		if err != nil {
			log.Error("Usage: hugin resolve <incident_id> [--note <msg>]: %v", err)
			os.Exit(1)
		}
		handleResolve(cfg, db, args[1], note, log)

	case "run-due":
		handleRunDue(eng, log)

	case "daemon":
		handleDaemon(eng, log)

	case "status":
		handleStatus(cfg, db, eng, log)

	case "deploy":
		targetName, opts, err := parseDeployArgs(args[1:])
		if err != nil {
			log.Error("Usage: hugin deploy <target> [--source PATH] [--dest PATH]: %v", err)
			os.Exit(1)
		}
		handleDeploy(cfg, targetName, opts, log)

	case "doctor":
		opts, err := parseDoctorArgs(args[1:])
		if err != nil {
			log.Error("Usage: hugin doctor [--no-ssh] [--ssh-timeout DURATION]: %v", err)
			os.Exit(1)
		}
		handleDoctor(cfg, opts)

	case "validate":
		handleValidate(cfg, log)

	case "cleanup":
		handleCleanup(db, log)

	default:
		log.Error("Unknown command: %s", command)
		printUsage()
		os.Exit(1)
	}
}

func handleRun(eng *engine.Engine, checkID string, log logger.Logger) {
	if err := eng.RunCheck(context.Background(), checkID); err != nil {
		log.Error("Run failed: %v", err)
		os.Exit(1)
	}
}

func parseRunsArgs(args []string) (string, int, error) {
	limit := 20
	checkID := ""

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--last":
			if i+1 >= len(args) {
				return "", 0, fmt.Errorf("--last requires a number")
			}
			i++
			n, err := parsePositiveInt(args[i])
			if err != nil {
				return "", 0, fmt.Errorf("--last: %w", err)
			}
			limit = n
		case strings.HasPrefix(arg, "--last="):
			n, err := parsePositiveInt(strings.TrimPrefix(arg, "--last="))
			if err != nil {
				return "", 0, fmt.Errorf("--last: %w", err)
			}
			limit = n
		case strings.HasPrefix(arg, "-"):
			return "", 0, fmt.Errorf("unknown flag %q", arg)
		default:
			if checkID != "" {
				return "", 0, fmt.Errorf("unexpected argument %q", arg)
			}
			checkID = arg
		}
	}

	if checkID == "" {
		return "", 0, fmt.Errorf("check_id is required")
	}
	return checkID, limit, nil
}

func parsePositiveInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return n, nil
}

func parseResolveNote(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if args[0] == "--note" {
		if len(args) == 1 {
			return "", fmt.Errorf("--note requires a message")
		}
		return strings.Join(args[1:], " "), nil
	}
	if strings.HasPrefix(args[0], "--note=") {
		note := strings.TrimPrefix(args[0], "--note=")
		if note == "" {
			return "", fmt.Errorf("--note requires a message")
		}
		if len(args) > 1 {
			note += " " + strings.Join(args[1:], " ")
		}
		return note, nil
	}
	return strings.Join(args, " "), nil
}

func parseDeployArgs(args []string) (string, deploy.CollectorsOptions, error) {
	opts := deploy.CollectorsOptions{Dest: deploy.DefaultCollectorsDest}
	targetName := ""

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--source":
			if i+1 >= len(args) {
				return "", opts, fmt.Errorf("--source requires a path")
			}
			i++
			opts.Source = args[i]
		case strings.HasPrefix(arg, "--source="):
			opts.Source = strings.TrimPrefix(arg, "--source=")
			if opts.Source == "" {
				return "", opts, fmt.Errorf("--source requires a path")
			}
		case arg == "--dest":
			if i+1 >= len(args) {
				return "", opts, fmt.Errorf("--dest requires a path")
			}
			i++
			opts.Dest = args[i]
		case strings.HasPrefix(arg, "--dest="):
			opts.Dest = strings.TrimPrefix(arg, "--dest=")
			if opts.Dest == "" {
				return "", opts, fmt.Errorf("--dest requires a path")
			}
		case strings.HasPrefix(arg, "-"):
			return "", opts, fmt.Errorf("unknown flag %q", arg)
		default:
			if targetName != "" {
				return "", opts, fmt.Errorf("unexpected argument %q", arg)
			}
			targetName = arg
		}
	}
	if targetName == "" {
		return "", opts, fmt.Errorf("target is required")
	}
	return targetName, opts, nil
}

func parseDoctorArgs(args []string) (doctor.Options, error) {
	opts := doctor.Options{CheckSSH: true, SSHTimeout: 5 * time.Second}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--no-ssh":
			opts.CheckSSH = false
		case arg == "--ssh-timeout":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--ssh-timeout requires a duration")
			}
			i++
			timeout, err := time.ParseDuration(args[i])
			if err != nil || timeout <= 0 {
				return opts, fmt.Errorf("--ssh-timeout must be a positive duration")
			}
			opts.SSHTimeout = timeout
		case strings.HasPrefix(arg, "--ssh-timeout="):
			timeout, err := time.ParseDuration(strings.TrimPrefix(arg, "--ssh-timeout="))
			if err != nil || timeout <= 0 {
				return opts, fmt.Errorf("--ssh-timeout must be a positive duration")
			}
			opts.SSHTimeout = timeout
		default:
			return opts, fmt.Errorf("unknown argument %q", arg)
		}
	}
	return opts, nil
}

func handleNote(db *storage.DB, checkID, content string, log logger.Logger) {
	if err := db.AddNote(checkID, content); err != nil {
		log.Error("Failed to add note: %v", err)
		os.Exit(1)
	}
	log.Info("Note added for check '%s'", checkID)
}

func handleRuns(db *storage.DB, checkID string, limit int, log logger.Logger) {
	runs, err := db.RecentRuns(checkID, limit)
	if err != nil {
		log.Error("Failed to fetch runs: %v", err)
		os.Exit(1)
	}
	if len(runs) == 0 {
		log.Info("No runs found for check '%s'", checkID)
		return
	}

	fmt.Printf("Recent runs for %s:\n", checkID)
	fmt.Printf("%-6s %-10s %-12s %-8s %s\n", "ID", "Status", "Duration", "Window", "Time")
	for _, r := range runs {
		dur := time.Duration(r.DurationMs) * time.Millisecond
		fmt.Printf("%-6d %-10s %-12s %-8s %s\n",
			r.ID, r.Status, dur.String(), r.Window,
			r.CreatedAt.Format(time.RFC3339))
	}
}

func handleStatus(cfg *config.Config, db *storage.DB, eng *engine.Engine, log logger.Logger) {
	ctx := context.Background()
	if err := eng.SyncSchedule(ctx); err != nil {
		log.Warn("Failed to sync scheduled checks: %v", err)
	}

	incidents, err := db.ActiveIncidents()
	if err != nil {
		log.Error("Failed to fetch incidents: %v", err)
		os.Exit(1)
	}

	var store tasks.Store
	if s, err := db.TaskStore(); err != nil {
		log.Warn("Failed to load scheduled task state: %v", err)
	} else {
		store = s
	}

	loc, err := time.LoadLocation(cfg.App.Timezone)
	if err != nil {
		loc = time.UTC
	}

	fmt.Println("Hugin status")
	fmt.Printf("Data: %s\n", filepath.Join(cfg.App.DataDir, "hugin.db"))
	fmt.Printf("Checks: %d  Targets: %d  Active incidents: %d\n\n", len(cfg.Checks), len(cfg.Targets), len(incidents))

	if len(incidents) == 0 {
		fmt.Println("Active incidents: none")
	} else {
		fmt.Println("Active incidents:")
		for _, inc := range incidents {
			fmt.Printf("- %s  %s  %s  since %s  %s\n",
				ellipsize(inc.ID, 36),
				ellipsize(inc.CheckID, 24),
				inc.Severity,
				formatStatusTime(inc.CreatedAt, loc),
				ellipsize(inc.Summary, 80),
			)
		}
	}

	fmt.Println("\nChecks:")
	fmt.Printf("%-24s %-12s %-18s %-10s %-10s %-18s %s\n", "CHECK", "TARGET", "LAST RUN", "STATUS", "AI", "NEXT RUN", "SUMMARY")
	for _, check := range cfg.Checks {
		lastRun := "never"
		collectorStatus := "-"
		analysisSeverity := "-"
		summary := "-"

		runs, err := db.RecentRuns(check.ID, 1)
		if err != nil {
			summary = "runs error: " + err.Error()
		} else if len(runs) > 0 {
			run := runs[0]
			lastRun = formatStatusTime(run.CreatedAt, loc)
			collectorStatus = run.Status
			analysis, err := db.RunAnalysis(run.ID)
			if err != nil {
				summary = "analysis error: " + err.Error()
			} else if analysis != nil {
				analysisSeverity = analysis.Severity
				summary = analysis.Summary
				if analysis.Error != "" {
					summary = "analysis failed: " + analysis.Error
				}
			}
		}

		fmt.Printf("%-24s %-12s %-18s %-10s %-10s %-18s %s\n",
			ellipsize(check.ID, 24),
			ellipsize(check.Target, 12),
			lastRun,
			collectorStatus,
			analysisSeverity,
			statusTaskNext(ctx, store, check.ID, loc),
			ellipsize(summary, 80),
		)
	}
}

func statusTaskNext(ctx context.Context, store tasks.Store, checkID string, loc *time.Location) string {
	if store == nil {
		return "-"
	}
	task, err := store.Get(ctx, engine.CheckTaskID(checkID))
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			return "not synced"
		}
		return "task error"
	}
	if task.Exhausted() {
		return "exhausted"
	}
	if task.LockedAt != nil && task.LockToken != "" {
		return "running"
	}
	return formatStatusTimePtr(task.NextRunAt, loc)
}

func formatStatusTimePtr(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "-"
	}
	return formatStatusTime(*t, loc)
}

func formatStatusTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func ellipsize(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func handleDeploy(cfg *config.Config, targetName string, opts deploy.CollectorsOptions, log logger.Logger) {
	target, ok := cfg.Targets[targetName]
	if !ok {
		log.Error("Target %q not found", targetName)
		os.Exit(1)
	}
	if target.Type != "ssh" {
		log.Error("Target %q is %q, deploy requires an ssh target", targetName, target.Type)
		os.Exit(1)
	}

	result, err := deploy.Collectors(context.Background(), target, opts)
	if err != nil {
		log.Error("Deploy collectors failed: %v", err)
		os.Exit(1)
	}
	log.Info("Deployed %d collector file(s) from %s to %s:%s", result.Files, result.Source, targetName, result.Dest)
}

func handleDoctor(cfg *config.Config, opts doctor.Options) {
	report := doctor.Check(context.Background(), cfg, opts)
	fmt.Println("Hugin doctor")
	for _, item := range report.Items {
		fmt.Printf("%-4s %-12s %s\n", item.Status, item.Area, item.Message)
	}
	if report.HasFailures() {
		os.Exit(1)
	}
}

func handleRunDue(eng *engine.Engine, log logger.Logger) {
	if err := eng.RunDue(context.Background()); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func handleDaemon(eng *engine.Engine, log logger.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := eng.RunDaemon(ctx); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func handleValidate(cfg *config.Config, log logger.Logger) {
	log.Info("Configuration is valid (%d checks)", len(cfg.Checks))
}

func handleCleanup(db *storage.DB, log logger.Logger) {
	cutoff := time.Now().Add(-14 * 24 * time.Hour) // 14-day retention
	n, err := db.DeleteOldRuns(cutoff)
	if err != nil {
		log.Error("Cleanup failed: %v", err)
		os.Exit(1)
	}
	log.Info("Cleaned up %d old run(s)", n)
}

func handleResolve(cfg *config.Config, db *storage.DB, incidentID, note string, log logger.Logger) {
	inc, err := db.Incident(incidentID)
	if err == sql.ErrNoRows {
		log.Error("Incident '%s' not found", incidentID)
		os.Exit(1)
	}
	if err != nil {
		log.Error("Failed to load incident: %v", err)
		os.Exit(1)
	}

	if err := db.ResolveIncident(incidentID, note); err != nil {
		if err == sql.ErrNoRows {
			log.Error("Incident '%s' not found", incidentID)
			os.Exit(1)
		}
		log.Error("Failed to resolve incident: %v", err)
		os.Exit(1)
	}
	log.Info("Incident '%s' resolved", incidentID)

	inc.Status = "resolved"
	inc.ResolutionNote = note
	check := cfg.FindCheck(inc.CheckID)
	if check != nil && !check.Alert.NotifyOnResolved {
		log.Debug("Resolution notification suppressed by check '%s' config", inc.CheckID)
		return
	}

	ntf := notifier.FromConfig(cfg, log)
	if err := ntf.NotifyResolved(context.Background(), *inc); err != nil {
		log.Error("Failed to send resolution notification: %v", err)
	}
}

func printUsage() {
	fmt.Println("Hugin - AI-first infrastructure monitoring")
	fmt.Println("\nUsage:")
	fmt.Println("  hugin run <check_id>            Execute a check and analyze results")
	fmt.Println("  hugin run-due                   Run all checks that are due")
	fmt.Println("  hugin daemon                    Run scheduled checks continuously")
	fmt.Println("  hugin status                    Show checks, incidents, and next runs")
	fmt.Println("  hugin deploy <target>          Install bundled collectors on an SSH target")
	fmt.Println("  hugin doctor                   Check config, env, and SSH readiness")
	fmt.Println("  hugin note <check_id> <msg>     Add an operator note for a check")
	fmt.Println("  hugin runs <check_id>           Show recent runs for a check")
	fmt.Println("  hugin resolve <incident_id>     Manually resolve an incident")
	fmt.Println("  hugin validate                  Validate configuration")
	fmt.Println("  hugin cleanup                   Remove old runs (14-day retention)")
	fmt.Println("\nFlags:")
	flag.PrintDefaults()
}
