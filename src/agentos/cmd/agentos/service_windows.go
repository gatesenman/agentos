//go:build windows

package main

// Windows service support (module M8), built on golang.org/x/sys/windows/svc.
//
//	agentos service install [--mode console|node] [--listen :7800] [--server URL --token T] [--name N] [--data DIR] [--manual]
//	agentos service uninstall | start | stop | restart | status
//	agentos service run [same flags]   (invoked by the Service Control Manager)
//
// The service runs as LocalSystem with auto start (delayed) and recovery
// actions "restart after 5s / 10s / 30s" (reset after 24h, also on non-crash
// failures). Logs go to <data>\logs\service.log and start/stop events to the
// Windows Event Log (source "AgentOS").
import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/version"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	svcName        = "AgentOS"
	svcDisplayName = "AgentOS"
	svcDescription = "AgentOS 本地 AI Agent 管理服务 (控制台 + 本地节点) / AgentOS agent manager"
)

type svcOptions struct {
	mode                      string // "console" (server + local node) or "node"
	listen, dataDir, nodeName string
	server, token             string // node mode
	confPath                  string
	withNode, manual          bool
	explicit                  map[string]bool // flags given on the command line
}

func parseSvcFlags(name string, args []string) (*svcOptions, error) {
	o := &svcOptions{explicit: map[string]bool{}}
	fs := flag.NewFlagSet("service "+name, flag.ContinueOnError)
	fs.StringVar(&o.mode, "mode", "console", "console (management server + local node) | node (join a remote console)")
	fs.StringVar(&o.listen, "listen", envOr("AGENTOS_LISTEN", ":7800"), "HTTP listen address (server mode)")
	fs.StringVar(&o.dataDir, "data", DefaultDataDir(), "data directory")
	fs.BoolVar(&o.withNode, "with-node", true, "also run the local node in-process (server mode)")
	fs.StringVar(&o.nodeName, "node-name", "", "node display name (alias of --name)")
	fs.StringVar(&o.nodeName, "name", "", "node display name")
	fs.StringVar(&o.server, "server", "", "console URL to join (node mode), e.g. http://192.168.1.10:7800")
	fs.StringVar(&o.token, "token", "", "node join token (node mode)")
	fs.StringVar(&o.confPath, "config", "", "config file (default <data>\\agentos.conf)")
	fs.BoolVar(&o.manual, "manual", false, "install with manual start type instead of automatic")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	fs.Visit(func(f *flag.Flag) { o.explicit[f.Name] = true })
	if o.explicit["node-name"] {
		o.explicit["name"] = true
	}
	if abs, err := filepath.Abs(o.dataDir); err == nil {
		o.dataDir = abs
	}
	if o.confPath == "" {
		o.confPath = filepath.Join(o.dataDir, "agentos.conf")
	}
	// values from the config file fill in everything not given as a flag
	if conf, err := readConf(o.confPath); err == nil {
		set := func(flagName string, dst *string, key string) {
			if v, ok := conf[key]; ok && !o.explicit[flagName] {
				*dst = v
			}
		}
		set("mode", &o.mode, "mode")
		set("listen", &o.listen, "listen")
		set("server", &o.server, "server")
		set("token", &o.token, "token")
		set("name", &o.nodeName, "name")
		if v, ok := conf["with_node"]; ok && !o.explicit["with-node"] {
			o.withNode = v != "false" && v != "0"
		}
	}
	o.mode = strings.ToLower(strings.TrimSpace(o.mode))
	switch o.mode {
	case "", "console", "server", "all", "all-in-one":
		o.mode = "console"
	case "node":
		if nf, ok := loadNodeFile(filepath.Join(o.dataDir, "node")); ok && (o.server == "" || o.token == "") {
			o.server, o.token = firstNonEmpty(o.server, nf.Server), firstNonEmpty(o.token, nf.Token)
			o.nodeName = firstNonEmpty(o.nodeName, nf.Name)
		}
		if o.server == "" || o.token == "" {
			return nil, errors.New("node mode needs --server and --token (or server=/token= in the config file)")
		}
	default:
		return nil, fmt.Errorf("unknown mode %q (console|node)", o.mode)
	}
	return o, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// readConf parses the simple key=value config written by the installer
// (UTF-16LE with BOM, or UTF-8). "[section]" lines and #/; comments are ignored.
func readConf(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var text string
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		text = string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		text = string(b[3:])
	default:
		text = string(b)
	}
	m := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\x00"))
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return m, nil
}

