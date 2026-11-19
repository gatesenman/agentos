package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/gorilla/websocket"
)

var ErrNodeOffline = errors.New("node is offline")

type nodeConn struct {
	id      string
	ws      *websocket.Conn
	wmu     sync.Mutex
	pmu     sync.Mutex
	pending map[string]chan api.Envelope
	seq     atomic.Uint64
	closed  chan struct{}
}

// Hub tracks connected nodes, request/response correlation and tunnels.
type Hub struct {
	mu      sync.Mutex
	nodes   map[string]*nodeConn
	tunnels map[string]chan *websocket.Conn
}

func NewHub() *Hub {
	return &Hub{nodes: map[string]*nodeConn{}, tunnels: map[string]chan *websocket.Conn{}}
}

func (h *Hub) Online(nodeID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.nodes[nodeID]
	return ok
}

func (nc *nodeConn) send(env api.Envelope, payload any) error {
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		env.Payload = b
	}
	nc.wmu.Lock()
	defer nc.wmu.Unlock()
	nc.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return nc.ws.WriteJSON(env)
}

// Request sends a request to a node and decodes the response payload into out (may be nil).
func (h *Hub) Request(ctx context.Context, nodeID, typ string, payload, out any) error {
	h.mu.Lock()
	nc := h.nodes[nodeID]
	h.mu.Unlock()
	if nc == nil {
		return ErrNodeOffline
	}
	id := "r" + strconv.FormatUint(nc.seq.Add(1), 10)
	ch := make(chan api.Envelope, 1)
	nc.pmu.Lock()
	nc.pending[id] = ch
	nc.pmu.Unlock()
	defer func() { nc.pmu.Lock(); delete(nc.pending, id); nc.pmu.Unlock() }()
	if err := nc.send(api.Envelope{Type: typ, ID: id}, payload); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-nc.closed:
		return ErrNodeOffline
	case resp := <-ch:
		if resp.Error != "" {
			return errors.New(resp.Error)
		}
		if out != nil && len(resp.Payload) > 0 {
			return json.Unmarshal(resp.Payload, out)
		}
		return nil
	}
}

// OpenTunnel asks the node to open a data tunnel and returns the node side
// WebSocket once the node has dialled back (within 15s).
func (h *Hub) OpenTunnel(ctx context.Context, nodeID, kind string, params map[string]string) (*websocket.Conn, error) {
	sid := auth.RandomToken(16)
	ch := make(chan *websocket.Conn, 1)
	h.mu.Lock()
	h.tunnels[sid] = ch
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.tunnels, sid); h.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := h.Request(ctx, nodeID, api.MsgTunnelOpen, api.TunnelOpen{SessionID: sid, Kind: kind, Params: params}, nil); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("tunnel: node did not connect: %w", ctx.Err())
	case c := <-ch:
		return c, nil
	}
}

// handleNodeTunnel: GET /api/v1/node/tunnel/{session} (node token auth).
func (s *Server) handleNodeTunnel(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	s.hub.mu.Lock()
	ch, ok := s.hub.tunnels[sid]
	s.hub.mu.Unlock()
	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	select {
	case ch <- c:
	default:
		c.Close()
	}
}

// Pipe relays messages (preserving text/binary type) between two sockets until either closes.
func Pipe(a, b *websocket.Conn) {
	errc := make(chan error, 2)
	cp := func(dst, src *websocket.Conn) {
		for {
			t, data, err := src.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := dst.WriteMessage(t, data); err != nil {
				errc <- err
				return
			}
		}
	}
	go cp(a, b)
	go cp(b, a)
	<-errc
	a.Close()
	b.Close()
}

