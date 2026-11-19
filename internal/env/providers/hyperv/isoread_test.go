package hyperv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestISOExtractFile(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "t.iso")
	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	initrd := append([]byte("INITRD"), payload...)
	if err := WriteSeedISOFile(iso, "CIDATA", []SeedFile{
		{Name: "vmlinuz", Data: payload},
		{Name: "initrd", Data: initrd},
	}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out", "vmlinuz")
	if err := isoExtractFile(iso, "vmlinuz", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("size %d want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("byte %d", i)
		}
	}
	dest2 := filepath.Join(dir, "out", "initrd")
	if err := isoExtractFile(iso, "initrd", dest2); err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(dest2)
	if err != nil || len(got2) != len(initrd) {
		t.Fatalf("initrd extract: %v len=%d", err, len(got2))
	}
}

func TestUbuntuAutoinstallAppend(t *testing.T) {
	if !strings.Contains(UbuntuAutoinstallAppend, "autoinstall") || !strings.Contains(UbuntuAutoinstallAppend, "ds=nocloud") {
		t.Fatalf("bad append %q", UbuntuAutoinstallAppend)
	}
	if strings.Contains(UbuntuAutoinstallAppend, ";") {
		t.Fatalf("append must not contain semicolon: %q", UbuntuAutoinstallAppend)
	}
	cmds := UbuntuGrubCommands("/casper/vmlinuz", "/casper/initrd")
	if len(cmds) != 3 || cmds[2] != "boot" {
		t.Fatalf("%v", cmds)
	}
	if !strings.Contains(cmds[0], UbuntuAutoinstallAppend) {
		t.Fatalf("grub linux missing append: %q", cmds[0])
	}
}

func TestISOReadSeedUserData(t *testing.T) {
	dir := t.TempDir()
	iso := filepath.Join(dir, "seed.iso")
	ud := []byte("#cloud-config\nautoinstall:\n  version: 1\n")
	md := []byte("instance-id: agentos-test\n")
	if err := WriteSeedISOFile(iso, "CIDATA", []SeedFile{
		{Name: "user-data", Data: ud},
		{Name: "meta-data", Data: md},
	}); err != nil {
		t.Fatal(err)
	}
	gotUD, gotMD, err := readSeedISOFiles(iso)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotUD) != string(ud) || string(gotMD) != string(md) {
		t.Fatalf("mismatch ud=%q md=%q", gotUD, gotMD)
	}
	// Also via isoReadFile with hyphenated Joliet name
	b, err := isoReadFile(iso, "user-data")
	if err != nil || string(b) != string(ud) {
		t.Fatalf("isoReadFile user-data: %v %q", err, b)
	}
}
