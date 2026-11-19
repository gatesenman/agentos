package screen

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Defaults and limits (docs/protocol.md §5, docs/api.md "Remote desktop").
const (
	DefaultFPS     = 8
	DefaultQuality = 60
	DefaultScale   = 1.0
	MinFPS, MaxFPS = 1, 30
	MinQuality     = 1
	MaxQuality     = 100
	MinScale       = 0.1
	MaxScale       = 1.0
	// DefaultKeyframeMS: when the screen is unchanged, a frame is still re-sent at
	// least this often (liveness + lets late joiners/decoders recover).
	DefaultKeyframeMS = 500
	// MaxEncodePixels caps JPEG encode area so nested VMs / low-RAM hosts (e.g.
	// WinBoat guest ~4 GiB) do not OOM when streaming a full desktop into WebView2.
	MaxEncodePixels = 1280 * 720
)

// Config holds the stream settings. Display is an AgentOS extension of the
// `config` message (multi-monitor switching without reconnecting).
type Config struct {
	FPS        int     `json:"fps"`
	Quality    int     `json:"quality"`
	Scale      float64 `json:"scale"`
	Display    int     `json:"display"`
	KeyframeMS int     `json:"keyframe_ms"`
}

func DefaultConfig() Config {
	return Config{FPS: DefaultFPS, Quality: DefaultQuality, Scale: DefaultScale, KeyframeMS: DefaultKeyframeMS}
}

// Clamp forces every field into its valid range.
func (c Config) Clamp() Config {
	c.FPS = clampInt(c.FPS, MinFPS, MaxFPS)
	c.Quality = clampInt(c.Quality, MinQuality, MaxQuality)
	if !(c.Scale >= MinScale) { // also catches NaN
		c.Scale = MinScale
	}
	if c.Scale > MaxScale {
		c.Scale = MaxScale
	}
	if c.Display < 0 {
		c.Display = 0
	}
	if c.KeyframeMS <= 0 {
		c.KeyframeMS = DefaultKeyframeMS
	}
	c.KeyframeMS = clampInt(c.KeyframeMS, 100, 60000)
	return c
}

// ConfigFromParams builds a Config from TunnelOpen.params (URL query values).
// Invalid values are ignored (defaults kept), out-of-range values clamped.
func ConfigFromParams(p map[string]string) Config {
	c := DefaultConfig()
	if v, err := strconv.Atoi(strings.TrimSpace(p["fps"])); err == nil {
		c.FPS = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(p["quality"])); err == nil {
		c.Quality = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(p["scale"]), 64); err == nil {
		c.Scale = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(p["display"])); err == nil {
		c.Display = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(p["keyframe_ms"])); err == nil {
		c.KeyframeMS = v
	}
	return c.Clamp()
}

// Hello is the first text frame node→browser. Fields beyond width/height/
// displays/format are additive AgentOS extensions.
type Hello struct {
	Type     string  `json:"type"`
	Width    int     `json:"width"`  // remote pixels of the selected display (mouse x/y space)
	Height   int     `json:"height"` //
	Displays int     `json:"displays"`
	Format   string  `json:"format"`
	Display  int     `json:"display"`
	FPS      int     `json:"fps"`
	Quality  int     `json:"quality"`
	Scale    float64 `json:"scale"`
	Input    bool    `json:"input"` // false when input injection is unavailable (view-only)
	Bounds   []Rect  `json:"bounds,omitempty"`
	Platform string  `json:"platform,omitempty"`
}

type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// ErrorMsg is a text frame {"type":"error","message":...}.
type ErrorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Pong answers a client {"type":"ping","t":...} (latency indicator extension).
type Pong struct {
	Type string  `json:"type"`
	T    float64 `json:"t"`
}

// Client→node message kinds.
const (
	MsgMouse  = "mouse"
	MsgKey    = "key"
	MsgText   = "text"
	MsgConfig = "config"
	MsgPing   = "ping"
)

// MouseEvent: action move|down|up|wheel, x/y in remote pixels of the selected display.
type MouseEvent struct {
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button int    `json:"button"` // DOM: 0 left, 1 middle, 2 right
	DX     int    `json:"dx"`
	DY     int    `json:"dy"` // DOM sign: positive = scroll down; 120 per notch
}

