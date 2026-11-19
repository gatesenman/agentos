# AgentOS

**AgentOS** 是一个面向本地 AI Agent 的可视化管理工具（"Agent 操作系统"）：在一台或多台机器上托管、启动、停止、
监控 Agent 进程，实时查看日志与资源占用，并可通过浏览器远程访问节点桌面。单个 Go 可执行文件，跨平台
（Windows / Linux / macOS），提供 Windows 安装包。

## 架构

```
 浏览器 (Web UI, zh-CN)                       agentos node (每台宿主机)
   │  REST / WebSocket                          ├─ supervisor：Agent 进程 (独立工作区)
   ▼                                            ├─ logs：日志环形缓冲 + 文件
 agentos server ──── 控制通道 WS (JSON) ─────────┤─ metrics：主机/进程资源
   ├─ Web UI (go:embed)                         └─ desktop：VNC 桥接 / 内置屏幕流
   ├─ REST API /api/v1
   ├─ SQLite (纯 Go, modernc.org/sqlite)
   └─ 远程桌面隧道代理 (浏览器 WS ⇄ 节点 WS)
```

- `agentos server`：管理控制台 + API + Web UI。默认 `--with-node=true`，同时在进程内运行一个本地节点（单机一体模式）。
- `agentos node`：Agent 宿主守护进程，主动连接 server（支持 NAT 后的机器），执行/监控 Agent，转发日志与指标，提供远程桌面隧道。
- Agent = 一个配置好的进程（命令、参数、环境变量、工作目录、重启策略），运行在 `<数据目录>/node/agents/<id>/workspace`。

详细文档：[REST API](docs/api.md) · [Server⇄Node 协议](docs/protocol.md) · [模块划分](docs/modules.md)


## 本机虚拟机（0.1.5，Windows Hyper-V，全自动）

在「新建虚拟电脑」向导中点一次即可得到**装好的桌面系统**：自动下载官方镜像 → 无人值守安装 → 生成登录账户（界面可查看/复制密码）→ 内置终端、文件与桌面。

| 预设 | 安装方式 | 桌面 | 终端/文件 |
|---|---|---|---|
| Ubuntu 24.04 桌面 | autoinstall（自动） | 内置桌面页签（VNC 经 SSH 隧道）/ Hyper-V 窗口 | SSH |
| Windows 11 桌面（企业版评估版） | autounattend（自动，ISO 自动下载） | 系统远程桌面（已预填账户）/ 内置桌面页签 / Hyper-V 窗口 | OpenSSH |
| Fedora Workstation（高级） | kickstart（自动） | Hyper-V 窗口 | SSH |
| macOS | **不可用**（Apple 许可）→ 提供「远程 Mac（SSH + 屏幕共享）」 | 屏幕共享 VNC | SSH |

- 默认 8 vCPU / 16 GB；磁盘与 ISO 缓存在 `D:\AgentOS-VMs`（无 D: 则 `%LOCALAPPDATA%\AgentOS\vms`）。
- 需启用 Hyper-V；安装程序会把当前用户加入「Hyper-V Administrators」（重新登录后免管理员）。
- 已过期的 Windows 预览版（Insider）上 Hyper-V 无法启动任何虚拟机，AgentOS 会检测并提示升级。
- 详见 [docs/local-vms.md](docs/local-vms.md)。**不会**分发任何 Windows/macOS 镜像。

## 快速开始

```bash
make build                       # 需要 Go 1.24+（本机: /usr/local/go）
./bin/agentos server --admin-password admin123
# 打开 http://localhost:7800 ，用 admin123 登录
```

首次启动若未指定密码，会生成随机初始密码并打印到日志，同时写入 `<数据目录>/server/initial-admin-password.txt`。
数据目录默认：Windows `%ProgramData%\AgentOS`，其他系统 `~/.agentos`（可用 `--data` 或 `AGENTOS_DATA` 覆盖）。

### 添加其他机器作为节点

```bash
agentos node --server http://<控制台IP>:7800 --token <节点令牌>
```
节点令牌在 server 启动日志和 Web UI「设置」页中可见。

## 安装

