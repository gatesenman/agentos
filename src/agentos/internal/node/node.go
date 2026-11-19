// Package node implements the `agentos node` daemon: it keeps a control
// WebSocket to the server, applies agent specs via the supervisor, streams
// status/log/stats events and serves remote-desktop tunnels.
// Owned by the lead/integrator (core). Modules plug in via interfaces.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/desktop"
	"github.com/agentos/agentos/internal/desktop/screen"
	"github.com/agentos/agentos/internal/desktop/virtual"
	"github.com/agentos/agentos/internal/desktop/vnc"
	"github.com/agentos/agentos/internal/logs"
	"github.com/agentos/agentos/internal/metrics"
	"github.com/agentos/agentos/internal/supervisor"
	"github.com/agentos/agentos/internal/version"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Config struct {
	ServerURL string // http(s)://host:port
	Token     string // node token
	// TokenFunc, when set, returns the current token (embedded node follows rotation).
	TokenFunc func() string
	Name      string // display name, default hostname
	DataDir   string
	VNCAddr   string // default 127.0.0.1:5900
	// Desktop "virtual" (Linux): run a private Xvfb + WM + x11vnc desktop for
	// this node (internal/desktop/virtual); VNCAddr and $DISPLAY then point at it.
	Desktop       string
	StatsInterval time.Duration // default 5s
}

type Node struct {
	cfg       Config
	id        string
	hostname  string
	sup       supervisor.Supervisor
	logs      logs.Store
	metrics   metrics.Collector
	providers map[string]desktop.Provider
	vdesk     *virtual.Desktop

	mu   sync.Mutex
	conn *websocket.Conn
	wmu  sync.Mutex // serialises writes on conn

	logMu  sync.Mutex
	logBuf map[string][]api.LogLine
}

func New(cfg Config) (*Node, error) {
	if cfg.ServerURL == "" || cfg.Token == "" {
		return nil, errors.New("node: server URL and token are required")
	}
	if cfg.StatsInterval == 0 {
		cfg.StatsInterval = 5 * time.Second
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	n := &Node{cfg: cfg, logBuf: map[string][]api.LogLine{}}
	n.hostname, _ = os.Hostname()
	if err := os.MkdirAll(n.home(), 0o755); err != nil {
		return nil, err
	}
	if n.cfg.Name == "" {
		n.cfg.Name = n.hostname
	}
	id, err := loadOrCreateID(filepath.Join(cfg.DataDir, "node-id"))
	if err != nil {
		return nil, err
	}
	n.id = id
	n.logs, err = logs.NewStore(filepath.Join(cfg.DataDir, "logs"), 0)
	if err != nil {
		return nil, err
	}
	n.logs.Subscribe(n.bufferLog)
	n.sup, err = supervisor.New(supervisor.Options{NodeID: id, DataDir: cfg.DataDir, Logs: n.logs, OnStatus: func(st api.AgentStatus) {
		n.sendEvent(api.MsgEvAgentStatus, st)
	}})
	if err != nil {
		return nil, err
	}
	n.metrics = metrics.New(cfg.DataDir)
	switch cfg.Desktop {
	case "", "none", "host":
	case virtual.Mode:
		if runtime.GOOS == "windows" {
			log.Printf("node: --desktop virtual is not available on Windows; using the built-in screen stream (VNC optional)")
			break
		}
		vd, err := virtual.Start(virtual.Options{Name: n.cfg.Name, LogDir: filepath.Join(cfg.DataDir, "logs", "desktop"), Logf: log.Printf})
		if err != nil {
			return nil, err
		}
		n.vdesk = vd
		// the screen stream (M5) and agents started by this node use it too
		os.Setenv("DISPLAY", vd.Display)
		n.cfg.VNCAddr = vd.VNCAddr
	default:
		return nil, fmt.Errorf("node: unknown desktop mode %q (want %q)", cfg.Desktop, virtual.Mode)
	}
	n.providers = map[string]desktop.Provider{}
	for _, p := range []desktop.Provider{vnc.New(n.cfg.VNCAddr), screen.New()} {
		n.providers[p.Kind()] = p
	}
	return n, nil
}

func (n *Node) ID() string { return n.id }

func loadOrCreateID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	id := uuid.NewString()
	return id, os.WriteFile(path, []byte(id), 0o644)
}

