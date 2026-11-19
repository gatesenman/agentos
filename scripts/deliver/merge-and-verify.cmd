@echo off
setlocal EnableExtensions
REM Merge chunked AgentOS delivery parts and verify SHA256.
REM Expects: %DEST%\_stage\parts\{Setup,Source,Portable}.part.*
REM          %DEST%\_stage\{SHA256SUMS,join.ps1,join.sh,verify-sha.ps1[,README-*.md]}
REM Optional: set VER=0.1.7 before calling (default below).
if not defined VER set VER=0.1.7
if not defined DEST set DEST=C:\Users\Administrator\GrokBot-Apps\AgentOS
set STAGE=%DEST%\_stage
cd /d "%STAGE%\parts" || (
  echo FAIL cannot cd to %STAGE%\parts
  exit /b 1
)

if exist "%DEST%\AgentOS-Setup-%VER%.exe" del /f "%DEST%\AgentOS-Setup-%VER%.exe"
if exist "%DEST%\AgentOS-source-%VER%.zip" del /f "%DEST%\AgentOS-source-%VER%.zip"
if exist "%DEST%\agentos-%VER%-windows-amd64.zip" del /f "%DEST%\agentos-%VER%-windows-amd64.zip"

copy /b Setup.part.* "%DEST%\AgentOS-Setup-%VER%.exe" || exit /b 1
copy /b Source.part.* "%DEST%\AgentOS-source-%VER%.zip" || exit /b 1
copy /b Portable.part.* "%DEST%\agentos-%VER%-windows-amd64.zip" || exit /b 1
copy /y "%STAGE%\SHA256SUMS" "%DEST%\SHA256SUMS" >nul
copy /y "%STAGE%\join.ps1" "%DEST%\join.ps1" >nul
copy /y "%STAGE%\join.sh" "%DEST%\join.sh" >nul
if exist "%STAGE%\README-交付说明.md" copy /y "%STAGE%\README-交付说明.md" "%DEST%\README-交付说明.md" >nul
if exist "%STAGE%\verify-sha.ps1" copy /y "%STAGE%\verify-sha.ps1" "%DEST%\verify-sha.ps1" >nul

REM latest-only: remove other 0.1.x setup/source/portable
for %%F in ("%DEST%\AgentOS-Setup-0.1.*.exe") do if /I not "%%~nxF"=="AgentOS-Setup-%VER%.exe" del /f "%%F"
for %%F in ("%DEST%\AgentOS-source-0.1.*.zip") do if /I not "%%~nxF"=="AgentOS-source-%VER%.zip" del /f "%%F"
for %%F in ("%DEST%\agentos-0.1.*-windows-amd64.zip") do if /I not "%%~nxF"=="agentos-%VER%-windows-amd64.zip" del /f "%%F"

echo === SHA256 verify ===
REM Must call a .ps1 file — inline -Command with caret-escaping emptied $h (got=).
set VPS=%STAGE%\verify-sha.ps1
if not exist "%VPS%" set VPS=%~dp0verify-sha.ps1
if not exist "%VPS%" set VPS=%DEST%\verify-sha.ps1
if not exist "%VPS%" (
  echo FAIL verify-sha.ps1 not found
  exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%VPS%" -Dest "%DEST%"
if errorlevel 1 exit /b 1
dir "%DEST%\AgentOS-Setup-%VER%.exe" "%DEST%\AgentOS-source-%VER%.zip" "%DEST%\agentos-%VER%-windows-amd64.zip"
echo ALL OK
