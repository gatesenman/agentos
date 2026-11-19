// Package screen is the built-in fallback remote desktop: periodic JPEG
// screen capture streamed as binary frames + JSON mouse/keyboard events
// injected locally. Protocol: docs/protocol.md §5. Owned by module M5.
//
// Platforms:
//   - windows: capture via GDI (kbinani/screenshot), input via SendInput
//     (golang.org/x/sys/windows, no cgo). Must run inside an interactive user
//     session: a Windows *service* lives in session 0 and cannot see the user
//     desktop, so Available() reports false there. Start `agentos node` /
//     `agentos server` from the logged-in user session (e.g. autostart).
//     The secure desktop (UAC prompt, lock screen, Ctrl+Alt+Del) cannot be
//     captured or driven.
//   - linux: X11 capture (MIT-SHM/XGetImage via jezek/xgb) and input via the
//     XTEST extension, both pure Go. Requires $DISPLAY.
//   - darwin and others: unsupported stub (Available()=false) – capture on
//     macOS requires cgo.
package screen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"image"
	"image/jpeg"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/desktop"
)

// ErrNotImplemented is returned on platforms without capture support.
var ErrNotImplemented = errors.New("screen streaming not supported on this platform")

// Capturer grabs screen pixels. Display bounds are in virtual-desktop
// coordinates (the same space the Injector uses).
type Capturer interface {
	Displays() ([]image.Rectangle, error)
	Capture(r image.Rectangle) (*image.RGBA, error)
	Close() error
}

// Injector synthesises local input. Mouse coordinates passed in are absolute
// virtual-desktop coordinates.
type Injector interface {
	MouseMove(x, y int) error
	MouseButton(x, y, button int, down bool) error // DOM button numbering
	MouseWheel(x, y, dx, dy int) error             // DOM sign/scale (120 per notch)
	Key(def KeyDef, down bool) error
	Text(s string) error
	Close() error
}

// Provider implements desktop.Provider for kind "screen".
type Provider struct {
	NewCapturer func() (Capturer, error)
	NewInjector func() (Injector, error) // may be nil → view-only
	IsAvailable func() bool
	Logf        func(format string, args ...any)
	// Now / clock hooks are only used by tests.
	now func() time.Time
}

// New returns a provider wired to the platform implementation.
func New() *Provider {
	return &Provider{
		NewCapturer: newPlatformCapturer,
		NewInjector: newPlatformInjector,
		IsAvailable: platformAvailable,
		Logf:        log.Printf,
	}
}

func (p *Provider) Kind() string       { return api.TunnelScreen }
func (p *Provider) Capability() string { return api.CapScreen }

// Available reports whether capture works here (cheap check, re-evaluated on demand).
func (p *Provider) Available() bool {
	if p.IsAvailable == nil {
		return p.NewCapturer != nil
	}
	return p.IsAvailable()
}

func (p *Provider) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

type ctrlMsg struct {
	cfg  *ConfigUpdate
	ping *float64
	err  string
}

