// Package env defines the Environment abstraction: every place an agent can run
// (local Agent OS instance, remote Agent OS node, Docker container, SSH machine,
// cloud sandbox, cloud VM) is an Environment created by a Provider.
// FROZEN CONTRACT v1 - do not edit without the lead.
package env

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

var ErrUnsupported = errors.New("env: capability not supported by this provider")
var ErrNotFound = errors.New("env: not found")

type Kind string

const (
	KindAgentOS Kind = "agentos" // local instance or remote enrolled node
	KindDocker  Kind = "container"
	KindVM      Kind = "vm"      // ssh, hyper-v, cloud vm
	KindSandbox Kind = "sandbox" // e2b, daytona, http-sandbox
)

type Capability string

const (
	CapLifecycle     Capability = "lifecycle" // start/stop/delete
	CapExec          Capability = "exec"
	CapFiles         Capability = "files"
	CapDesktopVNC    Capability = "desktop.vnc"
	CapDesktopScreen Capability = "desktop.screen"
	CapMetrics       Capability = "metrics"
	CapAgents        Capability = "agents"           // native agent supervision (Agent OS)
	CapInstallAgent  Capability = "install-agentos"  // can bootstrap an Agent OS node inside
)

type State string

const (
	StateCreating State = "creating"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StateError    State = "error"
	StateUnknown  State = "unknown"
)

// ProviderInfo is what the UI lists in the "new environment" wizard.
type ProviderInfo struct {
	Type         string          `json:"type"`  // e.g. "agentos-local","agentos-remote","docker","ssh"
	Name         string          `json:"name"`  // zh-CN display name
	Kind         Kind            `json:"kind"`
	Group        string          `json:"group"` // 本机 | 容器 | 虚拟机 | 云沙箱 | Agent OS
	Description  string          `json:"description"`
	Capabilities []Capability    `json:"capabilities"`
	ConfigSchema json.RawMessage `json:"config_schema"` // JSON Schema; fields with "x-secret": true are encrypted, never returned
}

// Record is the persisted environment row.
type Record struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Config    json.RawMessage   `json:"config"`         // secrets stripped when sent to UI
	Stack     string            `json:"stack,omitempty"` // stack template id
	NodeID    string            `json:"node_id,omitempty"` // Agent OS node bound to this env (native or bootstrapped)
	Labels    map[string]string `json:"labels,omitempty"`
	State     State             `json:"state"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type Status struct {
	State   State             `json:"state"`
	Message string            `json:"message,omitempty"`
	Address string            `json:"address,omitempty"` // ip/host if any
	Extra   map[string]string `json:"extra,omitempty"`
}

type ExecRequest struct {
	Cmd     []string          `json:"cmd"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"work_dir,omitempty"`
	TTY     bool              `json:"tty,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
}

type ExecSession interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader // may return an empty reader when TTY is true
	Resize(cols, rows int) error
	Wait() (exitCode int, err error)
	Close() error
}

type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Dir     bool      `json:"dir"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

type Metrics struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
}

// Env is a live handle to one environment. Unsupported operations return ErrUnsupported.
type Env interface {
	Status(ctx context.Context) (Status, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Delete(ctx context.Context) error
	Exec(ctx context.Context, req ExecRequest) (ExecSession, error)
	PutFile(ctx context.Context, path string, r io.Reader, mode uint32) error
	GetFile(ctx context.Context, path string) (io.ReadCloser, error)
	ListDir(ctx context.Context, path string) ([]FileInfo, error)
	// Desktop returns a raw byte stream: kind "vnc" = RFB stream, kind "screen" = AgentOS screen protocol (docs/protocol.md §5).
	Desktop(ctx context.Context, kind string) (io.ReadWriteCloser, error)
	Metrics(ctx context.Context) (Metrics, error)
	Close() error
}

type Provider interface {
	Info() ProviderInfo
	// Validate checks config shape; Test also checks connectivity/credentials.
	Validate(cfg json.RawMessage) error
	Test(ctx context.Context, cfg json.RawMessage) error
	// Create provisions a new environment (e.g. docker run, start local instance). rec.ID is preassigned.
	// It may update rec.Config (e.g. container id) and rec.NodeID before returning.
	Create(ctx context.Context, rec *Record) error
	// Open returns a handle for an existing record.
	Open(ctx context.Context, rec *Record) (Env, error)
}

// Registry of providers, populated in init() by provider packages via Register.
var providers = map[string]Provider{}

func Register(p Provider) { providers[p.Info().Type] = p }

func Get(typ string) (Provider, bool) { p, ok := providers[typ]; return p, ok }

func List() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Info())
	}
	return out
}
