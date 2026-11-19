//go:build linux

package screen

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// X11 core event types used by XTEST FakeInput.
const (
	xKeyPress      = 2
	xKeyRelease    = 3
	xButtonPress   = 4
	xButtonRelease = 5
	xMotionNotify  = 6

	ksShiftL = 0xffe1
)

type keyPos struct {
	code xproto.Keycode
	col  int // 0 = unshifted, 1 = shifted
}

// xInjector injects input through the XTEST extension (pure Go, jezek/xgb).
type xInjector struct {
	mu        sync.Mutex
	c         *xgb.Conn
	root      xproto.Window
	minKC     xproto.Keycode
	perKC     int
	syms      map[uint32]keyPos
	spare     []xproto.Keycode // keycodes with no keysyms, used for temporary remaps
	spareSym  map[xproto.Keycode]uint32
	spareNext int
}

func newPlatformInjector() (Injector, error) {
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("XTEST extension unavailable: %w", err)
	}
	setup := xproto.Setup(c)
	x := &xInjector{c: c, root: setup.DefaultScreen(c).Root, minKC: setup.MinKeycode, spareSym: map[xproto.Keycode]uint32{}}
	if err := x.loadMapping(setup.MinKeycode, setup.MaxKeycode); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

func (x *xInjector) loadMapping(min, max xproto.Keycode) error {
	r, err := xproto.GetKeyboardMapping(x.c, min, byte(int(max)-int(min)+1)).Reply()
	if err != nil {
		return fmt.Errorf("GetKeyboardMapping: %w", err)
	}
	x.perKC = int(r.KeysymsPerKeycode)
	x.syms = map[uint32]keyPos{}
	n := int(max) - int(min) + 1
	for col := 0; col < 2 && col < x.perKC; col++ { // prefer unshifted positions
		for i := 0; i < n; i++ {
			ks := uint32(r.Keysyms[i*x.perKC+col])
			if ks == 0 {
				continue
			}
			if _, ok := x.syms[ks]; !ok {
				x.syms[ks] = keyPos{code: xproto.Keycode(int(min) + i), col: col}
			}
		}
	}
	for i := n - 1; i >= 0 && len(x.spare) < 8; i-- {
		empty := true
		for col := 0; col < x.perKC; col++ {
			if r.Keysyms[i*x.perKC+col] != 0 {
				empty = false
				break
			}
		}
		if empty {
			x.spare = append(x.spare, xproto.Keycode(int(min)+i))
		}
	}
	return nil
}

func (x *xInjector) fake(typ, detail byte, rx, ry int) error {
	return xtest.FakeInputChecked(x.c, typ, detail, 0, x.root, int16(rx), int16(ry), 0).Check()
}

func (x *xInjector) MouseMove(px, py int) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.fake(xMotionNotify, 0, px, py)
}

func domToXButton(b int) (byte, error) {
	switch b {
	case 0:
		return 1, nil
	case 1:
		return 2, nil
	case 2:
		return 3, nil
	case 3:
		return 8, nil
	case 4:
		return 9, nil
	}
	return 0, fmt.Errorf("unsupported button %d", b)
}

func (x *xInjector) MouseButton(px, py, button int, down bool) error {
	xb, err := domToXButton(button)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.fake(xMotionNotify, 0, px, py); err != nil {
		return err
	}
	t := byte(xButtonRelease)
	if down {
		t = xButtonPress
	}
	return x.fake(t, xb, 0, 0)
}

// wheelClicks converts a DOM delta (≈120 per notch) to a click count ≥1.
func wheelClicks(d int) int {
	if d < 0 {
		d = -d
	}
	n := (d + 60) / 120
	if n < 1 {
		n = 1
	}
	if n > 20 {
		n = 20
	}
	return n
}

func (x *xInjector) MouseWheel(px, py, dx, dy int) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.fake(xMotionNotify, 0, px, py); err != nil {
		return err
	}
	click := func(b byte, n int) error {
		for i := 0; i < n; i++ {
			if err := x.fake(xButtonPress, b, 0, 0); err != nil {
				return err
			}
			if err := x.fake(xButtonRelease, b, 0, 0); err != nil {
				return err
			}
		}
		return nil
	}
	if dy != 0 {
		b := byte(5) // down
		if dy < 0 {
			b = 4
		}
		if err := click(b, wheelClicks(dy)); err != nil {
			return err
		}
	}
	if dx != 0 {
		b := byte(7) // right
		if dx < 0 {
			b = 6
		}
		return click(b, wheelClicks(dx))
	}
	return nil
}

func (x *xInjector) keyEvent(code xproto.Keycode, down bool) error {
	t := byte(xKeyRelease)
	if down {
		t = xKeyPress
	}
	return x.fake(t, byte(code), 0, 0)
}

func (x *xInjector) Key(def KeyDef, down bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	ks := def.Keysym
	pos, ok := x.syms[ks]
	if !ok && ks == 0xfe03 { // AltGr missing → Alt_R
		pos, ok = x.syms[0xffea]
	}
	if !ok {
		return fmt.Errorf("no keycode for keysym 0x%x", ks)
	}
	return x.keyEvent(pos.code, down)
}

// Text types each rune: via its existing keycode (+Shift if needed) or by
// temporarily binding the keysym to a spare keycode.
func (x *xInjector) Text(s string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if err := x.typeRune(r); err != nil {
			return err
		}
	}
	return nil
}

func (x *xInjector) typeRune(r rune) error {
	ks := RuneKeysym(r)
	if pos, ok := x.syms[ks]; ok && pos.col <= 1 {
		if pos.col == 1 {
			if sh, ok := x.syms[ksShiftL]; ok {
				if err := x.keyEvent(sh.code, true); err != nil {
					return err
				}
				defer x.keyEvent(sh.code, false)
			}
		}
		if err := x.keyEvent(pos.code, true); err != nil {
			return err
		}
		return x.keyEvent(pos.code, false)
	}
	code, err := x.bindSpare(ks)
	if err != nil {
		return err
	}
	if err := x.keyEvent(code, true); err != nil {
		return err
	}
	return x.keyEvent(code, false)
}

// bindSpare maps ks onto one of the spare keycodes (round robin, reusing an
// existing binding) so clients have time to process MappingNotify.
func (x *xInjector) bindSpare(ks uint32) (xproto.Keycode, error) {
	for code, s := range x.spareSym {
		if s == ks {
			return code, nil
		}
	}
	if len(x.spare) == 0 || x.perKC == 0 {
		return 0, errors.New("no spare keycode for unicode input")
	}
	code := x.spare[x.spareNext%len(x.spare)]
	x.spareNext++
	syms := make([]xproto.Keysym, x.perKC)
	for i := range syms {
		syms[i] = xproto.Keysym(ks)
	}
	if err := xproto.ChangeKeyboardMappingChecked(x.c, 1, code, byte(x.perKC), syms).Check(); err != nil {
		return 0, fmt.Errorf("ChangeKeyboardMapping: %w", err)
	}
	x.spareSym[code] = ks
	time.Sleep(15 * time.Millisecond) // let clients refresh their keymap
	return code, nil
}

func (x *xInjector) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return nil
	}
	empty := make([]xproto.Keysym, x.perKC)
	for code := range x.spareSym {
		_ = xproto.ChangeKeyboardMappingChecked(x.c, 1, code, byte(x.perKC), empty).Check()
	}
	x.c.Close()
	x.c = nil
	return nil
}
