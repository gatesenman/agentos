// File browser for /envs/{id}/files (owned by M6): navigate, upload (button or
// drag & drop), download, preview small text files.
import { h, icon, toast, fmtBytes, fmtTime, copyText, dialog } from '../util.js';
import { api, apiFetch } from '../api.js';
import { t } from '../i18n.js';
import { dataGrid, contextMenu } from './ui.js';

const join = (dir, name) => (dir.endsWith('/') ? dir : dir + '/') + name;
const parent = (p) => { const x = p.replace(/\/+$/, ''); const i = x.lastIndexOf('/'); return i <= 0 ? (/^[A-Za-z]:/.test(x) ? x.slice(0, 3) : '/') : x.slice(0, i) || '/'; };
const modeStr = (m) => (m == null ? '' : typeof m === 'string' ? m : (m & 0o777).toString(8).padStart(4, '0'));

export function mountFiles(container, env) {
  const base = `/envs/${encodeURIComponent(env.id)}/files`;
  const win = (env.node?.os || env.labels?.os || env.status?.os) === 'windows';
  let path = localStorage.getItem('agentos.files.' + env.id) || (win ? 'C:/' : '/');
  let rows = [], destroyed = false;
  const pathIn = h('input', { class: 'mono', value: path, 'aria-label': t('files.path'), onkeydown: (e) => { if (e.key === 'Enter') go(pathIn.value.trim() || '/'); } });
  const crumbs = h('div', { class: 'crumbs' });
  const fileIn = h('input', { type: 'file', multiple: true, style: 'display:none', onchange: () => { upload([...fileIn.files]); fileIn.value = ''; } });
  const info = h('span', { class: 'small muted' });
  const grid = dataGrid({
    multi: true, rowKey: 'name',
    columns: [
      { title: t('files.name'), cls: 'nm', render: (f) => h('span', { class: 'two' }, icon(f.dir ? 'folder' : 'file', f.dir ? 'ic-folder' : ''), h('span', {}, f.name)) },
      { title: t('files.size'), cls: 'num', width: '96px', render: (f) => (f.dir ? '' : fmtBytes(f.size)) },
      { title: t('files.modified'), width: '160px', render: (f) => fmtTime(f.mod_time) },
      { title: t('files.mode'), cls: 'mono', width: '70px', render: (f) => modeStr(f.mode) },
    ],
    onOpen: (f) => (f.dir ? go(join(path, f.name)) : preview(f)),
    onContext: (e, f, sel) => contextMenu(e, [
      f.dir ? { text: t('files.open'), icon: 'folder', onClick: () => go(join(path, f.name)) } : { text: t('files.preview'), icon: 'eye', onClick: () => preview(f) },
      { text: t('files.download'), icon: 'download', disabled: [...sel].every((n) => rows.find((r) => r.name === n)?.dir), onClick: () => [...sel].forEach((n) => { const r = rows.find((x) => x.name === n); if (r && !r.dir) download(r); }) },
      '-',
      { text: t('files.copyPath'), icon: 'copy', onClick: () => copyText(join(path, f.name)) },
    ]),
    empty: () => h('div', { class: 'empty' }, icon('folder'), h('div', {}, t('files.empty'))),
  });
  const drop = h('div', { class: 'files-drop dropzone', style: 'flex:1;min-height:0;display:flex;flex-direction:column;position:relative',
    ondragover: (e) => { e.preventDefault(); drop.classList.add('over'); }, ondragleave: () => drop.classList.remove('over'),
    ondrop: (e) => { e.preventDefault(); drop.classList.remove('over'); upload([...e.dataTransfer.files]); } }, grid.el);
  const el = h('div', { class: 'files' },
    h('div', { class: 'toolbar' },
      h('button', { class: 'btn sm subtle icon-only', title: t('files.up'), 'aria-label': t('files.up'), onclick: () => go(parent(path)) }, icon('up')),
      h('button', { class: 'btn sm subtle icon-only', title: t('cmd.refresh'), 'aria-label': t('cmd.refresh'), onclick: () => go(path) }, icon('refresh')),
      h('div', { class: 'grow', style: 'display:flex;flex-direction:column;gap:2px;min-width:0' }, pathIn),
      h('button', { class: 'btn sm', 'data-act': 'upload', onclick: () => fileIn.click() }, icon('upload'), t('files.upload')), fileIn),
    crumbs, drop, h('div', { class: 'files-foot small muted' }, info, h('span', { class: 'sp' }), t('files.dropHint')));
  container.append(el);

  async function go(p) {
    path = p.replace(/\\/g, '/'); pathIn.value = path; localStorage.setItem('agentos.files.' + env.id, path);
    const parts = path.split('/').filter(Boolean);
    const root = /^[A-Za-z]:$/.test(parts[0] || '') ? parts.shift() + '/' : '/';
    crumbs.replaceChildren(h('a', { href: '#', onclick: (e) => { e.preventDefault(); go(root); } }, root === '/' ? '/' : root),
      ...parts.flatMap((s, i) => [h('span', { class: 'sep' }, '›'), h('a', { href: '#', onclick: (e) => { e.preventDefault(); go(root + parts.slice(0, i + 1).join('/')); } }, s)]));
    info.textContent = '…';
    try {
      const list = (await api('GET', `${base}?path=${encodeURIComponent(path)}`)) || [];
      if (destroyed) return;
      rows = list.sort((a, b) => (b.dir - a.dir) || a.name.localeCompare(b.name));
      grid.setRows(rows);
      info.textContent = t('files.count', { n: rows.length });
    } catch (e) {
      if (destroyed) return;
      rows = []; grid.setRows([]); info.textContent = e.message; toast(e.message, true);
    }
  }
  async function fetchFile(f) {
    const r = await apiFetch(`${base}/content?path=${encodeURIComponent(join(path, f.name))}`);
    if (!r.ok) { let m = 'HTTP ' + r.status; try { m = (await r.json()).error || m; } catch {} throw new Error(m); }
    return r.blob();
  }
  async function download(f) {
    try {
      const blob = await fetchFile(f);
      const url = URL.createObjectURL(blob); const a = h('a', { href: url, download: f.name });
      document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 2000);
    } catch (e) { toast(e.message, true); }
  }
  async function preview(f) {
    if (f.size > 512 * 1024) return download(f);
    try {
      const txt = await (await fetchFile(f)).text();
      const bin = /[\x00-\x08\x0e-\x1f]/.test(txt.slice(0, 2000));
      if (bin) return download(f);
      dialog({ title: f.name, sub: join(path, f.name), wide: 'xwide', body: h('pre', { class: 'code selectable', style: 'max-height:56vh;overflow:auto;margin:0' }, txt),
        buttons: [{ text: t('files.download'), icon: 'download', onClick: () => { download(f); return false; } }, 'sp', { text: t('common.close'), accent: true }] });
    } catch (e) { toast(e.message, true); }
  }
  async function upload(list) {
    if (!list.length) return;
    let ok = 0;
    for (const file of list) {
      info.textContent = t('files.uploading', { name: file.name });
      try {
        const r = await apiFetch(`${base}/content?path=${encodeURIComponent(join(path, file.name))}&mode=0644`, { method: 'PUT', body: file, headers: { 'Content-Type': 'application/octet-stream' } });
        if (!r.ok) { let m = 'HTTP ' + r.status; try { m = (await r.json()).error || m; } catch {} throw new Error(m); }
        ok++;
      } catch (e) { toast(`${file.name}: ${e.message}`, true); }
    }
    if (ok) toast(t('files.uploaded', { n: ok }));
    go(path);
  }
  go(path);
  return { destroy() { destroyed = true; } };
}
