// Small DOM + formatting helpers shared by all pages (owned by M6).
// Exports used by M4/M5 files: h, toast (keep signatures stable).
import { t, locale } from './i18n.js';

export function h(tag, attrs = {}, ...children) {
  const svg = tag === 'svg' || tag === 'path';
  const el = svg ? document.createElementNS('http://www.w3.org/2000/svg', tag) : document.createElement(tag);
  let val;
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.setAttribute('class', v);
    else if (k === 'html') el.innerHTML = v;
    else if (k === 'value' && 'value' in el) val = v;
    else if (k === 'checked' || k === 'selected') el[k] = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  if (val !== undefined) el.value = val;
  return el;
}

// ---------- icons (inline SVG, stroke style) ----------
const P = {
  env: '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4M7 9l2.5 2L7 13M12 13h4"/>',
  container: '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  vm: '<rect x="2" y="4" width="20" height="14" rx="2"/><path d="M2 14h20M8 21h8"/>',
  cloud: '<path d="M7 18a5 5 0 1 1 .9-9.9A6 6 0 0 1 19 10a4 4 0 0 1-1 8z"/>',
  terminal: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M12 15h5"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  upload: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12"/>',
  template: '<rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="3" width="8" height="8" rx="1.5"/><rect x="3" y="13" width="8" height="8" rx="1.5"/><path d="M17 13v8M13 17h8"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>',
  chevron: '<path d="m6 9 6 6 6-6"/>', right: '<path d="m9 6 6 6-6 6"/>', left: '<path d="m15 6-6 6 6 6"/>',
  wmin: '<path d="M4 12h16"/>', wmax: '<rect x="4" y="4" width="16" height="16" rx="1"/>',
  wrestore: '<rect x="4" y="8" width="12" height="12" rx="1"/><path d="M8 8V5a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1h-3"/>', wclose: '<path d="M4 4l16 16M20 4 4 20"/>',
  plug: '<path d="M9 2v6M15 2v6M6 8h12v3a6 6 0 0 1-12 0zM12 17v5"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M16 7l3 3M18 5l2 2"/>',
  palette: '<path d="M12 3a9 9 0 1 0 0 18c1 0 1.5-.8 1.5-1.6 0-1.4-1-1.6-1-2.9 0-1 .8-1.5 1.8-1.5H17a4 4 0 0 0 4-4c0-4.4-4-8-9-8z"/><circle cx="7.5" cy="11" r="1"/><circle cx="10" cy="7" r="1"/><circle cx="15" cy="7.5" r="1"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  check: '<path d="m5 12 5 5 9-10"/>', wifi: '<path d="M2 9a15 15 0 0 1 20 0M5 13a10 10 0 0 1 14 0M8.5 16.5a5 5 0 0 1 7 0M12 20h.01"/>',
  install: '<path d="M12 3v12M7 10l5 5 5-5"/><rect x="3" y="17" width="18" height="4" rx="1"/>',
  layers: '<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 12 10 5 10-5M2 17l10 5 10-5"/>',
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  bot: '<rect x="4" y="8" width="16" height="12" rx="3"/><path d="M12 4v4M9 13h.01M15 13h.01M9 17h6"/><circle cx="12" cy="3.5" r="1"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  monitor: '<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>',
  play: '<path d="M7 4.5v15l12-7.5z"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="1.5"/>',
  restart: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
  edit: '<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  trash: '<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  alert: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0zM12 9v4M12 17h.01"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 16v-4M12 8h.01"/>',
  menu: '<path d="M3 6h18M3 12h18M3 18h18"/>',
  cpu: '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  up: '<path d="m18 15-6-6-6 6"/>', down: '<path d="m6 9 6 6 6-6"/>',
  pin: '<path d="M12 2v8M8 6h8"/><path d="M9 10h6l1 4H8z"/><path d="M12 14v8"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
  eyeoff: '<path d="M3 3l18 18M10.6 5.1A10 10 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-3.2 4M6.6 6.6C3.8 8.4 2 12 2 12s3.5 7 10 7c1.8 0 3.4-.5 4.8-1.3M9.9 9.9a3 3 0 0 0 4.2 4.2"/>',
  fit: '<path d="M4 9V5a1 1 0 0 1 1-1h4M15 4h4a1 1 0 0 1 1 1v4M20 15v4a1 1 0 0 1-1 1h-4M9 20H5a1 1 0 0 1-1-1v-4"/><rect x="8" y="8" width="8" height="8" rx="1"/>',
  actual: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M8 10v5M7 10h1M15 10v5M14 10h1M11.5 11h.01M11.5 14h.01"/>',
  fullscreen: '<path d="M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5"/>',
  fullexit: '<path d="M9 4v5H4M20 9h-5V4M15 20v-5h5M4 15h5v5"/>',
  keyboard: '<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M6 14h.01M18 14h.01M9 14h6"/>',
  clipboard: '<rect x="6" y="4" width="12" height="17" rx="2"/><path d="M9 4V3h6v1M9 10h6M9 14h6M9 18h3"/>',
  popout: '<path d="M14 4h6v6M20 4l-8 8M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  sliders: '<path d="M4 7h10M18 7h2M4 17h4M12 17h8"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="17" r="2"/>',
  power: '<path d="M12 3v9M6.3 6.3a8 8 0 1 0 11.4 0"/>',
  unplug: '<path d="M3 21l4-4M17 7l4-4M7 17l-2-2 4-4 6 6-4 4zM11 11l2-2M17 7l-2-2-4 4 6 6 4-4z"/>',
  screen: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4M6 13l4-4 3 3 5-5"/>',
  home: '<path d="M3 11 12 4l9 7M5 10v10h5v-6h4v6h5V10"/>',
  pulse: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  disk: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  mem: '<rect x="3" y="7" width="18" height="10" rx="1.5"/><path d="M7 17v3M11 17v3M15 17v3M7 10v4M11 10v4M15 10v4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  tag: '<path d="M3 12V4a1 1 0 0 1 1-1h8l9 9-9 9z"/><circle cx="7.5" cy="7.5" r="1.3"/>',
  shield: '<path d="M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6z"/>',
  sparkle: '<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M18 6l-2.5 2.5M8.5 15.5 6 18"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01"/>',
  chat: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>',
  send: '<path d="M21 3 10 14M21 3l-7 18-4-7-7-4z"/>',
  zap: '<path d="M13 2 4 14h7l-1 8 9-12h-7z"/>',
};
export function icon(name, cls = '') {
  const fill = name === 'play' || name === 'stop' ? 'currentColor' : 'none';
  return h('span', { class: 'icon ' + cls, 'aria-hidden': 'true',
    html: `<svg viewBox="0 0 24 24" fill="${fill}" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">${P[name] || ''}</svg>` });
}

