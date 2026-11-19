# AgentOS runtime repair: add current interactive user to Hyper-V Administrators.
# Designed to run elevated (UAC / elev queue / scheduled task).
param(
  [string]$LogPath = $(Join-Path $env:LOCALAPPDATA 'AgentOS\hyperv-repair.log'),
  [string]$User = ""
)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $LogPath) | Out-Null
function L([string]$m) {
  $line = "{0} {1}" -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $m
  Add-Content -LiteralPath $LogPath -Value $line -Encoding UTF8
  Write-Host $line
}
try {
  $null = Get-LocalGroup -SID 'S-1-5-32-578' -ErrorAction Stop
} catch {
  L "FATAL: Hyper-V Administrators 组不存在（请先启用 Hyper-V 功能）: $($_.Exception.Message)"
  exit 2
}
if (-not $User) {
  $User = [Security.Principal.WindowsIdentity]::GetCurrent().Name
  # When launched via elev as SYSTEM/Admin, prefer the console session user
  try {
    $ex = Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue
    if ($ex -and $ex.UserName) { $User = $ex.UserName }
  } catch {}
}
L "repair user=$User"
$added = $false
foreach ($m in @($User, ($User -replace '^.*\\',''), 'Administrator', ($env:COMPUTERNAME + '\Administrator'))) {
  if (-not $m) { continue }
  try {
    Add-LocalGroupMember -SID 'S-1-5-32-578' -Member $m -ErrorAction Stop
    L "ADDED $m"; $added = $true
  } catch {
    if ($_.Exception.Message -match 'already a member|已经是|已是') { L "ALREADY $m"; $added = $true }
    else { L "FAIL $m : $($_.Exception.Message)" }
  }
}
$members = (Get-LocalGroupMember -SID 'S-1-5-32-578' | ForEach-Object Name) -join ', '
L "MEMBERS: $members"
# Soft probe
try {
  $null = Get-VMHost -ErrorAction Stop
  L "Get-VMHost OK"
} catch {
  L "Get-VMHost still failing (may need sign-out/in for new group token): $($_.Exception.Message)"
}
if (-not $added) { exit 1 }
L "DONE — 请注销并重新登录后，AgentOS 即可管理 Hyper-V（无需整机重装）"
exit 0
