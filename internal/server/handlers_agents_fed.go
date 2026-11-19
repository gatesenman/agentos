package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/agentfed"
	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/store"
	"github.com/google/uuid"
)

// fedState holds federation runtime (A2A/ACP clients + task/log rings).
type fedState struct {
	fs   store.FedStore
	seal *agentfed.TokenSealer
	rt   *agentfed.Runtime
}

func (s *Server) initFed() {
	fs, _ := s.st.(store.FedStore)
	seal, err := agentfed.LoadSealer(s.cfg.DataDir)
	if err != nil || seal == nil {
		seal, _ = agentfed.LoadSealer("")
	}
	s.fed = &fedState{fs: fs, seal: seal, rt: agentfed.NewRuntime()}
}

func (s *Server) routesAgentsFed(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	if s.fed == nil {
		s.initFed()
	}
	// Discover / probe remote endpoint (auto-detect A2A card vs ACP).
	mux.Handle("POST /api/v1/federation/agents/discover", authed(s.handleFedDiscover))
	mux.Handle("POST /api/v1/federation/agents/scan-local", authed(s.handleFedScanLocal))
	mux.Handle("GET /api/v1/federation/agents/scan-local", authed(s.handleFedScanLocal))
	mux.Handle("POST /api/v1/federation/agents", authed(s.handleFedCreate))
	mux.Handle("GET /api/v1/federation/agents", authed(s.handleFedList))
	mux.Handle("GET /api/v1/federation/agents/{id}", authed(s.handleFedGet))
	mux.Handle("PUT /api/v1/federation/agents/{id}", authed(s.handleFedUpdate))
	mux.Handle("DELETE /api/v1/federation/agents/{id}", authed(s.handleFedDelete))
	mux.Handle("POST /api/v1/federation/agents/{id}/test", authed(s.handleFedTest))
	mux.Handle("POST /api/v1/federation/agents/{id}/invoke", authed(s.handleFedInvoke))
	mux.Handle("POST /api/v1/federation/agents/{id}/stop", authed(s.handleFedStop))
	mux.Handle("GET /api/v1/federation/agents/{id}/tasks", authed(s.handleFedTasks))
	mux.Handle("GET /api/v1/federation/agents/{id}/logs", authed(s.handleFedLogs))
}

