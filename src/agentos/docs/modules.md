# Work breakdown – phase 2 modules

Phase 1 (done) delivered the contracts and a working vertical slice. Remaining work is split into
**8 independent modules**. Each module owns a disjoint set of paths; touching anything else requires a
request to the lead/integrator.

## Global rules (all modules)
1. **Frozen contracts** – `internal/api/**`, and the interfaces in `internal/store/store.go`,
   `internal/auth/auth.go` (Authenticator), `internal/supervisor/supervisor.go`, `internal/logs/logs.go`
   (Store), `internal/metrics/metrics.go` (Collector), `internal/desktop/desktop.go` (Provider/Conn).
   Do not change signatures. Need a change? Write it in your report; the lead applies it.
2. **Core files (lead only):** `internal/api/**`, `internal/node/**`, `cmd/agentos/main.go`,
   `internal/server/{server.go,hub.go,events.go,util.go,handlers_nodes.go,handlers_agents.go}`.
3. Server routes: each module registers its routes inside its own `routes<Area>()` in its own
   `internal/server/handlers_<area>.go` file. New endpoints must be added to *your module's section* of
   `docs/api.md` (only edit your section).
4. **Dependencies:** `go get` what you need; go.mod/go.sum conflicts are resolved by `go mod tidy` at merge.
   **CGO_ENABLED=0 must keep working for windows/amd64, linux/amd64, darwin/arm64** (use build tags +
   stubs where a platform lacks a pure-Go implementation). No CDN assets in the UI (offline use).
5. Every module must pass before hand-off:
   `export PATH=/usr/local/go/bin:$PATH; go vet ./... && GOOS=windows go vet ./... && GOOS=darwin GOARCH=arm64 go vet ./... && go test -race ./... && make dist`
6. Language: UI text zh-CN; code/comments English; system log lines bilingual like existing ones.
7. Work on a branch `m<N>-<name>`, commit only your owned paths.

---

