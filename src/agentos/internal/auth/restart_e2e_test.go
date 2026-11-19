//go:build !windows

package auth_test

// End-to-end: real `agentos` binary. A session cookie stays valid after the
// server process restarts; `agentos admin` subcommands work offline.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type proc struct {
	cmd  *exec.Cmd
	base string
	log  *bytes.Buffer
}

func startServer(t *testing.T, bin, data string) *proc {
	t.Helper()
	port := freePort(t)
	p := &proc{base: fmt.Sprintf("http://127.0.0.1:%d", port), log: &bytes.Buffer{}}
	p.cmd = exec.Command(bin, "server", "--listen", fmt.Sprintf("127.0.0.1:%d", port), "--data", data,
		"--admin-password", "secret123", "--with-node=false")
	p.cmd.Stdout, p.cmd.Stderr = p.log, p.log
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.cmd.Process.Kill(); p.cmd.Wait() })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := http.Get(p.base + "/api/v1/health"); err == nil {
			r.Body.Close()
			return p
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server did not start:\n%s", p.log)
	return nil
}

func (p *proc) stop(t *testing.T) {
	p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		p.cmd.Process.Kill()
		t.Fatalf("server did not stop:\n%s", p.log)
	}
}

func (p *proc) req(t *testing.T, method, path string, ck *http.Cookie, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, p.base+"/api/v1"+path, bytes.NewReader(b))
	if ck != nil {
		r.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestBinaryRestartKeepsSession(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentos")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/agentos")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data := filepath.Join(dir, "data")

	p := startServer(t, bin, data)
	resp := p.req(t, "POST", "/auth/login", nil, map[string]string{"password": "secret123"})
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatalf("login %d", resp.StatusCode)
	}
	ck := resp.Cookies()[0]
	if r := p.req(t, "GET", "/auth/me", ck, nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	p.stop(t)

	p = startServer(t, bin, data) // new process, new port
	if r := p.req(t, "GET", "/auth/me", ck, nil); r.StatusCode != 200 {
		t.Fatalf("cookie rejected after restart: %d\n%s", r.StatusCode, p.log)
	}

	// admin CLI against the live DB.
	out, err := exec.Command(bin, "admin", "rotate-node-token", "--data", data).CombinedOutput()
	m := regexp.MustCompile(`aos_[0-9a-f]+`).FindString(string(out))
	if err != nil || m == "" {
		t.Fatalf("rotate-node-token: %v\n%s", err, out)
	}
	nodeReq := func(tok string) int {
		r, _ := http.NewRequest("GET", p.base+"/api/v1/node/connect", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := nodeReq(m); c == 401 {
		t.Fatal("rotated node token rejected")
	}
	if c := nodeReq("aos_bogus"); c != 401 {
		t.Fatalf("bogus node token: %d", c)
	}

	if out, err := exec.Command(bin, "admin", "reset-password", "--data", data, "brandnew1").CombinedOutput(); err != nil {
		t.Fatalf("reset-password: %v\n%s", err, out)
	}
	if r := p.req(t, "GET", "/auth/me", ck, nil); r.StatusCode != 401 {
		t.Fatalf("session survived reset-password: %d", r.StatusCode)
	}
	if r := p.req(t, "POST", "/auth/login", nil, map[string]string{"password": "brandnew1"}); r.StatusCode != 200 {
		t.Fatalf("new password login %d", r.StatusCode)
	}
	p.stop(t)

	if out, err := exec.Command(bin, "admin", "rotate-node-token", "--data", filepath.Join(dir, "missing")).CombinedOutput(); err == nil {
		t.Fatalf("expected error for missing DB:\n%s", out)
	}
}
