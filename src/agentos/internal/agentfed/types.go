// Package agentfed implements AgentOS federation over A2A (Agent2Agent) and
// legacy ACP (Agent Communication Protocol, Bee/IBM — now merged into A2A).
package agentfed

import (
	"encoding/json"
	"time"
)

// Protocol identifiers.
const (
	ProtoA2A   = "a2a"
	ProtoACP   = "acp"
	ProtoLocal = "local" // not stored here; used in unified list views
)

// AuthScheme is how AgentOS authenticates to the remote agent.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthAPIKey = "api_key" // header; AuthHeader names the header (default Authorization)
)

// FederatedAgent is a remote agent registered in AgentOS (not node-supervised).
type FederatedAgent struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Protocol     string            `json:"protocol"` // a2a | acp
	Endpoint     string            `json:"endpoint"` // base URL (card URL or ACP server root)
	AuthScheme   string            `json:"auth_scheme"`
	AuthHeader   string            `json:"auth_header,omitempty"` // for api_key
	HasAuth      bool              `json:"has_auth"`              // secret present (never expose token)
	AgentCard    json.RawMessage   `json:"agent_card,omitempty"` // cached A2A card or ACP manifest
	Capabilities []string          `json:"capabilities,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Status       string            `json:"status"` // online|offline|error|unknown
	LastError    string            `json:"last_error,omitempty"`
	LastSeen     *time.Time        `json:"last_seen,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Auth holds the decrypted token for outbound calls (never persisted as plain JSON).
type Auth struct {
	Scheme string // bearer|api_key|none
	Header string // e.g. Authorization or X-API-Key
	Token  string
}

// TaskInfo is a running or recent remote task/run surfaced in AgentOS.
type TaskInfo struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agent_id"`
	Protocol  string     `json:"protocol"`
	RemoteID  string     `json:"remote_id"` // A2A task id or ACP run_id
	State     string     `json:"state"`
	Input     string     `json:"input,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// ProbeResult is returned by TestConnection / Discover.
type ProbeResult struct {
	Protocol     string          `json:"protocol"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Endpoint     string          `json:"endpoint"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Card         json.RawMessage `json:"card,omitempty"`
	OK           bool            `json:"ok"`
	Error        string          `json:"error,omitempty"`
}

// CreateRequest is the body for registering a federated agent.
type CreateRequest struct {
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"` // a2a|acp|"" (auto-detect)
	Endpoint   string            `json:"endpoint"`
	AuthScheme string            `json:"auth_scheme"`
	AuthHeader string            `json:"auth_header,omitempty"`
	Token      string            `json:"token,omitempty"` // write-only
	Metadata   map[string]string `json:"metadata,omitempty"`
	FetchCard  bool              `json:"fetch_card"` // default true on create
}

// InvokeRequest sends a user message / starts a run.
type InvokeRequest struct {
	Message string `json:"message"`
	Stream  bool   `json:"stream"` // prefer streaming when supported
}
