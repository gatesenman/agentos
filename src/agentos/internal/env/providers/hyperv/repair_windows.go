//go:build windows

package hyperv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func repairHyperVGroupPlatform(ctx context.Context) RepairGroupResult {
	logDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "AgentOS")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, "hyperv-repair.log")

	script := FindRepairScript()
	if script == "" {
		inline := `
$ErrorActionPreference = 'Stop'
$log = $env:AGENTOS_REPAIR_LOG
if (-not $log) { $log = Join-Path $env:LOCALAPPDATA 'AgentOS\hyperv-repair.log' }
function L($m){ Add-Content -LiteralPath $log -Value ("{0} {1}" -f (Get-Date -Format o), $m) -Encoding UTF8 }
try { $null = Get-LocalGroup -SID 'S-1-5-32-578' } catch { L "no group"; exit 2 }
$user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
try { $cs = Get-CimInstance Win32_ComputerSystem; if ($cs.UserName) { $user = $cs.UserName } } catch {}
L "user=$user"
$ok = $false
foreach ($m in @($user, ($user -replace '^.*\\',''), 'Administrator', ($env:COMPUTERNAME + '\Administrator'))) {
  if (-not $m) { continue }
  try { Add-LocalGroupMember -SID 'S-1-5-32-578' -Member $m -ErrorAction Stop; L "ADDED $m"; $ok = $true }
  catch { if ($_.Exception.Message -match 'already|已经|已是') { L "ALREADY $m"; $ok = $true } else { L "FAIL $m $($_.Exception.Message)" } }
}
exit $(if ($ok) { 0 } else { 1 })
`
		tmp := filepath.Join(os.TempDir(), "agentos-hyperv-repair-inline.ps1")
		_ = os.WriteFile(tmp, []byte(inline), 0o644)
		script = tmp
	}

	// 1) Prefer E2E elev queue when present and runner is alive (fast path for labs).
	elevDir := `D:\AgentOS-E2E`
	if st, err := os.Stat(filepath.Join(elevDir, "elev.ps1")); err == nil && !st.IsDir() {
		job := fmt.Sprintf("j%x", time.Now().UnixNano()&0xffffff)
		jobPS1 := filepath.Join(elevDir, "elev", job+".ps1")
		_ = os.MkdirAll(filepath.Dir(jobPS1), 0o755)
		body := fmt.Sprintf(`$env:AGENTOS_REPAIR_LOG = '%s'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File '%s' -LogPath '%s'
$code = $LASTEXITCODE
Set-Content -LiteralPath '%s' -Value ("exit=$code") -Encoding ASCII
`, strings.ReplaceAll(logPath, `'`, `''`), strings.ReplaceAll(script, `'`, `''`), strings.ReplaceAll(logPath, `'`, `''`),
			strings.ReplaceAll(filepath.Join(elevDir, "elev", job+".done"), `'`, `''`))
		_ = os.WriteFile(jobPS1, []byte(body), 0o644)
		_ = os.WriteFile(filepath.Join(elevDir, "elev", job+".req"), []byte("repair-hyperv-group\n"), 0o644)
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return RepairGroupResult{OK: false, Message: "修复被取消", LogPath: logPath}
			default:
			}
			done := filepath.Join(elevDir, "elev", job+".done")
			if b, err := os.ReadFile(done); err == nil {
				ok := strings.Contains(string(b), "exit=0") || strings.TrimSpace(string(b)) == "0"
				msg := "提权修复未完全成功，详见日志。"
				if ok {
					msg = "已将当前用户加入「Hyper-V Administrators」。请注销并重新登录后生效（无需重装 AgentOS）。"
				}
				return RepairGroupResult{OK: ok, Message: msg, LogPath: logPath, NeedRelogin: ok, Elevated: true}
			}
			time.Sleep(500 * time.Millisecond)
		}
		// fall through to scheduled task if elev-runner did not pick up
	}

	// 2) One-shot elevated Scheduled Task (SYSTEM / Highest) — works from non-elevated tray.
	if res, handled := repairViaSchtasks(ctx, script, logPath); handled {
		return res
	}

	// 3) UAC prompt via Start-Process -Verb RunAs
	ps := fmt.Sprintf(`
$ErrorActionPreference = 'Continue'
$p = Start-Process -FilePath powershell.exe -Verb RunAs -Wait -PassThru -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File','%s','-LogPath','%s')
if ($null -eq $p) { 'FAIL:UAC' } elseif ($p.ExitCode -eq 0) { 'OK' } else { "FAIL:$($p.ExitCode)" }
`, strings.ReplaceAll(script, `'`, `''`), strings.ReplaceAll(logPath, `'`, `''`))
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(c, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err == nil && strings.Contains(s, "OK") {
		return RepairGroupResult{
			OK: true, NeedRelogin: true, Elevated: true, LogPath: logPath,
			Message: "已将当前用户加入「Hyper-V Administrators」。请注销并重新登录后生效（无需重装）。",
		}
	}

	// 4) Non-elevated last resort
	cmd2 := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", script, "-LogPath", logPath)
	if err2 := cmd2.Run(); err2 == nil {
		return RepairGroupResult{
			OK: true, NeedRelogin: true, LogPath: logPath,
			Message: "组成员已更新。请注销并重新登录后生效。",
		}
	}
	return RepairGroupResult{
		OK: false, LogPath: logPath,
		Message: "无法自动提权修复 Hyper-V 组。请右键「以管理员身份运行」AgentOS 后在设置中重试，或手动执行：Add-LocalGroupMember -Group 'Hyper-V Administrators' -Member $env:USERNAME。详情：" + truncate(err.Error()+" "+s, 200),
	}
}

