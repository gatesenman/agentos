// Package vnc bridges a tunnel to a local VNC (RFB) server so the browser's
// noVNC client can talk to it. Owned by module M4.
//
// Wire format on the tunnel: raw RFB bytes in binary WebSocket frames, in
// both directions. If the bridge cannot be established (bad target, VNC
// server down, not an RFB server) the provider sends exactly one text frame
// {"type":"error","message":"..."} (same shape as docs/protocol.md §5) and
// closes; the server translates it into a WebSocket close reason for the
// browser.
package vnc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/desktop"
)

const DefaultAddr = "127.0.0.1:5900"

// ErrForbiddenAddr is returned when a requested target is not loopback and
// not the address configured with --vnc.
var ErrForbiddenAddr = errors.New("vnc: target address not allowed (loopback only)")

type Provider struct {
	// Addr is the default VNC server (node flag --vnc / AGENTOS_VNC). The
	// operator may set a non-loopback address here; browser-supplied
	// params["addr"] are restricted to loopback or exactly this address.
	Addr string
	// DialTimeout for the TCP connection (default 3s).
	DialTimeout time.Duration
	// BannerTimeout bounds the wait for the "RFB xxx.yyy\n" greeting (default 10s).
	BannerTimeout time.Duration
}

func New(addr string) *Provider {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Provider{Addr: addr}
}

func (p *Provider) Kind() string       { return api.TunnelVNC }
func (p *Provider) Capability() string { return api.CapVNC }

// Available is evaluated on every call (no caching): a short TCP dial.
func (p *Provider) Available() bool {
	c, err := net.DialTimeout("tcp", p.Addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// ResolveAddr validates a requested target ("" = default, "5901" or ":5901" =
// loopback port, "host:port"). Allowed: loopback IPs, "localhost", or the
// configured Addr verbatim. Other host names are refused (no DNS lookups, so
// no rebinding tricks).
func (p *Provider) ResolveAddr(req string) (string, error) {
	req = strings.TrimSpace(req)
	if req == "" {
		return p.Addr, nil
	}
	if req == p.Addr {
		return req, nil
	}
	if !strings.Contains(req, ":") || strings.HasPrefix(req, ":") {
		req = "127.0.0.1:" + strings.TrimPrefix(req, ":")
	}
	host, port, err := net.SplitHostPort(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrForbiddenAddr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("%w: bad port %q", ErrForbiddenAddr, port)
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%w: %s", ErrForbiddenAddr, host)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// shortErr renders common network errors compactly; the message must fit a
// WebSocket close reason (123 bytes) together with the address.
func shortErr(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "连接被关闭"
	case errors.As(err, &ne) && ne.Timeout():
		return "连接超时"
	}
	s := err.Error()
	if len(s) > 40 {
		s = s[len(s)-40:]
	}
	return strings.ToValidUTF8(s, "")
}

type errorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func sendError(conn desktop.Conn, msg string) {
	b, _ := json.Marshal(errorMsg{Type: "error", Message: msg})
	conn.WriteMessage(desktop.TextMessage, b)
}

// Serve dials the VNC server, checks the RFB greeting and relays bytes until
// either side closes or ctx is cancelled. conn is closed on return.
func (p *Provider) Serve(ctx context.Context, conn desktop.Conn, params map[string]string) error {
	defer conn.Close()
	addr, err := p.ResolveAddr(params["addr"])
	if err != nil {
		sendError(conn, "不允许的 VNC 目标地址（仅限本机回环地址）: "+params["addr"])
		return err
	}
	dt := p.DialTimeout
	if dt <= 0 {
		dt = 3 * time.Second
	}
	d := net.Dialer{Timeout: dt}
	tcp, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		sendError(conn, fmt.Sprintf("无法连接 VNC 服务器 %s（%s），请确认节点上的 VNC 服务已启动", addr, shortErr(err)))
		return fmt.Errorf("vnc: dial %s: %w", addr, err)
	}
	defer tcp.Close()

	// Validate the RFB greeting ("RFB 003.008\n", 12 bytes) before relaying,
	// so a wrong port gives a clear message instead of a protocol error.
	bt := p.BannerTimeout
	if bt <= 0 {
		bt = 10 * time.Second
	}
	tcp.SetReadDeadline(time.Now().Add(bt))
	banner := make([]byte, 12)
	if _, err := io.ReadFull(tcp, banner); err != nil {
		sendError(conn, fmt.Sprintf("VNC 服务器 %s 未返回 RFB 握手（%s）", addr, shortErr(err)))
		return fmt.Errorf("vnc: banner from %s: %w", addr, err)
	}
	if string(banner[:4]) != "RFB " {
		sendError(conn, fmt.Sprintf("%s 不是 VNC (RFB) 服务器", addr))
		return fmt.Errorf("vnc: %s: not an RFB server (%q)", addr, banner)
	}
	tcp.SetReadDeadline(time.Time{})
	if err := conn.WriteMessage(desktop.BinaryMessage, banner); err != nil {
		return err
	}

	var once sync.Once
	stop := func() { once.Do(func() { tcp.Close(); conn.Close() }) }
	errc := make(chan error, 2)
	go func() { // tcp -> ws (only writer of conn)
		buf := make([]byte, 64*1024)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(desktop.BinaryMessage, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() { // ws -> tcp
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if _, err := tcp.Write(data); err != nil {
				errc <- err
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		stop()
		<-errc
		return ctx.Err()
	case err := <-errc:
		stop()
		<-errc
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}
