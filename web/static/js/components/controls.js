// Theme toggle + language select controls (owned by M6).
import { h, icon, setTheme, effectiveTheme, contextMenu } from '../util.js';
import { t, getLang, setLang, languages } from '../i18n.js';

export function themeButton() {
  const b = h('button', { class: 'tb-btn', 'data-act': 'theme', title: t('theme.toggle'), 'aria-label': t('theme.toggle'),
    onclick: () => { setTheme(effectiveTheme() === 'dark' ? 'light' : 'dark'); b.replaceChildren(icon(effectiveTheme() === 'dark' ? 'sun' : 'moon')); } },
  icon(effectiveTheme() === 'dark' ? 'sun' : 'moon'));
  const sync = () => { if (!b.isConnected) return window.removeEventListener('themechange', sync); b.replaceChildren(icon(effectiveTheme() === 'dark' ? 'sun' : 'moon')); };
  window.addEventListener('themechange', sync);
  return b;
}
export function langSelect() {
  return h('select', { class: 'inline', style: 'padding:4px 8px;font-size:12px', 'aria-label': t('lang.label'), onchange: (e) => setLang(e.target.value) },
    languages.map(([v, l]) => h('option', { value: v, selected: v === getLang() }, l)));
}


// Title-bar language button: globe + short code, opens a flyout menu.
export function langButton() {
  const cur = languages.find(([v]) => v === getLang());
  const b = h('button', { class: 'tb-btn lang', 'data-act': 'lang', title: t('lang.label'), 'aria-label': t('lang.label'),
    onclick: () => { const r = b.getBoundingClientRect(); contextMenu({ clientX: r.left, clientY: r.bottom + 4 }, languages.map(([v, l]) => ({ text: l, icon: v === getLang() ? 'check' : 'none', onClick: () => setLang(v) }))); } },
  icon('globe'), h('span', {}, getLang() === 'en' ? 'EN' : '中'));
  b.title = cur ? cur[1] : '';
  return b;
}
