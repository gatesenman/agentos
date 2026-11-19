package hyperv

// Minimal ISO 9660 writer (flat root directory, Joliet names) used to build the
// small "seed" CD that carries unattended-install answers into a new VM:
//   - Ubuntu: NoCloud datasource (volume label CIDATA, user-data / meta-data)
//   - Windows: autounattend.xml + first-logon scripts / installers
//   - Fedora: kickstart (volume label OEMDRV, ks.cfg)
// Pure Go (no cgo, no external tools) so it works on any host.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const isoSector = 2048

// SeedFile is one file in the root directory of a seed ISO.
type SeedFile struct {
	Name string // case preserved in Joliet ("user-data"); primary name is upper-cased/sanitised
	Data []byte
	Path string // alternatively read from this host file (large installers)
}

func (f SeedFile) size() (int64, error) {
	if f.Path == "" {
		return int64(len(f.Data)), nil
	}
	st, err := os.Stat(f.Path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// WriteSeedISO writes an ISO 9660 image with a Joliet supplementary volume
// descriptor. All files live in the root directory.
func WriteSeedISO(w io.Writer, label string, files []SeedFile, now time.Time) error {
	if len(files) == 0 {
		return errors.New("iso: no files")
	}
	files = append([]SeedFile(nil), files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	seen, pnames := map[string]bool{}, make([]string, len(files))
	for i, f := range files {
		if seen["j:"+f.Name] {
			return fmt.Errorf("iso: duplicate file name %q", f.Name)
		}
		seen["j:"+f.Name] = true
		pn := primaryName(f.Name)
		if seen[pn] {
			pn = fmt.Sprintf("F%06d.;1", i)
		}
		seen[pn] = true
		pnames[i] = pn
	}
	sizes := make([]int64, len(files))
	for i, f := range files {
		s, err := f.size()
		if err != nil {
			return err
		}
		if s > 0xFFFFFFFF {
			return fmt.Errorf("iso: %s too large", f.Name)
		}
		sizes[i] = s
	}
	// Directory sizes (records never cross a sector boundary).
	dirSize := func(joliet bool) int {
		used, sectors := 34*2, 1
		for i, f := range files {
			var n int
			if joliet {
				n = len(jolietName(f.Name))
			} else {
				n = len(pnames[i])
			}
			l := 33 + n
			if l%2 == 1 {
				l++
			}
			if used+l > isoSector {
				sectors++
				used = 0
			}
			used += l
		}
		return sectors
	}
	pSec, jSec := dirSize(false), dirSize(true)
	const (
		lbaPVD    = 16
		lbaSVD    = 17
		lbaTerm   = 18
		lbaPTLp   = 19
		lbaPTMp   = 20
		lbaPTLj   = 21
		lbaPTMj   = 22
		lbaRootP0 = 23
	)
	lbaRootJ := lbaRootP0 + pSec
	next := lbaRootJ + jSec
	extents := make([]int, len(files))
	for i := range files {
		extents[i] = next
		next += int((sizes[i] + isoSector - 1) / isoSector)
		if sizes[i] == 0 {
			extents[i] = 0
		}
	}
	total := next

	rec := func(extent int, size uint32, dir bool, name []byte) []byte {
		l := 33 + len(name)
		if l%2 == 1 {
			l++
		}
		b := make([]byte, l)
		b[0] = byte(l)
		both32(b[2:], uint32(extent))
		both32(b[10:], size)
		recDate(b[18:], now)
		if dir {
			b[25] = 2
		}
		both16(b[28:], 1)
		b[32] = byte(len(name))
		copy(b[33:], name)
		return b
	}
	dirBytes := func(self int, sectors int, joliet bool) []byte {
		out := make([]byte, 0, sectors*isoSector)
		cur := bytes.NewBuffer(nil)
		flush := func() {
			pad := make([]byte, isoSector-cur.Len())
			out = append(out, cur.Bytes()...)
			out = append(out, pad...)
			cur.Reset()
		}
		size := uint32(sectors * isoSector)
		cur.Write(rec(self, size, true, []byte{0}))
		cur.Write(rec(self, size, true, []byte{1}))
		for i, f := range files {
			var n []byte
			if joliet {
				n = jolietName(f.Name)
			} else {
				n = []byte(pnames[i])
			}
			r := rec(extents[i], uint32(sizes[i]), false, n)
			if cur.Len()+len(r) > isoSector {
				flush()
			}
			cur.Write(r)
		}
		flush()
		for len(out) < sectors*isoSector {
			out = append(out, make([]byte, isoSector)...)
		}
		return out
	}
	pathTable := func(root int, big bool) []byte {
		b := make([]byte, 10)
		b[0] = 1 // name length (root = 0x00)
		if big {
			binary.BigEndian.PutUint32(b[2:], uint32(root))
			binary.BigEndian.PutUint16(b[6:], 1)
		} else {
			binary.LittleEndian.PutUint32(b[2:], uint32(root))
			binary.LittleEndian.PutUint16(b[6:], 1)
		}
		return b
	}
	vd := func(joliet bool) []byte {
		b := make([]byte, isoSector)
		if joliet {
			b[0] = 2
		} else {
			b[0] = 1
		}
		copy(b[1:], "CD001")
		b[6] = 1
		if joliet {
			copy(b[8:40], ucs2Pad("", 16))
			copy(b[40:72], ucs2Pad(label, 16))
		} else {
			copy(b[8:40], padA("", 32))
			copy(b[40:72], padA(strings.ToUpper(label), 32))
		}
		both32(b[80:], uint32(total))
		if joliet {
			copy(b[88:], "%/E") // UCS-2 level 3
		}
		both16(b[120:], 1)
		both16(b[124:], 1)
		both16(b[128:], isoSector)
		both32(b[132:], 10)
		if joliet {
			binary.LittleEndian.PutUint32(b[140:], lbaPTLj)
			binary.BigEndian.PutUint32(b[148:], lbaPTMj)
			copy(b[156:], rec(lbaRootJ, uint32(jSec*isoSector), true, []byte{0}))
		} else {
			binary.LittleEndian.PutUint32(b[140:], lbaPTLp)
			binary.BigEndian.PutUint32(b[148:], lbaPTMp)
			copy(b[156:], rec(lbaRootP0, uint32(pSec*isoSector), true, []byte{0}))
		}
		// volume set, publisher, preparer, application (a-chars / UCS-2)
		for _, off := range []int{190, 318, 446, 574} {
			if joliet {
				copy(b[off:off+128], ucs2Pad("", 64))
			} else {
				copy(b[off:off+128], padA("", 128))
			}
		}
		if joliet {
			copy(b[574:574+128], ucs2Pad("AGENTOS", 64))
		} else {
			copy(b[574:574+128], padA("AGENTOS", 128))
		}
		for _, off := range []int{702, 739, 776} {
			if joliet {
				copy(b[off:off+37], ucs2Pad("", 18))
			} else {
				copy(b[off:off+37], padA("", 37))
			}
		}
		vdDate(b[813:], now)
		vdDate(b[830:], now)
		copy(b[847:], "0000000000000000\x00")
		copy(b[864:], "0000000000000000\x00")
		b[881] = 1
		return b
	}
	sector := func(b []byte) []byte {
		if len(b)%isoSector != 0 {
			b = append(b, make([]byte, isoSector-len(b)%isoSector)...)
		}
		return b
	}
	bw := &countWriter{w: w}
	bw.Write(make([]byte, 16*isoSector))
	bw.Write(vd(false))
	bw.Write(vd(true))
	term := make([]byte, isoSector)
	term[0] = 255
	copy(term[1:], "CD001")
	term[6] = 1
	bw.Write(term)
	bw.Write(sector(pathTable(lbaRootP0, false)))
	bw.Write(sector(pathTable(lbaRootP0, true)))
	bw.Write(sector(pathTable(lbaRootJ, false)))
	bw.Write(sector(pathTable(lbaRootJ, true)))
	bw.Write(dirBytes(lbaRootP0, pSec, false))
	bw.Write(dirBytes(lbaRootJ, jSec, true))
	for i, f := range files {
		if sizes[i] == 0 {
			continue
		}
		if f.Path != "" {
			fh, err := os.Open(f.Path)
			if err != nil {
				return err
			}
			n, err := io.Copy(bw, fh)
			fh.Close()
			if err != nil {
				return err
			}
			if n != sizes[i] {
				return fmt.Errorf("iso: %s changed while writing", f.Path)
			}
		} else {
			bw.Write(f.Data)
		}
		if r := sizes[i] % isoSector; r != 0 {
			bw.Write(make([]byte, isoSector-r))
		}
	}
	if bw.err != nil {
		return bw.err
	}
	if bw.n != int64(total)*isoSector {
		return fmt.Errorf("iso: internal size mismatch %d != %d", bw.n, int64(total)*isoSector)
	}
	return nil
}

// WriteSeedISOFile writes the ISO atomically to path.
func WriteSeedISOFile(path, label string, files []SeedFile) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := WriteSeedISO(f, label, files, time.Now()); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(path)
	return os.Rename(tmp, path)
}

type countWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		c.err = err
	}
	return n, err
}

