package server

// Environments REST/WS API (docs/api-envs.md, frozen v1) and /download.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/env"
	"github.com/agentos/agentos/internal/env/agentosnode"
	"github.com/agentos/agentos/internal/env/providers/agentoslocal"
	"github.com/agentos/agentos/internal/env/providers/agentosremote"
	"github.com/agentos/agentos/internal/env/providers/hyperv"
	"github.com/agentos/agentos/internal/store"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	provisionTimeout = 10 * time.Minute
	installTimeout   = 15 * time.Minute
)

func (s *Server) routesEnvs(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	m := s.envs
	withEnv := func(f func(w http.ResponseWriter, r *http.Request, row store.EnvRow)) http.Handler {
		return authed(func(w http.ResponseWriter, r *http.Request) {
			row, err := m.get(r.Context(), r.PathValue("id"))
			if err != nil {
				writeStoreErr(w, err)
				return
			}
			f(w, r, row)
		})
	}

	// ---- providers ----
	mux.Handle("GET /api/v1/providers", authed(func(w http.ResponseWriter, r *http.Request) {
		list := env.List()
		sort.Slice(list, func(i, j int) bool {
			if list[i].Group != list[j].Group {
				return list[i].Group < list[j].Group
			}
			return list[i].Type < list[j].Type
		})
		writeJSON(w, 200, list)
	}))
	mux.Handle("POST /api/v1/providers/{type}/test", authed(func(w http.ResponseWriter, r *http.Request) {
		p, ok := env.Get(r.PathValue("type"))
		if !ok {
			writeErr(w, 404, "not_found", "unknown provider")
			return
		}
		var body struct {
			Config json.RawMessage `json:"config"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
			return
		}
		if len(body.Config) == 0 {
			body.Config = json.RawMessage(`{}`)
		}
		if err := p.Validate(body.Config); err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := p.Test(ctx, body.Config); err != nil {
			writeErr(w, http.StatusBadGateway, "test_failed", err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))

	// ---- environments ----
	// 0.1.9: Hyper-V / QEMU status + one-click group repair
	mux.Handle("GET /api/v1/providers/hyperv/status", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, hyperv.Detect(r.Context()))
	}))
	mux.Handle("POST /api/v1/providers/hyperv/repair-group", authed(func(w http.ResponseWriter, r *http.Request) {
		res := hyperv.RepairHyperVGroup(r.Context())
		// Always 200: ok=false is an expected outcome (UAC denied / elev pending), not a server fault.
		writeJSON(w, 200, res)
	}))

	mux.Handle("GET /api/v1/envs", authed(func(w http.ResponseWriter, r *http.Request) {
		if m.es == nil {
			writeJSON(w, 200, []EnvView{})
			return
		}
		rows, err := m.es.ListEnvs(r.Context())
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		out := make([]EnvView, 0, len(rows))
		for _, row := range rows {
			out = append(out, m.view(r.Context(), row))
		}
		sortViews(out)
		writeJSON(w, 200, out)
	}))
	mux.Handle("POST /api/v1/envs", authed(s.handleCreateEnv))
	mux.Handle("GET /api/v1/envs/{id}", withEnv(func(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
		writeJSON(w, 200, m.view(r.Context(), row))
	}))
	mux.Handle("PATCH /api/v1/envs/{id}", withEnv(s.handlePatchEnv))
	mux.Handle("DELETE /api/v1/envs/{id}", withEnv(s.handleDeleteEnv))
	mux.Handle("POST /api/v1/envs/{id}/start", withEnv(func(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
		s.envLifecycle(w, r, row, true)
	}))
	mux.Handle("POST /api/v1/envs/{id}/stop", withEnv(func(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
		s.envLifecycle(w, r, row, false)
	}))
	mux.Handle("POST /api/v1/envs/{id}/test", withEnv(s.handleTestEnv))
	mux.Handle("POST /api/v1/envs/{id}/install-agentos", withEnv(s.handleInstallAgentOS))
	mux.Handle("POST /api/v1/envs/{id}/join", withEnv(s.handleJoinEnv))
	mux.Handle("GET /api/v1/envs/{id}/metrics", withEnv(s.handleEnvMetrics))
	mux.Handle("GET /api/v1/envs/{id}/logs", withEnv(func(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
		tail := 200
		if v, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && v > 0 {
			tail = v
		}
		writeJSON(w, 200, m.tailLog(row.ID, tail))
	}))
	mux.Handle("GET /api/v1/envs/{id}/files", withEnv(s.handleEnvListDir))
	mux.Handle("GET /api/v1/envs/{id}/files/content", withEnv(s.handleEnvGetFile))
	mux.Handle("PUT /api/v1/envs/{id}/files/content", withEnv(s.handleEnvPutFile))
	mux.Handle("GET /api/v1/envs/{id}/exec", withEnv(s.handleEnvExec))
	mux.Handle("GET /api/v1/envs/{id}/desktop", withEnv(s.handleEnvDesktop))
	mux.Handle("GET /api/v1/envs/{id}/screenshot", withEnv(s.handleEnvScreenshot))
	mux.Handle("POST /api/v1/envs/{id}/open", withEnv(s.handleEnvOpenNative))

	// ---- downloads (unauthenticated: used by install one-liners) ----
	mux.HandleFunc("GET /download/{file}", s.handleDownload)
}

// envErr maps provider/env errors to HTTP responses.
func envErr(w http.ResponseWriter, err error) {
	var fe *agentosnode.FileError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "环境不存在 / not found")
	case errors.Is(err, env.ErrNotFound):
		writeErr(w, 404, "not_found", "环境不存在 / "+err.Error())
	case errors.Is(err, env.ErrUnsupported):
		writeErr(w, http.StatusConflict, "unsupported", "该环境不支持此操作 / not supported by this environment")
	case errors.Is(err, agentosnode.ErrNodeOffline), errors.Is(err, ErrNodeOffline):
		writeErr(w, http.StatusConflict, "node_offline", "Agent OS 节点离线 / node is offline")
	case errors.As(err, &fe), errors.Is(err, os.ErrNotExist):
		msg := strings.ToLower(err.Error())
		if errors.Is(err, os.ErrNotExist) || strings.Contains(msg, "not found") || strings.Contains(msg, "no such file") ||
			strings.Contains(msg, "cannot find") || strings.Contains(msg, "not exist") {
			writeErr(w, 404, "not_found", "文件不存在 / "+err.Error())
		} else {
			writeErr(w, 400, "file_error", "文件操作失败 / "+err.Error())
		}
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "timeout", "操作超时，请稍后重试 / timed out")
	default:
		writeErr(w, http.StatusBadGateway, "env_error", err.Error())
	}
}

func cleanLabels(in map[string]string, keep map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k == agentoslocal.LabelBuiltin {
			continue // reserved
		}
		out[k] = v
	}
	if keep[agentoslocal.LabelBuiltin] != "" {
		out[agentoslocal.LabelBuiltin] = keep[agentoslocal.LabelBuiltin]
	}
	return out
}

func (s *Server) handleCreateEnv(w http.ResponseWriter, r *http.Request) {
	m := s.envs
	if m.es == nil {
		writeErr(w, 501, "unsupported", errNoEnvStore.Error())
		return
	}
	var body struct {
		Name     string            `json:"name"`
		Provider string            `json:"provider"`
		Config   json.RawMessage   `json:"config"`
		Stack    string            `json:"stack"` // optional suggestion only (0.1.3+: not auto-installed; call POST .../stacks/{id}/install)
		Labels   map[string]string `json:"labels"`
		Preset   string            `json:"preset"` // optional: GET /env-presets id; fills provider/config/labels (stack ignored for install)
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	baseName := ""
	if body.Preset != "" {
		pr, ok := s.findPresetOpt(r.Context(), body.Preset, true) // provider-scoped probe only (docker-desktop must not wait on Hyper-V)
		if !ok {
			writeErr(w, 400, "bad_request", "unknown preset")
			return
		}
		if !pr.Available {
			msg := pr.Reason
			if msg == "" {
				msg = "该预设当前不可用（请检查 Docker / Hyper-V 等是否已安装并运行）"
			}
			writeErr(w, 503, "provider_unavailable", msg)
			return
		}
		if body.Provider != "" && body.Provider != pr.Provider {
			writeErr(w, 400, "bad_request", "provider does not match preset")
			return
		}
		body.Provider = pr.Provider
		cfg, err := mergeConfigOver(pr.Config, body.Config)
		if err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		body.Config = cfg
		if body.Stack == "" {
			body.Stack = pr.Stack
		}
		if body.Labels == nil && len(pr.Labels) > 0 {
			body.Labels = pr.Labels
		}
		baseName = pr.BaseName
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" { // auto-generate a unique name ("Docker 桌面 1", ...)
		if baseName == "" {
			if p, ok := env.Get(body.Provider); ok {
				baseName = p.Info().Name
			}
		}
		if baseName == "" {
			writeErr(w, 400, "bad_request", "name is required (max 128 chars)")
			return
		}
		body.Name = s.uniqueEnvName(r.Context(), baseName)
	}
	if len(body.Name) > 128 {
		writeErr(w, 400, "bad_request", "name is required (max 128 chars)")
		return
	}
	p, ok := env.Get(body.Provider)
	if !ok {
		writeErr(w, 400, "bad_request", "unknown provider")
		return
	}
	if len(body.Config) == 0 || string(body.Config) == "null" {
		body.Config = json.RawMessage(`{}`)
	}
	if err := p.Validate(body.Config); err != nil {
		writeErr(w, 400, "invalid_config", err.Error())
		return
	}
	now := time.Now().UTC()
	row := store.EnvRow{Record: env.Record{ID: uuid.NewString(), Name: body.Name, Provider: body.Provider,
		Stack: body.Stack, Labels: cleanLabels(body.Labels, nil), State: env.StateCreating, CreatedAt: now, UpdatedAt: now}}
	if pp, ok := p.(env.Preparer); ok { // generated credentials etc. are stored before provisioning starts
		prep := row.Record
		prep.Config = body.Config
		if err := pp.Prepare(&prep); err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		body.Config = prep.Config
	}
	if err := m.setConfig(&row, body.Config); err != nil {
		writeErr(w, 400, "invalid_config", err.Error())
		return
	}
	if row.Provider == agentoslocal.Type {
		row.NodeID = row.ID // the instance's node identity is fixed up front
	}
	m.setBusy(row.ID, "provisioning")
	if err := m.es.CreateEnv(r.Context(), row); err != nil {
		m.setBusy(row.ID, "")
		writeStoreErr(w, err)
		return
	}
	m.log(row.ID, "info", fmt.Sprintf("创建环境 / creating environment %q (%s)", row.Name, row.Provider))
	v := m.view(r.Context(), row)
	s.bus.Publish(EventEnvCreated, v)
	writeJSON(w, http.StatusCreated, v)

	go m.provision(p, row.ID, body.Config)
}

func (s *Server) handlePatchEnv(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	var body struct {
		Name   *string           `json:"name"`
		Labels map[string]string `json:"labels"`
		Config json.RawMessage   `json:"config"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	renamed := false
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" || len(n) > 128 {
			writeErr(w, 400, "bad_request", "name is required (max 128 chars)")
			return
		}
		renamed = n != row.Name
		row.Name = n
	}
	if body.Labels != nil {
		row.Labels = cleanLabels(body.Labels, row.Labels)
	}
	reopen := false
	if len(body.Config) > 0 && string(body.Config) != "null" {
		p, ok := env.Get(row.Provider)
		if !ok {
			writeErr(w, 400, "bad_request", "unknown provider")
			return
		}
		// secret fields omitted from the patch keep their stored value; send
		// null to clear one (the UI never receives secret values).
		newMap, err := configMap(body.Config)
		if err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		old, err := m.seal.openSecrets(row.Secrets, row.ID)
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		for k := range secretKeys(p.Info().ConfigSchema) {
			v, present := newMap[k]
			switch {
			case !present:
				if ov, ok := old[k]; ok {
					newMap[k] = ov
				}
			case string(v) == "null":
				delete(newMap, k)
			}
		}
		merged, _ := json.Marshal(newMap)
		if err := p.Validate(merged); err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		if err := m.setConfig(&row, merged); err != nil {
			writeErr(w, 400, "invalid_config", err.Error())
			return
		}
		reopen = true
	}
	row.UpdatedAt = time.Now().UTC()
	if err := m.es.UpdateEnv(r.Context(), row); err != nil {
		writeStoreErr(w, err)
		return
	}
	if renamed && row.NodeID != "" {
		if n, err := s.st.GetNode(r.Context(), row.NodeID); err == nil {
			n.Name = row.Name
			s.st.UpsertNode(r.Context(), n)
			s.publishNode(n.ID)
		}
	}
	if reopen || renamed {
		m.dropHandle(row.ID)
		m.refresh(r.Context(), row)
	}
	v := m.view(r.Context(), row)
	s.bus.Publish(EventEnvUpdated, v)
	writeJSON(w, 200, v)
}

func (s *Server) handleDeleteEnv(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	if row.Labels[agentoslocal.LabelBuiltin] == "true" {
		writeErr(w, http.StatusConflict, "builtin", "内置环境不能删除 / the built-in environment cannot be deleted")
		return
	}
	if prev, ok := m.trySetBusy(row.ID, "deleting"); !ok {
		writeErr(w, http.StatusConflict, "busy", "环境正忙（"+prev+"），请稍后再试 / environment busy ("+prev+")")
		return
	}
	m.publish(r.Context(), row.ID, EventEnvUpdated)
	defer func() {
		m.setBusy(row.ID, "")
		// row may already be gone; publish is best-effort
		m.publish(context.Background(), row.ID, EventEnvUpdated)
	}()
	purge := r.URL.Query().Get("purge") == "1" || r.URL.Query().Get("purge") == "true"
	// Hyper-V Stop+Remove-VM and VHDX unlock often exceed 2 minutes.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	dctx := ctx
	if purge {
		dctx = env.WithPurge(ctx)
	}
	if row.State != env.StateError || row.Provider == agentoslocal.Type {
		if h, err := m.handle(ctx, row); err == nil {
			if err := h.Delete(dctx); err != nil && !errors.Is(err, env.ErrUnsupported) {
				m.log(row.ID, "error", "删除失败 / delete failed: "+err.Error())
				envErr(w, err)
				return
			}
		}
	} else if h, err := m.handle(ctx, row); err == nil {
		// A failed provisioning may still have left resources behind (e.g. a
		// half-created Hyper-V VM and its disk): clean up best effort, but never
		// block removing the broken row.
		if err := h.Delete(dctx); err != nil && !errors.Is(err, env.ErrUnsupported) {
			m.log(row.ID, "warn", "清理残留资源失败（已忽略）/ cleanup failed (ignored): "+err.Error())
		}
	}
	if m.es != nil {
		m.es.DeleteNodeCredential(ctx, row.ID)
	}
	if err := m.es.DeleteEnv(ctx, row.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	if row.NodeID != "" && row.NodeID != m.opts.BuiltinNodeID {
		// the node belongs to this environment only: revoke and forget it
		m.es.RevokeNode(ctx, row.NodeID)
		s.disconnectNode(row.NodeID)
		s.removeNode(ctx, row.NodeID)
	}
	m.forget(row.ID)
	s.bus.Publish(EventEnvDeleted, map[string]string{"id": row.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) envLifecycle(w http.ResponseWriter, r *http.Request, row store.EnvRow, start bool) {
	m := s.envs
	if row.Labels[agentoslocal.LabelBuiltin] == "true" {
		envErr(w, env.ErrUnsupported)
		return
	}
	h, err := m.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	op := "stopping"
	verb := "停止 / stop"
	if start {
		op, verb = "starting", "启动 / start"
	}
	if prev, ok := m.trySetBusy(row.ID, op); !ok {
		writeErr(w, http.StatusConflict, "busy", "环境正忙（"+prev+"），请稍后再试 / environment busy ("+prev+")")
		return
	}
	// Push busy to SSE clients immediately; always clear (+ republish) on the way out.
	m.publish(r.Context(), row.ID, EventEnvUpdated)
	defer func() {
		m.setBusy(row.ID, "")
		m.publish(context.Background(), row.ID, EventEnvUpdated)
	}()
	// Hyper-V Stop ~4m (graceful 2m + TurnOff); SSH StartCommand can wait ~5m for SSH.
	// Bound both above the slowest provider so the handler does not cancel early.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if start {
		err = h.Start(ctx)
	} else {
		err = h.Stop(ctx)
	}
	if err != nil {
		if !errors.Is(err, env.ErrUnsupported) {
			m.log(row.ID, "error", verb+" 失败 / failed: "+err.Error())
		}
		envErr(w, err)
		return
	}
	m.log(row.ID, "info", verb)
	if start {
		row.State = env.StateRunning
	} else {
		row.State = env.StateStopped
	}
	row.Error, row.UpdatedAt = "", time.Now().UTC()
	m.es.UpdateEnv(r.Context(), row)
	m.refresh(r.Context(), row)
	// Clear busy before the success view so the HTTP body is not stuck on starting/stopping.
	m.setBusy(row.ID, "")
	v := m.view(r.Context(), row)
	s.bus.Publish(EventEnvUpdated, v)
	writeJSON(w, 200, v)
}

func (s *Server) handleTestEnv(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	p, ok := env.Get(row.Provider)
	if !ok {
		writeErr(w, 400, "bad_request", "unknown provider")
		return
	}
	rec, err := m.full(row)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := p.Test(ctx, rec.Config); err != nil {
		m.log(row.ID, "warn", "连接测试失败 / test failed: "+err.Error())
		writeErr(w, http.StatusBadGateway, "test_failed", err.Error())
		return
	}
	m.refresh(ctx, row)
	writeJSON(w, 200, m.view(r.Context(), row))
}

// ---- install-agentos / join ----

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host
}

func (s *Server) distDirs() []string {
	if s.cfg.DistDir != "" {
		return []string{s.cfg.DistDir}
	}
	if exe, err := os.Executable(); err == nil {
		return []string{filepath.Join(filepath.Dir(exe), "dist")}
	}
	return nil
}

// binaryPath finds agentos-<goos>-<goarch>[.exe]; the running executable
// stands in for its own platform when the dist dir has no such file.
func (s *Server) binaryPath(goos, goarch string) (string, bool) {
	name := agentosremote.BinaryName(goos, goarch)
	for _, d := range s.distDirs() {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		if exe, err := os.Executable(); err == nil {
			return exe, true
		}
	}
	return "", false
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if file == "join.sh" || file == "join.ps1" { // packaged join scripts (scripts/join.*) from the dist dir
		for _, d := range s.distDirs() {
			if b, err := os.ReadFile(filepath.Join(d, file)); err == nil {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Write(b)
				return
			}
		}
		file = strings.Replace(file, "join", "install", 1) // same CLI, embedded copy
	}
	if b, ok := agentosremote.Script(file); ok {
		ct := "text/x-shellscript; charset=utf-8"
		if strings.HasSuffix(file, ".ps1") {
			ct = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
		return
	}
	goos, goarch, ok := agentosremote.ParseBinaryName(file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, ok := s.binaryPath(goos, goarch)
	if !ok {
		writeErr(w, 404, "not_found", fmt.Sprintf("没有 %s/%s 的安装包 / no agentos binary for %s/%s in the dist directory", goos, goarch, goos, goarch))
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeContent(w, r, file, fi.ModTime(), f)
}

func (s *Server) handleInstallAgentOS(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	p, ok := env.Get(row.Provider)
	if ok {
		rec := row.Record
		ok = hasCap(env.CapabilitiesOf(p, &rec), env.CapInstallAgent)
	}
	if !ok {
		envErr(w, env.ErrUnsupported)
		return
	}
	inst, ok := env.GetInstaller()
	if !ok {
		writeErr(w, http.StatusNotImplemented, "unsupported", "未安装 Agent OS 引导模块 / no Agent OS installer available")
		return
	}
	if row.NodeID != "" && s.hub.Online(row.NodeID) {
		writeErr(w, http.StatusConflict, "already_installed", "Agent OS 已在此环境运行 / an Agent OS node is already connected")
		return
	}
	m.mu.Lock()
	if b := m.busy[row.ID]; b != "" {
		m.mu.Unlock()
		writeErr(w, http.StatusConflict, "busy", "环境正忙 / environment busy: "+b)
		return
	}
	m.busy[row.ID] = "installing"
	m.mu.Unlock()
	h, err := m.handle(r.Context(), row)
	if err != nil {
		m.setBusy(row.ID, "")
		envErr(w, err)
		return
	}
	tok, err := m.issueCredential(r.Context(), row.ID)
	if err != nil {
		m.setBusy(row.ID, "")
		writeStoreErr(w, err)
		return
	}
	// the node identity is fixed up front so agents can be targeted at once
	if row.NodeID == "" {
		row.NodeID = uuid.NewString()
		row.UpdatedAt = time.Now().UTC()
		if err := m.es.UpdateEnv(r.Context(), row); err != nil {
			m.setBusy(row.ID, "")
			writeStoreErr(w, err)
			return
		}
	}
	rec, _ := m.full(row)
	base := r.URL.Query().Get("server_url")
	if base == "" {
		base = requestBaseURL(r)
	}
	base = strings.TrimRight(base, "/")
	req := env.InstallRequest{ServerURL: base, Token: tok, Name: row.Name, NodeID: row.NodeID,
		BinaryURL:  func(goos, goarch string) string { return base + "/download/" + agentosremote.BinaryName(goos, goarch) },
		BinaryPath: s.binaryPath,
		Log:        func(level, text string) { m.log(row.ID, level, text) }}
	m.log(row.ID, "info", "开始安装 Agent OS / installing Agent OS node")
	if row.Provider == "docker" && listensLoopbackOnly(s.cfg.Listen) {
		m.log(row.ID, "warn", "管理端只监听回环地址 ("+s.cfg.Listen+")，容器内的节点无法连接；请用 --listen :7800 启动管理端 / "+
			"the server listens on loopback only, a node inside the container cannot reach it (use --listen :7800)")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
		defer cancel()
		if err := inst.Install(ctx, h, rec, req); err != nil {
			m.log(row.ID, "error", "安装失败 / install failed: "+err.Error())
			m.setBusy(row.ID, "")
		} else {
			m.log(row.ID, "info", "Agent OS 已启动，等待连接 / node started, waiting for it to connect")
			go func() { // give up the busy marker if the node never connects
				time.Sleep(2 * time.Minute)
				m.mu.Lock()
				if m.busy[row.ID] == "installing" {
					delete(m.busy, row.ID)
				}
				m.mu.Unlock()
			}()
		}
		m.refreshAndPublish(row.ID)
	}()
	v := m.view(r.Context(), row)
	s.bus.Publish(EventEnvUpdated, v)
	writeJSON(w, 200, v)
}

// JoinResponse is returned by POST /envs/{id}/join.
type JoinResponse struct {
	Token           string                 `json:"token"`
	ServerURL       string                 `json:"server_url"`
	Commands        agentosremote.Commands `json:"commands"`
	ServiceCommands agentosremote.Commands `json:"service_commands"` // same, plus --install-service (auto-start)
}

func (s *Server) handleJoinEnv(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	if row.Provider != agentosremote.Type {
		writeErr(w, 400, "bad_request", "join is only available for agentos-remote environments")
		return
	}
	cfg, _ := agentosremote.Parse(row.Config)
	base := cfg.ServerURL
	if base == "" {
		base = r.URL.Query().Get("server_url")
	}
	if base == "" {
		base = requestBaseURL(r)
	}
	base = strings.TrimRight(base, "/")
	// a new token replaces the old one; a previously joined node is disconnected
	if row.NodeID != "" {
		m.es.RevokeNode(r.Context(), row.NodeID)
		s.disconnectNode(row.NodeID)
	}
	tok, err := m.issueCredential(r.Context(), row.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	m.log(row.ID, "info", "已生成加入令牌 / join token issued")
	writeJSON(w, 200, JoinResponse{Token: tok, ServerURL: base,
		Commands:        agentosremote.JoinCommandsDesktop(base, tok, row.Name, false, cfg.Desktop),
		ServiceCommands: agentosremote.JoinCommandsDesktop(base, tok, row.Name, true, cfg.Desktop)})
}

// ---- metrics ----

func (s *Server) handleEnvMetrics(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, 400, "bad_request", "since must be RFC3339")
			return
		}
		since = t
	}
	m.mu.Lock()
	all := m.metrics[row.ID]
	samples := make([]MetricSample, 0, len(all))
	for _, sm := range all {
		if sm.T.After(since) {
			samples = append(samples, sm)
		}
	}
	m.mu.Unlock()
	var latest *env.Metrics
	if h, err := m.handle(r.Context(), row); err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if mt, err := h.Metrics(ctx); err == nil {
			latest = &mt
		}
		cancel()
	}
	if latest == nil && len(all) > 0 {
		l := all[len(all)-1].Metrics
		latest = &l
	}
	writeJSON(w, 200, map[string]any{"latest": latest, "samples": samples})
}

// ---- files ----

func (s *Server) handleEnvListDir(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	h, err := s.envs.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	list, err := h.ListDir(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		envErr(w, err)
		return
	}
	if list == nil {
		list = []env.FileInfo{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleEnvGetFile(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, 400, "bad_request", "path is required")
		return
	}
	h, err := s.envs.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	rc, err := h.GetFile(r.Context(), p)
	if err != nil {
		envErr(w, err)
		return
	}
	defer rc.Close()
	name := filepath.Base(strings.ReplaceAll(p, `\`, "/"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q; filename*=UTF-8''%s`,
		strings.Map(func(c rune) rune {
			if c < 0x20 || c > 0x7e || c == '"' {
				return '_'
			}
			return c
		}, name), urlPathEscape(name)))
	io.Copy(w, rc)
}

func (s *Server) handleEnvPutFile(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeErr(w, 400, "bad_request", "path is required")
		return
	}
	mode := uint32(0o644)
	if v := r.URL.Query().Get("mode"); v != "" {
		n, err := strconv.ParseUint(v, 8, 32)
		if err != nil || n > 0o7777 {
			writeErr(w, 400, "bad_request", "mode must be octal, e.g. 0644")
			return
		}
		mode = uint32(n)
	}
	h, err := s.envs.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	if err := h.PutFile(r.Context(), p, r.Body, mode); err != nil {
		envErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- exec ----

type execCtl struct {
	Type    string `json:"type"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Code    *int   `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (s *Server) handleEnvExec(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	m := s.envs
	q := r.URL.Query()
	req := env.ExecRequest{WorkDir: q.Get("cwd"), TTY: q.Get("tty") == "1" || q.Get("tty") == "true"}
	req.Cols, _ = strconv.Atoi(q.Get("cols"))
	req.Rows, _ = strconv.Atoi(q.Get("rows"))
	if c := q.Get("cmd"); c != "" {
		if err := json.Unmarshal([]byte(c), &req.Cmd); err != nil || len(req.Cmd) == 0 {
			writeErr(w, 400, "bad_request", "cmd must be a JSON array of strings")
			return
		}
	} else {
		req.Cmd = []string{"/bin/sh", "-l"}
		if row.NodeID != "" {
			if n, err := s.st.GetNode(r.Context(), row.NodeID); err == nil && n.OS == "windows" {
				req.Cmd = []string{"powershell.exe", "-NoLogo"}
			}
		}
	}
	h, err := m.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, err := h.Exec(ctx, req)
	if err != nil {
		envErr(w, err)
		return
	}
	defer sess.Close()
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	var wmu sync.Mutex
	write := func(t int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return ws.WriteMessage(t, b)
	}
	var outWG sync.WaitGroup
	pump := func(rd io.Reader) {
		defer outWG.Done()
		if rd == nil {
			return
		}
		buf := make([]byte, 32*1024)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				if write(websocket.BinaryMessage, append([]byte(nil), buf[:n]...)) != nil {
					cancel()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	outWG.Add(2)
	go pump(sess.Stdout())
	go pump(sess.Stderr())

	go func() { // client -> process
		stdin := sess.Stdin()
		defer func() {
			if stdin != nil {
				stdin.Close()
			}
		}()
		for {
			t, data, err := ws.ReadMessage()
			if err != nil {
				cancel()
				sess.Close()
				return
			}
			if t == websocket.BinaryMessage {
				if stdin != nil {
					stdin.Write(data)
				}
				continue
			}
			var c execCtl
			if json.Unmarshal(data, &c) != nil {
				continue
			}
			switch c.Type {
			case "resize":
				if c.Cols > 0 && c.Rows > 0 {
					sess.Resize(c.Cols, c.Rows)
				}
			case "eof", "close_stdin":
				if stdin != nil {
					stdin.Close()
					stdin = nil
				}
			}
		}
	}()

	code, werr := sess.Wait()
	waitOut := make(chan struct{})
	go func() { outWG.Wait(); close(waitOut) }()
	select {
	case <-waitOut:
	case <-time.After(3 * time.Second):
	}
	var msg []byte
	if werr != nil && ctx.Err() == nil {
		msg, _ = json.Marshal(execCtl{Type: "error", Message: werr.Error()})
	} else {
		msg, _ = json.Marshal(execCtl{Type: "exit", Code: &code})
	}
	write(websocket.TextMessage, msg)
	wmu.Lock()
	ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	wmu.Unlock()
}

// ---- desktop ----

func (s *Server) handleEnvDesktop(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = api.TunnelVNC
	}
	if kind != api.TunnelVNC && kind != api.TunnelScreen {
		writeErr(w, 400, "bad_request", "kind must be vnc or screen")
		return
	}
	// Agent OS node bound and online: identical to /nodes/{id}/desktop
	// (frame-preserving relay; the screen protocol needs text+binary frames).
	if row.NodeID != "" && s.hub.Online(row.NodeID) {
		s.serveNodeDesktop(w, r, row.NodeID)
		return
	}
	// Docker on-demand VNC: drop a handle opened before virtual_desktop was set
	// so Open reloads config and Desktop() no longer returns ErrUnsupported.
	if row.Provider == "docker" && configHasTrue(row.Config, "virtual_desktop") {
		s.envs.dropHandle(row.ID)
	}
	h, err := s.envs.handle(r.Context(), row)
	if err != nil {
		envErr(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := h.Desktop(ctx, kind)
	if err != nil {
		envErr(w, err)
		return
	}
	defer stream.Close()
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	go func() { // browser -> env
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				stream.Close()
				return
			}
			if _, err := stream.Write(data); err != nil {
				ws.Close()
				return
			}
		}
	}()
	buf := make([]byte, 64*1024)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if ws.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// listensLoopbackOnly reports whether a listen address binds a loopback IP only
// (e.g. 127.0.0.1:7800), which containers/VMs cannot reach.
func listensLoopbackOnly(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasCapability(cs []env.Capability, c env.Capability) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// provision runs p.Create for env id in the background (also used to resume
// an interrupted provisioning on startup for providers implementing env.CreateResumer).
func (m *envManager) provision(p env.Provider, id string, full json.RawMessage) {
	{
		timeout := provisionTimeout
		if pt, ok := p.(env.ProvisionTimeouter); ok {
			rec := env.Record{ID: id, Config: full}
			if d := pt.ProvisionTimeout(&rec); d > timeout {
				timeout = d
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		row, err := m.es.GetEnv(ctx, id)
		if err != nil {
			m.setBusy(id, "")
			return
		}
		rec := row.Record
		rec.Config = full
		pctx := env.WithLogger(ctx, func(level, text string) { m.log(id, level, text) })
		pctx = env.WithConfigSaver(pctx, func(cfg json.RawMessage) {
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			if r, err := m.es.GetEnv(sctx, id); err == nil && m.setConfig(&r, cfg) == nil {
				r.UpdatedAt = time.Now().UTC()
				m.es.UpdateEnv(sctx, r)
				m.publish(sctx, id, EventEnvUpdated)
			}
		})
		cerr := p.Create(pctx, &rec)
		row, err = m.es.GetEnv(ctx, id) // may have been renamed/deleted meanwhile
		if err != nil {
			m.setBusy(id, "")
			return
		}
		if cerr != nil {
			if len(rec.Config) > 0 && string(rec.Config) != string(full) {
				m.setConfig(&row, rec.Config) // keep ids/credentials recorded before the failure (delete can clean up)
			}
			row.State, row.Error = env.StateError, cerr.Error()
			m.log(id, "error", "创建失败 / provisioning failed: "+cerr.Error())
		} else {
			if len(rec.Config) > 0 && string(rec.Config) != string(full) {
				m.setConfig(&row, rec.Config) // provider may record ids (container id, ...)
			}
			if rec.NodeID != "" {
				row.NodeID = rec.NodeID
			}
			row.State, row.Error = env.StateRunning, ""
			m.log(id, "info", "环境已创建 / environment created")
		}
		// Stack on the row is a suggestion only (0.1.3+): do NOT auto-install.
		// Clients call POST /envs/{id}/stacks/{stack}/install separately.
		if cerr == nil && row.Stack != "" {
			m.log(id, "info", "技术栈建议已记录（未自动安装） / stack suggested, not auto-installed: "+row.Stack)
		}
		row.UpdatedAt = time.Now().UTC()
		m.es.UpdateEnv(ctx, row)
		m.setBusy(id, "")
		m.dropHandle(id)
		m.refreshAndPublish(id)
	}
}

// GET /envs/{id}/screenshot?w=1024 -> image/png (env.Screenshotter, e.g. Hyper-V console;
// available while the OS is still installing).
func (s *Server) handleEnvScreenshot(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	p, ok := env.Get(row.Provider)
	if !ok {
		writeErr(w, 400, "bad_request", "unknown provider")
		return
	}
	rec, err := s.envs.full(row)
	if err != nil {
		envErr(w, err)
		return
	}
	// a fresh handle: works during provisioning too (cached handles are not opened then)
	h, err := p.Open(r.Context(), &rec)
	if err != nil {
		envErr(w, err)
		return
	}
	defer h.Close()
	ss, ok := h.(env.Screenshotter)
	if !ok {
		envErr(w, env.ErrUnsupported)
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	png, err := ss.Screenshot(ctx, width)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "screenshot_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

// POST /envs/{id}/open {kind: console|rdp} -> 204: open a native viewer on the
// server host (env.NativeOpener; Hyper-V: vmconnect / mstsc with saved credentials).
func (s *Server) handleEnvOpenNative(w http.ResponseWriter, r *http.Request, row store.EnvRow) {
	var body struct {
		Kind string `json:"kind"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON: "+err.Error())
			return
		}
	}
	p, ok := env.Get(row.Provider)
	if !ok {
		writeErr(w, 400, "bad_request", "unknown provider")
		return
	}
	rec, err := s.envs.full(row)
	if err != nil {
		envErr(w, err)
		return
	}
	h, err := p.Open(r.Context(), &rec)
	if err != nil {
		envErr(w, err)
		return
	}
	defer h.Close()
	no, ok := h.(env.NativeOpener)
	if !ok {
		envErr(w, env.ErrUnsupported)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := no.OpenNative(ctx, body.Kind); err != nil {
		if errors.Is(err, env.ErrUnsupported) {
			envErr(w, err)
			return
		}
		writeErr(w, http.StatusBadGateway, "open_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
