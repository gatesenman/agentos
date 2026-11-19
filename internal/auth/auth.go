// Package auth implements admin login sessions and node token validation.
// Owned by module M7. The Authenticator interface is a FROZEN contract.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookie = "agentos_session"
	SessionTTL    = 7 * 24 * time.Hour
)

var ErrBadCredentials = errors.New("invalid credentials")

type Authenticator interface {
	// Login checks the admin password and returns a new session token.
	Login(ctx context.Context, password string) (token string, err error)
	Logout(token string)
	ValidSession(token string) bool
	SetPassword(ctx context.Context, newPassword string) error

	NodeToken(ctx context.Context) (string, error)
	RotateNodeToken(ctx context.Context) (string, error)
	ValidNodeToken(ctx context.Context, token string) bool

	// RequireSession wraps admin/browser endpoints (cookie or Bearer).
	RequireSession(next http.Handler) http.Handler
	// RequireNode wraps node endpoints (Bearer node token).
	RequireNode(next http.Handler) http.Handler
}

type Manager struct {
	st      store.Store
	sess    store.SessionStore // nil => in-memory sessions (non-SQLite stores)
	limiter *RateLimiter
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time // in-memory fallback: token hash -> expiry
	// seen: sessions validated against the DB (token hash -> expiry). Used only
	// when the DB lookup fails transiently (busy / timeout under heavy load),
	// so a slow disk does not log users out; NotFound is always authoritative.
	seen map[string]time.Time
}

// bcryptCost is lowered by tests only.
var bcryptCost = bcrypt.DefaultCost

// sessionRenewAfter: sliding renewal writes the new expiry at most this often.
const sessionRenewAfter = time.Hour

// Bootstrap result tells the caller which secrets were freshly generated so
// they can be shown to the operator once.
type Bootstrap struct {
	GeneratedPassword string // first-run plaintext (random or --admin-password) for password-file / reveal
	NodeToken         string
}

// New creates the manager and initialises secrets on first run. If no admin
// password exists, initialPassword is used, or a random one is generated.
func New(ctx context.Context, st store.Store, initialPassword string) (*Manager, Bootstrap, error) {
	m := &Manager{st: st, sessions: map[string]time.Time{}, seen: map[string]time.Time{}, limiter: NewRateLimiter(), now: time.Now}
	if ss, ok := st.(store.SessionStore); ok {
		m.sess = ss
	}
	var b Bootstrap
	if _, err := st.GetSetting(ctx, store.SettingAdminPasswordHash); errors.Is(err, store.ErrNotFound) {
		pw := initialPassword
		if pw == "" {
			pw = RandomToken(6)
		}
		// Always expose the first-run plaintext so callers can write
		// initial-admin-password.txt (tray / login reveal), including when
		// --admin-password / AGENTOS_ADMIN_PASSWORD was provided.
		b.GeneratedPassword = pw
		if err := m.SetPassword(ctx, pw); err != nil {
			return nil, b, err
		}
	} else if err != nil {
		return nil, b, err
	}
	tok, err := st.GetSetting(ctx, store.SettingNodeToken)
	if errors.Is(err, store.ErrNotFound) {
		tok, err = m.RotateNodeToken(ctx)
	}
	if err != nil {
		return nil, b, err
	}
	b.NodeToken = tok
	return m, b, nil
}

func RandomToken(nBytes int) string {
	buf := make([]byte, nBytes)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// LoginLimiter returns the per-IP login rate limiter used by the HTTP handlers.
func (m *Manager) LoginLimiter() *RateLimiter { return m.limiter }

// CheckPassword verifies the admin password without creating a session.
func (m *Manager) CheckPassword(ctx context.Context, password string) error {
	hash, err := m.st.GetSetting(ctx, store.SettingAdminPasswordHash)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrBadCredentials
	}
	return nil
}

func (m *Manager) Login(ctx context.Context, password string) (string, error) {
	if err := m.CheckPassword(ctx, password); err != nil {
		return "", err
	}
	return m.IssueSession(ctx)
}

// IssueSession creates a session without a password check. Used for
// loopback-only desktop trust (AgentOS.exe → 127.0.0.1); not on the frozen
// Authenticator interface — callers type-assert.
func (m *Manager) IssueSession(ctx context.Context) (string, error) {
	tok := RandomToken(32)
	now := m.now()
	if m.sess != nil {
		_, _ = m.sess.PurgeExpiredSessions(ctx, now)
		if err := m.sess.CreateSession(ctx, store.Session{ID: hashToken(tok), CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(SessionTTL)}); err != nil {
			return "", err
		}
		return tok, nil
	}
	m.mu.Lock()
	m.sessions[hashToken(tok)] = now.Add(SessionTTL)
	m.mu.Unlock()
	return tok, nil
}