## M1 – Agent supervisor & isolation
**Owns:** `internal/supervisor/**`
**Implements:** `supervisor.Supervisor` (+ `supervisor.New(Options)`).
**Tasks**
- Windows: Job Object per agent (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`), kill whole tree on stop; graceful
  stop via `CTRL_BREAK_EVENT` then kill after `StopTimeout`. Unix: keep process-group SIGTERM→SIGKILL.
- Persist specs to `<DataDir>/agents/<id>/spec.json` on Apply, delete on Remove; `New()` loads them and
  starts those with `AutoStart` (node boots offline still runs agents).
- Restart policy: exponential backoff (delay, 2×… max 5 min), restart counter reset after 60s stable run,
  `MaxRestarts` honoured, `StateBackoff` while waiting.
- Spawn hygiene: resolve command via PATH *with the spec env*, clear error messages (`Error` field), workdir
  must stay inside workspace unless absolute; optional env var `AGENTOS_PORT_BASE` reserved – not now.
- Optional (stretch): per-agent memory/CPU limits via Job Object (Windows) / cgroups v2 (Linux) behind
  spec env keys `AGENTOS_LIMIT_MEM_MB`, `AGENTOS_LIMIT_CPU_PCT` (no contract change).
**Acceptance tests** (in `internal/supervisor/*_test.go`, run with `-race`)
- start/stdout+stderr capture/exit code; stop of `sh -c 'sleep 100 & sleep 100'` kills the child too
  (verify no orphan pid); restart on-failure with backoff and MaxRestarts; always-restart; spec persistence:
  new Supervisor over same DataDir autostarts an AutoStart agent; Remove deletes spec.json, keeps workspace.
- Windows-only tests behind `//go:build windows` (compile-checked with `GOOS=windows go vet`).
**Verify:** `go test -race ./internal/supervisor/ ./test/e2e/`

## M2 – Metrics / stats
**Owns:** `internal/metrics/**`, `internal/server/handlers_stats.go`
**Implements:** `metrics.Collector` via `metrics.New(diskPath)` (suggest `github.com/shirou/gopsutil/v4`, pure Go).
**Tasks**
- `Host()`: CPU% (delta since last call), cpu_count, mem total/used, disk total/used of the volume holding
  `diskPath`, uptime, load1 (non-Windows).
- `Process(pid)`: aggregate the process tree rooted at pid (CPU%, RSS, threads, procs count).
- Server side: keep history (already 120 samples); add `?since=<RFC3339>` filter to both stats endpoints;
  drop agent history on agent removal (expose `func (s *Server) dropAgentStats(id string)` – lead wires it).
**Acceptance tests**
- Unit: Host returns mem_total>0, cpu_count>0, disk_total>0 on linux; Process on a busy-loop child reports
  cpu_percent>50 on 2nd sample and mem_rss>0; Process on dead pid returns error.
- E2E (this box): run `make run`, create agent `sh -c 'while :; do :; done'`, after 12s
  `GET /api/v1/agents/{id}/stats` → latest.cpu_percent>50, `GET /api/v1/nodes/{id}` → host.mem_total>0.
**Verify:** `go test -race ./internal/metrics/ ./internal/server/` + curl above.

## M3 – Log streaming & storage
**Owns:** `internal/logs/**`, `internal/server/handlers_logs.go`
**Implements:** `logs.Store` via `logs.NewStore(dir, ringSize)`, `logs.Ring`.
**Tasks**
- Size-based rotation of `<dir>/<agent>.log` (10 MB × 5 files); on first access after node restart reload
  the last ringSize lines from file and continue `Seq` from the last value (persist seq in the file line
  format or a sidecar) so seq is monotonic across node restarts.
- Long-line safety (split > 64 KB), invalid UTF-8 replacement, ANSI escape passthrough (UI renders/strips).
- Server: `GET /agents/{id}/logs?tail=&since=&q=` (substring filter), `GET /agents/{id}/logs/download`
  (text/plain attachment proxied from node via a new request type? → **no protocol change**: use
  `agent.logs` with tail=5000), dedupe initial tail vs live stream by seq in the WS handler.
**Acceptance tests**
- Unit: ring overflow order; rotation produces ≤6 files and keeps newest lines; reload after reopening store
  returns previous lines and seq continues; 10k lines/s append benchmark doesn't block (<50 ms p99).
- E2E: agent printing 20k lines → `/logs?tail=100` returns exactly 100 newest; WS stream receives live
  lines with strictly increasing seq and no duplicates.
**Verify:** `go test -race ./internal/logs/ ./internal/server/ ./test/e2e/`

## M4 – Remote desktop: VNC proxy + noVNC client
**Owns:** `internal/desktop/vnc/**`, `internal/server/handlers_desktop.go`, `web/static/js/pages/desktop.js`,
`web/static/vendor/novnc/**`
**Implements:** `desktop.Provider` (kind `vnc`). Tunnel plumbing (hub.OpenTunnel / Pipe / node dial) already works.
**Tasks**
- Vendor noVNC (latest 1.x, ESM `core/` + licence) into `web/static/vendor/novnc/` – no build step.
- `desktop.js`: node picker (exists), connect via `new RFB(el, wsURL('/nodes/'+id+'/desktop?kind=vnc'))`,
  VNC password prompt (`credentialsrequired`), scale-to-fit/1:1, fullscreen, Ctrl+Alt+Del, clipboard
  paste box, view-only toggle, reconnect, status bar; fall back to the M5 `ScreenClient` when the node only
  has `desktop.screen` (button already present – keep the ScreenClient import contract).
- Node: restrict `params.addr` to loopback unless `--vnc` configured otherwise; re-check `Available()` on
  demand; clear error text to browser if VNC is down.
- Docs section in README: how to run TightVNC/UltraVNC on Windows, x11vnc on Linux, Screen Sharing on macOS.
**Acceptance tests**
- Go test: fake RFB TCP server → tunnel through httptest server+node → browser-side WS receives
  `RFB 003.008\n` and echo of client bytes.
- Manual/automated on this box (a VNC server listens on 127.0.0.1:5900): headless Chrome (see
  `scripts/ui-smoke.mjs` pattern in M6) opens `#/nodes/<id>/desktop`, clicks VNC, screenshot shows the remote
  desktop canvas (non-black pixels).
**Verify:** `go test ./internal/desktop/... ./test/e2e/` + screenshot.

## M5 – Built-in screen stream + input injection (no VNC needed)
**Owns:** `internal/desktop/screen/**`, `web/static/js/desktop/screen-client.js`
**Implements:** `desktop.Provider` (kind `screen`) and the browser `ScreenClient` class
(`new ScreenClient(containerEl, apiPath)`, `.connect()`, `.disconnect()`). Protocol: `docs/protocol.md` §5.
**Tasks**
- Capture with `github.com/kbinani/screenshot` (pure Go on windows & linux/X11; darwin needs cgo → stub
  with `Available()=false` when `!cgo`), JPEG encode (quality/scale/fps params), skip unchanged frames
  (hash), multi-monitor via `display` param.
- Input: Windows `SendInput` via `golang.org/x/sys/windows` (mouse move/buttons/wheel, keys by DOM `code`
  → VK map, unicode text via `KEYEVENTF_UNICODE`). Linux/X11: XTEST via `github.com/jezek/xgb` (stretch).
- `Available()`: true on Windows when a desktop session is accessible (note: a Windows *service* runs in
  session 0 and cannot capture – document; the all-in-one `agentos server` started from the user session works).
- Browser client: `<canvas>` rendering with createImageBitmap, coordinate scaling, pointer/keyboard/wheel
  capture, fps/quality controls, latency indicator.
**Acceptance tests**
- Unit: key-code map coverage (A–Z, 0–9, F1–F12, arrows, modifiers), JSON message parsing, frame
  throttling respects fps; fake capturer interface for tests.
- On this box (X display available, `DISPLAY=:0` or as found): `screen` tunnel yields a hello + ≥3 JPEG frames
  in 2s that decode with `image/jpeg`.
- `GOOS=windows go vet ./...` clean; cross-build stays CGO-free.
**Verify:** `go test ./internal/desktop/screen/` + probe client.

## M6 – Web UI (dashboard, agent detail, node pages)
**Owns:** `web/static/**` except M4/M5 files above; `web/embed.go`; `scripts/ui-smoke.mjs` (new).
**Implements:** pages per router in `web/static/js/app.js` (contract: page module default export
`{title, render(el, params) → cleanup?}`); uses only documented API (`docs/api.md`).
**Tasks**
- Dashboard polish: live updates via `/events`, node cards with CPU/mem sparkline (`/nodes/{id}/stats`),
  "在线" instead of stale last_seen for online nodes, agent table filters/search, bulk start/stop.
- Node detail page `#/nodes/:id` (info, rename, delete offline node, agents on node, stats charts).
- Agent detail: CPU/mem charts (tiny canvas chart lib written in-repo, no CDN), log search/highlight, ANSI
  colour rendering, download logs (when M3 endpoint exists), duplicate agent, env var editor rows.
- Agent form: templates (Python script, Node script, shell command), validation messages.
- i18n: move strings to `js/i18n.js` with zh-CN default and `en` switch (stored in localStorage).
- Responsive layout; favicon/brand; empty states; confirm dialogs (styled modal, not `confirm()`).
**Acceptance tests**
- `node --experimental-websocket scripts/ui-smoke.mjs` (headless Chrome via CDP – pattern from phase 1)
  logs in, visits every route, asserts zero console errors/exceptions, saves screenshots to `/tmp/ui-*.png`.
- Creating an agent through the form, starting it, seeing log lines appear within 2s (automated in smoke).
**Verify:** `make run` then the smoke script; review screenshots.

## M7 – Auth, security & persistence
**Owns:** `internal/auth/**`, `internal/store/**`, `internal/server/handlers_auth.go`, `cmd/agentos/admin.go`
**Implements:** `auth.Authenticator` (+ `auth.New`, `auth.CheckOrigin`, `auth.TokenFromRequest`),
`store.Store` (+ `store.OpenSQLite`).
**Tasks**
- Sessions persisted in SQLite (`sessions` table, expiry, sliding renewal) – survive server restart.
- Login rate limiting (per IP, e.g. 5/min with backoff), constant-time compares, cookie `Secure` when TLS.
- `CheckOrigin`: same-origin for browser sockets; allow requests without Origin (nodes).
- Versioned schema migrations (`schema_version` setting), store tests for every method, concurrent access test.
- `agentos admin reset-password|rotate-node-token --data DIR` (offline CLI).
- Optional: per-node tokens (table `node_tokens`) – only if it needs no contract change (else propose).
**Acceptance tests**
- Unit: login ok/bad, session expiry, persisted session valid after re-open, rate limit triggers 429
  (add `rate_limited` code to api.md auth section), node token rotate invalidates old, CheckOrigin cases,
  migration from empty DB and from v1 DB.
- E2E: restart server binary → existing cookie still works.
**Verify:** `go test -race ./internal/auth/ ./internal/store/ ./test/e2e/`

## M8 – Packaging, Windows service, cross-builds, CI & E2E
**Owns:** `packaging/**`, `scripts/**` (except `scripts/ui-smoke.mjs`), `Makefile`, `cmd/agentos/service_*.go`,
`cmd/agentos-tray/**` (new), `test/e2e/**`, `.github/workflows/**`, `README.md` (build/install sections)
**Tasks**
- Windows service (`golang.org/x/sys/windows/svc` + `svc/mgr` + eventlog): `agentos service
  install|uninstall|start|stop|run`; `run` calls `serveCtx(ctx, listen, dataDir, "", withNode, "")`.
  Install with auto-start + recovery actions (restart on failure).
- NSIS installer polish: icon (`packaging/windows/agentos.ico`, embed into exe via `go-winres`/`rsrc`
  `.syso` in `cmd/agentos/` generated by build script), upgrade-in-place (stop service/process before copy),
  run `agentos service install` section, open console after install, silent install `/S`, uninstall removes
  service; output `dist/AgentOS-Setup-<version>.exe`.
- Tray launcher `agentos-tray.exe` (nice-to-have; `github.com/getlantern/systray` or `fyne.io/systray`,
  CGO-free on Windows): start/stop local server, open console, show status; added to installer + autostart.
- Cross-builds + checksums (`dist/SHA256SUMS`), Linux tar.gz with systemd unit, macOS tar.gz with launchd plist.
- CI script `scripts/ci.sh` (vet all GOOS, test -race, build dist) and `.github/workflows/ci.yml`.
- E2E: extend `test/e2e` – real binary process test (build, start server with `--with-node`, drive REST),
  node reconnect after server restart (agents keep running, status resyncs), agent auto-restart, tunnel test
  with fake VNC server, node offline → agent state `unknown`.
**Acceptance tests**
- `make dist` produces 3 binaries + installer + SHA256SUMS; `makensis` emits no warnings.
- If `wine` is installable: silent install in wine prefix, `agentos.exe version` runs (best effort).
- `scripts/ci.sh` green locally.
**Verify:** `scripts/ci.sh && ls dist/`

---

## Integration (lead)
Merge order suggestion: M7 → M1 → M3 → M2 → M5 → M4 → M6 → M8. After each merge: `go mod tidy`,
`scripts/ci.sh` (or the command in rule 5), UI smoke. The lead wires any cross-module hooks requested in
reports (e.g. `dropAgentStats`).
