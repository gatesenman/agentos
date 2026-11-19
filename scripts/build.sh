#!/usr/bin/env bash
# Cross-build AgentOS for all targets, package archives + Windows installer, write SHA256SUMS.
# Usage: scripts/build.sh [all|linux|windows|darwin|tray|desktop|installer|archives|checksums]   (default: all)
# Env:   VERSION (default: git tag or 0.1.0), GO (default go), DIST (default dist)
# Outputs (dist/):
#   agentos-windows-amd64.exe agentos-linux-{amd64,arm64} agentos-darwin-{arm64,amd64} agentos-tray-windows-amd64.exe
#   AgentOS-desktop-windows-amd64.exe (desktop app, if cmd/agentos-desktop exists)
#   agentos-<v>-{linux-amd64,linux-arm64,darwin-arm64,darwin-amd64}.tar.gz  agentos-<v>-windows-amd64.zip
#   AgentOS-Setup-<v>.exe  join.sh  join.ps1  SHA256SUMS
# The installer also ships all five agentos binaries + join scripts in <install>\bin\dist\ so the
# console can serve them at /download/<file> (endpoint: module M9).
# Owned by module M8.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH=/usr/local/go/bin:$PATH
GO=${GO:-go}
DIST=${DIST:-dist}
VERSION=${VERSION:-$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)}
VERSION=${VERSION:-$(cat VERSION 2>/dev/null || true)}
VERSION=${VERSION:-0.1.12}
# numeric X.Y.Z.0 for Windows version resources / NSIS VIProductVersion
VERSION_NUM=$(echo "$VERSION" | grep -oE '^[0-9]+(\.[0-9]+){0,2}' || echo 0)
while [ "$(echo "$VERSION_NUM" | tr -cd . | wc -c)" -lt 3 ]; do VERSION_NUM="$VERSION_NUM.0"; done
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
PKG=github.com/agentos/agentos/internal/version
LDFLAGS="-s -w -X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT -X $PKG.Date=$DATE"
mkdir -p "$DIST"
DISTABS=$(cd "$DIST" && pwd)
UNIX_TARGETS="linux/amd64 linux/arm64 darwin/arm64 darwin/amd64"

SYSOS=(cmd/agentos/rsrc_windows_amd64.syso cmd/agentos-tray/rsrc_windows_amd64.syso)
[ -d cmd/agentos-desktop ] && ! git ls-files --error-unmatch cmd/agentos-desktop/rsrc_windows_amd64.syso >/dev/null 2>&1 \
  && SYSOS+=(cmd/agentos-desktop/rsrc_windows_amd64.syso)
cleanup_syso() { rm -f "${SYSOS[@]}"; }
trap cleanup_syso EXIT

# Windows resources (icon, version info, manifest) are generated into
# cmd/*/rsrc_windows_amd64.syso just for the build and removed afterwards.
gen_syso() {
  [ -f packaging/windows/agentos.ico ] || $GO run ./packaging/tools/wintool assets
  $GO run ./packaging/tools/wintool syso -version "$VERSION" -out cmd/agentos/rsrc_windows_amd64.syso \
    -desc "AgentOS - 本地 AI Agent 管理工具" -name agentos.exe
  $GO run ./packaging/tools/wintool syso -version "$VERSION" -out cmd/agentos-tray/rsrc_windows_amd64.syso \
    -desc "AgentOS 托盘启动器" -name agentos-tray.exe
}

build() { # os arch out
  echo ">> $1/$2 -> $DIST/$3"
  [ "$1" = windows ] && gen_syso
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 $GO build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$3" ./cmd/agentos
}

tray() {
  echo ">> windows/amd64 tray -> $DIST/agentos-tray-windows-amd64.exe"
  gen_syso
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $GO build -trimpath \
    -ldflags "-s -w -H windowsgui -X main.version=$VERSION" -o "$DIST/agentos-tray-windows-amd64.exe" ./cmd/agentos-tray
}

