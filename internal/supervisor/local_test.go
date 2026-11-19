//go:build !windows

package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/logs"
)

var fastTimings = timings{
	unit:        20 * time.Millisecond,
	defDelay:    3,
	maxDelay:    300 * 20 * time.Millisecond,
	stableAfter: time.Hour,
	pipeDrain:   500 * time.Millisecond,
}

type harness struct {
	s   *local
	ls  logs.Store
	dir string
	mu  sync.Mutex
	evs []api.AgentStatus
}

func newHarnessIn(t *testing.T, dir string, tm timings) *harness {
	t.Helper()
	ls, err := logs.NewStore(filepath.Join(dir, "logs"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{ls: ls, dir: dir}
	s, err := newLocal(Options{NodeID: "n", DataDir: dir, Logs: ls, StopTimeout: 2 * time.Second,
		OnStatus: func(st api.AgentStatus) { h.mu.Lock(); h.evs = append(h.evs, st); h.mu.Unlock() }}, tm)
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	t.Cleanup(func() { s.Close() })
	return h
}

func newHarness(t *testing.T) *harness { return newHarnessIn(t, t.TempDir(), fastTimings) }

func (h *harness) logText(id string) string {
	var all []string
	for _, l := range h.ls.Tail(id, 0, 0) {
		all = append(all, l.Stream+":"+l.Text)
	}
	return strings.Join(all, "\n")
}

func (h *harness) count(id, sub string) int { return strings.Count(h.logText(id), sub) }

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func waitState(t *testing.T, s Supervisor, id string, want api.AgentState) api.AgentStatus {
	t.Helper()
	var st api.AgentStatus
	waitFor(t, 5*time.Second, "state "+string(want), func() bool {
		st, _ = s.Status(id)
		return st.State == want
	})
	return st
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return err != nil && !os.IsNotExist(err)
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z'
	}
	return true
}

// childPID waits for the agent to print "CHILD <pid>".
func childPID(t *testing.T, h *harness, id string) int {
	t.Helper()
	var pid int
	waitFor(t, 5*time.Second, "child pid", func() bool {
		for _, l := range h.ls.Tail(id, 0, 0) {
			if strings.HasPrefix(l.Text, "CHILD ") {
				pid, _ = strconv.Atoi(strings.TrimPrefix(l.Text, "CHILD "))
				return pid > 0
			}
		}
		return false
	})
	return pid
}

func TestStartLogsExit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.s.Apply(api.AgentSpec{ID: "a", Command: "sh", Args: []string{"-c", "echo hello; echo oops >&2; echo ws=$AGENTOS_WORKSPACE id=$AGENTOS_AGENT_ID x=$X; exit 0"}, Env: map[string]string{"X": "y"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.Start("a"); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, h.s, "a", api.StateExited)
	if st.ExitCode == nil || *st.ExitCode != 0 || st.PID != 0 {
		t.Fatalf("status %+v", st)
	}
	j := h.logText("a")
	ws := filepath.Join(h.dir, "agents", "a", "workspace")
	if !strings.Contains(j, "stdout:hello") || !strings.Contains(j, "stderr:oops") || !strings.Contains(j, "stdout:ws="+ws+" id=a x=y") {
		t.Fatalf("logs: %s", j)
	}
}

func TestExitCodeFailed(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "a", Command: "sh", Args: []string{"-c", "exit 7"}, Restart: api.RestartNever})
	h.s.Start("a")
	st := waitState(t, h.s, "a", api.StateFailed)
	if *st.ExitCode != 7 || st.Restarts != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestStopKillsProcessTree(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "b", Command: "sh", Args: []string{"-c", "sleep 100 & echo CHILD $!; sleep 100"}, AutoStart: true})
	waitState(t, h.s, "b", api.StateRunning)
	child := childPID(t, h, "b")
	if !alive(child) {
		t.Fatal("child not running")
	}
	st, err := h.s.Stop("b")
	if err != nil || st.State != api.StateStopped {
		t.Fatalf("%+v %v", st, err)
	}
	waitFor(t, 2*time.Second, "child gone", func() bool { return !alive(child) })
}

