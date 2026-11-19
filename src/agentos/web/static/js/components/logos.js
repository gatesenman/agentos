// Provider / environment logos and empty-state illustrations (owned by M6).
// All inline SVG (no network). logo(kindOrProvider, size) -> <span class="logo"> tile.
import { h } from '../util.js';

const svg = (inner, vb = '0 0 32 32') => `<svg viewBox="${vb}" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">${inner}</svg>`;
const G = (id, a, b) => `<defs><linearGradient id="${id}" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs>`;
let gid = 0;
const LOGOS = {
  // AgentOS mark (same as favicon)
  agentos: (i) => svg(`${G('la' + i, '#7070ff', '#4338ca')}<rect width="32" height="32" rx="7" fill="url(#la${i})"/><path d="M9 22.5 16 8.5l7 14M11.8 18h8.4" stroke="#fff" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/><circle cx="23.5" cy="8.5" r="2.5" fill="#34d399"/>`),
  // Agent OS on this machine: mark + small monitor badge
  'agentos-local': (i) => svg(`${G('ll' + i, '#7070ff', '#4338ca')}<rect width="32" height="32" rx="7" fill="url(#ll${i})"/><path d="M9 22.5 16 8.5l7 14M11.8 18h8.4" stroke="#fff" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/><circle cx="23.5" cy="8.5" r="2.5" fill="#34d399"/>`),
  'agentos-remote': (i) => svg(`${G('lr' + i, '#5b8def', '#3046c5')}<rect width="32" height="32" rx="7" fill="url(#lr${i})"/><path d="M9 22.5 16 8.5l7 14M11.8 18h8.4" stroke="#fff" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/><path d="M20.5 25.5a6 6 0 0 1 8 0M22.6 27.6a3 3 0 0 1 3.8 0" stroke="#9ff0d0" stroke-width="1.6" fill="none" stroke-linecap="round"/>`),
  // Docker-like whale with containers
  docker: (i) => svg(`${G('ld' + i, '#2496ed', '#1766c0')}<rect width="32" height="32" rx="7" fill="url(#ld${i})"/><g fill="#fff"><rect x="7" y="13" width="3.2" height="3" rx=".4"/><rect x="10.7" y="13" width="3.2" height="3" rx=".4"/><rect x="14.4" y="13" width="3.2" height="3" rx=".4"/><rect x="18.1" y="13" width="3.2" height="3" rx=".4"/><rect x="10.7" y="9.6" width="3.2" height="3" rx=".4"/><rect x="14.4" y="9.6" width="3.2" height="3" rx=".4"/><rect x="14.4" y="6.2" width="3.2" height="3" rx=".4"/></g><path d="M4.8 17h20.4c.9-1.6 2.4-2.2 3.4-2-.2 1.3-1 2.3-2.2 2.7C25 23 20.4 26 14.5 26 8.6 26 5.4 22.4 4.8 17z" fill="#fff"/><circle cx="10" cy="20.5" r=".9" fill="#1766c0"/>`),
  // SSH host: terminal prompt on slate
  ssh: (i) => svg(`${G('ls' + i, '#475569', '#1e293b')}<rect width="32" height="32" rx="7" fill="url(#ls${i})"/><rect x="6" y="8" width="20" height="16" rx="2.5" fill="none" stroke="#cbd5e1" stroke-width="1.6"/><path d="m10 13.5 3 2.5-3 2.5M15 19h6" stroke="#5eead4" stroke-width="1.9" fill="none" stroke-linecap="round" stroke-linejoin="round"/>`),
  vm: (i) => svg(`${G('lv' + i, '#8b5cf6', '#5b21b6')}<rect width="32" height="32" rx="7" fill="url(#lv${i})"/><rect x="6.5" y="8" width="19" height="13" rx="2" fill="none" stroke="#fff" stroke-width="1.7"/><path d="M12 25h8M16 21v4" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/><rect x="9.5" y="11" width="6" height="7" rx="1" fill="#ddd6fe"/>`),
  cloud: (i) => svg(`${G('lc' + i, '#fb923c', '#ea580c')}<rect width="32" height="32" rx="7" fill="url(#lc${i})"/><path d="M11 23a5 5 0 1 1 1.1-9.9A6.3 6.3 0 0 1 24 15a4 4 0 0 1-.6 8z" fill="#fff"/>`),
  // preset tiles
  python: (i) => svg(`${G('lp' + i, '#3776ab', '#25507a')}<rect width="32" height="32" rx="7" fill="url(#lp${i})"/><path d="M15.8 6.5c-4.6 0-4.3 2-4.3 2v2.2h4.4v.7H9.8S6.8 11 6.8 15.8s2.6 4.6 2.6 4.6h1.6v-2.2s-.1-2.6 2.6-2.6h4.4s2.5 0 2.5-2.4V9.1s.4-2.6-4.7-2.6zm-2.4 1.4a.8.8 0 1 1 0 1.6.8.8 0 0 1 0-1.6z" fill="#fff"/><path d="M16.2 25.5c4.6 0 4.3-2 4.3-2v-2.2h-4.4v-.7h6.1s3 .4 3-4.4-2.6-4.6-2.6-4.6H21v2.2s.1 2.6-2.6 2.6H14s-2.5 0-2.5 2.4v4.1s-.4 2.6 4.7 2.6zm2.4-1.4a.8.8 0 1 1 0-1.6.8.8 0 0 1 0 1.6z" fill="#ffd43b"/>`),
  node: (i) => svg(`${G('ln' + i, '#5fa04e', '#3c7a30')}<rect width="32" height="32" rx="7" fill="url(#ln${i})"/><path d="M16 5.5 25 10.7v10.6L16 26.5 7 21.3V10.7z" fill="none" stroke="#fff" stroke-width="1.8" stroke-linejoin="round"/><path d="M18.6 13.2c-.4-1-1.4-1.5-2.7-1.5-1.6 0-2.6.8-2.6 2 0 2.8 5.5 1.5 5.5 4.3 0 1.3-1.1 2.2-2.9 2.2-1.6 0-2.7-.7-3-2" fill="none" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>`),
  claude: (i) => svg(`${G('lcl' + i, '#e07a50', '#b9532f')}<rect width="32" height="32" rx="7" fill="url(#lcl${i})"/><g stroke="#fff" stroke-width="2.2" stroke-linecap="round"><path d="M16 7v18M7 16h18M9.6 9.6l12.8 12.8M22.4 9.6 9.6 22.4"/></g><circle cx="16" cy="16" r="3" fill="#fff"/>`),
  browser: (i) => svg(`${G('lb' + i, '#0ea5e9', '#2563eb')}<rect width="32" height="32" rx="7" fill="url(#lb${i})"/><rect x="6" y="8" width="20" height="16" rx="2.5" fill="none" stroke="#fff" stroke-width="1.7"/><path d="M6 12.5h20" stroke="#fff" stroke-width="1.5"/><circle cx="8.8" cy="10.3" r=".9" fill="#fff"/><circle cx="11.4" cy="10.3" r=".9" fill="#fff"/><path d="m15 15.5 5.5 2.2-2.4.9-.9 2.4z" fill="#fde68a" stroke="#fde68a" stroke-width=".8" stroke-linejoin="round"/>`),
  desktop: (i) => svg(`${G('ldk' + i, '#2496ed', '#7c3aed')}<rect width="32" height="32" rx="7" fill="url(#ldk${i})"/><rect x="6" y="8" width="20" height="13" rx="2" fill="none" stroke="#fff" stroke-width="1.7"/><path d="M12 25h8M16 21v4" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/><path d="M8.5 18.5l4-4 3 3 2.5-2.5 5.5 3.5" stroke="#a5f3fc" stroke-width="1.4" fill="none" stroke-linejoin="round"/>`),
  container: (i) => LOGOS.docker(i),
  sandbox: (i) => LOGOS.cloud(i),
  generic: (i) => svg(`${G('lg' + i, '#64748b', '#334155')}<rect width="32" height="32" rx="7" fill="url(#lg${i})"/><rect x="7" y="8" width="18" height="12" rx="2" fill="none" stroke="#fff" stroke-width="1.7"/><path d="M12 24h8" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>`),
};

