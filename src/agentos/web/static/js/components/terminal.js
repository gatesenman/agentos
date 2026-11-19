// xterm.js terminal bound to WS /envs/{id}/exec (owned by M6).
import { Terminal } from '../../vendor/xterm/xterm.mjs';
import { FitAddon } from '../../vendor/xterm/addon-fit.mjs';
import { h, icon, copyText, effectiveTheme } from '../util.js';
import { openWS } from '../api.js';
import { t } from '../i18n.js';

let cssLoaded = false;
function loadCSS() {
  if (cssLoaded) return; cssLoaded = true;
  document.head.append(h('link', { rel: 'stylesheet', href: 'vendor/xterm/xterm.css' }));
}
const THEMES = {
  dark: { background: '#0c0c0c', foreground: '#e6e6e6', cursor: '#60cdff', selectionBackground: '#264f78', black: '#0c0c0c', brightBlack: '#767676' },
  light: null,
};

// mountTerminal(container, envId, {cmd, cwd}) -> {destroy(), focus(), term}
export function mountTerminal(container, envId, { cmd, cwd } = {}) {
  loadCSS();
  let fontSize = +(localStorage.getItem('agentos.term.font') || 13);
  const term = new Terminal({
    fontFamily: '"Cascadia Mono", "Cascadia Code", Consolas, "Sarasa Mono SC", "Microsoft YaHei UI", monospace',
    fontSize, cursorBlink: true, scrollback: 5000, allowTransparency: false, theme: THEMES.dark,
  });
  const fit = new FitAddon(); term.loadAddon(fit);
  const status = h('span', { class: 'st' }, h('i'), '…');
  const holder = h('div', { class: 'term' });
  const bar = h('div', { class: 'toolbar term-bar' },
    status, h('span', { class: 'sp' }),
    h('button', { class: 'btn sm subtle', title: t('term.copy'), onclick: () => { const s = term.getSelection(); if (s) copyText(s); term.focus(); } }, icon('copy'), t('common.copy')),
    h('button', { class: 'btn sm subtle', title: t('term.paste'), onclick: async () => { try { const s = await navigator.clipboard.readText(); if (s) send(s); } catch {} term.focus(); } }, icon('file'), t('term.paste')),
    h('button', { class: 'btn sm subtle icon-only', title: 'A-', 'aria-label': 'A-', onclick: () => zoom(-1) }, 'A-'),
    h('button', { class: 'btn sm subtle icon-only', title: 'A+', 'aria-label': 'A+', onclick: () => zoom(1) }, 'A+'),
    h('button', { class: 'btn sm subtle', onclick: () => { term.clear(); term.focus(); } }, icon('x'), t('logs.clear')),
    h('button', { class: 'btn sm', 'data-act': 'reconnect', onclick: () => connect(true) }, icon('restart'), t('term.reconnect')));
  const wrap = h('div', { class: 'term-wrap' }, bar, holder);
  container.append(wrap);
  term.open(holder);
  const enc = new TextEncoder();
  let ws = null, alive = false, exited = false, destroyed = false;
  const setStatus = (cls, text) => { status.className = 'st ' + cls; status.lastChild.textContent = text; };
  const send = (s) => { if (ws && ws.readyState === 1) ws.send(enc.encode(s)); };
  const zoom = (d) => { fontSize = Math.max(9, Math.min(24, fontSize + d)); term.options.fontSize = fontSize; localStorage.setItem('agentos.term.font', fontSize); refit(); };
  const refit = () => { if (holder.clientWidth > 20 && holder.clientHeight > 20) { try { fit.fit(); } catch {} } };

  function connect(reset) {
    if (destroyed) return;
    if (ws) { const o = ws; ws = null; try { o.close(); } catch {} }
    if (reset) term.reset();
    refit(); exited = false;
    const q = new URLSearchParams({ tty: '1', cols: String(term.cols), rows: String(term.rows) });
    if (cmd) q.set('cmd', JSON.stringify(cmd));
    if (cwd) q.set('cwd', cwd);
    setStatus('info spin', t('term.connecting'));
    const s = ws = openWS(`/envs/${encodeURIComponent(envId)}/exec?${q}`);
    s.onopen = () => { if (ws !== s) return; alive = true; setStatus('ok', t('term.connected')); term.focus(); };
    s.onmessage = (m) => {
      if (ws !== s) return;
      if (typeof m.data === 'string') {
        let x; try { x = JSON.parse(m.data); } catch { term.write(m.data); return; }
        if (x.type === 'exit') { exited = true; term.write(`\r\n\x1b[90m${t('term.exited', { code: x.code ?? '?' })}\x1b[0m\r\n`); setStatus('', t('term.exitedShort', { code: x.code ?? '?' })); }
        else if (x.type === 'error') { term.write(`\r\n\x1b[31m${x.message || 'error'}\x1b[0m\r\n`); setStatus('err', x.message || 'error'); }
        return;
      }
      term.write(new Uint8Array(m.data));
    };
    s.onerror = () => {};
    s.onclose = () => {
      if (ws !== s) return; ws = null;
      if (!alive) setStatus('err', t('term.failed'));
      else if (!exited) { setStatus('warn', t('term.closed')); term.write(`\r\n\x1b[90m${t('term.closedHint')}\x1b[0m\r\n`); }
      alive = false; exited = true;
    };
  }
  term.onData((d) => { if (exited && !ws) { if (d === '\r') connect(true); return; } send(d); });
  term.onResize(({ cols, rows }) => { if (ws && ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols, rows })); });
  // Ctrl+Shift+C / Ctrl+Shift+V like Windows Terminal; Ctrl+C copies when there is a selection.
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown') return true;
    if (e.ctrlKey && (e.key === 'C' || (e.key === 'c' && term.hasSelection()))) { copyText(term.getSelection()); term.clearSelection(); e.preventDefault(); return false; }
    if (e.ctrlKey && e.shiftKey && e.key === 'V') return true; // browser paste event delivers it
    if (e.key === 'F5' || (e.ctrlKey && (e.key === 'n' || e.key === 'N'))) return false; // leave app shortcuts alone
    return true;
  });
  const ro = new ResizeObserver(() => requestAnimationFrame(refit)); ro.observe(holder);
  const onTheme = () => { term.options.theme = THEMES[effectiveTheme()] || THEMES.dark; };
  addEventListener('themechange', onTheme);
  requestAnimationFrame(() => connect(false));
  return {
    term, focus: () => term.focus(),
    destroy() { destroyed = true; ro.disconnect(); removeEventListener('themechange', onTheme); if (ws) { const o = ws; ws = null; try { o.close(); } catch {} } term.dispose(); },
  };
}
