// Shared environment helpers (owned by M6): kind icons, group labels, join commands panel.
import { h, icon, copyText, fmtBytes, fmtPct } from '../util.js';
import { hostOf, store } from '../store.js';
import { t, getLang } from '../i18n.js';

export const GROUP_ORDER = ['Agent OS', '本机虚拟机', '容器', '虚拟机', '云沙箱'];
const GROUP_EN = { 'Agent OS': 'Agent OS', '本机虚拟机': 'Local VMs', '容器': 'Containers', '虚拟机': 'SSH / remote VMs', '云沙箱': 'Cloud sandboxes' };
export const groupLabel = (g) => (getLang() === 'en' ? GROUP_EN[g] || g : g);
export const KIND_ICON = { agentos: 'server', container: 'container', vm: 'vm', sandbox: 'cloud' };
export const kindIcon = (k) => KIND_ICON[k] || 'env';
export const envGroup = (e) => e.provider_info?.group || t('envs.otherGroup');
export const envIcon = (e) => kindIcon(e.provider_info?.kind);
export const CAP_KEYS = ['lifecycle', 'exec', 'files', 'desktop.vnc', 'desktop.screen', 'metrics', 'agents', 'install-agentos'];
export const capChip = (c, on = true) => h('span', { class: 'chip' + (on ? '' : ' off'), title: t('cap.' + c + '.d') }, t('cap.' + c));
const CAP_IC = { lifecycle: 'power', exec: 'terminal', files: 'folder', 'desktop.vnc': 'screen', 'desktop.screen': 'monitor', metrics: 'pulse', agents: 'bot', 'install-agentos': 'install' };
export const capItem = (c, on = true) => h('div', { class: 'cap' + (on ? '' : ' off'), title: t('cap.' + c + '.d'), 'data-cap': c }, icon(on ? 'check' : 'x'), h('span', { class: 'ci' }, icon(CAP_IC[c] || 'info')), t('cap.' + c));

// joinPanel({token, server_url, commands:{windows, linux, macos}}) -> Node
export function joinPanel(j) {
  const oses = [['windows', 'Windows (PowerShell)'], ['linux', 'Linux'], ['macos', 'macOS']].filter(([k]) => j.commands?.[k]);
  let cur = oses[0]?.[0], svc = false;
  const cmdOf = () => ((svc && j.service_commands?.[cur]) || j.commands[cur] || '');
  const pre = h('pre', { class: 'code selectable', style: 'white-space:pre-wrap;word-break:break-all;margin:0' });
  const seg = h('div', { class: 'seg' });
  const svcCb = h('input', { type: 'checkbox', onchange: () => { svc = svcCb.checked; draw(); } });
  const draw = () => { seg.replaceChildren(...oses.map(([k, l]) => h('button', { type: 'button', class: k === cur ? 'on' : '', onclick: () => { cur = k; draw(); } }, l))); pre.textContent = cmdOf(); };
  draw();
  return h('div', { class: 'join' },
    h('p', { class: 'muted', style: 'margin:0 0 10px' }, t('join.hint')),
    h('div', { class: 'row', style: 'margin-bottom:8px' }, seg, j.service_commands ? h('label', { class: 'chk', title: t('join.serviceD') }, svcCb, t('join.service')) : null, h('span', { class: 'sp' }),
      h('button', { type: 'button', class: 'btn sm accent', 'data-act': 'copy-join', onclick: () => copyText(cmdOf()) }, icon('copy'), t('join.copy'))),
    pre,
    h('div', { class: 'kv', style: 'margin-top:12px' },
      h('div', {}, t('join.server')), h('div', { class: 'code-row' }, h('code', { class: 'selectable' }, j.server_url || location.origin), h('button', { type: 'button', class: 'btn subtle sm icon-only', title: t('common.copy'), 'aria-label': t('common.copy'), onclick: () => copyText(j.server_url || location.origin) }, icon('copy'))),
      h('div', {}, t('join.token')), h('div', { class: 'code-row' }, h('code', { class: 'selectable' }, j.token || ''), h('button', { type: 'button', class: 'btn subtle sm icon-only', title: t('common.copy'), 'aria-label': t('common.copy'), onclick: () => copyText(j.token || '') }, icon('copy')))),
    h('div', { class: 'small muted', style: 'margin-top:8px' }, t('join.once')));
}

