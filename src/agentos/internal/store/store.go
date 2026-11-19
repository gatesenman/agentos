// Package store defines persistence for the server. Owned by module M7
// (auth & persistence). The interface is a FROZEN contract; the SQLite
// implementation may evolve freely.
package store

import (
	"context"
	"errors"

	"github.com/agentos/agentos/internal/api"
)

var ErrNotFound = errors.New("not found")

// Well-known setting keys.
const (
	SettingAdminPasswordHash = "admin_password_hash" // bcrypt
	SettingNodeToken         = "node_token"
	SettingSchemaVersion     = "schema_version"
)

type Store interface {
	// Nodes. UpsertNode inserts or updates all persisted fields of n
	// (Host and Status are runtime-only and ignored).
	ListNodes(ctx context.Context) ([]api.Node, error)
	GetNode(ctx context.Context, id string) (api.Node, error)
	UpsertNode(ctx context.Context, n api.Node) error
	DeleteNode(ctx context.Context, id string) error

	// Agents. nodeID "" lists all.
	ListAgents(ctx context.Context, nodeID string) ([]api.AgentSpec, error)
	GetAgent(ctx context.Context, id string) (api.AgentSpec, error)
	CreateAgent(ctx context.Context, a api.AgentSpec) error
	UpdateAgent(ctx context.Context, a api.AgentSpec) error
	DeleteAgent(ctx context.Context, id string) error

	// Settings (small key/value strings). Missing key => ErrNotFound.
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	Close() error
}
