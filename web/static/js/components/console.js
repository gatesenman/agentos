// Remote desktop console (owned by M6): a VM-console style frame around the two
// desktop clients (pages/desktop.js VncClient, desktop/screen-client.js ScreenClient).
//   mountConsole(container, env, {popout:boolean}) -> {destroy()}
// Floating auto-hiding toolbar (status, VNC/内置屏幕流 switch, fit/1:1, quality, Ctrl+Alt+Del,
// keys, clipboard, view-only, fullscreen, pop-out window, reconnect/disconnect), connection
// overlay and a bottom info strip. The clients keep their contract (container.dataset.state).
import { h, icon, toast } from '../util.js';
import { t } from '../i18n.js';
import { hasCap } from '../store.js';
import { logo } from './logos.js';

const enc = encodeURIComponent;
const QUAL = { high: { vnc: 'high', screen: 85 }, mid: { vnc: 'mid', screen: 60 }, low: { vnc: 'low', screen: 35 } };
const KEYS = [
  ['ctrl-alt-del', 'Ctrl+Alt+Del', ['ControlLeft', 'AltLeft', 'Delete']], ['win', 'Win', ['MetaLeft']], ['alt-tab', 'Alt+Tab', ['AltLeft', 'Tab']],
  ['ctrl-esc', 'Ctrl+Esc', ['ControlLeft', 'Escape']], ['alt-f4', 'Alt+F4', ['AltLeft', 'F4']], ['print', 'PrtSc', ['PrintScreen']],
];

