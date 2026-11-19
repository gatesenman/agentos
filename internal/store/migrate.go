package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// migrations[i] upgrades the schema from version i to i+1. Append only;
// never edit a released step. The current version is stored in the
// settings table under SettingSchemaVersion.
var migrations = []string{
	// v0 -> v1: phase-1 schema.
	`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, hostname TEXT, os TEXT, arch TEXT, version TEXT,
  remote_addr TEXT, capabilities TEXT, last_seen INTEGER, created_at INTEGER);
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY, node_id TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL,
  created_at INTEGER, updated_at INTEGER);
CREATE INDEX IF NOT EXISTS agents_node ON agents(node_id);`,
	// v1 -> v2: persistent admin sessions (keyed by SHA-256 of the token).
	`CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at);`,
	// v2 -> v3: environments (phase 2). config = non-secret JSON, secrets =
	// AES-GCM sealed JSON of the x-secret fields; node credentials (hashed
	// join/per-env tokens) and revoked node ids.
	`CREATE TABLE IF NOT EXISTS environments (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, provider TEXT NOT NULL, config TEXT NOT NULL DEFAULT '{}',
  secrets TEXT NOT NULL DEFAULT '', stack TEXT NOT NULL DEFAULT '', node_id TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '{}', state TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
  created_at INTEGER, updated_at INTEGER);
CREATE INDEX IF NOT EXISTS environments_node ON environments(node_id);
CREATE TABLE IF NOT EXISTS node_credentials (
  env_id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, node_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER, used_at INTEGER);
CREATE TABLE IF NOT EXISTS revoked_nodes (node_id TEXT PRIMARY KEY, revoked_at INTEGER);`,
	// v3 -> v4: federated agents (A2A / ACP remote registrations).
	`CREATE TABLE IF NOT EXISTS fed_agents (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL, endpoint TEXT NOT NULL,
  auth_scheme TEXT NOT NULL DEFAULT 'none', auth_header TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL DEFAULT '',
  agent_card TEXT NOT NULL DEFAULT 'null', capabilities TEXT NOT NULL DEFAULT '[]',
  metadata TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'unknown', last_error TEXT NOT NULL DEFAULT '',
  last_seen INTEGER, created_at INTEGER, updated_at INTEGER);
CREATE INDEX IF NOT EXISTS fed_agents_proto ON fed_agents(protocol);`,
}

// SchemaVersion is the schema version produced by this build.
var SchemaVersion = len(migrations)

// ErrSchemaTooNew is returned when the database was created by a newer build.
var ErrSchemaTooNew = errors.New("database schema is newer than this binary supports")

func (s *sqliteStore) schemaVersion(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='settings'`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, SettingSchemaVersion).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // settings exists but unversioned: replay (all steps are idempotent)
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *sqliteStore) migrate() error {
	ctx := context.Background()
	cur, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("%w (db v%d, binary v%d)", ErrSchemaTooNew, cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("schema v%d->v%d: %w", v, v+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			SettingSchemaVersion, strconv.Itoa(v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
