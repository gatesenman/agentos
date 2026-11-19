//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const nodeServiceName = "agentos-node"

func installNodeServicePlatform(exe, dataDir string, _ NodeFile) error {
	if runtime.GOOS == "darwin" {
		return installLaunchd(exe, dataDir)
	}
	return installSystemd(exe, dataDir)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installSystemd(exe, dataDir string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd not found; run `%s node --data %s` from your init system instead", exe, dataDir)
	}
	unit := fmt.Sprintf(`[Unit]
Description=AgentOS node
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%q node --data %q
Restart=always
RestartSec=5
KillMode=mixed
TimeoutStopSec=20

[Install]
WantedBy=%s
`, exe, dataDir, "%s")
	user := os.Geteuid() != 0
	var path string
	var ctl []string
	if user {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "systemd", "user", nodeServiceName+".service")
		unit = fmt.Sprintf(unit, "default.target")
		ctl = []string{"--user"}
	} else {
		path = "/etc/systemd/system/" + nodeServiceName + ".service"
		unit = fmt.Sprintf(unit, "multi-user.target")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", append(ctl, "daemon-reload")...); err != nil {
		return err
	}
	if err := run("systemctl", append(ctl, "enable", "--now", nodeServiceName)...); err != nil {
		return err
	}
	if err := run("systemctl", append(ctl, "restart", nodeServiceName)...); err != nil {
		return err
	}
	fmt.Printf("已安装服务 / installed service %s (%s)\n", nodeServiceName, path)
	if user {
		fmt.Println("提示 / hint: run `loginctl enable-linger $USER` to keep it running after logout")
	}
	return nil
}

func installLaunchd(exe, dataDir string) error {
	label := "com.agentos.node"
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>node</string><string>--data</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, xmlEsc(exe), xmlEsc(dataDir), xmlEsc(filepath.Join(dataDir, "node.log")), xmlEsc(filepath.Join(dataDir, "node.log")))
	var path string
	if os.Geteuid() == 0 {
		path = "/Library/LaunchDaemons/" + label + ".plist"
	} else {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	exec.Command("launchctl", "unload", path).Run()
	if err := run("launchctl", "load", "-w", path); err != nil {
		return err
	}
	fmt.Printf("已安装服务 / installed launchd service %s (%s)\n", label, path)
	return nil
}

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
