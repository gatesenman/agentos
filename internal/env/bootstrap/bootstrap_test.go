package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/env"
	_ "github.com/agentos/agentos/internal/env/providers/docker"
	_ "github.com/agentos/agentos/internal/env/providers/ssh"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// buildAgentOS cross-builds cmd/agentos for linux/<GOARCH> into a dist dir.
func buildAgentOS(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	out := filepath.Join(dist, BinaryName("linux", runtime.GOARCH))
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "github.com/agentos/agentos/cmd/agentos")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agentos: %v\n%s", err, b)
	}
	return dist
}

// fakeServer accepts the node control WS, checks the token, answers hello with welcome.
func fakeServer(t *testing.T, listen, token string) (string, <-chan api.HelloPayload) {
	t.Helper()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	hellos := make(chan api.HelloPayload, 4)
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathNodeConnect, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "bad token", 401)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var envl api.Envelope
		if c.ReadJSON(&envl) != nil || envl.Type != api.MsgHello {
			return
		}
		var h api.HelloPayload
		json.Unmarshal(envl.Payload, &h)
		pl, _ := json.Marshal(api.WelcomePayload{NodeID: h.NodeID, ServerVersion: "test"})
		c.WriteJSON(api.Envelope{Type: api.MsgWelcome, Payload: pl})
		hellos <- h
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port, hellos
}

func waitHello(t *testing.T, ch <-chan api.HelloPayload, e env.Env, logPath string) api.HelloPayload {
	select {
	case h := <-ch:
		return h
	case <-time.After(30 * time.Second):
		var b []byte
		if rc, err := e.GetFile(context.Background(), logPath); err == nil {
			b, _ = io.ReadAll(rc)
			rc.Close()
		}
		t.Fatalf("node never connected; log:\n%s", b)
	}
	return api.HelloPayload{}
}

// TestInstallDocker: REAL docker container + freshly built agentos + fake server.
func TestInstallDocker(t *testing.T) {
	p, _ := env.Get("docker")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := p.Test(ctx, nil); err != nil || os.Getenv("AGENTOS_TEST_DOCKER") == "0" {
		t.Skipf("no docker: %v", err)
	}
	gw := dockerGateway(t)
	port, hellos := fakeServer(t, net.JoinHostPort(gw, "0"), "tok123")
	dist := buildAgentOS(t)
	rec := &env.Record{ID: fmt.Sprintf("bs-%d", time.Now().UnixNano()%1e6), Config: json.RawMessage(`{"image":"ubuntu:24.04"}`)}
	if err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	e, _ := p.Open(ctx, rec)
	defer e.Close()
	defer e.Delete(env.WithPurge(context.Background()))
	var logs []string
	res, err := DefaultInstaller{}.Install(ctx, e, Options{ServerURL: "http://" + net.JoinHostPort(gw, port), JoinToken: "tok123",
		NodeName: "容器节点", DistDir: dist, NodeID: "node-fixed-1", Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }})
	if err != nil {
		t.Fatalf("%v (logs %v)", err, logs)
	}
	t.Logf("result %+v", res)
	h := waitHello(t, hellos, e, res.LogPath)
	if h.NodeID != "node-fixed-1" || h.Name != "容器节点" || h.OS != "linux" || res.NodeID != "node-fixed-1" || res.Method != "setsid+nohup" {
		t.Fatalf("hello %+v res %+v", h, res)
	}
	// idempotent re-install replaces the running node
	res2, err := InstallAgentOS(ctx, e, Options{ServerURL: "http://" + net.JoinHostPort(gw, port), JoinToken: "tok123", DistDir: dist})
	if err != nil {
		t.Fatal(err)
	}
	h = waitHello(t, hellos, e, res2.LogPath)
	if h.NodeID != "node-fixed-1" || res2.PID == res.PID || res2.NodeID != "node-fixed-1" {
		t.Fatalf("reinstall hello %+v res %+v", h, res2)
	}
	out, _ := runOK(ctx, e, "/bin/sh", "-c", "ps -eo pid,args 2>/dev/null || ls /proc")
	if strings.Contains(out, "tok123") {
		t.Fatalf("token visible in process list: %s", out)
	}
}

