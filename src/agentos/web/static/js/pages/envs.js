// 环境 (home) page (owned by M6): grouped environment list + detail pane with
// tabs 概览/智能体/终端/文件/桌面/日志/性能. Routes: #/envs[/:id[/:tab]].
import { vmProgressCard, vmAccessCard, vmPreview, vmInstalling, openNative } from '../components/vm-panel.js';
import { h, icon, toast, confirmDialog, promptDialog, dialog, fmtTime, ago, fmtPct, fmtBytes } from '../util.js';
import { api, MOCK } from '../api.js';
import { t, getLang } from '../i18n.js';
import { store, subscribe, load, notify, envById, agentsForEnv, hasCap, providers, hostOf, startLiveMetrics, presets as loadPresets } from '../store.js';
import { envStatePill, miniBars, gauges, envHost, capItem } from '../components/env-common.js';
import { page, cmd, masterDetail, tabs, envState, agentState, placeholder, dataGrid, setKeys, contextMenu, schemaForm } from '../components/ui.js';
import { GROUP_ORDER, groupLabel, envGroup, envIcon, capChip, CAP_KEYS } from '../components/env-common.js';
import { openEnvWizard, showJoinDialog } from '../components/env-wizard.js';
import { openAgentDialog } from '../components/agent-dialog.js';
import { logo } from '../components/logos.js';

const TABS = ['overview', 'agents', 'terminal', 'files', 'desktop', 'logs', 'perf'];
const TAB_ICON = { overview: 'info', agents: 'bot', terminal: 'terminal', files: 'folder', desktop: 'screen', logs: 'list', perf: 'pulse' };
const enc = encodeURIComponent;
const isBuiltin = (e) => e?.labels?.builtin === 'true' || e?.labels?.builtin === true;
const canLifecycle = (e) => hasCap(e, 'lifecycle') && !e.legacy && !isBuiltin(e);

function tabState(e, id) {
  const need = { agents: () => hasCap(e, 'agents') && e.node_id, terminal: () => hasCap(e, 'exec'), files: () => hasCap(e, 'files'),
    desktop: () => hasCap(e, 'desktop.vnc') || hasCap(e, 'desktop.screen'), perf: () => hasCap(e, 'metrics') };
  const ok = need[id] ? !!need[id]() : true;
  return { disabled: !ok, tip: ok ? '' : (id === 'agents' && hasCap(e, 'install-agentos') ? t('envs.tipInstall') : t('envs.tipNoCap', { cap: t('tab.' + id) })) };
}

