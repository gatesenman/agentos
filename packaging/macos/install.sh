#!/bin/sh
# AgentOS macOS installer (module M8).
#   ./install.sh            copy agentos to /usr/local/bin (sudo prompt) and load the launchd agent
#   ./install.sh uninstall  unload agent and remove binary (keeps ~/.agentos)
set -eu
cd "$(dirname "$0")"
PLIST="$HOME/Library/LaunchAgents/com.agentos.server.plist"
if [ "${1:-}" = uninstall ]; then
  launchctl unload "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"; sudo rm -f /usr/local/bin/agentos
  echo "AgentOS removed (data kept in ~/.agentos)"; exit 0
fi
sudo mkdir -p /usr/local/bin
sudo install -m 0755 agentos /usr/local/bin/agentos
# downloaded binaries are quarantined by Gatekeeper
sudo xattr -d com.apple.quarantine /usr/local/bin/agentos 2>/dev/null || true
mkdir -p "$HOME/Library/LaunchAgents"
launchctl unload "$PLIST" 2>/dev/null || true
install -m 0644 com.agentos.server.plist "$PLIST"
launchctl load -w "$PLIST"
echo "AgentOS started: http://localhost:7800/  (log: /tmp/agentos.log)"
echo "initial admin password: ~/.agentos/server/initial-admin-password.txt"
open "http://localhost:7800/" 2>/dev/null || true
