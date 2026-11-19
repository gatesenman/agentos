//go:build linux

package server_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
	"golang.org/x/crypto/ssh"
)

// newPhase2Harness: server listening on all interfaces (containers reach it via
// the bridge gateway), dist dir with the real agentos binary, embedded node as
// the built-in env.
func newPhase2Harness(t *testing.T) *envHarness {
	exe := agentosBinary(t)
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	os.MkdirAll(dist, 0o755)
	if err := os.Symlink(exe, filepath.Join(dist, "agentos-linux-amd64")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	am, boot, err := auth.New(ctx, st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Config{DataDir: filepath.Join(dir, "server"), DistDir: dist}, st, am)
	ts := httptest.NewUnstartedServer(srv.Handler())
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	base := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	n, err := node.New(node.Config{ServerURL: base, Token: boot.NodeToken, Name: "builtin", DataDir: filepath.Join(dir, "node")})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.EnableEnvs(ctx, server.EnvOptions{LocalServerURL: base, Exe: exe, BuiltinNodeID: n.ID(), SampleInterval: time.Second}); err != nil {
		t.Fatal(err)
	}
	nodeDone := make(chan struct{})
	go func() { n.Run(ctx); close(nodeDone) }()
	h := &envHarness{t: t, ts: ts, srv: srv, st: st, dir: dir, nodeTk: boot.NodeToken, base: base}
	t.Cleanup(func() {
		cancel()
		select {
		case <-nodeDone:
		case <-time.After(10 * time.Second):
		}
		srv.Close()
		ts.CloseClientConnections()
		ts.Close()
		st.Close()
	})
	var login struct{ Token string }
	if code := h.do("POST", "/auth/login", map[string]string{"password": "secret123"}, &login); code != 200 {
		t.Fatalf("login %d", code)
	}
	h.token = login.Token
	return h
}

func dockerAvailable() bool {
	c, err := net.DialTimeout("unix", "/var/run/docker.sock", time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return os.Getenv("AGENTOS_TEST_DOCKER") != "0"
}

func hasCapStr(cs []string, c string) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// Docker env through the API: provisioning, exec, install-agentos, agent on the bootstrapped node, purge.
func TestEnvsDockerInstallAgentOSE2E(t *testing.T) {
	if testing.Short() || !dockerAvailable() {
		t.Skip("needs docker (slow)")
	}
	h := newPhase2Harness(t)
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"name": "容器1", "provider": "docker",
		"config": map[string]any{"image": "ubuntu:24.04"}}, &v); code != 201 {
		t.Fatalf("create %d", code)
	}
	id := v.ID
	until(t, 3*time.Minute, "docker env running", func() bool {
		v = envView{}
		return h.do("GET", "/envs/"+id, nil, &v) == 200 && v.Status.State == "running"
	})
	if v.Config["container_id"] == nil {
		t.Fatalf("container_id not persisted: %+v", v.Config)
	}
	if hasCapStr(v.Capabilities, "desktop.vnc") || !hasCapStr(v.Capabilities, "install-agentos") {
		t.Fatalf("caps: %v", v.Capabilities)
	}
	containerID := v.Config["container_id"].(string)
	defer exec.Command("docker", "rm", "-f", containerID).Run()
	var logs []server.EnvLogEntry
	h.do("GET", "/envs/"+id+"/logs", nil, &logs)
	if !strings.Contains(fmt.Sprint(logs), "创建容器") {
		t.Fatalf("no provider progress in env logs: %+v", logs)
	}
	if out, code := h.execRun(id, []string{"echo", "hi"}); code != 0 || strings.TrimSpace(out) != "hi" {
		t.Fatalf("exec %q %d", out, code)
	}

	v = envView{}
	if code := h.do("POST", "/envs/"+id+"/install-agentos", nil, &v); code != 200 || v.NodeID == "" {
		t.Fatalf("install-agentos %d %+v", code, v)
	}
	nodeID := v.NodeID
	until(t, 2*time.Minute, "bootstrapped node online", func() bool {
		var nd api.Node
		return h.do("GET", "/nodes/"+nodeID, nil, &nd) == 200 && nd.Status == api.NodeOnline
	})
	v = envView{}
	h.do("GET", "/envs/"+id, nil, &v)
	if v.NodeID != nodeID || !hasCapStr(v.Capabilities, "agents") {
		t.Fatalf("env after install: %+v", v)
	}
	var a api.Agent
	if code := h.do("POST", "/agents", api.AgentSpec{NodeID: nodeID, Name: "in-container", Command: "sh",
		Args: []string{"-c", "echo ready; sleep 120"}}, &a); code != 201 {
		t.Fatalf("create agent %d", code)
	}
	h.do("POST", "/agents/"+a.ID+"/start", nil, &a)
	until(t, 30*time.Second, "agent running in container", func() bool {
		var x api.Agent
		return h.do("GET", "/agents/"+a.ID, nil, &x) == 200 && x.Status.State == api.StateRunning
	})

	if code := h.do("DELETE", "/envs/"+id+"?purge=1", nil, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if out, _ := exec.Command("docker", "inspect", containerID).CombinedOutput(); !strings.Contains(strings.ToLower(string(out)), "no such") {
		t.Fatalf("container survived purge: %s", out)
	}
}

func startSSHD(t *testing.T) (port int, user, keyPEM string) {
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil || os.Getenv("AGENTOS_TEST_SSHD") == "0" {
		t.Skip("no sshd")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	genKey := func() (ssh.Signer, string) {
		_, pk, _ := ed25519.GenerateKey(rand.Reader)
		b, err := ssh.MarshalPrivateKey(pk, "")
		if err != nil {
			t.Fatal(err)
		}
		s, _ := ssh.NewSignerFromKey(pk)
		return s, string(pem.EncodeToMemory(b))
	}
	_, hostPEM := genKey()
	os.WriteFile(filepath.Join(dir, "host_key"), []byte(hostPEM), 0o600)
	cli, cliPEM := genKey()
	os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(cli.PublicKey()), 0o600)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	sftp := "/usr/lib/openssh/sftp-server"
	if _, err := os.Stat(sftp); err != nil {
		sftp = "internal-sftp"
	}
	conf := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s/host_key\nPidFile %s/sshd.pid\nAuthorizedKeysFile %s/authorized_keys\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nAllowTcpForwarding yes\nSubsystem sftp %s\n",
		port, dir, dir, dir, sftp)
	os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(conf), 0o600)
	cmd := exec.Command(sshd, "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	if err := cmd.Start(); err != nil {
		t.Skipf("start sshd: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 300; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, _ := exec.Command("id", "-un").Output()
	return port, strings.TrimSpace(string(out)), cliPEM
}

// SSH env against a real local sshd; stacks catalog + detection on the built-in env.
func TestEnvsSSHAndStacksE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	h := newPhase2Harness(t)
	port, user, key := startSSHD(t)
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"name": "ssh1", "provider": "ssh",
		"config": map[string]any{"host": "127.0.0.1", "port": port, "user": user, "private_key": key}}, &v); code != 201 {
		t.Fatalf("create %d", code)
	}
	id := v.ID
	until(t, time.Minute, "ssh env provisioned", func() bool {
		v = envView{}
		return h.do("GET", "/envs/"+id, nil, &v) == 200 && v.Status.State == "running"
	})
	if s, _ := v.Config["host_key"].(string); !strings.HasPrefix(s, "SHA256:") || v.Config["os_detected"] != "linux" {
		t.Fatalf("host_key/os_detected not persisted: %+v", v.Config)
	}
	if _, leaked := v.Config["private_key"]; leaked || len(v.SecretsSet) != 1 {
		t.Fatalf("secret handling: %+v %v", v.Config, v.SecretsSet)
	}
	if hasCapStr(v.Capabilities, "lifecycle") {
		t.Fatalf("ssh without start/stop commands must not offer lifecycle: %v", v.Capabilities)
	}
	if out, code := h.execRun(id, []string{"echo", "hi"}); code != 0 || strings.TrimSpace(out) != "hi" {
		t.Fatalf("ssh exec %q %d", out, code)
	}
	if code := h.do("DELETE", "/envs/"+id, nil, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}

	// stacks
	var list []map[string]any
	if code := h.do("GET", "/stacks", nil, &list); code != 200 || len(list) != 18 {
		t.Fatalf("stacks: %d %d", code, len(list))
	}
	until(t, 20*time.Second, "built-in env online", func() bool {
		v = envView{}
		return h.do("GET", "/envs/local", nil, &v) == 200 && v.Status.State == "running"
	})
	var es struct {
		EnvID  string `json:"env_id"`
		OS     map[string]any
		Stacks []struct {
			ID        string `json:"id"`
			Installed bool   `json:"installed"`
		} `json:"stacks"`
	}
	if code := h.do("GET", "/envs/local/stacks", nil, &es); code != 200 || len(es.Stacks) != 18 || es.EnvID != "local" {
		t.Fatalf("detect on built-in: %d %+v", code, es)
	}
	if h.do("GET", "/envs/nope/stacks", nil, nil) != 404 {
		t.Fatal("unknown env should 404")
	}
	// install progress goes to the env log (fake env cannot exec -> failure is logged)
	var fv envView
	h.do("POST", "/envs", map[string]any{"name": "fake", "provider": fakeType}, &fv)
	fid := fv.ID
	until(t, 5*time.Second, "fake env", func() bool {
		fv = envView{}
		return h.do("GET", "/envs/"+fid, nil, &fv) == 200 && fv.Status.State == "running"
	})
	resp := h.req("POST", "/envs/"+fid+"/stacks/"+es.Stacks[0].ID+"/install", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stack install: %d", resp.StatusCode)
	}
	var logs []server.EnvLogEntry
	until(t, 20*time.Second, "stack install logged", func() bool {
		logs = nil
		h.do("GET", "/envs/"+fid+"/logs", nil, &logs)
		return strings.Contains(fmt.Sprint(logs), "[stack:")
	})
	t.Logf("stack install log: %+v", logs)
}
