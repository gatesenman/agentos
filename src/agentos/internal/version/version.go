// Package version holds build metadata injected via -ldflags.
package version

// Overridden at build time:
//
//	-ldflags "-X github.com/agentos/agentos/internal/version.Version=0.1.0 -X ...Commit=abc"
var (
	Version = "0.1.11"
	Commit  = "unknown"
	Date    = "unknown"
)
