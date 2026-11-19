// Package hyperv implements the "hyperv" local VM provider (Windows Hyper-V Gen2).
// On non-Windows hosts the provider registers but Create/Test report unavailable.
package hyperv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Type is the provider registry key.
const Type = "hyperv"

// Guest OS identifiers persisted in config.guest_os.
const (
	GuestUbuntu2404 = "ubuntu-24.04"
	GuestWindows11  = "windows-11"
	GuestFedora     = "fedora-workstation"
	GuestMacOS      = "macos" // never creatable on Windows (CapMacGuest=false)
	GuestCustom     = "custom"
)

// Official install media (download only — never redistributed in-repo).
const (
	// Ubuntu 24.04.5.1 LTS Desktop amd64 (respin fixing the installer crash).
	UbuntuDesktopISOURL    = "https://releases.ubuntu.com/24.04.5.1/ubuntu-24.04.5.1-desktop-amd64.iso"
	UbuntuDesktopISOName   = "ubuntu-24.04.5.1-desktop-amd64.iso"
	UbuntuDesktopISOSHA256 = "4da4a0c9035da8e68a59a838674f403f0a54472c78a83b4fb7f78d03588f85a7"
	// Fedora 44 Everything netinst + kickstart (OEMDRV) installs full
	// Workstation GNOME (@^workstation-product-environment). Live ISO cannot
	// be kickstarted; netinst still yields the same polished desktop out of box,
	// with AgentOS forcing Xorg + x11vnc for the embedded desktop tab.
	FedoraRelease            = 44
	FedoraWorkstationISOURL  = "https://download.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/iso/Fedora-Everything-netinst-x86_64-44-1.7.iso"
	FedoraWorkstationISOName = "Fedora-Everything-netinst-x86_64-44-1.7.iso"
	FedoraISOSHA256          = "bd285201494dd0ba09b54d05ac707de1401668b8512a573edb5922dcf9d7067e"
	// Windows 11 Enterprise 25H2 Evaluation (90 days, no key) — Microsoft's
	// public Eval Center CDN links.
	WindowsISOURLzhCN    = "https://software-static.download.prss.microsoft.com/dbazure/888969d5-f34g-4e03-ac9d-1f9786c66749/26200.6584.250915-1905.25h2_ge_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_zh-cn.iso"
	WindowsISOSHA256zhCN = "7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2"
	WindowsISOURLenUS    = "https://software-static.download.prss.microsoft.com/dbazure/888969d5-f34g-4e03-ac9d-1f9786c66749/26200.6584.250915-1905.25h2_ge_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_en-us.iso"
	WindowsISOSHA256enUS = "a61adeab895ef5a4db436e0a7011c92a2ff17bb0357f58b13bbc4062e535e7b9"
	WindowsEvalCenterURL = "https://www.microsoft.com/zh-cn/evalcenter/download-windows-11-enterprise"
	// Guest tools put on the Windows seed disk (cached under <store>/cache).
	OpenSSHMSIURL  = "https://github.com/PowerShell/Win32-OpenSSH/releases/download/v9.8.3.0p2-Preview/OpenSSH-Win64-v9.8.3.0.msi"
	TightVNCMSIURL = "https://www.tightvnc.com/download/2.8.90/tightvnc-2.8.90-gpl-setup-64bit.msi"
)

// knownSHA256 maps official ISO file names to their published SHA-256.
var knownSHA256 = map[string]string{
	UbuntuDesktopISOName:              UbuntuDesktopISOSHA256,
	FedoraWorkstationISOName:          FedoraISOSHA256,
	isoNameFromURL(WindowsISOURLzhCN): WindowsISOSHA256zhCN,
	isoNameFromURL(WindowsISOURLenUS): WindowsISOSHA256enUS,
}

func isoNameFromURL(u string) string {
	base := filepath.Base(strings.Split(u, "?")[0])
	if i := strings.LastIndex(u, "/"); i >= 0 {
		base = strings.Split(u[i+1:], "?")[0]
	}
	return base
}

