#!/bin/bash
set -u
R=/mnt/d/AgentOS-E2E/qemu-results/windows-11c
D=/root/e2e/windows-11c
EV=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage/pc-evidence
mkdir -p "$EV"
date
echo "qemu=$(pgrep -c -f 'qemu-system-x86_64' || true) drive4=$(pgrep -c -f 'drive4-win.sh' || true) swtpm=$(pgrep -c -f 'swtpm.*windows-11c' || true)"
ls -la "$D/disk.qcow2" 2>/dev/null || echo "no disk"
echo "---RESULT_TAIL---"
tail -30 "$R/result.txt" 2>/dev/null || true
echo "---SHOTS---"
ls -lt "$R/shots" 2>/dev/null | head -10
LATEST=$(ls -t "$R/shots"/*.png 2>/dev/null | head -1 || true)
if [ -n "${LATEST:-}" ]; then
  echo "LATEST=$LATEST"
  cp -f "$LATEST" "$EV/w11c-latest.png"
fi
echo "---MARKERS---"
grep -E 'READY|FAIL|TIMEOUT|HD boot|qemu exited|swtpm|PASS' "$R/result.txt" 2>/dev/null | tail -20 || true
ls -la "$R/ready.txt" 2>/dev/null || echo "no ready.txt"