func TestStopEscalatesToKill(t *testing.T) {
	h := newHarness(t)
	h.s.opt.StopTimeout = 300 * time.Millisecond
	h.s.Apply(api.AgentSpec{ID: "b", Command: "sh", Args: []string{"-c", "trap '' TERM; echo CHILD $$; while :; do sleep 0.05; done"}, AutoStart: true})
	pid := childPID(t, h, "b")
	t0 := time.Now()
	st, _ := h.s.Stop("b")
	if st.State != api.StateStopped || time.Since(t0) > 3*time.Second || alive(pid) {
		t.Fatalf("%+v after %s", st, time.Since(t0))
	}
	if !strings.Contains(h.logText("b"), "stop timed out") {
		t.Fatal("expected timeout log")
	}
}

func TestLeaderExitKillsOrphans(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "c", Command: "sh", Args: []string{"-c", "sleep 100 & echo CHILD $!; exit 2"}})
	h.s.Start("c")
	child := childPID(t, h, "c")
	st := waitState(t, h.s, "c", api.StateFailed)
	if *st.ExitCode != 2 {
		t.Fatalf("%+v", st)
	}
	waitFor(t, 2*time.Second, "orphan gone", func() bool { return !alive(child) })
}

func TestRestartOnFailureMax(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "c", Command: "sh", Args: []string{"-c", "exit 3"}, Restart: api.RestartOnFailure, MaxRestarts: 2, RestartDelaySec: 1})
	h.s.Start("c")
	waitFor(t, 5*time.Second, "failed after 2 restarts", func() bool {
		st, _ := h.s.Status("c")
		return st.State == api.StateFailed && st.Restarts == 2
	})
	time.Sleep(200 * time.Millisecond)
	if n := h.count("c", "started pid="); n != 3 {
		t.Fatalf("spawned %d times, want 3\n%s", n, h.logText("c"))
	}
	st, _ := h.s.Status("c")
	if !strings.Contains(st.Error, "max restarts") {
		t.Fatalf("error %q", st.Error)
	}
	h.mu.Lock()
	sawBackoff := false
	for _, e := range h.evs {
		sawBackoff = sawBackoff || e.State == api.StateBackoff
	}
	h.mu.Unlock()
	if !sawBackoff {
		t.Fatal("no backoff state emitted")
	}
}

func TestOnFailureNoRestartOnSuccess(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "c", Command: "true", Restart: api.RestartOnFailure, RestartDelaySec: 1})
	h.s.Start("c")
	st := waitState(t, h.s, "c", api.StateExited)
	time.Sleep(100 * time.Millisecond)
	if st.Restarts != 0 || h.count("c", "started pid=") != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestRestartAlways(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "d", Command: "true", Restart: api.RestartAlways, RestartDelaySec: 1})
	h.s.Start("d")
	waitFor(t, 5*time.Second, "3 restarts", func() bool {
		st, _ := h.s.Status("d")
		return st.Restarts >= 3
	})
	st, _ := h.s.Stop("d")
	if st.State != api.StateStopped {
		t.Fatalf("%+v", st)
	}
	n := h.count("d", "started pid=")
	time.Sleep(500 * time.Millisecond)
	if h.count("d", "started pid=") != n {
		t.Fatal("restarted after stop")
	}
}

func TestBackoffGrows(t *testing.T) {
	max := 5 * time.Minute
	want := []time.Duration{3, 6, 12, 24, 48, 96, 192, 300, 300}
	for i, w := range want {
		if d := backoffDelay(3*time.Second, i+1, max); d != w*time.Second {
			t.Fatalf("restart #%d: %s want %s", i+1, d, w*time.Second)
		}
	}
	if defaultTimings.maxDelay != 5*time.Minute || defaultTimings.stableAfter != 60*time.Second {
		t.Fatal("production timings changed")
	}
	// Integration: gaps between spawns grow.
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "e", Command: "false", Restart: api.RestartAlways, RestartDelaySec: 2}) // 40ms, 80, 160, 320
	h.s.Start("e")
	waitFor(t, 5*time.Second, "4 restarts", func() bool { return h.count("e", "started pid=") >= 5 })
	h.s.Stop("e")
	var times []time.Time
	for _, l := range h.ls.Tail("e", 0, 0) {
		if strings.Contains(l.Text, "started pid=") {
			times = append(times, l.Time)
		}
	}
	// Lower bounds only (scheduling jitter on a loaded box only lengthens gaps):
	// gap k must be at least ~ the nominal backoff 40ms * 2^k, and the last
	// one clearly longer than the first.
	for k := 0; k < 4; k++ {
		gap := times[k+1].Sub(times[k])
		if nominal := (40 * time.Millisecond) << k; gap < nominal*9/10 {
			t.Fatalf("gap %d (%s) shorter than backoff %s", k, gap, nominal)
		}
	}
	if first, last := times[1].Sub(times[0]), times[4].Sub(times[3]); last < first*2 {
		t.Fatalf("backoff not growing: first gap %s, last %s", first, last)
	}
}

