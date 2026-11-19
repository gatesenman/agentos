package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/env"
	"github.com/agentos/agentos/internal/env/providers/agentoslocal"
	"github.com/agentos/agentos/internal/env/providers/agentosremote"
	"github.com/agentos/agentos/internal/env/providers/hyperv"
	sshprov "github.com/agentos/agentos/internal/env/providers/ssh"
	"github.com/agentos/agentos/internal/stacks"
)

// EnvPreset is a curated "quick create" machine template for the new
// virtual-computer wizard (GET /api/v1/env-presets). Machine only — no tool
// stacks. POST /api/v1/envs {"preset": id} provisions the environment with
// zero further input: name auto-generated, config/labels from the preset
// (any provided field overrides it). Stack on a preset (if any) is recorded
// as a suggestion only; clients install via POST /envs/{id}/stacks/{id}/install.
type EnvPreset struct {
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	Category      string            `json:"category"` // agentos | container | ssh | vm
	Name          string            `json:"name"`
	NameEn        string            `json:"name_en"`
	Description   string            `json:"description"`
	DescriptionEn string            `json:"description_en"`
	Icon          string            `json:"icon"`      // logo key for the UI (agentos|docker|ssh|python|node|claude|browser|desktop)
	BaseName      string            `json:"base_name"` // auto names: "<base_name> 1", "<base_name> 2", ...
	Config        json.RawMessage   `json:"config"`
	Stack         string            `json:"stack,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Tags          []string          `json:"tags,omitempty"` // short feature chips (zh)
	Note          string            `json:"note,omitempty"` // extra explanation (zh), e.g. SSH prerequisites
	Available     bool              `json:"available"`
	Reason        string            `json:"reason,omitempty"`     // why unavailable (zh)
	HelpURL       string            `json:"help_url,omitempty"`   // install/help link when unavailable
	AltPreset     string            `json:"alt_preset,omitempty"` // suggested compliant alternative when unavailable
	// 0.1.9
	FallbackNote   string `json:"fallback_note,omitempty"` // e.g. 经 WSL QEMU
	RepairAction   string `json:"repair_action,omitempty"` // hyperv-group
	CanRepairGroup bool   `json:"can_repair_group,omitempty"`
}

// PresetSSHAddr is the "本机 SSH" target (tests point it at an in-process sshd).
var PresetSSHAddr = "127.0.0.1:22"

const presetProbeTTL = 60 * time.Second

type probeResult struct {
	at     time.Time
	ok     bool
	reason string
}

type presetProber struct {
	mu    sync.Mutex
	cache map[string]probeResult
}

var presetProbes = &presetProber{cache: map[string]probeResult{}}

// probe runs fn at most once per TTL per key (provider availability is cached).
func (p *presetProber) probe(key string, fresh bool, fn func() (bool, string)) (bool, string) {
	p.mu.Lock()
	r, ok := p.cache[key]
	p.mu.Unlock()
	if ok && !fresh && time.Since(r.at) < presetProbeTTL {
		return r.ok, r.reason
	}
	okv, reason := fn()
	p.mu.Lock()
	p.cache[key] = probeResult{at: time.Now(), ok: okv, reason: reason}
	p.mu.Unlock()
	return okv, reason
}

func rawJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func hasVirtualDesktopTools() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	for _, b := range []string{"Xvfb", "x11vnc"} {
		if _, err := exec.LookPath(b); err != nil {
			return false
		}
	}
	return true
}

// presetList builds the curated presets with availability (cached probes).
// presetDefs returns curated machine presets without availability probes.
func (s *Server) presetDefs() []EnvPreset {
	localDesk := "host"
	deskName, deskDesc := "本机 Agent OS 实例（屏幕桌面）", "在本机启动一个独立的 Agent OS 实例（独立数据目录与端口），远程桌面使用本机屏幕。"
	deskNameEn, deskDescEn := "Local Agent OS instance (host desktop)", "A separate Agent OS instance on this machine (own data dir and port), desktop = this screen."
	if hasVirtualDesktopTools() {
		localDesk = "virtual"
		deskName, deskDesc = "本机 Agent OS 实例（虚拟桌面）", "在本机启动一个独立的 Agent OS 实例，带自己的虚拟桌面（Xvfb + x11vnc），智能体的图形界面互不干扰。"
		deskNameEn, deskDescEn = "Local Agent OS instance (virtual desktop)", "A separate Agent OS instance with its own virtual desktop (Xvfb + x11vnc)."
	}
	if runtime.GOOS == "windows" {
		deskName, deskDesc = "Windows 本机 Agent OS", "在本机启动独立的 Agent OS 实例，远程桌面使用 Windows 本机屏幕。一键创建，高级选项可改端口与数据目录。"
		deskNameEn, deskDescEn = "Windows local Agent OS", "Local Agent OS instance using this Windows screen. One-click create; advanced options for port and data dir."
	}
	sshHost, sshPortS, _ := net.SplitHostPort(PresetSSHAddr)
	sshPort, _ := strconv.Atoi(sshPortS)
	sshCfg := map[string]any{"host": sshHost, "port": sshPort, "user": sshprov.CurrentUser(), "host_key_policy": "tofu"}
	if k := sshprov.DefaultKeyPath(); k != "" {
		sshCfg["private_key_path"] = k
	}
	// Machine presets only (0.1.3+). Beginner one-click: defaults prefilled; customize folds advanced.
	// Tool/AI stacks: GET /tool-presets + POST /envs/{id}/stacks/{stack}/install.
	list := []EnvPreset{
		{ID: "agentos-desktop", Provider: agentoslocal.Type, Category: "agentos", Icon: "agentos",
			Name: deskName, NameEn: deskNameEn, Description: deskDesc, DescriptionEn: deskDescEn, BaseName: "Agent OS 实例",
			Config: rawJSON(map[string]any{"desktop": localDesk, "auto_start": true}), Tags: []string{"推荐", "智能体", "终端", "桌面"}},
		{ID: "agentos-remote", Provider: agentosremote.Type, Category: "agentos", Icon: "agentos-remote",
			Name: "远程 Agent OS（加入另一台电脑）", NameEn: "Remote Agent OS (enroll another PC)",
			Description: "登记另一台电脑：创建后显示一条加入命令，在目标机器上运行即可接入。", DescriptionEn: "Enroll another computer: run the shown join command on it.",
			BaseName: "远程电脑", Config: rawJSON(map[string]any{"desktop": "host"}), Tags: []string{"Windows", "Linux", "macOS"}},
		{ID: "docker-desktop", Provider: "docker", Category: "container", Icon: "desktop",
			Name: "Ubuntu Desktop（容器）· XFCE", NameEn: "Ubuntu Desktop (container) · XFCE",
			Description: "Docker 容器（非本机虚拟机）：Ubuntu 24.04 + XFCE 图形桌面（非控制台；面板/壁纸/文件管理器，VNC），2 核 / 4 GB。一键开通，不预装 AI 工具。", DescriptionEn: "Ubuntu 24.04 XFCE graphical desktop container (not a console; panel/wallpaper/file manager via VNC), 2 CPU / 4 GB. Machine only.",
			BaseName: "Ubuntu 桌面", Config: rawJSON(map[string]any{"desktop": true, "cpus": 2, "memory_mb": 4096}), Tags: []string{"推荐", "桌面", "Ubuntu"}},
		{ID: "docker-debian-desktop", Provider: "docker", Category: "container", Icon: "desktop",
			Name: "Debian 桌面（容器）· XFCE", NameEn: "Debian Desktop (container) · XFCE",
			Description: "Docker 容器：Debian 13（trixie）+ XFCE + Plank 底栏（浏览器/终端，贴近 AgentOS 机器人桌面），VNC，2 核 / 4 GB。首次创建会自动构建镜像（约数分钟至十几分钟）。", DescriptionEn: "Debian 13 (trixie) + XFCE + Plank dock container (browser/terminal, AgentOS bot desktop look) via VNC, 2 CPU / 4 GB. First create builds the image.",
			BaseName: "Debian 桌面", Config: rawJSON(map[string]any{
				"desktop": true, "cpus": 2, "memory_mb": 4096,
				"image": "localhost/agentos-debian-desktop:0.1.22",
			}), Tags: []string{"推荐", "桌面", "Debian"}},
		{ID: "docker-python", Provider: "docker", Category: "container", Icon: "python",
			Name: "Python 3.12 沙箱镜像", NameEn: "Python 3.12 sandbox image",
			Description: "python:3.12-slim 隔离容器（镜像自带 Python，不另装 AI 工具）。", DescriptionEn: "Isolated python:3.12-slim container (image has Python; no extra tool install).",
			BaseName: "Python 沙箱", Config: rawJSON(map[string]any{"image": "python:3.12-slim", "cpus": 2, "memory_mb": 2048}), Tags: []string{"Python", "沙箱"}},
		{ID: "docker-node", Provider: "docker", Category: "container", Icon: "node",
			Name: "Node 22 沙箱镜像", NameEn: "Node 22 sandbox image",
			Description: "node:22 隔离容器（镜像自带 Node/npm，不另装 AI 工具）。", DescriptionEn: "Isolated node:22 container (image has Node/npm; no extra tool install).",
			BaseName: "Node 沙箱", Config: rawJSON(map[string]any{"image": "node:22", "cpus": 2, "memory_mb": 2048}), Tags: []string{"Node.js", "沙箱"}},
		{ID: "ssh-local", Provider: "ssh", Category: "ssh", Icon: "ssh",
			Name: "本机 SSH", NameEn: "Local SSH",
			Description:   fmt.Sprintf("通过 SSH 连接本机 %s（用户 %s，私钥 %s）。", PresetSSHAddr, sshCfg["user"], orDash(sshCfg["private_key_path"])),
			DescriptionEn: "Connect to this machine over SSH with your default key.",
			BaseName:      "本机 SSH", Config: rawJSON(sshCfg), Tags: []string{"终端", "文件", "SFTP"},
			Note: "SSH 需要可访问的主机：本机需运行 SSH 服务，且该私钥已加入 ~/.ssh/authorized_keys。连接其他机器请用「自定义」填写地址。"},
		// 本机虚拟机（Hyper-V）— 与 Docker「容器」清晰区分。0.1.5：全自动安装（零操作）。
		{ID: "hyperv-ubuntu-2404", Provider: hyperv.Type, Category: "vm", Icon: "desktop",
			Name: "本地虚拟机 · Ubuntu 24.04 桌面", NameEn: "Local VM · Ubuntu 24.04 Desktop",
			Description: "全自动安装（零操作）：应用内在线下载 Ubuntu 24.04.5 LTS 桌面官方镜像，无人值守安装中文 GNOME 桌面并自动登录；内嵌桌面（VNC）、终端、文件开箱即用。8 vCPU / 16 GB / 80 GB。", DescriptionEn: "Zero-touch in-app download of Ubuntu 24.04 Desktop, unattended Chinese GNOME, embedded VNC desktop + terminal/files. 8 vCPU / 16 GB / 80 GB.",
			BaseName: "Ubuntu 本地虚拟机", Config: rawJSON(map[string]any{
				"guest_os": "ubuntu-24.04", "cpus": 8, "memory_mb": 16384, "disk_gb": 80,
				"generation": 2, "secure_boot": "off", "enable_vnc": true, "language": "zh-CN",
				"iso_url": hyperv.UbuntuDesktopISOURL,
			}), Tags: []string{"推荐", "桌面", "全自动安装", "在线下载"},
			Note: "这是完整本地虚拟机（非 Docker 容器）。首次约下载 6 GB 官方镜像（应用内断点续传，缓存后复用），安装约 10–25 分钟，全程无需跳转浏览器；账户密码在概览页显示。需启用 Hyper-V，或以 WSL QEMU 回退。"},
		{ID: "hyperv-windows11", Provider: hyperv.Type, Category: "vm", Icon: "desktop",
			Name: "本地虚拟机 · Windows 11 桌面", NameEn: "Local VM · Windows 11 Desktop",
			Description: "全自动安装（零操作）：应用内在线下载微软官方 Windows 11 企业版评估版（简体中文，90 天），无人值守安装、本地管理员自动登录，开启远程桌面、SSH（PowerShell）与内嵌桌面。8 vCPU / 16 GB / 100 GB，含虚拟 TPM。", DescriptionEn: "Zero-touch in-app download of Windows 11 Enterprise Evaluation (zh-CN), unattended setup, RDP + SSH + embedded desktop. vTPM included.",
			BaseName: "Windows 本地虚拟机", Config: rawJSON(map[string]any{
				"guest_os": "windows-11", "cpus": 8, "memory_mb": 16384, "disk_gb": 100,
				"generation": 2, "secure_boot": "on", "enable_vnc": true, "language": "zh-CN",
			}), Tags: []string{"桌面", "全自动安装", "在线下载", "Windows"},
			Note: "首次约下载 6.9 GB 评估版镜像（微软官方 CDN，应用内断点续传，缓存后复用），安装约 15–30 分钟，无需跳转评估中心。评估版 90 天到期后需重建；高级选项可填本机已有 iso_path。"},
		{ID: "hyperv-macos", Provider: hyperv.Type, Category: "vm", Icon: "desktop",
			Name: "本地虚拟机 · macOS（不可用）", NameEn: "Local VM · macOS (unavailable)",
			Description: "Apple 许可只允许在 Apple 硬件上运行 macOS 虚拟机，Windows 电脑无法合规创建。可改用「远程 Mac（SSH + 屏幕共享）」接入你自己的 Mac。", DescriptionEn: "Apple's license only allows macOS VMs on Apple hardware. Connect your own Mac instead (SSH + Screen Sharing).",
			BaseName: "macOS 虚拟机", Config: rawJSON(map[string]any{"guest_os": "macos"}),
			Tags: []string{"macOS", "不可用"}, AltPreset: "ssh-mac"},
		{ID: "ssh-mac", Provider: "ssh", Category: "ssh", Icon: "ssh",
			Name: "远程 Mac（SSH + 屏幕共享桌面）", NameEn: "Remote Mac (SSH + Screen Sharing desktop)",
			Description: "接入你自己的 Mac 电脑桌面：终端、文件（SFTP）与 macOS「屏幕共享」桌面（经 SSH 隧道，内嵌桌面页签）。在 Mac 上打开「系统设置 → 通用 → 共享」中的「远程登录」和「屏幕共享」。Windows 上无法合规运行本地 macOS 虚拟机（Apple 许可）。", DescriptionEn: "Your own Mac desktop via Screen Sharing (SSH tunnel) + terminal/files. Local macOS VMs are not licensed on Windows.",
			BaseName: "远程 Mac", Config: rawJSON(map[string]any{"host": "", "port": 22, "user": "", "os": "darwin", "vnc_port": 5900, "host_key_policy": "tofu"}),
			Tags: []string{"macOS", "桌面", "屏幕共享"},
			Note: "填写 Mac 的地址、用户名与密码（或私钥）。桌面页签使用 Apple 屏幕共享认证，显示完整 macOS 桌面（非控制台）。"},
		{ID: "hyperv-fedora", Provider: hyperv.Type, Category: "vm", Icon: "desktop",
			Name: "本地虚拟机 · Fedora Workstation 桌面", NameEn: "Local VM · Fedora Workstation Desktop",
			Description: "全自动安装（kickstart）：应用内在线下载 Fedora 44，安装中文 GNOME 桌面（强制 Xorg + x11vnc），内嵌桌面开箱即用，终端/文件就绪。8 vCPU / 16 GB / 80 GB。", DescriptionEn: "Zero-touch in-app Fedora 44 download, Chinese GNOME on Xorg + x11vnc for embedded desktop, terminal/files ready.",
			BaseName: "Fedora 本地虚拟机", Config: rawJSON(map[string]any{
				"guest_os": "fedora-workstation", "cpus": 8, "memory_mb": 16384, "disk_gb": 80,
				"generation": 2, "secure_boot": "off", "enable_vnc": true, "language": "zh-CN",
				"iso_url": hyperv.FedoraWorkstationISOURL,
			}), Tags: []string{"桌面", "全自动安装", "在线下载", "Fedora"},
			Note: "完整 GNOME Workstation 桌面（非控制台）。首次联网下载安装介质与软件包（应用内进度），约 15–40 分钟；桌面页签经 SSH 隧道连接客户机 x11vnc。"},
		// 高级/终端容器（非桌面主路径）：排在全部桌面 VM 卡片之后
		{ID: "docker-ubuntu-server", Provider: "docker", Category: "container", Icon: "docker",
			Name: "Ubuntu Server（高级 · 终端）", NameEn: "Ubuntu Server (advanced · terminal)",
			Description: "ubuntu:24.04 无图形界面服务器容器，1 核 / 2 GB。仅适合脚本/终端；日常请优先选上方桌面预设。", DescriptionEn: "ubuntu:24.04 console-only container. Prefer desktop presets above for daily use.",
			BaseName: "Ubuntu 服务器", Config: rawJSON(map[string]any{"image": "ubuntu:24.04", "cpus": 1, "memory_mb": 2048}), Tags: []string{"高级", "终端", "Ubuntu"}},
		{ID: "docker-debian", Provider: "docker", Category: "container", Icon: "docker",
			Name: "Debian（高级 · 终端）", NameEn: "Debian (advanced · terminal)",
			Description: "debian:bookworm-slim 轻量无桌面容器，1 核 / 1 GB。", DescriptionEn: "debian:bookworm-slim console-only lightweight container.",
			BaseName: "Debian", Config: rawJSON(map[string]any{"image": "debian:bookworm-slim", "cpus": 1, "memory_mb": 1024}), Tags: []string{"高级", "终端", "Debian"}},
		{ID: "docker-fedora", Provider: "docker", Category: "container", Icon: "docker",
			Name: "Fedora（高级 · 终端）", NameEn: "Fedora (advanced · terminal)",
			Description: "fedora:40 无图形界面容器，1 核 / 2 GB。需要 Fedora 桌面请选「本地虚拟机 · Fedora Workstation」。", DescriptionEn: "fedora:40 console-only. For Fedora desktop use the local Fedora Workstation VM.",
			BaseName: "Fedora", Config: rawJSON(map[string]any{"image": "fedora:40", "cpus": 1, "memory_mb": 2048}), Tags: []string{"高级", "终端", "Fedora"}},
		// VirtualBox still not implemented — grey tip only.
		{ID: "virtualbox", Provider: "virtualbox", Category: "vm", Icon: "desktop",
			Name: "VirtualBox 虚拟机", NameEn: "VirtualBox virtual machine",
			Description: "通过 VirtualBox 创建虚拟机（尚未接入；请用本机虚拟机 Hyper-V 或 Docker）。", DescriptionEn: "VirtualBox not wired yet; use Hyper-V or Docker.",
			BaseName: "VirtualBox", Config: rawJSON(map[string]any{}), Tags: []string{"跨平台", "即将支持"}},
	}
	return list
}

// presetList builds the curated presets with availability (cached probes).
func (s *Server) presetList(ctx context.Context, fresh bool) []EnvPreset {
	list := s.presetDefs()
	sshHost, sshPortS, _ := net.SplitHostPort(PresetSSHAddr)
	sshPort, _ := strconv.Atoi(sshPortS)
	sshCfg := map[string]any{"host": sshHost, "port": sshPort, "user": sshprov.CurrentUser(), "host_key_policy": "tofu"}
	if k := sshprov.DefaultKeyPath(); k != "" {
		sshCfg["private_key_path"] = k
	}
	// availability
	var wg sync.WaitGroup
	var dockerOK, sshOK bool
	var dockerWhy, sshWhy string
	wg.Add(2)
	go func() {
		defer wg.Done()
		dockerOK, dockerWhy = presetProbes.probe("docker", fresh, func() (bool, string) {
			p, ok := env.Get("docker")
			if !ok {
				return false, "未启用 Docker 支持"
			}
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			if err := p.Test(c, json.RawMessage(`{}`)); err != nil {
				return false, "未检测到 Docker Desktop（无 \\\\.\\pipe\\docker_engine）。请安装并启动 Docker Desktop；嵌套虚拟机（如 WinBoat）内通常无法再跑 Docker。可改用「Windows 本机 Agent OS」或「本机 SSH」。详情：" + shortErr(err)
			}
			return true, ""
		})
	}()
	go func() {
		defer wg.Done()
		sshOK, sshWhy = presetProbes.probe("ssh:"+PresetSSHAddr, fresh, func() (bool, string) {
			cn, err := net.DialTimeout("tcp", PresetSSHAddr, 1500*time.Millisecond)
			if err != nil {
				return false, "本机未运行 SSH 服务（" + PresetSSHAddr + "）"
			}
			cn.Close()
			if sshCfg["private_key_path"] == nil {
				return false, "未找到 ~/.ssh/id_ed25519 或 id_rsa 私钥"
			}
			p, _ := env.Get("ssh")
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if err := p.Test(c, rawJSON(sshCfg)); err != nil {
				return false, "SSH 登录失败：" + shortErr(err)
			}
			return true, ""
		})
	}()
	wg.Wait()
	// Hyper-V Detect is expensive (PowerShell); run at most once per list build.
	hvDetect := hyperv.DetectResult{}
	hvProbed := false
	ensureHV := func() hyperv.DetectResult {
		if !hvProbed {
			hvDetect = hyperv.Detect(context.Background())
			hvProbed = true
			// keep probe cache in sync with the Detect we just paid for
			ok, why := hvDetect.Available, hvDetect.Reason
			if !ok && why == "" {
				why = "未检测到 Hyper-V / WSL QEMU。可改用 Docker 容器或本机 Agent OS"
			}
			if !ok {
				ok = false
			} else {
				why = ""
			}
			presetProbes.mu.Lock()
			presetProbes.cache["hyperv"] = probeResult{at: time.Now(), ok: ok, reason: why}
			presetProbes.mu.Unlock()
		}
		return hvDetect
	}
	for i := range list {
		p := &list[i]
		p.Available = true
		if _, ok := env.Get(p.Provider); !ok {
			p.Available, p.Reason = false, "该环境类型未启用"
			continue
		}
		switch p.Provider {
		case "docker":
			p.Available, p.Reason = dockerOK, dockerWhy
			if !dockerOK {
				p.HelpURL = "https://docs.docker.com/desktop/setup/install/windows-install/"
				p.AltPreset = "agentos-desktop"
			}
		case "ssh":
			if p.ID == "ssh-mac" { // user supplies the Mac's address; nothing to probe
				break
			}
			p.Available, p.Reason = sshOK, sshWhy
		case hyperv.Type:
			if p.ID == "hyperv-macos" {
				p.Available = false
				if hyperv.CapMacGuestHost() {
					p.Reason = "当前构建尚未接入 macOS 宿主虚拟化（计划 UTM / Virtualization.framework）；见 docs/local-vms.md"
				} else {
					p.Reason = "Apple 许可仅允许在 Apple 硬件上运行 macOS 虚拟机，Windows 主机无法合规创建。推荐改用「远程 Mac（SSH + 屏幕共享）」。"
				}
				p.HelpURL = "https://www.apple.com/legal/sla/"
				break
			}
			ok, why := presetProbes.probe("hyperv", fresh, detectHyperV)
			if fresh || !hvProbed {
				// prefer one Detect; probe() may have already run detectHyperV which Detects again —
				// ensureHV de-dupes further calls in this list build.
				_ = ensureHV()
				ok, why = presetProbes.cache["hyperv"].ok, presetProbes.cache["hyperv"].reason
			}
			p.Available, p.Reason = ok, why
			d := ensureHV()
			if d.FallbackNote != "" {
				p.FallbackNote = d.FallbackNote
				if p.Note == "" {
					p.Note = d.FallbackNote
				} else if !strings.Contains(p.Note, d.FallbackNote) {
					p.Note = p.Note + " · " + d.FallbackNote
				}
			}
			if d.CanRepairGroup {
				p.CanRepairGroup = true
				p.RepairAction = "hyperv-group"
			}
			if !ok {
				p.HelpURL = "https://learn.microsoft.com/windows-server/virtualization/hyper-v/get-started/install-hyper-v"
				if d.CanRepairGroup {
					p.Reason = d.Reason
					if p.Reason == "" {
						p.Reason = "当前账户不在「Hyper-V Administrators」组。可一键修复，或使用 WSL QEMU 回退。"
					}
				}
			}
		case "virtualbox":
			ok, why := presetProbes.probe("virtualbox", fresh, detectVirtualBox)
			p.Available, p.Reason = ok, why
			if !ok {
				p.HelpURL = "https://www.virtualbox.org/"
			}
		}
	}
	return list
}

// detectHyperV uses the hyperv provider probe (Hyper-V or WSL QEMU fallback).
func detectHyperV() (bool, string) {
	d := hyperv.Detect(context.Background())
	if d.Available {
		return true, ""
	}
	why := d.Reason
	if why == "" {
		why = "未检测到 Hyper-V / WSL QEMU。可改用 Docker 容器或本机 Agent OS"
	}
	return false, why
}

func detectVirtualBox() (bool, string) {
	if _, err := exec.LookPath("VBoxManage"); err == nil {
		return false, "已检测到 VirtualBox，但 AgentOS 尚未接入该类型（请用 Docker 或远程 Agent OS）"
	}
	return false, "未检测到 VirtualBox（VBoxManage）。安装后仍将置灰直至后续版本接入"
}

func orDash(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "—"
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func (s *Server) findPreset(ctx context.Context, id string) (EnvPreset, bool) {
	return s.findPresetOpt(ctx, id, false)
}

// findPresetOpt resolves one preset and probes only that provider.
// It must not call presetList (Hyper-V Detect would block docker-desktop create for tens of seconds).
func (s *Server) findPresetOpt(ctx context.Context, id string, fresh bool) (EnvPreset, bool) {
	pr, ok := s.presetByID(id)
	if !ok {
		return EnvPreset{}, false
	}
	s.applyProviderAvailability(ctx, &pr, fresh)
	return pr, true
}

// presetByID returns a copy of the static preset definition (no availability probes).
func (s *Server) presetByID(id string) (EnvPreset, bool) {
	for _, p := range s.presetDefs() {
		if p.ID == id {
			return p, true
		}
	}
	return EnvPreset{}, false
}

// applyProviderAvailability sets Available/Reason for a single preset using the probe cache.
func (s *Server) applyProviderAvailability(ctx context.Context, pr *EnvPreset, fresh bool) {
	if pr == nil {
		return
	}
	pr.Available = true
	if _, ok := env.Get(pr.Provider); !ok {
		pr.Available, pr.Reason = false, "该环境类型未启用"
		return
	}
	switch pr.Provider {
	case "docker":
		ok, why := presetProbes.probe("docker", fresh, func() (bool, string) {
			p, ok := env.Get("docker")
			if !ok {
				return false, "未启用 Docker 支持"
			}
			c, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			if err := p.Test(c, json.RawMessage(`{}`)); err != nil {
				return false, "未检测到 Docker Desktop（无 \\\\.\\pipe\\docker_engine）。请安装并启动 Docker Desktop；嵌套虚拟机（如 WinBoat）内通常无法再跑 Docker。可改用「Windows 本机 Agent OS」或「本机 SSH」。详情：" + shortErr(err)
			}
			return true, ""
		})
		pr.Available, pr.Reason = ok, why
		if !ok {
			pr.HelpURL = "https://docs.docker.com/desktop/setup/install/windows-install/"
			pr.AltPreset = "agentos-desktop"
		}
	case "ssh":
		if pr.ID == "ssh-mac" {
			return
		}
		ok, why := presetProbes.probe("ssh:"+PresetSSHAddr, fresh, func() (bool, string) {
			cn, err := net.DialTimeout("tcp", PresetSSHAddr, 1500*time.Millisecond)
			if err != nil {
				return false, "本机未运行 SSH 服务（" + PresetSSHAddr + "）"
			}
			cn.Close()
			sshHost, sshPortS, _ := net.SplitHostPort(PresetSSHAddr)
			sshPort, _ := strconv.Atoi(sshPortS)
			sshCfg := map[string]any{"host": sshHost, "port": sshPort, "user": sshprov.CurrentUser(), "host_key_policy": "tofu"}
			if k := sshprov.DefaultKeyPath(); k == "" {
				return false, "未找到 ~/.ssh/id_ed25519 或 id_rsa 私钥"
			} else {
				sshCfg["private_key_path"] = k
			}
			p, _ := env.Get("ssh")
			c, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if err := p.Test(c, rawJSON(sshCfg)); err != nil {
				return false, "SSH 登录失败：" + shortErr(err)
			}
			return true, ""
		})
		pr.Available, pr.Reason = ok, why
	case hyperv.Type:
		if pr.ID == "hyperv-macos" {
			pr.Available = false
			if hyperv.CapMacGuestHost() {
				pr.Reason = "当前构建尚未接入 macOS 宿主虚拟化（计划 UTM / Virtualization.framework）；见 docs/local-vms.md"
			} else {
				pr.Reason = "Apple 许可仅允许在 Apple 硬件上运行 macOS 虚拟机，Windows 主机无法合规创建。推荐改用「远程 Mac（SSH + 屏幕共享）」。"
			}
			pr.HelpURL = "https://www.apple.com/legal/sla/"
			pr.AltPreset = "ssh-mac"
			return
		}
		ok, why := presetProbes.probe("hyperv", fresh, detectHyperV)
		pr.Available, pr.Reason = ok, why
		d := hyperv.Detect(context.Background())
		if d.FallbackNote != "" {
			pr.FallbackNote = d.FallbackNote
		}
		if d.CanRepairGroup {
			pr.CanRepairGroup = true
			pr.RepairAction = "hyperv-group"
		}
		if !ok {
			pr.HelpURL = "https://learn.microsoft.com/windows-server/virtualization/hyper-v/get-started/install-hyper-v"
		}
	case "virtualbox":
		ok, why := presetProbes.probe("virtualbox", fresh, detectVirtualBox)
		pr.Available, pr.Reason = ok, why
	case agentoslocal.Type, agentosremote.Type:
		pr.Available = true
	}
}

// uniqueEnvName returns "<base> N" with the smallest N not used by an environment.
func (s *Server) uniqueEnvName(ctx context.Context, base string) string {
	used := map[string]bool{}
	if rows, err := s.envs.es.ListEnvs(ctx); err == nil {
		for _, r := range rows {
			used[r.Name] = true
		}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "环境"
	}
	for i := 1; ; i++ {
		n := base + " " + strconv.Itoa(i)
		if !used[n] {
			return n
		}
	}
}

// mergeConfigOver overlays the keys of over onto base (both JSON objects).
func mergeConfigOver(base, over json.RawMessage) (json.RawMessage, error) {
	m := map[string]any{}
	if len(base) > 0 && string(base) != "null" {
		if err := json.Unmarshal(base, &m); err != nil {
			return nil, err
		}
	}
	if len(over) > 0 && string(over) != "null" {
		o := map[string]any{}
		if err := json.Unmarshal(over, &o); err != nil {
			return nil, err
		}
		for k, v := range o {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func (s *Server) routesPresets(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	// GET /api/v1/env-presets[?refresh=1] — machine (虚拟电脑) presets only
	mux.Handle("GET /api/v1/env-presets", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.presetList(r.Context(), r.URL.Query().Get("refresh") == "1"))
	}))
	// GET /api/v1/tool-presets — install-onto-machine catalog (same as GET /stacks)
	mux.Handle("GET /api/v1/tool-presets", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, stacks.Views())
	}))
}
