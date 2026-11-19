// REST + WebSocket client (owned by M6). See docs/api.md and docs/api-envs.md.
// `?mock=1` in the page URL swaps in the in-browser mock backend (js/mock.js).
export class ApiError extends Error {
  constructor(status, body) { super(body?.error || ('HTTP ' + status)); this.status = status; this.code = body?.code; }
}

export const MOCK = new URLSearchParams(location.search).get('mock') === '1';
export const APP_MODE = new URLSearchParams(location.search).get('app') === '1';
let mock = null;
export async function initMock() { if (MOCK && !mock) mock = await import('./mock.js'); }

// Missing endpoints (404 before the server implements them) are remembered so
// the UI can degrade without hammering them.
export const missing = new Set();
export const isMissing = (e) => e instanceof ApiError && (e.status === 405 || (e.status === 404 && (e.message === 'no such endpoint' || e.code === 'not_found_route')));

export async function apiFetch(path, opts = {}) {
  if (mock) return mock.fetch(path, opts);
  return fetch('/api/v1' + path, { credentials: 'same-origin', ...opts });
}

// Probe session without redirecting (used at boot to avoid a login flash).
export async function authMe() {
  const res = await apiFetch('/auth/me', { method: 'GET' });
  if (res.status === 401) return null;
  if (!res.ok) return null;
  try { return await res.json(); } catch { return { authenticated: true }; }
}

// tryLocalAuth: passwordless session when the page is served from loopback
// (AgentOS.exe WebView / local browser). Returns true on success.
export async function tryLocalAuth() {
  try {
    const res = await apiFetch('/auth/local', { method: 'POST' });
    return res.ok;
  } catch { return false; }
}


export async function api(method, path, body, opts = {}) {
  const res = await apiFetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
    ...opts,
  });
  if (res.status === 401 && !path.startsWith('/auth/login')) {
    if (!location.hash.startsWith('#/login')) location.hash = '#/login';
    throw new ApiError(401, { error: 'unauthorized', code: 'unauthorized' });
  }
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null; try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!res.ok) {
    // A 404 without our JSON error body means "route not implemented on this server".
    const err = new ApiError(res.status, data || { error: res.status === 404 ? 'not implemented' : text.slice(0, 200), code: res.status === 404 && !data ? 'not_found_route' : undefined });
    if (isMissing(err)) missing.add(method + ' ' + path.split('?')[0]);
    throw err;
  }
  return data;
}

export function wsURL(path) {
  return (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/v1' + path;
}
// openWS returns a WebSocket (or a mock with the same surface in mock mode).
export function openWS(path) {
  if (mock) return mock.socket(path);
  const ws = new WebSocket(wsURL(path)); ws.binaryType = 'arraybuffer'; return ws;
}

// Global event stream with auto-reconnect. on(type, fn) returns an unsubscribe fn.
// Pseudo events: 'reconnect' (stream re-opened, refetch), 'conn' (payload: bool connected).
const handlers = new Map();
let sock = null, retry = null, everOpen = false, stopped = false, backoff = 1000;
export let connected = false;
export function on(type, fn) {
  if (!handlers.has(type)) handlers.set(type, new Set());
  handlers.get(type).add(fn);
  stopped = false;
  connectEvents();
  return () => handlers.get(type)?.delete(fn);
}
export const emit = (type, payload, ev) => { for (const fn of [...(handlers.get(type) || [])]) { try { fn(payload, ev); } catch (e) { setTimeout(() => { throw e; }); } } };
function setConn(v) { if (connected !== v) { connected = v; emit('conn', v); } }
function connectEvents() {
  if (sock || stopped) return;
  const s = sock = openWS('/events');
  s.onopen = () => { backoff = 1000; setConn(true); if (everOpen) emit('reconnect'); everOpen = true; };
  s.onmessage = (m) => {
    let ev; try { ev = JSON.parse(m.data); } catch { return; }
    emit(ev.type, ev.payload, ev); emit('*', ev.payload, ev);
  };
  s.onerror = () => {};
  s.onclose = () => {
    if (sock === s) sock = null; setConn(false); clearTimeout(retry);
    if (!stopped) { const d = backoff; backoff = Math.min(backoff * 2, 30000); retry = setTimeout(connectEvents, d); }
  };
}
export function reopenEvents() { if (stopped) { stopped = false; backoff = 1000; connectEvents(); } }
export function closeEvents() { stopped = true; clearTimeout(retry); if (sock) { const s = sock; sock = null; s.close(); } everOpen = false; setConn(false); }