func TestRestartCounterResetsAfterStableRun(t *testing.T) {
	tm := fastTimings
	tm.stableAfter = 150 * time.Millisecond
	h := newHarnessIn(t, t.TempDir(), tm)
	// Each run lasts 300ms (> stableAfter) then fails; MaxRestarts=1 would stop
	// it after one restart unless the counter is reset by stable runs.
	h.s.Apply(api.AgentSpec{ID: "f", Command: "sh", Args: []string{"-c", "sleep 0.3; exit 1"}, Restart: api.RestartOnFailure, MaxRestarts: 1, RestartDelaySec: 1})
	h.s.Start("f")
	waitFor(t, 10*time.Second, "4 spawns", func() bool { return h.count("f", "started pid=") >= 4 })
	if !strings.Contains(h.logText("f"), "restart counter reset") {
		t.Fatal("no reset log")
	}
	st, _ := h.s.Status("f")
	if st.Restarts > 1 || st.State == api.StateFailed {
		t.Fatalf("%+v", st)
	}
}

func TestPersistenceAutoStart(t *testing.T) {
	dir := t.TempDir()
	h1 := newHarnessIn(t, dir, fastTimings)
	if _, err := h1.s.Apply(api.AgentSpec{ID: "auto", Name: "A", Command: "sleep", Args: []string{"100"}, AutoStart: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h1.s.Apply(api.AgentSpec{ID: "manual", Command: "sleep", Args: []string{"100"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"auto", "manual"} {
		if _, err := os.Stat(filepath.Join(dir, "agents", id, "spec.json")); err != nil {
			t.Fatal(err)
		}
	}
	waitState(t, h1.s, "auto", api.StateRunning)
	h1.s.Close()
	if len(h1.s.PIDs()) != 0 {
		t.Fatal("still running after Close")
	}

	// A new supervisor on the same folder (node booting offline).
	h2 := newHarnessIn(t, dir, fastTimings)
	st := waitState(t, h2.s, "auto", api.StateRunning)
	if st.PID == 0 {
		t.Fatal("no pid")
	}
	if st, _ := h2.s.Status("manual"); st.State != api.StateStopped {
		t.Fatalf("manual: %+v", st)
	}
	if len(h2.s.List()) != 2 {
		t.Fatal("list")
	}
	// Apply of the same spec (server sync on reconnect) does not double start.
	h2.s.Apply(api.AgentSpec{ID: "auto", Name: "A", Command: "sleep", Args: []string{"100"}, AutoStart: true})
	if st2, _ := h2.s.Status("auto"); st2.PID != st.PID {
		t.Fatal("restarted on re-apply")
	}
	pid := st.PID
	if err := h2.s.Remove("auto"); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Fatal("process survived Remove")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "auto", "spec.json")); !os.IsNotExist(err) {
		t.Fatal("spec.json kept after Remove")
	}
	if fi, err := os.Stat(filepath.Join(dir, "agents", "auto", "workspace")); err != nil || !fi.IsDir() {
		t.Fatal("workspace removed")
	}
	if _, err := h2.s.Status("auto"); err != ErrUnknownAgent {
		t.Fatal(err)
	}
	if err := h2.s.Remove("nope"); err != ErrUnknownAgent {
		t.Fatal(err)
	}
	h3 := newHarnessIn(t, dir, fastTimings)
	if len(h3.s.List()) != 1 {
		t.Fatalf("removed agent came back: %+v", h3.s.List())
	}
}

func TestSpawnHygiene(t *testing.T) {
	h := newHarness(t)
	// Missing executable.
	h.s.Apply(api.AgentSpec{ID: "x", Command: "definitely-not-a-command-xyz"})
	st, err := h.s.Start("x")
	if err == nil || st.State != api.StateFailed || !strings.Contains(st.Error, "command not found") {
		t.Fatalf("%+v %v", st, err)
	}
	// work_dir escaping the workspace.
	h.s.Apply(api.AgentSpec{ID: "y", Command: "true", WorkDir: "../../etc"})
	if st, err := h.s.Start("y"); err == nil || !strings.Contains(st.Error, "work_dir") {
		t.Fatalf("%+v %v", st, err)
	}
	// Relative work_dir inside the workspace is created.
	h.s.Apply(api.AgentSpec{ID: "w", Command: "pwd", WorkDir: "sub/dir"})
	h.s.Start("w")
	waitState(t, h.s, "w", api.StateExited)
	if !strings.Contains(h.logText("w"), filepath.Join("w", "workspace", "sub", "dir")) {
		t.Fatal(h.logText("w"))
	}
	// PATH from the spec env is used for lookup.
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "mytool"), []byte("#!/bin/sh\necho mytool-ran\n"), 0o755)
	h.s.Apply(api.AgentSpec{ID: "z", Command: "mytool", Env: map[string]string{"PATH": bin + ":/usr/bin:/bin"}})
	if _, err := h.s.Start("z"); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.s, "z", api.StateExited)
	if !strings.Contains(h.logText("z"), "stdout:mytool-ran") {
		t.Fatal(h.logText("z"))
	}
	// Invalid ids are rejected (path traversal).
	if _, err := h.s.Apply(api.AgentSpec{ID: "../evil", Command: "true"}); err == nil {
		t.Fatal("accepted bad id")
	}
}

