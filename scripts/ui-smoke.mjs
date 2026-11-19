#!/usr/bin/env node
// UI smoke test for the AgentOS desktop UI via headless Chrome CDP (owned by M6).
//
//   node --experimental-websocket scripts/ui-smoke.mjs --url http://127.0.0.1:7800 [--mock] [--password admin123]
//        [--shots docs/screenshots] [--prefix desktop-] [--chrome google-chrome] [--cdp 9333] [--no-shell] [--desktop]
//
// --mock     load the UI with ?mock=1 (in-browser mock backend; any static server or the real server works)
// --desktop  real mode: also connect the env 桌面 tab (live screen stream; heavy, captures keyboard)
// --no-shell do not inject fake AgentOS.exe window bindings (agentosWin*), i.e. plain browser mode
// Viewport 1280x800. Walks 环境 (list, detail tabs, context menu, wizard, terminal, files), 智能体 (create via
// dialog from a template, start, log lines within 2s), 模板, 设置, keyboard shortcuts, dark mode and en.
// Exit 1 on any console error / uncaught exception / failed check. In real mode, 404s of endpoints the server
// does not implement yet (docs/api-envs.md) are reported separately as "pending endpoints" and do not fail.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';

const argv = process.argv.slice(2);
const arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 && argv[i + 1] && !argv[i + 1].startsWith('--') ? argv[i + 1] : d; };
const flag = (k) => argv.includes('--' + k);
const BASE = arg('url', process.env.AGENTOS_URL || 'http://127.0.0.1:7800').replace(/\/$/, '');
const MOCK = flag('mock');
const PASSWORD = arg('password', process.env.AGENTOS_PASSWORD || 'admin123');
const SHOTS = arg('shots', '/tmp');
const PREFIX = arg('prefix', MOCK ? 'desktop-' : 'desktop-real-');
const CHROME = arg('chrome', process.env.CHROME || 'google-chrome');
const SHELL = !flag('no-shell');
const W = 1280, H = 800;
let CDP = arg('cdp', '');
fs.mkdirSync(SHOTS, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => console.log('[smoke]', ...a);
const failures = [];
const check = (cond, msg) => { if (cond) log('ok  ', msg); else { log('FAIL', msg); failures.push(msg); } return !!cond; };

// ---------- chrome ----------
let chrome = null, profile = null;
const freePort = () => new Promise((res) => { const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); }); });
if (!CDP) {
  CDP = String(await freePort());
  profile = fs.mkdtempSync(path.join(os.tmpdir(), 'agentos-smoke-'));
  chrome = spawn(CHROME, ['--headless=new', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--lang=zh-CN',
    `--remote-debugging-port=${CDP}`, `--user-data-dir=${profile}`, `--window-size=${W},${H}`, 'about:blank'], { stdio: 'ignore' });
}
const cleanupChrome = () => { try { chrome?.kill('SIGKILL'); } catch {} try { profile && fs.rmSync(profile, { recursive: true, force: true }); } catch {} };
process.on('exit', cleanupChrome);
let target;
for (let i = 0; i < 75; i++) {
  try { target = await (await fetch(`http://127.0.0.1:${CDP}/json/new?about:blank`, { method: 'PUT' })).json(); break; } catch { await sleep(200); }
}
if (!target) { console.error('cannot reach chrome CDP'); process.exit(2); }
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
let mid = 0; const pending = new Map(); const errors = []; const pendingApi = [];
let where = 'start';
// Real mode: endpoints from docs/api-envs.md that the server may not implement yet.
const PENDING_RE = /\/api\/v1\/(providers|stacks|envs)(\/|\?|$)/;
ws.onmessage = (m) => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { const p = pending.get(d.id); pending.delete(d.id); d.error ? p.rej(new Error(d.error.message)) : p.res(d.result); }
  if (d.method === 'Runtime.exceptionThrown') errors.push(`[${where}] exception: ` + (d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text));
  if (d.method === 'Runtime.consoleAPICalled' && (d.params.type === 'error' || d.params.type === 'assert'))
    errors.push(`[${where}] console.error: ` + d.params.args.map((a) => a.value ?? a.description).join(' '));
  if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error') {
    const e = d.params.entry, s = `[${where}] log: ${e.text} ${e.url || ''}`;
    // Boot session probe (authMe) 401 is expected before login — do not fail the run.
    if (/\/api\/v1\/auth\/me/.test(e.url || e.text) && /401/.test(e.text)) return;
    if (!MOCK && PENDING_RE.test(e.url || e.text) && /404|405/.test(e.text)) pendingApi.push(s); else errors.push(s);
  }
};
const CDP_TIMEOUT = 45000; // a wedged renderer fails the run with context instead of hanging it
const send = (method, params = {}) => new Promise((res, rej) => {
  const i = ++mid;
  const tm = setTimeout(() => { if (pending.delete(i)) rej(new Error(`CDP ${method} timed out after ${CDP_TIMEOUT / 1000}s (at: ${where})`)); }, CDP_TIMEOUT);
  pending.set(i, { res: (v) => { clearTimeout(tm); res(v); }, rej: (e) => { clearTimeout(tm); rej(e); } });
  ws.send(JSON.stringify({ id: i, method, params }));
});
const evaluate = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text) + '\n  in: ' + expr.slice(0, 200));
  return r.result.value;
};
const waitFor = async (expr, timeout = 5000) => {
  const t0 = Date.now();
  while (Date.now() - t0 < timeout) { try { if (await evaluate(expr)) return Date.now() - t0; } catch {} await sleep(40); }
  return -1;
};
const q = (s) => JSON.stringify(s);
const exists = (sel) => evaluate(`!!document.querySelector(${q(sel)})`);
const click = (sel) => evaluate(`(() => { const e = document.querySelector(${q(sel)}); if (!e) throw new Error('no element ${sel.replace(/'/g, '')}'); e.click(); return true; })()`);
const selectRow = (sel) => evaluate(`(() => { const e = document.querySelector(${q(sel)}); if (!e) throw new Error('no row'); e.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 1 })); return e.dataset.key; })()`);
const key = async (k, code, mods = 0, vk = 0) => {
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: k, code, modifiers: mods, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
  await send('Input.dispatchKeyEvent', { type: 'keyUp', key: k, code, modifiers: mods, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
};
const shot = async (name) => {
  await sleep(300);
  const { data } = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: W, height: H, scale: 1 } });
  const f = path.join(SHOTS, `${PREFIX}${name}.png`); fs.writeFileSync(f, Buffer.from(data, 'base64')); log('shot', f);
};
const settle = () => waitFor(`!document.querySelector('.page .spinner, .tab-body .spinner')`, 6000);
const go = async (hash, ready) => {
  where = hash;
  await evaluate(`location.hash = ${q(hash)}`);
  const ms = await waitFor(`!!document.querySelector(${q(ready)})`, 8000);
  check(ms >= 0, `render ${hash}`);
  await settle(); await sleep(250);
};
const load = async (url, ready) => {
  // Same-document (hash-only) navigations do not reload, so navigate and then force a reload.
  await send('Page.navigate', { url }); await sleep(100);
  await send('Page.reload', { ignoreCache: false }); await sleep(150);
  return waitFor(`document.readyState === 'complete' && !!document.querySelector(${q(ready)})`, 12000);
};

