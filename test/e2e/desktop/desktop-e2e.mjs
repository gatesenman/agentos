#!/usr/bin/env node
// Desktop + per-type e2e for every environment type against a real agentos server
// (started by run.sh). For each env: open its 桌面 tab in headless Chrome, wait for
// dataset.state=connected, screenshot, CONTROL it through noVNC (click the xterm,
// type `echo AGENTOS-<type>-<rand> $DISPLAY > /tmp/agentos-e2e-<type>` + Enter),
// verify through exec in that env, screenshot again. Then the generic checks
// (exec, files, agent, metrics, logs, stop/delete). Writes <out>/RESULTS.md.
//
//   node --experimental-websocket test/e2e/desktop/desktop-e2e.mjs --url URL --password PW --out DIR \
//        --bin AGENTOS_BINARY --work DIR --ssh-port N [--only builtin,local,remote,docker,ssh]
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { launch, sleep } from './cdp.mjs';

const argv = process.argv.slice(2);
const arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 ? argv[i + 1] : d; };
const URL0 = arg('url', 'http://127.0.0.1:7800').replace(/\/$/, '');
const PW = arg('password', 'admin123');
const OUT = arg('out', '/tmp/agentos-desktop-e2e');
const BIN = arg('bin', '');
const WORK = arg('work', '/tmp/agentos-desktop-e2e-work');
const SSH_PORT = +arg('ssh-port', '0');
const SSH_USER = arg('ssh-user', 'aosssh'), SSH_PASS = arg('ssh-pass', 'aos-ssh-pw1');
const ONLY = (arg('only', 'builtin,local,remote,docker,ssh')).split(',');
fs.mkdirSync(OUT, { recursive: true });
const B = URL0 + '/api/v1';
const log = (...a) => console.log('[desk-e2e]', ...a);
const rand = () => Math.random().toString(36).slice(2, 8);

// ---------------- API helpers ----------------
let tok;
async function api(m, p, body, raw) {
  const r = await fetch(B + p, { method: m, headers: { ...(raw ? {} : { 'Content-Type': 'application/json' }), Authorization: 'Bearer ' + tok }, body: raw ?? (body ? JSON.stringify(body) : undefined) });
  const t = await r.text(); let j; try { j = JSON.parse(t); } catch { j = t; }
  if (r.status >= 400) { const e = new Error(`${m} ${p}: ${r.status} ${t.slice(0, 300)}`); e.status = r.status; throw e; }
  return j;
}
const until = async (what, f, s = 180) => { let last; for (let i = 0; i < s * 2; i++) { try { const v = await f(); if (v) return v; } catch (e) { last = e; } await sleep(500); } throw new Error('timeout: ' + what + (last ? ' (' + last.message + ')' : '')); };
function execIn(id, cmd, ms = 30000) {
  return new Promise((res, rej) => {
    const u = `${URL0.replace(/^http/, 'ws')}/api/v1/envs/${id}/exec?cmd=${encodeURIComponent(JSON.stringify(cmd))}&token=${tok}`;
    const ws = new WebSocket(u); ws.binaryType = 'arraybuffer'; let out = '';
    const tm = setTimeout(() => { try { ws.close(); } catch {} rej(new Error('exec timeout: ' + cmd.join(' '))); }, ms);
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') { const m = JSON.parse(ev.data); clearTimeout(tm); if (m.type === 'exit') res({ out, code: m.code }); else rej(new Error(m.message)); }
      else out += Buffer.from(ev.data).toString();
    };
    ws.onerror = () => { clearTimeout(tm); rej(new Error('exec ws error')); };
  });
}
const sh = (id, script) => execIn(id, ['sh', '-c', script]);

// ---------------- results ----------------
const results = {}; // type -> {desktop, connected, control, evidence, checks:{}}
const R = (type) => (results[type] ||= { type, desktop: '', connected: '❌', control: '❌', evidence: '', screen: '', checks: {} });
const check = (type, name, fn) => fn().then((v) => { R(type).checks[name] = '✅ ' + (v || ''); log(type, 'ok', name, v || ''); }, (e) => { R(type).checks[name] = '❌ ' + e.message.slice(0, 160); log(type, 'FAIL', name, e.message); });