// Serve streams the screen to conn until the browser disconnects or ctx ends.
func (p *Provider) Serve(ctx context.Context, conn desktop.Conn, params map[string]string) error {
	defer conn.Close()
	cfg := ConfigFromParams(params)

	sendErr := func(msg string) {
		b, _ := json.Marshal(ErrorMsg{Type: "error", Message: msg})
		_ = conn.WriteMessage(desktop.TextMessage, b)
	}
	if p.NewCapturer == nil {
		sendErr(ErrNotImplemented.Error())
		return ErrNotImplemented
	}
	capt, err := p.NewCapturer()
	if err != nil {
		sendErr("屏幕捕获不可用 / screen capture unavailable: " + err.Error())
		return err
	}
	defer capt.Close()
	displays, err := capt.Displays()
	if err == nil && len(displays) == 0 {
		err = errors.New("no active display")
	}
	if err != nil {
		sendErr("屏幕捕获不可用 / screen capture unavailable: " + err.Error())
		return err
	}
	if cfg.Display >= len(displays) {
		cfg.Display = 0
	}
	// Cap encode resolution early so nested/low-RAM hosts stay stable.
	{
		d0 := displays[cfg.Display]
		cfg = CapScaleForSize(cfg, d0.Dx(), d0.Dy())
	}

	var inj Injector
	if p.NewInjector != nil {
		if inj, err = p.NewInjector(); err != nil {
			p.logf("screen: 输入注入不可用 / input injection unavailable: %v", err)
			inj = nil
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cur atomic.Pointer[image.Rectangle] // selected display bounds (read by input goroutine)
	b := displays[cfg.Display]
	cur.Store(&b)

	writeJSON := func(v any) error {
		b, _ := json.Marshal(v)
		return conn.WriteMessage(desktop.TextMessage, b)
	}
	hello := func() error {
		bs := make([]Rect, len(displays))
		for i, d := range displays {
			bs[i] = Rect{X: d.Min.X, Y: d.Min.Y, W: d.Dx(), H: d.Dy()}
		}
		d := displays[cfg.Display]
		return writeJSON(Hello{Type: "hello", Width: d.Dx(), Height: d.Dy(), Displays: len(displays),
			Format: "jpeg", Display: cfg.Display, FPS: cfg.FPS, Quality: cfg.Quality, Scale: cfg.Scale,
			Input: inj != nil, Bounds: bs, Platform: runtime.GOOS})
	}
	if err := hello(); err != nil {
		if inj != nil {
			inj.Close()
		}
		return err
	}

	// Reader: parses browser messages, injects input (serialised here), forwards
	// config/ping to the writer loop, which is the only goroutine writing to conn.
	ctrl := make(chan ctrlMsg, 32)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		in := newInputState(inj, &cur)
		defer in.releaseAll()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt != desktop.TextMessage {
				continue
			}
			m, err := ParseClientMsg(data)
			if err != nil {
				if !errors.Is(err, ErrUnknownType) { // unknown types are ignored (forward compat)
					trySend(ctrl, ctrlMsg{err: err.Error()})
				}
				continue
			}
			switch m.Type {
			case MsgConfig:
				select { // config must not be dropped
				case ctrl <- ctrlMsg{cfg: m.Config}:
				case <-ctx.Done():
					return
				}
			case MsgPing:
				t := m.PingT
				trySend(ctrl, ctrlMsg{ping: &t})
			default:
				if err := in.handle(m); err != nil {
					p.logf("screen: input %s: %v", m.Type, err)
				}
			}
		}
	}()
	defer func() {
		cancel()
		conn.Close() // unblocks the reader
		<-readerDone
		if inj != nil {
			inj.Close()
		}
	}()

	fs := newFrameStreamer()
	timer := time.NewTimer(0)
	defer timer.Stop()
	next := p.clock()
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-ctrl:
			switch {
			case c.cfg != nil:
				old := cfg
				cfg = c.cfg.Apply(cfg)
				if cfg.Display >= len(displays) {
					if ds, err := capt.Displays(); err == nil && len(ds) > 0 {
						displays = ds
					}
					if cfg.Display >= len(displays) {
						cfg.Display = old.Display
						if cfg.Display >= len(displays) {
							cfg.Display = 0
						}
					}
				}
				b := displays[cfg.Display]
				cfg = CapScaleForSize(cfg, b.Dx(), b.Dy())
				cur.Store(&b)
				fs.force = true
				// Apply immediately (e.g. fps 2→20 should not wait for the old tick).
				next = p.clock()
				timer.Reset(0)
				if cfg.Display != old.Display || cfg.Scale != old.Scale {
					if err := hello(); err != nil {
						return nil
					}
				}
			case c.ping != nil:
				if err := writeJSON(Pong{Type: "pong", T: *c.ping}); err != nil {
					return nil
				}
			case c.err != "":
				if err := writeJSON(ErrorMsg{Type: "error", Message: c.err}); err != nil {
					return nil
				}
			}
		case <-timer.C:
			start := p.clock()
			d := displays[cfg.Display]
			frame, err := fs.next(capt, d, cfg, start)
			if err != nil {
				fs.failures++
				if fs.failures == 1 || fs.failures%50 == 0 {
					p.logf("screen: 捕获失败 / capture failed: %v", err)
					if werr := writeJSON(ErrorMsg{Type: "error", Message: "capture failed: " + err.Error()}); werr != nil {
						return nil
					}
				}
			} else {
				fs.failures = 0
				if frame != nil {
					if err := conn.WriteMessage(desktop.BinaryMessage, frame); err != nil {
						return nil
					}
				}
			}
			// Fixed-rate schedule; if we fell behind, don't burst to catch up.
			interval := time.Second / time.Duration(cfg.FPS)
			next = next.Add(interval)
			now := p.clock()
			if next.Before(now) {
				next = now
			}
			timer.Reset(next.Sub(now))
		}
	}
}

func trySend(ch chan ctrlMsg, m ctrlMsg) {
	select {
	case ch <- m:
	default:
	}
}

// frameStreamer captures, dedupes (by hash), scales and JPEG-encodes frames.
type frameStreamer struct {
	seed     maphash.Seed
	lastHash uint64
	lastSent time.Time
	lastCfg  Config
	lastRect image.Rectangle
	force    bool
	failures int
	buf      bytes.Buffer
	scaled   *image.RGBA
}

func newFrameStreamer() *frameStreamer {
	return &frameStreamer{seed: maphash.MakeSeed(), force: true}
}

