# AgentOS: add installing user + local Administrator to Hyper-V Administrators.
# Called elevated by the NSIS installer (0.1.9+). Logs every attempt.
param(
  [string]$LogPath = "$env:TEMP\agentos-hyperv-group.log",
  [string]$InstallerUser = ""
)
$ErrorActionPreference = 'Continue'
function L([string]$m) {
  $line = "{0} {1}" -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $m
  Add-Content -LiteralPath $LogPath -Value $line -Encoding UTF8 -ErrorAction SilentlyContinue
  Write-Host $line
}
try {
  $g = Get-LocalGroup -SID 'S-1-5-32-578' -ErrorAction Stop
} catch {
  L "SKIP: Hyper-V Administrators group missing ($($_.Exception.Message))"
  exit 0
}
L "group=$($g.Name) sid=$($g.SID)"
$targets = New-Object System.Collections.Generic.List[string]
if ($InstallerUser -and $InstallerUser -notmatch '^[\\]*$') {
  $targets.Add($InstallerUser.Trim())
}
# NSIS may pass DOMAIN\user; also try bare USERNAME / USERDOMAIN\USERNAME from env
foreach ($c in @(
  $env:USERDOMAIN + '\' + $env:USERNAME,
  $env:USERNAME,
  [Security.Principal.WindowsIdentity]::GetCurrent().Name
)) {
  if ($c -and $c -notmatch '^[\\]*$' -and -not $targets.Contains($c)) { $targets.Add($c) }
}
# Well-known local Administrator (RID 500)
try {
  $admin = Get-LocalUser | Where-Object { $_.SID.Value -match '-500$' } | Select-Object -First 1
  if ($admin) {
    $n = $env:COMPUTERNAME + '\' + $admin.Name
    if (-not $targets.Contains($n)) { $targets.Add($n) }
    if (-not $targets.Contains($admin.Name)) { $targets.Add($admin.Name) }
  }
} catch { L "WARN: resolve RID-500: $($_.Exception.Message)" }

$ok = 0; $fail = 0
foreach ($m in $targets) {
  try {
    Add-LocalGroupMember -SID 'S-1-5-32-578' -Member $m -ErrorAction Stop
    L "ADDED $m"; $ok++
  } catch {
    $msg = $_.Exception.Message
    if ($msg -match 'already a member|已经是|已是.*成员') { L "ALREADY $m"; $ok++ }
    else { L "FAIL $m : $msg"; $fail++ }
  }
}
try {
  $members = (Get-LocalGroupMember -SID 'S-1-5-32-578' -ErrorAction Stop | ForEach-Object { $_.Name }) -join ', '
  L "MEMBERS: $members"
} catch { L "WARN: list members: $($_.Exception.Message)" }
if ($ok -eq 0) { L "ERROR: no member added"; exit 1 }
L "DONE ok=$ok fail=$fail (sign out/in required for new token groups)"
exit 0
