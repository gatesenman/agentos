package ssh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/env"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sshEnv struct {
	cfg Config

	mu      sync.Mutex
	cl      *ssh.Client
	sf      *sftp.Client
	osName  string // linux | darwin | windows (cached)
	arch    string
	prevCPU *cpuSample
	closed  bool
}

// client returns a live connection, (re)dialing if needed.
func (e *sshEnv) client(ctx context.Context) (*ssh.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("ssh: env closed")
	}
	if e.cl != nil {
		if _, _, err := e.cl.SendRequest("keepalive@openssh.com", true, nil); err == nil {
			return e.cl, nil
		}
		e.resetLocked()
	}
	cl, fp, err := e.cfg.dial(ctx)
	if err != nil {
		return nil, err
	}
	if e.cfg.HostKey == "" && e.cfg.HostKeyPolicy == PolicyTOFU {
		e.cfg.HostKey = fp // pin for the life of this handle
	}
	e.cl = cl
	return cl, nil
}

func (e *sshEnv) resetLocked() {
	if e.sf != nil {
		e.sf.Close()
		e.sf = nil
	}
	if e.cl != nil {
		e.cl.Close()
		e.cl = nil
	}
}

func (e *sshEnv) sftp(ctx context.Context) (*sftp.Client, error) {
	cl, err := e.client(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sf != nil {
		return e.sf, nil
	}
	sf, err := sftp.NewClient(cl)
	if err != nil {
		return nil, fmt.Errorf("ssh: SFTP 不可用: %w", err)
	}
	e.sf = sf
	return sf, nil
}

// remoteOS returns the target OS (configured, or detected once).
func (e *sshEnv) remoteOS(ctx context.Context) (string, error) {
	if e.cfg.OS != "auto" && e.cfg.OS != "" {
		return e.cfg.OS, nil
	}
	e.mu.Lock()
	o := e.osName
	e.mu.Unlock()
	if o != "" {
		return o, nil
	}
	cl, err := e.client(ctx)
	if err != nil {
		return "", err
	}
	o, a, err := detectOS(ctx, cl)
	if err != nil {
		if e.cfg.OSDetected != "" {
			return e.cfg.OSDetected, nil
		}
		return "", err
	}
	e.mu.Lock()
	e.osName, e.arch = o, a
	e.mu.Unlock()
	return o, nil
}

// output runs a command line and returns combined stdout, stderr and exit code.
func output(ctx context.Context, cl *ssh.Client, cmdline string) (string, string, int, error) {
	s, err := cl.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer s.Close()
	var o, eb strings.Builder
	s.Stdout, s.Stderr = &o, &eb
	stop := context.AfterFunc(ctx, func() { s.Signal(ssh.SIGKILL); s.Close() })
	defer stop()
	err = s.Run(cmdline)
	if err != nil {
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			return o.String(), eb.String(), ee.ExitStatus(), nil
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return o.String(), eb.String(), -1, err
	}
	return o.String(), eb.String(), 0, nil
}

// detectOS probes `uname -sm`, falling back to Windows `ver` / %PROCESSOR_ARCHITECTURE%.
func detectOS(ctx context.Context, cl *ssh.Client) (osName, arch string, err error) {
	if out, _, code, err := output(ctx, cl, "uname -sm"); err == nil && code == 0 {
		if o, a := ParseUname(out); o != "" {
			return o, a, nil
		}
	}
	out, _, _, err := output(ctx, cl, "cmd /c ver & echo %PROCESSOR_ARCHITECTURE%")
	if err == nil && strings.Contains(strings.ToLower(out), "windows") {
		a := "amd64"
		if strings.Contains(strings.ToUpper(out), "ARM64") {
			a = "arm64"
		}
		return "windows", a, nil
	}
	if err == nil {
		err = fmt.Errorf("ssh: 无法识别远程系统: %q", strings.TrimSpace(out))
	}
	return "", "", err
}

// ParseUname maps `uname -sm` output to GOOS/GOARCH ("" if unknown).
func ParseUname(out string) (goos, goarch string) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return "", ""
	}
	switch strings.ToLower(f[0]) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	case "freebsd":
		goos = "freebsd"
	default:
		if strings.HasPrefix(strings.ToLower(f[0]), "mingw") || strings.HasPrefix(strings.ToLower(f[0]), "msys") || strings.HasPrefix(strings.ToLower(f[0]), "cygwin") {
			goos = "windows"
		} else {
			return "", ""
		}
	}
	switch strings.ToLower(f[1]) {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	case "armv7l", "armv6l":
		goarch = "arm"
	case "i386", "i686":
		goarch = "386"
	case "riscv64":
		goarch = "riscv64"
	default:
		goarch = strings.ToLower(f[1])
	}
	return goos, goarch
}

