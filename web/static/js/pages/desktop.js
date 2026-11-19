// Remote desktop page (owned by M4).
//
// Two interchangeable desktop clients share one contract:
//   const c = new Client(containerEl, apiPath, opts?); c.connect(); c.disconnect();
//   - VncClient (this file): noVNC (vendored, /vendor/novnc) over `?kind=vnc`.
//   - ScreenClient (../desktop/screen-client.js, M5): built-in stream over `?kind=screen`.
// Mode-switch hook: DESKTOP_CLIENTS maps tunnel kind -> {label, cap, Client};
// the page picks a kind (remembered per node, falling back to `screen` when
// the node has no VNC) and builds `/nodes/<id>/desktop?kind=<kind>`.
import { h, toast } from '../util.js';
import { api, wsURL } from '../api.js';
import { ScreenClient } from '../desktop/screen-client.js';

const NOVNC = '../../vendor/novnc/core/';
let novnc = null; // lazy: {RFB, keysyms}
async function loadNoVNC() {
  if (!novnc) {
    const [rfb, ks] = await Promise.all([import(NOVNC + 'rfb.js'), import(NOVNC + 'input/keysymdef.js')]);
    novnc = { RFB: rfb.default, keysyms: ks.default };
  }
  return novnc;
}

const XK = { Control_L: 0xffe3, Alt_L: 0xffe9, Super_L: 0xffeb, Tab: 0xff09, Escape: 0xff1b, Print: 0xff61, Return: 0xff0d, Delete: 0xffff, F4: 0xffc1 };
const COMBOS = {
  'ctrl-alt-del': { label: 'Ctrl+Alt+Del', keys: [['Control_L', 'ControlLeft'], ['Alt_L', 'AltLeft'], ['Delete', 'Delete']] },
  'win': { label: 'Win 键', keys: [['Super_L', 'MetaLeft']] },
  'alt-tab': { label: 'Alt+Tab', keys: [['Alt_L', 'AltLeft'], ['Tab', 'Tab']] },
  'ctrl-esc': { label: 'Ctrl+Esc', keys: [['Control_L', 'ControlLeft'], ['Escape', 'Escape']] },
  'print': { label: 'PrintScreen', keys: [['Print', 'PrintScreen']] },
  'alt-f4': { label: 'Alt+F4', keys: [['Alt_L', 'AltLeft'], ['F4', 'F4']] },
};
const QUALITY = { high: { label: '高画质', q: 8, c: 2 }, mid: { label: '均衡', q: 6, c: 2 }, low: { label: '省流量', q: 3, c: 7 } };
const STATE_TEXT = { idle: '未连接', connecting: '正在连接…', auth: '等待认证', connected: '已连接', disconnected: '已断开', error: '连接失败', reconnecting: '正在重连…' };
const STATE_BADGE = { connected: 'ok', connecting: 'warn', auth: 'warn', reconnecting: 'warn', error: 'err', disconnected: '' };

injectStyle();

// ---------------------------------------------------------------- VncClient
export class VncClient {
  /** @param {HTMLElement} container @param {string} apiPath e.g. `/nodes/<id>/desktop?kind=vnc` */
  constructor(container, apiPath, opts = {}) {
    this.container = container; this.apiPath = apiPath; this.opts = opts;
    this.rfb = null; this.ws = null; this.creds = null; this.state = 'idle';
    this.closeInfo = null; this.everConnected = false; this.retries = 0; this.retryTimer = null; this.stopped = false;
    this.settings = Object.assign({ scale: 'fit', quality: 'mid', viewOnly: false, autoReconnect: true },
      JSON.parse(localStorage.getItem('agentos.vnc.settings') || '{}'));
    this.onFullscreen = () => this.fsBtn && (this.fsBtn.textContent = document.fullscreenElement ? '退出全屏' : '全屏');
    document.addEventListener('fullscreenchange', this.onFullscreen);
    this.build();
  }

  saveSettings() { localStorage.setItem('agentos.vnc.settings', JSON.stringify(this.settings)); }

