//go:build !windows

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	envpkg "github.com/agentos/agentos/internal/env"
	_ "github.com/agentos/agentos/internal/env/providers/all"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
	"github.com/gorilla/websocket"
)

// ---- fake provider with a secret field ----

type fakeProvider struct {
	mu     sync.Mutex
	opened []json.RawMessage
}

const fakeType = "fake-secret"

func (p *fakeProvider) Info() envpkg.ProviderInfo {
	return envpkg.ProviderInfo{Type: fakeType, Name: "fake", Kind: envpkg.KindVM, Group: "测试",
		Capabilities: []envpkg.Capability{envpkg.CapLifecycle},
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"},"password":{"type":"string","x-secret":true}}}`)}
}
func (p *fakeProvider) Validate(cfg json.RawMessage) error                   { return nil }
func (p *fakeProvider) Test(ctx context.Context, cfg json.RawMessage) error  { return nil }
func (p *fakeProvider) Create(ctx context.Context, rec *envpkg.Record) error { return nil }
func (p *fakeProvider) Open(ctx context.Context, rec *envpkg.Record) (envpkg.Env, error) {
	p.mu.Lock()
	p.opened = append(p.opened, append(json.RawMessage(nil), rec.Config...))
	p.mu.Unlock()
	return fakeEnv{}, nil
}

type fakeEnv struct{}

func (fakeEnv) Status(context.Context) (envpkg.Status, error) {
	return envpkg.Status{State: envpkg.StateRunning}, nil
}
func (fakeEnv) Start(context.Context) error  { return nil }
func (fakeEnv) Stop(context.Context) error   { return nil }
func (fakeEnv) Delete(context.Context) error { return nil }
func (fakeEnv) Exec(context.Context, envpkg.ExecRequest) (envpkg.ExecSession, error) {
	return nil, envpkg.ErrUnsupported
}
func (fakeEnv) PutFile(context.Context, string, io.Reader, uint32) error {
	return envpkg.ErrUnsupported
}
func (fakeEnv) GetFile(context.Context, string) (io.ReadCloser, error) {
	return nil, envpkg.ErrUnsupported
}
func (fakeEnv) ListDir(context.Context, string) ([]envpkg.FileInfo, error) {
	return nil, envpkg.ErrUnsupported
}
func (fakeEnv) Desktop(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, envpkg.ErrUnsupported
}
func (fakeEnv) Metrics(context.Context) (envpkg.Metrics, error) {
	return envpkg.Metrics{}, envpkg.ErrUnsupported
}
func (fakeEnv) Close() error { return nil }

var fake = &fakeProvider{}

func init() { envpkg.Register(fake) }

// ---- harness ----

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// agentosBinary builds cmd/agentos once (local instances run `agentos node`).
func agentosBinary(t *testing.T) string {
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentos-e2e-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "agentos")
		cmd := exec.Command("go", "build", "-o", binPath, "github.com/agentos/agentos/cmd/agentos")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = err
			binPath = string(out)
		}
	})
	if binErr != nil {
		t.Fatalf("build agentos: %v\n%s", binErr, binPath)
	}
	return binPath
}

type envHarness struct {
	t      *testing.T
	base   string // client base URL ("" = ts.URL)
	ts     *httptest.Server
	srv    *server.Server
	st     store.Store
	dir    string
	token  string
	nodeTk string
}