# Desktop app (cmd/agentos-desktop, module M6): WebView2 window "AgentOS.exe". Optional: skipped
# when the directory does not exist; linux/darwin builds are best effort (may need cgo there).
desktop() {
  [ -d cmd/agentos-desktop ] || { echo ">> cmd/agentos-desktop not present, skipping desktop app"; return 0; }
  local own_syso=""
  if ! ls cmd/agentos-desktop/*.syso >/dev/null 2>&1; then
    [ -f packaging/windows/agentos.ico ] || $GO run ./packaging/tools/wintool assets
    $GO run ./packaging/tools/wintool syso -version "$VERSION" -out cmd/agentos-desktop/rsrc_windows_amd64.syso \
      -desc "AgentOS" -name AgentOS.exe
    own_syso=cmd/agentos-desktop/rsrc_windows_amd64.syso
  fi
  echo ">> windows/amd64 desktop -> $DIST/AgentOS-desktop-windows-amd64.exe"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $GO build -trimpath \
    -ldflags "$LDFLAGS -H windowsgui -X main.version=$VERSION" -o "$DIST/AgentOS-desktop-windows-amd64.exe" ./cmd/agentos-desktop
  [ -n "$own_syso" ] && rm -f "$own_syso"
  for t in linux/amd64 darwin/arm64; do
    if CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} $GO build -trimpath -ldflags "$LDFLAGS -X main.version=$VERSION" \
        -o "$DIST/agentos-desktop-${t%/*}-${t#*/}" ./cmd/agentos-desktop 2>/dev/null; then
      echo ">> $t desktop -> $DIST/agentos-desktop-${t%/*}-${t#*/}"
    else
      echo ">> $t desktop: not buildable with CGO_ENABLED=0, skipped"
    fi
  done
}

installer() {
  [ -f "$DIST/agentos-windows-amd64.exe" ] || build windows amd64 agentos-windows-amd64.exe
  [ -f "$DIST/agentos-tray-windows-amd64.exe" ] || tray
  local t
  for t in $UNIX_TARGETS; do
    [ -f "$DIST/agentos-${t/\//-}" ] || build "${t%/*}" "${t#*/}" "agentos-${t/\//-}"
  done
  command -v makensis >/dev/null || { echo "makensis not found (apt install nsis)"; exit 1; }
  echo ">> NSIS installer (warnings are errors)"
  local out desk=()
  if [ -d cmd/agentos-desktop ]; then
    [ -f "$DIST/AgentOS-desktop-windows-amd64.exe" ] || desktop
    desk=(-DDESKTOPEXE="$DISTABS/AgentOS-desktop-windows-amd64.exe")
  fi
  out=$(makensis -WX -V2 "${desk[@]}" -DVERSION="$VERSION" -DVERSION_NUM="$VERSION_NUM" \
    -DSRCEXE="$DISTABS/agentos-windows-amd64.exe" -DTRAYEXE="$DISTABS/agentos-tray-windows-amd64.exe" \
    -DASSETDIR="$(pwd)/packaging/windows" -DDISTDIR="$DISTABS" -DSCRIPTDIR="$(pwd)/scripts" -DOUTFILE="$DISTABS/AgentOS-Setup-$VERSION.exe" \
    packaging/nsis/agentos.nsi 2>&1) || { echo "$out"; exit 1; }
  [ -n "$out" ] && echo "$out"
  ls -la "$DIST/AgentOS-Setup-$VERSION.exe"
}

archive_unix() { # os arch binary
  local name="agentos-$VERSION-$1-$2" stage
  stage=$(mktemp -d)
  mkdir -p "$stage/$name"
  install -m 0755 "$DIST/$3" "$stage/$name/agentos"
  install -m 0644 README.md "$stage/$name/README.md"
  if [ "$1" = linux ]; then
    install -m 0755 scripts/join.sh "$stage/$name/join.sh"
    install -m 0644 packaging/linux/agentos.service packaging/linux/agentos-node.service "$stage/$name/"
    install -m 0755 packaging/linux/install.sh "$stage/$name/install.sh"
  else
    install -m 0644 packaging/macos/com.agentos.server.plist "$stage/$name/"
    install -m 0755 scripts/join.sh "$stage/$name/join.sh"
    install -m 0755 packaging/macos/install.sh "$stage/$name/install.sh"
  fi
  tar -C "$stage" --owner=0 --group=0 -czf "$DIST/$name.tar.gz" "$name"
  rm -rf "$stage"
  echo ">> $DIST/$name.tar.gz"
}