  build() {
    const s = this.settings;
    const sel = (opts, value, onchange, title) => {
      const e = h('select', { class: 'vnc-select', title, onchange: (ev) => onchange(ev.target.value) },
        Object.entries(opts).map(([k, v]) => h('option', { value: k, selected: k === value }, v)));
      return e;
    };
    this.statusBadge = h('span', { class: 'badge', 'data-testid': 'vnc-status' }, STATE_TEXT.idle);
    this.connectBtn = h('button', { class: 'btn sm primary', 'data-testid': 'vnc-connect', onclick: () => this.state === 'connected' || this.state === 'connecting' || this.state === 'auth' ? this.disconnect(true) : this.reconnectNow() }, '连接');
    this.fsBtn = h('button', { class: 'btn sm', onclick: () => this.toggleFullscreen() }, '全屏');
    this.keySel = h('select', { class: 'vnc-select', title: '发送组合键', onchange: (e) => { this.sendCombo(e.target.value); e.target.value = ''; } },
      h('option', { value: '' }, '发送按键…'), Object.entries(COMBOS).map(([k, v]) => h('option', { value: k }, v.label)));
    this.clipArea = h('textarea', { class: 'vnc-clip-text mono', rows: 4, placeholder: '在此粘贴文本，发送到远程剪贴板；远程复制的内容也会显示在这里' });
    this.clipPanel = h('div', { class: 'vnc-clip', hidden: true },
      this.clipArea,
      h('div', { class: 'row' },
        h('button', { class: 'btn sm primary', onclick: () => this.pasteClipboard() }, '发送到远程剪贴板'),
        h('button', { class: 'btn sm', title: '逐字模拟键盘输入（适用于不支持剪贴板的登录界面）', onclick: () => this.typeText(this.clipArea.value) }, '模拟键入'),
        h('button', { class: 'btn sm', onclick: () => { this.clipArea.value = ''; } }, '清空'),
        h('span', { class: 'muted', style: 'font-size:12px' }, '远程剪贴板变化会自动同步到此处')));
    const viewOnly = h('input', { type: 'checkbox', checked: s.viewOnly, onchange: (e) => { s.viewOnly = e.target.checked; this.saveSettings(); if (this.rfb) this.rfb.viewOnly = s.viewOnly; } });
    const autoRe = h('input', { type: 'checkbox', checked: s.autoReconnect, onchange: (e) => { s.autoReconnect = e.target.checked; this.saveSettings(); } });
    this.toolbar = h('div', { class: 'vnc-toolbar row' },
      this.connectBtn,
      sel({ fit: '适应窗口', real: '原始大小 (1:1)', remote: '调整远程分辨率' }, s.scale, (v) => { s.scale = v; this.saveSettings(); this.applyView(); }, '缩放模式'),
      sel(Object.fromEntries(Object.entries(QUALITY).map(([k, v]) => [k, v.label])), s.quality, (v) => { s.quality = v; this.saveSettings(); this.applyView(); }, '画质'),
      h('button', { class: 'btn sm', 'data-testid': 'vnc-cad', title: '发送 Ctrl+Alt+Del', onclick: () => this.sendCtrlAltDel() }, 'Ctrl+Alt+Del'),
      this.keySel,
      h('button', { class: 'btn sm', onclick: () => { this.clipPanel.hidden = !this.clipPanel.hidden; } }, '剪贴板'),
      this.fsBtn,
      h('label', { class: 'vnc-check' }, viewOnly, '仅查看'),
      h('label', { class: 'vnc-check' }, autoRe, '自动重连'));
    this.screen = h('div', { class: 'vnc-screen', 'data-testid': 'vnc-screen' });
    this.overlay = h('div', { class: 'vnc-overlay', hidden: true });
    this.view = h('div', { class: 'desktop-wrap vnc-view' }, this.screen, this.overlay);
    this.infoName = h('span', {}); this.infoSize = h('span', { class: 'mono' }); this.infoMsg = h('span', { class: 'muted vnc-msg' });
    this.statusBar = h('div', { class: 'vnc-statusbar' }, this.statusBadge, this.infoName, this.infoSize, this.infoMsg);
    this.container.replaceChildren(h('div', { class: 'vnc-root' + (this.opts.bare ? ' bare' : '') }, this.toolbar, this.clipPanel, this.view, this.statusBar));
  }

