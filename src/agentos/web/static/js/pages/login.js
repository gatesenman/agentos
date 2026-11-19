import { h, icon, toast } from '../util.js';
import { api, APP_MODE, tryLocalAuth } from '../api.js';
import { t } from '../i18n.js';

function passwordHint() {
  // Never surface raw filesystem paths (e2e/temp data dirs are hostile UX).
  const isWin = /Win/i.test(navigator.userAgent) || navigator.platform === 'Win32';
  if (APP_MODE || typeof window.agentosShellInfo === 'function') return t('login.hintDesktop');
  return isWin ? t('login.hintWin') : t('login.hint');
}

function attachReveal(tip, password) {
  if (!password || !tip?.isConnected) return;
  const row = h('div', { class: 'lf-reveal', style: 'display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-top:8px' });
  const masked = h('code', { class: 'mono', style: 'user-select:all' }, '••••••••');
  let shown = false;
  const toggle = h('button', { type: 'button', class: 'btn ghost sm' }, t('login.reveal'));
  const copy = h('button', { type: 'button', class: 'btn ghost sm' }, t('login.copy'));
  toggle.onclick = () => {
    shown = !shown;
    masked.textContent = shown ? password : '••••••••';
    toggle.textContent = shown ? t('login.hide') : t('login.reveal');
  };
  copy.onclick = async () => {
    try {
      await navigator.clipboard.writeText(password);
      toast(t('login.copied'));
    } catch {
      // fallback: select into password field
      const inp = tip.closest('form')?.querySelector('input[type=password]');
      if (inp) { inp.value = password; inp.select(); toast(t('login.copied')); }
    }
  };
  row.append(masked, toggle, copy);
  tip.append(row);
}

function isDesktopShell() {
  return APP_MODE || typeof window.agentosShellInfo === 'function';
}

export default {
  get title() { return t('login.title'); },
  render(el) {
    // AgentOS.exe / ?app=1 on loopback: skip the password form entirely.
    if (isDesktopShell()) {
      el.append(h('div', { class: 'login-bg' },
        h('div', { class: 'login' },
          h('div', { class: 'lb' },
            h('img', { src: 'favicon.svg', alt: '' }),
            h('h1', {}, 'AgentOS'),
            h('p', { class: 'muted' }, t('app.tagline')),
            h('div', { class: 'boot-skel', style: 'margin:16px auto' }, h('i'), h('i'), h('i'))))));
      (async () => {
        if (await tryLocalAuth()) { location.replace('#/envs'); return; }
        // Remote -server URL or non-loopback: fall through to password form.
        el.replaceChildren();
        renderPasswordForm(el);
      })();
      return;
    }
    renderPasswordForm(el);
  },
};

function renderPasswordForm(el) {
  const pw = h('input', { type: 'password', placeholder: t('login.password'), 'aria-label': t('login.password'), autocomplete: 'current-password' });
  const btn = h('button', { class: 'btn accent' }, t('login.submit'));
  const tip = h('div', { class: 'lf first-run', 'data-first-run': '1' }, icon('info'), h('span', {}, passwordHint()));
  if (typeof window.agentosShellInfo === 'function') {
    window.agentosShellInfo().then((i) => {
      if (!tip.isConnected) return;
      tip.querySelector('span').textContent = t('login.hintDesktop');
      if (i?.initial_password) attachReveal(tip, String(i.initial_password).trim());
    }).catch(() => {});
  }
  const submit = async (e) => {
    e.preventDefault();
    if (!pw.value) { pw.classList.add('invalid'); toast(t('login.empty'), true); pw.focus(); return; }
    btn.disabled = true;
    try { await api('POST', '/auth/login', { password: pw.value }); location.hash = '#/envs'; }
    catch (err) { toast(err.message, true); pw.select(); pw.classList.add('invalid'); }
    finally { btn.disabled = false; }
  };
  el.append(h('div', { class: 'login-bg' },
    h('form', { class: 'login', onsubmit: submit },
      h('div', { class: 'lb' }, h('img', { src: 'favicon.svg', alt: '' }), h('h1', {}, 'AgentOS'), h('p', { class: 'muted' }, t('app.tagline')), pw, btn),
      tip)));
  setTimeout(() => pw.focus(), 0);
}