// ---------- environment actions (shared by command bar, context menu, detail header) ----------
function patchBusy(e, busy) {
  const row = store.envs.find((x) => x.id === e.id);
  if (row) { row.busy = busy || undefined; notify(); }
}
export const envActions = {
  async start(e) {
    patchBusy(e, 'starting');
    try { await api('POST', `/envs/${enc(e.id)}/start`); toast(t('envs.started', { name: e.name })); }
    catch (x) { patchBusy(e, ''); toast(x.message, true); }
  },
  async stop(e) {
    if (!(await confirmDialog({ title: t('envs.stopTitle'), message: t('envs.stopMsg', { name: e.name }), okText: t('cmd.stop') }))) return;
    patchBusy(e, 'stopping');
    try { await api('POST', `/envs/${enc(e.id)}/stop`); toast(t('envs.stopped', { name: e.name })); }
    catch (x) { patchBusy(e, ''); toast(x.message, true); }
  },
  async test(e) {
    try { await api('POST', `/envs/${enc(e.id)}/test`); toast(t('envs.testOk', { name: e.name })); } catch (x) { toast(t('wiz.testFail', { msg: x.message }), true); }
  },
  async install(e) {
    if (!(await confirmDialog({ title: t('envs.installTitle'), message: t('envs.installMsg', { name: e.name }), okText: t('envs.install') }))) return;
    try { await api('POST', `/envs/${enc(e.id)}/install-agentos`); toast(t('envs.installStarted')); location.hash = `#/envs/${enc(e.id)}/logs`; } catch (x) { toast(x.message, true); }
  },
  async rename(e) {
    const name = await promptDialog({ title: t('envs.renameTitle'), label: t('wiz.name'), value: e.name, okText: t('common.save') });
    if (!name || name === e.name) return;
    try { await api('PATCH', `/envs/${enc(e.id)}`, { name: name.trim() }); toast(t('common.saved')); } catch (x) { toast(x.message, true); }
  },
  async editConfig(e) {
    const provs = await providers().catch(() => []);
    const p = provs.find((x) => x.type === e.provider);
    const schema = p?.config_schema || {};
    const secrets = new Set(e.secrets_set || []);
    const form = schemaForm(schema, e.config || {}, { secretsSaved: secrets });
    const labels = h('input', { class: 'mono', value: Object.entries(e.labels || {}).filter(([k]) => k !== 'builtin').map(([k, v]) => `${k}=${v}`).join(', ') });
    const body = h('div', {}, h('div', { class: 'form' }, h('div', { class: 'field full' }, h('label', {}, t('wiz.labels')), labels, h('div', { class: 'hint' }, t('wiz.labelsHint')))),
      h('div', { class: 'section-t' }, t('wiz.config')), form.el,
      secrets.size ? h('div', { class: 'small muted', style: 'margin-top:8px' }, icon('key'), ' ', t('envs.secretsSet', { list: [...secrets].join(', ') })) : null);
    dialog({ title: t('envs.editTitle'), sub: e.name, wide: true, body, buttons: ['sp', { text: t('common.cancel'), id: 'cancel' }, { text: t('common.save'), accent: true, id: 'save', onClick: async () => {
      if (!form.validate()) return false;
      const lab = {}; for (const x of labels.value.split(/[,，\n]/)) { const [k, ...v] = x.split('='); if (k.trim()) lab[k.trim()] = v.join('=').trim(); }
      try { await api('PATCH', `/envs/${enc(e.id)}`, { config: form.value(), labels: lab }); toast(t('common.saved')); return true; } catch (x) { toast(x.message, true); return false; }
    } }] });
  },
  async remove(list) {
    list = list.filter((e) => !isBuiltin(e) && !e.legacy);
    if (!list.length) return;
    const purge = h('input', { type: 'checkbox' });
    const one = list.length === 1;
    const extra = h('label', { class: 'chk', style: 'margin-top:12px' }, purge, t('envs.purge'));
    const ok = await confirmDialog({ title: t('envs.deleteTitle'), message: one ? t('envs.deleteMsg', { name: list[0].name }) : t('envs.deleteMany', { n: list.length }), danger: true, okText: t('cmd.delete'), extra });
    if (!ok) return;
    for (const e of list) {
      patchBusy(e, 'deleting');
      try { await api('DELETE', `/envs/${enc(e.id)}` + (purge.checked ? '?purge=1' : '')); }
      catch (x) { patchBusy(e, ''); toast(`${e.name}: ${x.message}`, true); }
    }
    toast(t('envs.deleted', { n: list.length }));
    if (!store.legacy) { store.envs = store.envs.filter((x) => !list.some((d) => d.id === x.id)); }
  },
  async installStack(e, stackId) {
    try { await api('POST', `/envs/${enc(e.id)}/stacks/${enc(stackId)}/install`); toast(t('tplp.installStarted')); location.hash = `#/envs/${enc(e.id)}/logs`; } catch (x) { toast(x.message, true); }
  },
};

