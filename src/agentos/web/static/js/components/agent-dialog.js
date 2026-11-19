// Create / edit / duplicate agent ContentDialog (owned by M6).
// openAgentDialog({agent?, mode: 'new'|'edit'|'duplicate', envId?, templateId?}) -> Promise<agent|null>
import { h, icon, toast, dialog, shellJoin } from '../util.js';
import { api } from '../api.js';
import { t } from '../i18n.js';
import { store, stacks as loadStacks } from '../store.js';
import { BUILTIN, BLANK, stackTemplates, stackName } from '../templates.js';

const ENV_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/;
const isActive = (s) => ['running', 'starting', 'backoff'].includes(s);

export async function openAgentDialog({ agent, mode = 'new', envId, templateId } = {}) {
  const editing = mode === 'edit';
  const hosts = store.envs.filter((e) => e.node_id);
  let a = { restart: 'never', auto_start: false, max_restarts: 0, restart_delay_sec: 3, args: [], env: {} };
  if (agent) { a = { ...agent }; delete a.status; if (mode === 'duplicate') { delete a.id; a.name = agent.name + t('form.copySuffix'); } }
  const defEnv = envId ? store.envs.find((e) => e.id === envId) : agent ? store.envs.find((e) => e.node_id === agent.node_id) : hosts.find((e) => e.state === 'running') || hosts[0];
  const stackList = await loadStacks().catch(() => []);
  const tpls = [BLANK, ...stackTemplates(stackList), ...BUILTIN];

  const errs = {};
  const field = (key, label, input, { req, hint, full } = {}) => {
    const err = h('div', { class: 'err-msg' });
    const f = h('div', { class: 'field' + (full ? ' full' : '') }, h('label', {}, label, req ? h('span', { class: 'req' }, ' *') : null), input, hint ? h('div', { class: 'hint' }, hint) : null, err);
    errs[key] = (msg) => { f.classList.toggle('has-err', !!msg); err.textContent = msg || ''; if (input.matches?.('input,select,textarea')) input.classList.toggle('invalid', !!msg); };
    return f;
  };
  const name = h('input', { value: a.name || '', placeholder: t('form.namePh'), maxlength: 64, 'data-f': 'name' });
  const envSel = h('select', { 'data-f': 'env', disabled: editing },
    !hosts.length ? h('option', { value: '' }, t('agentdlg.noHosts')) : null,
    hosts.map((e) => h('option', { value: e.node_id, selected: e === defEnv || e.node_id === a.node_id }, `${e.name} · ${t('envstate.' + e.state)}`)));
  const cmd = h('input', { value: a.command || '', placeholder: t('form.cmdPh'), class: 'mono', 'data-f': 'cmd', oninput: () => preview() });
  const desc = h('input', { value: a.description || '' });
  const workdir = h('input', { value: a.work_dir || '', placeholder: t('form.workdirPh'), class: 'mono' });
  const restart = h('select', {}, ['never', 'on-failure', 'always'].map((v) => h('option', { value: v, selected: v === a.restart }, t('restart.' + v))));
  const maxR = h('input', { type: 'number', min: 0, step: 1, value: a.max_restarts ?? 0 });
  const delay = h('input', { type: 'number', min: 0, step: 1, value: a.restart_delay_sec ?? 3 });
  const auto = h('input', { type: 'checkbox', checked: !!a.auto_start });
  const prev = h('div', { class: 'cmd-preview' });
  const argsBox = h('div', { class: 'kvrows' }), envBox = h('div', { class: 'kvrows' });
  const grow = (ta) => { ta.style.height = 'auto'; ta.style.height = Math.min(160, Math.max(32, ta.scrollHeight + 2)) + 'px'; };
  const argRow = (v = '') => {
    const ta = h('textarea', { rows: 1, class: 'mono', spellcheck: 'false', oninput: () => { grow(ta); preview(); } }); ta.value = v;
    const row = h('div', { class: 'argrow' }, h('span', { class: 'n' }), ta, h('button', { type: 'button', class: 'btn subtle sm icon-only', title: t('common.remove'), 'aria-label': t('common.remove'), onclick: () => { row.remove(); renum(); preview(); } }, icon('x')));
    argsBox.append(row); renum(); requestAnimationFrame(() => grow(ta)); return ta;
  };
  const renum = () => argsBox.querySelectorAll('.argrow .n').forEach((n, i) => (n.textContent = '$' + (i + 1)));
  const getArgs = () => [...argsBox.querySelectorAll('textarea')].map((x) => x.value).filter((x, i, arr) => x !== '' || i < arr.length - 1);
  const envRow = (k = '', v = '') => {
    const ki = h('input', { value: k, placeholder: 'KEY', class: 'mono', 'aria-label': 'key' }), vi = h('input', { value: v, placeholder: t('form.envValPh'), class: 'mono', 'aria-label': 'value' });
    const row = h('div', { class: 'kvrow' }, ki, vi, h('button', { type: 'button', class: 'btn subtle sm icon-only', title: t('common.remove'), 'aria-label': t('common.remove'), onclick: () => row.remove() }, icon('x')));
    envBox.append(row); return ki;
  };
  const setArgs = (l) => { argsBox.replaceChildren(); (l || []).forEach((v) => argRow(v)); };
  const setEnv = (e) => { envBox.replaceChildren(); Object.entries(e || {}).forEach(([k, v]) => envRow(k, v)); };
  const preview = () => { prev.textContent = cmd.value ? shellJoin(cmd.value, getArgs()) : '—'; };
  setArgs(a.args); setEnv(a.env); preview();

  // template list (left column)
  let tplId = null;
  const tplList = h('div', { class: 'tpl-list', role: 'listbox', 'aria-label': t('form.template') });
  const groupOf = (tp) => (tp.stack ? stackName(tp.stack) : tp.id === 'blank' ? '' : t('agentdlg.builtin'));
  let lastGroup = null;
  for (const tp of tpls) {
    const g = groupOf(tp);
    if (g && g !== lastGroup) tplList.append(h('div', { class: 'tpl-g' }, g)); lastGroup = g;
    tplList.append(h('button', { type: 'button', class: 'tpl-i', role: 'option', 'data-tpl': tp.id, onclick: () => applyTpl(tp) },
      tp.icon ? h('span', { class: 'em' }, tp.icon.cloneNode(true)) : h('span', { class: 'em' }, tp.em), h('span', { class: 'tx' }, h('b', {}, tp.name), h('small', {}, tp.desc))));
  }
  const winHost = () => (store.nodes.find((n) => n.id === envSel.value)?.os || store.envs.find((e) => e.node_id === envSel.value)?.node?.os) === 'windows';
  function applyTpl(tp) {
    const v = { description: '', args: [], env: {}, restart: 'never', max_restarts: 0, restart_delay_sec: 3, auto_start: false, command: '', ...tp.make(winHost()) };
    if (!name.value || name.dataset.auto === '1') { name.value = v.name || ''; name.dataset.auto = '1'; }
    desc.value = v.description || ''; cmd.value = v.command; setArgs(v.args); setEnv(v.env);
    restart.value = v.restart; maxR.value = v.max_restarts; delay.value = v.restart_delay_sec; auto.checked = !!v.auto_start;
    tplId = tp.id; tplList.querySelectorAll('.tpl-i').forEach((b) => { b.classList.toggle('on', b.dataset.tpl === tplId); b.setAttribute('aria-selected', b.dataset.tpl === tplId); });
    Object.values(errs).forEach((f) => f()); preview();
  }
  name.addEventListener('input', () => { name.dataset.auto = ''; errs.name(); });

  const sec = (title) => h('div', { class: 'section-t full' }, title);
  const form = h('form', { class: 'form', novalidate: true, onsubmit: (e) => e.preventDefault() },
    field('name', t('form.name'), name, { req: true }), field('env', t('agentdlg.env'), envSel, { req: true }),
    field('cmd', t('form.cmd'), cmd, { req: true, hint: t('form.cmdHint'), full: true }),
    field('args', t('form.args'), h('div', { class: 'kvrows' }, argsBox, h('div', {}, h('button', { type: 'button', class: 'btn sm', onclick: () => argRow('').focus() }, icon('plus'), t('form.addArg')))), { full: true }),
    field('preview', t('form.preview'), prev, { full: true }),
    field('env', t('form.env'), h('div', { class: 'kvrows' }, envBox, h('div', {}, h('button', { type: 'button', class: 'btn sm', onclick: () => envRow().focus() }, icon('plus'), t('form.addEnv')))), { full: true }),
    sec(t('form.policy')),
    field('restart', t('form.restart'), restart), field('auto', t('form.autoStart'), h('label', { class: 'switch' }, auto, h('span', { class: 'tr' }), h('span', { class: 'small muted' }, t('form.autoStartHint')))),
    field('maxR', t('form.maxRestarts'), maxR, { hint: t('form.maxRestartsHint') }), field('delay', t('form.delay'), delay, { hint: t('form.delayHint') }),
    field('workdir', t('form.workdir'), workdir, { hint: t('form.workdirHint') }), field('desc', t('form.desc'), desc));
  // the second 'env' key belongs to the variables editor
  const envVarsErr = errs.env; errs.envVars = envVarsErr;
  errs.env = (m) => { const f = envSel.closest('.field'); f.classList.toggle('has-err', !!m); f.querySelector('.err-msg').textContent = m || ''; envSel.classList.toggle('invalid', !!m); };

  const body = h('div', { class: 'agent-dlg' + (editing ? ' no-tpl' : '') }, editing ? null : tplList, h('div', { class: 'agent-form' }, form));
  const validate = () => {
    Object.values(errs).forEach((f) => f());
    let ok = true; const bad = (k, m) => { errs[k](m); ok = false; };
    if (!name.value.trim()) bad('name', t('form.err.name'));
    if (!envSel.value) bad('env', t('agentdlg.err.env'));
    if (!cmd.value.trim()) bad('cmd', t('form.err.cmd'));
    for (const [k, inp] of [['maxR', maxR], ['delay', delay]]) if (inp.value === '' || !Number.isInteger(+inp.value) || +inp.value < 0) bad(k, t('form.err.num'));
    const env = {}, seen = new Set();
    for (const row of envBox.querySelectorAll('.kvrow')) {
      const [ki, vi] = row.querySelectorAll('input'), k = ki.value.trim(); ki.classList.remove('invalid');
      if (!k && !vi.value) continue;
      if (!ENV_KEY.test(k)) { bad('envVars', t('form.err.envKey')); ki.classList.add('invalid'); continue; }
      if (seen.has(k)) { bad('envVars', t('form.err.envDup', { k })); ki.classList.add('invalid'); continue; }
      seen.add(k); env[k] = vi.value;
    }
    if (!ok) form.querySelector('.has-err input, .has-err select, .invalid')?.focus();
    return ok ? env : null;
  };
  let result = null;
  const submit = async (andStart) => {
    const env = validate(); if (!env) return false;
    const spec = { ...(editing ? a : {}), name: name.value.trim(), description: desc.value.trim(), node_id: envSel.value, command: cmd.value.trim(), args: getArgs(), env,
      work_dir: workdir.value.trim(), restart: restart.value, max_restarts: +maxR.value, restart_delay_sec: +delay.value, auto_start: auto.checked };
    try {
      let res = editing ? await api('PUT', '/agents/' + encodeURIComponent(a.id), spec) : await api('POST', '/agents', spec);
      if (andStart) {
        try {
          if (editing) res = await api('POST', `/agents/${encodeURIComponent(res.id)}/restart`);
          else if (!isActive(res.status?.state)) res = await api('POST', `/agents/${encodeURIComponent(res.id)}/start`);
        } catch (e) { if (!(e.status === 409 && isActive(res.status?.state))) toast(e.message, true); }
      }
      toast(editing ? t('common.saved') : t('form.created'));
      result = res; return true;
    } catch (e) { toast(e.message, true); return false; }
  };
  const d = dialog({
    title: t(editing ? 'form.editTitle' : mode === 'duplicate' ? 'form.dupTitle' : 'form.newTitle'), sub: editing ? a.name : t('agentdlg.sub'), wide: 'xwide', className: 'agent-modal', body,
    buttons: editing
      ? ['sp', { text: t('common.cancel'), id: 'cancel', onClick: (c) => c(null) }, isActive(agent?.status?.state) ? { text: t('form.saveRestart'), id: 'save-restart', icon: 'restart', onClick: () => submit(true) } : null, { text: t('common.save'), accent: true, id: 'save', onClick: () => submit(false) }].filter(Boolean)
      : ['sp', { text: t('common.cancel'), id: 'cancel', onClick: (c) => c(null) }, { text: t('form.create'), id: 'create', onClick: () => submit(false) }, { text: t('form.createStart'), accent: true, icon: 'play', id: 'create-start', onClick: () => submit(true) }],
  });
  if (!editing && mode !== 'duplicate') { const tp = tpls.find((x) => x.id === templateId) || BLANK; applyTpl(tp); }
  setTimeout(() => name.focus(), 30);
  await d.done;
  return result;
}
