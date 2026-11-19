package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/store"
)

func TestFedE2ERegisterInvokeLogs(t *testing.T) {
	var a2aURL string
	a2a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "MockA2A", "description": "test", "version": "1.0.0",
				"supportedInterfaces": []map[string]any{{
					"url": a2aURL, "protocolBinding": "JSONRPC", "protocolVersion": "1.0",
				}},
				"capabilities": map[string]any{"streaming": false},
				"defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"},
				"skills": []map[string]any{{"id": "echo", "name": "Echo", "description": "e", "tags": []string{}}},
			})
			return
		}
		if r.Method == http.MethodPost {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"id": "t1", "status": map[string]any{"state": "TASK_STATE_COMPLETED"},
					"artifacts": []map[string]any{{"artifactId": "a", "parts": []map[string]any{{"text": "pong"}}}},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer a2a.Close()
	a2aURL = a2a.URL

	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	am, _, err := auth.New(t.Context(), st, "admin123")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{DataDir: filepath.Join(dir, "server")}, st, am)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	tok := fedLogin(t, ts.URL, "admin123")
	do := func(method, path string, body any, out any) int {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequest(method, ts.URL+"/api/v1"+path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil && res.StatusCode < 300 {
			_ = json.NewDecoder(res.Body).Decode(out)
		} else if out != nil && res.StatusCode >= 300 {
			var errBody map[string]any
			_ = json.NewDecoder(res.Body).Decode(&errBody)
			t.Logf("%s %s -> %d %v", method, path, res.StatusCode, errBody)
		}
		return res.StatusCode
	}

	var probe map[string]any
	if c := do("POST", "/federation/agents/discover", map[string]any{"endpoint": a2a.URL}, &probe); c != 200 || probe["ok"] != true {
		t.Fatalf("discover %d %+v", c, probe)
	}
	if probe["protocol"] != "a2a" {
		t.Fatalf("proto %+v", probe)
	}

	var created map[string]any
	if c := do("POST", "/federation/agents", map[string]any{
		"endpoint": a2a.URL, "token": "test-token", "fetch_card": true,
	}, &created); c != 201 {
		t.Fatalf("create %d %+v", c, created)
	}
	id, _ := created["id"].(string)
	if id == "" || created["kind"] != "a2a" {
		t.Fatalf("%+v", created)
	}

	var agents []map[string]any
	if c := do("GET", "/agents?kind=a2a", nil, &agents); c != 200 || len(agents) != 1 {
		t.Fatalf("list kind=a2a %d %v", c, agents)
	}

	if c := do("POST", "/federation/agents/"+id+"/invoke", map[string]any{"message": "ping"}, nil); c != 202 {
		t.Fatalf("invoke %d", c)
	}
	deadline := time.Now().Add(3 * time.Second)
	var logs map[string]any
	for time.Now().Before(deadline) {
		do("GET", "/federation/agents/"+id+"/logs?tail=50", nil, &logs)
		if lines, _ := logs["lines"].([]any); len(lines) >= 1 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if lines, _ := logs["lines"].([]any); len(lines) < 1 {
		t.Fatalf("expected log lines, got %+v", logs)
	}

	if c := do("DELETE", "/federation/agents/"+id, nil, nil); c != 204 {
		t.Fatalf("delete %d", c)
	}
}

func fedLogin(t *testing.T, base, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	res, err := http.Post(base+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Token == "" {
		t.Fatalf("login failed status=%d", res.StatusCode)
	}
	return out.Token
}
