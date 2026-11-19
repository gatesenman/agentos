package docker

// Phase-2 extensions: per-record capabilities, purge-aware delete, server URL
// mapping for install-agentos and a VNC relay for remote daemons.

import (
	"context"
	"io"
	"net"
	"net/url"

	"github.com/agentos/agentos/internal/env"
)

// CapabilitiesFor: desktop.vnc for desktop images or after linux-desktop stack
// (virtual_desktop) so the UI desktop tab appears once the stack is installed.
func (p provider) CapabilitiesFor(rec *env.Record) []env.Capability {
	caps := p.Info().Capabilities
	c, err := parseConfig(rec.Config)
	if err != nil || !(c.Desktop || c.VirtualDesktop) {
		caps = env.Without(caps, env.CapDesktopVNC)
	}
	return caps
}

var _ env.CapabilityReporter = provider{}

// MapServerURL rewrites a loopback server URL to the container's gateway
// (the docker host as seen from the container) for a local daemon.
func (e *dockerEnv) MapServerURL(ctx context.Context, serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || e.cl.remoteHost() != "" {
		return serverURL
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !(ip.IsLoopback() || ip.IsUnspecified())) {
		return serverURL
	}
	ins, err := e.inspect(ctx)
	if err != nil {
		return serverURL
	}
	gw := ins.NetworkSettings.Gateway
	for _, n := range ins.NetworkSettings.Networks {
		if gw == "" {
			gw = n.Gateway
		}
	}
	if gw == "" {
		return serverURL
	}
	if p := u.Port(); p != "" {
		u.Host = net.JoinHostPort(gw, p)
	} else {
		u.Host = gw
	}
	return u.String()
}

var _ env.ServerURLMapper = (*dockerEnv)(nil)

// vncRelay reaches 127.0.0.1:5900 inside the container through docker exec
// (bash /dev/tcp), so a remote daemon never has to publish VNC on a public port.
func (e *dockerEnv) vncRelay(ctx context.Context) (io.ReadWriteCloser, error) {
	s, err := e.cl.exec(ctx, e.id, env.ExecRequest{Cmd: []string{"bash", "-c",
		`exec 3<>/dev/tcp/127.0.0.1/5900 || exit 1; cat <&3 & cat >&3; kill $! 2>/dev/null`}}, nil)
	if err != nil {
		return nil, err
	}
	return &execConn{s: s}, nil
}

type execConn struct{ s env.ExecSession }

func (c *execConn) Read(p []byte) (int, error)  { return c.s.Stdout().Read(p) }
func (c *execConn) Write(p []byte) (int, error) { return c.s.Stdin().Write(p) }
func (c *execConn) Close() error                { c.s.Stdin().Close(); return c.s.Close() }
