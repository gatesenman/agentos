# Companion for merge-and-verify.cmd.
# Avoids cmd.exe caret-escaping of nested PowerShell that previously emptied $h
# (symptom: "FAIL … got= expect=<hash>").
param(
  [Parameter(Mandatory = $true)][string]$Dest,
  [string]$Sums = '',
  [string[]]$Names = @()
)
$ErrorActionPreference = 'Stop'
if (-not $Sums) { $Sums = Join-Path $Dest 'SHA256SUMS' }
if (-not (Test-Path -LiteralPath $Sums)) {
  Write-Host "FAIL missing $Sums"
  exit 1
}
$lines = Get-Content -LiteralPath $Sums
if ($Names.Count -eq 0) {
  # Every non-comment line: "<hash>  <filename>" (GNU sha256sum / coreutils style).
  $Names = @(
    $lines |
      Where-Object { $_ -match '^\s*[0-9a-fA-F]{64}\s+\S+' } |
      ForEach-Object { ($_ -replace '^\s*[0-9a-fA-F]{64}\s+\*?', '').Trim() } |
      Where-Object { $_ -and ($_ -notmatch '[\\/]') }  # skip paths with dirs
  )
}
if ($Names.Count -eq 0) {
  Write-Host 'FAIL SHA256SUMS has no file entries'
  exit 1
}
$fail = 0
foreach ($n in $Names) {
  $path = Join-Path $Dest $n
  if (-not (Test-Path -LiteralPath $path)) {
    Write-Host "FAIL $n missing"
    $fail = 1
    continue
  }
  $h = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
  $expLine = $lines | Where-Object { $_ -match [regex]::Escape($n) + '\s*$' } | Select-Object -First 1
  if (-not $expLine) {
    Write-Host "FAIL $n no entry in SHA256SUMS"
    $fail = 1
    continue
  }
  $exp = (($expLine -split '\s+')[0]).ToLowerInvariant()
  if ($h -ne $exp) {
    Write-Host "FAIL $n got=$h expect=$exp"
    $fail = 1
  } else {
    Write-Host "OK $n"
  }
}
exit $fail
