// Browser client for the built-in screen stream (owned by M5).
// Protocol: docs/protocol.md §5 – binary frames are full JPEG images, text
// frames are JSON control/input messages.
//
// Usage (pages/desktop.js):
//   const c = new ScreenClient(containerEl, `/nodes/${id}/desktop?kind=screen`, { fps: 8 });
//   c.on('status', ({ state, message }) => ...);   // optional
//   c.connect();
//   ...
//   c.disconnect();                                  // also on page cleanup
//
// Options: fps(1-30, 8) quality(1-100, 60) scale(0.1-1, 1) display(0)
//   toolbar(true)  – render the built-in zh-CN toolbar (set false to build your own)
//   overlay(true)  – render the built-in connection overlay (set false when the host draws one)
//   viewOnly(false) fit(true) autoReconnect(false) pingInterval(2000ms)
// Methods: connect() disconnect() reconnect() setConfig({fps,quality,scale,display})
//   setViewOnly(b) setFit(b) toggleFullscreen() sendText(s) sendKeys(codes[])
//   sendCtrlAltDel() focus() on(evt, fn) → unsubscribe  off(evt, fn)
// Getters: state ('idle'|'connecting'|'connected'|'disconnected'|'error'; mirrored to
//   containerEl.dataset.state with 'idle' shown as 'disconnected'), info (last
//   hello: {width,height,displays,display,fps,quality,scale,input,bounds}),
//   stats ({fps, kbps, latency /*ms RTT or null*/, frames}), canvas
// Events: 'status' {state,message} · 'hello' info · 'frame' {width,height} ·
//   'stats' stats · 'error' {message}
import { h } from '../util.js';
import { wsURL } from '../api.js';

const DEFAULTS = { fps: 8, quality: 60, scale: 1, display: 0, toolbar: true, overlay: true, viewOnly: false,
  fit: true, autoReconnect: false, pingInterval: 2000 };
const STATE_TEXT = { idle: '未连接', connecting: '连接中…', connected: '已连接', disconnected: '已断开', error: '连接失败' };
const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));

export class ScreenClient {
  constructor(container, apiPath, options = {}) {
    this.container = container;
    this.apiPath = apiPath;
    this.opts = { ...DEFAULTS, ...options };
    this.ws = null;
    this._state = 'idle';
    this._info = null;
    this._handlers = new Map();
    this._pressed = new Set();
    this._buttons = new Set();
    this._pending = null;      // latest undecoded frame (older ones are dropped)
    this._decoding = false;
    this._timers = [];
    this._stats = { fps: 0, kbps: 0, latency: null, frames: 0 };
    this._win = { frames: 0, bytes: 0, t: performance.now() };
    this._moveQueued = null;
    this._wheelAcc = { x: 0, y: 0 };
    this._closing = false;
    this._buildDOM();
  }

  // ---------- public API ----------
  get state() { return this._state; }
  get info() { return this._info; }
  get stats() { return { ...this._stats }; }
  get canvas() { return this.canvasEl; }

  on(evt, fn) {
    if (!this._handlers.has(evt)) this._handlers.set(evt, new Set());
    this._handlers.get(evt).add(fn);
    return () => this.off(evt, fn);
  }
  off(evt, fn) { this._handlers.get(evt)?.delete(fn); }

