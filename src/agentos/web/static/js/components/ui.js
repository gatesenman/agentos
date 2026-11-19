// Fluent desktop component kit (owned by M6). Documented in ./README.md.
import { h, icon, contextMenu } from '../util.js';
import { t, stateClass } from '../i18n.js';
import { illustration } from './logos.js';

// ---------- page + command bar ----------
// page({title, crumbs, commands:[cmd()|'sep'|'sp'|Node], body, flush}) -> {el, bar, body, setCrumbs([{text, href}])}
// The header is a Fluent breadcrumb (title › item › item); the last crumb is the current location.
export function page({ title, crumbs = [], commands = [], body, flush = false }) {
  const h1 = h('h1', { class: 'bc' });
  const setCrumbs = (list = []) => {
    const all = [{ text: title, href: list.length ? null : undefined, root: true }, ...list.filter((c) => c && c.text)];
    h1.replaceChildren(...all.flatMap((c, i) => {
      const last = i === all.length - 1;
      const node = last ? h('span', { class: 'bc-i cur', title: c.text }, c.text)
        : h('a', { class: 'bc-i', href: c.href || null, title: c.text, onclick: c.onClick ? (e) => { e.preventDefault(); c.onClick(); } : null }, c.text);
      return i ? [h('span', { class: 'bc-sep', 'aria-hidden': 'true' }, icon('right')), node] : [node];
    }));
  };
  setCrumbs(crumbs);
  const bar = h('div', { class: 'cmdbar', role: 'toolbar', 'aria-label': title }, h1, h('div', { class: 'cmds' }, commands.map((c) => c === 'sep' ? h('span', { class: 'sep' }) : c === 'sp' ? h('span', { class: 'sp' }) : c)));
  const b = h('div', { class: 'page-body' + (flush ? ' flush' : '') }, body);
  return { el: h('div', { class: 'page' }, bar, b), bar, body: b, setCrumbs };
}
// cmd({icon, text, onClick, disabled, accent, title, id}) -> <button class="cmd">
export function cmd({ icon: ic, text, onClick, disabled, accent, title, id, hideText }) {
  return h('button', { type: 'button', class: 'cmd' + (accent ? ' accent' : ''), disabled, title: title || text, 'aria-label': text, 'data-cmd': id || null, onclick: onClick },
    ic ? icon(ic) : null, hideText ? null : h('span', {}, text));
}

// ---------- master / detail ----------
// masterDetail({key, list, detail, width}) -> {el, list, detail}
export function masterDetail({ key = 'md', list, detail, width = 420 }) {
  const lk = 'agentos.md.' + key;
  let w = +(localStorage.getItem(lk) || width);
  const L = h('div', { class: 'list' }, list); const D = h('div', { class: 'detail' }, detail);
  L.style.setProperty('--list-w', w + 'px');
  const sp = h('div', { class: 'splitter', role: 'separator', 'aria-orientation': 'vertical', title: '', onpointerdown: (e) => {
    e.preventDefault(); sp.setPointerCapture(e.pointerId); sp.classList.add('drag');
    const x0 = e.clientX, w0 = L.getBoundingClientRect().width;
    const mv = (ev) => { w = Math.max(260, Math.min(innerWidth * 0.7, w0 + ev.clientX - x0)); L.style.setProperty('--list-w', w + 'px'); };
    const up = () => { sp.classList.remove('drag'); sp.removeEventListener('pointermove', mv); sp.removeEventListener('pointerup', up); localStorage.setItem(lk, Math.round(w)); dispatchEvent(new Event('resize')); };
    sp.addEventListener('pointermove', mv); sp.addEventListener('pointerup', up);
  } });
  return { el: h('div', { class: 'md' }, L, sp, D), list: L, detail: D };
}

