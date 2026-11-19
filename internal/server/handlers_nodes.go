package server

import (
	"net/http"

	"github.com/agentos/agentos/internal/api"
)

func (s *Server) routesNodes(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/v1/nodes", authed(func(w http.ResponseWriter, r *http.Request) {
		nodes, err := s.st.ListNodes(r.Context())
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		for i := range nodes {
			nodes[i] = s.nodeView(nodes[i])
		}
		writeJSON(w, 200, nodes)
	}))
	mux.Handle("GET /api/v1/nodes/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		n, err := s.st.GetNode(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, 200, s.nodeView(n))
	}))
	mux.Handle("PATCH /api/v1/nodes/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &body); err != nil || body.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		n, err := s.st.GetNode(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		n.Name = body.Name
		if err := s.st.UpsertNode(r.Context(), n); err != nil {
			writeStoreErr(w, err)
			return
		}
		s.publishNode(n.ID)
		writeJSON(w, 200, s.nodeView(n))
	}))
	// DELETE removes an offline node and all its agent specs.
	mux.Handle("DELETE /api/v1/nodes/{id}", authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if s.hub.Online(id) {
			writeErr(w, 409, "node_online", "stop the node before deleting it")
			return
		}
		specs, _ := s.st.ListAgents(r.Context(), id)
		for _, a := range specs {
			s.st.DeleteAgent(r.Context(), a.ID)
			s.dropAgentStats(a.ID)
			s.bus.Publish(api.EventAgentRemoved, map[string]string{"id": a.ID})
		}
		if err := s.st.DeleteNode(r.Context(), id); err != nil {
			writeStoreErr(w, err)
			return
		}
		s.dropNodeStats(id)
		s.bus.Publish(api.EventNodeRemoved, map[string]string{"id": id})
		w.WriteHeader(http.StatusNoContent)
	}))
}
