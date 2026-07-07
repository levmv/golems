// Package store owns the SQLite schema and queries for the transcript model:
// conversations, runs, messages, and rolling summaries.
//
// Conversation #1 is the legacy/default Telegram chat; other transports can own
// their own default conversations. Subagent contexts are child conversations
// whose parent is a run. See schema.sql for the DDL.
// pkg/tasks/sqlite shares the same database handle via DB() but owns its own
// tables. store depends only on pkg/llm types and database/sql.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/levmv/golems/pkg/llm"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const (
	mainConversationID = 1
	busyTimeout        = 5 * time.Second
	// Transcript paging treats limit as a target page size and may expand a page
	// backward to include the user input for runs that cross the page boundary.
	// This hard cap keeps one pathological run from producing an unbounded page.
	maxExpandedMessagePage = 2000
)

// RoleEvent is a transcript-only role for external events injected into the
// conversation (a fired reminder, say) rather than produced by the model or the
// user. It is not an llm.Role the provider sees: context assembly maps it into a
// user-visible event line. Unlike a user message, an event does not make a run
// due — it never advances the coverage cursor.
const RoleEvent llm.Role = "event"

// All timestamp columns are stored as unix milliseconds (UTC) in INTEGER
// columns, via unixMillis/fromUnixMilli. Row ordering comes from the
// autoincrement id; milliseconds (rather than seconds) let UIs show sub-second
// work durations — e.g. the gap between a step and the next, which collapses to
// "0s" at whole-second precision.

// Store owns the SQLite handle and the transcript schema. It depends only on
// pkg/llm types and database/sql; it knows nothing of golem or engine.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, configures it
// for WAL/NORMAL, and applies the idempotent schema. pkg/tasks/sqlite is meant
// to share the same handle via DB().
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// v1 simplicity: a single connection sidesteps WAL writer contention.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// migration is one ordered, named schema change. up runs inside a transaction
// and must be self-contained; once recorded in schema_migrations it never runs
// again. ids are assigned in order and never reused.
type migration struct {
	id string
	up func(*sql.Tx) error
}

// schemaMigrations is the ordered migration list. 0001 is the pre-publication
// baseline: the whole current schema, built from schema.sql. Future public
// schema changes should be appended as new entries.
func schemaMigrations() []migration {
	return []migration{
		{id: "0001_initial", up: func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaSQL)
			return err
		}},
	}
}

