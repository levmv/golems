// Package tasksqlite provides a SQLite-backed tasks.Store.
//
// The adapter does not import a SQLite driver and does not own the database
// connection. Applications open and configure their own *sql.DB, then pass it to
// New.
package tasksqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/tasks"
)

const (
	defaultTable     = "tasks"
	sqliteTimeFormat = "2006-01-02T15:04:05.000000000Z"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Options struct {
	Table string
}

type Store struct {
	db    *sql.DB
	table string
}

var _ tasks.Store = (*Store)(nil)

func New(db *sql.DB, opts Options) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: db is required", tasks.ErrInvalid)
	}
	table, err := tableName(opts)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, table: table}, nil
}

func Schema(opts Options) (string, error) {
	table, err := tableName(opts)
	if err != nil {
		return "", err
	}

	tableSQL := quoteIdentifier(table)
	dueIndexSQL := quoteIdentifier("idx_" + table + "_next_run_at")
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %[1]s (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	payload BLOB NOT NULL,
	schedule TEXT NOT NULL,
	task_group TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}',
	timeout_ns INTEGER NOT NULL DEFAULT 0,
	next_run_at TEXT,
	locked_at TEXT,
	lock_token TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
	max_attempts INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_started_at TEXT,
	last_finished_at TEXT,
	last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS %[2]s
ON %[1]s(next_run_at)
WHERE next_run_at IS NOT NULL;
`, tableSQL, dueIndexSQL), nil
}

func EnsureSchema(ctx context.Context, db *sql.DB, opts Options) error {
	if db == nil {
		return fmt.Errorf("%w: db is required", tasks.ErrInvalid)
	}
	schema, err := Schema(opts)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, schema)
	return err
}

func (s *Store) Enqueue(ctx context.Context, task tasks.Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	schedule, metadata, err := encodeStructured(task.Schedule, task.Metadata)
	if err != nil {
		return err
	}
	payload := normalizePayload(task.Payload)
	_, err = s.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (
			id, kind, payload, schedule, task_group, metadata, timeout_ns,
			next_run_at, locked_at, lock_token, attempts, max_attempts,
			created_at, updated_at, last_started_at, last_finished_at, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, quoteIdentifier(s.table)),
		task.ID,
		task.Kind,
		payload,
		schedule,
		task.Group,
		metadata,
		int64(task.Timeout),
		formatTimePtr(task.NextRunAt),
		formatTimePtr(task.LockedAt),
		task.LockToken,
		task.Attempts,
		task.MaxAttempts,
		formatTime(task.CreatedAt),
		formatTime(task.UpdatedAt),
		formatTimePtr(task.LastStartedAt),
		formatTimePtr(task.LastFinishedAt),
		task.LastError,
	)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (tasks.Task, error) {
	if id == "" {
		return tasks.Task{}, fmt.Errorf("%w: task ID is required", tasks.ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, taskColumns(), quoteIdentifier(s.table)), id)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return tasks.Task{}, tasks.ErrNotFound
	}
	if err != nil {
		return tasks.Task{}, err
	}
	return task, nil
}

func (s *Store) List(ctx context.Context, filter tasks.ListFilter) ([]tasks.Task, error) {
	var conds []string
	var args []any
	if filter.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.Group != "" {
		conds = append(conds, "task_group = ?")
		args = append(args, filter.Group)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s %s`,
		taskColumns(), quoteIdentifier(s.table), where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]tasks.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortTasksByNextRun(out)
	return out, nil
}

func (s *Store) Delete(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("%w: task ID is required", tasks.ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, quoteIdentifier(s.table)), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) DeleteClaimed(ctx context.Context, id string, lockToken string) (bool, error) {
	if lockToken == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(
		ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE id = ? AND lock_token = ?`, quoteIdentifier(s.table)),
		id,
		lockToken,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) Reschedule(ctx context.Context, id string, schedule tasks.Schedule, nextRunAt time.Time, updatedAt time.Time) (tasks.Task, bool, error) {
	if id == "" {
		return tasks.Task{}, false, fmt.Errorf("%w: task ID is required", tasks.ErrInvalid)
	}
	if nextRunAt.IsZero() {
		return tasks.Task{}, false, fmt.Errorf("%w: next run time is required", tasks.ErrInvalid)
	}
	if err := schedule.Validate(); err != nil {
		return tasks.Task{}, false, fmt.Errorf("%w: schedule: %v", tasks.ErrInvalid, err)
	}
	scheduleJSON, err := encodeSchedule(schedule)
	if err != nil {
		return tasks.Task{}, false, err
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		UPDATE %s
		   SET schedule = ?,
		       next_run_at = ?,
		       locked_at = NULL,
		       lock_token = '',
		       attempts = 0,
		       updated_at = ?,
		       last_error = ''
		 WHERE id = ?
		 RETURNING %s`,
		quoteIdentifier(s.table),
		taskColumns(),
	),
		scheduleJSON,
		formatTime(nextRunAt),
		formatTime(updatedAt),
		id,
	)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return tasks.Task{}, false, nil
	}
	if err != nil {
		return tasks.Task{}, false, err
	}
	return task, true, nil
}

