package hyperv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

type provider struct{}

func init() { env.Register(provider{}) }

// New returns the Hyper-V provider (also registered as "hyperv" in init).
func New() env.Provider { return provider{} }

func (provider) Info() env.ProviderInfo {
	return env.ProviderInfo{
		Type:        Type,
		Name:        "本机虚拟机",
		Kind:        env.KindVM,
		Group:       "本机虚拟机",
		Description: "在本机创建 Ubuntu / Windows 11 / Fedora 桌面虚拟机：优先 Hyper-V（Gen2），若无权限或不可用则自动经 WSL QEMU 回退。自动下载官方镜像、无人值守安装、内置终端/文件/桌面。默认 8 vCPU / 16 GB，磁盘在 D:\\AgentOS-VMs。可一键修复 Hyper-V Administrators 组成员资格。",
		Capabilities: []env.Capability{
			env.CapLifecycle, env.CapExec, env.CapFiles, env.CapDesktopVNC, env.CapMetrics, env.CapInstallAgent,
		},
		ConfigSchema: json.RawMessage(schema),
	}
}

func (provider) CapabilitiesFor(rec *env.Record) []env.Capability {
	caps := []env.Capability{env.CapLifecycle, env.CapMetrics}
	c, err := parseConfig(rec.Config)
	if err != nil {
		return caps
	}
	if c.hasGuestAccess() {
		caps = append(caps, env.CapExec, env.CapFiles, env.CapInstallAgent)
	}
	if c.EnableVNC {
		caps = append(caps, env.CapDesktopVNC)
	}
	return caps
}

// ProvisionTimeout implements env.ProvisionTimeouter: ISO download + OS install.
func (provider) ProvisionTimeout(rec *env.Record) time.Duration { return 4 * time.Hour }

// ResumeCreate implements env.CreateResumer: Create is idempotent (existing VM
// and cached ISO are reused), so an interrupted provisioning is re-run.
func (provider) ResumeCreate(rec *env.Record) bool { return true }

var userRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)

func (provider) Validate(raw json.RawMessage) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	return c.validate()
}

// Prepare implements env.Preparer: generate credentials before the record is
// stored, so they survive an interrupted install and show up in the UI at once.
func (provider) Prepare(rec *env.Record) error {
	raw, err := prepareConfig(rec.Config, rec.Name)
	if err != nil {
		return err
	}
	rec.Config = raw
	return nil
}

func prepareConfig(raw json.RawMessage, envName string) (json.RawMessage, error) {
	c, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	set := map[string]any{}
	if c.unattended() {
		if c.GuestPassword == "" {
			set["guest_password"] = GeneratePassword()
		}
		set["guest_user"] = c.GuestUser
		if c.SSHPrivateKey == "" {
			priv, pub, _, err := newSSHKeyPair("agentos")
			if err != nil {
				return nil, err
			}
			set["ssh_private_key"], set["ssh_public_key"] = priv, pub
		}
		if c.HostKeyPrivate == "" && !c.isWindows() {
			priv, pub, fp, err := newSSHKeyPair("")
			if err != nil {
				return nil, err
			}
			_ = pub
			set["host_key_private"], set["host_key"] = priv, fp
		}
		if c.Hostname == "" {
			set["hostname"] = defaultHostname(c.GuestOS)
		}
		set["language"] = c.Language
	}
	if c.MACAddress == "" {
		b := make([]byte, 3)
		rand.Read(b)
		set["mac_address"] = "00155D" + strings.ToUpper(hex.EncodeToString(b))
	}
	if len(set) == 0 {
		return raw, nil
	}
	return mergeConfig(raw, set)
}

func defaultHostname(guest string) string {
	b := make([]byte, 2)
	rand.Read(b)
	suffix := strings.ToUpper(hex.EncodeToString(b))
	switch guest {
	case GuestWindows11:
		return "AGENTOS-W-" + suffix // NetBIOS limit: 15 chars
	case GuestFedora:
		return "agentos-fedora-" + strings.ToLower(suffix)
	default:
		return "agentos-ubuntu-" + strings.ToLower(suffix)
	}
}

