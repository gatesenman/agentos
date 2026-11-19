package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// EnvRow is a persisted environment: the public record (Config holds only
// non-secret fields) plus the sealed secret blob.
type EnvRow struct {
	env.Record
	Secrets string // opaque (AES-GCM sealed JSON), "" when none
}

// NodeCredential binds a hashed node token to an environment. NodeID is empty
// until the first node connects with it (one-time join), then fixed.
type NodeCredential struct {
	EnvID     string
	TokenHash string
	NodeID    string
	CreatedAt time.Time
	UsedAt    time.Time
}

// EnvStore is an optional extension implemented by the SQLite store (kept
// separate from the frozen Store interface; discover with a type assertion).
type EnvStore interface {
	ListEnvs(ctx context.Context) ([]EnvRow, error)
	GetEnv(ctx context.Context, id string) (EnvRow, error)
	CreateEnv(ctx context.Context, e EnvRow) error
	UpdateEnv(ctx context.Context, e EnvRow) error // all fields
	DeleteEnv(ctx context.Context, id string) error

	// PutNodeCredential creates or replaces the credential of c.EnvID.
	PutNodeCredential(ctx context.Context, c NodeCredential) error
	GetNodeCredentialByHash(ctx context.Context, hash string) (NodeCredential, error)
	GetNodeCredential(ctx context.Context, envID string) (NodeCredential, error)
	// BindNodeCredential sets node_id (only if still empty or equal) and used_at.
	BindNodeCredential(ctx context.Context, envID, nodeID string) error
	DeleteNodeCredential(ctx context.Context, envID string) error

	RevokeNode(ctx context.Context, nodeID string) error
	UnrevokeNode(ctx context.Context, nodeID string) error
	NodeRevoked(ctx context.Context, nodeID string) (bool, error)
}

var _ EnvStore = (*sqliteStore)(nil)

const envCols = `id,name,provider,config,secrets,stack,node_id,labels,state,error,created_at,updated_at`

func scanEnv(row interface{ Scan(...any) error }) (EnvRow, error) {
	var e EnvRow
	var cfg, labels string
	var created, updated int64
	var state string
	err := row.Scan(&e.ID, &e.Name, &e.Provider, &cfg, &e.Secrets, &e.Stack, &e.NodeID, &labels, &state, &e.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Config = json.RawMessage(cfg)
	if labels != "" && labels != "{}" {
		json.Unmarshal([]byte(labels), &e.Labels)
	}
	e.State = env.State(state)
	e.CreatedAt, e.UpdatedAt = fromMs(created), fromMs(updated)
	return e, nil
}

func envArgs(e EnvRow) []any {
	cfg := string(e.Config)
	if cfg == "" {
		cfg = "{}"
	}
	labels := "{}"
	if len(e.Labels) > 0 {
		b, _ := json.Marshal(e.Labels)
		labels = string(b)
	}
	return []any{e.ID, e.Name, e.Provider, cfg, e.Secrets, e.Stack, e.NodeID, labels, string(e.State), e.Error, ms(e.CreatedAt), ms(e.UpdatedAt)}
}

func (s *sqliteStore) ListEnvs(ctx context.Context) ([]EnvRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+envCols+` FROM environments ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EnvRow{}
	for rows.Next() {
		e, err := scanEnv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetEnv(ctx context.Context, id string) (EnvRow, error) {
	return scanEnv(s.db.QueryRowContext(ctx, `SELECT `+envCols+` FROM environments WHERE id=?`, id))
}

func (s *sqliteStore) CreateEnv(ctx context.Context, e EnvRow) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO environments(`+envCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, envArgs(e)...)
	return err
}

func (s *sqliteStore) UpdateEnv(ctx context.Context, e EnvRow) error {
	a := envArgs(e)
	res, err := s.db.ExecContext(ctx, `UPDATE environments SET name=?,provider=?,config=?,secrets=?,stack=?,node_id=?,labels=?,state=?,error=?,created_at=?,updated_at=? WHERE id=?`,
		append(a[1:], a[0])...)
	return affected(res, err)
}

func (s *sqliteStore) DeleteEnv(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM environments WHERE id=?`, id)
	return affected(res, err)
}

func (s *sqliteStore) PutNodeCredential(ctx context.Context, c NodeCredential) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_credentials(env_id,token_hash,node_id,created_at,used_at) VALUES(?,?,?,?,?)
ON CONFLICT(env_id) DO UPDATE SET token_hash=excluded.token_hash, node_id=excluded.node_id, created_at=excluded.created_at, used_at=excluded.used_at`,
		c.EnvID, c.TokenHash, c.NodeID, ms(c.CreatedAt), ms(c.UsedAt))
	return err
}

func scanCred(row *sql.Row) (NodeCredential, error) {
	var c NodeCredential
	var created, used int64
	err := row.Scan(&c.EnvID, &c.TokenHash, &c.NodeID, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt, c.UsedAt = fromMs(created), fromMs(used)
	return c, err
}

func (s *sqliteStore) GetNodeCredentialByHash(ctx context.Context, hash string) (NodeCredential, error) {
	return scanCred(s.db.QueryRowContext(ctx, `SELECT env_id,token_hash,node_id,created_at,used_at FROM node_credentials WHERE token_hash=?`, hash))
}

func (s *sqliteStore) GetNodeCredential(ctx context.Context, envID string) (NodeCredential, error) {
	return scanCred(s.db.QueryRowContext(ctx, `SELECT env_id,token_hash,node_id,created_at,used_at FROM node_credentials WHERE env_id=?`, envID))
}

func (s *sqliteStore) BindNodeCredential(ctx context.Context, envID, nodeID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE node_credentials SET node_id=?, used_at=? WHERE env_id=? AND (node_id='' OR node_id=?)`,
		nodeID, ms(time.Now()), envID, nodeID)
	return affected(res, err)
}

func (s *sqliteStore) DeleteNodeCredential(ctx context.Context, envID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM node_credentials WHERE env_id=?`, envID)
	return err
}

func (s *sqliteStore) RevokeNode(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO revoked_nodes(node_id,revoked_at) VALUES(?,?)`, nodeID, ms(time.Now()))
	return err
}

func (s *sqliteStore) UnrevokeNode(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM revoked_nodes WHERE node_id=?`, nodeID)
	return err
}

func (s *sqliteStore) NodeRevoked(ctx context.Context, nodeID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM revoked_nodes WHERE node_id=?`, nodeID).Scan(&n)
	return n > 0, err
}
