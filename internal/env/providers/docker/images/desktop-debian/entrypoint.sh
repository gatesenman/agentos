#!/bin/sh
# Starts Xvfb, D-Bus, XFCE (startxfce4), Plank dock, and x11vnc on :5900.
# Stays in the foreground (restarting x11vnc if it dies) until SIGTERM.
set -u
trap 'kill $(jobs -p) 2>/dev/null; exit 0' TERM INT
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0
mkdir -p /tmp/.X11-unix /root/.config/xfce4 /root/.config/plank/dock1/launchers /root/.config/autostart
chmod 1777 /tmp/.X11-unix 2>/dev/null || true

Xvfb "$DISPLAY" -screen 0 "$GEOMETRY" -nolisten tcp >/tmp/xvfb.log 2>&1 &
for i in $(seq 1 80); do [ -e /tmp/.X11-unix/X0 ] && break; sleep 0.1; done

if [ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ]; then
  if command -v dbus-launch >/dev/null 2>&1; then
    eval "$(dbus-launch --sh-syntax)"
    export DBUS_SESSION_BUS_ADDRESS DBUS_SESSION_BUS_PID
  fi
fi
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/runtime-root}"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR" 2>/dev/null || true

if command -v startxfce4 >/dev/null 2>&1; then
  startxfce4 >/tmp/xfce.log 2>&1 &
elif command -v xfce4-session >/dev/null 2>&1; then
  xfce4-session >/tmp/xfce.log 2>&1 &
else
  echo "agentos-desktop: XFCE not found" >&2
  exit 1
fi

# Soften first-run prompts; solid backdrop; prefer Plank dock over XFCE panel.
(
  sleep 2
  if command -v xfconf-query >/dev/null 2>&1; then
    for mon in monitor0 monitorVNC-0 monitorVirtual-1 monitorScreen; do
      xfconf-query -c xfce4-desktop -p /backdrop/screen0/$mon/workspace0/color-style -n -t int -s 1 2>/dev/null || true
      xfconf-query -c xfce4-desktop -p /backdrop/screen0/$mon/workspace0/rgba1 -n -t double -s 0.12 -t double -s 0.16 -t double -s 0.22 -t double -s 1 2>/dev/null || true
    done
    # Hide default bottom panel so Plank is the primary dock (bot-desktop look).
    xfconf-query -c xfce4-panel -p /panels/panel-1/autohide-behavior -n -t int -s 2 2>/dev/null || true
    xfconf-query -c xfce4-panel -p /panels/panel-1/size -n -t int -s 0 2>/dev/null || true
  fi
  # Stop the stock panel if still visible; Plank owns the bottom edge.
  if command -v xfce4-panel >/dev/null 2>&1; then
    xfce4-panel -q 2>/dev/null || true
    pkill -x xfce4-panel 2>/dev/null || true
  fi
  if command -v plank >/dev/null 2>&1; then
    # Prefer bottom dock; ignore theme failures.
    dconf write /net/launchpad/plank/docks/dock1/theme "'Transparent'" 2>/dev/null || true
    dconf write /net/launchpad/plank/docks/dock1/hide-mode 0 2>/dev/null || true
    nohup plank >/tmp/plank.log 2>&1 &
  fi
) &

while :; do
  x11vnc -display "$DISPLAY" -rfbport 5900 -forever -shared -nopw -quiet -noxdamage >/tmp/x11vnc.log 2>&1 &
  wait $!
  sleep 1
done