func (provider) Test(ctx context.Context, raw json.RawMessage) error {
	if _, err := parseConfig(raw); err != nil {
		return err
	}
	d := Detect(ctx)
	if !d.Available {
		return fmt.Errorf("%w: %s", errNotAvailable, d.Reason)
	}
	return nil
}

func (provider) Create(ctx context.Context, rec *env.Record) error {
	if runtime.GOOS != "windows" {
		return errNotWindows
	}
	raw, err := prepareConfig(rec.Config, rec.Name)
	if err != nil {
		return err
	}
	rec.Config = raw
	c, err := parseConfig(rec.Config)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	d := Detect(ctx)
	if !d.Available {
		return fmt.Errorf("%w: %s", errNotAvailable, d.Reason)
	}
	// Prefer explicit config backend; otherwise follow Detect (Hyper-V or QEMU/WSL fallback).
	if c.Backend == "" && d.Backend != "" {
		c.Backend = d.Backend
	}
	if c.VMName == "" {
		c.VMName = "agentos-" + sanitizeName(rec.ID)
		if rec.Name != "" {
			if n := sanitizeName(rec.Name); n != "vm" {
				c.VMName = "agentos-" + n + "-" + sanitizeName(rec.ID)[:minInt(6, len(sanitizeName(rec.ID)))]
			}
		}
	}
	dir := vmDir(c.StoreRoot, rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hyperv: 创建存储目录失败: %w", err)
	}
	c.VHDPath = filepath.Join(dir, c.VMName+".vhdx")
	save := func(set map[string]any) {
		if r, err := mergeConfig(rec.Config, set); err == nil {
			rec.Config = r
			env.SaveConfig(ctx, r)
		}
	}
	save(map[string]any{"vm_name": c.VMName, "vhd_path": c.VHDPath, "store_root": c.StoreRoot, "phase": PhaseDownload, "backend": c.Backend})

	if c.usingQEMU() || d.Backend == BackendQEMUWSL {
		c.Backend = BackendQEMUWSL
		return provider{}.createQEMUWSL(ctx, rec, &c, save)
	}

	// 1. install media (Hyper-V path)
	c.Phase = PhaseDownload
	isoPath, needsISO, note, err := resolveISO(ctx, &c)
	if err != nil {
		return err
	}
	c.NeedsISO, c.Notes = needsISO, note
	if isoPath != "" {
		c.ISOPath = isoPath
	}
	if needsISO {
		env.Logf(ctx, "warn", "%s", note)
	}

	if c.GuestOS == GuestUbuntu2404 && isoPath != "" {
		if cfg, err := isoReadFile(isoPath, "boot/grub/grub.cfg"); err == nil {
			if k, i := grubBootFiles(cfg); k != "" && i != "" {
				ubuntuKernel, ubuntuInitrd = k, i
			}
		}
	}

	// 2. seed (answer file) disk
	seedPath := ""
	if c.unattended() && isoPath != "" {
		seedPath = filepath.Join(dir, "agentos-seed.iso")
		if err := buildSeed(ctx, c, rec.ID, seedPath); err != nil {
			return fmt.Errorf("hyperv: 生成自动安装应答盘失败: %w", err)
		}
	}

	// 3. VM
	c.Phase = PhaseCreate
	switchName, err := ensureSwitch(ctx, c.SwitchName)
	if err != nil {
		return err
	}
	c.SwitchName = switchName
	env.Logf(ctx, "info", "创建 Hyper-V 虚拟机 / creating VM %s (%d vCPU, %d MB, %d GB, 交换机 %s)", c.VMName, c.CPUs, c.MemoryMB, c.DiskGB, c.SwitchName)
	cctx, cancel := withTimeout(ctx, 10*time.Minute)
	var created struct {
		Id      string `json:"Id"`
		Name    string `json:"Name"`
		Existed bool   `json:"Existed"`
		State   string `json:"State"`
		TPM     string `json:"TPM"`
	}
	err = psJSON(cctx, buildCreateScript(c, isoPath, seedPath), &created)
	cancel()
	if err != nil {
		return fmt.Errorf("创建虚拟机失败: %w", err)
	}
	c.VMID = created.Id
	if created.Name != "" {
		c.VMName = created.Name
	}
	if created.TPM != "" {
		env.Logf(ctx, "info", "虚拟 TPM / vTPM: %s", created.TPM)
	}
	save(map[string]any{"vm_id": c.VMID, "vm_name": c.VMName, "switch_name": c.SwitchName, "iso_path": c.ISOPath,
		"needs_iso": c.NeedsISO, "notes": c.Notes, "phase": PhaseInstall})

	// 4. boot (+ keystrokes for the installer boot menu on a fresh VM)
	started := time.Now()
	if strings.EqualFold(created.State, "Off") || created.State == "" {
		env.Logf(ctx, "info", "启动虚拟机 / starting VM %s", c.VMName)
		if _, err := runner().RunPS(ctx, fmt.Sprintf("Start-VM -Name '%s' -ErrorAction Stop; 'ok'", escapePS(c.VMName))); err != nil {
			return fmt.Errorf("启动虚拟机失败: %s", friendlyStartError(err.Error()))
		}
		if !created.Existed && c.unattended() && isoPath != "" {
			if err := bootKeys(ctx, c); err != nil {
				env.Logf(ctx, "warn", "发送安装引导按键失败: %v", err)
			}
		}
	} else {
		env.Logf(ctx, "info", "虚拟机已在运行，继续等待安装完成 / VM already running, resuming wait")
	}

	if c.NeedsISO || !c.unattended() {
		if c.NeedsISO {
			c.Notes = nextStepISO(c)
		} else {
			c.Notes = "虚拟机已从 ISO 启动，请在「在 Hyper-V 窗口中打开」里完成系统安装。"
		}
		save(map[string]any{"notes": c.Notes, "phase": ""})
		return nil
	}

	// 5. wait for the unattended install + guest setup
	env.Logf(ctx, "info", "%s", installBanner(c))
	addr, err := waitGuestReady(ctx, &c, started)
	if err != nil {
		save(map[string]any{"phase": PhaseInstall})
		return err
	}
	c.InstallSeconds = int(time.Since(started).Seconds())
	env.Logf(ctx, "info", "系统安装完成，用时 %s / OS installed in %s", fmtDur(time.Since(started)), fmtDur(time.Since(started)))

	// 6. eject install media + remove the seed (it contains the password)
	ejectScript := fmt.Sprintf(`
$ErrorActionPreference = 'Continue'
Get-VMDvdDrive -VMName '%s' | Set-VMDvdDrive -Path $null
'ok'`, escapePS(c.VMName))
	if _, err := runner().RunPS(ctx, ejectScript); err != nil {
		env.Logf(ctx, "warn", "弹出安装光盘失败（不影响使用）: %v", err)
	}
	if seedPath != "" {
		os.Remove(seedPath)
	}
	c.Notes = readyNote(c, addr)
	save(map[string]any{"phase": PhaseReady, "guest_address": addr, "install_seconds": c.InstallSeconds, "notes": c.Notes,
		"needs_iso": false})
	env.Logf(ctx, "info", "%s", c.Notes)
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d 分 %02d 秒", m, s)
}

