// Minimal headless-Chrome CDP driver for the desktop e2e (no dependencies).
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
export const freePort = () => new Promise((res) => { const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); }); });

export async function launch({ chrome = process.env.CHROME || 'google-chrome', width = 1280, height = 800 } = {}) {
  const port = await freePort();
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'agentos-desk-e2e-'));
  const proc = spawn(chrome, ['--headless=new', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--lang=zh-CN',
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, `--window-size=${width},${height}`, 'about:blank'], { stdio: 'ignore' });
  let target;
  for (let i = 0; i < 100 && !target; i++) {
    try { target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })).json(); } catch { await sleep(200); }
  }
  if (!target) throw new Error('cannot reach chrome CDP');
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
  let id = 0; const pending = new Map(); const errors = [];
  ws.onmessage = (m) => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) { const p = pending.get(d.id); pending.delete(d.id); d.error ? p.rej(new Error(d.error.message)) : p.res(d.result); }
    if (d.method === 'Runtime.exceptionThrown') errors.push('exception: ' + (d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text));
  };
  const send = (method, params = {}) => new Promise((res, rej) => {
    const i = ++id; const tm = setTimeout(() => pending.delete(i) && rej(new Error('CDP timeout ' + method)), 60000);
    pending.set(i, { res: (v) => { clearTimeout(tm); res(v); }, rej: (e) => { clearTimeout(tm); rej(e); } });
    ws.send(JSON.stringify({ id: i, method, params }));
  });
  await send('Page.enable'); await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
  const page = {
    send, errors,
    async eval(expr) {
      const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
      if (r.exceptionDetails) throw new Error('eval: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
      return r.result.value;
    },
    async goto(url) { await send('Page.navigate', { url }); await sleep(500); },
    async waitFor(expr, ms = 30000, what = expr) {
      const t0 = Date.now();
      for (;;) {
        let v; try { v = await page.eval(expr); } catch { v = null; }
        if (v) return v;
        if (Date.now() - t0 > ms) throw new Error('timeout waiting for ' + what);
        await sleep(250);
      }
    },
    async shot(file) {
      const r = await send('Page.captureScreenshot', { format: 'png' });
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, Buffer.from(r.data, 'base64'));
      return file;
    },
    async mouse(type, x, y, button = 'none', clickCount = 0) {
      await send('Input.dispatchMouseEvent', { type, x, y, button, buttons: button === 'left' && type === 'mousePressed' ? 1 : 0, clickCount });
    },
    async click(x, y) {
      await page.mouse('mouseMoved', x, y); await sleep(80);
      await page.mouse('mousePressed', x, y, 'left', 1); await sleep(60);
      await page.mouse('mouseReleased', x, y, 'left', 1);
    },
    async key(k) { // one printable char or 'Enter'
      const d = keyDef(k);
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: d.key, code: d.code, windowsVirtualKeyCode: d.vk, text: d.text, unmodifiedText: d.text, modifiers: d.mod });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: d.key, code: d.code, windowsVirtualKeyCode: d.vk, modifiers: d.mod });
    },
    async type(s, delay = 25) { for (const ch of s) { await page.key(ch); await sleep(delay); } },
    close() { try { ws.close(); } catch {} try { proc.kill('SIGKILL'); } catch {} try { fs.rmSync(profile, { recursive: true, force: true }); } catch {} },
  };
  return page;
}

const SHIFTED = { '>': ['Period', 190], '$': ['Digit4', 52], '_': ['Minus', 189], ':': ['Semicolon', 186], '"': ['Quote', 222], '|': ['Backslash', 220], '&': ['Digit7', 55] };
const PLAIN = { '-': ['Minus', 189], '/': ['Slash', 191], '.': ['Period', 190], ' ': ['Space', 32], '=': ['Equal', 187], ';': ['Semicolon', 186], "'": ['Quote', 222] };
function keyDef(k) {
  if (k === 'Enter') return { key: 'Enter', code: 'Enter', vk: 13, text: '\r', mod: 0 };
  if (/[a-z]/.test(k)) return { key: k, code: 'Key' + k.toUpperCase(), vk: k.toUpperCase().charCodeAt(0), text: k, mod: 0 };
  if (/[A-Z]/.test(k)) return { key: k, code: 'Key' + k, vk: k.charCodeAt(0), text: k, mod: 8 };
  if (/[0-9]/.test(k)) return { key: k, code: 'Digit' + k, vk: k.charCodeAt(0), text: k, mod: 0 };
  if (PLAIN[k]) return { key: k, code: PLAIN[k][0], vk: PLAIN[k][1], text: k, mod: 0 };
  if (SHIFTED[k]) return { key: k, code: SHIFTED[k][0], vk: SHIFTED[k][1], text: k, mod: 8 };
  throw new Error('no key mapping for ' + JSON.stringify(k));
}
