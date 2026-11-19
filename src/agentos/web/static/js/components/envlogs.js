// Environment log tab (owned by M6): /envs/{id}/logs + live env.log / env.updated events.
import { h, icon, debounce } from '../util.js';
import { api, on, isMissing } from '../api.js';
import { t, locale } from '../i18n.js';

export function mountEnvLogs(container, env) {
  const lines = []; let q = '', level = '', follow = true;
  const box = h('div', { class: 'logs selectable', role: 'log', tabindex: '0' });
  const search = h('input', { type: 'search', placeholder: t('logs.search'), oninput: debounce(() => { q = search.value.toLowerCase(); draw(); }, 150) });
  const lvl = h('select', { class: 'inline', style: 'padding:3px 8px;font-size:12px', onchange: () => { level = lvl.value; draw(); } },
    h('option', { value: '' }, t('logs.allLevels')), ['info', 'warn', 'error', 'event'].map((l) => h('option', { value: l }, t('logs.level.' + l))));
  const note = h('div');
  const foot = h('span');
  container.append(h('div', { class: 'logview' },
    h('div', { class: 'toolbar' }, h('div', { class: 'search', style: 'width:280px' }, icon('search'), search), lvl, h('span', { class: 'sp' }),
      h('button', { class: 'btn sm', onclick: () => download() }, icon('download'), t('logs.download')),
      h('button', { class: 'btn sm', onclick: () => load() }, icon('refresh'), t('cmd.refresh'))),
    note, box, h('div', { class: 'logfoot' }, h('span', { class: 'live on' }, t('logs.live')), foot)));
  box.addEventListener('scroll', () => { follow = box.scrollHeight - box.scrollTop - box.clientHeight < 30; });
  const show = (l) => (!level || l.level === level) && (!q || l.text.toLowerCase().includes(q));
  const lineEl = (l) => { const d = new Date(l.t); return h('div', { class: 'ln lv-' + (l.level || 'info') }, h('span', { class: 'ts', title: d.toLocaleString(locale(), { hour12: false }) }, d.toLocaleTimeString(locale(), { hour12: false })), h('span', { class: 'lvl' }, (l.level || 'info').toUpperCase()), h('span', { class: 'tx' }, l.text)); };
  const DOM_CAP = 800;
  function draw() {
    let vis = lines.filter(show);
    if (vis.length > DOM_CAP) vis = vis.slice(-DOM_CAP);
    box.replaceChildren(...(vis.length ? vis.map(lineEl) : [h('div', { class: 'ln empty' }, lines.length ? t('logs.noMatch') : t('logs.empty'))]));
    foot.textContent = t('logs.lines', { n: lines.length });
    if (follow) box.scrollTop = box.scrollHeight;
  }
  function add(l) {
    lines.push(l); if (lines.length > 3000) lines.shift();
    if (!show(l)) { foot.textContent = t('logs.lines', { n: lines.length }); return; }
    box.querySelector('.empty')?.remove(); box.append(lineEl(l));
    while (box.children.length > DOM_CAP) box.firstChild.remove();
    foot.textContent = t('logs.lines', { n: lines.length });
    if (follow) box.scrollTop = box.scrollHeight;
  }
  async function load() {
    note.replaceChildren();
    if (env.legacy) { note.replaceChildren(h('div', { class: 'alert info', style: 'margin:0 0 8px' }, icon('info'), t('logs.envPending'))); draw(); return; }
    try {
      const r = (await api('GET', `/envs/${encodeURIComponent(env.id)}/logs?tail=500`)) || [];
      lines.splice(0, lines.length, ...r.filter((x) => x && x.text != null));
    } catch (e) {
      note.replaceChildren(h('div', { class: 'alert info', style: 'margin:0 0 8px' }, icon('info'), isMissing(e) ? t('logs.envPending') : e.message));
    }
    draw();
  }
  let lastState = env.state;
  const offs = [
    on('env.log', (p) => { if (p?.id === env.id) add({ t: p.t || new Date().toISOString(), level: p.level || 'info', text: p.text || '' }); }),
    on('env.updated', (p) => { if (p?.id === env.id && p.state !== lastState) { add({ t: new Date().toISOString(), level: 'event', text: t('logs.stateChanged', { from: t('envstate.' + lastState), to: t('envstate.' + p.state) }) + (p.status?.message ? ' · ' + p.status.message : '') }); lastState = p.state; } }),
  ];
  function download() {
    const blob = new Blob([lines.map((l) => `${l.t} [${l.level}] ${l.text}`).join('\n') + '\n'], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob), a = h('a', { href: url, download: `${env.name.replace(/[^\w.-]+/g, '_')}-env.log` });
    document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  load();
  return { destroy() { offs.forEach((f) => f()); } };
}
