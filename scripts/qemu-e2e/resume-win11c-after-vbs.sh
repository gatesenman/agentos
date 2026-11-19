#!/bin/bash
# Resume windows-11c after QEMU nested-VBS internal-error; keep disk/seed.
set -u
B=/root/e2e
NAME=windows-11c
D=$B/$NAME
R=/mnt/d/AgentOS-E2E/qemu-results/$NAME
STAGE=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage

echo "$(date '+%F %T') resume-after-vbs begin"
pkill -f monitor-loop 2>/dev/null || true
pkill -f "drive4-win.sh" 2>/dev/null || true
if [ -f "$D/qemu.pid" ]; then kill "$(cat "$D/qemu.pid")" 2>/dev/null || true; fi
pkill -f "qemu-system-x86_64.*${NAME}" 2>/dev/null || true
pkill -f "swtpm.*${NAME}" 2>/dev/null || true
sleep 3

# Stage patched driver + helpers
cp -f "$STAGE/drive4-win.sh" "$B/drive4-win.sh"
cp -f "$STAGE/uefi-boot-hd.py" "$B/uefi-boot-hd.py" 2>/dev/null || true
cp -f "$STAGE/uefi-boot-cd.py" "$B/uefi-boot-cd.py" 2>/dev/null || true
cp -f "$STAGE/qmp.py" "$B/qmp.py" 2>/dev/null || true
chmod +x "$B/drive4-win.sh"

ls -lh "$D/disk.qcow2" "$D/seed.iso" "$D/creds.txt" "$D/id_ed25519" 2>&1 | tee -a "$R/result.txt"
[ -f "$D/disk.qcow2" ] || { echo "NO DISK — refuse resume"; exit 3; }

# Append marker; do not wipe R
echo "$(date '+%F %T') VBS resume FRESH=0" >> "$R/result.txt"
mkdir -p "$R/shots"

setsid nohup env NAME=$NAME PORT=2231 VNCD=91 MEM=8192 FRESH=0 \
  bash "$B/drive4-win.sh" >> "$R/driver.log" 2>&1 < /dev/null &
echo "driver pid $!"
sleep 25
tail -30 "$R/result.txt" 2>/dev/null || true
pgrep -af "drive4-win|qemu-system.*${NAME}" | head -8
echo "$(date '+%F %T') resume kicked"