func (e *sshEnv) Status(ctx context.Context) (env.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := env.Status{Address: e.cfg.Host, Extra: map[string]string{}}
	if _, err := e.client(ctx); err != nil {
		st.Message = err.Error()
		st.State = env.StateError
		if e.cfg.StartCommand != "" {
			st.State = env.StateStopped
		}
		return st, nil
	}
	st.State = env.StateRunning
	e.mu.Lock()
	st.Extra["host_key"] = e.cfg.HostKey
	if e.osName != "" {
		st.Extra["os"], st.Extra["arch"] = e.osName, e.arch
	}
	e.mu.Unlock()
	return st, nil
}

// Start runs start_command on the management host (if configured) and waits for SSH.
func (e *sshEnv) Start(ctx context.Context) error {
	if e.cfg.StartCommand == "" {
		return env.ErrUnsupported
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", e.cfg.StartCommand)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", e.cfg.StartCommand)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh: 开机命令失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		if _, err := e.client(wctx); err == nil {
			return nil
		}
		select {
		case <-wctx.Done():
			return fmt.Errorf("ssh: 等待主机上线超时: %w", wctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

// Stop runs stop_command on the remote host (if configured). A dropped connection counts as success.
func (e *sshEnv) Stop(ctx context.Context) error {
	if e.cfg.StopCommand == "" {
		return env.ErrUnsupported
	}
	cl, err := e.client(ctx)
	if err != nil {
		return err
	}
	_, serr, code, err := output(ctx, cl, e.cfg.StopCommand)
	e.mu.Lock()
	e.resetLocked()
	e.mu.Unlock()
	if err != nil {
		var em *ssh.ExitMissingError
		if errors.As(err, &em) || errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if code != 0 {
		return fmt.Errorf("ssh: 关机命令退出码 %d: %s", code, strings.TrimSpace(serr))
	}
	return nil
}

// Delete has nothing to deprovision (the machine is not ours); it just disconnects.
func (e *sshEnv) Delete(ctx context.Context) error {
	e.mu.Lock()
	e.resetLocked()
	e.mu.Unlock()
	return nil
}

func notFound(err error, p string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", env.ErrNotFound, p)
	}
	return err
}

func (e *sshEnv) PutFile(ctx context.Context, p string, r io.Reader, mode uint32) error {
	sf, err := e.sftp(ctx)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	p = toSFTPPath(p)
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := sf.MkdirAll(dir); err != nil {
			return fmt.Errorf("ssh: 创建目录 %s 失败: %w", dir, err)
		}
	}
	tmp := p + ".agentos-tmp"
	f, err := sf.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("ssh: 写入 %s 失败: %w", p, err)
	}
	stop := context.AfterFunc(ctx, func() { f.Close() })
	_, err = f.ReadFrom(r)
	stop()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		sf.Remove(tmp)
		return fmt.Errorf("ssh: 写入 %s 失败: %w", p, err)
	}
	_ = sf.Chmod(tmp, os.FileMode(mode&0o7777)) // not meaningful on Windows
	if err := sf.PosixRename(tmp, p); err != nil {
		sf.Remove(p)
		if err := sf.Rename(tmp, p); err != nil {
			sf.Remove(tmp)
			return fmt.Errorf("ssh: 写入 %s 失败: %w", p, err)
		}
	}
	return nil
}

