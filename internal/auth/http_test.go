package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/server"
	"github.com/agentos/agentos/internal/store"
	"github.com/gorilla/websocket"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	am, _, err := auth.New(context.Background(), st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(server.Config{}, st, am).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, url, tok string, body any) (*http.Response, map[string]string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]string{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestHTTPLoginRateLimit(t *testing.T) {
	ts := newServer(t)
	u := ts.URL + "/api/v1/auth/login"
	for i := 0; i < 5; i++ {
		if resp, out := post(t, u, "", map[string]string{"password": "nope"}); resp.StatusCode != 401 || out["code"] != "bad_credentials" {
			t.Fatalf("attempt %d: %d %v", i, resp.StatusCode, out)
		}
	}
	resp, out := post(t, u, "", map[string]string{"password": "secret123"})
	if resp.StatusCode != 429 || out["code"] != "rate_limited" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("want 429 rate_limited, got %d %v", resp.StatusCode, out)
	}
}

func TestHTTPLoginCookieLogoutPassword(t *testing.T) {
	ts := newServer(t)
	resp, out := post(t, ts.URL+"/api/v1/auth/login", "", map[string]string{"password": "secret123"})
	if resp.StatusCode != 200 || out["token"] == "" {
		t.Fatal(resp.StatusCode)
	}
	ck := resp.Cookies()
	if len(ck) != 1 || ck[0].Name != auth.SessionCookie || !ck[0].HttpOnly || ck[0].Secure {
		t.Fatalf("cookie %+v", ck)
	}
	tok := out["token"]
	_, out2 := post(t, ts.URL+"/api/v1/auth/login", "", map[string]string{"password": "secret123"})
	other := out2["token"]

	// wrong old password -> 403, no session leaked
	if resp, _ := post(t, ts.URL+"/api/v1/auth/password", tok, map[string]string{"old_password": "x", "new_password": "newpass1"}); resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := post(t, ts.URL+"/api/v1/auth/password", tok, map[string]string{"old_password": "secret123", "new_password": "abc"}); resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := post(t, ts.URL+"/api/v1/auth/password", tok, map[string]string{"old_password": "secret123", "new_password": "newpass1"}); resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	me := func(tk string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+tk)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if me(tok) != 200 || me(other) != 401 {
		t.Fatalf("after password change: current=%d other=%d", me(tok), me(other))
	}
	// node token rotate via API
	_, nt := post(t, ts.URL+"/api/v1/settings/node-token/rotate", tok, nil)
	if !strings.HasPrefix(nt["token"], "aos_") {
		t.Fatal(nt)
	}
	if resp, _ := post(t, ts.URL+"/api/v1/auth/logout", tok, nil); resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	if me(tok) != 401 {
		t.Fatal("valid after logout")
	}
}

func TestHTTPWebSocketOrigin(t *testing.T) {
	ts := newServer(t)
	_, out := post(t, ts.URL+"/api/v1/auth/login", "", map[string]string{"password": "secret123"})
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/events?token=" + out["token"]
	dial := func(origin string) int {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, resp, err := websocket.DefaultDialer.Dial(wsURL, h)
		if err == nil {
			c.Close()
			return 101
		}
		if resp == nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if code := dial(ts.URL); code != 101 {
		t.Fatalf("same origin: %d", code)
	}
	if code := dial(""); code != 101 {
		t.Fatalf("no origin: %d", code)
	}
	if code := dial("http://evil.example"); code != 403 {
		t.Fatalf("cross origin: %d", code)
	}
}