// ---------------- browser ----------------
let page;
async function openDesktop(env, kind) {
  await page.eval(`localStorage.setItem('agentos.desktop.kind.env.${env.id}', '${kind}'); true`);
  await page.goto(URL0 + '/#/envs');
  await sleep(300);
  await page.goto(URL0 + `/#/envs/${encodeURIComponent(env.id)}/desktop`);
  await page.waitFor(`document.querySelector('.desktop-host')?.dataset.state === 'connected'`, 45000, `${env.name} ${kind} connected`);
  await sleep(kind === 'vnc' ? 2500 : 2000); // first frames
}
// client coords of remote pixel (x, y) on the desktop canvas
async function remoteToClient(x, y) {
  return page.eval(`(() => { const c = [...document.querySelectorAll('.desktop-host canvas')].find((c) => c.width > 0 && c.getBoundingClientRect().width > 50);
    const r = c.getBoundingClientRect(); return { x: r.left + ${x} * r.width / c.width, y: r.top + ${y} * r.height / c.height, w: c.width, h: c.height }; })()`);
}

async function desktopControl(type, env, { displayCmd = 'echo $DISPLAY', kindLabel }) {
  const r = R(type);
  r.desktop = kindLabel;
  await openDesktop(env, 'vnc');
  r.connected = '✅';
  await page.shot(path.join(OUT, `${type}-connected.png`));
  const mark = `AGENTOS-${type}-${rand()}`;
  const file = `/tmp/agentos-e2e-${type}`;
  await sh(env.id, `rm -f ${file}`).catch(() => {});
  const p = await remoteToClient(330, 230); // inside the xterm (geometry 100x28+60+60)
  await page.click(p.x, p.y);
  await sleep(500);
  await page.type(`echo ${mark} $DISPLAY > ${file}`);
  await page.key('Enter');
  await page.type(`echo typed-${mark}`);
  await page.key('Enter');
  await sleep(1500);
  await page.shot(path.join(OUT, `${type}-controlled.png`));
  const got = await until(`${file} written`, async () => { const x = await sh(env.id, `cat ${file} 2>/dev/null`); return x.out.trim() || null; }, 15);
  if (!got.startsWith(mark)) throw new Error(`file content ${JSON.stringify(got)} != ${mark}`);
  const win = await sh(env.id, `${displayCmd}; xdotool getactivewindow getwindowname 2>&1 | head -1`).catch((e) => ({ out: e.message }));
  r.control = '✅';
  r.evidence = `typed via noVNC → \`${file}\` = \`${got}\`; exec: DISPLAY/active window = \`${win.out.trim().replace(/\n/g, ' / ')}\` (fb ${p.w}x${p.h})`;
  log(type, 'control verified', got);
  return got;
}

async function screenStreamTest(type, env) {
  await openDesktop(env, 'screen');
  const X = 777, Y = 333;
  const p = await remoteToClient(X, Y);
  for (let i = 0; i < 3; i++) { await page.mouse('mouseMoved', p.x + i * 0.3, p.y); await sleep(200); }
  await sleep(500);
  const dbg = await page.eval(`(() => { const e = document.elementFromPoint(${p.x}, ${p.y}); const cs = [...document.querySelectorAll('.desktop-host canvas')].map((c) => c.width + 'x' + c.height + '@' + JSON.stringify(c.getBoundingClientRect())); const chain = []; for (let x = e; x && chain.length < 6; x = x.parentElement) chain.push(x.tagName + '.' + x.className + (x.getAttribute('style') ? '[' + x.getAttribute('style').slice(0, 80) + ']' : '')); return chain.join(' < ') + ' | canvases ' + cs.join(' ; ') + ' | kind ' + document.querySelector('[data-kind].on')?.dataset.kind; })()`);
  log(type, 'screen debug', dbg);
  const loc = await until('mouse location', async () => {
    const o = (await sh(env.id, 'xdotool getmouselocation')).out;
    const m = /x:(\d+) y:(\d+)/.exec(o); if (!m) return null;
    return Math.abs(+m[1] - X) <= 3 && Math.abs(+m[2] - Y) <= 3 ? `${m[1]},${m[2]}` : null;
  }, 10);
  await page.shot(path.join(OUT, `${type}-screen.png`));
  R(type).screen = `✅ screen stream: pointer moved to remote (${X},${Y}) → xdotool getmouselocation ${loc}`;
}