func TestLongLineSplit(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "l", Command: "sh", Args: []string{"-c", "head -c 200000 /dev/zero | tr '\\0' a; echo; echo end"}})
	h.s.Start("l")
	waitState(t, h.s, "l", api.StateExited)
	total := 0
	for _, l := range h.ls.Tail("l", 0, 0) {
		if l.Stream == api.StreamStdout {
			total += len(l.Text)
		}
	}
	if total != 200003 {
		t.Fatalf("got %d bytes", total)
	}
}

func TestCloseStopsAll(t *testing.T) {
	h := newHarness(t)
	h.s.Apply(api.AgentSpec{ID: "p", Command: "sh", Args: []string{"-c", "sleep 100 & echo CHILD $!; wait"}, AutoStart: true})
	child := childPID(t, h, "p")
	h.s.Close()
	waitFor(t, 2*time.Second, "child gone", func() bool { return !alive(child) })
	if _, err := h.s.Start("p"); err == nil {
		t.Fatal("start after close")
	}
}

// A user-stopped AutoStart agent must not be restarted by a re-sync (Apply of
// the same spec) nor at the next node boot, until the user starts it again.
func TestUserStopSurvivesResyncAndReboot(t *testing.T) {
	dir := t.TempDir()
	h := newHarnessIn(t, dir, fastTimings)
	spec := api.AgentSpec{ID: "us", Command: "sleep", Args: []string{"30"}, AutoStart: true}
	if _, err := h.s.Apply(spec); err != nil {
		t.Fatal(err)
	}
	waitState(t, h.s, "us", api.StateRunning)
	if st, _ := h.s.Stop("us"); st.State != api.StateStopped {
		t.Fatalf("stop: %+v", st)
	}
	if st, _ := h.s.Apply(spec); st.State != api.StateStopped {
		t.Fatalf("re-sync restarted a user-stopped agent: %+v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if st, _ := h.s.Status("us"); st.State != api.StateStopped {
		t.Fatalf("state after re-sync: %s", st.State)
	}
	h.s.Close()

	h2 := newHarnessIn(t, dir, fastTimings)
	time.Sleep(100 * time.Millisecond)
	if st, _ := h2.s.Status("us"); st.State != api.StateStopped {
		t.Fatalf("reboot autostarted a user-stopped agent: %s", st.State)
	}
	if _, err := h2.s.Start("us"); err != nil {
		t.Fatal(err)
	}
	waitState(t, h2.s, "us", api.StateRunning)
	h2.s.Close() // shutdown stop must not count as a user stop

	h3 := newHarnessIn(t, dir, fastTimings)
	waitState(t, h3.s, "us", api.StateRunning)
}