// migrate applies every migration not yet recorded in schema_migrations, each in
// its own transaction, in order. Applied ids are tracked by name (not a single
// version integer) so the set tolerates being adopted on an existing database
// and is robust to out-of-order development.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			id         TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := db.Query(`SELECT id FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, m := range schemaMigrations() {
		if applied[m.id] {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.id, err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.id, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`,
			m.id, unixMillis(nowUTC())); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.id, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.id, err)
		}
	}
	return nil
}

func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + q.Encode()
}

// DB returns the shared connection so pkg/tasks/sqlite can own its own tables
// in the same database.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// Conversation is a durable conversation. Child conversations (subagents) point
// parent_run_id at the run that spawned them.
type Conversation struct {
	ID          int64
	UUID        string
	ParentRunID *int64
	Status      string // "active" | "archived"
	CreatedAt   time.Time
}

// Run is one agent execution within a conversation, whether user-, schedule-,
// or agent-initiated.
type Run struct {
	ID             int64
	ConversationID int64
	InputMessageID *int64 // messages.id this run answers; nil for maintenance runs
	Initiator      string // "user" | "schedule" | "agent"
	Model          string
	Status         string // "running" | "done" | "failed" | "cancelled"
	InputTokens    int64
	OutputTokens   int64
	Error          string
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

// Content is the model-facing shape stored in messages.content (spec D3). UI
// shapes are derived from this, not stored.
type Content struct {
	Text       string         `json:"text,omitempty"`
	Reasoning  string         `json:"reasoning,omitempty"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// Message is one row of the append-only transcript: one model-facing
// llm.Message.
type Message struct {
	ID             int64
	ConversationID int64
	RunID          *int64
	Role           llm.Role
	Source         string // telegram | web | reminder | schedule | delegate | ""
	Content        Content
	CreatedAt      time.Time
}

// Summary is a rolling compaction summary; the newest row per conversation is
// the live one.
type Summary struct {
	ID               int64
	ConversationID   int64
	ThroughMessageID int64
	Content          string
	CreatedAt        time.Time
}

// PushSubscription is one browser Push API subscription bound to a caliban
// conversation.
type PushSubscription struct {
	Endpoint       string
	ConversationID int64
	P256DH         string
	Auth           string
	UserAgent      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// WebSession is one browser session for the web UI. TokenHash is the sha256 hash
// of the random cookie token; the raw token is never stored.
type WebSession struct {
	TokenHash  string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func nowUTC() time.Time { return time.Now().UTC() }

func unixMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromUnixMilli(n int64) time.Time { return time.UnixMilli(n).UTC() }

func newConversationUUID() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate conversation uuid: %w", err)
	}
	return u.String(), nil
}

// EnsureMainConversation returns conversation 1, creating it on first startup.
func (s *Store) EnsureMainConversation(ctx context.Context) (Conversation, error) {
	return s.EnsureConversation(ctx, mainConversationID)
}

// EnsureConversation returns the requested top-level conversation, creating it
// with that id on first use. This is intentionally simple: transports can own a
// stable default conversation id, and future web-created sessions can use
// CreateConversation for arbitrary new ids.
func (s *Store) EnsureConversation(ctx context.Context, id int64) (Conversation, error) {
	if id <= 0 {
		return Conversation{}, fmt.Errorf("ensure conversation: id must be positive")
	}
	now := nowUTC()
	u, err := newConversationUUID()
	if err != nil {
		return Conversation{}, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO conversations (id, uuid, parent_run_id, status, created_at)
		 VALUES (?, ?, NULL, 'active', ?)`,
		id, u, unixMillis(now))
	if err != nil {
		return Conversation{}, fmt.Errorf("ensure conversation %d: %w", id, err)
	}
	return s.Conversation(ctx, id)
}

// CreateConversation creates a new active top-level conversation.
func (s *Store) CreateConversation(ctx context.Context) (Conversation, error) {
	u, err := newConversationUUID()
	if err != nil {
		return Conversation{}, err
	}
	return s.CreateConversationWithUUID(ctx, u)
}

// CreateConversationWithUUID creates a new active top-level conversation with a
// caller-provided external UUID. This is intended for future web sessions whose
// ids are minted by the browser.
func (s *Store) CreateConversationWithUUID(ctx context.Context, externalUUID string) (Conversation, error) {
	externalUUID = strings.TrimSpace(strings.ToLower(externalUUID))
	if externalUUID == "" {
		return Conversation{}, fmt.Errorf("create conversation: uuid is required")
	}
	parsed, err := uuid.Parse(externalUUID)
	if err != nil || parsed.Version() != 7 {
		return Conversation{}, fmt.Errorf("create conversation: uuid must be a valid UUIDv7")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (uuid, parent_run_id, status, created_at) VALUES (?, NULL, 'active', ?)`,
		externalUUID, unixMillis(now))
	if err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Conversation{}, fmt.Errorf("create conversation id: %w", err)
	}
	return Conversation{ID: id, UUID: externalUUID, Status: "active", CreatedAt: now}, nil
}

