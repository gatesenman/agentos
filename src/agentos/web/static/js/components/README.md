# AgentOS desktop UI kit (owned by M6)

Plain ES modules, no build step, no CDN. Fluent (Windows 11) look from `css/app.css`; light/dark via
`html[data-theme]`, strings via `i18n.js` (`t(key, vars)`, zh-CN default + en).

## Shell & routing (`js/app.js`)
- Frame: `.titlebar` (drag region; min/max/close shown only when the AgentOS.exe bindings `agentosWin*` exist),
  `.rail` nav (环境 / 智能体 / 模板 / 设置), `main#page`, `.statusbar` (connection, server, envs online,
  agents running, version).
- Routes: `#/envs[/:id[/:tab]]` (home), `#/agents[/:id[/:tab]]`, `#/agents/new`, `#/templates`, `#/settings`,
  `#/login`, `#/nodes/:id/desktop` (M4 page). Page module: `export default { title, render(el, params) → cleanup?,
  update?(params) }` — `update` is called instead of re-rendering when the same module stays active.
  `params.query` is the `URLSearchParams` after `?` in the hash.
- Global keys dispatched to the page's `setKeys({new, refresh, delete, find})`: Ctrl+N, F5 / Ctrl+R, Del, Ctrl+F.
- The browser context menu is suppressed except in text inputs; `.selectable` re-enables text selection.

## Data (`js/api.js`, `js/store.js`, `js/mock.js`)
- `api(method, path, body)` → JSON or throws `ApiError{status, code, message}`; 401 redirects to `#/login`.
  `isMissing(e)` is true for endpoints the server does not implement (route 404 / 405).
- `openWS(path)` → WebSocket (`binaryType = 'arraybuffer'`) or the mock socket. `on(type, fn)` subscribes to
  `/events` (pseudo events `conn`, `reconnect`).
- `store` keeps `envs / agents / nodes / providers / stacks`, live from events; `subscribe(fn)`, `load()`,
  `providers()`, `stacks()`, `envById`, `envForNode`, `agentsForEnv`, `hasCap`. Without `/envs` on the server
  (`store.legacy`), environments are derived from `/nodes`.
- `?mock=1` swaps in an in-browser backend implementing docs/api.md + docs/api-envs.md with fake data.

## Components (`js/components/ui.js` unless noted)
| export | use |
|---|---|
| `page({title, commands, body, flush})` | page with `.cmdbar` (title + commands) and `.page-body` |
| `cmd({icon, text, onClick, disabled, accent, id})` | command-bar button (`'sep'`, `'sp'` allowed in `commands`) |
| `masterDetail({key, list, detail, width})` | resizable list/detail split (width persisted per key) |
| `dataGrid({columns, rowKey, groupBy, groupOrder, groupLabel, multi, onSelect, onOpen, onContext, empty})` | dense grid: selection (Ctrl/Shift, arrows, Home/End, Ctrl+A), groups, Enter/dbl-click open, context menu → `{el, setRows, select, selected, primary, focus}` |
| `tabs(defs, active, onChange)` | `[{id, text, disabled, tip}]` → `{el, set(id)}` |
| `envState(s, busy?)`, `agentState(s)` | state dot + label; `busy` (`provisioning`/`installing`) shows a spinner label |
| `placeholder(emoji, title, text, action)` | empty / unsupported states |
| `schemaForm(schema, values, {secretsSaved})` | JSON-Schema form (string/number/integer/boolean/enum/array/object; `x-secret` → password, `format: textarea`, `readOnly` → hidden on create / disabled on edit). `secretsSaved` = bool or the env's `secrets_set` (Set/array): those secrets show "leave empty to keep" and are not required → `{el, value(), validate()}` |
| `setKeys(map)` | register page shortcuts; returns the unregister fn |
| `util.dialog({title, sub, body, buttons, wide})` | ContentDialog; buttons `{text, accent, danger, id, icon, onClick(close) → false keeps open}` → `{close, done, setButtons}` |
| `util.confirmDialog / promptDialog / contextMenu(ev, items) / toast` | styled confirms, flyout menus, toasts |
| `terminal.js mountTerminal(el, envId, {cmd, cwd})` | xterm.js (vendor/xterm, MIT) over `WS /envs/{id}/exec` with resize messages |
| `files.js mountFiles(el, env)` | file browser (navigate, upload incl. drag & drop, download, preview) |
| `metrics.js mountEnvMetrics / mountAgentMetrics` | tiles + `LineChart` (js/chart.js) |
| `envlogs.js mountEnvLogs`, `logview.js LogView` | env log + live events; agent logs (ANSI, search, download) |
| `env-wizard.js openEnvWizard()`, `showJoinDialog(env)` | 新建环境 wizard; agentos-remote join commands |
| `agent-dialog.js openAgentDialog({agent, mode, envId, templateId})` | create / edit / duplicate agents from templates (`js/templates.js`: built-ins + stack `agent_templates`) |

The environment 桌面 tab dynamically imports `pages/desktop.js` (M4) and uses its `DESKTOP_CLIENTS`
(`VncClient`, M5 `ScreenClient`) against `/nodes/{node_id}/desktop` (or `/envs/{id}/desktop` when the env has
no node).


Stack helpers live in `../templates.js`: `stackIcon(stack)` (renders `icon_svg` as an `<img>` data URL, never as markup), `stackName/stackDesc` (follow `name_en`/`description_en` in English), `stackTemplates(stacks)` (agent templates; `command_windows` on Windows hosts, `${VAR}` env placeholders become empty fields to fill).
