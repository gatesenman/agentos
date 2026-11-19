#!/usr/bin/env bash
# Wine smoke test for the Windows build (module M8). Needs wine (64-bit + 32-bit for the NSIS stub).
#   scripts/wine-smoke.sh            (expects dist/ from "make dist"; env WINEPREFIX to reuse a prefix)
# Steps: agentos.exe version / server; installer /S (server mode, /SERVICE, custom /PORT) -> files,
# config, shortcuts, service running + health; in-place upgrade (service kept); uninstall; installer
# /S /MODE=node joining a native Linux server as a Windows service; silent uninstall /PURGE.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${VERSION:-0.1.0}
SETUP="dist/AgentOS-Setup-$VERSION.exe"
command -v wine >/dev/null || { echo "wine not installed - skipping"; exit 0; }
[ -f "$SETUP" ] || { echo "missing $SETUP (run make dist)"; exit 1; }
export WINEDEBUG=-all WINEDLLOVERRIDES="mscoree,mshtml="
FRESH=0
if [ -z "${WINEPREFIX:-}" ]; then export WINEPREFIX=$(mktemp -d /tmp/agentos-wine.XXXX); FRESH=1; fi
C="$WINEPREFIX/drive_c"
INST="$C/Program Files/AgentOS"
DATA="$C/ProgramData/AgentOS"
EXE='C:\Program Files\AgentOS\bin\agentos-server.exe'
UNINST='C:\Program Files\AgentOS\uninstall.exe'
conf() { python3 -c 'import sys;b=open(sys.argv[1],"rb").read();print(b[2:].decode("utf-16-le") if b[:2]==b"\xff\xfe" else b.decode("utf-8-sig"))' "$DATA/agentos.conf"; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; exit 1; }
freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
health() { curl -fsS --max-time 2 "http://127.0.0.1:$1/api/v1/health" 2>/dev/null; }
waitfor() { local d=$1; shift; for _ in $(seq $((d*2))); do "$@" >/dev/null 2>&1 && return 0; sleep 0.5; done; return 1; }
# Wine's SCM is sometimes busy right after (re)registration: retry a start (real Windows does not need this)
ensure_running() {
  st=$(timeout 30 wine "$EXE" service status | tr -d '\r')
  for _ in 1 2 3 4 5 6; do
    [ "$st" = running ] && return 0
    echo "  (service status '$st', starting it)"; timeout 90 wine "$EXE" service start || true; sleep 5
    st=$(timeout 30 wine "$EXE" service status | tr -d '\r')
  done
}
LINUX_PID=""
cleanup() {
  [ -n "$LINUX_PID" ] && kill "$LINUX_PID" 2>/dev/null || true
  wineserver -k 2>/dev/null || true
  [ $FRESH = 1 ] && rm -rf "$WINEPREFIX"
}
trap cleanup EXIT

echo ">> wine $(wine --version), prefix $WINEPREFIX"
timeout 300 wineboot -i >/dev/null 2>&1 || true
wineserver -p 900 || true   # keep services alive between wine invocations

echo ">> 1. portable exe"
out=$(timeout 60 wine dist/agentos-windows-amd64.exe version | tr -d '\r')
[[ "$out" == "agentos $VERSION"*"windows/amd64" ]] && pass "version: $out" || fail "version: $out"
P=$(freeport)
timeout 60 wine dist/agentos-windows-amd64.exe server --listen "127.0.0.1:$P" --data 'C:\agentos-smoke' --admin-password smoke123 >/tmp/wine-server.log 2>&1 &
SP=$!
waitfor 40 health "$P" && pass "server health on :$P ($(health $P))" || { cat /tmp/wine-server.log; fail "server did not start"; }
waitfor 20 grep -q "node: connected" /tmp/wine-server.log && pass "embedded node connected" || fail "embedded node"
kill $SP 2>/dev/null; wait $SP 2>/dev/null || true
pkill -f 'agentos-windows-amd64.exe server' 2>/dev/null || true

echo ">> 2. silent install (/MODE=console, service, /PORT)"
P=$(freeport)
timeout 900 wine "$SETUP" /S /MODE=console /SERVICE "/PORT=$P" /NOWEBVIEW2 || fail "installer exit $?"
for f in bin/agentos-server.exe bin/agentos-tray.exe agentos.ico uninstall.exe bin/dist/agentos-linux-amd64 bin/dist/agentos-linux-arm64 \
         bin/dist/agentos-darwin-arm64 bin/dist/agentos-darwin-amd64 bin/dist/agentos-windows-amd64.exe bin/dist/join.ps1 bin/dist/join.sh; do
  [ -f "$INST/$f" ] || fail "missing $f"
