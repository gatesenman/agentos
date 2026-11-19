// '新建虚拟电脑' wizard (owned by M6):
// 1 快速创建 machine presets (GET /env-presets → POST /envs {preset}) or custom provider
// → 2 schema form (+ test; advanced folded) → create (POST /envs, machine only — no stack install)
// → 3 done: open computer / install tools (POST /envs/{id}/stacks/.../install is separate).
import { h, icon, toast, dialog, copyText } from '../util.js';
import { api } from '../api.js';
import { t, getLang } from '../i18n.js';
import { store, providers as loadProviders, presets as loadPresets } from '../store.js';
import { schemaForm, placeholder } from './ui.js';
import { GROUP_ORDER, groupLabel, kindIcon, capChip, joinPanel, envStatePill } from './env-common.js';
import { logo, illustration } from './logos.js';

const CAT_ORDER = ['agentos', 'container', 'ssh', 'vm'];
const isWin = /Win/i.test(navigator.userAgent) || navigator.platform === 'Win32';
const presetName = (p) => (getLang() === 'en' && p.name_en) || p.name;
const presetDesc = (p) => {
  // Windows-first copy for local Agent OS presets (host screen, not Xvfb).
  if (isWin && (p.id === 'agentos-desktop' || p.id === 'agentos-windows')) return t('wiz.presetWinLocal');
  return (getLang() === 'en' && p.description_en) || p.description;
};


// Prefer recommended desktop-like presets (docker-desktop / docker-debian-desktop / hyperv-ubuntu-2404).
// Never auto-select advanced/terminal console presets.
function isAdvancedPreset(p) {
  return (p.tags || []).some((x) => x === '高级' || x === '终端' || /高级|终端/.test(String(x)));
}
function pickDefaultPreset(list, prePreset) {
  if (prePreset) {
    const hit = list.find((p) => p.id === prePreset && p.available);
    if (hit) return hit;
  }
  const preferred = ['docker-desktop', 'docker-debian-desktop', 'hyperv-ubuntu-2404'];
  for (const id of preferred) {
    const hit = list.find((p) => p.id === id && p.available && !isAdvancedPreset(p));
    if (hit) return hit;
  }
  const recDesk = list.find((p) => p.available && !isAdvancedPreset(p)
    && (p.tags || []).includes('推荐') && ((p.tags || []).includes('桌面') || preferred.includes(p.id)));
  if (recDesk) return recDesk;
  return list.find((p) => p.available && !isAdvancedPreset(p)) || list.find((p) => p.available) || null;
}