// envStateEx(env): state indicator that also reflects env.busy (provisioning | installing)
export const BUSY = (e) => (e?.busy ? t('busy.' + e.busy) : '');

// State pill for headers: running/stopped/error/... or the busy state with a pulsing dot.
const PILL = { running: 'ok', creating: 'info spin', stopped: '', error: 'err', unknown: 'warn' };
export function envStatePill(e) {
  if (e.busy) return h('span', { class: 'pill info spin', 'data-state': e.state }, h('i'), t('busy.' + e.busy));
  return h('span', { class: 'pill ' + (PILL[e.state] ?? ''), 'data-state': e.state }, h('i'), t('envstate.' + e.state));
}
const pct = (u, tot) => (tot ? Math.min(100, (u / tot) * 100) : 0);
// Two tiny CPU / memory bars for list rows.
export function miniBars(hst) {
  if (!hst) return null;
  const cpu = Math.max(0, Math.min(100, hst.cpu_percent || 0)), mem = pct(hst.mem_used, hst.mem_total);
  const row = (k, v, cls) => h('div', { class: 'mb ' + cls, title: `${k} ${fmtPct(v)}` }, h('span', {}, k), h('span', { class: 'bar' }, h('i', { style: `width:${v.toFixed(1)}%` })));
  return h('div', { class: 'mini', 'aria-label': `CPU ${fmtPct(cpu)}, MEM ${fmtPct(mem)}` }, row('CPU', cpu, 'cpu'), row('MEM', mem, 'mem'));
}
// Ring gauges (CPU / memory / disk) for the overview resource card.
export function gauges(hst) {
  const ring = (cls, v, label, val, sub) => {
    const C = 2 * Math.PI * 24, off = C * (1 - Math.max(0, Math.min(100, v || 0)) / 100);
    return h('div', { class: 'gauge ' + cls },
      h('div', { class: 'ring', html: `<svg viewBox="0 0 56 56"><circle class="tr" cx="28" cy="28" r="24" fill="none" stroke-width="6"/><circle class="fg" cx="28" cy="28" r="24" fill="none" stroke-width="6" stroke-linecap="round" stroke-dasharray="${C.toFixed(2)}" stroke-dashoffset="${off.toFixed(2)}"/></svg><b>${v == null ? '–' : Math.round(v) + '%'}</b>` }),
      h('div', { class: 'gt' }, h('span', {}, label), h('b', {}, val), sub ? h('small', {}, sub) : null));
  };
  const x = hst || {};
  return h('div', { class: 'gauges' },
    ring('cpu', hst ? x.cpu_percent : null, 'CPU', hst ? fmtPct(x.cpu_percent) : '—', x.cpu_count ? t('envs.cores', { n: x.cpu_count }) : (x.load1 != null ? 'load ' + x.load1.toFixed(2) : '')),
    ring('mem', x.mem_total ? pct(x.mem_used, x.mem_total) : null, t('envs.memory'), x.mem_total ? fmtBytes(x.mem_used) : '—', x.mem_total ? '/ ' + fmtBytes(x.mem_total) : ''),
    ring('disk', x.disk_total ? pct(x.disk_used, x.disk_total) : null, t('envs.disk'), x.disk_total ? fmtBytes(x.disk_used) : '—', x.disk_total ? '/ ' + fmtBytes(x.disk_total) : ''));
}
// Short OS/host description for headers (Agent OS envs only).
export function envHost(e) {
  const n = (e.node_id && store.nodes.find((x) => x.id === e.node_id)) || e.node; if (!n) return '';
  const os = { windows: 'Windows', linux: 'Linux', darwin: 'macOS' }[n.os] || n.os || '';
  return [os && n.arch ? `${os} ${n.arch}` : os, n.hostname].filter(Boolean).join(' · ');
}
export { hostOf };
