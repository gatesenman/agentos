package hyperv

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/env"
)

type hypervEnv struct {
	cfg   Config
	envID string

	mu       sync.Mutex
	addr     string    // last known guest IP
	addrAt   time.Time // when addr was resolved
	guest    env.Env   // inner SSH env (exec/files/desktop)
	guestFor string    // address guest was opened for
}

func (e *hypervEnv) name() string {
	if e.cfg.VMName != "" {
		return e.cfg.VMName
	}
	return "agentos-" + sanitizeName(e.envID)
}

func (e *hypervEnv) Status(ctx context.Context) (env.Status, error) {
	if e.cfg.usingQEMU() {
		return e.qemuStatus(ctx)
	}
	if runtime.GOOS != "windows" {
		d := Detect(ctx)
		return env.Status{State: env.StateUnknown, Message: d.Reason}, nil
	}
	v, err := getVMInfo(ctx, e.name(), e.cfg.MACAddress)
	if err != nil {
		return env.Status{State: env.StateUnknown, Message: err.Error()}, nil
	}
	if v.Missing {
		return env.Status{State: env.StateError, Message: "虚拟机不存在（可能已在 Hyper-V 管理器中删除）"}, nil
	}
	st := mapVMState(v.State)
	addr := v.address()
	if addr != "" {
		e.mu.Lock()
		e.addr, e.addrAt = addr, time.Now()
		e.mu.Unlock()
	}
	msg := v.State
	if e.cfg.NeedsISO {
		msg = e.cfg.Notes
		if msg == "" {
			msg = "等待挂载安装 ISO"
		}
	} else if st == env.StateRunning && e.cfg.Phase == PhaseReady && e.cfg.GuestPassword != "" {
		msg = fmt.Sprintf("运行中 · 账户 %s", e.cfg.GuestUser)
	} else if st == env.StateRunning {
		msg = "运行中"
	} else if st == env.StateStopped {
		msg = "已关机"
	}
	extra := map[string]string{
		"vm_name": v.Name, "vm_id": v.Id, "guest_os": e.cfg.GuestOS, "heartbeat": v.Heartbeat,
		"uptime_s": fmt.Sprint(v.Uptime),
	}
	if e.cfg.Phase != "" {
		extra["phase"] = e.cfg.Phase
	}
	if addr != "" {
		extra["guest_address"] = addr
		if e.cfg.isWindows() {
			extra["rdp"] = fmt.Sprintf("%s:%d", addr, e.cfg.RDPPort)
		}
		if e.cfg.hasGuestAccess() {
			extra["ssh"] = fmt.Sprintf("%s@%s", e.cfg.GuestUser, addr)
		}
	}
	return env.Status{State: st, Message: msg, Address: addr, Extra: extra}, nil
}

func mapVMState(s string) env.State {
	switch strings.ToLower(s) {
	case "running":
		return env.StateRunning
	case "off", "stopped", "saved", "paused":
		return env.StateStopped
	case "starting", "saving", "stopping", "pausing", "resuming":
		return env.StateCreating
	default:
		return env.StateUnknown
	}
}

func (e *hypervEnv) Start(ctx context.Context) error {
	if e.cfg.usingQEMU() {
		return e.qemuStart(ctx)
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s'
if ($vm.State -eq 'Paused') { Resume-VM -VM $vm } elseif ($vm.State -ne 'Running') { Start-VM -VM $vm }
'ok'`, escapePS(e.name()))
	if _, err := runner().RunPS(ctx, script); err != nil {
		return fmt.Errorf("启动虚拟机失败: %s", friendlyStartError(err.Error()))
	}
	return nil
}

// Stop asks the guest OS to shut down (integration services), falling back
// to a hard power-off after 2 minutes.
func (e *hypervEnv) Stop(ctx context.Context) error {
	if e.cfg.usingQEMU() {
		return e.qemuStop(ctx)
	}
	e.dropGuest()
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s'
if ($vm.State -eq 'Off') { 'ok'; return }
$job = Start-Job -ScriptBlock { param($n) Stop-VM -Name $n -Force } -ArgumentList $vm.Name
if (-not (Wait-Job $job -Timeout 120)) { Stop-Job $job; Stop-VM -Name $vm.Name -TurnOff -Force }
Receive-Job $job -ErrorAction SilentlyContinue | Out-Null
if ((Get-VM -Name $vm.Name).State -ne 'Off') { Stop-VM -Name $vm.Name -TurnOff -Force }
'ok'`, escapePS(e.name()))
	c, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if _, err := runner().RunPS(c, script); err != nil {
		return fmt.Errorf("停止虚拟机失败: %w", err)
	}
	return nil
}