func installBanner(c Config) string {
	switch c.GuestOS {
	case GuestWindows11:
		return "正在无人值守安装 Windows 11（通常 15–30 分钟，期间会自动重启数次）… / unattended Windows setup running"
	case GuestFedora:
		return "正在无人值守安装 Fedora Workstation（需联网下载软件包，通常 15–40 分钟）… / kickstart install running"
	default:
		return "正在无人值守安装 Ubuntu 24.04 桌面（通常 10–25 分钟，含中文语言包与远程桌面组件）… / autoinstall running"
	}
}

func readyNote(c Config, addr string) string {
	acct := fmt.Sprintf("账户 %s / 密码 %s", c.GuestUser, c.GuestPassword)
	switch c.GuestOS {
	case GuestWindows11:
		return "Windows 11 已就绪（" + acct + "，已自动登录）。推荐「远程桌面」打开（动态分辨率、剪贴板、声音），也可用桌面页签或终端（PowerShell over SSH）。IP " + addr
	case GuestFedora:
		return "Fedora Workstation 已就绪（" + acct + "，已自动登录 GNOME/Xorg）。桌面页签可直接操作（经 SSH 隧道 x11vnc）；终端/文件开箱即用。IP " + addr
	default:
		return "Ubuntu 桌面已就绪（" + acct + "，已自动登录）。桌面页签可直接操作；也可「在 Hyper-V 窗口中打开」。IP " + addr
	}
}

