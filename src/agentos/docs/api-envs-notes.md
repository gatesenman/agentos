# Environments API — implementation notes (v1, additive to the frozen docs/api-envs.md)

Nothing here changes frozen semantics; these are extra fields/behaviours UI and providers can rely on.

## EnvView
`Record` + `status` + `capabilities` + `provider_info`, plus:
- `state` mirrors the live `status.state` (the stored record keeps the *desired* state: `stopped` envs
  are not auto-started).
- `secrets_set: ["password", ...]` — names of stored `x-secret` fields (values are never returned).
- `busy: "provisioning" | "installing"` while an async operation runs.
- `capabilities` always contains `agents` when `node_id` is set; for Agent OS envs `desktop.vnc` /
  `desktop.screen` follow what the connected node actually offers. The built-in env (`id:"local"`,
  `labels.builtin:"true"`) has no `lifecycle` and cannot be deleted (409 `builtin`) or stopped.
- `POST /envs` returns **201**. Reserved label `builtin` is ignored on create/patch.

## Secrets
Top-level config fields with `"x-secret": true` in the provider schema are AES-256-GCM sealed
(key `<data>/server/secret.key`, AAD = env id) and stripped from every response.
`PATCH config`: omitted secret fields keep their stored value; `null` clears one.

## Errors
`409 unsupported` (capability missing), `409 node_offline`, `409 busy`, `409 already_installed`,
`400 invalid_config`, `502 env_error` / `test_failed` (provider error), `404 not_found` (also missing files),
`501 unsupported` (install-agentos without a registered installer).
`POST /envs/{id}/test` returns EnvView on success and `502 test_failed` with the message on failure.

## join / install-agentos
`POST /envs/{id}/join[?server_url=]` → `{token, server_url, commands:{windows,linux,macos}, service_commands:{...}}`.
`server_url` = config.server_url, else `?server_url`, else the request's base URL. `service_commands`
add `--install-service` (auto-start). Calling join again revokes the previous token and node.
`POST /envs/{id}/install-agentos[?server_url=]` runs the registered `env.Installer` (internal/env/install.go)
asynchronously; progress in env logs; `node_id` is set when the node connects.

## exec / desktop
`WS /envs/{id}/exec`: when `tty` is not set stdout and stderr are both sent as binary frames (interleaved).
Extra client text message `{"type":"eof"}` closes stdin. After `exit` the server sends a normal close.
`WS /envs/{id}/desktop`: for envs with an online node this is exactly `/nodes/{node_id}/desktop`
(same params and framing, incl. text frames of the screen protocol); otherwise the provider's raw byte
stream is relayed as binary frames.

## metrics
`{latest: Metrics|null, samples:[{t, cpu_percent, mem_used, mem_total, disk_used, disk_total}]}` —
sampled every 10s, 1h history in memory.

## Downloads (no auth)
`GET /download/agentos-<os>-<arch>[.exe]` from the dist dir (`agentos server --dist DIR`,
`AGENTOS_DIST`, default `<exe dir>/dist`); the running binary serves its own platform as fallback.
`GET /download/install.sh`, `/download/install.ps1`: join scripts.

## Events
`env.created` / `env.updated` (EnvView), `env.deleted` `{id}`, `env.log` `{id,t,level,text}`.
`env.updated` is published on lifecycle actions, node connect/disconnect and status changes (10s sampler).

## Phase 2b additions (docker, ssh, bootstrap, stacks)
- `capabilities` are now **per environment** (optional `env.CapabilityReporter`, internal/env/extensions.go):
  docker has `desktop.vnc` only with `desktop:true`; ssh has `lifecycle` only when `start_command` or
  `stop_command` is set (DELETE is always allowed except for the built-in env). Grey UI actions by
  `EnvView.capabilities`, not by `provider_info`/`GET /providers`.
- Provider-written config (docker `container_id`, `vnc_port`; ssh `host_key`, `os_detected`) is persisted
  after Create and shown in `config` (read-only fields in the schema).
