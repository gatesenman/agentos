#!/bin/sh
# AgentOS node installer for Linux / macOS.
#   curl -fsSL http://SERVER:7800/download/install.sh | sh -s -- --server URL --token TOKEN [--name NAME] [--install-service] [--desktop virtual] [--prefix DIR] [--data DIR]
# Downloads the agentos binary from the server and runs
#   agentos node --server URL --token TOKEN --name NAME --data DIR [--install-service]
# With --install-service the node is installed as a systemd / launchd service
# (auto-start) instead of running in the foreground.
set -eu
SERVER=""; TOKEN=""; NAME=""; SERVICE=0; PREFIX=""; DATA=""; DESKTOP=${AGENTOS_DESKTOP:-}
while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER=$2; shift 2 ;;
    --token) TOKEN=$2; shift 2 ;;
    --name) NAME=$2; shift 2 ;;
    --install-service|--service|--install) SERVICE=1; shift ;;
    --prefix) PREFIX=$2; shift 2 ;;
    --data) DATA=$2; shift 2 ;;
    --desktop) DESKTOP=$2; shift 2 ;;
    *) echo "unknown argument / 未知参数: $1" >&2; exit 2 ;;
  esac
done
[ -n "$SERVER" ] && [ -n "$TOKEN" ] || { echo "need --server and --token / 需要 --server 和 --token" >&2; exit 2; }
SERVER=${SERVER%/}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "unsupported OS / 不支持的系统: $os" >&2; exit 1 ;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported arch / 不支持的架构: $arch" >&2; exit 1 ;; esac

if [ "$(id -u)" = "0" ]; then
  BIN=${PREFIX:-/usr/local/bin}; DATA=${DATA:-/var/lib/agentos/node}
else
  BIN=${PREFIX:-$HOME/.agentos/bin}; DATA=${DATA:-$HOME/.agentos/node}
fi
mkdir -p "$BIN" "$DATA"
EXE="$BIN/agentos"
url="$SERVER/download/agentos-$os-$arch"
echo ">> 下载 / downloading $url"
if command -v curl >/dev/null 2>&1; then curl -fL --retry 2 -o "$EXE.tmp" "$url"
else wget -O "$EXE.tmp" "$url"; fi
chmod +x "$EXE.tmp" && mv -f "$EXE.tmp" "$EXE"

set -- node --server "$SERVER" --token "$TOKEN" --data "$DATA"
[ -n "$NAME" ] && set -- "$@" --name "$NAME"
if [ "$DESKTOP" = "virtual" ]; then
  for p in Xvfb x11vnc; do command -v $p >/dev/null 2>&1 || echo "!! 虚拟桌面需要 / virtual desktop needs: xvfb x11vnc fluxbox xterm (apt install xvfb x11vnc fluxbox xterm xdotool)" >&2; done
  set -- "$@" --desktop virtual
fi
if [ "$SERVICE" = "1" ]; then
  echo ">> 安装为服务 / installing service"
  exec "$EXE" "$@" --install-service
fi
echo ">> 启动 / starting: $EXE node --data $DATA"
exec "$EXE" "$@"
