//go:build !windows

package supervisor

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// sysProc on Unix: each agent leads its own process group so the whole tree
// can be signalled (SIGTERM for graceful stop, SIGKILL to finish it off).
type sysProc struct {
	pgid     int
	warnings []string
}

func prepare(cmd *exec.Cmd, lim limits) (*sysProc, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	sp := &sysProc{}
	if lim.any() {
		sp.warnings = append(sp.warnings, fmt.Sprintf("此平台暂不支持资源限制，已忽略 / resource limits (%s, %s) not supported on this platform yet, ignored", EnvLimitMemMB, EnvLimitCPUPct))
	}
	return sp, nil
}

func (sp *sysProc) started(cmd *exec.Cmd) error {
	sp.pgid = cmd.Process.Pid
	return nil
}

func (sp *sysProc) release() {}

func (sp *sysProc) signal(sig syscall.Signal) error {
	if sp.pgid <= 0 {
		return errors.New("no process group")
	}
	err := syscall.Kill(-sp.pgid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// terminate sends SIGTERM to the whole process group.
func (sp *sysProc) terminate(cmd *exec.Cmd) error { return sp.signal(syscall.SIGTERM) }

// kill sends SIGKILL to the whole process group.
func (sp *sysProc) kill(cmd *exec.Cmd) {
	if sp.signal(syscall.SIGKILL) != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// cleanup runs after the leader was reaped: kill any remaining group members
// (the group id cannot be reused while members exist).
func (sp *sysProc) cleanup(cmd *exec.Cmd) { sp.signal(syscall.SIGKILL) }