- Provider progress (image pull, container creation) and stack installs (`[stack:<id>] ...`) appear in
  `/envs/{id}/logs` and `env.log` events.
- `install-agentos` sets `node_id` **immediately** (pre-seeded identity); the env becomes agent-capable
  (`agents` cap) at once, and the node shows online once it connects (`/nodes/{node_id}` status, `env.updated`).
  For local docker the server URL is rewritten to the container's bridge gateway.
- Docker `DELETE` without `purge=1` only stops the container (kept, labelled `agentos.env=<id>`);
  `purge=1` removes it with its anonymous volumes. SSH delete never touches the host.
- Docker status `error` with message "container missing" when the container was removed outside AgentOS.
- Remote `tcp://` docker daemons: VNC is published on the daemon's 127.0.0.1 only and the desktop is
  relayed through `docker exec` (bash /dev/tcp) — no unauthenticated VNC port on the network.
- Stacks: `GET /stacks` (14), `GET /envs/{id}/stacks` (detection: os + per-stack installed/version,
  `installing`), `POST /envs/{id}/stacks/{stack}/install` → 202; on success `Record.stack` is set and
  `env.updated` is published.

## Real desktops for every environment type (virtual desktop)
| type | desktop | how |
|---|---|---|
| built-in 本机 | `desktop.vnc` → `agentos server --vnc ADDR` (the machine's own VNC server), `desktop.screen` → built-in stream of the server's `$DISPLAY` (Linux X11) / the interactive session (Windows: GDI capture + SendInput; VNC optional) | unchanged |
| extra `agentos-local` | config `"desktop": "virtual"` → the instance runs `agentos node --desktop virtual` | own Xvfb display + window manager + xterm + x11vnc on 127.0.0.1:<free port>; VNC **and** screen stream use it; agents/exec of that instance inherit its `DISPLAY` |
| `agentos-remote` | config `"desktop": "virtual"` → join commands get `--desktop virtual` (`install.sh --desktop virtual`, env `AGENTOS_DESKTOP=virtual`) | same, on the remote Linux node; persisted in `node.json` by `--install-service`. Ignored on Windows nodes (screen stream). |
| `docker` | config `"desktop": true` → image `localhost/agentos-desktop:0.1.20` (Ubuntu XFCE); or `image: localhost/agentos-debian-desktop:0.1.22` (Debian 13 XFCE+Plank) | **built automatically** from embedded `images/desktop` or `images/desktop-debian` on first use (progress in `/envs/{id}/logs`); Xvfb + **XFCE** (+ Plank on Debian) + x11vnc :5900, published on 127.0.0.1 only |
| `hyperv` | Linux 客户机 VNC（IP:vnc_port）中继；Windows 客户机用 RDP/mstsc/vmconnect（Status.Extra） |
| `ssh` | config `"virtual_desktop": true` (+ `vnc_port`, default 5900) | when nothing listens on remote 127.0.0.1:`vnc_port`, the provider starts Xvfb (first free display ≥ :50) + WM + xterm + `x11vnc -localhost -rfbport vnc_port` for the ssh user (detached; display number in `~/.agentos/vdesk/display`), then relays VNC over SSH `direct-tcpip` |

UI: the wizard should offer a **虚拟桌面** toggle: `agentos-local` / `agentos-remote` → `config.desktop = "virtual" | "host"`
(schema enum with `x-enum-titles`; rendered by the wizard from config_schema), `ssh` → `config.virtual_desktop: true`, docker keeps `desktop: true` (no longer needs a manual build).

**Linux node prerequisites for the virtual desktop** (Debian/Ubuntu): `apt install xvfb x11vnc fluxbox xterm xdotool`
(openbox or twm also work as the window manager; xdotool is only needed for scripting/tests). Missing programs make
`agentos node --desktop virtual` fail at start with an explicit message, and `POST /providers/agentos-local/test` with
`desktop: virtual` reports them.