export default {
  get title() { return t('nav.envs'); },
  async render(el, params) {
    let curId = params.id ? decodeURIComponent(params.id) : null, curTab = TABS.includes(params.tab) ? params.tab : 'overview';
    let q = '';
    const search = h('input', { type: 'search', placeholder: t('envs.search'), 'aria-label': t('envs.search'), oninput: () => { q = search.value.toLowerCase(); fill(); } });
    const cStart = cmd({ icon: 'play', text: t('cmd.start'), id: 'start', onClick: () => sel().forEach(envActions.start) });
    const cStop = cmd({ icon: 'stop', text: t('cmd.stop'), id: 'stop', onClick: () => sel().forEach(envActions.stop) });
    const cDel = cmd({ icon: 'trash', text: t('cmd.delete'), id: 'delete', onClick: () => envActions.remove(sel()) });
    const pg = page({ title: t('nav.envs'), flush: true, commands: [
      cmd({ icon: 'plus', text: t('envs.new'), accent: true, id: 'new', onClick: () => newEnv() }), 'sep',
      cStart, cStop, cDel, 'sep',
      cmd({ icon: 'refresh', text: t('cmd.refresh'), id: 'refresh', onClick: () => refresh() })] });
    const grid = dataGrid({
      multi: true,
      columns: [{ title: t('envs.col.name'), cls: 'envc', render: (e) => envRow(e) }],
      groupBy: envGroup, groupOrder: GROUP_ORDER, groupLabel,
      onSelect: (row) => { if (row && row.id !== curId) { curId = row.id; nav(); showDetail(); } syncCmds(); },
      onOpen: (row) => { curId = row.id; nav(); showDetail(); },
      onContext: (ev, row, ids) => rowMenu(ev, envById(row.id), ids),
      empty: () => {
        if (!store.ready) return envListSkeleton();
        return q ? placeholder('search', t('envs.noMatch'), t('envs.noMatchHint', { q })) : placeholder('envs', t('envs.empty'), t('envs.emptyHint'), h('div', { class: 'col', style: 'align-items:center;gap:10px' }, h('div', { class: 'row', style: 'justify-content:center;gap:8px' }, h('button', { class: 'btn accent', 'data-act': 'one-click', onclick: () => oneClick() }, icon('zap'), t('envs.quickCreate')), h('button', { class: 'btn', onclick: () => newEnv() }, icon('plus'), t('envs.new'))), h('p', { class: 'muted small', style: 'max-width:420px;text-align:center;margin:0' }, t('envs.emptyPw'))));
      },
    });
    grid.el.querySelector('thead')?.remove();
    const detail = h('div', { class: 'detail-in', style: 'display:flex;flex-direction:column;min-height:0;flex:1' });
    const quick = h('div', { class: 'quick-strip', hidden: true });
    const md = masterDetail({ key: 'envs', list: [h('div', { class: 'list-tools' }, h('div', { class: 'search' }, icon('search'), search)), grid.el, quick], detail, width: 380 });
    // "快速创建" strip under a short list: one click on a preset creates it (POST /envs {preset}).
    let quickSig = '';
    async function drawQuick() {
      const own = store.envs.filter((e) => e.labels?.builtin !== 'true').length;
      if (!store.ready || own >= 3) { quick.hidden = true; quickSig = ''; return; }
      const list = (await loadPresets().catch(() => [])).filter((p) => p.available).slice(0, 4);
      const sig = list.map((p) => p.id).join();
      if (!list.length) { quick.hidden = true; return; }
      quick.hidden = false;
      if (sig === quickSig) return; quickSig = sig;
      quick.replaceChildren(h('div', { class: 'qs-h' }, icon('zap'), h('b', {}, t('wiz.quick')), h('span', { class: 'sp' }), h('a', { href: '#', onclick: (e) => { e.preventDefault(); newEnv(); } }, t('envs.moreTemplates'))),
        h('div', { class: 'qs-list' }, list.map((p) => h('button', { type: 'button', class: 'qs-i', 'data-preset': p.id, title: (getLang() === 'en' && p.description_en) || p.description, onclick: () => quickCreate(p) },
          logo(p.icon || p.provider, 24), h('span', {}, (getLang() === 'en' && p.name_en) || p.name)))));
    }
    function afterCreate(e) {
      curId = e.id;
      curTab = (hasCap(e, 'desktop.vnc') || hasCap(e, 'desktop.screen')) ? 'desktop' : 'overview';
      nav(); fill(); showDetail();
    }
    async function quickCreate(p) {
      if (!p) { toast(t('wiz.selectPreset'), true); return; }
      if (!p.available) { toast(p.reason || t('wiz.unavailable'), true); return; }
      toast(t('wiz.creatingHint'));
      try {
        const e = await api('POST', '/envs', { preset: p.id });
        toast(t('wiz.created', { name: e.name }));
        await load().catch(() => {}); afterCreate(e);
      } catch (x) { toast(x.message || String(x), true); }
    }
    async function oneClick() {
      const list = (await loadPresets().catch(() => [])).filter((p) => p.available);
      const p = list.find((x) => x.id === 'agentos-desktop') || list[0];
      if (p) quickCreate(p); else newEnv();
    }
    pg.body.append(md.el); el.append(pg.el);

    const sel = () => [...grid.selected].map(envById).filter(Boolean);
    function syncCmds() {
      const s = sel();
      cStart.disabled = !s.some((e) => canLifecycle(e) && e.state !== 'running');
      cStop.disabled = !s.some((e) => canLifecycle(e) && e.state === 'running');
      cDel.disabled = !s.some((e) => !isBuiltin(e) && !e.legacy);
    }
    function crumbs() { const e = envById(curId); pg.setCrumbs(e ? [{ text: e.name }, curTab !== 'overview' ? { text: t('tab.' + curTab) } : null] : []); }
    function nav() { crumbs(); const hsh = `#/envs/${enc(curId)}` + (curTab !== 'overview' ? '/' + curTab : ''); if (location.hash !== hsh) history.replaceState(null, '', hsh); }
    function fill(hint) {
      const rows = store.envs.filter((e) => !q || (e.name + ' ' + (e.provider_info?.name || '') + ' ' + e.provider + ' ' + (e.status?.address || '')).toLowerCase().includes(q));
      if (hint === 'metrics' && store.ready && rows.length) {
        // Patch live mini-bars / state cells by id — no full list rebuild.
        for (const e of rows) grid.patchRow(e.id, e);
        syncCmds();
        return;
      }
      grid.setRows(rows);
      if (store.ready && (!curId || !envById(curId))) curId = (store.envs.find(isBuiltin) || store.envs[0])?.id || null;
      if (curId && grid.primary !== curId) grid.select(curId, { silent: true });
      syncCmds();
    }
    async function newEnv() { const e = await openEnvWizard(); if (e) { await load().catch(() => {}); afterCreate(e); } }
    async function refresh() { try { await load(); toast(t('common.refreshed')); } catch (x) { toast(x.message, true); } }
    function rowMenu(ev, e, ids) {
      if (!e) return;
      const multi = ids.size > 1, list = [...ids].map(envById).filter(Boolean);
      contextMenu(ev, multi ? [
        { text: t('cmd.start'), icon: 'play', disabled: !list.some(canLifecycle), onClick: () => list.forEach(envActions.start) },
        { text: t('cmd.stop'), icon: 'stop', disabled: !list.some(canLifecycle), onClick: () => list.forEach(envActions.stop) }, '-',
        { text: t('cmd.delete'), icon: 'trash', danger: true, key: 'Del', disabled: !list.some((x) => !isBuiltin(x) && !x.legacy), onClick: () => envActions.remove(list) },
      ] : [
        { text: t('envs.open'), icon: 'right', onClick: () => { curId = e.id; nav(); showDetail(); } },
        { text: t('tab.terminal'), icon: 'terminal', disabled: !hasCap(e, 'exec'), onClick: () => { curId = e.id; curTab = 'terminal'; nav(); showDetail(); } },
        { text: t('tab.files'), icon: 'folder', disabled: !hasCap(e, 'files'), onClick: () => { curId = e.id; curTab = 'files'; nav(); showDetail(); } }, '-',
        { text: t('cmd.start'), icon: 'play', disabled: !canLifecycle(e) || e.state === 'running' || !!e.busy, onClick: () => envActions.start(e) },
        { text: t('cmd.stop'), icon: 'stop', disabled: !canLifecycle(e) || e.state !== 'running' || !!e.busy, onClick: () => envActions.stop(e) },
        { text: t('envs.test'), icon: 'plug', disabled: e.legacy, onClick: () => envActions.test(e) },
        hasCap(e, 'install-agentos') ? { text: t('envs.install'), icon: 'install', onClick: () => envActions.install(e) } : null,
        e.provider === 'agentos-remote' && !e.legacy ? { text: t('join.title'), icon: 'link', onClick: () => showJoinDialog(e) } : null,
        e.node_id ? { text: t('agents.new'), icon: 'bot', onClick: () => openAgentDialog({ envId: e.id }) } : null, '-',
        { text: t('common.rename'), icon: 'edit', disabled: e.legacy, onClick: () => envActions.rename(e) },
        { text: t('envs.editConfig'), icon: 'settings', disabled: e.legacy || isBuiltin(e), onClick: () => envActions.editConfig(e) },
        { text: t('cmd.delete'), icon: 'trash', danger: true, key: 'Del', disabled: isBuiltin(e) || e.legacy || !!e.busy, onClick: () => envActions.remove([e]) },
      ]);
    }

    // ---------- detail pane ----------
    let shown = { id: null, tab: null }, tabCleanup = null, tabBody = null, headEl = null, tabsCtl = null;
    function showDetail(force) {
      const e = envById(curId);
      if (!e) { teardown(); shown = { id: null, tab: null }; crumbs(); detail.replaceChildren(store.ready ? placeholder('envs', t('envs.none'), t('envs.noneHint')) : envDetailSkeleton()); return; }
      if (tabState(e, curTab).disabled) curTab = 'overview';
      if (!force && shown.id === e.id && shown.tab === curTab) { refreshHead(e); return; }
      const sameEnv = shown.id === e.id;
      shown = { id: e.id, tab: curTab };
      if (!sameEnv || !headEl) {
        headEl = h('div', { class: 'detail-h' });
        tabsCtl = tabs(TABS.map((id) => ({ id, text: t('tab.' + id), icon: TAB_ICON[id], ...tabState(e, id) })), curTab, (id) => { curTab = id; nav(); showDetail(); });
        tabBody = h('div', { class: 'tab-body' });
        detail.replaceChildren(headEl, tabsCtl.el, tabBody);
      } else tabsCtl.set(curTab);
      refreshHead(e);
      teardown();
      tabBody.className = 'tab-body' + (curTab === 'desktop' ? ' fill console-tab' : ['terminal', 'files', 'logs'].includes(curTab) ? ' fill' : '');
      md.el.classList.toggle('focus', curTab === 'desktop' && localStorage.getItem('agentos.console.wide') !== '0');
      tabBody.replaceChildren();
      renderTab(e, curTab, tabBody);
    }
    function teardown() { if (tabCleanup) { try { tabCleanup(); } catch (x) { console.warn(x); } tabCleanup = null; } }
    function refreshHead(e) {
      const acts = [];
      const go = (tab) => { curTab = tab; nav(); showDetail(); };
      if (canLifecycle(e)) acts.push(e.state === 'running'
        ? h('button', { class: 'btn', 'data-act': 'stop', disabled: !!e.busy, onclick: () => envActions.stop(e) }, icon('stop'), t('cmd.stop'))
        : h('button', { class: 'btn accent', 'data-act': 'start', disabled: e.state === 'creating' || !!e.busy, onclick: () => envActions.start(e) }, icon('play'), t('cmd.start')));
      const deskOk = !tabState(e, 'desktop').disabled && e.state === 'running';
      acts.push(h('button', { class: 'btn' + (deskOk && !canLifecycle(e) ? ' accent' : ''), 'data-act': 'open-desktop', disabled: !deskOk, title: t('envs.openDesktop'), onclick: () => go('desktop') }, icon('screen'), t('envs.openDesktop')));
      acts.push(h('button', { class: 'btn', 'data-act': 'open-terminal', disabled: tabState(e, 'terminal').disabled || e.state !== 'running', onclick: () => go('terminal') }, icon('terminal'), t('tab.terminal')));
      const toolsOk = hasCap(e, 'exec') && e.state === 'running' && !e.legacy;
      acts.push(h('button', { class: 'btn', 'data-act': 'install-tools', disabled: !toolsOk, title: t('envs.installTools'), onclick: () => { location.hash = '#/templates?env=' + enc(e.id); } }, icon('install'), t('envs.installTools')));
      if (e.provider === 'agentos-remote' && !e.legacy) acts.push(h('button', { class: 'btn', 'data-act': 'join', onclick: () => showJoinDialog(e) }, icon('link'), t('join.title')));
      acts.push(h('button', { class: 'btn icon-only subtle', title: t('common.more'), 'aria-label': t('common.more'), 'data-act': 'more', onclick: (ev) => { const r = ev.currentTarget.getBoundingClientRect(); rowMenu({ clientX: r.right - 220, clientY: r.bottom + 4 }, e, new Set([e.id])); } }, icon('more')));
      const sub = [envStatePill(e), h('span', {}, e.provider_info?.name || e.provider)];
      if (e.status?.address) sub.push(h('span', { class: 'dot' }), h('span', { class: 'mono' }, e.status.address));
      headEl.replaceChildren(h('div', { class: 'tile logo-tile' }, logo(e, 52)),
        h('div', { class: 'nm' }, h('h2', {}, h('span', { class: 't' }, e.name), isBuiltin(e) ? h('span', { class: 'badge info plain sm' }, t('envs.builtin')) : null), h('div', { class: 'sub' }, sub)),
        h('div', { class: 'acts' }, acts));
      // keep tab enablement in sync with capabilities
      tabsCtl?.el.querySelectorAll('.tab').forEach((b) => { const st = tabState(e, b.dataset.tab); b.disabled = st.disabled; b.title = st.tip; });
      crumbs();
    }
    async function renderTab(e, tab, box) {
      const R = TAB_RENDER[tab];
      const heavy = ['desktop', 'terminal', 'files'].includes(tab);
      if (heavy) box.replaceChildren(h('div', { class: 'tab-connecting', 'data-connecting': tab }, h('span', { class: 'spinner' }), h('p', {}, t('envs.tabConnecting', { tab: t('tab.' + tab) }))));
      try {
        const c = await R(box, e);
        // Heavy tabs replace the spinner themselves when they mount; clear if they appended alongside it.
        box.querySelector('[data-connecting]')?.remove();
        tabCleanup = typeof c === 'function' ? c : c?.destroy ? () => c.destroy() : null;
      } catch (x) { console.warn(x); box.replaceChildren(h('div', { class: 'alert err' }, icon('alert'), t('common.loadFailed', { msg: x.message }))); }
    }

    const unsub = subscribe((_st, hint) => {
      fill(hint); if (hint !== 'metrics') drawQuick();
      const e = envById(curId);
      if (!e) { showDetail(); return; }
      if (shown.id !== e.id) { showDetail(); return; }
      if (hint === 'metrics') {
        // Overview gauges + agent mini stats: refresh those panes without tearing down desktop/terminal.
        if (curTab === 'overview' || curTab === 'agents') { teardown(); tabBody.replaceChildren(); renderTab(e, curTab, tabBody); }
        else refreshHead(e);
        return;
      }
      refreshHead(e);
      if (curTab === 'overview' || curTab === 'agents') { teardown(); tabBody.replaceChildren(); renderTab(e, curTab, tabBody); }
    });
    const unlive = startLiveMetrics();
    const unkeys = setKeys({ new: () => newEnv(), refresh: () => refresh(), delete: () => envActions.remove(sel()), find: () => search.focus() });
    fill(); showDetail(); drawQuick();
    if (store.ready) {} else load().catch(() => {});
    if (params.query?.get('new') === '1') newEnv();
    this._update = (p) => { const id = p.id ? decodeURIComponent(p.id) : curId; const tb = TABS.includes(p.tab) ? p.tab : 'overview'; if (id !== curId || tb !== curTab) { curId = id; curTab = tb; fill(); showDetail(); } };
    return () => { unsub(); unkeys(); unlive(); teardown(); md.el.classList.remove('focus'); this._update = null; };
  },
  update(p) { if (this._update) this._update(p); else throw new Error('stale'); },
};