func (s *Store) ClaimDue(ctx context.Context, now time.Time, leaseDuration time.Duration, limit int, token string) ([]tasks.Task, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: lock token is required", tasks.ErrInvalid)
	}
	now = now.UTC()

	args := []any{
		formatTime(now),
	}
	lockSQL, lockArgs := claimableLockSQL(now, leaseDuration)
	args = append(args, lockArgs...)
	limitSQL := ""
	if limit > 0 {
		limitSQL = "LIMIT ?"
		args = append(args, limit)
	}
	args = append(args,
		formatTime(now),
		token,
		formatTime(now),
		formatTime(now),
	)

	query := fmt.Sprintf(`
		WITH selected(id) AS (
		    SELECT id
		      FROM %[1]s
		     WHERE next_run_at IS NOT NULL
		       AND next_run_at <= ?
		       AND %[2]s
		     ORDER BY next_run_at, id
		     %[3]s
		)
		UPDATE %[1]s
		   SET locked_at = ?,
		       lock_token = ?,
		       last_started_at = ?,
		       last_finished_at = NULL,
		       last_error = '',
		       updated_at = ?
		 WHERE id IN (SELECT id FROM selected)
		 RETURNING %[4]s`,
		quoteIdentifier(s.table),
		lockSQL,
		limitSQL,
		taskColumns(),
	)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]tasks.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortTasksByNextRun(out)
	return out, nil
}

func (s *Store) Finish(ctx context.Context, finish tasks.Finish) (bool, error) {
	if finish.LockToken == "" {
		return false, nil
	}
	assignments := []string{
		"locked_at = NULL",
		"lock_token = ''",
		"last_finished_at = ?",
		"last_error = ?",
		"next_run_at = ?",
		"updated_at = ?",
	}
	args := []any{
		formatTime(finish.FinishedAt),
		finish.Error,
		formatTimePtr(finish.NextRunAt),
		formatTime(finish.FinishedAt),
	}
	if finish.ResetAttempts {
		assignments = append(assignments, "attempts = 0")
	} else if finish.IncrementAttempts {
		assignments = append(assignments, "attempts = attempts + 1")
	}
	args = append(args, finish.ID, finish.LockToken)

	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		   SET %s
		 WHERE id = ?
		   AND lock_token = ?`,
		quoteIdentifier(s.table),
		strings.Join(assignments, ", "),
	), args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) NextRunAt(ctx context.Context, now time.Time, leaseDuration time.Duration) (*time.Time, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s
		  FROM %s
		 WHERE next_run_at IS NOT NULL`,
		taskColumns(),
		quoteIdentifier(s.table),
	))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now = now.UTC()
	var next *time.Time
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if task.NextRunAt == nil {
			continue
		}
		candidate := task.NextRunAt.UTC()
		if !candidate.After(now) && activeLock(task, now, leaseDuration) {
			candidate = task.LockedAt.Add(leaseDuration).UTC()
		}
		if next == nil || candidate.Before(*next) {
			next = &candidate
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cloneTimePtr(next), nil
}

func encodeStructured(schedule tasks.Schedule, metadata map[string]string) (string, string, error) {
	scheduleJSON, err := encodeSchedule(schedule)
	if err != nil {
		return "", "", err
	}
	metadataJSON, err := encodeMetadata(metadata)
	if err != nil {
		return "", "", err
	}
	return scheduleJSON, metadataJSON, nil
}