// Host defaults tuned for i9-class laptop (~96 GB RAM, prefer D:).
const (
	DefaultCPUs          = 8
	DefaultRAMMB         = 16384
	DefaultDiskGBUbuntu  = 80
	DefaultDiskGBWindows = 100
	DefaultDiskGBFedora  = 80
	DefaultGeneration    = 2
)

// CapMacGuest is the package-level capability flag for macOS guests on this
// build's primary target (Windows). Always false here; use CapMacGuestHost()
// for the runtime host check (true only on darwin for a future UTM path).
const CapMacGuest = false

// CapMacGuestHost reports whether this host can run licensed macOS guests.
func CapMacGuestHost() bool { return runtime.GOOS == "darwin" }

// Config is Record.Config. Fields written back by Create are marked readOnly in schema.
type Config struct {
	GuestOS      string `json:"guest_os,omitempty"`   // ubuntu-24.04 | windows-11 | fedora-workstation | macos | custom
	VMName       string `json:"vm_name,omitempty"`    // Hyper-V VM name (default agentos-<env name>)
	VMID         string `json:"vm_id,omitempty"`      // Hyper-V VmId GUID (written by Create)
	VHDPath      string `json:"vhd_path,omitempty"`   // written by Create
	ISOPath      string `json:"iso_path,omitempty"`   // user-provided or downloaded
	ISOURL       string `json:"iso_url,omitempty"`    // optional download URL
	StoreRoot    string `json:"store_root,omitempty"` // default D:\AgentOS-VMs or %LOCALAPPDATA%\AgentOS\vms
	CPUs         int    `json:"cpus,omitempty"`
	MemoryMB     int    `json:"memory_mb,omitempty"`
	DiskGB       int    `json:"disk_gb,omitempty"`
	Generation   int    `json:"generation,omitempty"` // 2 recommended
	SwitchName   string `json:"switch_name,omitempty"`
	SecureBoot   string `json:"secure_boot,omitempty"`   // on | off | auto (auto: off for Linux, on for Windows)
	EnableVNC    bool   `json:"enable_vnc,omitempty"`    // guest VNC for the desktop tab
	VNCPort      int    `json:"vnc_port,omitempty"`      // guest VNC (default 5900, guest loopback via SSH tunnel)
	RDPPort      int    `json:"rdp_port,omitempty"`      // guest RDP (default 3389)
	GuestAddress string `json:"guest_address,omitempty"` // last known IP (written by Create)
	NeedsISO     bool   `json:"needs_iso,omitempty"`     // true when the install ISO must be picked manually
	Notes        string `json:"notes,omitempty"`         // next-step tip for UI (zh)

	// 0.1.5 zero-touch install
	Unattended     *bool  `json:"unattended,omitempty"`       // default true: answer file + guest setup
	Language       string `json:"language,omitempty"`         // zh-CN (default) | en-US
	Hostname       string `json:"hostname,omitempty"`         // guest computer name
	GuestUser      string `json:"guest_user,omitempty"`       // login user (default "agent")
	GuestPassword  string `json:"guest_password,omitempty"`   // generated; shown in the UI on purpose (local VM)
	SSHPrivateKey  string `json:"ssh_private_key,omitempty"`  // x-secret: AgentOS -> guest key
	SSHPublicKey   string `json:"ssh_public_key,omitempty"`   // authorized_keys line
	HostKeyPrivate string `json:"host_key_private,omitempty"` // x-secret: guest SSH host key (Linux)
	HostKey        string `json:"host_key,omitempty"`         // pinned SHA256 fingerprint (Linux)
	MACAddress     string `json:"mac_address,omitempty"`      // static MAC (00155Dxxxxxx) for IP lookup
	Phase          string `json:"phase,omitempty"`            // download | create | install | configure | ready
	InstallSeconds int    `json:"install_seconds,omitempty"`  // measured OS install time
	InstallTimeout int    `json:"install_timeout_min,omitempty"`
	InstallProxy   string `json:"install_proxy,omitempty"` // http://host:port for Ubuntu/Fedora package downloads

	// 0.1.9: backend selection (hyperv | qemu-wsl). Empty means Hyper-V (legacy).
	Backend     string `json:"backend,omitempty"`
	SSHHostPort int    `json:"ssh_host_port,omitempty"` // QEMU user-net hostfwd localhost port (guest :22)
	QEMUVNCPort int    `json:"qemu_vnc_port,omitempty"` // host TCP port for QEMU -vnc (5900+display)
	WSLDistro   string `json:"wsl_distro,omitempty"`    // default AgentOS-E2E
	QEMURuntime string `json:"qemu_runtime,omitempty"` // WSL Linux dir for AF_UNIX sockets (/var/tmp/agentos/<id>)
}

