//go:build !windows

package e2e

// Real-binary end-to-end tests (module M8): build cmd/agentos once, run
// `agentos server` / `agentos node` as separate OS processes on random ports
// and drive them over REST/WebSocket.
//
//	TestBinaryAllInOneAutoRestart  server --with-node, agent restart policy on-failure + max_restarts
//	TestBinaryNodeReconnect        server restart: node reconnects, agent keeps running (same pid), status resyncs;
//	                               node killed: node offline + agent state "unknown"
//	TestBinaryVNCTunnel            browser WS -> server -> node -> fake RFB server and back

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agentos/agentos/internal/api"
)

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func agentosBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("binary e2e skipped in -short mode")
	}
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentos-e2e-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "agentos")
		cmd := exec.Command("go", "build", "-o", binPath, "github.com/agentos/agentos/cmd/agentos")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// proc is a running agentos process with captured output.
type proc struct {
	t    *testing.T
	name string
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
}

func startProc(t *testing.T, name string, args ...string) *proc {
	t.Helper()
	p := &proc{t: t, name: name, done: make(chan struct{})}
	p.cmd = exec.Command(agentosBin(t), args...)
	p.cmd.Env = append(os.Environ(), "AGENTOS_WITH_NODE=", "AGENTOS_DATA=")
	pr, pw := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = pw, pw
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			p.mu.Lock()
			p.out.WriteString(sc.Text() + "\n")
			p.mu.Unlock()
		}
	}()
	go func() { p.cmd.Wait(); pw.Close(); close(p.done) }()
	t.Cleanup(func() {
		p.stop(syscall.SIGKILL)
		if t.Failed() {
			t.Logf("---- %s output ----\n%s", p.name, p.output())
		}
	})
	return p
}

func (p *proc) output() string { p.mu.Lock(); defer p.mu.Unlock(); return p.out.String() }

func (p *proc) stop(sig syscall.Signal) {
	select {
	case <-p.done:
		return
	default:
	}
	p.cmd.Process.Signal(sig)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		p.cmd.Process.Kill()
		<-p.done
	}
}

func waitFor(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timeout (%s) waiting for %s", d, what)
}

// apiClient talks to the REST API with a bearer token.
type apiClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *apiClient) do(method, path string, body, out any) int {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func startServer(t *testing.T, port int, dataDir string, withNode bool, extra ...string) (*proc, *apiClient) {
	t.Helper()
	args := append([]string{"server", "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--data", dataDir,
		"--admin-password", "e2e-secret", fmt.Sprintf("--with-node=%v", withNode)}, extra...)
	p := startProc(t, "server", args...)
	c := &apiClient{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port)}
	waitFor(t, 20*time.Second, "server health", func() bool { return c.do("GET", "/health", nil, nil) == 200 })
	var tok struct{ Token string }
	if code := c.do("POST", "/auth/login", map[string]string{"password": "e2e-secret"}, &tok); code != 200 || tok.Token == "" {
		t.Fatalf("login: %d", code)
	}
	c.token = tok.Token
	return p, c
}

func (c *apiClient) nodeToken() string {
	var r struct{ Token string }
	if code := c.do("GET", "/settings/node-token", nil, &r); code != 200 || r.Token == "" {
		c.t.Fatalf("node token: %d", code)
	}
	return r.Token
}

func (c *apiClient) onlineNode(name string) api.Node {
	var found api.Node
	waitFor(c.t, 45*time.Second, "node "+name+" online", func() bool {
		var nodes []api.Node
		c.do("GET", "/nodes", nil, &nodes)
		for _, n := range nodes {
			if (name == "" || n.Name == name) && n.Status == api.NodeOnline {
				found = n
				return true
			}
		}
		return false
	})
	return found
}

func (c *apiClient) agent(id string) api.Agent {
	var a api.Agent
	c.do("GET", "/agents/"+id, nil, &a)
	return a
}

func pidAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func TestBinaryVersion(t *testing.T) {
	out, err := exec.Command(agentosBin(t), "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "agentos ") {
		t.Fatalf("version: %v %q", err, out)
	}
}

func TestBinaryAllInOneAutoRestart(t *testing.T) {
	port := freePort(t)
	_, c := startServer(t, port, t.TempDir(), true, "--node-name", "local-e2e")
	n := c.onlineNode("local-e2e")

	var a api.Agent
	code := c.do("POST", "/agents", api.AgentSpec{NodeID: n.ID, Name: "crashy", Command: "sh",
		Args: []string{"-c", "echo boot $AGENTOS_AGENT_NAME; sleep 0.3; exit 3"}, Restart: api.RestartOnFailure,
		MaxRestarts: 2, RestartDelaySec: 1}, &a)
	if code != 201 {
		t.Fatalf("create %d", code)
	}
	if code := c.do("POST", "/agents/"+a.ID+"/start", nil, &a); code != 200 {
		t.Fatalf("start %d", code)
	}
	// 1 initial run + 2 restarts, then gives up in state failed with exit code 3
	waitFor(t, 40*time.Second, "restarts exhausted", func() bool {
		a = c.agent(a.ID)
		return a.Status.Restarts >= 2 && a.Status.State == api.StateFailed
	})
	if a.Status.ExitCode == nil || *a.Status.ExitCode != 3 {
		t.Fatalf("exit code %v", a.Status.ExitCode)
	}
	time.Sleep(2500 * time.Millisecond) // no further restarts beyond MaxRestarts
	if a2 := c.agent(a.ID); a2.Status.Restarts != a.Status.Restarts || a2.Status.State != api.StateFailed {
		t.Fatalf("restarted beyond max_restarts: %+v", a2.Status)
	}
	var logs api.LogsResponse
	c.do("GET", "/agents/"+a.ID+"/logs?tail=100", nil, &logs)
	boots := 0
	for _, l := range logs.Lines {
		if strings.Contains(l.Text, "boot crashy") {
			boots++
		}
	}
	if boots != 3 {
		t.Fatalf("want 3 boots in logs, got %d", boots)
	}

	// restart policy "always": restarts even after a clean exit
	var b api.Agent
	c.do("POST", "/agents", api.AgentSpec{NodeID: n.ID, Name: "always", Command: "sh",
		Args: []string{"-c", "sleep 0.2; exit 0"}, Restart: api.RestartAlways, RestartDelaySec: 1, AutoStart: true}, &b)
	waitFor(t, 30*time.Second, "always restarts", func() bool { return c.agent(b.ID).Status.Restarts >= 2 })
	c.do("POST", "/agents/"+b.ID+"/stop", nil, nil)
	waitFor(t, 15*time.Second, "always stopped", func() bool { return c.agent(b.ID).Status.State == api.StateStopped })
}