async function generic(type, env, { agent = true, lifecycle = true, purge = true, deletable = true } = {}) {
  await check(type, 'exec', async () => { const r = await execIn(env.id, ['echo', 'hello-' + type]); if (r.out.trim() !== 'hello-' + type || r.code !== 0) throw new Error(JSON.stringify(r)); return '`echo` → ' + r.out.trim(); });
  await check(type, 'files', async () => {
    const p = `/tmp/agentos-e2e-file-${type}.txt`, body = 'file-' + rand();
    await api('PUT', `/envs/${env.id}/files/content?path=${encodeURIComponent(p)}&mode=0644`, null, body);
    const list = await api('GET', `/envs/${env.id}/files?path=${encodeURIComponent('/tmp')}`);
    if (!list.some((f) => f.name === path.basename(p))) throw new Error('not listed');
    const r = await fetch(`${B}/envs/${env.id}/files/content?path=${encodeURIComponent(p)}`, { headers: { Authorization: 'Bearer ' + tok } });
    const t = await r.text(); if (t !== body) throw new Error('content ' + t);
    return 'upload/list/download ' + p;
  });
  if (agent) await check(type, 'agent', async () => {
    const e = await api('GET', '/envs/' + env.id);
    if (!e.node_id) throw new Error('no node_id');
    const a = await api('POST', '/agents', { node_id: e.node_id, name: 'e2e-' + type, command: 'sh', args: ['-c', `while :; do echo "agent-${type} tick"; sleep 1; done`] });
    await api('POST', `/agents/${a.id}/start`);
    await until('agent running', async () => (await api('GET', '/agents/' + a.id)).status?.state === 'running', 60);
    const lines = await until('agent log', async () => { const l = await api('GET', `/agents/${a.id}/logs?tail=20`); const arr = Array.isArray(l) ? l : (l.lines || []); return arr.some((x) => (x.text || x.line || '').includes(`agent-${type} tick`)) && arr.length; }, 30);
    await api('POST', `/agents/${a.id}/stop`).catch(() => {});
    await api('DELETE', `/agents/${a.id}`).catch(() => {});
    return `running on node ${e.node_id.slice(0, 8)}, ${lines} log lines`;
  });
  await check(type, 'metrics', async () => {
    const m = await until('metrics', async () => { const x = await api('GET', `/envs/${env.id}/metrics`); return x.latest && x; }, 30);
    const l = m.latest; return `cpu ${l.cpu_percent?.toFixed?.(1) ?? l.cpu?.toFixed?.(1) ?? '?'}%, mem ${(((l.mem_used ?? l.memory_used ?? 0)) / 1048576).toFixed(0)} MiB, ${m.samples?.length ?? 0} samples`;
  });
  await check(type, 'logs', async () => { const l = await api('GET', `/envs/${env.id}/logs?tail=200`); return `${l.length} lifecycle log lines` + (l.length ? ` (last: ${String(l[l.length - 1].text).slice(0, 50)})` : ''); });
  if (lifecycle) await check(type, 'stop/start', async () => {
    await api('POST', `/envs/${env.id}/stop`);
    await until('stopped', async () => (await api('GET', '/envs/' + env.id)).status.state === 'stopped', 90);
    await api('POST', `/envs/${env.id}/start`);
    await until('running again', async () => (await api('GET', '/envs/' + env.id)).status.state === 'running', 120);
    return 'stopped → running';
  });
  else R(type).checks['stop/start'] = '— n/a (no lifecycle capability)';
  if (deletable) await check(type, 'delete', async () => {
    const r = await fetch(`${B}/envs/${env.id}${purge ? '?purge=1' : ''}`, { method: 'DELETE', headers: { Authorization: 'Bearer ' + tok } });
    if (r.status !== 204) throw new Error('status ' + r.status);
    await until('gone', async () => { try { await api('GET', '/envs/' + env.id); return false; } catch (e) { return e.status === 404; } }, 30);
    return 'DELETE' + (purge ? '?purge=1' : '') + ' → 204, then 404';
  });
  else await check(type, 'delete', async () => {
    const r = await fetch(`${B}/envs/${env.id}`, { method: 'DELETE', headers: { Authorization: 'Bearer ' + tok } });
    if (r.status < 400) throw new Error('builtin env was deletable: ' + r.status);
    return `refused for built-in env (HTTP ${r.status})`;
  });
}

const waitRunning = (id, s = 600) => until('env running ' + id, async () => { const e = await api('GET', '/envs/' + id); if (e.status.state === 'error') throw new Error(e.status.message); return e.status.state === 'running' && e; }, s);
const waitNode = (id, s = 180) => until('node online for ' + id, async () => { const e = await api('GET', '/envs/' + id); if (!e.node_id) return null; const n = await api('GET', '/nodes/' + e.node_id); return n.status === 'online' && e.capabilities?.includes('desktop.vnc') && e; }, s);

