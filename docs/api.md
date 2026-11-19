# AgentOS REST / WebSocket API (v1)

Base path: `/api/v1`. JSON everywhere (`Content-Type: application/json`). Types are defined in
`internal/api/models.go` (field names below are the JSON names). Timestamps are RFC 3339 UTC.

## Conventions
- **Auth (browser/admin):** `POST /auth/login` sets cookie `agentos_session` (HttpOnly, 7 days) and
  returns `{"token"}`. `POST /auth/local` (loopback only) issues a session without a password for AgentOS.exe. All endpoints except `/health`, `/version`, `/auth/login`, `/auth/local`, `/auth/logout` and the
  node endpoints require the session via cookie **or** `Authorization: Bearer <token>` **or** `?token=` (WS clients).
- **Auth (nodes):** `Authorization: Bearer <node token>` on `/node/*`.
- **Errors:** non-2xx bodies are `ErrorResponse {"error": "...", "code": "..."}`. Codes: `bad_request`,
  `unauthorized`, `bad_credentials`, `rate_limited` (429), `not_found`, `node_offline` (409), `node_online` (409), `node_error` (502),
  `tunnel_failed` (502), `internal` (500).
- **Status of agents on offline nodes** is reported as `state: "unknown"`.

## Misc
| Method | Path | Response |
|---|---|---|
| GET | `/health` | `{"status":"ok","version"}` (no auth) |
| GET | `/version` | `{"version","commit","date","protocol"}` (no auth) |

## Auth & settings (module M7)
| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/auth/login` | `{"password"}` | 200 `{"token"}` + cookie; 401 `bad_credentials` |
| POST | `/auth/local` | – | 200 `{"token"}` + cookie; **127.0.0.1/::1 only** (403 otherwise). Passwordless local desktop. |
| POST | `/auth/logout` | – | 204, clears cookie |
| GET | `/auth/me` | – | `{"authenticated":true,"user":"admin"}` |
| POST | `/auth/password` | `{"old_password","new_password"}` | 204; 403 wrong old; 400 too short (<6) |
| GET | `/settings/node-token` | – | `{"token"}` |
| POST | `/settings/node-token/rotate` | – | `{"token"}` (existing nodes must reconnect with new token) |

- **Sessions** are persisted in SQLite (`sessions` table, stored as SHA-256 of the token) and survive server
  restarts. TTL 7 days, sliding: an authenticated request more than 1 h after the last renewal extends the
  expiry to now+7d and re-sends the cookie. Cookie is `HttpOnly`, `SameSite=Lax`, `Secure` when served over TLS.
- **Rate limit:** `/auth/login` and `/auth/password` allow 5 failed attempts per client IP per minute; then
  `429 {"code":"rate_limited"}` with `Retry-After` (seconds). Lockout starts at 1 min and doubles per repeat
  (max 15 min); a successful login resets it.
- `POST /auth/password` success signs out all *other* sessions.
- **WebSocket origin:** browser sockets must be same-origin (`Origin` host:port == `Host`); requests without
  `Origin` (nodes, CLI) are allowed. Extra origins (reverse proxy): env `AGENTOS_ALLOWED_ORIGINS=https://a,https://b`.
  Cross-origin upgrade → 403.
- **Offline admin CLI** (works while the server runs; DB at `<data>/server/agentos.db`):
  `agentos admin reset-password [--data DIR] [NEWPASSWORD]` (random if omitted; signs out all sessions),
  `agentos admin rotate-node-token [--data DIR]`.
- Schema is versioned (`settings.schema_version`, currently 2); a DB from a newer build refuses to open.

## Nodes (core)
| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/nodes` | – | `Node[]` (with live `status`, latest `host` stats) |
| GET | `/nodes/{id}` | – | `Node` |
| PATCH | `/nodes/{id}` | `{"name"}` | `Node` |
| DELETE | `/nodes/{id}` | – | 204; 409 `node_online` if connected. Also deletes its agent specs |

Nodes are created implicitly when a node connects with a valid token (identity = node-generated UUID).

## Agents (core)
`Agent` = `AgentSpec` fields + `"status": AgentStatus`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/agents[?node_id=]` | – | `Agent[]` |
| POST | `/agents` | `AgentSpec` (no id) | 201 `Agent`. Required: `name`, `command`, `node_id`. Default `restart:"never"`. Pushed to node if online; `auto_start` starts it |
| GET | `/agents/{id}` | – | `Agent` |
| PUT | `/agents/{id}` | full `AgentSpec` | `Agent`; running process keeps old config until restart. Changing `node_id` moves it |
| DELETE | `/agents/{id}` | – | 204 (stops process; workspace dir kept on node) |
| POST | `/agents/{id}/start` | – | `Agent`; 409 `node_offline`; 502 `node_error` (e.g. spawn failed) |
| POST | `/agents/{id}/stop` | – | `Agent` (SIGTERM process group → SIGKILL after 10s) |
| POST | `/agents/{id}/restart` | – | `Agent` |

