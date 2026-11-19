//go:build !windows

package agentoslocal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeNode is a stand-in for `agentos node`: it prints its args/env and runs
// until SIGTERM (or exits with 3 when CRASH file exists).
const fakeNode = `#!/bin/sh
trap 'echo got-term; exit 0' TERM
echo "args: $*"
if [ -f "$(dirname "$0")/CRASH" ]; then echo crashing; exit 3; fi
echo "server=$AGENTOS_SERVER token=$AGENTOS_TOKEN name=$AGENTOS_NODE_NAME"
while :; do sleep 0.05; done
`

func newMgr(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-agentos")
	if err := os.WriteFile(exe, []byte(fakeNode), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(OptionsM{Exe: exe, Root: filepath.Join(dir, "localos"), ServerURL: "http://127.0.0.1:1", StopTimeout: 3 * time.Second, MaxBackoff: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func hasLine(m *Manager, id, sub string) bool {
	lines, _ := m.Logs(id, 0)
	return strings.Contains(strings.Join(lines, "\n"), sub)
}

func TestStartStopLogs(t *testing.T) {
	m, _ := newMgr(t)
	defer m.Close()
	if err := m.Ensure(Spec{ID: "../x"}); err == nil {
		t.Fatal("invalid id accepted")
	}
	spec := Spec{ID: "os_a", Name: "A", Token: "aosj_t", NodeID: "nid-1", VNCAddr: "127.0.0.1:5901"}
	if err := m.Ensure(spec); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(m.Dir("os_a"), "node-id"))
	if string(b) != "nid-1" {
		t.Fatalf("node-id %q", b)
	}
	if err := m.Start("os_a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running+output", func() bool {
		st, _ := m.Status("os_a")
		return st.State == StateRunning && hasLine(m, "os_a", "token=aosj_t name=A")
	})
	if !hasLine(m, "os_a", "--data "+m.Dir("os_a")) || !hasLine(m, "os_a", "--vnc 127.0.0.1:5901") {
		t.Fatal("args not passed")
	}
	// desktop=virtual replaces --vnc with --desktop virtual
	if err := m.Ensure(Spec{ID: "os_v", Name: "V", Token: "aosj_t", VNCAddr: "127.0.0.1:5901", Desktop: "virtual"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start("os_v"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "virtual args", func() bool { return hasLine(m, "os_v", "--desktop virtual") })
	if hasLine(m, "os_v", "--vnc") {
		t.Fatal("--vnc passed with desktop=virtual")
	}
	m.Stop("os_v")
	st, _ := m.Status("os_a")
	if st.PID == 0 || syscall.Kill(st.PID, 0) != nil {
		t.Fatal("no pid")
	}
	if err := m.Stop("os_a"); err != nil {
		t.Fatal(err)
	}
	st2, _ := m.Status("os_a")
	if st2.State != StateStopped || !hasLine(m, "os_a", "got-term") {
		t.Fatalf("after stop: %+v", st2)
	}
	if syscall.Kill(st.PID, 0) == nil {
		t.Fatal("process still alive")
	}
	if b, _ := os.ReadFile(filepath.Join(m.Dir("os_a"), "node.log")); !strings.Contains(string(b), "got-term") {
		t.Fatal("node.log missing output")
	}
	// restart works after stop
	if err := m.Restart("os_a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool { st, _ := m.Status("os_a"); return st.State == StateRunning })
	if err := m.Remove("os_a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Dir("os_a")); !os.IsNotExist(err) {
		t.Fatal("data dir not removed")
	}
}

func TestCrashRestartAndClose(t *testing.T) {
	m, dir := newMgr(t)
	os.WriteFile(filepath.Join(dir, "CRASH"), nil, 0o644)
	m.Ensure(Spec{ID: "os_b", Name: "B", Token: "t"})
	m.Start("os_b")
	waitFor(t, "restarts", func() bool { st, _ := m.Status("os_b"); return st.Restarts >= 2 })
	os.Remove(filepath.Join(dir, "CRASH"))
	waitFor(t, "recovered", func() bool { st, _ := m.Status("os_b"); return st.State == StateRunning })
	st, _ := m.Status("os_b")
	m.Close()
	if syscall.Kill(st.PID, 0) == nil {
		t.Fatal("child survived Close")
	}
	if err := m.Start("os_b"); err == nil {
		t.Fatal("start after close")
	}
}

func TestSpawnFailure(t *testing.T) {
	m, err := New(OptionsM{Exe: "/nonexistent/agentos", Root: t.TempDir(), ServerURL: "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.Ensure(Spec{ID: "c", Name: "C"})
	m.Start("c")
	waitFor(t, "failed", func() bool { st, _ := m.Status("c"); return st.State == StateFailed && st.Error != "" })
}
