#!/bin/bash
set -u
D=/root/e2e/windows-11c
R=/mnt/d/AgentOS-E2E/qemu-results/windows-11c
EV=/mnt/c/Users/Administrator/GrokBot-Apps/AgentOS/_stage/pc-evidence
mkdir -p "$EV"
for f in install-5988.png install-8114.png after-hd-boot-2054.png; do
  [ -f "$R/shots/$f" ] && cp -f "$R/shots/$f" "$EV/w11c-${f}"
done
# also latest
LATEST=$(ls -t "$R/shots"/*.png 2>/dev/null | head -1 || true)
[ -n "${LATEST:-}" ] && cp -f "$LATEST" "$EV/w11c-latest.png"
USER=$(sed -n 1p "$D/creds.txt")
echo "USER=$USER host=$(sed -n 3p "$D/creds.txt")"
echo "---SSH---"
timeout 15 ssh -i "$D/id_ed25519" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -o ConnectTimeout=8 -o BatchMode=yes -o LogLevel=ERROR -p 2231 "${USER}@127.0.0.1" \
  "echo SSH_OK & hostname & type C:\\ProgramData\\AgentOS\\ready.txt" 2>&1 | head -30 || echo "SSH_FAIL exit=$?"
echo "---QMP status---"
timeout 10 python3 -c "
import socket,json
s=socket.socket(socket.AF_UNIX); s.settimeout(8); s.connect('$D/qmp.sock'); f=s.makefile('rw')
f.readline(); f.write(json.dumps({'execute':'qmp_capabilities'})+'\n'); f.flush(); f.readline()
f.write(json.dumps({'execute':'query-status'})+'\n'); f.flush(); print(f.readline())
" 2>&1 || echo QMP_FAIL
echo "---disk---"
ls -la "$D/disk.qcow2"
echo "---markers---"
grep -E 'READY|FAIL|TIMEOUT|PASS|qemu exited' "$R/result.txt" | tail -10 || true
ls -la "$R/ready.txt" 2>&1 || true
