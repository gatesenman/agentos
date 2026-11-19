package stacks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// LogFunc receives installer progress. level is "info" | "warn" | "error"; text is one line.
type LogFunc func(level, text string)

// Timeouts (package vars so tests and callers can tune them).
var (
	StepTimeout   = 20 * time.Minute
	DetectTimeout = 45 * time.Second
	ProbeTimeout  = 30 * time.Second
)

var (
	ErrUnknownStack  = errors.New("未知的技术栈")
	ErrUnsupportedOS = errors.New("不支持的操作系统")
	ErrTimeout       = errors.New("执行超时")
)

// OSInfo describes the environment's operating system as seen through Exec.
type OSInfo struct {
	Family  string `json:"family"` // linux (debian/ubuntu) | alpine | rhel | darwin | windows
	ID      string `json:"id,omitempty"`
	Version string `json:"version,omitempty"`
	Pretty  string `json:"pretty,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Root    bool   `json:"root"`
}

// Detected is the per-stack detection result.
type Detected struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Output    string `json:"output,omitempty"` // first output line of the detect command
}

const posixProbe = `uname -s; uname -m; id -u; cat /etc/os-release 2>/dev/null || true`
const windowsProbe = `$v=[Environment]::OSVersion.Version.ToString(); $a=$env:PROCESSOR_ARCHITECTURE; $c=(Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue).Caption; Write-Output "WINDOWS"; Write-Output $v; Write-Output $a; Write-Output $c`

// DetectOS identifies the OS family by running uname + /etc/os-release, falling back to
// PowerShell / `cmd /c ver` for Windows.
func DetectOS(ctx context.Context, e env.Env) (OSInfo, error) {
	code, out, err := run(ctx, e, []string{"sh", "-c", posixProbe}, ProbeTimeout, nil)
	if err == nil && code == 0 {
		if info, ok := parsePosixProbe(out); ok {
			return info, nil
		}
	}
	if ctx.Err() != nil {
		return OSInfo{}, ctx.Err()
	}
	code, out, err = run(ctx, e, execArgv(append(append([]string{}, psArgs...), "-Command", windowsProbe)), ProbeTimeout, nil)
	if err == nil && code == 0 && strings.Contains(out, "WINDOWS") {
		lines := nonEmptyLines(out)
		info := OSInfo{Family: FamilyWindows, ID: "windows", Kernel: "Windows_NT"}
		for i, l := range lines {
			if l == "WINDOWS" {
				if i+1 < len(lines) {
					info.Version = lines[i+1]
				}
				if i+2 < len(lines) {
					info.Arch = lines[i+2]
				}
				if i+3 < len(lines) {
					info.Pretty = lines[i+3]
				}
			}
		}
		return info, nil
	}
	code, out, err = run(ctx, e, []string{"cmd", "/c", "ver"}, ProbeTimeout, nil)
	if err == nil && code == 0 && strings.Contains(strings.ToLower(out), "windows") {
		return OSInfo{Family: FamilyWindows, ID: "windows", Kernel: "Windows_NT", Pretty: strings.TrimSpace(out)}, nil
	}
	if err != nil {
		return OSInfo{}, fmt.Errorf("%w：无法在环境中执行探测命令（%v）", ErrUnsupportedOS, err)
	}
	return OSInfo{}, fmt.Errorf("%w：无法识别（uname / PowerShell / cmd 均失败）", ErrUnsupportedOS)
}

func parsePosixProbe(out string) (OSInfo, bool) {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	if len(lines) < 3 {
		return OSInfo{}, false
	}
	info := OSInfo{Kernel: strings.TrimSpace(lines[0]), Arch: strings.TrimSpace(lines[1]), Root: strings.TrimSpace(lines[2]) == "0"}
	rel := map[string]string{}
	for _, l := range lines[3:] {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			rel[k] = strings.Trim(v, `"'`)
		}
	}
	info.ID, info.Version, info.Pretty = rel["ID"], rel["VERSION_ID"], rel["PRETTY_NAME"]
	k := strings.ToLower(info.Kernel)
	switch {
	case k == "linux":
		info.Family = linuxFamily(rel["ID"], rel["ID_LIKE"])
	case k == "darwin":
		info.Family, info.ID = FamilyDarwin, "macos"
	case strings.HasPrefix(k, "mingw"), strings.HasPrefix(k, "msys"), strings.HasPrefix(k, "cygwin"):
		info.Family, info.ID = FamilyWindows, "windows"
	default:
		return info, false
	}
	return info, true
}

