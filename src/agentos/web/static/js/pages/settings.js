// 设置 page (owned by M6): appearance, language, admin password, node token, about.
import { h, icon, toast, copyText, confirmDialog, getTheme, setTheme } from '../util.js';
import { api, MOCK } from '../api.js';
import { t, getLang, setLang, languages } from '../i18n.js';
import { page, setKeys } from '../components/ui.js';

const srow = (ic, title, desc, ctl, extra) => h('div', { class: 'srow' + (extra ? ' exp' : '') }, h('span', { class: 'si' }, icon(ic)), h('div', { class: 'st-t' }, h('b', {}, title), desc ? h('small', {}, desc) : null), h('div', { class: 'ctl' }, ctl), extra || null);
const seg = (cur, items, onPick) => {
  const box = h('div', { class: 'seg' });
  const draw = (c) => box.replaceChildren(...items.map(([v, l, ic]) => h('button', { type: 'button', class: v === c ? 'on' : '', 'data-v': v, onclick: () => { onPick(v); draw(v); } }, ic ? icon(ic) : null, l)));
  draw(cur); return box;
};

export default {
  get title() { return t('nav.settings'); },
  async render(el) {
    const body = h('div', { class: 'settings' });
    el.append(page({ title: t('nav.settings'), body }).el);
    const [tok, ver, shell] = await Promise.all([
      api('GET', '/settings/node-token').catch(() => ({ token: '' })), api('GET', '/version').catch(() => ({})),
      window.agentosShellInfo ? window.agentosShellInfo().catch(() => null) : Promise.resolve(null)]);
    let token = tok.token || '', shown = false;
    const tokEl = h('code', { class: 'mono selectable' });
    const showBtn = h('button', { class: 'btn sm', onclick: () => { shown = !shown; sync(); } });
    const sync = () => { tokEl.textContent = token ? (shown ? token : token.slice(0, 4) + '•'.repeat(12)) : '—'; showBtn.replaceChildren(icon('eye'), shown ? t('settings.hide') : t('settings.show')); };
    sync();
    const oldPw = h('input', { type: 'password', autocomplete: 'current-password', 'aria-label': t('settings.oldPw'), placeholder: t('settings.oldPw') });
    const newPw = h('input', { type: 'password', autocomplete: 'new-password', 'aria-label': t('settings.newPw'), placeholder: t('settings.newPw') });
    const newPw2 = h('input', { type: 'password', autocomplete: 'new-password', 'aria-label': t('settings.newPw2'), placeholder: t('settings.newPw2') });
    const pwErr = h('div', { class: 'err-msg', style: 'display:block' });
    const pwForm = h('form', { class: 'pw-form', onsubmit: async (e) => {
      e.preventDefault(); pwErr.textContent = '';
      if (newPw.value.length < 6) { pwErr.textContent = t('settings.pwShort'); newPw.focus(); return; }
      if (newPw.value !== newPw2.value) { pwErr.textContent = t('settings.pwMismatch'); newPw2.focus(); return; }
      try { await api('POST', '/auth/password', { old_password: oldPw.value, new_password: newPw.value }); toast(t('settings.pwChanged')); oldPw.value = newPw.value = newPw2.value = ''; }
      catch (err) { pwErr.textContent = err.message; }
    } }, oldPw, newPw, newPw2, h('button', { class: 'btn accent' }, t('settings.changePw')), pwErr);
    const kv = (k, v) => [h('div', {}, k), h('div', { class: 'selectable' }, v ?? '—')];
    body.append(
      h('div', { class: 'section-t' }, t('settings.appearance')),
      srow('palette', t('settings.theme'), t('settings.themeD'), seg(getTheme(), [['light', t('theme.light'), 'sun'], ['dark', t('theme.dark'), 'moon'], ['system', t('theme.system'), 'monitor']], setTheme)),
      srow('globe', t('lang.label'), t('settings.langD'), h('select', { 'aria-label': t('lang.label'), onchange: (e) => setLang(e.target.value) }, languages.map(([v, l]) => h('option', { value: v, selected: v === getLang() }, l)))),
      h('div', { class: 'section-t' }, t('settings.security')),
      srow('user', t('settings.password'), t('settings.passwordD'), null, pwForm),
      srow('key', t('settings.token'), t('settings.tokenD'), h('div', { class: 'row' }, tokEl, showBtn,
        h('button', { class: 'btn sm', onclick: () => copyText(token) }, icon('copy'), t('common.copy')),
        h('button', { class: 'btn sm danger', onclick: async () => {
          if (!(await confirmDialog({ title: t('settings.rotateTitle'), message: t('settings.rotateMsg'), danger: true, okText: t('settings.rotate') }))) return;
          try { token = (await api('POST', '/settings/node-token/rotate')).token; sync(); toast(t('settings.rotated')); } catch (e) { toast(e.message, true); }
        } }, icon('restart'), t('settings.rotate')))),
      h('div', { class: 'section-t' }, t('settings.about')),
      h('div', { class: 'card about' }, h('div', { class: 'about-h' }, h('img', { src: 'favicon.svg', alt: '' }), h('div', {}, h('b', {}, 'AgentOS'), h('div', { class: 'small muted' }, t('app.tagline')))),
        h('div', { class: 'kv' },
          kv(t('settings.version'), ver.version), kv(t('settings.commit'), h('span', { class: 'mono' }, ver.commit || '—')), kv(t('settings.built'), ver.date),
          kv(t('settings.protocol'), ver.protocol != null ? String(ver.protocol) : '—'),
          kv(t('settings.server'), MOCK ? t('status.mock') : location.origin),
          kv(t('settings.shell'), shell ? `AgentOS.exe ${shell.version || ''} · WebView2 ${shell.webview2 || ''}` : t('settings.shellBrowser')))));
    return setKeys({});
  },
};
