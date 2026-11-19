//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "AgentOS"
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue    = "AgentOSTray"
)

var instanceMutex windows.Handle

func acquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\AgentOS.Tray`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return !errors.Is(err, windows.ERROR_ALREADY_EXISTS)
	}
	instanceMutex = h
	return true
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// terminate asks the child to exit. A CREATE_NO_WINDOW child has its own
// hidden console we cannot signal, so kill it; the agent supervisor's Job
// Objects (KILL_ON_JOB_CLOSE) take the agent process trees down with it.
func terminate(cmd *exec.Cmd) { cmd.Process.Kill() }

func shellExecute(target string) {
	verb, _ := windows.UTF16PtrFromString("open")
	t, _ := windows.UTF16PtrFromString(target)
	if err := windows.ShellExecute(0, verb, t, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	}
}

func openURL(u string)  { shellExecute(u) }
func openPath(p string) { shellExecute(p) }

var (
	user32      = windows.NewLazySystemDLL("user32.dll")
	pMessageBox = user32.NewProc("MessageBoxW")
)

func notify(msg string) {
	go func() {
		t, _ := windows.UTF16PtrFromString("AgentOS")
		m, _ := windows.UTF16PtrFromString(msg)
		pMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40 /*MB_ICONINFORMATION*/)
	}()
}

// ---- service (read access works for normal users; start/stop needs admin) ----

func openService(access uint32) (*mgr.Service, func(), error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, err
	}
	name, _ := windows.UTF16PtrFromString(serviceName)
	sh, err := windows.OpenService(h, name, access)
	if err != nil {
		windows.CloseServiceHandle(h)
		return nil, nil, err
	}
	s := &mgr.Service{Name: serviceName, Handle: sh}
	return s, func() { s.Close(); windows.CloseServiceHandle(h) }, nil
}

func serviceState() string {
	s, done, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "not-installed"
	}
	defer done()
	st, err := s.Query()
	if err != nil {
		return "unknown"
	}
	switch st.State {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	}
	return "pending"
}

func serviceStart() error {
	s, done, err := openService(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer done()
	return s.Start()
}

func serviceStop() error {
	s, done, err := openService(windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer done()
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	for i := 0; i < 60; i++ {
		if st, err := s.Query(); err != nil || st.State == svc.Stopped {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

// ---- autostart via HKCU Run (per user; the installer may set HKLM) ----

func autostartSupported() bool { return true }

func autostartEnabled() bool {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, runKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue(runValue)
		k.Close()
		if err == nil && v != "" {
			return true
		}
	}
	return false
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		k.DeleteValue(runValue)
		// a machine-wide entry written by the installer needs admin rights
		if m, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE); err == nil {
			m.DeleteValue(runValue)
			m.Close()
		}
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+strings.ReplaceAll(self, `"`, ``)+`" --open=false`)
}
