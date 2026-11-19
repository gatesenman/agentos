package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter throttles failed login attempts per key (client IP).
// After Max failures inside Window the key is locked for Window; each
// further lockout doubles the lock (exponential backoff) up to MaxLock.
// A successful login resets the key.
type RateLimiter struct {
	Max     int
	Window  time.Duration
	MaxLock time.Duration
	Now     func() time.Time // for tests

	mu      sync.Mutex
	entries map[string]*rlEntry
}

type rlEntry struct {
	fails       []time.Time
	strikes     int
	lockedUntil time.Time
	last        time.Time
}

// NewRateLimiter returns the default policy: 5 failures/minute, backoff up to 15 min.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{Max: 5, Window: time.Minute, MaxLock: 15 * time.Minute}
}

func (l *RateLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow reports whether key may attempt a login now; if not, retryAfter is
// the remaining lock time.
func (l *RateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return true, 0
	}
	if d := e.lockedUntil.Sub(l.now()); d > 0 {
		return false, d
	}
	return true, 0
}

// Failure records a failed attempt.
func (l *RateLimiter) Failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.entries == nil {
		l.entries = map[string]*rlEntry{}
	}
	l.gc(now)
	e := l.entries[key]
	if e == nil {
		e = &rlEntry{}
		l.entries[key] = e
	}
	// Forget earlier lockouts after a long quiet period.
	if e.strikes > 0 && now.Sub(e.last) > l.MaxLock+l.Window {
		e.strikes = 0
	}
	e.last = now
	keep := e.fails[:0]
	for _, t := range e.fails {
		if now.Sub(t) < l.Window {
			keep = append(keep, t)
		}
	}
	e.fails = append(keep, now)
	if len(e.fails) >= l.Max {
		e.strikes++
		lock := l.Window
		for i := 1; i < e.strikes && lock < l.MaxLock; i++ {
			lock *= 2
		}
		if lock > l.MaxLock {
			lock = l.MaxLock
		}
		e.lockedUntil = now.Add(lock)
		e.fails = e.fails[:0]
	}
}

// Success resets the key.
func (l *RateLimiter) Success(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// gc drops idle entries when the table grows large (caller holds mu).
func (l *RateLimiter) gc(now time.Time) {
	if len(l.entries) < 4096 {
		return
	}
	for k, e := range l.entries {
		if now.Sub(e.last) > l.MaxLock+l.Window && now.After(e.lockedUntil) {
			delete(l.entries, k)
		}
	}
}

// ClientIP returns the remote IP of r (proxy headers are not trusted).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// IsLoopbackRequest reports whether r came from 127.0.0.1 / ::1 (no proxy
// headers). Used to gate passwordless local-desktop session issue.
func IsLoopbackRequest(r *http.Request) bool {
	ip := net.ParseIP(ClientIP(r))
	return ip != nil && ip.IsLoopback()
}

