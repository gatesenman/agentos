#!/bin/bash
set -u
D=/root/e2e/windows-11c
R=/mnt/d/AgentOS-E2E/qemu-results/windows-11c
EV=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage/pc-evidence
mkdir -p "$EV"
LATEST=$(ls -t "$R/shots"/*.png 2>/dev/null | head -1 || true)
[ -n "${LATEST:-}" ] && cp -f "$LATEST" "$EV/w11c-latest.png" && echo "LATEST=$LATEST"
echo "--- listen 2231 ---"
ss -lntp 2>/dev/null | grep 2231 || netstat -lntp 2>/dev/null | grep 2231 || echo "no listener shown"
echo "--- nc ---"
timeout 3 bash -c 'echo >/dev/tcp/127.0.0.1/2231' 2>&1 && echo TCP_OPEN || echo TCP_CLOSED
USER=$(sed -n 1p "$D/creds.txt")
echo "USER=$USER"
# longer banner wait
timeout 25 ssh -i "$D/id_ed25519" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -o ConnectTimeout=15 -o BatchMode=yes -o LogLevel=ERROR \
  -o ConnectionAttempts=1 -p 2231 "${USER}@127.0.0.1" \
  "echo SSH_OK; hostname; type C:\\ProgramData\\AgentOS\\ready.txt" 2>&1 | head -40 || echo "SSH_FAIL=$?"
grep -E 'READY|TIMEOUT|FAIL|PASS' "$R/result.txt" | tail -10 || true
# elapsed estimate
START=$(head -5 "$R/result.txt" | grep START | head -1)
echo "START_LINE=$START"
date
