// Package api contains the shared, FROZEN data contracts used by the server,
// the node daemon and the web UI. Changing anything here requires sign-off
// from the lead architect (see docs/modules.md). Additive, backwards
// compatible fields (omitempty) are allowed when coordinated.
package api

import "time"

// ---------- Nodes ----------

type NodeStatus string

const (
	NodeOnline  NodeStatus = "online"
	NodeOffline NodeStatus = "offline"
)

// Capabilities a node may advertise in its hello message.
const (
	CapVNC    = "desktop.vnc"    // local VNC server reachable (RFB over tunnel)
	CapScreen = "desktop.screen" // built-in JPEG screen stream + input injection
	CapStats  = "stats"          // reports host/process metrics
)

type Node struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`   // runtime.GOOS
	Arch         string     `json:"arch"` // runtime.GOARCH
	Version      string     `json:"version"`
	Status       NodeStatus `json:"status"`
	RemoteAddr   string     `json:"remote_addr,omitempty"`
	Capabilities []string   `json:"capabilities"`
	LastSeen     time.Time  `json:"last_seen"`
	CreatedAt    time.Time  `json:"created_at"`
	Host         *HostStats `json:"host,omitempty"` // latest sample, not persisted
}

// ---------- Agents ----------

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"
)

// AgentSpec is the desired configuration of an agent. The server is the
// source of truth (persisted in SQLite) and pushes specs to the owning node.
type AgentSpec struct {
	ID              string            `json:"id"`
	NodeID          string            `json:"node_id"`
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	Command         string            `json:"command"`            // executable (PATH lookup allowed)
	Args            []string          `json:"args,omitempty"`     // argv[1:]
	Env             map[string]string `json:"env,omitempty"`      // extra env vars
	WorkDir         string            `json:"work_dir,omitempty"` // relative => inside agent workspace
	Restart         RestartPolicy     `json:"restart"`
	MaxRestarts     int               `json:"max_restarts"`      // 0 = unlimited
	RestartDelaySec int               `json:"restart_delay_sec"` // default 3
	AutoStart       bool              `json:"auto_start"`        // start when node boots / spec applied
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type AgentState string

const (
	StateStopped  AgentState = "stopped" // never started or stopped by user
	StateStarting AgentState = "starting"
	StateRunning  AgentState = "running"
	StateStopping AgentState = "stopping"
	StateExited   AgentState = "exited"  // process ended with code 0
	StateFailed   AgentState = "failed"  // non-zero exit / spawn error
	StateBackoff  AgentState = "backoff" // waiting to auto-restart
	StateUnknown  AgentState = "unknown" // node offline
)

// AgentStatus is the runtime state reported by the node.
type AgentStatus struct {
	ID        string     `json:"id"`
	NodeID    string     `json:"node_id"`
	State     AgentState `json:"state"`
	PID       int        `json:"pid,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ExitedAt  *time.Time `json:"exited_at,omitempty"`
	Restarts  int        `json:"restarts"`
	Error     string     `json:"error,omitempty"`
	Workspace string     `json:"workspace,omitempty"` // absolute path on node
	Stats     *ProcStats `json:"stats,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Agent is what the REST API returns: spec + latest status.
// Kind is "local" (node-supervised process), "a2a", or "acp" (federated).
type Agent struct {
	AgentSpec
	Status AgentStatus `json:"status"`
	Kind   string      `json:"kind,omitempty"` // local|a2a|acp; empty treated as local by older UIs
}

// ---------- Logs ----------

const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamSystem = "system" // supervisor messages (started, exited, restarting...)
)

type LogLine struct {
	AgentID string    `json:"agent_id"`
	Seq     uint64    `json:"seq"` // monotonically increasing per agent; persisted by the node log store, so it continues across node restarts
	Time    time.Time `json:"time"`
	Stream  string    `json:"stream"`
	Text    string    `json:"text"`
}

// ---------- Metrics ----------

type ProcStats struct {
	Time       time.Time `json:"time"`
	CPUPercent float64   `json:"cpu_percent"` // of one core * 100 (may exceed 100 on multi-core)
	MemRSS     uint64    `json:"mem_rss"`     // bytes
	Threads    int       `json:"threads,omitempty"`
	Procs      int       `json:"procs,omitempty"` // processes in the agent's tree
}

type HostStats struct {
	Time       time.Time `json:"time"`
	CPUPercent float64   `json:"cpu_percent"` // 0-100 whole machine
	CPUCount   int       `json:"cpu_count"`
	MemTotal   uint64    `json:"mem_total"`
	MemUsed    uint64    `json:"mem_used"`
	DiskTotal  uint64    `json:"disk_total"` // volume holding the node data dir
	DiskUsed   uint64    `json:"disk_used"`
	UptimeSec  uint64    `json:"uptime_sec"`
	Load1      float64   `json:"load1,omitempty"`
}

// ---------- Events (server -> browser on /api/v1/events) ----------

const (
	EventNodeStatus   = "node.status"   // payload: Node
	EventNodeRemoved  = "node.removed"  // payload: {"id": "..."}
	EventAgentStatus  = "agent.status"  // payload: AgentStatus
	EventAgentSpec    = "agent.spec"    // payload: AgentSpec (created/updated)
	EventAgentRemoved = "agent.removed" // payload: {"id": "..."}
	EventNodeStats    = "node.stats"    // payload: NodeStats
	EventAgentLog     = "agent.log"     // payload: LogLine (only on per-agent log streams)
)

type Event struct {
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Payload any       `json:"payload"`
}

// ---------- Errors ----------

// ErrorResponse is the body of every non-2xx REST response.
type ErrorResponse struct {
	Error string `json:"error"`          // human readable (English)
	Code  string `json:"code,omitempty"` // machine code, e.g. "not_found", "node_offline"
}
