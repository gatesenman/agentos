//go:build windows

package hyperv

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

func detectQEMUWSLPlatform(ctx context.Context) DetectResult {
	distro := DefaultWSLDistro
	out, err := wslRun(ctx, distro, "command", "-v", "qemu-system-x86_64")
	if err != nil {
		// Distro missing or qemu missing — try listing distros for a clearer reason.
		list, _ := exec.CommandContext(ctx, "wsl.exe", "-l", "-q").CombinedOutput()
		names := strings.ReplaceAll(string(list), "\x00", "")
		if !strings.Contains(names, distro) {
			return DetectResult{Available: false, Reason: "未找到 WSL 发行版 " + distro + "（可用于本机虚拟机 QEMU 回退）。可先安装 Ubuntu WSL 并创建该发行版。"}
		}
		return DetectResult{Available: false, Reason: "WSL 发行版 " + distro + " 中未安装 qemu-system-x86_64：" + strings.TrimSpace(string(out)) + errSuffix(err)}
	}
	ver := strings.TrimSpace(string(out))
	if ver == "" {
		ver = "qemu-system-x86_64"
	}
	// Soft check for KVM (best-effort; TCG still works).
	_, _ = wslRun(ctx, distro, "sh", "-c", "test -e /dev/kvm && echo kvm || echo tcg")
	return DetectResult{Available: true, Version: ver, Backend: BackendQEMUWSL}
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

func wslRun(ctx context.Context, distro string, args ...string) ([]byte, error) {
	a := append([]string{"-d", distro, "--"}, args...)
	cmd := exec.CommandContext(ctx, "wsl.exe", a...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func wslBash(ctx context.Context, distro, script string) ([]byte, error) {
	return wslRun(ctx, distro, "bash", "-lc", script)
}

// ensureQEMUDeps installs qemu/ovmf/swtpm inside the distro when missing (best-effort).
func ensureQEMUDeps(ctx context.Context, distro string) error {
	script := `
set -e
export DEBIAN_FRONTEND=noninteractive
need=""
command -v qemu-system-x86_64 >/dev/null || need="$need qemu-system-x86"
command -v qemu-img >/dev/null || need="$need qemu-utils"
[ -f /usr/share/OVMF/OVMF_CODE_4M.fd ] || [ -f /usr/share/OVMF/OVMF_CODE.fd ] || need="$need ovmf"
command -v swtpm >/dev/null || need="$need swtpm"
if [ -n "$need" ]; then
  sudo -n apt-get update -qq || apt-get update -qq
  sudo -n apt-get install -y -qq $need || apt-get install -y -qq $need
fi
command -v qemu-system-x86_64 >/dev/null
`
	_, err := wslBash(ctx, distro, script)
	return err
}

func (provider) createQEMUWSL(ctx context.Context, rec *env.Record, c *Config, save func(map[string]any)) error {
	distro := defaultDistro(*c)
	c.Backend = BackendQEMUWSL
	c.WSLDistro = distro
	env.Logf(ctx, "info", "Hyper-V 不可用或无权限，改用 WSL QEMU 回退（发行版 %s）", distro)

	if err := ensureQEMUDeps(ctx, distro); err != nil {
		return fmt.Errorf("hyperv/qemu: 准备 WSL QEMU 依赖失败: %w", err)
	}

	dir := qemuDir(c.StoreRoot, rec.ID)
	if err := ensureDir(dir); err != nil {
		return err
	}
	wslDir := winToWSL(dir)
	runtimeDir := qemuRuntimeDir(rec.ID)
	c.QEMURuntime = runtimeDir

	// Ports: SSH hostfwd + QEMU VNC display
	sshPort, err := allocHostPort(2200, 2299)
	if err != nil {
		return err
	}
	vncDisp := 0
	vncPort := 0
	for d := 90; d <= 99; d++ {
		p := 5900 + d
		ln, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if e != nil {
			continue
		}
		ln.Close()
		vncDisp, vncPort = d, p
		break
	}
	if vncPort == 0 {
		return fmt.Errorf("hyperv/qemu: 无可用 VNC 端口 5990–5999")
	}
	c.SSHHostPort = sshPort
	c.QEMUVNCPort = vncPort
	c.GuestAddress = "127.0.0.1"
	c.EnableVNC = true // host-side QEMU VNC always available as fallback desktop
	save(map[string]any{
		"backend": BackendQEMUWSL, "wsl_distro": distro,
		"ssh_host_port": sshPort, "qemu_vnc_port": vncPort,
		"guest_address": "127.0.0.1", "store_root": c.StoreRoot,
		"vm_name": c.VMName, "phase": PhaseDownload, "qemu_runtime": runtimeDir,
		"notes": "经 WSL QEMU 运行（Hyper-V 不可用或无权限时自动回退）",
	})

	// 1. ISO (reuse Hyper-V resolver)
	c.Phase = PhaseDownload
	isoPath, needsISO, note, err := resolveISO(ctx, c)
	if err != nil {
		return err
	}
	c.NeedsISO, c.Notes = needsISO, note
	if isoPath != "" {
		c.ISOPath = isoPath
	}
	if needsISO {
		env.Logf(ctx, "warn", "%s", note)
		save(map[string]any{"needs_iso": true, "notes": note, "phase": ""})
		return nil
	}

	if c.GuestOS == GuestUbuntu2404 && isoPath != "" {
		if cfg, err := isoReadFile(isoPath, "boot/grub/grub.cfg"); err == nil {
			if k, i := grubBootFiles(cfg); k != "" && i != "" {
				ubuntuKernel, ubuntuInitrd = k, i
			}
		}
	}

	// 2. seed (reuse buildSeed — do not fork)
	seedPath := ""
	if c.unattended() && isoPath != "" {
		seedPath = filepath.Join(dir, "seed.iso")
		if err := buildSeed(ctx, *c, rec.ID, seedPath); err != nil {
			return fmt.Errorf("hyperv/qemu: 生成自动安装应答盘失败: %w", err)
		}
	}

	// 3. disk + launch script
	c.Phase = PhaseCreate
	save(map[string]any{"phase": PhaseCreate, "iso_path": c.ISOPath})
	diskHost := filepath.Join(dir, "disk.qcow2")
	if _, err := os.Stat(diskHost); err != nil {
		gb := c.DiskGB
		if gb < 20 {
			gb = 40
		}
		_, err := wslBash(ctx, distro, fmt.Sprintf(`mkdir -p %q && qemu-img create -f qcow2 %q %dG`, wslDir, wslDir+"/disk.qcow2", gb))
		if err != nil {
			return fmt.Errorf("hyperv/qemu: 创建磁盘失败: %w", err)
		}
	}

	useTPM := c.GuestOS == GuestWindows11
	runSh := filepath.Join(dir, "run.sh")
	var direct *qemuDirectKernel
	if c.GuestOS == GuestUbuntu2404 && isoPath != "" && c.unattended() {
		bootDir := filepath.Join(dir, "boot")
		if err := os.MkdirAll(bootDir, 0o755); err != nil {
			return fmt.Errorf("hyperv/qemu: 创建 boot 目录失败: %w", err)
		}
		kHost := filepath.Join(bootDir, "vmlinuz")
		iHost := filepath.Join(bootDir, "initrd")
		env.Logf(ctx, "info", "从 ISO 提取内核/initrd 用于 -kernel 直启 autoinstall（跳过 GRUB QMP）")
		if err := isoExtractFile(isoPath, strings.TrimPrefix(ubuntuKernel, "/"), kHost); err != nil {
			return fmt.Errorf("hyperv/qemu: 提取内核失败 (%s): %w", ubuntuKernel, err)
		}
		if err := isoExtractFile(isoPath, strings.TrimPrefix(ubuntuInitrd, "/"), iHost); err != nil {
			return fmt.Errorf("hyperv/qemu: 提取 initrd 失败 (%s): %w", ubuntuInitrd, err)
		}
		// Desktop live squashfs ships empty NoCloud seeds that shadow CIDATA.
		// Inject autoinstall via casper-bottom inside the zstd initrd payload.
		if seedPath != "" {
			ud, md, err := readSeedISOFiles(seedPath)
			if err != nil {
				return fmt.Errorf("hyperv/qemu: 读取 seed 失败: %w", err)
			}
			env.Logf(ctx, "info", "向 initrd 注入 NoCloud autoinstall（casper-bottom + zstd）")
			if err := patchUbuntuInitrdSeed(iHost, ud, md); err != nil {
				return fmt.Errorf("hyperv/qemu: 修补 initrd seed 失败: %w", err)
			}
		}
		direct = &qemuDirectKernel{
			KernelWSL: winToWSL(kHost),
			InitrdWSL: winToWSL(iHost),
			Append:    UbuntuAutoinstallAppend,
		}
	}
	if err := writeQEMURunScript(runSh, wslDir, runtimeDir, winToWSL(isoPath), seedPath, *c, sshPort, vncDisp, useTPM, direct); err != nil {
		return err
	}
	// Normalize to LF for bash
	if b, err := os.ReadFile(runSh); err == nil {
		os.WriteFile(runSh, bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), 0o755)
	}

	env.Logf(ctx, "info", "启动 QEMU（SSH 本机 %d → 客户机 :22，VNC :%d；sockets %s）", sshPort, vncPort, runtimeDir)
	if err := qemuLaunch(ctx, distro, wslDir, runtimeDir); err != nil {
		return err
	}
	started := time.Now()
	save(map[string]any{
		"backend": BackendQEMUWSL, "ssh_host_port": sshPort, "qemu_vnc_port": vncPort,
		"guest_address": "127.0.0.1", "phase": PhaseInstall, "iso_path": c.ISOPath,
		"qemu_runtime": runtimeDir,
		"notes": "经 WSL QEMU · 正在无人值守安装",
	})
	if c.unattended() && isoPath != "" && direct == nil {
		// Ubuntu uses -kernel/-append (direct != nil); Windows/Fedora still need QMP keys.
		if err := qemuBootKeys(ctx, distro, runtimeDir, dir, *c); err != nil {
			env.Logf(ctx, "warn", "发送安装引导按键失败: %v", err)
		}
	} else if direct != nil {
		env.Logf(ctx, "info", "已用 -kernel/-append 注入 autoinstall，跳过 GRUB QMP 键入")
	}

	if !c.unattended() {
		c.Notes = "虚拟机已通过 WSL QEMU 启动，请在「远程桌面」查看安装过程。"
		save(map[string]any{"notes": c.Notes, "phase": ""})
		return nil
	}

	env.Logf(ctx, "info", "%s", installBanner(*c))
	// Wait for SSH on localhost hostfwd
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(c.InstallTimeout)*time.Minute)
	if c.InstallTimeout <= 0 {
		cancel()
		waitCtx, cancel = context.WithTimeout(ctx, 3*time.Hour)
	}
	defer cancel()
	env.Logf(ctx, "info", "等待客户机 SSH（127.0.0.1:%d）…", sshPort)
	c.SSHHostPort = sshPort
	c.GuestAddress = "127.0.0.1"
	ready := false
	lastProg := time.Time{}
	for {
		select {
		case <-waitCtx.Done():
			// If QEMU is still up, keep phase=install and succeed Create so UI shows 安装中
			// rather than sticky error (ResumeCreate / Status will keep surfacing progress).
			running := false
			{
				out, _ := wslBash(ctx, distro, fmt.Sprintf(`[ -f %s/qemu.pid ] && kill -0 "$(cat %s/qemu.pid)" 2>/dev/null && echo RUN || echo OFF`, shellQuote(runtimeDir), shellQuote(runtimeDir)))
				running = strings.Contains(string(out), "RUN")
			}
			if running {
				env.Logf(ctx, "warn", "安装仍在进行（QEMU 运行中），先返回安装中状态 / still installing, surfacing busy")
				save(map[string]any{"phase": PhaseInstall, "notes": "经 WSL QEMU · 正在无人值守安装（SSH 尚未就绪）", "qemu_runtime": runtimeDir})
				return nil
			}
			save(map[string]any{"phase": PhaseInstall, "qemu_runtime": runtimeDir})
			return fmt.Errorf("hyperv/qemu: 等待客户机就绪超时: %w", waitCtx.Err())
		default:
		}
		_ = waitTCP(waitCtx, "127.0.0.1", sshPort, 8*time.Second)
		out, err := probeGuest(waitCtx, *c, "127.0.0.1")
		if err == nil && strings.TrimSpace(out) != "" {
			ready = true
			break
		}
		if time.Since(lastProg) > 60*time.Second {
			lastProg = time.Now()
			env.Logf(ctx, "info", "安装进行中…（已等待 %s）", fmtDur(time.Since(started)))
		}
		select {
		case <-waitCtx.Done():
			continue
		case <-time.After(10 * time.Second):
		}
	}
	if !ready {
		return fmt.Errorf("hyperv/qemu: 客户机就绪探测失败")
	}
	c.InstallSeconds = int(time.Since(started).Seconds())
	env.Logf(ctx, "info", "系统安装完成（QEMU），用时 %s", fmtDur(time.Since(started)))

	// Detach install ISO for next boot (rewrite run script without CD, keep disk)
	if err := writeQEMURunScript(runSh, wslDir, runtimeDir, "", "", *c, sshPort, vncDisp, useTPM, nil); err == nil {
		if b, err := os.ReadFile(runSh); err == nil {
			os.WriteFile(runSh, bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), 0o755)
		}
	}
	if seedPath != "" {
		os.Remove(seedPath)
	}

	noteReady := fmt.Sprintf("经 WSL QEMU · 账户 %s · SSH 127.0.0.1:%d", c.GuestUser, sshPort)
	save(map[string]any{
		"backend": BackendQEMUWSL, "phase": PhaseReady, "guest_address": "127.0.0.1",
		"ssh_host_port": sshPort, "qemu_vnc_port": vncPort, "install_seconds": c.InstallSeconds,
		"notes": noteReady, "enable_vnc": true, "qemu_runtime": runtimeDir,
	})
	return nil
}

