# Environments API (frozen v1, with internal/env/env.go)

All under /api/v1, same auth as the rest (cookie/bearer). JSON errors as in api.md.

## Providers
- GET  /providers                     -> [ProviderInfo]
- POST /providers/{type}/test  {config} -> {ok:true} | error

## Presets (machine = 虚拟电脑, 0.1.3+; local VMs 0.1.4+)
- GET  /env-presets[?refresh=1]      -> [EnvPreset]  {id, provider, category (agentos|container|ssh|vm), name, name_en, description, description_en,
                                        icon, base_name, config, labels?, tags?, note?, available, reason?, help_url?}
  Machine (虚拟电脑) presets only — provisioning the Agent OS instance / Docker container / SSH join / Hyper-V local VM. No bundled tool install.
  Availability is probed per provider (Docker: provider Test; 本机 SSH: TCP + key login; Hyper-V: Get-VM) and cached ~60 s; refresh=1 re-probes.
  Presets: agentos-desktop, agentos-remote, docker-desktop (Ubuntu **容器** · XFCE),
  docker-debian-desktop (Debian 13 **容器** · XFCE + Plank),
  docker-ubuntu-server, docker-debian, docker-fedora, docker-python, docker-node, ssh-local;
  **0.1.4 local VMs:** hyperv-ubuntu-2404, hyperv-windows11, hyperv-macos (always grey on Windows; alt_preset "ssh-mac"), hyperv-fedora;
  **0.1.5:** all hyperv presets are zero-touch (ISO auto-download + unattended install + generated account); new preset ssh-mac (remote Mac via SSH + Screen Sharing);
  EnvPreset may carry alt_preset (compliant alternative shown on an unavailable card).
  virtualbox still greyed (provider not shipped). See docs/local-vms.md.
  (Removed in 0.1.3: agentos-claude, docker-claude, docker-browser — use tool-presets / stacks instead.)

## Tool presets (install onto a 虚拟电脑, 0.1.3+)
- GET  /tool-presets                 -> same shape as GET /stacks (languages + Claude Code / Codex / Gemini / Aider / browser-use / OpenHands, …)
  Alias of the stacks catalog for the "在虚拟电脑上安装" UI. Install with POST /envs/{id}/stacks/{stack}/install.

## Environments
EnvView = Record (secrets stripped) + {"status": Status, "capabilities": [Capability], "provider_info": {type,name,group,kind}}
- GET    /envs                        -> [EnvView]
- POST   /envs  {name?, provider, config, stack?, labels?, preset?} -> 201 EnvView (state "creating", provisioning async; progress via events + /envs/{id}/logs)
         name omitted -> unique "<preset base_name | provider name> N"; preset fills provider/config/labels (given fields override;
         config is merged key by key).
         **0.1.3+ two-step flow:** POST /envs ONLY provisions the machine. The `stack` field is still accepted for one release
         (recorded on the EnvView as a suggestion) but is **not** auto-installed. Clients must call
         POST /envs/{id}/stacks/{stack}/install after the env is running.
- GET    /envs/{id}                   -> EnvView
- PATCH  /envs/{id} {name?, labels?, config?} -> EnvView
- DELETE /envs/{id}?purge=1           -> 204 (purge = also delete underlying container/instance data; 0.1.5: environments in state "error" are cleaned up best effort too)
- POST   /envs/{id}/start | /stop | /test       -> EnvView
- POST   /envs/{id}/install-agentos  -> EnvView (bootstraps agentos node inside; sets node_id when it connects)
- POST   /envs/{id}/join              -> {token, server_url, commands:{windows, linux, macos}}  (agentos-remote only; one-time join token)
- GET    /envs/{id}/metrics?since=RFC3339 -> {latest: Metrics, samples:[{t, ...Metrics}]}
- GET    /envs/{id}/logs?tail=N       -> [{t, level, text}]   (provisioning / lifecycle log of the env itself)
- GET    /envs/{id}/files?path=       -> [FileInfo]
- GET    /envs/{id}/files/content?path= -> raw bytes (attachment)
- PUT    /envs/{id}/files/content?path=&mode=0644  body=raw -> 204
- WS     /envs/{id}/exec?cmd=<json array, urlencoded>&tty=1&cols=120&rows=30&cwd=
         server->client: binary = output bytes (stdout+stderr merged when tty), text JSON {"type":"exit","code":N} | {"type":"error","message"}
         client->server: binary = stdin bytes, text JSON {"type":"resize","cols","rows"}
         default cmd when absent: user's shell (powershell.exe on Windows envs, /bin/sh -l else)
- WS     /envs/{id}/desktop?kind=vnc|screen   -> raw relay, same framing as /nodes/{id}/desktop
- GET    /envs/{id}/screenshot?w=800  -> image/png (0.1.5; providers implementing env.Screenshotter, e.g. hyperv — works while provisioning)
- POST   /envs/{id}/open {"kind":"console"|"rdp"} -> 204 (0.1.5; opens a native viewer on the server host: vmconnect / mstsc)

## Agents
Agents keep using /agents with node_id. For an env with node_id, the UI creates agents with node_id = env.node_id.
GET /agents?env_id= filters by env (server maps env -> node_id).

## Stacks (technology templates = 环境与 AI 工具)
- GET /stacks -> [{id, name, icon, description, category (runtime|agent), languages:[...], setup:{windows:[[cmd...]], linux:[[...]], darwin:[[...]], docker_image}, agent_templates:[{id,name,command:[...],env:{}}], detect:[cmd...]}]
- GET /tool-presets -> same as GET /stacks (0.1.3+ catalog for "安装环境与 AI 工具")
- GET /envs/{id}/stacks -> detect installed stacks on that machine
- POST /envs/{id}/stacks/{stack}/install -> 202, progress in env logs (required after create; create no longer auto-installs)

## Events (existing /events WS)
New event types: "env.created", "env.updated" (payload EnvView), "env.deleted" {id}, "env.log" {id, t, level, text}

## Provider types
agentos-local (本机 Agent OS 实例, group "Agent OS"), agentos-remote (远程 Agent OS, group "Agent OS"),
docker (group "容器"), ssh (group "虚拟机"), **hyperv (group "本机虚拟机", Windows Hyper-V Gen2)**.
The built-in embedded node of `agentos server` appears as env "本机" of type agentos-local with labels.builtin=true.