func encodeSchedule(schedule tasks.Schedule) (string, error) {
	if err := schedule.Validate(); err != nil {
		return "", fmt.Errorf("%w: schedule: %v", tasks.ErrInvalid, err)
	}
	data, err := json.Marshal(schedule)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func encodeMetadata(metadata map[string]string) (string, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (tasks.Task, error) {
	var task tasks.Task
	var scheduleJSON, metadataJSON string
	var timeoutNS int64
	var nextRunAt, lockedAt, createdAt, updatedAt, lastStartedAt, lastFinishedAt sql.NullString
	if err := row.Scan(
		&task.ID,
		&task.Kind,
		&task.Payload,
		&scheduleJSON,
		&task.Group,
		&metadataJSON,
		&timeoutNS,
		&nextRunAt,
		&lockedAt,
		&task.LockToken,
		&task.Attempts,
		&task.MaxAttempts,
		&createdAt,
		&updatedAt,
		&lastStartedAt,
		&lastFinishedAt,
		&task.LastError,
	); err != nil {
		return tasks.Task{}, err
	}
	if err := json.Unmarshal([]byte(scheduleJSON), &task.Schedule); err != nil {
		return tasks.Task{}, err
	}
	if err := task.Schedule.Validate(); err != nil {
		return tasks.Task{}, err
	}
	if metadataJSON == "" {
		metadataJSON = "{}"
	}
	if err := json.Unmarshal([]byte(metadataJSON), &task.Metadata); err != nil {
		return tasks.Task{}, err
	}
	task.Timeout = time.Duration(timeoutNS)

	var err error
	if task.NextRunAt, err = parseNullableTime(nextRunAt); err != nil {
		return tasks.Task{}, err
	}
	if task.LockedAt, err = parseNullableTime(lockedAt); err != nil {
		return tasks.Task{}, err
	}
	if task.CreatedAt, err = parseRequiredTime(createdAt); err != nil {
		return tasks.Task{}, err
	}
	if task.UpdatedAt, err = parseRequiredTime(updatedAt); err != nil {
		return tasks.Task{}, err
	}
	if task.LastStartedAt, err = parseNullableTime(lastStartedAt); err != nil {
		return tasks.Task{}, err
	}
	if task.LastFinishedAt, err = parseNullableTime(lastFinishedAt); err != nil {
		return tasks.Task{}, err
	}
	return task, nil
}

func taskColumns() string {
	return "id, kind, payload, schedule, task_group, metadata, timeout_ns, next_run_at, locked_at, lock_token, attempts, max_attempts, created_at, updated_at, last_started_at, last_finished_at, last_error"
}

func claimableLockSQL(now time.Time, leaseDuration time.Duration) (string, []any) {
	if leaseDuration <= 0 {
		return "(lock_token = '' OR locked_at IS NULL)", nil
	}
	return "(lock_token = '' OR locked_at IS NULL OR locked_at <= ?)", []any{formatTime(now.Add(-leaseDuration))}
}

func activeLock(task tasks.Task, now time.Time, leaseDuration time.Duration) bool {
	if task.LockToken == "" || task.LockedAt == nil {
		return false
	}
	if leaseDuration <= 0 {
		return true
	}
	return task.LockedAt.Add(leaseDuration).After(now)
}

func sortTasksByNextRun(values []tasks.Task) {
	slices.SortStableFunc(values, compareTasksByNextRun)
}

func compareTasksByNextRun(left, right tasks.Task) int {
	switch {
	case left.NextRunAt == nil && right.NextRunAt == nil:
		return strings.Compare(left.ID, right.ID)
	case left.NextRunAt == nil:
		return 1
	case right.NextRunAt == nil:
		return -1
	case left.NextRunAt.Before(*right.NextRunAt):
		return -1
	case right.NextRunAt.Before(*left.NextRunAt):
		return 1
	default:
		return strings.Compare(left.ID, right.ID)
	}
}

func normalizePayload(in []byte) []byte {
	if len(in) == 0 {
		return []byte("null")
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	t, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func parseRequiredTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || value.String == "" {
		return time.Time{}, fmt.Errorf("required time is empty")
	}
	return parseTime(value.String)
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(sqliteTimeFormat, value)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, value)
	}
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	out := t.UTC()
	return &out
}

func tableName(opts Options) (string, error) {
	table := opts.Table
	if table == "" {
		table = defaultTable
	}
	if !identifierPattern.MatchString(table) {
		return "", fmt.Errorf("%w: invalid table name %q", tasks.ErrInvalid, table)
	}
	return table, nil
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
