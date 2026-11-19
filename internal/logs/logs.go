// Package logs stores agent output on the node (ring buffer + log file) and
// provides a small ring type reused by the server. Owned by module M3.
// The Store interface is a FROZEN contract.
package logs

import (
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
)

// Store is the node-side per-agent log store.
type Store interface {
	// Append records one line (without trailing newline) and returns it with Seq/Time set.
	Append(agentID, stream, text string) api.LogLine
	// Tail returns up to n most recent lines with Seq > since (oldest first).
	Tail(agentID string, n int, since uint64) []api.LogLine
	// Subscribe registers fn for every appended line; call cancel to stop.
	// fn must not block.
	Subscribe(fn func(api.LogLine)) (cancel func())
	// Remove drops in-memory state for an agent (files are kept).
	Remove(agentID string)
	Close() error
}

const DefaultRingSize = 2000

// Ring is a fixed-size, concurrency-safe ring of log lines.
type Ring struct {
	mu    sync.Mutex
	buf   []api.LogLine
	start int
	n     int
}

func NewRing(size int) *Ring {
	if size <= 0 {
		size = DefaultRingSize
	}
	return &Ring{buf: make([]api.LogLine, size)}
}

func (r *Ring) Add(l api.LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(l)
}

func (r *Ring) add(l api.LogLine) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = l
		r.n++
		return
	}
	r.buf[r.start] = l
	r.start = (r.start + 1) % len(r.buf)
}

// AddNew adds l unless it duplicates a line already seen (Seq <= last Seq and
// within the ring's window). A Seq far below the last one is treated as a
// counter reset (e.g. log files deleted on the node) and accepted.
// It reports whether the line was added.
func (r *Ring) AddNew(l api.LogLine) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n > 0 {
		last := r.buf[(r.start+r.n-1)%len(r.buf)].Seq
		first := r.buf[r.start].Seq
		if l.Seq <= last && l.Seq >= first {
			return false
		}
	}
	r.add(l)
	return true
}

// Len returns the number of lines held.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Cap returns the ring capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Last returns the newest line (ok=false when empty).
func (r *Ring) Last() (api.LogLine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return api.LogLine{}, false
	}
	return r.buf[(r.start+r.n-1)%len(r.buf)], true
}

// First returns the oldest line (ok=false when empty).
func (r *Ring) First() (api.LogLine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return api.LogLine{}, false
	}
	return r.buf[r.start], true
}

// Tail returns up to n lines with Seq > since, oldest first. n<=0 means all.
func (r *Ring) Tail(n int, since uint64) []api.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Lines are stored in seq order: walk backwards and stop early.
	cnt := 0
	for i := r.n - 1; i >= 0; i-- {
		if r.buf[(r.start+i)%len(r.buf)].Seq <= since {
			break
		}
		cnt++
		if n > 0 && cnt == n {
			break
		}
	}
	out := make([]api.LogLine, cnt)
	for i := 0; i < cnt; i++ {
		out[i] = r.buf[(r.start+r.n-cnt+i)%len(r.buf)]
	}
	return out
}

// MaxLineBytes is the longest text stored in one LogLine; longer input is
// split into several lines.
const MaxLineBytes = 64 * 1024

// SplitText normalises raw output into storable line texts: invalid UTF-8 is
// replaced with U+FFFD, embedded newlines start new lines, and texts longer
// than MaxLineBytes are split on rune boundaries. ANSI escapes pass through.
func SplitText(text string) []string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= MaxLineBytes && !strings.ContainsRune(text, '\n') {
		return []string{text}
	}
	var out []string
	for _, part := range strings.Split(text, "\n") {
		part = strings.TrimSuffix(part, "\r")
		for len(part) > MaxLineBytes {
			cut := MaxLineBytes
			for cut > 0 && !runeStart(part[cut]) {
				cut--
			}
			if cut == 0 {
				cut = MaxLineBytes
			}
			out = append(out, part[:cut])
			part = part[cut:]
		}
		out = append(out, part)
	}
	return out
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }

// FormatLine renders a line as human-readable text (used for downloads).
func FormatLine(l api.LogLine) string {
	return l.Time.UTC().Format(time.RFC3339Nano) + " [" + l.Stream + "] " + l.Text
}
