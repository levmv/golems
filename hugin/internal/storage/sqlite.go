package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/pkg/tasks"
	tasksqlite "github.com/levmv/golems/pkg/tasks/sqlite"

	_ "modernc.org/sqlite"
)

var taskStoreOptions = tasksqlite.Options{Table: "tasks"}

const sqliteBusyTimeout = 5 * time.Second

type DB struct {
	sql *sql.DB
}

// New initializes the SQLite database and ensures the schema exists.
func New(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
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

func sqliteDSN(dbPath string) string {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()))

	separator := "?"
	if strings.Contains(dbPath, "?") {
		separator = "&"
	}
	return dbPath + separator + query.Encode()
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
		analysis_severity TEXT NOT NULL DEFAULT '',
		analysis_should_alert INTEGER,
		analysis_summary TEXT NOT NULL DEFAULT '',
		analysis_evidence TEXT NOT NULL DEFAULT '',
		analysis_error TEXT NOT NULL DEFAULT '',
		analysis_model TEXT NOT NULL DEFAULT '',
		analysis_created_at DATETIME,
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
		last_notified_at DATETIME,
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

	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_severity", "analysis_severity TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_should_alert", "analysis_should_alert INTEGER"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_summary", "analysis_summary TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_evidence", "analysis_evidence TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_error", "analysis_error TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_model", "analysis_model TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "runs", "analysis_created_at", "analysis_created_at DATETIME"); err != nil {
		return err
	}
	if err := ensureColumn(db, "incidents", "last_notified_at", "last_notified_at DATETIME"); err != nil {
		return err
	}
	return tasksqlite.EnsureSchema(context.Background(), db, taskStoreOptions)
}

func ensureColumn(db *sql.DB, table string, name string, definition string) error {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, definition))
	return err
}

