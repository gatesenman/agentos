//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const nodeTaskName = "AgentOS Node"

// installNodeServicePlatform installs the node as the real "AgentOS" Windows
// service (LocalSystem, auto start; service_windows.go). Without admin rights
// it falls back to a per-user logon task, which also keeps screen capture
// available (a service runs in session 0).
func installNodeServicePlatform(exe, dataDir string, nf NodeFile) error {
	err := installNodeAsWindowsService(dataDir, nf)
	if err == nil {
		fmt.Println("已安装 Windows 服务 / installed Windows service \"AgentOS\" (node mode)")
		return nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !strings.Contains(strings.ToLower(err.Error()), "access is denied") {
		return err
	}
	fmt.Println("没有管理员权限，改为安装登录自启动任务 / not elevated, installing a logon task instead")
	return installNodeLogonTask(exe, dataDir)
}

func installNodeLogonTask(exe, dataDir string) error {
	tr := fmt.Sprintf(`"%s" node --data "%s"`, exe, dataDir)
	cmd := exec.Command("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/RL", "LIMITED", "/TN", nodeTaskName, "/TR", tr)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create: %v: %s", err, strings.TrimSpace(string(out)))
	}
	run := exec.Command("schtasks", "/Run", "/TN", nodeTaskName)
	run.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("已安装登录自启动任务 / installed logon task %q\n", nodeTaskName)
	return nil
}
