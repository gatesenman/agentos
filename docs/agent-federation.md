# Agent federation (A2A / ACP)

AgentOS can **register remote agents** and drive them from the 智能体 page alongside local (node-supervised) process agents.

| Kind | Source | Lifecycle |
|------|--------|-----------|
| `local` | Process on an AgentOS node | start / stop / restart / logs / metrics |
| `a2a` | [A2A Protocol](https://a2a-protocol.org/latest/) v1.0 | discover Agent Card → SendMessage / stream → logs |
| `acp` | Legacy [ACP](https://github.com/i-am-bee/acp) (Bee/IBM; merged into A2A under LF) | GET `/agents` → POST `/runs` → events → logs |

## A2A 1.0 (preferred)

1. Fetch Agent Card from `/.well-known/agent-card.json` (also tries legacy paths).
2. Read `supportedInterfaces[]` (`url`, `protocolBinding`, `protocolVersion`). Prefer **JSONRPC**.
3. Call JSON-RPC `SendMessage` (fallback `message/send`) or streaming `SendStreamingMessage`.
4. Map task states (`TASK_STATE_*`) to AgentOS status / log lines.

Compatible with older cards that still use top-level `url` / `preferredTransport` (v0.3).

## ACP (best-effort)

ACP development is winding down in favor of A2A. AgentOS still supports:

- `GET /agents`, `GET /agents/{name}`
- `POST /runs` (sync / async / stream)
- `POST /runs/{id}/cancel`, `GET /runs/{id}`

Use this for existing BeeACP deployments until they migrate.

## HTTP API

Base: `/api/v1/federation/agents` (auth required).

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/discover` | Probe endpoint (auto-detect A2A card vs ACP) |
| GET/POST | `/` | List / create federated agents |
| GET/PUT/DELETE | `/{id}` | Read / update / delete |
| POST | `/{id}/test` | Re-fetch card / ping |
| POST | `/{id}/invoke` | Send message / create run (`{message, stream}`) |
| POST | `/{id}/stop` | Cancel active remote task/run |
| GET | `/{id}/tasks` | In-memory task history |
| GET | `/{id}/logs` | Federation log ring |

Unified list: `GET /api/v1/agents?kind=local|a2a|acp` merges local + federated rows (`kind` field).

## Security

- Auth tokens are **AES-256-GCM sealed** at rest (`fed1:…`) using the same `secret.key` as environment secrets (`<data>/server/secret.key`).
- Tokens are **never** returned in API responses (`has_auth: true|false` only).
- Prefer HTTPS endpoints in production; Bearer token is sent as `Authorization: Bearer …`.
- Federation calls are server-side (browser never holds the remote token after save).

## UI

智能体 page:

- Filter: **全部 / 本地 / A2A / ACP**
- Toolbar: **接入外部智能体** — paste URL + optional token → Discover → Save
- Federated detail: **概览 / 对话 / 日志** (chat invokes remote; logs show task events)

## Tests

- `internal/agentfed` — mock Agent Card + JSON-RPC; mock ACP discover/run; token sealer
- `internal/store` — FedStore CRUD + migration v4
- `internal/server` — `TestFedE2ERegisterInvokeLogs` (register → invoke → logs)

Verified against **mock** A2A/ACP HTTP servers in-process. Not yet verified against a live public A2A or BeeACP deployment.
