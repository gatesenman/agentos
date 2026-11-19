//go:build windows

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/logs"
)

// The test binary doubles as the agent: AGENTOS_SUP_HELPER=tree starts a
// grandchild, prints "CHILD <pid>" and sleeps; =sleep just sleeps;
// =treeexit starts a grandchild and exits 4.
func TestMain(m *testing.M) {
	switch os.Getenv("AGENTOS_SUP_HELPER") {
	case "sleep":
		time.Sleep(100 * time.Second)
		os.Exit(0)
	case "tree", "treeexit":
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), "AGENTOS_SUP_HELPER=sleep")
		if err := c.Start(); err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Println("CHILD", c.Process.Pid)
		if os.Getenv("AGENTOS_SUP_HELPER") == "treeexit" {
			time.Sleep(300 * time.Millisecond)
			os.Exit(4)
		}
		time.Sleep(100 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperSpec(id, mode string) api.AgentSpec {
	exe, _ := os.Executable()
	return api.AgentSpec{ID: id, Command: exe, Env: map[string]string{"AGENTOS_SUP_HELPER": mode}, AutoStart: true}
}

func grandchild(t *testing.T, ls logs.Store, id string) uint32 {
	t.Helper()
	var pid int
	waitWin(t, "child pid", func() bool {
		for _, l := range ls.Tail(id, 0, 0) {
			if strings.HasPrefix(l.Text, "CHILD ") {
				pid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l.Text, "CHILD ")))
			}
		}
		return pid > 0
	})
	return uint32(pid)
}

func newWinSup(t *testing.T) (*local, logs.Store) {
	dir := t.TempDir()
	ls, err := logs.NewStore(filepath.Join(dir, "logs"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ls.Close() })
	tm := defaultTimings
	tm.unit = 20 * time.Millisecond
	s, err := newLocal(Options{NodeID: "n", DataDir: dir, Logs: ls, StopTimeout: 3 * time.Second}, tm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, ls
}

func waitWin(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func winAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == uint32(windows.WAIT_TIMEOUT)
}

func TestWindowsStopKillsJobTree(t *testing.T) {
	s, ls := newWinSup(t)
	if _, err := s.Apply(helperSpec("w", "tree")); err != nil {
		t.Fatal(err)
	}
	kid := grandchild(t, ls, "w")
	if !winAlive(kid) {
		t.Fatal("grandchild not running")
	}
	st, err := s.Stop("w")
	if err != nil || st.State != api.StateStopped {
		t.Fatalf("%+v %v", st, err)
	}
	waitWin(t, "tree gone", func() bool { return !winAlive(kid) })
}

func TestWindowsLeaderExitKillsOrphans(t *testing.T) {
	s, ls := newWinSup(t)
	s.Apply(helperSpec("o", "treeexit"))
	kid := grandchild(t, ls, "o")
	waitWin(t, "failed", func() bool { st, _ := s.Status("o"); return st.State == api.StateFailed })
	if st, _ := s.Status("o"); st.ExitCode == nil || *st.ExitCode != 4 {
		t.Fatalf("%+v", st)
	}
	waitWin(t, "orphan gone", func() bool { return !winAlive(kid) })
}

func TestWindowsGracefulCtrlBreak(t *testing.T) {
	if !ensureConsole() {
		t.Skip("no console available for CTRL_BREAK (e.g. wine without a console)")
	}
	s, ls := newWinSup(t)
	// ping exits on CTRL_BREAK; the stop must complete well before StopTimeout.
	s.Apply(api.AgentSpec{ID: "g", Command: "ping", Args: []string{"-n", "100", "127.0.0.1"}, AutoStart: true})
	waitWin(t, "running", func() bool { st, _ := s.Status("g"); return st.State == api.StateRunning })
	time.Sleep(500 * time.Millisecond)
	t0 := time.Now()
	s.Stop("g")
	t.Logf("stop took %s", time.Since(t0))
	var j []string
	for _, l := range ls.Tail("g", 0, 0) {
		j = append(j, l.Text)
	}
	if time.Since(t0) > 2*time.Second || !strings.Contains(strings.Join(j, "\n"), "stopping") || strings.Contains(strings.Join(j, "\n"), "stop timed out") || strings.Contains(strings.Join(j, "\n"), "graceful stop unavailable") {
		t.Fatalf("graceful stop did not work: %s", strings.Join(j, "\n"))
	}
}

func TestWindowsResolvePathExt(t *testing.T) {
	p, err := resolveCommand("cmd", `C:\`, []string{`PATH=C:\Windows\System32`, "PATHEXT=.COM;.EXE"})
	if err != nil || !strings.EqualFold(filepath.Base(p), "cmd.exe") {
		t.Fatalf("%q %v", p, err)
	}
}