### Windows 安装包 `AgentOS-Setup-<版本>.exe`
中文安装向导，安装类型二选一：
1. **管理软件（含本机 Agent OS）**（默认）：桌面程序 `AgentOS.exe` + 管理端 + 本机节点；开始菜单/桌面快捷方式及「安装后启动」均打开 `AgentOS.exe`。
   `<安装目录>\bin\dist\` 附带各平台 `agentos` 二进制与 `join.ps1`/`join.sh`，由管理端在 `/download/<文件名>` 提供下载（其他电脑一键加入）。
2. **仅 Agent OS 节点**：填写 管理端地址 / 加入令牌 / 名称，以 Windows 服务运行 `agentos node`（开机自启、故障自动重启）。

托盘：存在桌面程序 `AgentOS.exe` 时由它负责托盘图标，`agentos-tray.exe` 仍随附（无桌面程序或节点模式下的轻量启动器：启动/停止服务器或节点、打开控制台、查看日志），其「登录时启动」默认关闭。

可选组件：注册为 Windows 服务（管理软件模式下可选，节点模式默认）、开始菜单/桌面快捷方式、登录时启动托盘、WebView2 运行时（缺失时从微软下载 Evergreen Bootstrapper 静默安装；Windows 11 已内置）。
安装目录布局：请运行根目录 **`AgentOS.exe`（桌面窗口，无黑框）**；`bin\agentos-server.exe`（命令行/服务器/节点，高级）与 `bin\agentos-tray.exe` 在 `bin\`。
节点安装包在 `bin\dist\`。请勿双击 `agentos-server.exe` 作为日常入口。快捷方式：`AgentOS.exe --agentos "<安装目录>\bin\agentos-server.exe"`。

重复运行安装包即**原地升级**：自动停止服务/进程、替换文件、保留配置与数据并恢复服务。设置保存在 `%ProgramData%\AgentOS\agentos.conf`。

静默安装 / 卸载：
```bat
AgentOS-Setup-0.1.0.exe /S                                   :: 管理软件（含本机 Agent OS）
AgentOS-Setup-0.1.0.exe /S /MODE=console /SERVICE /PORT=7800 :: 同上 + Windows 服务
AgentOS-Setup-0.1.0.exe /S /MODE=node /SERVER=http://192.168.1.10:7800 /TOKEN=<令牌> /NAME=办公室电脑
::  其他: /NOSERVICE /NODESKTOP /NOTRAY /NODIST /NOWEBVIEW2 /D=C:\安装目录
"%ProgramFiles%\AgentOS\uninstall.exe" /S [/PURGE]            :: /PURGE 同时删除 %ProgramData%\AgentOS
```

Windows 服务（`agentos.exe`，需管理员）：
```bat
agentos service install [--mode console|node] [--listen :7800] [--server URL --token T] [--name N] [--data DIR]
agentos service start | stop | restart | status | uninstall
agentos node --server URL --token T --name N --install-service   :: 节点自启 (Windows: 登录计划任务, 可捕获桌面; Linux: systemd; macOS: launchd)
```
服务以 LocalSystem 运行，失败后 5s/10s/30s 自动重启；日志 `%ProgramData%\AgentOS\logs\service.log`，启动/停止写入事件日志（来源 AgentOS）。
注意：服务运行在 session 0，内置屏幕流无法捕获用户桌面（远程桌面请用 VNC 或由 `AgentOS.exe` 运行）。

### 其他电脑加入（join 脚本）
```bash
# Linux / macOS（--install: root 时注册 systemd 服务，macOS 注册 launchd）
curl -fsSL http://<管理端>:7800/download/join.sh | sh -s -- --server http://<管理端>:7800 --token <令牌> --name 名称 --install
```
```powershell
# Windows（管理员 PowerShell，-Install 注册为 Windows 服务）
irm http://<管理端>:7800/download/join.ps1 -OutFile join.ps1; .\join.ps1 -Server http://<管理端>:7800 -Token <令牌> -Install
```
脚本从 `<管理端>/download/agentos-<os>-<arch>[.exe]` 下载二进制（os: linux/darwin/windows，arch: amd64/arm64）。

### Linux / macOS 压缩包
`agentos-<版本>-linux-{amd64,arm64}.tar.gz`：`sudo ./install.sh`（systemd 服务 `agentos`，数据 `/var/lib/agentos`），
节点：`sudo ./install.sh node <URL> <令牌> [名称]`。`agentos-<版本>-darwin-{arm64,amd64}.tar.gz`：`./install.sh`（launchd 用户代理）。

### Linux 节点：虚拟桌面（可选）

`agentos node --desktop virtual`（或环境配置 `desktop: "virtual"`、`install.sh --desktop virtual`）让节点运行自己的虚拟桌面：
Xvfb（自动选择空闲显示号）+ fluxbox 窗口管理器（含任务栏）+ xterm + x11vnc（仅监听 127.0.0.1 的空闲端口），由节点托管、异常自动重启；
VNC 与内置屏幕流都使用该桌面，节点上的智能体继承其 `DISPLAY`。前置软件（Debian/Ubuntu）：

```sh
sudo apt install xvfb x11vnc fluxbox xterm xdotool
```

Docker 桌面镜像：Ubuntu `localhost/agentos-desktop:0.1.20`（XFCE）与 Debian `localhost/agentos-debian-desktop:0.1.22`（XFCE + Plank）在首次创建对应桌面容器时自动构建；SSH 环境可开启 `virtual_desktop`，
在远程主机上按需为该用户启动同样的虚拟桌面（远程需安装上述软件）。

## 构建与打包

```bash
make test        # 单元测试 + 端到端测试 (-race)
make dist        # 全部产物 -> dist/
make ci          # = scripts/ci.sh：gofmt、三平台 go vet、CGO_ENABLED=0 交叉编译检查、test -race、make dist、产物校验
make wine-smoke  # 在 Wine 中运行 agentos.exe 并静默安装（两种模式）验证
```
`dist/` 产物：`agentos-windows-amd64.exe`、`agentos-linux-{amd64,arm64}`、`agentos-darwin-{arm64,amd64}`、
`AgentOS-desktop-windows-amd64.exe`（存在 `cmd/agentos-desktop` 时，`-H windowsgui`）、`agentos-tray-windows-amd64.exe`、
`AgentOS-Setup-<版本>.exe`、`agentos-<版本>-*.tar.gz`、`agentos-<版本>-windows-amd64.zip`（便携版）、`join.sh`、`join.ps1`、`SHA256SUMS`。
依赖：Go、`nsis`（makensis，告警视为错误 `-WX`）、python3（zip）。所有目标均为 `CGO_ENABLED=0` 纯 Go 构建。
图标/安装向导图片由 `make assets`（`packaging/tools/wintool`）生成；Windows 资源（图标、版本信息、清单）在构建时生成 `.syso` 并于构建后删除。
GitHub Actions：`.github/workflows/ci.yml`（Linux 构建 + Windows 静默安装冒烟 + tag 发布）。

## 目录结构

| 路径 | 说明 |
|---|---|
| `cmd/agentos` | 命令行入口 (server / node / service / admin / version) |
| `internal/api` | 共享数据模型与协议（冻结的契约） |
| `internal/server` | HTTP/WS 服务、节点 hub、各功能 handler |
| `internal/node` | 节点守护进程 |
| `internal/supervisor` | Agent 进程管理与隔离 |
| `internal/logs` | 日志存储 |
| `internal/metrics` | 资源指标采集 |
| `internal/desktop` | 远程桌面（`vnc/` 桥接、`screen/` 内置屏幕流） |
| `internal/auth`, `internal/store` | 认证与 SQLite 持久化 |
| `web/static` | Web UI（原生 ES Modules，无需构建） |
| `packaging` | NSIS 安装脚本、图标与生成工具、systemd / launchd 单元 |
| `cmd/agentos-tray` | 托盘启动器（纯 Go，fyne.io/systray） |
| `test/e2e` | 端到端测试 |

## 当前状态（Phase 1）

已可用：server/node 注册、Agent 增删改查、启动/停止/重启、自动重启、实时日志、VNC 隧道（字节级打通）、Web UI 基础页面、NSIS 安装包。
待完成（见 docs/modules.md）：Windows Job Object、指标采集、日志轮转、noVNC 客户端、内置屏幕流、Windows 服务、托盘程序等。
