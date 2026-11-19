//go:build !windows

package hyperv

import "context"

func platformRunner() Runner { return stubRunner{} }

type stubRunner struct{}

func (stubRunner) RunPS(ctx context.Context, script string) (string, error) {
	return "", errNotWindows
}

func detectPlatform(ctx context.Context) DetectResult {
	return DetectResult{
		Available: false,
		Reason:    "Hyper-V 仅在 Windows 上可用；当前系统请使用 Docker 容器或远程 Agent OS",
	}
}