type KeyEvent struct {
	Action string `json:"action"` // down|up
	Code   string `json:"code"`   // DOM KeyboardEvent.code
	Key    string `json:"key"`    // DOM KeyboardEvent.key (fallback for unmapped codes)
}

// ConfigUpdate: nil fields are left unchanged.
type ConfigUpdate struct {
	FPS        *int     `json:"fps"`
	Quality    *int     `json:"quality"`
	Scale      *float64 `json:"scale"`
	Display    *int     `json:"display"`
	KeyframeMS *int     `json:"keyframe_ms"`
}

func (u ConfigUpdate) Apply(c Config) Config {
	if u.FPS != nil {
		c.FPS = *u.FPS
	}
	if u.Quality != nil {
		c.Quality = *u.Quality
	}
	if u.Scale != nil {
		c.Scale = *u.Scale
	}
	if u.Display != nil {
		c.Display = *u.Display
	}
	if u.KeyframeMS != nil {
		c.KeyframeMS = *u.KeyframeMS
	}
	return c.Clamp()
}

// ClientMsg is a parsed browser→node message; exactly one pointer is set
// according to Type.
type ClientMsg struct {
	Type   string
	Mouse  *MouseEvent
	Key    *KeyEvent
	Text   string
	Config *ConfigUpdate
	PingT  float64
}

var ErrUnknownType = errors.New("unknown message type")

// ParseClientMsg parses and validates one browser→node text frame.
func ParseClientMsg(b []byte) (ClientMsg, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return ClientMsg{}, fmt.Errorf("bad json: %w", err)
	}
	m := ClientMsg{Type: head.Type}
	switch head.Type {
	case MsgMouse:
		var e MouseEvent
		if err := json.Unmarshal(b, &e); err != nil {
			return m, fmt.Errorf("bad mouse message: %w", err)
		}
		switch e.Action {
		case "move", "down", "up", "wheel":
		default:
			return m, fmt.Errorf("bad mouse action %q", e.Action)
		}
		if e.Button < 0 || e.Button > 4 {
			return m, fmt.Errorf("bad mouse button %d", e.Button)
		}
		m.Mouse = &e
	case MsgKey:
		var e KeyEvent
		if err := json.Unmarshal(b, &e); err != nil {
			return m, fmt.Errorf("bad key message: %w", err)
		}
		if e.Action != "down" && e.Action != "up" {
			return m, fmt.Errorf("bad key action %q", e.Action)
		}
		if e.Code == "" && e.Key == "" {
			return m, errors.New("key message without code/key")
		}
		m.Key = &e
	case MsgText:
		var e struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return m, fmt.Errorf("bad text message: %w", err)
		}
		if len(e.Text) > 64<<10 {
			return m, errors.New("text too long")
		}
		m.Text = e.Text
	case MsgConfig:
		var u ConfigUpdate
		if err := json.Unmarshal(b, &u); err != nil {
			return m, fmt.Errorf("bad config message: %w", err)
		}
		m.Config = &u
	case MsgPing:
		var e struct {
			T float64 `json:"t"`
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return m, fmt.Errorf("bad ping message: %w", err)
		}
		m.PingT = e.T
	default:
		return m, fmt.Errorf("%w %q", ErrUnknownType, head.Type)
	}
	return m, nil
}


// CapScaleForSize lowers Scale so width*height*scale^2 stays within MaxEncodePixels.
// Call after the display size is known (and again when Display changes).
func CapScaleForSize(cfg Config, width, height int) Config {
	cfg = cfg.Clamp()
	if width <= 0 || height <= 0 {
		return cfg
	}
	area := float64(width) * float64(height) * cfg.Scale * cfg.Scale
	if area <= float64(MaxEncodePixels) {
		return cfg
	}
	s := math.Sqrt(float64(MaxEncodePixels) / (float64(width) * float64(height)))
	if s < MinScale {
		s = MinScale
	}
	if s > cfg.Scale {
		return cfg
	}
	cfg.Scale = s
	return cfg.Clamp()
}
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
