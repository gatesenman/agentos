// Desktop application shell + hash router (owned by M6).
// Frame: title bar (drag region, window buttons wired to the AgentOS.exe
// WebView2 bindings), navigation rail, content, status bar.
// Pages are ES modules exporting default { title, render(el, params) => cleanup?, update?(params) }.
// params = route params + `query` (URLSearchParams after '?' in the hash).
import { h, icon, applyTheme, initNativeBackdrop, closeMenu, contextMenu, copyText } from './util.js';
import { api, apiFetch, tryLocalAuth, closeEvents, reopenEvents, initMock, MOCK, APP_MODE } from './api.js';
import { t } from './i18n.js';
import { themeButton, langButton } from './components/controls.js';
import { logo } from './components/logos.js';
import { store, subscribe, load, wire } from './store.js';
import { getKeys } from './components/ui.js';

applyTheme(); initNativeBackdrop();

const routes = [
  ['/login', () => import('./pages/login.js'), { bare: true }],
  ['/', () => import('./pages/envs.js'), { nav: 'envs' }],
  ['/envs', () => import('./pages/envs.js'), { nav: 'envs' }],
  ['/envs/:id', () => import('./pages/envs.js'), { nav: 'envs' }],
  ['/envs/:id/:tab', () => import('./pages/envs.js'), { nav: 'envs' }],
  ['/agents', () => import('./pages/agents.js'), { nav: 'agents' }],
  ['/agents/new', () => import('./pages/agents.js'), { nav: 'agents', mode: 'new' }],
  ['/agents/:id', () => import('./pages/agents.js'), { nav: 'agents' }],
  ['/agents/:id/:tab', () => import('./pages/agents.js'), { nav: 'agents' }],
  ['/templates', () => import('./pages/templates.js'), { nav: 'templates' }],
  ['/settings', () => import('./pages/settings.js'), { nav: 'settings' }],
  ['/nodes/:id/desktop', () => import('./pages/desktop.js'), { nav: 'envs' }],
  ['/desktop', () => import('./pages/desktop.js'), { nav: 'envs' }],
  ['/console/:id', () => import('./pages/console.js'), { chromeless: true }],
  ['/os', () => import('./pages/envs.js'), { nav: 'envs' }],
];
const notFound = () => import('./pages/notfound.js');

function match(path) {
  for (const [pattern, load, opts] of routes) {
    const pp = pattern.split('/'), xp = path.split('/');
    if (pp.length !== xp.length) continue;
    const params = {};
    if (pp.every((p, i) => p.startsWith(':') ? (params[p.slice(1)] = decodeURIComponent(xp[i]), xp[i] !== '') : p === xp[i]))
      return { load, params, opts: opts || {} };
  }
  return null;
}

let cleanup = null, curMod = null, version = '', seq = 0, frame = null, authed = false;
const shellAPI = () => typeof window.agentosWinClose === 'function';

// ---------- title bar ----------
function titlebar(bare) {
  const maxBtn = h('button', { class: 'winbtn', 'aria-label': t('win.max'), title: t('win.max'), onclick: () => window.agentosWinToggleMaximize?.().then(syncMax) }, icon('wmax'));
  const syncMax = () => window.agentosWinIsMaximized?.().then((m) => { maxBtn.replaceChildren(icon(m ? 'wrestore' : 'wmax')); maxBtn.title = t(m ? 'win.restore' : 'win.max'); document.documentElement.classList.toggle('maximized', !!m); }).catch(() => {});
  if (shellAPI()) { addEventListener('resize', () => { clearTimeout(syncMax.t); syncMax.t = setTimeout(syncMax, 120); }); syncMax(); }
  const bar = h('header', { class: 'titlebar chrome',
    onmousedown: (e) => { if (e.button === 0 && !e.target.closest('button,select,a,input,.tb-search') && shellAPI() && e.detail === 1) window.agentosWinDrag(); },
    ondblclick: (e) => { if (!e.target.closest('button,select,a,input,.tb-search') && shellAPI()) window.agentosWinToggleMaximize().then(syncMax); } },
  h('div', { class: 'tb-l' }, h('img', { class: 'tb-icon', src: 'favicon.svg', alt: '' }), h('span', { class: 'ttl' }, 'AgentOS'),
    MOCK ? h('span', { class: 'badge info plain sm', title: t('status.mockHint') }, t('status.mock')) : null),
  bare ? h('span', { class: 'sp' }) : globalSearch(),
  h('div', { class: 'tb-r' }, h('div', { class: 'tb-tools' }, langButton(), themeButton()),
    shellAPI() ? h('div', { class: 'winbtns' },
      h('button', { class: 'winbtn', 'aria-label': t('win.min'), title: t('win.min'), onclick: () => window.agentosWinMinimize() }, icon('wmin')),
      maxBtn,
      h('button', { class: 'winbtn close', 'aria-label': t('win.close'), title: t('win.close'), onclick: () => window.agentosWinClose() }, icon('wclose'))) : null));
  return bar;
}

