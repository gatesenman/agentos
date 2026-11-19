package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

type dockerEnv struct {
	cl  *client
	cfg Config
	id  string // container id or name
}

type inspect struct {
	ID    string `json:"Id"`
	State struct {
		Status     string
		Running    bool
		Paused     bool
		Restarting bool
		ExitCode   int
		Error      string
		StartedAt  string
	}
	Config struct {
		Image  string
		Labels map[string]string
	}
	NetworkSettings struct {
		IPAddress string
		Gateway   string
		Ports     map[string][]struct{ HostIp, HostPort string }
		Networks  map[string]struct{ IPAddress, Gateway string }
	}
}

func (i *inspect) ip() string {
	if i.NetworkSettings.IPAddress != "" {
		return i.NetworkSettings.IPAddress
	}
	for _, n := range i.NetworkSettings.Networks {
		if n.IPAddress != "" {
			return n.IPAddress
		}
	}
	return ""
}

func (i *inspect) hostPort(key string) int {
	for _, b := range i.NetworkSettings.Ports[key] {
		if p, err := strconv.Atoi(b.HostPort); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

func (e *dockerEnv) path(suffix string) string { return "/containers/" + url.PathEscape(e.id) + suffix }

func (e *dockerEnv) inspect(ctx context.Context) (*inspect, error) {
	var ins inspect
	if err := e.cl.json(ctx, "GET", e.path("/json"), nil, nil, &ins); err != nil {
		return nil, err
	}
	return &ins, nil
}

func (e *dockerEnv) Status(ctx context.Context) (env.Status, error) {
	ins, err := e.inspect(ctx)
	if err != nil {
		if isStatus(err, 404) {
			return env.Status{State: env.StateError, Message: "容器不存在（可能已被手动删除）"}, nil
		}
		return env.Status{State: env.StateUnknown, Message: err.Error()}, nil
	}
	st := env.Status{Address: ins.ip(), Extra: map[string]string{
		"container_id": ins.ID, "image": ins.Config.Image, "docker_state": ins.State.Status}}
	if p := ins.hostPort("5900/tcp"); p > 0 {
		st.Extra["vnc_port"] = strconv.Itoa(p)
	}
	switch ins.State.Status {
	case "running":
		st.State = env.StateRunning
		if ins.State.Paused {
			st.Message = "已暂停"
		}
	case "paused":
		st.State, st.Message = env.StateStopped, "已暂停"
	case "created", "restarting":
		st.State = env.StateCreating
	case "exited":
		st.State, st.Message = env.StateStopped, fmt.Sprintf("退出码 %d", ins.State.ExitCode)
	case "dead", "removing":
		st.State, st.Message = env.StateError, ins.State.Error
	default:
		st.State = env.StateUnknown
	}
	if ins.State.Error != "" && st.Message == "" {
		st.Message = ins.State.Error
	}
	return st, nil
}

func (e *dockerEnv) Start(ctx context.Context) error {
	return e.cl.json(ctx, "POST", e.path("/start"), nil, nil, nil) // 304 = already running
}

func (e *dockerEnv) Stop(ctx context.Context) error {
	return e.cl.json(ctx, "POST", e.path("/stop"), url.Values{"t": {"10"}}, nil, nil)
}

// Delete removes the container (and its anonymous volumes) when the server
// deletes with purge (env.IsPurge); otherwise the container is only stopped
// and kept (labelled agentos.env=<id>) so its data can be recovered.
func (e *dockerEnv) Delete(ctx context.Context) error {
	if !env.IsPurge(ctx) {
		err := e.cl.json(ctx, "POST", e.path("/stop"), url.Values{"t": {"10"}}, nil, nil)
		if isStatus(err, 404) || isStatus(err, 304) {
			return nil
		}
		return err
	}
	err := e.cl.json(ctx, "DELETE", e.path(""), url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if isStatus(err, 404) {
		return nil
	}
	return err
}

func (e *dockerEnv) defaultCmd() []string {
	if e.cfg.Shell != "" {
		return strings.Fields(e.cfg.Shell)
	}
	return []string{"/bin/sh", "-l"}
}

func (e *dockerEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	return e.cl.exec(ctx, e.id, req, e.defaultCmd())
}

// run executes cmd non-interactively and returns stdout, stderr and exit code.
func (e *dockerEnv) run(ctx context.Context, cmd ...string) (string, string, int, error) {
	s, err := e.cl.exec(ctx, e.id, env.ExecRequest{Cmd: cmd}, nil)
	if err != nil {
		return "", "", -1, err
	}
	defer s.Close()
	s.Stdin().Close()
	var out, errb bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&errb, s.Stderr()); close(done) }()
	io.Copy(&out, s.Stdout())
	<-done
	code, err := s.Wait()
	return out.String(), errb.String(), code, err
}

func (e *dockerEnv) PutFile(ctx context.Context, p string, r io.Reader, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	p = path.Clean(p)
	if !path.IsAbs(p) {
		return fmt.Errorf("docker: path must be absolute: %q", p)
	}
	// Tar headers need the size up front: spool to a temp file.
	tmp, err := os.CreateTemp("", "agentos-put-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, r)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: path.Base(p), Mode: int64(mode & 0o7777), Size: n,
			ModTime: time.Now(), Typeflag: tar.TypeReg, Format: tar.FormatPAX})
		if err == nil {
			_, err = io.Copy(tw, tmp)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	dir := path.Dir(p)
	put := func(body io.Reader) error {
		return e.cl.json(ctx, "PUT", e.path("/archive"), url.Values{"path": {dir}}, body, nil)
	}
	// The target directory must exist; create it first (requires a running container).
	if _, _, code, err := e.run(ctx, "mkdir", "-p", dir); err != nil || code != 0 {
		_ = err // stopped container or no mkdir: let the archive call report a real error
	}
	err = put(pr)
	pr.CloseWithError(err)
	return err
}

type tarFile struct {
	io.Reader
	body io.Closer
}

func (t tarFile) Close() error { return t.body.Close() }

func (e *dockerEnv) GetFile(ctx context.Context, p string) (io.ReadCloser, error) {
	resp, err := e.cl.do(ctx, "GET", e.path("/archive"), url.Values{"path": {p}}, nil, "")
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(resp.Body)
	for {
		h, err := tr.Next()
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("docker: read archive: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeReg:
			return tarFile{tr, resp.Body}, nil
		case tar.TypeDir:
			resp.Body.Close()
			return nil, fmt.Errorf("docker: %s is a directory", p)
		case tar.TypeSymlink: // archive API does not follow the final symlink
			resp.Body.Close()
			target := h.Linkname
			if !path.IsAbs(target) {
				target = path.Join(path.Dir(p), target)
			}
			if target == p {
				return nil, fmt.Errorf("docker: symlink loop at %s", p)
			}
			return e.GetFile(ctx, target)
		}
	}
}

// listScript prints "size|rawmode(hex)|mtime|name" per entry; works with GNU coreutils and busybox.
const listScript = `cd -- "$1" || exit 3
for f in .[!.]* ..?* *; do
  [ -e "$f" ] || [ -L "$f" ] || continue
  stat -L -c '%s|%f|%Y|%n' -- "$f" 2>/dev/null || stat -c '%s|%f|%Y|%n' -- "$f"
done`

func (e *dockerEnv) ListDir(ctx context.Context, p string) ([]env.FileInfo, error) {
	if p == "" {
		p = "/"
	}
	out, serr, code, err := e.run(ctx, "/bin/sh", "-c", listScript, "sh", p)
	if err != nil {
		return nil, err
	}
	if code == 3 {
		if strings.Contains(serr, "No such file") || strings.Contains(serr, "can't cd") {
			return nil, fmt.Errorf("%w: %s", env.ErrNotFound, p)
		}
		return nil, fmt.Errorf("docker: list %s: %s", p, strings.TrimSpace(serr))
	}
	if code != 0 && out == "" {
		return nil, fmt.Errorf("docker: list %s: exit %d: %s", p, code, strings.TrimSpace(serr))
	}
	return parseStatList(out), nil
}

func parseStatList(out string) []env.FileInfo {
	res := []env.FileInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "|", 4)
		if len(f) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[0], 10, 64)
		raw, _ := strconv.ParseUint(f[1], 16, 32)
		mt, _ := strconv.ParseInt(f[2], 10, 64)
		res = append(res, env.FileInfo{Name: f[3], Size: size, Dir: raw&0o170000 == 0o040000,
			Mode: uint32(raw & 0o7777), ModTime: time.Unix(mt, 0).UTC()})
	}
	return res
}