done
if [ -d cmd/agentos-desktop ]; then
  [ -f "$INST/AgentOS.exe" ] && pass "desktop app AgentOS.exe installed" || fail "AgentOS.exe missing"
  lnk="$C/ProgramData/Microsoft/Windows/Start Menu/Programs/AgentOS/AgentOS.lnk"
  python3 -c 'import sys;b=open(sys.argv[1],"rb").read();sys.exit(0 if b"AgentOS.exe" in b or "AgentOS.exe".encode("utf-16-le") in b else 1)' "$lnk" \
    && pass "start menu shortcut -> AgentOS.exe" || fail "shortcut does not launch AgentOS.exe"
fi
pass "files installed (bin\\agentos.exe, bin\\dist\\ for /download)"
[ -f "$C/ProgramData/Microsoft/Windows/Start Menu/Programs/AgentOS/AgentOS.lnk" ] && pass "start menu shortcut" || fail "start menu shortcut"
ls "$C/users/Public/Desktop/AgentOS.lnk" >/dev/null 2>&1 && pass "desktop shortcut" || fail "desktop shortcut"
conf | grep -q "listen=:$P" && pass "agentos.conf written (mode=console, :$P)" || fail "config"
ensure_running
[ "$st" = running ] && pass "service status: running" || { cat "$DATA/logs/service.log" || true; fail "service status: $st"; }
waitfor 30 health "$P" && pass "service health on :$P" || { cat "$DATA/logs/service.log"; fail "service health"; }

echo ">> 3. in-place upgrade (re-run installer silently)"
timeout 900 wine "$SETUP" /S /NOWEBVIEW2 || fail "upgrade exit"
ensure_running
[ "$st" = running ] && pass "service kept + restarted after upgrade" || fail "after upgrade: $st"
conf | grep -q "listen=:$P" && pass "port kept on upgrade" || fail "port lost on upgrade"
waitfor 30 health "$P" && pass "health after upgrade" || fail "health after upgrade"
# the uninstaller re-launches itself from %TEMP% and returns at once: poll for the result
timeout 400 wine "$UNINST" /S || true
waitfor 180 test ! -f "$INST/bin/agentos-server.exe" || true
st=$(timeout 30 wine dist/agentos-windows-amd64.exe service status | tr -d '\r')
[ "$st" = not-installed ] && pass "uninstall removed service" || fail "service after uninstall: $st"
[ ! -f "$INST/bin/agentos-server.exe" ] && pass "uninstall removed files" || { ls -la "$INST"; fail "files left"; }
[ -d "$DATA/server" ] && pass "data kept without /PURGE"

echo ">> 4. node mode: Windows node (service) joins a native Linux console"
LP=$(freeport); LD=$(mktemp -d)
dist/agentos-linux-amd64 server --listen "127.0.0.1:$LP" --data "$LD" --admin-password smoke123 --with-node=false >/tmp/linux-server.log 2>&1 &
LINUX_PID=$!
waitfor 20 health "$LP" || fail "linux server"
TOK=$(curl -fsS -X POST -H 'Content-Type: application/json' -d '{"password":"smoke123"}' "http://127.0.0.1:$LP/api/v1/auth/login" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
NT=$(curl -fsS -H "Authorization: Bearer $TOK" "http://127.0.0.1:$LP/api/v1/settings/node-token" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')
rc=0; timeout 900 wine "$SETUP" /S /MODE=node "/SERVER=http://127.0.0.1:$LP" "/TOKEN=$NT" /NAME=wine-node || rc=$?
[ $rc = 0 ] || fail "node installer exit $rc"
[ ! -d "$INST/bin/dist" ] && pass "node install has no dist/"
ensure_running; [ "$st" = running ] && pass "node service running" || fail "node service: $st"
conf | grep -q "mode=node" && pass "config mode=node" || fail "node config"
nodes() { curl -fsS -H "Authorization: Bearer $TOK" "http://127.0.0.1:$LP/api/v1/nodes"; }
online() { nodes | python3 -c 'import json,sys;sys.exit(0 if any(n["name"]=="wine-node" and n["status"]=="online" and n["os"]=="windows" for n in json.load(sys.stdin)) else 1)'; }
waitfor 40 online && pass "wine-node (windows) online in Linux console" || { nodes; tail -20 "$DATA/logs/service.log"; fail "node not online"; }
timeout 30 wine "$EXE" service stop >/dev/null || true
offline() { nodes | python3 -c 'import json,sys;sys.exit(0 if any(n["name"]=="wine-node" and n["status"]=="offline" for n in json.load(sys.stdin)) else 1)'; }
waitfor 30 offline && pass "service stop -> node offline" || fail "node still online"

echo ">> 5. silent uninstall /PURGE"
timeout 400 wine "$UNINST" /S /PURGE || true
waitfor 180 test ! -d "$DATA" || true
[ ! -d "$DATA" ] && pass "data purged" || fail "data dir left"
echo ">> wine smoke OK"