// hashToken: sessions are stored by SHA-256 so a leaked DB yields no usable token.
func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func bg() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (m *Manager) Logout(token string) {
	if token == "" {
		return
	}
	id := hashToken(token)
	if m.sess != nil {
		m.mu.Lock()
		delete(m.seen, id)
		m.mu.Unlock()
		ctx, cancel := bg()
		defer cancel()
		_ = m.sess.DeleteSession(ctx, id)
		return
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// RevokeOtherSessions deletes every session except keepToken ("" = all).
func (m *Manager) RevokeOtherSessions(ctx context.Context, keepToken string) error {
	keep := ""
	if keepToken != "" {
		keep = hashToken(keepToken)
	}
	if m.sess != nil {
		m.mu.Lock()
		for k := range m.seen {
			if k != keep {
				delete(m.seen, k)
			}
		}
		m.mu.Unlock()
		_, err := m.sess.DeleteSessionsExcept(ctx, keep)
		return err
	}
	m.mu.Lock()
	for k := range m.sessions {
		if k != keep {
			delete(m.sessions, k)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) ValidSession(token string) bool {
	ok, _ := m.checkSession(token)
	return ok
}

// checkSession validates token and applies sliding renewal; renewed reports
// whether the expiry was extended (so the cookie can be refreshed).
func (m *Manager) checkSession(token string) (ok, renewed bool) {
	if token == "" {
		return false, false
	}
	id := hashToken(token)
	now := m.now()
	if m.sess != nil {
		ctx, cancel := bg()
		defer cancel()
		x, err := m.sess.GetSession(ctx, id)
		if err != nil {
			m.mu.Lock()
			defer m.mu.Unlock()
			if errors.Is(err, store.ErrNotFound) {
				delete(m.seen, id)
				return false, false
			}
			// transient DB failure: trust a session this process validated before
			exp, ok := m.seen[id]
			return ok && now.Before(exp), false
		}
		if !now.Before(x.ExpiresAt) {
			_ = m.sess.DeleteSession(ctx, id)
			m.mu.Lock()
			delete(m.seen, id)
			m.mu.Unlock()
			return false, false
		}
		m.mu.Lock()
		m.seen[id] = x.ExpiresAt
		m.mu.Unlock()
		if x.ExpiresAt.Sub(now) < SessionTTL-sessionRenewAfter {
			if m.sess.TouchSession(ctx, id, now, now.Add(SessionTTL)) == nil {
				renewed = true
			}
		}
		return true, renewed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, found := m.sessions[id]
	if !found {
		return false, false
	}
	if !now.Before(exp) {
		delete(m.sessions, id)
		return false, false
	}
	if exp.Sub(now) < SessionTTL-sessionRenewAfter {
		m.sessions[id] = now.Add(SessionTTL)
		renewed = true
	}
	return true, renewed
}

func (m *Manager) SetPassword(ctx context.Context, pw string) error {
	if len(pw) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return err
	}
	return m.st.SetSetting(ctx, store.SettingAdminPasswordHash, string(h))
}

func (m *Manager) NodeToken(ctx context.Context) (string, error) {
	return m.st.GetSetting(ctx, store.SettingNodeToken)
}

func (m *Manager) RotateNodeToken(ctx context.Context) (string, error) {
	tok := "aos_" + RandomToken(24)
	return tok, m.st.SetSetting(ctx, store.SettingNodeToken, tok)
}

func (m *Manager) ValidNodeToken(ctx context.Context, token string) bool {
	want, err := m.NodeToken(ctx)
	if err != nil || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

// TokenFromRequest extracts a Bearer token, falling back to the session cookie
// and finally the "token" query parameter (for WebSocket clients that cannot
// set headers).
func TokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// SetSessionCookie writes the session cookie (Secure when served over TLS).
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: int(SessionTTL.Seconds())})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (m *Manager) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := TokenFromRequest(r)
		ok, renewed := m.checkSession(tok)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","code":"unauthorized"}`))
			return
		}
		if renewed {
			if c, err := r.Cookie(SessionCookie); err == nil && c.Value == tok {
				SetSessionCookie(w, r, tok)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) RequireNode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.ValidNodeToken(r.Context(), TokenFromRequest(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid node token","code":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

var _ Authenticator = (*Manager)(nil)
