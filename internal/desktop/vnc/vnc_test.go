package vnc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/desktop"
)

// pipeConn is an in-memory desktop.Conn.
type msg struct {
	t int
	b []byte
}
type pipeConn struct {
	in     chan msg // browser -> provider
	out    chan msg // provider -> browser
	closed chan struct{}
	once   sync.Once
}

func newPipeConn() *pipeConn {
	return &pipeConn{in: make(chan msg, 16), out: make(chan msg, 64), closed: make(chan struct{})}
}
func (c *pipeConn) ReadMessage() (int, []byte, error) {
	select {
	case m := <-c.in:
		return m.t, m.b, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}
func (c *pipeConn) WriteMessage(t int, b []byte) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	c.out <- msg{t, append([]byte(nil), b...)}
	return nil
}
func (c *pipeConn) Close() error { c.once.Do(func() { close(c.closed) }); return nil }

var _ desktop.Conn = (*pipeConn)(nil)

// fakeRFB greets with banner then echoes everything back.
func fakeRFB(t *testing.T, banner string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte(banner))
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func closedPort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

func recv(t *testing.T, c *pipeConn) msg {
	t.Helper()
	select {
	case m := <-c.out:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}
	return msg{}
}

func TestResolveAddr(t *testing.T) {
	p := New("10.0.0.5:5900") // operator-configured non-loopback default
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "10.0.0.5:5900", true},
		{"10.0.0.5:5900", "10.0.0.5:5900", true},
		{"127.0.0.1:5901", "127.0.0.1:5901", true},
		{"127.1.2.3:5901", "127.1.2.3:5901", true},
		{"localhost:5902", "127.0.0.1:5902", true},
		{"[::1]:5900", "[::1]:5900", true},
		{"5903", "127.0.0.1:5903", true},
		{":5904", "127.0.0.1:5904", true},
		{"10.0.0.5:22", "", false},
		{"192.168.1.1:5900", "", false},
		{"example.com:5900", "", false},
		{"127.0.0.1.nip.io:5900", "", false},
		{"0.0.0.0:5900", "", false},
		{"127.0.0.1:0", "", false},
		{"127.0.0.1:70000", "", false},
		{"127.0.0.1:abc", "", false},
	}
	for _, c := range cases {
		got, err := p.ResolveAddr(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("ResolveAddr(%q) = %q, %v; want %q ok=%v", c.in, got, err, c.want, c.ok)
		}
		if err != nil && !errors.Is(err, ErrForbiddenAddr) {
			t.Errorf("ResolveAddr(%q) error not ErrForbiddenAddr: %v", c.in, err)
		}
	}
}

func TestServeRelay(t *testing.T) {
	p := New(fakeRFB(t, "RFB 003.008\n"))
	if !p.Available() {
		t.Fatal("fake server should be available")
	}
	c := newPipeConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx, c, nil) }()
	if m := recv(t, c); m.t != desktop.BinaryMessage || string(m.b) != "RFB 003.008\n" {
		t.Fatalf("banner %d %q", m.t, m.b)
	}
	c.in <- msg{desktop.BinaryMessage, []byte("RFB 003.008\n")}
	var got []byte
	for len(got) < 12 {
		got = append(got, recv(t, c).b...)
	}
	if string(got) != "RFB 003.008\n" {
		t.Fatalf("echo %q", got)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestServeClientClose(t *testing.T) {
	p := New(fakeRFB(t, "RFB 003.003\n"))
	c := newPipeConn()
	done := make(chan error, 1)
	go func() { done <- p.Serve(context.Background(), c, nil) }()
	recv(t, c)
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after client close")
	}
}

func expectError(t *testing.T, p *Provider, params map[string]string, contains string) {
	t.Helper()
	c := newPipeConn()
	err := p.Serve(context.Background(), c, params)
	if err == nil {
		t.Fatal("expected error")
	}
	m := recv(t, c)
	var e struct{ Type, Message string }
	if m.t != desktop.TextMessage || json.Unmarshal(m.b, &e) != nil || e.Type != "error" || !strings.Contains(e.Message, contains) {
		t.Fatalf("error frame %d %q", m.t, m.b)
	}
}

func TestServeErrors(t *testing.T) {
	expectError(t, New(closedPort(t)), nil, "无法连接")
	expectError(t, New(fakeRFB(t, "SSH-2.0-OpenSSH_9.6\r\n")), nil, "不是 VNC")
	expectError(t, New(fakeRFB(t, "RFB 003.008\n")), map[string]string{"addr": "8.8.8.8:5900"}, "不允许")
	p := New(closedPort(t))
	if p.Available() {
		t.Fatal("closed port reported available")
	}
}
