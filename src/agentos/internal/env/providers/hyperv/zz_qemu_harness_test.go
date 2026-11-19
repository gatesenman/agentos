package hyperv

// Temporary E2E harness (not committed): AGENTOS_QEMU_OUT=dir AGENTOS_QEMU_OS=ubuntu-24.04 go test -run TestQEMUSeed
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQEMUSeed(t *testing.T) {
	out := os.Getenv("AGENTOS_QEMU_OUT")
	if out == "" {
		t.Skip()
	}
	gos := os.Getenv("AGENTOS_QEMU_OS")
	raw := json.RawMessage(`{"guest_os":"` + gos + `","language":"zh-CN","enable_vnc":true,"store_root":"` + out + `/store","install_proxy":"` + os.Getenv("AGENTOS_QEMU_PROXY") + `"}`)
	full, err := prepareConfig(raw, "qemu")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseConfig(full)
	if err != nil {
		t.Fatal(err)
	}
	if iso := os.Getenv("AGENTOS_QEMU_ISO"); iso != "" && gos == GuestUbuntu2404 {
		if cfg, err := isoReadFile(iso, "boot/grub/grub.cfg"); err == nil {
			k, i := grubBootFiles(cfg)
			t.Logf("grub kernel=%q initrd=%q", k, i)
			if k != "" {
				ubuntuKernel, ubuntuInitrd = k, i
			}
		} else {
			t.Logf("grub read: %v", err)
		}
	}
	os.MkdirAll(out, 0o755)
	if err := buildSeed(context.Background(), c, "qemu", filepath.Join(out, "seed.iso")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(out, "config.json"), full, 0o600)
	os.WriteFile(filepath.Join(out, "id_ed25519"), []byte(c.SSHPrivateKey), 0o600)
	os.WriteFile(filepath.Join(out, "grub.txt"), []byte(strings.Join(UbuntuGrubCommands(ubuntuKernel, ubuntuInitrd), "\n")+"\n"), 0o644)
	t.Logf("user=%s pass=%s host=%s", c.GuestUser, c.GuestPassword, c.Hostname)
	os.WriteFile(filepath.Join(out, "creds.txt"), []byte(c.GuestUser+"\n"+c.GuestPassword+"\n"+c.Hostname+"\n"), 0o600)
}