await send('Runtime.enable'); await send('Page.enable'); await send('Log.enable'); await send('Network.enable');
await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] });
if (SHELL) await send('Page.addScriptToEvaluateOnNewDocument', { source: `(() => {
  let max = false; window.__winCalls = [];
  const rec = (n, v) => async (...a) => { window.__winCalls.push(n); return typeof v === 'function' ? v(...a) : v; };
  window.agentosWinMinimize = rec('min'); window.agentosWinToggleMaximize = rec('max', () => (max = !max));
  window.agentosWinIsMaximized = rec('ismax', () => max); window.agentosWinClose = rec('close'); window.agentosWinDrag = rec('drag');
  window.agentosOpenExternal = rec('open'); window.agentosShellInfo = rec('info', { shell: 'webview2', version: 'smoke', webview2: '131.0.2903.70', tray: false });
})();` });

const URL0 = BASE + (MOCK ? '/?mock=1' : '/?app=1');
const T0 = Date.now();

// ---------- login / boot ----------
where = 'boot';
await send('Page.navigate', { url: URL0 + (MOCK ? '#/envs' : '#/login') });
await waitFor(`document.readyState === 'complete'`, 10000);
await evaluate(`localStorage.setItem('agentos.theme','light'); localStorage.setItem('agentos.lang','zh-CN'); localStorage.removeItem('agentos.md.envs'); true`);
if (!MOCK) {
  check(await load(URL0 + '#/login', '.login input[type=password]') >= 0, 'login page renders');
  await evaluate(`document.querySelector('.login input[type=password]').value = ${q(PASSWORD)}; document.querySelector('.login button').click(); true`);
} else await load(URL0 + '#/envs', '.app');
check(await waitFor(`!!document.querySelector('.grid tbody tr[data-key]')`, 10000) >= 0, 'environment list loads');
await settle(); await sleep(400);

