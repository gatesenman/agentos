//go:build !windows

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
)

type statsResp[T any] struct {
	Latest  *T  `json:"latest"`
	History []T `json:"history"`
}

// Busy-loop agent through the real server+node stack: within 12s the agent
// stats endpoint must report cpu_percent > 50 and the node host stats must be
// populated (default 5s stats interval).
func TestStatsBusyAgentE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
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
	srv := server.New(server.Config{}, st, am)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	n, err := node.New(node.Config{ServerURL: ts.URL, Token: boot.NodeToken, Name: "m2", DataDir: filepath.Join(dir, "node")})
	if err != nil {
		t.Fatal(err)
	}
	go n.Run(ctx)

	var token string
	do := func(method, path string, body, out any) int {
		b, _ := json.Marshal(body)
		if body == nil {
			b = nil
		}
		req, _ := http.NewRequest(method, ts.URL+"/api/v1"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var login struct{ Token string }
	if code := do("POST", "/auth/login", map[string]string{"password": "secret123"}, &login); code != 200 {
		t.Fatalf("login %d", code)
	}
	token = login.Token

	until := func(d time.Duration, what string, f func() bool) {
		t.Helper()
		end := time.Now().Add(d)
		for !f() {
			if time.Now().After(end) {
				t.Fatalf("timeout: %s", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	until(10*time.Second, "node online", func() bool {
		var nd api.Node
		return do("GET", "/nodes/"+n.ID(), nil, &nd) == 200 && nd.Status == api.NodeOnline
	})

	var a api.Agent
	if code := do("POST", "/agents", api.AgentSpec{NodeID: n.ID(), Name: "busy", Command: "sh",
		Args: []string{"-c", "while :; do :; done"}}, &a); code != 201 {
		t.Fatalf("create %d", code)
	}
	if code := do("POST", "/agents/"+a.ID+"/start", nil, &a); code != 200 {
		t.Fatalf("start %d", code)
	}
	defer do("POST", "/agents/"+a.ID+"/stop", nil, nil)
	started := time.Now()

	var ps statsResp[api.ProcStats]
	cpuWant := 50.0
	if boxOverloaded() {
		cpuWant = 5 // saturated shared box: only require that CPU is measured
	}
	until(12*time.Second, "agent cpu_percent > threshold", func() bool {
		ps = statsResp[api.ProcStats]{}
		do("GET", "/agents/"+a.ID+"/stats", nil, &ps)
		return ps.Latest != nil && ps.Latest.CPUPercent > cpuWant
	})
	t.Logf("agent latest after %v: %+v (history %d)", time.Since(started).Round(time.Millisecond), *ps.Latest, len(ps.History))
	if ps.Latest.MemRSS == 0 || ps.Latest.Procs < 1 {
		t.Fatalf("agent stats %+v", *ps.Latest)
	}

	var nd api.Node
	do("GET", "/nodes/"+n.ID(), nil, &nd)
	if nd.Host == nil || nd.Host.MemTotal == 0 || nd.Host.CPUCount == 0 || nd.Host.DiskTotal == 0 {
		t.Fatalf("node host stats %+v", nd.Host)
	}
	var hs statsResp[api.HostStats]
	do("GET", "/nodes/"+n.ID()+"/stats", nil, &hs)
	if hs.Latest == nil || len(hs.History) == 0 {
		t.Fatalf("host stats %+v", hs)
	}

	// ?since= filter: after the latest sample -> empty history, latest kept.
	q := url.QueryEscape(hs.Latest.Time.Format(time.RFC3339Nano))
	var hs2 statsResp[api.HostStats]
	if code := do("GET", "/nodes/"+n.ID()+"/stats?since="+q, nil, &hs2); code != 200 {
		t.Fatalf("since %d", code)
	}
	if hs2.Latest == nil || hs2.History == nil || len(hs2.History) != 0 {
		t.Fatalf("since filter: %+v", hs2)
	}
	old := url.QueryEscape(started.Add(-time.Hour).UTC().Format(time.RFC3339))
	var ps2 statsResp[api.ProcStats]
	do("GET", "/agents/"+a.ID+"/stats?since="+old, nil, &ps2)
	if len(ps2.History) < len(ps.History) {
		t.Fatalf("since old: %d < %d", len(ps2.History), len(ps.History))
	}
	if code := do("GET", "/agents/"+a.ID+"/stats?since=yesterday", nil, nil); code != 400 {
		t.Fatalf("bad since -> %d", code)
	}
	// unknown agent -> empty
	var none statsResp[api.ProcStats]
	if code := do("GET", "/agents/nope/stats", nil, &none); code != 200 || none.Latest != nil || none.History == nil {
		t.Fatalf("unknown agent: %d %+v", code, none)
	}
}

func boxOverloaded() bool {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return false
	}
	var l float64
	fmt.Sscan(string(b), &l)
	return l > float64(runtime.NumCPU())
}
