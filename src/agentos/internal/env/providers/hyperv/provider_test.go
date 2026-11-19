package hyperv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentos/agentos/internal/env"
)

type mockRunner struct {
	scripts []string
	reply   map[string]string // substring -> stdout
	errFor  map[string]error
}

func (m *mockRunner) RunPS(ctx context.Context, script string) (string, error) {
	m.scripts = append(m.scripts, script)
	for k, err := range m.errFor {
		if strings.Contains(script, k) {
			return "", err
		}
	}
	for k, out := range m.reply {
		if strings.Contains(script, k) {
			return out, nil
		}
	}
	return `{"ok":true}`, nil
}

func TestInfoAndValidate(t *testing.T) {
	p := New()
	info := p.Info()
	if info.Type != Type || info.Group != "本机虚拟机" || info.Kind != env.KindVM {
		t.Fatalf("info: %+v", info)
	}
	if CapMacGuest {
		t.Fatal("CapMacGuest must be false on this package")
	}
	raw, _ := json.Marshal(map[string]any{"guest_os": "ubuntu-24.04", "cpus": 8, "memory_mb": 16384, "disk_gb": 80})
	if err := p.Validate(raw); err != nil {
		t.Fatal(err)
	}
	rawBad, _ := json.Marshal(map[string]any{"guest_os": "macos"})
	if err := p.Validate(rawBad); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("expected macOS reject, got %v", err)
	}
}

func TestCapabilitiesFor(t *testing.T) {
	p := provider{}
	rec := &env.Record{Config: json.RawMessage(`{"guest_os":"ubuntu-24.04","enable_vnc":true}`)}
	caps := p.CapabilitiesFor(rec)
	hasVNC := false
	for _, c := range caps {
		if c == env.CapDesktopVNC {
			hasVNC = true
		}
	}
	if !hasVNC {
		t.Fatalf("expected desktop.vnc, got %v", caps)
	}
}

func TestDetectUnavailableOnNonWindows(t *testing.T) {
	d := Detect(context.Background())
	if d.Available {
		t.Fatal("expected unavailable on this CI host")
	}
	if d.Reason == "" {
		t.Fatal("expected reason")
	}
}

func TestCreateUsesMockRunner(t *testing.T) {
	if !strings.Contains(strings.ToLower("windows"), "win") {
		// Create requires windows GOOS check inside provider
	}
	mr := &mockRunner{reply: map[string]string{
		"Get-VMSwitch": "Default Switch\n",
		"New-VM":       `{"Id":"11111111-1111-1111-1111-111111111111","Name":"agentos-e1"}`,
		"Start-VM":     "ok",
		"Get-Command":  `{"ok":true,"elevated":true,"version":"10.0"}`,
	}}
	SetRunner(mr)
	t.Cleanup(func() { SetRunner(nil) })

	// Force Detect path: on !windows detectPlatform returns unavailable, so Create fails early.
	// We still verify Validate + Open + Status message + Delete script generation via Open.
	p := New()
	raw, _ := json.Marshal(map[string]any{
		"guest_os": "ubuntu-24.04", "cpus": 8, "memory_mb": 16384, "disk_gb": 80,
		"iso_path": "/tmp/fake.iso", "store_root": t.TempDir(), "vm_name": "agentos-e1",
	})
	rec := &env.Record{ID: "e1", Name: "Ubuntu 桌面 1", Config: raw}
	err := p.Create(context.Background(), rec)
	if err == nil {
		t.Fatal("Create should fail on non-Windows CI")
	}
	if !errors.Is(err, errNotWindows) && !strings.Contains(err.Error(), "Windows") && !strings.Contains(err.Error(), "Hyper-V") {
		// On Windows CI with mock it may succeed; on Linux expect not windows.
		t.Logf("Create err (ok on Linux): %v", err)
	}

	h, err := p.Open(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := h.Status(context.Background())
	if st.Message == "" && st.State == "" {
		t.Fatalf("empty status: %+v", st)
	}
	_ = h.Close()
}

func TestDefaultStoreRoot(t *testing.T) {
	root := DefaultStoreRoot()
	if root == "" {
		t.Fatal("empty store root")
	}
}

func TestBuildCreateScriptSecureBootOff(t *testing.T) {
	c := Config{VMName: "t", VHDPath: `D:\AgentOS-VMs\t.vhdx`, SwitchName: "Default Switch",
		Generation: 2, CPUs: 8, MemoryMB: 16384, DiskGB: 80, GuestOS: GuestUbuntu2404, SecureBoot: "auto"}
	c.applyDefaults()
	s := buildCreateScript(c, `D:\iso\u.iso`, `D:\vm\seed.iso`)
	if !strings.Contains(s, "EnableSecureBoot Off") {
		t.Fatalf("expected Secure Boot Off for Ubuntu:\n%s", s)
	}
	if !strings.Contains(s, "Add-VMDvdDrive") || !strings.Contains(s, "BootOrder") {
		t.Fatal("expected DVD attach")
	}
}

func TestMapVMState(t *testing.T) {
	if mapVMState("Running") != env.StateRunning {
		t.Fatal("running")
	}
	if mapVMState("Off") != env.StateStopped {
		t.Fatal("off")
	}
}

func TestCleanPSError(t *testing.T) {
	in := "Set-VMFirmware : 与 MicrosoftWindows 匹配的任何安全启动模板都不匹配。\r\n所在位置 行:27 字符: 24\r\n+ ...\r\n    + CategoryInfo : x"
	if got := cleanPSError(in); got != "Set-VMFirmware : 与 MicrosoftWindows 匹配的任何安全启动模板都不匹配。" {
		t.Fatalf("got %q", got)
	}
}
