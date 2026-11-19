package hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
	sshprov "github.com/agentos/agentos/internal/env/providers/ssh"
	"golang.org/x/crypto/ssh"
)

func publicFromPrivate(privPEM string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privPEM))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

// keyboardScript types into the VM console through Msvm_Keyboard (the same
// mechanism Packer's Hyper-V builder uses for boot commands).
func keyboardScript(vm string, body string) string {
	return fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$cs = Get-WmiObject -Namespace root\virtualization\v2 -Class Msvm_ComputerSystem -Filter "ElementName='%s'"
if (-not $cs) { throw 'VM not found' }
$kb = $cs.GetRelated('Msvm_Keyboard') | Select-Object -First 1
function T([string]$s) { [void]$kb.TypeText($s) }
function K([int]$vk) { [void]$kb.TypeKey($vk) }
%s
'ok'
`, strings.ReplaceAll(escapePS(vm), `"`, ``), body)
}

func psStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// bootKeys handles the installer boot menu right after the first power-on.
func bootKeys(ctx context.Context, c Config) error {
	var body strings.Builder
	switch c.GuestOS {
	case GuestUbuntu2404:
		// GRUB on the desktop ISO: open the command line ('c'), Esc+c again a few
		// seconds later in case the menu was not up yet (Esc leaves the command
		// line, so the pair is idempotent), then boot the live installer with
		// autoinstall (no confirmation prompt) and noprompt (no "remove medium").
		body.WriteString("Start-Sleep -Seconds 9\nK 0x43\nStart-Sleep -Seconds 5\nK 0x1B\nStart-Sleep -Milliseconds 800\nK 0x43\nStart-Sleep -Milliseconds 800\n")
		for _, l := range UbuntuGrubCommands(ubuntuKernel, ubuntuInitrd) {
			body.WriteString("T " + psStr(l) + "\nK 0x0D\nStart-Sleep -Milliseconds 600\n")
		}
		env.Logf(ctx, "info", "向安装引导菜单发送 autoinstall 启动参数 / typing GRUB autoinstall command")
	case GuestWindows11:
		// "Press any key to boot from CD or DVD..." — tap space for ~25 s.
		body.WriteString("for ($i = 0; $i -lt 35; $i++) { K 0x20; Start-Sleep -Milliseconds 700 }\n")
		env.Logf(ctx, "info", "确认从安装光盘启动 / pressing a key for the DVD boot prompt")
	case GuestFedora:
		// netinst GRUB defaults to "Test this media & install"; pick "Install".
		body.WriteString("Start-Sleep -Seconds 9\nK 0x26\nStart-Sleep -Milliseconds 400\nK 0x0D\n")
	default:
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := runner().RunPS(cctx, keyboardScript(c.VMName, body.String()))
	return err
}

// Desktop ISO boot files (checked against ubuntu-24.04.5.1-desktop-amd64.iso).
var (
	ubuntuKernel = "/casper/vmlinuz"
	ubuntuInitrd = "/casper/initrd"
)

type vmInfo struct {
	Missing   bool     `json:"missing"`
	Name      string   `json:"Name"`
	Id        string   `json:"Id"`
	State     string   `json:"State"`
	CPUUsage  int      `json:"CPUUsage"`
	Memory    int64    `json:"MemoryAssigned"`
	Uptime    int64    `json:"Uptime"`
	Heartbeat string   `json:"Heartbeat"`
	IPs       []string `json:"IPs"`
	Neighbor  []string `json:"Neighbor"`
}

func vmInfoScript(name, mac string) string {
	return fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$vm = Get-VM -Name '%s' -ErrorAction SilentlyContinue
if (-not $vm) { '{"missing":true}'; return }
$ips = @(); $nb = @(); $hb = ''
try {
  $na = Get-VMNetworkAdapter -VMName $vm.Name | Select-Object -First 1
  if ($na -and $na.IPAddresses) { $ips = @($na.IPAddresses | Where-Object { $_ -match '^\d+\.\d+\.\d+\.\d+$' -and $_ -notmatch '^169\.254\.' }) }
  $mac = '%s'
  if (-not $mac -and $na) { $mac = $na.MacAddress }
  if ($mac -and $ips.Count -eq 0) {
    $dash = ($mac -replace '(..)(?!$)', '$1-').ToUpper()
    $nb = @(Get-NetNeighbor -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.LinkLayerAddress -eq $dash -and $_.State -ne 'Unreachable' -and $_.IPAddress -notmatch '^169\.254\.' } | ForEach-Object { $_.IPAddress })
  }
  $hbs = Get-VMIntegrationService -VMName $vm.Name -ErrorAction SilentlyContinue | Where-Object { $_.Id -match '84EAAE65' -or $_.Name -match 'Heartbeat|检测信号' } | Select-Object -First 1
  if ($hbs) { $hb = [string]$hbs.PrimaryStatusDescription }
} catch {}
[pscustomobject]@{
  missing = $false; Name = $vm.Name; Id = $vm.VMId.Guid; State = $vm.State.ToString()
  CPUUsage = [int]$vm.CPUUsage; MemoryAssigned = [int64]$vm.MemoryAssigned; Uptime = [int64]$vm.Uptime.TotalSeconds
  Heartbeat = $hb; IPs = @($ips); Neighbor = @($nb)
} | ConvertTo-Json -Compress
`, escapePS(name), escapePS(strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", "")))
}

func getVMInfo(ctx context.Context, name, mac string) (vmInfo, error) {
	var v vmInfo
	c, cancel := withTimeout(ctx, 30*time.Second)
	defer cancel()
	err := psJSON(c, vmInfoScript(name, mac), &v)
	return v, err
}

func (v vmInfo) address() string {
	if len(v.IPs) > 0 {
		return v.IPs[0]
	}
	if len(v.Neighbor) > 0 {
		return v.Neighbor[0]
	}
	return ""
}

// sshConfigFor builds the inner ssh provider config for the guest.
func sshConfigFor(c Config, addr string) json.RawMessage {
	port := 22
	if c.SSHHostPort > 0 {
		port = c.SSHHostPort
	}
	m := map[string]any{
		"host": addr, "port": port, "user": c.GuestUser, "private_key": c.SSHPrivateKey,
		"vnc_port": c.VNCPort,
	}
	if c.isWindows() {
		m["os"] = "windows"
		m["shell"] = "powershell.exe -NoLogo"
		m["host_key_policy"] = sshprov.PolicyInsecure // sshd generates its key in the guest; NAT-only network
	} else {
		m["os"] = "linux"
		if c.HostKey != "" {
			m["host_key_policy"], m["host_key"] = sshprov.PolicyStrict, c.HostKey
		} else {
			m["host_key_policy"] = sshprov.PolicyInsecure
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func openGuestSSH(ctx context.Context, c Config, addr string) (env.Env, error) {
	return sshprov.New().Open(ctx, &env.Record{ID: "hyperv-guest", Config: sshConfigFor(c, addr)})
}

// probeGuest runs a readiness check over SSH; returns the command output.
func probeGuest(ctx context.Context, c Config, addr string) (string, error) {
	port := "22"
	if c.SSHHostPort > 0 {
		port = strconv.Itoa(c.SSHHostPort)
	}
	d := net.Dialer{Timeout: 4 * time.Second}
	cn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, port))
	if err != nil {
		return "", err
	}
	cn.Close()
	g, err := openGuestSSH(ctx, c, addr)
	if err != nil {
		return "", err
	}
	defer g.Close()
	cmd := []string{"sh", "-c", "cat /var/lib/agentos/installed 2>/dev/null && echo READY"}
	if c.isWindows() {
		cmd = []string{"powershell.exe", "-NoProfile", "-Command", "if (Test-Path C:\\ProgramData\\AgentOS\\ready.txt) { 'READY' } else { 'SETUP' }"}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, code, err := runCollect(cctx, g, cmd)
	if err != nil {
		return out, err
	}
	if code != 0 || !strings.Contains(out, "READY") {
		return out, fmt.Errorf("guest setup not finished (%s)", strings.TrimSpace(out))
	}
	return out, nil
}

func runCollect(ctx context.Context, g env.Env, cmd []string) (string, int, error) {
	s, err := g.Exec(ctx, env.ExecRequest{Cmd: cmd})
	if err != nil {
		return "", -1, err
	}
	defer s.Close()
	s.Stdin().Close()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.Stdout().Read(buf)
			out.Write(buf[:n])
			if err != nil {
				break
			}
		}
		close(done)
	}()
	code, err := s.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return out.String(), code, err
}

// probeVNC checks that the guest VNC server answers through the SSH tunnel.
func probeVNC(ctx context.Context, c Config, addr string) error {
	g, err := openGuestSSH(ctx, c, addr)
	if err != nil {
		return err
	}
	defer g.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := g.Desktop(cctx, "vnc")
	if err != nil {
		return err
	}
	defer conn.Close()
	buf := make([]byte, 12)
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() { n, err := conn.Read(buf); ch <- res{n, err} }()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if !strings.HasPrefix(string(buf[:r.n]), "RFB ") {
			return fmt.Errorf("unexpected VNC banner %q", buf[:r.n])
		}
		return nil
	case <-cctx.Done():
		return cctx.Err()
	}
}

// waitGuestReady polls until the OS is installed and the AgentOS guest setup
// finished (SSH login with the generated key + ready marker).
func waitGuestReady(ctx context.Context, c *Config, started time.Time) (string, error) {
	limit := time.Duration(c.InstallTimeout) * time.Minute
	if limit <= 0 {
		limit = 90 * time.Minute
		if c.GuestOS == GuestUbuntu2404 {
			limit = 75 * time.Minute
		}
	}
	deadline := started.Add(limit)
	lastLog := time.Time{}
	lastState := ""
	offSince := time.Time{}
	sshSeen := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		v, err := getVMInfo(ctx, c.VMName, c.MACAddress)
		addr := ""
		if err == nil {
			if v.Missing {
				return "", fmt.Errorf("虚拟机 %s 已不存在", c.VMName)
			}
			addr = v.address()
			if strings.EqualFold(v.State, "Off") {
				if offSince.IsZero() {
					offSince = time.Now()
				}
				if time.Since(offSince) > 90*time.Second {
					env.Logf(ctx, "warn", "安装过程中虚拟机已关机，自动重新启动 / VM powered off during install, starting it again")
					runner().RunPS(ctx, fmt.Sprintf("Start-VM -Name '%s' -ErrorAction SilentlyContinue; 'ok'", escapePS(c.VMName)))
					offSince = time.Time{}
				}
			} else {
				offSince = time.Time{}
			}
		}
		phase := "安装系统"
		if addr != "" {
			out, perr := probeGuest(ctx, *c, addr)
			if perr == nil {
				if c.EnableVNC {
					// the desktop session (autologin) starts the VNC server shortly after boot
					vdeadline := time.Now().Add(4 * time.Minute)
					for {
						if verr := probeVNC(ctx, *c, addr); verr == nil {
							env.Logf(ctx, "info", "桌面 VNC 已就绪（经 SSH 隧道）/ desktop VNC ready")
							break
						} else if time.Now().After(vdeadline) {
							env.Logf(ctx, "warn", "桌面 VNC 未就绪：%v（终端/文件可用；可稍后重试桌面页签）", verr)
							break
						}
						time.Sleep(10 * time.Second)
					}
				}
				return addr, nil
			}
			if strings.Contains(perr.Error(), "not finished") || strings.Contains(out, "SETUP") {
				phase = "配置客户机（SSH 已连通）"
				if !sshSeen {
					sshSeen = true
					env.Logf(ctx, "info", "客户机 SSH 已连通，等待首次登录配置完成 / SSH up at %s, waiting for guest setup", addr)
				}
			}
		}
		state := v.State
		if time.Since(lastLog) > 60*time.Second || state != lastState {
			ip := addr
			if ip == "" {
				ip = "—"
			}
			env.Logf(ctx, "info", "%s… 已用 %s（虚拟机 %s，IP %s，CPU %d%%）", phase, fmtDur(time.Since(started)), state, ip, v.CPUUsage)
			lastLog, lastState = time.Now(), state
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("等待系统安装超时（%s）。可在「在 Hyper-V 窗口中打开」查看安装界面；日志见环境日志", fmtDur(limit))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
}