// Run connects and reconnects until ctx is cancelled, then stops all agents.
func (n *Node) Run(ctx context.Context) error {
	// Graceful shutdown: stop agents while the control connection is still up,
	// flush their final status/log lines, then close the connection.
	shutdown := make(chan struct{})
	go func() {
		<-ctx.Done()
		n.sup.Close()
		n.flushLogs()
		if n.vdesk != nil {
			n.vdesk.Stop()
		}
		close(shutdown)
	}()
	go n.flushLogsLoop(ctx)
	go n.statsLoop(ctx)
	backoff := time.Second
	for {
		start := time.Now()
		err := n.session(ctx, shutdown)
		if ctx.Err() != nil {
			<-shutdown
			n.logs.Close()
			return nil
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		log.Printf("node: disconnected (%v), retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func wsURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http", "":
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

func (n *Node) dial(ctx context.Context, path string) (*websocket.Conn, error) {
	u, err := wsURL(n.cfg.ServerURL, path)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	tok := n.cfg.Token
	if n.cfg.TokenFunc != nil {
		if t := n.cfg.TokenFunc(); t != "" {
			tok = t
		}
	}
	h.Set(api.NodeTokenHeader, "Bearer "+tok)
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: http.ProxyFromEnvironment}
	c, resp, err := d.DialContext(ctx, u, h)
	if err != nil && resp != nil {
		return nil, fmt.Errorf("%w (HTTP %d)", err, resp.StatusCode)
	}
	return c, err
}

func (n *Node) capabilities() []string {
	caps := []string{api.CapStats, api.CapExec, api.CapFiles}
	for _, p := range n.providers {
		if p.Available() {
			caps = append(caps, p.Capability())
		}
	}
	return caps
}

func (n *Node) session(ctx context.Context, shutdown <-chan struct{}) error {
	c, err := n.dial(ctx, api.PathNodeConnect)
	if err != nil {
		return err
	}
	defer c.Close()
	hello := api.HelloPayload{
		ProtocolVersion: api.ProtocolVersion, NodeID: n.id, Name: n.cfg.Name, Hostname: n.hostname,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Version: version.Version,
		Capabilities: n.capabilities(), Agents: n.sup.List(),
	}
	if err := writeEnv(c, &n.wmu, api.Envelope{Type: api.MsgHello}, hello); err != nil {
		return err
	}
	var welcome api.Envelope
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := c.ReadJSON(&welcome); err != nil {
		return err
	}
	if welcome.Type != api.MsgWelcome {
		return fmt.Errorf("unexpected %q: %s", welcome.Type, welcome.Error)
	}
	log.Printf("node: connected to %s as %s (%s)", n.cfg.ServerURL, n.cfg.Name, n.id)
	n.mu.Lock()
	n.conn = c
	n.mu.Unlock()
	defer func() { n.mu.Lock(); n.conn = nil; n.mu.Unlock() }()

	c.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(90 * time.Second)); return nil })
	c.SetPingHandler(func(s string) error {
		c.SetReadDeadline(time.Now().Add(90 * time.Second))
		n.wmu.Lock()
		defer n.wmu.Unlock()
		return c.WriteControl(websocket.PongMessage, []byte(s), time.Now().Add(5*time.Second))
	})
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keepalive
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-shutdown:
				c.Close()
				return
			case <-t.C:
				n.wmu.Lock()
				c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				n.wmu.Unlock()
			}
		}
	}()
	for {
		var env api.Envelope
		if err := c.ReadJSON(&env); err != nil {
			return err
		}
		c.SetReadDeadline(time.Now().Add(90 * time.Second))
		go n.handle(ctx, c, env)
	}
}

func writeEnv(c *websocket.Conn, mu *sync.Mutex, env api.Envelope, payload any) error {
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		env.Payload = b
	}
	mu.Lock()
	defer mu.Unlock()
	c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.WriteJSON(env)
}

func (n *Node) sendEvent(typ string, payload any) {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	if c != nil {
		writeEnv(c, &n.wmu, api.Envelope{Type: typ}, payload)
	}
}

func (n *Node) handle(ctx context.Context, c *websocket.Conn, env api.Envelope) {
	result, err := n.dispatch(ctx, env)
	if env.ID == "" {
		return
	}
	resp := api.Envelope{Type: api.MsgResponse, ReplyTo: env.ID}
	if err != nil {
		resp.Error = err.Error()
		result = nil
	}
	writeEnv(c, &n.wmu, resp, result)
}

