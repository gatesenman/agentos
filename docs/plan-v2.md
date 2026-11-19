# AgentOS 第一期规划（v2）

定位：智能体运行环境管理器（类似 VMware / Docker Desktop，但对象是"给智能体用的电脑"）。

三层概念：
1. 环境提供方 Provider（internal/env 注册表）：agentos-local（本机多实例）、agentos-remote（登记远程节点）、docker、ssh。后续：e2b、daytona、http-sandbox、wsl、hyperv、云 VM。
2. 运行环境 Environment：统一能力 = 启停 / 终端(exec) / 文件 / 远程桌面 / 监控 / 日志；不支持的能力 UI 置灰。
3. 智能体 Agent：运行在环境里，由技术栈模板创建（python/node/shell/claude-code/codex）。

Agent OS = 我们的节点守护程序（agentos node）。原生 Agent OS 环境能力最全（CapAgents）。
docker/ssh 环境可一键"安装 Agent OS"（CapInstallAgent）：把 agentos node 二进制放进去并接回管理端，从而获得完整的智能体托管能力。
智能体管理始终通过 Agent OS 节点完成（Record.NodeID），不另起第二套。

软件形态：Windows 桌面程序 AgentOS.exe（WebView2 窗口，Windows 11 Fluent 风格），左导航：环境 / 智能体 / 模板 / 设置；中间列表，右侧详情标签：概览 / 终端 / 文件 / 桌面 / 日志 / 性能。
安装包两种模式：管理软件（含本机 Agent OS），或仅 Agent OS 节点（填管理端地址 + 令牌）。

契约：internal/env/env.go（冻结 v1）。

## 0.1.4 local VMs
- hyperv provider shipped (Windows). Presets: Ubuntu 24.04 Desktop / Windows 11 Eval / macOS grey / Fedora advanced.
- CapMacGuest=false on Windows. Docs: local-vms.md.
