// M4 UI smoke: headless Chrome opens the noVNC viewer through the AgentOS UI
// and checks the remote desktop canvas is not black.
// Usage:
//   google-chrome --headless=new --no-sandbox --remote-debugging-port=<cdp> &
//   node --experimental-websocket internal/desktop/vnc/testdata/vnc-ui-smoke.mjs <base-url> <password> <cdp-port> [out.png] [node-name]
const [base, password, cdpPort = '9333', out = '/tmp/vnc-ui.png', nodeName] = process.argv.slice(2);
const fs = await import('node:fs');
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const fail = (m) => { console.error('FAIL:', m); process.exit(1); };

const login = await fetch(base + '/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }) });
const { token } = await login.json();
const nodes = await (await fetch(base + '/api/v1/nodes', { headers: { Authorization: 'Bearer ' + token } })).json();
const node = nodes.find(n => n.status === 'online' && (nodeName ? n.name === nodeName : n.capabilities.includes('desktop.vnc')));
if (!node) fail('no online node');
console.log('node', node.id, node.capabilities);

const r = await fetch(`http://127.0.0.1:${cdpPort}/json/new?about:blank`, { method: 'PUT' });
const { webSocketDebuggerUrl } = await r.json();
const ws = new WebSocket(webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let id = 0; const pending = new Map(); const errors = [];
ws.onmessage = (m) => { const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { pending.get(d.id)(d.result ?? d); pending.delete(d.id); }
  if (d.method === 'Runtime.exceptionThrown') errors.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
  if (d.method === 'Runtime.consoleAPICalled' && d.params.type === 'error') errors.push(JSON.stringify(d.params.args.map(a => a.value ?? a.description)));
};
const send = (method, params = {}) => new Promise(res => { const i = ++id; pending.set(i, res); ws.send(JSON.stringify({ id: i, method, params })); });
const evaluate = async (expr) => (await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result?.value;
await send('Runtime.enable'); await send('Page.enable');
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
const u = new URL(base);
await send('Network.enable');
await send('Network.setCookie', { name: 'agentos_session', value: token, domain: u.hostname, path: '/' });
await send('Page.navigate', { url: `${base}/#/nodes/${node.id}/desktop` });
await sleep(1500);
await evaluate(`document.querySelector('[data-testid=mode-vnc]').click()`);
let state = '';
for (let i = 0; i < 40 && state !== 'connected'; i++) {
  await sleep(250);
  state = await evaluate(`document.querySelector('.desktop-host')?.dataset.state || ''`);
}
console.log('state', state, 'status:', await evaluate(`document.querySelector('[data-testid=vnc-status]')?.textContent`));
if (state !== 'connected') fail('viewer did not connect; msg=' + await evaluate(`document.querySelector('.vnc-msg')?.textContent`));
await sleep(2500); // let framebuffer updates arrive
const stats = await evaluate(`(() => {
  const c = document.querySelector('[data-testid=vnc-screen] canvas');
  if (!c) return null;
  const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
  let nonBlack = 0, n = 0;
  for (let i = 0; i < d.length; i += 4 * 97) { n++; if (d[i] + d[i+1] + d[i+2] > 30) nonBlack++; }
  return { w: c.width, h: c.height, nonBlackRatio: nonBlack / n, size: document.querySelector('.vnc-statusbar')?.textContent };
})()`);
console.log('canvas', JSON.stringify(stats));
const { data } = await send('Page.captureScreenshot', { format: 'png' });
fs.writeFileSync(out, Buffer.from(data, 'base64'));
console.log('screenshot', out);
if (!stats || stats.w === 0) fail('no canvas');
if (stats.nonBlackRatio < 0.01) fail('canvas is black');
if (errors.length) console.log('page errors:', JSON.stringify(errors));
console.log('PASS');
process.exit(0);
