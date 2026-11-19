# agentos-desktop image

Used by the `docker` provider when `desktop: true` and no `image` is set.

    docker build -t localhost/agentos-desktop:0.1.20 -t localhost/agentos-desktop:latest \
      internal/env/providers/docker/images/desktop

* Xvfb display `:0` (`GEOMETRY` env, default 1280x800x24).
* **XFCE** graphical desktop (`startxfce4`): panel, Thunar, xfce4-terminal, mousepad, CJK fonts.
  Not a fluxbox/xterm console.
* Pleasant solid dark wallpaper via XFCE backdrop defaults.
* x11vnc on port 5900 without password. The provider publishes it on `127.0.0.1` only
  (local daemon) and the console relays it through `/envs/{id}/desktop?kind=vnc`.
* Image tag is versioned (`DesktopImage`, e.g. `:0.1.20`) so content changes force a rebuild;
  `:latest` is also applied as an alias when the server builds the image.
* Custom desktop images work too: set `image` + `desktop: true`; the image must run its
  own VNC server on 5900 (the provider keeps the image's ENTRYPOINT/CMD in desktop mode).
