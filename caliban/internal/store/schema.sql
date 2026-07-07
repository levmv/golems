-- Draft schema for the caliban transcript model.
-- Conversation, run, and task persistence for Caliban.
--
-- The main Telegram chat is conversation 1 by default. Other transports can
-- use their own default conversation ids, and web sessions can create ordinary
-- conversations later.
--
-- pkg/tasks/sqlite owns its own tables in the same database.
--
-- No foreign keys by design: cross-table references are noted in column
-- comments, and referential consistency is the store layer's job, enforced
-- by code and tests, not the database.

CREATE TABLE IF NOT EXISTS conversations (
    id            INTEGER PRIMARY KEY,
    uuid          TEXT NOT NULL,
    parent_run_id INTEGER,                        -- runs.id; NULL for top-level chats
    status        TEXT NOT NULL DEFAULT 'active', -- active | archived
    created_at    INTEGER NOT NULL,               -- unix milliseconds, UTC
    -- The newest user message id this conversation has answered. A run is "due"
    -- while a user message with a larger id exists. This makes conversation
    -- progress explicit and durable instead of inferring it from "is the last
    -- transcript row a user message", which lost messages appended mid-run.
    last_covered_user_message_id INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS runs (
    id               INTEGER PRIMARY KEY,
    conversation_id  INTEGER NOT NULL,            -- conversations.id
    input_message_id INTEGER,                     -- messages.id the run answers; NULL for maintenance runs (compaction)
    initiator       TEXT NOT NULL,                -- user | schedule | agent
    model           TEXT NOT NULL,
    status          TEXT NOT NULL,                -- running | done | failed | cancelled
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    created_at      INTEGER NOT NULL,             -- unix milliseconds, UTC
    finished_at     INTEGER                       -- unix milliseconds, UTC; NULL until terminal
);

-- The append-only transcript: user input, assistant output, tool calls and
-- results. One row per model-facing llm.Message; content is a JSON object
-- {text, reasoning, tool_calls, tool_call_id}. UI shapes are derived from
-- this, not stored.
CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL,             -- conversations.id
    run_id          INTEGER,                      -- runs.id; NULL when appended outside a run
    role            TEXT NOT NULL,                -- user | assistant | tool | event
    source          TEXT NOT NULL DEFAULT '',     -- telegram | web | reminder | schedule | delegate | ''
    content         TEXT NOT NULL,
    created_at      INTEGER NOT NULL              -- unix milliseconds, UTC (sub-second for work-duration display)
);

-- Rolling compaction summaries. The newest row per conversation is the live
-- one; older rows are kept as history. through_message_id marks how far the
-- summary has folded the transcript.
CREATE TABLE IF NOT EXISTS summaries (
    id                 INTEGER PRIMARY KEY,
    conversation_id    INTEGER NOT NULL,          -- conversations.id
    through_message_id INTEGER NOT NULL,          -- messages.id
    content            TEXT NOT NULL,
    created_at         INTEGER NOT NULL           -- unix milliseconds, UTC
);

-- Browser Web Push subscriptions for web/PWA transports. Endpoints are issued
-- by browser push services and are globally unique enough to use as the key.
CREATE TABLE IF NOT EXISTS web_push_subscriptions (
    endpoint        TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL,             -- conversations.id
    p256dh          TEXT NOT NULL,
    auth            TEXT NOT NULL,
    user_agent      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,             -- unix milliseconds, UTC
    updated_at      INTEGER NOT NULL              -- unix milliseconds, UTC
);

-- Web UI single-user auth state. The password hash is mutable operational state,
-- so it lives in the database rather than the static config file.
CREATE TABLE IF NOT EXISTS web_auth (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    updated_at    INTEGER NOT NULL                -- unix milliseconds, UTC
);

CREATE TABLE IF NOT EXISTS web_sessions (
    token_hash   TEXT PRIMARY KEY,
    user_agent   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,                -- unix milliseconds, UTC
    last_seen_at INTEGER NOT NULL,                -- unix milliseconds, UTC
    expires_at   INTEGER NOT NULL                 -- unix milliseconds, UTC
);

-- Managed background shell/task processes. The full output lives in output_path
-- outside the workspace so it is not auto-committed with memory/work files.
CREATE TABLE IF NOT EXISTS background_tasks (
    id                TEXT PRIMARY KEY,
    kind              TEXT NOT NULL,               -- shell
    command           TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL,               -- running | completed | failed | killed | timed_out | lost
    conversation_id   INTEGER NOT NULL DEFAULT 0,  -- conversations.id; 0 when started outside a run
    started_by_run_id INTEGER NOT NULL DEFAULT 0,  -- runs.id; 0 when started outside a run
    pid               INTEGER,
    exit_code         INTEGER,
    error             TEXT,
    stop_reason       TEXT,
    output_path       TEXT NOT NULL,
    output_offset     INTEGER NOT NULL DEFAULT 0,
    notified          INTEGER NOT NULL DEFAULT 0,
    timeout_seconds   INTEGER NOT NULL DEFAULT 0,
    started_at        INTEGER NOT NULL,            -- unix milliseconds, UTC
    finished_at       INTEGER,                     -- unix milliseconds, UTC; NULL until terminal
    updated_at        INTEGER NOT NULL             -- unix milliseconds, UTC
);

CREATE INDEX IF NOT EXISTS idx_runs_conversation ON runs(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, id);
CREATE INDEX IF NOT EXISTS idx_summaries_conversation ON summaries(conversation_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_uuid ON conversations(uuid);
CREATE INDEX IF NOT EXISTS idx_web_push_subscriptions_conversation ON web_push_subscriptions(conversation_id);
CREATE INDEX IF NOT EXISTS idx_web_sessions_expires_at ON web_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_background_tasks_status ON background_tasks(status, started_at);
CREATE INDEX IF NOT EXISTS idx_background_tasks_conversation ON background_tasks(conversation_id, started_at);
