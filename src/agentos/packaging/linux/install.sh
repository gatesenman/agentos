#!/bin/sh
# AgentOS Linux installer (module M8).
#   sudo ./install.sh            install/upgrade binary + server unit, enable and start it
#   sudo ./install.sh node URL TOKEN [NAME]   install as a remote node instead
#   sudo ./install.sh uninstall  stop + remove units and binary (keeps /var/lib/agentos)
set -eu
cd "$(dirname "$0")"
[ "$(id -u)" = 0 ] || { echo "please run as root (sudo)"; exit 1; }
BIN=/usr/local/bin/agentos
UNITDIR=/etc/systemd/system
case "${1:-server}" in
  uninstall)
    systemctl disable --now agentos.service agentos-node.service 2>/dev/null || true
    rm -f "$UNITDIR/agentos.service" "$UNITDIR/agentos-node.service" "$BIN"
    systemctl daemon-reload
    echo "AgentOS removed (data kept in /var/lib/agentos)"; exit 0 ;;
  node)
    [ $# -ge 3 ] || { echo "usage: $0 node <server-url> <token> [name]"; exit 2; }
    install -m 0755 agentos "$BIN"
    install -m 0644 agentos-node.service "$UNITDIR/agentos-node.service"
    umask 077
    printf 'AGENTOS_SERVER=%s\nAGENTOS_TOKEN=%s\nAGENTOS_NODE_NAME=%s\n' "$2" "$3" "${4:-$(hostname)}" > /etc/default/agentos-node
    systemctl daemon-reload
    systemctl enable agentos-node.service
    systemctl restart agentos-node.service
    echo "AgentOS node installed and started (journalctl -u agentos-node -f)"; exit 0 ;;
  server)
    install -m 0755 agentos "$BIN"
    install -m 0644 agentos.service "$UNITDIR/agentos.service"
    mkdir -p /var/lib/agentos
    systemctl daemon-reload
    systemctl enable agentos.service
    systemctl restart agentos.service
    echo "AgentOS server installed: http://localhost:7800/"
    echo "initial admin password: /var/lib/agentos/server/initial-admin-password.txt (after first start)"; exit 0 ;;
  *) echo "usage: $0 [server|node URL TOKEN [NAME]|uninstall]"; exit 2 ;;
esac
