# AgentOS desktop image: Debian 13 (trixie) + Xvfb + XFCE + Plank dock + x11vnc
# (VNC on :5900, no password; docker provider publishes 5900 on 127.0.0.1 only).
# Matches the AgentOS bot desktop look: XFCE session + bottom Plank dock
# (browser + terminal launchers).
#
#   docker build -t localhost/agentos-debian-desktop:0.1.22 \
#     -t localhost/agentos-debian-desktop:latest \
#     internal/env/providers/docker/images/desktop-debian
#
# Built automatically from the embedded context on first use of the versioned tag.
#
FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    DISPLAY=:0 GEOMETRY=1280x800x24 HOME=/root
# Prefer HTTPS Aliyun Debian mirror. Behind CN Clash/TUN, HTTP mid-transfer drops
# and HTTPS may hit MITM certs — disable apt TLS verify for the build only.
# Keep Docker Desktop HTTP(S)_PROXY. Retries + long timeout.
# XFCE + Plank dock; firefox-esr for a browser icon; fonts-noto-cjk for Chinese UI.
RUN set -eux; \
    printf '%s\n' \
      'Acquire::Retries "10";' \
      'Acquire::http::Timeout "120";' \
      'Acquire::https::Timeout "120";' \
      'Acquire::http::Pipeline-Depth "0";' \
      'Acquire::https::Verify-Peer "false";' \
      'Acquire::https::Verify-Host "false";' \
      > /etc/apt/apt.conf.d/80-retries; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i \
        -e 's|http://deb.debian.org/debian|https://mirrors.aliyun.com/debian|g' \
        -e 's|https://deb.debian.org/debian|https://mirrors.aliyun.com/debian|g' \
        -e 's|http://deb.debian.org/debian-security|https://mirrors.aliyun.com/debian-security|g' \
        -e 's|https://deb.debian.org/debian-security|https://mirrors.aliyun.com/debian-security|g' \
        /etc/apt/sources.list.d/debian.sources; \
    elif [ -f /etc/apt/sources.list ]; then \
      sed -i \
        -e 's|http://deb.debian.org|https://mirrors.aliyun.com|g' \
        -e 's|https://deb.debian.org|https://mirrors.aliyun.com|g' \
        -e 's|http://security.debian.org|https://mirrors.aliyun.com/debian-security|g' \
        -e 's|https://security.debian.org|https://mirrors.aliyun.com/debian-security|g' \
        /etc/apt/sources.list; \
    fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends --fix-missing \
      xfce4 xfce4-terminal xfce4-panel thunar mousepad \
      plank firefox-esr \
      dbus-x11 xvfb x11vnc xdotool \
      fonts-noto-cjk ca-certificates curl procps; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p \
      /usr/share/backgrounds/agentos \
      /root/.config/xfce4/xfconf/xfce-perchannel-xml \
      /root/.config/autostart \
      /root/.config/plank/dock1/launchers
# Pleasant solid backdrop (dark blue-gray) via XFCE desktop channel defaults.
COPY xfce4-desktop.xml /root/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml
COPY plank-autostart.desktop /root/.config/autostart/plank.desktop
COPY dock-terminal.dockitem /root/.config/plank/dock1/launchers/xfce4-terminal.dockitem
COPY dock-browser.dockitem /root/.config/plank/dock1/launchers/firefox-esr.dockitem
COPY entrypoint.sh /usr/local/bin/agentos-desktop
RUN chmod +x /usr/local/bin/agentos-desktop
EXPOSE 5900
ENTRYPOINT ["/usr/local/bin/agentos-desktop"]
