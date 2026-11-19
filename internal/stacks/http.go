package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/env"
)

// Deps wires the stacks HTTP handlers into the server. Only Open is required.
type Deps struct {
	// Open returns a live handle for env id; release is called when the caller is done
	// (may be nil). Return an error wrapping env.ErrNotFound for unknown ids.
	Open func(ctx context.Context, envID string) (e env.Env, release func(), err error)
	// Log appends one line to the env's provisioning log (/envs/{id}/logs) and should
	// publish the "env.log" event. level: info | warn | error.
	Log func(envID, level, text string)
	// Done is called once an install finishes (err nil on success); optional, e.g. to
	// publish env.updated or set Record.Stack.
	Done func(envID, stackID string, err error)
	// AfterDetect is called after GET /envs/{id}/stacks detection with the list of
	// installed stack ids (optional; used to auto-heal env config flags).
	AfterDetect func(envID string, installed []string)
	// InstallTimeout bounds a whole install job (default 60 min).
	InstallTimeout time.Duration
}

// Handler serves the Stacks API (docs/api-envs.md):
//
//	GET  /api/v1/stacks                                 -> [View]
//	GET  /api/v1/envs/{id}/stacks                       -> EnvStacks
//	POST /api/v1/envs/{id}/stacks/{stack}/install       -> 202 InstallAccepted
//
// Authentication is the caller's job (see Register).
type Handler struct {
	d       Deps
	mu      sync.Mutex
	running map[string]string // env id -> stack id being installed
}

// NewHandler builds the handler.
func NewHandler(d Deps) *Handler {
	if d.InstallTimeout == 0 {
		d.InstallTimeout = 60 * time.Minute
	}
	if d.Log == nil {
		d.Log = func(string, string, string) {}
	}
	return &Handler{d: d, running: map[string]string{}}
}

// Register mounts the routes on mux, wrapping each with wrap (e.g. session auth).
// wrap may be nil.
func (h *Handler) Register(mux *http.ServeMux, wrap func(http.HandlerFunc) http.Handler) {
	if wrap == nil {
		wrap = func(f http.HandlerFunc) http.Handler { return f }
	}
	mux.Handle("GET /api/v1/stacks", wrap(h.list))
	mux.Handle("GET /api/v1/envs/{id}/stacks", wrap(h.detect))
	mux.Handle("POST /api/v1/envs/{id}/stacks/{stack}/install", wrap(h.install))
}

// ServeHTTP lets the handler be mounted standalone (no auth).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.Register(mux, nil)
	mux.ServeHTTP(w, r)
}

// EnvStacks is the GET /envs/{id}/stacks body.
type EnvStacks struct {
	EnvID      string     `json:"env_id"`
	OS         OSInfo     `json:"os"`
	Stacks     []Detected `json:"stacks"`               // every catalog stack with installed/version
	Installed  []string   `json:"installed"`            // ids of installed stacks
	Installing string     `json:"installing,omitempty"` // stack id of a running install job
}

// InstallAccepted is the 202 body of POST .../install.
type InstallAccepted struct {
	EnvID  string `json:"env_id"`
	Stack  string `json:"stack"`
	Status string `json:"status"` // "installing"
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Views())
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request, id string) (env.Env, func(), bool) {
	if h.d.Open == nil {
		writeErr(w, http.StatusNotImplemented, "not_implemented", "环境服务未就绪")
		return nil, nil, false
	}
	e, release, err := h.d.Open(r.Context(), id)
	if err != nil {
		if errors.Is(err, env.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "环境不存在")
		} else if errors.Is(err, env.ErrUnsupported) {
			writeErr(w, http.StatusConflict, "unsupported", "该环境不支持执行命令（exec）")
		} else {
			writeErr(w, http.StatusConflict, "env_unavailable", "无法连接环境："+err.Error())
		}
		return nil, nil, false
	}
	if release == nil {
		release = func() {}
	}
	return e, release, true
}

func (h *Handler) detect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, release, ok := h.open(w, r, id)
	if !ok {
		return
	}
	defer release()
	ds, osi, err := Detect(r.Context(), e)
	if err != nil {
		writeErr(w, http.StatusConflict, "detect_failed", "检测失败："+err.Error())
		return
	}
	h.mu.Lock()
	installing := h.running[id]
	h.mu.Unlock()
	// While a stack is still marked installing, do not report it as finally installed.
	// Install's post-step detect can succeed before Done/unmark; clients must wait.
	for i := range ds {
		if installing != "" && ds[i].ID == installing {
			ds[i].Installed = false
			ds[i].Version = ""
			ds[i].Output = ""
		}
	}
	res := EnvStacks{EnvID: id, OS: osi, Stacks: ds, Installed: []string{}, Installing: installing}
	for _, d := range ds {
		if d.Installed {
			res.Installed = append(res.Installed, d.ID)
		}
	}
	if h.d.AfterDetect != nil {
		h.d.AfterDetect(id, append([]string{}, res.Installed...))
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	id, stackID := r.PathValue("id"), r.PathValue("stack")
	s, ok := Get(stackID)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "未知的技术栈："+stackID)
		return
	}
	h.mu.Lock()
	if cur, busy := h.running[id]; busy {
		h.mu.Unlock()
		writeErr(w, http.StatusConflict, "busy", "该环境正在安装「"+cur+"」，请等待完成")
		return
	}
	h.running[id] = stackID
	h.mu.Unlock()
	unmark := func() { h.mu.Lock(); delete(h.running, id); h.mu.Unlock() }

	e, release, ok := h.open(w, r, id)
	if !ok {
		unmark()
		return
	}
	go func() {
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), h.d.InstallTimeout)
		defer cancel()
		logf := func(level, text string) { h.d.Log(id, level, "[stack:"+stackID+"] "+text) }
		err := Install(ctx, e, s.ID, logf)
		// Done first (e.g. virtual_desktop flag), then clear installing so clients
		// that wait for installing=="" see a consistent post-install config.
		if h.d.Done != nil {
			h.d.Done(id, stackID, err)
		}
		unmark()
	}()
	writeJSON(w, http.StatusAccepted, InstallAccepted{EnvID: id, Stack: stackID, Status: "installing"})
}

// StartInstall starts installing stackID into env id in the background (same
// job tracking, logging and Done callback as POST .../install). Used by the
// server to install a stack chosen at environment creation.
func (h *Handler) StartInstall(ctx context.Context, id, stackID string) error {
	s, ok := Get(stackID)
	if !ok {
		return fmt.Errorf("unknown stack %q", stackID)
	}
	if h.d.Open == nil {
		return errors.New("stacks: no env opener")
	}
	h.mu.Lock()
	if cur, busy := h.running[id]; busy {
		h.mu.Unlock()
		return fmt.Errorf("busy installing %q", cur)
	}
	h.running[id] = stackID
	h.mu.Unlock()
	unmark := func() { h.mu.Lock(); delete(h.running, id); h.mu.Unlock() }
	e, release, err := h.d.Open(ctx, id)
	if err != nil {
		unmark()
		return err
	}
	if release == nil {
		release = func() {}
	}
	go func() {
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), h.d.InstallTimeout)
		defer cancel()
		logf := func(level, text string) { h.d.Log(id, level, "[stack:"+stackID+"] "+text) }
		err := Install(ctx, e, s.ID, logf)
		if h.d.Done != nil {
			h.d.Done(id, stackID, err)
		}
		unmark()
	}()
	return nil
}

// Busy reports the stack being installed in env id, if any.
func (h *Handler) Busy(envID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.running[envID]
	return s, ok
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, api.ErrorResponse{Error: msg, Code: errCode})
}
