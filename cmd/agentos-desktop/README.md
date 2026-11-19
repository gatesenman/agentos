# agentos-desktop → `AgentOS.exe` (owned by M6)

AgentOS.exe is the desktop shell for AgentOS. It shows the embedded web console (`web/static`) as a native
Windows 11 style application: a frameless WebView2 window with the UI's own title bar, navigation rail and
status bar.

## Build (for packaging, module D)

```sh
# Windows (the release artifact). cgo-free; -H=windowsgui gives a GUI exe with no console window.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-H=windowsgui -s -w -X main.version=$VERSION" -o dist/AgentOS.exe ./cmd/agentos-desktop

# Linux / macOS (optional launcher; opens a Chromium/Edge --app window or the default browser)
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o dist/agentos-desktop ./cmd/agentos-desktop
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o dist/agentos-desktop ./cmd/agentos-desktop
```

- Ship `AgentOS.exe` **next to** `agentos.exe` (same directory). Installer shortcuts (Start menu / desktop)
  should point to `AgentOS.exe`. The icon is embedded via `agentos.ico` at runtime (window + taskbar).
  For an icon on the .exe file itself, D may add a `.syso` resource (e.g. `go-winres` / `rsrc`) to this
  package; nothing else needs to change.
- No WebView2 SDK or DLLs are needed: go-webview2 loads the system WebView2 runtime (bundled with Windows 11,
  Evergreen runtime on Windows 10). The installer may chain the Evergreen bootstrapper
  (`https://go.microsoft.com/fwlink/p/?LinkId=2124703`).

## Flags / environment

| flag | env | default | meaning |
|---|---|---|---|
| `--port` | `AGENTOS_PORT` | 7800 | port of the local server |
| `--data` | `AGENTOS_DATA` | (agentos default) | `--data` passed to a server this app spawns |
| `--server` | `AGENTOS_SERVER_URL` | | open this (remote) server; never spawn |
| `--agentos` | | sibling `agentos[.exe]` | path of the server binary |
| `--service` | | `AgentOS` | Windows service started (SCM) if installed but stopped |
| `--keep-server` | | false | leave a spawned server running after the window closes |
| `--browser` | | false | force the Edge/Chromium `--app=` window |
| `--debug` | | false | WebView2 devtools + default context menu |

## Behaviour

1. **Single instance**: named mutex `Local\AgentOSDesktop` (flock on Linux/macOS). A second launch finds the
   window (class `webview`, title `AgentOS`), restores and focuses it, then exits.
2. **Server**: `GET http://127.0.0.1:<port>/api/v1/health`. If nothing answers: start the Windows service
   `--service` if installed, otherwise spawn `agentos server --listen 127.0.0.1:<port> [--data …]` hidden
   (`CREATE_NO_WINDOW`), log to `%AppData%\AgentOS\server.log`, wait up to 30 s.
3. **Window**: WebView2, frameless (caption removed in `WM_NCCALCSIZE`; resize borders,
   Aero Snap by dragging and maximize keep working), min size 1100×700, size/position/maximized remembered in
   `%AppData%\AgentOS\window.json`, title `AgentOS`, icon, per-monitor-v2 DPI, WebView2 user data in
   `%AppData%\AgentOS\WebView2`. Loads `http://127.0.0.1:<port>/?app=1#/`. The default browser context menu and devtools
   are off unless `--debug`.
4. **Title bar bindings** (JS, injected before navigation; the UI shows its min/max/close buttons only when
   these exist): `agentosWinMinimize()`, `agentosWinToggleMaximize() → bool`, `agentosWinIsMaximized() → bool`,
   `agentosWinClose()`, `agentosWinDrag()` (mousedown on the drag region), `agentosOpenExternal(url)`,
   `agentosShellInfo() → {shell, version, webview2, tray, backdrop, build}`, `agentosSetTheme(dark bool)` (the UI calls it on
   load and on every theme change), `agentosSetBackdrop(kind)` (Mica mode only).
4a. **Rendering / window frame** (`backdrop_windows.go`): the WebView2 is **opaque** by default — its
   `DefaultBackgroundColor` is the theme base colour (#202020 dark / #f3f3f3 light; `WEBVIEW2_DEFAULT_BACKGROUND_COLOR`
   before creation, `ICoreWebView2Controller2.put_DefaultBackgroundColor` on theme change), the host window erases with the
   same colour, and WM_SIZE / WM_DPICHANGED / theme changes re-fit the controller (`put_Bounds` +
   `NotifyParentWindowPositionChanged`) and force a full repaint (`RedrawWindow` RDW_ALLCHILDREN|RDW_UPDATENOW).
   Rounded corners (Win11) and a dark/light DWM frame always follow the UI theme.
   **Mica is opt-in**: set `AGENTOS_MICA=1` (Windows 11) for a transparent WebView2 over the Mica system backdrop
   (`DWMWA_SYSTEMBACKDROP_TYPE`, sheet-of-glass frame) and translucent UI surfaces. It was the 0.1.0 default and produced
   white/grey ghost rectangles on some machines, so it is off by default.
5. **WebView2 runtime missing**: native MessageBox — *Yes* opens the runtime download page, *No* falls back to
   an Edge `--app=` window (own profile in `%AppData%\AgentOS\browser-profile`).
6. **Close**: saves placement. If a tray process holds mutex `Local\AgentOSTray` the window only hides
   (the tray owns the server); otherwise the app exits and stops a server *it* spawned (agents are cleaned
   up by the supervisor's job objects) unless `--keep-server`. A service or pre-existing server is never stopped.
7. **Linux / macOS**: Edge / Chrome / Chromium `--app=URL` (own `--user-data-dir`, waits for the window
   to close), otherwise `xdg-open` / `open` and waits for Ctrl+C.

Logs: `%AppData%\AgentOS\desktop.log` (`~/.config/AgentOS/` on Linux, `~/Library/Application Support/AgentOS/` on macOS).