// repairViaSchtasks registers a one-shot SYSTEM Highest task, starts it, waits for log/done.
func repairViaSchtasks(ctx context.Context, script, logPath string) (RepairGroupResult, bool) {
	taskName := fmt.Sprintf("AgentOS-HyperV-Repair-%d", time.Now().Unix()%100000)
	workDir := filepath.Join(os.TempDir(), "agentos-hyperv-repair")
	_ = os.MkdirAll(workDir, 0o755)
	wrapper := filepath.Join(workDir, taskName+".ps1")
	doneFile := filepath.Join(workDir, taskName+".done")
	_ = os.Remove(doneFile)
	body := fmt.Sprintf(`$ErrorActionPreference = 'Continue'
$env:AGENTOS_REPAIR_LOG = '%s'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File '%s' -LogPath '%s'
$code = $LASTEXITCODE
Set-Content -LiteralPath '%s' -Value ("exit=$code") -Encoding ASCII
`, strings.ReplaceAll(logPath, `'`, `''`), strings.ReplaceAll(script, `'`, `''`),
		strings.ReplaceAll(logPath, `'`, `''`), strings.ReplaceAll(doneFile, `'`, `''`))
	if err := os.WriteFile(wrapper, []byte(body), 0o644); err != nil {
		return RepairGroupResult{}, false
	}

	ps := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$tn = '%s'
Unregister-ScheduledTask -TaskName $tn -Confirm:$false -ErrorAction SilentlyContinue
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument '-NoProfile -ExecutionPolicy Bypass -File "%s"'
$principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
Register-ScheduledTask -TaskName $tn -Action $action -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $tn
'OK'
`, strings.ReplaceAll(taskName, `'`, `''`), strings.ReplaceAll(wrapper, `"`, `\"`))

	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		// Creating a SYSTEM task from a non-admin session often fails — caller falls through.
		return RepairGroupResult{}, false
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			cleanupSchtask(taskName)
			return RepairGroupResult{OK: false, Message: "修复被取消", LogPath: logPath}, true
		default:
		}
		if b, err := os.ReadFile(doneFile); err == nil {
			ok := strings.Contains(string(b), "exit=0")
			cleanupSchtask(taskName)
			msg := "提权修复未完全成功，详见日志 " + logPath
			if ok {
				msg = "已将当前用户加入「Hyper-V Administrators」。请注销并重新登录后生效（无需重装 AgentOS）。"
			}
			return RepairGroupResult{OK: ok, Message: msg, LogPath: logPath, NeedRelogin: ok, Elevated: true}, true
		}
		time.Sleep(1 * time.Second)
	}
	cleanupSchtask(taskName)
	return RepairGroupResult{
		OK: false, LogPath: logPath, Elevated: true,
		Message: "已启动提权计划任务，但 90 秒内未完成。请查看日志，或以管理员手动执行修复脚本。",
	}, true
}

func cleanupSchtask(name string) {
	exec.Command("powershell.exe", "-NoProfile", "-Command",
		fmt.Sprintf("Unregister-ScheduledTask -TaskName '%s' -Confirm:$false -ErrorAction SilentlyContinue", strings.ReplaceAll(name, `'`, `''`))).Run()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