// Backend identifiers persisted in config.backend.
const (
	BackendHyperV  = "hyperv"
	BackendQEMUWSL = "qemu-wsl"
)

func (c Config) usingQEMU() bool {
	return c.Backend == BackendQEMUWSL
}


// Phases of a zero-touch install.
const (
	PhaseDownload  = "download"
	PhaseCreate    = "create"
	PhaseInstall   = "install"
	PhaseConfigure = "configure"
	PhaseReady     = "ready"
)

func (c Config) unattended() bool {
	if c.Unattended != nil {
		return *c.Unattended
	}
	switch c.GuestOS {
	case GuestUbuntu2404, GuestWindows11, GuestFedora:
		return true
	}
	return false
}

// hasGuestAccess reports whether AgentOS can reach the guest over SSH.
func (c Config) hasGuestAccess() bool {
	return c.unattended() && c.SSHPrivateKey != "" && c.GuestUser != ""
}

func (c Config) isWindows() bool { return c.GuestOS == GuestWindows11 }

func (c Config) zh() bool { return !strings.HasPrefix(strings.ToLower(c.Language), "en") }

const schema = `{
  "type": "object",
  "properties": {
    "guest_os": {"type": "string", "title": "客户机系统", "enum": ["ubuntu-24.04", "windows-11", "fedora-workstation", "custom"], "default": "ubuntu-24.04",
      "description": "预设客户机类型。macOS 不可在 Windows 宿主上正版运行。"},
    "cpus": {"type": "integer", "title": "vCPU", "minimum": 1, "maximum": 32, "default": 8, "description": "推荐 8（本机 i9 级）"},
    "memory_mb": {"type": "integer", "title": "内存 (MB)", "minimum": 2048, "maximum": 65536, "default": 16384, "description": "推荐 16384"},
    "disk_gb": {"type": "integer", "title": "磁盘 (GB)", "minimum": 20, "maximum": 500, "default": 80},
    "iso_path": {"type": "string", "title": "ISO 路径（高级）", "default": "", "description": "留空则应用内自动下载官方镜像（推荐）；仅当本机已有 ISO 时填写路径作为回退，不会跳转浏览器"},
    "iso_url": {"x-advanced": true, "type": "string", "title": "ISO 下载地址", "default": ""},
    "store_root": {"x-advanced": true, "type": "string", "title": "虚拟机存储根目录", "default": "", "description": "默认优先 D:\\AgentOS-VMs，否则 %LOCALAPPDATA%\\AgentOS\\vms"},
    "switch_name": {"x-advanced": true, "type": "string", "title": "虚拟交换机", "default": "", "description": "留空自动选用 Default Switch / 第一个外部交换机"},
    "generation": {"x-advanced": true, "type": "integer", "title": "代数", "enum": [1, 2], "default": 2},
    "secure_boot": {"x-advanced": true, "type": "string", "title": "安全启动", "enum": ["auto", "on", "off"], "default": "auto", "description": "Linux 预设默认关闭；Windows 默认开启（并启用虚拟 TPM）"},
    "enable_vnc": {"x-advanced": true, "type": "boolean", "title": "期望客户机 VNC", "default": true, "description": "Ubuntu / Fedora / Windows 自动安装 VNC（仅客户机本地监听，经 SSH 隧道接入桌面页签；Fedora 强制 GNOME on Xorg）"},
    "vnc_port": {"x-advanced": true, "type": "integer", "title": "客户机 VNC 端口", "default": 5900},
    "rdp_port": {"x-advanced": true, "type": "integer", "title": "客户机 RDP 端口", "default": 3389},
    "unattended": {"x-advanced": true, "type": "boolean", "title": "全自动安装", "default": true, "description": "自动应答安装系统、创建账户、配置 SSH 与远程桌面；关闭则只创建空虚拟机并挂载 ISO"},
    "language": {"type": "string", "title": "系统语言", "enum": ["zh-CN", "en-US"], "default": "zh-CN"},
    "install_proxy": {"x-advanced": true, "type": "string", "title": "安装代理（可选）", "default": "", "description": "Ubuntu/Fedora 安装与软件包下载使用的 HTTP 代理，如 http://192.168.1.10:7890；网络正常时留空（连不上软件源时 Ubuntu 会离线完成安装，开机后自动补装桌面共享等组件）"},
    "guest_user": {"x-advanced": true, "type": "string", "title": "登录用户名", "default": "agent"},
    "guest_password": {"type": "string", "title": "登录密码", "format": "password", "readOnly": true, "description": "自动生成，可在概览中查看/复制"},
    "ssh_private_key": {"type": "string", "title": "SSH 私钥", "x-secret": true, "readOnly": true},
    "host_key_private": {"type": "string", "title": "主机密钥", "x-secret": true, "readOnly": true},
    "ssh_public_key": {"type": "string", "title": "SSH 公钥", "readOnly": true},
    "host_key": {"type": "string", "title": "主机密钥指纹", "readOnly": true},
    "mac_address": {"type": "string", "title": "MAC 地址", "readOnly": true},
    "phase": {"type": "string", "title": "安装阶段", "readOnly": true},
    "install_seconds": {"type": "integer", "title": "安装耗时（秒）", "readOnly": true},
    "vm_name": {"type": "string", "title": "Hyper-V 名称", "readOnly": true},
    "vm_id": {"type": "string", "title": "VM ID", "readOnly": true},
    "vhd_path": {"type": "string", "title": "VHDX 路径", "readOnly": true},
    "guest_address": {"type": "string", "title": "客户机 IP", "readOnly": true},
    "needs_iso": {"type": "boolean", "title": "需手动指定 ISO", "readOnly": true},
    "notes": {"type": "string", "title": "提示", "readOnly": true},
    "backend": {"type": "string", "title": "后端", "readOnly": true, "description": "hyperv 或 qemu-wsl（经 WSL QEMU 回退）"},
    "ssh_host_port": {"type": "integer", "title": "本机 SSH 转发端口", "readOnly": true},
    "qemu_vnc_port": {"type": "integer", "title": "QEMU VNC 端口", "readOnly": true}
  }
}`

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("hyperv: 配置格式错误: %w", err)
		}
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.GuestOS == "" {
		c.GuestOS = GuestUbuntu2404
	}
	if c.CPUs <= 0 {
		c.CPUs = DefaultCPUs
	}
	if c.MemoryMB <= 0 {
		c.MemoryMB = DefaultRAMMB
	}
	if c.DiskGB <= 0 {
		switch c.GuestOS {
		case GuestWindows11:
			c.DiskGB = DefaultDiskGBWindows
		default:
			c.DiskGB = DefaultDiskGBUbuntu
		}
	}
	if c.Generation != 1 {
		c.Generation = DefaultGeneration
	}
	if c.SecureBoot == "" {
		c.SecureBoot = "auto"
	}
	if c.VNCPort <= 0 {
		c.VNCPort = 5900
	}
	if c.RDPPort <= 0 {
		c.RDPPort = 3389
	}
	if c.StoreRoot == "" {
		c.StoreRoot = DefaultStoreRoot()
	}
	if c.Language == "" {
		c.Language = "zh-CN"
	}
	if c.ISOURL == "" {
		switch c.GuestOS {
		case GuestUbuntu2404:
			c.ISOURL = UbuntuDesktopISOURL
		case GuestFedora:
			c.ISOURL = FedoraWorkstationISOURL
		case GuestWindows11:
			if c.zh() {
				c.ISOURL = WindowsISOURLzhCN
			} else {
				c.ISOURL = WindowsISOURLenUS
			}
		}
	}
	// 0.1.4 presets pointed at the Fedora 42 Live ISO (now archived, and not automatable).
	if c.GuestOS == GuestFedora && strings.Contains(c.ISOURL, "/releases/42/Workstation/") {
		c.ISOURL = FedoraWorkstationISOURL
	}
	if !c.EnableVNC && (c.GuestOS == GuestUbuntu2404 || c.GuestOS == GuestFedora || (c.GuestOS == GuestWindows11 && c.unattended())) {
		c.EnableVNC = true // Ubuntu/Fedora: guest x11vnc on Xorg; Windows: TightVNC
	}
	if c.GuestUser == "" {
		c.GuestUser = "agent"
	}
}

