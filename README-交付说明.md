# AgentOS 0.1.22 交付

Commit: 1965d96（VERSION 0.1.22）

## 本版要点
1. **嵌套虚拟机远程桌面防 OOM**：桌面/控制台不再自动连接；隐藏页断开；低内存提示与省流量参数；服务端 JPEG 编码面积上限 1280×720。
2. **Docker 不可用时更清晰**：说明无 `\\.\pipe\docker_engine`，嵌套 WinBoat 通常无法再跑 Docker Desktop；AltPreset 指向「Windows 本机 Agent OS」。
3. 未在访客内静默安装 Docker Desktop（磁盘紧张 + 嵌套虚拟化风险）。

## 安装
- `AgentOS-Setup-0.1.22.exe` 静默或 GUI 安装；**请勿自动启动**（用户机上启动会卡）。
- 校验：`verify-sha.ps1` + `SHA256SUMS`。

## latest-only
目录只保留 0.1.22 的 Setup / source / portable。
