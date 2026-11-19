package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/env"
)

type execSession struct {
	c      *client
	id     string
	conn   net.Conn
	stdin  *stdinWriter
	stdout *bufPipe
	stderr *bufPipe
	done   chan struct{} // closed when the output stream ended
	once   sync.Once
}

type closeWriter interface{ CloseWrite() error }

type stdinWriter struct {
	conn net.Conn
	once sync.Once
}

func (w *stdinWriter) Write(p []byte) (int, error) { return w.conn.Write(p) }
func (w *stdinWriter) Close() error {
	var err error
	w.once.Do(func() {
		if cw, ok := w.conn.(closeWriter); ok {
			err = cw.CloseWrite()
		}
	})
	return err
}

func (c *client) exec(ctx context.Context, container string, req env.ExecRequest, defaultCmd []string) (*execSession, error) {
	cmd := req.Cmd
	if len(cmd) == 0 {
		cmd = defaultCmd
	}
	cfg := map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true,
		"Tty": req.TTY, "Cmd": cmd,
	}
	if len(req.Env) > 0 {
		var ev []string
		for k, v := range req.Env {
			ev = append(ev, k+"="+v)
		}
		cfg["Env"] = ev
	}
	if req.WorkDir != "" {
		cfg["WorkingDir"] = req.WorkDir
	}
	if req.TTY && req.Cols > 0 && req.Rows > 0 {
		cfg["ConsoleSize"] = []int{req.Rows, req.Cols}
	}
	var created struct{ Id string }
	if err := c.json(ctx, "POST", "/containers/"+url.PathEscape(container)+"/exec", nil, cfg, &created); err != nil {
		return nil, err
	}
	start := map[string]any{"Detach": false, "Tty": req.TTY}
	if req.TTY && req.Cols > 0 && req.Rows > 0 {
		start["ConsoleSize"] = []int{req.Rows, req.Cols}
	}
	conn, br, err := c.hijack(ctx, "/exec/"+created.Id+"/start", start)
	if err != nil {
		return nil, err
	}
	s := &execSession{c: c, id: created.Id, conn: conn, stdin: &stdinWriter{conn: conn}, done: make(chan struct{})}
	outW, errW := newBufPipe(8<<20), newBufPipe(8<<20)
	s.stdout, s.stderr = outW, errW
	go func() {
		defer close(s.done)
		var err error
		if req.TTY {
			_, err = io.Copy(outW, br)
		} else {
			err = demux(br, outW, errW)
		}
		if err != nil && isClosedErr(err) {
			err = nil
		}
		outW.CloseWithError(err)
		errW.CloseWithError(err)
	}()
	if req.TTY && req.Cols > 0 && req.Rows > 0 {
		s.Resize(req.Cols, req.Rows) // older daemons ignore ConsoleSize
	}
	return s, nil
}

func isClosedErr(err error) bool {
	return err == io.EOF || strings.Contains(err.Error(), "use of closed network connection")
}

// demux splits Docker's multiplexed stream: 8-byte header [stream,0,0,0,len(be32)] + payload.
// Each frame is fully drained from r; if a consumer stops reading, output for it is discarded.
func demux(r *bufio.Reader, stdout, stderr io.Writer) error {
	var hdr [8]byte
	outOK, errOK := true, true
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		var w io.Writer = io.Discard
		switch hdr[0] {
		case 0, 1:
			if outOK {
				w = stdout
			}
		case 2:
			if errOK {
				w = stderr
			}
		}
		lr := &io.LimitedReader{R: r, N: n}
		_, err := io.Copy(w, lr)
		if err == io.ErrClosedPipe { // consumer closed this stream: drop it, keep the other
			if hdr[0] == 2 {
				errOK = false
			} else {
				outOK = false
			}
			_, err = io.Copy(io.Discard, lr)
		}
		if err == nil && lr.N > 0 {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
	}
}

func (s *execSession) Stdin() io.WriteCloser { return s.stdin }
func (s *execSession) Stdout() io.Reader     { return s.stdout }
func (s *execSession) Stderr() io.Reader     { return s.stderr }

func (s *execSession) Resize(cols, rows int) error {
	q := url.Values{"w": {strconv.Itoa(cols)}, "h": {strconv.Itoa(rows)}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.c.json(ctx, "POST", "/exec/"+s.id+"/resize", q, nil, nil)
}

// Wait blocks until the process output ends, then returns the exit code.
func (s *execSession) Wait() (int, error) {
	<-s.done
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		var st struct {
			Running  bool
			ExitCode int
		}
		if err := s.c.json(ctx, "GET", "/exec/"+s.id+"/json", nil, nil, &st); err != nil {
			return -1, err
		}
		if !st.Running {
			return st.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, fmt.Errorf("docker: exec %s still running", s.id)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *execSession) Close() error {
	s.once.Do(func() {
		s.conn.Close()
		s.stdout.Close()
		s.stderr.Close()
	})
	return nil
}
