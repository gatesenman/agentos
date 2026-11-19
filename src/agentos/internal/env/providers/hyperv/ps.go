package hyperv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes PowerShell scripts (injectable for tests).
type Runner interface {
	// RunPS runs powershell -NoProfile -NonInteractive -Command script and returns stdout.
	RunPS(ctx context.Context, script string) (stdout string, err error)
}

// defaultRunner is set per-OS in ps_windows.go / ps_stub.go.
var defaultRunner Runner

// SetRunner replaces the PowerShell runner (tests). Pass nil to restore default.
func SetRunner(r Runner) {
	if r == nil {
		defaultRunner = platformRunner()
		return
	}
	defaultRunner = r
}

func runner() Runner {
	if defaultRunner == nil {
		defaultRunner = platformRunner()
	}
	return defaultRunner
}

// DetectResult is the Hyper-V availability probe (plus optional QEMU/WSL fallback).
type DetectResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"` // zh tip when unavailable
	Elevated  bool   `json:"elevated,omitempty"`
	Version   string `json:"version,omitempty"`
	// NoPermission: Hyper-V is installed but this process may not manage it.
	NoPermission bool `json:"no_permission,omitempty"`
	// 0.1.9
	Backend        string `json:"backend,omitempty"`          // hyperv | qemu-wsl
	FallbackNote   string `json:"fallback_note,omitempty"`    // e.g. 经 WSL QEMU
	CanRepairGroup bool   `json:"can_repair_group,omitempty"` // offer Hyper-V Administrators repair
	HyperVOK       bool   `json:"hyperv_ok,omitempty"`        // true only when Hyper-V Start-VM path is usable
}

// Detect probes Hyper-V cmdlets. Safe on Linux (returns unavailable).
func Detect(ctx context.Context) DetectResult {
	return detectPlatform(ctx)
}

// Available is a short form used by presets.
func Available(ctx context.Context) (bool, string) {
	d := Detect(ctx)
	return d.Available, d.Reason
}

type psVM struct {
	Name           string `json:"Name"`
	Id             string `json:"Id"`
	State          string `json:"State"`
	Status         string `json:"Status"`
	CPUUsage       int    `json:"CPUUsage"`
	MemoryAssigned int64  `json:"MemoryAssigned"`
	Uptime         string `json:"Uptime"`
	Version        string `json:"Version"`
}

func psJSON(ctx context.Context, script string, out any) error {
	stdout, err := runner().RunPS(ctx, script)
	if err != nil {
		return err
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" || stdout == "null" {
		return fmt.Errorf("hyperv: PowerShell 无输出")
	}
	return json.Unmarshal([]byte(stdout), out)
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// cmdRunner runs real powershell.exe (Windows).
type cmdRunner struct {
	exe string
}

func (c cmdRunner) RunPS(ctx context.Context, script string) (string, error) {
	if c.exe == "" {
		c.exe = "powershell.exe"
	}
	// Prefer -EncodedCommand-free simple -Command for short scripts.
	// Force UTF-8 on the pipe so localized (e.g. GBK) error messages survive.
	script = psPrelude + script
	cmd := exec.CommandContext(ctx, c.exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("hyperv: PowerShell 失败: %s", cleanPSError(msg))
	}
	return stdout.String(), nil
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

const psPrelude = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $OutputEncoding=[Text.Encoding]::UTF8; "

// cleanPSError drops the PowerShell position/category noise after the
// actual message ("所在位置 行:1 ..." / "At line:1 ...").
func cleanPSError(msg string) string {
	lines := strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n")
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "所在位置") || strings.HasPrefix(t, "At line:") || strings.HasPrefix(t, "At ") && strings.Contains(t, " char:") {
			break
		}
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return strings.TrimSpace(msg)
	}
	return strings.Join(out, " ")
}
