import { h, icon } from '../util.js';
import { t } from '../i18n.js';
import { placeholder } from '../components/ui.js';
export default {
  get title() { return t('common.notFound'); },
  render(el) { el.append(placeholder('🧭', t('common.notFound'), location.hash, h('a', { class: 'btn accent', href: '#/envs' }, icon('env'), t('common.goHome')))); },
};