export async function mountConsole(container, env, { popout = false, wide = null, onWide = null } = {}) {
  const mod = await import('../pages/desktop.js');
  const kinds = [['vnc', 'desktop.vnc', 'VNC'], ['screen', 'desktop.screen', t('desk.screen')]].filter(([, c]) => hasCap(env, c));
  const pref = JSON.parse(localStorage.getItem('agentos.console') || '{}');
  const key = 'agentos.desktop.kind.env.' + env.id;
  let kind = localStorage.getItem(key); if (!kinds.some(([k]) => k === kind)) kind = kinds[0]?.[0];
  const lowMem = () => {
    const dm = navigator.deviceMemory; // GiB; Chromium/WebView2 only
    return dm != null && dm <= 4;
  };
  let scale = pref.scale || 'fit', quality = pref.quality || (lowMem() ? 'low' : 'mid'), viewOnly = !!pref.viewOnly;
  let pinState = !!pref.pinned;
  let client = null, state = 'idle', msg = '', info = {}, destroyed = false, everConnected = false;
  // Safer screen-stream defaults: full-res @ 8fps JPEG + WebView2 OOM'd nested QEMU (~4 GiB).
  const screenStreamOpts = () => {
    const o = { toolbar: false, overlay: false, fit: scale === 'fit', viewOnly, quality: QUAL[quality].screen, autoReconnect: false };
    if (lowMem()) { o.fps = 4; o.scale = 0.5; o.quality = Math.min(o.quality, 40); }
    else { o.fps = 6; o.scale = 0.75; }
    return o;
  };
  const save = () => localStorage.setItem('agentos.console', JSON.stringify({ scale, quality, viewOnly, pinned: pinState }));
  const base = env.node_id ? `/nodes/${enc(env.node_id)}/desktop` : `/envs/${enc(env.id)}/desktop`;

  // ---------- DOM ----------
  const host = h('div', { class: 'desktop-host', 'data-testid': 'desktop-host' });
  const stage = h('div', { class: 'stage' }, host);
  const cst = h('span', { class: 'cst' }, h('i'), h('span', {}, ''));
  const seg = h('div', { class: 'seg', role: 'tablist', 'aria-label': t('desk.mode') });
  const tb = (ic, title, onClick, extra = {}) => h('button', { type: 'button', title, 'aria-label': title, onclick: onClick, ...extra }, icon(ic));
  const bFit = tb('fit', t('con.fit'), () => setScale('fit'), { 'data-act': 'fit' });
  const bReal = tb('actual', t('con.actual'), () => setScale('real'), { 'data-act': 'actual' });
  const qSel = h('select', { title: t('con.quality'), 'aria-label': t('con.quality'), onchange: () => { quality = qSel.value; save(); applyQuality(); } },
    ['high', 'mid', 'low'].map((q) => h('option', { value: q, selected: q === quality }, t('con.q.' + q))));
  const bCad = h('button', { type: 'button', title: t('con.cad'), 'data-act': 'cad', onclick: () => sendKeys('ctrl-alt-del') }, icon('keyboard'), h('span', { class: 'lbl' }, 'Ctrl+Alt+Del'));
  const bKeys = tb('more', t('con.keys'), () => toggleFly(keysFly), { 'data-act': 'keys' });
  const bClip = tb('clipboard', t('con.clipboard'), () => toggleFly(clipFly), { 'data-act': 'clipboard' });
  const bView = tb(viewOnly ? 'eyeoff' : 'eye', t('con.viewOnly'), () => setViewOnly(!viewOnly), { 'data-act': 'viewonly' });
  const bFull = tb('fullscreen', t('con.fullscreen'), () => toggleFull(), { 'data-act': 'fullscreen' });
  const bPin = tb('pin', t('con.pinToolbar'), () => {
    pinState = root.classList.toggle('pinned');
    save();
    bPin.classList.toggle('on', pinState); if (pinState) root.classList.remove('idle'); else wake();
  }, { 'data-act': 'pin', class: pinState ? 'on' : '' });
  const bWide = onWide ? tb(wide?.() ? 'fullexit' : 'fit', t('con.wide'), () => { const v = !wide?.(); onWide(v); bWide.classList.toggle('on', v); bWide.replaceChildren(icon(v ? 'left' : 'right')); }, { 'data-act': 'wide' }) : null;
  const bPop = popout ? null : tb('popout', t('con.popout'), () => openPopout(env), { 'data-act': 'popout' });
  const bConn = tb('power', t('con.disconnect'), () => (live() ? disconnect() : connect()), { 'data-act': 'connect' });
  const bar = h('div', { class: 'ctb', role: 'toolbar', 'aria-label': t('tab.desktop') }, cst,
    kinds.length > 1 ? [h('span', { class: 'sep' }), seg] : null, h('span', { class: 'sep' }),
    bFit, bReal, qSel, h('span', { class: 'sep' }), bCad, bKeys, bClip, bView, h('span', { class: 'sep' }), bPin, bWide, bFull, bPop, bConn);
  const clipTa = h('textarea', { class: 'mono', placeholder: t('con.clipPh') });
  const clipFly = h('div', { class: 'cfly', hidden: true, 'data-fly': 'clipboard' }, h('h4', {}, t('con.clipboard')), h('p', {}, t('con.clipHint')), clipTa,
    h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn sm accent', onclick: () => sendClip() }, icon('send'), t('con.clipSend')),
      h('button', { type: 'button', class: 'btn sm', title: t('con.typeHint'), onclick: () => typeClip() }, icon('keyboard'), t('con.type')), h('span', { class: 'sp' }),
      h('button', { type: 'button', class: 'btn sm', onclick: () => toggleFly(clipFly) }, t('common.close'))));
  const keysFly = h('div', { class: 'cfly', hidden: true, 'data-fly': 'keys' }, h('h4', {}, t('con.keys')),
    h('div', { class: 'keys' }, KEYS.map(([id, label, codes]) => h('button', { type: 'button', class: 'btn sm', 'data-key': id, onclick: () => { sendKeys(id); toggleFly(keysFly); } }, label))));
  const ov = h('div', { class: 'cov', 'data-testid': 'console-overlay' });
  const iRes = h('span', {}), iKind = h('span', {}), iStat = h('span', {}), iName = h('span', {});
  const strip = h('div', { class: 'cinfo' }, iName, iKind, iRes, h('span', { class: 'sp' }), iStat);
  const hot = h('div', { class: 'ctb-hot', onmouseenter: () => wake() });
  const root = h('div', { class: 'console', 'data-kind': kind || '' }, stage, hot, bar, clipFly, keysFly, ov, strip);
  container.append(root);

  // ---------- behaviour ----------
  const live = () => ['connected', 'connecting', 'auth', 'reconnecting'].includes(state);
  let idleT = null;
  if (pinState) root.classList.add('pinned');
  function wake() {
    root.classList.remove('idle'); clearTimeout(idleT);
    if (state === 'connected' && !pinState) {
      idleT = setTimeout(() => { if (clipFly.hidden && keysFly.hidden) root.classList.add('idle'); }, 3200);
    }
  }
  root.addEventListener('mousemove', (e) => {
    const y = e.clientY - root.getBoundingClientRect().top;
    if (y < 88) wake();
  });
  // Top hot zone always reveals the toolbar so it stays reachable.
  hot.addEventListener('mousemove', () => wake());
  function toggleFly(f) { const show = f.hidden; clipFly.hidden = keysFly.hidden = true; f.hidden = !show; if (show) { wake(); f.querySelector('textarea')?.focus(); } }
  function setScale(v) { scale = v; save(); syncBar(); if (kind === 'vnc') client?.setScale?.(v); else client?.setFit?.(v === 'fit'); }
  function applyQuality() { if (!client) return; if (kind === 'vnc') client.setQuality?.(QUAL[quality].vnc); else client.setConfig?.({ quality: QUAL[quality].screen }); }
  function setViewOnly(v) { viewOnly = v; save(); syncBar(); client?.setViewOnly?.(v); toast(v ? t('con.viewOnlyOn') : t('con.viewOnlyOff')); }
  function sendKeys(id) {
    if (!client || state !== 'connected') return toast(t('con.notConnected'), true);
    if (viewOnly) return toast(t('con.viewOnlyOn'), true);
    const k = KEYS.find((x) => x[0] === id); if (!k) return;
    if (id === 'ctrl-alt-del') client.sendCtrlAltDel?.();
    else if (kind === 'vnc') client.sendCombo?.(id);
    else client.sendKeys?.(k[2]);
    if (kind !== 'vnc' || id !== 'ctrl-alt-del') toast(t('con.sent', { k: k[1] }));
    client.focus?.();
  }
  function sendClip() {
    const v = clipTa.value; if (!v) return;
    if (kind === 'vnc') client?.sendClipboard?.(v); else { client?.sendText?.(v); toast(t('con.typed')); }
  }
  function typeClip() { const v = clipTa.value; if (!v) return; if (kind === 'vnc') client?.typeText?.(v); else client?.sendText?.(v); }
  function toggleFull() { if (document.fullscreenElement) document.exitFullscreen(); else root.requestFullscreen?.().catch((e) => toast(e.message, true)); }
  const onFs = () => { bFull.replaceChildren(icon(document.fullscreenElement ? 'fullexit' : 'fullscreen')); bFull.title = t(document.fullscreenElement ? 'con.exitFullscreen' : 'con.fullscreen'); };
  document.addEventListener('fullscreenchange', onFs);

  if (bWide) { const v = !!wide?.(); bWide.classList.toggle('on', v); bWide.replaceChildren(icon(v ? 'left' : 'right')); }
  function syncBar() {
    bFit.classList.toggle('on', scale === 'fit'); bReal.classList.toggle('on', scale !== 'fit');
    bView.classList.toggle('on', viewOnly); bView.replaceChildren(icon(viewOnly ? 'eyeoff' : 'eye'));
    seg.replaceChildren(...kinds.map(([k, , l]) => h('button', { type: 'button', role: 'tab', class: k === kind ? 'on' : '', 'data-kind': k, onclick: () => { if (k !== kind) switchKind(k); } }, l)));
    const ok = state === 'connected';
    for (const b of [bCad, bKeys, bClip]) b.disabled = !ok || viewOnly;
    bConn.classList.toggle('danger', live()); bConn.title = t(live() ? 'con.disconnect' : 'con.connect'); bConn.setAttribute('aria-label', bConn.title);
    bConn.replaceChildren(icon(live() ? 'power' : 'refresh'));
    const cls = { connected: 'ok', connecting: 'warn', auth: 'warn', reconnecting: 'warn', error: 'err' }[state] || '';
    cst.className = 'cst ' + cls; cst.lastChild.textContent = t('con.st.' + (state === 'idle' ? 'disconnected' : state));
    iKind.replaceChildren(icon('screen'), kind === 'vnc' ? 'VNC' : t('desk.screen'));
    iName.replaceChildren(icon('env'), env.name + (info.name ? ' · ' + info.name : ''));
    iRes.replaceChildren(...(info.width ? [icon('fit'), `${info.width} × ${info.height}`] : []));
  }
  const isWin = /Win/i.test(navigator.userAgent) || navigator.platform === 'Win32';
  let autoSwitched = false;
  function failHint() {
    if (kind === 'vnc') return t('con.vncFailedHint');
    return t('con.failedHint');
  }
  function overlay() {
    if (state === 'connected' || state === 'auth') { ov.hidden = true; return; }
    ov.hidden = false;
    const L = logo(env, 56);
    if (state === 'connecting' || state === 'reconnecting') {
      ov.replaceChildren(h('div', { class: 'box' }, L, h('h3', {}, t(state === 'reconnecting' ? 'con.reconnecting' : 'con.connecting', { name: env.name })),
        h('p', {}, msg || (kind === 'vnc' ? t('con.viaVnc') : t('con.viaScreen'))), h('div', { class: 'dots' }, h('i'), h('i'), h('i'))));
      return;
    }
    // Idle / never connected: do NOT auto-connect (nested QEMU + WebView2 + stream can OOM).
    if (state === 'idle' || (state === 'disconnected' && !everConnected && !msg)) {
      ov.replaceChildren(h('div', { class: 'box' }, L, h('h3', {}, t('con.ready', { name: env.name })),
        h('p', {}, t('con.readyHint')),
        lowMem() ? h('p', { style: 'color:var(--warn,#b45309);font-size:13px' }, t('con.lowRamWarn')) : null,
        h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn accent', 'data-act': 'connect-now', onclick: () => connect() }, icon('refresh'), t('con.connect')))));
      return;
    }
    const err = state === 'error';
    const other = kinds.find(([k]) => k !== kind);
    ov.replaceChildren(h('div', { class: 'box' }, L, h('h3', {}, t(err ? 'con.failed' : 'con.ended')), h('p', {}, msg || (err ? failHint() : t('con.endedHint'))),
      h('div', { class: 'row' }, h('button', { type: 'button', class: 'btn accent', 'data-act': 'reconnect', onclick: () => connect() }, icon('refresh'), t('con.reconnect')),
        other ? h('button', { type: 'button', class: 'btn', 'data-act': 'use-other', onclick: () => switchKind(other[0]) }, icon('screen'), t('con.useOther', { k: other[2] })) : null));
  }
  let vncFails = 0;
  function onState(s, m) {
    if (destroyed) return;
    state = s; msg = m || '';
    if (s === 'connected') { everConnected = true; vncFails = 0; autoSwitched = false; wake(); setTimeout(() => client?.focus?.(), 50); }
    else root.classList.remove('idle');
    // VNC → built-in screen stream fallback (esp. Windows host desktop where VNC is often absent).
    // Only after an explicit connect attempt — never auto-connect on tab open.
    if (kind === 'vnc' && (s === 'error' || s === 'disconnected') && kinds.some(([k]) => k === 'screen') && everConnected === false && client) {
      vncFails++;
      if ((isWin && !autoSwitched) || vncFails >= 2) {
        autoSwitched = true;
        if (isWin) toast(t('con.vncFailedWin'));
        switchKind('screen', true);
        return;
      }
    }
    syncBar(); overlay();
  }
  function switchKind(k, forceConnect = false) {
    if (!k || k === kind) { if (forceConnect) connect(); return; }
    kind = k; localStorage.setItem(key, k); root.dataset.kind = k; info = {};
    if (forceConnect || everConnected || live() || state === 'error' || state === 'connecting' || state === 'reconnecting') connect();
    else { try { client?.disconnect(); } catch {} client = null; syncBar(); onState('idle', ''); }
  }
  function disconnect() { const c = client; client = null; try { c?.disconnect(true); } catch {} host.dataset.state = 'disconnected'; onState('disconnected', t('con.byUser')); }
  function connect() {
    if (destroyed) return;
    try { client?.disconnect(); } catch {}
    client = null; host.replaceChildren(); info = {};
    const spec = mod.DESKTOP_CLIENTS?.[kind];
    if (!spec) { onState('error', t('desk.unavailable')); return; }
    const path = `${base}?kind=${kind}`;
    if (kind === 'vnc') {
      client = new spec.Client(host, path, { bare: true, onState: (s, m) => onState(s, m), onInfo: (x) => { Object.assign(info, x); syncBar(); }, onClipboard: (txt) => { clipTa.value = txt; } });
      client.setScale(scale); client.setQuality(QUAL[quality].vnc); client.setViewOnly(viewOnly);
    } else {
      client = new spec.Client(host, path, screenStreamOpts());
      client.on('status', ({ state: s, message }) => onState(s, message));
      client.on('hello', (x) => { info.width = x.width; info.height = x.height; syncBar(); });
      client.on('stats', (st) => { iStat.replaceChildren(icon('pulse'), `${st.fps} fps · ${st.kbps} kbps` + (st.latency != null ? ` · ${st.latency} ms` : '')); });
    }
    onState('connecting', '');
    client.connect();
  }
  function onVis() {
    if (destroyed) return;
    if (document.hidden && live()) {
      disconnect();
      toast(t('con.pausedHidden'));
    }
  }
  document.addEventListener('visibilitychange', onVis);
  if (!kinds.length) { onState('error', t('desk.unavailable')); }
  else { syncBar(); onState('idle', ''); } // manual connect — avoids nested-QEMU OOM on tab open
  return {
    destroy() { destroyed = true; clearTimeout(idleT); document.removeEventListener('fullscreenchange', onFs); document.removeEventListener('visibilitychange', onVis); try { client?.disconnect(); } catch {} client = null; if (document.fullscreenElement === root) document.exitFullscreen().catch(() => {}); },
  };
}

/