// On-demand stack detection (GET /envs/{id}/stacks runs detect commands inside the env).
const stackCache = new Map();
function stacksCard(e) {
  const body = h('div', {});
  const draw = (r) => {
    if (!r) { body.replaceChildren(h('div', { class: 'row' }, h('span', { class: 'muted small grow' }, t('envs.stacksHint')), h('button', { class: 'btn sm', 'data-act': 'detect-stacks', onclick: run }, icon('search'), t('envs.detect')))); return; }
    if (r.loading) { body.replaceChildren(h('div', { class: 'row' }, h('span', { class: 'spinner sm' }), t('tplp.detecting'))); return; }
    if (r.error) { body.replaceChildren(h('div', { class: 'alert warn', style: 'margin:0' }, icon('alert'), r.error), h('button', { class: 'btn sm', style: 'margin-top:8px', onclick: run }, icon('refresh'), t('envs.detect'))); return; }
    const all = r.stacks || [];
    const inst = all.filter((x) => x.installed);
    const miss = all.filter((x) => !x.installed).slice(0, 8);
    body.replaceChildren(
      r.os ? h('div', { class: 'small muted', style: 'margin-bottom:8px' }, [r.os.pretty || [r.os.id, r.os.version].filter(Boolean).join(' ') || r.os.family, r.os.arch].filter(Boolean).join(' · ')) : null,
      r.installing ? h('div', { class: 'alert info', style: 'margin:0 0 8px' }, h('span', { class: 'spinner sm' }), t('tplp.installing', { name: r.installing })) : null,
      inst.length ? h('div', { style: 'display:flex;flex-wrap:wrap;gap:6px;margin-bottom:8px' }, inst.map((x) => h('span', { class: 'chip', title: x.output || '' }, icon('check'), ' ', x.name, x.version ? ' ' + x.version : ''))) : h('div', { class: 'muted small', style: 'margin-bottom:8px' }, t('envs.noStacks')),
      miss.length && !r.installing ? h('div', { class: 'row', style: 'flex-wrap:wrap;gap:6px;margin-bottom:8px' }, miss.map((x) => h('button', { type: 'button', class: 'btn sm', 'data-act': 'install-stack', 'data-stack': x.id, disabled: e.state !== 'running', onclick: () => envActions.installStack(e, x.id) }, icon('install'), x.name))) : null,
      h('div', { class: 'row', style: 'margin-top:4px' }, h('a', { href: '#/templates?env=' + enc(e.id) }, t('envs.moreStacks')), h('span', { class: 'sp' }), h('button', { class: 'btn sm subtle', onclick: run }, icon('refresh'), t('envs.detect'))));
  };
  async function run() {
    stackCache.set(e.id, { loading: true }); draw({ loading: true });
    try { const r = await api('GET', `/envs/${enc(e.id)}/stacks`); stackCache.set(e.id, r); }
    catch (x) { stackCache.set(e.id, { error: x.message }); }
    if (body.isConnected) draw(stackCache.get(e.id));
  }
  draw(stackCache.get(e.id));
  return h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, t('envs.stacksTitle'))), body);
}

