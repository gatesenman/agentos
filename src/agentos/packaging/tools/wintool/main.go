// Command wintool generates Windows branding assets and resource objects for
// AgentOS (module M8). Pure Go, no external tools.
//
//	go run ./packaging/tools/wintool assets            # (re)draw packaging/windows/{agentos.ico,agentos.png,wizard.bmp,header.bmp} + tray icons
//	go run ./packaging/tools/wintool syso -version 0.1.0 -out cmd/agentos/rsrc_windows_amd64.syso \
//	       -desc "AgentOS" -name agentos.exe [-gui]
//
// The .syso embeds the icon, a version-info block and an application
// manifest (asInvoker, PerMonitorV2 DPI, Common Controls v6); the Go linker
// picks it up automatically for GOOS=windows builds.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: wintool assets|syso [flags]")
	}
	switch os.Args[1] {
	case "assets":
		fs := flag.NewFlagSet("assets", flag.ExitOnError)
		root := fs.String("root", ".", "repository root")
		fs.Parse(os.Args[2:])
		if err := genAssets(*root); err != nil {
			log.Fatal(err)
		}
	case "syso":
		fs := flag.NewFlagSet("syso", flag.ExitOnError)
		ver := fs.String("version", "0.1.0", "product version")
		out := fs.String("out", "", "output .syso path")
		ico := fs.String("icon", "packaging/windows/agentos.ico", "icon file")
		desc := fs.String("desc", "AgentOS", "file description")
		name := fs.String("name", "agentos.exe", "original file name")
		fs.Parse(os.Args[2:])
		if *out == "" {
			log.Fatal("-out required")
		}
		if err := genSyso(*ver, *out, *ico, *desc, *name); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

// ---------------- drawing ----------------

type vec struct{ x, y float64 }

func lerpC(a, b [3]float64, t float64) [3]float64 {
	t = math.Max(0, math.Min(1, t))
	return [3]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, a[2] + (b[2]-a[2])*t}
}

// sdRoundRect: signed distance to a rounded square centred at (0.5,0.5).
func sdRoundRect(p vec, half, r float64) float64 {
	qx := math.Abs(p.x-0.5) - (half - r)
	qy := math.Abs(p.y-0.5) - (half - r)
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

func sdSegment(p, a, b vec) float64 {
	pax, pay := p.x-a.x, p.y-a.y
	bax, bay := b.x-a.x, b.y-a.y
	h := math.Max(0, math.Min(1, (pax*bax+pay*bay)/(bax*bax+bay*bay)))
	return math.Hypot(pax-bax*h, pay-bay*h)
}

// The logo: a rounded square with an indigo->cyan diagonal gradient and a
// white "agent network": an A-shaped triangle of three nodes plus a hub.
var (
	cTop    = [3]float64{79, 70, 229} // indigo-600
	cBottom = [3]float64{6, 182, 212} // cyan-500
	nodes   = []vec{{0.5, 0.24}, {0.25, 0.74}, {0.75, 0.74}}
	hub     = vec{0.5, 0.56}
)

// sample returns premultiplied-free RGBA (0..1) at point p in unit space.
func sample(p vec, px float64) (r, g, b, a float64) {
	d := sdRoundRect(p, 0.47, 0.2)
	if d > 0 {
		return 0, 0, 0, 0
	}
	c := lerpC(cTop, cBottom, (p.x+p.y)/2)
	// subtle top highlight
	if p.y < 0.5 {
		k := (0.5 - p.y) * 0.18
		c = lerpC(c, [3]float64{255, 255, 255}, k)
	}
	r, g, b, a = c[0]/255, c[1]/255, c[2]/255, 1
	// stroke widths scale up a bit for tiny sizes to stay legible
	lw := math.Max(0.045, 1.6*px)
	nr := math.Max(0.085, 2.2*px)
	hr := math.Max(0.06, 1.8*px)
	white := false
	edges := [][2]vec{{nodes[0], nodes[1]}, {nodes[0], nodes[2]}, {nodes[1], nodes[2]},
		{hub, nodes[0]}, {hub, nodes[1]}, {hub, nodes[2]}}
	for i, e := range edges {
		w := lw
		if i >= 3 {
			w = lw * 0.6
		}
		if sdSegment(p, e[0], e[1]) < w/2 {
			white = true
		}
	}
	for _, n := range nodes {
		if math.Hypot(p.x-n.x, p.y-n.y) < nr {
			white = true
		}
	}
	if math.Hypot(p.x-hub.x, p.y-hub.y) < hr {
		// hub: cyan dot with white ring
		if math.Hypot(p.x-hub.x, p.y-hub.y) < hr*0.55 {
			return 0.2, 0.83, 0.93, 1
		}
		white = true
	}
	if white {
		return 1, 1, 1, 1
	}
	return
}

func renderIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 6
	px := 1.0 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sr, sg, sb, sa float64
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					p := vec{(float64(x) + (float64(i)+0.5)/ss) * px, (float64(y) + (float64(j)+0.5)/ss) * px}
					r, g, b, a := sample(p, px)
					sr += r * a
					sg += g * a
					sb += b * a
					sa += a
				}
			}
			n := float64(ss * ss)
			if sa == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(sr / sa * 255), uint8(sg / sa * 255), uint8(sb / sa * 255), uint8(sa / n * 255)})
		}
	}
	return img
}

