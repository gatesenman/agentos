// 智能体 page (owned by M6): all agents across environments; master-detail with
// tabs 概览/日志/性能/配置. Routes: #/agents[/:id[/:tab]], #/agents/new.
import { h, icon, toast, confirmDialog, fmtTime, fmtDuration, since, fmtPct, fmtBytes, shellJoin, copyText } from '../util.js';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { store, subscribe, load, envForNode } from '../store.js';
import { page, cmd, masterDetail, tabs, agentState, placeholder, dataGrid, setKeys, contextMenu } from '../components/ui.js';
import { openAgentDialog } from '../components/agent-dialog.js';
import { openFedWizard } from '../components/fed-wizard.js';
import { openLocalScanDialog } from '../components/local-scan-dialog.js';
import { logo } from '../components/logos.js';
import { LogView } from '../components/logview.js';
import { mountAgentMetrics } from '../components/metrics.js';

const TABS = ['overview', 'logs', 'perf', 'config'];
const TAB_ICON = { overview: 'info', logs: 'list', perf: 'pulse', config: 'settings' };
const AGENT_ST = { running: 'ok', starting: 'info spin', backoff: 'warn', failed: 'err', exited: '', stopped: '', unknown: 'warn' };
// Agent avatar: gradient tile with the first letter (stable colour per name).
const HUES = [212, 262, 190, 152, 28, 330, 236, 175];
export function agentAvatar(a, size = 32) {
  let x = 0; for (const ch of a.name || '?') x = (x * 31 + ch.codePointAt(0)) >>> 0;
  const hue = HUES[x % HUES.length], ch = ([...(a.name || '?').replace(/^[^\p{L}\p{N}]+/u, '')][0] || '?').toUpperCase();
  return h('span', { class: 'avatar', style: `width:${size}px;height:${size}px;font-size:${Math.round(size * 0.44)}px;background:linear-gradient(135deg,hsl(${hue} 70% 58%),hsl(${hue + 24} 64% 42%))` }, ch,
    h('i', { class: 'dot ' + (AGENT_ST[a.status?.state || 'stopped'] || '') }));
}
const statePill = (st) => h('span', { class: 'pill ' + (AGENT_ST[st] || '') }, h('i'), t('state.' + st));
const enc = encodeURIComponent;
const ACTIVE = ['running', 'starting', 'backoff'];
const byId = (id) => store.agents.find((a) => a.id === id);
const envName = (a) => {
  if (a.kind === 'a2a' || a.kind === 'acp') return (a.kind || '').toUpperCase();
  return envForNode(a.node_id)?.name || store.nodes.find((n) => n.id === a.node_id)?.name || a.node_id || '—';
};
const isFed = (a) => a && (a.kind === 'a2a' || a.kind === 'acp');

export const agentActions = {
  async run(a, act) {
    if (isFed(a)) {
      if (act === 'stop') {
        if (!(await confirmDialog({ title: t('agent.stopTitle'), message: t('agent.stopMsg', { name: a.name }), okText: t('cmd.stop') }))) return;
        try { await api('POST', `/federation/agents/${enc(a.id)}/stop`); toast(t('agent.stopped')); } catch (e) { toast(`${a.name}: ${e.message}`, true); }
      } else if (act === 'start' || act === 'restart') {
        toast(t('fed.useChat'), false);
      }
      return;
    }
    if (act === 'stop' && !(await confirmDialog({ title: t('agent.stopTitle'), message: t('agent.stopMsg', { name: a.name }), okText: t('cmd.stop') }))) return;
    try { await api('POST', `/agents/${enc(a.id)}/${act}`); toast(t({ start: 'agent.started', stop: 'agent.stopped', restart: 'agent.restartedOk' }[act])); }
    catch (e) { toast(`${a.name}: ${e.message}`, true); }
  },
  async bulk(list, act) {
    if (!list.length) return;
    if (act === 'stop' && list.length > 1 && !(await confirmDialog({ title: t('agents.bulkStopTitle'), message: t('agents.bulkStopMsg', { n: list.length }), okText: t('cmd.stop') }))) return;
    if (list.length === 1) return this.run(list[0], act);
    let ok = 0, fail = 0;
    await Promise.all(list.map((a) => {
      const path = isFed(a)
        ? (act === 'stop' ? `/federation/agents/${enc(a.id)}/stop` : null)
        : `/agents/${enc(a.id)}/${act}`;
      if (!path) { fail++; return Promise.resolve(); }
      return api('POST', path).then(() => ok++, () => fail++);
    }));
    toast(t('agents.bulkDone', { ok, fail }), fail > 0);
  },
  async remove(list) {
    if (!list.length) return;
    const msg = list.length === 1 ? t('agent.deleteMsg', { name: list[0].name }) : t('agents.deleteMany', { n: list.length });
    if (!(await confirmDialog({ title: t('agent.deleteTitle'), message: msg, danger: true, okText: t('cmd.delete') }))) return;
    for (const a of list) {
      try {
        await api('DELETE', isFed(a) ? '/federation/agents/' + enc(a.id) : '/agents/' + enc(a.id));
        store.agents = store.agents.filter((x) => x.id !== a.id);
      } catch (e) { toast(`${a.name}: ${e.message}`, true); }
    }
    toast(t('agent.deleted'));
  },
  async invoke(a, message) {
    await api('POST', `/federation/agents/${enc(a.id)}/invoke`, { message, stream: true });
  },
};

