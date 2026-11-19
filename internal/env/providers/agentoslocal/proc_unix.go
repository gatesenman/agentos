//go:build !windows

package agentoslocal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type jobHandle struct{}

func afterStart(*exec.Cmd) jobHandle { return jobHandle{} }
func closeJob(jobHandle)             {}

// The node gets its own process group; SIGTERM lets it stop its agents.
func terminate(cmd *exec.Cmd, _ jobHandle) {
	if cmd.Process != nil {
		syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
	}
}

func kill(cmd *exec.Cmd, _ jobHandle) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// cleanStale kills a node left over from a previous server run (e.g. after a
// crash on macOS, where there is no parent-death signal) so two processes
// never share the same node identity.
func cleanStale(dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "node.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	if !isOurNode(pid, dir) {
		return
	}
	syscall.Kill(pid, syscall.SIGKILL)
}

// isOurNode checks (best effort, Linux /proc) that pid still runs a node on dir.
func isOurNode(pid int, dir string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false // no /proc (macOS) or gone: don't risk killing an unrelated pid
	}
	return strings.Contains(string(b), dir)
}
