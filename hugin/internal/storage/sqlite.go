package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/pkg/schedule"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

// New initializes the SQLite database and ensures the schema exists.
func New(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Enable WAL mode for better concurrent performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("failed to set WAL mode: %w", err)
	}

	if err := initSchema(db); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return &DB{sql: db}, nil
}

func initSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		check_id TEXT NOT NULL,
		status TEXT NOT NULL,
		metrics JSON,
		errors JSON,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		window TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_runs_check_id ON runs(check_id);
	CREATE INDEX IF NOT EXISTS idx_runs_created_at ON runs(created_at);

	CREATE TABLE IF NOT EXISTS incidents (
		id TEXT PRIMARY KEY,
		check_id TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		severity TEXT NOT NULL,
		summary TEXT NOT NULL,
		evidence TEXT,
		first_run_id INTEGER REFERENCES runs(id),
		last_run_id INTEGER REFERENCES runs(id),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		resolved_at DATETIME,
		resolution_note TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_incidents_check_id ON incidents(check_id);
	CREATE INDEX IF NOT EXISTS idx_incidents_status ON incidents(status);

	CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		check_id TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_notes_check_id ON notes(check_id);

	CREATE TABLE IF NOT EXISTS schedule_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id TEXT NOT NULL,
		occurrence_key TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT '',
		ref TEXT NOT NULL DEFAULT '',
		job_group TEXT NOT NULL DEFAULT '',
		due_at DATETIME NOT NULL,
		started_at DATETIME NOT NULL,
		finished_at DATETIME,
		status TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(job_id, occurrence_key)
	);

	CREATE INDEX IF NOT EXISTS idx_schedule_runs_job_id ON schedule_runs(job_id);
	CREATE INDEX IF NOT EXISTS idx_schedule_runs_due_at ON schedule_runs(due_at);
	`
	_, err := db.Exec(schema)
	return err
}

// RunRecord is a row from the runs table.
type RunRecord struct {
	ID         int64
	CheckID    string
	Status     string
	Metrics    map[string]any
	Errors     []models.ErrorDetail
	DurationMs int64
	Window     string
	CreatedAt  time.Time
}

// IncidentRecord is a row from the incidents table.
type IncidentRecord struct {
	ID             string
	CheckID        string
	Status         string
	Severity       string
	Summary        string
	Evidence       string
	FirstRunID     *int64
	LastRunID      *int64
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	ResolutionNote string
}

// InsertRun saves a completed execution into the database and returns the new row ID.
func (d *DB) InsertRun(checkID string, output *models.CollectorOutput, durationMs int64) (int64, error) {
	metricsJSON, _ := json.Marshal(output.Metrics)
	errorsJSON, _ := json.Marshal(output.Errors)

	res, err := d.sql.Exec(
		`INSERT INTO runs (check_id, status, metrics, errors, duration_ms, window, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		checkID, string(output.Status), string(metricsJSON), string(errorsJSON),
		durationMs, output.Window, time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RunsSince returns all runs for a check after the given time, ordered newest first.
func (d *DB) RunsSince(checkID string, since time.Time, limit int) ([]RunRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, check_id, status, metrics, errors, duration_ms, window, created_at
		 FROM runs
		 WHERE check_id = ? AND created_at >= ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		checkID, since.UTC(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// RecentRuns returns the most recent runs for a check.
func (d *DB) RecentRuns(checkID string, limit int) ([]RunRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, check_id, status, metrics, errors, duration_ms, window, created_at
		 FROM runs
		 WHERE check_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		checkID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// ActiveIncident returns the currently active (unresolved) incident for a check, if any.
func (d *DB) ActiveIncident(checkID string) (*IncidentRecord, error) {
	row := d.sql.QueryRow(
		`SELECT id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, created_at, resolved_at, resolution_note
		 FROM incidents
		 WHERE check_id = ? AND status = 'active'
		 ORDER BY created_at DESC
		 LIMIT 1`,
		checkID,
	)
	var inc IncidentRecord
	err := scanIncident(row, &inc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inc, nil
}

// Incident returns an incident by ID.
func (d *DB) Incident(incidentID string) (*IncidentRecord, error) {
	row := d.sql.QueryRow(
		`SELECT id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, created_at, resolved_at, resolution_note
		 FROM incidents
		 WHERE id = ?`,
		incidentID,
	)
	var inc IncidentRecord
	err := scanIncident(row, &inc)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	return &inc, nil
}

// CreateIncident inserts a new incident.
func (d *DB) CreateIncident(id, checkID, severity, summary, evidence string, firstRunID, lastRunID int64) error {
	_, err := d.sql.Exec(
		`INSERT INTO incidents (id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, created_at)
		 VALUES (?, ?, 'active', ?, ?, ?, ?, ?, ?)`,
		id, checkID, severity, summary, evidence, firstRunID, lastRunID, time.Now().UTC(),
	)
	return err
}

// UpdateIncidentRun updates the last_run_id of an incident (used when new runs belong to same incident).
func (d *DB) UpdateIncidentRun(incidentID string, lastRunID int64) error {
	_, err := d.sql.Exec(
		`UPDATE incidents SET last_run_id = ? WHERE id = ?`,
		lastRunID, incidentID,
	)
	return err
}

// ResolveIncident marks an incident as resolved.
func (d *DB) ResolveIncident(incidentID, note string) error {
	res, err := d.sql.Exec(
		`UPDATE incidents SET status = 'resolved', resolved_at = ?, resolution_note = ? WHERE id = ?`,
		time.Now().UTC(), note, incidentID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Notes returns all notes for a check.
func (d *DB) Notes(checkID string) ([]string, error) {
	rows, err := d.sql.Query(
		`SELECT content FROM notes WHERE check_id = ? ORDER BY created_at ASC`,
		checkID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		notes = append(notes, content)
	}
	return notes, rows.Err()
}

// AddNote inserts a new note for a check.
func (d *DB) AddNote(checkID, content string) error {
	_, err := d.sql.Exec(
		`INSERT INTO notes (check_id, content, created_at) VALUES (?, ?, ?)`,
		checkID, content, time.Now().UTC(),
	)
	return err
}

// DeleteOldRuns removes runs older than the cutoff, except those tied to active incidents.
func (d *DB) DeleteOldRuns(before time.Time) (int64, error) {
	res, err := d.sql.Exec(
		`DELETE FROM runs WHERE created_at < ? AND id NOT IN (
			SELECT first_run_id FROM incidents WHERE status = 'active'
			UNION
			SELECT last_run_id FROM incidents WHERE status = 'active'
		)`,
		before.UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LastRun returns the latest scheduler coordination run for a job.
func (d *DB) LastRun(ctx context.Context, jobID string) (*schedule.RunRecord, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT id, job_id, occurrence_key, kind, ref, job_group, due_at, started_at, finished_at, status, error
		 FROM schedule_runs
		 WHERE job_id = ?
		 ORDER BY due_at DESC, started_at DESC, id DESC
		 LIMIT 1`,
		jobID,
	)
	rec, err := scanScheduleRun(row)
	if err == sql.ErrNoRows {
		return nil, schedule.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// LastRuns returns the latest scheduler coordination run for each requested job.
func (d *DB) LastRuns(ctx context.Context, jobIDs []string) (map[string]schedule.RunRecord, error) {
	result := make(map[string]schedule.RunRecord, len(jobIDs))
	for _, jobID := range jobIDs {
		rec, err := d.LastRun(ctx, jobID)
		if errors.Is(err, schedule.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[jobID] = *rec
	}
	return result, nil
}

// TryCreateRun atomically claims one scheduler occurrence.
func (d *DB) TryCreateRun(ctx context.Context, record schedule.RunRecord) (schedule.RunRecord, bool, error) {
	res, err := d.sql.ExecContext(ctx,
		`INSERT OR IGNORE INTO schedule_runs
		 (job_id, occurrence_key, kind, ref, job_group, due_at, started_at, status, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.JobID,
		record.OccurrenceKey,
		record.Kind,
		record.Ref,
		record.Group,
		record.DueAt.UTC(),
		record.StartedAt.UTC(),
		string(record.Status),
		record.Error,
	)
	if err != nil {
		return schedule.RunRecord{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return schedule.RunRecord{}, false, err
	}
	if n == 0 {
		return schedule.RunRecord{}, false, nil
	}

	id, err := res.LastInsertId()
	if err != nil {
		return schedule.RunRecord{}, false, err
	}
	record.ID = strconv.FormatInt(id, 10)
	return record, true, nil
}

// FinishRun records the scheduler outcome for a claimed occurrence.
func (d *DB) FinishRun(ctx context.Context, runID string, status schedule.RunStatus, message string, finishedAt time.Time) error {
	id, err := strconv.ParseInt(runID, 10, 64)
	if err != nil {
		return schedule.ErrNotFound
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE schedule_runs
		 SET status = ?, error = ?, finished_at = ?
		 WHERE id = ?`,
		string(status), message, finishedAt.UTC(), id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return schedule.ErrNotFound
	}
	return nil
}

func scanRuns(rows *sql.Rows) ([]RunRecord, error) {
	var runs []RunRecord
	for rows.Next() {
		var r RunRecord
		var metricsJSON, errorsJSON string
		if err := rows.Scan(&r.ID, &r.CheckID, &r.Status, &metricsJSON, &errorsJSON, &r.DurationMs, &r.Window, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(metricsJSON), &r.Metrics)
		json.Unmarshal([]byte(errorsJSON), &r.Errors)
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanScheduleRun(row scanner) (schedule.RunRecord, error) {
	var rec schedule.RunRecord
	var id int64
	var finishedAt sql.NullTime
	if err := row.Scan(
		&id,
		&rec.JobID,
		&rec.OccurrenceKey,
		&rec.Kind,
		&rec.Ref,
		&rec.Group,
		&rec.DueAt,
		&rec.StartedAt,
		&finishedAt,
		&rec.Status,
		&rec.Error,
	); err != nil {
		return schedule.RunRecord{}, err
	}
	rec.ID = strconv.FormatInt(id, 10)
	if finishedAt.Valid {
		rec.FinishedAt = &finishedAt.Time
	}
	return rec, nil
}

func scanIncident(row *sql.Row, inc *IncidentRecord) error {
	var evidence sql.NullString
	var resolutionNote sql.NullString
	var resolvedAt sql.NullTime
	err := row.Scan(
		&inc.ID, &inc.CheckID, &inc.Status, &inc.Severity, &inc.Summary,
		&evidence, &inc.FirstRunID, &inc.LastRunID,
		&inc.CreatedAt, &resolvedAt, &resolutionNote,
	)
	inc.Evidence = evidence.String
	inc.ResolutionNote = resolutionNote.String
	if resolvedAt.Valid {
		inc.ResolvedAt = &resolvedAt.Time
	}
	return err
}

// Close gracefully closes the database connection.
func (d *DB) Close() error {
	return d.sql.Close()
}
