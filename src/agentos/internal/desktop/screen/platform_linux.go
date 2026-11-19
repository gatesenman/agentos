//go:build linux

package screen

import (
	"os"

	"github.com/jezek/xgb"
)

func platformAvailable() bool {
	if os.Getenv("DISPLAY") == "" {
		return false
	}
	c, err := xgb.NewConn()
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func unavailableReason() string {
	if os.Getenv("DISPLAY") == "" {
		return "DISPLAY not set (X11 required)"
	}
	return "cannot connect to X display " + os.Getenv("DISPLAY")
}