// renderBanner draws an RGB bitmap with the gradient and the logo placed at
// (lx,ly) with size ls; used for the NSIS wizard/header bitmaps.
func renderBanner(w, h int, lx, ly, ls int, gradient bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := [3]float64{255, 255, 255}
			if gradient {
				c = lerpC(cTop, cBottom, float64(y)/float64(h)*0.9+float64(x)/float64(w)*0.1)
				// soft decorative circles
				for _, cc := range []struct{ x, y, r float64 }{{0.9, 0.85, 0.45}, {0.1, 0.95, 0.3}} {
					dx := float64(x)/float64(w) - cc.x
					dy := (float64(y) - cc.y*float64(h)) / float64(w)
					if math.Hypot(dx, dy) < cc.r {
						c = lerpC(c, [3]float64{255, 255, 255}, 0.08)
					}
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(c[0]), uint8(c[1]), uint8(c[2]), 255})
		}
	}
	logo := renderIcon(ls)
	for y := 0; y < ls; y++ {
		for x := 0; x < ls; x++ {
			s := logo.NRGBAAt(x, y)
			if s.A == 0 {
				continue
			}
			d := img.NRGBAAt(lx+x, ly+y)
			a := float64(s.A) / 255
			mix := func(sv, dv uint8) uint8 { return uint8(float64(sv)*a + float64(dv)*(1-a)) }
			img.SetNRGBA(lx+x, ly+y, color.NRGBA{mix(s.R, d.R), mix(s.G, d.G), mix(s.B, d.B), 255})
		}
	}
	return img
}

// ---------------- file formats ----------------

// encodeICO writes classic BMP (32-bit BGRA + AND mask) entries for sizes
// < 256 and a PNG entry for 256, which every Windows version and NSIS accept.
func encodeICO(imgs []*image.NRGBA) ([]byte, error) {
	var entries [][]byte
	for _, im := range imgs {
		s := im.Bounds().Dx()
		if s >= 256 {
			var b bytes.Buffer
			if err := png.Encode(&b, im); err != nil {
				return nil, err
			}
			entries = append(entries, b.Bytes())
			continue
		}
		var b bytes.Buffer
		hdr := struct {
			Size                     uint32
			W, H                     int32
			Planes, Bits             uint16
			Compression, ImageSize   uint32
			XPPM, YPPM, Used, Import uint32
		}{40, int32(s), int32(2 * s), 1, 32, 0, 0, 0, 0, 0, 0}
		binary.Write(&b, binary.LittleEndian, hdr)
		for y := s - 1; y >= 0; y-- {
			for x := 0; x < s; x++ {
				c := im.NRGBAAt(x, y)
				b.Write([]byte{c.B, c.G, c.R, c.A})
			}
		}
		rowBytes := ((s + 31) / 32) * 4
		mask := make([]byte, rowBytes*s) // all zero: alpha channel decides
		b.Write(mask)
		entries = append(entries, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	for i, im := range imgs {
		s := im.Bounds().Dx()
		d := uint8(s)
		if s >= 256 {
			d = 0
		}
		binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Res uint8
			Planes, Bits      uint16
			Size, Offset      uint32
		}{d, d, 0, 0, 1, 32, uint32(len(entries[i])), uint32(offset)})
		offset += len(entries[i])
	}
	for _, e := range entries {
		out.Write(e)
	}
	return out.Bytes(), nil
}