func nextStepISO(c Config) string {
	if c.GuestOS == GuestWindows11 {
		return "虚拟机已创建，但应用内下载 Windows 11 评估版 ISO 失败。请删除此环境后重试（会自动续传）；或在高级选项填写本机已有 iso_path 后重新创建。"
	}
	return "虚拟机已创建，但 ISO 未就绪。请删除后重试应用内下载，或在高级选项填写本机已有 iso_path。"
}

func buildSeed(ctx context.Context, c Config, envID, path string) error {
	p := SeedParams{
		Hostname: c.Hostname, User: c.GuestUser, Password: c.GuestPassword,
		PasswordHash:  sha512Crypt(c.GuestPassword, randomString(16, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./")),
		AuthorizedKey: c.SSHPublicKey, HostKeyPriv: c.HostKeyPrivate, HostKeyPub: hostKeyPublic(c),
		Locale: "zh_CN.UTF-8", WinLocale: "zh-CN", TimeZone: "Asia/Shanghai", WinTimeZone: "China Standard Time",
		VNCPort: c.VNCPort, FedoraRel: FedoraRelease, Proxy: strings.TrimSpace(c.InstallProxy),
	}
	if !c.zh() {
		p.Locale, p.WinLocale = "en_US.UTF-8", "en-US"
	}
	var files []SeedFile
	var err error
	label := "CIDATA"
	switch c.GuestOS {
	case GuestUbuntu2404:
		files, err = UbuntuSeed(p, "agentos-"+envID)
	case GuestFedora:
		files, err = FedoraSeed(p)
		label = "OEMDRV"
	case GuestWindows11:
		var extra []SeedFile
		for _, t := range []struct{ url, name string }{{OpenSSHMSIURL, "OpenSSH-Win64.msi"}, {TightVNCMSIURL, "tightvnc-setup-64bit.msi"}} {
			if pth, e := cachedTool(ctx, c.StoreRoot, t.url, t.name); e == nil {
				extra = append(extra, SeedFile{Name: t.name, Path: pth})
			} else {
				env.Logf(ctx, "warn", "下载 %s 失败（客户机将尝试在线安装）: %v", t.name, e)
			}
		}
		files, err = WindowsSeed(p, extra)
		label = "AGENTOS"
	default:
		return fmt.Errorf("不支持自动安装的系统 %s", c.GuestOS)
	}
	if err != nil {
		return err
	}
	env.Logf(ctx, "info", "生成自动安装应答盘 / seed disk: %s", path)
	return WriteSeedISOFile(path, label, files)
}

// hostKeyPublic derives the public half of the stored host key.
func hostKeyPublic(c Config) string {
	if c.HostKeyPrivate == "" {
		return ""
	}
	pub, err := publicFromPrivate(c.HostKeyPrivate)
	if err != nil {
		return ""
	}
	return pub
}

func buildCreateScript(c Config, isoPath, seedPath string) string {
	mem := int64(c.MemoryMB) * 1024 * 1024
	disk := int64(c.DiskGB) * 1024 * 1024 * 1024
	sb := "On"
	if c.secureBootOff() {
		sb = "Off"
	}
	sbTemplate := "MicrosoftWindows"
	if !c.isWindows() {
		sbTemplate = "MicrosoftUEFICertificateAuthority"
	}
	tpm := "$false"
	if c.isWindows() {
		tpm = "$true"
	}
	return fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$name = '%s'
$vhd = '%s'
$switch = '%s'
$gen = %d
$mem = [int64]%d
$disk = [int64]%d
$cpus = %d
$iso = '%s'
$seed = '%s'
$mac = '%s'
$existing = Get-VM -Name $name -ErrorAction SilentlyContinue
if ($existing) {
  [pscustomobject]@{ Id = $existing.VMId.Guid; Name = $existing.Name; Existed = $true; State = $existing.State.ToString() } | ConvertTo-Json -Compress
  return
}
if (-not (Test-Path -LiteralPath $vhd)) { New-VHD -Path $vhd -SizeBytes $disk -Dynamic | Out-Null }
New-VM -Name $name -MemoryStartupBytes $mem -Generation $gen -VHDPath $vhd -SwitchName $switch -Path (Split-Path -Parent $vhd) | Out-Null
Set-VMProcessor -VMName $name -Count $cpus
Set-VMMemory -VMName $name -DynamicMemoryEnabled $false
Set-VM -Name $name -AutomaticCheckpointsEnabled $false -CheckpointType Disabled -AutomaticStopAction ShutDown -AutomaticStartAction Nothing
if ($mac) { Set-VMNetworkAdapter -VMName $name -StaticMacAddress $mac }
Enable-VMIntegrationService -VMName $name -Name 'Guest Service Interface' -ErrorAction SilentlyContinue
$tpmState = ''
if ($gen -eq 2) {
  if ('%s' -eq 'On') {
    try { Set-VMFirmware -VMName $name -EnableSecureBoot On -SecureBootTemplate '%s' -ErrorAction Stop }
    catch { Set-VMFirmware -VMName $name -EnableSecureBoot On }
  } else { Set-VMFirmware -VMName $name -EnableSecureBoot Off }
  try { Set-VMVideo -VMName $name -HorizontalResolution 1920 -VerticalResolution 1080 -ResolutionType Single } catch {}
  if (%s) {
    try { Set-VMKeyProtector -VMName $name -NewLocalKeyProtector; Enable-VMTPM -VMName $name; $tpmState = 'enabled' } catch { $tpmState = 'unavailable: ' + $_.Exception.Message }
  }
}
if ($iso) { Add-VMDvdDrive -VMName $name -Path $iso }
if ($seed) { Add-VMDvdDrive -VMName $name -Path $seed }
if ($gen -eq 2) {
  $hdd = Get-VMHardDiskDrive -VMName $name | Select-Object -First 1
  $dvd = Get-VMDvdDrive -VMName $name | Where-Object { $_.Path -eq $iso } | Select-Object -First 1
  $nic = Get-VMNetworkAdapter -VMName $name | Select-Object -First 1
  # empty disk first: firmware falls through to the installer once, and the installed OS boots on its own afterwards
  $order = @($hdd)
  if ($dvd) { $order += $dvd }
  $order += $nic
  Set-VMFirmware -VMName $name -BootOrder $order
}
$vm = Get-VM -Name $name
[pscustomobject]@{ Id = $vm.VMId.Guid; Name = $vm.Name; Existed = $false; State = $vm.State.ToString(); TPM = $tpmState } | ConvertTo-Json -Compress
`, escapePS(c.VMName), escapePS(c.VHDPath), escapePS(c.SwitchName), c.Generation, mem, disk, c.CPUs,
		escapePS(isoPath), escapePS(seedPath), escapePS(c.MACAddress), sb, sbTemplate, tpm)
}

func ensureSwitch(ctx context.Context, preferred string) (string, error) {
	if preferred != "" {
		return preferred, nil
	}
	script := `
$ErrorActionPreference = 'Stop'
$sw = Get-VMSwitch -Name 'Default Switch' -ErrorAction SilentlyContinue
if (-not $sw) { $sw = Get-VMSwitch | Where-Object { $_.SwitchType -eq 'External' } | Select-Object -First 1 }
if (-not $sw) { $sw = Get-VMSwitch | Select-Object -First 1 }
if (-not $sw) { throw '没有可用的虚拟交换机。请在 Hyper-V 管理器中创建「默认开关」或外部交换机。' }
$sw.Name
`
	out, err := runner().RunPS(ctx, script)
	if err != nil {
		return "", fmt.Errorf("选择虚拟交换机失败: %w", err)
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", fmt.Errorf("未找到 Hyper-V 虚拟交换机")
	}
	return name, nil
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' {
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		return "vm"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (provider) Open(ctx context.Context, rec *env.Record) (env.Env, error) {
	c, err := parseConfig(rec.Config)
	if err != nil {
		return nil, err
	}
	return &hypervEnv{cfg: c, envID: rec.ID}, nil
}