func writeQEMURunScript(hostPath, wslDir, runtimeDir, isoWSL, seedHost string, c Config, sshPort, vncDisp int, useTPM bool, direct *qemuDirectKernel) error {
	seedWSL := ""
	if seedHost != "" {
		seedWSL = winToWSL(seedHost)
	}
	mem := c.MemoryMB
	if mem < 2048 {
		mem = 4096
	}
	cpus := c.CPUs
	if cpus < 2 {
		cpus = 4
	}
	install := isoWSL != ""
	// Ubuntu Desktop: SeaBIOS + optional -kernel (direct boot). OVMF + GRUB QMP
	// racing Esc/c was unreliable; -kernel/-append injects autoinstall stably.
	useOVMF := c.GuestOS != GuestUbuntu2404
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")
	fmt.Fprintf(&b, "DIR=%s\n", shellQuote(wslDir))
	fmt.Fprintf(&b, "RT=%s\n", shellQuote(runtimeDir))
	b.WriteString("mkdir -p \"$DIR\" \"$RT\"\n")
	b.WriteString("cd \"$DIR\"\n")
	b.WriteString("ACCEL=tcg\n[ -e /dev/kvm ] && ACCEL=kvm\n")
	b.WriteString("EXTRA=\"\"\n")
	if useOVMF {
		b.WriteString("[ -f vars.fd ] || cp /usr/share/OVMF/OVMF_VARS_4M.ms.fd vars.fd 2>/dev/null || cp /usr/share/OVMF/OVMF_VARS_4M.fd vars.fd 2>/dev/null || cp /usr/share/OVMF/OVMF_VARS.fd vars.fd\n")
		b.WriteString("CODE=/usr/share/OVMF/OVMF_CODE_4M.fd\n")
		b.WriteString("[ -f /usr/share/OVMF/OVMF_CODE_4M.ms.fd ] && CODE=/usr/share/OVMF/OVMF_CODE_4M.ms.fd\n")
		if useTPM {
			b.WriteString("mkdir -p \"$DIR/tpm\" \"$RT/tpm\"\n")
			b.WriteString("if [ ! -S \"$RT/tpm/sock\" ]; then swtpm socket --tpmstate dir=\"$DIR/tpm\" --ctrl type=unixio,path=\"$RT/tpm/sock\" --tpm2 -d || true; fi\n")
			b.WriteString("EXTRA=\"$EXTRA -chardev socket,id=chrtpm,path=$RT/tpm/sock -tpmdev emulator,id=tpm0,chardev=chrtpm -device tpm-tis,tpmdev=tpm0\"\n")
			b.WriteString("[ -f /usr/share/OVMF/OVMF_CODE_4M.ms.fd ] && CODE=/usr/share/OVMF/OVMF_CODE_4M.ms.fd\n")
		}
		fmt.Fprintf(&b, "exec qemu-system-x86_64 -machine q35,accel=$ACCEL -cpu max -smp %d -m %d \\\n", cpus, mem)
		b.WriteString("  -drive if=pflash,format=raw,readonly=on,file=$CODE -drive if=pflash,format=raw,file=vars.fd \\\n")
	} else {
		fmt.Fprintf(&b, "exec qemu-system-x86_64 -machine q35,accel=$ACCEL -cpu max -smp %d -m %d \\\n", cpus, mem)
	}
	if install {
		if direct != nil && direct.KernelWSL != "" {
			// Direct kernel boot: ISO still needed for casper squashfs; seed = CIDATA.
			fmt.Fprintf(&b, "  -drive file=%s,media=cdrom,if=none,id=cd0,readonly=on -device ide-cd,drive=cd0,bus=ide.0 \\\n", shellQuote(isoWSL))
			b.WriteString("  -drive file=disk.qcow2,if=none,id=hd,format=qcow2 -device virtio-blk-pci,drive=hd,bootindex=1 \\\n")
			if seedWSL != "" {
				fmt.Fprintf(&b, "  -drive file=%s,media=cdrom,if=none,id=cd1,readonly=on -device ide-cd,drive=cd1,bus=ide.1 \\\n", shellQuote(seedWSL))
			}
			fmt.Fprintf(&b, "  -kernel %s -initrd %s \\\n", shellQuote(direct.KernelWSL), shellQuote(direct.InitrdWSL))
			fmt.Fprintf(&b, "  -append %s \\\n", shellQuote(direct.Append))
		} else {
			// CD bootindex=1 so an empty HD does not steal UEFI; HD follows.
			fmt.Fprintf(&b, "  -drive file=%s,media=cdrom,if=none,id=cd0,readonly=on -device ide-cd,drive=cd0,bus=ide.0,bootindex=1 \\\n", shellQuote(isoWSL))
			b.WriteString("  -drive file=disk.qcow2,if=none,id=hd,format=qcow2 -device virtio-blk-pci,drive=hd,bootindex=2 \\\n")
			if seedWSL != "" {
				fmt.Fprintf(&b, "  -drive file=%s,media=cdrom,if=none,id=cd1,readonly=on -device ide-cd,drive=cd1,bus=ide.1 \\\n", shellQuote(seedWSL))
			}
		}
	} else {
		b.WriteString("  -drive file=disk.qcow2,if=none,id=hd,format=qcow2 -device virtio-blk-pci,drive=hd,bootindex=1 \\\n")
	}
	fmt.Fprintf(&b, "  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22 -device virtio-net-pci,netdev=n0 \\\n", sshPort)
	// AF_UNIX under $RT (Linux FS). DrvFS /mnt/* returns ENOTSUP on bind().
	fmt.Fprintf(&b, "  -vga std -usb -device usb-tablet -vnc 127.0.0.1:%d -qmp unix:$RT/qmp.sock,server,nowait $EXTRA \\\n", vncDisp)
	b.WriteString("  -serial file:$RT/serial.log -display none -pidfile $RT/qemu.pid -daemonize\n")
	return os.WriteFile(hostPath, []byte(b.String()), 0o755)
}

