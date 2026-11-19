package metrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/agentos/agentos/internal/api"
)

// ErrNoProcess is returned by Process when the root pid does not exist.
var ErrNoProcess = errors.New("metrics: process not found")

// procKey identifies a process instance (pid + start time guards against pid reuse).
type procKey struct {
	pid    int32
	create int64 // ms since epoch
}

// treeSample is the previous CPU sample of one process tree.
type treeSample struct {
	at      time.Time
	members map[procKey]float64 // cumulative cpu seconds (user+system) per member
}

type collector struct {
	diskPath string

	mu       sync.Mutex
	hostPrev *cpu.TimesStat
	trees    map[procKey]*treeSample
	tbl      *procTable
	lastGC   time.Time
}

func newCollector(diskPath string) *collector {
	return &collector{diskPath: diskPath, trees: map[procKey]*treeSample{}}
}

// ---------- host ----------

func busyTotal(t cpu.TimesStat) (busy, total float64) {
	// Guest time is already included in User on Linux.
	total = t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	busy = total - t.Idle - t.Iowait
	return
}

func cpuDeltaPercent(prev, cur cpu.TimesStat) float64 {
	b0, t0 := busyTotal(prev)
	b1, t1 := busyTotal(cur)
	if t1 <= t0 {
		return 0
	}
	p := (b1 - b0) / (t1 - t0) * 100
	return clamp(p, 0, 100)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func hostCPUTimes() (*cpu.TimesStat, error) {
	ts, err := cpu.Times(false)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, ErrUnsupported
	}
	return &ts[0], nil
}