// ---------- shell chrome ----------
where = 'shell';
check(await exists('.titlebar') && await exists('.rail') && await exists('.statusbar'), 'title bar + nav rail + status bar present');
check(await evaluate(`document.querySelectorAll('.rail a.nav[data-nav]').length`) === 4, 'nav rail has 环境/智能体/模板/设置');
check(await evaluate(`document.querySelector('.rail a.nav.active')?.dataset.nav`) === 'envs', '环境 is the home page');
check(await evaluate(`/环境/.test(document.querySelector('.statusbar').textContent)`), 'status bar shows environments');
if (SHELL) {
  check(await evaluate(`document.querySelectorAll('.winbtns .winbtn').length`) === 3, 'window buttons shown inside the desktop shell');
  await click('.winbtns .winbtn:nth-child(2)'); await sleep(150);
  check(await evaluate(`window.__winCalls.includes('max')`), 'maximize button calls agentosWinToggleMaximize');
  await evaluate(`document.querySelector('.titlebar .ttl').dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, detail: 1 })); true`);
  check(await evaluate(`window.__winCalls.includes('drag')`), 'title bar drag calls agentosWinDrag');
}
check(await evaluate(`getComputedStyle(document.querySelector('.rail')).userSelect === 'none'`), 'no text selection on chrome');
check(await evaluate(`(() => { const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true }); document.querySelector('.cmdbar h1').dispatchEvent(e); return e.defaultPrevented; })()`), 'browser context menu suppressed');

// ---------- environments ----------
where = 'envs';
const envCount = await evaluate(`document.querySelectorAll('.grid tbody tr[data-key]').length`);
check(envCount >= 1, `environment rows: ${envCount}`);
check(await evaluate(`document.querySelectorAll('.grid .group-h').length`) >= 1, 'environments grouped by provider group');
check(await exists('.detail-h h2') && await exists('.tabs .tab.on'), 'detail pane with tabs');
const envId = await evaluate(`document.querySelector('.grid tbody tr.sel')?.dataset.key`);
check(!!envId, 'an environment is selected by default: ' + envId);
await shot('envs');
// tab gating
const tabInfo = await evaluate(`[...document.querySelectorAll('.tabs .tab')].map((b) => [b.dataset.tab, b.disabled, b.title])`);
log('tabs', JSON.stringify(tabInfo));
check(tabInfo.length === 7, 'env detail has 7 tabs');
for (const [tab, dis] of tabInfo) {
  if (dis || tab === 'overview') continue;
  if (tab === 'desktop' && !MOCK && !flag('desktop')) { log('skip desktop tab in real mode (pass --desktop to connect the live screen)'); continue; }
  where = 'env tab ' + tab;
  await click(`.tabs .tab[data-tab=${tab}]`);
  const ok = await waitFor(`location.hash.endsWith('/${tab}') && document.querySelector('.tab-body').children.length > 0`, 5000);
  check(ok >= 0, `env tab ${tab} renders`);
  if (tab === 'agents' && MOCK) await shot('env-agents');
  if (tab === 'perf') { await sleep(600); if (MOCK) await shot('env-perf'); }
  if (tab === 'logs') { await sleep(300); }
  if (tab === 'desktop') { check(await waitFor(`!!document.querySelector('.tab-body .desktop-host, .tab-body .console, .tab-body .desktop-wrap, .tab-body .placeholder, .tab-body .tab-connecting')`, 8000) >= 0, 'desktop tab mounts M4/M5 viewer'); await waitFor(`!!document.querySelector('.tab-body .desktop-host, .tab-body .placeholder, .tab-body .alert')`, 8000); await sleep(1000); await shot('desktop'); }
  if (tab === 'files') { check(await waitFor(`document.querySelectorAll('.files .grid tbody tr[data-key]').length > 0 || !!document.querySelector('.files .empty')`, 5000) >= 0, 'file list loads'); if (MOCK) await shot('files'); }
  if (tab === 'terminal') {
    const c = await waitFor(`document.querySelector('.term-bar .st')?.classList.contains('ok')`, 6000);
    check(c >= 0, `terminal connects (${c}ms)`);
    await evaluate(`document.querySelector('.xterm-helper-textarea')?.focus(); true`);
    await send('Input.insertText', { text: MOCK ? 'ls' : 'echo agentos-smoke' });
    await key('Enter', 'Enter', 0, 13);
    const out = await waitFor(`/${MOCK ? 'projects|workspace|\\\\.bashrc' : 'agentos-smoke'}/.test(document.querySelector('.xterm-rows')?.textContent || '')`, 4000);
    check(out >= 0, 'terminal round-trip (input → output)');
    if (MOCK) { await send('Input.insertText', { text: 'nvidia-smi' }); await key('Enter', 'Enter', 0, 13); await sleep(300); }
    await shot('terminal');
  }
}
await click('.tabs .tab[data-tab=overview]'); await sleep(300);