  connect() {
    this.disconnect();
    this._closing = false;
    this.container.replaceChildren(this.root);
    const o = this.opts;
    const q = new URLSearchParams({ fps: o.fps, quality: o.quality, scale: o.scale, display: o.display });
    const url = wsURL(this.apiPath + (this.apiPath.includes('?') ? '&' : '?') + q);
    this._setState('connecting');
    let ws;
    try { ws = new WebSocket(url); } catch (e) { this._setState('error', e.message); return; }
    ws.binaryType = 'blob';
    this.ws = ws;
    ws.onmessage = (ev) => this._onMessage(ev);
    ws.onerror = () => { if (this.ws === ws && this._state === 'connecting') this._setState('error', '无法建立连接（节点离线或隧道失败）'); };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this._stopTimers();
      if (this._state !== 'error') this._setState('disconnected', this._lastError || '');
      if (this.opts.autoReconnect && !this._closing) { this._retryDelay = Math.min((this._retryDelay || 1000) * 2, 15000); this._timers.push(setTimeout(() => this.connect(), this._retryDelay)); }
    };
    this._timers.push(setInterval(() => this._ping(), this.opts.pingInterval));
    this._timers.push(setInterval(() => this._tickStats(), 1000));
  }

  disconnect() {
    this._closing = true;
    this._releaseAll();
    this._stopTimers();
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.onclose = ws.onmessage = ws.onerror = null;
      try { ws.close(); } catch { /* ignore */ }
      this._setState('disconnected');
    }
  }

  reconnect() { this.connect(); }

  setConfig(cfg = {}) {
    const c = {};
    if (cfg.fps != null) c.fps = this.opts.fps = clamp(+cfg.fps, 1, 30);
    if (cfg.quality != null) c.quality = this.opts.quality = clamp(+cfg.quality, 1, 100);
    if (cfg.scale != null) c.scale = this.opts.scale = clamp(+cfg.scale, 0.1, 1);
    if (cfg.display != null) c.display = this.opts.display = Math.max(0, cfg.display | 0);
    this._syncToolbar();
    this._send({ type: 'config', ...c });
  }

  setViewOnly(v) {
    if (v) this._releaseAll();
    this.opts.viewOnly = !!v;
    this.canvasEl.style.cursor = v ? 'not-allowed' : 'default';
    this._syncToolbar();
  }

  setFit(v) { this.opts.fit = !!v; this._layout(); this._syncToolbar(); }

  toggleFullscreen() {
    if (document.fullscreenElement) document.exitFullscreen();
    else this.root.requestFullscreen?.().then(() => this.focus()).catch(() => {});
  }

  sendText(text) { if (text && !this.opts.viewOnly) this._send({ type: 'text', text: String(text) }); }

  // Press codes in order, release in reverse (e.g. ['ControlLeft','AltLeft','Delete']).
  sendKeys(codes) {
    if (this.opts.viewOnly) return;
    for (const code of codes) this._send({ type: 'key', action: 'down', code, key: '' });
    for (const code of [...codes].reverse()) this._send({ type: 'key', action: 'up', code, key: '' });
  }

  // Note: Windows blocks injected Ctrl+Alt+Del (secure attention sequence); it
  // works for apps/remote sessions that read it as a normal key combo.
  sendCtrlAltDel() { this.sendKeys(['ControlLeft', 'AltLeft', 'Delete']); }

  focus() { this.canvasEl.focus({ preventScroll: true }); }

  // ---------- internals ----------
  _emit(evt, data) { for (const fn of this._handlers.get(evt) || []) { try { fn(data); } catch (e) { console.error(e); } } }

  _setState(state, message = '') {
    this._state = state;
    // Host contract (pages/desktop.js): connecting | connected | disconnected | error
    if (state === 'connected') this._retryDelay = 1000;
    if (this.container?.dataset) this.container.dataset.state = state === 'idle' ? 'disconnected' : state;
    if (this.statusEl) {
      this.statusEl.textContent = STATE_TEXT[state] + (message ? '：' + message : '');
      this.statusEl.style.color = state === 'connected' ? '#16a34a' : state === 'error' ? '#dc2626' : '#6b7280';
    }
    if (this.overlayEl && this.opts.overlay) {
      const show = state !== 'connected';
      this.overlayEl.style.display = show ? 'flex' : 'none';
      this.overlayText.textContent = STATE_TEXT[state] + (message ? '：' + message : '');
      // Show Connect on idle too — desktop page no longer auto-connects (nested VM OOM).
      this.overlayBtn.style.display = (state === 'idle' || state === 'disconnected' || state === 'error') ? '' : 'none';
      if (this.overlayBtn) this.overlayBtn.textContent = state === 'idle' ? '连接' : '重新连接';
    }
    this._emit('status', { state, message });
  }

  _send(obj) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(obj));
  }

  _stopTimers() { for (const t of this._timers) { clearInterval(t); clearTimeout(t); } this._timers = []; }

  _ping() { this._send({ type: 'ping', t: performance.now() }); }

  _tickStats() {
    const now = performance.now(), dt = (now - this._win.t) / 1000 || 1;
    this._stats.fps = Math.round(this._win.frames / dt * 10) / 10;
    this._stats.kbps = Math.round(this._win.bytes * 8 / 1000 / dt);
    this._win = { frames: 0, bytes: 0, t: now };
    if (this.metricsEl) {
      const lat = this._stats.latency == null ? '–' : this._stats.latency + ' ms';
      this.metricsEl.textContent = `${this._stats.fps} 帧/秒 · ${this._stats.kbps} kbps · 延迟 ${lat}`;
    }
    this._emit('stats', this.stats);
  }

  _onMessage(ev) {
    if (typeof ev.data === 'string') {
      let m; try { m = JSON.parse(ev.data); } catch { return; }
      if (m.type === 'hello') {
        this._info = m;
        this.opts.display = m.display ?? this.opts.display;
        if (m.fps) this.opts.fps = m.fps;
        if (m.quality) this.opts.quality = m.quality;
        if (m.scale) this.opts.scale = m.scale;
        this._lastError = '';
        this._setState('connected');
        this._syncToolbar();
        this._layout();
        this._emit('hello', m);
      } else if (m.type === 'pong') {
        this._stats.latency = Math.round(performance.now() - m.t);
      } else if (m.type === 'error') {
        this._lastError = m.message;
        if (this._state !== 'connected') this._setState('error', m.message);
        this._emit('error', { message: m.message });
      }
      return;
    }
    this._win.frames++; this._win.bytes += ev.data.size; this._stats.frames++;
    this._pending = ev.data;
    if (!this._decoding) this._decodeLoop();
  }

  async _decodeLoop() {
    this._decoding = true;
    while (this._pending) {
      const blob = this._pending; this._pending = null;
      try {
        const bmp = await createImageBitmap(blob);
        const c = this.canvasEl;
        if (c.width !== bmp.width || c.height !== bmp.height) { c.width = bmp.width; c.height = bmp.height; this._layout(); }
        this.ctx.drawImage(bmp, 0, 0);
        bmp.close?.();
        this._emit('frame', { width: c.width, height: c.height });
      } catch { /* corrupt frame: skip */ }
    }
    this._decoding = false;
  }

  _layout() {
    const c = this.canvasEl, info = this._info;
    if (this.opts.fit) {
      Object.assign(c.style, { width: '', height: '', maxWidth: '100%', maxHeight: document.fullscreenElement ? (this.opts.toolbar ? 'calc(100vh - 44px)' : '100vh') : (this.opts.toolbar ? '78vh' : '100%'), objectFit: 'contain' });
    } else {
      const w = info ? info.width : c.width, hh = info ? info.height : c.height;
      Object.assign(c.style, { width: w / (window.devicePixelRatio || 1) + 'px', height: hh / (window.devicePixelRatio || 1) + 'px', maxWidth: 'none', maxHeight: 'none' });
    }
  }

  // Map a pointer event to remote pixels of the selected display.
  _remotePos(ev) {
    const r = this.canvasEl.getBoundingClientRect();
    const info = this._info || { width: this.canvasEl.width, height: this.canvasEl.height };
    const x = clamp(Math.floor((ev.clientX - r.left) / r.width * info.width), 0, info.width - 1);
    const y = clamp(Math.floor((ev.clientY - r.top) / r.height * info.height), 0, info.height - 1);
    return { x, y };
  }

  _inputAllowed() { return !this.opts.viewOnly && this._state === 'connected' && (!this._info || this._info.input !== false); }

  _releaseAll() {
    for (const code of this._pressed) this._send({ type: 'key', action: 'up', code, key: '' });
    this._pressed.clear();
    for (const b of this._buttons) this._send({ type: 'mouse', action: 'up', x: this._lastPos?.x || 0, y: this._lastPos?.y || 0, button: b });
    this._buttons.clear();
  }

  _bindInput(c) {
    c.addEventListener('contextmenu', (e) => e.preventDefault());
    c.addEventListener('pointermove', (e) => {
      if (!this._inputAllowed()) return;
      this._lastPos = this._remotePos(e);
      if (this._moveQueued) return; // coalesce to one move per animation frame
      this._moveQueued = requestAnimationFrame(() => {
        this._moveQueued = null;
        this._send({ type: 'mouse', action: 'move', ...this._lastPos, button: 0 });
      });
    });
    c.addEventListener('pointerdown', (e) => {
      this.focus();
      if (!this._inputAllowed()) return;
      e.preventDefault();
      c.setPointerCapture?.(e.pointerId);
      this._lastPos = this._remotePos(e);
      this._buttons.add(e.button);
      this._send({ type: 'mouse', action: 'down', ...this._lastPos, button: e.button });
    });
    c.addEventListener('pointerup', (e) => {
      if (!this._inputAllowed()) return;
      e.preventDefault();
      this._lastPos = this._remotePos(e);
      this._buttons.delete(e.button);
      this._send({ type: 'mouse', action: 'up', ...this._lastPos, button: e.button });
    });
    c.addEventListener('wheel', (e) => {
      if (!this._inputAllowed()) return;
      e.preventDefault();
      // Normalise to 120 units per notch (deltaMode: 0 px, 1 lines, 2 pages).
      const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
      this._wheelAcc.x += e.deltaX * k; this._wheelAcc.y += e.deltaY * k;
      const step = (v) => (Math.abs(v) >= 50 ? Math.sign(v) * Math.max(120, Math.round(Math.abs(v) / 120) * 120) : 0);
      const dx = step(this._wheelAcc.x), dy = step(this._wheelAcc.y);
      if (!dx && !dy) return;
      if (dx) this._wheelAcc.x = 0;
      if (dy) this._wheelAcc.y = 0;
      this._send({ type: 'mouse', action: 'wheel', ...this._remotePos(e), button: 0, dx, dy });
    }, { passive: false });
    c.addEventListener('keydown', (e) => {
      if (!this._inputAllowed() || e.isComposing || e.key === 'Process') return;
      e.preventDefault();
      this._pressed.add(e.code);
      this._send({ type: 'key', action: 'down', code: e.code, key: e.key });
    });
    c.addEventListener('keyup', (e) => {
      if (!this._inputAllowed()) return;
      e.preventDefault();
      this._pressed.delete(e.code);
      this._send({ type: 'key', action: 'up', code: e.code, key: e.key });
    });
    c.addEventListener('blur', () => this._releaseAll());
    c.addEventListener('paste', (e) => {
      const t = e.clipboardData?.getData('text');
      if (t) { e.preventDefault(); this.sendText(t); }
    });
  }

  _buildDOM() {
    const c = this.canvasEl = h('canvas', { tabindex: '0', title: '点击后可操作远程桌面',
      style: 'display:block;outline:none;cursor:default;touch-action:none;background:#000;max-width:100%' });
    c.width = 1; c.height = 1;
    this.ctx = c.getContext('2d', { alpha: false });
    this._bindInput(c);
    this.overlayText = h('span', {}, '');
    this.overlayBtn = h('button', { class: 'btn sm', onclick: () => this.connect() }, '重新连接');
    this.overlayEl = h('div', { style: 'position:absolute;inset:0;display:flex;flex-direction:column;gap:10px;align-items:center;justify-content:center;color:#e5e7eb;background:rgba(0,0,0,.55);pointer-events:auto' },
      this.overlayText, this.overlayBtn);
    if (!this.opts.overlay) { this.overlayEl.hidden = true; this.overlayEl.style.display = 'none'; } // inline display:flex would override [hidden] and swallow all pointer input
    this.viewport = h('div', { class: 'sc-viewport', style: 'position:relative;flex:1;display:flex;align-items:center;justify-content:center;overflow:auto;min-height:420px;background:#000' }, c, this.overlayEl);
    this.root = h('div', { class: 'screen-client', style: 'width:100%;align-self:stretch;display:flex;flex-direction:column;background:#000' });
    if (this.opts.toolbar) this.root.append(this._buildToolbar());
    this.root.append(this.viewport);
    this.root.addEventListener('fullscreenchange', () => this._layout());
    this._setState('idle');
  }

  _buildToolbar() {
    const sel = (opts, cur, onchange) => {
      const s = h('select', { style: 'width:auto;padding:3px 6px;font-size:12px', onchange: () => onchange(s.value) },
        opts.map(([v, t]) => h('option', { value: v }, t)));
      s.value = String(cur);
      return s;
    };
    this.fpsSel = sel([[2, '2 帧/秒'], [5, '5 帧/秒'], [8, '8 帧/秒'], [10, '10 帧/秒'], [15, '15 帧/秒'], [20, '20 帧/秒'], [30, '30 帧/秒']],
      this.opts.fps, (v) => this.setConfig({ fps: +v }));
    this.qSel = sel([[30, '画质 低'], [60, '画质 中'], [80, '画质 高'], [95, '画质 极高']], this.opts.quality, (v) => this.setConfig({ quality: +v }));
    this.scaleSel = sel([[0.25, '缩放 25%'], [0.5, '缩放 50%'], [0.75, '缩放 75%'], [1, '缩放 100%']], this.opts.scale, (v) => this.setConfig({ scale: +v }));
    this.dispSel = sel([[0, '显示器 1']], this.opts.display, (v) => this.setConfig({ display: +v }));
    this.viewOnlyBtn = h('button', { class: 'btn sm', onclick: () => this.setViewOnly(!this.opts.viewOnly) }, '仅查看');
    this.fitBtn = h('button', { class: 'btn sm', onclick: () => this.setFit(!this.opts.fit) }, '适应窗口');
    const textIn = h('input', { placeholder: '发送文本（支持中文）', style: 'width:180px;padding:3px 8px;font-size:12px',
      onkeydown: (e) => { if (e.key === 'Enter') { this.sendText(textIn.value); textIn.value = ''; } } });
    this.statusEl = h('span', { style: 'font-size:12px' });
    this.metricsEl = h('span', { class: 'mono', style: 'font-size:11px;color:#9ca3af' }, '');
    const bar = h('div', { style: 'display:flex;flex-wrap:wrap;gap:6px;align-items:center;padding:6px 8px;background:#111827;color:#e5e7eb' },
      this.statusEl, this.metricsEl, h('span', { style: 'flex:1' }),
      this.dispSel, this.fpsSel, this.qSel, this.scaleSel, this.fitBtn, this.viewOnlyBtn,
      h('button', { class: 'btn sm', title: '发送 Ctrl+Alt+Del', onclick: () => this.sendCtrlAltDel() }, 'Ctrl+Alt+Del'),
      textIn, h('button', { class: 'btn sm', onclick: () => { this.sendText(textIn.value); textIn.value = ''; } }, '发送'),
      h('button', { class: 'btn sm', onclick: () => this.toggleFullscreen() }, '全屏'),
      h('button', { class: 'btn sm', onclick: () => this.connect() }, '重连'));
    return bar;
  }

  _syncToolbar() {
    if (!this.opts.toolbar || !this.fpsSel) return;
    const setSel = (s, v) => {
      if (![...s.options].some((o) => o.value === String(v))) s.append(h('option', { value: v }, String(v)));
      s.value = String(v);
    };
    const n = this._info?.displays || 1;
    if (this.dispSel.options.length !== n) {
      this.dispSel.replaceChildren(...Array.from({ length: n }, (_, i) => h('option', { value: i }, `显示器 ${i + 1}`)));
    }
    this.dispSel.style.display = n > 1 ? '' : 'none';
    setSel(this.dispSel, this.opts.display);
    setSel(this.fpsSel, this.opts.fps);
    setSel(this.qSel, this.opts.quality);
    setSel(this.scaleSel, this.opts.scale);
    this.viewOnlyBtn.classList.toggle('primary', this.opts.viewOnly);
    this.fitBtn.classList.toggle('primary', this.opts.fit);
    this.viewOnlyBtn.disabled = this._info?.input === false;
    if (this._info?.input === false) this.viewOnlyBtn.title = '该节点不支持输入注入（仅查看）';
  }
}