func linuxFamily(id, like string) string {
	words := append([]string{strings.ToLower(id)}, strings.Fields(strings.ToLower(like))...)
	for _, w := range words {
		switch w {
		case "debian", "ubuntu", "linuxmint", "pop", "raspbian", "kali":
			return FamilyLinux
		case "alpine":
			return FamilyAlpine
		case "rhel", "fedora", "centos", "rocky", "almalinux", "amzn", "ol":
			return FamilyRHEL
		}
	}
	return "linux-" + strings.ToLower(id) // unknown distro: no steps, detection still works
}

var versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// DetectStack runs the stack's detect command; Installed is true when it exits 0.
func DetectStack(ctx context.Context, e env.Env, osi OSInfo, s *Stack) Detected {
	d := Detected{ID: s.ID, Name: s.Name}
	script := s.DetectScript(osi.Family)
	if script == "" {
		return d
	}
	code, out, err := run(ctx, e, execArgv(DetectArgv(osi.Family, script)), DetectTimeout, nil)
	if err != nil || code != 0 {
		return d
	}
	d.Installed = true
	for _, l := range nonEmptyLines(out) {
		if d.Output == "" {
			d.Output = l
		}
		if v := versionRe.FindString(l); v != "" {
			d.Version, d.Output = v, l
			break
		}
	}
	return d
}

// Detect probes the OS and every catalog stack; it returns all stacks with Installed flags.
func Detect(ctx context.Context, e env.Env) ([]Detected, OSInfo, error) {
	osi, err := DetectOS(ctx, e)
	if err != nil {
		return nil, osi, err
	}
	all := All()
	out := make([]Detected, 0, len(all))
	for i := range all {
		if ctx.Err() != nil {
			return out, osi, ctx.Err()
		}
		out = append(out, DetectStack(ctx, e, osi, &all[i]))
	}
	return out, osi, nil
}

// Install installs a stack (and its "requires" dependencies first) into e. It is
// idempotent: a stack whose detect command already succeeds is skipped. Steps run
// sequentially, each bounded by StepTimeout; output lines stream to logf.
func Install(ctx context.Context, e env.Env, stackID string, logf LogFunc) error {
	if logf == nil {
		logf = func(string, string) {}
	}
	s, ok := Get(stackID)
	if !ok {
		return fmt.Errorf("%w：%s", ErrUnknownStack, stackID)
	}
	logf("info", "正在识别环境操作系统…")
	osi, err := DetectOS(ctx, e)
	if err != nil {
		logf("error", err.Error())
		return err
	}
	desc := osi.Pretty
	if desc == "" {
		desc = osi.ID
	}
	logf("info", fmt.Sprintf("操作系统：%s（%s，%s）", desc, osi.Family, osi.Arch))
	err = installRec(ctx, e, osi, s, logf, map[string]bool{})
	if err != nil {
		logf("error", err.Error())
	}
	return err
}

func installRec(ctx context.Context, e env.Env, osi OSInfo, s *Stack, logf LogFunc, seen map[string]bool) error {
	if seen[s.ID] {
		return nil
	}
	seen[s.ID] = true
	for _, dep := range s.Requires {
		ds, ok := Get(dep)
		if !ok {
			return fmt.Errorf("%w：%s（%s 的依赖）", ErrUnknownStack, dep, s.ID)
		}
		logf("info", fmt.Sprintf("「%s」依赖「%s」，先检查依赖", s.Name, ds.Name))
		if err := installRec(ctx, e, osi, ds, logf, seen); err != nil {
			return err
		}
	}
	if d := DetectStack(ctx, e, osi, s); d.Installed {
		logf("info", fmt.Sprintf("「%s」已安装（%s），跳过", s.Name, firstNonEmpty(d.Version, d.Output)))
		return nil
	}
	steps := s.Steps(osi.Family)
	if len(steps) == 0 {
		return fmt.Errorf("%w：「%s」暂不支持 %s（%s），可用：%s", ErrUnsupportedOS, s.Name, osi.Family, osi.Pretty, strings.Join(sortedKeys(s.Setup), ", "))
	}
	logf("info", fmt.Sprintf("开始安装「%s」，共 %d 步", s.Name, len(steps)))
	for i, step := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("安装「%s」已取消：%w", s.Name, err)
		}
		logf("info", fmt.Sprintf("[%d/%d] %s", i+1, len(steps), summarize(step)))
		start := time.Now()
		code, out, err := run(ctx, e, execArgv(StepArgv(osi.Family, step)), StepTimeout, func(stream, line string) {
			logf("info", line)
		})
		switch {
		case errors.Is(err, ErrTimeout):
			return fmt.Errorf("安装「%s」第 %d/%d 步超时（超过 %s）：%s", s.Name, i+1, len(steps), StepTimeout, summarize(step))
		case err != nil:
			return fmt.Errorf("安装「%s」第 %d/%d 步无法执行：%v", s.Name, i+1, len(steps), err)
		case code != 0:
			msg := fmt.Sprintf("安装「%s」第 %d/%d 步失败（退出码 %d）：%s", s.Name, i+1, len(steps), code, summarize(step))
			if h := hint(out); h != "" {
				msg += "。可能原因：" + h
			}
			return errors.New(msg)
		}
		logf("info", fmt.Sprintf("[%d/%d] 完成（%s）", i+1, len(steps), time.Since(start).Round(time.Second)))
	}
	d := DetectStack(ctx, e, osi, s)
	if !d.Installed {
		return fmt.Errorf("「%s」安装步骤已完成，但检测命令仍失败（%s）；可能需要重新登录或检查 PATH", s.Name, s.DetectScript(osi.Family))
	}
	logf("info", fmt.Sprintf("「%s」安装完成，版本 %s", s.Name, firstNonEmpty(d.Version, d.Output)))
	return nil
}

