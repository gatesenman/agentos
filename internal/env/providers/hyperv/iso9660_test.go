package hyperv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"
	"unicode/utf16"
)

// readJolietRoot is a tiny reader used to verify WriteSeedISO output.
func readJolietRoot(t *testing.T, img []byte) (label string, files map[string][]byte) {
	t.Helper()
	sec := func(n int) []byte { return img[n*isoSector : (n+1)*isoSector] }
	pvd, svd := sec(16), sec(17)
	if string(pvd[1:6]) != "CD001" || pvd[0] != 1 || svd[0] != 2 || string(svd[88:91]) != "%/E" || sec(18)[0] != 255 {
		t.Fatalf("bad volume descriptors")
	}
	if got := binary.LittleEndian.Uint32(pvd[80:]); int(got)*isoSector != len(img) {
		t.Fatalf("volume size %d sectors, image %d bytes", got, len(img))
	}
	dec := func(b []byte) string {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return string(utf16.Decode(u))
	}
	label = string(bytes.TrimRight(svd[40:72], "\x00"))
	label = dec(svd[40:72])
	root := svd[156:]
	lba := int(binary.LittleEndian.Uint32(root[2:]))
	size := int(binary.LittleEndian.Uint32(root[10:]))
	files = map[string][]byte{}
	dir := img[lba*isoSector : lba*isoSector+size]
	for off := 0; off < len(dir); {
		l := int(dir[off])
		if l == 0 {
			off = (off/isoSector + 1) * isoSector
			continue
		}
		r := dir[off : off+l]
		nl := int(r[32])
		name := r[33 : 33+nl]
		if !(nl == 1 && (name[0] == 0 || name[0] == 1)) {
			ext := int(binary.LittleEndian.Uint32(r[2:]))
			sz := int(binary.LittleEndian.Uint32(r[10:]))
			n := dec(name)
			if len(n) > 2 && n[len(n)-2:] == ";1" {
				n = n[:len(n)-2]
			}
			files[n] = img[ext*isoSector : ext*isoSector+sz]
		}
		off += l
	}
	return trimSpace(label), files
}

func trimSpace(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

func TestWriteSeedISO(t *testing.T) {
	big := make([]byte, 5000)
	for i := range big {
		big[i] = byte(i)
	}
	tmp := t.TempDir() + "/big.bin"
	os.WriteFile(tmp, bytes.Repeat([]byte("x"), 70000), 0o644)
	files := []SeedFile{
		{Name: "user-data", Data: []byte("#cloud-config\n")},
		{Name: "meta-data", Data: []byte("instance-id: x\n")},
		{Name: "autounattend.xml", Data: big},
		{Name: "empty.txt"},
		{Name: "OpenSSH-Win64.msi", Path: tmp},
	}
	// many files to force a multi-sector directory
	for i := 0; i < 60; i++ {
		files = append(files, SeedFile{Name: fmt.Sprintf("padding-file-with-a-long-name-%02d.txt", i), Data: []byte{byte(i)}})
	}
	var buf bytes.Buffer
	if err := WriteSeedISO(&buf, "CIDATA", files, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("AGENTOS_ISO_OUT"); out != "" {
		os.WriteFile(out, buf.Bytes(), 0o644)
	}
	label, got := readJolietRoot(t, buf.Bytes())
	if label != "CIDATA" {
		t.Fatalf("label %q", label)
	}
	if string(got["user-data"]) != "#cloud-config\n" || string(got["meta-data"]) != "instance-id: x\n" {
		t.Fatalf("content mismatch: %q", got["user-data"])
	}
	if !bytes.Equal(got["autounattend.xml"], big) || len(got["OpenSSH-Win64.msi"]) != 70000 || len(got["empty.txt"]) != 0 {
		t.Fatalf("file mismatch")
	}
	if len(got) != len(files) {
		t.Fatalf("got %d files want %d", len(got), len(files))
	}
}

func TestPrimaryName(t *testing.T) {
	for in, want := range map[string]string{"user-data": "USER_DATA.;1", "autounattend.xml": "AUTOUNATTEND.XML;1", "ks.cfg": "KS.CFG;1"} {
		if got := primaryName(in); got != want {
			t.Errorf("%s: %s want %s", in, got, want)
		}
	}
}

func TestISOReadBack(t *testing.T) {
	p := t.TempDir() + "/x.iso"
	if err := WriteSeedISOFile(p, "CIDATA", []SeedFile{{Name: "grub.cfg", Data: []byte("set timeout=30\nmenuentry \"Try or Install Ubuntu\" {\n\tset gfxpayload=keep\n\tlinux\t/casper/vmlinuz  --- quiet splash\n\tinitrd\t/casper/initrd\n}\n")}}); err != nil {
		t.Fatal(err)
	}
	b, err := isoReadFile(p, "GRUB.CFG")
	if err != nil {
		t.Fatal(err)
	}
	k, i := grubBootFiles(b)
	if k != "/casper/vmlinuz" || i != "/casper/initrd" {
		t.Fatalf("got %q %q", k, i)
	}
}
