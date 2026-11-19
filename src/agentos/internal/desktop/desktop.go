// Package desktop defines remote-desktop providers that run on the node and
// serve a tunnel WebSocket opened by the server. FROZEN contract.
// vnc/ is owned by module M4, screen/ by module M5.
package desktop

import "context"

// WebSocket message types (same values as gorilla/websocket).
const (
	TextMessage   = 1
	BinaryMessage = 2
)

// Conn is a message-oriented connection (a *websocket.Conn satisfies it).
// Implementations must not call WriteMessage concurrently from several goroutines.
type Conn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type Provider interface {
	Kind() string       // api.TunnelVNC or api.TunnelScreen
	Capability() string // api.CapVNC or api.CapScreen
	Available() bool    // cheap check, used for hello capabilities and on demand
	// Serve blocks until the session ends. params come from api.TunnelOpen.
	Serve(ctx context.Context, conn Conn, params map[string]string) error
}