func (e *hypervEnv) Delete(ctx context.Context) error {
	if e.cfg.usingQEMU() {
		return e.qemuDelete(ctx)
	}
	e.dropGuest()
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s' -ErrorAction SilentlyContinue
if ($vm) {
  if ($vm.State -ne 'Off') { Stop-VM -VM $vm -Force -TurnOff -ErrorAction SilentlyContinue }
  Remove-VM -VM $vm -Force
}
'ok'
`, escapePS(e.name()))
	if _, err := runner().RunPS(ctx, script); err != nil {
		return fmt.Errorf("删除虚拟机失败: %w", err)
	}
	// VM folder: VHDX, seed disk, Hyper-V config. Downloaded ISOs stay in <store>/iso.
	if e.cfg.StoreRoot != "" && e.envID != "" {
		dir := vmDir(e.cfg.StoreRoot, e.envID)
		if strings.HasPrefix(filepath.Base(dir), "agentos-") {
			var err error
			for i := 0; i < 10; i++ { // vmms may hold the VHDX for a moment
				if err = os.RemoveAll(dir); err == nil {
					break
				}
				time.Sleep(time.Second)
			}
			if err != nil {
				return fmt.Errorf("虚拟机已删除，但清理磁盘目录失败: %w", err)
			}
		}
	} else if e.cfg.VHDPath != "" {
		os.Remove(e.cfg.VHDPath)
	}
	return nil
}

// guestEnv returns the SSH handle to the guest, re-resolving the IP when needed
// (the Default Switch hands out a new subnet after a host reboot).
func (e *hypervEnv) guestEnv(ctx context.Context) (env.Env, error) {
	if !e.cfg.hasGuestAccess() {
		return nil, env.ErrUnsupported
	}
	e.mu.Lock()
	addr, fresh := e.addr, time.Since(e.addrAt) < 30*time.Second
	if e.guest != nil && e.guestFor == addr && addr != "" {
		g := e.guest
		e.mu.Unlock()
		return g, nil
	}
	e.mu.Unlock()
	if e.cfg.usingQEMU() {
		addr = "127.0.0.1"
		if e.cfg.GuestAddress != "" {
			addr = e.cfg.GuestAddress
		}
		g, err := openGuestSSH(ctx, e.cfg, addr)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		if e.guest != nil {
			e.guest.Close()
		}
		e.guest, e.guestFor, e.addr, e.addrAt = g, addr, addr, time.Now()
		e.mu.Unlock()
		return g, nil
	}
	if addr == "" || !fresh {
		v, err := getVMInfo(ctx, e.name(), e.cfg.MACAddress)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(v.State, "Running") {
			return nil, fmt.Errorf("虚拟机未运行（%s）", v.State)
		}
		addr = v.address()
		if addr == "" {
			addr = e.cfg.GuestAddress
		}
	}
	if addr == "" {
		return nil, fmt.Errorf("hyperv: 尚未获取客户机 IP（系统可能仍在启动）")
	}
	g, err := openGuestSSH(ctx, e.cfg, addr)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.guest != nil {
		e.guest.Close()
	}
	e.guest, e.guestFor, e.addr, e.addrAt = g, addr, addr, time.Now()
	e.mu.Unlock()
	return g, nil
}

func (e *hypervEnv) dropGuest() {
	e.mu.Lock()
	g := e.guest
	e.guest, e.guestFor = nil, ""
	e.mu.Unlock()
	if g != nil {
		g.Close()
	}
}

// withGuest runs f on the guest handle, retrying once with a fresh handle
// (and fresh IP) when the cached connection fails to dial.
func withGuest[T any](e *hypervEnv, ctx context.Context, f func(env.Env) (T, error)) (T, error) {
	var zero T
	g, err := e.guestEnv(ctx)
	if err != nil {
		return zero, err
	}
	r, err := f(g)
	if err != nil && isConnErr(err) {
		e.dropGuest()
		e.mu.Lock()
		e.addrAt = time.Time{}
		e.mu.Unlock()
		if g, err2 := e.guestEnv(ctx); err2 == nil {
			return f(g)
		}
	}
	return r, err
}

func isConnErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "dial") || strings.Contains(s, "connection") || strings.Contains(s, "EOF") || strings.Contains(s, "timeout")
}

func (e *hypervEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	return withGuest(e, ctx, func(g env.Env) (env.ExecSession, error) { return g.Exec(ctx, req) })
}

func (e *hypervEnv) PutFile(ctx context.Context, path string, r io.Reader, mode uint32) error {
	g, err := e.guestEnv(ctx)
	if err != nil {
		return err
	}
	return g.PutFile(ctx, path, r, mode)
}

func (e *hypervEnv) GetFile(ctx context.Context, path string) (io.ReadCloser, error) {
	return withGuest(e, ctx, func(g env.Env) (io.ReadCloser, error) { return g.GetFile(ctx, path) })
}

func (e *hypervEnv) ListDir(ctx context.Context, path string) ([]env.FileInfo, error) {
	return withGuest(e, ctx, func(g env.Env) ([]env.FileInfo, error) { return g.ListDir(ctx, path) })
}

// Desktop relays the guest VNC server. Zero-touch guests: through the SSH
// tunnel to guest loopback. Legacy (0.1.4) guests: direct TCP to IP:vnc_port.
func (e *hypervEnv) Desktop(ctx context.Context, kind string) (io.ReadWriteCloser, error) {
	if kind != "vnc" {
		return nil, env.ErrUnsupported
	}
	if e.cfg.usingQEMU() {
		// Prefer guest x11vnc (GNOME desktop) when SSH is ready; fall back to QEMU VNC.
		if !e.cfg.hasGuestAccess() {
			return e.qemuDesktop(ctx)
		}
		c, err := withGuest(e, ctx, func(g env.Env) (io.ReadWriteCloser, error) { return g.Desktop(ctx, kind) })
		if err != nil {
			if q, qerr := e.qemuDesktop(ctx); qerr == nil {
				return q, nil
			}
			return nil, fmt.Errorf("hyperv/qemu: 客户机桌面暂不可用：%v", err)
		}
		return c, nil
	}
	if e.cfg.hasGuestAccess() {
		c, err := withGuest(e, ctx, func(g env.Env) (io.ReadWriteCloser, error) { return g.Desktop(ctx, kind) })
		if err != nil {
			return nil, fmt.Errorf("hyperv: 客户机桌面（VNC）暂不可用：%v。系统刚启动时请稍等片刻，或使用「在 Hyper-V 窗口中打开」", err)
		}
		return c, nil
	}
	st, err := e.Status(ctx)
	if err != nil {
		return nil, err
	}
	addr := st.Address
	if addr == "" {
		addr = e.cfg.GuestAddress
	}
	if addr == "" {
		return nil, fmt.Errorf("hyperv: 尚无客户机 IP。请完成系统安装并确保 Hyper-V 能读到 IP（集成服务），或在客户机启用 VNC 后重试")
	}
	port := e.cfg.VNCPort
	if port <= 0 {
		port = 5900
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", addr, port))
	if err != nil {
		return nil, fmt.Errorf("hyperv: 无法连接客户机 VNC %s:%d（可使用「在 Hyper-V 窗口中打开」）：%w", addr, port, err)
	}
	return conn, nil
}

func (e *hypervEnv) Metrics(ctx context.Context) (env.Metrics, error) {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s' -ErrorAction Stop
$disk = 0; $used = 0
try { $v = Get-VHD -VMId $vm.VMId | Select-Object -First 1; $disk = [int64]$v.Size; $used = [int64]$v.FileSize } catch {}
[pscustomobject]@{ cpu = [double]$vm.CPUUsage; mem = [int64]$vm.MemoryAssigned; disk = $disk; disk_used = $used } | ConvertTo-Json -Compress
`, escapePS(e.name()))
	var res struct {
		CPU      float64 `json:"cpu"`
		Mem      int64   `json:"mem"`
		Disk     int64   `json:"disk"`
		DiskUsed int64   `json:"disk_used"`
	}
	c, cancel := withTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := psJSON(c, script, &res); err != nil {
		return env.Metrics{}, err
	}
	m := env.Metrics{CPUPercent: res.CPU, MemTotal: uint64(int64(e.cfg.MemoryMB) << 20), DiskTotal: uint64(res.Disk), DiskUsed: uint64(res.DiskUsed)}
	// guest view (real memory use, file system) when the guest is reachable
	e.mu.Lock()
	g := e.guest
	e.mu.Unlock()
	if g != nil {
		gc, gcancel := context.WithTimeout(ctx, 8*time.Second)
		gm, err := g.Metrics(gc)
		gcancel()
		if err == nil && gm.MemTotal > 0 {
			m.MemUsed, m.MemTotal = gm.MemUsed, gm.MemTotal
			if gm.DiskTotal > 0 {
				m.DiskUsed, m.DiskTotal = gm.DiskUsed, gm.DiskTotal
			}
			return m, nil
		}
	}
	m.MemUsed = uint64(res.Mem)
	return m, nil
}

