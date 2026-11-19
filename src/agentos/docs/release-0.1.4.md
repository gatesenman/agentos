# AgentOS 0.1.4 交付说明

## 本版重点：本机虚拟机（Hyper-V）

在 Windows 11 宿主上新增 **Hyper-V 本机虚拟机**提供方（`hyperv`），与 Docker **容器**在向导中分组区分（「本机虚拟机」vs「容器」）。

### 默认镜像预设（锁死）

| 预设 ID | 镜像 | 默认资源 | 说明 |
|---|---|---|---|
| `hyperv-ubuntu-2404` | Ubuntu 24.04.5.1 LTS Desktop amd64 | 8 vCPU / 16 GB / 80 GB VHDX，Gen2，Secure Boot 关 | ISO 可自动下载到存储目录 |
| `hyperv-windows11` | Windows 11 Enterprise Evaluation | 8 vCPU / 16 GB / 100 GB | **需用户自备 ISO**（[微软评估中心](https://www.microsoft.com/en-us/evalcenter/evaluate-windows-11-enterprise)）；向导仍创建 VM 并提示下一步 |
| `hyperv-macos` | — | — | **置灰**：macOS 虚拟机需 Apple 主机；Windows 无法正版运行（`CapMacGuest=false`） |
| `hyperv-fedora` | Fedora Workstation（高级） | 8 / 16 / 80 | 可选 |

磁盘默认路径：`D:\AgentOS-VMs`（无 D: 则 `%LOCALAPPDATA%\AgentOS\vms`）。适合本机配置：i9-14900HX、~96 GB RAM、RTX 4060 Laptop、Hyper-V/WSL2 已启用。

**不会**在安装包或源码中分发 Windows/macOS ISO。详见源码 `docs/local-vms.md`。

### 其他收敛

- 命名：虚拟电脑 / 环境与 AI 工具 / 智能体（本地|A2A|ACP）
- Docker Ubuntu 卡片标明「容器」，避免与 Hyper-V Ubuntu 混淆
- 需启用 Hyper-V；创建 VM 建议管理员权限

## 交付文件

| 文件 | 说明 |
|---|---|
| `AgentOS-Setup-0.1.4.exe` | Windows 安装程序 |
| `agentos-0.1.4-windows-amd64.zip` | Windows 免安装包 |
| `agentos-0.1.4-linux-*.tar.gz` / `darwin-*.tar.gz` | 其他平台 |
| `AgentOS-source-0.1.4.zip` | 源码归档 |
| `SHA256SUMS` | 校验和 |
| `join.sh` / `join.ps1` | 节点加入脚本 |

## 已知限制

- Hyper-V 仅 Windows；Linux CI 上预设置灰，单测用 mock
- Exec/Files 未通客户机（无内置 guest agent）
- 桌面页签依赖客户机 VNC；Windows 客户机请用 mstsc/vmconnect（Status.Extra 含 rdp）
- macOS 客户机不支持（许可）；未来仅在 Apple 宿主考虑 UTM/Virtualization.framework