// qemuDirectKernel holds host-extracted installer kernel/initrd for -kernel boot.
type qemuDirectKernel struct {
	KernelWSL string
	InitrdWSL string
	Append    string
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func qemuLaunch(ctx context.Context, distro, wslDir, runtimeDir string) error {
	_, _ = wslBash(ctx, distro, fmt.Sprintf(`
RT=%s
mkdir -p "$RT"
if [ -f "$RT/qemu.pid" ]; then
  kill "$(cat "$RT/qemu.pid")" 2>/dev/null || true
  sleep 1
  kill -9 "$(cat "$RT/qemu.pid")" 2>/dev/null || true
  rm -f "$RT/qemu.pid" "$RT/qmp.sock"
fi
DIR=%s
if [ -f "$DIR/qemu.pid" ]; then
  kill "$(cat "$DIR/qemu.pid")" 2>/dev/null || true
  rm -f "$DIR/qemu.pid" "$DIR/qmp.sock"
fi
`, shellQuote(runtimeDir), shellQuote(wslDir)))
	_, err := wslBash(ctx, distro, fmt.Sprintf(`chmod +x %s/run.sh && %s/run.sh`, shellQuote(wslDir), shellQuote(wslDir)))
	return err
}

func runtimeOrDefault(c Config, envID string) string {
	if c.QEMURuntime != "" {
		return c.QEMURuntime
	}
	return qemuRuntimeDir(envID)
}

// qemuBootKeys injects installer boot-menu keystrokes over QMP (same sequence as Hyper-V Msvm_Keyboard).
// Helper scripts are written on the Windows store path (DrvFS) then executed inside WSL — avoids
// fragile multiline `wsl.exe bash -lc` argument packing that dropped RT= and broke heredocs.
func qemuBootKeys(ctx context.Context, distro, runtimeDir, hostDir string, c Config) error {
	var mode string
	var lines []string
	switch c.GuestOS {
	case GuestUbuntu2404:
		mode = "grub"
		lines = UbuntuGrubCommands(ubuntuKernel, ubuntuInitrd)
		env.Logf(ctx, "info", "向 QEMU GRUB 发送 autoinstall 启动参数 / typing GRUB autoinstall via QMP")
	case GuestWindows11:
		mode = "spaces"
		env.Logf(ctx, "info", "确认从安装光盘启动 / pressing a key for the DVD boot prompt via QMP")
	case GuestFedora:
		mode = "fedora"
		env.Logf(ctx, "info", "选择 Fedora Install 菜单项 / picking Install via QMP")
	default:
		return nil
	}
	grubText := ""
	if mode == "grub" {
		grubText = strings.Join(lines, "\n") + "\n"
	}
	py := `# -*- coding: utf-8 -*-
import json, socket, sys, time
sock, mode = sys.argv[1], sys.argv[2]
grubf = sys.argv[3] if len(sys.argv) > 3 else ""
grub = open(grubf, "r", encoding="utf-8").read() if grubf else ""
print("grub_lines", len(grub.splitlines()), "preview", repr(grub[:120]))
class QMP:
    def __init__(s, path):
        s.s = socket.socket(socket.AF_UNIX); s.s.connect(path); s.f = s.s.makefile("rw")
        s.f.readline(); s.cmd("qmp_capabilities")
    def cmd(s, ex, **a):
        s.f.write(json.dumps({"execute": ex, "arguments": a} if a else {"execute": ex}) + "\n"); s.f.flush()
        while True:
            r = json.loads(s.f.readline())
            if "return" in r or "error" in r: return r
    def keys(s, *qc):
        return s.cmd("send-key", keys=[{"type": "qcode", "data": k} for k in qc])
SPECIAL = {" ": "spc", "-": "minus", "/": "slash", ".": "dot", "=": "equal", ",": "comma",
           "_": ("shift", "minus"), ":": ("shift", "semicolon"), ";": "semicolon",
           "\\": "backslash", '"': ("shift", "apostrophe"), "'": "apostrophe"}
def type_text(q, t):
    for ch in t:
        if ch.isalpha() and ch.isupper(): q.keys("shift", ch.lower())
        elif ch.isalnum(): q.keys(ch)
        else:
            k = SPECIAL.get(ch)
            if not k: continue
            q.keys(*(k if isinstance(k, tuple) else (k,)))
        time.sleep(0.03)
q = QMP(sock)
if mode == "grub":
    time.sleep(9); q.keys("c"); time.sleep(5); q.keys("esc"); time.sleep(0.8); q.keys("c"); time.sleep(0.8)
    for l in grub.splitlines():
        type_text(q, l); q.keys("ret"); time.sleep(0.6)
elif mode == "spaces":
    for i in range(35): q.keys("spc"); time.sleep(0.7)
elif mode == "fedora":
    time.sleep(9); q.keys("up"); time.sleep(0.4); q.keys("ret")
print("ok")
`
	if err := ensureDir(hostDir); err != nil {
		return err
	}
	pyPath := filepath.Join(hostDir, "grub_boot.py")
	grubPath := filepath.Join(hostDir, "grub_cmds.txt")
	shPath := filepath.Join(hostDir, "grub_boot.sh")
	if err := os.WriteFile(pyPath, []byte(py), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(grubPath, []byte(grubText), 0o644); err != nil {
		return err
	}
	wslPy := winToWSL(pyPath)
	wslGrub := winToWSL(grubPath)
	sh := "#!/bin/bash\nset -e\n" +
		"RT=\"$1\"\nMODE=\"$2\"\nPY=\"$3\"\nGRUBF=\"$4\"\n" +
		"for i in $(seq 1 60); do [ -S \"$RT/qmp.sock\" ] && break; sleep 1; done\n" +
		"[ -S \"$RT/qmp.sock\" ] || { echo \"qmp missing RT=$RT\"; exit 1; }\n" +
		"python3 \"$PY\" \"$RT/qmp.sock\" \"$MODE\" \"$GRUBF\"\n"
	if err := os.WriteFile(shPath, []byte(sh), 0o755); err != nil {
		return err
	}
	// Normalize LF (Windows editors / Go on Windows may write CRLF)
	for _, f := range []string{pyPath, grubPath, shPath} {
		if b, err := os.ReadFile(f); err == nil {
			_ = os.WriteFile(f, bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), 0o755)
		}
	}
	cmd := fmt.Sprintf("bash %s %s %s %s %s",
		shellQuote(winToWSL(shPath)),
		shellQuote(runtimeDir),
		shellQuote(mode),
		shellQuote(wslPy),
		shellQuote(wslGrub),
	)
	_, err := wslBash(ctx, distro, cmd)
	return err
}


func (e *hypervEnv) qemuRunning() bool {
	if e.cfg.QEMUVNCPort > 0 {
		d := net.Dialer{Timeout: 800 * time.Millisecond}
		cn, err := d.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.cfg.QEMUVNCPort)))
		if err == nil {
			cn.Close()
			return true
		}
	}
	distro := defaultDistro(e.cfg)
	rt := runtimeOrDefault(e.cfg, e.envID)
	out, err := wslBash(context.Background(), distro, fmt.Sprintf(`[ -f %s/qemu.pid ] && kill -0 "$(cat %s/qemu.pid)" 2>/dev/null && echo RUN || echo OFF`, shellQuote(rt), shellQuote(rt)))
	return err == nil && strings.Contains(string(out), "RUN")
}