func writeConf(path string, o *svcOptions) error {
	var sb strings.Builder
	sb.WriteString("# AgentOS configuration (written by agentos service install / the installer)\r\n[agentos]\r\n")
	fmt.Fprintf(&sb, "mode=%s\r\n", o.mode)
	if o.mode == "node" {
		fmt.Fprintf(&sb, "server=%s\r\ntoken=%s\r\n", o.server, o.token)
	} else {
		fmt.Fprintf(&sb, "listen=%s\r\nwith_node=%v\r\n", o.listen, o.withNode)
	}
	if o.nodeName != "" {
		fmt.Fprintf(&sb, "name=%s\r\n", o.nodeName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

const serviceUsage = `用法: agentos service <install|uninstall|start|stop|restart|status|run> [参数]
  install    注册 Windows 服务 (开机自启, 失败自动重启)
               管理端: --listen :7800 --data DIR --with-node=true [--name N] [--manual]
               节点:   --mode node --server URL --token T [--name N] [--data DIR]
             参数保存到 <data>\agentos.conf, 服务以 "service run --config <conf>" 运行
  uninstall  停止并删除服务
  start      启动服务
  stop       停止服务
  restart    重启服务
  status     显示服务状态
  run        由服务管理器调用 (请勿手动执行)
`

func runService(args []string) error {
	if len(args) < 1 {
		fmt.Print(serviceUsage)
		return errors.New("missing service sub-command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "install":
		o, err := parseSvcFlags(cmd, rest)
		if err != nil {
			return err
		}
		return installService(o)
	case "uninstall", "remove":
		return uninstallService()
	case "start":
		return startService()
	case "stop":
		return stopService(30 * time.Second)
	case "restart":
		if err := stopService(30 * time.Second); err != nil && !errors.Is(err, errNotRunning) {
			return err
		}
		return startService()
	case "status":
		st, err := serviceStatus()
		if err != nil {
			return err
		}
		fmt.Println(st)
		return nil
	case "run":
		o, err := parseSvcFlags(cmd, rest)
		if err != nil {
			return err
		}
		return runAsService(o)
	case "-h", "--help", "help":
		fmt.Print(serviceUsage)
		return nil
	}
	fmt.Print(serviceUsage)
	return fmt.Errorf("unknown service sub-command %q", cmd)
}

var errNotRunning = errors.New("service is not running")

func connectMgr() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("需要管理员权限 / administrator rights required: %w", err)
		}
		return nil, err
	}
	return m, nil
}

func installService(o *svcOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// persist settings (keeps the token out of the service command line)
	settingsGiven := false
	for _, k := range []string{"mode", "listen", "server", "token", "name", "with-node"} {
		settingsGiven = settingsGiven || o.explicit[k]
	}
	if _, err := os.Stat(o.confPath); err != nil || settingsGiven {
		if err := writeConf(o.confPath, o); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
	}
	if o.mode == "node" {
		// same settings file as `agentos node --install-service`, so a manual
		// `agentos node --data <data>\node` uses them too
		if err := saveNodeFile(filepath.Join(o.dataDir, "node"), NodeFile{Server: o.server, Token: o.token, Name: o.nodeName}); err != nil {
			log.Printf("warning: write node.json: %v", err)
		}
	}
	runArgs := []string{"service", "run", "--data", o.dataDir, "--config", o.confPath}
	start := uint32(mgr.StartAutomatic)
	if o.manual {
		start = mgr.StartManual
	}
	cfg := mgr.Config{
		DisplayName:  svcDisplayName,
		Description:  svcDescription,
		StartType:    start,
		ErrorControl: mgr.ErrorNormal,
	}

	s, err := m.OpenService(svcName)
	if err == nil {
		// Upgrade in place: refresh binary path + config of the existing service.
		cur := cfg
		cur.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
		cur.BinaryPathName = quoteCmdLine(exe, runArgs)
		if err := updateServiceConfig(s, cur); err != nil {
			s.Close()
			return fmt.Errorf("update service: %w", err)
		}
		log.Printf("服务已存在, 已更新配置 / service %s exists, configuration updated", svcName)
	} else {
		err = retryLocked(func() (e error) { s, e = m.CreateService(svcName, exe, cfg, runArgs...); return })
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		log.Printf("服务已安装 / service %s installed (%s)", svcName, exe)
	}
	defer s.Close()

	// Delayed auto start is best effort (not supported by e.g. Wine).
	// (skipped under Wine, whose delayed-start thread races with explicit starts)
	if !o.manual && !isWine() {
		if err := setDelayedAutoStart(s, true); err != nil {
			log.Printf("warning: delayed auto start: %v", err)
		}
	}
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		log.Printf("warning: set recovery actions: %v", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		log.Printf("warning: set recovery on non-crash failures: %v", err)
	}
	if err := eventlog.InstallAsEventCreate(svcName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
		!strings.Contains(err.Error(), "exists") {
		log.Printf("warning: event log source: %v", err)
	}
	if err := os.MkdirAll(o.dataDir, 0o755); err != nil {
		log.Printf("warning: create data dir: %v", err)
	}
	what := "管理端 + 本地节点, listen " + o.listen
	if o.mode == "node" {
		what = "节点 -> " + o.server
	}
	fmt.Printf("服务已注册 / service registered: %s  (%s, data %s, config %s)\n运行 \"agentos service start\" 启动。\n",
		svcName, what, o.dataDir, o.confPath)
	return nil
}

// updateServiceConfig is mgr.Service.UpdateConfig without the SID-type call
// (which fails on older systems and Wine).
func updateServiceConfig(s *mgr.Service, c mgr.Config) error {
	p := func(v string) *uint16 {
		if v == "" {
			return nil
		}
		r, _ := windows.UTF16PtrFromString(v)
		return r
	}
	if err := windows.ChangeServiceConfig(s.Handle, c.ServiceType, c.StartType, c.ErrorControl,
		p(c.BinaryPathName), p(c.LoadOrderGroup), nil, nil, p(c.ServiceStartName), nil, p(c.DisplayName)); err != nil {
		return err
	}
	d := windows.SERVICE_DESCRIPTION{Description: p(c.Description)}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_DESCRIPTION, (*byte)(unsafe.Pointer(&d)))
}

// isWine reports whether we run under Wine (ntdll exports wine_get_version).
func isWine() bool {
	return windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
}

func setDelayedAutoStart(s *mgr.Service, on bool) error {
	var v uint32
	if on {
		v = 1
	}
	info := struct{ delayed uint32 }{v}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO, (*byte)(unsafe.Pointer(&info)))
}

