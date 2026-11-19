//go:build !windows

package node

import (
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

type ptyProc struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func startExec(cmd *exec.Cmd, tty bool, cols, rows int) (execProc, error) {
	if tty {
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) // setsid: own process group
		if err != nil {
			return nil, err
		}
		return &ptyProc{cmd: cmd, ptmx: f}, nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return startPipes(cmd)
}

func (p *ptyProc) Stdin() io.WriteCloser { return p.ptmx }
func (p *ptyProc) Output() io.Reader     { return p.ptmx }
func (p *ptyProc) Resize(cols, rows int) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
func (p *ptyProc) Kill() { killTree(p.cmd) }
func (p *ptyProc) Wait() (int, error) {
	err := p.cmd.Wait()
	code, err := exitCode(p.cmd, err)
	p.ptmx.Close()
	return code, err
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Process.Kill()
	}
}