func (e *sshEnv) GetFile(ctx context.Context, p string) (io.ReadCloser, error) {
	sf, err := e.sftp(ctx)
	if err != nil {
		return nil, err
	}
	p = toSFTPPath(p)
	f, err := sf.Open(p)
	if err != nil {
		return nil, notFound(err, p)
	}
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		f.Close()
		return nil, fmt.Errorf("ssh: %s is a directory", p)
	}
	return f, nil
}

func (e *sshEnv) ListDir(ctx context.Context, p string) ([]env.FileInfo, error) {
	sf, err := e.sftp(ctx)
	if err != nil {
		return nil, err
	}
	if p == "" {
		if p, err = sf.Getwd(); err != nil {
			p = "/"
		}
	}
	p = toSFTPPath(p)
	list, err := sf.ReadDir(p)
	if err != nil {
		return nil, notFound(err, p)
	}
	out := make([]env.FileInfo, 0, len(list))
	for _, fi := range list {
		if fi.Mode()&fs.ModeSymlink != 0 {
			if t, err := sf.Stat(path.Join(p, fi.Name())); err == nil {
				fi = namedInfo{t, fi.Name()}
			}
		}
		out = append(out, env.FileInfo{Name: fi.Name(), Size: fi.Size(), Dir: fi.IsDir(),
			Mode: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UTC()})
	}
	return out, nil
}

type namedInfo struct {
	fs.FileInfo
	name string
}

func (n namedInfo) Name() string { return n.name }