func newEnvHarness(t *testing.T) *envHarness {
	exe := agentosBinary(t)
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	am, boot, err := auth.New(ctx, st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Config{DataDir: filepath.Join(dir, "server")}, st, am)
	ts := httptest.NewServer(srv.Handler())
	if err := srv.EnableEnvs(ctx, server.EnvOptions{LocalServerURL: ts.URL, Exe: exe, SampleInterval: time.Second}); err != nil {
		t.Fatal(err)
	}
	h := &envHarness{t: t, ts: ts, srv: srv, st: st, dir: dir, nodeTk: boot.NodeToken}
	t.Cleanup(func() {
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

func (h *envHarness) req(method, path string, body io.Reader) *http.Response {
	h.t.Helper()
	base := h.base
	if base == "" {
		base = h.ts.URL
	}
	req, _ := http.NewRequest(method, base+"/api/v1"+path, body)
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *envHarness) do(method, path string, body, out any) int {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	resp := h.req(method, path, rd)
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func until(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("timeout: %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type envView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	NodeID       string            `json:"node_id"`
	Stack        string            `json:"stack"`
	State        string            `json:"state"`
	Config       map[string]any    `json:"config"`
	Labels       map[string]string `json:"labels"`
	SecretsSet   []string          `json:"secrets_set"`
	Capabilities []string          `json:"capabilities"`
	Status       struct {
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"status"`
}

func (h *envHarness) wsDial(path string) *websocket.Conn {
	h.t.Helper()
	base := h.base
	if base == "" {
		base = h.ts.URL
	}
	u := "ws" + strings.TrimPrefix(base, "http") + "/api/v1" + path
	hdr := http.Header{"Authorization": {"Bearer " + h.token}}
	c, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		code := 0
		var b []byte
		if resp != nil {
			code = resp.StatusCode
			b, _ = io.ReadAll(resp.Body)
		}
		h.t.Fatalf("dial %s: %v (%d %s)", path, err, code, b)
	}
	return c
}

// execRun runs cmd over the exec WebSocket and returns output and exit code.
func (h *envHarness) execRun(envID string, cmd []string) (string, int) {
	h.t.Helper()
	b, _ := json.Marshal(cmd)
	c := h.wsDial("/envs/" + envID + "/exec?cmd=" + url.QueryEscape(string(b)))
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(20 * time.Second))
	var out bytes.Buffer
	for {
		t, data, err := c.ReadMessage()
		if err != nil {
			h.t.Fatalf("exec read: %v (out %q)", err, out.String())
		}
		if t == websocket.BinaryMessage {
			out.Write(data)
			continue
		}
		var m struct {
			Type    string `json:"type"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		json.Unmarshal(data, &m)
		switch m.Type {
		case "exit":
			return out.String(), m.Code
		case "error":
			h.t.Fatalf("exec error: %s", m.Message)
		}
	}
}

// ---- tests ----

func TestEnvsLocalInstancesE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: spawns agentos node processes")
	}
	h := newEnvHarness(t)
	var provs []envpkg.ProviderInfo
	if h.do("GET", "/providers", nil, &provs) != 200 || len(provs) < 2 {
		t.Fatalf("providers: %+v", provs)
	}

	ids := []string{}
	for _, name := range []string{"实例A", "instB"} {
		var v envView
		if code := h.do("POST", "/envs", map[string]any{"name": name, "provider": "agentos-local", "config": map[string]any{}}, &v); code != 201 {
			t.Fatalf("create %s: %d", name, code)
		}
		if v.State != "creating" || v.NodeID == "" {
			t.Fatalf("create view: %+v", v)
		}
		ids = append(ids, v.ID)
	}
	views := map[string]envView{}
	for _, id := range ids {
		until(t, 40*time.Second, "env "+id+" running", func() bool {
			var v envView
			ok := h.do("GET", "/envs/"+id, nil, &v) == 200 && v.Status.State == "running"
			views[id] = v
			return ok
		})
	}
	var all []envView
	h.do("GET", "/envs", nil, &all)
	if len(all) != 2 {
		t.Fatalf("expected 2 envs (no auto duplicates), got %d: %+v", len(all), all)
	}

	for i, id := range ids {
		v := views[id]
		var nd api.Node
		if h.do("GET", "/nodes/"+v.NodeID, nil, &nd) != 200 || nd.Status != api.NodeOnline {
			t.Fatalf("node of %s: %+v", id, nd)
		}
		// agent on the env's node
		var a api.Agent
		if code := h.do("POST", "/agents", api.AgentSpec{NodeID: v.NodeID, Name: "a" + string(rune('0'+i)), Command: "sh",
			Args: []string{"-c", "echo ready; sleep 60"}}, &a); code != 201 {
			t.Fatalf("create agent: %d", code)
		}
		if code := h.do("POST", "/agents/"+a.ID+"/start", nil, &a); code != 200 {
			t.Fatalf("start agent: %d", code)
		}
		until(t, 15*time.Second, "agent running", func() bool {
			var x api.Agent
			return h.do("GET", "/agents/"+a.ID, nil, &x) == 200 && x.Status.State == api.StateRunning
		})
		var list []api.Agent
		if h.do("GET", "/agents?env_id="+id, nil, &list) != 200 || len(list) != 1 || list[0].ID != a.ID {
			t.Fatalf("agents?env_id: %+v", list)
		}

		out, code := h.execRun(id, []string{"echo", "hi"})
		if code != 0 || strings.TrimSpace(out) != "hi" {
			t.Fatalf("exec: %q code %d", out, code)
		}
		if _, code := h.execRun(id, []string{"sh", "-c", "exit 3"}); code != 3 {
			t.Fatalf("exit code %d", code)
		}

		payload := "hello 文件 " + id
		resp := h.req("PUT", "/envs/"+id+"/files/content?path=sub/t.txt&mode=0600", strings.NewReader(payload))
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("put file %d", resp.StatusCode)
		}
		resp = h.req("GET", "/envs/"+id+"/files/content?path=sub/t.txt", nil)
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(got) != payload {
			t.Fatalf("get file %d %q", resp.StatusCode, got)
		}
		var fl []envpkg.FileInfo
		if h.do("GET", "/envs/"+id+"/files?path=sub", nil, &fl) != 200 || len(fl) != 1 || fl[0].Name != "t.txt" || fl[0].Size != int64(len(payload)) {
			t.Fatalf("list: %+v", fl)
		}
		resp = h.req("GET", "/envs/"+id+"/files/content?path=nope.txt", nil)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("missing file: %d", resp.StatusCode)
		}
		var logs []server.EnvLogEntry
		if h.do("GET", "/envs/"+id+"/logs?tail=50", nil, &logs) != 200 || len(logs) == 0 {
			t.Fatalf("env logs empty")
		}
	}

	// stop A -> offline; start -> back online
	a := ids[0]
	var v envView
	if code := h.do("POST", "/envs/"+a+"/stop", nil, &v); code != 200 {
		t.Fatalf("stop %d", code)
	}
	until(t, 20*time.Second, "env stopped", func() bool {
		return h.do("GET", "/envs/"+a, nil, &v) == 200 && v.Status.State == "stopped"
	})
	if code := h.do("POST", "/envs/"+a+"/start", nil, &v); code != 200 {
		t.Fatalf("start %d", code)
	}
	until(t, 30*time.Second, "env running again", func() bool {
		return h.do("GET", "/envs/"+a, nil, &v) == 200 && v.Status.State == "running"
	})

	for _, id := range ids {
		nodeID := views[id].NodeID
		if code := h.do("DELETE", "/envs/"+id+"?purge=1", nil, nil); code != 204 {
			t.Fatalf("delete %d", code)
		}
		if h.do("GET", "/envs/"+id, nil, nil) != 404 {
			t.Fatal("env still there")
		}
		if h.do("GET", "/nodes/"+nodeID, nil, nil) != 404 {
			t.Fatal("node record not removed")
		}
		if _, err := os.Stat(filepath.Join(h.dir, "server", "localos", id)); !os.IsNotExist(err) {
			t.Fatalf("instance data not purged: %v", err)
		}
	}
}

func TestEnvsRemoteJoinE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: spawns an agentos node process")
	}
	h := newEnvHarness(t)
	exe := agentosBinary(t)
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"name": "远程机", "provider": "agentos-remote", "config": map[string]any{}}, &v); code != 201 {
		t.Fatalf("create %d", code)
	}
	id := v.ID
	var join struct {
		Token     string            `json:"token"`
		ServerURL string            `json:"server_url"`
		Commands  map[string]string `json:"commands"`
		Service   map[string]string `json:"service_commands"`
	}
	if code := h.do("POST", "/envs/"+id+"/join", nil, &join); code != 200 || !strings.HasPrefix(join.Token, "aosj_") ||
		join.ServerURL != h.ts.URL || !strings.Contains(join.Commands["linux"], join.Token) || !strings.Contains(join.Commands["windows"], "install.ps1") ||
		!strings.Contains(join.Service["linux"], "--install-service") {
		t.Fatalf("join %d %+v", code, join)
	}
	// install script and own-platform binary are downloadable without auth
	for _, f := range []string{"install.sh", "install.ps1", "join.sh", "join.ps1"} {
		resp, err := http.Get(h.ts.URL + "/download/" + f)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("download %s: %v %v", f, err, resp)
		}
		resp.Body.Close()
	}

	runNode := func(data, token string) *exec.Cmd {
		cmd := exec.Command(exe, "node", "--server", h.ts.URL, "--token", token, "--name", "ignored", "--data", data)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		return cmd
	}
	n1 := runNode(filepath.Join(h.dir, "remote1"), join.Token)
	until(t, 30*time.Second, "remote env running", func() bool {
		return h.do("GET", "/envs/"+id, nil, &v) == 200 && v.Status.State == "running" && v.NodeID != ""
	})
	var nd api.Node
	if h.do("GET", "/nodes/"+v.NodeID, nil, &nd) != 200 || nd.Name != "远程机" {
		t.Fatalf("node name not forced to env name: %+v", nd)
	}
	if out, code := h.execRun(id, []string{"echo", "hi"}); code != 0 || strings.TrimSpace(out) != "hi" {
		t.Fatalf("remote exec %q %d", out, code)
	}

	// the one-time token is bound: a different node using it is rejected
	runNode(filepath.Join(h.dir, "remote2"), join.Token)
	time.Sleep(3 * time.Second)
	var nodes []api.Node
	h.do("GET", "/nodes", nil, &nodes)
	if len(nodes) != 1 {
		t.Fatalf("second node with a bound token was accepted: %+v", nodes)
	}

	// rotating the token revokes the old one (connect attempt -> 401)
	old := join.Token
	if code := h.do("POST", "/envs/"+id+"/join", nil, &join); code != 200 || join.Token == old {
		t.Fatalf("rejoin %d", code)
	}
	req, _ := http.NewRequest("GET", h.ts.URL+api.PathNodeConnect, nil)
	req.Header.Set("Authorization", "Bearer "+old)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("revoked token: %d", resp.StatusCode)
	}
	n1.Process.Kill()
	n1.Wait()

	// the re-issued token binds a fresh node; deleting the env revokes it
	runNode(filepath.Join(h.dir, "remote3"), join.Token)
	until(t, 30*time.Second, "rejoined", func() bool {
		return h.do("GET", "/envs/"+id, nil, &v) == 200 && v.Status.State == "running"
	})
	nodeID := v.NodeID
	if code := h.do("DELETE", "/envs/"+id, nil, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	time.Sleep(2 * time.Second)
	if h.do("GET", "/nodes/"+nodeID, nil, nil) != 404 {
		t.Fatal("deleted env's node reappeared")
	}
	var envs []envView
	h.do("GET", "/envs", nil, &envs)
	if len(envs) != 0 {
		t.Fatalf("envs after delete: %+v", envs)
	}
}

