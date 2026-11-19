package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
)

// timings groups the tunables of the restart policy. Tests shrink them.
type timings struct {
	unit        time.Duration // unit of AgentSpec.RestartDelaySec (1s in production)
	defDelay    int           // default restart delay in units when RestartDelaySec<=0
	maxDelay    time.Duration // cap of the exponential backoff
	stableAfter time.Duration // a run this long resets the restart counter
	pipeDrain   time.Duration // max wait for stdout/stderr EOF after the tree is gone
}

var defaultTimings = timings{
	unit:        time.Second,
	defDelay:    3,
	maxDelay:    5 * time.Minute,
	stableAfter: 60 * time.Second,
	pipeDrain:   2 * time.Second,
}

// run is one spawned process (tree).
type run struct {
	cmd     *exec.Cmd
	sys     *sysProc
	done    chan struct{} // closed after the process exited and was reaped
	started time.Time
}

type proc struct {
	spec        api.AgentSpec
	st          api.AgentStatus
	cur         *run // nil when not running
	wantRunning bool
	userStopped bool        // stopped explicitly via Stop(); suppresses AutoStart on re-sync/boot (persisted marker)
	timer       *time.Timer // pending backoff restart
	timerSeq    int         // identifies the pending timer
	stable      *time.Timer // resets the restart counter after a stable run
}

type local struct {
	opt    Options
	t      timings
	mu     sync.Mutex
	fileMu sync.Mutex // serialises spec.json writes/removals
	procs  map[string]*proc
	closed bool
}

// New returns the default local process supervisor. Specs persisted under
// DataDir/agents/<id>/spec.json are loaded and AutoStart agents are started,
// so a node that boots without server connectivity still runs its agents.
func New(opt Options) (Supervisor, error) {
	return newLocal(opt, defaultTimings)
}

func newLocal(opt Options, t timings) (*local, error) {
	if opt.StopTimeout <= 0 {
		opt.StopTimeout = 10 * time.Second
	}
	if opt.Logs == nil {
		return nil, errors.New("supervisor: Logs is required")
	}
	s := &local{opt: opt, t: t, procs: map[string]*proc{}}
	var auto []string
	for _, spec := range s.loadSpecs() {
		p := s.newProc(spec)
		p.userStopped = s.hasStopMarker(spec.ID)
		s.procs[spec.ID] = p
		if spec.AutoStart && !p.userStopped {
			auto = append(auto, spec.ID)
		}
	}
	for _, id := range auto {
		s.opt.Logs.Append(id, api.StreamSystem, "节点启动，自动启动 / node boot, auto-starting")
		s.Start(id) // errors are reflected in the status
	}
	return s, nil
}

func (s *local) agentDir(id string) string { return filepath.Join(s.opt.DataDir, "agents", id) }
func (s *local) workspace(id string) string {
	return filepath.Join(s.agentDir(id), "workspace")
}

func (s *local) newProc(spec api.AgentSpec) *proc {
	return &proc{spec: spec, st: api.AgentStatus{
		ID: spec.ID, NodeID: s.opt.NodeID, State: api.StateStopped,
		Workspace: s.workspace(spec.ID), UpdatedAt: time.Now().UTC(),
	}}
}

func (s *local) emit(st api.AgentStatus) {
	if s.opt.OnStatus != nil {
		s.opt.OnStatus(st)
	}
}

func (s *local) sys(id, msg string) { s.opt.Logs.Append(id, api.StreamSystem, msg) }

// setState must be called with s.mu held; returns a copy to emit after unlock.
func (p *proc) setState(state api.AgentState) api.AgentStatus {
	p.st.State = state
	p.st.UpdatedAt = time.Now().UTC()
	return p.st
}

func (p *proc) stopTimers() {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if p.stable != nil {
		p.stable.Stop()
		p.stable = nil
	}
}

// validID reports whether id is usable as a single path element.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\:`) && filepath.IsLocal(id)
}

