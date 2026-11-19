// Central client-side state (owned by M6): environments, agents, nodes,
// providers and stacks, kept live via the /events stream. Pages subscribe()
// and re-render the parts they show.
import { api, on, isMissing, connected } from './api.js';

export const store = {
  envs: [], agents: [], nodes: [], providers: null, stacks: null,
  legacy: false,       // true when the server has no /envs API yet (derived from /nodes)
  ready: false, error: null, connected,
};
const subs = new Set();
let queued = false, pendingHint = null;
export function subscribe(fn) { subs.add(fn); return () => subs.delete(fn); }
// hint: undefined = full change; 'metrics' = host/agent stats only (pages can patch rows).
export function notify(hint) {
  if (hint === 'metrics' && pendingHint !== 'full') pendingHint = 'metrics';
  else pendingHint = 'full';
  if (queued) return; queued = true;
  requestAnimationFrame(() => {
    queued = false; const h = pendingHint; pendingHint = null;
    for (const fn of [...subs]) { try { fn(store, h); } catch (e) { setTimeout(() => { throw e; }); } }
  });
}

const loopback = (a) => /^(127\.|::1|\[::1\]|localhost)/.test(a || '');
export function nodeToEnv(n) {
  const local = loopback(n.remote_addr);
  const caps = ['agents', 'metrics', ...(n.capabilities || []).filter((c) => c.startsWith('desktop.'))];
  return {
    id: 'node:' + n.id, name: n.name, provider: local ? 'agentos-local' : 'agentos-remote', node_id: n.id, legacy: true,
    labels: local ? { builtin: 'true' } : {}, config: {}, created_at: n.created_at, updated_at: n.last_seen,
    state: n.status === 'online' ? 'running' : 'unknown',
    status: { state: n.status === 'online' ? 'running' : 'unknown', address: n.remote_addr || '', message: n.status === 'online' ? '' : 'offline' },
    capabilities: caps, provider_info: { type: local ? 'agentos-local' : 'agentos-remote', name: local ? '本机 Agent OS' : '远程 Agent OS', group: 'Agent OS', kind: 'agentos' },
    node: n,
  };
}

let loading = null;
export function load() {
  if (loading) return loading;
  loading = (async () => {
    const [nodes, agents] = await Promise.all([api('GET', '/nodes').catch(() => []), api('GET', '/agents').catch(() => [])]);
    store.nodes = nodes || []; store.agents = agents || [];
    try { store.envs = (await api('GET', '/envs')) || []; store.legacy = false; }
    catch (e) { if (!isMissing(e)) throw e; store.legacy = true; store.envs = store.nodes.map(nodeToEnv); }
    store.ready = true; store.error = null; notify();
  })().catch((e) => { store.error = e; notify(); throw e; }).finally(() => { loading = null; });
  return loading;
}

export async function providers() {
  if (store.providers) return store.providers;
  try { store.providers = (await api('GET', '/providers')) || []; }
  catch (e) { if (!isMissing(e)) throw e; store.providers = []; store.providersMissing = true; }
  return store.providers;
}
// Quick-create presets (GET /env-presets). Server caches provider probes ~60s.
let presetsAt = 0;
export async function presets(force) {
  if (store.presets && !force && Date.now() - presetsAt < 30000) return store.presets;
  try { store.presets = (await api('GET', '/env-presets')) || []; presetsAt = Date.now(); }
  catch (e) { if (!isMissing(e)) throw e; store.presets = []; store.presetsMissing = true; }
  return store.presets;
}
export async function stacks(force) {
  if (store.stacks && !force) return store.stacks;
  try { store.stacks = (await api('GET', '/stacks')) || []; }
  catch (e) { if (!isMissing(e)) throw e; store.stacks = []; store.stacksMissing = true; }
  return store.stacks;
}

export const envById = (id) => store.envs.find((e) => e.id === id);
export const envForNode = (nid) => (nid ? store.envs.find((e) => e.node_id === nid) : null);
export const agentsForEnv = (env) => (env?.node_id ? store.agents.filter((a) => a.node_id === env.node_id) : []);
export const hasCap = (env, c) => (env?.capabilities || []).includes(c);
export const envOnline = (env) => env?.state === 'running';

