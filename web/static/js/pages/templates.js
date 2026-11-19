// 模板 page (owned by M6): stack templates gallery (GET /stacks) with install-to-env,
// plus built-in agent templates.
import { h, icon, toast, dialog } from '../util.js';
import { t } from '../i18n.js';
import { store, stacks as loadStacks, load, hasCap } from '../store.js';
import { page, cmd, placeholder, setKeys } from '../components/ui.js';
import { BUILTIN, stackIcon, stackName, stackDesc } from '../templates.js';
import { api } from '../api.js';
import { getLang } from '../i18n.js';

const CAT_ORDER = ['base', 'agent', 'runtime'];
const groupStacks = (list) => { const m = new Map(); for (const s of list) { const c = s.category || 'other'; if (!m.has(c)) m.set(c, []); m.get(c).push(s); } return [...m].sort((a, b) => ((CAT_ORDER.indexOf(a[0]) + 1 || 9) - (CAT_ORDER.indexOf(b[0]) + 1 || 9))); };
import { openAgentDialog } from '../components/agent-dialog.js';
import { envActions } from './envs.js';
import { logo, illustration } from '../components/logos.js';

function pickEnv(stack, preferEnv) {
  const ok = (e) => e.state === 'running' && hasCap(e, 'exec') && !e.legacy;
  const list = store.envs.filter((e) => !e.legacy);
  let chosen = (preferEnv && list.some((e) => e.id === preferEnv && ok(e))) ? preferEnv : '';
  let det = {}; // env id -> 'loading' | {installed, version} | {error}
  const rows = h('div', { class: 'pick-list', role: 'listbox' });
  const detect = async (id) => {
    if (det[id]) return; det[id] = 'loading'; draw();
    try { const r = await api('GET', `/envs/${encodeURIComponent(id)}/stacks`); const x = (r?.stacks || []).find((y) => y.id === stack.id);
      det[id] = r?.installing ? { installing: r.installing } : { installed: !!x?.installed || (r?.installed || []).includes(stack.id), version: x?.version || '' }; }
    catch (e) { det[id] = { error: e.message }; }
    if (chosen === id) d.setButtons(btns()); draw();
  };
  const stat = (id) => { const x = det[id]; if (!x) return ''; if (x === 'loading') return ' · ' + t('tplp.detecting'); if (x.error) return ' · ' + x.error;
    if (x.installing) return ' · ' + t('tplp.installing', { name: x.installing }); return ' · ' + (x.installed ? t('tplp.installed', { v: x.version || '' }) : t('tplp.notInstalled')); };
  const draw = () => rows.replaceChildren(...(list.length ? list.map((e) => h('button', { type: 'button', role: 'option', class: 'pick' + (chosen === e.id ? ' on' : ''), disabled: !ok(e), 'data-env': e.id,
    onclick: () => { chosen = e.id; draw(); d.setButtons(btns()); detect(e.id); } }, logo(e, 32), h('span', { class: 'tx' }, h('b', {}, e.name), h('small', {}, `${e.provider_info?.name || e.provider} · ${t('envstate.' + e.state)}${stat(e.id)}`)), chosen === e.id ? icon('check') : null)) : [h('div', { class: 'col', style: 'gap:8px;align-items:flex-start' }, h('div', { class: 'muted' }, t('tplp.noEnvs')), h('a', { class: 'btn accent sm', href: '#/envs?new=1' }, t('envs.new')))]));
  const busyOrDone = () => { const x = det[chosen]; return x && x !== 'loading' && (x.installed || x.installing); };
  const btns = () => ['sp', { text: t('common.cancel'), id: 'cancel' }, { text: t('tplp.install'), accent: true, id: 'install', icon: 'install', disabled: !chosen || busyOrDone(), onClick: () => { envActions.installStack(store.envs.find((e) => e.id === chosen), stack.id); } }];
  const d = dialog({ title: t('tplp.installTo', { name: stackName(stack) }), sub: t('tplp.installSub'), body: rows, buttons: [] });
  draw(); d.setButtons(btns());
  if (chosen) detect(chosen);
}

