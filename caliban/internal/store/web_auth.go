package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SetWebAuthPasswordHash replaces the single web UI password hash.
func (s *Store) SetWebAuthPasswordHash(ctx context.Context, hash string) error {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return fmt.Errorf("set web auth password: hash is required")
	}
	now := unixMillis(nowUTC())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO web_auth (id, password_hash, updated_at)
		 VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		    password_hash = excluded.password_hash,
		    updated_at = excluded.updated_at`,
		hash, now)
	if err != nil {
		return fmt.Errorf("set web auth password: %w", err)
	}
	return nil
}

// WebAuthPasswordHash returns the configured web UI password hash, if any.
func (s *Store) WebAuthPasswordHash(ctx context.Context) (string, bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM web_auth WHERE id = 1`).Scan(&hash)
	if err == nil {
		return hash, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, fmt.Errorf("read web auth password: %w", err)
}

// CreateWebSession stores a hashed web session token.
func (s *Store) CreateWebSession(ctx context.Context, sess WebSession) error {
	sess.TokenHash = strings.TrimSpace(sess.TokenHash)
	if sess.TokenHash == "" {
		return fmt.Errorf("create web session: token hash is required")
	}
	if sess.ExpiresAt.IsZero() {
		return fmt.Errorf("create web session: expires_at is required")
	}
	now := nowUTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastSeenAt.IsZero() {
		sess.LastSeenAt = sess.CreatedAt
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO web_sessions (token_hash, user_agent, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.TokenHash,
		strings.TrimSpace(sess.UserAgent),
		unixMillis(sess.CreatedAt),
		unixMillis(sess.LastSeenAt),
		unixMillis(sess.ExpiresAt))
	if err != nil {
		return fmt.Errorf("create web session: %w", err)
	}
	return nil
}

// WebSession returns a stored web session by hashed token.
func (s *Store) WebSession(ctx context.Context, tokenHash string) (WebSession, bool, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return WebSession{}, false, nil
	}
	var sess WebSession
	var createdAt, lastSeenAt, expiresAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, user_agent, created_at, last_seen_at, expires_at
		   FROM web_sessions
		  WHERE token_hash = ?`,
		tokenHash).Scan(&sess.TokenHash, &sess.UserAgent, &createdAt, &lastSeenAt, &expiresAt)
	if err == nil {
		sess.CreatedAt = fromUnixMilli(createdAt)
		sess.LastSeenAt = fromUnixMilli(lastSeenAt)
		sess.ExpiresAt = fromUnixMilli(expiresAt)
		return sess, true, nil
	}
	if err == sql.ErrNoRows {
		return WebSession{}, false, nil
	}
	return WebSession{}, false, fmt.Errorf("read web session: %w", err)
}

// TouchWebSession updates last_seen_at for an existing web session.
func (s *Store) TouchWebSession(ctx context.Context, tokenHash string, at time.Time) error {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil
	}
	if at.IsZero() {
		at = nowUTC()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE web_sessions SET last_seen_at = ? WHERE token_hash = ?`,
		unixMillis(at), tokenHash)
	if err != nil {
		return fmt.Errorf("touch web session: %w", err)
	}
	return nil
}

// DeleteWebSession deletes one web session by hashed token.
func (s *Store) DeleteWebSession(ctx context.Context, tokenHash string) (bool, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return false, fmt.Errorf("delete web session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteExpiredWebSessions deletes sessions whose expiry is at or before now.
func (s *Store) DeleteExpiredWebSessions(ctx context.Context, at time.Time) (int64, error) {
	if at.IsZero() {
		at = nowUTC()
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at <= ?`, unixMillis(at))
	if err != nil {
		return 0, fmt.Errorf("delete expired web sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