// ---------------- main ----------------
let remoteNode = null;
try {
  tok = (await (await fetch(B + '/auth/login', { method: 'POST', body: JSON.stringify({ password: PW }) })).json()).token;
  page = await launch();
  await page.goto(URL0 + '/#/login');
  await page.waitFor(`!!document.querySelector('.login input[type=password]')`, 20000, 'login page');
  await page.eval(`document.querySelector('.login input[type=password]').value = ${JSON.stringify(PW)}; document.querySelector('.login input[type=password]').dispatchEvent(new Event('input')); document.querySelector('.login button').click(); true`);
  await page.waitFor(`!location.hash.includes('login')`, 20000, 'logged in');

  const created = {};
  // create everything first (docker image build / pulls run in parallel)
  if (ONLY.includes('local')) created.local = await api('POST', '/envs', { name: '本机实例 B（虚拟桌面）', provider: 'agentos-local', config: { desktop: 'virtual' } });
  if (ONLY.includes('remote')) {
    const e = await api('POST', '/envs', { name: '远程节点（虚拟桌面）', provider: 'agentos-remote', config: { desktop: 'virtual' } });
    const j = await api('POST', `/envs/${e.id}/join`, null);
    if (!/--desktop virtual/.test(j.commands.linux)) throw new Error('join command lacks --desktop virtual: ' + j.commands.linux);
    const dir = path.join(WORK, 'remote-node'); fs.mkdirSync(dir, { recursive: true });
    remoteNode = spawn(BIN, ['node', '--server', URL0, '--token', j.token, '--name', 'remote-e2e', '--desktop', 'virtual', '--data', dir],
      { stdio: ['ignore', fs.openSync(path.join(WORK, 'remote-node.log'), 'a'), fs.openSync(path.join(WORK, 'remote-node.log'), 'a')], env: { ...process.env, DISPLAY: '' } });
    created.remote = e;
  }
  if (ONLY.includes('docker')) created.docker = await api('POST', '/envs', { name: 'Docker 桌面容器', provider: 'docker', config: { desktop: true } });
  if (ONLY.includes('ssh')) created.ssh = await api('POST', '/envs', { name: 'SSH 主机（虚拟桌面）', provider: 'ssh',
    config: { host: '127.0.0.1', port: SSH_PORT, user: SSH_USER, password: SSH_PASS, host_key_policy: 'tofu', vnc_port: 5977, virtual_desktop: true } });

  const displays = {};
  // (1) built-in 本机
  if (ONLY.includes('builtin')) {
    const env = await waitRunning('local');
    try { await desktopControl('builtin', env, { kindLabel: 'VNC → host display (--vnc) + screen stream' }); } catch (e) { R('builtin').evidence = '❌ ' + e.message; log('builtin FAIL', e.message); }
    try { await screenStreamTest('builtin', env); } catch (e) { R('builtin').screen = '❌ ' + e.message; log('builtin screen FAIL', e.message); }
    displays.builtin = (await sh('local', 'echo $DISPLAY')).out.trim();
    await generic('builtin', env, { lifecycle: false, deletable: false });
  }
  // (2) extra local instance with its own virtual desktop
  if (created.local) {
    const env = await waitNode(created.local.id);
    try { await desktopControl('local', env, { kindLabel: 'VNC → own virtual desktop (Xvfb+fluxbox+x11vnc) + screen stream' }); } catch (e) { R('local').evidence = '❌ ' + e.message; log('local FAIL', e.message); }
    try { await screenStreamTest('local', env); } catch (e) { R('local').screen = '❌ ' + e.message; }
    displays.local = (await sh(env.id, 'echo $DISPLAY')).out.trim();
  }
  // (3) remote node joined with a token, own virtual desktop
  if (created.remote) {
    const env = await waitNode(created.remote.id);
    try { await desktopControl('remote', env, { kindLabel: 'VNC → remote node virtual desktop (--desktop virtual)' }); } catch (e) { R('remote').evidence = '❌ ' + e.message; log('remote FAIL', e.message); }
    displays.remote = (await sh(env.id, 'echo $DISPLAY')).out.trim();
  }
  // (4) docker desktop container
  if (created.docker) {
    const env = await waitRunning(created.docker.id, 900);
    try { await desktopControl('docker', env, { kindLabel: 'VNC → container x11vnc (localhost/agentos-desktop, auto-built)' }); } catch (e) { R('docker').evidence = '❌ ' + e.message; log('docker FAIL', e.message); }
  }
  // (5) ssh host with an on-demand virtual desktop, relayed via direct-tcpip
  if (created.ssh) {
    const env = await waitRunning(created.ssh.id, 120);
    try { await desktopControl('ssh', env, { kindLabel: 'VNC over SSH direct-tcpip → remote 127.0.0.1:5977 (virtual_desktop)', displayCmd: 'export DISPLAY=:$(cat ~/.agentos/vdesk/display); echo $DISPLAY' }); } catch (e) { R('ssh').evidence = '❌ ' + e.message; log('ssh FAIL', e.message); }
  }
  if (Object.keys(displays).length > 1) {
    const vals = Object.values(displays);
    const sep = new Set(vals).size === vals.length;
    log('displays', JSON.stringify(displays), sep ? 'all separate' : 'NOT separate');
    results._separate = `${sep ? '✅' : '❌'} Agent OS env displays: ${Object.entries(displays).map(([k, v]) => `${k}=${v}`).join(', ')}`;
  }

  // generic checks (docker/ssh: install-agentos first so agents can run)
  if (created.local) await generic('local', created.local);
  if (created.remote) await generic('remote', created.remote, { lifecycle: false });
  for (const t of ['docker', 'ssh']) {
    if (!created[t]) continue;
    await check(t, 'install-agentos', async () => {
      await api('POST', `/envs/${created[t].id}/install-agentos`);
      const e = await until('bootstrapped node online', async () => { const e = await api('GET', '/envs/' + created[t].id); if (!e.node_id) return null; const n = await api('GET', '/nodes/' + e.node_id); return n.status === 'online' && e; }, 240);
      return 'node ' + e.node_id.slice(0, 8) + ' online';
    });
    await generic(t, created[t], { lifecycle: t === 'docker' });
  }
} catch (e) {
  log('FATAL', e.stack || e.message);
  results._fatal = e.message;
} finally {
  page?.close();
  if (remoteNode) { remoteNode.kill('SIGTERM'); await sleep(1500); }
}

