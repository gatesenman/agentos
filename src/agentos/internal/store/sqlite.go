package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/agentos/agentos/internal/api"
	_ "modernc.org/sqlite" // pure-Go driver, name "sqlite"
)

type sqliteStore struct{ db *sql.DB }

// OpenSQLite opens (and migrates) the database file at path.
func OpenSQLite(path string) (Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: serialize writers
	s := &sqliteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }
func fromMs(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func (s *sqliteStore) scanNode(row interface{ Scan(...any) error }) (api.Node, error) {
	var n api.Node
	var caps string
	var last, created int64
	err := row.Scan(&n.ID, &n.Name, &n.Hostname, &n.OS, &n.Arch, &n.Version, &n.RemoteAddr, &caps, &last, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}
	_ = json.Unmarshal([]byte(caps), &n.Capabilities)
	n.LastSeen, n.CreatedAt = fromMs(last), fromMs(created)
	n.Status = api.NodeOffline
	return n, nil
}

const nodeCols = `id,name,hostname,os,arch,version,remote_addr,capabilities,last_seen,created_at`

// nodeSelect tolerates NULLs in optional columns (rows written by older builds or by hand).
const nodeSelect = `id,name,COALESCE(hostname,''),COALESCE(os,''),COALESCE(arch,''),COALESCE(version,''),
 COALESCE(remote_addr,''),COALESCE(capabilities,''),COALESCE(last_seen,0),COALESCE(created_at,0)`

func (s *sqliteStore) ListNodes(ctx context.Context) ([]api.Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeSelect+` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Node{}
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetNode(ctx context.Context, id string) (api.Node, error) {
	return s.scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeSelect+` FROM nodes WHERE id=?`, id))
}

func (s *sqliteStore) UpsertNode(ctx context.Context, n api.Node) error {
	caps, _ := json.Marshal(n.Capabilities)
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO nodes(`+nodeCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, hostname=excluded.hostname, os=excluded.os, arch=excluded.arch,
 version=excluded.version, remote_addr=excluded.remote_addr, capabilities=excluded.capabilities, last_seen=excluded.last_seen`,
		n.ID, n.Name, n.Hostname, n.OS, n.Arch, n.Version, n.RemoteAddr, string(caps), ms(n.LastSeen), ms(n.CreatedAt))
	return err
}

func (s *sqliteStore) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) ListAgents(ctx context.Context, nodeID string) ([]api.AgentSpec, error) {
	q, args := `SELECT spec FROM agents ORDER BY name`, []any{}
	if nodeID != "" {
		q, args = `SELECT spec FROM agents WHERE node_id=? ORDER BY name`, []any{nodeID}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AgentSpec{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var a api.AgentSpec
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *sqliteStore) GetAgent(ctx context.Context, id string) (api.AgentSpec, error) {
	var raw string
	var a api.AgentSpec
	err := s.db.QueryRowContext(ctx, `SELECT spec FROM agents WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	return a, json.Unmarshal([]byte(raw), &a)
}

func (s *sqliteStore) CreateAgent(ctx context.Context, a api.AgentSpec) error {
	raw, _ := json.Marshal(a)
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(id,node_id,name,spec,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		a.ID, a.NodeID, a.Name, string(raw), ms(a.CreatedAt), ms(a.UpdatedAt))
	return err
}

func (s *sqliteStore) UpdateAgent(ctx context.Context, a api.AgentSpec) error {
	raw, _ := json.Marshal(a)
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET node_id=?, name=?, spec=?, updated_at=? WHERE id=?`,
		a.NodeID, a.Name, string(raw), ms(a.UpdatedAt), a.ID)
	return affected(res, err)
}

func (s *sqliteStore) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id=?`, id)
	return affected(res, err)
}

func (s *sqliteStore) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *sqliteStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
