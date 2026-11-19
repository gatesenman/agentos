// Package agentosnode implements env.Env on top of an Agent OS node connected
// to this server: exec, files, desktop and metrics are carried over the node
// control channel and tunnels (docs/protocol.md §4, §6). It is shared by the
// agentos-local and agentos-remote providers, and by the server for any
// environment that has a bound node (rec.NodeID).
package agentosnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/env"
	"github.com/gorilla/websocket"
)

// ErrNodeOffline is returned when the node bound to an environment is not connected.
var ErrNodeOffline = errors.New("agent os node is offline")

// Gateway is implemented by the server (hub) and installed with SetGateway.
type Gateway interface {
	NodeOnline(nodeID string) bool
	NodeCapabilities(nodeID string) []string
	// OpenTunnel opens a message-framed tunnel to the node (api.Tunnel* kinds).
	OpenTunnel(ctx context.Context, nodeID, kind string, params map[string]string) (*websocket.Conn, error)
	ListDir(ctx context.Context, nodeID, path string) (api.FsListResponse, error)
	// HostStats returns the latest host sample reported by the node.
	HostStats(nodeID string) (api.HostStats, bool)
}

var (
	gwMu sync.RWMutex
	gw   Gateway
)

func SetGateway(g Gateway) { gwMu.Lock(); gw = g; gwMu.Unlock() }

func gateway() (Gateway, error) {
	gwMu.RLock()
	defer gwMu.RUnlock()
	if gw == nil {
		return nil, errors.New("agentosnode: no gateway configured")
	}
	return gw, nil
}

// Online reports whether nodeID is connected (false without a gateway).
func Online(nodeID string) bool {
	g, err := gateway()
	return err == nil && nodeID != "" && g.NodeOnline(nodeID)
}

// HasCap reports whether the connected node advertises capability c.
func HasCap(nodeID, c string) bool {
	g, err := gateway()
	if err != nil || !g.NodeOnline(nodeID) {
		return false
	}
	for _, x := range g.NodeCapabilities(nodeID) {
		if x == c {
			return true
		}
	}
	return false
}

// Env is an env.Env backed by a node. Lifecycle funcs are optional
// (nil => env.ErrUnsupported); StatusFn nil => running iff node online.
type Env struct {
	NodeID   func() string
	StatusFn func(ctx context.Context) (env.Status, error)
	StartFn  func(ctx context.Context) error
	StopFn   func(ctx context.Context) error
	DeleteFn func(ctx context.Context) error
	CloseFn  func() error
}

var _ env.Env = (*Env)(nil)

func (e *Env) node() (Gateway, string, error) {
	g, err := gateway()
	if err != nil {
		return nil, "", err
	}
	id := ""
	if e.NodeID != nil {
		id = e.NodeID()
	}
	if id == "" || !g.NodeOnline(id) {
		return nil, "", ErrNodeOffline
	}
	return g, id, nil
}

func (e *Env) Status(ctx context.Context) (env.Status, error) {
	if e.StatusFn != nil {
		return e.StatusFn(ctx)
	}
	if _, _, err := e.node(); err != nil {
		return env.Status{State: env.StateUnknown, Message: "离线 / offline"}, nil
	}
	return env.Status{State: env.StateRunning}, nil
}

func call(f func(context.Context) error, ctx context.Context) error {
	if f == nil {
		return env.ErrUnsupported
	}
	return f(ctx)
}

func (e *Env) Start(ctx context.Context) error  { return call(e.StartFn, ctx) }
func (e *Env) Stop(ctx context.Context) error   { return call(e.StopFn, ctx) }
func (e *Env) Delete(ctx context.Context) error { return call(e.DeleteFn, ctx) }
func (e *Env) Close() error {
	if e.CloseFn != nil {
		return e.CloseFn()
	}
	return nil
}

func (e *Env) tunnel(ctx context.Context, kind string, params map[string]string) (*websocket.Conn, error) {
	g, id, err := e.node()
	if err != nil {
		return nil, err
	}
	return g.OpenTunnel(ctx, id, kind, params)
}

// ---- exec ----

func (e *Env) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	params := map[string]string{"cwd": req.WorkDir}
	if len(req.Cmd) > 0 {
		b, _ := json.Marshal(req.Cmd)
		params["cmd"] = string(b)
	}
	if len(req.Env) > 0 {
		b, _ := json.Marshal(req.Env)
		params["env"] = string(b)
	}
	if req.TTY {
		params["tty"] = "1"
	}
	if req.Cols > 0 {
		params["cols"] = strconv.Itoa(req.Cols)
	}
	if req.Rows > 0 {
		params["rows"] = strconv.Itoa(req.Rows)
	}
	c, err := e.tunnel(ctx, api.TunnelExec, params)
	if err != nil {
		return nil, err
	}
	return newExecSession(c), nil
}

type execSession struct {
	c      *websocket.Conn
	wmu    sync.Mutex
	outR   *io.PipeReader
	outW   *io.PipeWriter
	done   chan struct{}
	code   int
	err    error
	closer sync.Once
}

func newExecSession(c *websocket.Conn) *execSession {
	s := &execSession{c: c, done: make(chan struct{}), code: -1}
	s.outR, s.outW = io.Pipe()
	go s.read()
	return s
}

