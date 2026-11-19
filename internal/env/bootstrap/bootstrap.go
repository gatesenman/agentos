// Package bootstrap installs an Agent OS node (`agentos node`) into any env.Env
// that supports Exec + PutFile (docker containers, ssh hosts, ...), so the env
// gets native agent supervision by connecting back to the management server.
package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/agentos/agentos/internal/env"
)

// Options for InstallAgentOS. ServerURL and JoinToken are required, plus at least
// one binary source: DistDir, DownloadBase, or (fallback) the running executable
// when the target OS/arch equals the server's own.
type Options struct {
	ServerURL string // management server URL as reachable FROM the env, e.g. http://172.17.0.1:7800
	JoinToken string // node token; passed via AGENTOS_TOKEN env (not argv)
	NodeName  string // display name (--name); default: env hostname

	DistDir      string // local dir with agentos-<os>-<arch>[.exe] (scripts/build.sh layout)
	DownloadBase string // fetched by the server: <DownloadBase>/agentos-<os>-<arch>[.exe]
	OSHint       string // "", "linux", "darwin", "windows": skips/assists detection

	NodeID     string                           // optional: pre-seeds <data>/node-id so the caller knows Record.NodeID upfront
	InstallDir string                           // optional remote dir; default $HOME/.agentos or %LOCALAPPDATA%\AgentOS
	Wait       time.Duration                    // how long the process must stay alive to count as "up" (default 2s)
	Logf       func(format string, args ...any) // optional progress log (e.g. env.log events)
	HTTPClient *http.Client                     // for DownloadBase (default http.DefaultClient)
}

// Result describes what was installed and how it was started.
type Result struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	InstallDir string `json:"install_dir"`
	BinaryPath string `json:"binary_path"`
	LogPath    string `json:"log_path"`
	PID        int    `json:"pid"`
	NodeID     string `json:"node_id,omitempty"` // read back from <data>/node-id (or Options.NodeID)
	Method     string `json:"method"`            // setsid+nohup | nohup | systemd-run | win32-process
}

// Installer is the seam the API layer depends on; DefaultInstaller is the real one.
type Installer interface {
	Install(ctx context.Context, e env.Env, opts Options) (*Result, error)
}

// DefaultInstaller implements Installer with InstallAgentOS.
type DefaultInstaller struct{}

func (DefaultInstaller) Install(ctx context.Context, e env.Env, opts Options) (*Result, error) {
	return InstallAgentOS(ctx, e, opts)
}

var errExec = errors.New("bootstrap: exec failed")

// run executes cmd in e (no tty) and returns stdout, stderr and the exit code.
func run(ctx context.Context, e env.Env, cmd ...string) (string, string, int, error) {
	s, err := e.Exec(ctx, env.ExecRequest{Cmd: cmd})
	if err != nil {
		return "", "", -1, err
	}
	defer s.Close()
	s.Stdin().Close()
	var o, eb bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&eb, s.Stderr()); close(done) }()
	io.Copy(&o, s.Stdout())
	<-done
	code, err := s.Wait()
	return o.String(), eb.String(), code, err
}

func runOK(ctx context.Context, e env.Env, cmd ...string) (string, error) {
	o, se, code, err := run(ctx, e, cmd...)
	if err != nil {
		return o, err
	}
	if code != 0 {
		return o, fmt.Errorf("%w: %s: exit %d: %s", errExec, cmd[0], code, strings.TrimSpace(se+" "+o))
	}
	return o, nil
}

// Detect returns the env's GOOS/GOARCH by running `uname -sm` (or Windows `ver`).
func Detect(ctx context.Context, e env.Env, hint string) (goos, goarch string, err error) {
	if hint != "windows" {
		if out, _, code, err := run(ctx, e, "uname", "-sm"); err == nil && code == 0 {
			if o, a := parseUname(out); o != "" {
				return o, a, nil
			}
		}
	}
	out, _, _, err := run(ctx, e, "cmd", "/c", "ver & echo %PROCESSOR_ARCHITECTURE%")
	if err == nil && strings.Contains(strings.ToLower(out), "windows") {
		if strings.Contains(strings.ToUpper(out), "ARM64") {
			return "windows", "arm64", nil
		}
		return "windows", "amd64", nil
	}
	if err == nil {
		err = fmt.Errorf("unrecognized output %q", strings.TrimSpace(out))
	}
	return "", "", fmt.Errorf("bootstrap: 无法识别目标系统: %w", err)
}