func (c Config) validate() error {
	if c.GuestOS == GuestMacOS {
		return errors.New("hyperv: macOS 虚拟机需 Apple 主机（Apple Silicon / macOS 宿主）；当前 Windows 主机无法正版运行")
	}
	if !CapMacGuestHost() && c.GuestOS == GuestMacOS {
		return errors.New("hyperv: CapMacGuest=false — 不可在此宿主创建 macOS 客户机")
	}
	if c.CPUs < 1 || c.CPUs > 64 {
		return errors.New("hyperv: cpus 应在 1–64")
	}
	if c.MemoryMB < 1024 {
		return errors.New("hyperv: memory_mb 至少 1024")
	}
	if c.DiskGB < 20 {
		return errors.New("hyperv: disk_gb 至少 20")
	}
	if c.Generation != 1 && c.Generation != 2 {
		return errors.New("hyperv: generation 只能是 1 或 2")
	}
	switch c.GuestOS {
	case GuestUbuntu2404, GuestWindows11, GuestFedora, GuestCustom, "":
	default:
		return fmt.Errorf("hyperv: 未知 guest_os %q", c.GuestOS)
	}
	if c.GuestUser != "" && !userRe.MatchString(c.GuestUser) {
		return fmt.Errorf("hyperv: 登录用户名 %q 无效（小写字母开头，仅含小写字母/数字/-/_，最多 20 位）", c.GuestUser)
	}
	switch c.GuestUser {
	case "root", "admin", "administrator", "guest":
		return fmt.Errorf("hyperv: 登录用户名不能是保留名 %q", c.GuestUser)
	}
	if c.Hostname != "" {
		if !hostRe.MatchString(c.Hostname) {
			return fmt.Errorf("hyperv: 主机名 %q 无效（字母/数字/-，最多 63 位）", c.Hostname)
		}
	}
	if p := strings.TrimSpace(c.InstallProxy); p != "" && !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		return errors.New("hyperv: install_proxy 需以 http:// 或 https:// 开头")
	}
	return nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func (c Config) secureBootOff() bool {
	switch strings.ToLower(c.SecureBoot) {
	case "off":
		return true
	case "on":
		return false
	default: // auto
		return c.GuestOS == GuestUbuntu2404 || c.GuestOS == GuestFedora || c.GuestOS == GuestCustom
	}
}

// DefaultStoreRoot prefers D:\AgentOS-VMs on Windows when D: exists.
func DefaultStoreRoot() string {
	if runtime.GOOS == "windows" {
		if st, err := os.Stat(`D:\`); err == nil && st.IsDir() {
			return `D:\AgentOS-VMs`
		}
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
		return filepath.Join(base, "AgentOS", "vms")
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".agentos", "vms")
}

func vmDir(store, envID string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, envID)
	return filepath.Join(store, "agentos-"+safe)
}

// QEMURuntimeDir is the WSL Linux path for AF_UNIX sockets (qmp, swtpm, pidfile, serial).
// DrvFS (/mnt/*) does not support unix sockets; disks/ISO/seed stay on the Windows store.
const QEMURuntimeRoot = "/var/tmp/agentos"
