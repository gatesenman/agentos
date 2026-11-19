//go:build !windows

package metrics

import "syscall"

// pgidOf returns the process group id of pid, or 0 if unknown.
func pgidOf(pid int32) int32 {
	pg, err := syscall.Getpgid(int(pid))
	if err != nil {
		return 0
	}
	return int32(pg)
}
