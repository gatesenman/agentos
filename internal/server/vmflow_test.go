//go:build !windows

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	envpkg "github.com/agentos/agentos/internal/env"
)

// fakeVM mimics the Hyper-V provider's 0.1.5 extensions: Prepare, config
// saving during Create, screenshots, native viewers, cleanup of failed VMs.
type fakeVM struct {
	mu      sync.Mutex
	deleted int
	opened  []string
}

const fakeVMType = "fake-vm"

func (p *fakeVM) Info() envpkg.ProviderInfo {
	return envpkg.ProviderInfo{Type: fakeVMType, Name: "fake vm", Kind: envpkg.KindVM, Group: "测试",
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"fail":{"type":"boolean"},"password":{"type":"string","x-secret":true}}}`)}
}
func (p *fakeVM) Validate(json.RawMessage) error                { return nil }
func (p *fakeVM) Test(context.Context, json.RawMessage) error   { return nil }
func (p *fakeVM) ProvisionTimeout(*envpkg.Record) time.Duration { return time.Hour }
func (p *fakeVM) ResumeCreate(*envpkg.Record) bool              { return true }
func (p *fakeVM) Prepare(rec *envpkg.Record) error {
	var m map[string]any
	json.Unmarshal(rec.Config, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["password"] = "generated-pw"
	rec.Config, _ = json.Marshal(m)
	return nil
}
func (p *fakeVM) Create(ctx context.Context, rec *envpkg.Record) error {
	var m map[string]any
	json.Unmarshal(rec.Config, &m)
	m["vm_name"] = "vm-" + rec.ID[:6]
	b, _ := json.Marshal(m)
	envpkg.SaveConfig(ctx, b) // persisted before the slow part
	if m["fail"] == true {
		return io.ErrUnexpectedEOF
	}
	rec.Config = b
	return nil
}
func (p *fakeVM) Open(ctx context.Context, rec *envpkg.Record) (envpkg.Env, error) {
	return &fakeVMEnv{p: p}, nil
}

type fakeVMEnv struct {
	fakeEnv
	p *fakeVM
}

func (e *fakeVMEnv) Delete(context.Context) error {
	e.p.mu.Lock()
	e.p.deleted++
	e.p.mu.Unlock()
	return nil
}
func (e *fakeVMEnv) Screenshot(context.Context, int) ([]byte, error) {
	return []byte("\x89PNG\r\n\x1a\nfake"), nil
}
func (e *fakeVMEnv) OpenNative(_ context.Context, kind string) error {
	e.p.mu.Lock()
	e.p.opened = append(e.p.opened, kind)
	e.p.mu.Unlock()
	return nil
}

var fakevm = &fakeVM{}

func init() { envpkg.Register(fakevm) }

func TestVMProvisionFlow(t *testing.T) {
	h := newEnvHarness(t)
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"name": "ok", "provider": fakeVMType}, &v); code != 201 {
		t.Fatalf("create %d", code)
	}
	until(t, 5*time.Second, "running", func() bool {
		return h.do("GET", "/envs/"+v.ID, nil, &v) == 200 && v.Status.State == "running"
	})
	if v.Config["vm_name"] == nil || len(v.SecretsSet) != 1 || v.SecretsSet[0] != "password" {
		t.Fatalf("prepared/saved config missing: %+v", v)
	}
	resp := h.req("GET", "/envs/"+v.ID+"/screenshot?w=640", nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Fatalf("screenshot %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if code := h.do("POST", "/envs/"+v.ID+"/open", map[string]string{"kind": "console"}, nil); code != 204 {
		t.Fatalf("open %d", code)
	}
	fakevm.mu.Lock()
	if len(fakevm.opened) == 0 || fakevm.opened[len(fakevm.opened)-1] != "console" {
		t.Fatalf("open kinds %v", fakevm.opened)
	}
	fakevm.mu.Unlock()

	// failed provisioning keeps the saved config and deleting cleans up the VM
	var f envView
	if code := h.do("POST", "/envs", map[string]any{"name": "bad", "provider": fakeVMType, "config": map[string]any{"fail": true}}, &f); code != 201 {
		t.Fatalf("create %d", code)
	}
	until(t, 5*time.Second, "error", func() bool {
		return h.do("GET", "/envs/"+f.ID, nil, &f) == 200 && f.Status.State == "error"
	})
	if f.Config["vm_name"] == nil {
		t.Fatalf("config saved during Create was lost: %+v", f.Config)
	}
	fakevm.mu.Lock()
	before := fakevm.deleted
	fakevm.mu.Unlock()
	if code := h.do("DELETE", "/envs/"+f.ID, nil, nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	fakevm.mu.Lock()
	defer fakevm.mu.Unlock()
	if fakevm.deleted != before+1 {
		t.Fatal("provider Delete not called for a failed environment")
	}
}
