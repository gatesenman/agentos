package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a persisted admin session. The ID is an opaque key chosen by
// the caller (auth stores a SHA-256 of the bearer token, never the token).
type Session struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

// SessionStore is an optional extension implemented by the SQLite store.
// It is deliberately separate from the frozen Store interface; callers
// discover it with a type assertion.
type SessionStore interface {
	CreateSession(ctx context.Context, sess Session) error
	// GetSession returns ErrNotFound for unknown ids (expired rows are
	// returned as-is; the caller decides).
	GetSession(ctx context.Context, id string) (Session, error)
	// TouchSession updates last_seen and expires_at (sliding renewal).
	TouchSession(ctx context.Context, id string, lastSeen, expiresAt time.Time) error
	DeleteSession(ctx context.Context, id string) error
	// DeleteSessionsExcept removes every session but keepID ("" = all).
	DeleteSessionsExcept(ctx context.Context, keepID string) (int64, error)
	// PurgeExpiredSessions removes sessions that expired before now.
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

var _ SessionStore = (*sqliteStore)(nil)

func (s *sqliteStore) CreateSession(ctx context.Context, x Session) error {
	if x.CreatedAt.IsZero() {
		x.CreatedAt = time.Now()
	}
	if x.LastSeen.IsZero() {
		x.LastSeen = x.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id,created_at,expires_at,last_seen) VALUES(?,?,?,?)`,
		x.ID, ms(x.CreatedAt), ms(x.ExpiresAt), ms(x.LastSeen))
	return err
}

func (s *sqliteStore) GetSession(ctx context.Context, id string) (Session, error) {
	x := Session{ID: id}
	var c, e, l int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at,expires_at,last_seen FROM sessions WHERE id=?`, id).Scan(&c, &e, &l)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	x.CreatedAt, x.ExpiresAt, x.LastSeen = fromMs(c), fromMs(e), fromMs(l)
	return x, nil
}

func (s *sqliteStore) TouchSession(ctx context.Context, id string, lastSeen, expiresAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen=?, expires_at=? WHERE id=?`, ms(lastSeen), ms(expiresAt), id)
	return affected(res, err)
}

func (s *sqliteStore) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return affected(res, err)
}

func (s *sqliteStore) DeleteSessionsExcept(ctx context.Context, keepID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id<>?`, keepID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *sqliteStore) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<?`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
