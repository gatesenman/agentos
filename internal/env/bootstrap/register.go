package bootstrap

// Glue to the server: adapts InstallAgentOS to env.Installer and registers it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentos/agentos/internal/env"
)

func init() { env.RegisterInstaller(envInstaller{}) }

type envInstaller struct{}

func (envInstaller) Install(ctx context.Context, e env.Env, rec env.Record, req env.InstallRequest) error {
	logf := func(format string, args ...any) {
		if req.Log != nil {
			req.Log("info", fmt.Sprintf(format, args...))
		}
	}
	serverURL := req.ServerURL
	if m, ok := e.(env.ServerURLMapper); ok {
		if u := m.MapServerURL(ctx, serverURL); u != "" && u != serverURL {
			logf("管理端地址（环境内）/ server URL inside the environment: %s", u)
			serverURL = u
		}
	}
	goos, goarch, err := Detect(ctx, e, "")
	if err != nil {
		return err
	}
	opts := Options{ServerURL: serverURL, JoinToken: req.Token, NodeName: req.Name, NodeID: req.NodeID,
		OSHint: goos, Logf: logf}
	if req.BinaryPath != nil {
		if p, ok := req.BinaryPath(goos, goarch); ok {
			name := BinaryName(goos, goarch)
			if filepath.Base(p) == name {
				opts.DistDir = filepath.Dir(p)
			} else { // e.g. the running executable: expose it under the expected name
				tmp, err := os.MkdirTemp("", "agentos-dist-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				if err := os.Symlink(p, filepath.Join(tmp, name)); err != nil {
					return err
				}
				opts.DistDir = tmp
			}
		}
	}
	if opts.DistDir == "" && req.BinaryURL != nil {
		// let the server fetch from its own /download (other dist layouts)
		u := req.BinaryURL(goos, goarch)
		opts.DownloadBase = u[:len(u)-len(BinaryName(goos, goarch))]
	}
	res, err := InstallAgentOS(ctx, e, opts)
	if err != nil {
		return err
	}
	logf("节点进程已启动 / node started: pid %d, %s (%s)", res.PID, res.BinaryPath, res.Method)
	return nil
}