// installNodeAsWindowsService registers the node (settings in nf) as the
// "AgentOS" SCM service (LocalSystem, auto start, recovery actions) and
// starts it. nodeDataDir is the node --data dir (default <data>\node); the
// service config/logs live one level up. Glue for main.go if the lead wants a
// real service (used by installNodeServicePlatform in node_service_windows.go):
//
//	return installNodeAsWindowsService(abs, nf)   // in installNodeService on windows
//
// Note: a service runs in session 0, so built-in screen capture is unavailable.
func installNodeAsWindowsService(nodeDataDir string, nf NodeFile) error {
	base := nodeDataDir
	if filepath.Base(base) == "node" {
		base = filepath.Dir(base)
	}
	args := []string{"--mode", "node", "--server", nf.Server, "--token", nf.Token, "--data", base}
	if nf.Name != "" {
		args = append(args, "--name", nf.Name)
	}
	o, err := parseSvcFlags("install", args)
	if err != nil {
		return err
	}
	if err := installService(o); err != nil {
		return err
	}
	if err := stopService(30 * time.Second); err != nil && !errors.Is(err, errNotRunning) {
		log.Printf("warning: stop old service: %v", err)
	}
	return startService()
}

// quoteCmdLine builds a Windows command line the same way mgr.CreateService does.
func quoteCmdLine(exe string, args []string) string {
	s := windows.EscapeArg(exe)
	for _, a := range args {
		s += " " + windows.EscapeArg(a)
	}
	return s
}

func uninstallService() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", svcName)
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		_ = stopOpened(s, 30*time.Second)
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(svcName)
	fmt.Printf("服务已删除 / service %s removed\n", svcName)
	return nil
}

func startService() error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("service %s is not installed (run \"agentos service install\")", svcName)
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		fmt.Println("服务已在运行 / service already running")
		return nil
	}
	if err := retryLocked(func() error { return s.Start() }); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			fmt.Println("服务已启动 / service started")
			return nil
		}
		if st.State == svc.Stopped {
			return fmt.Errorf("service stopped right after start (exit code %d), see logs\\service.log", st.ServiceSpecificExitCode)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("timeout waiting for service to start")
}

func stopService(timeout time.Duration) error {
	m, err := connectMgr()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", svcName)
	}
	defer s.Close()
	return stopOpened(s, timeout)
}

func stopOpened(s *mgr.Service, timeout time.Duration) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		fmt.Println("服务未运行 / service not running")
		return errNotRunning
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err = s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			fmt.Println("服务已停止 / service stopped")
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("timeout waiting for service to stop")
}

