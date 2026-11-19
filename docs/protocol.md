# Server ⇄ Node protocol (v1)

Defined in `internal/api/protocol.go` (FROZEN – changes need lead sign-off, bump `ProtocolVersion` if breaking).

## 1. Control channel
`GET /api/v1/node/connect` upgraded to WebSocket, `Authorization: Bearer <node token>`.
One JSON `Envelope` per text frame:

```json
{"type":"agent.start","id":"r12","payload":{"id":"<agent>"}}          // request (server→node)
{"type":"response","reply_to":"r12","payload":{...}}                  // success
{"type":"response","reply_to":"r12","error":"unknown agent"}          // failure
{"type":"agent.status","payload":{...}}                               // event (no id)
```

Handshake: node sends `hello` (HelloPayload: node_id, name, hostname, os, arch, version, capabilities,
agents[] runtime status, protocol_version) within 15s → server replies `welcome` (or `{"type":"error","error"}`
and closes) → server sends `agent.sync` with the node's full desired spec list.
A second connection with the same node_id replaces the first.

Keepalive: both sides send WS pings every 30s; read deadline 90s. Node reconnects with exponential backoff
1s→30s (reset after a session lasted >30s).

## 2. Messages
| type | dir | payload | response payload |
|---|---|---|---|
| hello | N→S | HelloPayload | (welcome) |
| welcome | S→N | WelcomePayload | – |
| agent.sync | S→N | SyncPayload — full set; node applies all, removes unknown | null |
| agent.apply | S→N | AgentSpec | AgentStatus |
| agent.remove | S→N | AgentRef | null |
| agent.start / agent.stop / agent.restart | S→N | AgentRef | AgentStatus |
| agent.logs | S→N | LogsRequest{id,tail,since} | LogsResponse{lines} |
| tunnel.open | S→N | TunnelOpen{session_id,kind,params} | null, then node dials tunnel |
| agent.status | N→S event | AgentStatus (every state change) | – |
| agent.log | N→S event | LogBatch (flushed every 200ms, ≤500 lines) | – |
| node.stats | N→S event | NodeStats (every 5s) | – |

Requests time out after 30s server-side. Nodes handle each request in its own goroutine; writes are serialised.

## 3. Ownership of state
- Server (SQLite) owns **specs**; node keeps runtime state only (M1 may cache specs on disk for offline autostart).
- Node owns **runtime status** and **logs** (ring + `<node-data>/logs/<agent>.log`); server keeps a 1000-line ring per agent for offline viewing.
- Agent workspace: `<node-data>/agents/<agent-id>/workspace` (env `AGENTOS_WORKSPACE`, `AGENTOS_AGENT_ID`, `AGENTOS_AGENT_NAME`).

## 4. Tunnels (remote desktop and future data streams)
1. Browser opens `WS /api/v1/nodes/{id}/desktop?kind=...`.
2. Server creates random `session_id`, sends `tunnel.open` to node.
3. Node dials `GET /api/v1/node/tunnel/{session_id}` (node token) within 15s, hands the socket to the
   `desktop.Provider` for `kind`.
4. Server upgrades the browser socket and relays frames 1:1 preserving text/binary type until either side closes.

## 5. `screen` tunnel protocol (built-in fallback, module M5)
Node → browser:
- **Text** `{"type":"hello","width":1920,"height":1080,"displays":1,"format":"jpeg","display":0,"fps":8,"quality":60,"scale":1.0,"input":true,"bounds":[{"x":0,"y":0,"w":1920,"h":1080}],"platform":"windows"}`
  — first frame (and re-sent after a `config` that changes display/scale). `width/height` = remote pixels of the
  selected display (mouse x/y space); `input:false` = view-only (injection unavailable); `bounds` = all displays.
- **Binary** — one complete JPEG image per frame (full frame; M5 may add dirty-rect mode via a
  `{"type":"rect",x,y,w,h}` text frame immediately preceding the binary frame).
- **Text** `{"type":"error","message":"..."}` — also sent for malformed client messages (session continues).
- **Text** `{"type":"pong","t":<echoed>}` — reply to a client `ping`.
- When the screen is unchanged a keyframe is still re-sent every `keyframe_ms` (default 500).

Browser → node (text JSON):
- `{"type":"mouse","action":"move|down|up|wheel","x":123,"y":456,"button":0|1|2,"dx":0,"dy":-120}` — x/y in remote pixels.
- `{"type":"key","action":"down|up","code":"KeyA","key":"a"}` — `code` = DOM `KeyboardEvent.code`.
- `{"type":"text","text":"你好"}` — unicode text injection.
- `{"type":"config","fps":10,"quality":70,"scale":1.0,"display":0,"keyframe_ms":500}` — all fields optional, clamped
  (fps 1–30, quality 1–100, scale 0.1–1, keyframe_ms 100–60000).
- `{"type":"ping","t":<number>}` → node answers `pong` with the same `t` (latency indicator).
- Unknown `type` values are ignored (forward compatibility); malformed JSON / invalid fields → `error` frame.
Defaults: fps 8, quality 60, scale 1.0. Params from the URL are passed in `TunnelOpen.params`.

## 6. Phase 2: exec / files over node tunnels (additive, protocol version stays 1)

Nodes advertise `exec` and `files` in `hello.capabilities` (see `internal/api/protocol.go`).

- `tunnel.open` kind `exec`, params `cmd` (JSON array), `env` (JSON object), `cwd`, `tty`=`1`, `cols`, `rows`.
  node→server: binary = output (stdout+stderr), text `{"type":"exit","code":N}` | `{"type":"error","message"}`.
  server→node: binary = stdin, text `{"type":"resize","cols","rows"}` | `{"type":"eof"}`. Unix TTY via pty; Windows without TTY (pipes).
- `tunnel.open` kind `file.get`, param `path`: node sends `{"type":"info","size","mode"}`, binary chunks, `{"type":"eof"}` (or `{"type":"error"}`).
- `tunnel.open` kind `file.put`, params `path`, `mode` (octal): server sends binary chunks then `{"type":"eof"}`; node answers `{"type":"ok","size"}` | `{"type":"error"}`. Parent dirs are created; the write is atomic (temp + rename).
- control request `fs.list` `{path}` → `{path, entries:[{name,size,dir,mode,mod_time(unix ms)}]}`.
- Relative paths / cwd resolve against `<node-data>/home`.

### Node authentication and identity
- `/api/v1/node/connect` and `/node/tunnel/*` accept the shared node token **or** a per-environment
  credential (`aosj_…`, issued by `POST /envs/{id}/join` or `install-agentos`; stored as sha256).
- A credential binds to the first node id that connects with it; other node ids are rejected with an
  `error` envelope. Re-issuing (join again) replaces it and disconnects/revokes the previous node.
- The node name is forced to the environment name. Deleting an environment revokes its node
  (shared-token reconnects are rejected too) and removes the node record and its agents.
- A node joining with the shared token and no environment is listed as an `agentos-remote`
  environment with `labels.auto="true"`.

### `agentos node` additions
- `--install-service` (or `AGENTOS_INSTALL_SERVICE=1`): saves server/token/name/vnc to `<data>/node.json`
  (0600) and installs auto-start (systemd system/user unit `agentos-node`, launchd `com.agentos.node`,
  Windows logon scheduled task "AgentOS Node"), then exits.
- When `--server`/`--token` are empty the node falls back to `<data>/node.json`.