export default {
  get title() { return t('nav.agents'); },
  async render(el, params) {
    let curId = params.id ? decodeURIComponent(params.id) : null, curTab = TABS.includes(params.tab) ? params.tab : 'overview';
    let q = '', stF = '', kindF = '';
    const search = h('input', { type: 'search', placeholder: t('agents.search'), 'aria-label': t('agents.search'), oninput: () => { q = search.value.toLowerCase(); fill(); } });
    const stSel = h('select', { class: 'inline', 'aria-label': t('agents.col.state'), onchange: () => { stF = stSel.value; fill(); } }, h('option', { value: '' }, t('agents.allStates')),
      ['running', 'stopped', 'failed', 'backoff', 'exited', 'unknown'].map((s) => h('option', { value: s }, t('state.' + s))));
    const kindSel = h('select', { class: 'inline', 'aria-label': t('agents.kind'), onchange: () => { kindF = kindSel.value; fill(); } },
      h('option', { value: '' }, t('agents.kindAll')),
      h('option', { value: 'local' }, t('agents.kindLocal')),
      h('option', { value: 'a2a' }, 'A2A'),
      h('option', { value: 'acp' }, 'ACP'));
    const c = {
      start: cmd({ icon: 'play', text: t('cmd.start'), id: 'start', onClick: () => agentActions.bulk(sel().filter((a) => !ACTIVE.includes(a.status?.state)), 'start') }),
      stop: cmd({ icon: 'stop', text: t('cmd.stop'), id: 'stop', onClick: () => agentActions.bulk(sel().filter((a) => ACTIVE.includes(a.status?.state)), 'stop') }),
      restart: cmd({ icon: 'restart', text: t('cmd.restart'), id: 'restart', onClick: () => agentActions.bulk(sel(), 'restart') }),
      del: cmd({ icon: 'trash', text: t('cmd.delete'), id: 'delete', onClick: () => agentActions.remove(sel()) }),
    };
    const pg = page({ title: t('nav.agents'), flush: true, commands: [
      cmd({ icon: 'plus', text: t('agents.new'), accent: true, id: 'new', onClick: () => newAgent() }),
      cmd({ icon: 'plug', text: t('fed.connect'), id: 'fed', onClick: () => connectFed() }),
      cmd({ icon: 'search', text: t('fed.scanLocal'), id: 'scanLocal', onClick: () => scanLocalAgents() }), 'sep',
      c.start, c.stop, c.restart, c.del, 'sep', cmd({ icon: 'refresh', text: t('cmd.refresh'), id: 'refresh', onClick: () => refresh() }),
    ] });
    const grid = dataGrid({
      multi: true,
      columns: [
        { title: t('agents.col.name'), cls: 'nm', render: (a) => h('span', { class: 'two' }, agentAvatar(a, 32), h('span', { class: 'tx' }, h('b', {}, a.name), h('small', {}, isFed(a) ? h('span', {}, h('span', { class: 'chip', style: 'margin-right:4px' }, (a.kind || '').toUpperCase()), envName(a)) : envName(a)))) },
        { title: t('agents.col.state'), width: '104px', render: (a) => agentState(a.status?.state || 'stopped') },
        { title: 'CPU', cls: 'num', width: '58px', render: (a) => (a.status?.state === 'running' && a.status?.stats ? fmtPct(a.status.stats.cpu_percent) : '—') },
        { title: t('agents.col.mem'), cls: 'num', width: '76px', render: (a) => (a.status?.state === 'running' && a.status?.stats ? fmtBytes(a.status.stats.mem_rss) : '—') },
      ],
      onSelect: (row) => { if (row && row.id !== curId) { curId = row.id; nav(); showDetail(); } syncCmds(); },
      onOpen: (row) => { curId = row.id; curTab = 'logs'; nav(); showDetail(); },
      onContext: (ev, row, ids) => rowMenu(ev, byId(row.id), ids),
      empty: () => store.agents.length ? placeholder('search', t('agents.noMatch'), '') : placeholder('agents', t('agents.empty'), t('agents.emptyHint'), h('button', { class: 'btn accent', onclick: () => newAgent() }, icon('plus'), t('agents.new'))),
    });
    const detail = h('div', { class: 'detail-in', style: 'display:flex;flex-direction:column;min-height:0;flex:1' });
    const tools = h('div', { class: 'list-tools' }, h('div', { class: 'search' }, icon('search'), search), kindSel, stSel);
    pg.body.append(masterDetail({ key: 'agents', list: [tools, grid.el], detail, width: 440 }).el); el.append(pg.el);
    const sel = () => [...grid.selected].map(byId).filter(Boolean);
    function syncCmds() {
      const s = sel();
      c.start.disabled = !s.some((a) => !ACTIVE.includes(a.status?.state) && a.status?.state !== 'unknown');
      c.stop.disabled = !s.some((a) => ACTIVE.includes(a.status?.state));
      c.restart.disabled = !s.some((a) => a.status?.state !== 'unknown'); c.del.disabled = !s.length;
    }
    function crumbs() { const a = byId(curId); pg.setCrumbs(a ? [{ text: a.name }, curTab !== 'overview' ? { text: t('tab.' + curTab) } : null] : []); }
    function nav() { crumbs(); const hs = `#/agents/${enc(curId)}` + (curTab !== 'overview' ? '/' + curTab : ''); if (location.hash !== hs) history.replaceState(null, '', hs); }
    function fill() {
      const rows = store.agents.filter((a) => {
        const k = a.kind || 'local';
        if (kindF && k !== kindF) return false;
        if (stF && (a.status?.state || 'stopped') !== stF) return false;
        if (q && !`${a.name} ${a.command || ''} ${envName(a)} ${a.description || ''} ${k}`.toLowerCase().includes(q)) return false;
        return true;
      }).sort((a, b) => a.name.localeCompare(b.name));
      grid.setRows(rows);
      if (!curId || !byId(curId)) curId = rows[0]?.id || null;
      if (curId && grid.primary !== curId) grid.select(curId, { silent: true });
      syncCmds();
    }
    async function newAgent(envId) { const a = await openAgentDialog({ envId }); if (a) { if (!byId(a.id)) store.agents.push(a); curId = a.id; curTab = 'logs'; nav(); fill(); showDetail(); } }
    async function connectFed() {
      const created = await openFedWizard();
      if (!created) return;
      const a = {
        id: created.id, name: created.name, description: created.description, kind: created.kind || created.protocol,
        command: '(' + (created.protocol || created.kind) + ')', status: created.status || { state: 'stopped' },
        endpoint: created.endpoint, capabilities: created.capabilities,
      };
      if (!byId(a.id)) store.agents.push(a);
      curId = a.id; curTab = 'overview'; nav(); fill(); showDetail();
    }
    async function scanLocalAgents() {
      const createdList = await openLocalScanDialog();
      if (!createdList || !createdList.length) return;
      for (const created of createdList) {
        const a = {
          id: created.id, name: created.name, description: created.description, kind: created.kind || created.protocol,
          command: '(' + (created.protocol || created.kind) + ')', status: created.status || { state: 'stopped' },
          endpoint: created.endpoint, capabilities: created.capabilities,
        };
        if (!byId(a.id)) store.agents.push(a);
        curId = a.id;
      }
      curTab = 'overview'; nav(); fill(); showDetail();
    }
    async function refresh() { try { await load(); toast(t('common.refreshed')); } catch (x) { toast(x.message, true); } }
    function rowMenu(ev, a, ids) {
      if (!a) return;
      const list = [...ids].map(byId).filter(Boolean), st = a.status?.state;
      contextMenu(ev, list.length > 1 ? [
        { text: t('cmd.start'), icon: 'play', onClick: () => agentActions.bulk(list.filter((x) => !ACTIVE.includes(x.status?.state)), 'start') },
        { text: t('cmd.stop'), icon: 'stop', onClick: () => agentActions.bulk(list.filter((x) => ACTIVE.includes(x.status?.state)), 'stop') },
        { text: t('cmd.restart'), icon: 'restart', onClick: () => agentActions.bulk(list, 'restart') }, '-',
        { text: t('cmd.delete'), icon: 'trash', danger: true, key: 'Del', onClick: () => agentActions.remove(list) },
      ] : [
        { text: t('cmd.start'), icon: 'play', disabled: ACTIVE.includes(st) || st === 'unknown', onClick: () => agentActions.run(a, 'start') },
        { text: t('cmd.stop'), icon: 'stop', disabled: !ACTIVE.includes(st), onClick: () => agentActions.run(a, 'stop') },
        { text: t('cmd.restart'), icon: 'restart', disabled: st === 'unknown', onClick: () => agentActions.run(a, 'restart') }, '-',
        { text: t('tab.logs'), icon: 'file', onClick: () => { curId = a.id; curTab = 'logs'; nav(); showDetail(); } },
        { text: t('common.edit'), icon: 'edit', onClick: () => openAgentDialog({ agent: a, mode: 'edit' }) },
        { text: t('common.duplicate'), icon: 'copy', onClick: async () => { const n = await openAgentDialog({ agent: a, mode: 'duplicate' }); if (n) { curId = n.id; nav(); fill(); showDetail(); } } },
        { text: t('agents.gotoEnv'), icon: 'env', disabled: !envForNode(a.node_id), onClick: () => { location.hash = '#/envs/' + enc(envForNode(a.node_id).id); } }, '-',
        { text: t('cmd.delete'), icon: 'trash', danger: true, key: 'Del', onClick: () => agentActions.remove([a]) },
      ]);
    }

    let shown = { id: null, tab: null }, tabCleanup = null, headEl = null, tabsCtl = null, tabBody = null;
    function teardown() { if (tabCleanup) { try { tabCleanup(); } catch (x) { console.warn(x); } tabCleanup = null; } }
    function showDetail(force) {
      const a = byId(curId);
      if (!a) { teardown(); shown = {}; headEl = null; detail.replaceChildren(store.ready ? placeholder('agents', t('agents.none'), t('agents.noneHint')) : h('div', { class: 'placeholder' }, h('span', { class: 'spinner' }))); return; }
      if (!force && shown.id === a.id && shown.tab === curTab) { refreshHead(a); return; }
      if (shown.id !== a.id || !headEl) {
        headEl = h('div', { class: 'detail-h' });
        const tabIds = isFed(a) ? ['overview', 'chat', 'logs'] : TABS;
        if (!tabIds.includes(curTab)) curTab = 'overview';
        const icons = { ...TAB_ICON, chat: 'chat' };
        tabsCtl = tabs(tabIds.map((id) => ({ id, text: t('tab.' + id), icon: icons[id] || 'info' })), curTab, (id) => { curTab = id; nav(); showDetail(); });
        tabBody = h('div', { class: 'tab-body' });
        detail.replaceChildren(headEl, tabsCtl.el, tabBody);
      } else tabsCtl.set(curTab);
      shown = { id: a.id, tab: curTab };
      refreshHead(a); teardown();
      tabBody.className = 'tab-body' + (curTab === 'logs' ? ' fill' : ''); tabBody.replaceChildren();
      const R = TAB_RENDER[curTab];
      try { const r = R(tabBody, a); tabCleanup = typeof r === 'function' ? r : r?.destroy ? () => r.destroy() : null; }
      catch (x) { console.warn(x); tabBody.replaceChildren(h('div', { class: 'alert err' }, icon('alert'), x.message)); }
    }
    function refreshHead(a) {
      const st = a.status?.state || 'stopped';
      const env = envForNode(a.node_id);
      crumbs();
      headEl.replaceChildren(h('div', { class: 'tile logo-tile' }, agentAvatar(a, 52)),
        h('div', { class: 'nm' }, h('h2', {}, h('span', { class: 't' }, a.name)), h('div', { class: 'sub' }, statePill(st),
          h('a', { class: 'row', style: 'gap:6px;flex-wrap:nowrap', href: env ? '#/envs/' + enc(env.id) : null }, env ? logo(env, 16) : icon('env'), envName(a)),
          a.status?.pid ? [h('span', { class: 'dot' }), h('span', { class: 'mono' }, 'PID ' + a.status.pid)] : null)),
        h('div', { class: 'acts' },
          isFed(a)
            ? [
              h('button', { class: 'btn accent', 'data-act': 'chat', onclick: () => { curTab = 'chat'; nav(); showDetail(true); } }, icon('chat'), t('fed.chat')),
              ACTIVE.includes(st) ? h('button', { class: 'btn', onclick: () => agentActions.run(a, 'stop') }, icon('stop'), t('cmd.stop')) : null,
            ]
            : [
              ACTIVE.includes(st)
                ? h('button', { class: 'btn', 'data-act': 'stop', onclick: () => agentActions.run(a, 'stop') }, icon('stop'), t('cmd.stop'))
                : h('button', { class: 'btn accent', 'data-act': 'start', disabled: st === 'unknown', onclick: () => agentActions.run(a, 'start') }, icon('play'), t('cmd.start')),
              h('button', { class: 'btn', 'data-act': 'restart', disabled: st === 'unknown', onclick: () => agentActions.run(a, 'restart') }, icon('restart'), t('cmd.restart')),
            ],
          h('button', { class: 'btn icon-only subtle', title: t('common.more'), 'aria-label': t('common.more'), onclick: (ev) => { const r = ev.currentTarget.getBoundingClientRect(); rowMenu({ clientX: r.left, clientY: r.bottom + 4 }, a, new Set([a.id])); } }, icon('more'))));
    }
    const unsub = subscribe(() => {
      fill();
      const a = byId(curId);
      if (!a || shown.id !== a.id) { showDetail(); return; }
      refreshHead(a);
      if (curTab === 'overview' || curTab === 'config') showDetail(true);
    });
    const unkeys = setKeys({ new: () => newAgent(), refresh: () => refresh(), delete: () => agentActions.remove(sel()), find: () => search.focus() });
    fill(); showDetail();
    if (params.mode === 'new') { history.replaceState(null, '', '#/agents'); newAgent(params.query?.get('env') || undefined); }
    this._update = (p) => {
      if (p.mode === 'new') { history.replaceState(null, '', '#/agents'); newAgent(p.query?.get('env') || undefined); return; }
      const id = p.id ? decodeURIComponent(p.id) : curId, tb = TABS.includes(p.tab) ? p.tab : 'overview';
      if (id !== curId || tb !== curTab) { curId = id; curTab = tb; fill(); showDetail(); }
    };
    return () => { unsub(); unkeys(); teardown(); this._update = null; };
  },
  update(p) { if (this._update) this._update(p); else throw new Error('stale'); },
};

