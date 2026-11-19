// Performance tabs (owned by M6): environment metrics (/envs/{id}/metrics, or
// /nodes/{id}/stats for node-derived envs) and agent metrics (/agents/{id}/stats).
import { h, fmtBytes, fmtPct } from '../util.js';
import { api, isMissing } from '../api.js';
import { t } from '../i18n.js';
import { LineChart } from '../chart.js';
import { placeholder } from './ui.js';

const POLL = 8000;
const tile = (k) => { const v = h('div', { class: 'v' }, '-'), s = h('div', { class: 's' }, ''), bar = h('i', { style: 'width:0' });
  return { el: h('div', { class: 'tile-m' }, h('div', { class: 'k' }, k), v, s, h('div', { class: 'bar' }, bar)), set(val, sub, pct) { v.textContent = val; s.textContent = sub || ''; bar.style.width = Math.max(0, Math.min(100, pct || 0)) + '%'; } }; };
const card = (title, legend) => h('div', { class: 'card' }, h('div', { class: 'card-h' }, h('h3', {}, title), legend || null));
const ts = (x) => Date.parse(x.t || x.time);

function poller(fn) {
  let tm = null, dead = false;
  const loop = async () => { if (dead) return; try { await fn(); } catch (e) { if (e?.stop) return; } if (!dead) tm = setTimeout(loop, POLL); };
  loop();
  return () => { dead = true; clearTimeout(tm); };
}

export function mountEnvMetrics(container, env) {
  const cpu = tile(t('perf.cpu')), mem = tile(t('perf.mem')), disk = tile(t('perf.disk')), load = tile(t('perf.load'));
  const tiles = h('div', { class: 'metric-tiles' }, cpu.el, mem.el, disk.el, load.el);
  const c1 = card(t('perf.cpuChart')), c2 = card(t('perf.memChart'));
  const body = h('div', { class: 'perf' }, tiles, h('div', { class: 'cols' }, c1, c2));
  container.append(body);
  const cpuC = new LineChart(c1, { series: [{ label: 'CPU', color: '--chart-cpu' }], max: 100, yFmt: (v) => Math.round(v) + '%', height: 200, emptyText: t('perf.noData') });
  const memC = new LineChart(c2, { series: [{ label: t('perf.mem'), color: '--chart-mem', fmt: (v) => fmtBytes(v) }], yFmt: (v) => fmtBytes(v), minMax: 2 ** 30, height: 200, emptyText: t('perf.noData') });
  const times = [], cpus = [], mems = [];
  let since = new Date(Date.now() - 30 * 60e3).toISOString();
  const apply = (latest, samples) => {
    for (const s of samples) { const tm = ts(s); if (times.length && tm <= times[times.length - 1]) continue; times.push(tm); cpus.push(s.cpu_percent || 0); mems.push(s.mem_used || 0); }
    while (times.length > 360) { times.shift(); cpus.shift(); mems.shift(); }
    if (!apply._raf) apply._raf = requestAnimationFrame(() => { apply._raf = 0; cpuC.setData([...times], [[...cpus]]); memC.setData([...times], [[...mems]]); });
    const l = latest || samples[samples.length - 1];
    if (l) {
      cpu.set(fmtPct(l.cpu_percent), l.cpu_count ? t('perf.cores', { n: l.cpu_count }) : '', l.cpu_percent);
      mem.set(l.mem_total ? fmtPct((l.mem_used / l.mem_total) * 100) : fmtBytes(l.mem_used), `${fmtBytes(l.mem_used)} / ${fmtBytes(l.mem_total)}`, l.mem_total ? (l.mem_used / l.mem_total) * 100 : 0);
      disk.set(l.disk_total ? fmtPct((l.disk_used / l.disk_total) * 100) : '-', l.disk_total ? `${fmtBytes(l.disk_used)} / ${fmtBytes(l.disk_total)}` : '', l.disk_total ? (l.disk_used / l.disk_total) * 100 : 0);
      load.set(l.load1 != null ? l.load1.toFixed(2) : '-', l.load5 != null ? `5m ${l.load5.toFixed(2)} · 15m ${(l.load15 ?? 0).toFixed(2)}` : '', 0);
    }
    const last = samples[samples.length - 1]; if (last) since = last.t || last.time;
  };
  const stop = poller(async () => {
    try {
      if (env.legacy && env.node_id) {
        const r = await api('GET', `/nodes/${encodeURIComponent(env.node_id)}/stats?since=${encodeURIComponent(since)}`);
        apply(r?.latest, r?.history || []);
      } else {
        const r = await api('GET', `/envs/${encodeURIComponent(env.id)}/metrics?since=${encodeURIComponent(since)}`);
        apply(r?.latest, r?.samples || []);
      }
    } catch (e) {
      if (isMissing(e)) { body.replaceChildren(placeholder('📈', t('perf.unsupported'), t('common.pendingApi'))); throw { stop: true }; }
      throw e;
    }
  });
  return { destroy() { stop(); cpuC.destroy(); memC.destroy(); } };
}

export function mountAgentMetrics(container, agent) {
  const cpu = tile(t('perf.cpuAgent')), mem = tile(t('perf.rss')), procs = tile(t('perf.procs')), rs = tile(t('agent.restartsK'));
  const c1 = card(t('perf.cpuChart'), h('span', { class: 'small muted' }, t('perf.perCore'))), c2 = card(t('perf.memChart'));
  container.append(h('div', { class: 'perf' }, h('div', { class: 'metric-tiles' }, cpu.el, mem.el, procs.el, rs.el), h('div', { class: 'cols' }, c1, c2)));
  const cpuC = new LineChart(c1, { series: [{ label: 'CPU', color: '--chart-cpu' }], minMax: 100, yFmt: (v) => Math.round(v) + '%', height: 200, emptyText: t('perf.noData') });
  const memC = new LineChart(c2, { series: [{ label: 'RSS', color: '--chart-mem', fmt: (v) => fmtBytes(v) }], yFmt: (v) => fmtBytes(v), minMax: 64 * 2 ** 20, height: 200, emptyText: t('perf.noData') });
  const times = [], cpus = [], mems = [];
  let since = new Date(Date.now() - 30 * 60e3).toISOString();
  const stop = poller(async () => {
    const r = await api('GET', `/agents/${encodeURIComponent(agent.id)}/stats?since=${encodeURIComponent(since)}`);
    for (const s of r?.history || []) { const tm = ts(s); if (times.length && tm <= times[times.length - 1]) continue; times.push(tm); cpus.push(s.cpu_percent || 0); mems.push(s.mem_rss || 0); since = s.time || s.t; }
    while (times.length > 360) { times.shift(); cpus.shift(); mems.shift(); }
    if (!mountAgentMetrics._raf) mountAgentMetrics._raf = requestAnimationFrame(() => { mountAgentMetrics._raf = 0; cpuC.setData([...times], [[...cpus]]); memC.setData([...times], [[...mems]]); });
    const l = r?.latest || agent.status?.stats;
    cpu.set(l ? fmtPct(l.cpu_percent) : '-', t('perf.perCoreShort'), l ? Math.min(100, l.cpu_percent) : 0);
    mem.set(l ? fmtBytes(l.mem_rss) : '-', '', 0);
    procs.set(l?.procs != null ? String(l.procs) : '-', '', 0);
    rs.set(String(agent.status?.restarts ?? 0), '', 0);
  });
  return { destroy() { stop(); cpuC.destroy(); memC.destroy(); } };
}