// ---------- tabs ----------
// tabs([{id, text, icon, disabled, tip}], active, onChange) -> {el, set(id)}
export function tabs(defs, active, onChange) {
  const el = h('div', { class: 'tabs', role: 'tablist' });
  const draw = () => el.replaceChildren(...defs.map((d) => h('button', { type: 'button', role: 'tab', class: 'tab' + (d.id === active ? ' on' : ''), 'aria-selected': d.id === active ? 'true' : 'false',
    'data-tab': d.id, disabled: d.disabled, title: d.disabled ? (d.tip || '') : '', onclick: () => { if (d.id !== active) { active = d.id; draw(); onChange(d.id); } } }, d.icon ? icon(d.icon) : null, d.text)));
  draw();
  return { el, set(id) { active = id; draw(); } };
}

// ---------- state indicator ----------
const ENV_STATE_CLS = { running: 'ok', creating: 'info spin', stopped: '', error: 'err', unknown: 'warn' };
export function envState(s, busy) { return busy ? h('span', { class: 'st info spin', title: t('envstate.' + s) }, h('i'), t('busy.' + busy)) : h('span', { class: 'st ' + (ENV_STATE_CLS[s] ?? '') }, h('i'), t('envstate.' + s)); }
export function agentState(s) { return h('span', { class: 'st ' + (stateClass[s] === 'ok' ? 'ok' : stateClass[s] === 'err' ? 'err' : stateClass[s] === 'warn' ? 'warn' : stateClass[s] === 'info' ? 'info spin' : '') }, h('i'), t('state.' + s)); }

const EMOJI_ILL = { '🖥️': 'envs', '🤖': 'agents', '🔍': 'search', '✅': 'done' };
// placeholder(emojiOrIllustration, title, text, action) — Fluent empty state. Known keys
// ('envs','agents','desktop','search','done' or their emoji) render an inline SVG illustration.
export function placeholder(emoji, title, text, action) {
  const k = EMOJI_ILL[emoji] || (['envs', 'agents', 'desktop', 'search', 'done'].includes(emoji) ? emoji : null);
  return h('div', { class: 'placeholder' }, k ? illustration(k) : h('div', { class: 'big' }, emoji), h('h3', {}, title), text ? h('p', {}, text) : null, action ? h('div', { class: 'acts' }, action) : null);
}