// handleNodeConnect: GET /api/v1/node/connect (node token auth). Control channel.
func (s *Server) handleNodeConnect(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	var env api.Envelope
	if err := c.ReadJSON(&env); err != nil || env.Type != api.MsgHello {
		c.WriteJSON(api.Envelope{Type: "error", Error: "expected hello"})
		return
	}
	var hello api.HelloPayload
	if err := json.Unmarshal(env.Payload, &hello); err != nil || hello.NodeID == "" {
		c.WriteJSON(api.Envelope{Type: "error", Error: "invalid hello"})
		return
	}
	if hello.ProtocolVersion != api.ProtocolVersion {
		c.WriteJSON(api.Envelope{Type: "error", Error: fmt.Sprintf("protocol version %d not supported (server %d)", hello.ProtocolVersion, api.ProtocolVersion)})
		return
	}
	forcedName, err := s.checkNodeIdentity(r, &hello)
	if err != nil {
		c.WriteJSON(api.Envelope{Type: "error", Error: err.Error()})
		log.Printf("server: node %s rejected: %v", hello.NodeID, err)
		return
	}
	ctx := context.Background()
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	node, err := s.st.GetNode(ctx, hello.NodeID)
	if err != nil {
		node = api.Node{ID: hello.NodeID, Name: hello.Name, CreatedAt: time.Now().UTC()}
	}
	node.Hostname, node.OS, node.Arch, node.Version = hello.Hostname, hello.OS, hello.Arch, hello.Version
	node.Capabilities, node.RemoteAddr, node.LastSeen = hello.Capabilities, host, time.Now().UTC()
	if forcedName != "" {
		node.Name = forcedName
	}
	if node.Name == "" {
		node.Name = hello.Hostname
	}
	if err := s.st.UpsertNode(ctx, node); err != nil {
		c.WriteJSON(api.Envelope{Type: "error", Error: err.Error()})
		return
	}
	nc := &nodeConn{id: node.ID, ws: c, pending: map[string]chan api.Envelope{}, closed: make(chan struct{})}
	s.hub.mu.Lock()
	if old := s.hub.nodes[node.ID]; old != nil {
		old.ws.Close()
	}
	s.hub.nodes[node.ID] = nc
	s.hub.mu.Unlock()
	defer func() {
		close(nc.closed)
		s.hub.mu.Lock()
		if s.hub.nodes[node.ID] == nc {
			delete(s.hub.nodes, node.ID)
			s.hub.mu.Unlock()
			s.onNodeOffline(node.ID)
		} else {
			s.hub.mu.Unlock()
		}
	}()
	nc.send(api.Envelope{Type: api.MsgWelcome}, api.WelcomePayload{NodeID: node.ID, ServerVersion: s.version})
	for _, st := range hello.Agents {
		s.setStatus(st)
	}
	s.publishNode(node.ID)
	go s.envs.nodeChanged(node.ID)
	log.Printf("server: node %s (%s) connected from %s", node.Name, node.ID, host)

	go func() { // push desired state
		specs, err := s.st.ListAgents(ctx, node.ID)
		if err != nil {
			return
		}
		if err := s.hub.Request(ctx, node.ID, api.MsgAgentSync, api.SyncPayload{Agents: specs}, nil); err != nil {
			log.Printf("server: sync node %s: %v", node.ID, err)
		}
	}()
	go func() { // keepalive
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-nc.closed:
				return
			case <-t.C:
				nc.wmu.Lock()
				err := c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				nc.wmu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
	c.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(90 * time.Second)); return nil })
	for {
		var env api.Envelope
		if err := c.ReadJSON(&env); err != nil {
			if !strings.Contains(err.Error(), "use of closed") {
				log.Printf("server: node %s disconnected: %v", node.ID, err)
			}
			return
		}
		c.SetReadDeadline(time.Now().Add(90 * time.Second))
		s.onNodeMessage(nc, env)
	}
}

func (s *Server) onNodeMessage(nc *nodeConn, env api.Envelope) {
	switch env.Type {
	case api.MsgResponse:
		nc.pmu.Lock()
		ch := nc.pending[env.ReplyTo]
		nc.pmu.Unlock()
		if ch != nil {
			select {
			case ch <- env:
			default:
			}
		}
	case api.MsgEvAgentStatus:
		var st api.AgentStatus
		if json.Unmarshal(env.Payload, &st) == nil {
			st.NodeID = nc.id
			s.setStatus(st)
		}
	case api.MsgEvAgentLog:
		var b api.LogBatch
		if json.Unmarshal(env.Payload, &b) == nil {
			s.recordLogs(b)
		}
	case api.MsgEvNodeStats:
		var ns api.NodeStats
		if json.Unmarshal(env.Payload, &ns) == nil {
			ns.NodeID = nc.id
			s.recordStats(ns)
		}
	default:
		log.Printf("server: unknown message from node %s: %q", nc.id, env.Type)
	}
}
