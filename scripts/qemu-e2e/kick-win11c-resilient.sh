#!/bin/bash
# Archive SIGHUP flake (preserve prior post-reboot-shell-fail), stage tip scripts, setsid kick.
set -u
B=/root/e2e
NAME=windows-11c
D=$B/$NAME
R=/mnt/d/AgentOS-E2E/qemu-results/$NAME
STAGE=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage
ARCH_SIGHUP=/mnt/d/AgentOS-E2E/qemu-results/${NAME}-sighup-flake-0435
ARCH_GOOD=/mnt/d/AgentOS-E2E/qemu-results/${NAME}-post-reboot-shell-fail

echo "$(date '+%F %T') kick-win11c-resilient begin"
echo "good archive shots: $(ls "$ARCH_GOOD/shots" 2>/dev/null | wc -l)"

if [ -d "$R" ]; then
  rm -rf "$ARCH_SIGHUP"
  mkdir -p "$ARCH_SIGHUP"
  cp -a "$R"/. "$ARCH_SIGHUP"/ 2>/dev/null || true
  echo "archived sighup flake -> $ARCH_SIGHUP"
fi

pkill -f "drive4-win.sh" 2>/dev/null || true
pkill -f "qemu-system-x86_64.*${NAME}" 2>/dev/null || true
pkill -f "swtpm.*${NAME}" 2>/dev/null || true
sleep 2

cp -f "$STAGE/drive4-win.sh" "$B/drive4-win.sh"
cp -f "$STAGE/uefi-boot-hd.py" "$B/uefi-boot-hd.py" 2>/dev/null || true
cp -f "$STAGE/uefi-boot-cd.py" "$B/uefi-boot-cd.py" 2>/dev/null || true
cp -f "$STAGE/qmp.py" "$B/qmp.py" 2>/dev/null || true
chmod +x "$B/drive4-win.sh"
grep -nE 'ensure_tpm|setsid|trap|bootindex' "$B/drive4-win.sh" | head -20
ls -la "$B/uefi-boot-hd.py" "$B/drive4-win.sh"

rm -rf "$D" "$R"
mkdir -p "$R/shots"

unset PROXY
export HTTP_PROXY= HTTPS_PROXY= http_proxy= https_proxy=
if curl -fsS -o /dev/null -m 2 http://127.0.0.1:7897 2>/dev/null; then
  export HTTP_PROXY=http://127.0.0.1:7897 HTTPS_PROXY=http://127.0.0.1:7897
  echo "seedgen proxy=$HTTP_PROXY"
else
  echo "seedgen proxy=none"
fi

setsid nohup env NAME=$NAME PORT=2231 VNCD=91 MEM=8192 FRESH=1 \
  bash "$B/drive4-win.sh" > "$R/driver.log" 2>&1 < /dev/null &
echo "driver pid $!"
sleep 22
echo "---- result ----"
tail -40 "$R/result.txt" 2>/dev/null || true
echo "---- procs ----"
pgrep -af "drive4-win|qemu-system|swtpm" | head -12
ls -la "$D/tpm/sock" "$D/disk.qcow2" "$D/qemu.pid" 2>/dev/null || true
echo "$(date '+%F %T') kick done"