// mock: pick an env that offers the install-agentos action and a disabled tab tooltip
if (MOCK) {
  where = 'envs mock checks';
  await selectRow('.grid tbody tr[data-key="env-py"]'); await sleep(300);
  check(await exists('.detail [data-act=install]'), "'安装 Agent OS' shown when capability install-agentos is present");
  check(await evaluate(`(() => { const b = document.querySelector('.tabs .tab[data-tab=agents]'); return b.disabled && b.title.length > 0; })()`), 'missing capability → tab disabled with tooltip');
  await selectRow('.grid tbody tr[data-key="local"]'); await sleep(200);
}

// stacks card (GET /envs/{id}/stacks) on the overview
if (await exists('.tab-body [data-act=detect-stacks]')) {
  where = 'stacks detect';
  await click('.tab-body [data-act=detect-stacks]');
  check(await waitFor(`!document.querySelector('.tab-body .card .spinner') && !document.querySelector('.tab-body [data-act=detect-stacks]')`, MOCK ? 4000 : 60000) >= 0, 'stack detection result shown');
  if (MOCK) check(await evaluate(`/Python/.test(document.querySelector('.tab-body').textContent)`), 'installed stacks listed');
}

// context menu on a row
where = 'context menu';
await evaluate(`(() => { const r = document.querySelector('.grid tbody tr.sel').getBoundingClientRect(); document.querySelector('.grid tbody tr.sel').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: r.left + 60, clientY: r.top + 20 })); return true; })()`);
check(await waitFor(`!!document.querySelector('.menu button')`, 1500) >= 0, 'row context menu opens');
if (MOCK) await shot('context-menu');
await key('Escape', 'Escape', 0, 27); await evaluate(`document.querySelector('.menu')?.remove(); true`);

// keyboard: F5 refresh must not reload, Ctrl+N opens the new-env wizard
where = 'keyboard';
await evaluate(`window.__noReload = 1; true`);
await key('F5', 'F5', 0, 116); await sleep(500);
check(await evaluate(`window.__noReload === 1`), 'F5 refreshes data without reloading the page');
await evaluate(`document.activeElement?.blur?.(); true`);
await key('n', 'KeyN', 2, 78);
check(await waitFor(`!!document.querySelector('.modal.wizard')`, 3000) >= 0, 'Ctrl+N opens the new-environment wizard');