// primaryName maps a name to an ISO 9660 level-2 style identifier ("USER_DATA.;1").
func primaryName(name string) string {
	up := strings.ToUpper(name)
	base, ext := up, ""
	if i := strings.LastIndex(up, "."); i > 0 {
		base, ext = up[:i], up[i+1:]
	}
	clean := func(s string, max int) string {
		var b strings.Builder
		for _, r := range s {
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		out := b.String()
		if len(out) > max {
			out = out[:max]
		}
		return out
	}
	base = clean(base, 30-len(ext))
	ext = clean(ext, 4)
	if len(base) > 30-len(ext) {
		base = base[:30-len(ext)]
	}
	return base + "." + ext + ";1"
}

func jolietName(name string) []byte {
	return ucs2(name + ";1")
}

func ucs2(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.BigEndian.PutUint16(b[2*i:], c)
	}
	return b
}

func ucs2Pad(s string, chars int) []byte {
	r := []rune(s)
	if len(r) > chars {
		r = r[:chars]
	}
	for len(r) < chars {
		r = append(r, ' ')
	}
	return ucs2(string(r))
}

func padA(s string, n int) []byte {
	if len(s) > n {
		s = s[:n]
	}
	return []byte(s + strings.Repeat(" ", n-len(s)))
}

func both16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
	binary.BigEndian.PutUint16(b[2:], v)
}

func both32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
	binary.BigEndian.PutUint32(b[4:], v)
}

func recDate(b []byte, t time.Time) {
	t = t.UTC()
	b[0] = byte(t.Year() - 1900)
	b[1] = byte(t.Month())
	b[2] = byte(t.Day())
	b[3] = byte(t.Hour())
	b[4] = byte(t.Minute())
	b[5] = byte(t.Second())
	b[6] = 0
}

func vdDate(b []byte, t time.Time) {
	t = t.UTC()
	copy(b, fmt.Sprintf("%04d%02d%02d%02d%02d%02d00", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second()))
	b[16] = 0
}
