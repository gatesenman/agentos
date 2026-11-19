// Live log viewer: WS stream, ANSI colours, search/highlight/filter, download (owned by M6).
import { h, icon, debounce, toast } from '../util.js';
import { openWS, apiFetch, api } from '../api.js';
import { t, locale } from '../i18n.js';
import { renderAnsi, stripAnsi } from '../ansi.js';

const MAX = 8000; // buffered lines
const DOM_MAX = 2000; // lines kept in the DOM (windowed from the end when following)
const pref = (k, d) => { try { const v = localStorage.getItem('agentos.log.' + k); return v == null ? d : v === '1'; } catch { return d; } };
const setPref = (k, v) => { try { localStorage.setItem('agentos.log.' + k, v ? '1' : '0'); } catch {} };

export class LogView {
  constructor(container, { agentId, name }) {
    this.id = agentId; this.name = name || agentId;
    this.lines = []; this.keys = new Set(); this.query = ''; this.re = null; this.stream = '';
    this.marks = []; this.cur = -1; this.closed = false; this.ws = null; this.retry = null;
    const sw = (key, label, def, on) => {
      const cb = h('input', { type: 'checkbox', checked: pref(key, def), onchange: () => { setPref(key, cb.checked); on(cb.checked); } });
      return [cb, h('label', { class: 'chk' }, cb, label)];
    };
    this.search = h('input', { type: 'search', placeholder: t('logs.search'), 'aria-label': t('logs.search'),
      oninput: debounce(() => this.setQuery(this.search.value), 150),
      onkeydown: (e) => { if (e.key === 'Enter') { e.preventDefault(); this.jump(e.shiftKey ? -1 : 1); } } });
    this.count = h('span', { class: 'small muted nowrap' });
    [this.onlyCb, this.onlyLbl] = sw('only', t('logs.onlyMatch'), false, () => this.rerender());
    [this.followCb, this.followLbl] = sw('follow', t('logs.follow'), true, (v) => v && this.scrollEnd());
    [, this.wrapLbl] = sw('wrap', t('logs.wrap'), true, (v) => this.box.classList.toggle('nowrap', !v));
    [, this.tsLbl] = sw('ts', t('logs.ts'), true, (v) => this.box.classList.toggle('nots', !v));
    this.streamSel = h('select', { class: 'inline', style: 'padding:5px 8px;font-size:12px', onchange: () => { this.stream = this.streamSel.value; this.rerender(); } },
      h('option', { value: '' }, t('logs.allStreams')), ['stdout', 'stderr', 'system'].map((s) => h('option', { value: s }, s)));
    this.box = h('div', { class: 'logs' + (pref('wrap', true) ? '' : ' nowrap') + (pref('ts', true) ? '' : ' nots'), tabindex: '0', role: 'log', 'aria-live': 'off' });
    this.box.addEventListener('scroll', () => {
      const atEnd = this.box.scrollHeight - this.box.scrollTop - this.box.clientHeight < 30;
      if (!atEnd && this.followCb.checked && !this.autoScrolling) { this.followCb.checked = false; }
      else if (atEnd && !this.followCb.checked && pref('follow', true)) this.followCb.checked = true;
      this.autoScrolling = false;
    });
    this.status = h('span', { class: 'live' }, '…'); this.total = h('span');
    this.el = h('div', { class: 'logview' },
      h('div', { class: 'toolbar', style: 'margin:0' },
        h('div', { class: 'search grow', style: 'max-width:340px' }, icon('search'), this.search),
        h('button', { class: 'btn sm icon-only', title: t('logs.prev'), 'aria-label': t('logs.prev'), onclick: () => this.jump(-1) }, icon('up')),
        h('button', { class: 'btn sm icon-only', title: t('logs.next'), 'aria-label': t('logs.next'), onclick: () => this.jump(1) }, icon('down')),
        this.count, this.onlyLbl, h('span', { class: 'grow' }), this.streamSel, this.followLbl, this.wrapLbl, this.tsLbl,
        h('button', { class: 'btn sm', onclick: () => this.download() }, icon('download'), t('logs.download')),
        h('button', { class: 'btn sm', onclick: () => { this.lines = []; this.rerender(); } }, t('logs.clear'))),
      this.box, h('div', { class: 'logfoot' }, this.status, this.total));
    container.append(this.el);
    this.rerender();
    this.connect();
  }

  connect() {
    if (this.closed) return;
    const tail = this.lines.length ? 200 : 500;
    const ws = this.ws = openWS(`/agents/${encodeURIComponent(this.id)}/logs/stream?tail=${tail}`);
    let batch = [], raf = 0;
    ws.onopen = () => this.setLive(true);
    ws.onmessage = (m) => {
      let ev; try { ev = JSON.parse(m.data); } catch { return; }
      if (ev.type && ev.type !== 'agent.log') return;
      batch.push(ev.payload);
      if (!raf) raf = requestAnimationFrame(() => { raf = 0; const b = batch; batch = []; this.add(b); });
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.setLive(false);
      if (!this.closed) this.retry = setTimeout(() => this.connect(), 2000);
    };
    ws.onerror = () => {};
  }
  setLive(on) { this.status.className = 'live' + (on ? ' on' : ''); this.status.textContent = on ? t('logs.live') : t('logs.disconnected'); }
  destroy() { this.closed = true; clearTimeout(this.retry); if (this.ws) { const w = this.ws; this.ws = null; w.close(); } }

