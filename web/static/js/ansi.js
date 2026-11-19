// ANSI SGR → DOM renderer with search highlighting (owned by M6).
// Supports 16 colours (fg/bg, bright), 256-colour and truecolor (38/48;5|2),
// bold/dim/italic/underline and resets. Other escape sequences are stripped.

// eslint-disable-next-line no-control-regex
const ESC_RE = /\x1b\[([0-9;?]*)([A-Za-z])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78DEHMc]/g;
// eslint-disable-next-line no-control-regex
const CTRL_RE = /[\x00-\x08\x0b-\x1f\x7f]/g;

const C16 = ['#000000', '#cd3131', '#0dbc79', '#e5e510', '#2472c8', '#bc3fbc', '#11a8cd', '#e5e5e5',
  '#666666', '#f14c4c', '#23d18b', '#f5f543', '#3b8eea', '#d670d6', '#29b8db', '#ffffff'];
function c256(n) {
  if (n < 16) return C16[n];
  if (n >= 232) { const v = 8 + (n - 232) * 10; return `rgb(${v},${v},${v})`; }
  n -= 16; const s = [0, 95, 135, 175, 215, 255];
  return `rgb(${s[Math.floor(n / 36)]},${s[Math.floor(n / 6) % 6]},${s[n % 6]})`;
}

export function stripAnsi(s) { return s.replace(ESC_RE, '').replace(CTRL_RE, ''); }

// parse → [{text, style:{fg,bg,cls[]}}]
export function parseAnsi(s) {
  const out = []; let st = { fg: null, bg: null, b: false, d: false, i: false, u: false };
  let last = 0; ESC_RE.lastIndex = 0; let m;
  const push = (text) => { text = text.replace(CTRL_RE, ''); if (text) out.push({ text, st: { ...st } }); };
  while ((m = ESC_RE.exec(s))) {
    push(s.slice(last, m.index)); last = ESC_RE.lastIndex;
    if (m[2] !== 'm') continue;
    const codes = (m[1] || '0').split(';').map((x) => (x === '' ? 0 : +x));
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) st = { fg: null, bg: null, b: false, d: false, i: false, u: false };
      else if (c === 1) st.b = true; else if (c === 2) st.d = true; else if (c === 3) st.i = true; else if (c === 4) st.u = true;
      else if (c === 22) st.b = st.d = false; else if (c === 23) st.i = false; else if (c === 24) st.u = false;
      else if ((c >= 30 && c <= 37) || (c >= 90 && c <= 97)) st.fg = 'a-' + c;
      else if ((c >= 40 && c <= 47) || (c >= 100 && c <= 107)) st.bg = 'a-' + c;
      else if (c === 39) st.fg = null; else if (c === 49) st.bg = null;
      else if (c === 38 || c === 48) {
        let col = null;
        if (codes[i + 1] === 5) { col = c256(codes[i + 2] || 0); i += 2; }
        else if (codes[i + 1] === 2) { col = `rgb(${codes[i + 2] || 0},${codes[i + 3] || 0},${codes[i + 4] || 0})`; i += 4; }
        if (c === 38) st.fg = col ? { css: col } : null; else st.bg = col ? { css: col } : null;
      }
    }
  }
  push(s.slice(last));
  return out;
}

// Render text into `parent`, wrapping occurrences of `re` (global RegExp) in <mark>.
// Returns number of matches.
export function renderAnsi(parent, text, re) {
  const segs = parseAnsi(text);
  let count = 0;
  // Highlight against the plain text so matches can span segments.
  const plain = segs.map((s) => s.text).join('');
  const ranges = [];
  if (re) { re.lastIndex = 0; let m; while ((m = re.exec(plain))) { if (!m[0].length) { re.lastIndex++; continue; } ranges.push([m.index, m.index + m[0].length]); } }
  count = ranges.length;
  let pos = 0, ri = 0;
  for (const seg of segs) {
    const target = seg.st.fg || seg.st.bg || seg.st.b || seg.st.d || seg.st.i || seg.st.u ? styled(seg.st) : null;
    const host = target || parent;
    let off = 0; const txt = seg.text;
    while (off < txt.length) {
      const abs = pos + off;
      while (ri < ranges.length && ranges[ri][1] <= abs) ri++;
      const r = ranges[ri];
      if (r && r[0] <= abs) {
        const end = Math.min(txt.length, r[1] - pos);
        const mk = document.createElement('mark'); mk.textContent = txt.slice(off, end); host.append(mk); off = end;
      } else {
        const end = r ? Math.min(txt.length, r[0] - pos) : txt.length;
        host.append(txt.slice(off, end)); off = end;
      }
    }
    if (target) parent.append(target);
    pos += txt.length;
  }
  return count;
}

function styled(st) {
  const sp = document.createElement('span'); const cls = [];
  if (typeof st.fg === 'string') cls.push(st.fg); else if (st.fg) sp.style.color = st.fg.css;
  if (typeof st.bg === 'string') cls.push(st.bg); else if (st.bg) sp.style.background = st.bg.css;
  if (st.b) cls.push('a-b'); if (st.d) cls.push('a-d'); if (st.i) cls.push('a-i'); if (st.u) cls.push('a-u');
  if (cls.length) sp.className = cls.join(' ');
  return sp;
}