func (s *execSession) read() {
	defer close(s.done)
	for {
		t, data, err := s.c.ReadMessage()
		if err != nil {
			if s.err == nil && s.code < 0 {
				s.err = fmt.Errorf("exec stream closed: %w", err)
			}
			s.outW.Close()
			return
		}
		if t == websocket.BinaryMessage {
			if _, err := s.outW.Write(data); err != nil {
				// reader gone: keep draining so exit is still observed
				continue
			}
			continue
		}
		var m struct {
			Type    string `json:"type"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "exit":
			s.code, s.err = m.Code, nil
			s.outW.Close()
			s.c.Close()
			return
		case "error":
			s.err = errors.New(m.Message)
		}
	}
}

func (s *execSession) send(t int, b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return s.c.WriteMessage(t, b)
}

type stdinWriter struct{ s *execSession }

func (w stdinWriter) Write(p []byte) (int, error) {
	if err := w.s.send(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w stdinWriter) Close() error { return w.s.send(websocket.TextMessage, []byte(`{"type":"eof"}`)) }

func (s *execSession) Stdin() io.WriteCloser { return stdinWriter{s} }
func (s *execSession) Stdout() io.Reader     { return s.outR }
func (s *execSession) Stderr() io.Reader     { return eofReader{} }
func (s *execSession) Resize(cols, rows int) error {
	b, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	return s.send(websocket.TextMessage, b)
}
func (s *execSession) Wait() (int, error) {
	<-s.done
	return s.code, s.err
}
func (s *execSession) Close() error {
	s.closer.Do(func() { s.c.Close() })
	return nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// ---- files ----

func (e *Env) PutFile(ctx context.Context, path string, r io.Reader, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	c, err := e.tunnel(ctx, api.TunnelFilePut, map[string]string{"path": path, "mode": strconv.FormatUint(uint64(mode), 8)})
	if err != nil {
		return err
	}
	defer c.Close()
	buf := make([]byte, 64*1024)
	for {
		k, rerr := r.Read(buf)
		if k > 0 {
			c.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if err := c.WriteMessage(websocket.BinaryMessage, buf[:k]); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"eof"}`)); err != nil {
		return err
	}
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return err
		}
		var m struct{ Type, Message string }
		json.Unmarshal(data, &m)
		switch m.Type {
		case "ok":
			return nil
		case "error":
			return errors.New(m.Message)
		}
	}
}

func (e *Env) GetFile(ctx context.Context, path string) (io.ReadCloser, error) {
	c, err := e.tunnel(ctx, api.TunnelFileGet, map[string]string{"path": path})
	if err != nil {
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	_, data, err := c.ReadMessage() // info or error
	if err != nil {
		c.Close()
		return nil, err
	}
	var m struct{ Type, Message string }
	json.Unmarshal(data, &m)
	if m.Type != "info" {
		c.Close()
		if m.Type == "error" {
			return nil, &FileError{Msg: m.Message}
		}
		return nil, fmt.Errorf("unexpected message %q", m.Type)
	}
	pr, pw := io.Pipe()
	go func() {
		defer c.Close()
		for {
			c.SetReadDeadline(time.Now().Add(60 * time.Second))
			t, data, err := c.ReadMessage()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if t == websocket.BinaryMessage {
				if _, err := pw.Write(data); err != nil {
					return
				}
				continue
			}
			var m struct{ Type, Message string }
			json.Unmarshal(data, &m)
			switch m.Type {
			case "eof":
				pw.Close()
				return
			case "error":
				pw.CloseWithError(errors.New(m.Message))
				return
			}
		}
	}()
	return pr, nil
}

// FileError is a file operation error reported by the node (e.g. not found).
type FileError struct{ Msg string }

func (e *FileError) Error() string { return e.Msg }

func (e *Env) ListDir(ctx context.Context, path string) ([]env.FileInfo, error) {
	g, id, err := e.node()
	if err != nil {
		return nil, err
	}
	res, err := g.ListDir(ctx, id, path)
	if err != nil {
		return nil, &FileError{Msg: err.Error()}
	}
	out := make([]env.FileInfo, 0, len(res.Entries))
	for _, f := range res.Entries {
		out = append(out, env.FileInfo{Name: f.Name, Size: f.Size, Dir: f.Dir, Mode: f.Mode, ModTime: time.UnixMilli(f.ModTime).UTC()})
	}
	return out, nil
}

// ---- desktop / metrics ----

// Desktop returns the tunnel as a byte stream (binary frames). The server's
// /envs/{id}/desktop handler relays node tunnels frame-by-frame instead, so
// the screen protocol's text frames are preserved; this is for other callers.
func (e *Env) Desktop(ctx context.Context, kind string) (io.ReadWriteCloser, error) {
	c, err := e.tunnel(ctx, kind, nil)
	if err != nil {
		return nil, err
	}
	return &wsStream{c: c}, nil
}

type wsStream struct {
	c   *websocket.Conn
	buf []byte
	wmu sync.Mutex
}

func (w *wsStream) Read(p []byte) (int, error) {
	for len(w.buf) == 0 {
		_, data, err := w.c.ReadMessage()
		if err != nil {
			return 0, err
		}
		w.buf = data
	}
	k := copy(p, w.buf)
	w.buf = w.buf[k:]
	return k, nil
}

func (w *wsStream) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := w.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsStream) Close() error { return w.c.Close() }

func (e *Env) Metrics(ctx context.Context) (env.Metrics, error) {
	g, id, err := e.node()
	if err != nil {
		return env.Metrics{}, err
	}
	h, ok := g.HostStats(id)
	if !ok {
		return env.Metrics{}, errors.New("no metrics reported yet")
	}
	return env.Metrics{CPUPercent: h.CPUPercent, MemUsed: h.MemUsed, MemTotal: h.MemTotal, DiskUsed: h.DiskUsed, DiskTotal: h.DiskTotal}, nil
}