func parseUname(out string) (string, string) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return "", ""
	}
	var o, a string
	switch strings.ToLower(f[0]) {
	case "linux":
		o = "linux"
	case "darwin":
		o = "darwin"
	default:
		return "", ""
	}
	switch strings.ToLower(f[1]) {
	case "x86_64", "amd64":
		a = "amd64"
	case "aarch64", "arm64":
		a = "arm64"
	case "armv7l", "armv6l":
		a = "arm"
	case "i386", "i686":
		a = "386"
	default:
		a = strings.ToLower(f[1])
	}
	return o, a
}

// BinaryName is the dist file name for a target (matches scripts/build.sh).
func BinaryName(goos, goarch string) string {
	n := "agentos-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// openBinary finds the agentos binary for the target. Caller closes.
func openBinary(ctx context.Context, opts Options, goos, goarch string) (io.ReadCloser, string, error) {
	name := BinaryName(goos, goarch)
	if opts.DistDir != "" {
		for _, p := range []string{filepath.Join(opts.DistDir, name), filepath.Join(opts.DistDir, goos+"-"+goarch, exeName(goos))} {
			if f, err := os.Open(p); err == nil {
				return f, p, nil
			}
		}
	}
	if opts.DownloadBase != "" {
		u := strings.TrimRight(opts.DownloadBase, "/") + "/" + name
		hc := opts.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("bootstrap: 下载 %s 失败: %w", u, err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, "", fmt.Errorf("bootstrap: 下载 %s 失败: HTTP %d", u, resp.StatusCode)
		}
		return resp.Body, u, nil
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		if p, err := os.Executable(); err == nil {
			if f, err := os.Open(p); err == nil {
				return f, p, nil
			}
		}
	}
	return nil, "", fmt.Errorf("bootstrap: 找不到 %s/%s 的 agentos 程序（请设置 DistDir 或 DownloadBase）", goos, goarch)
}

func exeName(goos string) string {
	if goos == "windows" {
		return "agentos.exe"
	}
	return "agentos"
}

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]*$`)

// InstallAgentOS detects the env's OS/arch, uploads the matching agentos binary,
// starts `agentos node --server URL --name NAME` detached (token via AGENTOS_TOKEN),
// and returns once the process has stayed up for opts.Wait. It is idempotent:
// a previously installed node in the same InstallDir is stopped and replaced.
func InstallAgentOS(ctx context.Context, e env.Env, opts Options) (*Result, error) {
	if opts.ServerURL == "" || opts.JoinToken == "" {
		return nil, errors.New("bootstrap: ServerURL 和 JoinToken 必填")
	}
	if !tokenRe.MatchString(opts.JoinToken) || strings.ContainsAny(opts.ServerURL+opts.NodeName+opts.NodeID, "\"'\r\n%`$") {
		return nil, errors.New("bootstrap: 参数包含不支持的字符")
	}
	if opts.Wait == 0 {
		opts.Wait = 2 * time.Second
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	goos, goarch := "", ""
	var err error
	if opts.OSHint == "windows" || opts.OSHint == "linux" || opts.OSHint == "darwin" || opts.OSHint == "" {
		goos, goarch, err = Detect(ctx, e, opts.OSHint)
	} else {
		err = fmt.Errorf("bootstrap: 未知 OSHint %q", opts.OSHint)
	}
	if err != nil {
		return nil, err
	}
	logf("检测到目标系统 %s/%s", goos, goarch)
	bin, src, err := openBinary(ctx, opts, goos, goarch)
	if err != nil {
		return nil, err
	}
	defer bin.Close()
	if goos == "windows" {
		return installWindows(ctx, e, opts, goarch, bin, src, logf)
	}
	return installPosix(ctx, e, opts, goos, goarch, bin, src, logf)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func installPosix(ctx context.Context, e env.Env, opts Options, goos, goarch string, bin io.Reader, src string, logf func(string, ...any)) (*Result, error) {
	dir := opts.InstallDir
	if dir == "" {
		home, _ := runOK(ctx, e, "/bin/sh", "-c", `printf %s "$HOME"`)
		home = strings.TrimRight(strings.TrimSpace(home), "/")
		if home == "" {
			home = "/tmp"
		}
		dir = home + "/.agentos"
	}
	r := &Result{OS: goos, Arch: goarch, InstallDir: dir, BinaryPath: dir + "/agentos", LogPath: dir + "/node.log"}
	data, pidf := dir+"/data", dir+"/node.pid"
	// stop a previous instance installed here
	runOK(ctx, e, "/bin/sh", "-c", `p=$(cat `+shq(pidf)+` 2>/dev/null) && [ -n "$p" ] && kill "$p" 2>/dev/null && sleep 1; `+
		`command -v systemctl >/dev/null 2>&1 && systemctl stop agentos-node >/dev/null 2>&1; true`)
	logf("上传 agentos (%s) 到 %s", src, r.BinaryPath)
	if _, err := runOK(ctx, e, "mkdir", "-p", data); err != nil {
		return nil, err
	}
	if err := e.PutFile(ctx, r.BinaryPath, bin, 0o755); err != nil {
		return nil, fmt.Errorf("bootstrap: 上传失败: %w", err)
	}
	if opts.NodeID != "" {
		if err := e.PutFile(ctx, data+"/node-id", strings.NewReader(opts.NodeID), 0o644); err != nil {
			return nil, err
		}
	}
	script := "#!/bin/sh\n# generated by AgentOS bootstrap\nexport AGENTOS_TOKEN=" + shq(opts.JoinToken) + "\nexec " + shq(r.BinaryPath) +
		" node --server " + shq(opts.ServerURL) + " --data " + shq(data)
	if opts.NodeName != "" {
		script += " --name " + shq(opts.NodeName)
	}
	script += "\n"
	runner := dir + "/run-node.sh"
	if err := e.PutFile(ctx, runner, strings.NewReader(script), 0o700); err != nil {
		return nil, err
	}
	// Prefer a transient systemd unit when we are root on a systemd host (restarts on crash);
	// otherwise setsid+nohup so the node outlives the exec session.
	launch := `cd ` + shq(dir) + ` || exit 1
if [ "$(id -u)" = 0 ] && command -v systemd-run >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
  systemctl reset-failed agentos-node >/dev/null 2>&1
  systemd-run --unit agentos-node --collect -p Restart=always -p StandardOutput=append:` + r.LogPath + ` -p StandardError=append:` + r.LogPath + ` /bin/sh ` + shq(runner) + ` >/dev/null 2>&1 || exit 1
  sleep 0.5; systemctl show -p MainPID --value agentos-node > ` + shq(pidf) + `
  echo systemd-run; cat ` + shq(pidf) + `; exit 0
fi
S=""; M=nohup
if command -v setsid >/dev/null 2>&1; then S=setsid; M=setsid+nohup; fi
nohup $S /bin/sh ` + shq(runner) + ` >> ` + shq(r.LogPath) + ` 2>&1 < /dev/null &
echo $! > ` + shq(pidf) + `
echo $M; echo $!`
	out, err := runOK(ctx, e, "/bin/sh", "-c", launch)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 启动失败: %w", err)
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return nil, fmt.Errorf("bootstrap: 启动输出异常: %q", out)
	}
	r.Method = f[0]
	r.PID, _ = strconv.Atoi(f[1])
	logf("agentos node 已启动 (pid %d, %s)", r.PID, r.Method)
	alive := func() bool {
		_, _, code, err := run(ctx, e, "/bin/sh", "-c", "kill -0 "+strconv.Itoa(r.PID))
		return err == nil && code == 0
	}
	if err := waitUp(ctx, opts.Wait, alive); err != nil {
		tail, _ := runOK(ctx, e, "tail", "-n", "20", r.LogPath)
		return r, fmt.Errorf("bootstrap: agentos node 启动后退出: %s", strings.TrimSpace(tail))
	}
	r.NodeID = readNodeID(ctx, e, data+"/node-id", opts.NodeID)
	return r, nil
}

