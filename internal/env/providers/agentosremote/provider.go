// Package agentosremote is the "agentos-remote" environment provider: an
// Agent OS node on another machine that joins this server with a one-time
// join token (issued by POST /envs/{id}/join). The token becomes that node's
// credential; deleting the environment revokes it and disconnects the node.
// Nodes that connect with the shared node token and are not bound to any
// environment are listed automatically as agentos-remote environments.
package agentosremote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/agentos/agentos/internal/env"
	"github.com/agentos/agentos/internal/env/agentosnode"
)

const Type = "agentos-remote"

// LabelAuto marks environments auto-created for nodes that joined with the
// shared node token.
const LabelAuto = "auto"

type Config struct {
	// ServerURL overrides the server address used in join commands (the URL
	// the remote machine can reach), e.g. http://192.168.1.10:7800.
	ServerURL string `json:"server_url,omitempty"`
	// Desktop "virtual": the join command starts the node with --desktop
	// virtual (Linux: its own Xvfb + window manager + x11vnc desktop).
	Desktop string `json:"desktop,omitempty"`
}

const schema = `{
  "type": "object",
  "properties": {
    "server_url": {"type": "string", "title": "管理端地址", "default": "", "description": "远程机器访问本管理端使用的地址，如 http://192.168.1.10:7800（留空则使用当前浏览器地址）", "format": "uri"},
    "desktop":    {"type": "string", "title": "桌面", "enum": ["host", "virtual"], "default": "host",
                   "x-enum-titles": ["该机器的桌面 / VNC", "虚拟桌面（独立 Xvfb 桌面，Linux 节点）"],
                   "description": "virtual：加入命令带 --desktop virtual，Linux 节点运行自己的虚拟桌面（需要 xvfb x11vnc fluxbox xterm）"}
  },
  "additionalProperties": false
}`

type provider struct{}

func init() { env.Register(provider{}) }

func (provider) Info() env.ProviderInfo {
	return env.ProviderInfo{
		Type: Type, Name: "远程 Agent OS", Kind: env.KindAgentOS, Group: "Agent OS",
		Description: "把另一台电脑（Windows / Linux / macOS）登记为 Agent OS：在目标机器上运行一条安装命令即可接入。",
		Capabilities: []env.Capability{env.CapExec, env.CapFiles, env.CapDesktopVNC, env.CapDesktopScreen,
			env.CapMetrics, env.CapAgents},
		ConfigSchema: json.RawMessage(schema),
	}
}

func Parse(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) == 0 || string(raw) == "null" {
		return c, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid agentos-remote config: %w", err)
	}
	if c.ServerURL != "" {
		u, err := url.Parse(c.ServerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return c, fmt.Errorf("server_url must be an http(s) URL")
		}
	}
	return c, nil
}

func (provider) Validate(cfg json.RawMessage) error                    { _, err := Parse(cfg); return err }
func (p provider) Test(ctx context.Context, cfg json.RawMessage) error { return p.Validate(cfg) }

// Create only validates: the node joins later via the join token.
func (p provider) Create(ctx context.Context, rec *env.Record) error { return p.Validate(rec.Config) }

func (provider) Open(ctx context.Context, rec *env.Record) (env.Env, error) {
	nodeID := rec.NodeID
	return &agentosnode.Env{
		NodeID: func() string { return nodeID },
		StatusFn: func(ctx context.Context) (env.Status, error) {
			switch {
			case nodeID == "":
				return env.Status{State: env.StateCreating, Message: "等待远程节点加入 / waiting for the node to join"}, nil
			case agentosnode.Online(nodeID):
				return env.Status{State: env.StateRunning}, nil
			}
			return env.Status{State: env.StateUnknown, Message: "节点离线 / node offline"}, nil
		},
		// Revocation and disconnect are done by the server on DELETE.
		DeleteFn: func(ctx context.Context) error { return nil },
	}, nil
}