archive_windows() { # portable zip (no installer)
  local name="agentos-$VERSION-windows-amd64"
  python3 - "$DIST" "$name" <<'PY'
import sys, zipfile, os
dist, name = sys.argv[1], sys.argv[2]
how = (
    "【请这样启动 AgentOS】\r\n\r\n"
    "✅ 双击本目录下的 AgentOS.exe（桌面窗口程序，无黑框控制台）\r\n\r\n"
    "❌ 不要运行 bin\\agentos-server.exe\r\n"
    "   那是后台服务/命令行工具，双击会弹出黑色 cmd 窗口，不是日常使用入口。\r\n\r\n"
    "便携版也可直接运行：启动 AgentOS.bat\r\n\r\n"
    "安装版请用开始菜单或桌面上的「AgentOS」快捷方式（同样指向 AgentOS.exe）。\r\n"
)
bat = "@echo off\r\nchcp 65001 >nul\r\ncd /d \"%~dp0\"\r\nstart \"\" \"%~dp0AgentOS.exe\"\r\n"
with zipfile.ZipFile(f"{dist}/{name}.zip", "w", zipfile.ZIP_DEFLATED) as z:
    # GUI at zip root; console renamed so users do not double-click a black cmd window
    z.write(f"{dist}/agentos-windows-amd64.exe", f"{name}/bin/agentos-server.exe")
    z.write(f"{dist}/agentos-tray-windows-amd64.exe", f"{name}/bin/agentos-tray.exe")
    z.write("README.md", f"{name}/README.md")
    z.write("scripts/join.ps1", f"{name}/join.ps1")
    z.writestr(f"{name}/请先阅读-如何启动.txt", how.encode("utf-8-sig"))
    z.writestr(f"{name}/启动 AgentOS.bat", bat.encode("utf-8"))
    if os.path.exists(f"{dist}/AgentOS-desktop-windows-amd64.exe"):
        z.write(f"{dist}/AgentOS-desktop-windows-amd64.exe", f"{name}/AgentOS.exe")
PY
  echo ">> $DIST/$name.zip"
}

archives() {
  local t
  for t in $UNIX_TARGETS; do archive_unix "${t%/*}" "${t#*/}" "agentos-${t/\//-}"; done
  if command -v python3 >/dev/null; then archive_windows; fi
}

checksums() {
  cp scripts/join.sh scripts/join.ps1 "$DIST/"
  ( cd "$DIST" && rm -f SHA256SUMS && \
    sha256sum $(ls -1 | grep -E '^(agentos-|AgentOS-|join\.)' | sort) > SHA256SUMS )
  echo ">> $DIST/SHA256SUMS"; cat "$DIST/SHA256SUMS"
}

case "${1:-all}" in
  linux)   build linux amd64 agentos-linux-amd64; build linux arm64 agentos-linux-arm64 ;;
  windows) build windows amd64 agentos-windows-amd64.exe ;;
  darwin)  build darwin arm64 agentos-darwin-arm64; build darwin amd64 agentos-darwin-amd64 ;;
  tray)    tray ;;
  desktop) desktop ;;
  installer) installer ;;
  archives)  archives ;;
  checksums) checksums ;;
  all)
    rm -f "$DIST"/agentos-* "$DIST"/AgentOS-* "$DIST"/SHA256SUMS "$DIST"/join.*
    build windows amd64 agentos-windows-amd64.exe
    for t in $UNIX_TARGETS; do build "${t%/*}" "${t#*/}" "agentos-${t/\//-}"; done
    tray
    desktop
    installer
    archives
    checksums ;;
  *) echo "unknown target $1"; exit 2 ;;
esac