// wizard: 快速创建 (machine preset only; success offers open + install tools)
where = 'wizard-quick';
const nPreset = await evaluate(`document.querySelectorAll('.modal.wizard .gcard[data-preset]').length`);
if (nPreset) {
  check(nPreset >= 3, `quick-create machine presets listed (${nPreset})`);
  check(await evaluate(`!document.querySelector('.modal.wizard .gcard[data-preset=agentos-claude]')`), 'tool-bundled Claude preset removed from machine catalog');
  await shot('wizard-quick');
  const pid = MOCK ? 'docker-python' : 'agentos-remote';
  await click(`.modal.wizard .gcard[data-preset=${pid}]`);
  await click('.modal.wizard .mf [data-act=quick-create]');
  check(await waitFor(`!!document.querySelector('.modal.wizard [data-step=done]')`, 8000) >= 0, 'preset created → success page (machine only)');
  const qName = await evaluate(`document.querySelector('.modal.wizard .wiz-done .sum b')?.textContent || ''`);
  check(/\s\d+$/.test(qName), `preset env got an auto-generated name (${qName})`);
  check(await exists('.modal.wizard .mf [data-act=install-tools]'), 'success offers 安装环境与 AI 工具');
  check(await exists('.modal.wizard .mf [data-act=done]'), 'success offers 打开这台电脑');
  await shot('wizard-quick-done');
  // Install-tools path: opens templates with ?env=
  await click('.modal.wizard .mf [data-act=install-tools]');
  check(await waitFor(`!document.querySelector('.modal.wizard') && location.hash.includes('/templates')`, 5000) >= 0, 'install-tools opens 环境与 AI 工具 page');
  await shot('templates-after-create');
  if (MOCK) {
    const nStacks = await evaluate(`document.querySelectorAll('.tpl-page .gcard[data-stack]').length`);
    check(nStacks >= 3, `tool catalog lists stacks (${nStacks})`);
    await click('.tpl-page .gcard[data-stack] [data-act=install]');
    check(await waitFor(`!!document.querySelector('.modal .pick-list')`, 3000) >= 0, 'pick target 虚拟电脑 dialog');
    await shot('templates-pick-env');
    await key('Escape', 'Escape', 0, 27);
  }
  await go('#/envs', '.cmdbar [data-cmd=new]');
  if (!MOCK) {
    await evaluate(`(async () => { const r = await fetch('/api/v1/envs', { credentials: 'same-origin' }); const l = await r.json(); for (const e of l) if (e.name === ${JSON.stringify(qName)}) await fetch('/api/v1/envs/' + e.id, { method: 'DELETE', credentials: 'same-origin' }); return true; })()`);
  }
  await key('n', 'KeyN', 2, 78); await waitFor(`!!document.querySelector('.modal.wizard')`, 3000);
  await click('.modal.wizard [data-mode=custom]'); await sleep(150);
} else log('no /env-presets: quick create skipped');

// wizard (custom)
where = 'wizard';
const nProv = await evaluate(`document.querySelectorAll('.modal.wizard .gcard[data-provider]').length`);
if (MOCK || nProv) {
  check(nProv >= 2, `wizard step 1 lists providers (${nProv})`);
  await click(MOCK ? '.modal.wizard .gcard[data-provider=docker]' : '.modal.wizard .gcard[data-provider]');
  await shot('wizard');
  await click('.modal.wizard .mf [data-act=next]'); await sleep(250);
  check(await exists('.modal.wizard .form .field[data-key]') || await exists('.modal.wizard .form .muted'), 'wizard step 2 renders config_schema form');
  if (MOCK) {
    const fld = (k) => `document.querySelector('.modal.wizard .field[data-key=${k}] input')`;
    await evaluate(`${fld('image')}.value = ''; true`);
    await click('.modal.wizard .mf [data-act=create2]'); await sleep(150);
    check(await exists('.modal.wizard .field[data-key=image].has-err'), 'required schema fields are validated');
    await evaluate(`${fld('image')}.value = 'python:3.12-slim'; document.querySelector('.modal.wizard [data-f=name]').value = 'smoke-docker'; true`);
    await click('.modal.wizard .mf [data-act=test]');
    check(await waitFor(`!!document.querySelector('.modal.wizard [data-test=ok]')`, 3000) >= 0, 'test connection (POST /providers/{type}/test) succeeds');
    await shot('wizard-config');
    // 0.1.3+: no stack step — create provisions machine only
    check(!await exists('.modal.wizard .gcard[data-stack]'), 'wizard has no stack step (tools install separately)');
    await click('.modal.wizard .mf [data-act=create2]');
    check(await waitFor(`!!document.querySelector('.modal.wizard [data-step=done]')`, 4000) >= 0, 'wizard shows the success page');
    check(await exists('.modal.wizard .mf [data-act=install-tools]'), 'success offers install tools');
    await shot('wizard-done');
    await click('.modal.wizard .mf [data-act=done]');
    check(await waitFor(`!document.querySelector('.modal.wizard') && /smoke-docker/.test(document.querySelector('.detail-h h2')?.textContent || '')`, 4000) >= 0, 'virtual computer created and selected');
    check(await waitFor(`/运行中/.test(document.querySelector('.detail-h .sub')?.textContent || '')`, 5000) >= 0, 'new environment becomes running (events)');
    check(await exists('.detail-h [data-act=install-tools]'), 'env detail has 安装工具');
    // agentos-remote shows join commands
    await key('n', 'KeyN', 2, 78); await waitFor(`!!document.querySelector('.modal.wizard')`, 2000);
    if (await exists('.modal.wizard [data-mode=custom]')) { await click('.modal.wizard [data-mode=custom]'); await sleep(100); }
    await click('.modal.wizard .gcard[data-provider=agentos-remote]'); await click('.modal.wizard .mf [data-act=next]'); await sleep(150);
    await evaluate(`document.querySelector('.modal.wizard [data-f=name]').value = 'smoke-remote'; true`);
    await click('.modal.wizard .mf [data-act=create2]');
    check(await waitFor(`!!document.querySelector('.modal.wizard .join pre') && /JOINTOKEN|join/.test(document.querySelector('.modal.wizard .join pre').textContent)`, 3000) >= 0, 'agentos-remote shows join commands with copy buttons');
    await shot('wizard-join');
    await click('.modal.wizard .mf [data-act=done]'); await sleep(200);
  } else {
    await shot('wizard-config');
    await click('.modal.wizard .mf [data-act=back]'); await sleep(100);
    await key('Escape', 'Escape', 0, 27);
  }
} else {
  check(await exists('.modal.wizard .placeholder'), 'wizard degrades gracefully when /providers is missing');
  await shot('wizard');
  await key('Escape', 'Escape', 0, 27);
}
check(await waitFor(`!document.querySelector('.modal-bg')`, 2000) >= 0, 'wizard closes');