// ---------- tab renderers: (box, env) -> cleanup | {destroy} ----------
const TAB_RENDER = {
  overview(box, e) {
    const caps = e.capabilities || [];
    const ags = agentsForEnv(e);
    const kv = (k, v) => [h('div', {}, k), h('div', { class: 'selectable' }, v ?? '—')];
    const labels = Object.entries(e.labels || {});
    const n = (e.node_id && store.nodes.find((x) => x.id === e.node_id)) || e.node;
    const hst = hostOf(e);
    const run = ags.filter((a) => a.status?.state === 'running').length, bad = ags.filter((a) => ['failed', 'backoff'].includes(a.status?.state)).length;
    const buildingDesk = e.provider === 'docker' && (e.state === 'creating' || e.busy === 'provisioning') && (e.config?.desktop === true || e.config?.desktop === 'true');
    const hypervISO = e.provider === 'hyperv' && (e.config?.needs_iso === true || e.config?.needs_iso === 'true');
    const hv = e.provider === 'hyperv' && !hypervISO;
    box.append(...[
      hv && vmInstalling(e) ? vmProgressCard(e) : null,
      hv && (e.config?.guest_user || e.config?.vm_name) ? vmAccessCard(e) : null,
      buildingDesk ? h('div', { class: 'alert info', 'data-banner': 'desktop-build' }, h('span', { class: 'spinner sm' }), h('div', { class: 'grow' }, t('envs.buildDesktop')), h('a', { href: `#/envs/${enc(e.id)}/logs` }, t('tab.logs'))) : null,
      hypervISO ? h('div', { class: 'alert warn', 'data-banner': 'hyperv-iso' }, icon('alert'), h('div', { class: 'grow' }, e.config?.notes || t('desk.hypervNeedsISO')), h('a', { href: `#/envs/${enc(e.id)}/logs` }, t('tab.logs'))) : null,
      e.state === 'error' || e.status?.message ? h('div', { class: 'alert ' + (e.state === 'error' ? 'err' : 'info') }, icon(e.state === 'error' ? 'alert' : 'info'), h('div', { class: 'grow selectable' }, e.status?.message || e.error || t('envstate.error'))) : null,
      hasCap(e, 'install-agentos') && !e.node_id && !vmInstalling(e) && e.provider !== 'hyperv' ? h('div', { class: 'alert info' }, icon('install'), h('div', { class: 'grow' }, h('b', {}, t('envs.installTitle')), h('div', { class: 'small' }, t('envs.installHint'))),
        h('button', { class: 'btn accent sm', 'data-act': 'install', onclick: () => envActions.install(e) }, t('envs.install'))) : null,
      e.state === 'running' && (hst || hasCap(e, 'metrics')) ? h('div', { class: 'card', 'data-card': 'resources' }, h('div', { class: 'card-h' }, h('h3', {}, icon('pulse'), t('envs.resources')), h('a', { href: `#/envs/${enc(e.id)}/perf` }, t('envs.details'))), gauges(hst)) : null,
      h('div', { class: 'cols' },
        h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, icon('info'), t('envs.info'))),
          h('div', { class: 'kv' },
            kv(t('envs.col.state'), envStatePill(e)), kv(t('envs.type'), h('span', { class: 'row', style: 'gap:8px' }, logo(e, 18), e.provider_info?.name || e.provider)), kv(t('envs.group'), groupLabel(envGroup(e))),
            kv(t('envs.address'), e.status?.address ? h('span', { class: 'mono' }, e.status.address) : '—'),
            kv(t('envs.node'), e.node_id ? h('span', { class: 'mono' }, e.node_id) : t('envs.noNode')),
            n ? kv(t('node.system'), [envHost(e), n.version ? 'v' + n.version : ''].filter(Boolean).join(' · ')) : null,
            e.stack ? kv(t('envs.stack'), e.stack) : null,
            kv(t('envs.created'), e.created_at ? fmtTime(e.created_at) : '—'),
            kv('ID', h('span', { class: 'mono' }, e.id)))),
        h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, icon('shield'), t('envs.caps'))),
          h('div', { class: 'capgrid' }, CAP_KEYS.map((c) => capItem(c, caps.includes(c)))),
          h('div', { class: 'card-h', style: 'margin-top:16px' }, h('h3', {}, icon('tag'), t('envs.labels'))),
          labels.length ? h('div', { class: 'chips' }, labels.map(([k, v]) => h('span', { class: 'chip mono' }, `${k}=${v}`))) : h('div', { class: 'muted small' }, t('common.none')))),
      hasCap(e, 'exec') && !e.legacy ? stacksCard(e) : null,
      e.node_id ? h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, icon('bot'), t('envs.agentsSummary')), h('span', { class: 'sp' }), h('a', { href: `#/envs/${enc(e.id)}/agents` }, t('dash.viewAll'))),
        h('div', { class: 'metric-tiles', style: 'margin:0' },
          h('div', { class: 'tile-m' }, h('div', { class: 'k' }, t('envs.agentsTotal')), h('div', { class: 'v' }, String(ags.length))),
          h('div', { class: 'tile-m' }, h('div', { class: 'k' }, h('span', { class: 'st ok' }, h('i')), t('state.running')), h('div', { class: 'v' }, String(run))),
          h('div', { class: 'tile-m' }, h('div', { class: 'k' }, h('span', { class: 'st err' }, h('i')), t('state.failed')), h('div', { class: 'v', style: bad ? 'color:var(--err)' : '' }, String(bad))))) : null].filter(Boolean));
  },
  agents(box, e) {
    const ags = agentsForEnv(e);
    box.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, t('envs.agentsOn', { n: ags.length })), h('span', { class: 'sp' }),
      h('button', { class: 'btn accent sm', 'data-act': 'new-agent', disabled: e.state !== 'running' && !e.legacy, onclick: async () => { const a = await openAgentDialog({ envId: e.id }); if (a) location.hash = '#/agents/' + enc(a.id); } }, icon('plus'), t('agents.new'))));
    if (!ags.length) { box.append(placeholder('🤖', t('agents.empty'), t('envs.agentsEmptyHint'))); return; }
    const g = dataGrid({
      columns: [
        { title: t('agents.col.name'), cls: 'nm', render: (a) => h('span', { class: 'two' }, h('span', { class: 'ei' }, icon('bot')), h('span', { class: 'tx' }, h('b', {}, a.name), h('small', { class: 'mono' }, [a.command, ...(a.args || [])].join(' ').slice(0, 80)))) },
        { title: t('agents.col.state'), width: '110px', render: (a) => agentState(a.status?.state || 'stopped') },
        { title: 'CPU', cls: 'num', width: '70px', render: (a) => (a.status?.stats ? fmtPct(a.status.stats.cpu_percent) : '—') },
        { title: t('agents.col.mem'), cls: 'num', width: '84px', render: (a) => (a.status?.stats ? fmtBytes(a.status.stats.mem_rss) : '—') },
        { title: t('agents.col.uptime'), width: '96px', render: (a) => (a.status?.state === 'running' && a.status.started_at ? ago(a.status.started_at) : '—') },
      ],
      onOpen: (a) => { location.hash = '#/agents/' + enc(a.id); },
      onContext: (ev, a) => contextMenu(ev, [{ text: t('envs.open'), icon: 'right', onClick: () => { location.hash = '#/agents/' + enc(a.id); } }, { text: t('tab.logs'), icon: 'file', onClick: () => { location.hash = '#/agents/' + enc(a.id) + '/logs'; } }]),
    });
    g.setRows(ags);
    box.append(h('div', { class: 'card flush', style: 'padding:0;overflow:hidden' }, g.el));
  },
  async terminal(box, e) {
    if (e.state !== 'running') { box.replaceChildren(placeholder('⌨️', t('term.notRunning'), t('term.notRunningHint'))); return; }
    const { mountTerminal } = await import('../components/terminal.js');
    box.replaceChildren(); // disconnect previous session when leaving is handled by teardown; connect only now
    return mountTerminal(box, e.id);
  },
  async files(box, e) {
    const { mountFiles } = await import('../components/files.js');
    box.replaceChildren();
    return mountFiles(box, e);
  },
  async desktop(box, e) {
    if (MOCK) { box.replaceChildren(h('div', { class: 'console' }, h('div', { class: 'stage' }, h('div', { class: 'mockscreen' }, placeholder('desktop', t('desk.mock'), t('desk.mockHint')))))); return; }
    if (e.provider === 'hyperv' && (e.config?.needs_iso === true || e.config?.needs_iso === 'true')) {
      box.replaceChildren(placeholder('desktop', t('desk.hypervNeedsISO'), e.config?.notes || '')); return;
    }
    if (vmInstalling(e)) { // live console preview while the OS installs
      const pv = vmPreview(e, { width: 1280, every: 3000, cls: 'big' });
      box.replaceChildren(h('div', { class: 'vm-desk-wait' }, h('div', { class: 'alert info' }, h('span', { class: 'spinner sm' }), h('div', { class: 'grow' }, t('vm.deskInstalling')), h('button', { class: 'btn sm', onclick: () => openNative(e, 'console') }, icon('popout'), t('vm.openConsole'))), pv));
      return () => pv.destroy();
    }
    if (e.state !== 'running') { box.replaceChildren(placeholder('desktop', t('desk.offline'), t('desk.offlineHint'))); return; }
    const { mountConsole } = await import('../components/console.js');
    box.replaceChildren();
    const md = box.closest('.md');
    return mountConsole(box, e, { wide: () => md?.classList.contains('focus'), onWide: (v) => { localStorage.setItem('agentos.console.wide', v ? '1' : '0'); md?.classList.toggle('focus', v); dispatchEvent(new Event('resize')); } });
  },
  async logs(box, e) {
    const { mountEnvLogs } = await import('../components/envlogs.js');
    return mountEnvLogs(box, e);
  },
  async perf(box, e) {
    const { mountEnvMetrics } = await import('../components/metrics.js');
    return mountEnvMetrics(box, e);
  },
};

