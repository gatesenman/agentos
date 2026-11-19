# AgentOS 0.1.9

## 本版要点

1. **Hyper-V Administrators 组成员修复**
   - 安装程序（NSIS）改为使用 `$USERDOMAIN\$USERNAME` 调用 `hyperv-add-group.ps1`，并额外尝试本机 Administrator（RID 500）；写入 `hyperv-group.log`。
   - 运行时一键修复：`POST /api/v1/providers/hyperv/repair-group`，优先走 `D:\AgentOS-E2E` 提权队列，否则 UAC；向导在缺权限时显示「一键修复权限」。
   - 修复后需**注销并重新登录**，新组令牌才会生效（无需重装）。

2. **WSL QEMU 回退后端**
   - 当 Hyper-V 不可用/无权限/过期探测失败时，同一套傻瓜预设（`hyperv-ubuntu-2404` / `hyperv-windows11` / `hyperv-fedora`）自动经 WSL2 发行版 `AgentOS-E2E` 的 QEMU/KVM 创建可用桌面。
   - 复用现有 seed/ISO 逻辑（`buildSeed` / `resolveISO`），不另起一套应答盘。
   - 能力：生命周期、exec、文件、desktop.vnc（Ubuntu/Win 客户机 VNC+SSH；Fedora 用 QEMU 宿主 VNC）、截图（QMP）、指标尽力而为。
   - UI 仍显示「本机虚拟机」，并带微妙提示「经 WSL QEMU」。

3. **主题点击 E2E**：设置页点击浅色/深色/跟随系统，断言 `data-theme` 与 `localStorage` 持久化。

4. 版本号、交付说明、RESULTS 对齐 0.1.9。
