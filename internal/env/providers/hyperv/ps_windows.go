//go:build windows

package hyperv

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func platformRunner() Runner {
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		exe = "powershell.exe"
	}
	return cmdRunner{exe: exe}
}

func detectPlatform(ctx context.Context) DetectResult {
	res := detectHyperVOnly(ctx)
	if res.Available {
		res.Backend = BackendHyperV
		res.HyperVOK = true
		return res
	}
	if res.NoPermission {
		res.CanRepairGroup = true
	}
	if q := detectQEMUWSL(ctx); q.Available {
		res.Available = true
		res.Reason = ""
		res.Backend = BackendQEMUWSL
		res.FallbackNote = "经 WSL QEMU"
		if q.Version != "" {
			res.Version = q.Version
		}
		return res
	}
	return res
}

func detectHyperVOnly(ctx context.Context) DetectResult {
	c, cancel := withTimeout(ctx, 12*time.Second)
	defer cancel()
	// Soft probe: Get-Command Get-VM without requiring elevation for the probe itself.
	script := `
$ErrorActionPreference = 'Stop'
try {
  $cmd = Get-Command Get-VM -ErrorAction Stop
  $feat = $null
  try { $feat = Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All -ErrorAction SilentlyContinue } catch {}
  $perm = $true; $permErr = ''
  try { $null = Get-VMHost -ErrorAction Stop } catch { $perm = $false; $permErr = $_.Exception.Message }
  $elev = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  [pscustomobject]@{
    ok = $true
    elevated = [bool]$elev
    version = $cmd.Version.ToString()
    feature = if ($feat) { $feat.State.ToString() } else { 'Unknown' }
    perm = $perm
    permError = $permErr
  } | ConvertTo-Json -Compress
} catch {
  [pscustomobject]@{ ok = $false; error = $_.Exception.Message } | ConvertTo-Json -Compress
}
`
	var res struct {
		OK       bool   `json:"ok"`
		Elevated bool   `json:"elevated"`
		Version  string `json:"version"`
		Feature  string `json:"feature"`
		Error    string `json:"error"`
		Perm     *bool  `json:"perm"`
		PermErr  string `json:"permError"`
	}
	if err := psJSON(c, script, &res); err != nil {
		// Fallback: look for vmcompute / vmconnect on PATH / System32
		for _, b := range []string{"vmcompute.exe", "vmconnect.exe"} {
			if _, e := exec.LookPath(b); e == nil {
				return DetectResult{Available: false, Reason: "检测到 Hyper-V 相关组件，但无法调用 Get-VM。请以管理员运行 AgentOS，或启用「Hyper-V」Windows 功能后重试。"}
			}
		}
		return DetectResult{Available: false, Reason: "未检测到 Hyper-V（或未启用）。请在「启用或关闭 Windows 功能」中勾选 Hyper-V，并以管理员身份运行 AgentOS。"}
	}
	if !res.OK {
		msg := res.Error
		if msg == "" {
			msg = "Get-VM 不可用"
		}
		if strings.Contains(strings.ToLower(msg), "not recognized") || strings.Contains(msg, "找不到") {
			return DetectResult{Available: false, Reason: "未启用 Hyper-V。请以管理员打开 PowerShell 执行：Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All，重启后再试。"}
		}
		return DetectResult{Available: false, Reason: "Hyper-V 探测失败：" + msg}
	}
	if res.Perm != nil && !*res.Perm {
		return DetectResult{Available: false, Elevated: res.Elevated, Version: res.Version, NoPermission: true,
			Reason: "当前账户没有 Hyper-V 管理权限（" + strings.TrimSpace(res.PermErr) + "）。请右键 AgentOS 选择「以管理员身份运行」；或将账户加入「Hyper-V Administrators」组（AgentOS 安装程序会自动加入，注销并重新登录后生效）。"}
	}
	if exp, build := buildExpiry(); expiredBuildReason(exp, build, time.Now()) != "" {
		return DetectResult{Available: false, Elevated: res.Elevated, Version: res.Version, Reason: expiredBuildReason(exp, build, time.Now())}
	}
	return DetectResult{Available: true, Elevated: res.Elevated, Version: res.Version}
}
