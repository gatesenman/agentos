package agentfed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchAgentCardV1(t *testing.T) {
	card := map[string]any{
		"name":        "Echo",
		"description": "echo agent",
		"version":     "1.0.0",
		"supportedInterfaces": []map[string]any{{
			"url": "http://example/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0",
		}},
		"capabilities":       map[string]any{"streaming": true},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             []map[string]any{{"id": "echo", "name": "Echo", "description": "echo", "tags": []string{"demo"}}},
	}
	body, _ := json.Marshal(card)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/agent-card.json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	got, err := FetchAgentCard(context.Background(), srv.URL, Auth{}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Echo" || !got.Capabilities.Streaming {
		t.Fatalf("%+v", got)
	}
	rpc, binding, ver := got.ResolveInterface()
	if rpc != "http://example/a2a" || binding != "JSONRPC" || ver != "1.0" {
		t.Fatalf("iface %s %s %s", rpc, binding, ver)
	}
}

func TestA2ASendMessageJSONRPC(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "T", "description": "t", "version": "1",
			"supportedInterfaces": []map[string]any{{
				"url": r.Host, // filled below
			}},
			"capabilities": map[string]any{}, "defaultInputModes": []string{"text/plain"},
			"defaultOutputModes": []string{"text/plain"}, "skills": []any{},
		})
	})
	var srvURL string
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req jsonRPCReq
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "SendMessage" && req.Method != "message/send" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(jsonRPCResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(`{"id":"task-1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"a1","parts":[{"text":"pong"}]}]}`),
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	_ = srvURL

	cli := &A2AClient{
		HTTP: srv.Client(), BaseURL: srv.URL, RPCURL: srv.URL,
		Binding: "JSONRPC", Version: "1.0",
		Card: AgentCard{Name: "T", SupportedInterfaces: []AgentInterface{{URL: srv.URL, ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0"}}},
	}
	id, st, raw, err := cli.SendMessage(context.Background(), "ping")
	if err != nil {
		t.Fatal(err)
	}
	if id != "task-1" || st != "completed" {
		t.Fatalf("id=%s st=%s raw=%s", id, st, raw)
	}
}

func TestTokenSealer(t *testing.T) {
	s, err := LoadSealer("")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("secret-token", "agent1")
	if err != nil || sealed == "" || strings.Contains(sealed, "secret") {
		t.Fatal(sealed, err)
	}
	tok, err := s.Open(sealed, "agent1")
	if err != nil || tok != "secret-token" {
		t.Fatal(tok, err)
	}
	if _, err := s.Open(sealed, "other"); err == nil {
		t.Fatal("expected aad mismatch")
	}
}

func TestDetectProtocolA2A(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"name": "X", "description": "d", "version": "1",
		"supportedInterfaces": []map[string]any{{"url": "http://x/rpc", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}},
		"capabilities": map[string]any{}, "defaultInputModes": []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"}, "skills": []any{},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "agent-card") {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p, err := DetectProtocol(context.Background(), srv.URL, Auth{}, srv.Client())
	if err != nil || !p.OK || p.Protocol != ProtoA2A || p.Name != "X" {
		t.Fatalf("%+v %v", p, err)
	}
}