## Logs (module M3)
| Method | Path | Response |
|---|---|---|
| GET | `/agents/{id}/logs?tail=200&since=&q=` | `{"lines": LogLine[]}` oldest first (node authoritative, server ring fallback; tail ≤ 5000) |
| GET | `/agents/{id}/logs/download?tail=5000&since=&q=` | `text/plain; charset=utf-8` attachment (`Content-Disposition: attachment`), one line per entry: `<RFC3339Nano time> [<stream>] <text>`; default/max 5000 newest lines |
| WS | `/agents/{id}/logs/stream?tail=200&since=&q=` | server→client text frames: `Event{type:"agent.log", payload: LogLine}`; first the tail, then live. Server dedupes by `seq` (no line sent twice, seq strictly increasing unless the node's log was wiped) |

Query parameters (all optional, all three endpoints):
- `tail` – max lines returned (1–5000; default 200, download 5000).
- `since` – either a **seq number** (only lines with `seq > since`) or an **RFC 3339 time** (only lines newer than it).
- `q` – case-insensitive substring filter on `text`. With `q` or a time `since`, the newest 5000 lines are scanned, then the newest `tail` matches returned. On the stream, `q`/`since` also filter live lines.
- Invalid `tail`/`since` → 400 `bad_request`.

`seq` is monotonic per agent **across node restarts** (persisted in the node log file). Node storage:
`<node-data>/logs/<agent>.log`, rotated at 10 MB, keeping `.1`…`.5` (≤ 60 MB per agent); after a node restart
the newest 2000 lines are reloaded on first access. Lines > 64 KB are split, invalid UTF-8 is replaced by U+FFFD,
ANSI escapes pass through unchanged (UI renders/strips them).

## Stats (module M2)
| Method | Path | Response |
|---|---|---|
| GET | `/nodes/{id}/stats[?since=RFC3339]` | `{"latest": HostStats|null, "history": HostStats[]}` |
| GET | `/agents/{id}/stats[?since=RFC3339]` | `{"latest": ProcStats|null, "history": ProcStats[]}` |

- History is an in-memory ring per node / agent (120 samples, node reports every 5s ≈ 10 min), oldest first,
  lost on server restart. Unknown ids return `{"latest":null,"history":[]}`.
- `since` (RFC 3339, fractional seconds allowed, exclusive) limits `history` to samples with `time > since`;
  `latest` is always the newest sample regardless of `since`. Pollers pass the last `time` they saw. Invalid
  `since` → 400 `bad_request`.
- `HostStats`: `cpu_percent` 0–100 whole machine (delta since previous sample), `cpu_count` (logical),
  `mem_total`/`mem_used` (used = total − available, bytes), `disk_total`/`disk_used` (volume holding the node
  data dir), `uptime_sec`, `load1` (omitted on Windows).
- `ProcStats`: aggregate over the agent's process tree (descendants by parent pid, plus the agent's process
  group on Unix): `cpu_percent` (100 = one core, may exceed 100; first sample = lifetime average),
  `mem_rss` (bytes, summed), `threads`, `procs`.
- Agent stats history is dropped when the agent is deleted; node host history when the node is deleted.

## Events (core)
`WS /events` – server→client JSON `Event {type, time, payload}`:
`node.status` (Node), `node.removed` ({id}), `node.stats` (NodeStats), `agent.status` (AgentStatus),
`agent.spec` (AgentSpec), `agent.removed` ({id}). (`agent.log` only on per-agent log streams.)
Server pings every 30s. Slow clients drop events (UI should refetch on reconnect).

## Remote desktop (module M4 / M5)
`WS /nodes/{id}/desktop?kind=vnc|screen[&addr=..][&quality=1-100&fps=1-30&display=0&scale=0.1-1]`
- 409 `node_offline`, 502 `tunnel_failed` before upgrade; 400 `bad_request` for an unknown `kind`.
- `kind=vnc`: binary frames carry raw RFB bytes in both directions (noVNC: `new RFB(el, ws)`; subprotocol
  `binary` accepted). Optional `addr` (`host:port`, `port` or `:port`) selects another VNC server on the node;
  the node only allows loopback targets (`127.0.0.0/8`, `::1`, `localhost`) or exactly its configured `--vnc`
  address. Other query params are not forwarded for vnc.
- `kind=vnc` errors after upgrade (VNC server down, target refused, not an RFB server): the server closes the
  browser socket with **close code `4502`** and a zh-CN human-readable reason (≤123 bytes). Internally the node
  reports this as one text frame `{"type":"error","message"}` on the tunnel (same shape as protocol §5).
- `kind=screen`: see `docs/protocol.md` §5 (other query params forwarded as `TunnelOpen.params`).
- UI hook (`web/static/js/pages/desktop.js`): `DESKTOP_CLIENTS = {vnc: VncClient, screen: ScreenClient}`; all
  clients follow `new Client(containerEl, apiPath, opts?)`, `.connect()`, `.disconnect()`.