// next returns an encoded JPEG, or nil when the frame is unchanged and no
// keyframe is due.
func (f *frameStreamer) next(c Capturer, r image.Rectangle, cfg Config, now time.Time) ([]byte, error) {
	img, err := c.Capture(r)
	if err != nil {
		return nil, err
	}
	h := maphash.Bytes(f.seed, img.Pix)
	keyframeDue := now.Sub(f.lastSent) >= time.Duration(cfg.KeyframeMS)*time.Millisecond
	if !f.force && h == f.lastHash && !keyframeDue && cfg == f.lastCfg && r == f.lastRect {
		return nil, nil
	}
	var src image.Image = img
	if cfg.Scale < 0.999 {
		f.scaled = scaleRGBA(img, cfg.Scale, f.scaled)
		src = f.scaled
	}
	f.buf.Reset()
	if err := jpeg.Encode(&f.buf, src, &jpeg.Options{Quality: cfg.Quality}); err != nil {
		return nil, fmt.Errorf("jpeg: %w", err)
	}
	f.lastHash, f.lastSent, f.lastCfg, f.lastRect, f.force = h, now, cfg, r, false
	out := make([]byte, f.buf.Len())
	copy(out, f.buf.Bytes())
	return out, nil
}

// scaleRGBA downsamples src by factor s (0<s<1) using a box filter.
func scaleRGBA(src *image.RGBA, s float64, dst *image.RGBA) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := int(float64(sw)*s+0.5), int(float64(sh)*s+0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	if dst == nil || dst.Bounds().Dx() != dw || dst.Bounds().Dy() != dh {
		dst = image.NewRGBA(image.Rect(0, 0, dw, dh))
	}
	xs := make([]int, dw+1)
	for x := 0; x <= dw; x++ {
		xs[x] = x * sw / dw
	}
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		drow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			x0, x1 := xs[x], xs[x+1]
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint32
			for yy := y0; yy < y1; yy++ {
				row := src.Pix[yy*src.Stride+x0*4 : yy*src.Stride+x1*4]
				for _, v := range unsafe.Slice((*uint32)(unsafe.Pointer(&row[0])), x1-x0) {
					r += v & 0xff // little-endian RGBA
					g += (v >> 8) & 0xff
					b += (v >> 16) & 0xff
					n++
				}
			}
			d := (*uint32)(unsafe.Pointer(&drow[x*4]))
			*d = (r / n) | (g/n)<<8 | (b/n)<<16 | 0xff000000
		}
	}
	return dst
}

// inputState maps protocol events to the injector, translating display-local
// coordinates and tracking pressed keys/buttons so they are released when the
// session ends (no stuck modifiers).
type inputState struct {
	inj     Injector
	cur     *atomic.Pointer[image.Rectangle]
	mu      sync.Mutex
	keys    map[string]KeyDef
	buttons map[int]bool
	lastX   int
	lastY   int
}

func newInputState(inj Injector, cur *atomic.Pointer[image.Rectangle]) *inputState {
	return &inputState{inj: inj, cur: cur, keys: map[string]KeyDef{}, buttons: map[int]bool{}}
}

func (s *inputState) abs(x, y int) (int, int) {
	r := *s.cur.Load()
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x >= r.Dx() {
		x = r.Dx() - 1
	}
	if y >= r.Dy() {
		y = r.Dy() - 1
	}
	return r.Min.X + x, r.Min.Y + y
}

func (s *inputState) handle(m ClientMsg) error {
	if s.inj == nil {
		return nil // view-only
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch m.Type {
	case MsgMouse:
		e := m.Mouse
		x, y := s.abs(e.X, e.Y)
		s.lastX, s.lastY = x, y
		switch e.Action {
		case "move":
			return s.inj.MouseMove(x, y)
		case "down", "up":
			down := e.Action == "down"
			if down {
				s.buttons[e.Button] = true
			} else {
				delete(s.buttons, e.Button)
			}
			return s.inj.MouseButton(x, y, e.Button, down)
		case "wheel":
			return s.inj.MouseWheel(x, y, e.DX, e.DY)
		}
	case MsgKey:
		e := m.Key
		down := e.Action == "down"
		if def, ok := LookupKey(e.Code); ok {
			if down {
				s.keys[e.Code] = def
			} else {
				delete(s.keys, e.Code)
			}
			return s.inj.Key(def, down)
		}
		// Unmapped physical key (e.g. IME/locale specific): type the produced character.
		if r, ok := singleRune(e.Key); ok && down {
			return s.inj.Text(string(r))
		}
		return nil
	case MsgText:
		if m.Text == "" {
			return nil
		}
		return s.inj.Text(m.Text)
	}
	return nil
}

func (s *inputState) releaseAll() {
	if s.inj == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, def := range s.keys {
		_ = s.inj.Key(def, false)
		delete(s.keys, code)
	}
	for b := range s.buttons {
		_ = s.inj.MouseButton(s.lastX, s.lastY, b, false)
		delete(s.buttons, b)
	}
}
