package stacks

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// localEnv is a minimal env.Env that runs commands on this machine via os/exec.
type localEnv struct {
	baseEnv
	steps atomic.Int32 // setup-step executions (scripts starting with the step preamble)
}

type localSession struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	out, err io.Reader
}

func (s *localSession) Stdin() io.WriteCloser { return s.in }
func (s *localSession) Stdout() io.Reader     { return s.out }
func (s *localSession) Stderr() io.Reader     { return s.err }
func (s *localSession) Resize(int, int) error { return env.ErrUnsupported }
func (s *localSession) Wait() (int, error) {
	err := s.cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
func (s *localSession) Close() error {
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	return nil
}

func (l *localEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	if strings.HasPrefix(scriptOf(req.Cmd), "set -e") {
		l.steps.Add(1)
	}
	cmd := exec.CommandContext(ctx, req.Cmd[0], req.Cmd[1:]...)
	cmd.Dir = req.WorkDir
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	er, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &localSession{cmd: cmd, in: in, out: out, err: er}, nil
}

func requirePosix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix host required")
	}
}

func TestRealDetectOS(t *testing.T) {
	requirePosix(t)
	osi, err := DetectOS(context.Background(), &localEnv{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("local OS: %+v", osi)
	if runtime.GOOS == "linux" && !strings.HasPrefix(osi.Family, "linux") && osi.Family != FamilyAlpine && osi.Family != FamilyRHEL {
		t.Errorf("family %q on linux", osi.Family)
	}
}

// TestRealPythonNode detects python and node on this machine and runs Install, which
// must be a no-op (detected -> skipped). It never installs anything: when a runtime
// is absent the subtest is skipped.
func TestRealPythonNode(t *testing.T) {
	requirePosix(t)
	ctx := context.Background()
	le := &localEnv{}
	osi, err := DetectOS(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"python", "node"} {
		t.Run(id, func(t *testing.T) {
			s, _ := Get(id)
			d := DetectStack(ctx, le, osi, s)
			if !d.Installed {
				t.Skipf("%s not present on this machine; not installing heavy packages in tests", id)
			}
			if d.Version == "" {
				t.Errorf("no version parsed from %q", d.Output)
			}
			t.Logf("%s detected: %s (%s)", id, d.Version, d.Output)
			var l logRec
			before := le.steps.Load()
			if err := Install(ctx, le, id, l.f); err != nil {
				t.Fatal(err)
			}
			if le.steps.Load() != before {
				t.Errorf("install ran setup steps although %s is present", id)
			}
			if !strings.Contains(l.String(), "跳过") {
				t.Errorf("expected skip log, got:\n%s", l.String())
			}
		})
	}
	ds, _, err := Detect(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Installed {
			t.Logf("installed: %-18s %s", d.ID, d.Version)
		}
	}
}

// TestRealRun exercises streaming, exit codes and timeouts through a real process.
func TestRealRun(t *testing.T) {
	requirePosix(t)
	var mu sync.Mutex
	var got []string
	code, out, err := run(context.Background(), &localEnv{}, []string{"sh", "-c", "echo 一; echo two >&2; printf 'p1\\rp2\\n'; exit 3"}, 10*time.Second, func(stream, line string) {
		mu.Lock()
		got = append(got, stream+":"+line)
		mu.Unlock()
	})
	if err != nil || code != 3 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	j := strings.Join(got, "|")
	if !strings.Contains(j, "stdout:一") || !strings.Contains(j, "stderr:two") || !strings.Contains(j, "stdout:p2") || !strings.Contains(out, "two") {
		t.Errorf("lines %v out %q", got, out)
	}
	start := time.Now()
	_, _, err = run(context.Background(), &localEnv{}, []string{"sh", "-c", "exec sleep 30"}, 300*time.Millisecond, nil)
	if err != ErrTimeout || time.Since(start) > 10*time.Second {
		t.Errorf("timeout: %v after %s", err, time.Since(start))
	}
}

// TestRealPosixScriptsParse syntax-checks every generated POSIX step/detect script with
// sh -n (and bash -n when present).
func TestRealPosixScriptsParse(t *testing.T) {
	requirePosix(t)
	shells := []string{"sh"}
	if _, err := exec.LookPath("bash"); err == nil {
		shells = append(shells, "bash")
	}
	for _, s := range All() {
		for _, fam := range []string{FamilyLinux, FamilyAlpine, FamilyRHEL, FamilyDarwin} {
			scripts := []string{DetectArgv(fam, s.DetectScript(fam))[2]}
			for _, st := range s.Steps(fam) {
				scripts = append(scripts, StepArgv(fam, st)[2])
			}
			for _, sc := range scripts {
				for _, sh := range shells {
					if out, err := exec.Command(sh, "-n", "-c", sc).CombinedOutput(); err != nil {
						t.Errorf("%s/%s (%s): %v %s\n%s", s.ID, fam, sh, err, out, sc)
					}
				}
			}
		}
	}
}

// TestRealWindowsScriptsParse parses every Windows script with the PowerShell parser
// when pwsh is available (PWSH env or PATH); skipped otherwise.
func TestRealWindowsScriptsParse(t *testing.T) {
	pwsh := os.Getenv("PWSH")
	if pwsh == "" {
		p, err := exec.LookPath("pwsh")
		if err != nil {
			t.Skip("pwsh not available")
		}
		pwsh = p
	}
	dir := t.TempDir()
	n := 0
	write := func(sc string) {
		n++
		os.WriteFile(filepath.Join(dir, "s"+strings.Repeat("0", 4-len(itoa4(n)))+itoa4(n)+".ps1"), []byte(sc), 0o644)
	}
	for _, s := range All() {
		write(DetectArgv(FamilyWindows, s.DetectScript(FamilyWindows))[6])
		for _, st := range s.Steps(FamilyWindows) {
			write(StepArgv(FamilyWindows, st)[6])
		}
	}
	write(windowsProbe)
	check := `$bad=0; Get-ChildItem '` + dir + `' -Filter *.ps1 | ForEach-Object { $e=$null; $tk=$null; [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName,[ref]$tk,[ref]$e); if ($e.Count) { $bad++; Write-Output ($_.Name + ': ' + ($e | Out-String)); Get-Content $_.FullName | Write-Output } }; exit $bad`
	out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell parse errors: %v\n%s", err, out)
	}
	t.Logf("parsed %d PowerShell scripts", n)
}

func itoa4(n int) string {
	b := []byte{}
	for n > 0 || len(b) == 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
