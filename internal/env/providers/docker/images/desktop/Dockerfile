# AgentOS desktop image: Ubuntu 24.04 + Xvfb + XFCE (full panel/desktop) + x11vnc
# (VNC on :5900, no password; the docker provider publishes 5900 on 127.0.0.1 only
# and the console relays it). This is a real graphical desktop — not fluxbox/xterm console.
#
#   docker build -t localhost/agentos-desktop:0.1.20 -t localhost/agentos-desktop:latest \
#     internal/env/providers/docker/images/desktop
#
# (the server builds it automatically from an embedded copy on first use of the
# versioned tag; bumping DesktopImage forces a rebuild so old fluxbox layers are not reused)
#
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8 \
    DISPLAY=:0 GEOMETRY=1280x800x24 HOME=/root
# Prefer HTTPS Aliyun mirror. Behind CN Clash/TUN, HTTP mid-transfer drops on large
# indexes (noble/universe) and HTTPS hits MITM certs (198.18.x). Disable apt TLS
# verify for the build only; keep Docker Desktop HTTP(S)_PROXY. Retries + long timeout.
# XFCE desktop chrome first (panel, thunar, terminal); skip browser to keep image lean.
# fonts-noto-cjk for Chinese UI. dbus-x11 required by xfce4-session.
RUN set -eux; \
    printf '%s\n' \
      'Acquire::Retries "10";' \
      'Acquire::http::Timeout "120";' \
      'Acquire::https::Timeout "120";' \
      'Acquire::http::Pipeline-Depth "0";' \
      'Acquire::https::Verify-Peer "false";' \
      'Acquire::https::Verify-Host "false";' \
      > /etc/apt/apt.conf.d/80-retries; \
    if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then \
      sed -i \
        -e 's|http://archive.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
        -e 's|https://archive.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
        -e 's|http://security.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
        -e 's|https://security.ubuntu.com/ubuntu|https://mirrors.aliyun.com/ubuntu|g' \
        /etc/apt/sources.list.d/ubuntu.sources; \
    elif [ -f /etc/apt/sources.list ]; then \
      sed -i \
        -e 's|http://archive.ubuntu.com|https://mirrors.aliyun.com|g' \
        -e 's|https://archive.ubuntu.com|https://mirrors.aliyun.com|g' \
        -e 's|http://security.ubuntu.com|https://mirrors.aliyun.com|g' \
        -e 's|https://security.ubuntu.com|https://mirrors.aliyun.com|g' \
        /etc/apt/sources.list; \
    fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends --fix-missing \
      xfce4 xfce4-terminal xfce4-panel thunar mousepad \
      dbus-x11 xvfb x11vnc xdotool \
      fonts-noto-cjk ca-certificates curl procps; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /usr/share/backgrounds/agentos /root/.config/xfce4/xfconf/xfce-perchannel-xml
# Pleasant solid backdrop (dark blue-gray) via XFCE desktop channel defaults.
COPY xfce4-desktop.xml /root/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml
COPY entrypoint.sh /usr/local/bin/agentos-desktop
RUN chmod +x /usr/local/bin/agentos-desktop
EXPOSE 5900
ENTRYPOINT ["/usr/local/bin/agentos-desktop"]
