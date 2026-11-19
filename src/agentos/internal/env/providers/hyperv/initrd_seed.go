package hyperv

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"time"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

// patchUbuntuInitrdSeed injects a casper-bottom hook + NoCloud user-data/meta-data
// into an Ubuntu Desktop casper initrd. Ubuntu 24.04 desktop initrds are:
//
//	[early microcode cpio…][firmware cpio][zstd(main cpio…)]
//
// The live squashfs ships empty /var/lib/cloud/seed/nocloud/* which shadows the
// CIDATA seed CD. We overwrite those via casper-bottom (listed in ORDER).
//
// The kernel unpacks consecutive newc archives inside the decompressed zstd
// payload (later same-path entries win). We append our overlay *inside* that
// zstd stream. Flattening the main archive is unsafe: busybox applets are
// hardlinks (filesize=0) and must keep shared inodes.
func patchUbuntuInitrdSeed(initrdPath string, userData, metaData []byte) error {
	raw, err := os.ReadFile(initrdPath)
	if err != nil {
		return fmt.Errorf("read initrd: %w", err)
	}
	zoff, err := findInitrdZstdFrame(raw)
	if err != nil {
		return err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return err
	}
	defer dec.Close()
	main, err := dec.DecodeAll(raw[zoff:], nil)
	if err != nil {
		return fmt.Errorf("zstd decompress initrd main: %w", err)
	}
	if len(main) < 6 || string(main[:6]) != "070701" {
		return fmt.Errorf("decompressed initrd main is not a newc cpio")
	}
	order, ok := cpioExtractFile(main, "scripts/casper-bottom/ORDER")
	if !ok {
		return fmt.Errorf("casper-bottom ORDER missing from initrd")
	}
	const hookName = "99-agentos-nocloud"
	orderTxt := string(order)
	hookLine := "/scripts/casper-bottom/" + hookName + " \"$@\"\n[ -e /conf/param.conf ] && . /conf/param.conf\n"
	if !bytes.Contains(order, []byte(hookName)) {
		needle := []byte(`/scripts/casper-bottom/99casperboot`)
		if !bytes.Contains(order, needle) {
			return fmt.Errorf("casper-bottom ORDER missing 99casperboot anchor")
		}
		orderTxt = string(bytes.Replace(order, needle, append([]byte(hookLine), needle...), 1))
	}
	hook := []byte(`#!/bin/sh
PREREQ=""
prereqs() { echo "$PREREQ"; }
case $1 in prereqs) prereqs; exit 0;; esac
. /scripts/functions
mkdir -p /root/var/lib/cloud/seed/nocloud /root/etc/cloud/cloud.cfg.d /root/run /run
if [ -f /agentos-seed/user-data ]; then
  cp -f /agentos-seed/user-data /root/var/lib/cloud/seed/nocloud/user-data
  cp -f /agentos-seed/meta-data /root/var/lib/cloud/seed/nocloud/meta-data
  chmod 644 /root/var/lib/cloud/seed/nocloud/user-data /root/var/lib/cloud/seed/nocloud/meta-data
  # Keep a copy on the live root too (survives pivot) in case cmdline uses s=.
  mkdir -p /root/agentos-seed
  cp -f /agentos-seed/user-data /root/agentos-seed/user-data
  cp -f /agentos-seed/meta-data /root/agentos-seed/meta-data
fi
# Force NoCloud so empty squashfs seeds / other datasources cannot win.
cat > /root/etc/cloud/cloud.cfg.d/99-agentos-nocloud.cfg <<'EOF'
datasource_list: [NoCloud, None]
EOF
if grep -q autoinstall /root/var/lib/cloud/seed/nocloud/user-data 2>/dev/null; then
  echo AgentOS-initrd-seed-OK > /root/run/agentos-initrd-seed
  echo AgentOS-initrd-seed-OK > /dev/kmsg 2>/dev/null || true
  echo AgentOS-initrd-seed-OK > /root/etc/agentos-initrd-seed
else
  echo AgentOS-initrd-seed-FAIL > /root/run/agentos-initrd-seed
  echo AgentOS-initrd-seed-FAIL > /dev/kmsg 2>/dev/null || true
  echo AgentOS-initrd-seed-FAIL > /root/etc/agentos-initrd-seed
fi
log_success_msg "AgentOS: injected NoCloud autoinstall seed"
`)
	cloudCfg := []byte("datasource_list: [NoCloud, None]\n")
	// Append a trailing newc archive *inside* the zstd payload (NOT after the
	// zstd frame — the kernel ignores bytes past the compressed stream).
	// Do NOT flatten the main archive: Ubuntu's initramfs relies on busybox
	// hardlinks (filesize=0 stubs sharing an inode); flattening them into
	// independent empty files makes /bin/sh unexecutable (ENOEXEC on /init).
	overlay, err := buildNewcCPIO([]cpioMember{
		{Name: "agentos-seed", Mode: 0o40755},
		{Name: "agentos-seed/user-data", Mode: 0o100644, Data: userData},
		{Name: "agentos-seed/meta-data", Mode: 0o100644, Data: metaData},
		{Name: "scripts/casper-bottom/" + hookName, Mode: 0o100755, Data: hook},
		{Name: "scripts/casper-bottom/ORDER", Mode: 0o100644, Data: []byte(orderTxt)},
		{Name: "agentos-seed/99-agentos-nocloud.cfg", Mode: 0o100644, Data: cloudCfg},
	})
	if err != nil {
		return err
	}
	merged := append(main, overlay...)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return err
	}
	comp := enc.EncodeAll(merged, make([]byte, 0, len(merged)/2))
	_ = enc.Close()
	if len(comp) < 4 || !bytes.Equal(comp[:4], zstdMagic) {
		return fmt.Errorf("zstd recompress produced unexpected header")
	}
	out := append(append([]byte{}, raw[:zoff]...), comp...)
	orig := initrdPath + ".orig"
	if _, err := os.Stat(orig); err != nil {
		_ = os.WriteFile(orig, raw, 0o644)
	}
	if err := os.WriteFile(initrdPath, out, 0o644); err != nil {
		return fmt.Errorf("write patched initrd: %w", err)
	}
	return nil
}

