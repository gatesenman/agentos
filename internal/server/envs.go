package server

// Environment manager (core, phase 2): persistence with sealed secrets,
// cached provider handles, status/metrics sampling, lifecycle logs, node
// credentials (join tokens) and node<->environment binding.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/env"
	"github.com/agentos/agentos/internal/env/agentosnode"
	"github.com/agentos/agentos/internal/env/providers/agentoslocal"
	"github.com/agentos/agentos/internal/env/providers/agentosremote"
	"github.com/agentos/agentos/internal/store"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Event types for environments (docs/api-envs.md).
const (
	EventEnvCreated = "env.created" // EnvView
	EventEnvUpdated = "env.updated" // EnvView
	EventEnvDeleted = "env.deleted" // {"id"}
	EventEnvLog     = "env.log"     // {"id","t","level","text"}
)

// EnvOptions enables the environment runtime (EnableEnvs).
type EnvOptions struct {
	LocalServerURL string   // URL local instances dial (loopback)
	BuiltinNodeID  string   // id of the server's embedded node ("" = none)
	Exe            string   // executable for local instances (default os.Executable())
	LocalExtraEnv  []string // extra env for local instances (tests)
	SampleInterval time.Duration
}

// EnvView is the REST/event representation of an environment.
type EnvView struct {
	env.Record
	Status       env.Status       `json:"status"`
	Capabilities []env.Capability `json:"capabilities"`
	ProviderInfo providerBrief    `json:"provider_info"`
	SecretsSet   []string         `json:"secrets_set,omitempty"` // names of stored secret fields (values never returned)
	Busy         string           `json:"busy,omitempty"`        // running operation: provisioning | installing | ...
}

type providerBrief struct {
	Type  string   `json:"type"`
	Name  string   `json:"name"`
	Group string   `json:"group"`
	Kind  env.Kind `json:"kind"`
}

