package hyperv

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// isoLocate returns the LBA and size of a file inside an ISO 9660 image
// (names compared case-insensitively, ";1" and trailing "." ignored).
func isoLocate(f *os.File, name string) (lba uint32, size uint32, err error) {
	pvd := make([]byte, isoSector)
	if _, err = f.ReadAt(pvd, 16*isoSector); err != nil {
		return 0, 0, err
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return 0, 0, fmt.Errorf("not an ISO 9660 image")
	}
	lba = binary.LittleEndian.Uint32(pvd[156+2:])
	size = binary.LittleEndian.Uint32(pvd[156+10:])
	parts := strings.Split(strings.Trim(name, "/"), "/")
	for i, part := range parts {
		dir := make([]byte, size)
		if _, err = f.ReadAt(dir, int64(lba)*isoSector); err != nil && err != io.EOF {
			return 0, 0, err
		}
		// Match Joliet-style names and ISO-9660 primary sanitisation
		// (e.g. "user-data" is stored as "USER_DATA.;1" on the PVD).
		wants := []string{part}
		pn := strings.TrimSuffix(strings.SplitN(primaryName(part), ";", 2)[0], ".")
		if pn != "" && !strings.EqualFold(pn, part) {
			wants = append(wants, pn)
		}
		found := false
		for off := 0; off < len(dir); {
			l := int(dir[off])
			if l == 0 {
				off = (off/isoSector + 1) * isoSector
				continue
			}
			if off+l > len(dir) || l < 34 {
				break
			}
			r := dir[off : off+l]
			n := string(r[33 : 33+int(r[32])])
			n = strings.TrimSuffix(strings.SplitN(n, ";", 2)[0], ".")
			match := false
			for _, w := range wants {
				if strings.EqualFold(n, w) {
					match = true
					break
				}
			}
			if match {
				lba = binary.LittleEndian.Uint32(r[2:])
				size = binary.LittleEndian.Uint32(r[10:])
				isDir := r[25]&2 != 0
				if (i < len(parts)-1) != isDir {
					return 0, 0, fmt.Errorf("%s: unexpected type", part)
				}
				found = true
				break
			}
			off += l
		}
		if !found {
			return 0, 0, fmt.Errorf("%s not found in ISO", name)
		}
	}
	return lba, size, nil
}

// isoReadFile reads a file from an ISO 9660 image into memory.
// Rejects files larger than 16 MiB (use isoExtractFile for kernels/initrds).
func isoReadFile(isoPath, name string) ([]byte, error) {
	f, err := os.Open(isoPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lba, size, err := isoLocate(f, name)
	if err != nil {
		return nil, err
	}
	if size > 16<<20 {
		return nil, fmt.Errorf("%s too large", name)
	}
	out := make([]byte, size)
	_, err = f.ReadAt(out, int64(lba)*isoSector)
	if err == io.EOF {
		err = nil
	}
	return out, err
}

// isoExtractFile streams a file from an ISO 9660 image onto disk (no size cap).
// Used to pull casper/vmlinuz + casper/initrd for QEMU -kernel/-initrd boot.
func IsoExtractFile(isoPath, name, destPath string) error { return isoExtractFile(isoPath, name, destPath) }

func isoExtractFile(isoPath, name, destPath string) error {
	f, err := os.Open(isoPath)
	if err != nil {
		return err
	}
	defer f.Close()
	lba, size, err := isoLocate(f, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tmp := destPath + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	sr := io.NewSectionReader(f, int64(lba)*isoSector, int64(size))
	if _, err := io.Copy(out, sr); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, destPath)
}

// grubBootFiles returns kernel/initrd of the first menuentry in a grub.cfg.
func grubBootFiles(cfg []byte) (kernel, initrd string) {
	sc := bufio.NewScanner(bytes.NewReader(cfg))
	in := false
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "menuentry ") {
			in = true
			continue
		}
		if !in {
			continue
		}
		fs := strings.Fields(l)
		if len(fs) >= 2 && (fs[0] == "linux" || fs[0] == "linuxefi") && kernel == "" {
			kernel = fs[1]
		}
		if len(fs) >= 2 && (fs[0] == "initrd" || fs[0] == "initrdefi") && initrd == "" {
			initrd = fs[1]
		}
		if l == "}" {
			break
		}
	}
	return
}