func dockerGateway(t *testing.T) string {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}}
	resp, err := hc.Get("http://docker/networks/bridge")
	if err != nil {
		t.Skip(err)
	}
	defer resp.Body.Close()
	var n struct {
		IPAM struct{ Config []struct{ Gateway string } }
	}
	json.NewDecoder(resp.Body).Decode(&n)
	if len(n.IPAM.Config) == 0 || n.IPAM.Config[0].Gateway == "" {
		t.Skip("no bridge gateway")
	}
	return n.IPAM.Config[0].Gateway
}

// TestInstallSSH: REAL local OpenSSH sshd (unprivileged) as the target.
func TestInstallSSH(t *testing.T) {
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil || os.Getenv("AGENTOS_TEST_SSHD") == "0" {
		t.Skip("no sshd")
	}
	dir := t.TempDir()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(hostPriv, "")
	os.WriteFile(filepath.Join(dir, "host_key"), pem.EncodeToMemory(blk), 0o600)
	_, cliPriv, _ := ed25519.GenerateKey(rand.Reader)
	cliSigner, _ := ssh.NewSignerFromKey(cliPriv)
	cblk, _ := ssh.MarshalPrivateKey(cliPriv, "")
	os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(cliSigner.PublicKey()), 0o600)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	sport := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	conf := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s/host_key\nPidFile %s/sshd.pid\nAuthorizedKeysFile %s/authorized_keys\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nSubsystem sftp /usr/lib/openssh/sftp-server\n", sport, dir, dir, dir)
	os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(conf), 0o600)
	cmd := exec.Command(sshd, "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	var logb syncBuffer
	cmd.Stderr = &logb
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 300; i++ { // up to 30s on a loaded box
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sport)); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	user, _ := exec.Command("id", "-un").Output()
	port, hellos := fakeServer(t, "127.0.0.1:0", "sshtok")
	raw, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": sport, "user": strings.TrimSpace(string(user)), "private_key": string(pem.EncodeToMemory(cblk))})
	p, _ := env.Get("ssh")
	rec := &env.Record{ID: "bs-ssh", Config: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := p.Create(ctx, rec); err != nil {
		t.Fatalf("%v\n%s", err, logb.String())
	}
	e, _ := p.Open(ctx, rec)
	defer e.Close()
	inst := t.TempDir()
	res, err := InstallAgentOS(ctx, e, Options{ServerURL: "http://127.0.0.1:" + port, JoinToken: "sshtok", NodeName: "ssh-node",
		DistDir: buildAgentOS(t), InstallDir: inst})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("kill", fmt.Sprint(res.PID)).Run() })
	h := waitHello(t, hellos, e, res.LogPath)
	if h.Name != "ssh-node" || res.NodeID == "" || h.NodeID != res.NodeID {
		t.Fatalf("hello %+v res %+v", h, res)
	}
	// the node must survive the end of the SSH session / connection
	e.Close()
	time.Sleep(500 * time.Millisecond)
	if err := exec.Command("kill", "-0", fmt.Sprint(res.PID)).Run(); err != nil {
		t.Fatalf("node died with the ssh session: %v", err)
	}
}

func TestDetectAndErrors(t *testing.T) {
	if o, a := parseUname("Linux aarch64\n"); o != "linux" || a != "arm64" {
		t.Fatal(o, a)
	}
	if BinaryName("windows", "amd64") != "agentos-windows-amd64.exe" {
		t.Fatal()
	}
	if _, err := InstallAgentOS(context.Background(), nil, Options{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := InstallAgentOS(context.Background(), nil, Options{ServerURL: "http://x'; rm -rf /", JoinToken: "t"}); err == nil {
		t.Fatal("expected injection rejection")
	}
	var _ Installer = DefaultInstaller{}
}

// syncBuffer is a goroutine-safe buffer for subprocess output read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
