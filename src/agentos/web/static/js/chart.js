// Tiny canvas chart library (owned by M6, no dependencies).
//   sparkline(canvas, values, {color, max})
//   new LineChart(container, {series:[{label, color, fmt}], max?, yFmt?, fill?})
//     .setData(times:number[] (ms), values:number[][] per series)
import { h } from './util.js';

const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
export const cssVar = css;

function fit(canvas) {
  const r = canvas.getBoundingClientRect(), dpr = window.devicePixelRatio || 1;
  const w = Math.max(1, Math.round(r.width)), hh = Math.max(1, Math.round(r.height));
  if (canvas.width !== w * dpr || canvas.height !== hh * dpr) { canvas.width = w * dpr; canvas.height = hh * dpr; }
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, hh);
  return { ctx, w, h: hh };
}

function alpha(color, a) {
  if (color.startsWith('#') && color.length === 7) {
    const n = parseInt(color.slice(1), 16);
    return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
  }
  return color;
}

export function sparkline(canvas, values, { color = css('--chart-cpu'), max, min = 0 } = {}) {
  const { ctx, w, h: H } = fit(canvas);
  const v = (values || []).filter((x) => x != null && !isNaN(x));
  if (v.length < 2) {
    ctx.strokeStyle = css('--chart-grid'); ctx.lineWidth = 1; ctx.setLineDash([3, 3]);
    ctx.beginPath(); ctx.moveTo(0, H - 1.5); ctx.lineTo(w, H - 1.5); ctx.stroke();
    return;
  }
  const hi = max ?? Math.max(...v, 1), lo = min;
  const pad = 2, xs = (i) => (i / (v.length - 1)) * w, ys = (x) => H - pad - ((Math.min(x, hi) - lo) / (hi - lo || 1)) * (H - pad * 2);
  const g = ctx.createLinearGradient(0, 0, 0, H);
  g.addColorStop(0, alpha(color, 0.35)); g.addColorStop(1, alpha(color, 0));
  ctx.beginPath(); ctx.moveTo(0, H);
  v.forEach((x, i) => ctx.lineTo(xs(i), ys(x)));
  ctx.lineTo(w, H); ctx.closePath(); ctx.fillStyle = g; ctx.fill();
  ctx.beginPath(); v.forEach((x, i) => (i ? ctx.lineTo(xs(i), ys(x)) : ctx.moveTo(0, ys(x))));
  ctx.strokeStyle = color; ctx.lineWidth = 1.5; ctx.lineJoin = 'round'; ctx.stroke();
  ctx.beginPath(); ctx.arc(xs(v.length - 1), ys(v[v.length - 1]), 2.2, 0, Math.PI * 2); ctx.fillStyle = color; ctx.fill();
}