func findInitrdZstdFrame(buf []byte) (int, error) {
	off := 0
	for off < len(buf) {
		for off < len(buf) && buf[off] == 0 {
			off++
		}
		if off >= len(buf) {
			break
		}
		if off+6 <= len(buf) && string(buf[off:off+6]) == "070701" {
			end, err := skipNewcCPIO(buf, off)
			if err != nil {
				return 0, err
			}
			off = end
			continue
		}
		if off+4 <= len(buf) && bytes.Equal(buf[off:off+4], zstdMagic) {
			return off, nil
		}
		return 0, fmt.Errorf("initrd: unexpected bytes at %d (%x)", off, buf[off:min(off+8, len(buf))])
	}
	return 0, fmt.Errorf("initrd: no zstd frame after early cpio archives")
}

func skipNewcCPIO(buf []byte, start int) (int, error) {
	off := start
	for off+110 <= len(buf) {
		if string(buf[off:off+6]) != "070701" {
			return 0, fmt.Errorf("cpio: bad magic at %d", off)
		}
		h := buf[off : off+110]
		namesize, err1 := parseHex8(h[94:102])
		filesize, err2 := parseHex8(h[54:62])
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("cpio: bad header at %d", off)
		}
		nameOff := off + 110
		if nameOff+namesize > len(buf) {
			return 0, fmt.Errorf("cpio: truncated name at %d", off)
		}
		name := buf[nameOff : nameOff+namesize-1]
		nameEnd := nameOff + namesize
		dataOff := (nameEnd + 3) &^ 3
		dataEnd := dataOff + filesize
		if dataEnd > len(buf) {
			return 0, fmt.Errorf("cpio: truncated data at %d", off)
		}
		off = (dataEnd + 3) &^ 3
		if string(name) == "TRAILER!!!" {
			return off, nil
		}
	}
	return 0, fmt.Errorf("cpio: missing TRAILER")
}

