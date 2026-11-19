// Command agentos-desktop is the desktop shell for AgentOS ("AgentOS.exe").
//
// It makes sure an AgentOS server is reachable (an already running process or
// Windows service, otherwise it starts the sibling `agentos server` hidden in
// the background) and then shows the web console in a native application
// window: WebView2 on Windows (cgo-free, github.com/jchv/go-webview2), or a
// Chromium/Edge `--app=` window elsewhere. See README.md in this directory.
//
// Owned by module M6.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Port        int
	DataDir     string // passed to the server only when set
	ServerURL   string // connect to an existing (possibly remote) server, never spawn
	AgentOSPath string // path of the agentos binary (default: sibling)
	ServiceName string // Windows service to try before spawning a child
	KeepServer  bool   // leave a spawned server running after the window closes
	Browser     bool   // force the browser --app fallback
	Debug       bool   // WebView2 devtools + context menu
}

func parseFlags() config {
	c := config{}
	port, _ := strconv.Atoi(envOr("AGENTOS_PORT", "7800"))
	flag.IntVar(&c.Port, "port", port, "port of the local AgentOS server")
	flag.StringVar(&c.DataDir, "data", os.Getenv("AGENTOS_DATA"), "data directory for a spawned server (default: agentos default)")
	flag.StringVar(&c.ServerURL, "server", os.Getenv("AGENTOS_SERVER_URL"), "open this server URL instead of the local one (no spawn)")
	flag.StringVar(&c.AgentOSPath, "agentos", "", "path to the agentos binary (default: next to this executable)")
	flag.StringVar(&c.ServiceName, "service", "AgentOS", "Windows service name to start if installed but stopped")
	flag.BoolVar(&c.KeepServer, "keep-server", false, "keep a server started by this app running after the window closes")
	flag.BoolVar(&c.Browser, "browser", false, "use a Chromium/Edge --app window instead of the embedded WebView2")
	flag.BoolVar(&c.Debug, "debug", false, "enable WebView2 developer tools")
	flag.Parse()
	return c
}

func main() {
	cfg := parseFlags()
	setupLogging()
	if !acquireSingleInstance() {
		log.Printf("another instance is running; focusing it")
		focusExistingInstance()
		return
	}
	base := cfg.ServerURL
	if base == "" {
		base = "http://127.0.0.1:" + strconv.Itoa(cfg.Port)
	}
	base = strings.TrimRight(base, "/")

	srv, err := ensureServer(cfg, base)
	if err != nil {
		fatalDialog("AgentOS", "无法启动 AgentOS 服务 / Cannot start the AgentOS server:\n\n"+err.Error())
		os.Exit(1)
	}
	url := base + "/?app=1#/"
	log.Printf("opening %s", url)
	openWindow(cfg, url) // blocks until the window is closed
	if srv != nil && !cfg.KeepServer && !trayRunning() {
		log.Printf("stopping server started by the desktop app (pid %d)", srv.Process.Pid)
		stopServer(srv)
	}
}

// ensureServer returns the spawned child (nil when a server was already up).
func ensureServer(cfg config, base string) (*exec.Cmd, error) {
	if healthy(base, 400*time.Millisecond) {
		log.Printf("server already running at %s", base)
		return nil, nil
	}
	if cfg.ServerURL != "" {
		return nil, fmt.Errorf("server %s is not reachable", base)
	}
	if startService(cfg.ServiceName) {
		log.Printf("started Windows service %q", cfg.ServiceName)
		if waitHealthy(base, 20*time.Second) {
			return nil, nil
		}
		log.Printf("service did not become healthy; spawning a server")
	}
	bin, err := findAgentOS(cfg.AgentOSPath)
	if err != nil {
		return nil, err
	}
	args := []string{"server", "--listen", ":" + strconv.Itoa(cfg.Port)}
	if cfg.DataDir != "" {
		args = append(args, "--data", cfg.DataDir)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Dir(bin)
	hideChildWindow(cmd)
	if lf, err := os.OpenFile(filepath.Join(appDir(), "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	log.Printf("starting %s %s", bin, strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("agentos server exited early (%v); see %s", err, filepath.Join(appDir(), "server.log"))
		case <-time.After(100 * time.Millisecond):
		}
		if healthy(base, 200*time.Millisecond) {
			return cmd, nil
		}
	}
	_ = cmd.Process.Kill()
	return nil, errors.New("timed out waiting for the server to start")
}

func healthy(base string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode == 200
}

func waitHealthy(base string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if healthy(base, 500*time.Millisecond) {
			return true
		}
	}
	return false
}

