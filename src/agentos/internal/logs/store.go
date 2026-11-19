package logs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
)

// File format, one line per LogLine (seq persisted so it survives restarts):
//
//	<RFC3339Nano time> #<seq> [<stream>] <text>\n
//
// Legacy phase-1 lines ("<time> [<stream>] <text>") are still readable.

const (
	DefaultMaxFileSize = 10 << 20 // rotate <agent>.log at 10 MB
	DefaultMaxBackups  = 5        // keep <agent>.log.1 … .5 (≤ 6 files per agent)
)

// Options configures NewStoreWithOptions. Zero values select the defaults.
type Options struct {
	RingSize    int
	MaxFileSize int64
	MaxBackups  int
}

type agentLog struct {
	mu     sync.Mutex
	ring   *Ring
	seq    uint64
	file   *os.File
	size   int64
	path   string
	closed bool
}

type fileStore struct {
	dir  string
	opt  Options
	mu   sync.Mutex
	agts map[string]*agentLog
	done bool

	subMu   sync.RWMutex
	subs    map[int]func(api.LogLine)
	nextSub int
}

// NewStore creates a Store writing <dir>/<agentID>.log files with size-based
// rotation (10 MB × 5 backups) and reload of the last ringSize lines after a
// restart; Seq continues from the last persisted value.
func NewStore(dir string, ringSize int) (Store, error) {
	return NewStoreWithOptions(dir, Options{RingSize: ringSize})
}

// NewStoreWithOptions is NewStore with tunable rotation (used by tests).
func NewStoreWithOptions(dir string, opt Options) (Store, error) {
	if opt.RingSize <= 0 {
		opt.RingSize = DefaultRingSize
	}
	if opt.MaxFileSize <= 0 {
		opt.MaxFileSize = DefaultMaxFileSize
	}
	if opt.MaxBackups <= 0 {
		opt.MaxBackups = DefaultMaxBackups
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &fileStore{dir: dir, opt: opt, agts: map[string]*agentLog{}, subs: map[int]func(api.LogLine){}}, nil
}

// fileName maps an agent id to a safe file name.
func fileName(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	id = r.Replace(id)
	if id == "" {
		id = "_"
	}
	return id + ".log"
}

// get returns the agent's state, loading it from disk on first access.
func (s *fileStore) get(id string) *agentLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agts[id]; ok {
		return a
	}
	a := &agentLog{ring: NewRing(s.opt.RingSize), path: filepath.Join(s.dir, fileName(id)), closed: s.done}
	lines := readLastLines(s.files(a.path), s.opt.RingSize)
	for _, l := range lines {
		l.AgentID = id
		a.ring.Add(l)
	}
	if len(lines) > 0 {
		a.seq = lines[len(lines)-1].Seq
	}
	s.agts[id] = a
	return a
}

// files lists the log files of an agent, newest first.
func (s *fileStore) files(path string) []string {
	out := []string{path}
	for i := 1; i <= s.opt.MaxBackups; i++ {
		out = append(out, path+"."+strconv.Itoa(i))
	}
	return out
}

func (s *fileStore) openFile(a *agentLog) {
	if a.file != nil || a.closed {
		return
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if fi, err := f.Stat(); err == nil {
		a.size = fi.Size()
	}
	a.file = f
}

// rotate shifts <agent>.log → .1 → … → .MaxBackups (oldest dropped).
func (s *fileStore) rotate(a *agentLog) {
	if a.file != nil {
		a.file.Close()
		a.file = nil
	}
	os.Remove(a.path + "." + strconv.Itoa(s.opt.MaxBackups))
	for i := s.opt.MaxBackups - 1; i >= 1; i-- {
		os.Rename(a.path+"."+strconv.Itoa(i), a.path+"."+strconv.Itoa(i+1))
	}
	os.Rename(a.path, a.path+".1")
	a.size = 0
	s.openFile(a)
}

func encodeLine(l api.LogLine) []byte {
	b := make([]byte, 0, len(l.Text)+64)
	b = l.Time.AppendFormat(b, time.RFC3339Nano)
	b = append(b, " #"...)
	b = strconv.AppendUint(b, l.Seq, 10)
	b = append(b, " ["...)
	b = append(b, l.Stream...)
	b = append(b, "] "...)
	b = append(b, l.Text...)
	return append(b, '\n')
}

// parseLine decodes one file line (without '\n'). Seq is 0 for legacy lines.
func parseLine(s string) (api.LogLine, bool) {
	var l api.LogLine
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return l, false
	}
	t, err := time.Parse(time.RFC3339Nano, s[:sp])
	if err != nil {
		return l, false
	}
	l.Time = t.UTC()
	s = s[sp+1:]
	if strings.HasPrefix(s, "#") {
		sp = strings.IndexByte(s, ' ')
		if sp < 0 {
			return l, false
		}
		seq, err := strconv.ParseUint(s[1:sp], 10, 64)
		if err != nil {
			return l, false
		}
		l.Seq = seq
		s = s[sp+1:]
	}
	if !strings.HasPrefix(s, "[") {
		return l, false
	}
	end := strings.Index(s, "] ")
	if end < 0 {
		if strings.HasSuffix(s, "]") { // empty text, trailing space trimmed
			end = len(s) - 1
		} else {
			return l, false
		}
	}
	l.Stream = s[1:end]
	if end+2 <= len(s) {
		l.Text = s[end+2:]
	}
	return l, true
}