// CreateChildConversation creates an active conversation owned by a parent run.
func (s *Store) CreateChildConversation(ctx context.Context, parentRunID int64) (Conversation, error) {
	if parentRunID <= 0 {
		return Conversation{}, fmt.Errorf("create child conversation: parent run id must be positive")
	}
	now := nowUTC()
	u, err := newConversationUUID()
	if err != nil {
		return Conversation{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (uuid, parent_run_id, status, created_at)
		 SELECT ?, ?, 'active', ?
		 WHERE EXISTS (SELECT 1 FROM runs WHERE id = ?)`,
		u, parentRunID, unixMillis(now), parentRunID)
	if err != nil {
		return Conversation{}, fmt.Errorf("create child conversation: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Conversation{}, fmt.Errorf("create child conversation: %w", err)
	} else if n == 0 {
		return Conversation{}, fmt.Errorf("create child conversation: parent run %d not found", parentRunID)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Conversation{}, fmt.Errorf("create child conversation id: %w", err)
	}
	return s.Conversation(ctx, id)
}

// Conversation loads one conversation by id.
func (s *Store) Conversation(ctx context.Context, id int64) (Conversation, error) {
	var (
		c         Conversation
		parent    sql.NullInt64
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uuid, parent_run_id, status, created_at FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.UUID, &parent, &c.Status, &createdAt)
	if err != nil {
		return Conversation{}, fmt.Errorf("load conversation %d: %w", id, err)
	}
	if parent.Valid {
		c.ParentRunID = &parent.Int64
	}
	c.CreatedAt = fromUnixMilli(createdAt)
	return c, nil
}

// ConversationByUUID loads one conversation by its external UUID.
func (s *Store) ConversationByUUID(ctx context.Context, externalUUID string) (Conversation, bool, error) {
	externalUUID = strings.TrimSpace(strings.ToLower(externalUUID))
	if externalUUID == "" {
		return Conversation{}, false, nil
	}
	var (
		c         Conversation
		parent    sql.NullInt64
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uuid, parent_run_id, status, created_at FROM conversations WHERE uuid = ?`,
		externalUUID).Scan(&c.ID, &c.UUID, &parent, &c.Status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, fmt.Errorf("load conversation uuid %s: %w", externalUUID, err)
	}
	if parent.Valid {
		c.ParentRunID = &parent.Int64
	}
	c.CreatedAt = fromUnixMilli(createdAt)
	return c, true, nil
}

// EnsureConversationByUUID returns the active top-level conversation with the
// given external UUID, creating it if absent. It is the uuid-keyed analogue of
// EnsureConversation, for system conversations (e.g. free-time) that need a
// stable, code-owned identity rather than a fixed numeric id that auto-increment
// (delegation child conversations) could collide with. Idempotent via the unique
// uuid index.
func (s *Store) EnsureConversationByUUID(ctx context.Context, externalUUID string) (Conversation, error) {
	externalUUID = strings.TrimSpace(strings.ToLower(externalUUID))
	if externalUUID == "" {
		return Conversation{}, fmt.Errorf("ensure conversation: uuid is required")
	}
	parsed, err := uuid.Parse(externalUUID)
	if err != nil || parsed.Version() != 7 {
		return Conversation{}, fmt.Errorf("ensure conversation: uuid must be a valid UUIDv7")
	}
	now := nowUTC()
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO conversations (uuid, parent_run_id, status, created_at)
		 VALUES (?, NULL, 'active', ?)`,
		externalUUID, unixMillis(now)); err != nil {
		return Conversation{}, fmt.Errorf("ensure conversation uuid %s: %w", externalUUID, err)
	}
	conv, ok, err := s.ConversationByUUID(ctx, externalUUID)
	if err != nil {
		return Conversation{}, err
	}
	if !ok {
		return Conversation{}, fmt.Errorf("ensure conversation uuid %s: missing after insert", externalUUID)
	}
	return conv, nil
}

// ActiveConversations returns active top-level conversations with status
// 'active', ordered by id. Child conversations are driven only by delegation
// tool calls, not by normal transport workers.
func (s *Store) ActiveConversations(ctx context.Context) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, uuid, parent_run_id, status, created_at FROM conversations
		 WHERE status = 'active' AND parent_run_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query active conversations: %w", err)
	}
	defer rows.Close()

	var out []Conversation
	for rows.Next() {
		var (
			c         Conversation
			parent    sql.NullInt64
			createdAt int64
		)
		if err := rows.Scan(&c.ID, &c.UUID, &parent, &c.Status, &createdAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		if parent.Valid {
			c.ParentRunID = &parent.Int64
		}
		c.CreatedAt = fromUnixMilli(createdAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneChildConversations deletes delegation (subagent) child conversations
// created before cutoff, along with their messages and summaries, in one
// transaction. Child conversations (parent_run_id NOT NULL) are the transient
// transcripts delegation spawns; their only durable output — the final text —
// already lives in the parent's tool result, so they are disposable once old.
// Runs rows are deliberately left intact so cost history survives the prune.
// Returns the number of conversations deleted.
func (s *Store) PruneChildConversations(ctx context.Context, cutoff time.Time) (int, error) {
	before := unixMillis(cutoff)
	// Selects the child conversations to prune; reused for each dependent table.
	const targets = `SELECT id FROM conversations WHERE parent_run_id IS NOT NULL AND created_at < ?`

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin prune tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM messages WHERE conversation_id IN (`+targets+`)`, before); err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("prune child messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM summaries WHERE conversation_id IN (`+targets+`)`, before); err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("prune child summaries: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM conversations WHERE parent_run_id IS NOT NULL AND created_at < ?`, before)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("prune child conversations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit prune tx: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// UpsertPushSubscription stores a browser Push API subscription for a
// conversation. Endpoint is the stable key the browser push service issued.
func (s *Store) UpsertPushSubscription(ctx context.Context, ps PushSubscription) error {
	ps.Endpoint = strings.TrimSpace(ps.Endpoint)
	ps.P256DH = strings.TrimSpace(ps.P256DH)
	ps.Auth = strings.TrimSpace(ps.Auth)
	if ps.Endpoint == "" {
		return fmt.Errorf("upsert push subscription: endpoint is required")
	}
	if ps.ConversationID <= 0 {
		return fmt.Errorf("upsert push subscription: conversation id must be positive")
	}
	if ps.P256DH == "" || ps.Auth == "" {
		return fmt.Errorf("upsert push subscription: keys are required")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO web_push_subscriptions
		    (endpoint, conversation_id, p256dh, auth, user_agent, created_at, updated_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?
		   WHERE EXISTS (SELECT 1 FROM conversations WHERE id = ? AND status = 'active')
		 ON CONFLICT(endpoint) DO UPDATE SET
		    conversation_id = excluded.conversation_id,
		    p256dh = excluded.p256dh,
		    auth = excluded.auth,
		    user_agent = excluded.user_agent,
		    updated_at = excluded.updated_at`,
		ps.Endpoint, ps.ConversationID, ps.P256DH, ps.Auth, ps.UserAgent,
		unixMillis(now), unixMillis(now), ps.ConversationID)
	if err != nil {
		return fmt.Errorf("upsert push subscription: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("upsert push subscription: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("upsert push subscription: conversation %d not found or not active", ps.ConversationID)
	}
	return nil
}

// PushSubscriptions returns all browser push subscriptions bound to a
// conversation.
func (s *Store) PushSubscriptions(ctx context.Context, conversationID int64) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint, conversation_id, p256dh, auth, user_agent, created_at, updated_at
		   FROM web_push_subscriptions
		  WHERE conversation_id = ?
		  ORDER BY updated_at DESC, endpoint`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query push subscriptions: %w", err)
	}
	defer rows.Close()

	var out []PushSubscription
	for rows.Next() {
		var (
			ps        PushSubscription
			createdAt int64
			updatedAt int64
		)
		if err := rows.Scan(&ps.Endpoint, &ps.ConversationID, &ps.P256DH, &ps.Auth, &ps.UserAgent, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		ps.CreatedAt = fromUnixMilli(createdAt)
		ps.UpdatedAt = fromUnixMilli(updatedAt)
		out = append(out, ps)
	}
	return out, rows.Err()
}

// DeletePushSubscription removes one browser push subscription by endpoint.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM web_push_subscriptions WHERE endpoint = ?`, strings.TrimSpace(endpoint))
	if err != nil {
		return false, fmt.Errorf("delete push subscription: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete push subscription: rows affected: %w", err)
	}
	return n > 0, nil
}