// ---------- toasts ----------
export function toast(msg, isErr = false) {
  let box = document.getElementById('toasts');
  if (!box) { box = h('div', { id: 'toasts' }); document.body.append(box); }
  const el = h('div', { class: 'toast' + (isErr ? ' err' : ''), role: isErr ? 'alert' : 'status' }, msg);
  box.append(el);
  setTimeout(() => el.remove(), isErr ? 6000 : 3500);
}

// ---------- ContentDialog ----------
// dialog({title, sub, body, buttons:[{text, accent, danger, onClick(close)->bool|Promise}], wide, onClose})
// Returns {root, body, close(v), done: Promise}. Esc / smoke click closes with undefined.
export function dialog({ title, sub, body, buttons = [], wide, className = '', onClose, dismissable = true } = {}) {
  let resolve; const done = new Promise((r) => (resolve = r));
  const prev = document.activeElement;
  const mb = h('div', { class: 'mb' }, body);
  const foot = h('div', { class: 'mf' });
  const root = h('div', { class: 'modal-bg', onmousedown: (e) => { if (e.target === root && dismissable) close(undefined); } },
    h('div', { class: 'modal ' + (wide === true ? 'wide' : wide || '') + ' ' + className, role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      h('div', { class: 'mh' }, h('h3', {}, title), sub ? h('div', { class: 'msub' }, sub) : null), mb, foot));
  let closed = false;
  function close(v) {
    if (closed) return; closed = true;
    root.remove(); document.removeEventListener('keydown', onKey, true);
    try { onClose?.(v); } catch {}
    prev?.focus?.(); resolve(v);
  }
  const onKey = (e) => {
    if (!root.isConnected) return;
    if (e.key === 'Escape' && dismissable) { e.preventDefault(); e.stopPropagation(); close(undefined); }
  };
  const setButtons = (list) => {
    foot.replaceChildren(...list.map((b) => b === 'sp' ? h('span', { class: 'sp' }) : h('button', {
      type: 'button', class: 'btn' + (b.accent ? ' accent' : '') + (b.danger ? ' danger solid' : ''), disabled: b.disabled,
      'data-act': b.id || null,
      onclick: async (e) => { const btn = e.currentTarget; btn.disabled = true; try { const r = await (b.onClick ? b.onClick(close) : true); if (r !== false && !closed) close(b.value ?? b.text); } finally { btn.disabled = false; } },
    }, b.icon ? icon(b.icon) : null, b.text)));
  };
  setButtons(buttons);
  document.body.append(root);
  document.addEventListener('keydown', onKey, true);
  requestAnimationFrame(() => (root.querySelector('input,select,textarea') || foot.querySelector('.btn.accent') || foot.querySelector('.btn'))?.focus());
  return { root, body: mb, foot, close, done, setButtons, setTitle: (t2) => (root.querySelector('.mh h3').textContent = t2), setSub: (t2) => { const m = root.querySelector('.msub'); if (m) m.textContent = t2; } };
}

function modal({ title, message, danger = false, okText, cancelText, input, extra }) {
  const inp = input ? h('input', { value: input.value || '', placeholder: input.placeholder || '', 'aria-label': input.label || title }) : null;
  const body = h('div', {}, message ? h('div', {}, message) : null, input?.label ? h('div', { class: 'small muted', style: 'margin:10px 0 4px' }, input.label) : null, inp, extra || null);
  const d = dialog({ title, body, buttons: [
    { text: okText || t('common.ok'), accent: !danger, danger, id: 'ok', onClick: (close) => { close(inp ? inp.value : true); } },
    { text: cancelText || t('common.cancel'), id: 'cancel', onClick: (close) => { close(inp ? null : false); } }] });
  if (inp) inp.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); d.close(inp.value); } });
  if (inp) setTimeout(() => { inp.focus(); inp.select(); });
  return d.done.then((v) => (v === undefined ? (inp ? null : false) : v));
}
export const confirmDialog = (opts) => modal(opts);
export const promptDialog = (opts) => modal({ ...opts, input: { value: opts.value, label: opts.label, placeholder: opts.placeholder } });

