package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/levmv/golems/hugin/internal/models"
	"github.com/levmv/golems/pkg/tasks"
	tasksqlite "github.com/levmv/golems/pkg/tasks/sqlite"

	_ "modernc.org/sqlite"
)

var taskStoreOptions = tasksqlite.Options{Table: "tasks"}

type DB struct {
	sql *sql.DB
}

type CheckTaskRef struct {
	CheckID     string
	TaskID      string
	Fingerprint string
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

	CREATE TABLE IF NOT EXISTS hugin_check_tasks (
		check_id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL
	);

	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	return tasksqlite.EnsureSchema(context.Background(), db, taskStoreOptions)
}

func (d *DB) TaskStore() (tasks.Store, error) {
	return tasksqlite.New(d.sql, taskStoreOptions)
}

func (d *DB) CheckTaskRefs(ctx context.Context) ([]CheckTaskRef, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT check_id, task_id, fingerprint FROM hugin_check_tasks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []CheckTaskRef
	for rows.Next() {
		var ref CheckTaskRef
		if err := rows.Scan(&ref.CheckID, &ref.TaskID, &ref.Fingerprint); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

func (d *DB) UpsertCheckTaskRef(ctx context.Context, ref CheckTaskRef) error {
	_, err := d.sql.ExecContext(ctx, `
		INSERT INTO hugin_check_tasks (check_id, task_id, fingerprint)
		VALUES (?, ?, ?)
		ON CONFLICT(check_id) DO UPDATE SET
			task_id = excluded.task_id,
			fingerprint = excluded.fingerprint`,
		ref.CheckID,
		ref.TaskID,
		ref.Fingerprint,
	)
	return err
}

func (d *DB) DeleteCheckTaskRef(ctx context.Context, checkID string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM hugin_check_tasks WHERE check_id = ?`, checkID)
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
