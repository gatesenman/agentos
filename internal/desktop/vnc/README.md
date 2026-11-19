# VNC 远程桌面（模块 M4）

节点上的 `vnc` 提供者把隧道桥接到**本机** VNC 服务器（默认 `127.0.0.1:5900`，`agentos node --vnc`
或环境变量 `AGENTOS_VNC` 可改）。浏览器通过内置的 noVNC（`web/static/vendor/novnc`，v1.7.0，MPL-2.0）连接，
无需额外端口暴露：VNC 服务器**只需监听 127.0.0.1**。

安全：浏览器可用 `addr` 参数选择其它端口，但节点只允许回环地址或 `--vnc` 配置的地址本身。
建议仍为 VNC 设置密码（界面会弹出密码输入框，密码不会保存到服务器）。

## Windows
- **TightVNC Server**（推荐）：安装时选择“作为服务运行”，在 *Access Control* 勾选
  *Allow loopback connections*，并在 *Access Control → IP 访问控制* 只允许 `127.0.0.1`；设置主密码。
- **UltraVNC**：`uvnc_settings` 中启用 *Allow Loopback Connections*，勾选 *LoopbackOnly*，设置 VNC 密码，
  以服务方式运行（可在登录界面使用 Ctrl+Alt+Del）。
- 注意：TightVNC/UltraVNC 的 VNC 密码最长 8 位。

## Linux
- 已有 X 桌面：`x11vnc -display :0 -localhost -forever -shared -rfbauth ~/.vnc/passwd`
  （先 `x11vnc -storepasswd`）。
- 无显示器服务器：`Xvfb :1 -screen 0 1920x1080x24 &` 后 `x11vnc -display :1 -localhost -forever -shared`，
  或 TigerVNC `vncserver :1 -localhost yes`（端口 5901，节点使用 `--vnc 127.0.0.1:5901`）。
- Wayland（GNOME）：使用“设置 → 共享 → 远程桌面”（RDP/VNC）或 `wayvnc 127.0.0.1 5900`。

## macOS
- “系统设置 → 通用 → 共享 → 屏幕共享”打开；在“电脑设置”中启用 *VNC 显示程序可以使用密码控制屏幕*
  并设置密码。屏幕共享监听 5900（含回环），节点默认配置即可使用。
- macOS 的 Apple 认证（类型 30）依赖浏览器 WebCrypto，需要 HTTPS（或 localhost）安全上下文；通过 http 访问时请使用 VNC 密码方式。

## 排错
- 页面提示“无法连接 VNC 服务器 …（连接被拒绝）”：节点上的 VNC 服务未启动或端口不对。
- 节点上线时检测不到 VNC，“VNC”按钮会显示“(未检测到)”，但仍可点击重试（每次连接时都会重新检测）。
- 测试：`go test ./internal/desktop/vnc/ ./internal/server/`；UI 冒烟脚本见 `testdata/`。
