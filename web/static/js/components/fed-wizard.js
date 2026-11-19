// Wizard: 接入外部智能体 (A2A 1.0 / ACP). Paste URL + optional token → discover → preview → save.
import { h, icon, toast, dialog } from '../util.js';
import { api } from '../api.js';
import { t } from '../i18n.js';

export function openFedWizard() {
  const state = { endpoint: '', token: '', protocol: '', probe: null, name: '' };
  const endpoint = h('input', { type: 'url', placeholder: 'https://agent.example.com/.well-known/agent-card.json', oninput: () => { state.endpoint = endpoint.value.trim(); } });
  const token = h('input', { type: 'password', autocomplete: 'off', placeholder: t('fed.tokenPh'), oninput: () => { state.token = token.value; } });
  const proto = h('select', { onchange: () => { state.protocol = proto.value; } },
    h('option', { value: '' }, t('fed.autoDetect')),
    h('option', { value: 'a2a' }, 'A2A 1.0'),
    h('option', { value: 'acp' }, 'ACP'));
  const name = h('input', { oninput: () => { state.name = name.value; } });
  const preview = h('div', { class: 'fed-preview' });

  function renderPreview() {
    if (!state.probe) { preview.replaceChildren(); return; }
    if (!state.probe.ok) {
      preview.replaceChildren(h('div', { class: 'alert err' }, icon('alert'), state.probe.error || t('fed.discoverFail')));
      return;
    }
    const label = state.probe.protocol === 'acp' ? 'ACP' : 'A2A';
    name.value = state.name || state.probe.name || '';
    state.name = name.value;
    preview.replaceChildren(
      h('div', { class: 'row', style: 'gap:8px;align-items:center;margin-bottom:8px' },
        h('span', { class: 'pill ok' }, h('i'), label), h('b', {}, state.probe.name || '—')),
      h('p', { class: 'muted small', style: 'margin:0 0 8px' }, state.probe.description || ''),
      h('div', { class: 'field' }, h('label', {}, t('form.name')), name),
      (state.probe.capabilities || []).length
        ? h('div', { class: 'row', style: 'flex-wrap:wrap;gap:6px;margin-top:8px' },
          ...(state.probe.capabilities || []).slice(0, 12).map((c) => h('span', { class: 'chip' }, c)))
        : null,
    );
  }

  const body = h('div', { class: 'col', style: 'gap:12px' },
    h('p', { class: 'muted small', style: 'margin:0' }, t('fed.hint')),
    h('div', { class: 'field' }, h('label', {}, t('fed.endpoint'), h('span', { class: 'req' }, ' *')), endpoint),
    h('div', { class: 'field' }, h('label', {}, t('fed.token')), token),
    h('div', { class: 'field' }, h('label', {}, t('fed.protocolForce')), proto),
    preview,
  );

  const d = dialog({
    title: t('fed.title'),
    sub: 'A2A · ACP',
    body,
    wide: true,
    buttons: [
      { text: t('common.cancel'), id: 'cancel', onClick: (close) => close(null) },
      'sp',
      { text: t('fed.discover'), icon: 'search', id: 'discover', onClick: async () => {
        state.endpoint = endpoint.value.trim();
        if (!state.endpoint) { toast(t('fed.endpoint'), true); return false; }
        try {
          const payload = { endpoint: state.endpoint };
          if (token.value) { payload.token = token.value; payload.auth_scheme = 'bearer'; }
          if (proto.value) payload.protocol = proto.value;
          state.probe = await api('POST', '/federation/agents/discover', payload);
          if (state.probe?.ok) state.name = state.probe.name || state.name;
          else toast(state.probe?.error || t('fed.discoverFail'), true);
          renderPreview();
        } catch (e) {
          state.probe = { ok: false, error: e.message };
          renderPreview();
          toast(e.message, true);
        }
        return false;
      } },
      { text: t('fed.save'), icon: 'check', accent: true, id: 'save', onClick: async (close) => {
        if (!state.probe?.ok) { toast(t('fed.discoverFail'), true); return false; }
        try {
          const created = await api('POST', '/federation/agents', {
            endpoint: state.endpoint || endpoint.value.trim(),
            token: token.value || undefined,
            auth_scheme: token.value ? 'bearer' : 'none',
            protocol: state.probe.protocol,
            name: name.value || state.probe.name,
            fetch_card: true,
          });
          toast(t('fed.saved'));
          close(created);
        } catch (e) {
          toast(e.message, true);
          return false;
        }
      } },
    ],
  });
  return d.done;
}