func hint(out string) string {
	o := strings.ToLower(out)
	switch {
	case strings.Contains(o, "a password is required"), strings.Contains(o, "are you root"),
		strings.Contains(o, "permission denied"), strings.Contains(o, "eacces"), strings.Contains(o, "requires elevation"),
		strings.Contains(o, "administrator"):
		return "权限不足，需要 root / 免密 sudo（NOPASSWD）或管理员权限"
	case strings.Contains(o, "could not resolve"), strings.Contains(o, "temporary failure in name resolution"),
		strings.Contains(o, "network is unreachable"), strings.Contains(o, "connection timed out"), strings.Contains(o, "failed to connect"):
		return "网络不可用或无法访问软件源"
	case strings.Contains(o, "unable to locate package"), strings.Contains(o, "no such package"), strings.Contains(o, "no match for argument"):
		return "软件源中找不到该软件包（发行版版本过旧或未启用对应仓库）"
	case strings.Contains(o, "could not get lock"), strings.Contains(o, "unable to lock"):
		return "包管理器被其他进程占用，请稍后重试"
	case strings.Contains(o, "command not found"), strings.Contains(o, "is not recognized"), strings.Contains(o, "not found"):
		return "缺少依赖命令"
	}
	return ""
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return "未知版本"
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---- exec plumbing ----

const tailLines = 200

// run executes argv in e, streaming lines to line (may be nil). It returns the exit code
// and the last tailLines of combined output. Timeout yields ErrTimeout.
func run(ctx context.Context, e env.Env, argv []string, timeout time.Duration, line func(stream, text string)) (int, string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := e.Exec(cctx, env.ExecRequest{Cmd: argv})
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return -1, "", ErrTimeout
		}
		return -1, "", err
	}
	defer sess.Close()
	if in := sess.Stdin(); in != nil {
		in.Close()
	}
	var (
		mu   sync.Mutex
		tail []string
		wg   sync.WaitGroup
	)
	pump := func(r io.Reader, stream string) {
		defer wg.Done()
		if r == nil {
			return
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			t := strings.TrimRight(sc.Text(), "\r")
			if i := strings.LastIndexByte(t, '\r'); i >= 0 { // progress bars: keep the final state
				t = t[i+1:]
			}
			mu.Lock()
			tail = append(tail, t)
			if len(tail) > tailLines {
				tail = tail[len(tail)-tailLines:]
			}
			mu.Unlock()
			if line != nil && strings.TrimSpace(t) != "" {
				line(stream, t)
			}
		}
		io.Copy(io.Discard, r)
	}
	wg.Add(2)
	go pump(sess.Stdout(), "stdout")
	go pump(sess.Stderr(), "stderr")
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		wg.Wait()
		c, err := sess.Wait()
		done <- result{c, err}
	}()
	joined := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(tail, "\n")
	}
	select {
	case r := <-done:
		if r.code != 0 {
			return r.code, joined(), nil
		}
		if r.err != nil {
			if cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				return -1, joined(), ErrTimeout
			}
			return -1, joined(), r.err
		}
		return 0, joined(), nil
	case <-cctx.Done():
		sess.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		if ctx.Err() != nil {
			return -1, joined(), ctx.Err()
		}
		return -1, joined(), ErrTimeout
	}
}
