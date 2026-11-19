package screen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/desktop"
)

type wsMsg struct {
	t int
	b []byte
}

// fakeConn is an in-memory desktop.Conn.
type fakeConn struct {
	in     chan wsMsg
	out    chan wsMsg
	closed chan struct{}
	once   sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan wsMsg, 64), out: make(chan wsMsg, 4096), closed: make(chan struct{})}
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	select {
	case m := <-c.in:
		return m.t, m.b, nil
	case <-c.closed:
		return 0, nil, errors.New("closed")
	}
}

func (c *fakeConn) WriteMessage(t int, b []byte) error {
	select {
	case <-c.closed:
		return errors.New("closed")
	default:
	}
	select {
	case c.out <- wsMsg{t, append([]byte(nil), b...)}:
		return nil
	case <-c.closed:
		return errors.New("closed")
	}
}

func (c *fakeConn) Close() error { c.once.Do(func() { close(c.closed) }); return nil }

func (c *fakeConn) send(s string) { c.in <- wsMsg{desktop.TextMessage, []byte(s)} }

// fakeCapturer returns solid frames; if changing, each capture differs.
type fakeCapturer struct {
	displays []image.Rectangle
	changing bool
	captures atomic.Int64
}

func (f *fakeCapturer) Displays() ([]image.Rectangle, error) { return f.displays, nil }
func (f *fakeCapturer) Capture(r image.Rectangle) (*image.RGBA, error) {
	n := f.captures.Add(1)
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	v := byte(100)
	if f.changing {
		v = byte(n)
	}
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img, nil
}
func (f *fakeCapturer) Close() error { return nil }

type fakeInjector struct {
	mu     sync.Mutex
	events []string
	closed bool
}

func (f *fakeInjector) add(s string) error {
	f.mu.Lock()
	f.events = append(f.events, s)
	f.mu.Unlock()
	return nil
}
func (f *fakeInjector) MouseMove(x, y int) error { return f.add(fmt.Sprintf("move %d,%d", x, y)) }
func (f *fakeInjector) MouseButton(x, y, b int, down bool) error {
	return f.add(fmt.Sprintf("button %d,%d %d %v", x, y, b, down))
}
func (f *fakeInjector) MouseWheel(x, y, dx, dy int) error {
	return f.add(fmt.Sprintf("wheel %d,%d %d %d", x, y, dx, dy))
}
func (f *fakeInjector) Key(d KeyDef, down bool) error {
	return f.add(fmt.Sprintf("key %#x %v", d.VK, down))
}
func (f *fakeInjector) Text(s string) error { return f.add("text " + s) }
func (f *fakeInjector) Close() error        { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
func (f *fakeInjector) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func fakeProvider(c Capturer, inj Injector) *Provider {
	return &Provider{
		NewCapturer: func() (Capturer, error) { return c, nil },
		NewInjector: func() (Injector, error) {
			if inj == nil {
				return nil, errors.New("no input")
			}
			return inj, nil
		},
		IsAvailable: func() bool { return true },
	}
}

// run starts Serve; the returned channel is closed when Serve returns.
func run(t *testing.T, p *Provider, params map[string]string) (*fakeConn, chan struct{}) {
	t.Helper()
	conn := newFakeConn()
	done := make(chan struct{})
	go func() { p.Serve(context.Background(), conn, params); close(done) }()
	t.Cleanup(func() {
		conn.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Serve did not return after close")
		}
	})
	return conn, done
}

func readHello(t *testing.T, c *fakeConn) Hello {
	t.Helper()
	select {
	case m := <-c.out:
		var h Hello
		if m.t != desktop.TextMessage || json.Unmarshal(m.b, &h) != nil || h.Type != "hello" {
			t.Fatalf("first message not hello: %d %s", m.t, m.b)
		}
		return h
	case <-time.After(2 * time.Second):
		t.Fatal("no hello")
	}
	return Hello{}
}

