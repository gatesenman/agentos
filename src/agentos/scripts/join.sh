#!/bin/sh
# Join this Linux/macOS machine to an AgentOS console as an Agent OS node (module M8).
#
#   curl -fsSL http://<console>:7800/download/join.sh | sh -s -- --server http://<console>:7800 --token <TOKEN> [--name NAME] [--install]
#   ./join.sh --server URL --token TOKEN [--name NAME] [--install|--uninstall] [--dir DIR]
#
# Downloads <server>/download/agentos-<os>-<arch> (served by the console from its dist/ folder),
# then either runs the node in the foreground, or with --install registers it as a service
# (systemd when root, launchd user agent on macOS, otherwise a nohup background process).
# Env equivalents: AGENTOS_SERVER, AGENTOS_TOKEN, AGENTOS_NODE_NAME.
set -eu

SERVER=${AGENTOS_SERVER:-}
TOKEN=${AGENTOS_TOKEN:-}
NAME=${AGENTOS_NODE_NAME:-$(hostname 2>/dev/null || echo node)}
MODE=run
DIR=""

usage() { sed -n '2,12p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit "${1:-2}"; }
while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER=$2; shift 2 ;;
    --server=*) SERVER=${1#*=}; shift ;;
    --token) TOKEN=$2; shift 2 ;;
    --token=*) TOKEN=${1#*=}; shift ;;
    --name) NAME=$2; shift 2 ;;
    --name=*) NAME=${1#*=}; shift ;;
    --dir) DIR=$2; shift 2 ;;
    --install) MODE=install; shift ;;
    --uninstall) MODE=uninstall; shift ;;
    -h|--help) usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in linux|darwin) ;; *) echo "unsupported OS: $OS (use join.ps1 on Windows)" >&2; exit 1 ;; esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac
ROOT=0; [ "$(id -u)" = 0 ] && ROOT=1
if [ -z "$DIR" ]; then
  if [ $ROOT = 1 ]; then DIR=/usr/local/bin; else DIR="$HOME/.local/bin"; fi
fi
BIN="$DIR/agentos"
if [ $ROOT = 1 ]; then DATA=/var/lib/agentos/node; else DATA="$HOME/.agentos/node"; fi

if [ "$MODE" = uninstall ]; then
  if [ "$OS" = linux ] && [ $ROOT = 1 ] && command -v systemctl >/dev/null; then
    systemctl disable --now agentos-node.service 2>/dev/null || true
    rm -f /etc/systemd/system/agentos-node.service /etc/default/agentos-node; systemctl daemon-reload
  elif [ "$OS" = darwin ]; then
    P="$HOME/Library/LaunchAgents/com.agentos.node.plist"; launchctl unload "$P" 2>/dev/null || true; rm -f "$P"
  else
    pkill -f "$BIN node" 2>/dev/null || true
  fi
  echo "AgentOS node removed (binary $BIN and data $DATA kept)"; exit 0
fi

[ -n "$SERVER" ] && [ -n "$TOKEN" ] || { echo "--server and --token are required" >&2; usage; }
SERVER=${SERVER%/}
URL="$SERVER/download/agentos-$OS-$ARCH"

mkdir -p "$DIR"
echo ">> downloading $URL"
TMP="$BIN.download.$$"
if command -v curl >/dev/null; then curl -fL --progress-bar -o "$TMP" "$URL"
elif command -v wget >/dev/null; then wget -q -O "$TMP" "$URL"
else echo "curl or wget required" >&2; exit 1; fi
chmod 0755 "$TMP"
"$TMP" version >/dev/null 2>&1 || { rm -f "$TMP"; echo "downloaded file is not a valid agentos binary for $OS/$ARCH" >&2; exit 1; }
mv -f "$TMP" "$BIN"
[ "$OS" = darwin ] && xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true
echo ">> $("$BIN" version)"

if [ "$MODE" = run ]; then
  echo ">> starting node (Ctrl+C to stop); use --install to run it as a service"
  exec "$BIN" node --server "$SERVER" --token "$TOKEN" --name "$NAME" --data "$DATA"
fi

mkdir -p "$DATA"
if [ "$OS" = linux ] && [ $ROOT = 1 ] && command -v systemctl >/dev/null && [ -d /run/systemd/system ]; then
  umask 077
  printf 'AGENTOS_SERVER=%s\nAGENTOS_TOKEN=%s\nAGENTOS_NODE_NAME=%s\n' "$SERVER" "$TOKEN" "$NAME" > /etc/default/agentos-node
  umask 022
  cat > /etc/systemd/system/agentos-node.service <<UNIT
[Unit]
Description=AgentOS node (agent host)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/default/agentos-node
ExecStart=$BIN node --data $DATA
Restart=always
RestartSec=5
KillMode=mixed
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable agentos-node.service >/dev/null
  systemctl restart agentos-node.service
  echo ">> installed systemd service agentos-node (journalctl -u agentos-node -f)"
elif [ "$OS" = darwin ]; then
  P="$HOME/Library/LaunchAgents/com.agentos.node.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  launchctl unload "$P" 2>/dev/null || true
  cat > "$P" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.agentos.node</string>
  <key>ProgramArguments</key><array>
    <string>$BIN</string><string>node</string><string>--data</string><string>$DATA</string>
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>AGENTOS_SERVER</key><string>$SERVER</string>
    <key>AGENTOS_TOKEN</key><string>$TOKEN</string>
    <key>AGENTOS_NODE_NAME</key><string>$NAME</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/.agentos/node.log</string>
  <key>StandardErrorPath</key><string>$HOME/.agentos/node.log</string>
</dict></plist>
PLIST
  chmod 600 "$P"
  launchctl load -w "$P"
  echo ">> installed launchd agent com.agentos.node (log ~/.agentos/node.log)"
else
  LOG="$DATA/node.log"
  AGENTOS_TOKEN="$TOKEN" nohup "$BIN" node --server "$SERVER" --name "$NAME" --data "$DATA" >>"$LOG" 2>&1 &
  echo ">> started node in background (pid $!, log $LOG); not root/systemd: it will not survive reboot"
fi
echo ">> node '$NAME' joining $SERVER — check the console's node list"