// Map an env / provider object to a logo key.
export function logoKey(x) {
  const type = x?.provider || x?.type || '';
  const kind = x?.provider_info?.kind || x?.kind || '';
  if (LOGOS[type]) return type;
  if (/docker|podman|container/.test(type)) return 'docker';
  if (/ssh/.test(type)) return 'ssh';
  if (/e2b|daytona|modal|cloud|sandbox/.test(type)) return 'cloud';
  if (/vm|hyperv|vbox|qemu|vmware/.test(type)) return 'vm';
  if (kind === 'agentos') return 'agentos';
  return LOGOS[kind] ? kind : 'generic';
}
export function logo(x, size = 32, cls = '') {
  const k = typeof x === 'string' ? x : logoKey(x);
  return h('span', { class: 'logo ' + cls, style: `width:${size}px;height:${size}px`, 'data-logo': k, html: (LOGOS[k] || LOGOS.generic)(++gid) });
}

// ---------- empty-state illustrations (duotone, follow the accent) ----------
const ILL = {
  envs: `<svg viewBox="0 0 160 120" aria-hidden="true"><ellipse cx="80" cy="108" rx="56" ry="6" fill="var(--ill-shadow)"/>
    <rect x="28" y="22" width="88" height="62" rx="8" fill="var(--ill-b)" stroke="var(--ill-s)"/><rect x="28" y="22" width="88" height="12" rx="6" fill="var(--ill-a)" opacity=".9"/>
    <circle cx="36" cy="28" r="2" fill="#fff"/><circle cx="43" cy="28" r="2" fill="#fff" opacity=".7"/><path d="m40 50 7 6-7 6M53 62h16" stroke="var(--ill-a)" stroke-width="3" fill="none" stroke-linecap="round" stroke-linejoin="round"/>
    <rect x="86" y="52" width="48" height="40" rx="7" fill="var(--ill-c)" stroke="var(--ill-s)"/><path d="M110 64v16M102 72h16" stroke="var(--ill-a)" stroke-width="3.2" stroke-linecap="round"/></svg>`,
  agents: `<svg viewBox="0 0 160 120" aria-hidden="true"><ellipse cx="80" cy="108" rx="52" ry="6" fill="var(--ill-shadow)"/>
    <rect x="46" y="34" width="68" height="56" rx="14" fill="var(--ill-b)" stroke="var(--ill-s)"/><path d="M80 34V20" stroke="var(--ill-s)" stroke-width="3"/><circle cx="80" cy="17" r="5" fill="var(--ill-a)"/>
    <circle cx="66" cy="58" r="6" fill="var(--ill-a)"/><circle cx="94" cy="58" r="6" fill="var(--ill-a)"/><path d="M68 74h24" stroke="var(--ill-a)" stroke-width="3.5" stroke-linecap="round"/>
    <rect x="34" y="52" width="8" height="18" rx="4" fill="var(--ill-c)"/><rect x="118" y="52" width="8" height="18" rx="4" fill="var(--ill-c)"/></svg>`,
  desktop: `<svg viewBox="0 0 160 120" aria-hidden="true"><ellipse cx="80" cy="108" rx="56" ry="6" fill="var(--ill-shadow)"/>
    <rect x="22" y="16" width="116" height="74" rx="8" fill="var(--ill-b)" stroke="var(--ill-s)"/><rect x="30" y="24" width="100" height="58" rx="4" fill="var(--ill-c)"/>
    <path d="M30 66c18-14 30-14 46-4s30 4 54-12v28c0 2-2 4-4 4H34c-2 0-4-2-4-4z" fill="var(--ill-a)" opacity=".55"/><path d="M68 98h24" stroke="var(--ill-s)" stroke-width="4" stroke-linecap="round"/></svg>`,
  search: `<svg viewBox="0 0 160 120" aria-hidden="true"><ellipse cx="80" cy="108" rx="44" ry="6" fill="var(--ill-shadow)"/><circle cx="72" cy="54" r="30" fill="var(--ill-b)" stroke="var(--ill-s)" stroke-width="2"/><circle cx="72" cy="54" r="20" fill="var(--ill-c)"/><path d="m94 76 18 18" stroke="var(--ill-a)" stroke-width="8" stroke-linecap="round"/></svg>`,
  done: `<svg viewBox="0 0 160 120" aria-hidden="true"><ellipse cx="80" cy="108" rx="44" ry="6" fill="var(--ill-shadow)"/><circle cx="80" cy="56" r="38" fill="var(--ill-ok-b)"/><circle cx="80" cy="56" r="28" fill="var(--ill-ok)"/><path d="m66 56 10 10 19-21" stroke="#fff" stroke-width="6" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>`,
};
export function illustration(k, cls = '') { return h('span', { class: 'ill ' + cls, html: ILL[k] || ILL.envs }); }
