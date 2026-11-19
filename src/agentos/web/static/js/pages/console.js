// Pop-out remote desktop window (owned by M6): #/console/<env id>.
// Chromeless frame (title bar only) hosting components/console.js.
import { h, icon } from '../util.js';
import { t } from '../i18n.js';
import { store, subscribe, load, envById } from '../store.js';
import { placeholder } from '../components/ui.js';

export default {
  get title() { return t('tab.desktop'); },
  async render(el, params) {
    const id = decodeURIComponent(params.id || '');
    if (!store.ready) await load().catch(() => {});
    const box = h('div', { class: 'tab-body fill console-tab', style: 'padding:0' });
    el.append(box);
    let c = null, shownState = null;
    const draw = () => {
      const e = envById(id);
      if (!e) { c?.destroy?.(); c = null; shownState = null; box.replaceChildren(placeholder('desktop', t('envs.none'), t('envs.noneHint'))); return; }
      document.title = t('con.title', { name: e.name });
      const ttl = document.querySelector('.titlebar .ttl'); if (ttl) ttl.textContent = t('con.title', { name: e.name });
      const run = e.state === 'running';
      if (run === shownState) return;
      shownState = run; c?.destroy?.(); c = null; box.replaceChildren();
      if (!run) { box.append(placeholder('desktop', t('desk.offline'), t('desk.offlineHint'))); return; }
      import('../components/console.js').then(({ mountConsole }) => mountConsole(box, e, { popout: true })).then((x) => { if (shownState) c = x; else x.destroy(); });
    };
    const unsub = subscribe(draw); draw();
    return () => { unsub(); c?.destroy?.(); };
  },
};