const TAB_RENDER = {
  overview(box, a) {
    const s = a.status || {}, st = s.state || 'stopped';
    const kv = (k, v) => [h('div', {}, k), h('div', { class: 'selectable' }, v ?? '—')];
    const up = st === 'running' && s.started_at ? fmtDuration(since(s.started_at)) : '—';
    box.append(...[
      isFed(a) ? h('div', { class: 'alert info' }, icon('plug'), t('fed.chatHint', { proto: (a.kind || '').toUpperCase() })) : null,
      !isFed(a) && st === 'unknown' ? h('div', { class: 'alert warn' }, icon('alert'), t('agent.nodeOffline')) : null,
      s.error ? h('div', { class: 'alert err selectable' }, icon('alert'), s.error) : null,
      h('div', { class: 'metric-tiles' },
        h('div', { class: 'tile-m' }, h('div', { class: 'k' }, icon('pulse'), t('agents.col.state')), h('div', { class: 'v', style: 'font-size:16px;line-height:32px' }, statePill(st))),
        h('div', { class: 'tile-m' }, h('div', { class: 'k' }, icon('clock'), t('agent.uptime')), h('div', { class: 'v' }, up)),
        h('div', { class: 'tile-m' }, h('div', { class: 'k' }, icon('cpu'), 'CPU'), h('div', { class: 'v' }, st === 'running' && s.stats ? fmtPct(s.stats.cpu_percent) : '—'), h('div', { class: 's' }, t('perf.perCoreShort'))),
        h('div', { class: 'tile-m' }, h('div', { class: 'k' }, icon('mem'), t('perf.rss')), h('div', { class: 'v' }, st === 'running' && s.stats ? fmtBytes(s.stats.mem_rss) : '—'))),
      h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, icon('info'), t('agent.overview'))),
        h('div', { class: 'kv' },
          kv(t('agent.cmd'), h('code', { class: 'mono' }, shellJoin(a.command, a.args || []))),
          kv(t('agent.env'), envName(a)), kv(t('agent.pid'), s.pid || '—'),
          kv(t('agent.restartPolicy'), t('agent.restarted', { policy: t('restart.' + (a.restart || 'never')), n: s.restarts || 0 })),
          kv(t('agent.startedAt'), s.started_at ? fmtTime(s.started_at) : '—'),
          s.exit_code != null && st !== 'running' ? kv(t('agent.exitCode'), String(s.exit_code)) : null,
          s.exited_at && st !== 'running' ? kv(t('agent.exitedAt'), fmtTime(s.exited_at)) : null,
          kv(t('agent.workspace'), s.workspace || a.work_dir ? h('span', { class: 'mono' }, s.workspace || a.work_dir) : '—'),
          a.description ? kv(t('agent.desc'), a.description) : null, kv('ID', h('span', { class: 'mono' }, a.id))))].filter(Boolean));
  },
  logs(box, a) {
    if (isFed(a)) {
      const wrap = h('div', { class: 'log-box fill', style: 'overflow:auto;font-family:var(--mono);font-size:12px;padding:8px' });
      box.append(wrap);
      let stop = false;
      const tick = async () => {
        if (stop) return;
        try {
          const r = await api('GET', `/federation/agents/${enc(a.id)}/logs?tail=200`);
          wrap.replaceChildren(...(r.lines || []).map((l) => h('div', { class: 'ln' },
            h('span', { class: 'lvl' }, (l.stream || 'out').slice(0, 3).toUpperCase()), h('span', { class: 'tx' }, l.text))));
          wrap.scrollTop = wrap.scrollHeight;
        } catch {}
        if (!stop) setTimeout(tick, 1500);
      };
      tick();
      return () => { stop = true; };
    }
    const lv = new LogView(box, { agentId: a.id, name: a.name }); return () => lv.destroy();
  },
  perf(box, a) { return mountAgentMetrics(box, a); },
  chat(box, a) {
    if (!isFed(a)) { box.append(placeholder('agents', t('fed.localOnly'), '')); return; }
    const log = h('div', { class: 'log-box', style: 'flex:1;min-height:160px;overflow:auto;font-family:var(--mono);font-size:12px;padding:8px' });
    const input = h('textarea', { rows: 2, placeholder: t('fed.msgPh'), style: 'flex:1;resize:vertical' });
    const send = async () => {
      const msg = input.value.trim();
      if (!msg) return;
      input.value = '';
      log.append(h('div', { class: 'ln' }, h('span', { class: 'lvl' }, 'YOU'), h('span', { class: 'tx' }, msg)));
      try {
        await agentActions.invoke(a, msg);
        toast(t('fed.sent'));
        // poll logs a few times
        for (let i = 0; i < 20; i++) {
          await new Promise((r) => setTimeout(r, 400));
          const r = await api('GET', `/federation/agents/${enc(a.id)}/logs?tail=50`);
          log.replaceChildren(...(r.lines || []).map((l) => h('div', { class: 'ln lv-' + (l.stream === 'system' ? 'info' : 'info') },
            h('span', { class: 'lvl' }, (l.stream || 'out').slice(0, 3).toUpperCase()), h('span', { class: 'tx' }, l.text))));
          log.scrollTop = log.scrollHeight;
          if ((r.lines || []).some((l) => /done remote=/.test(l.text))) break;
        }
      } catch (e) { toast(e.message, true); }
    };
    box.append(
      h('div', { class: 'col', style: 'flex:1;min-height:0;gap:8px' },
        h('p', { class: 'muted small', style: 'margin:0' }, t('fed.chatHint', { proto: (a.kind || '').toUpperCase() })),
        log,
        h('div', { class: 'row', style: 'gap:8px;align-items:flex-end' }, input,
          h('button', { class: 'btn accent', onclick: send }, icon('send'), t('fed.send')))),
    );
    // initial logs
    api('GET', `/federation/agents/${enc(a.id)}/logs?tail=50`).then((r) => {
      log.replaceChildren(...(r.lines || []).map((l) => h('div', { class: 'ln' },
        h('span', { class: 'lvl' }, (l.stream || 'out').slice(0, 3).toUpperCase()), h('span', { class: 'tx' }, l.text))));
    }).catch(() => {});
  },
  config(box, a) {
    if (isFed(a)) {
      box.append(h('div', { class: 'card' }, h('div', { class: 'kv' },
        [h('div', {}, t('fed.protocol')), h('div', {}, (a.kind || '').toUpperCase())],
        [h('div', {}, t('fed.endpoint')), h('div', { class: 'mono selectable' }, a.endpoint || a.command || '—')],
        [h('div', {}, 'ID'), h('div', { class: 'mono' }, a.id)],
      )));
      return;
    }
    const kv = (k, v) => [h('div', {}, k), h('div', { class: 'selectable' }, v ?? '—')];
    const envs = Object.entries(a.env || {});
    const json = JSON.stringify({ ...a, status: undefined }, null, 2);
    box.append(h('div', { class: 'toolbar' }, h('span', { class: 'sp' }),
      h('button', { class: 'btn sm', onclick: () => copyText(json) }, icon('copy'), t('agent.copyJson')),
      h('button', { class: 'btn sm', onclick: () => openAgentDialog({ agent: a, mode: 'duplicate' }) }, icon('copy'), t('common.duplicate')),
      h('button', { class: 'btn sm accent', 'data-act': 'edit', onclick: () => openAgentDialog({ agent: a, mode: 'edit' }) }, icon('edit'), t('common.edit'))),
    h('div', { class: 'card' }, h('div', { class: 'kv' },
      kv(t('form.name'), a.name), kv(t('form.cmd'), h('code', { class: 'mono' }, a.command)),
      kv(t('form.args'), (a.args || []).length ? h('div', { class: 'mono', style: 'white-space:pre-wrap' }, (a.args || []).map((x, i) => `$${i + 1}  ${x}`).join('\n')) : '—'),
      kv(t('form.workdir'), a.work_dir || '—'), kv(t('form.restart'), t('restart.' + (a.restart || 'never'))),
      kv(t('form.maxRestarts'), String(a.max_restarts ?? 0)), kv(t('form.delay'), String(a.restart_delay_sec ?? 0)),
      kv(t('form.autoStart'), a.auto_start ? t('common.yes') : t('common.no')),
      kv(t('form.env'), envs.length ? h('div', { class: 'mono', style: 'white-space:pre-wrap' }, envs.map(([k, v]) => `${k}=${v}`).join('\n')) : '—'))),
    h('details', { class: 'card' }, h('summary', {}, 'JSON'), h('pre', { class: 'code selectable', style: 'margin:8px 0 0;max-height:320px;overflow:auto' }, json)));
  },
};