// Desktop("vnc") connects to the container's VNC server on 5900: directly via the
// container IP when the daemon is local on Linux, otherwise via the published host port.
// When virtual_desktop is set (linux-desktop stack on a headless image), start Xvfb+
// x11vnc inside the container on demand and relay over docker exec.
func (e *dockerEnv) Desktop(ctx context.Context, kind string) (io.ReadWriteCloser, error) {
	if kind != "vnc" {
		return nil, env.ErrUnsupported
	}
	if !e.cfg.Desktop && !e.cfg.VirtualDesktop {
		return nil, env.ErrUnsupported
	}
	ins, err := e.inspect(ctx)
	if err != nil {
		return nil, err
	}
	if !ins.State.Running {
		return nil, errors.New("docker: 容器未运行")
	}
	if e.cfg.VirtualDesktop && !e.cfg.Desktop {
		if err := e.ensureVirtualDesktop(ctx); err != nil {
			return nil, err
		}
		return e.vncRelay(ctx)
	}
	if e.cl.remoteHost() != "" { // remote daemon: VNC is bound to its loopback only
		return e.vncRelay(ctx)
	}
	var addrs []string
	if ip := ins.ip(); ip != "" && runtime.GOOS == "linux" && e.cl.remoteHost() == "" {
		addrs = append(addrs, net.JoinHostPort(ip, "5900"))
	}
	if p := ins.hostPort("5900/tcp"); p > 0 {
		h := e.cl.remoteHost()
		if h == "" {
			h = "127.0.0.1"
		}
		addrs = append(addrs, net.JoinHostPort(h, strconv.Itoa(p)))
	}
	if len(addrs) == 0 {
		// Desktop image without published port (unusual): fall back to exec relay.
		return e.vncRelay(ctx)
	}
	var lastErr error
	for _, a := range addrs {
		d := net.Dialer{Timeout: 3 * time.Second}
		c, err := d.DialContext(ctx, "tcp", a)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("docker: 连接 VNC 失败: %w", lastErr)
}

// vdeskBootstrap starts Xvfb + WM + x11vnc on 127.0.0.1:5900 inside the container
// (same shape as the SSH virtual_desktop helper). Background processes are detached
// with setsid so they survive the exec session.
const vdeskBootstrap = `P=5900
command -v Xvfb >/dev/null 2>&1 && command -v x11vnc >/dev/null 2>&1 || { echo "缺少 Xvfb/x11vnc，请先安装「Linux 桌面」技术栈" >&2; exit 3; }
# already listening?
if (command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':5900 ') || (echo >/dev/tcp/127.0.0.1/$P) 2>/dev/null; then
  echo "VNC already on 127.0.0.1:$P"; exit 0
fi
L="$HOME/.agentos/vdesk"; mkdir -p "$L"
D=""; n=50; while [ $n -lt 100 ]; do if [ ! -e /tmp/.X11-unix/X$n ] && [ ! -e /tmp/.X$n-lock ]; then D=$n; break; fi; n=$((n+1)); done
[ -n "$D" ] || { echo "no free X display" >&2; exit 4; }
bg() { if command -v setsid >/dev/null 2>&1; then setsid "$@" </dev/null & else nohup "$@" </dev/null & fi; }
bg Xvfb :$D -screen 0 1280x800x24 -nolisten tcp >"$L/xvfb.log" 2>&1
i=0; while [ ! -e /tmp/.X11-unix/X$D ] && [ $i -lt 100 ]; do sleep 0.1 2>/dev/null || sleep 1; i=$((i+1)); done
export DISPLAY=:$D
for wm in startxfce4 xfce4-session fluxbox openbox twm; do
  if command -v $wm >/dev/null 2>&1; then bg $wm >"$L/wm.log" 2>&1; break; fi
done
if ! command -v startxfce4 >/dev/null 2>&1 && ! command -v xfce4-session >/dev/null 2>&1; then
  command -v xterm >/dev/null 2>&1 && bg xterm -geometry 100x28+60+60 -title "AgentOS" >"$L/xterm.log" 2>&1
fi
bg x11vnc -display :$D -rfbport "$P" -localhost -forever -shared -nopw -noxdamage -quiet >"$L/x11vnc.log" 2>&1
echo $D > "$L/display"
# wait briefly for listen
j=0; while [ $j -lt 50 ]; do
  if (echo >/dev/tcp/127.0.0.1/$P) 2>/dev/null; then echo "DISPLAY=:$D VNC=127.0.0.1:$P"; exit 0; fi
  sleep 0.1 2>/dev/null || sleep 1; j=$((j+1))
done
echo "x11vnc did not listen on :$P" >&2; tail -n 20 "$L/x11vnc.log" 2>/dev/null >&2; exit 5
`

func (e *dockerEnv) ensureVirtualDesktop(ctx context.Context) error {
	sess, err := e.Exec(ctx, env.ExecRequest{Cmd: []string{"bash", "-lc", vdeskBootstrap}})
	if err != nil {
		return fmt.Errorf("docker: 启动虚拟桌面失败: %w", err)
	}
	defer sess.Close()
	_ = sess.Stdin().Close()
	var outB, errB strings.Builder
	done := make(chan struct{})
	go func() { io.Copy(&errB, io.LimitReader(sess.Stderr(), 64<<10)); close(done) }()
	io.Copy(&outB, io.LimitReader(sess.Stdout(), 64<<10))
	<-done
	code, werr := sess.Wait()
	msg := strings.TrimSpace(outB.String() + "\n" + errB.String())
	if werr != nil {
		return fmt.Errorf("docker: 启动虚拟桌面失败: %v: %s", werr, msg)
	}
	if code != 0 {
		return fmt.Errorf("docker: 启动虚拟桌面失败（退出码 %d）: %s", code, msg)
	}
	env.Logf(ctx, "info", "容器虚拟桌面已就绪 / container virtual desktop: %s", msg)
	return nil
}

type statsResp struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

func (s *statsResp) metrics() env.Metrics {
	var m env.Metrics
	cpuD := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysD := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	n := float64(s.CPUStats.OnlineCPUs)
	if n == 0 {
		n = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if n == 0 {
		n = 1
	}
	if cpuD > 0 && sysD > 0 {
		m.CPUPercent = cpuD / sysD * n * 100
	}
	used := s.MemoryStats.Usage
	for _, k := range []string{"inactive_file", "total_inactive_file", "cache"} { // v2, v1, old v1
		if v, ok := s.MemoryStats.Stats[k]; ok && v < used {
			used -= v
			break
		}
	}
	m.MemUsed, m.MemTotal = used, s.MemoryStats.Limit
	return m
}

func (e *dockerEnv) Metrics(ctx context.Context) (env.Metrics, error) {
	var s statsResp
	if err := e.cl.json(ctx, "GET", e.path("/stats"), url.Values{"stream": {"false"}}, nil, &s); err != nil {
		return env.Metrics{}, err
	}
	m := s.metrics()
	// Disk usage of the container root fs (best effort).
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, _, code, err := e.run(dctx, "df", "-kP", "/"); err == nil && code == 0 {
		m.DiskUsed, m.DiskTotal = parseDF(out)
	}
	return m, nil
}

// parseDF parses `df -kP` output (second line: fs blocks used avail ...).
func parseDF(out string) (used, total uint64) {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Scan()
	if sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 4 {
			t, _ := strconv.ParseUint(f[1], 10, 64)
			u, _ := strconv.ParseUint(f[2], 10, 64)
			return u * 1024, t * 1024
		}
	}
	return 0, 0
}

func (e *dockerEnv) Close() error { e.cl.close(); return nil }