type fedView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Kind         string            `json:"kind"` // a2a | acp
	Protocol     string            `json:"protocol"`
	Endpoint     string            `json:"endpoint"`
	AuthScheme   string            `json:"auth_scheme"`
	HasAuth      bool              `json:"has_auth"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	AgentCard    json.RawMessage   `json:"agent_card,omitempty"`
	Status       api.AgentStatus   `json:"status"`
	FedStatus    string            `json:"fed_status"`
	LastError    string            `json:"last_error,omitempty"`
	LastSeen     *time.Time        `json:"last_seen,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func (s *Server) rowToView(r store.FedRow) fedView {
	st := api.AgentStatus{ID: r.ID, State: api.StateStopped, UpdatedAt: r.UpdatedAt}
	if s.fed.rt.AgentBusy(r.ID) {
		st.State = api.StateRunning
		now := time.Now().UTC()
		st.StartedAt = &now
	} else if r.Status == "online" {
		st.State = api.StateStopped
	} else if r.Status == "error" {
		st.State = api.StateFailed
		st.Error = r.LastError
	}
	var lastSeen *time.Time
	if !r.LastSeen.IsZero() {
		t := r.LastSeen
		lastSeen = &t
	}
	return fedView{
		ID: r.ID, Name: r.Name, Description: r.Description, Kind: r.Protocol, Protocol: r.Protocol,
		Endpoint: r.Endpoint, AuthScheme: r.AuthScheme, HasAuth: r.AuthToken != "",
		Capabilities: r.Capabilities, Metadata: r.Metadata, AgentCard: r.AgentCard,
		Status: st, FedStatus: r.Status, LastError: r.LastError, LastSeen: lastSeen,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (s *Server) handleFedDiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint   string `json:"endpoint"`
		AuthScheme string `json:"auth_scheme"`
		AuthHeader string `json:"auth_header"`
		Token      string `json:"token"`
		Protocol   string `json:"protocol"` // optional force
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Endpoint) == "" {
		writeErr(w, 400, "bad_request", "endpoint is required")
		return
	}
	auth := agentfed.Auth{Scheme: req.AuthScheme, Header: req.AuthHeader, Token: req.Token}
	if auth.Scheme == "" {
		if auth.Token != "" {
			auth.Scheme = agentfed.AuthBearer
		} else {
			auth.Scheme = agentfed.AuthNone
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if req.Protocol == agentfed.ProtoA2A {
		card, err := agentfed.FetchAgentCard(ctx, req.Endpoint, auth, nil)
		if err != nil {
			writeJSON(w, 200, agentfed.ProbeResult{OK: false, Error: err.Error(), Endpoint: req.Endpoint, Protocol: agentfed.ProtoA2A})
			return
		}
		rpc, _, _ := card.ResolveInterface()
		writeJSON(w, 200, agentfed.ProbeResult{
			OK: true, Protocol: agentfed.ProtoA2A, Name: card.Name, Description: card.Description,
			Endpoint: firstNonEmpty(rpc, req.Endpoint), Capabilities: card.CapList(), Card: card.Raw,
		})
		return
	}
	if req.Protocol == agentfed.ProtoACP {
		agents, err := agentfed.DiscoverACP(ctx, req.Endpoint, auth, nil)
		if err != nil {
			writeJSON(w, 200, agentfed.ProbeResult{OK: false, Error: err.Error(), Endpoint: req.Endpoint, Protocol: agentfed.ProtoACP})
			return
		}
		if len(agents) == 0 {
			writeJSON(w, 200, agentfed.ProbeResult{OK: false, Error: "no ACP agents", Endpoint: req.Endpoint})
			return
		}
		a := agents[0]
		raw, _ := json.Marshal(agents)
		writeJSON(w, 200, agentfed.ProbeResult{
			OK: true, Protocol: agentfed.ProtoACP, Name: a.Name, Description: a.Description,
			Endpoint: strings.TrimRight(req.Endpoint, "/"), Capabilities: agentfed.CapListFromACP(a), Card: raw,
		})
		return
	}
	probe, err := agentfed.DetectProtocol(ctx, req.Endpoint, auth, nil)
	if err != nil {
		writeErr(w, 502, "discover_failed", err.Error())
		return
	}
	writeJSON(w, 200, probe)
}


func (s *Server) handleFedScanLocal(w http.ResponseWriter, r *http.Request) {
	var opt agentfed.ScanLocalOptions
	if r.Method == http.MethodPost {
		_ = readJSON(r, &opt) // optional body; ignore decode errors for empty body
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := agentfed.ScanLocalDetailed(ctx, opt)
	if err != nil {
		writeErr(w, 500, "scan_failed", err.Error())
		return
	}
	// Merge AgentOS-supervised local agents (already registered on nodes).
	if specs, err := s.st.ListAgents(ctx, ""); err == nil {
		seen := map[string]struct{}{}
		for _, h := range res.Agents {
			seen[h.Protocol+"|"+strings.TrimRight(h.Endpoint, "/")] = struct{}{}
		}
		for _, sp := range specs {
			ep := "local://supervised/" + sp.ID
			key := agentfed.ProtoLocal + "|" + ep
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			desc := sp.Command
			if sp.WorkDir != "" {
				desc = sp.Command + " @ " + sp.WorkDir
			}
			res.Agents = append(res.Agents, agentfed.LocalHit{
				Name:         sp.Name,
				Protocol:     agentfed.ProtoLocal,
				Endpoint:     ep,
				Description:  "已在 Agent OS 托管 · " + desc,
				Capabilities: []string{"supervised", "local"},
				Source:       "supervised",
			})
		}
		res.Count = len(res.Agents)
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleFedCreate(w http.ResponseWriter, r *http.Request) {
	if s.fed.fs == nil {
		writeErr(w, 501, "unsupported", "store does not support federation")
		return
	}
	var req agentfed.CreateRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON")
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Name = strings.TrimSpace(req.Name)
	if req.Endpoint == "" {
		writeErr(w, 400, "bad_request", "endpoint is required")
		return
	}
	auth := agentfed.Auth{Scheme: req.AuthScheme, Header: req.AuthHeader, Token: req.Token}
	if auth.Scheme == "" {
		if auth.Token != "" {
			auth.Scheme = agentfed.AuthBearer
		} else {
			auth.Scheme = agentfed.AuthNone
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	proto := req.Protocol
	var card json.RawMessage
	var caps []string
	desc := ""
	name := req.Name
	endpoint := req.Endpoint
	if req.FetchCard || proto == "" {
		probe, _ := agentfed.DetectProtocol(ctx, req.Endpoint, auth, nil)
		if !probe.OK && proto == "" {
			writeErr(w, 400, "discover_failed", probe.Error)
			return
		}
		if probe.OK {
			if proto == "" {
				proto = probe.Protocol
			}
			if name == "" {
				name = probe.Name
			}
			desc = probe.Description
			card = probe.Card
			caps = probe.Capabilities
			if probe.Endpoint != "" {
				endpoint = probe.Endpoint
			}
		}
	}
	if proto != agentfed.ProtoA2A && proto != agentfed.ProtoACP {
		writeErr(w, 400, "bad_request", "protocol must be a2a or acp")
		return
	}
	if name == "" {
		name = proto + "-agent"
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	sealed, err := s.fed.seal.Seal(req.Token, id)
	if err != nil {
		writeErr(w, 500, "seal_failed", err.Error())
		return
	}
	row := store.FedRow{
		ID: id, Name: name, Description: desc, Protocol: proto, Endpoint: endpoint,
		AuthScheme: auth.Scheme, AuthHeader: auth.Header, AuthToken: sealed,
		AgentCard: card, Capabilities: caps, Metadata: req.Metadata,
		Status: "unknown", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.fed.fs.CreateFedAgent(r.Context(), row); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	// Best-effort connectivity probe after save.
	s.probeAndUpdate(r.Context(), row)
	row, _ = s.fed.fs.GetFedAgent(r.Context(), id)
	writeJSON(w, 201, s.rowToView(row))
}

func (s *Server) handleFedList(w http.ResponseWriter, r *http.Request) {
	if s.fed.fs == nil {
		writeJSON(w, 200, []fedView{})
		return
	}
	proto := r.URL.Query().Get("protocol")
	rows, err := s.fed.fs.ListFedAgents(r.Context(), proto)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]fedView, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.rowToView(row))
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleFedGet(w http.ResponseWriter, r *http.Request) {
	row, err := s.fed.fs.GetFedAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, s.rowToView(row))
}

func (s *Server) handleFedUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := s.fed.fs.GetFedAgent(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req agentfed.CreateRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON")
		return
	}
	if n := strings.TrimSpace(req.Name); n != "" {
		row.Name = n
	}
	if e := strings.TrimSpace(req.Endpoint); e != "" {
		row.Endpoint = e
	}
	if req.Protocol == agentfed.ProtoA2A || req.Protocol == agentfed.ProtoACP {
		row.Protocol = req.Protocol
	}
	if req.AuthScheme != "" {
		row.AuthScheme = req.AuthScheme
	}
	if req.AuthHeader != "" {
		row.AuthHeader = req.AuthHeader
	}
	if req.Token != "" {
		sealed, err := s.fed.seal.Seal(req.Token, id)
		if err != nil {
			writeErr(w, 500, "seal_failed", err.Error())
			return
		}
		row.AuthToken = sealed
	}
	if req.Metadata != nil {
		row.Metadata = req.Metadata
	}
	row.UpdatedAt = time.Now().UTC()
	if err := s.fed.fs.UpdateFedAgent(r.Context(), row); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, s.rowToView(row))
}

func (s *Server) handleFedDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.fed.rt.Stop(id)
	if err := s.fed.fs.DeleteFedAgent(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleFedTest(w http.ResponseWriter, r *http.Request) {
	row, err := s.fed.fs.GetFedAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	ok, msg := s.probeAndUpdate(r.Context(), row)
	row, _ = s.fed.fs.GetFedAgent(r.Context(), row.ID)
	writeJSON(w, 200, map[string]any{"ok": ok, "error": msg, "agent": s.rowToView(row)})
}

func (s *Server) handleFedInvoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := s.fed.fs.GetFedAgent(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req agentfed.InvokeRequest
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeErr(w, 400, "bad_request", "message is required")
		return
	}
	auth, err := s.fedAuth(row)
	if err != nil {
		writeErr(w, 500, "auth_error", err.Error())
		return
	}
	s.fed.rt.AppendLog(id, "system", "invoke: "+truncateRunes(req.Message, 200))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s.fed.rt.SetActive(id, "pending", cancel)

	go func() {
		defer cancel()
		onEv := func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			s.fed.rt.AppendLog(id, "stdout", truncateRunes(line, 4000))
			s.bus.Publish(api.EventAgentLog, map[string]any{
				"agent_id": id, "text": line, "stream": "stdout", "time": time.Now().UTC(),
			})
		}
		var remoteID, state string
		var invokeErr error
		switch row.Protocol {
		case agentfed.ProtoA2A:
			cli, err := agentfed.NewA2AClientFromEndpoint(ctx, row.Endpoint, auth, nil)
			if err != nil {
				invokeErr = err
				break
			}
			if req.Stream || cli.Card.Capabilities.Streaming {
				remoteID, invokeErr = cli.StreamMessage(ctx, req.Message, onEv)
				state = "completed"
				if invokeErr != nil {
					state = "failed"
				}
			} else {
				var raw json.RawMessage
				remoteID, state, raw, invokeErr = cli.SendMessage(ctx, req.Message)
				if t := string(raw); t != "" {
					onEv(t)
				}
			}
		case agentfed.ProtoACP:
			agentName := row.Name
			if row.Metadata != nil && row.Metadata["acp_agent"] != "" {
				agentName = row.Metadata["acp_agent"]
			}
			cli := &agentfed.ACPClient{HTTP: http.DefaultClient, BaseURL: row.Endpoint, Auth: auth}
			run, err := cli.CreateRun(ctx, agentName, req.Message, req.Stream, onEv)
			invokeErr = err
			remoteID, state = run.RunID, agentfed.NormalizeACPState(run.Status)
		default:
			invokeErr = errUnsupportedProto
		}
		if invokeErr != nil {
			s.fed.rt.AppendLog(id, "system", "error: "+invokeErr.Error())
			s.fed.rt.UpdateTaskRemote(id, remoteID, "failed", invokeErr.Error())
			s.fed.rt.ClearActive(id)
			return
		}
		if remoteID == "" {
			remoteID = uuid.NewString()
		}
		s.fed.rt.AddTask(id, row.Protocol, remoteID, firstNonEmpty(state, "completed"), req.Message)
		s.fed.rt.AppendLog(id, "system", "done remote="+remoteID+" state="+state)
		s.fed.rt.ClearActive(id)
	}()

	writeJSON(w, 202, map[string]any{"accepted": true, "agent_id": id})
}

var errUnsupportedProto = errString("unsupported protocol")

type errString string

func (e errString) Error() string { return string(e) }

func (s *Server) handleFedStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := s.fed.fs.GetFedAgent(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	remote := s.fed.rt.ActiveRemote(id)
	s.fed.rt.Stop(id)
	if remote != "" && remote != "pending" {
		auth, _ := s.fedAuth(row)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		switch row.Protocol {
		case agentfed.ProtoA2A:
			if cli, err := agentfed.NewA2AClientFromEndpoint(ctx, row.Endpoint, auth, nil); err == nil {
				_ = cli.CancelTask(ctx, remote)
			}
		case agentfed.ProtoACP:
			cli := &agentfed.ACPClient{HTTP: http.DefaultClient, BaseURL: row.Endpoint, Auth: auth}
			_ = cli.CancelRun(ctx, remote)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleFedTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.fed.fs.GetFedAgent(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, 200, s.fed.rt.ListTasks(id))
}

func (s *Server) handleFedLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.fed.fs.GetFedAgent(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	tail := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			tail = n
		}
	}
	writeJSON(w, 200, map[string]any{"lines": s.fed.rt.Logs(id, tail)})
}

func (s *Server) fedAuth(row store.FedRow) (agentfed.Auth, error) {
	tok, err := s.fed.seal.Open(row.AuthToken, row.ID)
	if err != nil {
		return agentfed.Auth{}, err
	}
	return agentfed.Auth{Scheme: row.AuthScheme, Header: row.AuthHeader, Token: tok}, nil
}

func (s *Server) probeAndUpdate(ctx context.Context, row store.FedRow) (bool, string) {
	auth, err := s.fedAuth(row)
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var ok bool
	var msg string
	switch row.Protocol {
	case agentfed.ProtoA2A:
		card, err := agentfed.FetchAgentCard(ctx, row.Endpoint, auth, nil)
		if err != nil {
			ok, msg = false, err.Error()
		} else {
			ok = true
			row.AgentCard = card.Raw
			row.Capabilities = card.CapList()
			if row.Description == "" {
				row.Description = card.Description
			}
		}
	case agentfed.ProtoACP:
		// Always list remote agents: the run must target a remote agent_name, which is
		// usually not the local display name (bug 0.1.6: POST /runs 404 "Agent <local name> not found").
		agents, err := agentfed.DiscoverACP(ctx, row.Endpoint, auth, nil)
		if err != nil {
			cli := &agentfed.ACPClient{HTTP: http.DefaultClient, BaseURL: row.Endpoint, Auth: auth}
			if perr := cli.Ping(ctx); perr != nil {
				ok, msg = false, err.Error()
			} else {
				ok = true
			}
		} else {
			ok = true
			raw, _ := json.Marshal(agents)
			row.AgentCard = raw
			if row.Metadata == nil {
				row.Metadata = map[string]string{}
			}
			if cur := row.Metadata["acp_agent"]; len(agents) > 0 && !acpHas(agents, cur) {
				pick := agents[0].Name
				for _, a := range agents {
					if a.Name == row.Name {
						pick = a.Name
					}
				}
				row.Metadata["acp_agent"] = pick
			}
		}
	}
	now := time.Now().UTC()
	row.UpdatedAt = now
	row.LastSeen = now
	if ok {
		row.Status = "online"
		row.LastError = ""
	} else {
		row.Status = "error"
		row.LastError = msg
	}
	_ = s.fed.fs.UpdateFedAgent(ctx, row)
	return ok, msg
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func mustRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errString("not int")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// fedAsAgent projects a federated row into the unified Agent list shape.
func (s *Server) fedAsAgent(r store.FedRow) api.Agent {
	v := s.rowToView(r)
	return api.Agent{
		AgentSpec: api.AgentSpec{
			ID: r.ID, Name: r.Name, Description: r.Description,
			Command: "(" + r.Protocol + ")", CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		},
		Status: v.Status,
		Kind:   r.Protocol,
	}
}

func acpHas(agents []agentfed.ACPManifest, name string) bool {
	if name == "" {
		return false
	}
	for _, a := range agents {
		if a.Name == name {
			return true
		}
	}
	return false
}