func (n *Node) dispatch(ctx context.Context, env api.Envelope) (any, error) {
	var ref api.AgentRef
	switch env.Type {
	case api.MsgAgentStart, api.MsgAgentStop, api.MsgAgentRestart, api.MsgAgentRemove:
		if err := json.Unmarshal(env.Payload, &ref); err != nil {
			return nil, err
		}
	}
	switch env.Type {
	case api.MsgAgentSync:
		var p api.SyncPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, err
		}
		want := map[string]bool{}
		for _, s := range p.Agents {
			want[s.ID] = true
			if _, err := n.sup.Apply(s); err != nil {
				log.Printf("node: apply %s: %v", s.ID, err)
			}
		}
		for _, st := range n.sup.List() {
			if !want[st.ID] {
				n.sup.Remove(st.ID)
			}
		}
		for _, st := range n.sup.List() {
			n.sendEvent(api.MsgEvAgentStatus, st)
		}
		return nil, nil
	case api.MsgAgentApply:
		var s api.AgentSpec
		if err := json.Unmarshal(env.Payload, &s); err != nil {
			return nil, err
		}
		return n.sup.Apply(s)
	case api.MsgAgentRemove:
		return nil, n.sup.Remove(ref.ID)
	case api.MsgAgentStart:
		return n.sup.Start(ref.ID)
	case api.MsgAgentStop:
		return n.sup.Stop(ref.ID)
	case api.MsgAgentRestart:
		return n.sup.Restart(ref.ID)
	case api.MsgAgentLogs:
		var r api.LogsRequest
		if err := json.Unmarshal(env.Payload, &r); err != nil {
			return nil, err
		}
		if r.Tail <= 0 {
			r.Tail = 200
		}
		return api.LogsResponse{Lines: n.logs.Tail(r.ID, r.Tail, r.Since)}, nil
	case api.MsgTunnelOpen:
		var t api.TunnelOpen
		if err := json.Unmarshal(env.Payload, &t); err != nil {
			return nil, err
		}
		serve := n.builtinTunnel(t.Kind) // exec / file.get / file.put (exec.go, files.go)
		if serve == nil {
			p, ok := n.providers[t.Kind]
			if !ok {
				return nil, fmt.Errorf("unsupported tunnel kind %q", t.Kind)
			}
			serve = func(ctx context.Context, c *websocket.Conn, params map[string]string) error {
				return p.Serve(ctx, c, params)
			}
		}
		tc, err := n.dial(ctx, api.PathNodeTunnel+t.SessionID)
		if err != nil {
			return nil, err
		}
		go func() {
			defer tc.Close()
			if err := serve(ctx, tc, t.Params); err != nil {
				log.Printf("node: tunnel %s/%s ended: %v", t.Kind, t.SessionID, err)
			}
		}()
		return nil, nil
	case api.MsgFsList:
		var r api.FsListRequest
		if err := json.Unmarshal(env.Payload, &r); err != nil {
			return nil, err
		}
		return n.listDir(r.Path)
	}
	return nil, fmt.Errorf("unknown message type %q", env.Type)
}

func (n *Node) bufferLog(l api.LogLine) {
	n.logMu.Lock()
	n.logBuf[l.AgentID] = append(n.logBuf[l.AgentID], l)
	n.logMu.Unlock()
}

// flushLogsLoop batches log lines every 200ms into agent.log events.
func (n *Node) flushLogsLoop(ctx context.Context) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n.flushLogs()
	}
}

// flushLogs sends buffered log lines as agent.log batches (≤500 lines each).
func (n *Node) flushLogs() {
	n.logMu.Lock()
	buf := n.logBuf
	n.logBuf = map[string][]api.LogLine{}
	n.logMu.Unlock()
	for id, lines := range buf {
		for len(lines) > 0 {
			k := min(len(lines), 500)
			n.sendEvent(api.MsgEvAgentLog, api.LogBatch{AgentID: id, Lines: lines[:k]})
			lines = lines[k:]
		}
	}
}

func (n *Node) statsLoop(ctx context.Context) {
	t := time.NewTicker(n.cfg.StatsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		host, err := n.metrics.Host()
		if err != nil {
			continue
		}
		ns := api.NodeStats{NodeID: n.id, Host: host, Agents: map[string]api.ProcStats{}}
		for id, pid := range n.sup.PIDs() {
			if ps, err := n.metrics.Process(pid); err == nil {
				ns.Agents[id] = ps
			}
		}
		n.sendEvent(api.MsgEvNodeStats, ns)
	}
}
