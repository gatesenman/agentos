//go:build !windows

package hyperv

import (
	"context"
	"fmt"
	"io"

	"github.com/agentos/agentos/internal/env"
)

func detectQEMUWSLPlatform(ctx context.Context) DetectResult {
	return DetectResult{Available: false, Reason: "QEMU/WSL 回退仅在 Windows 宿主可用"}
}

func (provider) createQEMUWSL(ctx context.Context, rec *env.Record, c *Config, save func(map[string]any)) error {
	return fmt.Errorf("%w: QEMU/WSL 仅 Windows", errNotWindows)
}

func (e *hypervEnv) qemuStatus(ctx context.Context) (env.Status, error) {
	return env.Status{State: env.StateUnknown, Message: "QEMU/WSL 仅 Windows"}, nil
}
func (e *hypervEnv) qemuStart(ctx context.Context) error   { return errNotWindows }
func (e *hypervEnv) qemuStop(ctx context.Context) error    { return errNotWindows }
func (e *hypervEnv) qemuDelete(ctx context.Context) error  { return errNotWindows }
func (e *hypervEnv) qemuDesktop(ctx context.Context) (io.ReadWriteCloser, error) {
	return nil, errNotWindows
}
func (e *hypervEnv) qemuScreenshot(ctx context.Context, width int) ([]byte, error) {
	return nil, errNotWindows
}
func (e *hypervEnv) qemuRunning() bool { return false }