type EnvLogEntry struct {
	T     time.Time `json:"t"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

type MetricSample struct {
	T time.Time `json:"t"`
	env.Metrics
}

const (
	envLogRing    = 1000
	metricHistory = 360 // 1h at 10s
	credPrefix    = "aosj_"
)

type envLog struct {
	ring   []EnvLogEntry
	loaded bool
}

type envManager struct {
	s       *Server
	es      store.EnvStore
	seal    *sealer
	logDir  string
	opts    EnvOptions
	enabled bool

	mu      sync.Mutex
	handles map[string]env.Env
	status  map[string]env.Status
	logs    map[string]*envLog
	metrics map[string][]MetricSample
	busy    map[string]string
	stop    context.CancelFunc
}

func newEnvManager(s *Server, dataDir string) *envManager {
	m := &envManager{s: s, handles: map[string]env.Env{}, status: map[string]env.Status{},
		logs: map[string]*envLog{}, metrics: map[string][]MetricSample{}, busy: map[string]string{}}
	m.es, _ = s.st.(store.EnvStore)
	var err error
	if m.seal, err = loadSealer(dataDir); err != nil {
		log.Printf("envs: secret key unavailable (%v), using an ephemeral key", err)
		m.seal, _ = loadSealer("")
	}
	if dataDir != "" {
		m.logDir = filepath.Join(dataDir, "envlogs")
		os.MkdirAll(m.logDir, 0o755)
	}
	return m
}

var errNoEnvStore = errors.New("store does not support environments")

// EnableEnvs configures local instances, ensures the built-in environment,
// opens every environment (auto-starting local instances) and starts the
// status/metrics sampler. Call once, before or after the HTTP server starts.
func (s *Server) EnableEnvs(ctx context.Context, o EnvOptions) error {
	m := s.envs
	if m.es == nil {
		return errNoEnvStore
	}
	if o.SampleInterval <= 0 {
		o.SampleInterval = 10 * time.Second
	}
	m.opts = o
	agentosnode.SetGateway(s)
	if err := agentoslocal.Configure(agentoslocal.Options{
		Root: filepath.Join(filepath.Dir(m.logDirOr()), "localos"), ServerURL: o.LocalServerURL, Exe: o.Exe,
		ExtraEnv: o.LocalExtraEnv,
		Token: func() string {
			t, _ := s.auth.NodeToken(context.Background())
			return t
		},
		Log: func(id, level, text string) { m.log(id, level, text) },
	}); err != nil {
		return fmt.Errorf("local instances: %w", err)
	}
	if o.BuiltinNodeID != "" {
		if err := m.ensureBuiltin(ctx, o.BuiltinNodeID); err != nil {
			return err
		}
	}
	rows, err := m.es.ListEnvs(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.State == env.StateCreating && row.Provider == agentoslocal.Type {
			// interrupted provisioning of a local instance: Open re-creates it
			row.State = env.StateRunning
			m.es.UpdateEnv(ctx, row)
		}
		if row.State == env.StateCreating {
			if p, ok := env.Get(row.Provider); ok {
				if cr, ok := p.(env.CreateResumer); ok {
					if rec, err := m.full(row); err == nil && cr.ResumeCreate(&rec) {
						m.log(row.ID, "warn", "服务重启：继续未完成的创建 / resuming interrupted provisioning")
						m.setBusy(row.ID, "provisioning")
						go m.provision(p, row.ID, rec.Config)
						continue
					}
				}
			}
		}
		if _, err := m.handle(ctx, row); err != nil {
			m.log(row.ID, "error", "打开环境失败 / open failed: "+err.Error())
		}
	}
	sctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.enabled, m.stop = true, cancel
	m.mu.Unlock()
	go m.sampler(sctx)
	return nil
}

// logDirOr returns <data>/server/envlogs or a temp dir.
func (m *envManager) logDirOr() string {
	if m.logDir != "" {
		return m.logDir
	}
	d, _ := os.MkdirTemp("", "agentos-envs-")
	m.logDir = filepath.Join(d, "server", "envlogs")
	os.MkdirAll(m.logDir, 0o755)
	return m.logDir
}

// Close stops local instances and the sampler.
func (s *Server) Close() {
	m := s.envs
	m.mu.Lock()
	stop := m.stop
	handles := m.handles
	m.handles = map[string]env.Env{}
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, h := range handles {
		h.Close()
	}
	if m.enabled {
		agentoslocal.Shutdown()
	}
}

func (m *envManager) ensureBuiltin(ctx context.Context, nodeID string) error {
	row, err := m.es.GetEnv(ctx, agentoslocal.BuiltinEnvID)
	now := time.Now().UTC()
	if errors.Is(err, store.ErrNotFound) {
		name := "本机"
		row = store.EnvRow{Record: env.Record{ID: agentoslocal.BuiltinEnvID, Name: name, Provider: agentoslocal.Type,
			Config: json.RawMessage(`{}`), NodeID: nodeID, Labels: map[string]string{agentoslocal.LabelBuiltin: "true"},
			State: env.StateRunning, CreatedAt: now, UpdatedAt: now}}
		// adopt an env auto-created for this node earlier
		if old, ok := m.envByNode(ctx, nodeID); ok && old.ID != row.ID {
			m.es.DeleteEnv(ctx, old.ID)
		}
		return m.es.CreateEnv(ctx, row)
	}
	if err != nil {
		return err
	}
	if row.NodeID != nodeID {
		row.NodeID, row.UpdatedAt = nodeID, now
		return m.es.UpdateEnv(ctx, row)
	}
	return nil
}

// ---- records, handles, views ----

func (m *envManager) get(ctx context.Context, id string) (store.EnvRow, error) {
	if m.es == nil {
		return store.EnvRow{}, errNoEnvStore
	}
	return m.es.GetEnv(ctx, id)
}

func (m *envManager) envByNode(ctx context.Context, nodeID string) (store.EnvRow, bool) {
	if m.es == nil || nodeID == "" {
		return store.EnvRow{}, false
	}
	rows, _ := m.es.ListEnvs(ctx)
	for _, r := range rows {
		if r.NodeID == nodeID {
			return r, true
		}
	}
	return store.EnvRow{}, false
}

// full returns the record with decrypted secrets merged into Config.
func (m *envManager) full(row store.EnvRow) (env.Record, error) {
	rec := row.Record
	sec, err := m.seal.openSecrets(row.Secrets, row.ID)
	if err != nil {
		return rec, fmt.Errorf("decrypt secrets: %w", err)
	}
	rec.Config = mergeConfig(row.Config, sec)
	if rec.Labels != nil {
		l := make(map[string]string, len(rec.Labels))
		for k, v := range rec.Labels {
			l[k] = v
		}
		rec.Labels = l
	}
	return rec, nil
}

// setConfig splits cfg by the provider schema and stores it in row.
func (m *envManager) setConfig(row *store.EnvRow, cfg json.RawMessage) error {
	p, ok := env.Get(row.Provider)
	if !ok {
		return fmt.Errorf("unknown provider %q", row.Provider)
	}
	pub, sec, err := splitConfig(cfg, secretKeys(p.Info().ConfigSchema))
	if err != nil {
		return err
	}
	sealed, err := m.seal.sealSecrets(sec, row.ID)
	if err != nil {
		return err
	}
	row.Config, row.Secrets = pub, sealed
	return nil
}

func (m *envManager) handle(ctx context.Context, row store.EnvRow) (env.Env, error) {
	m.mu.Lock()
	h, ok := m.handles[row.ID]
	m.mu.Unlock()
	if ok {
		return h, nil
	}
	p, ok := env.Get(row.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", row.Provider)
	}
	rec, err := m.full(row)
	if err != nil {
		return nil, err
	}
	h, err = p.Open(ctx, &rec)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if old, ok := m.handles[row.ID]; ok { // lost a race
		m.mu.Unlock()
		h.Close()
		return old, nil
	}
	m.handles[row.ID] = h
	m.mu.Unlock()
	return h, nil
}

func (m *envManager) dropHandle(id string) {
	m.mu.Lock()
	h := m.handles[id]
	delete(m.handles, id)
	m.mu.Unlock()
	if h != nil {
		h.Close()
	}
}

func (m *envManager) refresh(ctx context.Context, row store.EnvRow) env.Status {
	st := env.Status{State: row.State, Message: row.Error}
	m.mu.Lock()
	provisioning := m.busy[row.ID] == "provisioning"
	m.mu.Unlock()
	if provisioning || row.State == env.StateCreating || row.State == env.StateError {
		// not opened while provisioning / after a failed create
	} else if h, err := m.handle(ctx, row); err != nil {
		st = env.Status{State: env.StateError, Message: err.Error()}
	} else {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		s, err := h.Status(cctx)
		cancel()
		if err != nil {
			st = env.Status{State: env.StateError, Message: err.Error()}
		} else {
			st = s
		}
	}
	m.mu.Lock()
	if b := m.busy[row.ID]; b != "" && st.State != env.StateError {
		if st.State != env.StateRunning || b == "provisioning" {
			st.State = env.StateCreating
		}
	}
	m.status[row.ID] = st
	m.mu.Unlock()
	return st
}

func (m *envManager) cachedStatus(ctx context.Context, row store.EnvRow) env.Status {
	m.mu.Lock()
	st, ok := m.status[row.ID]
	m.mu.Unlock()
	if ok {
		return st
	}
	return m.refresh(ctx, row)
}

func (m *envManager) caps(row store.EnvRow, p env.Provider) []env.Capability {
	info := p.Info()
	set := map[env.Capability]bool{}
	rec := row.Record
	for _, c := range env.CapabilitiesOf(p, &rec) {
		set[c] = true
	}
	if row.NodeID != "" {
		set[env.CapAgents] = true
		online := m.s.hub.Online(row.NodeID)
		nodeCaps := map[string]bool{}
		for _, c := range m.s.NodeCapabilities(row.NodeID) {
			nodeCaps[c] = true
		}
		if info.Kind == env.KindAgentOS {
			// desktop availability follows what the node actually offers
			set[env.CapDesktopVNC] = online && nodeCaps[api.CapVNC]
			set[env.CapDesktopScreen] = online && nodeCaps[api.CapScreen]
		} else if online {
			set[env.CapDesktopVNC] = set[env.CapDesktopVNC] || nodeCaps[api.CapVNC]
			set[env.CapDesktopScreen] = set[env.CapDesktopScreen] || nodeCaps[api.CapScreen]
		}
	}
	if row.Labels[agentoslocal.LabelBuiltin] == "true" {
		set[env.CapLifecycle] = false
	}
	out := []env.Capability{}
	for _, c := range []env.Capability{env.CapLifecycle, env.CapExec, env.CapFiles, env.CapDesktopVNC, env.CapDesktopScreen,
		env.CapMetrics, env.CapAgents, env.CapInstallAgent} {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

func (m *envManager) view(ctx context.Context, row store.EnvRow) EnvView {
	// Auto-heal: docker + linux-desktop already marked installed but virtual_desktop missing.
	if row.Provider == "docker" && row.Labels != nil && row.Labels["stack.linux-desktop"] == "installed" {
		row = m.healDockerVirtualDesktop(ctx, row, true)
	}
	v := EnvView{Record: row.Record, Status: m.cachedStatus(ctx, row)}
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	if len(v.Config) == 0 {
		v.Config = json.RawMessage(`{}`)
	}
	if p, ok := env.Get(row.Provider); ok {
		info := p.Info()
		v.ProviderInfo = providerBrief{Type: info.Type, Name: info.Name, Group: info.Group, Kind: info.Kind}
		v.Capabilities = m.caps(row, p)
	} else {
		v.ProviderInfo = providerBrief{Type: row.Provider, Name: row.Provider}
		v.Capabilities = []env.Capability{}
	}
	if sec, err := m.seal.openSecrets(row.Secrets, row.ID); err == nil && len(sec) > 0 {
		v.SecretsSet = sortedKeys(sec)
	}
	m.mu.Lock()
	v.Busy = m.busy[row.ID]
	m.mu.Unlock()
	v.State = v.Status.State // top-level state mirrors live status; row.State is the persisted desired state
	if v.Status.State == env.StateError && v.Error == "" {
		v.Error = v.Status.Message
	}
	return v
}

func (m *envManager) publish(ctx context.Context, id, typ string) {
	row, err := m.get(ctx, id)
	if err != nil {
		return
	}
	m.s.bus.Publish(typ, m.view(ctx, row))
}

func (m *envManager) refreshAndPublish(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, err := m.get(ctx, id)
	if err != nil {
		return
	}
	m.refresh(ctx, row)
	m.s.bus.Publish(EventEnvUpdated, m.view(ctx, row))
}

func (m *envManager) setBusy(id, op string) {
	m.mu.Lock()
	if op == "" {
		delete(m.busy, id)
	} else {
		m.busy[id] = op
	}
	m.mu.Unlock()
}

// trySetBusy sets busy only when the env is idle. Returns the prior op (empty if idle).
func (m *envManager) trySetBusy(id, op string) (prev string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev = m.busy[id]
	if prev != "" {
		return prev, false
	}
	m.busy[id] = op
	return "", true
}

// ---- lifecycle log ----

func (m *envManager) logFile(id string) string {
	if m.logDir == "" || strings.ContainsAny(id, `/\.`) {
		return ""
	}
	return filepath.Join(m.logDir, id+".log")
}

func (m *envManager) loadLog(id string) *envLog {
	l, ok := m.logs[id]
	if ok {
		return l
	}
	l = &envLog{}
	if p := m.logFile(id); p != "" {
		if f, err := os.Open(p); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for sc.Scan() {
				var e EnvLogEntry
				if json.Unmarshal(sc.Bytes(), &e) == nil {
					l.ring = append(l.ring, e)
					if len(l.ring) > 2*envLogRing {
						l.ring = append([]EnvLogEntry(nil), l.ring[len(l.ring)-envLogRing:]...)
					}
				}
			}
			f.Close()
		}
	}
	if len(l.ring) > envLogRing {
		l.ring = append([]EnvLogEntry(nil), l.ring[len(l.ring)-envLogRing:]...)
	}
	m.logs[id] = l
	return l
}

func (m *envManager) log(id, level, text string) {
	e := EnvLogEntry{T: time.Now().UTC(), Level: level, Text: text}
	m.mu.Lock()
	l := m.loadLog(id)
	l.ring = append(l.ring, e)
	if len(l.ring) > envLogRing {
		l.ring = l.ring[len(l.ring)-envLogRing:]
	}
	if p := m.logFile(id); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			b, _ := json.Marshal(e)
			f.Write(append(b, '\n'))
			if fi, err := f.Stat(); err == nil && fi.Size() > 4<<20 { // keep the file bounded
				f.Close()
				os.Rename(p, p+".1")
			} else {
				f.Close()
			}
		}
	}
	m.mu.Unlock()
	m.s.bus.Publish(EventEnvLog, map[string]any{"id": id, "t": e.T, "level": level, "text": text})
}

func (m *envManager) tailLog(id string, n int) []EnvLogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.loadLog(id)
	out := l.ring
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return append([]EnvLogEntry{}, out...)
}

func (m *envManager) forget(id string) {
	m.dropHandle(id)
	m.mu.Lock()
	delete(m.status, id)
	delete(m.logs, id)
	delete(m.metrics, id)
	delete(m.busy, id)
	m.mu.Unlock()
	if p := m.logFile(id); p != "" {
		os.Remove(p)
		os.Remove(p + ".1")
	}
}

// ---- sampler ----

func (m *envManager) sampler(ctx context.Context) {
	t := time.NewTicker(m.opts.SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := m.es.ListEnvs(ctx)
		if err != nil {
			continue
		}
		for _, row := range rows {
			m.mu.Lock()
			prev, had := m.status[row.ID]
			m.mu.Unlock()
			st := m.refresh(ctx, row)
			if !had || !reflect.DeepEqual(prev, st) {
				m.s.bus.Publish(EventEnvUpdated, m.view(ctx, row))
			}
			if st.State != env.StateRunning {
				continue
			}
			v := m.view(ctx, row)
			if !hasCap(v.Capabilities, env.CapMetrics) {
				continue
			}
			h, err := m.handle(ctx, row)
			if err != nil {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			mt, err := h.Metrics(cctx)
			cancel()
			if err != nil {
				continue
			}
			m.mu.Lock()
			hs := append(m.metrics[row.ID], MetricSample{T: time.Now().UTC(), Metrics: mt})
			if len(hs) > metricHistory {
				hs = hs[len(hs)-metricHistory:]
			}
			m.metrics[row.ID] = hs
			m.mu.Unlock()
		}
	}
}

func hasCap(cs []env.Capability, c env.Capability) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// ---- node credentials & binding ----

func hashToken(t string) string { s := sha256.Sum256([]byte(t)); return hex.EncodeToString(s[:]) }

// issueCredential creates a fresh one-time join token for envID (replacing
// any previous one) and returns it. The first node connecting with it is bound.
func (m *envManager) issueCredential(ctx context.Context, envID string) (string, error) {
	tok := credPrefix + auth.RandomToken(24)
	err := m.es.PutNodeCredential(ctx, store.NodeCredential{EnvID: envID, TokenHash: hashToken(tok)})
	return tok, err
}

type credCtxKey struct{}

// requireNode accepts the shared node token or a per-environment credential.
func (s *Server) requireNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := auth.TokenFromRequest(r)
		if s.auth.ValidNodeToken(r.Context(), tok) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(tok, credPrefix) && s.envs.es != nil {
			if c, err := s.envs.es.GetNodeCredentialByHash(r.Context(), hashToken(tok)); err == nil {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), credCtxKey{}, c)))
				return
			}
		}
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid node token")
	})
}

// checkNodeIdentity runs after hello. It enforces credential binding and
// revocation and links the node to its environment. It returns the display
// name to force on the node record ("" = keep).
func (s *Server) checkNodeIdentity(r *http.Request, hello *api.HelloPayload) (string, error) {
	m := s.envs
	if m.es == nil {
		return "", nil
	}
	ctx := r.Context()
	cred, hasCred := ctx.Value(credCtxKey{}).(store.NodeCredential)
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()
	if !hasCred && !enabled {
		return "", nil
	}
	if !hasCred {
		if revoked, _ := m.es.NodeRevoked(ctx, hello.NodeID); revoked {
			return "", errors.New("节点已被撤销 / this node has been revoked")
		}
		if _, ok := m.envByNode(ctx, hello.NodeID); !ok && hello.NodeID != m.opts.BuiltinNodeID {
			m.autoEnv(ctx, hello)
		}
		return "", nil
	}
	if cred.NodeID != "" && cred.NodeID != hello.NodeID {
		return "", errors.New("令牌已绑定到其他节点 / token is bound to another node")
	}
	row, err := m.es.GetEnv(ctx, cred.EnvID)
	if err != nil {
		return "", errors.New("environment no longer exists")
	}
	if err := m.es.BindNodeCredential(ctx, cred.EnvID, hello.NodeID); err != nil {
		return "", errors.New("令牌已绑定到其他节点 / token is bound to another node")
	}
	m.es.UnrevokeNode(ctx, hello.NodeID)
	firstBind := cred.NodeID == "" || row.NodeID != hello.NodeID
	if row.NodeID != hello.NodeID {
		if other, ok := m.envByNode(ctx, hello.NodeID); ok && other.ID != row.ID && other.Labels[agentosremote.LabelAuto] == "true" {
			m.es.DeleteEnv(ctx, other.ID) // replace the auto-listed env
			m.forget(other.ID)
			s.bus.Publish(EventEnvDeleted, map[string]string{"id": other.ID})
		}
		row.NodeID, row.UpdatedAt = hello.NodeID, time.Now().UTC()
		if row.State == env.StateCreating && row.Provider == agentosremote.Type {
			row.State = env.StateRunning
		}
		m.es.UpdateEnv(ctx, row)
		m.dropHandle(row.ID)
	}
	if firstBind {
		m.log(row.ID, "info", fmt.Sprintf("Agent OS 节点已加入 / node joined: %s (%s, %s/%s)", hello.Hostname, hello.NodeID, hello.OS, hello.Arch))
		m.setBusy(row.ID, "")
	}
	hello.Name = row.Name
	return row.Name, nil
}

// autoEnv lists a node that joined with the shared token as an environment.
func (m *envManager) autoEnv(ctx context.Context, hello *api.HelloPayload) {
	if _, err := m.es.GetEnv(ctx, hello.NodeID); err == nil {
		return // local instance whose node id is its env id (being created)
	}
	now := time.Now().UTC()
	name := hello.Name
	if name == "" {
		name = hello.Hostname
	}
	row := store.EnvRow{Record: env.Record{ID: uuid.NewString(), Name: name, Provider: agentosremote.Type,
		Config: json.RawMessage(`{}`), NodeID: hello.NodeID, Labels: map[string]string{agentosremote.LabelAuto: "true"},
		State: env.StateRunning, CreatedAt: now, UpdatedAt: now}}
	if err := m.es.CreateEnv(ctx, row); err == nil {
		go m.publish(context.Background(), row.ID, EventEnvCreated)
	}
}

// nodeChanged is called when a node connects or disconnects.
func (m *envManager) nodeChanged(nodeID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if row, ok := m.envByNode(ctx, nodeID); ok {
		m.refreshAndPublish(row.ID)
	}
}

func (s *Server) disconnectNode(nodeID string) {
	s.hub.mu.Lock()
	nc := s.hub.nodes[nodeID]
	s.hub.mu.Unlock()
	if nc != nil {
		nc.ws.Close()
	}
}

// removeNode deletes a node record and its agents (after its env was deleted).
func (s *Server) removeNode(ctx context.Context, nodeID string) {
	specs, _ := s.st.ListAgents(ctx, nodeID)
	for _, a := range specs {
		s.st.DeleteAgent(ctx, a.ID)
		s.mu.Lock()
		delete(s.statuses, a.ID)
		s.mu.Unlock()
		s.dropAgentStats(a.ID)
		s.bus.Publish(api.EventAgentRemoved, map[string]string{"id": a.ID})
	}
	if s.st.DeleteNode(ctx, nodeID) == nil {
		s.dropNodeStats(nodeID)
		s.bus.Publish(api.EventNodeRemoved, map[string]string{"id": nodeID})
	}
}

// ---- agentosnode.Gateway implementation ----

func (s *Server) NodeOnline(nodeID string) bool { return s.hub.Online(nodeID) }

func (s *Server) NodeCapabilities(nodeID string) []string {
	n, err := s.st.GetNode(context.Background(), nodeID)
	if err != nil {
		return nil
	}
	return n.Capabilities
}

func (s *Server) OpenTunnel(ctx context.Context, nodeID, kind string, params map[string]string) (*websocket.Conn, error) {
	return s.hub.OpenTunnel(ctx, nodeID, kind, params)
}

func (s *Server) ListDir(ctx context.Context, nodeID, path string) (api.FsListResponse, error) {
	var out api.FsListResponse
	err := s.hub.Request(ctx, nodeID, api.MsgFsList, api.FsListRequest{Path: path}, &out)
	return out, err
}

func (s *Server) HostStats(nodeID string) (api.HostStats, bool) { return s.stats.latestHost(nodeID) }

var _ agentosnode.Gateway = (*Server)(nil)

func sortViews(v []EnvView) {
	sort.SliceStable(v, func(i, j int) bool {
		bi, bj := v[i].Labels[agentoslocal.LabelBuiltin] == "true", v[j].Labels[agentoslocal.LabelBuiltin] == "true"
		if bi != bj {
			return bi
		}
		return v[i].CreatedAt.Before(v[j].CreatedAt)
	})
}

// ---- stacks wiring (internal/stacks, handlers_stacks.go) ----

// openEnv resolves an environment id to a live handle for the stacks engine.
func (s *Server) openEnv(ctx context.Context, id string) (env.Env, func(), error) {
	row, err := s.envs.get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("%w: %s", env.ErrNotFound, id)
	}
	if err != nil {
		return nil, nil, err
	}
	h, err := s.envs.handle(ctx, row)
	return h, nil, err
}

// envLog appends to /envs/{id}/logs and publishes env.log.
func (s *Server) envLog(id, level, text string) { s.envs.log(id, level, text) }

// onStackInstalled records the installed stack on the environment.
// On failure it writes a clear error log line (install already streams step
// errors; this is the terminal summary so the UI/logs never look "silent").
// For linux-desktop on docker headless images it also flips virtual_desktop so
// the desktop tab and VNC relay become available without recreating the env.
func (s *Server) onStackInstalled(envID, stackID string, err error) {
	if err != nil {
		s.envLog(envID, "error", "[stack:"+stackID+"] 安装失败："+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row, gerr := s.envs.get(ctx, envID)
	if gerr != nil {
		s.envLog(envID, "error", "[stack:"+stackID+"] 安装完成但读取环境失败："+gerr.Error())
		return
	}
	row.Stack, row.UpdatedAt = stackID, time.Now().UTC()
	if row.Labels == nil {
		row.Labels = map[string]string{}
	}
	row.Labels["stack."+stackID] = "installed"
	if stackID == "linux-desktop" && row.Provider == "docker" {
		if raw, merr := mergeRecordConfig(row.Config, map[string]any{"virtual_desktop": true}); merr != nil {
			s.envLog(envID, "error", "[stack:linux-desktop] 合并 virtual_desktop 失败："+merr.Error())
		} else {
			row.Config = raw
			s.envLog(envID, "info", "[stack:linux-desktop] 已启用按需虚拟桌面（打开「桌面」页签即可）")
		}
	}
	if uerr := s.envs.es.UpdateEnv(ctx, row); uerr != nil {
		s.envLog(envID, "error", "[stack:"+stackID+"] UpdateEnv 失败："+uerr.Error())
		return
	}
	// Drop cached handle so Desktop()/Capabilities pick up virtual_desktop and other config merges.
	if stackID == "linux-desktop" && row.Provider == "docker" {
		s.envs.dropHandle(envID)
	}
	s.bus.Publish(EventEnvUpdated, s.envs.view(ctx, row))
}

// afterStackDetect auto-heals docker virtual_desktop when linux-desktop is detected installed.
func (s *Server) afterStackDetect(envID string, installed []string) {
	hasLD := false
	for _, id := range installed {
		if id == "linux-desktop" {
			hasLD = true
			break
		}
	}
	if !hasLD {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	row, err := s.envs.get(ctx, envID)
	if err != nil {
		return
	}
	s.envs.healDockerVirtualDesktop(ctx, row, true)
}

// healDockerVirtualDesktop sets virtual_desktop when a docker env already has
// linux-desktop installed but the config flag was missed (Done race / UpdateEnv error).
// Returns the possibly-updated row.
func (m *envManager) healDockerVirtualDesktop(ctx context.Context, row store.EnvRow, linuxDesktopInstalled bool) store.EnvRow {
	if row.Provider != "docker" || !linuxDesktopInstalled {
		return row
	}
	if configHasTrue(row.Config, "virtual_desktop") {
		// Flag already in store; drop any handle opened before the flag so Desktop() sees it.
		m.dropHandle(row.ID)
		return row
	}
	raw, err := mergeRecordConfig(row.Config, map[string]any{"virtual_desktop": true})
	if err != nil {
		m.log(row.ID, "error", "[heal] 合并 virtual_desktop 失败："+err.Error())
		return row
	}
	row.Config = raw
	row.UpdatedAt = time.Now().UTC()
	if row.Labels == nil {
		row.Labels = map[string]string{}
	}
	row.Labels["stack.linux-desktop"] = "installed"
	if uerr := m.es.UpdateEnv(ctx, row); uerr != nil {
		m.log(row.ID, "error", "[heal] UpdateEnv virtual_desktop 失败："+uerr.Error())
		return row
	}
	m.dropHandle(row.ID) // reopen with VirtualDesktop on next Desktop()/handle
	m.log(row.ID, "info", "[heal] 已补写 virtual_desktop（linux-desktop 已安装）")
	m.s.bus.Publish(EventEnvUpdated, m.view(ctx, row))
	return row
}

func configHasTrue(raw json.RawMessage, key string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	m := map[string]any{}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

// mergeRecordConfig sets keys on a JSON object config, preserving unknown fields.
func mergeRecordConfig(raw json.RawMessage, set map[string]any) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	for k, v := range set {
		m[k] = v
	}
	return json.Marshal(m)
}