// ---------- agents: create via dialog, logs within 2s ----------
where = 'agents';
await go('#/agents', '.cmdbar [data-cmd=new]');
await click('.cmdbar [data-cmd=new]');
check(await waitFor(`!!document.querySelector('.modal.agent-modal .tpl-i[data-tpl=python]')`, 4000) >= 0, 'new agent dialog with templates');
await click('.modal.agent-modal .tpl-i[data-tpl=python]');
const agName = 'smoke-' + Date.now().toString(36);
await evaluate(`(() => { const n = document.querySelector('.modal.agent-modal [data-f=name]'); n.value = ${q(agName)}; n.dispatchEvent(new Event('input')); const s = document.querySelector('.modal.agent-modal [data-f=env]'); const o = [...s.options].find((o) => /本机|local/i.test(o.textContent)) || s.options[0]; s.value = o.value; return s.value; })()`);
if (MOCK) await shot('agent-dialog');
const tStart = Date.now();
await click('.modal.agent-modal [data-act=create-start]');
check(await waitFor(`!document.querySelector('.modal.agent-modal')`, 6000) >= 0, 'agent created via dialog');
const shownMs = await waitFor(`location.hash.includes('/logs') && [...document.querySelectorAll('.logs .ln:not(.empty)')].some((l) => /Hello from AgentOS|tick/.test(l.textContent))`, 2000 + 4000);
const logMs = shownMs >= 0 ? Date.now() - tStart : -1;
check(shownMs >= 0 && logMs <= 2000 + 1500, `agent log lines visible after create+start (${logMs}ms incl. create)`);
check(await evaluate(`[...document.querySelectorAll('.grid tbody tr')].some((r) => r.textContent.includes(${q(agName)}))`), 'agent appears in the list');
await sleep(MOCK ? 2500 : 2200);
await evaluate(`(() => { const s = document.querySelector('.logview input[type=search]'); s.value = 'tick'; s.dispatchEvent(new Event('input')); return true; })()`); await sleep(400);
check(await evaluate(`document.querySelectorAll('.logs mark').length > 0`), 'log search highlights matches');
await shot('agent-logs');
await evaluate(`(() => { const s = document.querySelector('.logview input[type=search]'); s.value = ''; s.dispatchEvent(new Event('input')); return true; })()`);
for (const tab of ['perf', 'config', 'overview']) {
  await click(`.tabs .tab[data-tab=${tab}]`);
  check(await waitFor(`location.hash.endsWith('/${tab}') || (${q(tab)} === 'overview' && !/\\/(logs|perf|config)$/.test(location.hash))`, 3000) >= 0 && await waitFor(`document.querySelector('.tab-body').children.length > 0`, 3000) >= 0, `agent tab ${tab}`);
  if (tab === 'perf') { await sleep(800); if (MOCK) await shot('agent-perf'); }
}
// stop it (confirm dialog), keep the record (no deletion)
await click('.detail-h [data-act=stop]');
check(await waitFor(`!!document.querySelector('.modal [data-act=ok]')`, 2000) >= 0, 'stop asks for confirmation (ContentDialog)');
await click('.modal [data-act=ok]');
check(await waitFor(`/已停止|已退出/.test(document.querySelector('.detail-h .sub')?.textContent || '')`, 6000) >= 0, 'agent stopped');

