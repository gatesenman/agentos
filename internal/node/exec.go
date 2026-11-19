package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func deadline() time.Time { return time.Now().Add(5 * time.Second) }

// DefaultShell is the command used when an exec request has no cmd.
func DefaultShell(goos string) []string {
	if goos == "windows" {
		return []string{"powershell.exe", "-NoLogo"}
	}
	return []string{"/bin/sh", "-l"}
}

// execProc abstracts a pty- or pipe-backed process.
type execProc interface {
	Stdin() io.WriteCloser
	Output() io.Reader // merged stdout+stderr
	Resize(cols, rows int) error
	Wait() (int, error)
	Kill()
}

func (n *Node) serveExec(ctx context.Context, c *websocket.Conn, params map[string]string) error {
	var argv []string
	if s := params["cmd"]; s != "" {
		if err := json.Unmarshal([]byte(s), &argv); err != nil {
			return sendErr(c, fmt.Errorf("invalid cmd: %w", err))
		}
	}
	if len(argv) == 0 {
		argv = DefaultShell(runtime.GOOS)
	}
	env := os.Environ()
	if s := params["env"]; s != "" {
		var extra map[string]string
		if err := json.Unmarshal([]byte(s), &extra); err != nil {
			return sendErr(c, fmt.Errorf("invalid env: %w", err))
		}
		for k, v := range extra {
			env = append(env, k+"="+v)
		}
	}
	cwd := n.resolvePath(params["cwd"])
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return sendErr(c, fmt.Errorf("working directory %s not found", cwd))
	}
	cols, _ := strconv.Atoi(params["cols"])
	rows, _ := strconv.Atoi(params["rows"])
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, append(env, "TERM=xterm-256color")
	p, err := startExec(cmd, params["tty"] == "1", cols, rows)
	if err != nil {
		return sendErr(c, err)
	}

	var wmu sync.Mutex
	write := func(t int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		c.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return c.WriteMessage(t, b)
	}
	outDone := make(chan struct{})
	go func() { // output -> ws
		defer close(outDone)
		buf := make([]byte, 32*1024)
		for {
			k, err := p.Output().Read(buf)
			if k > 0 && write(websocket.BinaryMessage, append([]byte(nil), buf[:k]...)) != nil {
				return
			}
			if err != nil {
				return
			}
		}
	}()
	inDone := make(chan struct{})
	go func() { // ws -> stdin / resize
		defer close(inDone)
		for {
			t, data, err := c.ReadMessage()
			if err != nil {
				p.Kill() // client went away
				return
			}
			if t == websocket.BinaryMessage {
				p.Stdin().Write(data)
				continue
			}
			var m struct {
				Type       string
				Cols, Rows int
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "resize":
				p.Resize(m.Cols, m.Rows)
			case "eof":
				p.Stdin().Close()
			}
		}
	}()
	code, werr := p.Wait()
	select { // drain remaining output (pty readers may block: bounded wait)
	case <-outDone:
	case <-time.After(2 * time.Second):
	}
	if werr != nil && code < 0 {
		b, _ := json.Marshal(map[string]string{"type": "error", "message": werr.Error()})
		write(websocket.TextMessage, b)
	}
	b, _ := json.Marshal(map[string]any{"type": "exit", "code": code})
	write(websocket.TextMessage, b)
	wmu.Lock()
	c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline())
	wmu.Unlock()
	select {
	case <-inDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// pipeProc runs without a terminal: stdout and stderr merged into one pipe.
type pipeProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *io.PipeReader
}

func startPipes(cmd *exec.Cmd) (*pipeProc, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &pipeProc{cmd: cmd, stdin: stdin, out: pr}
	return p, nil
}

func (p *pipeProc) Stdin() io.WriteCloser { return p.stdin }
func (p *pipeProc) Output() io.Reader     { return p.out }
func (p *pipeProc) Resize(int, int) error { return nil }
func (p *pipeProc) Kill()                 { killTree(p.cmd) }
func (p *pipeProc) Wait() (int, error) {
	err := p.cmd.Wait()
	p.out.CloseWithError(io.EOF)
	return exitCode(p.cmd, err)
}

func exitCode(cmd *exec.Cmd, err error) (int, error) {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), nil
	}
	if err == nil {
		err = errors.New("process state unavailable")
	}
	return -1, err
}