  // ---- host-chrome API (used by the console in components/console.js with opts.bare) ----
  // opts.bare hides the built-in toolbar/status bar; state changes go to opts.onState(state, msg),
  // desktop name / framebuffer size to opts.onInfo({name, width, height}).
  setScale(v) { this.settings.scale = v; this.saveSettings(); this.applyView(); }
  setQuality(v) { this.settings.quality = v; this.saveSettings(); this.applyView(); }
  setViewOnly(v) { this.settings.viewOnly = !!v; this.saveSettings(); if (this.rfb) this.rfb.viewOnly = !!v; }
  sendClipboard(text) { this.clipArea.value = text; this.pasteClipboard(); }
  focus() { this.rfb?.focus(); }

  setState(state, msg) {
    this.state = state;
    this.statusBadge.className = 'badge ' + (STATE_BADGE[state] || '');
    this.statusBadge.textContent = STATE_TEXT[state] || state;
    if (msg !== undefined) this.infoMsg.textContent = msg;
    const live = state === 'connected' || state === 'connecting' || state === 'auth';
    this.connectBtn.textContent = live ? '断开' : (this.everConnected || state === 'error' ? '重新连接' : '连接');
    this.connectBtn.className = 'btn sm ' + (live ? 'danger' : 'primary');
    this.container.dataset.state = state;
    this.opts.onState?.(state, msg !== undefined ? msg : this.infoMsg.textContent);
  }

  async connect() {
    this.stopped = false;
    this.setState('connecting', '');
    let RFB;
    try { ({ RFB } = await loadNoVNC()); } catch (e) {
      this.setState('error', '加载 noVNC 失败: ' + e.message); return;
    }
    if (this.stopped) return;
    this.closeInfo = null;
    const ws = new WebSocket(wsURL(this.apiPath), ['binary']);
    ws.binaryType = 'arraybuffer';
    ws.addEventListener('close', (e) => { this.closeInfo = { code: e.code, reason: e.reason }; });
    this.ws = ws;
    this.screen.replaceChildren();
    const rfb = new RFB(this.screen, ws, { shared: true, credentials: this.creds || undefined });
    this.rfb = rfb;
    rfb.background = '#000';
    rfb.focusOnClick = true;
    rfb.viewOnly = this.settings.viewOnly;
    this.applyView();
    rfb.addEventListener('connect', () => {
      this.everConnected = true; this.retries = 0; this.hideOverlay();
      this.setState('connected', '');
      this.updateSize(); rfb.focus();
    });
    rfb.addEventListener('disconnect', (e) => this.onDisconnect(rfb, e.detail.clean));
    rfb.addEventListener('credentialsrequired', (e) => this.promptCredentials(rfb, e.detail.types || ['password']));
    rfb.addEventListener('securityfailure', (e) => {
      this.creds = null;
      this.securityError = '认证失败' + (e.detail.reason ? '：' + e.detail.reason : '（密码错误？）');
    });
    rfb.addEventListener('serververification', (e) => {
      const fp = e.detail?.publickey ? Array.from(e.detail.publickey.slice(0, 16), b => b.toString(16).padStart(2, '0')).join(':') : '';
      if (confirm('确认信任该 VNC 服务器的公钥？\n' + fp)) rfb.approveServer(); else rfb.disconnect();
    });
    rfb.addEventListener('clipboard', (e) => {
      this.clipArea.value = e.detail.text;
      this.infoMsg.textContent = '已收到远程剪贴板内容';
      this.opts.onClipboard?.(e.detail.text);
      navigator.clipboard?.writeText?.(e.detail.text).catch(() => {});
    });
    rfb.addEventListener('desktopname', (e) => { this.infoName.textContent = e.detail.name; this.opts.onInfo?.({ name: e.detail.name }); });
    this.resizeObs = new ResizeObserver(() => this.updateSize());
    this.resizeObs.observe(this.screen);
  }