export class LineChart {
  constructor(container, opts) {
    this.opts = { fill: true, yFmt: (v) => String(Math.round(v)), ...opts };
    this.canvas = h('canvas');
    this.tip = h('div', { class: 'tip' });
    this.nodata = h('div', { class: 'nodata' }, opts.emptyText || '');
    this.el = h('div', { class: 'chart', style: opts.height ? `height:${opts.height}px` : null }, this.canvas, this.tip, this.nodata);
    container.append(this.el);
    this.times = []; this.values = []; this.hover = -1;
    this.canvas.addEventListener('mousemove', (e) => this.onMove(e));
    this.canvas.addEventListener('mouseleave', () => { this.hover = -1; this.tip.style.display = 'none'; this.draw(); });
    this.ro = new ResizeObserver(() => this.draw()); this.ro.observe(this.el);
    this.onTheme = () => this.draw(); window.addEventListener('themechange', this.onTheme);
  }
  destroy() { this.ro.disconnect(); window.removeEventListener('themechange', this.onTheme); }
  setData(times, values) { this.times = times; this.values = values; this.draw(); }
  push(time, vals, cap = 120) {
    this.times.push(time); vals.forEach((v, i) => (this.values[i] = this.values[i] || []).push(v));
    if (this.times.length > cap) { this.times.shift(); this.values.forEach((s) => s.shift()); }
    this.draw();
  }
  layout() {
    const r = this.el.getBoundingClientRect();
    return { L: 52, R: 10, T: 10, B: 22, W: r.width, H: r.height };
  }
  draw() {
    if (!this.el.isConnected) return;
    const { ctx, w, h: H } = fit(this.canvas);
    const { L, R, T, B } = this.layout();
    const n = this.times.length;
    this.nodata.style.display = n < 2 ? 'flex' : 'none';
    const grid = css('--chart-grid'), muted = css('--muted');
    let hi = this.opts.max;
    if (hi == null) { hi = 0; for (const s of this.values) for (const v of s || []) if (v > hi) hi = v; hi = niceMax(Math.max(hi, this.opts.minMax || 1)); }
    const pw = w - L - R, ph = H - T - B;
    ctx.font = '11px ' + getComputedStyle(document.body).fontFamily; ctx.fillStyle = muted; ctx.textBaseline = 'middle';
    ctx.strokeStyle = grid; ctx.lineWidth = 1;
    for (let i = 0; i <= 4; i++) {
      const y = Math.round(T + ph - (ph * i) / 4) + 0.5;
      ctx.beginPath(); ctx.moveTo(L, y); ctx.lineTo(w - R, y); ctx.stroke();
      if (n >= 2) { ctx.textAlign = 'right'; ctx.fillText(this.opts.yFmt((hi * i) / 4), L - 8, y); }
    }
    if (n < 2) return;
    const t0 = this.times[0], t1 = this.times[n - 1], span = t1 - t0 || 1;
    const xs = (tm) => L + ((tm - t0) / span) * pw, ys = (v) => T + ph - (Math.min(v, hi) / hi) * ph;
    ctx.textAlign = 'center'; ctx.textBaseline = 'top';
    const ticks = Math.max(2, Math.min(6, Math.floor(pw / 90)));
    for (let i = 0; i <= ticks; i++) {
      const tm = t0 + (span * i) / ticks;
      ctx.textAlign = i === 0 ? 'left' : i === ticks ? 'right' : 'center';
      ctx.fillText(new Date(tm).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false }), xs(tm), H - B + 6);
    }
    this.opts.series.forEach((s, si) => {
      const vals = this.values[si] || [], color = s.color ? css(s.color) : css('--chart-cpu');
      if (this.opts.fill) {
        const g = ctx.createLinearGradient(0, T, 0, T + ph);
        g.addColorStop(0, alpha(color, 0.28)); g.addColorStop(1, alpha(color, 0.02));
        ctx.beginPath(); ctx.moveTo(xs(this.times[0]), T + ph);
        vals.forEach((v, i) => ctx.lineTo(xs(this.times[i]), ys(v || 0)));
        ctx.lineTo(xs(this.times[n - 1]), T + ph); ctx.closePath(); ctx.fillStyle = g; ctx.fill();
      }
      ctx.beginPath();
      vals.forEach((v, i) => (i ? ctx.lineTo(xs(this.times[i]), ys(v || 0)) : ctx.moveTo(xs(this.times[i]), ys(v || 0))));
      ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.lineJoin = 'round'; ctx.setLineDash(s.dash || []); ctx.stroke(); ctx.setLineDash([]);
    });
    if (this.hover >= 0 && this.hover < n) {
      const x = xs(this.times[this.hover]);
      ctx.strokeStyle = muted; ctx.globalAlpha = 0.5; ctx.beginPath(); ctx.moveTo(x, T); ctx.lineTo(x, T + ph); ctx.stroke(); ctx.globalAlpha = 1;
      this.opts.series.forEach((s, si) => {
        const v = (this.values[si] || [])[this.hover]; if (v == null) return;
        ctx.beginPath(); ctx.arc(x, ys(v), 3.5, 0, Math.PI * 2); ctx.fillStyle = s.color ? css(s.color) : css('--chart-cpu'); ctx.fill();
        ctx.strokeStyle = css('--panel'); ctx.lineWidth = 1.5; ctx.stroke();
      });
    }
  }
  onMove(e) {
    const n = this.times.length; if (n < 2) return;
    const r = this.canvas.getBoundingClientRect(), { L, R } = this.layout();
    const x = e.clientX - r.left, t0 = this.times[0], span = this.times[n - 1] - t0 || 1;
    const tm = t0 + ((x - L) / (r.width - L - R)) * span;
    let best = 0; for (let i = 1; i < n; i++) if (Math.abs(this.times[i] - tm) < Math.abs(this.times[best] - tm)) best = i;
    this.hover = best; this.draw();
    this.tip.replaceChildren(h('div', { class: 'muted' }, new Date(this.times[best]).toLocaleTimeString([], { hour12: false })),
      ...this.opts.series.map((s, si) => h('div', {}, h('span', { style: `color:${s.color ? css(s.color) : ''}` }, '● '), s.label + ': ',
        h('b', {}, (s.fmt || this.opts.yFmt)((this.values[si] || [])[best] ?? 0)))));
    this.tip.style.display = 'block';
    const tw = this.tip.offsetWidth;
    this.tip.style.left = Math.min(Math.max(0, x + 12), r.width - tw) + 'px'; this.tip.style.top = '6px';
  }
}

function niceMax(v) {
  const p = Math.pow(10, Math.floor(Math.log10(v))), m = v / p;
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * p;
}