// CreateRun inserts a run in status 'running'. inputMessageID is the user
// message the run answers; pass 0 for a maintenance run (compaction) that
// answers no input.
func (s *Store) CreateRun(ctx context.Context, conversationID int64, initiator, model string, inputMessageID int64) (Run, error) {
	now := nowUTC()
	var inputArg any
	var inputPtr *int64
	if inputMessageID != 0 {
		inputArg = inputMessageID
		inputPtr = &inputMessageID
	}
	// Invariant (no DB foreign keys): a run may only exist for an active
	// conversation. The guarded insert is atomic under the single writer.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (conversation_id, input_message_id, initiator, model, status, created_at)
		 SELECT ?, ?, ?, ?, 'running', ?
		 WHERE EXISTS (SELECT 1 FROM conversations WHERE id = ? AND status = 'active')`,
		conversationID, inputArg, initiator, model, unixMillis(now), conversationID)
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	} else if n == 0 {
		return Run{}, fmt.Errorf("create run: conversation %d not found or not active", conversationID)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Run{}, fmt.Errorf("create run id: %w", err)
	}
	return Run{
		ID:             id,
		ConversationID: conversationID,
		InputMessageID: inputPtr,
		Initiator:      initiator,
		Model:          model,
		Status:         "running",
		CreatedAt:      now,
	}, nil
}

// FinishRun records a terminal status, usage, optional error, and finish time.
func (s *Store) FinishRun(ctx context.Context, runID int64, status string, usage llm.Usage, errMsg string) error {
	return finishRun(ctx, s.db, runID, status, usage, errMsg)
}

// RunIDsByInputMessage returns run ids keyed by the input message they answer.
// UIs use this to group a user message with the assistant/tool messages produced
// by its run.
func (s *Store) RunIDsByInputMessage(ctx context.Context, conversationID int64, inputMessageIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64)
	ids := uniquePositiveInt64s(inputMessageIDs)
	if len(ids) == 0 {
		return out, nil
	}

	args := make([]any, 0, len(ids)+1)
	args = append(args, conversationID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT input_message_id, MAX(id) FROM runs
		 WHERE conversation_id = ? AND input_message_id IN (`+queryPlaceholders(len(ids))+`)
		 GROUP BY input_message_id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("run ids for input messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var inputID, runID int64
		if err := rows.Scan(&inputID, &runID); err != nil {
			return nil, fmt.Errorf("scan run id for input message: %w", err)
		}
		out[inputID] = runID
	}
	return out, rows.Err()
}

func finishRun(ctx context.Context, ex execer, runID int64, status string, usage llm.Usage, errMsg string) error {
	now := nowUTC()
	// Invariant: only a still-running run may be finished, and exactly one row
	// must move. The status guard makes finishing idempotent-safe and turns a
	// double-finish or a finish of a missing/already-terminal run into an error
	// instead of a silent no-op. (Bulk restart cleanup uses its own UPDATE.)
	res, err := ex.ExecContext(ctx,
		`UPDATE runs SET status = ?, input_tokens = ?, output_tokens = ?, error = ?, finished_at = ?
		 WHERE id = ? AND status = 'running'`,
		status, usage.PromptTokens, usage.CompletionTokens, nullString(errMsg), unixMillis(now), runID)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish run %d: %w", runID, err)
	}
	if n != 1 {
		return fmt.Errorf("finish run %d: not in running state", runID)
	}
	return nil
}

// CompleteRun persists a successful run in one transaction: it appends the run's
// output messages, marks the run done, and advances the conversation's coverage
// cursor past the answered input. Folding these together keeps the cursor and
// the visible transcript in agreement — a crash between them cannot leave the
// input covered with no reply, nor a reply with the input still due. It returns
// the stored output messages with ids/timestamps assigned.
func (s *Store) CompleteRun(ctx context.Context, runID, conversationID, inputMessageID int64, out []Message, usage llm.Usage) ([]Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin complete-run tx: %w", err)
	}
	stored := []Message(nil)
	if len(out) > 0 {
		stored, err = s.appendMessages(ctx, tx, out)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := finishRun(ctx, tx, runID, "done", usage, ""); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := advanceCover(ctx, tx, conversationID, inputMessageID); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit complete-run tx: %w", err)
	}
	return stored, nil
}

// FailRun marks a run failed, appends its visible failure message, and advances
// the conversation's coverage cursor past the answered input, all in one
// transaction. Advancing the cursor stops the worker from re-running the same
// poison input forever; committing it with the failure message rules out the
// half state where the run is failed but the input still looks due. Returns the
// stored failure message.
//
// This is for runs that produced a result (success or a real failure). Runs
// interrupted by a restart are cleaned up by FailRunningRuns, which does NOT
// advance the cursor: with no visible result persisted, the input stays due and
// re-runs. inputMessageID 0 leaves the cursor untouched (no input to cover).
func (s *Store) FailRun(ctx context.Context, runID, conversationID, inputMessageID int64, usage llm.Usage, errMsg string, failure Message) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("begin fail-run tx: %w", err)
	}
	if err := finishRun(ctx, tx, runID, "failed", usage, errMsg); err != nil {
		tx.Rollback()
		return Message{}, err
	}
	out, err := s.appendMessages(ctx, tx, []Message{failure})
	if err != nil {
		tx.Rollback()
		return Message{}, err
	}
	if err := advanceCover(ctx, tx, conversationID, inputMessageID); err != nil {
		tx.Rollback()
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit fail-run tx: %w", err)
	}
	return out[0], nil
}

// advanceCover moves the conversation's coverage cursor forward to throughID.
// It never moves backwards and is a no-op for throughID 0, so out-of-order or
// maintenance callers cannot un-cover already-answered input.
func advanceCover(ctx context.Context, ex execer, conversationID, throughID int64) error {
	if throughID == 0 {
		return nil
	}
	_, err := ex.ExecContext(ctx,
		`UPDATE conversations SET last_covered_user_message_id = ?
		 WHERE id = ? AND last_covered_user_message_id < ?`,
		throughID, conversationID, throughID)
	if err != nil {
		return fmt.Errorf("advance cover for conversation %d: %w", conversationID, err)
	}
	return nil
}

// MarkCovered advances the coverage cursor outside a run. It exists for callers
// that record conversation progress directly (and for tests that seed a
// transcript without driving real runs).
func (s *Store) MarkCovered(ctx context.Context, conversationID, throughID int64) error {
	return advanceCover(ctx, s.db, conversationID, throughID)
}

// CoveredThrough returns the conversation's coverage cursor: the newest user
// message id it has answered. 0 means nothing answered yet.
func (s *Store) CoveredThrough(ctx context.Context, conversationID int64) (int64, error) {
	var cursor int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_covered_user_message_id FROM conversations WHERE id = ?`, conversationID).
		Scan(&cursor)
	if err != nil {
		return 0, fmt.Errorf("covered-through for conversation %d: %w", conversationID, err)
	}
	return cursor, nil
}

