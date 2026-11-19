// Package metrics samples host and per-agent process resource usage on the
// node. Owned by module M2. The Collector interface is a FROZEN contract.
package metrics

import (
	"errors"

	"github.com/agentos/agentos/internal/api"
)

var ErrUnsupported = errors.New("metrics: unsupported on this platform")

type Collector interface {
	// Host samples machine-wide stats. diskPath selects the volume to report.
	Host() (api.HostStats, error)
	// Process samples the process tree rooted at pid. CPU% is computed
	// against the previous call for the same pid (first call may return 0).
	Process(pid int) (api.ProcStats, error)
}

// New returns the platform collector (gopsutil based, cgo-free).
// diskPath is the node data dir; its volume is reported as disk usage.
func New(diskPath string) Collector { return newCollector(diskPath) }
