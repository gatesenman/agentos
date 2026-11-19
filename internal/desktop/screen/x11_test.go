//go:build linux

package screen

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// TestX11TunnelFrames runs the real provider against this machine's X display
// over a real WebSocket: hello + ≥3 decodable JPEG frames within 2s.
func TestX11TunnelFrames(t *testing.T) {
	if !platformAvailable() {
		t.Skip("no X display")
	}
	p := New()
	p.Logf = t.Logf
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		params := map[string]string{}
		for k := range r.URL.Query() {
			params[k] = r.URL.Query().Get(k)
		}
		p.Serve(r.Context(), c, params)
	}))
	defer srv.Close()
	q := "/?kind=screen" // protocol defaults: fps 8, quality 60, scale 1.0
	budget := 2 * time.Second
	if raceEnabled {
		// The race detector instruments image/jpeg and the pixel loops (~8x
		// slower, ≈0.6s per 1920x1200 frame); the 2s acceptance budget is
		// asserted in normal (non -race) runs.
		budget = 8 * time.Second
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+q, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	start := time.Now()
	ws.SetReadDeadline(start.Add(budget))
	mt, b, err := ws.ReadMessage()
	if err != nil || mt != websocket.TextMessage {
		t.Fatalf("hello: %v %d", err, mt)
	}
	var h Hello
	if err := json.Unmarshal(b, &h); err != nil || h.Type != "hello" || h.Width <= 0 || h.Height <= 0 || h.Displays < 1 || h.Format != "jpeg" {
		t.Fatalf("bad hello %s", b)
	}
	t.Logf("hello: %s", b)
	frames := 0
	for frames < 3 {
		mt, b, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("after %d frames in %v: %v", frames, time.Since(start), err)
		}
		if mt != websocket.BinaryMessage {
			t.Logf("text: %s", b)
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		if want := int(float64(h.Width)*h.Scale + 0.5); img.Bounds().Dx() != want {
			t.Fatalf("frame size %v vs hello %dx%d scale %v", img.Bounds(), h.Width, h.Height, h.Scale)
		}
		frames++
		t.Logf("frame %d: %d bytes at %v", frames, len(b), time.Since(start))
	}
	if el := time.Since(start); el > budget {
		t.Fatalf("3 frames took %v", el)
	}
}

func TestX11CapturerDisplays(t *testing.T) {
	if !platformAvailable() {
		t.Skip("no X display")
	}
	c, err := newPlatformCapturer()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := c.Displays()
	if err != nil || len(ds) == 0 {
		t.Fatal(ds, err)
	}
	img, err := c.Capture(ds[0])
	if err != nil || img.Bounds().Dx() != ds[0].Dx() {
		t.Fatal(err)
	}
}

// TestXTestMouseMove injects pointer motion and verifies it with QueryPointer.
func TestXTestMouseMove(t *testing.T) {
	if !platformAvailable() {
		t.Skip("no X display")
	}
	inj, err := newPlatformInjector()
	if err != nil {
		t.Skip("XTEST unavailable:", err)
	}
	defer inj.Close()
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	root := xproto.Setup(c).DefaultScreen(c).Root
	orig, _ := xproto.QueryPointer(c, root).Reply()
	defer func() {
		if orig != nil {
			inj.MouseMove(int(orig.RootX), int(orig.RootY))
		}
	}()
	for _, pt := range [][2]int{{37, 41}, {123, 77}} {
		if err := inj.MouseMove(pt[0], pt[1]); err != nil {
			t.Fatal(err)
		}
		var r *xproto.QueryPointerReply
		for i := 0; i < 20; i++ {
			r, err = xproto.QueryPointer(c, root).Reply()
			if err == nil && int(r.RootX) == pt[0] && int(r.RootY) == pt[1] {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil || int(r.RootX) != pt[0] || int(r.RootY) != pt[1] {
			t.Fatalf("pointer at %d,%d want %v (%v)", r.RootX, r.RootY, pt, err)
		}
	}
	// Key lookup for common keys must resolve on any keymap.
	xi := inj.(*xInjector)
	for _, code := range []string{"KeyA", "Digit1", "Enter", "ShiftLeft", "F1", "ArrowLeft"} {
		def, _ := LookupKey(code)
		if _, ok := xi.syms[def.Keysym]; !ok {
			t.Errorf("no X keycode for %s", code)
		}
	}
}