func (s *local) Apply(spec api.AgentSpec) (api.AgentStatus, error) {
	if !validID(spec.ID) {
		return api.AgentStatus{}, fmt.Errorf("invalid agent id %q", spec.ID)
	}
	if strings.TrimSpace(spec.Command) == "" {
		return api.AgentStatus{}, errors.New("spec requires a command")
	}
	if err := os.MkdirAll(s.workspace(spec.ID), 0o755); err != nil {
		return api.AgentStatus{}, err
	}
	if err := s.saveSpec(spec); err != nil {
		return api.AgentStatus{}, fmt.Errorf("persist spec: %w", err)
	}
	s.mu.Lock()
	p, known := s.procs[spec.ID]
	if !known {
		p = s.newProc(spec)
		s.procs[spec.ID] = p
	}
	wasAuto := known && p.spec.AutoStart
	p.spec = spec
	st := p.st
	// AutoStart applies when an agent first appears on this node, or when
	// AutoStart is switched on. Re-applying an unchanged spec (server re-sync
	// on reconnect) must not resurrect an agent the user stopped or that exited.
	start := spec.AutoStart && !wasAuto && !p.userStopped && p.cur == nil && !p.wantRunning && !s.closed
	s.mu.Unlock()
	if start {
		return s.Start(spec.ID)
	}
	return st, nil
}

func (s *local) Remove(id string) error {
	_, err := s.Stop(id)
	if err != nil && !errors.Is(err, ErrUnknownAgent) {
		return err
	}
	known := err == nil
	s.mu.Lock()
	delete(s.procs, id)
	s.mu.Unlock()
	if validID(id) {
		s.setStopMarker(id, false)
		removed, ferr := s.deleteSpec(id)
		if ferr != nil {
			return ferr
		}
		known = known || removed
	}
	if !known {
		return ErrUnknownAgent
	}
	s.opt.Logs.Remove(id)
	return nil
}

func (s *local) Start(id string) (api.AgentStatus, error) {
	s.mu.Lock()
	p, ok := s.procs[id]
	if !ok {
		s.mu.Unlock()
		return api.AgentStatus{}, ErrUnknownAgent
	}
	if s.closed {
		st := p.st
		s.mu.Unlock()
		return st, errors.New("supervisor closed")
	}
	if p.cur != nil {
		st := p.st
		s.mu.Unlock()
		return st, nil
	}
	p.stopTimers()
	p.wantRunning = true
	if p.userStopped {
		p.userStopped = false
		s.setStopMarker(id, false)
	}
	p.st.Restarts = 0
	st, err := s.spawnLocked(p)
	if err != nil {
		p.wantRunning = false
	}
	s.mu.Unlock()
	s.emit(st)
	return st, err
}