// ---------- data grid ----------
// dataGrid({columns:[{key,title,cls,render(row)}], rowKey, groupBy(row), groupOrder:[], groupLabel(g), multi,
//           onSelect(primaryRow|null, selectedIds:Set), onOpen(row), onContext(event,row,selectedIds), empty:()=>Node})
export function dataGrid(o) {
  const key = o.rowKey || 'id';
  let rows = [], sel = new Set(), primary = null, anchor = null; const closed = new Set();
  const tbody = h('tbody');
  const table = h('table', { class: 'grid', role: 'grid', 'aria-multiselectable': o.multi ? 'true' : 'false' },
    h('thead', {}, h('tr', {}, o.columns.map((c) => h('th', { class: c.cls || '', style: c.width ? `width:${c.width}` : null }, c.title || '')))), tbody);
  const el = h('div', { class: 'list-scroll', tabindex: '0', onkeydown: (e) => onKey(e) }, table);
  const visibleRows = () => rows.filter((r) => !o.groupBy || !closed.has(o.groupBy(r)));
  const fire = () => o.onSelect?.(rows.find((r) => r[key] === primary) || null, new Set(sel));
  function select(id, { add = false, range = false, silent = false } = {}) {
    if (range && anchor != null) {
      const vr = visibleRows(), a = vr.findIndex((r) => r[key] === anchor), b = vr.findIndex((r) => r[key] === id);
      if (a >= 0 && b >= 0) { if (!add) sel.clear(); vr.slice(Math.min(a, b), Math.max(a, b) + 1).forEach((r) => sel.add(r[key])); }
    } else if (add && o.multi) { sel.has(id) ? sel.delete(id) : sel.add(id); anchor = id; }
    else { sel = new Set(id == null ? [] : [id]); anchor = id; }
    primary = sel.has(id) ? id : [...sel].pop() ?? null;
    paintSel(); if (!silent) fire();
    tbody.querySelector(`tr[data-key="${CSS.escape(String(primary))}"]`)?.scrollIntoView({ block: 'nearest' });
  }
  function paintSel() { tbody.querySelectorAll('tr[data-key]').forEach((tr) => { const on = sel.has(tr.dataset.key); tr.classList.toggle('sel', on); tr.setAttribute('aria-selected', on ? 'true' : 'false'); }); }
  function onKey(e) {
    if (e.target !== el) return;
    const vr = visibleRows(); if (!vr.length) return;
    const i = vr.findIndex((r) => r[key] === primary);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); const n = vr[Math.max(0, Math.min(vr.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))]; select(n[key], { range: e.shiftKey }); }
    else if (e.key === 'Home' || e.key === 'End') { e.preventDefault(); select(vr[e.key === 'Home' ? 0 : vr.length - 1][key]); }
    else if (e.key === 'Enter' && i >= 0) { e.preventDefault(); o.onOpen?.(vr[i]); }
    else if ((e.key === 'a' || e.key === 'A') && (e.ctrlKey || e.metaKey) && o.multi) { e.preventDefault(); vr.forEach((r) => sel.add(r[key])); paintSel(); fire(); }
    else if (e.key === 'ContextMenu' && i >= 0) { const tr = tbody.querySelector('tr.sel'); const r = tr?.getBoundingClientRect(); if (r) o.onContext?.({ clientX: r.left + 40, clientY: r.bottom, preventDefault() {} }, vr[i], new Set(sel)); }
  }
  function render() {
    const ids = new Set(rows.map((r) => r[key])); for (const s of [...sel]) if (!ids.has(s)) sel.delete(s);
    if (primary != null && !ids.has(primary)) primary = null;
    if (!rows.length) { tbody.replaceChildren(h('tr', {}, h('td', { colspan: o.columns.length, style: 'border:0;max-width:none;white-space:normal' }, o.empty ? o.empty() : ''))); return; }
    const out = [];
    const rowEl = (r) => h('tr', { 'data-key': r[key], class: sel.has(String(r[key])) ? 'sel' : '', role: 'row',
      onmousedown: (e) => { if (e.button === 0) { el.focus({ preventScroll: true }); select(r[key], { add: e.ctrlKey || e.metaKey, range: e.shiftKey }); } },
      ondblclick: () => o.onOpen?.(r),
      oncontextmenu: (e) => { e.preventDefault(); if (!sel.has(r[key])) select(r[key]); o.onContext?.(e, r, new Set(sel)); } },
    o.columns.map((c) => { const v = c.render ? c.render(r) : r[c.key]; return h('td', { class: c.cls || '', title: typeof v === 'string' ? v : null }, v); }));
    if (o.groupBy) {
      const groups = new Map();
      for (const r of rows) { const g = o.groupBy(r); if (!groups.has(g)) groups.set(g, []); groups.get(g).push(r); }
      const order = [...groups.keys()].sort((a, b) => { const ia = (o.groupOrder || []).indexOf(a), ib = (o.groupOrder || []).indexOf(b); return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a.localeCompare(b); });
      for (const g of order) {
        out.push(h('tr', { class: 'group-h' + (closed.has(g) ? ' closed' : ''), onclick: () => { closed.has(g) ? closed.delete(g) : closed.add(g); render(); } },
          h('td', { colspan: o.columns.length }, icon('chevron'), o.groupLabel ? o.groupLabel(g) : g, h('span', { class: 'cnt' }, groups.get(g).length))));
        if (!closed.has(g)) out.push(...groups.get(g).map(rowEl));
      }
    } else out.push(...rows.map(rowEl));
    tbody.replaceChildren(...out);
  }
  // Patch a single row's cells in place (avoids full list re-render on live metrics).
  function patchRow(id, row) {
    const tr = tbody.querySelector(`tr[data-key="${CSS.escape(String(id))}"]`);
    if (!tr) return false;
    const i = rows.findIndex((r) => r[key] === String(id));
    if (i >= 0) rows[i] = { ...row, [key]: String(id) };
    const cells = tr.querySelectorAll('td');
    o.columns.forEach((c, ci) => {
      const td = cells[ci]; if (!td) return;
      const v = c.render ? c.render(rows[i] || row) : (rows[i] || row)[c.key];
      td.replaceChildren(v == null ? '' : v);
      if (typeof v === 'string') td.title = v;
    });
    return true;
  }
  // Structural update: full render only when ids / order / groups change; otherwise patch cells.
  function setRows(r, { patch = true } = {}) {
    const next = r.map((x) => ({ ...x, [key]: String(x[key]) }));
    const sameStruct = patch && rows.length === next.length && rows.every((x, i) => x[key] === next[i][key]
      && (!o.groupBy || o.groupBy(x) === o.groupBy(next[i])));
    if (sameStruct) {
      for (let i = 0; i < next.length; i++) {
        if (rows[i] === next[i]) continue;
        rows[i] = next[i];
        patchRow(next[i][key], next[i]);
      }
      return;
    }
    rows = next; render();
  }
  return {
    el,
    setRows,
    patchRow,
    select(id, opts) { if (id == null) { sel.clear(); primary = null; paintSel(); if (!opts?.silent) fire(); return; } select(String(id), opts); },
    get selected() { return new Set(sel); }, get primary() { return primary; },
    focus() { el.focus({ preventScroll: true }); },
  };
}

