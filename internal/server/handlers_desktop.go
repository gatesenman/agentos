package server

// Owned by module M4 (remote desktop).

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/agentos/agentos/internal/api"
)

// CloseDesktopError is the WebSocket close code sent to the browser when the
// node reports that the desktop session could not be established (e.g. VNC
// server down). The close reason carries the node's message.
const CloseDesktopError = 4502

func (s *Server) routesDesktop(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	// GET /api/v1/nodes/{id}/desktop?kind=vnc|screen[&addr=..][&quality=..&fps=..&display=..&scale=..] (WebSocket)
	// Relays browser <-> node tunnel. vnc: raw RFB (noVNC); screen: docs/protocol.md §5.
	mux.Handle("GET /api/v1/nodes/{id}/desktop", authed(func(w http.ResponseWriter, r *http.Request) {
		s.serveNodeDesktop(w, r, r.PathValue("id"))
	}))
}

// serveNodeDesktop relays a desktop WebSocket to nodeID (also used by
// /envs/{id}/desktop for environments with a bound node).
func (s *Server) serveNodeDesktop(w http.ResponseWriter, r *http.Request, nodeID string) {
	{
		q := r.URL.Query()
		kind := q.Get("kind")
		if kind == "" {
			kind = api.TunnelVNC
		}
		if kind != api.TunnelVNC && kind != api.TunnelScreen {
			writeErr(w, 400, "bad_request", "kind must be vnc or screen")
			return
		}
		if !s.hub.Online(nodeID) {
			writeStoreErr(w, ErrNodeOffline)
			return
		}
		params := map[string]string{}
		keys := []string{"quality", "fps", "display", "scale"}
		if kind == api.TunnelVNC {
			keys = []string{"addr"} // validated (loopback only) on the node
		}
		for _, k := range keys {
			if v := q.Get(k); v != "" {
				params[k] = v
			}
		}
		nodeWS, err := s.hub.OpenTunnel(r.Context(), nodeID, kind, params)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "tunnel_failed", err.Error())
			return
		}
		browserWS, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			nodeWS.Close()
			return
		}
		log.Printf("server: desktop session %s on node %s / 远程桌面会话开始", kind, nodeID)
		if kind == api.TunnelVNC {
			relayVNC(browserWS, nodeWS)
		} else {
			Pipe(browserWS, nodeWS)
		}
		log.Printf("server: desktop session %s on node %s ended / 远程桌面会话结束", kind, nodeID)
	}
}

// relayVNC pipes RFB bytes between browser and node. A text frame from the
// node is an error report ({"type":"error","message"}); it is turned into a
// close frame (code CloseDesktopError, reason = message) so the browser can
// show it, since noVNC only understands binary RFB data.
func relayVNC(browser, node *websocket.Conn) {
	done := make(chan struct{}, 2)
	closeBrowser := func(code int, reason string) {
		browser.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, truncateReason(reason)),
			time.Now().Add(time.Second))
	}
	go func() { // node -> browser
		defer func() { done <- struct{}{} }()
		for {
			t, data, err := node.ReadMessage()
			if err != nil {
				closeBrowser(websocket.CloseNormalClosure, "")
				return
			}
			if t == websocket.TextMessage {
				var m struct{ Type, Message string }
				if json.Unmarshal(data, &m) == nil && m.Type == "error" {
					closeBrowser(CloseDesktopError, m.Message)
					return
				}
				continue // unknown control text: never feed it to the RFB stream
			}
			if err := browser.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		}
	}()
	go func() { // browser -> node
		defer func() { done <- struct{}{} }()
		for {
			_, data, err := browser.ReadMessage()
			if err != nil {
				return
			}
			if err := node.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		}
	}()
	<-done
	node.Close()
	browser.Close()
	<-done
}

// truncateReason keeps a close reason within the 123-byte limit without
// splitting a UTF-8 sequence.
func truncateReason(s string) string {
	const max = 123
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
