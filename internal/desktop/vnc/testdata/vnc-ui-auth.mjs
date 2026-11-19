// M4 UI test: password prompt + clipboard against fake_rfb_auth.py, and the
// error message when the node's VNC server is down.
// Usage: node --experimental-websocket vnc-ui-auth.mjs <base-url> <password> <cdp-port> <auth-node-name> <dead-node-name>
const [base, password, cdpPort, authNode, deadNode] = process.argv.slice(2);
const fs = await import('node:fs');
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const fail = (m) => { console.error('FAIL:', m); process.exit(1); };
const { token } = await (await fetch(base + '/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }) })).json();
const nodes = await (await fetch(base + '/api/v1/nodes', { headers: { Authorization: 'Bearer ' + token } })).json();
const byName = (n) => nodes.find(x => x.name === n && x.status === 'online') || fail('node ' + n);
const { webSocketDebuggerUrl } = await (await fetch(`http://127.0.0.1:${cdpPort}/json/new?about:blank`, { method: 'PUT' })).json();
const ws = new WebSocket(webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let id = 0; const pending = new Map(); const errors = [];
ws.onmessage = (m) => { const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { pending.get(d.id)(d.result ?? d); pending.delete(d.id); }
  if (d.method === 'Runtime.exceptionThrown') errors.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
};
const send = (method, params = {}) => new Promise(res => { const i = ++id; pending.set(i, res); ws.send(JSON.stringify({ id: i, method, params })); });
const ev = async (expr) => (await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result?.value;
const waitState = async (want, ms = 10000) => {
  let s = '';
  for (let t = 0; t < ms && s !== want; t += 200) { await sleep(200); s = await ev(`document.querySelector('.desktop-host')?.dataset.state || ''`); }
  return s;
};
await send('Runtime.enable'); await send('Page.enable'); await send('Network.enable');
await send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
await send('Network.setCookie', { name: 'agentos_session', value: token, domain: new URL(base).hostname, path: '/' });

// 1. password prompt
const a = byName(authNode);
await send('Page.navigate', { url: `${base}/#/nodes/${a.id}/desktop` });
if (await waitState('auth') !== 'auth') fail('no credentials prompt');
await ev(`(() => { const f = document.querySelector('[data-testid=vnc-auth]'); f.querySelector('input[name=password]').value = 'secret'; f.requestSubmit(); })()`);
if (await waitState('connected') !== 'connected') fail('not connected after password');
await sleep(1000);
const px = await ev(`(() => { const c = document.querySelector('[data-testid=vnc-screen] canvas'); const d = c.getContext('2d').getImageData(10, 10, 1, 1).data; return [c.width, c.height, d[0], d[1], d[2]]; })()`);
console.log('auth canvas', px);
if (!(px[0] === 320 && px[3] === 140 && [px[2], px[4]].sort().join() === '255,32')) // fake server ignores SetPixelFormat
   fail('unexpected framebuffer ' + px);
const clip = await ev(`document.querySelector('.vnc-clip-text').value`);
if (clip !== 'hello from remote') fail('clipboard not received: ' + clip);
await ev(`document.querySelector('[data-testid=vnc-cad]').click()`);
fs.writeFileSync('/tmp/vnc-ui-auth.png', Buffer.from((await send('Page.captureScreenshot', { format: 'png' })).data, 'base64'));
console.log('password prompt + clipboard + CAD OK');

// 2. VNC down on node -> clear error text
const d = byName(deadNode);
await send('Page.navigate', { url: `${base}/#/nodes/${d.id}/desktop` });
await sleep(800);
await ev(`document.querySelector('[data-testid=mode-vnc]').click()`);
if (await waitState('error', 20000) !== 'error') fail('no error state');
const msg = await ev(`document.querySelector('.vnc-overlay')?.textContent`);
console.log('error overlay:', msg);
if (!msg.includes('无法连接')) fail('error text missing');
fs.writeFileSync('/tmp/vnc-ui-error.png', Buffer.from((await send('Page.captureScreenshot', { format: 'png' })).data, 'base64'));
if (errors.length) fail('page errors: ' + JSON.stringify(errors));
console.log('PASS');
process.exit(0);