func TestEnvsSecretsNeverReturned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	am, _, _ := auth.New(ctx, st, "secret123")
	srv := server.New(server.Config{DataDir: filepath.Join(dir, "server")}, st, am)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	h := &envHarness{t: t, ts: ts, srv: srv, st: st, dir: dir}
	var login struct{ Token string }
	h.do("POST", "/auth/login", map[string]string{"password": "secret123"}, &login)
	h.token = login.Token

	const pw = "S3cr3t-pa55"
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"name": "vm", "provider": fakeType,
		"config": map[string]any{"host": "10.0.0.1", "password": pw}}, &v); code != 201 {
		t.Fatalf("create %d", code)
	}
	check := func(path string) {
		resp := h.req("GET", path, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(b), pw) {
			t.Fatalf("%s leaks the secret: %s", path, b)
		}
		if !strings.Contains(string(b), `"secrets_set":["password"]`) || !strings.Contains(string(b), "10.0.0.1") {
			t.Fatalf("%s: %s", path, b)
		}
	}
	until(t, 5*time.Second, "provisioned", func() bool {
		return h.do("GET", "/envs/"+v.ID, nil, &v) == 200 && v.Status.State == "running"
	})
	check("/envs/" + v.ID)
	check("/envs")
	// PATCH without the secret keeps it; provider still receives it
	if code := h.do("PATCH", "/envs/"+v.ID, map[string]any{"config": map[string]any{"host": "10.0.0.2"}}, &v); code != 200 {
		t.Fatalf("patch %d", code)
	}
	if _, ok := v.Config["password"]; ok || len(v.SecretsSet) != 1 {
		t.Fatalf("patched view: %+v", v)
	}
	h.do("GET", "/envs/"+v.ID, nil, &v)
	fake.mu.Lock()
	last := string(fake.opened[len(fake.opened)-1])
	fake.mu.Unlock()
	if !strings.Contains(last, pw) || !strings.Contains(last, "10.0.0.2") {
		t.Fatalf("provider did not get merged config: %s", last)
	}
	// at rest: sealed
	row, err := st.(store.EnvStore).GetEnv(ctx, v.ID)
	if err != nil || strings.Contains(string(row.Config), pw) || strings.Contains(row.Secrets, pw) || row.Secrets == "" {
		t.Fatalf("stored row: %+v %v", row, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server", "secret.key")); err != nil {
		t.Fatalf("secret key file: %v", err)
	}
	// clearing with null
	id := v.ID
	v = envView{}
	h.do("PATCH", "/envs/"+id, map[string]any{"config": map[string]any{"host": "h", "password": nil}}, &v)
	if len(v.SecretsSet) != 0 || v.ID != id {
		t.Fatalf("secret not cleared: %+v", v)
	}
	if h.do("DELETE", "/envs/"+v.ID, nil, nil) != 204 {
		t.Fatal("delete")
	}
}

