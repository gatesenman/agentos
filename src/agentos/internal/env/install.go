package env

// Phase-2 extension (additive, does not modify env.go): pluggable Agent OS
// bootstrapper for providers with CapInstallAgent (docker, ssh, ...).
// Implemented by package internal/env/bootstrap, which registers itself in
// init() via RegisterInstaller.

import (
	"context"
	"sync"
)

// InstallRequest carries what a bootstrapper needs to turn an environment
// into an Agent OS node connected back to this server.
type InstallRequest struct {
	// ServerURL is the server base URL as seen from the API client
	// (e.g. http://192.168.1.10:7800). Bootstrappers may rewrite the host for
	// their network (e.g. host.docker.internal for containers).
	ServerURL string
	// Token is a per-environment node credential: the first node that
	// connects with it is bound to this environment (rec.NodeID is set by the
	// server when it connects). Pass it to the node via AGENTOS_TOKEN or --token.
	Token string
	// Name is the node display name (the environment name).
	Name string
	// BinaryURL returns the download URL of the agentos binary for goos/goarch
	// ("<ServerURL>/download/agentos-<goos>-<goarch>[.exe]").
	BinaryURL func(goos, goarch string) string
	// BinaryPath returns a local file path of the agentos binary for
	// goos/goarch when the server has one (for push-style installs via
	// PutFile); ok=false when unavailable.
	BinaryPath func(goos, goarch string) (path string, ok bool)
	// Log appends a line to the environment's lifecycle log
	// (level: "info" | "warn" | "error").
	Log func(level, text string)
	// NodeID is the node identity to pre-seed (<data>/node-id); the server
	// sets rec.NodeID to it before installing. "" = let the node choose.
	NodeID string
}

// Installer bootstraps an Agent OS node inside a running environment, using
// the environment's own Exec/PutFile. It must return once the node process
// has been started in the background (the server waits for the node to
// connect and then sets rec.NodeID); ctx bounds the whole installation.
type Installer interface {
	Install(ctx context.Context, e Env, rec Record, req InstallRequest) error
}

var (
	instMu    sync.RWMutex
	installer Installer
)

// RegisterInstaller sets the process-wide Agent OS bootstrapper.
func RegisterInstaller(i Installer) {
	instMu.Lock()
	installer = i
	instMu.Unlock()
}

// GetInstaller returns the registered bootstrapper, if any.
func GetInstaller() (Installer, bool) {
	instMu.RLock()
	defer instMu.RUnlock()
	return installer, installer != nil
}
