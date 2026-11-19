//go:build linux

package agentoslocal

import (
	"os/exec"
	"syscall"
)

// Children receive SIGTERM if the server dies, so a restarted server never
// races an orphaned instance for the same node id.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
