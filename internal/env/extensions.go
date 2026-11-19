package env

// Phase-2 optional extensions (additive; env.go stays frozen).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CapabilityReporter is optionally implemented by a Provider whose effective
// capabilities depend on the record's config (e.g. docker desktop.vnc only
// with desktop:true, ssh lifecycle only with start/stop commands). The
// server uses it instead of ProviderInfo.Capabilities for EnvView.
type CapabilityReporter interface {
	CapabilitiesFor(rec *Record) []Capability
}

// CapabilitiesOf returns the effective capabilities of rec under p.
func CapabilitiesOf(p Provider, rec *Record) []Capability {
	if cr, ok := p.(CapabilityReporter); ok {
		return cr.CapabilitiesFor(rec)
	}
	return p.Info().Capabilities
}

// Without returns caps minus drop.
func Without(caps []Capability, drop ...Capability) []Capability {
	out := make([]Capability, 0, len(caps))
next:
	for _, c := range caps {
		for _, d := range drop {
			if c == d {
				continue next
			}
		}
		out = append(out, c)
	}
	return out
}

// ServerURLMapper is optionally implemented by an Env whose network sees the
// management server under a different address (e.g. a container must use the
// bridge gateway instead of 127.0.0.1). Used by install-agentos.
type ServerURLMapper interface {
	MapServerURL(ctx context.Context, serverURL string) string
}

// LogFunc receives progress lines (level: info | warn | error).
type LogFunc func(level, text string)

type logKey struct{}

// WithLogger attaches a progress logger to ctx; providers report long steps
// of Create/Start (image pulls, ...) with Logf. The server routes them to
// the environment's lifecycle log (/envs/{id}/logs, env.log events).
func WithLogger(ctx context.Context, f LogFunc) context.Context {
	return context.WithValue(ctx, logKey{}, f)
}

// Logf reports a progress line if ctx carries a logger.
func Logf(ctx context.Context, level, format string, args ...any) {
	if f, _ := ctx.Value(logKey{}).(LogFunc); f != nil {
		f(level, fmt.Sprintf(format, args...))
	}
}

// ---- 0.1.5 local-VM extensions (all optional) ----

// Preparer is implemented by a Provider that fills generated values (e.g.
// guest credentials, keys) into rec.Config before the record is first stored.
type Preparer interface {
	Prepare(rec *Record) error
}

// ProvisionTimeouter lets a Provider extend the server's default provisioning
// timeout (e.g. ISO download + unattended OS install).
type ProvisionTimeouter interface {
	ProvisionTimeout(rec *Record) time.Duration
}

// CreateResumer marks providers whose Create is idempotent: a provisioning
// interrupted by a server restart is re-run on startup instead of failing.
type CreateResumer interface {
	ResumeCreate(rec *Record) bool
}

// Screenshotter is implemented by an Env that can return a PNG of its screen
// without a desktop session (e.g. the Hyper-V console thumbnail).
type Screenshotter interface {
	Screenshot(ctx context.Context, width int) ([]byte, error)
}

// NativeOpener is implemented by an Env that can open a native viewer on the
// server host (kind "console" = hypervisor window, "rdp" = Remote Desktop).
type NativeOpener interface {
	OpenNative(ctx context.Context, kind string) error
}

type saverKey struct{}

// WithConfigSaver attaches a callback that persists rec.Config while a long
// Create is still running (so ids, credentials and progress survive restarts
// and the UI sees them). Providers call SaveConfig.
func WithConfigSaver(ctx context.Context, f func(json.RawMessage)) context.Context {
	return context.WithValue(ctx, saverKey{}, f)
}

// SaveConfig persists cfg if ctx carries a saver.
func SaveConfig(ctx context.Context, cfg json.RawMessage) {
	if f, _ := ctx.Value(saverKey{}).(func(json.RawMessage)); f != nil {
		f(cfg)
	}
}
