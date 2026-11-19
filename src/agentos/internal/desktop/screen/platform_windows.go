//go:build windows

package screen

import (
	"image"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows"
)

// sessionID returns the Terminal Services session of this process; services
// run in session 0 which has no interactive desktop.
func sessionID() uint32 {
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err != nil {
		return 0
	}
	return sid
}

func platformAvailable() bool {
	return sessionID() != 0 && screenshot.NumActiveDisplays() > 0
}

func unavailableReason() string {
	if sessionID() == 0 {
		return "running in session 0 (Windows service) – no interactive desktop; start agentos from the user session"
	}
	return "no active display"
}

func fallbackBounds() (image.Rectangle, bool) { return image.Rectangle{}, false }
