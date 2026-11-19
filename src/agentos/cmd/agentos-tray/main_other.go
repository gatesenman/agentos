//go:build !windows && !linux

// fyne.io/systray needs cgo on macOS; the tray is not built there (use the
// launchd plist from the macOS archive instead).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "agentos-tray is only available on Windows and Linux; use launchd (see packaging/macos) on this platform")
	os.Exit(1)
}
