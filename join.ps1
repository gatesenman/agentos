<#
.SYNOPSIS
  Join this Windows machine to an AgentOS console as an Agent OS node (module M8).
.DESCRIPTION
  Downloads <Server>/download/agentos-windows-<arch>.exe (served by the console from its dist\ folder)
  and runs it as a node, or with -Install registers it as the "AgentOS" Windows service
  (auto start, restart on failure; needs an elevated PowerShell).

  irm http://<console>:7800/download/join.ps1 -OutFile join.ps1; .\join.ps1 -Server http://<console>:7800 -Token <TOKEN> -Install
  # or in one line:
  & ([scriptblock]::Create((irm http://<console>:7800/download/join.ps1))) -Server http://<console>:7800 -Token <TOKEN> -Install
.PARAMETER Server   Console URL, e.g. http://192.168.1.10:7800
.PARAMETER Token    Node join token (console Settings page)
.PARAMETER Name     Node display name (default: computer name)
.PARAMETER Install  Install as Windows service instead of running in this window
.PARAMETER Uninstall Remove the AgentOS service
.PARAMETER Dir      Install directory (default: %ProgramFiles%\AgentOS\bin when elevated, else %LOCALAPPDATA%\AgentOS\bin)
#>
param(
  [string]$Server = $env:AGENTOS_SERVER,
  [string]$Token = $env:AGENTOS_TOKEN,
  [string]$Name = $(if ($env:AGENTOS_NODE_NAME) { $env:AGENTOS_NODE_NAME } else { $env:COMPUTERNAME }),
  [switch]$Install,
  [switch]$Uninstall,
  [string]$Dir = ""
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
# bin\ like the installer (AgentOS.exe, the desktop app, may live in the parent folder)
if (-not $Dir) { $Dir = if ($admin) { Join-Path $env:ProgramFiles "AgentOS\bin" } else { Join-Path $env:LOCALAPPDATA "AgentOS\bin" } }
$exe = Join-Path $Dir "agentos.exe"
$data = if ($admin) { Join-Path $env:ProgramData "AgentOS" } else { Join-Path $env:LOCALAPPDATA "AgentOS\data" }

if ($Uninstall) {
  if (-not $admin) { throw "请以管理员身份运行 PowerShell / run as Administrator" }
  & $exe service uninstall
  Write-Host "AgentOS 节点服务已删除 (数据保留在 $data)"
  return
}
if (-not $Server -or -not $Token) { throw "需要 -Server 和 -Token 参数 / -Server and -Token are required" }
if ($Install -and -not $admin) { throw "-Install 需要以管理员身份运行 PowerShell / run as Administrator" }
$Server = $Server.TrimEnd("/")

$arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($arch) { "AMD64" { "amd64" } "ARM64" { "arm64" } default { throw "不支持的 CPU 架构: $arch" } }
$url = "$Server/download/agentos-windows-$arch.exe"

New-Item -ItemType Directory -Force -Path $Dir, $data | Out-Null
if ($Install -and (Get-Service -Name AgentOS -ErrorAction SilentlyContinue)) {
  Write-Host ">> 停止现有 AgentOS 服务"
  & $exe service stop 2>$null | Out-Null
}
$tmp = "$exe.download"
Write-Host ">> 下载 $url"
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
$ver = & $tmp version 2>&1
if ($LASTEXITCODE -ne 0) { Remove-Item $tmp -Force; throw "下载的文件不是有效的 agentos 程序: $ver" }
Move-Item -Force $tmp $exe
Write-Host ">> $ver"

if ($Install) {
  & $exe service install --mode node --server $Server --token $Token --name $Name --data $data
  if ($LASTEXITCODE -ne 0) { throw "服务注册失败" }
  & $exe service start
  if ($LASTEXITCODE -ne 0) { throw "服务启动失败, 详见 $data\logs\service.log" }
  Write-Host ">> 节点 '$Name' 已作为 Windows 服务运行并加入 $Server"
} else {
  Write-Host ">> 启动节点 (Ctrl+C 停止); 使用 -Install 安装为 Windows 服务"
  & $exe node --server $Server --token $Token --name $Name --data (Join-Path $data "node")
}
