//go:build windows || linux

// Command agentos-tray is the AgentOS system-tray launcher (module M8).
//
// It is what the desktop / start-menu shortcuts start: it makes sure a local
// AgentOS server is running (an installed Windows service if present,
// otherwise a hidden child process "agentos server"), opens the browser
// console and then lives in the notification area with a status indicator
// and start / stop / restart / open console / logs / autostart menu items.
//
//	agentos-tray [--listen :7800] [--data DIR] [--open=true] [--start=true] [--agentos PATH]
//
// Pure Go (fyne.io/systray): CGO_ENABLED=0 on Windows and Linux (D-Bus SNI).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	_ "embed"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconICO []byte

//go:embed icon.png
var iconPNG []byte

var version = "dev" // set by -ldflags "-X main.version=..."

type app struct {
	listen, dataDir, agentosExe string
	mode, server, token, name   string // from <data>/agentos.conf; mode "node" joins a remote console

	mu      sync.Mutex
	child   *exec.Cmd
	childCh chan struct{} // closed when child exits
	logFile *os.File

	mStatus, mOpen, mStart, mStop, mRestart, mLogs, mData, mPw, mAuto, mQuit *systray.MenuItem
}

func main() {
	listen := flag.String("listen", envOr("AGENTOS_LISTEN", ":7800"), "server listen address")
	dataDir := flag.String("data", defaultDataDir(), "data directory")
	open := flag.Bool("open", true, "open the browser console once the server is up")
	start := flag.Bool("start", true, "start the server if it is not running")
	exe := flag.String("agentos", "", "path of agentos executable (default: next to agentos-tray)")
	showVer := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVer {
		fmt.Println("agentos-tray", version)
		return
	}
	a := &app{listen: *listen, dataDir: *dataDir, agentosExe: *exe}
	if a.agentosExe == "" {
		a.agentosExe = siblingExe()
	}
	a.loadConf(filepath.Join(a.dataDir, "agentos.conf"))
	os.MkdirAll(filepath.Join(a.dataDir, "logs"), 0o755)
	if f, err := os.OpenFile(filepath.Join(a.dataDir, "logs", "tray.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}

	if !acquireSingleInstance() {
		// Already running: behave like a "open console" shortcut.
		log.Printf("another tray instance is running; opening console")
		if *start && !a.healthy() {
			a.startServer()
			a.waitHealthy(30 * time.Second)
		}
		openURL(a.consoleURL())
		return
	}
	systray.Run(func() { a.onReady(*start, *open) }, a.onExit)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// defaultDataDir mirrors cmd/agentos DefaultDataDir.
func defaultDataDir() string {
	if v := os.Getenv("AGENTOS_DATA"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "AgentOS")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".agentos")
	}
	return ".agentos"
}

func siblingExe() string {
	name := "agentos"
	if runtime.GOOS == "windows" {
		name = "agentos.exe"
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func (a *app) port() string {
	_, p, err := net.SplitHostPort(a.listen)
	if err != nil || p == "" {
		return "7800"
	}
	return p
}

func (a *app) isNode() bool { return a.mode == "node" }

// role is the user-facing name of what this machine runs.
func (a *app) role() string {
	if a.isNode() {
		return "节点"
	}
	return "服务器"
}

func (a *app) consoleURL() string {
	if a.isNode() {
		return strings.TrimRight(a.server, "/") + "/"
	}
	return "http://localhost:" + a.port() + "/"
}

// loadConf reads the installer's key=value config (UTF-16LE BOM or UTF-8).
func (a *app) loadConf(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := string(b)
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		text = string(utf16.Decode(u))
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\x00"))
		k, v, ok := strings.Cut(line, "=")
		if !ok || line[0] == '#' || line[0] == ';' {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "mode":
			a.mode = strings.ToLower(v)
		case "server":
			a.server = v
		case "token":
			a.token = v
		case "name":
			a.name = v
		case "listen":
			if a.listen == ":7800" && v != "" {
				a.listen = v
			}
		}
	}
	if a.isNode() && (a.server == "" || a.token == "") {
		log.Printf("config: node mode without server/token, falling back to server mode")
		a.mode = ""
	}
}

type health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (a *app) probe() (health, bool) {
	if a.isNode() {
		// a node has no local HTTP endpoint: "healthy" = process/service running
		return health{Status: "ok", Version: version}, a.childRunning() || serviceState() == "running"
	}
	c := http.Client{Timeout: 1500 * time.Millisecond}
	var h health
	resp, err := c.Get("http://127.0.0.1:" + a.port() + "/api/v1/health")
	if err != nil {
		return h, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&h) != nil {
		return h, false
	}
	return h, h.Status == "ok"
}

func (a *app) healthy() bool { _, ok := a.probe(); return ok }

func (a *app) waitHealthy(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if a.healthy() {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

func (a *app) childRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.child != nil
}

// startServer starts the installed service if there is one, else a hidden
// child process. It returns a short human readable description.
func (a *app) startServer() string {
	if a.healthy() {
		return "already running"
	}
	if st := serviceState(); st == "stopped" {
		if err := serviceStart(); err == nil {
			log.Printf("started Windows service")
			return "service"
		} else {
			log.Printf("service start failed (%v), falling back to child process", err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.child != nil {
		return "child"
	}
	logPath := filepath.Join(a.dataDir, "logs", "server.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("open server log: %v", err)
	}
	args := []string{"server", "--listen", a.listen, "--data", a.dataDir}
	if a.isNode() {
		args = []string{"node", "--server", a.server, "--token", a.token, "--data", filepath.Join(a.dataDir, "node")}
		if a.name != "" {
			args = append(args, "--name", a.name)
		}
	}
	cmd := exec.Command(a.agentosExe, args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.Dir = filepath.Dir(a.agentosExe)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("start server: %v", err)
		if lf != nil {
			lf.Close()
		}
		return "error: " + err.Error()
	}
	log.Printf("started server pid %d", cmd.Process.Pid)
	a.child, a.logFile, a.childCh = cmd, lf, make(chan struct{})
	ch := a.childCh
	go func() {
		err := cmd.Wait()
		log.Printf("server exited: %v", err)
		a.mu.Lock()
		if a.child == cmd {
			a.child = nil
			if a.logFile != nil {
				a.logFile.Close()
				a.logFile = nil
			}
		}
		a.mu.Unlock()
		close(ch)
		a.refresh()
	}()
	return "child"
}

func (a *app) stopServer() {
	a.mu.Lock()
	cmd, ch := a.child, a.childCh
	a.mu.Unlock()
	if cmd != nil {
		terminate(cmd)
		select {
		case <-ch:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
		}
		return
	}
	if serviceState() == "running" {
		if err := serviceStop(); err != nil {
			log.Printf("service stop: %v", err)
			notify("无法停止服务 (需要管理员权限): " + err.Error())
		}
		return
	}
	if a.healthy() {
		notify("服务器由其他进程启动, 托盘无法停止它。")
	}
}

func (a *app) onReady(start, open bool) {
	if runtime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTitle("AgentOS")
	systray.SetTooltip("AgentOS")
	a.mStatus = systray.AddMenuItem("状态: 检测中…", "")
	a.mStatus.Disable()
	systray.AddSeparator()
	a.mOpen = systray.AddMenuItem("打开控制台", "在浏览器中打开 AgentOS 控制台")
	a.mStart = systray.AddMenuItem("启动"+a.role(), "")
	a.mStop = systray.AddMenuItem("停止"+a.role(), "")
	a.mRestart = systray.AddMenuItem("重启"+a.role(), "")
	systray.AddSeparator()
	a.mLogs = systray.AddMenuItem("查看"+a.role()+"日志", "")
	a.mData = systray.AddMenuItem("打开数据目录", a.dataDir)
	a.mPw = systray.AddMenuItem("查看初始管理员密码", "")
	if a.isNode() {
		a.mPw.Hide()
		a.mOpen.SetTooltip("打开远程控制台 " + a.consoleURL())
	}
	a.mAuto = systray.AddMenuItemCheckbox("登录时自动启动", "", autostartEnabled())
	if !autostartSupported() {
		a.mAuto.Hide()
	}
	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("退出", "退出托盘 (并停止由托盘启动的服务器)")
	systray.SetOnTapped(func() { openURL(a.consoleURL()) })

	go func() {
		if start && !a.healthy() {
			a.mStatus.SetTitle("状态: 正在启动…")
			a.startServer()
			a.waitHealthy(30 * time.Second)
		}
		a.refresh()
		if open && a.healthy() {
			openURL(a.consoleURL())
		}
	}()
	go a.pollLoop()
	go a.menuLoop()
}

func (a *app) pollLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for range t.C {
		a.refresh()
	}
}

func (a *app) refresh() {
	if a.mStatus == nil {
		return
	}
	h, ok := a.probe()
	mode := ""
	switch {
	case a.childRunning():
		mode = "托盘进程"
	case serviceState() == "running":
		mode = "Windows 服务"
	case ok:
		mode = "外部进程"
	}
	if ok && a.isNode() {
		a.mStatus.SetTitle(fmt.Sprintf("状态: 节点运行中 (%s) → %s", mode, a.server))
		systray.SetTooltip("AgentOS 节点 - 运行中")
		a.mStart.Disable()
		a.mStop.Enable()
		a.mRestart.Enable()
	} else if ok {
		a.mStatus.SetTitle(fmt.Sprintf("状态: 运行中 v%s (%s) :%s", strings.TrimPrefix(h.Version, "v"), mode, a.port()))
		systray.SetTooltip("AgentOS - 运行中 " + a.consoleURL())
		a.mStart.Disable()
		a.mStop.Enable()
		a.mRestart.Enable()
		a.mOpen.Enable()
	} else {
		if a.childRunning() {
			a.mStatus.SetTitle("状态: 正在启动…")
		} else {
			a.mStatus.SetTitle("状态: 已停止")
		}
		systray.SetTooltip("AgentOS - 已停止")
		a.mStart.Enable()
		a.mStop.Disable()
		a.mRestart.Disable()
	}
}

func (a *app) menuLoop() {
	for {
		select {
		case <-a.mOpen.ClickedCh:
			if !a.healthy() {
				a.startServer()
				a.waitHealthy(30 * time.Second)
			}
			openURL(a.consoleURL())
		case <-a.mStart.ClickedCh:
			a.mStatus.SetTitle("状态: 正在启动…")
			a.startServer()
			a.waitHealthy(30 * time.Second)
			a.refresh()
		case <-a.mStop.ClickedCh:
			a.mStatus.SetTitle("状态: 正在停止…")
			a.stopServer()
			a.refresh()
		case <-a.mRestart.ClickedCh:
			a.mStatus.SetTitle("状态: 正在重启…")
			a.stopServer()
			waitDown(a, 15*time.Second)
			a.startServer()
			a.waitHealthy(30 * time.Second)
			a.refresh()
		case <-a.mLogs.ClickedCh:
			p := filepath.Join(a.dataDir, "logs", "server.log")
			if serviceState() == "running" {
				p = filepath.Join(a.dataDir, "logs", "service.log")
			}
			openPath(p)
		case <-a.mData.ClickedCh:
			openPath(a.dataDir)
		case <-a.mPw.ClickedCh:
			p := filepath.Join(a.dataDir, "server", "initial-admin-password.txt")
			if _, err := os.Stat(p); err != nil {
				notify("未找到初始密码文件 (可能已在首次启动时指定密码或已删除)。")
			} else {
				openPath(p)
			}
		case <-a.mAuto.ClickedCh:
			on := !a.mAuto.Checked()
			if err := setAutostart(on); err != nil {
				notify("设置自动启动失败: " + err.Error())
			} else if on {
				a.mAuto.Check()
			} else {
				a.mAuto.Uncheck()
			}
		case <-a.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func waitDown(a *app, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for ctx.Err() == nil && a.healthy() {
		time.Sleep(300 * time.Millisecond)
	}
}

func (a *app) onExit() {
	if a.childRunning() {
		a.stopServer()
	}
}