// FailRunningRuns marks every still-running run as failed; called on startup to
// clean up runs interrupted by a restart. Returns the number affected.
func (s *Store) FailRunningRuns(ctx context.Context) (int64, error) {
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'failed', error = 'interrupted by restart', finished_at = ?
		 WHERE status = 'running'`, unixMillis(now))
	if err != nil {
		return 0, fmt.Errorf("fail running runs: %w", err)
	}
	return res.RowsAffected()
}

// AppendMessage appends one transcript message, setting its ID and CreatedAt.
func (s *Store) AppendMessage(ctx context.Context, m Message) (Message, error) {
	out, err := s.appendMessages(ctx, s.db, []Message{m})
	if err != nil {
		return Message{}, err
	}
	return out[0], nil
}

// AppendMessages appends several messages in one transaction.
func (s *Store) AppendMessages(ctx context.Context, ms []Message) ([]Message, error) {
	if len(ms) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin append tx: %w", err)
	}
	out, err := s.appendMessages(ctx, tx, ms)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit append tx: %w", err)
	}
	return out, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) appendMessages(ctx context.Context, ex execer, ms []Message) ([]Message, error) {
	out := make([]Message, len(ms))
	for i, m := range ms {
		if m.CreatedAt.IsZero() {
			m.CreatedAt = nowUTC()
		}
		payload, err := json.Marshal(m.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal message content: %w", err)
		}
		// Invariant: a message may only be appended to an existing conversation.
		res, err := ex.ExecContext(ctx,
			`INSERT INTO messages (conversation_id, run_id, role, source, content, created_at)
			 SELECT ?, ?, ?, ?, ?, ?
			 WHERE EXISTS (SELECT 1 FROM conversations WHERE id = ?)`,
			m.ConversationID, nullInt64(m.RunID), string(m.Role), m.Source, string(payload), unixMillis(m.CreatedAt), m.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("append message: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("append message: %w", err)
		} else if n == 0 {
			return nil, fmt.Errorf("append message: conversation %d not found", m.ConversationID)
		}
		if m.ID, err = res.LastInsertId(); err != nil {
			return nil, fmt.Errorf("append message id: %w", err)
		}
		out[i] = m
	}
	return out, nil
}

// LastMessage returns the newest message in the conversation. The bool is false
// when the conversation has no messages.
func (s *Store) LastMessage(ctx context.Context, conversationID int64) (Message, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`, conversationID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, fmt.Errorf("last message: %w", err)
	}
	return m, true, nil
}

