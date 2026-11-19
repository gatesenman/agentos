//go:build !windows

package server_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/server"
	"golang.org/x/crypto/ssh"
)

type presetView struct {
	ID        string         `json:"id"`
	Provider  string         `json:"provider"`
	Name      string         `json:"name"`
	BaseName  string         `json:"base_name"`
	Config    map[string]any `json:"config"`
	Stack     string         `json:"stack"`
	Available bool           `json:"available"`
	Reason    string         `json:"reason"`
}

// tiny in-process sshd: public-key auth for one key; exec requests succeed with no output.
func startPresetSSHD(t *testing.T, allowed ssh.PublicKey) string {
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := ssh.NewSignerFromKey(hostPriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(allowed.Marshal()) {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	cfg.AddHostKey(hs)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "session" {
						nc.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					ch, rq, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range rq {
							r.Reply(r.Type == "exec" || r.Type == "pty-req" || r.Type == "env", nil)
							if r.Type == "exec" || r.Type == "shell" {
								st := make([]byte, 4)
								binary.BigEndian.PutUint32(st, 0)
								ch.SendRequest("exit-status", false, st)
								ch.Close()
								return
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestEnvPresetsZeroInput(t *testing.T) {
	// "本机 SSH": a fresh HOME with ~/.ssh/id_ed25519 authorised by the in-process sshd
	h := newEnvHarness(t) // builds the agentos binary before HOME changes (go caches live under HOME)
	home := t.TempDir()
	t.Setenv("HOME", home)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), pem.EncodeToMemory(blk), 0o600)
	sp, _ := ssh.NewPublicKey(pub)
	old := server.PresetSSHAddr
	server.PresetSSHAddr = startPresetSSHD(t, sp)
	defer func() { server.PresetSSHAddr = old }()

	var presets []presetView
	if code := h.do("GET", "/env-presets?refresh=1", nil, &presets); code != 200 {
		t.Fatalf("GET /env-presets %d", code)
	}
	byID := map[string]presetView{}
	for _, p := range presets {
		byID[p.ID] = p
		t.Logf("preset %-16s %-15s available=%v %s", p.ID, p.Provider, p.Available, p.Reason)
	}
	for _, id := range []string{"agentos-desktop", "agentos-remote", "docker-desktop", "docker-debian-desktop", "docker-ubuntu-server", "docker-debian", "docker-fedora", "docker-python", "docker-node", "ssh-local", "hyperv-ubuntu-2404", "hyperv-windows11", "hyperv-macos", "hyperv-fedora", "virtualbox"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing preset %s", id)
		}
	}
	for _, id := range []string{"agentos-claude", "docker-claude", "docker-browser"} {
		if _, ok := byID[id]; ok {
			t.Fatalf("tool-bundled preset %s must be removed from env-presets (use tool-presets)", id)
		}
	}
	for _, id := range []string{"hyperv-macos", "virtualbox"} {
		if byID[id].Available {
			t.Fatalf("%s should be unavailable: %+v", id, byID[id])
		}
	}
	// On Linux CI Hyper-V presets are unavailable (no Hyper-V); on Windows with Hyper-V they may be available.
	if byID["hyperv-ubuntu-2404"].Provider != "hyperv" {
		t.Fatalf("ubuntu vm preset provider: %+v", byID["hyperv-ubuntu-2404"])
	}
	if !strings.Contains(byID["hyperv-macos"].Reason, "macOS") && !strings.Contains(byID["hyperv-macos"].Reason, "Apple") {
		t.Fatalf("macos reason: %+v", byID["hyperv-macos"])
	}

	if !byID["ssh-local"].Available || byID["ssh-local"].Config["private_key_path"] != "~/.ssh/id_ed25519" {
		t.Fatalf("ssh-local: %+v", byID["ssh-local"])
	}

	create := func(id string) envView {
		t.Helper()
		var v envView
		if code := h.do("POST", "/envs", map[string]any{"preset": id}, &v); code != 201 {
			t.Fatalf("create preset %s: %d", id, code)
		}
		if !strings.HasPrefix(v.Name, byID[id].BaseName+" ") {
			t.Fatalf("preset %s: auto name %q", id, v.Name)
		}
		return v
	}
	waitRunning := func(v envView, d time.Duration) envView {
		t.Helper()
		until(t, d, v.Name+" running", func() bool {
			h.do("GET", "/envs/"+v.ID, nil, &v)
			if v.Status.State == "error" {
				t.Fatalf("%s failed: %+v", v.Name, v)
			}
			return v.Status.State == "running"
		})
		return v
	}

	// Agent OS local instance (desktop preset): two creations get "… 1" and "… 2"
	a1 := create("agentos-desktop")
	a1 = waitRunning(a1, 60*time.Second)
	if a1.Name != "Agent OS 实例 1" {
		t.Fatalf("name %q", a1.Name)
	}
	if h.do("DELETE", "/envs/"+a1.ID, nil, nil) != 204 {
		t.Fatal("delete local")
	}

	// remote Agent OS: created; join commands available with no input
	r := create("agentos-remote")
	var join map[string]any
	if code := h.do("POST", "/envs/"+r.ID+"/join", map[string]any{}, &join); code != 200 || join["token"] == nil {
		t.Fatalf("join %d %v", code, join)
	}
	h.do("DELETE", "/envs/"+r.ID, nil, nil)

	// omitted name without preset → "<provider name> N"
	var v envView
	if code := h.do("POST", "/envs", map[string]any{"provider": "agentos-remote"}, &v); code != 201 || v.Name != "远程 Agent OS 1" {
		t.Fatalf("auto name without preset: %d %q", code, v.Name)
	}
	h.do("DELETE", "/envs/"+v.ID, nil, nil)

	// ssh-local against the in-process sshd
	s := waitRunning(create("ssh-local"), 30*time.Second)
	h.do("DELETE", "/envs/"+s.ID, nil, nil)

	// docker presets (only when a docker daemon is available)
	if !byID["docker-python"].Available {
		t.Logf("docker unavailable (%s): skipping docker presets", byID["docker-python"].Reason)
		return
	}
	if os.Getenv("AGENTOS_TEST_DOCKER") == "" {
		if _, err := exec.LookPath("docker"); err != nil {
			return
		}
	}
	for _, id := range []string{"docker-desktop", "docker-python"} {
		d := waitRunning(create(id), 6*time.Minute)
		if code := h.do("DELETE", "/envs/"+d.ID, nil, nil); code != 204 {
			t.Fatalf("delete %s: %d", id, code)
		}
	}
}

// TestCreateEnvStackNotAutoInstalled ensures POST /envs with a stack field only
// records the suggestion and does not start a stack install (0.1.3+).
func TestCreateEnvStackNotAutoInstalled(t *testing.T) {
	h := newEnvHarness(t)
	var created envView
	if code := h.do("POST", "/envs", map[string]any{
		"name": "stack-suggest", "provider": "agentos-remote",
		"config": map[string]any{}, "stack": "claude-code",
	}, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if created.Stack != "claude-code" {
		t.Fatalf("stack suggestion not recorded: %q", created.Stack)
	}
	id := created.ID
	// Provisioning is async; wait for create log (remote stays "creating" until a node joins).
	var logs []server.EnvLogEntry
	until(t, 15*time.Second, "create finished logging", func() bool {
		logs = nil
		h.do("GET", "/envs/"+id+"/logs?tail=200", nil, &logs)
		joined := fmt.Sprint(logs)
		return strings.Contains(joined, "environment created") || strings.Contains(joined, "环境已创建") ||
			strings.Contains(joined, "not auto-installed") || strings.Contains(joined, "未自动安装")
	})
	joined := ""
	for _, e := range logs {
		joined += e.Text + "\n"
	}
	if strings.Contains(joined, "installing stack") || strings.Contains(joined, "开始安装技术栈") {
		t.Fatalf("create must not auto-install stack; logs:\n%s", joined)
	}
	if !strings.Contains(joined, "not auto-installed") && !strings.Contains(joined, "未自动安装") {
		t.Fatalf("expected suggestion log that stack was not auto-installed; logs:\n%s", joined)
	}
	time.Sleep(500 * time.Millisecond) // brief settle: install would have started by now if buggy
	logs = nil
	h.do("GET", "/envs/"+id+"/logs?tail=200", nil, &logs)
	joined = ""
	for _, e := range logs {
		joined += e.Text + "\n"
	}
	if strings.Contains(joined, "installing stack") || strings.Contains(joined, "开始安装技术栈") || strings.Contains(joined, "[stack:") {
		t.Fatalf("late auto-install detected; logs:\n%s", joined)
	}
	// tool-presets catalog exists
	var tools []map[string]any
	if code := h.do("GET", "/tool-presets", nil, &tools); code != 200 || len(tools) < 5 {
		t.Fatalf("GET /tool-presets: %d len=%d", code, len(tools))
	}
	h.do("DELETE", "/envs/"+id, nil, nil)
}