// spawnLocked starts the process for p. On error the state is Failed and
// p.st.Error explains why. Called with s.mu held.
func (s *local) spawnLocked(p *proc) (api.AgentStatus, error) {
	spec := p.spec
	now := time.Now().UTC()
	p.st.ExitCode, p.st.ExitedAt, p.st.Error, p.st.PID = nil, nil, "", 0
	fail := func(err error) (api.AgentStatus, error) {
		p.st.Error = err.Error()
		p.st.ExitedAt = &now
		s.sys(spec.ID, "启动失败 / failed to start: "+err.Error())
		return p.setState(api.StateFailed), err
	}
	cmd, err := s.buildCmd(spec)
	if err != nil {
		return fail(err)
	}
	lim, warns := parseLimits(spec.Env)
	for _, w := range warns {
		s.sys(spec.ID, w)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return fail(err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	sp, err := prepare(cmd, lim)
	if err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return fail(err)
	}
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err == nil {
		if err = sp.started(cmd); err != nil {
			sp.kill(cmd)
			cmd.Wait()
		}
	}
	if err != nil {
		sp.release()
		outR.Close()
		errR.Close()
		return fail(err)
	}
	for _, w := range sp.warnings {
		s.sys(spec.ID, w)
	}
	r := &run{cmd: cmd, sys: sp, done: make(chan struct{}), started: time.Now()}
	p.cur = r
	p.st.PID = cmd.Process.Pid
	p.st.StartedAt = &now
	s.sys(spec.ID, fmt.Sprintf("进程已启动 / started pid=%d: %s %s", cmd.Process.Pid, cmd.Path, strings.Join(spec.Args, " ")))
	var wg sync.WaitGroup
	wg.Add(2)
	go s.pump(spec.ID, api.StreamStdout, outR, &wg)
	go s.pump(spec.ID, api.StreamStderr, errR, &wg)
	go s.wait(p, r, &wg, outR, errR)
	if p.st.Restarts > 0 {
		p.stable = time.AfterFunc(s.t.stableAfter, func() { s.markStable(p, r) })
	}
	return p.setState(api.StateRunning), nil
}

// markStable resets the restart counter once a run has lasted stableAfter.
func (s *local) markStable(p *proc, r *run) {
	s.mu.Lock()
	if p.cur != r || p.st.Restarts == 0 {
		s.mu.Unlock()
		return
	}
	p.stable = nil
	p.st.Restarts = 0
	st := p.setState(p.st.State)
	s.mu.Unlock()
	s.sys(p.spec.ID, "运行稳定，重启计数已清零 / running stable, restart counter reset")
	s.emit(st)
}

func (s *local) pump(id, stream string, r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 || err == nil {
			// ReadSlice returns ErrBufferFull for long lines: emit the chunk.
			s.opt.Logs.Append(id, stream, strings.TrimRight(string(line), "\r\n"))
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

func (s *local) wait(p *proc, r *run, pumps *sync.WaitGroup, readers ...*os.File) {
	err := r.cmd.Wait()
	// The leader is gone: take down whatever is left of its tree so no
	// orphans survive and the output pipes reach EOF.
	r.sys.cleanup(r.cmd)
	drained := make(chan struct{})
	go func() { pumps.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(s.t.pipeDrain):
		// Something escaped the tree and holds the pipe; stop reading.
		for _, f := range readers {
			f.Close()
		}
	}
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	ranFor := time.Since(r.started)
	now := time.Now().UTC()
	s.sys(p.spec.ID, fmt.Sprintf("进程已退出 / exited code=%d (运行 / ran %s)", code, ranFor.Round(time.Millisecond)))
	s.mu.Lock()
	p.cur = nil
	if p.stable != nil {
		p.stable.Stop()
		p.stable = nil
	}
	p.st.PID = 0
	p.st.ExitCode = &code
	p.st.ExitedAt = &now
	close(r.done)
	if ranFor >= s.t.stableAfter {
		p.st.Restarts = 0
	}
	st := s.afterExitLocked(p, code)
	s.mu.Unlock()
	s.emit(st)
}

// backoffDelay returns base*2^(n-1) capped at max, for the n-th consecutive restart.
func backoffDelay(base time.Duration, n int, max time.Duration) time.Duration {
	d := base
	for i := 1; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// afterExitLocked decides what happens after a run ended (or a restart spawn
// failed) and returns the new status. Called with s.mu held.
func (s *local) afterExitLocked(p *proc, code int) api.AgentStatus {
	if !p.wantRunning || s.closed {
		p.wantRunning = false
		return p.setState(api.StateStopped)
	}
	if s.wantsRestart(p.spec.Restart, code) {
		if p.spec.MaxRestarts > 0 && p.st.Restarts >= p.spec.MaxRestarts {
			p.wantRunning = false
			msg := fmt.Sprintf("已达到最大重启次数 / max restarts reached (%d)", p.spec.MaxRestarts)
			s.sys(p.spec.ID, msg)
			if p.st.Error == "" {
				p.st.Error = msg
			}
			if code == 0 {
				return p.setState(api.StateExited)
			}
			return p.setState(api.StateFailed)
		}
		p.st.Restarts++
		base := time.Duration(p.spec.RestartDelaySec) * s.t.unit
		if p.spec.RestartDelaySec <= 0 {
			base = time.Duration(s.t.defDelay) * s.t.unit
		}
		delay := backoffDelay(base, p.st.Restarts, s.t.maxDelay)
		s.sys(p.spec.ID, fmt.Sprintf("将在 %s 后自动重启 / restarting in %s (#%d)", delay, delay, p.st.Restarts))
		p.timerSeq++
		seq := p.timerSeq
		p.timer = time.AfterFunc(delay, func() { s.restartFromBackoff(p, seq) })
		return p.setState(api.StateBackoff)
	}
	p.wantRunning = false
	if code == 0 {
		return p.setState(api.StateExited)
	}
	return p.setState(api.StateFailed)
}

func (s *local) restartFromBackoff(p *proc, seq int) {
	s.mu.Lock()
	if p.timer == nil || p.timerSeq != seq || !p.wantRunning || p.cur != nil || s.closed || s.procs[p.spec.ID] != p {
		s.mu.Unlock()
		return
	}
	p.timer = nil
	st, err := s.spawnLocked(p)
	if err != nil {
		st = s.afterExitLocked(p, -1)
	}
	s.mu.Unlock()
	s.emit(st)
}

func (s *local) wantsRestart(policy api.RestartPolicy, code int) bool {
	switch policy {
	case api.RestartAlways:
		return true
	case api.RestartOnFailure:
		return code != 0
	}
	return false
}

func (s *local) Stop(id string) (api.AgentStatus, error) {
	s.mu.Lock()
	p, ok := s.procs[id]
	if !ok {
		s.mu.Unlock()
		return api.AgentStatus{}, ErrUnknownAgent
	}
	p.wantRunning = false
	if !s.closed && !p.userStopped {
		p.userStopped = true
		s.setStopMarker(id, true)
	}
	p.stopTimers()
	r := p.cur
	if r == nil {
		var st api.AgentStatus
		changed := p.st.State == api.StateBackoff
		if changed {
			st = p.setState(api.StateStopped)
		} else {
			st = p.st
		}
		s.mu.Unlock()
		if changed {
			s.sys(id, "已取消自动重启 / auto-restart cancelled")
			s.emit(st)
		}
		return st, nil
	}
	st := p.setState(api.StateStopping)
	s.mu.Unlock()
	s.emit(st)
	s.sys(id, "正在停止 / stopping")
	if err := r.sys.terminate(r.cmd); err != nil {
		s.sys(id, "无法优雅停止，强制结束 / graceful stop unavailable, killing: "+err.Error())
		r.sys.kill(r.cmd)
	}
	select {
	case <-r.done:
	case <-time.After(s.opt.StopTimeout):
		s.sys(id, fmt.Sprintf("停止超时 (%s)，强制结束进程树 / stop timed out, killing process tree", s.opt.StopTimeout))
		r.sys.kill(r.cmd)
		<-r.done
	}
	s.mu.Lock()
	st = p.st
	s.mu.Unlock()
	return st, nil
}

func (s *local) Restart(id string) (api.AgentStatus, error) {
	if _, err := s.Stop(id); err != nil {
		return api.AgentStatus{}, err
	}
	return s.Start(id)
}

func (s *local) Status(id string) (api.AgentStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[id]
	if !ok {
		return api.AgentStatus{}, ErrUnknownAgent
	}
	return p.st, nil
}

func (s *local) List() []api.AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.AgentStatus, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, p.st)
	}
	return out
}

func (s *local) PIDs() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]int{}
	for id, p := range s.procs {
		if p.cur != nil {
			m[id] = p.st.PID
		}
	}
	return m
}

// Close stops all agents (in parallel, each bounded by StopTimeout). Specs
// stay on disk so AutoStart agents come back with the next supervisor.
func (s *local) Close() error {
	s.mu.Lock()
	s.closed = true
	ids := make([]string, 0, len(s.procs))
	for id := range s.procs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) { defer wg.Done(); s.Stop(id) }(id)
	}
	wg.Wait()
	return nil
}

func logf(format string, a ...any) { log.Printf("supervisor: "+format, a...) }