// collect drains output for d and returns binary frames and text messages.
func collect(c *fakeConn, d time.Duration) (frames [][]byte, texts []string) {
	deadline := time.After(d)
	for {
		select {
		case m := <-c.out:
			if m.t == desktop.BinaryMessage {
				frames = append(frames, m.b)
			} else {
				texts = append(texts, string(m.b))
			}
		case <-deadline:
			return
		}
	}
}

func TestServeHelloAndFrames(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 320, 200), image.Rect(320, 0, 960, 480)}, changing: true}
	conn, _ := run(t, fakeProvider(capt, &fakeInjector{}), map[string]string{"display": "1", "fps": "20", "scale": "0.5"})
	h := readHello(t, conn)
	if h.Width != 640 || h.Height != 480 || h.Displays != 2 || h.Format != "jpeg" || h.Display != 1 || !h.Input || len(h.Bounds) != 2 {
		t.Fatalf("hello: %+v", h)
	}
	frames, _ := collect(conn, 400*time.Millisecond)
	if len(frames) < 3 {
		t.Fatalf("only %d frames", len(frames))
	}
	img, err := jpeg.Decode(bytes.NewReader(frames[0]))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Fatalf("scaled size %v", b)
	}
}

func TestServeFPSLimit(t *testing.T) {
	for _, fps := range []int{2, 5, 10} {
		capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 64, 48)}, changing: true}
		conn, _ := run(t, fakeProvider(capt, nil), map[string]string{"fps": fmt.Sprint(fps)})
		readHello(t, conn)
		frames, _ := collect(conn, time.Second)
		// First frame is immediate, then one per 1/fps: expect ≈ fps+1 in 1s.
		if len(frames) > fps+2 || len(frames) < fps-1 {
			t.Errorf("fps=%d: got %d frames in 1s", fps, len(frames))
		}
		conn.Close()
	}
}

func TestServeConfigChangesFPS(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 64, 48)}, changing: true}
	conn, _ := run(t, fakeProvider(capt, nil), map[string]string{"fps": "2"})
	readHello(t, conn)
	conn.send(`{"type":"config","fps":20}`)
	frames, _ := collect(conn, time.Second)
	if len(frames) < 10 { // ~20 expected; loose for loaded CI
		t.Errorf("after config fps=20 got only %d frames", len(frames))
	}
}

func TestServeSkipsUnchangedFrames(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 64, 48)}}
	conn, _ := run(t, fakeProvider(capt, nil), map[string]string{"fps": "20", "keyframe_ms": "500"})
	readHello(t, conn)
	frames, _ := collect(conn, 1200*time.Millisecond)
	// Static screen: initial frame + keyframes at ~500ms, ~1000ms.
	if len(frames) < 2 || len(frames) > 4 {
		t.Errorf("static screen: %d frames (captures %d)", len(frames), capt.captures.Load())
	}
	if capt.captures.Load() < 15 {
		t.Errorf("captures %d, expected ~24", capt.captures.Load())
	}
}