// ---------- templates + settings ----------
where = 'templates';
await go('#/templates', '.tpl-page .section-t');
check(await evaluate(`document.querySelectorAll('.tpl-page .gcard').length`) >= 1, 'template gallery renders');
if (MOCK) {
  await click('.tpl-page .gcard[data-stack] [data-act=install]');
  check(await waitFor(`!!document.querySelector('.modal .pick-list .pick')`, 2000) >= 0, 'install-to-environment dialog');
  await key('Escape', 'Escape', 0, 27); await sleep(150);
}
await shot('templates');
where = 'settings';
await go('#/settings', '.settings .srow');
await shot('settings');

// ---------- theme click E2E (0.1.9): light / dark / system via UI, assert data-theme + persistence ----------
where = 'theme-click';
async function clickTheme(v) {
  const ok = await evaluate(`(() => { const b = document.querySelector('.settings .seg button[data-v="${v}"]'); if (!b) return false; b.click(); return true; })()`);
  check(ok, `theme click button ${v} present`);
  await sleep(200);
}
await clickTheme('light');
check(await evaluate(`document.documentElement.dataset.theme`) === 'light', 'theme click → light (data-theme)');
check(await evaluate(`localStorage.getItem('agentos.theme')`) === 'light', 'theme click → light persisted');
await shot('theme-light');
await clickTheme('dark');
check(await evaluate(`document.documentElement.dataset.theme`) === 'dark', 'theme click → dark (data-theme)');
check(await evaluate(`localStorage.getItem('agentos.theme')`) === 'dark', 'theme click → dark persisted');
await shot('theme-dark');
await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] });
await clickTheme('system');
check(await evaluate(`localStorage.getItem('agentos.theme')`) === 'system', 'theme click → system persisted');
check(await evaluate(`document.documentElement.dataset.theme`) === 'light', "theme click → system follows OS (light)");
await shot('theme-system');

// ---------- dark mode + en ----------
where = 'dark';
await evaluate(`localStorage.setItem('agentos.theme','dark'); true`);
check(await load(URL0 + '#/envs' + (envId ? '/' + encodeURIComponent(envId) + '/perf' : ''), '.grid tbody tr[data-key]') >= 0, 'reload in dark mode');
await settle(); await sleep(900);
check(await evaluate(`document.documentElement.dataset.theme`) === 'dark', 'dark theme applied');
await shot('dark');
where = 'en';
await evaluate(`localStorage.setItem('agentos.theme','system'); localStorage.setItem('agentos.lang','en'); true`);
await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });
check(await load(URL0 + '#/agents', '.grid tbody tr[data-key]') >= 0, 'reload in English');
await settle(); await sleep(400);
check(await evaluate(`document.querySelector('.rail a.nav[data-nav=envs]').textContent.includes('Virtual computers')`), 'English UI strings');
check(await evaluate(`document.documentElement.dataset.theme`) === 'dark', "theme 'system' follows the OS (dark)");
await shot('en-dark');
await evaluate(`localStorage.setItem('agentos.lang','zh-CN'); localStorage.setItem('agentos.theme','system'); true`);

// ---------- result ----------
await sleep(300);
log(`done in ${((Date.now() - T0) / 1000).toFixed(1)}s, mode=${MOCK ? 'mock' : 'real'}, shell=${SHELL}`);
if (pendingApi.length) { log(`pending endpoints (404, not counted as errors): ${pendingApi.length}`); [...new Set(pendingApi.map((s) => s.replace(/^\[[^\]]*\] /, '')))].forEach((s) => log('  ' + s)); }
if (errors.length) { log('console errors:'); errors.forEach((e) => log('  ' + e)); }
check(errors.length === 0, `zero console errors (${errors.length})`);
ws.close(); cleanupChrome();
if (failures.length) { log(`${failures.length} FAILED:`); failures.forEach((f) => log('  - ' + f)); process.exit(1); }
log('ALL OK');
process.exit(0);
