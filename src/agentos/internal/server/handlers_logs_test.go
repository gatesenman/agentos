//go:build !windows

package server_test

// M3 end-to-end tests: in-process server + node over real HTTP/WebSocket.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	"github.com/gorilla/websocket"
)

type env struct {
	t        *testing.T
	ts       *httptest.Server
	token    string
	nodeTok  string
	dataDir  string
	nodeID   string
	stopNode func()
}

func (e *env) do(method, path string, body, out any) *http.Response {
	e.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+"/api/v1"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if out != nil {
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp
}

func eventually(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func (e *env) startNode() {
	ctx, cancel := context.WithCancel(context.Background())
	n, err := node.New(node.Config{ServerURL: e.ts.URL, Token: e.nodeTok, Name: "n1", DataDir: e.dataDir})
	if err != nil {
		e.t.Fatal(err)
	}
	e.nodeID = n.ID()
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	e.stopNode = func() { cancel(); <-done }
	eventually(e.t, 10*time.Second, "node online", func() bool {
		var nd api.Node
		e.do("GET", "/nodes/"+e.nodeID, nil, &nd)
		return nd.Status == api.NodeOnline
	})
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	am, boot, err := auth.New(ctx, st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(server.Config{}, st, am).Handler())
	e := &env{t: t, ts: ts, nodeTok: boot.NodeToken, dataDir: filepath.Join(dir, "node")}
	var lr struct{ Token string }
	e.do("POST", "/auth/login", map[string]string{"password": "secret123"}, &lr)
	if lr.Token == "" {
		t.Fatal("login failed")
	}
	e.token = lr.Token
	e.startNode()
	t.Cleanup(func() { e.stopNode(); ts.Close() })
	return e
}

func (e *env) createAgent(name, script string) api.Agent {
	var a api.Agent
	resp := e.do("POST", "/agents", api.AgentSpec{NodeID: e.nodeID, Name: name, Command: "sh", Args: []string{"-c", script}}, &a)
	if resp.StatusCode != 201 {
		e.t.Fatalf("create %d", resp.StatusCode)
	}
	if resp := e.do("POST", "/agents/"+a.ID+"/start", nil, &a); resp.StatusCode != 200 {
		e.t.Fatalf("start %d", resp.StatusCode)
	}
	return a
}

func (e *env) logs(id, query string) []api.LogLine {
	var lr api.LogsResponse
	resp := e.do("GET", "/agents/"+id+"/logs?"+query, nil, &lr)
	if resp.StatusCode != 200 {
		e.t.Fatalf("logs %s -> %d", query, resp.StatusCode)
	}
	return lr.Lines
}

func TestLogs20kTailFilterDownload(t *testing.T) {
	e := newEnv(t)
	a := e.createAgent("bulk 日志", `i=1; while [ $i -le 20000 ]; do echo "line $i"; i=$((i+1)); done; sleep 60`)
	eventually(t, 30*time.Second, "20000 lines", func() bool {
		l := e.logs(a.ID, "tail=1")
		return len(l) == 1 && l[0].Text == "line 20000"
	})
	got := e.logs(a.ID, "tail=100")
	if len(got) != 100 {
		t.Fatalf("tail=100 returned %d", len(got))
	}
	for i, l := range got {
		if want := fmt.Sprintf("line %d", 19901+i); l.Text != want {
			t.Fatalf("line %d: %q want %q", i, l.Text, want)
		}
		if i > 0 && l.Seq != got[i-1].Seq+1 {
			t.Fatalf("seq gap %d -> %d", got[i-1].Seq, l.Seq)
		}
	}
	// since=<seq>
	last := got[99].Seq
	if s := e.logs(a.ID, fmt.Sprintf("tail=100&since=%d", last-5)); len(s) != 5 || s[4].Seq != last {
		t.Fatalf("since: %d", len(s))
	}
	// since=<RFC3339> in the future -> nothing
	if s := e.logs(a.ID, "since="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); len(s) != 0 {
		t.Fatalf("since time: %d", len(s))
	}
	// q substring (case-insensitive) over the newest 5000 lines
	q := e.logs(a.ID, "q=LINE%201999")
	if len(q) != 10 || q[0].Text != "line 19990" || q[9].Text != "line 19999" {
		t.Fatalf("q: %d %v", len(q), q)
	}
	if q := e.logs(a.ID, "q=1999&tail=3"); len(q) != 3 || q[2].Text != "line 19999" {
		t.Fatalf("q tail: %v", q)
	}
	if resp := e.do("GET", "/agents/"+a.ID+"/logs?since=abc", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("bad since %d", resp.StatusCode)
	}
	// download
	resp := e.do("GET", "/agents/"+a.ID+"/logs/download", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download %d %v", resp.StatusCode, resp.Header)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != 5000 || !strings.HasSuffix(lines[4999], "[stdout] line 20000") || !strings.HasSuffix(lines[0], "line 15001") {
		t.Fatalf("download lines=%d first=%q last=%q", len(lines), lines[0], lines[len(lines)-1])
	}
	if resp := e.do("GET", "/agents/nope/logs/download", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("download unknown %d", resp.StatusCode)
	}
}

func TestLogStreamNoDuplicates(t *testing.T) {
	e := newEnv(t)
	a := e.createAgent("ticker", `i=0; while :; do i=$((i+1)); echo "tick $i"; sleep 0.005; done`)
	eventually(t, 10*time.Second, "some lines", func() bool { return len(e.logs(a.ID, "tail=30")) >= 30 })

	wsURL := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/api/v1/agents/" + a.ID + "/logs/stream?tail=50&token=" + e.token
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var seqs []uint64
	deadline := time.Now().Add(3 * time.Second)
	c.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		var ev struct {
			Type    string
			Payload api.LogLine
		}
		if err := c.ReadJSON(&ev); err != nil {
			break
		}
		if ev.Type != api.EventAgentLog || ev.Payload.AgentID != a.ID {
			t.Fatalf("unexpected event %+v", ev)
		}
		seqs = append(seqs, ev.Payload.Seq)
	}
	if len(seqs) < 100 {
		t.Fatalf("too few lines: %d", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not strictly increasing at %d: %d then %d", i, seqs[i-1], seqs[i])
		}
	}
	t.Logf("received %d lines (seq %d..%d)", len(seqs), seqs[0], seqs[len(seqs)-1])
}

func TestLogsReloadAfterNodeRestart(t *testing.T) {
	e := newEnv(t)
	a := e.createAgent("short", `for i in 1 2 3 4 5; do echo "run $i"; done`)
	eventually(t, 10*time.Second, "exited", func() bool {
		l := e.logs(a.ID, "tail=10")
		return len(l) > 0 && strings.Contains(l[len(l)-1].Text, "exited")
	})
	before := e.logs(a.ID, "tail=100")
	lastSeq := before[len(before)-1].Seq

	e.stopNode()
	// Wait until the server noticed, so the next query is answered by the new
	// node process (not the server-side fallback ring).
	eventually(t, 10*time.Second, "node offline", func() bool {
		var nd api.Node
		e.do("GET", "/nodes/"+e.nodeID, nil, &nd)
		return nd.Status != api.NodeOnline
	})
	e.startNode() // same data dir: logs reloaded from file on demand
	after := e.logs(a.ID, "tail=100")
	if len(after) < len(before) {
		t.Fatalf("after restart %d lines, before %d", len(after), len(before))
	}
	for i := range before {
		if after[i].Seq != before[i].Seq || after[i].Text != before[i].Text {
			t.Fatalf("line %d differs: %+v vs %+v", i, after[i], before[i])
		}
	}
	if resp := e.do("POST", "/agents/"+a.ID+"/start", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("restart %d", resp.StatusCode)
	}
	eventually(t, 10*time.Second, "new lines", func() bool {
		l := e.logs(a.ID, fmt.Sprintf("since=%d", lastSeq))
		return len(l) > 0 && l[0].Seq == lastSeq+1 && strings.Contains(l[len(l)-1].Text, "exited")
	})
}
