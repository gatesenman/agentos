//go:build !windows

// Package e2e holds end-to-end tests (owned by module M8). This test runs
// the phase-1 vertical slice fully in-process: server + node over real
// HTTP/WebSocket, create/start/stop an agent and read its logs.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
	"github.com/agentos/agentos/web"
)

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func (c *client) do(method, path string, body, out any) int {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestVerticalSlice(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	am, boot, err := auth.New(ctx, st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Config{WebFS: web.FS()}, st, am)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	n, err := node.New(node.Config{ServerURL: ts.URL, Token: boot.NodeToken, Name: "test-node", DataDir: filepath.Join(dir, "node")})
	if err != nil {
		t.Fatal(err)
	}
	go n.Run(ctx)

	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}

	// UI shell served
	resp, _ := http.Get(ts.URL + "/")
	if resp.StatusCode != 200 {
		t.Fatalf("ui %d", resp.StatusCode)
	}
	// auth required
	if code := c.do("GET", "/nodes", nil, nil); code != 401 {
		t.Fatalf("want 401, got %d", code)
	}
	if code := c.do("POST", "/auth/login", map[string]string{"password": "wrong"}, nil); code != 401 {
		t.Fatalf("bad login %d", code)
	}
	if code := c.do("POST", "/auth/login", map[string]string{"password": "secret123"}, nil); code != 200 {
		t.Fatalf("login %d", code)
	}
	// node registers
	var nodes []api.Node
	eventually(t, "node online", func() bool {
		c.do("GET", "/nodes", nil, &nodes)
		return len(nodes) == 1 && nodes[0].Status == api.NodeOnline
	})
	if nodes[0].ID != n.ID() || nodes[0].Name != "test-node" {
		t.Fatalf("node %+v", nodes[0])
	}
	// create agent
	var a api.Agent
	code := c.do("POST", "/agents", api.AgentSpec{NodeID: n.ID(), Name: "ticker", Command: "sh",
		Args: []string{"-c", `echo "hello from $AGENTOS_AGENT_NAME"; while true; do echo tick; sleep 0.2; done`}}, &a)
	if code != 201 || a.ID == "" {
		t.Fatalf("create %d %+v", code, a)
	}
	if code := c.do("POST", "/agents/"+a.ID+"/start", nil, &a); code != 200 {
		t.Fatalf("start %d", code)
	}
	eventually(t, "running", func() bool {
		c.do("GET", "/agents/"+a.ID, nil, &a)
		return a.Status.State == api.StateRunning && a.Status.PID > 0
	})
	var logs api.LogsResponse
	eventually(t, "logs", func() bool {
		c.do("GET", "/agents/"+a.ID+"/logs?tail=50", nil, &logs)
		var all []string
		for _, l := range logs.Lines {
			all = append(all, l.Text)
		}
		j := strings.Join(all, "\n")
		return strings.Contains(j, "hello from ticker") && strings.Contains(j, "tick")
	})
	if code := c.do("POST", "/agents/"+a.ID+"/stop", nil, &a); code != 200 {
		t.Fatalf("stop %d", code)
	}
	eventually(t, "stopped", func() bool {
		c.do("GET", "/agents/"+a.ID, nil, &a)
		return a.Status.State == api.StateStopped
	})
	if code := c.do("DELETE", "/agents/"+a.ID, nil, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	var list []api.Agent
	c.do("GET", "/agents", nil, &list)
	if len(list) != 0 {
		t.Fatalf("agents left: %d", len(list))
	}
}