// ---------- global search (title bar, Ctrl+K) ----------
function globalSearch() {
  const inp = h('input', { type: 'search', placeholder: t('gs.placeholder'), 'aria-label': t('gs.placeholder'), autocomplete: 'off', spellcheck: 'false' });
  const box = h('div', { class: 'tb-search', role: 'combobox', 'aria-expanded': 'false' }, icon('search'), inp, h('kbd', {}, 'Ctrl+K'));
  let fly = null, items = [], cur = 0;
  const close = () => { fly?.remove(); fly = null; box.setAttribute('aria-expanded', 'false'); };
  const go = (it) => { close(); inp.value = ''; inp.blur(); it.run(); };
  const draw = () => {
    const q = inp.value.trim().toLowerCase();
    const pages = [
      { ic: 'env', text: t('nav.envs'), sub: t('gs.page'), run: () => { location.hash = '#/envs'; } },
      { ic: 'bot', text: t('nav.agents'), sub: t('gs.page'), run: () => { location.hash = '#/agents'; } },
      { ic: 'template', text: t('nav.templates'), sub: t('gs.page'), run: () => { location.hash = '#/templates'; } },
      { ic: 'settings', text: t('nav.settings'), sub: t('gs.page'), run: () => { location.hash = '#/settings'; } },
      { ic: 'plus', text: t('envs.new'), sub: t('gs.action'), run: () => { location.hash = '#/envs?new=1'; } },
      { ic: 'plus', text: t('agents.new'), sub: t('gs.action'), run: () => { location.hash = '#/agents/new'; } },
    ];
    const envs = store.envs.map((e) => ({ logo: e, text: e.name, sub: `${t('gs.env')} · ${e.provider_info?.name || e.provider} · ${t('envstate.' + e.state)}`, run: () => { location.hash = '#/envs/' + encodeURIComponent(e.id); } }));
    const ags = store.agents.map((a) => ({ ic: 'bot', text: a.name, sub: `${t('gs.agent')} · ${t('state.' + (a.status?.state || 'stopped'))}`, run: () => { location.hash = '#/agents/' + encodeURIComponent(a.id); } }));
    const m = (x) => !q || (x.text + ' ' + x.sub).toLowerCase().includes(q);
    const groups = [[t('gs.envs'), envs.filter(m).slice(0, 6)], [t('gs.agents'), ags.filter(m).slice(0, 6)], [t('gs.go'), pages.filter(m)]].filter(([, l]) => l.length);
    items = groups.flatMap(([, l]) => l); cur = Math.min(cur, Math.max(0, items.length - 1));
    if (!fly) { fly = h('div', { class: 'flyout gs-fly', role: 'listbox' }); document.body.append(fly); box.setAttribute('aria-expanded', 'true'); }
    const r = box.getBoundingClientRect(); fly.style.left = r.left + 'px'; fly.style.top = r.bottom + 6 + 'px'; fly.style.width = r.width + 'px';
    let i = 0;
    fly.replaceChildren(...(items.length ? groups.flatMap(([g, l]) => [h('div', { class: 'gs-g' }, g), ...l.map((x) => { const k = i++; return h('button', { type: 'button', role: 'option', class: 'gs-i' + (k === cur ? ' on' : ''), onmousedown: (e) => { e.preventDefault(); go(x); }, onmousemove: () => { if (cur !== k) { cur = k; draw(); } } },
      x.logo ? logo(x.logo, 20) : icon(x.ic), h('span', { class: 'tx' }, h('b', {}, x.text), h('small', {}, x.sub))); })]) : [h('div', { class: 'gs-empty' }, t('gs.none'))]));
  };
  inp.addEventListener('focus', draw); inp.addEventListener('input', () => { cur = 0; draw(); });
  inp.addEventListener('blur', () => setTimeout(close, 120));
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (items.length) { cur = (cur + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length; draw(); } }
    else if (e.key === 'Enter') { e.preventDefault(); if (items[cur]) go(items[cur]); }
    else if (e.key === 'Escape') { inp.value = ''; close(); inp.blur(); }
  });
  addEventListener('keydown', (e) => { if ((e.ctrlKey || e.metaKey) && (e.key === 'k' || e.key === 'K') && !document.querySelector('.modal-bg')) { e.preventDefault(); inp.focus(); inp.select(); } });
  return box;
}

