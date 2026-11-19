package auth_test

import (
	"net/http"
	"testing"

	"github.com/agentos/agentos/internal/auth"
)

func TestHTTPLocalSessionLoopbackOnly(t *testing.T) {
	ts := newServer(t)

	// httptest uses 127.0.0.1 — must succeed without password.
	resp, out := post(t, ts.URL+"/api/v1/auth/local", "", nil)
	if resp.StatusCode != 200 || out["token"] == "" {
		t.Fatalf("loopback local: want 200+token, got %d %v", resp.StatusCode, out)
	}
	ck := resp.Cookies()
	if len(ck) < 1 || ck[0].Name != auth.SessionCookie {
		t.Fatalf("cookie %+v", ck)
	}
	tok := out["token"]
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	me, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	if me.StatusCode != 200 {
		t.Fatalf("me: %d", me.StatusCode)
	}
}

func TestIsLoopbackRequest(t *testing.T) {
	mk := func(addr string) *http.Request {
		r, _ := http.NewRequest("GET", "http://x/", nil)
		r.RemoteAddr = addr
		return r
	}
	if !auth.IsLoopbackRequest(mk("127.0.0.1:1234")) {
		t.Fatal("127.0.0.1")
	}
	if !auth.IsLoopbackRequest(mk("[::1]:9")) {
		t.Fatal("::1")
	}
	if auth.IsLoopbackRequest(mk("192.168.1.10:9")) {
		t.Fatal("LAN must not be loopback")
	}
	if auth.IsLoopbackRequest(mk("10.0.0.5:7800")) {
		t.Fatal("10.x must not be loopback")
	}
}