// ---------- live resource usage for list rows / overview ----------
// Agent OS envs get host stats pushed via node.stats; other running envs with the
// 'metrics' capability are polled (latest sample only) while a page holds startLiveMetrics().
const liveMetrics = new Map(); // env id -> {cpu_percent, mem_used, mem_total, disk_used, disk_total}
let liveRefs = 0, liveTimer = null;
export function hostOf(env) {
  if (!env) return null;
  const n = env.node_id ? store.nodes.find((x) => x.id === env.node_id) : null;
  return n?.host || env.node?.host || liveMetrics.get(env.id) || null;
}
async function pollLive() {
  const list = store.envs.filter((e) => e.state === 'running' && !e.legacy && hasCap(e, 'metrics') && !(e.node_id && store.nodes.find((n) => n.id === e.node_id)?.host)).slice(0, 16);
  let changed = false;
  await Promise.all(list.map(async (e) => {
    try {
      const r = await api('GET', `/envs/${encodeURIComponent(e.id)}/metrics?since=${encodeURIComponent(new Date(Date.now() - 20e3).toISOString())}`);
      if (r?.latest) { liveMetrics.set(e.id, r.latest); changed = true; }
    } catch (x) { if (isMissing(x)) liveMetrics.set(e.id, null); }
  }));
  if (changed) notify('metrics');
}
export function startLiveMetrics() {
  if (!liveRefs++) { setTimeout(pollLive, 700); liveTimer = setInterval(() => { if (!document.hidden) pollLive(); }, 5000); }
  return () => { if (!--liveRefs) { clearInterval(liveTimer); liveTimer = null; } };
}

// ---------- event wiring ----------
let wired = false;
export function wire() {
  if (wired) return; wired = true;
  const upsert = (list, item, key = 'id') => { const i = list.findIndex((x) => x[key] === item[key]); if (i >= 0) list[i] = { ...list[i], ...item }; else list.push(item); };
  // EnvView events carry the full view: replace (optional fields such as busy are omitted when cleared).
  const putEnv = (e) => { const i = store.envs.findIndex((x) => x.id === e.id); if (i >= 0) store.envs[i] = e; else store.envs.push(e); };
  on('env.created', (e) => { if (!store.legacy && e?.id) { putEnv(e); notify(); } });
  on('env.updated', (e) => { if (!store.legacy && e?.id) { putEnv(e); notify(); } });
  on('env.deleted', (x) => { store.envs = store.envs.filter((e) => e.id !== x.id); notify(); });
  on('node.status', (n) => {
    upsert(store.nodes, n);
    if (store.legacy) upsert(store.envs, nodeToEnv(n));
    for (const a of store.agents) if (a.node_id === n.id && n.status !== 'online') a.status = { ...a.status, state: 'unknown' };
    notify();
  });
  on('node.removed', (x) => { store.nodes = store.nodes.filter((n) => n.id !== x.id); if (store.legacy) store.envs = store.envs.filter((e) => e.node_id !== x.id); notify(); });
  on('agent.status', (s) => {
    const a = store.agents.find((x) => x.id === s.id);
    if (!a) { load().catch(() => {}); return; }
    const sameState = a.status?.state === s.state && a.status?.restarts === s.restarts;
    a.status = { ...s, stats: s.stats || a.status?.stats };
    notify(sameState ? 'metrics' : 'full');
  });
  on('agent.spec', (s) => { const a = store.agents.find((x) => x.id === s.id); if (a) Object.assign(a, s); else store.agents.push({ ...s, status: s.status || { id: s.id, node_id: s.node_id, state: 'stopped', restarts: 0 } }); notify(); });
  on('agent.removed', (x) => { store.agents = store.agents.filter((a) => a.id !== x.id); notify(); });
  let statsT = 0;
  on('node.stats', (ns) => {
    for (const a of store.agents) { const ps = ns.agents?.[a.id]; if (ps) a.status = { ...a.status, stats: ps }; }
    const n = store.nodes.find((x) => x.id === ns.node_id); if (n) n.host = ns.host;
    // Debounce sparkline/metrics UI updates (~250ms) so bursts of node.stats don't thrash the list.
    clearTimeout(statsT); statsT = setTimeout(() => notify('metrics'), 250);
  });
  on('reconnect', () => load().catch(() => {}));
  on('conn', (v) => { store.connected = v; notify(); });
}