func serviceStatus() (string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return "not-installed", nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "", err
	}
	switch st.State {
	case svc.Running:
		return "running", nil
	case svc.Stopped:
		return "stopped", nil
	case svc.StartPending:
		return "start-pending", nil
	case svc.StopPending:
		return "stop-pending", nil
	}
	return fmt.Sprintf("state-%d", st.State), nil
}

// ---- service process ----

type agentService struct {
	o    *svcOptions
	elog *eventlog.Log
}

func runAsService(o *svcOptions) error {
	logDir := filepath.Join(o.dataDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	f, ferr := os.OpenFile(filepath.Join(logDir, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	// f stays open for the process lifetime (main logs the returned error).
	// svc.IsWindowsService() relies on the service running in session 0,
	// which is not true everywhere (e.g. Wine), so just try to connect to the
	// service control manager and fall back to foreground mode if that fails.
	isSvc, _ := svc.IsWindowsService()
	if ferr == nil {
		log.SetOutput(f)
		// runtime panics / fatal errors go to the log file as well
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
		os.Stderr = f
	}
	if !isSvc {
		err := runDispatcher(o)
		if !errors.Is(err, windows.ERROR_FAILED_SERVICE_CONTROLLER_CONNECT) {
			return err
		}
		// Debug mode (run from a console): behave like "agentos server".
		if ferr == nil {
			log.SetOutput(io.MultiWriter(f, consoleWriter{}))
		}
		log.Printf("not started by the service manager; running in foreground")
		ctx, cancel := signalContext()
		defer cancel()
		return o.run(ctx)
	}
	return runDispatcher(o)
}

// consoleWriter writes to the original console handle, ignoring errors.
type consoleWriter struct{}

func (consoleWriter) Write(p []byte) (int, error) {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err == nil && h != 0 && h != windows.InvalidHandle {
		var n uint32
		windows.WriteFile(h, p, &n, nil)
	}
	return len(p), nil
}

func runDispatcher(o *svcOptions) error {
	el, err := eventlog.Open(svcName)
	if err != nil {
		el = nil
	}
	h := &agentService{o: o, elog: el}
	err = svc.Run(svcName, h)
	if el != nil {
		el.Close()
	}
	return err
}

// run executes the configured role until ctx is cancelled.
func (o *svcOptions) run(ctx context.Context) error {
	if o.mode != "node" {
		return serveCtx(ctx, o.listen, o.dataDir, "", o.withNode, o.nodeName)
	}
	n, err := node.New(node.Config{ServerURL: o.server, Token: o.token, Name: o.nodeName,
		DataDir: filepath.Join(o.dataDir, "node"), VNCAddr: envOr("AGENTOS_VNC", "127.0.0.1:5900")})
	if err != nil {
		return err
	}
	log.Printf("AgentOS node %s (id %s) -> %s", version.Version, n.ID(), o.server)
	return n.Run(ctx)
}

func (h *agentService) info(msg string) {
	log.Print(msg)
	if h.elog != nil {
		h.elog.Info(1, msg)
	}
}

func (h *agentService) errorf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	log.Print(msg)
	if h.elog != nil {
		h.elog.Error(1, msg)
	}
}

func (h *agentService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.o.run(ctx)
	}()
	changes <- svc.Status{State: svc.Running, Accepts: accepted}
	if h.o.mode == "node" {
		h.info(fmt.Sprintf("AgentOS 节点服务已启动 / node service started (server %s, data %s)", h.o.server, h.o.dataDir))
	} else {
		h.info(fmt.Sprintf("AgentOS 服务已启动 / service started (listen %s, data %s)", h.o.listen, h.o.dataDir))
	}

	for {
		select {
		case err := <-done:
			// serveCtx returned on its own: treat as failure so the SCM
			// recovery actions restart us.
			if err == nil {
				err = errors.New("server exited unexpectedly")
			}
			h.errorf("AgentOS 服务异常退出 / service failed: %v", err)
			changes <- svc.Status{State: svc.StopPending}
			return true, 1
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.info("AgentOS 服务正在停止 / service stopping")
				changes <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					log.Printf("timeout waiting for server shutdown")
				}
				h.info("AgentOS 服务已停止 / service stopped")
				return false, 0
			}
		}
	}
}

// retryLocked retries fn while the SCM reports ERROR_SERVICE_DATABASE_LOCKED (another
// installer/SCM operation in flight; common right after boot and under Wine).
func retryLocked(fn func() error) error {
	var err error
	for i := 0; i < 30; i++ {
		if err = fn(); !errors.Is(err, windows.ERROR_SERVICE_DATABASE_LOCKED) {
			return err
		}
		time.Sleep(time.Second)
	}
	return err
}