// ---------- context menu ----------
// contextMenu(event|{x,y}, [{text, icon, onClick, disabled, danger, key}, '-'])
let openMenu = null;
export function closeMenu() { openMenu?.remove(); openMenu = null; }
export function contextMenu(ev, items) {
  ev.preventDefault?.(); ev.stopPropagation?.();
  closeMenu();
  const m = h('div', { class: 'menu', role: 'menu' }, items.filter(Boolean).map((it) => it === '-' ? h('hr') :
    h('button', { role: 'menuitem', class: it.danger ? 'danger' : '', disabled: it.disabled, onclick: () => { closeMenu(); it.onClick?.(); } },
      icon(it.icon || 'none'), it.text, it.key ? h('span', { class: 'k' }, it.key) : null)));
  document.body.append(m); openMenu = m;
  const x = ev.clientX ?? ev.x, y = ev.clientY ?? ev.y, r = m.getBoundingClientRect();
  m.style.left = Math.min(x, innerWidth - r.width - 4) + 'px';
  m.style.top = Math.min(y, innerHeight - r.height - 4) + 'px';
  m.querySelector('button:not(:disabled)')?.focus();
  m.addEventListener('keydown', (e) => {
    const bs = [...m.querySelectorAll('button:not(:disabled)')], i = bs.indexOf(document.activeElement);
    if (e.key === 'ArrowDown') { e.preventDefault(); bs[(i + 1) % bs.length]?.focus(); }
    if (e.key === 'ArrowUp') { e.preventDefault(); bs[(i - 1 + bs.length) % bs.length]?.focus(); }
    if (e.key === 'Escape') closeMenu();
  });
}
addEventListener('mousedown', (e) => { if (openMenu && !openMenu.contains(e.target)) closeMenu(); }, true);
addEventListener('blur', closeMenu);
addEventListener('resize', closeMenu);

