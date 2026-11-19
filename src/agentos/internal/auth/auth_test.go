package auth

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock() *clock               { return &clock{t: time.Now()} }

func newMgr(t *testing.T, path string) (*Manager, store.Store) {
	t.Helper()
	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, _, err := New(context.Background(), st, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	return m, st
}

func TestLoginOkBad(t *testing.T) {
	m, _ := newMgr(t, filepath.Join(t.TempDir(), "db"))
	ctx := context.Background()
	if _, err := m.Login(ctx, "wrong"); err != ErrBadCredentials {
		t.Fatalf("want ErrBadCredentials, got %v", err)
	}
	tok, err := m.Login(ctx, "secret123")
	if err != nil || len(tok) != 64 {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if !m.ValidSession(tok) || m.ValidSession("") || m.ValidSession(tok+"x") {
		t.Fatal("session validity wrong")
	}
	m.Logout(tok)
	if m.ValidSession(tok) {
		t.Fatal("valid after logout")
	}
}

func TestBootstrapGeneratesSecrets(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, b, err := New(context.Background(), st, "")
	if err != nil || b.GeneratedPassword == "" || !strings.HasPrefix(b.NodeToken, "aos_") {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := m.Login(context.Background(), b.GeneratedPassword); err != nil {
		t.Fatal(err)
	}
	_, b2, _ := New(context.Background(), st, "")
	if b2.GeneratedPassword != "" || b2.NodeToken != b.NodeToken {
		t.Fatalf("second boot regenerated secrets: %+v", b2)
	}
}


func TestBootstrapProvidedPasswordExposesPlaintext(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, b, err := New(context.Background(), st, "admin123")
	if err != nil {
		t.Fatal(err)
	}
	if b.GeneratedPassword != "admin123" {
		t.Fatalf("want provided password exposed for initial-admin-password.txt, got %q", b.GeneratedPassword)
	}
}

func TestSessionStoredHashed(t *testing.T) {
	m, st := newMgr(t, filepath.Join(t.TempDir(), "db"))
	tok, _ := m.Login(context.Background(), "secret123")
	ss := st.(store.SessionStore)
	if _, err := ss.GetSession(context.Background(), tok); err != store.ErrNotFound {
		t.Fatal("raw token stored in DB")
	}
	if _, err := ss.GetSession(context.Background(), hashToken(tok)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionExpiryAndSlidingRenewal(t *testing.T) {
	m, _ := newMgr(t, filepath.Join(t.TempDir(), "db"))
	c := newClock()
	m.now = c.Now
	tok, _ := m.Login(context.Background(), "secret123")
	c.Add(30 * time.Minute)
	if ok, renewed := m.checkSession(tok); !ok || renewed {
		t.Fatalf("ok=%v renewed=%v (no renewal within first hour)", ok, renewed)
	}
	c.Add(6 * 24 * time.Hour) // 6d 0.5h after login: still valid, renewed
	if ok, renewed := m.checkSession(tok); !ok || !renewed {
		t.Fatalf("ok=%v renewed=%v", ok, renewed)
	}
	c.Add(SessionTTL - time.Minute) // still inside renewed window
	if !m.ValidSession(tok) {
		t.Fatal("sliding renewal did not extend session")
	}
	c.Add(SessionTTL + time.Second) // idle past TTL
	if m.ValidSession(tok) {
		t.Fatal("expired session accepted")
	}
}

func TestSessionPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	m, st := newMgr(t, path)
	tok, _ := m.Login(context.Background(), "secret123")
	st.Close()
	m2, _ := newMgr(t, path)
	if !m2.ValidSession(tok) {
		t.Fatal("session lost after re-open")
	}
}

func TestPasswordChangeAndRevoke(t *testing.T) {
	m, _ := newMgr(t, filepath.Join(t.TempDir(), "db"))
	ctx := context.Background()
	a, _ := m.Login(ctx, "secret123")
	b, _ := m.Login(ctx, "secret123")
	if err := m.SetPassword(ctx, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := m.SetPassword(ctx, "newpass1"); err != nil {
		t.Fatal(err)
	}
	if m.CheckPassword(ctx, "secret123") != ErrBadCredentials || m.CheckPassword(ctx, "newpass1") != nil {
		t.Fatal("password not changed")
	}
	m.RevokeOtherSessions(ctx, a)
	if !m.ValidSession(a) || m.ValidSession(b) {
		t.Fatal("revoke wrong")
	}
	m.RevokeOtherSessions(ctx, "")
	if m.ValidSession(a) {
		t.Fatal("revoke all failed")
	}
}

func TestNodeTokenRotate(t *testing.T) {
	m, _ := newMgr(t, filepath.Join(t.TempDir(), "db"))
	ctx := context.Background()
	old, _ := m.NodeToken(ctx)
	if !m.ValidNodeToken(ctx, old) || m.ValidNodeToken(ctx, "") || m.ValidNodeToken(ctx, "aos_x") {
		t.Fatal("node token check wrong")
	}
	nw, err := m.RotateNodeToken(ctx)
	if err != nil || nw == old {
		t.Fatal(err)
	}
	if m.ValidNodeToken(ctx, old) || !m.ValidNodeToken(ctx, nw) {
		t.Fatal("rotation did not invalidate old token")
	}
	h := m.RequireNode(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for tok, want := range map[string]int{old: 401, nw: 204} {
		r := httptest.NewRequest("GET", "/api/v1/node/connect", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("token %s: %d want %d", tok, w.Code, want)
		}
	}
}

func TestInMemoryFallback(t *testing.T) {
	// A Store without SessionStore still works (sessions in memory).
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "db"))
	defer st.Close()
	m, _, err := New(context.Background(), struct{ store.Store }{st}, "secret123")
	if err != nil || m.sess != nil {
		t.Fatal(err)
	}
	tok, _ := m.Login(context.Background(), "secret123")
	if !m.ValidSession(tok) {
		t.Fatal()
	}
	m.Logout(tok)
	if m.ValidSession(tok) {
		t.Fatal()
	}
}

func TestRequireSessionAndCookie(t *testing.T) {
	m, _ := newMgr(t, filepath.Join(t.TempDir(), "db"))
	c := newClock()
	m.now = c.Now
	tok, _ := m.Login(context.Background(), "secret123")
	h := m.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	do := func(mod func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
		mod(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(func(r *http.Request) {}); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := do(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := do(func(r *http.Request) { r.URL.RawQuery = "token=" + tok }); w.Code != 200 {
		t.Fatal(w.Code)
	}
	c.Add(2 * time.Hour) // triggers renewal -> cookie refreshed, Secure under TLS
	w := do(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
		r.TLS = &tls.ConnectionState{}
	})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	ck := w.Result().Cookies()
	if len(ck) != 1 || ck[0].Value != tok || !ck[0].Secure || !ck[0].HttpOnly {
		t.Fatalf("renewed cookie %+v", ck)
	}
}

func TestRateLimiter(t *testing.T) {
	c := newClock()
	l := NewRateLimiter()
	l.Now = c.Now
	for i := 0; i < 4; i++ {
		l.Failure("1.2.3.4")
	}
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Fatal("locked too early")
	}
	l.Failure("1.2.3.4")
	ok, retry := l.Allow("1.2.3.4")
	if ok || retry != time.Minute {
		t.Fatalf("ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("5.6.7.8"); !ok {
		t.Fatal("other IP affected")
	}
	c.Add(time.Minute)
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Fatal("not released")
	}
	for i := 0; i < 5; i++ { // second lockout doubles
		l.Failure("1.2.3.4")
	}
	if _, retry := l.Allow("1.2.3.4"); retry != 2*time.Minute {
		t.Fatalf("backoff %v", retry)
	}
	for k := 0; k < 10; k++ { // capped
		c.Add(time.Hour / 4)
		for i := 0; i < 5; i++ {
			l.Failure("1.2.3.4")
		}
	}
	if _, retry := l.Allow("1.2.3.4"); retry != l.MaxLock {
		t.Fatalf("cap %v", retry)
	}
	l.Success("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Fatal("success did not reset")
	}
	// failures spread beyond the window never lock
	for i := 0; i < 10; i++ {
		l.Failure("9.9.9.9")
		c.Add(20 * time.Second)
	}
	if ok, _ := l.Allow("9.9.9.9"); !ok {
		t.Fatal("slow failures locked")
	}
}

func TestCheckOrigin(t *testing.T) {
	cases := []struct {
		host, origin, env string
		want              bool
	}{
		{"localhost:7800", "", "", true}, // node / CLI
		{"localhost:7800", "http://localhost:7800", "", true},
		{"LOCALHOST:7800", "http://localhost:7800", "", true},
		{"192.168.1.5:7800", "http://192.168.1.5:7800", "", true},
		{"example.com", "https://example.com", "", true},
		{"example.com", "https://example.com:443", "", true},
		{"example.com", "http://example.com:80", "", true},
		{"[::1]:7800", "http://[::1]:7800", "", true},
		{"localhost:7800", "http://localhost:7801", "", false},
		{"localhost:7800", "http://evil.com", "", false},
		{"localhost:7800", "http://localhost:7800.evil.com", "", false},
		{"example.com", "https://example.com:8443", "", false},
		{"localhost:7800", "null", "", false},
		{"localhost:7800", "file://", "", false},
		{"localhost:7800", "chrome-extension://abc", "", false},
		{"127.0.0.1:7800", "https://agentos.example.com", "https://agentos.example.com", true},
		{"127.0.0.1:7800", "https://other.example.com", "https://agentos.example.com", false},
		{"127.0.0.1:7800", "http://agentos.example.com", "https://agentos.example.com", false},
	}
	for _, c := range cases {
		t.Setenv(AllowedOriginsEnv, c.env)
		r := httptest.NewRequest("GET", "/api/v1/events", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := CheckOrigin(r); got != c.want {
			t.Errorf("host=%s origin=%s env=%s: got %v want %v", c.host, c.origin, c.env, got, c.want)
		}
	}
}