// findAgentOS locates the agentos CLI/server binary: --agentos, then
// <dir>\bin\agentos-server.exe (installer layout), legacy bin\agentos.exe, <dir>\agentos.exe, the
// agentos-<os>-<arch> dist name, then PATH. The desktop app itself
// (AgentOS.exe — the same name on case-insensitive Windows) is never chosen.
func findAgentOS(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	exe, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	self, _ := os.Stat(exe)
	isSelf := func(p string, st os.FileInfo) bool {
		if self != nil && os.SameFile(self, st) {
			return true
		}
		if abs, err := filepath.Abs(p); err == nil {
			return strings.EqualFold(filepath.Clean(abs), filepath.Clean(exe))
		}
		return false
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	dir := filepath.Dir(exe)
	cands := []string{
		filepath.Join(dir, "bin", "agentos-server"+ext),
		filepath.Join(dir, "bin", "agentos"+ext), // pre-0.1.11 portable/installer name
		filepath.Join(dir, "agentos-server"+ext),
		filepath.Join(dir, "agentos"+ext),
		filepath.Join(dir, "agentos-"+runtime.GOOS+"-"+runtime.GOARCH+ext),
		filepath.Join(dir, "bin", "agentos-"+runtime.GOOS+"-"+runtime.GOARCH+ext),
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && !isSelf(p, st) {
			return p, nil
		}
	}
	if p, err := exec.LookPath("agentos"); err == nil {
		if st, err := os.Stat(p); err == nil && !isSelf(p, st) {
			return p, nil
		}
	}
	return "", fmt.Errorf("agentos executable not found in %s, %s or PATH", dir, filepath.Join(dir, "bin"))
}

// appDir is the per-user state directory (window placement, logs, webview profile).
func appDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	d = filepath.Join(d, "AgentOS")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func setupLogging() {
	if f, err := os.OpenFile(filepath.Join(appDir(), "desktop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
	log.SetPrefix("agentos-desktop: ")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// browserCandidates lists Chromium-family browsers that support --app.
func browserCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			if b := os.Getenv(env); b != "" {
				out = append(out, filepath.Join(b, `Microsoft\Edge\Application\msedge.exe`),
					filepath.Join(b, `Google\Chrome\Application\chrome.exe`))
			}
		}
		return out
	case "darwin":
		return []string{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium"}
	default:
		return []string{"microsoft-edge", "microsoft-edge-stable", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	}
}

// openAppBrowser opens url in a Chromium --app window with a dedicated
// profile (so the process lives exactly as long as the window) and waits.
// Falls back to the default browser (xdg-open/open/start) without waiting.
func openAppBrowser(url string, w, h int) error {
	profile := filepath.Join(appDir(), "browser-profile")
	for _, b := range browserCandidates() {
		p := b
		if !filepath.IsAbs(p) {
			lp, err := exec.LookPath(p)
			if err != nil {
				continue
			}
			p = lp
		} else if _, err := os.Stat(p); err != nil {
			continue
		}
		cmd := exec.Command(p, "--app="+url, fmt.Sprintf("--window-size=%d,%d", w, h), "--user-data-dir="+profile,
			"--no-first-run", "--no-default-browser-check", "--class=AgentOS")
		log.Printf("launching %s --app", p)
		if err := cmd.Start(); err != nil {
			continue
		}
		return cmd.Wait()
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	log.Printf("no --app capable browser found; opening default browser")
	if err := cmd.Run(); err != nil {
		return err
	}
	waitForInterrupt()
	return nil
}
