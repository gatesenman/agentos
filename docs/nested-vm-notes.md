# Nested VM / WinBoat notes (0.1.22+)

## Remote desktop (桌面 / 远程桌面)

Opening the in-app desktop tab used to **auto-connect** a WebView2 + screen/VNC stream.
On nested guests (WinBoat Win11 ≈ 4 GiB RAM, TCG) that often OOM-killed the host QEMU.

Mitigations in 0.1.22:

- Console and Desktop pages **do not auto-connect**; user must click 连接.
- Low-RAM heuristic (`navigator.deviceMemory ≤ 4`): prefer low fps/quality/scale; show warning.
- Screen encode capped to **MaxEncodePixels = 1280×720** (`CapScaleForSize`).
- Console disconnects the stream when the document is hidden (`visibilitychange`).

## Docker presets inside nested Windows

Guest often has **no** `\\.\pipe\docker_engine` (Docker Desktop not installed / not nested-safe).
AgentOS marks the Docker preset unavailable, points HelpURL at Docker Desktop install docs,
and sets **AltPreset = `agentos-desktop`** (Windows 本机 Agent OS). Prefer that or SSH
rather than nesting Docker Desktop inside WinBoat (disk + nested virt cost).
