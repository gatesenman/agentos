package server

// Owned by module M2 (metrics/stats).

import (
	"net/http"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
)

const statsHistory = 120 // samples kept per node / agent (10 min at 5s)

// statsRing is a fixed-capacity in-memory ring of samples (oldest overwritten).
type statsRing[T any] struct {
	buf  []T
	next int // write position
	n    int // number of valid samples
}

func newStatsRing[T any]() *statsRing[T] { return &statsRing[T]{buf: make([]T, statsHistory)} }

func (r *statsRing[T]) push(v T) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// at returns the i-th sample, oldest first.
func (r *statsRing[T]) at(i int) T {
	return r.buf[(r.next-r.n+i+len(r.buf))%len(r.buf)]
}

func (r *statsRing[T]) latest() (T, bool) {
	var zero T
	if r == nil || r.n == 0 {
		return zero, false
	}
	return r.at(r.n - 1), true
}

// since copies samples (oldest first) whose time is strictly after t
// (all samples when t is zero).
func (r *statsRing[T]) since(t time.Time, timeOf func(T) time.Time) []T {
	out := []T{}
	if r == nil {
		return out
	}
	for i := 0; i < r.n; i++ {
		v := r.at(i)
		if t.IsZero() || timeOf(v).After(t) {
			out = append(out, v)
		}
	}
	return out
}

type statsState struct {
	mu    sync.RWMutex
	host  map[string]*statsRing[api.HostStats] // nodeID -> history
	procs map[string]*statsRing[api.ProcStats] // agentID -> history
}

func newStatsState() *statsState {
	return &statsState{host: map[string]*statsRing[api.HostStats]{}, procs: map[string]*statsRing[api.ProcStats]{}}
}

func (st *statsState) latestHost(nodeID string) (api.HostStats, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.host[nodeID].latest()
}

func hostTime(h api.HostStats) time.Time { return h.Time }
func procTime(p api.ProcStats) time.Time { return p.Time }

// recordStats is called by the hub for every node.stats event.
func (s *Server) recordStats(ns api.NodeStats) {
	now := time.Now().UTC()
	if ns.Host.Time.IsZero() {
		ns.Host.Time = now
	}
	s.stats.mu.Lock()
	hr := s.stats.host[ns.NodeID]
	if hr == nil {
		hr = newStatsRing[api.HostStats]()
		s.stats.host[ns.NodeID] = hr
	}
	hr.push(ns.Host)
	for id, ps := range ns.Agents {
		if ps.Time.IsZero() {
			ps.Time = now
			ns.Agents[id] = ps
		}
		pr := s.stats.procs[id]
		if pr == nil {
			pr = newStatsRing[api.ProcStats]()
			s.stats.procs[id] = pr
		}
		pr.push(ps)
	}
	s.stats.mu.Unlock()
	s.mu.Lock()
	for id, ps := range ns.Agents {
		if st, ok := s.statuses[id]; ok {
			ps := ps
			st.Stats = &ps
			s.statuses[id] = st
		}
	}
	s.mu.Unlock()
	s.bus.Publish(api.EventNodeStats, ns)
}

// dropAgentStats forgets the stats history of a removed agent.
// The lead wires this into agent deletion.
func (s *Server) dropAgentStats(id string) {
	s.stats.mu.Lock()
	delete(s.stats.procs, id)
	s.stats.mu.Unlock()
}

// dropNodeStats forgets the host stats history of a removed node.
// The lead wires this into node deletion.
func (s *Server) dropNodeStats(id string) {
	s.stats.mu.Lock()
	delete(s.stats.host, id)
	s.stats.mu.Unlock()
}

// parseSince reads the optional ?since=<RFC3339> query parameter.
func parseSince(r *http.Request) (time.Time, bool) {
	v := r.URL.Query().Get("since")
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func badSince(w http.ResponseWriter) {
	writeErr(w, http.StatusBadRequest, "bad_request", "since must be RFC3339 (since 参数必须为 RFC3339 时间)")
}

func (s *Server) routesStats(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	// GET /api/v1/nodes/{id}/stats[?since=RFC3339] -> {"latest":HostStats|null,"history":[...]}
	mux.Handle("GET /api/v1/nodes/{id}/stats", authed(func(w http.ResponseWriter, r *http.Request) {
		since, ok := parseSince(r)
		if !ok {
			badSince(w)
			return
		}
		s.stats.mu.RLock()
		ring := s.stats.host[r.PathValue("id")]
		h := ring.since(since, hostTime)
		l, has := ring.latest()
		s.stats.mu.RUnlock()
		var latest *api.HostStats
		if has {
			latest = &l
		}
		writeJSON(w, 200, map[string]any{"latest": latest, "history": h})
	}))
	// GET /api/v1/agents/{id}/stats[?since=RFC3339] -> {"latest":ProcStats|null,"history":[...]}
	mux.Handle("GET /api/v1/agents/{id}/stats", authed(func(w http.ResponseWriter, r *http.Request) {
		since, ok := parseSince(r)
		if !ok {
			badSince(w)
			return
		}
		s.stats.mu.RLock()
		ring := s.stats.procs[r.PathValue("id")]
		h := ring.since(since, procTime)
		l, has := ring.latest()
		s.stats.mu.RUnlock()
		var latest *api.ProcStats
		if has {
			latest = &l
		}
		writeJSON(w, 200, map[string]any{"latest": latest, "history": h})
	}))
}