// Screenshot implements env.Screenshotter: the VM console thumbnail from
// Hyper-V (works during installation, before the guest has any network).
func (e *hypervEnv) Screenshot(ctx context.Context, width int) ([]byte, error) {
	if e.cfg.usingQEMU() {
		return e.qemuScreenshot(ctx, width)
	}
	if width <= 0 || width > 1920 {
		width = 1024
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$name = '%s'
$w = %d
$hres = 1024; $vres = 768
try { $v = Get-VMVideo -VMName $name; if ($v.HorizontalResolution -gt 0) { $hres = $v.HorizontalResolution; $vres = $v.VerticalResolution } } catch {}
$h = [int]([math]::Round($w * $vres / $hres))
$cs = Get-WmiObject -Namespace root\virtualization\v2 -Class Msvm_ComputerSystem -Filter "ElementName='$name'"
if (-not $cs) { throw 'VM not found' }
$vssd = $cs.GetRelated('Msvm_VirtualSystemSettingData') | Where-Object { $_.VirtualSystemType -eq 'Microsoft:Hyper-V:System:Realized' } | Select-Object -First 1
$svc = Get-WmiObject -Namespace root\virtualization\v2 -Class Msvm_VirtualSystemManagementService
$r = $svc.GetVirtualSystemThumbnailImage($vssd, [uint16]$w, [uint16]$h)
if ($r.ReturnValue -ne 0 -or -not $r.ImageData) { throw ('thumbnail failed: ' + $r.ReturnValue) }
'{0}x{1}:{2}' -f $w, $h, [Convert]::ToBase64String($r.ImageData)
`, escapePS(e.name()), width)
	c, cancel := withTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := runner().RunPS(c, script)
	if err != nil {
		return nil, err
	}
	return rgb565ToPNG(strings.TrimSpace(out))
}

// rgb565ToPNG decodes "WxH:base64(RGB565 little-endian)".
func rgb565ToPNG(s string) ([]byte, error) {
	var w, h int
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return nil, fmt.Errorf("bad thumbnail data")
	}
	if _, err := fmt.Sscanf(s[:i], "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("bad thumbnail size %q", s[:i])
	}
	raw, err := base64.StdEncoding.DecodeString(s[i+1:])
	if err != nil {
		return nil, err
	}
	if len(raw) < w*h*2 {
		return nil, fmt.Errorf("thumbnail truncated (%d < %d)", len(raw), w*h*2)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := uint16(raw[2*(y*w+x)]) | uint16(raw[2*(y*w+x)+1])<<8
			r := uint8((p >> 11) & 0x1f)
			g := uint8((p >> 5) & 0x3f)
			b := uint8(p & 0x1f)
			img.SetRGBA(x, y, color.RGBA{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// OpenNative implements env.NativeOpener (only meaningful when the server runs
// on the Hyper-V host itself, which it always does for this provider):
//
//	kind "console": vmconnect.exe (Hyper-V window; enhanced session for Windows guests)
//	kind "rdp":     mstsc.exe with the generated credentials pre-stored (Windows guests)
func (e *hypervEnv) OpenNative(ctx context.Context, kind string) error {
	if runtime.GOOS != "windows" {
		return errNotWindows
	}
	switch kind {
	case "", "console":
		exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "vmconnect.exe")
		// via ShellExecute ("start") so a non-elevated AgentOS gets the UAC prompt vmconnect needs
		return exec.Command("cmd.exe", "/c", "start", "", exe, "localhost", e.name()).Start()
	case "rdp":
		st, _ := e.Status(ctx)
		addr := st.Address
		if addr == "" {
			return fmt.Errorf("尚未获取客户机 IP")
		}
		if e.cfg.GuestPassword != "" {
			exec.Command("cmdkey", "/generic:TERMSRV/"+addr, "/user:"+e.cfg.GuestUser, "/pass:"+e.cfg.GuestPassword).Run()
		}
		rdp := filepath.Join(os.TempDir(), "agentos-"+sanitizeName(e.name())+".rdp")
		body := strings.Join([]string{
			"full address:s:" + fmt.Sprintf("%s:%d", addr, e.cfg.RDPPort),
			"username:s:" + e.cfg.GuestUser,
			"prompt for credentials:i:0", "authentication level:i:0", "screen mode id:i:1",
			"dynamic resolution:i:1", "smart sizing:i:1", "desktopwidth:i:1600", "desktopheight:i:900",
			"redirectclipboard:i:1", "audiomode:i:0", "autoreconnection enabled:i:1",
			"session bpp:i:32", "use multimon:i:0", "enablecredsspsupport:i:1",
		}, "\r\n") + "\r\n"
		if err := os.WriteFile(rdp, []byte(body), 0o600); err != nil {
			return err
		}
		return exec.Command("mstsc.exe", rdp).Start()
	}
	return env.ErrUnsupported
}

func (e *hypervEnv) Close() error {
	e.dropGuest()
	return nil
}