func TestServeInputAndRelease(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 100, 100), image.Rect(100, 50, 300, 250)}}
	inj := &fakeInjector{}
	conn, done := run(t, fakeProvider(capt, inj), map[string]string{"display": "1"})
	readHello(t, conn)
	conn.send(`{"type":"mouse","action":"move","x":10,"y":20}`)
	conn.send(`{"type":"mouse","action":"down","x":10,"y":20,"button":0}`)
	conn.send(`{"type":"mouse","action":"wheel","x":5000,"y":-5,"dy":120}`)
	conn.send(`{"type":"key","action":"down","code":"ShiftLeft","key":"Shift"}`)
	conn.send(`{"type":"key","action":"down","code":"KeyA","key":"A"}`)
	conn.send(`{"type":"key","action":"up","code":"KeyA","key":"A"}`)
	conn.send(`{"type":"key","action":"down","code":"Lang9","key":"ß"}`)
	conn.send(`{"type":"text","text":"你好"}`)
	conn.send(`{"type":"ping","t":42}`)
	conn.send(`{"type":"mouse","action":"fly"}`)
	_, texts := collect(conn, 300*time.Millisecond)
	conn.Close()
	<-done
	got := inj.snapshot()
	want := []string{
		"move 110,70",
		"button 110,70 0 true",
		"wheel 299,50 0 120", // clamped to display bounds
		"key 0xa0 true",
		"key 0x41 true",
		"key 0x41 false",
		"text ß",
		"text 你好",
	}
	if len(got) < len(want) {
		t.Fatalf("events %v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("event %d = %q want %q", i, got[i], w)
		}
	}
	// On disconnect: held shift + left button released, injector closed.
	rest := got[len(want):]
	if len(rest) != 2 || rest[0] != "key 0xa0 false" || rest[1] != "button 299,50 0 false" {
		t.Errorf("release events %v", rest)
	}
	if !inj.closed {
		t.Error("injector not closed")
	}
	var pong, errMsg bool
	for _, s := range texts {
		var m map[string]any
		json.Unmarshal([]byte(s), &m)
		if m["type"] == "pong" && m["t"] == 42.0 {
			pong = true
		}
		if m["type"] == "error" {
			errMsg = true
		}
	}
	if !pong || !errMsg {
		t.Errorf("pong=%v error=%v texts=%v", pong, errMsg, texts)
	}
}

func TestServeDisplaySwitchSendsHello(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 100, 80), image.Rect(100, 0, 300, 160)}}
	conn, _ := run(t, fakeProvider(capt, nil), nil)
	if h := readHello(t, conn); h.Input || h.Width != 100 {
		t.Fatalf("hello %+v", h)
	}
	conn.send(`{"type":"config","display":1}`)
	_, texts := collect(conn, 300*time.Millisecond)
	found := false
	for _, s := range texts {
		var h Hello
		if json.Unmarshal([]byte(s), &h) == nil && h.Type == "hello" && h.Display == 1 && h.Width == 200 && h.Height == 160 {
			found = true
		}
	}
	if !found {
		t.Errorf("no hello for display 1: %v", texts)
	}
}

func TestServeCaptureUnavailable(t *testing.T) {
	p := &Provider{NewCapturer: func() (Capturer, error) { return nil, errors.New("nope") }}
	conn := newFakeConn()
	if err := p.Serve(context.Background(), conn, nil); err == nil {
		t.Fatal("expected error")
	}
	m := <-conn.out
	if !bytes.Contains(m.b, []byte(`"type":"error"`)) {
		t.Fatalf("got %s", m.b)
	}
}

func TestServeContextCancel(t *testing.T) {
	capt := &fakeCapturer{displays: []image.Rectangle{image.Rect(0, 0, 10, 10)}}
	ctx, cancel := context.WithCancel(context.Background())
	conn := newFakeConn()
	done := make(chan error, 1)
	go func() { done <- fakeProvider(capt, &fakeInjector{}).Serve(ctx, conn, nil) }()
	readHello(t, conn)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop on cancel")
	}
}

func TestScaleRGBA(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		px := i / 4
		v := byte(0)
		if px%2 == 1 {
			v = 200
		}
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = v, v, v, 255
	}
	dst := scaleRGBA(src, 0.5, nil)
	if dst.Bounds().Dx() != 2 || dst.Bounds().Dy() != 1 {
		t.Fatalf("size %v", dst.Bounds())
	}
	if dst.Pix[0] != 100 || dst.Pix[3] != 255 {
		t.Fatalf("box average %v", dst.Pix[:4])
	}
}

func TestProviderKind(t *testing.T) {
	p := New()
	if p.Kind() != "screen" || p.Capability() != "desktop.screen" {
		t.Fatal(p.Kind(), p.Capability())
	}
	var _ desktop.Provider = p
}