// NextDueInput returns the user message that should drive the next run: the
// newest user message whose id is past the conversation's coverage cursor. The
// bool is false when no run is due. Earlier uncovered user messages (a burst)
// are intentionally not each answered separately — the newest is the input and
// the rest become history, then the cursor covers them all at once.
//
// This replaces the older "is the newest transcript row a user message" check,
// which could not tell an unanswered user message from one that was buried under
// a later assistant reply, and so dropped messages appended while a run was in
// flight.
func (s *Store) NextDueInput(ctx context.Context, conversationID int64) (Message, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM messages
		 WHERE conversation_id = ? AND role = ?
		   AND id > (SELECT last_covered_user_message_id FROM conversations WHERE id = ?)
		 ORDER BY id DESC LIMIT 1`,
		conversationID, string(llm.RoleUser), conversationID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, fmt.Errorf("next due input: %w", err)
	}
	return m, true, nil
}

// logicalKeyExpr is each message's position in conversational (causal) order,
// as opposed to physical append order (messages.id). A message produced by a run
// (assistant/tool/failure rows) belongs right after the user input that run
// answered, so it sorts at input_message_id*2 + 1; a user message or external
// event sorts at its own id*2. The *2 leaves the odd slot for "the answer to
// this input", and the +1 places that answer immediately after its input.
//
// This matters when a user message arrives while a run is in flight: the reply
// to the earlier input is appended physically after the newer user message, but
// causally belongs before it. Ordering history by this key (ties broken by
// physical id) keeps an answer next to its question and, crucially, keeps that
// answer in the next run's context instead of being cut by a physical-id window.
const logicalKeyExpr = `(CASE WHEN m.run_id IS NOT NULL
	THEN COALESCE(r.input_message_id, m.id) * 2 + 1
	ELSE m.id * 2 END)`

// MessagesForInput returns the post-summary context window for a run answering
// the message with id inputID: every message after afterID whose logical
// position is at or before the input, in logical order. The input itself is the
// last row. Messages that are causally later than the input — a newer user
// message, or the reply to one — are excluded so they remain for their own run.
func (s *Store) MessagesForInput(ctx context.Context, conversationID, afterID, inputID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.conversation_id, m.run_id, m.role, m.source, m.content, m.created_at
		 FROM messages m
		 LEFT JOIN runs r ON m.run_id = r.id
		 WHERE m.conversation_id = ? AND m.id > ? AND `+logicalKeyExpr+` <= ?
		 ORDER BY `+logicalKeyExpr+` ASC, m.id ASC`,
		conversationID, afterID, inputID*2)
	if err != nil {
		return nil, fmt.Errorf("messages for input: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessagesAfter returns messages with id > afterID in the conversation,
// ascending. Pass afterID 0 for all messages.
func (s *Store) MessagesAfter(ctx context.Context, conversationID, afterID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM messages WHERE conversation_id = ? AND id > ? ORDER BY id ASC`,
		conversationID, afterID)
	if err != nil {
		return nil, fmt.Errorf("messages after: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessagesTail returns a semantic tail page in ascending order. limit is a
// target size, not a hard maximum: if the raw tail starts inside one or more
// runs, the page expands backward to the oldest input message for those runs so
// UI grouping can render a complete user -> work block. Expansion is capped by
// maxExpandedMessagePage. hasMore is true when at least one older row exists.
func (s *Store) MessagesTail(ctx context.Context, conversationID int64, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("messages tail: limit must be positive")
	}
	msgs, err := s.loadMessagesTail(ctx, conversationID, 0, limit)
	if err != nil {
		return nil, false, err
	}
	if len(msgs) == 0 {
		return nil, false, nil
	}

	lowerID := msgs[0].ID
	runStartID, err := s.oldestRunInputForMessages(ctx, conversationID, msgs)
	if err != nil {
		return nil, false, err
	}
	if runStartID > 0 && runStartID < lowerID {
		lowerID = runStartID
		expandedLimit := maxExpandedMessagePage
		if expandedLimit < limit {
			expandedLimit = limit
		}
		msgs, err = s.loadMessagesTail(ctx, conversationID, lowerID, expandedLimit)
		if err != nil {
			return nil, false, err
		}
	}

	hasMore, err := s.hasMessageBefore(ctx, conversationID, msgs[0].ID)
	if err != nil {
		return nil, false, err
	}
	return msgs, hasMore, nil
}

func (s *Store) loadMessagesTail(ctx context.Context, conversationID, lowerID int64, limit int) ([]Message, error) {
	where := `conversation_id = ?`
	args := []any{conversationID}
	if lowerID > 0 {
		where += ` AND id >= ?`
		args = append(args, lowerID)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM (
		   SELECT id, conversation_id, run_id, role, source, content, created_at
		   FROM messages
		   WHERE `+where+`
		   ORDER BY id DESC
		   LIMIT ?
		 )
		 ORDER BY id ASC`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("messages tail: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MessagesBefore returns a semantic page older than beforeID (id < beforeID) in
// ascending order, for upward "load older" pagination. limit is a target size,
// not a hard maximum: if the raw page starts inside one or more runs, the page
// expands backward to the oldest input message for those runs so UI grouping can
// render a complete user -> work block. Expansion is capped by
// maxExpandedMessagePage. hasMore is true when at least one even-older row
// exists.
func (s *Store) MessagesBefore(ctx context.Context, conversationID, beforeID int64, limit int) ([]Message, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("messages before: limit must be positive")
	}
	msgs, err := s.loadMessagesBefore(ctx, conversationID, beforeID, 0, limit)
	if err != nil {
		return nil, false, err
	}
	if len(msgs) == 0 {
		return nil, false, nil
	}

	lowerID := msgs[0].ID
	runStartID, err := s.oldestRunInputForMessages(ctx, conversationID, msgs)
	if err != nil {
		return nil, false, err
	}
	if runStartID > 0 && runStartID < lowerID {
		lowerID = runStartID
		expandedLimit := maxExpandedMessagePage
		if expandedLimit < limit {
			expandedLimit = limit
		}
		msgs, err = s.loadMessagesBefore(ctx, conversationID, beforeID, lowerID, expandedLimit)
		if err != nil {
			return nil, false, err
		}
	}

	hasMore, err := s.hasMessageBefore(ctx, conversationID, msgs[0].ID)
	if err != nil {
		return nil, false, err
	}
	return msgs, hasMore, nil
}

func (s *Store) loadMessagesBefore(ctx context.Context, conversationID, beforeID, lowerID int64, limit int) ([]Message, error) {
	where := `conversation_id = ? AND id < ?`
	args := []any{conversationID, beforeID}
	if lowerID > 0 {
		where += ` AND id >= ?`
		args = append(args, lowerID)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM (
		   SELECT id, conversation_id, run_id, role, source, content, created_at
		   FROM messages
		   WHERE `+where+`
		   ORDER BY id DESC
		   LIMIT ?
		 )
		 ORDER BY id ASC`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("messages before: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) oldestRunInputForMessages(ctx context.Context, conversationID int64, msgs []Message) (int64, error) {
	runIDs := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.RunID != nil {
			runIDs = append(runIDs, *m.RunID)
		}
	}
	runIDs = uniquePositiveInt64s(runIDs)
	if len(runIDs) == 0 {
		return 0, nil
	}

	args := make([]any, 0, len(runIDs)+1)
	args = append(args, conversationID)
	for _, id := range runIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT input_message_id FROM runs
		 WHERE conversation_id = ? AND id IN (`+queryPlaceholders(len(runIDs))+`)
		   AND input_message_id IS NOT NULL`,
		args...)
	if err != nil {
		return 0, fmt.Errorf("run inputs for messages: %w", err)
	}
	defer rows.Close()

	var min int64
	for rows.Next() {
		var inputID int64
		if err := rows.Scan(&inputID); err != nil {
			return 0, fmt.Errorf("scan run input: %w", err)
		}
		if inputID > 0 && (min == 0 || inputID < min) {
			min = inputID
		}
	}
	return min, rows.Err()
}

func (s *Store) hasMessageBefore(ctx context.Context, conversationID, beforeID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM messages WHERE conversation_id = ? AND id < ? LIMIT 1`,
		conversationID, beforeID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check older messages: %w", err)
	}
	return true, nil
}

// SearchMessages searches raw transcript rows in one conversation, newest first.
// It is intentionally simple and bounded; the tool layer uses it for occasional
// recall after compaction, not for analytics.
func (s *Store) SearchMessages(ctx context.Context, conversationID int64, query string, limit int) ([]Message, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search messages: query is required")
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}
	pattern := likeLiteralPattern(query)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM messages
		 WHERE conversation_id = ?
		   AND content LIKE ? ESCAPE '\'
		 ORDER BY id DESC
		 LIMIT ?`,
		conversationID, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendSummary inserts a new rolling summary row (the newest becomes live).
// It sets the returned ID and CreatedAt.
func (s *Store) AppendSummary(ctx context.Context, sm Summary) (Summary, error) {
	if sm.CreatedAt.IsZero() {
		sm.CreatedAt = nowUTC()
	}
	// Invariant: the summary's through_message_id must belong to the same
	// conversation it summarizes, or the post-summary window would be computed
	// against an id from another conversation.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO summaries (conversation_id, through_message_id, content, created_at)
		 SELECT ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM messages WHERE id = ? AND conversation_id = ?)`,
		sm.ConversationID, sm.ThroughMessageID, sm.Content, unixMillis(sm.CreatedAt),
		sm.ThroughMessageID, sm.ConversationID)
	if err != nil {
		return Summary{}, fmt.Errorf("append summary: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Summary{}, fmt.Errorf("append summary: %w", err)
	} else if n == 0 {
		return Summary{}, fmt.Errorf("append summary: through_message_id %d not in conversation %d", sm.ThroughMessageID, sm.ConversationID)
	}
	if sm.ID, err = res.LastInsertId(); err != nil {
		return Summary{}, fmt.Errorf("append summary id: %w", err)
	}
	return sm, nil
}

// LatestSummary returns the newest summary for the conversation. The bool is
// false when none exists.
func (s *Store) LatestSummary(ctx context.Context, conversationID int64) (Summary, bool, error) {
	var (
		sm        Summary
		createdAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, through_message_id, content, created_at
		 FROM summaries WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`, conversationID).
		Scan(&sm.ID, &sm.ConversationID, &sm.ThroughMessageID, &sm.Content, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, false, nil
	}
	if err != nil {
		return Summary{}, false, fmt.Errorf("latest summary: %w", err)
	}
	sm.CreatedAt = fromUnixMilli(createdAt)
	return sm, true, nil
}

func uniquePositiveInt64s(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func queryPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(row scanner) (Message, error) {
	var (
		m         Message
		runID     sql.NullInt64
		role      string
		payload   string
		createdAt int64
	)
	if err := row.Scan(&m.ID, &m.ConversationID, &runID, &role, &m.Source, &payload, &createdAt); err != nil {
		return Message{}, err
	}
	if runID.Valid {
		m.RunID = &runID.Int64
	}
	m.Role = llm.Role(role)
	if err := json.Unmarshal([]byte(payload), &m.Content); err != nil {
		return Message{}, fmt.Errorf("unmarshal message content: %w", err)
	}
	m.CreatedAt = fromUnixMilli(createdAt)
	return m, nil
}

func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func likeLiteralPattern(s string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}
