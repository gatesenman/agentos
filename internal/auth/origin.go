package auth

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AllowedOriginsEnv lists extra allowed browser origins (comma separated,
// e.g. "https://agentos.example.com") for reverse-proxy setups where the
// Host header seen by the server differs from the browser's origin.
const AllowedOriginsEnv = "AGENTOS_ALLOWED_ORIGINS"

// CheckOrigin is used by the server's WebSocket upgrader for browser and node
// sockets. Requests without an Origin header (nodes, CLI tools) are allowed;
// browser requests must be same-origin (Origin host == Host header, scheme
// default ports normalised) or listed in AGENTOS_ALLOWED_ORIGINS.
func CheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if sameHost(u.Scheme, u.Host, r.Host) {
		return true
	}
	for _, o := range strings.Split(os.Getenv(AllowedOriginsEnv), ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if a, err := url.Parse(o); err == nil && strings.EqualFold(a.Scheme, u.Scheme) && sameHost(u.Scheme, a.Host, u.Host) {
			return true
		}
	}
	return false
}

func sameHost(scheme, a, b string) bool {
	return strings.EqualFold(normHost(scheme, a), normHost(scheme, b))
}

func normHost(scheme, h string) string {
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host, port = h, ""
	}
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(scheme)]
	}
	return strings.Trim(host, "[]") + ":" + port
}
