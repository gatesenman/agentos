package stacks

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// posixPath prepends the per-user install locations used by the installers in the
// catalog (uv, rustup, bun, deno, dotnet-install, Go tarball, Homebrew) so detection
// works right after installation without a new login shell.
const posixPath = `export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$HOME/.bun/bin:$HOME/.deno/bin:$HOME/.dotnet:/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"`

// posixPreamble is prepended to every POSIX setup step. $SUDO is empty for root and
// "sudo -n ..." otherwise (non-interactive: fails fast instead of prompting).
const posixPreamble = `set -e
` + posixPath + `
export DEBIAN_FRONTEND=noninteractive
SUDO=""
if [ "$(id -u)" != "0" ] && command -v sudo >/dev/null 2>&1; then SUDO="sudo -n env DEBIAN_FRONTEND=noninteractive"; fi
ensure_uv() {
  command -v uv >/dev/null 2>&1 && return 0
  if ! command -v curl >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then $SUDO apt-get update -y && $SUDO apt-get install -y --no-install-recommends curl ca-certificates
    elif command -v apk >/dev/null 2>&1; then $SUDO apk add --no-cache curl ca-certificates
    elif command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y curl; fi
  fi
  curl -LsSf https://astral.sh/uv/install.sh | sh
}
npm_global() {
  p="$(npm prefix -g)"
  if [ -w "$p/lib/node_modules" ] || { [ ! -e "$p/lib/node_modules" ] && [ -w "$p" ]; }; then npm install -g "$@"; else $SUDO npm install -g "$@"; fi
}
`

// windowsPreamble is prepended to every Windows (PowerShell 5.1+) setup step.
const windowsPreamble = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}
function Refresh-Path {
  $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User') + ";$env:USERPROFILE\.local\bin;$env:USERPROFILE\.cargo\bin;$env:USERPROFILE\.bun\bin;$env:USERPROFILE\.deno\bin;$env:LOCALAPPDATA\Microsoft\dotnet;$env:APPDATA\npm"
}
Refresh-Path
function Try-Winget([string]$id) {
  if (-not (Get-Command winget -ErrorAction SilentlyContinue)) { Write-Host "winget 不可用"; return $false }
  winget install --id $id -e --silent --accept-package-agreements --accept-source-agreements --disable-interactivity | Out-Host
  # 0x8A15002B no applicable update, 0x8A150061 already installed
  $ok = ($LASTEXITCODE -eq 0 -or $LASTEXITCODE -eq -1978335189 -or $LASTEXITCODE -eq -1978335135)
  $global:LASTEXITCODE = 0
  Refresh-Path
  return $ok
}
function Try-Choco([string]$id) {
  if (-not (Get-Command choco -ErrorAction SilentlyContinue)) { Write-Host "choco 不可用"; return $false }
  choco install $id -y --no-progress | Out-Host
  $ok = ($LASTEXITCODE -eq 0)
  $global:LASTEXITCODE = 0
  Refresh-Path
  return $ok
}
function Install-Pkg([string]$wingetId, [string]$chocoId) {
  if (Try-Winget $wingetId) { return }
  if ($chocoId -and (Try-Choco $chocoId)) { return }
  throw "无法安装 ${wingetId}：winget 与 choco 均不可用或安装失败"
}
function Ensure-Uv {
  if (Get-Command uv -ErrorAction SilentlyContinue) { return }
  if (-not (Try-Winget 'astral-sh.uv')) { Invoke-RestMethod https://astral.sh/uv/install.ps1 | Invoke-Expression }
  Refresh-Path
}
`

const windowsDetectPreamble = `$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}
$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User') + ";$env:USERPROFILE\.local\bin;$env:USERPROFILE\.cargo\bin;$env:USERPROFILE\.bun\bin;$env:USERPROFILE\.deno\bin;$env:LOCALAPPDATA\Microsoft\dotnet;$env:APPDATA\npm"
`

func windowsWrap(preamble, script string) string {
	return preamble + "try {\n" + script + "\n} catch { [Console]::Error.WriteLine('错误: ' + $_.Exception.Message); exit 1 }\nif ($LASTEXITCODE) { exit $LASTEXITCODE }\nexit 0\n"
}

var psArgs = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"}

// StepArgv is the human-readable argv for a setup step (used in the GET /stacks view).
func StepArgv(family, script string) []string {
	if family == FamilyWindows {
		return append(append([]string{}, psArgs...), "-Command", windowsWrap(windowsPreamble, script))
	}
	return []string{"sh", "-c", posixPreamble + script}
}

// DetectArgv is the human-readable argv for a detect script.
func DetectArgv(family, script string) []string {
	if family == FamilyWindows {
		return append(append([]string{}, psArgs...), "-Command", windowsWrap(windowsDetectPreamble, script))
	}
	return []string{"sh", "-c", posixPath + "\n" + script}
}

// execArgv converts a readable argv into the form actually executed: on Windows the
// script is passed as -EncodedCommand (UTF-16LE base64) so quoting and non-ASCII text
// survive any transport (local CreateProcess, ssh, agent tunnel).
func execArgv(argv []string) []string {
	if len(argv) >= 2 && argv[0] == "powershell.exe" && argv[len(argv)-2] == "-Command" {
		out := append([]string{}, argv[:len(argv)-2]...)
		return append(out, "-EncodedCommand", encodePS(argv[len(argv)-1]))
	}
	return argv
}

func encodePS(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodePS reverses encodePS (tests, logging).
func decodePS(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%2 != 0 {
		return ""
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// scriptOf extracts the script text from an executed argv (for logs/tests).
func scriptOf(argv []string) string {
	if len(argv) >= 2 && argv[len(argv)-2] == "-EncodedCommand" {
		return decodePS(argv[len(argv)-1])
	}
	if len(argv) > 0 {
		return argv[len(argv)-1]
	}
	return ""
}

// summarize returns the first meaningful line of a step script for log messages.
func summarize(script string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(script), "\n", 2)[0])
	if len(line) > 160 {
		line = line[:157] + "..."
	}
	return line
}