func (e *hypervEnv) qemuStatus(ctx context.Context) (env.Status, error) {
	running := e.qemuRunning()
	st := env.StateStopped
	msg := "已关机（QEMU）"
	if running {
		st = env.StateRunning
		msg = "运行中"
		// Soft-promote install → ready when SSH answers (Create may have returned early).
		if e.cfg.Phase == PhaseInstall && e.cfg.SSHHostPort > 0 && e.cfg.GuestUser != "" {
			pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			out, err := probeGuest(pctx, e.cfg, "127.0.0.1")
			cancel()
			if err == nil && strings.TrimSpace(out) != "" {
				e.cfg.Phase = PhaseReady
				e.cfg.Notes = fmt.Sprintf("经 WSL QEMU · 账户 %s · SSH 127.0.0.1:%d", e.cfg.GuestUser, e.cfg.SSHHostPort)
			}
		}
		if e.cfg.Phase == PhaseReady && e.cfg.GuestUser != "" {
			msg = fmt.Sprintf("运行中 · 账户 %s", e.cfg.GuestUser)
		} else if e.cfg.Phase == PhaseInstall {
			msg = "安装中（经 WSL QEMU）"
			st = env.StateCreating // keep UI busy while autoinstall runs
		}
	}
	if e.cfg.Notes != "" && (e.cfg.Phase == PhaseInstall || e.cfg.NeedsISO) {
		msg = e.cfg.Notes
	}
	addr := "127.0.0.1"
	e.mu.Lock()
	e.addr, e.addrAt = addr, time.Now()
	e.mu.Unlock()
	extra := map[string]string{
		"backend": BackendQEMUWSL, "guest_os": e.cfg.GuestOS,
		"fallback": "经 WSL QEMU",
	}
	if e.cfg.SSHHostPort > 0 {
		extra["ssh"] = fmt.Sprintf("%s@127.0.0.1:%d", e.cfg.GuestUser, e.cfg.SSHHostPort)
		extra["ssh_host_port"] = strconv.Itoa(e.cfg.SSHHostPort)
	}
	if e.cfg.QEMUVNCPort > 0 {
		extra["qemu_vnc"] = fmt.Sprintf("127.0.0.1:%d", e.cfg.QEMUVNCPort)
	}
	if e.cfg.Phase != "" {
		extra["phase"] = e.cfg.Phase
	}
	return env.Status{State: st, Message: msg, Address: addr, Extra: extra}, nil
}