// toSFTPPath normalizes Windows paths (C:\x -> /C:/x) for OpenSSH-for-Windows sftp-server.
func toSFTPPath(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		return "/" + strings.ReplaceAll(p, `\`, "/")
	}
	return p
}

// Desktop("vnc") tunnels to 127.0.0.1:<vnc_port> on the remote host via direct-tcpip.
func (e *sshEnv) Desktop(ctx context.Context, kind string) (io.ReadWriteCloser, error) {
	if kind != "vnc" {
		return nil, env.ErrUnsupported
	}
	cl, err := e.client(ctx)
	if err != nil {
		return nil, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(e.cfg.VNCPort)
	c, err := cl.DialContext(ctx, "tcp", addr)
	if err != nil && e.cfg.VirtualDesktop && e.cfg.OSDetected != "windows" {
		if verr := e.startVirtualDesktop(ctx, cl); verr != nil {
			return nil, verr
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			if c, err = cl.DialContext(ctx, "tcp", addr); err == nil || time.Now().After(deadline) || ctx.Err() != nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: 远程 VNC (127.0.0.1:%d) 不可用: %w", e.cfg.VNCPort, err)
	}
	return c, nil
}

// vdeskScript starts Xvfb (first free display >= 50) + a window manager +
// xterm + x11vnc -localhost -rfbport $1 for the ssh user, detached. The
// display number is written to ~/.agentos/vdesk/display.
const vdeskScript = `P=$1
command -v Xvfb >/dev/null 2>&1 && command -v x11vnc >/dev/null 2>&1 || { echo "remote host lacks Xvfb/x11vnc (apt install xvfb x11vnc fluxbox xterm)" >&2; exit 3; }
L="$HOME/.agentos/vdesk"; mkdir -p "$L"
D=""; n=50; while [ $n -lt 100 ]; do if [ ! -e /tmp/.X11-unix/X$n ] && [ ! -e /tmp/.X$n-lock ]; then D=$n; break; fi; n=$((n+1)); done
[ -n "$D" ] || { echo "no free X display" >&2; exit 4; }
bg() { if command -v setsid >/dev/null 2>&1; then setsid "$@" </dev/null & else nohup "$@" </dev/null & fi; }
bg Xvfb :$D -screen 0 1280x800x24 -nolisten tcp >"$L/xvfb.log" 2>&1
i=0; while [ ! -e /tmp/.X11-unix/X$D ] && [ $i -lt 100 ]; do sleep 0.1 2>/dev/null || sleep 1; i=$((i+1)); done
export DISPLAY=:$D
for wm in fluxbox openbox twm; do if command -v $wm >/dev/null 2>&1; then bg $wm >"$L/wm.log" 2>&1; break; fi; done
command -v xterm >/dev/null 2>&1 && bg xterm -geometry 100x28+60+60 -title "AgentOS $(hostname)" >"$L/xterm.log" 2>&1
bg x11vnc -display :$D -rfbport "$P" -localhost -forever -shared -nopw -noxdamage -quiet >"$L/x11vnc.log" 2>&1
echo $D > "$L/display"
echo "DISPLAY=:$D VNC=127.0.0.1:$P"
`

func (e *sshEnv) startVirtualDesktop(ctx context.Context, cl *ssh.Client) error {
	sess, err := cl.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	cmd := "sh -c " + shellQuote(vdeskScript) + " vdesk " + strconv.Itoa(e.cfg.VNCPort)
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("ssh: 启动远程虚拟桌面失败 / virtual desktop: %v: %s", err, strings.TrimSpace(string(out)))
	}
	env.Logf(ctx, "info", "远程虚拟桌面已启动 / remote virtual desktop: %s", strings.TrimSpace(string(out)))
	return nil
}

func (e *sshEnv) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.resetLocked()
	return nil
}

// --- metrics ---

type cpuSample struct{ idle, total uint64 }

func parseProcStat(line string) (cpuSample, bool) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuSample{}, false
	}
	var s cpuSample
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		if i == 7 { // stop before guest/guest_nice (already included in user)
			break
		}
		s.total += n
		if i == 3 || i == 4 { // idle + iowait
			s.idle += n
		}
	}
	return s, true
}

func cpuPercent(a, b cpuSample) float64 {
	if b.total <= a.total {
		return 0
	}
	return float64((b.total-a.total)-(b.idle-a.idle)) / float64(b.total-a.total) * 100
}

// ParseLinuxMetrics parses the output of linuxMetricsCmd. It returns the cpu samples found.
func parseLinuxMetrics(out string) (m env.Metrics, samples []cpuSample) {
	sec := strings.Split(out, "@@@\n")
	for _, l := range strings.Split(sec[0], "\n") {
		if s, ok := parseProcStat(l); ok {
			samples = append(samples, s)
		}
	}
	if len(sec) > 1 {
		var total, avail uint64
		sc := bufio.NewScanner(strings.NewReader(sec[1]))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = n * 1024
			case "MemAvailable:":
				avail = n * 1024
			}
		}
		m.MemTotal = total
		if avail <= total {
			m.MemUsed = total - avail
		}
	}
	if len(sec) > 2 {
		m.DiskUsed, m.DiskTotal = parseDF(sec[2])
	}
	return m, samples
}

func parseDF(out string) (used, total uint64) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) >= 4 {
		t, _ := strconv.ParseUint(f[1], 10, 64)
		u, _ := strconv.ParseUint(f[2], 10, 64)
		return u * 1024, t * 1024
	}
	return 0, 0
}

// parseWindowsMetrics parses "cpu totalKB freeKB diskSize diskFree".
func parseWindowsMetrics(out string) (env.Metrics, error) {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 5 {
		return env.Metrics{}, fmt.Errorf("ssh: 无法解析 Windows 性能数据: %q", out)
	}
	cpu, _ := strconv.ParseFloat(f[0], 64)
	tot, _ := strconv.ParseUint(f[1], 10, 64)
	free, _ := strconv.ParseUint(f[2], 10, 64)
	ds, _ := strconv.ParseUint(f[3], 10, 64)
	df, _ := strconv.ParseUint(f[4], 10, 64)
	m := env.Metrics{CPUPercent: cpu, MemTotal: tot * 1024, DiskTotal: ds}
	if free <= tot {
		m.MemUsed = (tot - free) * 1024
	}
	if df <= ds {
		m.DiskUsed = ds - df
	}
	return m, nil
}

const winMetricsPS = `$c=(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average; ` +
	`$o=Get-CimInstance Win32_OperatingSystem; $d=Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$($env:SystemDrive)'"; ` +
	`Write-Output ("{0} {1} {2} {3} {4}" -f [int]$c,$o.TotalVisibleMemorySize,$o.FreePhysicalMemory,$d.Size,$d.FreeSpace)`

// parseDarwinMetrics parses "cpu-idle-percent@@@memsize@@@vm_stat@@@df".
func parseDarwinMetrics(out string) env.Metrics {
	var m env.Metrics
	sec := strings.Split(out, "@@@\n")
	if len(sec) < 4 {
		return m
	}
	// top: "CPU usage: 3.33% user, 5.0% sys, 91.66% idle"
	if i := strings.LastIndex(sec[0], "% idle"); i > 0 {
		f := strings.Fields(sec[0][:i])
		if idle, err := strconv.ParseFloat(f[len(f)-1], 64); err == nil {
			m.CPUPercent = 100 - idle
		}
	}
	m.MemTotal, _ = strconv.ParseUint(strings.TrimSpace(sec[1]), 10, 64)
	pageSize := uint64(4096)
	var free uint64
	for _, l := range strings.Split(sec[2], "\n") {
		if strings.Contains(l, "page size of") {
			for _, w := range strings.Fields(l) {
				if n, err := strconv.ParseUint(w, 10, 64); err == nil {
					pageSize = n
				}
			}
		}
		for _, k := range []string{"Pages free:", "Pages inactive:", "Pages speculative:"} {
			if strings.HasPrefix(l, k) {
				n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(l, k)), "."), 10, 64)
				free += n
			}
		}
	}
	if free*pageSize <= m.MemTotal {
		m.MemUsed = m.MemTotal - free*pageSize
	}
	m.DiskUsed, m.DiskTotal = parseDF(sec[3])
	return m
}

