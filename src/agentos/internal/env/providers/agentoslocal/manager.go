// Manager (salvaged from the abandoned M9 worktree, reviewed) supervises
// local Agent OS instances: each is a
// child `agentos node` process of the server, using the same executable, with
// its own data dir <root>/<id>/ (so agent workspaces, logs and node identity
// are isolated), its own name, credential and optional VNC address.
//
// The manager restarts crashed instances with exponential backoff, keeps the
// last lines of each instance's output in memory and in <root>/<id>/node.log,
// and stops every child when closed (children also die with the server:
// Pdeathsig on Linux, a kill-on-close Job Object on Windows).
package agentoslocal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateBackoff  State = "backoff" // crashed, waiting to restart
	StateFailed   State = "failed"  // could not spawn
)

// Spec describes one local instance.
type Spec struct {
	ID      string // directory name, [A-Za-z0-9_-]
	Name    string
	Token   string // node credential (passed via env, never on the command line)
	NodeID  string // pre-assigned node identity written to <dir>/node-id
	VNCAddr string // optional, passed as --vnc
	Desktop string // "virtual" -> --desktop virtual
}

// Status is the process state of an instance.
type Status struct {
	State     State      `json:"state"`
	PID       int        `json:"pid,omitempty"`
	Restarts  int        `json:"restarts"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Error     string     `json:"error,omitempty"`
	DataDir   string     `json:"data_dir"`
}

type OptionsM struct {
	Exe       string // executable to run (default os.Executable())
	Root      string // parent data dir, e.g. <data>/localos
	ServerURL string // URL children dial, e.g. http://127.0.0.1:7800
	// ExtraEnv is appended to the child environment (tests).
	ExtraEnv    []string
	StopTimeout time.Duration // graceful stop before kill, default 15s
	MaxBackoff  time.Duration // default 60s
	LogLines    int           // in-memory ring per instance, default 500
	Logf        func(format string, args ...any)
	// OnLine is called for every output/lifecycle line of an instance (env log).
	OnLine func(id, line string)
}

type Manager struct {
	opt  OptionsM
	mu   sync.Mutex
	inst map[string]*instance
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

type instance struct {
	spec     Spec
	dir      string
	want     bool // desired running
	st       Status
	cmd      *exec.Cmd
	done     chan struct{} // closed when current process exits
	wake     chan struct{} // interrupts backoff sleep
	loopDone chan struct{}
	ring     []string
	logf     *os.File
	logSize  int64
	lmu      sync.Mutex
}

func New(opt OptionsM) (*Manager, error) {
	// Root must be absolute: the child runs with cmd.Dir = its own dir, so a relative
	// --data path would nest and the child would mint a fresh node-id (bug 0.1.6).
	if opt.Root != "" {
		if abs, err := filepath.Abs(opt.Root); err == nil {
			opt.Root = abs
		}
	}
	if opt.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		opt.Exe = exe
	}
	if opt.Root == "" || opt.ServerURL == "" {
		return nil, errors.New("localos: Root and ServerURL are required")
	}
	if opt.StopTimeout == 0 {
		opt.StopTimeout = 15 * time.Second
	}
	if opt.MaxBackoff == 0 {
		opt.MaxBackoff = 60 * time.Second
	}
	if opt.LogLines == 0 {
		opt.LogLines = 500
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(opt.Root, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{opt: opt, inst: map[string]*instance{}, ctx: ctx, stop: cancel}, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Dir returns the data dir of instance id.
func (m *Manager) Dir(id string) string { return filepath.Join(m.opt.Root, id) }

// Ensure registers (or updates) an instance and prepares its data dir. It
// does not start it. Spec changes apply on next (re)start.
func (m *Manager) Ensure(spec Spec) error {
	if !validID(spec.ID) {
		return fmt.Errorf("localos: invalid id %q", spec.ID)
	}
	dir := m.Dir(spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if spec.NodeID != "" {
		p := filepath.Join(dir, "node-id")
		if b, err := os.ReadFile(p); err != nil || strings.TrimSpace(string(b)) != spec.NodeID {
			if err := os.WriteFile(p, []byte(spec.NodeID), 0o644); err != nil {
				return err
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.inst[spec.ID]; ok {
		in.lmu.Lock()
		in.spec = spec
		in.lmu.Unlock()
		return nil
	}
	m.inst[spec.ID] = &instance{spec: spec, dir: dir, st: Status{State: StateStopped, DataDir: dir}, wake: make(chan struct{}, 1)}
	return nil
}

func (m *Manager) get(id string) (*instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.inst[id]
	if !ok {
		return nil, fmt.Errorf("localos: unknown instance %q", id)
	}
	return in, nil
}

// Status returns the process status of id.
func (m *Manager) Status(id string) (Status, error) {
	in, err := m.get(id)
	if err != nil {
		return Status{}, err
	}
	in.lmu.Lock()
	defer in.lmu.Unlock()
	return in.st, nil
}

// Start launches the instance (no-op if already running) and keeps it alive.
func (m *Manager) Start(id string) error {
	in, err := m.get(id)
	if err != nil {
		return err
	}
	if m.ctx.Err() != nil {
		return errors.New("localos: manager closed")
	}
	in.lmu.Lock()
	if in.want && in.loopDone != nil {
		in.lmu.Unlock()
		return nil
	}
	in.want = true
	select { // drop a stale stop request
	case <-in.wake:
	default:
	}
	in.st.Restarts = 0
	in.st.Error = ""
	ld := make(chan struct{})
	in.loopDone = ld
	in.lmu.Unlock()
	m.wg.Add(1)
	go m.loop(in, ld)
	return nil
}

// Stop terminates the instance gracefully and waits for it to exit.
func (m *Manager) Stop(id string) error {
	in, err := m.get(id)
	if err != nil {
		return err
	}
	m.stopInstance(in)
	return nil
}

func (m *Manager) stopInstance(in *instance) {
	in.lmu.Lock()
	in.want = false
	ld := in.loopDone
	in.lmu.Unlock()
	select {
	case in.wake <- struct{}{}:
	default:
	}
	if ld != nil {
		<-ld
	}
}

// Restart stops then starts the instance.
func (m *Manager) Restart(id string) error {
	if err := m.Stop(id); err != nil {
		return err
	}
	return m.Start(id)
}

// Remove stops the instance, forgets it and optionally deletes its data dir.
func (m *Manager) Remove(id string, deleteData bool) error {
	in, err := m.get(id)
	if err == nil {
		m.stopInstance(in)
		in.lmu.Lock()
		if in.logf != nil {
			in.logf.Close()
			in.logf = nil
		}
		in.lmu.Unlock()
		m.mu.Lock()
		delete(m.inst, id)
		m.mu.Unlock()
	}
	if deleteData && validID(id) {
		return os.RemoveAll(m.Dir(id))
	}
	return nil
}

// Logs returns up to tail most recent output lines (oldest first).
func (m *Manager) Logs(id string, tail int) ([]string, error) {
	in, err := m.get(id)
	if err != nil {
		return nil, err
	}
	in.lmu.Lock()
	defer in.lmu.Unlock()
	if tail <= 0 || tail > len(in.ring) {
		tail = len(in.ring)
	}
	return append([]string(nil), in.ring[len(in.ring)-tail:]...), nil
}

// Close stops all instances (in parallel) and waits for them.
func (m *Manager) Close() {
	m.mu.Lock()
	list := make([]*instance, 0, len(m.inst))
	for _, in := range m.inst {
		list = append(list, in)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, in := range list {
		wg.Add(1)
		go func() { defer wg.Done(); m.stopInstance(in) }()
	}
	wg.Wait()
	m.stop()
	m.wg.Wait()
	for _, in := range list {
		in.lmu.Lock()
		if in.logf != nil {
			in.logf.Close()
			in.logf = nil
		}
		in.lmu.Unlock()
	}
}

func (in *instance) appendLog(line string) {
	in.lmu.Lock()
	defer in.lmu.Unlock()
	in.ring = append(in.ring, line)
	if in.logf != nil {
		n, _ := in.logf.WriteString(line + "\n")
		in.logSize += int64(n)
		if in.logSize > 5<<20 { // simple rotation: node.log -> node.log.1
			in.logf.Close()
			p := filepath.Join(in.dir, "node.log")
			os.Rename(p, p+".1")
			in.logf, _ = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			in.logSize = 0
		}
	}
}

func (m *Manager) logLine(in *instance, format string, args ...any) {
	line := time.Now().Format("2006/01/02 15:04:05") + " [agentos] " + fmt.Sprintf(format, args...)
	m.addLine(in, line)
}

func (m *Manager) addLine(in *instance, line string) {
	in.appendLog(line)
	if m.opt.OnLine != nil {
		m.opt.OnLine(in.spec.ID, line)
	}
	in.lmu.Lock()
	if n := len(in.ring); n > m.opt.LogLines {
		in.ring = append(in.ring[:0:0], in.ring[n-m.opt.LogLines:]...)
	}
	in.lmu.Unlock()
}

func (m *Manager) setState(in *instance, f func(st *Status)) {
	in.lmu.Lock()
	f(&in.st)
	in.lmu.Unlock()
}

// loop runs the instance until want=false, restarting it on exit with backoff.
func (m *Manager) loop(in *instance, loopDone chan struct{}) {
	defer m.wg.Done()
	defer close(loopDone)
	defer m.setState(in, func(st *Status) {
		in.loopDone = nil
		if st.State != StateFailed {
			st.State = StateStopped
		}
		st.PID = 0
	})
	backoff := time.Second
	for {
		in.lmu.Lock()
		want := in.want
		in.lmu.Unlock()
		if !want || m.ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := m.runOnce(in)
		in.lmu.Lock()
		want = in.want
		in.lmu.Unlock()
		if !want || m.ctx.Err() != nil {
			return
		}
		if time.Since(start) > 60*time.Second {
			backoff = time.Second
		}
		m.setState(in, func(st *Status) {
			st.State = StateBackoff
			st.Restarts++
			if err != nil {
				st.Error = err.Error()
			}
		})
		m.logLine(in, "instance exited (%v), restarting in %s / 实例退出，%s 后重启", err, backoff, backoff)
		select {
		case <-in.wake:
		case <-m.ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, m.opt.MaxBackoff)
	}
}

func (m *Manager) childEnv(spec Spec) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "AGENTOS_SERVER", "AGENTOS_TOKEN", "AGENTOS_NODE_NAME", "AGENTOS_DATA", "AGENTOS_VNC",
			"AGENTOS_ADMIN_PASSWORD", "AGENTOS_LISTEN", "AGENTOS_WITH_NODE", "AGENTOS_DESKTOP":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "AGENTOS_SERVER="+m.opt.ServerURL, "AGENTOS_TOKEN="+spec.Token, "AGENTOS_NODE_NAME="+spec.Name,
		"AGENTOS_LOCALOS_ID="+spec.ID)
	return append(env, m.opt.ExtraEnv...)
}

// runOnce spawns the child and waits for it to exit or be stopped.
func (m *Manager) runOnce(in *instance) error {
	in.lmu.Lock()
	spec := in.spec
	if in.logf == nil {
		p := filepath.Join(in.dir, "node.log")
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			in.logf = f
			if fi, err := f.Stat(); err == nil {
				in.logSize = fi.Size()
			}
		}
	}
	in.st.State = StateStarting
	in.lmu.Unlock()
	cleanStale(in.dir)

	args := []string{"node", "--data", in.dir, "--name", spec.Name}
	if spec.Desktop == "virtual" {
		args = append(args, "--desktop", "virtual")
	} else if spec.VNCAddr != "" {
		args = append(args, "--vnc", spec.VNCAddr)
	}
	cmd := exec.Command(m.opt.Exe, args...)
	cmd.Dir = in.dir
	cmd.Env = m.childEnv(spec)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		pw.Close()
		m.setState(in, func(st *Status) { st.State = StateFailed; st.Error = err.Error() })
		m.logLine(in, "spawn failed / 启动失败: %v", err)
		in.lmu.Lock()
		in.want = false
		in.lmu.Unlock()
		return err
	}
	job := afterStart(cmd)
	os.WriteFile(filepath.Join(in.dir, "node.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	now := time.Now().UTC()
	m.setState(in, func(st *Status) {
		st.State, st.PID, st.StartedAt, st.ExitCode, st.Error = StateRunning, cmd.Process.Pid, &now, nil, ""
	})
	m.logLine(in, "started pid %d / 已启动: %s %s", cmd.Process.Pid, m.opt.Exe, strings.Join(args, " "))
	m.opt.Logf("localos: %s (%s) started pid %d", spec.Name, spec.ID, cmd.Process.Pid)

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			m.addLine(in, sc.Text())
		}
		io.Copy(io.Discard, pr)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); pw.Close() }()

	var werr error
	select {
	case werr = <-exited:
	case <-in.wake:
		werr = m.terminate(in, cmd, job, exited)
	case <-m.ctx.Done():
		werr = m.terminate(in, cmd, job, exited)
	}
	<-copied
	closeJob(job)
	os.Remove(filepath.Join(in.dir, "node.pid"))
	code := cmd.ProcessState.ExitCode()
	m.setState(in, func(st *Status) { st.PID = 0; st.ExitCode = &code })
	m.logLine(in, "process exited with code %d / 进程退出，代码 %d", code, code)
	if werr == nil {
		return errors.New("exited")
	}
	return werr
}

func (m *Manager) terminate(in *instance, cmd *exec.Cmd, job jobHandle, exited chan error) error {
	in.lmu.Lock()
	want := in.want
	in.lmu.Unlock()
	if want && m.ctx.Err() == nil { // spurious wake while running: keep waiting
		return <-exited
	}
	m.setState(in, func(st *Status) { st.State = StateStopping })
	terminate(cmd, job)
	select {
	case err := <-exited:
		return err
	case <-time.After(m.opt.StopTimeout):
		m.logLine(in, "graceful stop timed out, killing / 优雅停止超时，强制结束")
		kill(cmd, job)
		return <-exited
	}
}
