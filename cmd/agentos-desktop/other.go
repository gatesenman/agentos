//go:build !windows

package main

import (
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var lockFile *os.File

func acquireSingleInstance() bool {
	f, err := os.OpenFile(filepath.Join(appDir(), "desktop.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f
	return true
}

// focusExistingInstance: browser windows cannot be focused portably; the
// running instance owns the window, so there is nothing else to do.
func focusExistingInstance() {}

func trayRunning() bool { return false }

func startService(string) bool { return false }

func hideChildWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func stopServer(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
	}
}

func fatalDialog(title, msg string) { log.Printf("%s: %s", title, msg) }

func openWindow(cfg config, url string) {
	if err := openAppBrowser(url, 1280, 800); err != nil {
		log.Printf("browser: %v", err)
	}
}

func waitForInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
