package server_test

// M4 acceptance: fake RFB server -> node vnc provider -> tunnel -> server ->
// browser-side WebSocket.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agentos/agentos/internal/api"
	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/node"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
)

func fakeRFBServer(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte("RFB 003.008\n"))
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

type desktopEnv struct {
	base, token, nodeID string
}

func startDesktopEnv(t *testing.T, vncAddr string) desktopEnv {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.OpenSQLite(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	am, boot, err := auth.New(ctx, st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(server.Config{}, st, am).Handler())
	n, err := node.New(node.Config{ServerURL: ts.URL, Token: boot.NodeToken, Name: "desk", DataDir: filepath.Join(dir, "node"), VNCAddr: vncAddr})
	if err != nil {
		t.Fatal(err)
	}
	nodeDone := make(chan struct{})
	go func() { n.Run(ctx); close(nodeDone) }()
	t.Cleanup(func() { cancel(); <-nodeDone; ts.Close(); st.Close() })

	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewReader([]byte(`{"password":"secret123"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var tok struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	env := desktopEnv{base: ts.URL, token: tok.Token, nodeID: n.ID()}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/nodes/"+n.ID(), nil)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		if r, err := http.DefaultClient.Do(req); err == nil {
			var nd api.Node
			json.NewDecoder(r.Body).Decode(&nd)
			r.Body.Close()
			if nd.Status == api.NodeOnline {
				return env
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("node did not come online")
	return env
}

func (e desktopEnv) dial(t *testing.T, query string) (*websocket.Conn, *http.Response, error) {
	u := "ws" + strings.TrimPrefix(e.base, "http") + "/api/v1/nodes/" + e.nodeID + "/desktop?" + query + "&token=" + url.QueryEscape(e.token)
	d := websocket.Dialer{Subprotocols: []string{"binary"}, HandshakeTimeout: 20 * time.Second}
	return d.Dial(u, nil)
}

func TestDesktopVNCTunnel(t *testing.T) {
	env := startDesktopEnv(t, fakeRFBServer(t))
	ws, resp, err := env.dial(t, "kind=vnc")
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close()
	if resp.Header.Get("Sec-WebSocket-Protocol") != "binary" {
		t.Errorf("subprotocol %q", resp.Header.Get("Sec-WebSocket-Protocol"))
	}
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	mt, data, err := ws.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || string(data) != "RFB 003.008\n" {
		t.Fatalf("greeting %d %q %v", mt, data, err)
	}
	payload := []byte("RFB 003.008\n\x01hello-echo")
	if err := ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for len(got) < len(payload) {
		mt, data, err := ws.ReadMessage()
		if err != nil || mt != websocket.BinaryMessage {
			t.Fatalf("echo read %d %v", mt, err)
		}
		got = append(got, data...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo %q", got)
	}
}

func expectClose(t *testing.T, env desktopEnv, query, contains string) {
	t.Helper()
	ws, resp, err := env.dial(t, query)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, _, err = ws.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != server.CloseDesktopError || !strings.Contains(ce.Text, contains) {
		t.Fatalf("want close %d containing %q, got %v", server.CloseDesktopError, contains, err)
	}
}

func TestDesktopVNCErrors(t *testing.T) {
	env := startDesktopEnv(t, fakeRFBServer(t))
	// VNC server down (loopback port nobody listens on)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	expectClose(t, env, "kind=vnc&addr="+url.QueryEscape(dead), "无法连接")
	// non-loopback target refused by the node
	expectClose(t, env, "kind=vnc&addr="+url.QueryEscape("192.0.2.1:5900"), "不允许")
	// bad kind
	_, resp, err := env.dial(t, "kind=rdp")
	if err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("kind=rdp: %v %v", err, resp)
	}
}