// ---------- status bar ----------
function statusbar() {
  const conn = h('span', { class: 'si' }), addr = h('span', { class: 'si' }), envs = h('span', { class: 'si' }), ags = h('span', { class: 'si' }), ver = h('span', { class: 'si' });
  const el = h('footer', { class: 'statusbar chrome', role: 'status' }, conn, addr, envs, ags, h('span', { class: 'sp' }), ver);
  const draw = () => {
    const on = MOCK || store.connected;
    conn.replaceChildren(h('span', { class: 'st ' + (on ? 'ok' : 'err') }, h('i'), on ? t('status.connected') : t('status.disconnected')));
    addr.replaceChildren(icon(MOCK ? 'sparkle' : 'server'), MOCK ? t('status.mock') : location.host);
    const online = store.envs.filter((e) => e.state === 'running').length;
    envs.replaceChildren(icon('env'), t('status.envs', { on: online, n: store.envs.length }));
    ags.replaceChildren(icon('bot'), t('status.agents', { n: store.agents.filter((a) => a.status?.state === 'running').length }));
    ver.replaceChildren(icon('info'), version ? 'AgentOS v' + version : 'AgentOS');
    ver.title = store.legacy ? t('status.legacy') : '';
  };
  subscribe(draw); draw();
  return el;
}

// ---------- navigation pane ----------
function rail() {
  const badge = (key) => h('span', { class: 'nav-b', 'data-badge': key });
  const link = (href, key, ic, text) => h('a', { href, class: 'nav', 'data-nav': key, title: text, 'aria-label': text }, icon(ic), h('span', { class: 'nl' }, text), badge(key));
  const compact = localStorage.getItem('agentos.rail') === 'compact';
  const pill = h('i', { class: 'nav-pill', 'aria-hidden': 'true' });
  const r = h('nav', { class: 'rail chrome' + (compact ? ' compact' : ''), 'aria-label': 'main' },
    h('button', { class: 'nav menu-btn', 'aria-label': t('nav.toggle'), title: t('nav.toggle'), onclick: () => { r.classList.toggle('compact'); localStorage.setItem('agentos.rail', r.classList.contains('compact') ? 'compact' : ''); requestAnimationFrame(placePill); setTimeout(() => dispatchEvent(new Event('resize')), 200); } }, icon('menu')),
    pill,
    link('#/envs', 'envs', 'env', t('nav.envs')),
    link('#/agents', 'agents', 'bot', t('nav.agents')),
    link('#/templates', 'templates', 'template', t('nav.templates')),
    h('span', { class: 'sp' }),
    link('#/settings', 'settings', 'settings', t('nav.settings')),
    h('a', { href: '#/login', class: 'nav', title: t('nav.logout'), 'aria-label': t('nav.logout'), onclick: async (e) => {
      e.preventDefault();
      try { await api('POST', '/auth/logout'); } catch {}
      closeEvents(); location.hash = '#/login';
    } }, icon('logout'), h('span', { class: 'nl' }, t('nav.logout'))));
  // selection pill slides between items (Fluent NavigationView indicator)
  function placePill() {
    const a = r.querySelector('a.nav.active');
    if (!a) { pill.style.opacity = '0'; return; }
    pill.style.opacity = '1'; pill.style.transform = `translateY(${a.offsetTop + a.offsetHeight / 2 - 8}px)`;
  }
  r.placePill = placePill;
  subscribe(() => {
    const run = store.agents.filter((a) => a.status?.state === 'running').length, bad = store.agents.filter((a) => ['failed', 'backoff'].includes(a.status?.state)).length;
    const b = r.querySelector('[data-badge=agents]'); b.textContent = bad ? String(bad) : run ? String(run) : ''; b.className = 'nav-b' + (bad ? ' err' : '');
    const eb = r.querySelector('[data-badge=envs]'); const ee = store.envs.filter((e) => e.state === 'error').length; eb.textContent = ee ? String(ee) : ''; eb.className = 'nav-b' + (ee ? ' err' : '');
  });
  return r;
}

