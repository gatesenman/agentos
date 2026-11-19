package server

// Owned by module M7 (auth & persistence).

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/agentos/agentos/internal/auth"
)

// Optional capabilities of auth.Manager, discovered by type assertion so the
// frozen Authenticator interface stays unchanged.
type (
	limiterProvider interface{ LoginLimiter() *auth.RateLimiter }
	passwordChecker interface {
		CheckPassword(ctx context.Context, pw string) error
	}
	sessionRevoker interface {
		RevokeOtherSessions(ctx context.Context, keepToken string) error
	}
	sessionIssuer interface {
		IssueSession(ctx context.Context) (string, error)
	}
)

// fallbackLimiters is used when the Authenticator has no limiter of its own.
var fallbackLimiters sync.Map // *Server -> *auth.RateLimiter

func (s *Server) loginLimiter() *auth.RateLimiter {
	if lp, ok := s.auth.(limiterProvider); ok && lp.LoginLimiter() != nil {
		return lp.LoginLimiter()
	}
	l, _ := fallbackLimiters.LoadOrStore(s, auth.NewRateLimiter())
	return l.(*auth.RateLimiter)
}

func (s *Server) checkPassword(ctx context.Context, pw string) error {
	if pc, ok := s.auth.(passwordChecker); ok {
		return pc.CheckPassword(ctx, pw)
	}
	tok, err := s.auth.Login(ctx, pw)
	if err == nil {
		s.auth.Logout(tok)
	}
	return err
}

// rateLimited writes 429 if ip is locked out.
func rateLimited(w http.ResponseWriter, l *auth.RateLimiter, ip string) bool {
	ok, retry := l.Allow(ip)
	if ok {
		return false
	}
	secs := int(retry/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请 "+strconv.Itoa(secs)+" 秒后再试 / too many attempts, retry later")
	return true
}

func (s *Server) routesAuth(mux *http.ServeMux, authed func(http.HandlerFunc) http.Handler) {
	mux.HandleFunc("POST /api/v1/auth/local", func(w http.ResponseWriter, r *http.Request) {
		// Passwordless session for AgentOS.exe (and any loopback browser).
		// Remote LAN access to :7800 still requires POST /auth/login.
		if !auth.IsLoopbackRequest(r) {
			writeErr(w, http.StatusForbidden, "forbidden", "仅本机可用 / local loopback only")
			return
		}
		si, ok := s.auth.(sessionIssuer)
		if !ok {
			writeErr(w, 500, "internal", "local session not supported")
			return
		}
		tok, err := si.IssueSession(r.Context())
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		auth.SetSessionCookie(w, r, tok)
		writeJSON(w, 200, map[string]string{"token": tok})
	})
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		lim, ip := s.loginLimiter(), auth.ClientIP(r)
		if rateLimited(w, lim, ip) {
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON")
			return
		}
		tok, err := s.auth.Login(r.Context(), body.Password)
		if errors.Is(err, auth.ErrBadCredentials) {
			lim.Failure(ip)
			writeErr(w, 401, "bad_credentials", "密码错误 / invalid password")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", err.Error())
			return
		}
		lim.Success(ip)
		auth.SetSessionCookie(w, r, tok)
		writeJSON(w, 200, map[string]string{"token": tok})
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		s.auth.Logout(auth.TokenFromRequest(r))
		auth.ClearSessionCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /api/v1/auth/me", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"authenticated": true, "user": "admin"})
	}))
	mux.Handle("POST /api/v1/auth/password", authed(func(w http.ResponseWriter, r *http.Request) {
		lim, ip := s.loginLimiter(), auth.ClientIP(r)
		if rateLimited(w, lim, ip) {
			return
		}
		var body struct {
			OldPassword string `json:"old_password"`
			NewPassword string `json:"new_password"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, "bad_request", "invalid JSON")
			return
		}
		if err := s.checkPassword(r.Context(), body.OldPassword); err != nil {
			if errors.Is(err, auth.ErrBadCredentials) {
				lim.Failure(ip)
				writeErr(w, 403, "bad_credentials", "旧密码错误 / old password incorrect")
			} else {
				writeErr(w, 500, "internal", err.Error())
			}
			return
		}
		if err := s.auth.SetPassword(r.Context(), body.NewPassword); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		// Keep tray/login reveal file in sync (cfg.DataDir is the server/ subdir).
		if s.cfg.DataDir != "" {
			_ = os.MkdirAll(s.cfg.DataDir, 0o755)
			_ = os.WriteFile(filepath.Join(s.cfg.DataDir, "initial-admin-password.txt"), []byte(body.NewPassword+"\n"), 0o600)
		}
		// Changing the password signs out every other session.
		if sr, ok := s.auth.(sessionRevoker); ok {
			_ = sr.RevokeOtherSessions(r.Context(), auth.TokenFromRequest(r))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.Handle("GET /api/v1/settings/node-token", authed(func(w http.ResponseWriter, r *http.Request) {
		tok, err := s.auth.NodeToken(r.Context())
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"token": tok})
	}))
	mux.Handle("POST /api/v1/settings/node-token/rotate", authed(func(w http.ResponseWriter, r *http.Request) {
		tok, err := s.auth.RotateNodeToken(r.Context())
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"token": tok})
	}))
}
