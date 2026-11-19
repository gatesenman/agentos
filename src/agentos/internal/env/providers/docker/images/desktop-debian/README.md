# agentos-debian-desktop image

Used by the `docker` provider when creating the `docker-debian-desktop` preset
(`desktop: true` + `image: localhost/agentos-debian-desktop:<ver>`).

    docker build -t localhost/agentos-debian-desktop:0.1.22 \
      -t localhost/agentos-debian-desktop:latest \
      internal/env/providers/docker/images/desktop-debian

* Base: **Debian 13 (trixie)** — first-class alternative to the Ubuntu XFCE image.
* Xvfb display `:0` (`GEOMETRY` env, default 1280x800x24).
* **XFCE** session (`startxfce4`) + **Plank** bottom dock (browser + terminal),
  CJK fonts, Thunar, mousepad — matches the AgentOS bot desktop look.
* x11vnc on port 5900 without password (published on `127.0.0.1` only).
* Image tag is versioned (`DesktopDebianImage`) so content changes force a rebuild
  without forcing a rebuild of the Ubuntu `DesktopImage`.
* Aliyun Debian mirror + resilient apt options (same spirit as the Ubuntu Dockerfile).
