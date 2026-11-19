#!/usr/bin/env bash
# Desktop e2e runner: builds the real binary, starts a "host" X desktop for the
# built-in env (Xvfb + fluxbox + xterm + x11vnc, like a real Linux desktop),
# a throwaway sshd (port from python, user aosssh), the agentos server, then
# runs desktop-e2e.mjs. Everything is torn down on exit.
#
#   test/e2e/desktop/run.sh [OUT_DIR] [--only builtin,local,remote,docker,ssh]
#
# Prerequisites (Debian/Ubuntu): xvfb x11vnc fluxbox xterm xdotool openssh-server
# google-chrome, node >= 22, docker (for the docker type), sudo (sshd + user).
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
OUT=${1:-/workspace/deliver/e2e}; shift || true
WORK=$(mktemp -d /tmp/agentos-desk-e2e.XXXX)
SSH_USER=${SSH_USER:-aosssh}; SSH_PASS=${SSH_PASS:-aos-ssh-pw1}
export PATH=/usr/local/go/bin:$PATH
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
pids=()
cleanup() {
  set +e
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done
  [ -f "$WORK/sshd.pid" ] && sudo kill "$(cat "$WORK/sshd.pid")" 2>/dev/null
  sudo pkill -u "$SSH_USER" 2>/dev/null   # the ssh user's virtual desktop / bootstrapped node
  sleep 2
  for p in "${pids[@]}"; do kill -9 "$p" 2>/dev/null; done
  echo ">> logs kept in $WORK"
}
trap cleanup EXIT

echo ">> build"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$WORK/agentos" ./cmd/agentos)

echo ">> host desktop for the built-in env"
exec 3< <(Xvfb -displayfd 1 -screen 0 1280x800x24 -nolisten tcp 2>"$WORK/host-xvfb.log")
read -r -u 3 HD; pids+=($!)
export HOST_DISPLAY=":$HD"
DISPLAY=$HOST_DISPLAY fluxbox >"$WORK/host-wm.log" 2>&1 & pids+=($!)
sleep 1
DISPLAY=$HOST_DISPLAY xterm -geometry 100x28+60+60 -title "AgentOS host" >/dev/null 2>&1 & pids+=($!)
HVNC=$(free_port)
x11vnc -display "$HOST_DISPLAY" -rfbport "$HVNC" -localhost -forever -shared -nopw -noxdamage -quiet >"$WORK/host-x11vnc.log" 2>&1 & pids+=($!)
echo "   host DISPLAY=$HOST_DISPLAY VNC=127.0.0.1:$HVNC"

echo ">> sshd"
id "$SSH_USER" >/dev/null 2>&1 || sudo useradd -m -s /bin/bash "$SSH_USER"
echo "$SSH_USER:$SSH_PASS" | sudo chpasswd
SP=$(free_port)
ssh-keygen -q -t ed25519 -N '' -f "$WORK/host_key"
cat >"$WORK/sshd_config" <<CONF
Port $SP
ListenAddress 127.0.0.1
HostKey $WORK/host_key
PidFile $WORK/sshd.pid
PasswordAuthentication yes
KbdInteractiveAuthentication no
UsePAM no
AllowTcpForwarding yes
AllowUsers $SSH_USER
Subsystem sftp internal-sftp
CONF
sudo /usr/sbin/sshd -f "$WORK/sshd_config" -E "$WORK/sshd.log"
echo "   sshd 127.0.0.1:$SP user $SSH_USER"

echo ">> server"
PORT=$(free_port)
DISPLAY=$HOST_DISPLAY "$WORK/agentos" server --listen "0.0.0.0:$PORT" --data "$WORK/data" --admin-password admin123 \
  --vnc "127.0.0.1:$HVNC" >"$WORK/server.log" 2>&1 & pids+=($!)
for i in $(seq 1 100); do curl -s -o /dev/null "http://127.0.0.1:$PORT/api/v1/auth/me" && break; sleep 0.2; done
sleep 1

echo ">> e2e"
rc=0
node --experimental-websocket "$ROOT/test/e2e/desktop/desktop-e2e.mjs" --url "http://127.0.0.1:$PORT" --password admin123 \
  --out "$OUT" --bin "$WORK/agentos" --work "$WORK" --ssh-port "$SP" --ssh-user "$SSH_USER" --ssh-pass "$SSH_PASS" "$@" || rc=$?
echo ">> e2e exit $rc (results: $OUT/RESULTS.md)"
exit $rc