// encodeBMP24 writes a bottom-up 24-bit BMP (what NSIS MUI expects).
func encodeBMP24(im *image.NRGBA) []byte {
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	row := (w*3 + 3) &^ 3
	var b bytes.Buffer
	size := 54 + row*h
	b.WriteString("BM")
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(size), 0, 54, 40})
	binary.Write(&b, binary.LittleEndian, []int32{int32(w), int32(h)})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 24})
	binary.Write(&b, binary.LittleEndian, []uint32{0, uint32(row * h), 2835, 2835, 0, 0})
	pad := make([]byte, row-w*3)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := im.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R})
		}
		b.Write(pad)
	}
	return b.Bytes()
}

func genAssets(root string) error {
	win := filepath.Join(root, "packaging", "windows")
	tray := filepath.Join(root, "cmd", "agentos-tray")
	os.MkdirAll(win, 0o755)
	var imgs []*image.NRGBA
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 128, 256} {
		imgs = append(imgs, renderIcon(s))
	}
	ico, err := encodeICO(imgs)
	if err != nil {
		return err
	}
	// tray icon: small sizes only (keeps the binary small)
	trayICO, err := encodeICO([]*image.NRGBA{renderIcon(16), renderIcon(20), renderIcon(24), renderIcon(32), renderIcon(48)})
	if err != nil {
		return err
	}
	var big, small bytes.Buffer
	png.Encode(&big, imgs[len(imgs)-1])
	png.Encode(&small, renderIcon(64))
	files := map[string][]byte{
		filepath.Join(win, "agentos.ico"): ico,
		filepath.Join(win, "agentos.png"): big.Bytes(),
		filepath.Join(win, "wizard.bmp"):  encodeBMP24(renderBanner(164, 314, 34, 40, 96, true)),
		filepath.Join(win, "header.bmp"):  encodeBMP24(renderBanner(150, 57, 99, 5, 47, false)),
		filepath.Join(tray, "icon.ico"):   trayICO,
		filepath.Join(tray, "icon.png"):   small.Bytes(),
	}
	for p, b := range files {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", p, len(b))
	}
	return nil
}

// ---------------- .syso ----------------

var numRe = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

func numericVersion(v string) [4]uint16 {
	var out [4]uint16
	m := numRe.FindStringSubmatch(v)
	for i := 1; i < len(m) && i <= 3; i++ {
		n, _ := strconv.Atoi(m[i])
		out[i-1] = uint16(n)
	}
	return out
}

func genSyso(ver, out, icoPath, desc, name string) error {
	rs := &winres.ResourceSet{}
	f, err := os.Open(icoPath)
	if err != nil {
		return err
	}
	defer f.Close()
	icon, err := winres.LoadICO(f)
	if err != nil {
		return fmt.Errorf("load icon: %w", err)
	}
	if err := rs.SetIcon(winres.Name("APP"), icon); err != nil {
		return err
	}
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "AgentOS." + name},
		Description:         desc,
		Compatibility:       winres.Win7AndAbove,
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
	})
	n := numericVersion(ver)
	vi := version.Info{FileVersion: n, ProductVersion: n}
	for k, v := range map[string]string{
		version.ProductName:      "AgentOS",
		version.ProductVersion:   ver,
		version.FileVersion:      ver,
		version.FileDescription:  desc,
		version.OriginalFilename: name,
		version.InternalName:     name,
		version.CompanyName:      "AgentOS",
		version.LegalCopyright:   "Copyright © AgentOS contributors",
	} {
		vi.Set(0, k, v) // language-neutral
	}
	rs.SetVersionInfo(vi)
	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	return rs.WriteObject(w, winres.ArchAMD64)
}