func (d *DB) TaskStore() (tasks.Store, error) {
	return tasksqlite.New(d.sql, taskStoreOptions)
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

type RunAnalysis struct {
	Severity    string
	ShouldAlert bool
	Summary     string
	Evidence    string
	Error       string
	Model       string
	CreatedAt   time.Time
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
	LastNotifiedAt *time.Time
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	ResolutionNote string
}

type NoteRecord struct {
	ID        int64
	CheckID   string
	Content   string
	CreatedAt time.Time
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

func (d *DB) UpdateRunAnalysis(runID int64, analysis RunAnalysis) error {
	if analysis.CreatedAt.IsZero() {
		analysis.CreatedAt = time.Now().UTC()
	}
	_, err := d.sql.Exec(
		`UPDATE runs
		    SET analysis_severity = ?,
		        analysis_should_alert = ?,
		        analysis_summary = ?,
		        analysis_evidence = ?,
		        analysis_error = ?,
		        analysis_model = ?,
		        analysis_created_at = ?
		  WHERE id = ?`,
		analysis.Severity,
		analysis.ShouldAlert,
		analysis.Summary,
		analysis.Evidence,
		analysis.Error,
		analysis.Model,
		analysis.CreatedAt.UTC(),
		runID,
	)
	return err
}

func (d *DB) RunAnalysis(runID int64) (*RunAnalysis, error) {
	row := d.sql.QueryRow(
		`SELECT analysis_severity, analysis_should_alert, analysis_summary,
		        analysis_evidence, analysis_error, analysis_model, analysis_created_at
		   FROM runs
		  WHERE id = ?`,
		runID,
	)
	var analysis RunAnalysis
	var shouldAlert sql.NullBool
	var createdAt sql.NullTime
	if err := row.Scan(
		&analysis.Severity,
		&shouldAlert,
		&analysis.Summary,
		&analysis.Evidence,
		&analysis.Error,
		&analysis.Model,
		&createdAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if analysis.Severity == "" && !shouldAlert.Valid && analysis.Summary == "" && analysis.Error == "" {
		return nil, nil
	}
	analysis.ShouldAlert = shouldAlert.Valid && shouldAlert.Bool
	if createdAt.Valid {
		analysis.CreatedAt = createdAt.Time
	}
	return &analysis, nil
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

// RunsSinceExcluding returns runs for a check after the given time, excluding one run ID.
func (d *DB) RunsSinceExcluding(checkID string, since time.Time, excludeRunID int64, limit int) ([]RunRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, check_id, status, metrics, errors, duration_ms, window, created_at
		 FROM runs
		 WHERE check_id = ? AND created_at >= ? AND id <> ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		checkID, since.UTC(), excludeRunID, limit,
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
		`SELECT id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, last_notified_at, created_at, resolved_at, resolution_note
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

func (d *DB) ActiveIncidents() ([]IncidentRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, last_notified_at, created_at, resolved_at, resolution_note
		 FROM incidents
		 WHERE status = 'active'
		 ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var incidents []IncidentRecord
	for rows.Next() {
		var inc IncidentRecord
		if err := scanIncident(rows, &inc); err != nil {
			return nil, err
		}
		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
}

// Incident returns an incident by ID.
func (d *DB) Incident(incidentID string) (*IncidentRecord, error) {
	row := d.sql.QueryRow(
		`SELECT id, check_id, status, severity, summary, evidence, first_run_id, last_run_id, last_notified_at, created_at, resolved_at, resolution_note
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

// UpdateIncident refreshes the visible state of an active incident.
func (d *DB) UpdateIncident(incidentID string, severity, summary, evidence string, lastRunID int64) error {
	res, err := d.sql.Exec(
		`UPDATE incidents
		    SET severity = ?,
		        summary = ?,
		        evidence = ?,
		        last_run_id = ?
		  WHERE id = ?`,
		severity,
		summary,
		evidence,
		lastRunID,
		incidentID,
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

func (d *DB) MarkIncidentNotified(incidentID string, notifiedAt time.Time) error {
	res, err := d.sql.Exec(
		`UPDATE incidents SET last_notified_at = ? WHERE id = ?`,
		notifiedAt.UTC(), incidentID,
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
	records, err := d.NoteRecords(checkID)
	if err != nil {
		return nil, err
	}
	notes := make([]string, len(records))
	for i, note := range records {
		notes[i] = note.Content
	}
	return notes, nil
}

// NoteRecords returns all notes for a check with IDs and timestamps.
func (d *DB) NoteRecords(checkID string) ([]NoteRecord, error) {
	rows, err := d.sql.Query(
		`SELECT id, check_id, content, created_at
		   FROM notes
		  WHERE check_id = ?
		  ORDER BY created_at ASC, id ASC`,
		checkID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []NoteRecord
	for rows.Next() {
		var note NoteRecord
		if err := rows.Scan(&note.ID, &note.CheckID, &note.Content, &note.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, note)
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

// DeleteNote removes a note by ID.
func (d *DB) DeleteNote(noteID int64) error {
	res, err := d.sql.Exec(`DELETE FROM notes WHERE id = ?`, noteID)
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

// DeleteOldRuns removes runs older than the cutoff, except those tied to active incidents.
func (d *DB) DeleteOldRuns(before time.Time) (int64, error) {
	res, err := d.sql.Exec(
		`DELETE FROM runs
		  WHERE created_at < ?
		    AND NOT EXISTS (
			    SELECT 1
			      FROM incidents
			     WHERE status = 'active'
			       AND (first_run_id = runs.id OR last_run_id = runs.id)
		    )`,
		before.UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanRuns(rows *sql.Rows) ([]RunRecord, error) {
	var runs []RunRecord
	for rows.Next() {
		var r RunRecord
		var metricsJSON, errorsJSON string
		if err := rows.Scan(&r.ID, &r.CheckID, &r.Status, &metricsJSON, &errorsJSON, &r.DurationMs, &r.Window, &r.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(metricsJSON), &r.Metrics); err != nil {
			return nil, fmt.Errorf("decode run %d metrics: %w", r.ID, err)
		}
		if err := json.Unmarshal([]byte(errorsJSON), &r.Errors); err != nil {
			return nil, fmt.Errorf("decode run %d errors: %w", r.ID, err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func scanIncident(row interface{ Scan(dest ...any) error }, inc *IncidentRecord) error {
	var evidence sql.NullString
	var resolutionNote sql.NullString
	var resolvedAt sql.NullTime
	var lastNotifiedAt sql.NullTime
	err := row.Scan(
		&inc.ID, &inc.CheckID, &inc.Status, &inc.Severity, &inc.Summary,
		&evidence, &inc.FirstRunID, &inc.LastRunID, &lastNotifiedAt,
		&inc.CreatedAt, &resolvedAt, &resolutionNote,
	)
	inc.Evidence = evidence.String
	inc.ResolutionNote = resolutionNote.String
	if lastNotifiedAt.Valid {
		inc.LastNotifiedAt = &lastNotifiedAt.Time
	}
	if resolvedAt.Valid {
		inc.ResolvedAt = &resolvedAt.Time
	}
	return err
}

// Close gracefully closes the database connection.
func (d *DB) Close() error {
	return d.sql.Close()
}
