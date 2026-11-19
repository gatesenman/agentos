// Command agentos is the single binary for AgentOS.
//
//	agentos server [--listen :7800] [--data DIR] [--admin-password PW] [--with-node]
//	agentos node   --server http://host:7800 --token TOKEN [--name NAME] [--data DIR]
//	agentos service install|uninstall|start|stop   (Windows service helper, module M8)
//	agentos version
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/agentos/agentos/internal/auth"
	_ "github.com/agentos/agentos/internal/env/providers/all" // environment providers + Agent OS bootstrapper
	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
	"github.com/agentos/agentos/internal/version"
	"github.com/agentos/agentos/web"
)

const usage = `AgentOS - 本地 AI Agent 可视化管理工具

用法:
  agentos server   启动管理控制台 (Web UI + API)
  agentos node     启动 Agent 宿主节点并注册到控制台
  agentos service  Windows 服务管理 (install|uninstall|start|stop)
  agentos admin    管理工具 (reset-password 等)
  agentos version  显示版本

运行 "agentos <命令> -h" 查看参数。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(os.Args[2:])
	case "node":
		err = runNode(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "admin":
		err = runAdmin(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("agentos %s (commit %s, built %s) %s/%s\n", version.Version, version.Commit, version.Date, runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("agentos: %v", err)
	}
}

// DefaultDataDir: %ProgramData%\AgentOS on Windows, ~/.agentos elsewhere.
func DefaultDataDir() string {
	if v := os.Getenv("AGENTOS_DATA"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "AgentOS")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentos"
	}
	return filepath.Join(home, ".agentos")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", envOr("AGENTOS_LISTEN", ":7800"), "HTTP listen address")
	dataDir := fs.String("data", DefaultDataDir(), "data directory")
	adminPW := fs.String("admin-password", os.Getenv("AGENTOS_ADMIN_PASSWORD"), "initial admin password (first run only; random if empty)")
	withNode := fs.Bool("with-node", envOr("AGENTOS_WITH_NODE", "true") == "true", "also run a local node in-process (all-in-one mode)")
	nodeName := fs.String("node-name", "", "name of the embedded local node (default hostname)")
	fs.StringVar(&embeddedVNC, "vnc", embeddedVNC, "VNC server address for the embedded local node (env AGENTOS_VNC)")
	fs.StringVar(&distDirFlag, "dist", distDirFlag, "directory with agentos-<os>-<arch>[.exe] binaries served at /download (env AGENTOS_DIST, default <exe dir>/dist)")
	fs.Parse(args)
	return serve(*listen, *dataDir, *adminPW, *withNode, *nodeName)
}

// embeddedVNC is the VNC address used by the embedded node (flag --vnc /
// AGENTOS_VNC; empty = node default 127.0.0.1:5900). Kept out of the serveCtx
// signature so the Windows service entry point stays unchanged.
var embeddedVNC = os.Getenv("AGENTOS_VNC")

// serve is shared by the CLI and the Windows service entry point.
func serve(listen, dataDir, adminPW string, withNode bool, nodeName string) error {
	ctx, cancel := signalContext()
	defer cancel()
	return serveCtx(ctx, listen, dataDir, adminPW, withNode, nodeName)
}

func serveCtx(ctx context.Context, listen, dataDir, adminPW string, withNode bool, nodeName string) error {
	sdir := filepath.Join(dataDir, "server")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		return err
	}
	st, err := store.OpenSQLite(filepath.Join(sdir, "agentos.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	am, boot, err := auth.New(ctx, st, adminPW)
	if err != nil {
		return err
	}
	if boot.GeneratedPassword != "" {
		pwFile := filepath.Join(sdir, "initial-admin-password.txt")
		_ = writeInitialAdminPasswordFile(sdir, boot.GeneratedPassword)
		log.Printf("============================================================")
		log.Printf(" 初始管理员密码 / initial admin password: %s", boot.GeneratedPassword)
		log.Printf(" (saved to %s)", pwFile)
		log.Printf("============================================================")
	}
	log.Printf("节点令牌 / node token: %s", boot.NodeToken)
	srv := server.New(server.Config{Listen: listen, WebFS: web.FS(), DataDir: sdir, DistDir: distDir()}, st, am)
	defer srv.Close()
	log.Printf("AgentOS %s 控制台 / console: http://%s", version.Version, displayAddr(listen))

	nodeDone := make(chan struct{})
	var embedded *node.Node
	if withNode {
		embedded, err = node.New(node.Config{ServerURL: "http://" + loopbackAddr(listen), Token: boot.NodeToken,
			TokenFunc: func() string { t, _ := am.NodeToken(context.Background()); return t },
			Name:      nodeName, DataDir: filepath.Join(dataDir, "node"), VNCAddr: embeddedVNC})
		if err != nil {
			log.Printf("embedded node: %v", err)
		}
	}
	eo := server.EnvOptions{LocalServerURL: "http://" + loopbackAddr(listen)}
	if embedded != nil {
		eo.BuiltinNodeID = embedded.ID()
	}
	if err := srv.EnableEnvs(ctx, eo); err != nil {
		log.Printf("environments: %v", err)
	}
	if embedded != nil {
		go func() {
			defer close(nodeDone)
			time.Sleep(300 * time.Millisecond)
			embedded.Run(ctx)
		}()
	} else {
		close(nodeDone)
	}
	err = srv.Run(ctx)
	if ctx.Err() == nil {
		return err // failed to serve (e.g. port in use)
	}
	select { // let the embedded node stop its agents before exiting
	case <-nodeDone:
	case <-time.After(15 * time.Second):
	}
	return err
}

// distDir is where agentos-<os>-<arch>[.exe] binaries for /download live
// (flag --dist / AGENTOS_DIST, default <exe dir>/dist).
var distDirFlag = os.Getenv("AGENTOS_DIST")

func distDir() string {
	if distDirFlag != "" {
		return distDirFlag
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "dist")
	}
	return ""
}

func loopbackAddr(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	return "127.0.0.1:" + port
}

func displayAddr(listen string) string {
	if strings.HasPrefix(listen, ":") {
		return "localhost" + listen
	}
	return listen
}

func runNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	serverURL := fs.String("server", os.Getenv("AGENTOS_SERVER"), "server URL, e.g. http://192.168.1.10:7800")
	token := fs.String("token", os.Getenv("AGENTOS_TOKEN"), "node token (shown in server log / settings page)")
	name := fs.String("name", os.Getenv("AGENTOS_NODE_NAME"), "node display name (default hostname)")
	dataDir := fs.String("data", filepath.Join(DefaultDataDir(), "node"), "node data directory")
	vncAddr := fs.String("vnc", envOr("AGENTOS_VNC", "127.0.0.1:5900"), "local VNC server address")
	desk := fs.String("desktop", os.Getenv("AGENTOS_DESKTOP"), "desktop mode: \"\" (use --vnc / the current $DISPLAY) or \"virtual\" (Linux: own Xvfb + window manager + x11vnc on a free display/port)")
	installSvc := fs.Bool("install-service", os.Getenv("AGENTOS_INSTALL_SERVICE") == "1", "save settings to <data>/node.json and install an auto-start service, then exit")
	fs.Parse(args)
	if *installSvc {
		return installNodeService(*dataDir, NodeFile{Server: *serverURL, Token: *token, Name: *name, VNC: *vncAddr, Desktop: *desk})
	}
	if nf, ok := loadNodeFile(*dataDir); ok { // settings saved by --install-service
		if *serverURL == "" {
			*serverURL = nf.Server
		}
		if *token == "" {
			*token = nf.Token
		}
		if *name == "" {
			*name = nf.Name
		}
		if nf.VNC != "" && os.Getenv("AGENTOS_VNC") == "" && !flagSet(fs, "vnc") {
			*vncAddr = nf.VNC
		}
		if *desk == "" {
			*desk = nf.Desktop
		}
	}
	n, err := node.New(node.Config{ServerURL: *serverURL, Token: *token, Name: *name, DataDir: *dataDir, VNCAddr: *vncAddr, Desktop: *desk})
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	log.Printf("AgentOS node %s (id %s) -> %s", version.Version, n.ID(), *serverURL)
	return n.Run(ctx)
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
