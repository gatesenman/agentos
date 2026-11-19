package screen

import (
	"errors"
	"testing"
)

func TestParseClientMsg(t *testing.T) {
	m, err := ParseClientMsg([]byte(`{"type":"mouse","action":"down","x":12,"y":34,"button":2}`))
	if err != nil || m.Mouse == nil || m.Mouse.X != 12 || m.Mouse.Y != 34 || m.Mouse.Button != 2 || m.Mouse.Action != "down" {
		t.Fatalf("mouse: %+v %v", m.Mouse, err)
	}
	m, err = ParseClientMsg([]byte(`{"type":"mouse","action":"wheel","x":1,"y":2,"dx":0,"dy":-120}`))
	if err != nil || m.Mouse.DY != -120 {
		t.Fatalf("wheel: %+v %v", m.Mouse, err)
	}
	m, err = ParseClientMsg([]byte(`{"type":"key","action":"up","code":"KeyA","key":"a"}`))
	if err != nil || m.Key == nil || m.Key.Code != "KeyA" || m.Key.Action != "up" {
		t.Fatalf("key: %+v %v", m.Key, err)
	}
	m, err = ParseClientMsg([]byte(`{"type":"text","text":"你好"}`))
	if err != nil || m.Text != "你好" {
		t.Fatalf("text: %q %v", m.Text, err)
	}
	m, err = ParseClientMsg([]byte(`{"type":"config","fps":15,"quality":80}`))
	if err != nil || m.Config == nil || *m.Config.FPS != 15 || *m.Config.Quality != 80 || m.Config.Scale != nil {
		t.Fatalf("config: %+v %v", m.Config, err)
	}
	c := m.Config.Apply(DefaultConfig())
	if c.FPS != 15 || c.Quality != 80 || c.Scale != 1.0 {
		t.Fatalf("apply: %+v", c)
	}
	m, err = ParseClientMsg([]byte(`{"type":"ping","t":123.5}`))
	if err != nil || m.PingT != 123.5 {
		t.Fatalf("ping: %v %v", m.PingT, err)
	}

	bad := []string{
		`not json`,
		`{"type":"mouse","action":"jump","x":1,"y":1}`,
		`{"type":"mouse","action":"down","button":9}`,
		`{"type":"mouse","action":"move","x":"a"}`,
		`{"type":"key","action":"press","code":"KeyA"}`,
		`{"type":"key","action":"down"}`,
		`{"type":"config","fps":"fast"}`,
	}
	for _, b := range bad {
		if _, err := ParseClientMsg([]byte(b)); err == nil {
			t.Errorf("expected error for %s", b)
		}
	}
	if _, err := ParseClientMsg([]byte(`{"type":"future"}`)); !errors.Is(err, ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
}

func TestConfigFromParams(t *testing.T) {
	c := ConfigFromParams(nil)
	if c.FPS != 8 || c.Quality != 60 || c.Scale != 1.0 || c.Display != 0 {
		t.Fatalf("defaults: %+v", c)
	}
	c = ConfigFromParams(map[string]string{"fps": "100", "quality": "0", "scale": "0.01", "display": "2"})
	if c.FPS != MaxFPS || c.Quality != MinQuality || c.Scale != MinScale || c.Display != 2 {
		t.Fatalf("clamp: %+v", c)
	}
	c = ConfigFromParams(map[string]string{"fps": "x", "quality": "75", "scale": "0.5", "display": "-1"})
	if c.FPS != 8 || c.Quality != 75 || c.Scale != 0.5 || c.Display != 0 {
		t.Fatalf("mixed: %+v", c)
	}
	c = ConfigFromParams(map[string]string{"scale": "NaN"})
	if c.Scale != MinScale {
		t.Fatalf("nan: %+v", c)
	}
}

func TestCapScaleForSize(t *testing.T) {
	t.Parallel()
	// 1920x1080 at scale 1 exceeds 1280x720 budget → scale capped below 1
	cfg := CapScaleForSize(Config{FPS: 8, Quality: 60, Scale: 1, KeyframeMS: DefaultKeyframeMS}, 1920, 1080)
	if cfg.Scale >= 1 {
		t.Fatalf("expected scale < 1 for 1080p, got %v", cfg.Scale)
	}
	outW, outH := int(float64(1920)*cfg.Scale+0.5), int(float64(1080)*cfg.Scale+0.5)
	if outW*outH > MaxEncodePixels {
		t.Fatalf("capped pixels %dx%d=%d > MaxEncodePixels %d", outW, outH, outW*outH, MaxEncodePixels)
	}
	// already small: unchanged
	got := CapScaleForSize(Config{Scale: 1, FPS: 8, Quality: 60, KeyframeMS: DefaultKeyframeMS}, 800, 600)
	if got.Scale != 1 {
		t.Fatalf("small frame should keep scale 1, got %v", got.Scale)
	}
	// zero dims keep requested scale
	got = CapScaleForSize(Config{Scale: 0.5, FPS: 8, Quality: 60, KeyframeMS: DefaultKeyframeMS}, 0, 0)
	if got.Scale != 0.5 {
		t.Fatalf("zero dims keep scale, got %v", got.Scale)
	}
	// requested scale already tiny
	got = CapScaleForSize(Config{Scale: 0.1, FPS: 8, Quality: 60, KeyframeMS: DefaultKeyframeMS}, 3840, 2160)
	if got.Scale != 0.1 {
		t.Fatalf("tiny scale should stay, got %v", got.Scale)
	}
}