// waitUp requires alive() to hold throughout d (polled), then returns nil.
func waitUp(ctx context.Context, d time.Duration, alive func() bool) error {
	deadline := time.Now().Add(d)
	for {
		if !alive() {
			return errors.New("process exited")
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func readNodeID(ctx context.Context, e env.Env, p, preset string) string {
	if preset != "" {
		return preset
	}
	for i := 0; i < 20; i++ {
		if rc, err := e.GetFile(ctx, p); err == nil {
			b, _ := io.ReadAll(io.LimitReader(rc, 256))
			rc.Close()
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(250 * time.Millisecond):
		}
	}
	return ""
}

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func powershell(ctx context.Context, e env.Env, script string) (string, error) {
	return runOK(ctx, e, "powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePS(script))
}

// installWindows: binary under %LOCALAPPDATA%\AgentOS, started through
// Win32_Process.Create so it escapes the SSH session's job object (Start-Process
// children are killed when an OpenSSH session ends).
func installWindows(ctx context.Context, e env.Env, opts Options, goarch string, bin io.Reader, src string, logf func(string, ...any)) (*Result, error) {
	dir := opts.InstallDir
	if dir == "" {
		base, err := powershell(ctx, e, `Write-Output $env:LOCALAPPDATA`)
		base = strings.TrimSpace(base)
		if err != nil || base == "" {
			return nil, fmt.Errorf("bootstrap: 无法获取 LOCALAPPDATA: %v", err)
		}
		dir = base + `\AgentOS`
	}
	r := &Result{OS: "windows", Arch: goarch, InstallDir: dir, BinaryPath: dir + `\agentos.exe`, LogPath: dir + `\node.log`, Method: "win32-process"}
	data := dir + `\data`
	powershell(ctx, e, `Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq `+psq(r.BinaryPath)+` } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; `+
		`New-Item -ItemType Directory -Force -Path `+psq(data)+` | Out-Null; Start-Sleep -Milliseconds 500`)
	logf("上传 agentos (%s) 到 %s", src, r.BinaryPath)
	if err := e.PutFile(ctx, r.BinaryPath, bin, 0o755); err != nil {
		return nil, fmt.Errorf("bootstrap: 上传失败: %w", err)
	}
	if opts.NodeID != "" {
		if err := e.PutFile(ctx, data+`\node-id`, strings.NewReader(opts.NodeID), 0o644); err != nil {
			return nil, err
		}
	}
	cmdf := "@echo off\r\nrem generated by AgentOS bootstrap\r\nset \"AGENTOS_TOKEN=" + opts.JoinToken + "\"\r\n\"" + r.BinaryPath +
		"\" node --server \"" + opts.ServerURL + "\" --data \"" + data + "\""
	if opts.NodeName != "" {
		cmdf += " --name \"" + opts.NodeName + "\""
	}
	cmdf += " >> \"" + r.LogPath + "\" 2>&1\r\n"
	runner := dir + `\run-node.cmd`
	if err := e.PutFile(ctx, runner, strings.NewReader(cmdf), 0o700); err != nil {
		return nil, err
	}
	out, err := powershell(ctx, e, `$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=`+
		psq(`cmd.exe /d /c "`+runner+`"`)+`; CurrentDirectory=`+psq(dir)+`}; if ($r.ReturnValue -ne 0) { Write-Error "create failed $($r.ReturnValue)"; exit 1 }; Write-Output $r.ProcessId`)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 启动失败: %w", err)
	}
	r.PID, _ = strconv.Atoi(strings.TrimSpace(out))
	logf("agentos node 已启动 (pid %d)", r.PID)
	alive := func() bool {
		_, err := powershell(ctx, e, `Get-Process -Id `+strconv.Itoa(r.PID)+` -ErrorAction Stop | Out-Null`)
		return err == nil
	}
	if err := waitUp(ctx, opts.Wait, alive); err != nil {
		tail, _ := powershell(ctx, e, `Get-Content -Tail 20 -LiteralPath `+psq(r.LogPath))
		return r, fmt.Errorf("bootstrap: agentos node 启动后退出: %s", strings.TrimSpace(tail))
	}
	r.NodeID = readNodeID(ctx, e, data+`\node-id`, opts.NodeID)
	return r, nil
}
