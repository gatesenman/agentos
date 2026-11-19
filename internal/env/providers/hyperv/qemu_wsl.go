package hyperv

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultWSLDistro is the Ubuntu-based distro used for the QEMU/KVM fallback.
const DefaultWSLDistro = "AgentOS-E2E"

// detectQEMUWSL probes WSL2 + qemu-system-x86_64 in DefaultWSLDistro.
// On non-Windows hosts it always returns unavailable.
func detectQEMUWSL(ctx context.Context) DetectResult {
	if runtime.GOOS != "windows" {
		return DetectResult{Available: false, Reason: "QEMU/WSL 回退仅在 Windows 宿主可用"}
	}
	return detectQEMUWSLPlatform(ctx)
}

func defaultDistro(c Config) string {
	if c.WSLDistro != "" {
		return c.WSLDistro
	}
	return DefaultWSLDistro
}

// allocHostPort finds a free TCP port on 127.0.0.1 in [lo, hi].
func allocHostPort(lo, hi int) (int, error) {
	for p := lo; p <= hi; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			continue
		}
		addr := ln.Addr().(*net.TCPAddr)
		ln.Close()
		return addr.Port, nil
	}
	return 0, fmt.Errorf("hyperv/qemu: 无可用本机端口 %d–%d", lo, hi)
}

// winToWSL converts a Windows path (D:\foo) to a WSL mount path (/mnt/d/foo).
func winToWSL(p string) string {
	p = filepath.Clean(p)
	if len(p) >= 2 && p[1] == ':' {
		drive := strings.ToLower(string(p[0]))
		rest := strings.ReplaceAll(p[2:], `\`, `/`)
		rest = strings.TrimPrefix(rest, `/`)
		if rest == "" {
			return "/mnt/" + drive
		}
		return "/mnt/" + drive + "/" + rest
	}
	return strings.ReplaceAll(p, `\`, `/`)
}

func qemuDir(store, envID string) string {
	return filepath.Join(vmDir(store, envID), "qemu")
}

// qemuRuntimeDir returns a WSL-native path for AF_UNIX sockets. DrvFS (/mnt/*)
// rejects bind() with ENOTSUP; disks/ISO/seed stay on the Windows store via qemuDir.
func qemuRuntimeDir(envID string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, envID)
	if len(safe) > 48 {
		safe = safe[:48]
	}
	return QEMURuntimeRoot + "/" + safe
}

func waitTCP(ctx context.Context, host string, port int, every time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 3 * time.Second}
	for {
		cn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			cn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// ensureDir creates dir with 0755.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