// existingDir walks up from p until it finds an existing path (the data dir
// may not exist yet), so disk usage is reported for the right volume.
func existingDir(p string) string {
	if p == "" {
		p = "."
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

func (c *collector) Host() (api.HostStats, error) {
	hs := api.HostStats{Time: time.Now().UTC(), CPUCount: runtime.NumCPU()}
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		hs.CPUCount = n
	}

	// CPU% = busy delta since the previous Host() call. On the very first
	// call take a short 200ms baseline so the value is meaningful.
	c.mu.Lock()
	prev := c.hostPrev
	c.mu.Unlock()
	if prev == nil {
		if t, err := hostCPUTimes(); err == nil {
			prev = t
			time.Sleep(200 * time.Millisecond)
		}
	}
	if cur, err := hostCPUTimes(); err == nil {
		if prev != nil {
			hs.CPUPercent = cpuDeltaPercent(*prev, *cur)
		}
		c.mu.Lock()
		c.hostPrev = cur
		c.mu.Unlock()
	}

	var firstErr error
	if vm, err := mem.VirtualMemory(); err == nil {
		hs.MemTotal = vm.Total
		// Used = Total - Available: what users expect ("in use", excludes cache).
		if vm.Available <= vm.Total {
			hs.MemUsed = vm.Total - vm.Available
		} else {
			hs.MemUsed = vm.Used
		}
	} else {
		firstErr = err
	}
	if du, err := disk.Usage(existingDir(c.diskPath)); err == nil {
		hs.DiskTotal = du.Total
		hs.DiskUsed = du.Used
	} else if firstErr == nil {
		firstErr = err
	}
	if up, err := host.Uptime(); err == nil {
		hs.UptimeSec = up
	}
	if runtime.GOOS != "windows" {
		if la, err := load.Avg(); err == nil {
			hs.Load1 = la.Load1
		}
	}
	if hs.MemTotal == 0 && hs.DiskTotal == 0 && firstErr != nil {
		return hs, fmt.Errorf("metrics: host: %w", firstErr)
	}
	return hs, nil
}

// ---------- process tree ----------

// procTable is a snapshot of the process table (parent links and, on Unix,
// process groups). It is cached briefly so sampling N agents in one stats
// tick enumerates the system only once.
type procTable struct {
	at       time.Time
	byPid    map[int32]*process.Process
	children map[int32][]int32
	groups   map[int32][]int32 // pgid -> member pids (Unix only)
}

const tableTTL = time.Second

func (c *collector) table(ctx context.Context) *procTable {
	c.mu.Lock()
	t := c.tbl
	c.mu.Unlock()
	if t != nil && time.Since(t.at) < tableTTL {
		return t
	}
	t = &procTable{at: time.Now(), byPid: map[int32]*process.Process{}, children: map[int32][]int32{}, groups: map[int32][]int32{}}
	all, err := process.ProcessesWithContext(ctx)
	if err == nil {
		for _, p := range all {
			t.byPid[p.Pid] = p
			if ppid, err := p.PpidWithContext(ctx); err == nil && ppid != p.Pid {
				t.children[ppid] = append(t.children[ppid], p.Pid)
			}
			if pg := pgidOf(p.Pid); pg > 0 && pg != p.Pid {
				t.groups[pg] = append(t.groups[pg], p.Pid)
			}
		}
	}
	c.mu.Lock()
	c.tbl = t
	c.mu.Unlock()
	return t
}

// treeMembers returns the root process plus all descendants (by parent pid)
// and, on Unix, every process in the root's process group (the supervisor
// starts agents as group leaders, so double-forked daemons re-parented to
// init are still attributed to the agent).
func (c *collector) treeMembers(ctx context.Context, root *process.Process) []*process.Process {
	t := c.table(ctx)
	seen := map[int32]bool{root.Pid: true}
	out := []*process.Process{root}
	queue := []int32{root.Pid}
	add := func(pid int32) {
		if seen[pid] || pid <= 0 {
			return
		}
		seen[pid] = true
		p := t.byPid[pid]
		if p == nil {
			p = &process.Process{Pid: pid}
		}
		out = append(out, p)
		queue = append(queue, pid)
	}
	if pgidOf(root.Pid) == root.Pid {
		for _, pid := range t.groups[root.Pid] {
			add(pid)
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, ch := range t.children[pid] {
			add(ch)
		}
	}
	return out
}

func (c *collector) Process(pid int) (api.ProcStats, error) {
	ctx := context.Background()
	now := time.Now()
	ps := api.ProcStats{Time: now.UTC()}
	if pid <= 0 {
		return ps, ErrNoProcess
	}
	root, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return ps, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}
	if st, err := root.StatusWithContext(ctx); err == nil && len(st) > 0 && st[0] == process.Zombie {
		return ps, fmt.Errorf("%w: pid %d is a zombie", ErrNoProcess, pid)
	}
	rootCreate, _ := root.CreateTimeWithContext(ctx)
	rootKey := procKey{int32(pid), rootCreate}

	members := c.treeMembers(ctx, root)
	cur := map[procKey]float64{}
	var lifetimeCPU float64
	for _, p := range members {
		create, err := p.CreateTimeWithContext(ctx)
		if err != nil {
			continue // exited meanwhile
		}
		k := procKey{p.Pid, create}
		if t, err := p.TimesWithContext(ctx); err == nil {
			cur[k] = t.User + t.System
			lifetimeCPU += t.User + t.System
		}
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil {
			ps.MemRSS += mi.RSS
		}
		if n, err := p.NumThreadsWithContext(ctx); err == nil {
			ps.Threads += int(n)
		}
		ps.Procs++
	}
	if ps.Procs == 0 {
		return ps, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.trees[rootKey]
	if prev != nil {
		dt := now.Sub(prev.at).Seconds()
		var used float64
		for k, v := range cur {
			if pv, ok := prev.members[k]; ok {
				if v > pv {
					used += v - pv
				}
			} else if time.UnixMilli(k.create).After(prev.at) {
				used += v // started since the previous sample
			}
		}
		if dt > 0 {
			ps.CPUPercent = used / dt * 100
		}
	} else if rootCreate > 0 {
		// First call: lifetime average of the tree (better than 0).
		if age := now.Sub(time.UnixMilli(rootCreate)).Seconds(); age > 0.05 {
			ps.CPUPercent = lifetimeCPU / age * 100
		}
	}
	if max := float64(runtime.NumCPU()) * 100; ps.CPUPercent > max {
		ps.CPUPercent = max
	}
	if ps.CPUPercent < 0 {
		ps.CPUPercent = 0
	}
	c.trees[rootKey] = &treeSample{at: now, members: cur}
	c.gcLocked(now)
	return ps, nil
}

// gcLocked drops CPU baselines of trees not sampled for 10 minutes.
func (c *collector) gcLocked(now time.Time) {
	if now.Sub(c.lastGC) < time.Minute {
		return
	}
	c.lastGC = now
	for k, s := range c.trees {
		if now.Sub(s.at) > 10*time.Minute {
			delete(c.trees, k)
		}
	}
}