  async onDisconnect(rfb, clean) {
    if (rfb !== this.rfb) return;
    this.rfb = null; this.ws = null; this.resizeObs?.disconnect();
    if (this.stopped) { this.setState('disconnected', ''); return; }
    const ci = this.closeInfo || {};
    let msg = '';
    if (this.securityError) { msg = this.securityError; this.securityError = null; this.setState('error', msg); this.showOverlay(msg, true); return; }
    if (ci.code === 4502 && ci.reason) msg = ci.reason; // node-side error (VNC down, target refused…)
    else if (!this.everConnected || ci.code === 1006) msg = await this.diagnose();
    const fatal = ci.code === 4502;
    if (!fatal && this.settings.autoReconnect && this.everConnected && this.retries < 5) {
      const delay = Math.min(1000 * 2 ** this.retries, 15000);
      this.retries++;
      this.setState('reconnecting', `连接中断，${Math.round(delay / 1000)} 秒后第 ${this.retries} 次重连…`);
      this.retryTimer = setTimeout(() => this.connect(), delay);
      return;
    }
    this.setState(clean && !msg ? 'disconnected' : 'error', msg || (clean ? '会话已结束' : '连接意外断开'));
    this.showOverlay(msg || (clean ? '会话已结束' : '连接意外断开'), true);
  }

  async diagnose() {
    const id = this.apiPath.split('/')[2];
    try {
      const n = await api('GET', '/nodes/' + id);
      if (n.status !== 'online') return '节点已离线';
    } catch { return '无法访问服务器'; }
    return '无法建立远程桌面隧道（节点无响应或 VNC 服务不可用）';
  }

  showOverlay(msg, withActions) {
    if (this.opts.bare) return; // host chrome renders its own connection overlay
    const acts = [];
    if (withActions) {
      acts.push(h('button', { class: 'btn primary', onclick: () => this.reconnectNow() }, '重新连接'));
      if (this.opts.onFallback) acts.push(h('button', { class: 'btn', onclick: () => this.opts.onFallback() }, '改用内置屏幕流'));
    }
    this.overlay.replaceChildren(h('div', { class: 'vnc-dialog' }, h('div', { class: 'vnc-dialog-msg' }, msg), h('div', { class: 'row' }, acts)));
    this.overlay.hidden = false;
  }
  hideOverlay() { this.overlay.hidden = true; this.overlay.replaceChildren(); }

  promptCredentials(rfb, types) {
    this.setState('auth', '服务器要求认证');
    const fields = {};
    const label = { username: '用户名', password: 'VNC 密码', target: '目标' };
    const form = h('form', { class: 'vnc-dialog vnc-auth', 'data-testid': 'vnc-auth', onsubmit: (e) => {
      e.preventDefault();
      const creds = {};
      for (const t of types) creds[t] = fields[t].value;
      this.creds = creds;
      this.hideOverlay();
      this.setState('connecting', '正在认证…');
      rfb.sendCredentials(creds);
    } },
      h('div', { class: 'vnc-dialog-msg' }, h('b', {}, '需要 VNC 认证'), h('div', { class: 'muted', style: 'font-size:12px;margin-top:4px' }, '密码仅用于本次会话，不会保存到服务器')),
      types.map(t => h('label', { class: 'vnc-field' }, label[t] || t,
        fields[t] = h('input', { type: t === 'password' ? 'password' : 'text', name: t, autocomplete: t === 'password' ? 'current-password' : 'username', value: this.creds?.[t] || '' }))),
      h('div', { class: 'row' }, h('button', { class: 'btn primary', type: 'submit' }, '确定'),
        h('button', { class: 'btn', type: 'button', onclick: () => this.disconnect(true) }, '取消')));
    this.overlay.replaceChildren(form);
    this.overlay.hidden = false;
    setTimeout(() => (fields.username && !fields.username.value ? fields.username : fields.password || Object.values(fields)[0])?.focus(), 0);
  }

  applyView() {
    const rfb = this.rfb; if (!rfb) return;
    const s = this.settings;
    rfb.scaleViewport = s.scale === 'fit';
    rfb.resizeSession = s.scale === 'remote';
    rfb.clipViewport = false;
    this.view.classList.toggle('vnc-real', s.scale === 'real');
    const q = QUALITY[s.quality] || QUALITY.mid;
    rfb.qualityLevel = q.q; rfb.compressionLevel = q.c;
  }

