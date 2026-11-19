//go:build linux

package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func acquireSingleInstance() bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, "agentos-tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	return true // keep f open for the process lifetime
}

func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func terminate(cmd *exec.Cmd) { cmd.Process.Signal(syscall.SIGTERM) }

func openURL(u string)  { exec.Command("xdg-open", u).Start() }
func openPath(p string) { exec.Command("xdg-open", p).Start() }
func notify(msg string) {
	if err := exec.Command("notify-send", "AgentOS", msg).Run(); err != nil {
		log.Print(msg)
	}
}

// Linux uses systemd for the service; the tray only manages its own child.
func serviceState() string { return "not-installed" }
func serviceStart() error  { return nil }
func serviceStop() error   { return nil }

func autostartSupported() bool   { return false }
func autostartEnabled() bool     { return false }
func setAutostart(on bool) error { return nil }
