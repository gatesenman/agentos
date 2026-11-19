// Dialog: 探查本机已安装/注册的 A2A / ACP / CLI 智能体 → 一键注册.
import { h, icon, toast, dialog } from '../util.js';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { store } from '../store.js';

const SCAN_TIMEOUT_MS = 35000;

export function openLocalScanDialog() {
  const list = h('div', { class: 'col', style: 'gap:8px;min-height:120px' });
  const status = h('p', { class: 'muted small', style: 'margin:0' }, t('fed.scanLocalHint'));
  let hits = [];
  let meta = { ports_scanned: 0, listen_ports: 0, cli_found: 0 };
  const selected = new Set();
  let scanning = false;

  function protoLabel(p) {
    if (p === 'local') return 'LOCAL';
    return (p || '').toUpperCase() || '—';
  }

  function renderEmpty() {
    const n = meta.ports_scanned || 0;
    const cli = meta.cli_found || 0;
    list.replaceChildren(h('div', { class: 'placeholder', style: 'padding:24px;text-align:center;gap:12px' },
      icon('search'),
      h('p', {}, t('fed.scanLocalEmptyPorts', { n, cli })),
      h('p', { class: 'muted small', style: 'margin:0' }, t('fed.scanLocalInstallHostHint')),
      h('div', { class: 'row', style: 'gap:8px;justify-content:center;flex-wrap:wrap' },
        h('a', {
          class: 'btn accent sm', href: '#/templates',
          onclick: () => { try { d.close(null); } catch (_) {} },
        }, icon('install'), t('fed.scanLocalInstallHost')),
        h('button', {
          class: 'btn sm', type: 'button',
          onclick: () => { try { d.close(null); location.hash = '#/agents'; } catch (_) {} },
        }, icon('bot'), t('agents.new')),
      ),
    ));
  }

  function renderList() {
    if (!hits.length) {
      renderEmpty();
      return;
    }
    list.replaceChildren(...hits.map((hit, i) => {
      const id = String(i);
      const row = h('label', {
        class: 'row',
        style: 'gap:10px;align-items:flex-start;padding:10px 12px;border:1px solid var(--border);border-radius:8px;cursor:pointer',
      },
        h('input', {
          type: 'checkbox', checked: selected.has(id) || undefined,
          onchange: (ev) => { if (ev.target.checked) selected.add(id); else selected.delete(id); },
        }),
        h('span', { class: 'tx', style: 'flex:1;min-width:0' },
          h('span', { class: 'row', style: 'gap:8px;align-items:center;flex-wrap:wrap' },
            h('span', { class: 'chip' }, protoLabel(hit.protocol)),
            hit.source ? h('span', { class: 'chip muted' }, hit.source) : null,
            h('b', {}, hit.name || '—')),
          h('small', { class: 'muted', style: 'display:block;word-break:break-all' }, hit.endpoint || ''),
          hit.description ? h('small', { class: 'muted', style: 'display:block' }, hit.description) : null,
        ),
      );
      return row;
    }));
  }

  function defaultNodeId() {
    const local = store.nodes?.find((n) => n.status === 'online') || store.nodes?.[0];
    const env = store.envs?.find((e) => e.id === 'local' && e.node_id) || store.envs?.find((e) => e.node_id && e.state === 'running');
    return env?.node_id || local?.id || '';
  }

  function parseCliCard(hit) {
    try {
      if (!hit.card) return null;
      const c = typeof hit.card === 'string' ? JSON.parse(hit.card) : hit.card;
      return c;
    } catch (_) { return null; }
  }

  async function registerHit(hit) {
    if (hit.protocol === 'local' || (hit.endpoint || '').startsWith('local://')) {
      if ((hit.source || '') === 'supervised' || (hit.endpoint || '').includes('/supervised/')) {
        return { skipped: true, hit };
      }
      const card = parseCliCard(hit) || {};
      const nodeId = defaultNodeId();
      if (!nodeId) throw new Error(t('fed.scanLocalNoNode'));
      const cmd = card.path || (card.command && card.command[0]) || String(hit.endpoint || '').replace(/^local:\/\//, '');
      const args = Array.isArray(card.command) && card.command.length > 1 ? card.command.slice(1) : [];
      return api('POST', '/agents', {
        name: hit.name || cmd,
        description: hit.description || '',
        node_id: nodeId,
        command: cmd,
        args,
        restart: 'never',
        auto_start: false,
        metadata: { source: 'local-scan', scan_source: hit.source || 'cli', stack_id: card.stack_id || '' },
      });
    }
    return api('POST', '/federation/agents', {
      endpoint: hit.endpoint,
      protocol: hit.protocol,
      name: hit.name,
      auth_scheme: 'none',
      fetch_card: true,
      metadata: { source: 'local-scan', scan_source: hit.source || 'port' },
    });
  }

  async function runScan() {
    if (scanning) return;
    scanning = true;
    status.textContent = t('fed.scanLocalRunning');
    list.replaceChildren(
      h('div', { class: 'row', style: 'gap:8px;align-items:center;padding:16px' },
        h('span', { class: 'spinner sm' }),
        h('p', { class: 'muted', style: 'margin:0' }, t('fed.scanLocalRunning')),
      ),
    );
    selected.clear();
    const ac = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const timer = ac ? setTimeout(() => ac.abort(), SCAN_TIMEOUT_MS) : null;
    try {
      const r = await api('POST', '/federation/agents/scan-local', {}, ac ? { signal: ac.signal } : undefined);
      hits = Array.isArray(r?.agents) ? r.agents : [];
      meta = {
        ports_scanned: r?.ports_scanned || 0,
        listen_ports: r?.listen_ports || 0,
        cli_found: r?.cli_found || 0,
      };
      status.textContent = t('fed.scanLocalDoneDetail', {
        n: hits.length,
        ports: meta.ports_scanned,
        cli: meta.cli_found,
      });
      hits.forEach((_, i) => selected.add(String(i)));
      renderList();
    } catch (e) {
      const aborted = e && (e.name === 'AbortError' || /abort/i.test(String(e.message || '')));
      const msg = aborted ? t('fed.scanLocalTimeout') : (e.message || String(e));
      status.textContent = msg;
      // Never leave the dialog stuck on「正在探查」— show empty/error with install hint.
      meta = { ports_scanned: meta.ports_scanned || 0, listen_ports: 0, cli_found: 0 };
      hits = [];
      list.replaceChildren(h('div', { class: 'col', style: 'gap:12px' },
        h('div', { class: 'alert err' }, icon('alert'), msg),
        h('div', { class: 'placeholder', style: 'padding:16px;text-align:center;gap:12px' },
          h('p', { class: 'muted small', style: 'margin:0' }, t('fed.scanLocalInstallHostHint')),
          h('div', { class: 'row', style: 'gap:8px;justify-content:center;flex-wrap:wrap' },
            h('a', {
              class: 'btn accent sm', href: '#/templates',
              onclick: () => { try { d.close(null); } catch (_) {} },
            }, icon('install'), t('fed.scanLocalInstallHost')),
          ),
        ),
      ));
      toast(msg, true);
    } finally {
      if (timer) clearTimeout(timer);
      scanning = false;
    }
  }

  const body = h('div', { class: 'col', style: 'gap:12px' }, status, list);
  const d = dialog({
    title: t('fed.scanLocal'),
    sub: 'A2A · ACP · CLI',
    body,
    wide: true,
    buttons: [
      { text: t('common.cancel'), id: 'cancel', onClick: (close) => close(null) },
      { text: t('fed.scanLocalAgain'), icon: 'refresh', id: 'rescan', onClick: async () => { await runScan(); return false; } },
      'sp',
      { text: t('fed.scanLocalRegister'), icon: 'check', accent: true, id: 'reg', onClick: async (close) => {
        const picks = [...selected].map((i) => hits[+i]).filter(Boolean);
        if (!picks.length) { toast(t('fed.scanLocalPick'), true); return false; }
        const created = [];
        for (const hit of picks) {
          try {
            const row = await registerHit(hit);
            if (row && !row.skipped) created.push(row);
          } catch (e) {
            toast(`${hit.name}: ${e.message}`, true);
          }
        }
        if (created.length) toast(t('fed.scanLocalRegistered', { n: created.length }));
        close(created);
      } },
    ],
  });
  runScan();
  return d.done;
}