// Rich environment row: logo, name + badges, provider · address, state + live mini CPU/mem bars.
function envRow(e) {
  const sub = [e.provider_info?.name || e.provider];
  if (e.status?.address) sub.push(e.status.address);
  const nAg = e.node_id ? agentsForEnv(e).length : 0;
  return h('div', { class: 'envrow' }, logo(e, 36),
    h('div', { class: 'tx' }, h('b', {}, h('span', { class: 'n' }, e.name), isBuiltin(e) ? h('span', { class: 'badge info plain sm' }, t('envs.builtin')) : null),
      h('small', {}, sub.join(' · '), nAg ? h('span', { title: t('envs.agentsOn', { n: nAg }) }, icon('bot'), ' ', String(nAg)) : null)),
    h('div', { class: 'rt' }, envState(e.state, e.busy), e.state === 'running' ? miniBars(hostOf(e)) : null));
}

function envListSkeleton() {
  return h('div', { class: 'env-skel', 'data-skel': 'list', 'aria-busy': 'true' },
    ...Array.from({ length: 5 }, () => h('div', { class: 'sk-row' }, h('div', { class: 'sk-logo' }), h('div', { class: 'sk-lines' }, h('i'), h('i')))));
}
function envDetailSkeleton() {
  return h('div', { class: 'env-skel', style: 'padding:20px', 'data-skel': 'detail', 'aria-busy': 'true' },
    h('div', { class: 'sk-row', style: 'height:72px' }, h('div', { class: 'sk-logo', style: 'width:52px;height:52px' }), h('div', { class: 'sk-lines' }, h('i'), h('i'))),
    h('div', { class: 'sk-row', style: 'height:120px;margin-top:12px' }, h('div', { class: 'sk-lines', style: 'padding:12px' }, h('i'), h('i'), h('i'))));
}