// readLastLines returns up to n newest parsable lines across files (newest
// file first), oldest first. Legacy lines without seq are numbered so that
// seq stays increasing.
func readLastLines(files []string, n int) []api.LogLine {
	return readLines(files, n, 0)
}

// readLines is readLastLines with an extra Seq > since filter (since applies
// only to lines that carry a persisted seq).
func readLines(files []string, n int, since uint64) []api.LogLine {
	var rev []api.LogLine // newest first
	stop := false
	for _, p := range files {
		if stop || (n > 0 && len(rev) >= n) {
			break
		}
		f, err := os.Open(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			continue
		}
		eachLineReverse(f, func(line string) bool {
			l, ok := parseLine(line)
			if !ok {
				return true
			}
			if l.Seq != 0 && l.Seq <= since {
				stop = true
				return false
			}
			rev = append(rev, l)
			return n <= 0 || len(rev) < n
		})
		f.Close()
	}
	out := make([]api.LogLine, len(rev))
	for i, l := range rev {
		out[len(rev)-1-i] = l
	}
	// Fill in seq for legacy lines (0): continue from the previous known seq.
	var prev uint64
	for i := range out {
		if out[i].Seq == 0 {
			out[i].Seq = prev + 1
		}
		if out[i].Seq <= prev { // keep strictly increasing
			out[i].Seq = prev + 1
		}
		prev = out[i].Seq
	}
	return out
}

// eachLineReverse calls fn for every line of f from the last to the first
// until fn returns false. It reads backwards in chunks.
func eachLineReverse(f *os.File, fn func(string) bool) {
	fi, err := f.Stat()
	if err != nil {
		return
	}
	const chunk = 64 * 1024
	pos := fi.Size()
	var carry []byte // partial line (start not yet seen)
	buf := make([]byte, chunk)
	for pos > 0 {
		sz := int64(chunk)
		if pos < sz {
			sz = pos
		}
		pos -= sz
		if _, err := f.ReadAt(buf[:sz], pos); err != nil && err != io.EOF {
			return
		}
		data := append(append([]byte{}, buf[:sz]...), carry...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			line := data[i+1:]
			data = data[:i]
			if len(line) > 0 && !fn(string(line)) {
				return
			}
		}
		carry = data
	}
	if len(carry) > 0 {
		fn(string(carry))
	}
}

func (s *fileStore) Append(agentID, stream, text string) api.LogLine {
	a := s.get(agentID)
	a.mu.Lock()
	defer a.mu.Unlock()
	s.subMu.RLock()
	subs := make([]func(api.LogLine), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	s.subMu.RUnlock()
	var l api.LogLine
	for _, t := range SplitText(text) {
		a.seq++
		l = api.LogLine{AgentID: agentID, Seq: a.seq, Time: time.Now().UTC(), Stream: stream, Text: t}
		a.ring.Add(l)
		if !a.closed {
			s.openFile(a)
			if a.file != nil {
				b := encodeLine(l)
				if a.size > 0 && a.size+int64(len(b)) > s.opt.MaxFileSize {
					s.rotate(a)
				}
				if a.file != nil {
					if n, err := a.file.Write(b); err == nil {
						a.size += int64(n)
					}
				}
			}
		}
		// Subscribers run under the agent lock so per-agent order is preserved
		// even when stdout and stderr are appended concurrently.
		for _, fn := range subs {
			fn(l)
		}
	}
	return l
}

func (s *fileStore) Tail(agentID string, n int, since uint64) []api.LogLine {
	a := s.get(agentID)
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.ring.Tail(n, since)
	// The ring holds everything unless it is full; older lines live on disk.
	// n<=0 means "everything in memory" (never scans the files).
	first, ok := a.ring.First()
	if n <= 0 || !ok || a.ring.Len() < a.ring.Cap() || first.Seq <= since+1 || (n > 0 && len(out) >= n) {
		return out
	}
	lines := readLines(s.files(a.path), n, since)
	for i := range lines {
		lines[i].AgentID = agentID
	}
	return lines
}

func (s *fileStore) Subscribe(fn func(api.LogLine)) func() {
	s.subMu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.subMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { s.subMu.Lock(); delete(s.subs, id); s.subMu.Unlock() })
	}
}

func (s *fileStore) Remove(agentID string) {
	s.mu.Lock()
	a, ok := s.agts[agentID]
	delete(s.agts, agentID)
	s.mu.Unlock()
	if ok {
		a.mu.Lock()
		if a.file != nil {
			a.file.Close()
			a.file = nil
		}
		a.closed = true
		a.mu.Unlock()
	}
}

func (s *fileStore) Close() error {
	s.mu.Lock()
	s.done = true
	agts := make([]*agentLog, 0, len(s.agts))
	for _, a := range s.agts {
		agts = append(agts, a)
	}
	s.mu.Unlock()
	var errs []error
	for _, a := range agts {
		a.mu.Lock()
		if a.file != nil {
			if err := a.file.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close %s: %w", a.path, err))
			}
			a.file = nil
		}
		a.closed = true
		a.mu.Unlock()
	}
	return errors.Join(errs...)
}