func TestBinaryNodeReconnect(t *testing.T) {
	port := freePort(t)
	srvData, nodeData := t.TempDir(), t.TempDir()
	srv, c := startServer(t, port, srvData, false)
	token := c.nodeToken()
	node := startProc(t, "node", "node", "--server", c.base, "--token", token, "--name", "remote-e2e", "--data", nodeData)
	n := c.onlineNode("remote-e2e")

	var a api.Agent
	if code := c.do("POST", "/agents", api.AgentSpec{NodeID: n.ID, Name: "sleeper", Command: "sh",
		Args: []string{"-c", "while true; do echo alive; sleep 0.5; done"}}, &a); code != 201 {
		t.Fatalf("create %d", code)
	}
	c.do("POST", "/agents/"+a.ID+"/start", nil, nil)
	waitFor(t, 15*time.Second, "running", func() bool {
		a = c.agent(a.ID)
		return a.Status.State == api.StateRunning && a.Status.PID > 0
	})
	pid := a.Status.PID
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL); syscall.Kill(pid, syscall.SIGKILL) })

	// --- server goes away: agent must keep running on the node ---
	srv.stop(syscall.SIGTERM)
	time.Sleep(1500 * time.Millisecond)
	if !pidAlive(pid) {
		t.Fatal("agent process died while server was down")
	}
	srv2, c2 := startServer(t, port, srvData, false)
	_ = srv2
	n2 := c2.onlineNode("remote-e2e")
	if n2.ID != n.ID {
		t.Fatalf("node identity changed: %s -> %s", n.ID, n2.ID)
	}
	waitFor(t, 15*time.Second, "status resync after reconnect", func() bool {
		s := c2.agent(a.ID).Status
		return s.State == api.StateRunning && s.PID == pid
	})
	// commands work again over the new session
	var logs api.LogsResponse
	waitFor(t, 10*time.Second, "logs after reconnect", func() bool {
		c2.do("GET", "/agents/"+a.ID+"/logs?tail=10", nil, &logs)
		return len(logs.Lines) > 0
	})

	// --- node goes offline: node offline, agent state unknown ---
	node.stop(syscall.SIGKILL)
	waitFor(t, 20*time.Second, "node offline", func() bool {
		var nn api.Node
		c2.do("GET", "/nodes/"+n.ID, nil, &nn)
		return nn.Status == api.NodeOffline
	})
	if s := c2.agent(a.ID).Status; s.State != api.StateUnknown {
		t.Fatalf("agent state on offline node = %q, want unknown", s.State)
	}
	var list []api.Agent
	c2.do("GET", "/agents?node_id="+n.ID, nil, &list)
	if len(list) != 1 || list[0].Status.State != api.StateUnknown {
		t.Fatalf("list on offline node: %+v", list)
	}
	if code := c2.do("POST", "/agents/"+a.ID+"/start", nil, nil); code != http.StatusConflict {
		t.Fatalf("start on offline node: %d, want 409", code)
	}
}

func TestBinaryVNCTunnel(t *testing.T) {
	// fake RFB server: greeting, then echo
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.Write([]byte("RFB 003.008\n"))
				io.Copy(conn, conn)
			}()
		}
	}()

	port := freePort(t)
	_, c := startServer(t, port, t.TempDir(), false)
	startProc(t, "node", "node", "--server", c.base, "--token", c.nodeToken(), "--name", "vnc-e2e",
		"--data", t.TempDir(), "--vnc", l.Addr().String())
	n := c.onlineNode("vnc-e2e")

	url := fmt.Sprintf("ws://127.0.0.1:%d/api/v1/nodes/%s/desktop?kind=vnc&token=%s", port, n.ID, c.token)
	hdr := http.Header{"Origin": {c.base}}
	ws, resp, err := websocket.DefaultDialer.Dial(url, hdr)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial desktop: %v (%d)", err, code)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got []byte
	for len(got) < 12 {
		_, b, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b...)
	}
	if string(got[:12]) != "RFB 003.008\n" {
		t.Fatalf("greeting %q", got)
	}
	ws.WriteMessage(websocket.BinaryMessage, []byte("RFB 003.008\n"))
	got = got[12:]
	for len(got) < 12 {
		_, b, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b...)
	}
	if string(got) != "RFB 003.008\n" {
		t.Fatalf("echo %q", got)
	}
}
