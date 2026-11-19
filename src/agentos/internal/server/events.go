package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/gorilla/websocket"
)

// Bus fans out api.Events to browser subscribers.
type Bus struct {
	mu   sync.Mutex
	subs map[chan api.Event]func(api.Event) bool
}

func NewBus() *Bus { return &Bus{subs: map[chan api.Event]func(api.Event) bool{}} }

// Subscribe returns a buffered channel receiving events accepted by filter
// (nil = all). Slow consumers drop events.
func (b *Bus) Subscribe(filter func(api.Event) bool) (<-chan api.Event, func()) {
	ch := make(chan api.Event, 256)
	b.mu.Lock()
	b.subs[ch] = filter
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(typ string, payload any) {
	ev := api.Event{Type: typ, Time: time.Now().UTC(), Payload: payload}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch, f := range b.subs {
		if f != nil && !f(ev) {
			continue
		}
		select {
		case ch <- ev:
		default:
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	Subprotocols:    []string{"binary"}, // noVNC
	CheckOrigin:     auth.CheckOrigin,   // implemented by M7
}

// streamEvents pumps bus events matching filter to a browser WebSocket.
// initial events (if any) are sent first.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, filter func(api.Event) bool, initial []api.Event) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	ch, cancel := s.bus.Subscribe(filter)
	defer cancel()
	done := make(chan struct{})
	go func() { // reader: detect close
		defer close(done)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
	for _, ev := range initial {
		if c.WriteJSON(ev) != nil {
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
			c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if c.WriteJSON(ev) != nil {
				return
			}
		}
	}
}

func (s *Server) routesEvents(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	// GET /api/v1/events (WebSocket): all events except agent.log.
	mux.Handle("GET /api/v1/events", authed(func(w http.ResponseWriter, r *http.Request) {
		s.streamEvents(w, r, func(e api.Event) bool { return e.Type != api.EventAgentLog }, nil)
	}))
}
