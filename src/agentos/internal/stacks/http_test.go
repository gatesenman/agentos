package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
)

func TestHTTP(t *testing.T) {
	envs := map[string]*fakeEnv{"e1": newFake("linux", "python"), "e2": newFake("windows")}
	var mu sync.Mutex
	logs := map[string][]string{}
	done := make(chan string, 4)
	block := make(chan struct{})
	h := NewHandler(Deps{
		Open: func(ctx context.Context, id string) (env.Env, func(), error) {
			if id == "slow" {
				f := newFake("linux")
				return &gateEnv{fakeEnv: f, gate: block}, nil, nil
			}
			f, ok := envs[id]
			if !ok {
				return nil, nil, fmt.Errorf("open %s: %w", id, env.ErrNotFound)
			}
			return f, func() {}, nil
		},
		Log: func(id, level, text string) { mu.Lock(); logs[id] = append(logs[id], level+" "+text); mu.Unlock() },
		Done: func(id, stack string, err error) {
			done <- fmt.Sprintf("%s/%s/%v", id, stack, err)
		},
	})
	mux := http.NewServeMux()
	authCalls := 0
	h.Register(mux, func(f http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { authCalls++; f(w, r) })
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	do := func(method, path string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := res.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return res, []byte(b.String())
	}

	res, body := do("GET", "/api/v1/stacks")
	var views []View
	if res.StatusCode != 200 || json.Unmarshal(body, &views) != nil || len(views) != len(All()) {
		t.Fatalf("GET /stacks: %d %s", res.StatusCode, body[:min(200, len(body))])
	}
	if authCalls == 0 {
		t.Error("wrap not applied")
	}

	res, body = do("GET", "/api/v1/envs/e1/stacks")
	var es EnvStacks
	if res.StatusCode != 200 || json.Unmarshal(body, &es) != nil || es.OS.Family != FamilyLinux || len(es.Installed) != 1 || es.Installed[0] != "python" {
		t.Fatalf("GET env stacks: %d %s", res.StatusCode, body)
	}

	res, body = do("POST", "/api/v1/envs/e2/stacks/openai-codex-cli/install")
	if res.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"status":"installing"`) {
		t.Fatalf("install: %d %s", res.StatusCode, body)
	}
	select {
	case d := <-done:
		if d != "e2/openai-codex-cli/<nil>" {
			t.Fatalf("done: %s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("install did not finish")
	}
	mu.Lock()
	joined := strings.Join(logs["e2"], "\n")
	mu.Unlock()
	if !strings.Contains(joined, "[stack:openai-codex-cli]") || !strings.Contains(joined, "安装完成") {
		t.Errorf("logs: %s", joined)
	}

	if res, _ := do("POST", "/api/v1/envs/nope/stacks/python/install"); res.StatusCode != 404 {
		t.Errorf("unknown env: %d", res.StatusCode)
	}
	if res, _ := do("GET", "/api/v1/envs/nope/stacks"); res.StatusCode != 404 {
		t.Errorf("unknown env detect: %d", res.StatusCode)
	}
	if res, _ := do("POST", "/api/v1/envs/e1/stacks/cobol/install"); res.StatusCode != 404 {
		t.Errorf("unknown stack: %d", res.StatusCode)
	}

	// busy: second install on the same env while the first is running -> 409
	if res, _ := do("POST", "/api/v1/envs/slow/stacks/go/install"); res.StatusCode != 202 {
		t.Fatalf("slow install: %d", res.StatusCode)
	}
	if res, body := do("POST", "/api/v1/envs/slow/stacks/node/install"); res.StatusCode != 409 {
		t.Errorf("busy: %d %s", res.StatusCode, body)
	}
	if s, ok := h.Busy("slow"); !ok || s != "go" {
		t.Errorf("Busy: %s %v", s, ok)
	}
	close(block)
	<-done
	if _, ok := h.Busy("slow"); ok {
		t.Error("still busy after completion")
	}
}

// gateEnv blocks every Exec until gate is closed.
type gateEnv struct {
	*fakeEnv
	gate chan struct{}
}

func (g *gateEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	<-g.gate
	return g.fakeEnv.Exec(ctx, req)
}