export default {
  get title() { return t('nav.templates'); },
  async render(el, params) {
    const preferEnv = params?.query?.get('env') || '';
    const body = h('div', { class: 'tpl-page' });
    const pg = page({ title: t('nav.templates'), commands: [cmd({ icon: 'refresh', text: t('cmd.refresh'), id: 'refresh', onClick: () => draw(true) })], body });
    el.append(pg.el);
    if (!store.ready) load().catch(() => {});
    async function draw(force) {
      body.replaceChildren(h('div', { class: 'placeholder' }, h('span', { class: 'spinner' })));
      const stacks = await loadStacks(force).catch((e) => { toast(e.message, true); return []; });
      const nStacks = stacks.length;
      const nEnvs = (store.envs || []).filter((e) => !e.legacy).length;
      body.replaceChildren(
        h('div', { class: 'hero' }, illustration('envs'), h('div', {}, h('h2', {}, t('tpl.heroTitle')), h('p', {}, t('tpl.heroText'))),
          h('div', { class: 'acts' }, h('button', { class: 'btn', onclick: () => openAgentDialog({}) }, icon('bot'), t('agents.new')), h('a', { class: 'btn accent', href: '#/envs?new=1' }, icon('plus'), t('envs.new')))),
        !nEnvs ? h('div', { class: 'alert info' }, icon('info'), h('div', { class: 'grow' }, t('tplp.noEnvs')), h('a', { class: 'btn accent sm', href: '#/envs?new=1' }, t('envs.new'))) : null,
        h('div', { class: 'section-t' }, t('tplp.stacks'), nStacks ? h('span', { class: 'cnt' }, String(nStacks)) : null),
        ...(stacks.length ? groupStacks(stacks).flatMap(([cat, list]) => [h('div', { class: 'section-t sub' }, t('tplp.cat.' + cat)), h('div', { class: 'gallery big' }, list.map((s) => h('div', { class: 'gcard static', 'data-stack': s.id },
          h('div', { class: 'gt' }, stackIcon(s), h('b', {}, stackName(s))),
          h('div', { class: 'gd' }, stackDesc(s)),
          s.languages?.length ? h('div', { class: 'gf' }, s.languages.map((l) => h('span', { class: 'chip' }, l))) : null,
          (s.agent_templates || []).length ? h('div', { class: 'gl' }, h('div', { class: 'small muted' }, t('tplp.agentTpls')),
            (s.agent_templates || []).map((at) => h('button', { type: 'button', class: 'link-row', title: (at.command || []).join(' '), onclick: () => openAgentDialog({ templateId: `${s.id}/${at.id}` }) }, icon('bot'), (getLang() === 'en' && at.name_en) || at.name))) : null,
          (s.env_vars || []).length ? h('div', { class: 'small muted' }, t('tplp.envVars') + ': ', (s.env_vars || []).map((v) => h('code', { class: 'mono', title: v.description || '' }, v.name + (v.secret ? ' 🔒' : ''))).reduce((a, x) => (a.length ? [...a, ' ', x] : [x]), [])) : null,
          h('div', { class: 'ga' }, s.homepage ? h('a', { class: 'btn subtle sm', href: s.homepage, target: '_blank', rel: 'noopener', onclick: (e) => { if (window.agentosOpenExternal) { e.preventDefault(); window.agentosOpenExternal(s.homepage); } } }, icon('link'), t('tplp.homepage')) : null, h('span', { class: 'sp' }), h('button', { class: 'btn accent sm', 'data-act': 'install', onclick: () => pickEnv(s, preferEnv) }, icon('install'), t('tplp.install')))))) ]) :
          [placeholder('envs', store.stacksMissing ? t('tplp.pending') : t('tplp.none'), store.stacksMissing ? t('common.pendingApi') : '')]),
        h('div', { class: 'section-t' }, t('tplp.builtin')),
        h('div', { class: 'gallery' }, BUILTIN.map((tp) => h('button', { type: 'button', class: 'gcard', 'data-tpl': tp.id, onclick: () => openAgentDialog({ templateId: tp.id }) },
          h('div', { class: 'gt' }, h('span', { class: 'gi em' }, tp.em), h('b', {}, tp.name)), h('div', { class: 'gd' }, tp.desc)))));
    }
    const unkeys = setKeys({ refresh: () => draw(true) });
    await draw();
    return unkeys;
  },
};