  updateSize() {
    const c = this.screen.querySelector('canvas');
    this.infoSize.textContent = c && c.width ? `${c.width}×${c.height}` : '';
    if (c && c.width) this.opts.onInfo?.({ width: c.width, height: c.height });
  }

  sendCtrlAltDel() { if (this.rfb && !this.settings.viewOnly) { this.rfb.sendCtrlAltDel(); toast('已发送 Ctrl+Alt+Del'); } }
  sendCombo(k) {
    const c = COMBOS[k]; if (!c || !this.rfb || this.settings.viewOnly) return;
    if (k === 'ctrl-alt-del') return this.sendCtrlAltDel();
    for (const [sym, code] of c.keys) this.rfb.sendKey(XK[sym], code, true);
    for (const [sym, code] of [...c.keys].reverse()) this.rfb.sendKey(XK[sym], code, false);
    this.rfb.focus();
  }
  pasteClipboard() {
    if (!this.rfb) return toast('未连接', true);
    this.rfb.clipboardPasteFrom(this.clipArea.value);
    toast('已发送到远程剪贴板');
  }
  typeText(text) {
    if (!this.rfb || this.settings.viewOnly) return toast('未连接或处于仅查看模式', true);
    for (const ch of text.replace(/\r\n/g, '\n')) {
      const sym = ch === '\n' ? XK.Return : novnc.keysyms.lookup(ch.codePointAt(0));
      this.rfb.sendKey(sym, null, true); this.rfb.sendKey(sym, null, false);
    }
    this.rfb.focus();
  }

  toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else this.view.requestFullscreen?.().catch((e) => toast('无法进入全屏: ' + e.message, true));
  }

  reconnectNow() {
    clearTimeout(this.retryTimer); this.retries = 0; this.hideOverlay();
    if (this.rfb) { const r = this.rfb; this.rfb = null; r.disconnect(); }
    this.connect();
  }

  disconnect(userInitiated) {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    this.hideOverlay();
    if (this.rfb) { const r = this.rfb; this.rfb = null; try { r.disconnect(); } catch {} }
    this.resizeObs?.disconnect();
    if (userInitiated) this.setState('disconnected', '已手动断开');
    if (!userInitiated) document.removeEventListener('fullscreenchange', this.onFullscreen);
  }
}

// Mode-switch hook: tunnel kind -> client. M5's ScreenClient is the fallback.
export const DESKTOP_CLIENTS = {
  vnc: { label: 'VNC', cap: 'desktop.vnc', Client: VncClient },
  screen: { label: '内置屏幕流', cap: 'desktop.screen', Client: ScreenClient },
};

// Pick the initial kind: remembered choice if supported, else first supported, else vnc.
export function pickKind(node, remembered) {
  const caps = node.capabilities || [];
  if (remembered && DESKTOP_CLIENTS[remembered] && caps.includes(DESKTOP_CLIENTS[remembered].cap)) return remembered;
  return Object.keys(DESKTOP_CLIENTS).find(k => caps.includes(DESKTOP_CLIENTS[k].cap)) || 'vnc';
}

