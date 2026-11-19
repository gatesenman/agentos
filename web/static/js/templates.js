// Agent templates (owned by M6): built-ins plus agent_templates from GET /stacks.
// Template shape: {id, name, desc, icon, stack?, make(win) -> partial agent spec}
import { t, getLang } from './i18n.js';
import { h } from './util.js';

const PY = `import time, datetime
print("Hello from AgentOS \\U0001F44B", flush=True)
i = 0
while True:
    i += 1
    print(f"\\033[36m[{datetime.datetime.now():%H:%M:%S}]\\033[0m tick {i}", flush=True)
    time.sleep(2)`;
const JS = `let i = 0;
console.log('Hello from AgentOS 👋');
setInterval(() => console.log(\`\\x1b[32m[\${new Date().toLocaleTimeString()}]\\x1b[0m tick \${++i}\`), 2000);`;

export const BUILTIN = [
  { id: 'python', em: '🐍', make: (win) => ({ name: 'python-agent', command: win ? 'python' : 'python3', args: ['-u', '-c', PY], env: { PYTHONUNBUFFERED: '1' }, restart: 'on-failure' }) },
  { id: 'node', em: '🟩', make: () => ({ name: 'node-agent', command: 'node', args: ['-e', JS], restart: 'on-failure' }) },
  { id: 'shell', em: '🐚', make: (win) => (win
    ? { name: 'shell-task', command: 'powershell', args: ['-NoProfile', '-Command', 'Write-Output "Hello from $env:AGENTOS_AGENT_NAME"; while ($true) { Get-Date; Start-Sleep 5 }'], restart: 'never' }
    : { name: 'shell-task', command: 'sh', args: ['-c', 'echo "Hello from $AGENTOS_AGENT_NAME in $PWD"; while true; do date; sleep 5; done'], restart: 'never' }) },
  { id: 'claude', em: '✳️', make: () => ({ name: 'claude-code', description: 'Claude Code headless', command: 'claude', args: ['-p', t('tpl.claudePrompt'), '--output-format', 'stream-json', '--verbose'], restart: 'never' }) },
  { id: 'cli', em: '🤖', make: () => ({ name: 'cli-agent', command: '', args: [], restart: 'on-failure', max_restarts: 5, restart_delay_sec: 5, auto_start: true }) },
  { id: 'http', em: '🌐', make: (win) => ({ name: 'file-server', command: win ? 'python' : 'python3', args: ['-u', '-m', 'http.server', '8000', '--bind', '127.0.0.1'], restart: 'on-failure' }) },
].map((x) => ({ ...x, get name() { return t('tpl.' + x.id); }, get desc() { return t('tpl.' + x.id + 'D'); } }));

export const BLANK = { id: 'blank', em: '📄', make: () => ({}), get name() { return t('tpl.blank'); }, get desc() { return t('tpl.blankD'); } };

const slug = (s) => String(s || 'agent').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'agent';
const en = () => getLang() === 'en';
export const stackName = (s) => (en() && s.name_en) || s.name;
export const stackDesc = (s) => (en() && s.description_en) || s.description || '';
// stackIcon(stack) -> Node: server-rendered icon_svg (as an <img>, never injected as markup), emoji, or a puzzle piece.
export function stackIcon(s, size = 24) {
  if (s?.icon_svg && s.icon_svg.startsWith('<svg')) return h('img', { class: 'stack-ic', width: size, height: size, alt: '', src: 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(s.icon_svg) });
  return h('span', { class: 'gi em' }, s?.icon && [...s.icon].length <= 2 ? s.icon : '🧩');
}
export function stackTemplates(stacks) {
  const out = [];
  for (const s of stacks || []) for (const at of s.agent_templates || []) {
    const vars = s.env_vars || [];
    out.push({ id: `${s.id}/${at.id}`, stack: s, icon: stackIcon(s, 20), em: '🧩', name: (en() && at.name_en) || at.name,
      desc: `${stackName(s)} · ${(at.command || []).join(' ')}`.slice(0, 120),
      make: (win) => {
        const [command = '', ...args] = (win && at.command_windows?.length ? at.command_windows : at.command) || [];
        const env = {};
        for (const [k, v] of Object.entries(at.env || {})) env[k] = /^\$\{\w+\}$/.test(String(v)) ? '' : v;
        for (const v of vars) if (!(v.name in env)) env[v.name] = '';
        return { name: slug(at.id || at.name), command, args, env, restart: 'on-failure', description: (en() && at.name_en) || at.name };
      } });
  }
  return out;
}
