//go:build !windows

package main

// Module M8. `agentos service ...` is Windows-only; Linux/macOS use the
// systemd unit / launchd plist shipped in the release archives (install.sh),
// or `agentos node --install-service` (node_service_unix.go) for a node.
import "errors"

func runService(args []string) error {
	return errors.New("service management is only built-in on Windows; use install.sh from the release archive " +
		"(systemd / launchd), or `agentos node --install-service` for a node")
}
