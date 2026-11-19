# 本机虚拟机（Local VMs）— AgentOS 0.1.5

AgentOS 在 **Windows 宿主**上通过 **Hyper-V**（提供方 `hyperv`）**全自动**创建可直接使用的桌面虚拟机：
自动下载官方镜像 → 生成无人值守应答盘 → 创建 Gen2 VM → 无人值守安装 → 生成登录账户 → 内置终端 / 文件 / 桌面。
用户只需在向导里点一次「创建」。

| | Ubuntu 24.04 桌面 | Windows 11 | Fedora Workstation（高级） | macOS |
|---|---|---|---|---|
| 预设 | `hyperv-ubuntu-2404` | `hyperv-windows11` | `hyperv-fedora` | `hyperv-macos`（置灰）→ `ssh-mac` |
| 镜像 | ubuntu-24.04.5.1-desktop-amd64.iso（官方，SHA-256 校验） | Win11 25H2 企业版评估版（微软直链，zh-CN / en-US，90 天评估） | Fedora 44 Everything netinst | 不提供（Apple 许可） |
| 无人值守 | autoinstall（CIDATA 应答盘 + GRUB 键入启动参数） | autounattend.xml（跳过 TPM/联网检查、本地管理员、自动登录） | kickstart（OEMDRV 应答盘） | — |
| 桌面 | 内置桌面页签（x11vnc 仅本机监听，经 SSH 隧道）+ Hyper-V 窗口 | 系统远程桌面（mstsc，已预填账户）+ 内置桌面页签（TightVNC 仅本机，经 SSH 隧道）+ Hyper-V 窗口 | Hyper-V 窗口（GNOME 仅 Wayland，无内置 VNC） | 远程 Mac：SSH + 屏幕共享 |
| 终端 / 文件 | SSH（生成 ed25519 密钥，主机密钥预置并固定） | OpenSSH Server（管理员 authorized_keys） | SSH | SSH |
| 账户 | `agent` + 随机密码（界面可显示/复制） | 同左（本地管理员） | 同左 | 用户填写 |

## 流程与阶段（`config.phase`）

`download` → `create` → `install` → `configure` → `ready`。进度写入 `/envs/{id}/logs`，界面「概览」显示进度卡片与实时画面（`GET /envs/{id}/screenshot`，来自 Hyper-V 缩略图，安装期间也可用）。

- ISO 缓存：`<store_root>\iso`（默认 `D:\AgentOS-VMs\iso`），断点续传（`.partial` + HTTP Range），校验 SHA-256 后写 `.sha256ok` 标记；删除虚拟机**不会**删除 ISO。
- 应答盘：纯 Go 写出的 ISO 9660 + Joliet（无外部工具），安装完成后弹出并删除。
- 启动顺序：硬盘 → 安装光盘 → 网络。空盘自动落到安装盘；装好后从硬盘启动；中途重启/服务重启可继续（`CreateResumer`，服务重启后自动续跑）。
- 超时：整体 4 小时（`ProvisionTimeouter`）；客户机就绪等待 Ubuntu/Fedora 75 分钟、Windows 90 分钟。
- 配置在每个阶段即时保存（`env.SaveConfig`），中途删除也能清理干净；**创建失败的环境删除时也会清理残留 VM 与磁盘**（0.1.5 修复）。

## 网络不好 / 需要代理时

- Ubuntu：autoinstall 设置 `apt.fallback: offline-install`，连不上软件源也能用 ISO 自带软件**离线装完**；需要联网的组件（x11vnc、Hyper-V KVP 工具、中文字体/输入法）由 `agentos-packages.service` 在每次开机联网后自动补装，装好即停用。
- 可选 `install_proxy`（高级设置，如 `http://192.168.1.10:7890`）：Ubuntu 安装器与 apt、Fedora kickstart 软件源都会走该代理（Windows 不需要联网，OpenSSH/TightVNC 安装包随应答盘提供）。

## 能力矩阵（hyperv）

| Capability | 支持 | 说明 |
|---|---|---|
| lifecycle | ✓ | Create / Start / Stop（先优雅关机 120 s，再强制）/ Delete（Remove-VM + 删除整个 VM 目录） |
| exec / files | ✓ | 经客户机 SSH（复用 ssh 提供方） |
| desktop.vnc | ✓ | Ubuntu / Windows：SSH 隧道到客户机 127.0.0.1:5900 |
| screenshot | ✓ | `GET /envs/{id}/screenshot?w=` → PNG |
| open native | ✓ | `POST /envs/{id}/open {"kind":"console"|"rdp"}` → vmconnect / mstsc（cmdkey 预填凭据） |
| metrics | ✓ | Hyper-V CPU/内存 + VHDX 占用；连上客户机后补充客户机指标 |
| CapMacGuest | false | Apple 许可：Windows 宿主不能正版运行 macOS |

## 检测与权限

1. 探测 `Get-Command Get-VM` + `Get-VMHost`（真正调用一次，确认有权限）。
2. 没有权限：卡片置灰并说明——以管理员运行 AgentOS，或加入「Hyper-V Administrators」组。**0.1.5 安装程序会自动把当前用户加入该组**（重新登录后生效，此后无需管理员运行）。
3. **Windows 预览版过期检测（0.1.5 新增）**：读取 `Kernel-ExpirationDate`；Insider 版本过期后 Hyper-V 无法加载虚拟机固件（`Microsoft Virtual BIOS … 0x80090006 无效签名`），任何 VM 都起不来。此时卡片置灰并提示升级 Windows；`Start-VM` 的此类错误也会翻译成可操作的中文提示。
4. PowerShell 输出强制 UTF-8，本地化错误信息不再乱码；只保留错误正文，去掉「所在位置…」噪音。

## macOS

Apple 软件许可只允许在 Apple 硬件上虚拟化 macOS，因此 Windows 上的 macOS 卡片始终置灰，并提供合规替代：**「远程 Mac（SSH + 屏幕共享）」**（预设 `ssh-mac`，在 Mac 上开启「远程登录」和「屏幕共享」后填写地址即可，桌面走 VNC 5900）。不提供、不链接任何 macOS 镜像或黑苹果方案。

## 配置字段（摘录）

`guest_os`, `language`（zh-CN|en-US）, `unattended`（默认 true）, `guest_user`, `guest_password`（x-secret）, `ssh_private_key`（x-secret）, `ssh_public_key`, `host_key_private`（x-secret）, `host_key`（指纹）, `hostname`, `mac_address`（静态，便于 ARP 找 IP）, `phase`, `install_seconds`, `guest_address`, `enable_vnc`, `vnc_port`, `store_root`, `switch_name`（默认 Default Switch）。
