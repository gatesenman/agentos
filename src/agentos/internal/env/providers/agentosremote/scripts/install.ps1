# AgentOS node installer for Windows (PowerShell):
#   $env:AGENTOS_SERVER='http://host:7800'; $env:AGENTOS_TOKEN='aosj_...'; $env:AGENTOS_NODE_NAME='PC-1'; irm http://host:7800/download/install.ps1 | iex
# Optional $env:AGENTOS_INSTALL_SERVICE='1': install a logon task (runs in the
# user session so the remote desktop can capture the screen) and return.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$server = "$env:AGENTOS_SERVER".TrimEnd('/')
$token = "$env:AGENTOS_TOKEN"
$name = "$env:AGENTOS_NODE_NAME"
if (-not $server -or -not $token) { Write-Error 'need AGENTOS_SERVER and AGENTOS_TOKEN / 需要 AGENTOS_SERVER 和 AGENTOS_TOKEN'; return }
$root = Join-Path $env:LOCALAPPDATA 'AgentOS'
$data = Join-Path $root 'node'
$exe = Join-Path $root 'agentos.exe'
New-Item -ItemType Directory -Force -Path $data | Out-Null
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
Get-Process -Name agentos -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe } | Stop-Process -Force -ErrorAction SilentlyContinue
$tmp = "$exe.download"
try {
  Write-Host ">> 下载 / downloading $server/download/agentos-windows-$arch.exe"
  Invoke-WebRequest -UseBasicParsing -Uri "$server/download/agentos-windows-$arch.exe" -OutFile $tmp
} catch {
  if ($arch -eq 'amd64') { throw }
  Write-Host ">> arm64 build not found, using amd64"
  Invoke-WebRequest -UseBasicParsing -Uri "$server/download/agentos-windows-amd64.exe" -OutFile $tmp
}
Move-Item -Force $tmp $exe
$a = @('node', '--server', $server, '--token', $token, '--data', $data)
if ($name) { $a += @('--name', $name) }
if ("$env:AGENTOS_INSTALL_SERVICE" -eq '1') {
  Write-Host ">> 安装为登录启动任务 / installing logon task"
  & $exe @a '--install-service'
  return
}
Write-Host ">> 启动 / starting: $exe node --data $data"
& $exe @a