  setQuery(q) {
    this.query = q;
    this.re = q ? new RegExp(q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi') : null;
    this.rerender();
  }
  visible(l) {
    if (this.stream && l.stream !== this.stream) return false;
    if (this.re && this.onlyCb.checked) { this.re.lastIndex = 0; return this.re.test(stripAnsi(l.text)); }
    return true;
  }
  lineEl(l) {
    const tx = h('span', { class: 'tx' });
    const n = renderAnsi(tx, l.text, this.re);
    const d = new Date(l.time);
    const el = h('div', { class: 'ln ' + (l.stream || '') },
      h('span', { class: 'ts', title: d.toLocaleString(locale(), { hour12: false }) }, d.toLocaleTimeString(locale(), { hour12: false })), tx);
    if (n) this.marks.push(...el.querySelectorAll('mark'));
    return el;
  }
  add(batch) {
    const fresh = [];
    for (const l of batch) {
      if (!l) continue;
      const key = l.seq + '|' + l.time;
      if (this.keys.has(key)) continue;
      this.keys.add(key); fresh.push(l);
    }
    if (!fresh.length) return;
    fresh.sort((a, b) => (a.time < b.time ? -1 : a.time > b.time ? 1 : a.seq - b.seq));
    this.lines.push(...fresh);
    if (this.lines.length > MAX) {
      const drop = this.lines.splice(0, this.lines.length - MAX);
      drop.forEach((l) => this.keys.delete(l.seq + '|' + l.time));
    }
    // Following: append and trim DOM to last DOM_MAX visible lines (windowed).
    // Searching / not following: full (filtered) rerender of the buffer window.
    if (this.followCb.checked && !this.re && !this.stream && !this.onlyCb.checked) {
      if (this.box.querySelector('.empty')) this.box.replaceChildren();
      const frag = document.createDocumentFragment();
      for (const l of fresh) frag.append(this.lineEl(l));
      this.box.append(frag);
      while (this.box.childElementCount > DOM_MAX) this.box.firstElementChild.remove();
      this.updateCount();
      this.scrollEnd();
      return;
    }
    this.rerender();
  }
  visibleSlice() {
    const vis = [];
    for (const l of this.lines) if (this.visible(l)) vis.push(l);
    // Keep the DOM bounded: show the last DOM_MAX matching lines (or a window around the current mark).
    if (vis.length <= DOM_MAX) return vis;
    if (this.cur >= 0 && this.marks.length) {
      // keep context around the current search hit if possible — fall back to tail
      return vis.slice(-DOM_MAX);
    }
    return vis.slice(-DOM_MAX);
  }
  rerender() {
    this.marks = []; this.cur = -1;
    const frag = document.createDocumentFragment();
    const slice = this.visibleSlice();
    for (const l of slice) frag.append(this.lineEl(l));
    if (!frag.childNodes.length) frag.append(h('div', { class: 'ln empty' }, this.lines.length ? t('logs.noMatch') : t('logs.empty')));
    this.box.replaceChildren(frag);
    this.updateCount();
    if (this.followCb.checked) this.scrollEnd();
  }
  updateCount() {
    this.total.textContent = t('logs.lines', { n: this.lines.length });
    this.count.textContent = this.re ? t('logs.matches', { n: this.marks.length }) : '';
  }
  jump(dir) {
    if (!this.marks.length) return;
    this.marks[this.cur]?.classList.remove('cur');
    this.cur = (this.cur + dir + this.marks.length) % this.marks.length;
    const m = this.marks[this.cur]; m.classList.add('cur');
    this.followCb.checked = false;
    m.scrollIntoView({ block: 'center' });
    this.count.textContent = `${this.cur + 1} / ${this.marks.length}`;
  }
  scrollEnd() { this.autoScrolling = true; this.box.scrollTop = this.box.scrollHeight; }

  async download() {
    // Prefer the server-side download endpoint (M3, supports ?q=); fall back to the
    // node-side tail (≤5000 lines) and finally to what is buffered in this view.
    const q = this.query && this.onlyCb.checked ? this.query : '';
    const fname = `${this.name.replace(/[^\w.-]+/g, '_')}-${new Date().toISOString().slice(0, 19).replace(/[:T-]/g, '')}.log`;
    let blob = null;
    try {
      const r = await apiFetch(`/agents/${encodeURIComponent(this.id)}/logs/download` + (q ? '?q=' + encodeURIComponent(q) : ''));
      if (r.ok) blob = await r.blob();
    } catch {}
    if (!blob) {
      let lines = this.lines;
      try {
        const r = await api('GET', `/agents/${encodeURIComponent(this.id)}/logs?tail=5000` + (q ? '&q=' + encodeURIComponent(q) : ''));
        if (r?.lines?.length) lines = r.lines;
      } catch (e) { if (!lines.length) { toast(e.message, true); return; } }
      if (q) { const re = new RegExp(q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'i'); lines = lines.filter((l) => re.test(stripAnsi(l.text))); }
      blob = new Blob([lines.map((l) => `${l.time} [${l.stream}] ${stripAnsi(l.text)}`).join('\n') + '\n'], { type: 'text/plain;charset=utf-8' });
    }
    const url = URL.createObjectURL(blob);
    const a = h('a', { href: url, download: fname });
    document.body.append(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}
