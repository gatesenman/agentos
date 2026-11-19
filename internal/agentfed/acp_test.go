package agentfed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestACPDiscoverAndRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agents": []map[string]any{{
				"name": "echo", "description": "echo agent",
				"input_content_types": []string{"text/plain"}, "output_content_types": []string{"text/plain"},
			}},
		})
	})
	mux.HandleFunc("/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ACPRun{
			AgentName: "echo", RunID: "run-1", Status: "completed",
			Output: json.RawMessage(`[{"role":"agent","parts":[{"content_type":"text/plain","content":"hello"}]}]`),
			CreatedAt: "2026-01-01T00:00:00Z",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	agents, err := DiscoverACP(context.Background(), srv.URL, Auth{}, srv.Client())
	if err != nil || len(agents) != 1 || agents[0].Name != "echo" {
		t.Fatalf("%v %v", agents, err)
	}
	cli := &ACPClient{HTTP: srv.Client(), BaseURL: srv.URL}
	var lines []string
	run, err := cli.CreateRun(context.Background(), "echo", "hi", false, func(s string) { lines = append(lines, s) })
	if err != nil || run.RunID != "run-1" || NormalizeACPState(run.Status) != "completed" {
		t.Fatalf("%+v %v", run, err)
	}
	if len(lines) == 0 {
		t.Fatal("expected log lines")
	}
}

func TestDetectProtocolACP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents" {
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []map[string]any{{
				"name": "a", "description": "d", "input_content_types": []string{"*/*"}, "output_content_types": []string{"*/*"},
			}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p, err := DetectProtocol(context.Background(), srv.URL, Auth{}, srv.Client())
	if err != nil || !p.OK || p.Protocol != ProtoACP {
		t.Fatalf("%+v %v", p, err)
	}
}
