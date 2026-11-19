// Package agentoslocal is the "agentos-local" environment provider: extra
// Agent OS instances on the server machine, each a supervised child
// `agentos node` process (Manager) with its own data dir <data>/localos/<id>/.
// The server's built-in embedded node is shown as the environment "本机"
// (labels.builtin=true) of this type; it cannot be started/stopped/deleted.
package agentoslocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agentos/agentos/internal/desktop/virtual"
	"strings"
	"sync"

	"github.com/agentos/agentos/internal/env"
	"github.com/agentos/agentos/internal/env/agentosnode"
)

const Type = "agentos-local"

// LabelBuiltin marks the built-in environment of the server's embedded node.
const LabelBuiltin = "builtin"

// BuiltinEnvID is the fixed record id of the built-in environment.
const BuiltinEnvID = "local"

// Config is the provider config (JSON).
type Config struct {
	VNCAddr   string `json:"vnc_addr,omitempty"`   // e.g. 127.0.0.1:5901 (desktop of this instance)
	AutoStart *bool  `json:"auto_start,omitempty"` // start with the server (default true)
	// Desktop "virtual" (Linux): the instance runs its own Xvfb + window
	// manager + x11vnc desktop (agentos node --desktop virtual); vnc_addr is
	// then ignored. "" / "host" = use vnc_addr / the server's $DISPLAY.
	Desktop string `json:"desktop,omitempty"`
}

func (c Config) autoStart() bool { return c.AutoStart == nil || *c.AutoStart }

const schema = `{
  "type": "object",
  "properties": {
    "vnc_addr":   {"x-advanced": true, "type": "string", "title": "VNC 地址", "default": "", "description": "该实例远程桌面使用的本机 VNC 服务地址，如 127.0.0.1:5901（可选）"},
    "desktop":    {"type": "string", "title": "桌面", "enum": ["host", "virtual"], "default": "host",
                   "x-enum-titles": ["本机桌面 / VNC 地址", "虚拟桌面（独立 Xvfb 桌面，Linux）"],
                   "description": "virtual：该实例运行自己的虚拟桌面（Xvfb + 窗口管理器 + x11vnc，需要 xvfb x11vnc fluxbox xterm）"},
    "auto_start": {"x-advanced": true, "type": "boolean", "title": "随管理端自动启动", "default": true}
  },
  "additionalProperties": false
}`

// Options configure the provider; set once by the server with Configure.
type Options struct {
	Root      string        // <data>/localos
	ServerURL string        // loopback URL children dial
	Exe       string        // default os.Executable()
	Token     func() string // node credential for children (shared node token)
	// Log appends to an environment's lifecycle log.
	Log func(envID, level, text string)
	// ExtraEnv is appended to child environments (tests).
	ExtraEnv []string
}

type provider struct {
	mu  sync.Mutex
	opt Options
	mgr *Manager
}

var p = &provider{}

func init() { env.Register(p) }

// Configure creates the instance manager. Call before opening records.
func Configure(opt Options) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mgr != nil {
		p.mgr.Close()
	}
	m, err := New(ManagerOptions(opt))
	if err != nil {
		return err
	}
	p.opt, p.mgr = opt, m
	return nil
}

// ManagerOptions maps provider options to manager options.
func ManagerOptions(opt Options) OptionsM {
	return OptionsM{Exe: opt.Exe, Root: opt.Root, ServerURL: opt.ServerURL, ExtraEnv: opt.ExtraEnv,
		OnLine: func(id, line string) {
			if opt.Log != nil {
				level := "info"
				if l := strings.ToLower(line); strings.Contains(l, "error") || strings.Contains(l, "failed") || strings.Contains(l, "失败") {
					level = "warn"
				}
				opt.Log(id, level, line)
			}
		}}
}

// Shutdown stops all child instances (call when the server exits).
func Shutdown() {
	p.mu.Lock()
	m := p.mgr
	p.mgr = nil
	p.mu.Unlock()
	if m != nil {
		m.Close()
	}
}

func (pr *provider) manager() (*Manager, error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.mgr == nil {
		return nil, errors.New("agentos-local provider not configured")
	}
	return pr.mgr, nil
}

func (pr *provider) Info() env.ProviderInfo {
	return env.ProviderInfo{
		Type: Type, Name: "本机 Agent OS 实例", Kind: env.KindAgentOS, Group: "Agent OS",
		Description: "在本机再运行一个独立的 Agent OS（独立数据目录、名称与工作区），由管理端托管启停。",
		Capabilities: []env.Capability{env.CapLifecycle, env.CapExec, env.CapFiles, env.CapDesktopVNC,
			env.CapDesktopScreen, env.CapMetrics, env.CapAgents},
		ConfigSchema: json.RawMessage(schema),
	}
}

func parse(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) == 0 || string(raw) == "null" {
		return c, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid agentos-local config: %w", err)
	}
	switch c.Desktop {
	case "", "host", virtual.Mode:
	default:
		return c, fmt.Errorf("invalid agentos-local config: desktop must be \"host\" or \"virtual\"")
	}
	return c, nil
}

