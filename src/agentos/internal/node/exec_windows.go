//go:build windows

package node

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Windows: no ConPTY yet (0.1.2 still uses pipes for tty exec).
// ConPTY needs cgo-free bindings to kernel32 CreatePseudoConsole; tracked for 0.2.
// Interactive programs (vim, shells with line editing) may display poorly until then.
func startExec(cmd *exec.Cmd, tty bool, cols, rows int) (execProc, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return startPipes(cmd)
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// taskkill /T terminates the child tree spawned by the shell.
		exec.Command("taskkill", "/T", "/F", "/PID", itoa(cmd.Process.Pid)).Run()
		cmd.Process.Kill()
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
