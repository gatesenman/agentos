package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/google/uuid"
)

func (s *Server) agentView(spec api.AgentSpec) api.Agent {
	return api.Agent{AgentSpec: spec, Status: s.status(spec), Kind: "local"}
}

func validateSpec(a *api.AgentSpec) string {
	a.Name = strings.TrimSpace(a.Name)
	a.Command = strings.TrimSpace(a.Command)
	switch {
	case a.Name == "":
		return "name is required"
	case a.Command == "":
		return "command is required"
	case a.NodeID == "":
		return "node_id is required"
	}
	switch a.Restart {
	case "":
		a.Restart = api.RestartNever
	case api.RestartNever, api.RestartOnFailure, api.RestartAlways:
	default:
		return "restart must be never|on-failure|always"
	}
	if a.MaxRestarts < 0 || a.RestartDelaySec < 0 {
		return "max_restarts and restart_delay_sec must be >= 0"
	}
	return ""
}

// pushSpec applies the spec on its node if online (best effort).
func (s *Server) pushSpec(ctx context.Context, spec api.AgentSpec) {
	var st api.AgentStatus
	if err := s.hub.Request(ctx, spec.NodeID, api.MsgAgentApply, spec, &st); err == nil {
		s.setStatus(st)
	}
}

func (s *Server) routesAgents(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/v1/agents", authed(func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.URL.Query().Get("node_id")
		if envID := r.URL.Query().Get("env_id"); envID != "" {
			row, err := s.envs.get(r.Context(), envID)
			if err != nil {
				writeStoreErr(w, err)
				return
			}
			if row.NodeID == "" || (nodeID != "" && nodeID != row.NodeID) {
				writeJSON(w, 200, []api.Agent{})
				return
			}
			nodeID = row.NodeID
		}
		specs, err := s.st.ListAgents(r.Context(), nodeID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		kind := r.URL.Query().Get("kind") // local|a2a|acp|"" (all)
		out := make([]api.Agent, 0, len(specs))
		if kind == "" || kind == "local" {
			for _, sp := range specs {
				out = append(out, s.agentView(sp))
			}
		}
		if (kind == "" || kind == "a2a" || kind == "acp") && s.fed != nil && s.fed.fs != nil && nodeID == "" {
			proto := kind
			if kind == "" {
				proto = ""
			}
			if rows, err := s.fed.fs.ListFedAgents(r.Context(), proto); err == nil {
				for _, row := range rows {
					out = append(out, s.fedAsAgent(row))
				}
			}
		}
		writeJSON(w, 200, out)
	}))
	mux.Handle("POST /api/v1/agents", authed(func(w http.ResponseWriter, r *http.Request) {
		var spec api.AgentSpec
		if err := readJSON(r, &spec); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
			return
		}
		if msg := validateSpec(&spec); msg != "" {
			writeErr(w, 400, "bad_request", msg)
			return
		}
		if _, err := s.st.GetNode(r.Context(), spec.NodeID); err != nil {
			writeErr(w, 400, "bad_request", "unknown node_id")
			return
		}
		now := time.Now().UTC()
		spec.ID, spec.CreatedAt, spec.UpdatedAt = uuid.NewString(), now, now
		if err := s.st.CreateAgent(r.Context(), spec); err != nil {
			writeStoreErr(w, err)
			return
		}
		s.bus.Publish(api.EventAgentSpec, spec)
		s.pushSpec(r.Context(), spec)
		writeJSON(w, 201, s.agentView(spec))
	}))
	mux.Handle("GET /api/v1/agents/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, 200, s.agentView(spec))
	}))
	mux.Handle("PUT /api/v1/agents/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		old, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		var spec api.AgentSpec
		if err := readJSON(r, &spec); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
			return
		}
		if spec.NodeID == "" {
			spec.NodeID = old.NodeID
		}
		if msg := validateSpec(&spec); msg != "" {
			writeErr(w, 400, "bad_request", msg)
			return
		}
		spec.ID, spec.CreatedAt, spec.UpdatedAt = old.ID, old.CreatedAt, time.Now().UTC()
		if err := s.st.UpdateAgent(r.Context(), spec); err != nil {
			writeStoreErr(w, err)
			return
		}
		if old.NodeID != spec.NodeID {
			s.hub.Request(r.Context(), old.NodeID, api.MsgAgentRemove, api.AgentRef{ID: old.ID}, nil)
		}
		s.bus.Publish(api.EventAgentSpec, spec)
		s.pushSpec(r.Context(), spec)
		writeJSON(w, 200, s.agentView(spec))
	}))
	mux.Handle("DELETE /api/v1/agents/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		// Offline nodes drop it on next agent.sync.
		s.hub.Request(r.Context(), spec.NodeID, api.MsgAgentRemove, api.AgentRef{ID: spec.ID}, nil)
		if err := s.st.DeleteAgent(r.Context(), spec.ID); err != nil {
			writeStoreErr(w, err)
			return
		}
		s.mu.Lock()
		delete(s.statuses, spec.ID)
		s.mu.Unlock()
		s.dropAgentStats(spec.ID)
		s.bus.Publish(api.EventAgentRemoved, map[string]string{"id": spec.ID})
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, action := range []struct{ path, msg string }{
		{"start", api.MsgAgentStart}, {"stop", api.MsgAgentStop}, {"restart", api.MsgAgentRestart},
	} {
		msg := action.msg
		mux.Handle("POST /api/v1/agents/{id}/"+action.path, authed(func(w http.ResponseWriter, r *http.Request) {
			spec, err := s.st.GetAgent(r.Context(), r.PathValue("id"))
			if err != nil {
				writeStoreErr(w, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			var st api.AgentStatus
			if err := s.hub.Request(ctx, spec.NodeID, msg, api.AgentRef{ID: spec.ID}, &st); err != nil {
				if err == ErrNodeOffline {
					writeStoreErr(w, err)
				} else {
					writeErr(w, http.StatusBadGateway, "node_error", err.Error())
				}
				return
			}
			s.setStatus(st)
			writeJSON(w, 200, s.agentView(spec))
		}))
	}
}
