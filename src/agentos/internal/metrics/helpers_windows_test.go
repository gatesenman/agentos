//go:build windows

package metrics

import "os/exec"

func setPgid(cmd *exec.Cmd) {}
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