function buildFrame(bare, chromeless) {
  const content = h('main', { class: 'content', id: 'page' });
  const plain = bare || chromeless;
  const app = h('div', { class: 'app' + (bare ? ' bare' : '') + (chromeless ? ' chromeless' : '') }, titlebar(plain), plain ? h('div', { class: 'frame' }, content) : h('div', { class: 'frame' }, rail(), content), plain ? h('div') : statusbar());
  app.bare = bare; app.mode = bare ? 'bare' : chromeless ? 'chromeless' : 'full';
  return app;
}

function setNav(nav) {
  frame.querySelectorAll('a.nav[data-nav]').forEach((a) => { const on = a.dataset.nav === nav; a.classList.toggle('active', on); if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
  requestAnimationFrame(() => frame.querySelector('.rail')?.placePill?.());
}

async function render() {
  const my = ++seq;
  const full = location.hash.replace(/^#/, '') || '/';
  const [path, qs] = full.split('?');
  const m = match(path.replace(/\/+$/, '') || '/') || { load: notFound, params: {}, opts: {} };
  m.params.query = new URLSearchParams(qs || '');
  if (m.opts.mode) m.params.mode = m.opts.mode;
  const mod = (await m.load()).default;
  if (my !== seq) return;
  closeMenu();
  const bare = !!m.opts.bare;
  if (bare) authed = false;
  else if (!authed) {
    // Auth gate: one /auth/me probe instead of a burst of 401s from every list endpoint.
    try { await api('GET', '/auth/me'); authed = true; } catch (e) { if (e.status === 401) return; authed = true; }
    if (my !== seq) return;
  }
  if (!bare) { wire(); reopenEvents(); if (!store.ready) load().catch(() => {}); }
  // Same page module with an update() hook: let it handle the new params in place.
  const mode = bare ? 'bare' : m.opts.chromeless ? 'chromeless' : 'full';
  if (mod === curMod && mod.update && frame && frame.mode === mode && cleanup !== undefined) {
    setNav(m.opts.nav);
    try { mod.update(m.params); return; } catch (e) { console.warn(e); }
  }
  if (cleanup) { try { cleanup(); } catch (e) { console.warn(e); } cleanup = null; }
  curMod = mod;
  const title = typeof mod.title === 'function' ? mod.title() : mod.title;
  document.title = (title ? title + ' - ' : '') + 'AgentOS';
  const app = document.getElementById('app');
  if (!frame || frame.mode !== mode || !app.contains(frame)) { frame = buildFrame(bare, !!m.opts.chromeless); app.replaceChildren(frame); }
  setNav(m.opts.nav);
  const host = document.getElementById('page');
  const el = h('div', { class: 'page-host', style: 'height:100%;min-height:0;display:flex;flex-direction:column' });
  host.replaceChildren(el);
  try {
    const c = await mod.render(el, m.params);
    if (my !== seq) { if (typeof c === 'function') c(); return; }
    cleanup = typeof c === 'function' ? c : null;
  } catch (e) {
    if (my !== seq) return;
    if (e.status !== 401) {
      console.warn(e);
      el.replaceChildren(h('div', { class: 'page-body' }, h('div', { class: 'alert err' }, icon('alert'), t('common.loadFailed', { msg: e.message }))));
    }
  }
}

// ---------- global keyboard + context menu ----------
const inField = (el) => el && (el.matches?.('input,textarea,select,[contenteditable]') || el.closest?.('.xterm'));
addEventListener('keydown', (e) => {
  if (document.querySelector('.modal-bg')) return; // dialogs own the keyboard
  const k = getKeys();
  if ((e.ctrlKey || e.metaKey) && (e.key === 'n' || e.key === 'N')) { e.preventDefault(); k.new?.(); }
  else if (e.key === 'F5' || ((e.ctrlKey || e.metaKey) && (e.key === 'r' || e.key === 'R') && !e.shiftKey)) { e.preventDefault(); if (k.refresh) k.refresh(); else load().catch(() => {}); }
  else if (e.key === 'Delete' && !inField(document.activeElement)) { if (k.delete) { e.preventDefault(); k.delete(); } }
  else if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F') && k.find) { e.preventDefault(); k.find(); }
});
addEventListener('contextmenu', (e) => {
  if (e.defaultPrevented) return;
  if (inField(e.target) && !e.target.closest('.xterm')) return; // native menu for text inputs
  e.preventDefault();
  const sel = String(getSelection() || '');
  if (sel && e.target.closest?.('.selectable,.logs,.kv,.code,.modal .mb')) contextMenu(e, [{ text: t('common.copy'), icon: 'copy', onClick: () => copyText(sel) }]);
});
addEventListener('dragover', (e) => { if (!e.target.closest?.('.dropzone')) e.preventDefault(); });
addEventListener('drop', (e) => { if (!e.target.closest?.('.dropzone')) e.preventDefault(); });

addEventListener('hashchange', render);
addEventListener('langchange', () => { frame = null; curMod = null; render(); });
function showBootSplash() {
  const app = document.getElementById('app');
  if (!app || app.querySelector('.boot-splash')) return;
  app.replaceChildren(h('div', { class: 'boot-splash', 'data-boot': '1' },
    h('img', { src: 'favicon.svg', alt: '' }),
    h('div', { class: 'boot-skel' }, h('i'), h('i'), h('i')),
    h('p', {}, 'AgentOS')));
}

async function isDesktopShell() {
  if (APP_MODE) return true;
  if (typeof window.agentosShellInfo === 'function') return true;
  return false;
}

(async () => {
  showBootSplash();
  await initMock();
  // Probe the session before the first paint so a valid cookie never flashes #/login.
  // Desktop (AgentOS.exe / ?app=1) against loopback: issue a local session — no password UI.
  if (!MOCK && !location.hash.startsWith('#/login')) {
    try {
      const r = await apiFetch('/auth/me');
      if (r.status === 401) {
        if (await isDesktopShell() && await tryLocalAuth()) {
          authed = true;
          const hsh = (location.hash.replace(/^#/, '') || '/').split('?')[0].replace(/\/+$/, '') || '/';
          if (hsh === '/' || hsh === '' || hsh === '/login') location.replace('#/envs');
        } else {
          authed = false;
          location.replace('#/login');
        }
      } else if (r.ok) {
        authed = true;
        const hsh = (location.hash.replace(/^#/, '') || '/').split('?')[0].replace(/\/+$/, '') || '/';
        if (hsh === '/' || hsh === '') location.replace('#/envs');
      }
    } catch {}
  } else if (!MOCK && location.hash.startsWith('#/login') && await isDesktopShell()) {
    // Landed on login in desktop shell: auto local session, never show the form.
    if (await tryLocalAuth()) {
      authed = true;
      location.replace('#/envs');
    }
  }
  try { version = (await api('GET', '/version')).version; } catch {}
  render();
})();

