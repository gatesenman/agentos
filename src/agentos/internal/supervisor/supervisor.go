// Package supervisor runs agent processes on a node: spawn in an isolated
// workspace, capture output into logs.Store, stop/restart, auto-restart.
// Owned by module M1. The Supervisor interface is a FROZEN contract.
package supervisor

import (
	"errors"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/logs"
)

var ErrUnknownAgent = errors.New("unknown agent")

type Supervisor interface {
	// Apply creates or updates an agent spec. A running agent keeps running
	// with the old config until restarted. If spec.AutoStart and not running,
	// it is started.
	Apply(spec api.AgentSpec) (api.AgentStatus, error)
	// Remove stops the agent (if running) and forgets it. Workspace is kept.
	Remove(id string) error
	Start(id string) (api.AgentStatus, error)
	Stop(id string) (api.AgentStatus, error)
	Restart(id string) (api.AgentStatus, error)
	Status(id string) (api.AgentStatus, error)
	List() []api.AgentStatus
	// PIDs returns root PIDs of running agents (for metrics sampling).
	PIDs() map[string]int
	// Close stops all agents (graceful, bounded by StopTimeout).
	Close() error
}

type Options struct {
	NodeID      string
	DataDir     string // workspaces live in DataDir/agents/<id>/workspace
	Logs        logs.Store
	OnStatus    func(api.AgentStatus) // called (outside locks) on every state change
	StopTimeout time.Duration         // grace period before kill, default 10s
}