func (e *hypervEnv) qemuStart(ctx context.Context) error {
	distro := defaultDistro(e.cfg)
	dir := winToWSL(qemuDir(e.cfg.StoreRoot, e.envID))
	rt := runtimeOrDefault(e.cfg, e.envID)
	if err := qemuLaunch(ctx, distro, dir, rt); err != nil {
		return fmt.Errorf("启动 QEMU 失败: %w", err)
	}
	return nil
}

func (e *hypervEnv) qemuStop(ctx context.Context) error {
	e.dropGuest()
	distro := defaultDistro(e.cfg)
	rt := runtimeOrDefault(e.cfg, e.envID)
	_, err := wslBash(ctx, distro, fmt.Sprintf(`
RT=%s
if [ -S "$RT/qmp.sock" ]; then
  printf '%%s\n' '{"execute":"qmp_capabilities"}' '{"execute":"system_powerdown"}' | timeout 5 socat - UNIX-CONNECT:"$RT/qmp.sock" 2>/dev/null || true
  sleep 8
fi
if [ -f "$RT/qemu.pid" ]; then
  kill "$(cat "$RT/qemu.pid")" 2>/dev/null || true
  sleep 1
  kill -9 "$(cat "$RT/qemu.pid")" 2>/dev/null || true
  rm -f "$RT/qemu.pid" "$RT/qmp.sock"
fi
`, shellQuote(rt)))
	return err
}