// ---------- JSON-schema form ----------
// schemaForm(schema, values, {secretsSaved}) -> {el, value(), validate()}
export function schemaForm(schema, values = {}, { secretsSaved = false } = {}) {
  const props = schema?.properties || {}, req = new Set(schema?.required || []);
  const fields = [];
  const el = h('div', { class: 'form' });
  // fields marked "x-advanced" go into a collapsed "高级选项" section
  const advBody = h('div', { class: 'form' });
  const adv = h('details', { class: 'adv full', 'data-adv': '' }, h('summary', {}, icon('sliders'), t('schema.advanced')), advBody);
  const saved = (k) => (secretsSaved instanceof Set ? secretsSaved.has(k) : Array.isArray(secretsSaved) ? secretsSaved.includes(k) : !!secretsSaved);
  for (const [k, p] of Object.entries(props)) {
    const v = values?.[k] ?? p.default;
    if (p.readOnly) { // server-written fields (e.g. os_detected): hidden on create, shown read-only on edit
      if (values?.[k] != null && values[k] !== '') el.append(h('div', { class: 'field' }, h('label', {}, p.title || k), h('input', { type: 'text', value: String(values[k]), disabled: true, readonly: true })));
      continue;
    }
    const typ = Array.isArray(p.type) ? p.type[0] : p.type || (p.enum ? 'string' : 'string');
    let input, get;
    if (p.enum) {
      input = h('select', {}, (req.has(k) ? [] : [h('option', { value: '' }, '—')]).concat(p.enum.map((e, i) => h('option', { value: String(e), selected: String(e) === String(v ?? '') }, p['x-enum-titles']?.[i] || String(e)))));
      get = () => (input.value === '' ? undefined : typ === 'integer' || typ === 'number' ? +input.value : input.value);
    } else if (typ === 'boolean') {
      const cb = h('input', { type: 'checkbox', checked: !!v });
      input = h('label', { class: 'switch' }, cb, h('span', { class: 'tr' }), h('span', { class: 'small' }, p.description || ''));
      get = () => cb.checked;
    } else if (typ === 'integer' || typ === 'number') {
      input = h('input', { type: 'number', value: v ?? '', step: typ === 'integer' ? 1 : 'any', min: p.minimum, max: p.maximum });
      get = () => (input.value === '' ? undefined : +input.value);
    } else if (typ === 'array') {
      input = h('textarea', { rows: 3, placeholder: t('schema.arrayHint') }); input.value = (v || []).join('\n');
      get = () => { const a = input.value.split('\n').map((s) => s.trim()).filter(Boolean); const it = p.items?.type; return it === 'integer' || it === 'number' ? a.map(Number) : a; };
    } else if (typ === 'object') {
      input = h('textarea', { rows: 4, class: 'mono', placeholder: '{ "KEY": "value" }' }); input.value = v && Object.keys(v).length ? JSON.stringify(v, null, 2) : '';
      get = () => (input.value.trim() ? JSON.parse(input.value) : undefined);
    } else if (p['x-secret']) {
      input = p.format === 'textarea' ? h('textarea', { rows: 4, class: 'mono', placeholder: saved(k) ? t('schema.secretKeep') : '' }) : h('input', { type: 'password', autocomplete: 'new-password', placeholder: saved(k) ? t('schema.secretKeep') : '' });
      get = () => (input.value === '' ? undefined : input.value);
    } else {
      input = p.format === 'textarea' ? h('textarea', { rows: 4 }) : h('input', { type: 'text', placeholder: p.examples?.[0] || '' });
      input.value = v ?? '';
      get = () => (input.value.trim() === '' ? undefined : input.value);
    }
    const err = h('div', { class: 'err-msg' });
    const f = h('div', { class: 'field' + (typ === 'array' || typ === 'object' || p.format === 'textarea' || typ === 'boolean' ? ' full' : '') },
      h('label', {}, p.title || k, req.has(k) ? h('span', { class: 'req' }, ' *') : null), input,
      p.description && typ !== 'boolean' ? h('div', { class: 'hint' }, p.description) : null, err);
    f.dataset.key = k;
    fields.push({ k, p, typ, f, err, input, get });
    (p['x-advanced'] ? advBody : el).append(f);
  }
  if (advBody.childElementCount) el.append(adv);
  if (!fields.length) el.append(h('div', { class: 'muted small full' }, t('schema.noFields')));
  return {
    el,
    value() { const o = {}; for (const x of fields) { try { const v = x.get(); if (v !== undefined) o[x.k] = v; } catch { /* validated */ } } return o; },
    validate() {
      let ok = true;
      for (const x of fields) {
        let msg = '';
        try {
          const v = x.get();
          if (req.has(x.k) && (v === undefined || v === '' || (Array.isArray(v) && !v.length)) && !(x.p['x-secret'] && saved(x.k))) msg = t('schema.required');
          else if (typeof v === 'number' && (isNaN(v) || (x.p.minimum != null && v < x.p.minimum) || (x.p.maximum != null && v > x.p.maximum) || (x.typ === 'integer' && !Number.isInteger(v)))) msg = t('schema.badNumber', { min: x.p.minimum ?? '-∞', max: x.p.maximum ?? '∞' });
          else if (typeof v === 'string' && x.p.pattern && !new RegExp(x.p.pattern).test(v)) msg = t('schema.badPattern');
        } catch { msg = t('schema.badJSON'); }
        x.f.classList.toggle('has-err', !!msg); x.err.textContent = msg; if (msg && x.p['x-advanced']) adv.open = true;
        const inp = x.input.matches?.('input,select,textarea') ? x.input : null; inp?.classList.toggle('invalid', !!msg);
        if (msg) ok = false;
      }
      return ok;
    },
  };
}

// ---------- page-level keyboard shortcuts ----------
// setKeys({new, refresh, delete, find}) — app.js dispatches Ctrl+N / F5 / Del / Ctrl+F to the current page.
let keymap = {};
export function setKeys(m) { keymap = m || {}; return () => { if (keymap === m) keymap = {}; }; }
export function getKeys() { return keymap; }

export { contextMenu };