func (e *sshEnv) Metrics(ctx context.Context) (env.Metrics, error) {
	o, err := e.remoteOS(ctx)
	if err != nil {
		return env.Metrics{}, err
	}
	cl, err := e.client(ctx)
	if err != nil {
		return env.Metrics{}, err
	}
	switch o {
	case "windows":
		out, serr, code, err := output(ctx, cl, "powershell -NoProfile -NonInteractive -EncodedCommand "+encodePS(winMetricsPS))
		if err != nil || code != 0 {
			return env.Metrics{}, fmt.Errorf("ssh: windows metrics: %v %s", err, serr)
		}
		return parseWindowsMetrics(out)
	case "darwin":
		out, _, _, err := output(ctx, cl, `top -l 2 -n 0 -s 1 | grep "CPU usage" | tail -1; echo @@@; sysctl -n hw.memsize; echo @@@; vm_stat; echo @@@; df -kP /`)
		if err != nil {
			return env.Metrics{}, err
		}
		return parseDarwinMetrics(out), nil
	default:
		e.mu.Lock()
		prev := e.prevCPU
		e.mu.Unlock()
		cmd := `head -1 /proc/stat; echo @@@; cat /proc/meminfo; echo @@@; df -kP /`
		if prev == nil { // first call: take two samples one second apart
			cmd = `head -1 /proc/stat; sleep 1; head -1 /proc/stat; echo @@@; cat /proc/meminfo; echo @@@; df -kP /`
		}
		out, serr, code, err := output(ctx, cl, cmd)
		if err != nil {
			return env.Metrics{}, err
		}
		m, samples := parseLinuxMetrics(out)
		if len(samples) == 0 {
			return env.Metrics{}, fmt.Errorf("ssh: 无法读取 /proc/stat (exit %d): %s", code, strings.TrimSpace(serr))
		}
		if prev != nil {
			samples = append([]cpuSample{*prev}, samples...)
		}
		if len(samples) >= 2 {
			m.CPUPercent = cpuPercent(samples[len(samples)-2], samples[len(samples)-1])
		}
		last := samples[len(samples)-1]
		e.mu.Lock()
		e.prevCPU = &last
		e.mu.Unlock()
		return m, nil
	}
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
