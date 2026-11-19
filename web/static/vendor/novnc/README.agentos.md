# Vendored noVNC

- Upstream: https://github.com/novnc/noVNC, tag **v1.7.0** (unmodified).
- Included: `core/**` (MPL-2.0), `vendor/pako/**` (MIT, see `vendor/pako/LICENSE`),
  `LICENSE.txt`, `LICENSE.MPL-2.0`, `AUTHORS`.
- Used as plain ES modules, no build step: `import RFB from '/vendor/novnc/core/rfb.js'`.
- To update: replace `core/` and `vendor/pako/` from a newer upstream tag; keep the licence files.