func cpioExtractFile(buf []byte, want string) ([]byte, bool) {
	off := 0
	var found []byte
	ok := false
	for off+110 <= len(buf) {
		if string(buf[off:off+6]) != "070701" {
			if off < len(buf) && buf[off] == 0 {
				off++
				continue
			}
			return found, ok
		}
		h := buf[off : off+110]
		namesize, _ := parseHex8(h[94:102])
		filesize, _ := parseHex8(h[54:62])
		nameOff := off + 110
		name := string(buf[nameOff : nameOff+namesize-1])
		nameEnd := nameOff + namesize
		dataOff := (nameEnd + 3) &^ 3
		blob := buf[dataOff : dataOff+filesize]
		off = (dataOff + filesize + 3) &^ 3
		n := name
		if len(n) >= 2 && n[:2] == "./" {
			n = n[2:]
		}
		if n == want || name == want {
			found = append([]byte{}, blob...)
			ok = true // keep scanning so a later overlay wins
		}
		if n == "TRAILER!!!" || name == "TRAILER!!!" {
			for off < len(buf) && buf[off] == 0 {
				off++
			}
			continue
		}
	}
	return found, ok
}

type cpioMember struct {
	Name string
	Mode int
	Data []byte
}

func buildNewcCPIO(files []cpioMember) ([]byte, error) {
	var b bytes.Buffer
	mtime := uint32(time.Now().Unix())
	write := func(name string, mode int, data []byte) error {
		name = path.Clean("/" + name)
		if name == "/" {
			name = "."
		} else {
			name = name[1:]
		}
		n := []byte(name + "\x00")
		hdr := make([]byte, 0, 110)
		hex := func(v uint32) []byte { return []byte(fmt.Sprintf("%08x", v)) }
		hdr = append(hdr, []byte("070701")...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(uint32(mode))...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(1)...)
		hdr = append(hdr, hex(mtime)...)
		hdr = append(hdr, hex(uint32(len(data)))...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(0)...)
		hdr = append(hdr, hex(uint32(len(n)))...)
		hdr = append(hdr, hex(0)...)
		if len(hdr) != 110 {
			return fmt.Errorf("cpio header len %d", len(hdr))
		}
		if _, err := b.Write(hdr); err != nil {
			return err
		}
		if _, err := b.Write(n); err != nil {
			return err
		}
		pad := (4 - (110+len(n))%4) % 4
		if pad > 0 {
			if _, err := b.Write(bytes.Repeat([]byte{0}, pad)); err != nil {
				return err
			}
		}
		if _, err := b.Write(data); err != nil {
			return err
		}
		pad = (4 - len(data)%4) % 4
		if pad > 0 {
			if _, err := b.Write(bytes.Repeat([]byte{0}, pad)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range files {
		if err := write(f.Name, f.Mode, f.Data); err != nil {
			return nil, err
		}
	}
	if err := write("TRAILER!!!", 0, nil); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func parseHex8(b []byte) (int, error) {
	var v int
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			v |= int(c - 'A' + 10)
		default:
			return 0, fmt.Errorf("bad hex %q", b)
		}
	}
	return v, nil
}

// readSeedISOFiles loads user-data / meta-data from a seed ISO for initrd inject.
func readSeedISOFiles(seedISO string) (userData, metaData []byte, err error) {
	userData, err = isoReadFile(seedISO, "user-data")
	if err != nil {
		return nil, nil, fmt.Errorf("seed user-data: %w", err)
	}
	metaData, err = isoReadFile(seedISO, "meta-data")
	if err != nil {
		return nil, nil, fmt.Errorf("seed meta-data: %w", err)
	}
	return userData, metaData, nil
}
