// Hyper-V local VM panel (0.1.5): zero-touch install progress with a live
// console preview, guest credentials, and native viewers (vmconnect / mstsc).
import { h, icon, toast, copyText } from '../util.js';
import { api } from '../api.js';
import { t } from '../i18n.js';

const enc = encodeURIComponent;
const PHASES = ['download', 'create', 'install', 'configure', 'ready'];

export const isHyperV = (e) => e?.provider === 'hyperv';
export const vmInstalling = (e) => isHyperV(e) && (e.busy === 'provisioning' || e.state === 'creating') && e.config?.phase !== 'ready';

export async function openNative(e, kind) {
  try { await api('POST', `/envs/${enc(e.id)}/open`, { kind }); toast(kind === 'rdp' ? t('vm.rdpOpened') : t('vm.consoleOpened')); }
  catch (x) { toast(x.message, true); }
}

// Live console thumbnail (Hyper-V), refreshed while mounted.
export function vmPreview(e, { width = 960, every = 4000, cls = '' } = {}) {
  const img = h('img', { class: 'vm-shot', alt: t('vm.preview'), draggable: 'false' });
  const err = h('div', { class: 'vm-shot-err muted small' });
  const box = h('div', { class: 'vm-preview ' + cls }, img, err);
  let timer = null, alive = true;
  const load = () => {
    if (!alive) return;
    if (!box.isConnected && timer) { alive = false; return; }
    const next = new Image();
    next.onload = () => { img.src = next.src; err.textContent = ''; box.classList.add('ok'); schedule(); };
    next.onerror = () => { err.textContent = t('vm.previewNA'); schedule(); };
    next.src = `/api/v1/envs/${enc(e.id)}/screenshot?w=${width}&t=${Date.now()}`;
  };
  const schedule = () => { if (alive) timer = setTimeout(load, every); };
  load();
  box.destroy = () => { alive = false; clearTimeout(timer); };
  return box;
}

function step(label, state) {
  return h('div', { class: 'vm-step ' + state }, h('i', {}, state === 'done' ? icon('check') : state === 'on' ? h('span', { class: 'spinner sm' }) : ''), h('span', {}, label));
}

export function vmProgressCard(e) {
  const ph = e.config?.phase || (e.state === 'creating' ? 'download' : '');
  const idx = Math.max(0, PHASES.indexOf(ph));
  const steps = PHASES.map((p, i) => step(t('vm.phase.' + p), i < idx || ph === 'ready' ? 'done' : i === idx ? 'on' : ''));
  const last = h('div', { class: 'small muted vm-last' });
  api('GET', `/envs/${enc(e.id)}/logs?tail=1`).then((l) => { if (l?.[0]) last.textContent = l[0].text; }).catch(() => {});
  return h('div', { class: 'card vm-progress', 'data-card': 'vm-progress' },
    h('div', { class: 'card-h' }, h('h3', {}, icon('install'), t('vm.installing')), h('span', { class: 'sp' }),
      h('a', { href: `#/envs/${enc(e.id)}/logs` }, t('tab.logs'))),
    h('div', { class: 'vm-steps' }, steps), last,
    ph === 'install' || ph === 'configure' ? vmPreview(e, { width: 800 }) : null,
    h('div', { class: 'small muted', style: 'margin-top:6px' }, t('vm.installHint')));
}

function secretField(value) {
  let shown = false;
  const v = h('span', { class: 'mono selectable' }, '••••••••');
  const eye = h('button', { type: 'button', class: 'btn xs subtle', title: t('vm.reveal'), onclick: () => { shown = !shown; v.textContent = shown ? value : '••••••••'; } }, icon('eye'));
  return h('span', { class: 'row', style: 'gap:6px' }, v, eye, h('button', { type: 'button', class: 'btn xs subtle', title: t('join.copy'), onclick: () => copyText(value) }, icon('copy')));
}

export function vmAccessCard(e) {
  const c = e.config || {};
  const win = c.guest_os === 'windows-11';
  const fedora = c.guest_os === 'fedora-workstation';
  const ip = e.status?.address || c.guest_address || '';
  const kv = (k, v) => [h('div', {}, k), h('div', {}, v ?? '—')];
  const running = e.state === 'running';
  const hasDesk = (e.capabilities || []).includes('desktop.vnc');
  return h('div', { class: 'card vm-access', 'data-card': 'vm-access' },
    h('div', { class: 'card-h' }, h('h3', {}, icon('key'), t('vm.access'))),
    h('div', { class: 'kv' },
      kv(t('vm.os'), t('vm.os.' + (c.guest_os || 'custom'))),
      c.guest_user ? kv(t('vm.user'), h('span', { class: 'row', style: 'gap:6px' }, h('span', { class: 'mono selectable' }, c.guest_user), h('button', { type: 'button', class: 'btn xs subtle', onclick: () => copyText(c.guest_user) }, icon('copy')))) : null,
      c.guest_password ? kv(t('vm.password'), secretField(c.guest_password)) : null,
      ip ? kv('IP', h('span', { class: 'mono selectable' }, ip)) : null,
      ip && c.guest_user ? kv('SSH', h('span', { class: 'mono selectable' }, `${c.guest_user}@${ip}`)) : null,
      c.install_seconds ? kv(t('vm.installTime'), `${Math.floor(c.install_seconds / 60)} ${t('vm.min')} ${c.install_seconds % 60} ${t('vm.sec')}`) : null,
      c.vm_name ? kv('Hyper-V', h('span', { class: 'mono selectable' }, c.vm_name)) : null),
    h('div', { class: 'row', style: 'flex-wrap:wrap;gap:8px;margin-top:12px' },
      hasDesk ? h('a', { class: 'btn sm accent' + (running ? '' : ' disabled'), href: `#/envs/${enc(e.id)}/desktop`, 'data-act': 'vm-desktop' }, icon('screen'), t('vm.openDesktop')) : null,
      win ? h('button', { type: 'button', class: 'btn sm' + (hasDesk ? '' : ' accent'), disabled: !running || !ip, 'data-act': 'vm-rdp', onclick: () => openNative(e, 'rdp') }, icon('monitor'), t('vm.openRDP')) : null,
      h('button', { type: 'button', class: 'btn sm', 'data-act': 'vm-console', onclick: () => openNative(e, 'console') }, icon('popout'), t('vm.openConsole'))),
    h('div', { class: 'small muted', style: 'margin-top:8px' }, win ? t('vm.hintWin') : fedora ? t('vm.hintFedora') : t('vm.hintUbuntu')));
}