// ---------- formatting ----------
export function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(n < 10 && i ? 1 : 0) + ' ' + u[i];
}
export function fmtPct(v) { return v == null || isNaN(v) ? '-' : (v >= 10 ? v.toFixed(0) : v.toFixed(1)) + '%'; }
const validDate = (s) => { if (!s) return null; const d = new Date(s); return isNaN(d) || d.getFullYear() < 2000 ? null : d; };
export function fmtTime(s) {
  const d = validDate(s);
  return d ? d.toLocaleString(locale(), { hour12: false }) : '-';
}
export function ago(s) {
  const d = validDate(s);
  if (!d) return '-';
  const sec = Math.max(0, Math.floor((Date.now() - d) / 1000));
  if (sec < 5) return t('time.just');
  if (sec < 60) return t('time.sec', { n: sec });
  if (sec < 3600) return t('time.min', { n: Math.floor(sec / 60) });
  if (sec < 86400) return t('time.hour', { n: Math.floor(sec / 3600) });
  return t('time.day', { n: Math.floor(sec / 86400) });
}
export function fmtDuration(sec) {
  if (sec == null || isNaN(sec) || sec < 0) return '-';
  sec = Math.floor(sec);
  const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
  const p = [];
  if (d) p.push(t('dur.d', { n: d }));
  if (hh) p.push(t('dur.h', { n: hh }));
  if (m && !d) p.push(t('dur.m', { n: m }));
  if (!d && !hh) p.push(t('dur.s', { n: s }));
  return p.join(' ');
}
export function since(s) { const d = validDate(s); return d ? (Date.now() - d) / 1000 : null; }

export function debounce(fn, ms) { let tm; return (...a) => { clearTimeout(tm); tm = setTimeout(() => fn(...a), ms); }; }

export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); }
  catch {
    const ta = h('textarea', { style: 'position:fixed;opacity:0' }); ta.value = text; document.body.append(ta); ta.select();
    try { document.execCommand('copy'); } finally { ta.remove(); }
  }
  toast(t('common.copied'));
}

// Shell-ish quoting for display only.
export function shellJoin(cmd, args = []) {
  return [cmd, ...args].filter((x) => x !== undefined && x !== null).map((a) => (a === '' ? "''" : /[\s"'$`\\|&;<>(){}*?!#~]/.test(a) ? `'${a.replace(/'/g, `'\\''`)}'` : a)).join(' ');
}

export function emptyState(emoji, title, text, action) {
  return h('div', { class: 'empty' }, h('div', { class: 'big' }, emoji), h('h3', {}, title), text ? h('p', {}, text) : null, action || null);
}

// ---------- theme ----------
export function getTheme() { try { return localStorage.getItem('agentos.theme') || 'system'; } catch { return 'system'; } }
export function effectiveTheme() {
  const m = getTheme();
  return m === 'system' ? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : m;
}
export function applyTheme() {
  const th = effectiveTheme(); document.documentElement.dataset.theme = th;
  // Desktop shell (WebView2): keep the DWM window frame in sync with the UI theme.
  if (typeof window.agentosSetTheme === 'function') window.agentosSetTheme(th === 'dark').catch(() => {});
}
// Desktop shell: only when the user opted into Mica (AGENTOS_MICA=1) and the
// native backdrop is really active do we use translucent surfaces and let it
// show through. Default: everything opaque (see css "opaque surfaces").
export function initNativeBackdrop() {
  if (typeof window.agentosShellInfo !== 'function') return;
  window.agentosShellInfo().then((i) => { if (i && i.backdrop) document.documentElement.classList.add('mica', 'native-mica'); }).catch(() => {});
}
export function setTheme(m) {
  try { localStorage.setItem('agentos.theme', m); } catch {}
  applyTheme(); window.dispatchEvent(new CustomEvent('themechange'));
}
matchMedia('(prefers-color-scheme: dark)').addEventListener?.('change', () => { if (getTheme() === 'system') { applyTheme(); window.dispatchEvent(new CustomEvent('themechange')); } });