export async function openEnvWizard({ provider: preType, preset: prePreset } = {}) {
  const [provs, presetList] = await Promise.all([loadProviders().catch(() => []), loadPresets().catch(() => [])]);
  let step = 1, prov = provs.find((p) => p.type === preType) || null, form = null, created = null;
  let mode = presetList.length && !preType ? 'quick' : 'custom';
  let preset = pickDefaultPreset(presetList, prePreset);
  let formSeed = {}; // prefill for the custom form (from a preset)
  const name = h('input', { placeholder: t('wiz.namePh'), maxlength: 64, 'data-f': 'name' });
  const labels = h('input', { placeholder: 'team=ml, gpu=true', class: 'mono' });
  const nameErr = h('div', { class: 'err-msg' });
  const testOut = h('div');
  const stepsEl = h('div', { class: 'steps' });
  const content = h('div', { class: 'wiz-body' });
  const d = dialog({ title: t('wiz.title'), sub: t('wiz.sub'), wide: 'xwide', className: 'wizard', body: h('div', {}, stepsEl, content), buttons: [] });

  const STEPS = () => [t('wiz.s1'), t('wiz.s2'), t('wiz.s5')];
  function drawSteps() {
    stepsEl.replaceChildren(...STEPS().flatMap((s, i) => [i ? h('i', { class: i + 1 <= step ? 'done' : '' }) : null, h('span', { class: i + 1 === step ? 'on' : i + 1 < step ? 'done' : '' }, h('b', {}, i + 1 < step || (step === 3 && i === 2) ? icon('check') : String(i + 1)), s)]).filter(Boolean));
  }
  function show() {
    drawSteps();
    if (step === 1) step1(); else if (step === 2) step2(); else step4();
  }
  function modeSwitch() {
    if (!presetList.length) return null;
    const b = (id, ic, label) => h('button', { type: 'button', class: mode === id ? 'on' : '', 'data-mode': id, onclick: () => { mode = id; show(); } }, icon(ic), label);
    return h('div', { class: 'seg wiz-mode' }, b('quick', 'zap', t('wiz.quick')), b('custom', 'sliders', t('wiz.custom')));
  }
  function step1() {
    if (mode === 'quick' && presetList.length) return stepQuick();
    stepCustom();
    const sw = modeSwitch(); if (sw) content.prepend(sw);
  }
  async function repairHyperVGroup() {
    try {
      toast('正在修复 Hyper-V Administrators 组成员…');
      const r = await api('POST', '/providers/hyperv/repair-group');
      toast(r.message || (r.ok ? '修复完成，请注销后重新登录' : '修复失败'), !r.ok);
      if (r.need_relogin) toast('请注销并重新登录 Windows，新组权限才会生效');
      await loadPresets(true).catch(() => {});
      stepQuick();
    } catch (e) {
      toast('修复失败：' + (e.message || e), true);
    }
  }
  function presetCard(p) {
    const on = preset?.id === p.id;
    const tags = [...(p.tags || [])];
    if (p.fallback_note && p.available) tags.unshift(p.fallback_note);
    return h('button', { type: 'button', class: 'gcard preset' + (on ? ' on' : '') + (p.available ? '' : ' unavailable'), 'data-preset': p.id, 'aria-disabled': p.available ? null : 'true',
      title: p.available ? (p.fallback_note || '') : p.reason || '', onclick: () => { if (!p.available) { toast(p.reason || t('wiz.unavailable'), true); return; } preset = p; stepQuick(); }, ondblclick: () => { if (!p.available) return; preset = p; quickCreate(); } },
    h('div', { class: 'gt' }, logo(p.icon || p.provider, 32), h('div', { class: 'grow' }, h('b', {}, presetName(p)))),
    h('div', { class: 'gd' }, presetDesc(p) || ''),
    p.available ? h('div', { class: 'gf' }, tags.slice(0, 5).map((x) => h('span', { class: 'chip' + (x === p.fallback_note ? ' soft' : '') }, x)))
      : h('div', { class: 'gna' }, icon('alert'), h('span', { class: 'grow' }, t('wiz.unavailableHint', { reason: p.reason || t('wiz.unavailable') })),
        p.can_repair_group ? h('button', { type: 'button', class: 'btn sm', 'data-repair': 'hyperv-group', onclick: (e) => { e.preventDefault(); e.stopPropagation(); repairHyperVGroup(); } }, '一键修复权限') : null,
        (p.help_url && !/evalcenter|download-windows|releases\.ubuntu|fedoraproject\.org\/.*iso/i.test(p.help_url)) ? h('a', { href: p.help_url, target: '_blank', rel: 'noopener', onclick: (e) => { e.stopPropagation(); if (window.agentosOpenExternal) { e.preventDefault(); window.agentosOpenExternal(p.help_url); } } }, t('wiz.help')) : null,
        altOf(p) ? h('a', { href: '#', class: 'alt', 'data-alt': altOf(p).id, onclick: (e) => { e.preventDefault(); e.stopPropagation(); preset = altOf(p); stepQuick(); } }, t('wiz.altUse', { name: presetName(altOf(p)) })) : null));
  }
  function altOf(p) {
    if (!p.alt_preset) return null;
    const a = presetList.find((x) => x.id === p.alt_preset);
    return a && a.available ? a : null;
  }
  function stepQuick() {
    if (preset && !preset.available) preset = null;
    const isAdv = isAdvancedPreset;
    const groups = new Map();
    for (const p of presetList) { const g = p.category || 'other'; if (!groups.has(g)) groups.set(g, []); groups.get(g).push(p); }
    for (const [, arr] of groups) arr.sort((a, b) => (isAdv(a) - isAdv(b)));
    const order = [...groups.keys()].sort((a, b) => ((CAT_ORDER.indexOf(a) + 1 || 99) - (CAT_ORDER.indexOf(b) + 1 || 99)));
    content.replaceChildren(...[modeSwitch(), h('p', { class: 'muted wiz-lead' }, t('wiz.quickHint')),
      ...order.flatMap((g) => [h('div', { class: 'section-t' }, t('wiz.cat.' + g)), h('div', { class: 'gallery presets' }, groups.get(g).map(presetCard))]),
      preset?.note ? h('div', { class: 'alert info', style: 'margin-top:12px' }, icon('info'), preset.note) : null,
      presetList.some((x) => x.can_repair_group) ? h('div', { class: 'alert warn', style: 'margin-top:12px' }, icon('shield'),
        h('span', { class: 'grow' }, '当前账户不在「Hyper-V Administrators」组；本机虚拟机可经 WSL QEMU 使用。也可一键修复组权限（需提权，注销后生效）。'),
        h('button', { type: 'button', class: 'btn sm', onclick: () => repairHyperVGroup() }, '一键修复权限')) : null].filter(Boolean));
    d.setButtons([
      { text: t('wiz.customize'), icon: 'edit', id: 'customize', disabled: !preset || busy, onClick: () => { customizePreset(); return false; } }, 'sp',
      { text: t('common.cancel'), id: 'cancel', disabled: busy },
      { text: busy ? t('wiz.creating') : t('wiz.create'), accent: true, icon: busy ? null : 'plus', id: 'quick-create', disabled: !preset || busy, onClick: async () => { await quickCreate(); return false; } }]);
  }
  function customizePreset() {
    if (!preset) return;
    prov = provs.find((p) => p.type === preset.provider) || null;
    if (!prov) { toast(t('wiz.noProvider'), true); return; }
    formSeed = { ...(preset.config || {}) }; form = null; name.value = ''; mode = 'custom'; step = 2; show();
  }
  let busy = false;
  function setCreatingUI(on) {
    busy = on;
    if (step !== 1 || mode !== 'quick') return;
    d.setButtons([
      { text: t('wiz.customize'), icon: 'edit', id: 'customize', disabled: !preset || busy, onClick: () => { customizePreset(); return false; } }, 'sp',
      { text: t('common.cancel'), id: 'cancel', disabled: busy },
      { text: busy ? t('wiz.creating') : t('wiz.create'), accent: true, icon: busy ? null : 'plus', id: 'quick-create', disabled: !preset || busy, onClick: async () => { await quickCreate(); return false; } }]);
  }
  async function quickCreate() {
    if (busy) { toast(t('wiz.createBusy')); return; }
    if (!preset) { toast(t('wiz.selectPreset'), true); return; }
    if (!preset.available) { toast(preset.reason || t('wiz.unavailable'), true); return; }
    if (preset.provider === 'ssh' && preset.config && preset.config.host === '') { customizePreset(); return; } // needs an address
    setCreatingUI(true);
    toast(t('wiz.creatingHint'));
    const ac = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const timer = ac ? setTimeout(() => ac.abort(), 90000) : null;
    try {
      created = await api('POST', '/envs', { preset: preset.id }, ac ? { signal: ac.signal } : undefined);
      prov = provs.find((p) => p.type === preset.provider) || { type: preset.provider, name: presetName(preset) };
      toast(t('wiz.created', { name: created.name }));
      created._toasted = true;
      busy = false;
      step = 3; show();
    } catch (e) {
      const msg = (e && e.name === 'AbortError') ? t('wiz.createTimeout') : (e.message || String(e));
      toast(msg, true);
      setCreatingUI(false);
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
  function stepCustom() {
    if (!provs.length) {
      content.replaceChildren(placeholder('🧩', t('wiz.noProviders'), store.providersMissing ? t('wiz.noProvidersHint') : ''));
      d.setButtons(['sp', { text: t('common.close'), id: 'cancel' }]);
      // Legacy servers: a remote Agent OS can still join with the node token.
      api('GET', '/settings/node-token').then(({ token }) => {
        if (!token || !content.isConnected) return;
        const c = `agentos node --server ${location.origin} --token ${token}`;
        content.append(h('div', { class: 'card', style: 'margin-top:8px' }, h('div', { class: 'card-h' }, h('h3', {}, t('wiz.legacyJoin'))),
          h('p', { class: 'muted small', style: 'margin:0 0 8px' }, t('settings.addNodeD')),
          h('div', { class: 'code-row' }, h('pre', { class: 'code selectable', style: 'flex:1;margin:0;white-space:pre-wrap;word-break:break-all' }, c),
            h('button', { type: 'button', class: 'btn sm', onclick: () => copyText(c) }, icon('copy'), t('join.copy')))));
      }).catch(() => {});
      return;
    }
    const groups = new Map();
    for (const p of provs) { const g = p.group || 'other'; if (!groups.has(g)) groups.set(g, []); groups.get(g).push(p); }
    const order = [...groups.keys()].sort((a, b) => ((GROUP_ORDER.indexOf(a) + 1 || 99) - (GROUP_ORDER.indexOf(b) + 1 || 99)));
    content.replaceChildren(...order.flatMap((g) => [h('div', { class: 'section-t' }, groupLabel(g)),
      h('div', { class: 'gallery' }, groups.get(g).map((p) => h('button', { type: 'button', class: 'gcard' + (prov?.type === p.type ? ' on' : ''), 'data-provider': p.type,
        onclick: () => { prov = p; form = null; formSeed = {}; show(); }, ondblclick: () => { prov = p; form = null; formSeed = {}; step = 2; show(); } },
      h('div', { class: 'gt' }, logo(p, 32), h('b', {}, p.name)),
      h('div', { class: 'gd' }, p.description || ''),
      h('div', { class: 'gf' }, (p.capabilities || []).filter((c) => !c.startsWith('desktop.') || true).slice(0, 6).map((c) => capChip(c))))))]));
    d.setButtons(['sp', { text: t('common.cancel'), id: 'cancel' }, { text: t('wiz.next'), accent: true, id: 'next', disabled: !prov, onClick: () => { step = 2; show(); return false; } }]);
  }
  function step2() {
    if (!form) form = schemaForm(prov.config_schema || {}, formSeed);
    if (!name.value) name.value = suggestName(preset && formSeed !== null && preset.provider === prov.type && Object.keys(formSeed).length ? preset.base_name : (prov.type === 'agentos-local' ? t('wiz.defNameLocal') : prov.name));
    content.replaceChildren(
      h('div', { class: 'wiz-prov' }, logo(prov, 40), h('div', { class: 'grow' }, h('b', {}, prov.name), h('div', { class: 'small muted' }, prov.description || '')), h('button', { type: 'button', class: 'btn sm subtle', onclick: () => { step = 1; show(); } }, icon('edit'), t('wiz.change'))),
      h('div', { class: 'form' },
        h('div', { class: 'field' }, h('label', {}, t('wiz.name'), h('span', { class: 'req' }, ' *')), name, nameErr),
        h('div', { class: 'field' }, h('label', {}, t('wiz.labels')), labels, h('div', { class: 'hint' }, t('wiz.labelsHint')))),
      h('div', { class: 'section-t' }, t('wiz.config')), form.el, testOut);
    d.setButtons([
      { text: t('wiz.test'), icon: 'plug', id: 'test', onClick: async () => { await testConn(); return false; } }, 'sp',
      { text: t('wiz.back'), id: 'back', onClick: () => { step = 1; show(); return false; } },
      { text: busy ? t('wiz.creating') : t('wiz.create'), accent: true, icon: busy ? null : 'plus', id: 'create2', disabled: busy, onClick: async () => { if (!validate2()) return false; await create(); return false; } }]);
    setTimeout(() => name.focus());
  }
  function suggestName(base) { // "<base> N", first free N (server does the same when name is omitted)
    const used = new Set(store.envs.map((e) => e.name));
    for (let i = 1; ; i++) if (!used.has(`${base} ${i}`)) return `${base} ${i}`;
  }
  function validate2() {
    const ok1 = !!name.value.trim(); nameErr.textContent = ok1 ? '' : t('form.err.name'); name.classList.toggle('invalid', !ok1); name.closest('.field').classList.toggle('has-err', !ok1);
    const ok2 = form.validate();
    if (!ok1) name.focus(); else if (!ok2) content.querySelector('.has-err input, .has-err select, .has-err textarea')?.focus();
    return ok1 && ok2;
  }
  async function testConn() {
    if (!form.validate()) return;
    testOut.replaceChildren(h('div', { class: 'alert info' }, h('span', { class: 'spinner sm' }), t('wiz.testing')));
    try {
      const r = await api('POST', `/providers/${encodeURIComponent(prov.type)}/test`, { config: form.value() });
      testOut.replaceChildren(h('div', { class: 'alert ok', 'data-test': 'ok' }, icon('check'), t('wiz.testOk') + (r?.message ? ' · ' + r.message : '')));
    } catch (e) {
      testOut.replaceChildren(h('div', { class: 'alert err', 'data-test': 'err' }, icon('alert'), t('wiz.testFail', { msg: e.message })));
    }
  }
  async function create() {
    if (busy) { toast(t('wiz.createBusy')); return false; }
    const lab = {};
    for (const p of labels.value.split(/[,，\n]/)) { const [k, ...v] = p.split('='); if (k.trim()) lab[k.trim()] = v.join('=').trim(); }
    const body = { name: name.value.trim(), provider: prov.type, config: form.value(), labels: lab };
    // 0.1.3+: create provisions the machine only; tools install is a separate step.
    busy = true;
    d.setButtons([
      { text: t('wiz.test'), icon: 'plug', id: 'test', disabled: true, onClick: async () => false }, 'sp',
      { text: t('wiz.back'), id: 'back', disabled: true, onClick: () => false },
      { text: t('wiz.creating'), accent: true, id: 'create2', disabled: true, onClick: async () => false }]);
    toast(t('wiz.creatingHint'));
    const ac = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const timer = ac ? setTimeout(() => ac.abort(), 90000) : null;
    try {
      created = await api('POST', '/envs', body, ac ? { signal: ac.signal } : undefined);
      busy = false;
      step = 3; show(); return false;
    } catch (e) {
      const msg = (e && e.name === 'AbortError') ? t('wiz.createTimeout') : (e.message || String(e));
      toast(msg, true);
      busy = false;
      step2();
      return false;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
  async function step4() {
    const e = store.envs.find((x) => x.id === created.id) || created;
    const sum = h('div', { class: 'sum' },
      h('span', {}, t('wiz.sumName')), h('b', {}, created.name),
      h('span', {}, t('wiz.sumType')), h('span', { class: 'row', style: 'gap:8px;flex-wrap:nowrap' }, logo(prov || created, 18), (prov && prov.name) || created.provider),
      h('span', {}, t('wiz.sumState')), h('span', {}, envStatePill(e)));
    const join = h('div', { class: 'join-slot', style: 'width:100%' });
    content.replaceChildren(h('div', { class: 'wiz-done', 'data-step': 'done' }, illustration('done'), h('h3', {}, t('wiz.doneTitle')), h('p', {}, t('wiz.doneMsg', { name: created.name })),
      e.busy || e.state === 'creating' ? h('p', { class: 'small' }, t('wiz.doneProvisioning')) : null, sum, join));
    const openCreated = (e) => {
      const desk = (e.capabilities || []).some((c) => c === 'desktop.vnc' || c === 'desktop.screen');
      location.assign('#/envs/' + encodeURIComponent(e.id) + (desk ? '/desktop' : ''));
    };
    const goInstall = () => {
      const c = created; d.close(c);
      setTimeout(() => { location.assign('#/templates?env=' + encodeURIComponent(c.id)); });
      return false;
    };
    d.setButtons([
      { text: t('wiz.doneAnother'), id: 'another', onClick: () => { const c = created; d.close(c); setTimeout(() => openEnvWizard().then((x) => x && openCreated(x))); return false; } },
      { text: t('wiz.doneInstall'), id: 'install-tools', icon: 'install', onClick: goInstall },
      'sp',
      { text: t('wiz.doneOpen'), accent: true, id: 'done', icon: 'right', onClick: () => { const c = created; d.close(c); setTimeout(() => openCreated(c)); return false; } }]);
    if (prov.type !== 'agentos-remote') return;
    join.replaceChildren(h('div', { class: 'row', style: 'justify-content:center' }, h('span', { class: 'spinner sm' }), t('wiz.joinLoading')));
    try { const j = await api('POST', `/envs/${encodeURIComponent(created.id)}/join`); join.replaceChildren(h('div', { class: 'card', style: 'text-align:left;margin:8px 0 0' }, h('div', { class: 'card-h' }, h('h3', {}, icon('link'), t('join.title'))), joinPanel(j))); }
    catch (x) { join.replaceChildren(h('div', { class: 'alert err' }, icon('alert'), x.message)); }
  }
  if (prov) step = 2;
  show();
  await d.done;
  if (created && !created._toasted) toast(t('wiz.created', { name: created.name }));
  return created;
}

export async function showJoinDialog(env) {
  const body = h('div', {}, h('div', { class: 'row' }, h('span', { class: 'spinner sm' }), t('wiz.joinLoading')));
  const d = dialog({ title: t('join.title'), sub: env.name, wide: true, body, buttons: ['sp', { text: t('common.close'), accent: true, id: 'close' }] });
  try { body.replaceChildren(joinPanel(await api('POST', `/envs/${encodeURIComponent(env.id)}/join`))); }
  catch (e) { body.replaceChildren(h('div', { class: 'alert err' }, icon('alert'), e.message)); }
  return d.done;
}
