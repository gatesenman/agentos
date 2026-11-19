#!/usr/bin/env bash
# AgentOS CI (module M8): vet for all target OSes, race tests, full dist build + artifact checks.
# Usage: scripts/ci.sh            (env: VERSION, SKIP_TESTS=1, SKIP_DIST=1, WINE_SMOKE=1 to also run scripts/wine-smoke.sh)
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH=/usr/local/go/bin:$PATH
export VERSION=${VERSION:-0.1.0}
step() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }

step "gofmt (packaging-owned code is enforced, the rest is reported)"
bad=$(gofmt -l cmd/agentos/service_*.go cmd/agentos-tray packaging test/e2e/binary_test.go 2>/dev/null || true)
[ -z "$bad" ] || { echo "gofmt needed:"; echo "$bad"; exit 1; }
other=$(gofmt -l cmd internal test 2>/dev/null || true)
[ -z "$other" ] || { echo "warning: not gofmt-ed:"; echo "$other"; }

step "go vet (linux, windows, darwin/arm64)"
go vet ./...
GOOS=windows GOARCH=amd64 go vet ./...
GOOS=darwin GOARCH=arm64 go vet ./...

step "web JS syntax check"
for f in $(find web/static/js -name "*.js" -not -path "*vendor*"); do node --input-type=module --check < "$f" || { echo "syntax error: $f"; exit 1; }; done

step "CGO_ENABLED=0 cross-compile check"
for t in linux/amd64 windows/amd64 darwin/arm64; do
  CGO_ENABLED=0 GOOS=${t%/*} GOARCH=${t#*/} go build -o /dev/null ./cmd/agentos
done
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/agentos-tray

if [ "${SKIP_TESTS:-}" != 1 ]; then
  step "go test -race"
  go test -race -count=1 ./...
fi

step "shell scripts"
for f in scripts/*.sh packaging/linux/install.sh packaging/macos/install.sh; do
  if head -1 "$f" | grep -q bash; then bash -n "$f"; else sh -n "$f"; fi
done
command -v shellcheck >/dev/null && shellcheck -S error scripts/*.sh packaging/*/install.sh || true

if [ "${SKIP_DIST:-}" != 1 ]; then
  step "make dist"
  make dist
  step "verify dist artifacts"
  for f in agentos-linux-amd64 agentos-windows-amd64.exe agentos-darwin-arm64 agentos-tray-windows-amd64.exe \
           "AgentOS-Setup-$VERSION.exe" "agentos-$VERSION-linux-amd64.tar.gz" "agentos-$VERSION-darwin-arm64.tar.gz" \
           join.sh join.ps1 SHA256SUMS; do
    [ -s "dist/$f" ] || { echo "missing dist/$f"; exit 1; }
  done
  (cd dist && sha256sum -c --quiet SHA256SUMS)
  if command -v readelf >/dev/null; then
    ! readelf -l dist/agentos-linux-amd64 | grep -q INTERP || { echo "linux binary not static"; exit 1; }
  fi
  dist/agentos-linux-amd64 version | grep -q "agentos $VERSION"
  tar -tzf "dist/agentos-$VERSION-linux-amd64.tar.gz" | grep -q "agentos.service"
  tar -tzf "dist/agentos-$VERSION-darwin-arm64.tar.gz" | grep -q "com.agentos.server.plist"
  ls -la dist/
fi

if [ "${WINE_SMOKE:-}" = 1 ]; then
  step "wine smoke test"
  scripts/wine-smoke.sh
fi
step "CI OK"
