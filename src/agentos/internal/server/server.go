// Package server implements `agentos server`: REST + WebSocket API, the node
// control hub and the embedded web UI. Core files (server.go, hub.go,
// events.go, handlers_nodes.go, handlers_agents.go, util.go) are owned by the
// lead/integrator; handlers_<area>.go files are owned by the modules listed in
// docs/modules.md and each registers its own routes via routes<Area>().
package server

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/logs"
	"github.com/agentos/agentos/internal/stacks"
	"github.com/agentos/agentos/internal/store"
	"github.com/agentos/agentos/internal/version"
)

type Config struct {
	Listen string // e.g. ":7800"
	WebFS  fs.FS  // static UI root (index.html at top level); nil disables UI
	// DataDir holds server-side state files (secret.key, envlogs/); "" = in-memory/ephemeral.
	DataDir string
	// DistDir holds agentos-<os>-<arch>[.exe] binaries served by /download/ ("" = only own binary).
	DistDir string
}

type Server struct {
	cfg     Config
	st      store.Store
	auth    auth.Authenticator
	hub     *Hub
	bus     *Bus
	version string

	mu       sync.RWMutex
	statuses map[string]api.AgentStatus // agentID -> latest runtime status
	stats    *statsState                // owned by handlers_stats.go (M2)
	logRings map[string]*logs.Ring      // owned by handlers_logs.go (M3)
	envs     *envManager                // environments (envs.go, handlers_envs.go)
	stacksH  *stacks.Handler            // stack installs (handlers_stacks.go)
	fed      *fedState                 // A2A/ACP federation (handlers_agents_fed.go)
}

func New(cfg Config, st store.Store, a auth.Authenticator) *Server {
	s := &Server{
		cfg: cfg, st: st, auth: a, hub: NewHub(), bus: NewBus(), version: version.Version,
		statuses: map[string]api.AgentStatus{}, stats: newStatsState(), logRings: map[string]*logs.Ring{},
	}
	s.envs = newEnvManager(s, cfg.DataDir)
	s.initFed()
	return s
}

// Handler builds the HTTP routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.Handler { return s.auth.RequireSession(h) }

	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": s.version})
	})
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"version": version.Version, "commit": version.Commit, "date": version.Date, "protocol": api.ProtocolVersion})
	})
	// node-facing endpoints
	mux.Handle("GET "+api.PathNodeConnect, s.requireNode(http.HandlerFunc(s.handleNodeConnect)))
	mux.Handle("GET "+api.PathNodeTunnel+"{session}", s.requireNode(http.HandlerFunc(s.handleNodeTunnel)))

	s.routesAuth(mux, authed)                                                                                      // handlers_auth.go    (M7)
	s.routesNodes(mux, authed)                                                                                     // handlers_nodes.go   (core)
	s.routesAgents(mux, authed)                                                                                    // handlers_agents.go  (core)
	s.routesAgentsFed(mux, authed)                                                                                 // handlers_agents_fed.go
	s.routesLogs(mux, authed)                                                                                      // handlers_logs.go    (M3)
	s.routesStats(mux, authed)                                                                                     // handlers_stats.go   (M2)
	s.routesDesktop(mux, authed)                                                                                   // handlers_desktop.go (M4)
	s.routesEvents(mux, authed)                                                                                    // events.go           (core)
	s.routesEnvs(mux, authed)                                                                                      // handlers_envs.go    (core, phase 2)
	s.stacksH = s.routesStacks(mux, authed, stacks.Deps{Open: s.openEnv, Log: s.envLog, Done: s.onStackInstalled, AfterDetect: s.afterStackDetect}) // handlers_stacks.go (E)
	s.routesPresets(mux, authed)                                                                                   // presets.go

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	if s.cfg.WebFS != nil {
		mux.Handle("/", spaHandler(s.cfg.WebFS))
	}
	return logRequests(mux)
}

// spaHandler serves static files and falls back to index.html.
func spaHandler(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(root, p); err != nil {
				r.URL.Path = "/"
			}
		}
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Upgrade") == "" {
			log.Printf("http: %s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// ---- runtime state helpers (core) ----

func (s *Server) setStatus(st api.AgentStatus) {
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	if st.Stats == nil {
		if old, ok := s.statuses[st.ID]; ok && st.State == api.StateRunning {
			st.Stats = old.Stats
		}
	}
	s.statuses[st.ID] = st
	s.mu.Unlock()
	s.bus.Publish(api.EventAgentStatus, st)
}

func (s *Server) status(spec api.AgentSpec) api.AgentStatus {
	s.mu.RLock()
	st, ok := s.statuses[spec.ID]
	s.mu.RUnlock()
	if !ok {
		st = api.AgentStatus{ID: spec.ID, NodeID: spec.NodeID, State: api.StateStopped}
	}
	if !s.hub.Online(spec.NodeID) {
		st.State = api.StateUnknown
	}
	return st
}

func (s *Server) onNodeOffline(nodeID string) {
	s.publishNode(nodeID)
	go s.envs.nodeChanged(nodeID)
	specs, _ := s.st.ListAgents(context.Background(), nodeID)
	for _, sp := range specs {
		s.bus.Publish(api.EventAgentStatus, s.status(sp))
	}
}

func (s *Server) nodeView(n api.Node) api.Node {
	if s.hub.Online(n.ID) {
		n.Status = api.NodeOnline
	} else {
		n.Status = api.NodeOffline
	}
	if h, ok := s.stats.latestHost(n.ID); ok {
		n.Host = &h
	}
	if n.Capabilities == nil {
		n.Capabilities = []string{}
	}
	return n
}

func (s *Server) publishNode(nodeID string) {
	n, err := s.st.GetNode(context.Background(), nodeID)
	if err == nil {
		s.bus.Publish(api.EventNodeStatus, s.nodeView(n))
	}
}