// --------------------------------------------------------------------- page
export default {
  title: '远程桌面',
  async render(el, { id }) {
    if (!id) {
      const nodes = (await api('GET', '/nodes')).filter(n => n.status === 'online');
      el.append(h('div', { class: 'head' }, h('h1', {}, '远程桌面')),
        h('div', { class: 'card' }, nodes.length ? h('table', {}, nodes.map(n => h('tr', {},
          h('td', {}, h('b', {}, n.name)), h('td', {}, `${n.os}/${n.arch}`),
          h('td', {}, capText(n)), h('td', {}, h('a', { class: 'btn sm primary', href: `#/nodes/${n.id}/desktop` }, '连接')))))
          : h('div', { class: 'empty' }, '没有在线节点')));
      return;
    }
    const n = await api('GET', '/nodes/' + id);
    const host = h('div', { class: 'desktop-host' });
    let client = null, kind = null;
    const key = 'agentos.desktop.kind.' + id;
    const modeBtns = {};
    const lowMem = () => {
      try {
        const dm = navigator.deviceMemory;
        if (typeof dm === 'number' && dm > 0 && dm <= 4) return true;
      } catch {}
      return false;
    };
    const select = (k, autoConnect = false) => {
      client?.disconnect();
      kind = k; localStorage.setItem(key, k);
      for (const [kk, b] of Object.entries(modeBtns)) b.className = 'btn sm' + (kk === k ? ' primary' : '');
      const spec = DESKTOP_CLIENTS[k];
      let opts = {};
      if (k === 'vnc' && (n.capabilities || []).includes('desktop.screen')) opts = { onFallback: () => select('screen', true) };
      if (k === 'screen') {
        // Cap stream for nested VMs / low RAM; user still must click 连接 (no auto-connect).
        opts = { ...opts, fps: lowMem() ? 4 : 6, scale: lowMem() ? 0.5 : 0.75, quality: lowMem() ? 40 : 55 };
      }
      client = new spec.Client(host, `/nodes/${id}/desktop?kind=${k}`, opts);
      // Do not auto-connect on page open: WebView2+stream can OOM nested QEMU guests (~4 GiB).
      if (autoConnect) client.connect();
      else host.dataset.state = 'disconnected';
    };
    for (const [k, spec] of Object.entries(DESKTOP_CLIENTS)) {
      const has = (n.capabilities || []).includes(spec.cap);
      modeBtns[k] = h('button', { class: 'btn sm', 'data-kind': k, 'data-testid': 'mode-' + k,
        title: has ? '' : '节点上线时未检测到该能力，仍可尝试连接', onclick: () => select(k, true) }, spec.label + (has ? '' : ' (未检测到)'));
    }
    el.append(h('div', { class: 'head' }, h('h1', {}, `远程桌面 · ${n.name}`),
      h('div', { class: 'row' }, h('span', { class: 'muted' }, n.status === 'online' ? capText(n) : '节点离线'),
        h('span', { class: 'muted' }, '连接方式:'), Object.values(modeBtns))),
      host);
    if (n.status === 'online') select(pickKind(n, localStorage.getItem(key)), false);
    else host.replaceChildren(h('div', { class: 'desktop-wrap' }, h('span', { class: 'muted' }, '节点离线，无法连接远程桌面')));
    return () => client?.disconnect();
  },
};

function capText(n) {
  const c = [];
  if (n.capabilities.includes('desktop.vnc')) c.push('VNC');
  if (n.capabilities.includes('desktop.screen')) c.push('内置屏幕流');
  return c.length ? '支持: ' + c.join(' / ') : '该节点暂无可用远程桌面';
}

function injectStyle() {
  if (document.getElementById('vnc-style')) return;
  document.head.append(h('style', { id: 'vnc-style' }, `
.vnc-root{display:flex;flex-direction:column;gap:8px}
.vnc-toolbar{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:8px 10px}
.vnc-select{width:auto;padding:4px 8px;font-size:12px}
.vnc-check{display:inline-flex;align-items:center;gap:4px;font-size:12px;color:var(--muted);cursor:pointer}
.vnc-check input{width:auto}
.vnc-clip{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:10px;display:flex;flex-direction:column;gap:8px}
.vnc-clip[hidden]{display:none}
.vnc-view{height:calc(100vh - 230px);min-height:360px;display:block;padding:0}
.vnc-view:fullscreen{height:100vh;border-radius:0}
.vnc-screen{width:100%;height:100%}
.vnc-view.vnc-real .vnc-screen{overflow:auto}
.vnc-view canvas{max-width:none}
.vnc-overlay{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.55);z-index:5}
.vnc-overlay[hidden]{display:none}
.vnc-dialog{background:var(--layer-solid);color:var(--text);border-radius:8px;padding:20px 22px;min-width:320px;max-width:520px;display:flex;flex-direction:column;gap:12px;box-shadow:0 10px 30px rgba(0,0,0,.3)}
.vnc-dialog-msg{word-break:break-word}
.vnc-field{display:flex;flex-direction:column;gap:4px;color:var(--muted);font-size:12px}
.vnc-statusbar{display:flex;gap:14px;align-items:center;font-size:12px;padding:2px 4px}
.vnc-msg{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
`));
}
