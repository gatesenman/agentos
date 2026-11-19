package server

// Owned by module M3 (log streaming).

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/logs"
	"github.com/gorilla/websocket"
)

const (
	serverRingSize  = 1000
	logsMaxTail     = 5000
	logsDefaultTail = 200
)

// recordLogs is called by the hub for every agent.log batch from a node.
// Lines already seen (same seq) are dropped so the ring and live streams
// never carry duplicates.
func (s *Server) recordLogs(b api.LogBatch) {
	s.mu.Lock()
	ring, ok := s.logRings[b.AgentID]
	if !ok {
		ring = logs.NewRing(serverRingSize)
		s.logRings[b.AgentID] = ring
	}
	s.mu.Unlock()
	for _, l := range b.Lines {
		if l.AgentID == "" {
			l.AgentID = b.AgentID
		}
		if ring.AddNew(l) {
			s.bus.Publish(api.EventAgentLog, l)
		}
	}
}

// logQuery holds the parsed ?tail=&since=&q= parameters.
type logQuery struct {
	tail      int
	sinceSeq  uint64    // since=<seq>
	sinceTime time.Time // since=<RFC3339>
	q         string    // lower-cased substring
}

func parseLogQuery(r *http.Request, defTail int) (logQuery, error) {
	v := r.URL.Query()
	lq := logQuery{tail: defTail, q: strings.ToLower(v.Get("q"))}
	if t := v.Get("tail"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil {
			return lq, fmt.Errorf("invalid tail")
		}
		if n > 0 {
			lq.tail = min(n, logsMaxTail)
		}
	}
	if sv := v.Get("since"); sv != "" {
		if n, err := strconv.ParseUint(sv, 10, 64); err == nil {
			lq.sinceSeq = n
		} else if t, err := time.Parse(time.RFC3339Nano, sv); err == nil {
			lq.sinceTime = t
		} else {
			return lq, fmt.Errorf("invalid since (want seq number or RFC3339 time)")
		}
	}
	return lq, nil
}

func (q logQuery) filtered() bool { return q.q != "" || !q.sinceTime.IsZero() }

func (q logQuery) match(l api.LogLine) bool {
	if l.Seq <= q.sinceSeq {
		return false
	}
	if !q.sinceTime.IsZero() && !l.Time.After(q.sinceTime) {
		return false
	}
	return q.q == "" || strings.Contains(strings.ToLower(l.Text), q.q)
}

// fetchLogs prefers the node (authoritative), falling back to the server ring.
func (s *Server) fetchLogs(ctx context.Context, spec api.AgentSpec, tail int, since uint64) []api.LogLine {
	var resp api.LogsResponse
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.hub.Request(ctx, spec.NodeID, api.MsgAgentLogs, api.LogsRequest{ID: spec.ID, Tail: tail, Since: since}, &resp); err == nil && resp.Lines != nil {
		return resp.Lines
	}
	s.mu.RLock()
	ring := s.logRings[spec.ID]
	s.mu.RUnlock()
	if ring == nil {
		return []api.LogLine{}
	}
	return ring.Tail(tail, since)
}

// queryLogs returns up to q.tail newest lines matching q, oldest first.
// With a text/time filter the newest logsMaxTail lines are scanned.
func (s *Server) queryLogs(ctx context.Context, spec api.AgentSpec, q logQuery) []api.LogLine {
	if !q.filtered() {
		return s.fetchLogs(ctx, spec, q.tail, q.sinceSeq)
	}
	lines := s.fetchLogs(ctx, spec, logsMaxTail, q.sinceSeq)
	out := make([]api.LogLine, 0, len(lines))
	for _, l := range lines {
		if q.match(l) {
			out = append(out, l)
		}
	}
	if len(out) > q.tail {
		out = out[len(out)-q.tail:]
	}
	return out
}

var unsafeFileChars = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func (s *Server) routesLogs(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	// GET /api/v1/agents/{id}/logs?tail=200&since=<seq|RFC3339>&q=<substring> -> {"lines":[...]}
	mux.Handle("GET /api/v1/agents/{id}/logs", authed(func(w http.ResponseWriter, r *http.Request) {
		spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		q, err := parseLogQuery(r, logsDefaultTail)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeJSON(w, 200, api.LogsResponse{Lines: s.queryLogs(r.Context(), spec, q)})
	}))
	// GET /api/v1/agents/{id}/logs/download?tail=5000&since=&q= -> text/plain attachment.
	mux.Handle("GET /api/v1/agents/{id}/logs/download", authed(func(w http.ResponseWriter, r *http.Request) {
		spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		q, err := parseLogQuery(r, logsMaxTail)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		lines := s.queryLogs(r.Context(), spec, q)
		name := unsafeFileChars.ReplaceAllString(spec.Name, "_")
		if name == "" || name == "_" {
			name = "agent"
		}
		fn := fmt.Sprintf("%s-%s.log", name, time.Now().UTC().Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="agent-%s.log"; filename*=UTF-8''%s`, spec.ID, urlPathEscape(fn)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		bw := bufio.NewWriterSize(w, 64*1024)
		for _, l := range lines {
			bw.WriteString(logs.FormatLine(l))
			bw.WriteByte('\n')
		}
		bw.Flush()
	}))
	// GET /api/v1/agents/{id}/logs/stream?tail=200&since=&q= (WebSocket): agent.log events,
	// first the tail, then live lines; deduplicated by seq.
	mux.Handle("GET /api/v1/agents/{id}/logs/stream", authed(func(w http.ResponseWriter, r *http.Request) {
		spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		q, err := parseLogQuery(r, logsDefaultTail)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		s.streamLogs(w, r, spec, q)
	}))
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c < 0x80 && (c == '-' || c == '.' || c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// logDedupe tracks the seq window already sent to one client. A line is a
// duplicate when its seq falls in [first, last]; a seq below first is a
// counter reset on the node (e.g. log files wiped) and starts a new window.
type logDedupe struct {
	first, last uint64
	any         bool
}

func (d *logDedupe) accept(seq uint64) bool {
	if d.any && seq >= d.first && seq <= d.last {
		return false
	}
	if !d.any || seq < d.first {
		d.first = seq
	}
	d.last = seq
	d.any = true
	return true
}

// streamLogs subscribes to the bus *before* fetching the initial tail so no
// line is lost in between, then drops live lines already sent in the tail.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, spec api.AgentSpec, q logQuery) {
	ch, cancel := s.bus.Subscribe(func(e api.Event) bool {
		l, ok := e.Payload.(api.LogLine)
		return ok && e.Type == api.EventAgentLog && l.AgentID == spec.ID
	})
	defer cancel()
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	done := make(chan struct{})
	go func() { // reader: detect close
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	var d logDedupe
	send := func(l api.LogLine) bool {
		if !d.accept(l.Seq) || !q.match(l) {
			return true
		}
		c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return c.WriteJSON(api.Event{Type: api.EventAgentLog, Time: l.Time, Payload: l}) == nil
	}
	for _, l := range s.queryLogs(r.Context(), spec, q) {
		if !send(l) {
			return
		}
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-done:
			return
		case <-ping.C:
			if c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !send(ev.Payload.(api.LogLine)) {
				return
			}
		}
	}
}
