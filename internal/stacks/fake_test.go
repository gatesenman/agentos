package stacks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/agentos/agentos/internal/env"
)

// fakeSession is a canned ExecSession.
type fakeSession struct {
	out, errb *bytes.Reader
	code      int
	block     chan struct{} // if non-nil, Wait blocks until closed
	once      sync.Once
}

type nopWC struct{}

func (nopWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopWC) Close() error                { return nil }

func (s *fakeSession) Stdin() io.WriteCloser { return nopWC{} }
func (s *fakeSession) Stdout() io.Reader     { return s.out }
func (s *fakeSession) Stderr() io.Reader     { return s.errb }
func (s *fakeSession) Resize(int, int) error { return nil }
func (s *fakeSession) Wait() (int, error) {
	if s.block != nil {
		<-s.block
		return -1, errors.New("killed")
	}
	return s.code, nil
}
func (s *fakeSession) Close() error {
	if s.block != nil {
		s.once.Do(func() { close(s.block) })
	}
	return nil
}

// baseEnv implements the non-exec parts of env.Env.
type baseEnv struct{}

func (baseEnv) Status(context.Context) (env.Status, error) {
	return env.Status{State: env.StateRunning}, nil
}
func (baseEnv) Start(context.Context) error  { return nil }
func (baseEnv) Stop(context.Context) error   { return nil }
func (baseEnv) Delete(context.Context) error { return nil }
func (baseEnv) PutFile(context.Context, string, io.Reader, uint32) error {
	return env.ErrUnsupported
}
func (baseEnv) GetFile(context.Context, string) (io.ReadCloser, error) {
	return nil, env.ErrUnsupported
}
func (baseEnv) ListDir(context.Context, string) ([]env.FileInfo, error) {
	return nil, env.ErrUnsupported
}
func (baseEnv) Desktop(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, env.ErrUnsupported
}
func (baseEnv) Metrics(context.Context) (env.Metrics, error) {
	return env.Metrics{}, env.ErrUnsupported
}
func (baseEnv) Close() error { return nil }

// fakeEnv simulates one OS family: it answers the OS probe, reports stacks in
// `installed` as present, and "installs" a stack when its last setup step runs.
type fakeEnv struct {
	baseEnv
	family    string // linux|alpine|rhel|darwin|windows|unknown
	mu        sync.Mutex
	installed map[string]bool
	calls     [][]string // every argv
	steps     []string   // "<stack>#<n>" for executed setup steps
	failStep  string     // "<stack>#<n>" -> exit 100 with failOut
	failOut   string
	hangStep  string
	current   string // stack whose detect last failed
}

func newFake(family string, installed ...string) *fakeEnv {
	f := &fakeEnv{family: family, installed: map[string]bool{}}
	for _, s := range installed {
		f.installed[s] = true
	}
	return f
}

var probes = map[string]string{
	"linux":   "Linux\nx86_64\n1000\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n",
	"alpine":  "Linux\naarch64\n0\nID=alpine\nVERSION_ID=3.22.0\nPRETTY_NAME=\"Alpine Linux v3.22\"\n",
	"rhel":    "Linux\nx86_64\n0\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.4\"\n",
	"darwin":  "Darwin\narm64\n501\n",
	"unknown": "Linux\nx86_64\n0\nID=nixos\n",
}

func (f *fakeEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{}, req.Cmd...))
	ok := func(out string) (env.ExecSession, error) {
		return &fakeSession{out: bytes.NewReader([]byte(out)), errb: bytes.NewReader(nil)}, nil
	}
	fail := func(code int, out string) (env.ExecSession, error) {
		return &fakeSession{out: bytes.NewReader(nil), errb: bytes.NewReader([]byte(out)), code: code}, nil
	}
	argv := req.Cmd
	isWin := argv[0] == "powershell.exe" || argv[0] == "cmd"
	if f.family == "windows" && !isWin {
		return nil, errors.New(`exec: "` + argv[0] + `": executable file not found in %PATH%`)
	}
	if f.family != "windows" && isWin {
		return nil, errors.New(`exec: "` + argv[0] + `": executable file not found in $PATH`)
	}
	script := scriptOf(argv)
	if script == posixProbe {
		return ok(probes[f.family])
	}
	if script == windowsProbe {
		return ok("WINDOWS\n10.0.22631.0\nAMD64\nMicrosoft Windows 11 Pro\n")
	}
	fam := f.family
	if fam == "unknown" {
		fam = "linux-nixos"
	}
	all := All()
	isStep := strings.HasPrefix(script, "set -e") || strings.Contains(script, "function Try-Winget")
	for i := range all {
		s := &all[i]
		if !isStep {
			if d := s.DetectScript(fam); d != "" && scriptOf(execArgv(DetectArgv(fam, d))) == script {
				if f.installed[s.ID] {
					return ok(s.ID + " version 1.2.3\n")
				}
				f.current = s.ID
				return fail(127, "command not found")
			}
			continue
		}
		if s.ID != f.current {
			continue
		}
		for n, st := range s.Steps(fam) {
			if scriptOf(execArgv(StepArgv(fam, st))) == script {
				key := s.ID + "#" + strconv.Itoa(n)
				f.steps = append(f.steps, key)
				if key == f.hangStep {
					return &fakeSession{out: bytes.NewReader(nil), errb: bytes.NewReader(nil), block: make(chan struct{})}, nil
				}
				if key == f.failStep {
					return fail(100, f.failOut)
				}
				if n == len(s.Steps(fam))-1 {
					f.installed[s.ID] = true
				}
				return ok("step output for " + key + "\n")
			}
		}
	}
	return fail(1, "fake: unexpected command")
}