func (pr *provider) Validate(cfg json.RawMessage) error { _, err := parse(cfg); return err }

func (pr *provider) Test(ctx context.Context, cfg json.RawMessage) error {
	if err := pr.Validate(cfg); err != nil {
		return err
	}
	if c, _ := parse(cfg); c.Desktop == virtual.Mode {
		if err := virtual.Check(); err != nil {
			return err
		}
	}
	_, err := pr.manager()
	return err
}

func isBuiltin(rec *env.Record) bool { return rec.Labels[LabelBuiltin] == "true" }

func (pr *provider) spec(rec *env.Record, c Config) Spec {
	tok := ""
	if pr.opt.Token != nil {
		tok = pr.opt.Token()
	}
	return Spec{ID: rec.ID, Name: rec.Name, Token: tok, NodeID: rec.NodeID, VNCAddr: c.VNCAddr, Desktop: c.Desktop}
}

// Create assigns the node identity (= record id) and starts the instance.
func (pr *provider) Create(ctx context.Context, rec *env.Record) error {
	if isBuiltin(rec) {
		return errors.New("the built-in environment is created by the server")
	}
	c, err := parse(rec.Config)
	if err != nil {
		return err
	}
	m, err := pr.manager()
	if err != nil {
		return err
	}
	rec.NodeID = rec.ID
	if err := m.Ensure(pr.spec(rec, c)); err != nil {
		return err
	}
	return m.Start(rec.ID)
}

// Open returns the handle. Non-builtin instances are registered with the
// manager and auto-started unless the user stopped them (rec.State stopped)
// or auto_start is false.
func (pr *provider) Open(ctx context.Context, rec *env.Record) (env.Env, error) {
	nodeID := rec.NodeID
	nid := func() string { return nodeID }
	if isBuiltin(rec) {
		return &agentosnode.Env{NodeID: nid, StatusFn: func(ctx context.Context) (env.Status, error) {
			if agentosnode.Online(nodeID) {
				return env.Status{State: env.StateRunning, Address: "127.0.0.1"}, nil
			}
			return env.Status{State: env.StateUnknown, Message: "内置节点未连接 / built-in node not connected"}, nil
		}}, nil
	}
	c, err := parse(rec.Config)
	if err != nil {
		return nil, err
	}
	m, err := pr.manager()
	if err != nil {
		return nil, err
	}
	if nodeID == "" {
		nodeID = rec.ID
	}
	sp := pr.spec(rec, c)
	sp.NodeID = nodeID
	if err := m.Ensure(sp); err != nil {
		return nil, err
	}
	if c.autoStart() && rec.State != env.StateStopped && rec.State != env.StateCreating {
		m.Start(rec.ID)
	}
	id := rec.ID
	return &agentosnode.Env{
		NodeID: nid,
		StatusFn: func(ctx context.Context) (env.Status, error) {
			st, err := m.Status(id)
			if err != nil {
				return env.Status{State: env.StateUnknown, Message: err.Error()}, nil
			}
			extra := map[string]string{"data_dir": st.DataDir}
			if st.PID > 0 {
				extra["pid"] = fmt.Sprint(st.PID)
			}
			switch st.State {
			case StateRunning:
				if agentosnode.Online(nodeID) {
					return env.Status{State: env.StateRunning, Address: "127.0.0.1", Extra: extra}, nil
				}
				return env.Status{State: env.StateCreating, Message: "正在连接管理端 / connecting", Extra: extra}, nil
			case StateStarting:
				return env.Status{State: env.StateCreating, Message: "正在启动 / starting", Extra: extra}, nil
			case StateBackoff:
				return env.Status{State: env.StateError, Message: "实例异常退出，等待重启 / crashed, restarting: " + st.Error, Extra: extra}, nil
			case StateFailed:
				return env.Status{State: env.StateError, Message: st.Error, Extra: extra}, nil
			case StateStopping:
				return env.Status{State: env.StateRunning, Message: "正在停止 / stopping", Extra: extra}, nil
			}
			return env.Status{State: env.StateStopped, Extra: extra}, nil
		},
		StartFn: func(ctx context.Context) error {
			sp := pr.spec(rec, c) // refresh token
			sp.NodeID = nodeID
			if err := m.Ensure(sp); err != nil {
				return err
			}
			return m.Start(id)
		},
		StopFn: func(ctx context.Context) error { return m.Stop(id) },
		// Delete stops the instance; with env.IsPurge(ctx) its data dir is removed too.
		DeleteFn: func(ctx context.Context) error { return m.Remove(id, env.IsPurge(ctx)) },
	}, nil
}

// InstanceLogs returns the last output lines of a local instance.
func InstanceLogs(id string, tail int) ([]string, error) {
	m, err := p.manager()
	if err != nil {
		return nil, err
	}
	return m.Logs(id, tail)
}
