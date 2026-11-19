package hyperv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestBuildNewcCPIORoundTrip(t *testing.T) {
	raw, err := buildNewcCPIO([]cpioMember{
		{Name: "agentos-seed", Mode: 0o40755},
		{Name: "agentos-seed/user-data", Mode: 0o100644, Data: []byte("#cloud-config\nautoinstall:\n  version: 1\n")},
		{Name: "scripts/casper-bottom/ORDER", Mode: 0o100644, Data: []byte("hello\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ud, ok := cpioExtractFile(raw, "agentos-seed/user-data")
	if !ok || !bytes.Contains(ud, []byte("autoinstall")) {
		t.Fatalf("user-data missing: ok=%v %q", ok, ud)
	}
	order, ok := cpioExtractFile(raw, "scripts/casper-bottom/ORDER")
	if !ok || string(order) != "hello\n" {
		t.Fatalf("order=%q ok=%v", order, ok)
	}
}

func TestPatchUbuntuInitrdSeedSynthetic(t *testing.T) {
	// early empty cpio + zstd(main with ORDER + 99casperboot anchor)
	order := []byte(`/scripts/casper-bottom/05mountpoints "$@"
[ -e /conf/param.conf ] && . /conf/param.conf
/scripts/casper-bottom/99casperboot "$@"
[ -e /conf/param.conf ] && . /conf/param.conf
`)
	main, err := buildNewcCPIO([]cpioMember{
		{Name: "scripts/casper-bottom", Mode: 0o40755},
		{Name: "scripts/casper-bottom/ORDER", Mode: 0o100644, Data: order},
		{Name: "scripts/casper-bottom/99casperboot", Mode: 0o100755, Data: []byte("#!/bin/sh\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	early, err := buildNewcCPIO([]cpioMember{{Name: "kernel", Mode: 0o40755}})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	comp := enc.EncodeAll(main, nil)
	_ = enc.Close()
	initrd := append(append([]byte{}, early...), comp...)

	dir := t.TempDir()
	path := filepath.Join(dir, "initrd")
	if err := os.WriteFile(path, initrd, 0o644); err != nil {
		t.Fatal(err)
	}
	ud := []byte("#cloud-config\nautoinstall:\n  version: 1\n")
	md := []byte("instance-id: test\n")
	if err := patchUbuntuInitrdSeed(path, ud, md); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zoff, err := findInitrdZstdFrame(out)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	merged, err := dec.DecodeAll(out[zoff:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(merged, []byte("99-agentos-nocloud")) {
		t.Fatal("hook not in patched initrd")
	}
	if !bytes.Contains(merged, []byte("AgentOS-initrd-seed")) {
		t.Fatal("marker missing")
	}
	// last ORDER must mention hook
	off := 0
	var lastOrder []byte
	for off+110 <= len(merged) {
		if string(merged[off:off+6]) != "070701" {
			break
		}
		h := merged[off : off+110]
		namesize, _ := parseHex8(h[94:102])
		filesize, _ := parseHex8(h[54:62])
		name := string(merged[off+110 : off+110+namesize-1])
		nameEnd := off + 110 + namesize
		dataOff := (nameEnd + 3) &^ 3
		blob := merged[dataOff : dataOff+filesize]
		n := name
		if len(n) >= 2 && n[:2] == "./" {
			n = n[2:]
		}
		if n == "scripts/casper-bottom/ORDER" {
			lastOrder = append([]byte{}, blob...)
		}
		off = (dataOff + filesize + 3) &^ 3
		if n == "TRAILER!!!" {
			for off < len(merged) && merged[off] == 0 {
				off++
			}
		}
	}
	if !bytes.Contains(lastOrder, []byte("99-agentos-nocloud")) {
		t.Fatalf("patched ORDER missing hook:\n%s", lastOrder)
	}
	if n := bytes.Count(merged, []byte("TRAILER!!!")); n < 2 {
		t.Fatalf("expected main+overlay cpio TRAILERs (>=2), got %d", n)
	}
	ud, ok := cpioExtractFile(merged, "agentos-seed/user-data")
	if !ok || !bytes.Contains(ud, []byte("autoinstall")) {
		t.Fatalf("overlay user-data missing: ok=%v", ok)
	}
}
