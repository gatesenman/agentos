# AgentOS 0.1.5 发布说明

## 本版重点：本机虚拟机全自动安装（零交互）

| 预设 | 结果 | 体验 |
|---|---|---|
| `hyperv-ubuntu-2404` | ISO 自动下载+SHA256 校验 → autoinstall → 中文桌面自动登录 | GNOME Xorg 会话、中文字体/输入法、Asia/Shanghai 时区、无"新版本升级"弹窗；x11vnc(仅 127.0.0.1，经 SSH 隧道) 供内嵌桌面 |
| `hyperv-windows11` | 自动下载微软评估版 ISO → autounattend（跳过 TPM/OOBE/联网账户）| 中文界面、自动登录、RDP 开启、OpenSSH Server、TightVNC；`打开` 按钮直接拉起 mstsc |
| `hyperv-fedora` | kickstart 自动安装 | GNOME 图形界面自动登录（graphical.target） |
| `hyperv-macos` | 置灰（Apple 许可）| 一键切换到 `ssh-mac`：远程 Mac（SSH + 屏幕共享） |

- 自动生成账户（默认 `agent`，随机强密码）、ed25519 密钥与主机密钥钉扎；Exec/文件经 SSH。
- 截图（Hyper-V 缩略图）、`打开`（vmconnect / mstsc）。
- 可选 `install_proxy`（受限网络下装机），无网也能完成 Ubuntu 离线安装，软件包由 `agentos-packages.service` 联网后自动补齐（失败 60 s 重试）。
- Windows ComputerName 截断到 ≤15 字符（默认 `AGENTOS-W-XXXX`），并关闭 VBS/DeviceGuard，避免 specialize 阶段「计算机意外地重新启动」与嵌套虚拟化崩溃。
- 检测过期的 Windows Insider 宿主（`Kernel-ExpirationDate`），Start-VM 0x80090006 给出可读提示：请先 Windows 更新并重启。
- 失败状态的环境删除时会清理残留 VM；PowerShell 错误 UTF-8 化、去噪；安装包把当前用户加入 Hyper-V Administrators。

## 测试

单元/集成测试：`go test ./...`。端到端：在用户 PC（WSL2 内 KVM/QEMU，使用 AgentOS 生成的同一份种子盘）完成 Ubuntu / Fedora / Windows 11 安装验证，详见交付的《测试报告-0.1.5.zip》。宿主 Insider 版本已过期（2026-08-11 UTC），真实 Hyper-V 启动被系统拒绝，需更新 Windows 后复测。

## 已知限制

- Hyper-V 专属路径（Msvm_Keyboard 按键、缩略图、KVP、vmconnect/mstsc）未在本机实测（宿主过期）。
- Ubuntu 完全离线时安装器镜像探测约多 17 分钟。
- Fedora 未内置 VNC（用 vmconnect）。
