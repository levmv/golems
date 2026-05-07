package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/engine"
	"github.com/levmv/golems/hugin/internal/notifier"
	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/logger"
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
		handleNote(db, args[1], args[2], log)

	case "runs":
		if len(args) < 2 {
			log.Error("Usage: hugin runs <check_id> [--last N]")
			os.Exit(1)
		}
		handleRuns(db, args[1], log)

	case "resolve":
		if len(args) < 2 {
			log.Error("Usage: hugin resolve <incident_id> [--note <msg>]")
			os.Exit(1)
		}
		note := ""
		if len(args) > 2 {
			note = strings.Join(args[2:], " ")
		}
		handleResolve(cfg, db, args[1], note, log)

	case "run-due":
		handleRunDue(eng, log)

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

func handleNote(db *storage.DB, checkID, content string, log logger.Logger) {
	if err := db.AddNote(checkID, content); err != nil {
		log.Error("Failed to add note: %v", err)
		os.Exit(1)
	}
	log.Info("Note added for check '%s'", checkID)
}

func handleRuns(db *storage.DB, checkID string, log logger.Logger) {
	runs, err := db.RecentRuns(checkID, 20)
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

func handleRunDue(eng *engine.Engine, log logger.Logger) {
	if err := eng.RunDue(context.Background()); err != nil {
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
	fmt.Println("  hugin note <check_id> <msg>     Add an operator note for a check")
	fmt.Println("  hugin runs <check_id>           Show recent runs for a check")
	fmt.Println("  hugin resolve <incident_id>     Manually resolve an incident")
	fmt.Println("  hugin validate                  Validate configuration")
	fmt.Println("  hugin cleanup                   Remove old runs (14-day retention)")
	fmt.Println("\nFlags:")
	flag.PrintDefaults()
}
