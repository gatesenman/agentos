package metrics

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestHost(t *testing.T) {
	c := New(t.TempDir() + "/not/yet/created")
	h, err := c.Host()
	if err != nil {
		t.Fatal(err)
	}
	if h.CPUCount <= 0 || h.MemTotal == 0 || h.MemUsed == 0 || h.DiskTotal == 0 || h.DiskUsed == 0 {
		t.Fatalf("zero host fields: %+v", h)
	}
	if h.MemUsed > h.MemTotal || h.DiskUsed > h.DiskTotal {
		t.Fatalf("used > total: %+v", h)
	}
	if runtime.GOOS == "linux" && h.UptimeSec == 0 {
		t.Fatalf("uptime 0: %+v", h)
	}
	if h.CPUPercent < 0 || h.CPUPercent > 100 {
		t.Fatalf("cpu%% out of range: %v", h.CPUPercent)
	}
	time.Sleep(100 * time.Millisecond)
	h2, err := c.Host()
	if err != nil || h2.CPUPercent < 0 || h2.CPUPercent > 100 {
		t.Fatalf("second sample: %+v %v", h2, err)
	}
}

func startBusy(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	cmd := exec.Command("sh", "-c", script)
	setPgid(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killGroup(cmd); cmd.Wait() })
	return cmd
}

func TestProcessBusyLoop(t *testing.T) {
	cmd := startBusy(t, "while :; do :; done")
	c := New(t.TempDir())
	if _, err := c.Process(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	ps, err := c.Process(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("busy: %+v", ps)
	want := 50.0
	if overloaded() {
		want = 5 // box saturated by other work: only require that CPU time is measured
	}
	if ps.CPUPercent <= want {
		t.Fatalf("cpu_percent=%v want >%v", ps.CPUPercent, want)
	}
	if ps.MemRSS == 0 || ps.Procs < 1 || ps.Threads < 1 {
		t.Fatalf("bad stats %+v", ps)
	}
}

// The tree aggregate includes children (and backgrounded grandchildren).
func TestProcessTree(t *testing.T) {
	cmd := startBusy(t, "(while :; do :; done) & (while :; do :; done) & sleep 100")
	c := New(t.TempDir())
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := c.Process(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if s.Procs >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("procs=%d want >=4", s.Procs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	s, err := c.Process(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tree: %+v", s)
	want := 80.0 // lower bound; under load busy children share cores (Procs>=4 already proved aggregation)
	if runtime.NumCPU() < 2 {
		want = 40
	}
	if overloaded() {
		want = 10
	}
	if s.CPUPercent < want {
		t.Fatalf("tree cpu=%v want >=%v", s.CPUPercent, want)
	}
}

func TestProcessDead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_, err := New(t.TempDir()).Process(cmd.Process.Pid)
	if !errors.Is(err, ErrNoProcess) {
		t.Fatalf("want ErrNoProcess, got %v", err)
	}
	if _, err := New(t.TempDir()).Process(0); err == nil {
		t.Fatal("pid 0 should fail")
	}
}

// overloaded reports a 1-minute load average above the CPU count (shared CI
// boxes), where CPU-percentage thresholds are meaningless.
func overloaded() bool {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return false
	}
	var l float64
	fmt.Sscan(string(b), &l)
	return l > float64(runtime.NumCPU())
}
