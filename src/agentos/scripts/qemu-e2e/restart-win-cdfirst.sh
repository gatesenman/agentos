#!/bin/bash
# Kill stuck windows-11c and restart with CD bootindex=1 (fixes UEFI-shell from empty HD first).
set -u
B=/root/e2e
NAME=windows-11c
D=$B/$NAME
R=/mnt/d/AgentOS-E2E/qemu-results/$NAME
STAGE=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage

echo "$(date '+%F %T') restart-win-cdfirst begin"

# stop old driver/qemu/tpm
pkill -f "drive4-win.sh" 2>/dev/null || true
if [ -f "$D/qemu.pid" ]; then kill "$(cat "$D/qemu.pid")" 2>/dev/null || true; fi
pkill -f "qemu-system-x86_64.*${NAME}" 2>/dev/null || true
pkill -f "swtpm.*${NAME}" 2>/dev/null || true
sleep 2

# archive prior failure evidence
if [ -d "$R" ]; then
  ARCH=/mnt/d/AgentOS-E2E/qemu-results/${NAME}-post-reboot-shell-fail
  rm -rf "$ARCH"
  mkdir -p "$ARCH"
  cp -a "$R"/. "$ARCH"/ 2>/dev/null || true
  echo "archived prior run -> $ARCH"
fi

rm -rf "$D" "$R"
mkdir -p "$R/shots"

cp -f "$STAGE/drive4-win.sh" "$B/drive4-win.sh"
cp -f "$STAGE/uefi-boot-cd.py" "$B/uefi-boot-cd.py" 2>/dev/null || true
cp -f "$STAGE/uefi-boot-hd.py" "$B/uefi-boot-hd.py" 2>/dev/null || true
cp -f "$STAGE/qmp.py" "$B/qmp.py" 2>/dev/null || true
chmod +x "$B/drive4-win.sh"
grep -n bootindex "$B/drive4-win.sh" | head -8
ls -la "$B/uefi-boot-hd.py" "$B/uefi-boot-cd.py" "$B/drive4-win.sh"

# Host proxy for seedgen MSI only (never guest 10.0.2.2 from WSL)
unset PROXY
export HTTP_PROXY= HTTPS_PROXY= http_proxy= https_proxy=
if curl -fsS -o /dev/null -m 2 http://127.0.0.1:7897 2>/dev/null; then
  export HTTP_PROXY=http://127.0.0.1:7897 HTTPS_PROXY=http://127.0.0.1:7897
  echo "seedgen proxy=http://127.0.0.1:7897"
else
  echo "seedgen proxy=none"
fi

setsid nohup env NAME=$NAME PORT=2231 VNCD=91 MEM=8192 FRESH=1 \
  bash "$B/drive4-win.sh" > "$R/driver.log" 2>&1 < /dev/null &
echo "driver pid $!"
sleep 15
echo "---- result ----"
tail -40 "$R/result.txt" 2>/dev/null || true
echo "---- driver ----"
tail -20 "$R/driver.log" 2>/dev/null || true
ps aux | grep -E "qemu-system|drive4|seedgen" | grep -v grep | head -10