func TestEnvsBuiltinUndeletable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	am, _, _ := auth.New(ctx, st, "secret123")
	srv := server.New(server.Config{DataDir: filepath.Join(dir, "server")}, st, am)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if err := srv.EnableEnvs(ctx, server.EnvOptions{LocalServerURL: ts.URL, BuiltinNodeID: "builtin-node"}); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	h := &envHarness{t: t, ts: ts, srv: srv, st: st, dir: dir}
	var login struct{ Token string }
	h.do("POST", "/auth/login", map[string]string{"password": "secret123"}, &login)
	h.token = login.Token
	var all []envView
	if h.do("GET", "/envs", nil, &all) != 200 || len(all) != 1 || all[0].ID != "local" || all[0].Name != "本机" ||
		all[0].Labels["builtin"] != "true" || all[0].NodeID != "builtin-node" || all[0].Provider != "agentos-local" {
		t.Fatalf("builtin env: %+v", all)
	}
	if code := h.do("DELETE", "/envs/local", nil, nil); code != 409 {
		t.Fatalf("delete builtin: %d", code)
	}
	if code := h.do("POST", "/envs/local/stop", nil, nil); code != 409 {
		t.Fatalf("stop builtin: %d", code)
	}
	// labels.builtin cannot be forged or removed
	var v envView
	h.do("PATCH", "/envs/local", map[string]any{"labels": map[string]string{"x": "1"}}, &v)
	if v.Labels["builtin"] != "true" || v.Labels["x"] != "1" {
		t.Fatalf("labels: %+v", v.Labels)
	}
	v = envView{}
	if code := h.do("POST", "/envs", map[string]any{"name": "x", "provider": fakeType, "labels": map[string]string{"builtin": "true"}}, &v); code != 201 || v.Labels["builtin"] != "" {
		t.Fatalf("forged builtin: %d %+v", code, v.Labels)
	}
}
