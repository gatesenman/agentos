// Package virtual runs a private X11 desktop for a node ("virtual desktop"):
// Xvfb on a free display number, a lightweight window manager (fluxbox,
// openbox or twm, with fluxbox's toolbar as the panel), an xterm, and x11vnc
// bound to 127.0.0.1 on a free port. All processes are supervised (restarted
// with backoff) until Stop. The node then uses Display for the built-in
// screen stream / agents and VNCAddr for the VNC tunnel.
//
// Linux / BSD only. Prerequisites (Debian/Ubuntu):
//
//	apt install xvfb x11vnc fluxbox xterm xdotool
package virtual

import (
	"fmt"
	"os/exec"
	"strings"
)

// Mode value accepted by --desktop / env config "desktop".
const Mode = "virtual"

// Options configure Start.
type Options struct {
	Name   string // used in the xterm title
	Width  int    // default 1280
	Height int    // default 800
	LogDir string // child output (xvfb.log, wm.log, x11vnc.log, xterm.log); "" = discard
	Logf   func(format string, args ...any)
}

// ErrMissing reports missing prerequisite programs.
type ErrMissing struct{ Programs []string }

func (e *ErrMissing) Error() string {
	return fmt.Sprintf("virtual desktop: missing %s (Debian/Ubuntu: apt install xvfb x11vnc fluxbox xterm xdotool)",
		strings.Join(e.Programs, ", "))
}

// Check reports which required programs are missing ("" WM is fine if any of fluxbox/openbox/twm exists).
func Check() error {
	var miss []string
	for _, p := range []string{"Xvfb", "x11vnc"} {
		if _, err := exec.LookPath(p); err != nil {
			miss = append(miss, p)
		}
	}
	if len(miss) > 0 {
		return &ErrMissing{Programs: miss}
	}
	return nil
}

func windowManager() string {
	for _, p := range []string{"fluxbox", "openbox", "twm"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}
