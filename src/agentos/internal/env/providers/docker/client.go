package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// client is a minimal Docker Engine API client over plain net/http.
// It talks to the daemon's current API version (unversioned paths).
type client struct {
	network, addr string // "unix" path | "npipe" path | "tcp" host:port
	httpHost      string // Host used in URLs / Host header
	hc            *http.Client
}

// parseHost accepts unix:///path, npipe:////./pipe/name, tcp://host:port, host:port.
// Empty means $DOCKER_HOST or the platform default.
func newClient(host string) (*client, error) {
	if host == "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if host == "" {
		host = defaultHost
	}
	c := &client{httpHost: "docker"}
	switch {
	case strings.HasPrefix(host, "unix://"):
		c.network, c.addr = "unix", strings.TrimPrefix(host, "unix://")
	case strings.HasPrefix(host, "npipe://"):
		p := strings.TrimPrefix(host, "npipe://")
		c.network, c.addr = "npipe", strings.ReplaceAll(p, "/", `\`)
	case strings.HasPrefix(host, "tcp://"), strings.HasPrefix(host, "http://"):
		u, err := url.Parse(host)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("docker: invalid host %q", host)
		}
		c.network, c.addr, c.httpHost = "tcp", u.Host, u.Host
	case strings.Contains(host, "://"):
		return nil, fmt.Errorf("docker: unsupported host scheme %q (use unix://, npipe:// or tcp://)", host)
	default:
		if _, _, err := net.SplitHostPort(host); err != nil {
			return nil, fmt.Errorf("docker: invalid host %q", host)
		}
		c.network, c.addr, c.httpHost = "tcp", host, host
	}
	c.hc = &http.Client{Transport: &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return c.dial(ctx) },
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSHandshakeTimeout: 10 * time.Second,
	}}
	return c, nil
}

func (c *client) dial(ctx context.Context) (net.Conn, error) {
	switch c.network {
	case "npipe":
		return dialPipe(ctx, c.addr)
	default:
		var d net.Dialer
		return d.DialContext(ctx, c.network, c.addr)
	}
}

// remoteHost returns the docker host's hostname for tcp daemons ("" for local sockets).
func (c *client) remoteHost() string {
	if c.network != "tcp" {
		return ""
	}
	h, _, _ := net.SplitHostPort(c.addr)
	return h
}

func (c *client) close() { c.hc.CloseIdleConnections() }

// apiError is a non-2xx answer from the daemon.
type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Msg, e.Code) }

func (e *apiError) Is(target error) bool {
	return target == env.ErrNotFound && e.Code == http.StatusNotFound
}

func isStatus(err error, code int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == code
}

func (c *client) newRequest(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Request, error) {
	u := "http://" + c.httpHost + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return http.NewRequestWithContext(ctx, method, u, body)
}

// do performs a request; body may be nil, an io.Reader (sent raw) or any value (JSON).
// The caller must close the returned body.
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any, ctype string) (*http.Response, error) {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case io.Reader:
		r = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		r, ctype = bytes.NewReader(buf), "application/json"
	}
	req, err := c.newRequest(ctx, method, path, q, r)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &apiError{Code: resp.StatusCode, Msg: msg}
}

// json does a request and decodes a JSON answer into out (if non-nil).
func (c *client) json(ctx context.Context, method, path string, q url.Values, in, out any) error {
	resp, err := c.do(ctx, method, path, q, in, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// hijack performs a POST that upgrades to a raw bidirectional stream (exec start, attach).
func (c *client) hijack(ctx context.Context, path string, in any) (net.Conn, *bufio.Reader, error) {
	buf, err := json.Marshal(in)
	if err != nil {
		return nil, nil, err
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("docker: %w", err)
	}
	req, _ := c.newRequest(context.Background(), http.MethodPost, path, nil, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("docker: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("docker: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		defer conn.Close()
		return nil, nil, readAPIError(resp)
	}
	if !stop() {
		conn.Close()
		return nil, nil, ctx.Err()
	}
	return conn, br, nil
}