func (e *hypervEnv) qemuDelete(ctx context.Context) error {
	_ = e.qemuStop(ctx)
	distro := defaultDistro(e.cfg)
	rt := runtimeOrDefault(e.cfg, e.envID)
	_, _ = wslBash(ctx, distro, fmt.Sprintf(`rm -rf %s`, shellQuote(rt)))
	dir := qemuDir(e.cfg.StoreRoot, e.envID)
	return os.RemoveAll(dir)
}

func (e *hypervEnv) qemuDesktop(ctx context.Context) (io.ReadWriteCloser, error) {
	port := e.cfg.QEMUVNCPort
	if port <= 0 {
		return nil, fmt.Errorf("hyperv/qemu: 未配置 QEMU VNC 端口")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func (e *hypervEnv) qemuScreenshot(ctx context.Context, width int) ([]byte, error) {
	distro := defaultDistro(e.cfg)
	rt := runtimeOrDefault(e.cfg, e.envID)
	dir := winToWSL(qemuDir(e.cfg.StoreRoot, e.envID))
	pngHost := dir + "/shot.png"
	script := fmt.Sprintf(`
RT=%s
DIR=%s
[ -S "$RT/qmp.sock" ] || exit 2
printf '%%s\n' '{"execute":"qmp_capabilities"}' '{"execute":"screendump","arguments":{"filename":"'"$DIR"'/shot.png"}}' | timeout 8 socat - UNIX-CONNECT:"$RT/qmp.sock" >/dev/null 2>&1 || \
python3 - "$RT/qmp.sock" "$DIR/shot.png" <<'PY'
import socket,sys
sock, out = sys.argv[1], sys.argv[2]
s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
s.connect(sock)
s.recv(4096)
s.send(b'{"execute":"qmp_capabilities"}\n')
s.recv(4096)
s.send(('{"execute":"screendump","arguments":{"filename":%%r}}\n' %% out).encode())
s.recv(4096)
PY
[ -f "$DIR/shot.png" ] && base64 -w0 "$DIR/shot.png"
`, shellQuote(rt), shellQuote(dir))
	out, err := wslBash(ctx, distro, script)
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		img := image.NewRGBA(image.Rect(0, 0, 320, 200))
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return buf.Bytes(), fmt.Errorf("hyperv/qemu: 截图失败: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	_ = pngHost
	_ = width
	return raw, nil
}
