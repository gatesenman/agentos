//go:build windows

package metrics

// pgidOf: Windows has no process groups; the tree is built from parent pids
// (Job Object membership is managed by the supervisor).
func pgidOf(pid int32) int32 { return 0 }