// ---------------- report ----------------
const names = { builtin: '本机 (built-in agentos-local)', local: '本机实例 B (extra agentos-local)', remote: '远程 Agent OS (agentos-remote, token join)', docker: 'Docker (desktop:true)', ssh: 'SSH (local sshd, user ' + SSH_USER + ')' };
const types = ['builtin', 'local', 'remote', 'docker', 'ssh'].filter((t) => results[t]);
const checkNames = ['install-agentos', 'exec', 'files', 'agent', 'metrics', 'logs', 'stop/start', 'delete'];
let md = `# AgentOS desktop + environment e2e results\n\nRun: ${new Date().toISOString()} against ${URL0} (real \`agentos\` binary, headless Chrome 1280x800).\n\n`;
md += `## Desktop: connected + controlled\n\n| type | desktop kind | connected | control verified | evidence | screenshots |\n|---|---|---|---|---|---|\n`;
for (const t of types) { const r = results[t]; md += `| ${names[t]} | ${r.desktop} | ${r.connected} | ${r.control} | ${r.evidence.replace(/\|/g, '\\|')}${r.screen ? '<br>' + r.screen : ''} | \`${t}-connected.png\`, \`${t}-controlled.png\`${r.screen.startsWith('✅') ? `, \`${t}-screen.png\`` : ''} |\n`; }
if (results._separate) md += `\n${results._separate}\n`;
md += `\n## Other checks per type\n\n| type | ${checkNames.join(' | ')} |\n|---|${checkNames.map(() => '---').join('|')}|\n`;
for (const t of types) md += `| ${t} | ${checkNames.map((c) => (results[t].checks[c] || '—').replace(/\|/g, '\\|')).join(' | ')} |\n`;
if (results._fatal) md += `\n**FATAL:** ${results._fatal}\n`;
fs.writeFileSync(path.join(OUT, 'RESULTS.md'), md);
console.log(md);
const bad = types.some((t) => results[t].control !== '✅' || results[t].screen.startsWith('❌') || Object.values(results[t].checks).some((v) => v.startsWith('❌'))) || results._fatal;
process.exit(bad ? 1 : 0);
