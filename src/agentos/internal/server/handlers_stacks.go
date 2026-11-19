package server

import (
	"net/http"

	"github.com/agentos/agentos/internal/stacks"
)

// routesStacks mounts the Stacks API (docs/api-envs.md "Stacks"):
//
//	GET  /api/v1/stacks
//	GET  /api/v1/envs/{id}/stacks
//	POST /api/v1/envs/{id}/stacks/{stack}/install   (202; progress via deps.Log -> env logs + "env.log" events)
//
// Wiring (one line in Server.Handler, next to the other routes*):
//
//	s.routesStacks(mux, authed, stacks.Deps{Open: s.openEnv, Log: s.envLog, Done: s.onStackInstalled})
//
// where openEnv(ctx, id) (env.Env, release func(), error) resolves Record -> Provider.Open,
// envLog(id, level, text) appends to /envs/{id}/logs and publishes "env.log", and Done is optional.
func (s *Server) routesStacks(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler, deps stacks.Deps) *stacks.Handler {
	h := stacks.NewHandler(deps)
	h.Register(mux, authed)
	return h
}
