//go:build windows

package agentfed

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// localRegistryBases reads HKCU/HKLM Uninstall + known agent host keys for
// localhost URL / port hints (source tag "registry" in ScanLocal).
func localRegistryBases() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" {
			return
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return
		}
		low := strings.ToLower(u)
		if !(strings.Contains(low, "://127.0.0.1") || strings.Contains(low, "://localhost") || strings.Contains(low, "://[::1]")) {
			return
		}
		// origin only
		rest := u
		if i := strings.Index(rest, "://"); i >= 0 {
			rest = rest[i+3:]
		}
		hostport := rest
		if i := strings.IndexAny(hostport, "/?#"); i >= 0 {
			hostport = hostport[:i]
		}
		scheme := "http"
		if strings.HasPrefix(strings.ToLower(u), "https://") {
			scheme = "https"
		}
		base := scheme + "://" + hostport
		if _, ok := seen[base]; ok {
			return
		}
		seen[base] = struct{}{}
		out = append(out, base)
	}
	addPort := func(port int) {
		if port > 0 && port < 65536 {
			add(fmt.Sprintf("http://127.0.0.1:%d", port))
		}
	}

	roots := []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE}
	for _, root := range roots {
		for _, sub := range []string{
			`Software\AgentOS`,
			`Software\Anthropic\Claude`,
			`Software\Cursor`,
			`Software\Microsoft\Windows\CurrentVersion\Uninstall`,
			`Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
		} {
			k, err := registry.OpenKey(root, sub, registry.READ)
			if err != nil {
				continue
			}
			harvestRegKey(k, add, addPort)
			// Uninstall: walk one level of subkeys
			names, _ := k.ReadSubKeyNames(256)
			k.Close()
			if !strings.Contains(strings.ToLower(sub), `uninstall`) {
				continue
			}
			for _, name := range names {
				sk, err := registry.OpenKey(root, sub+`\`+name, registry.READ)
				if err != nil {
					continue
				}
				display, _, _ := sk.GetStringValue("DisplayName")
				low := strings.ToLower(display + " " + name)
				if strings.Contains(low, "claude") || strings.Contains(low, "codex") || strings.Contains(low, "cursor") ||
					strings.Contains(low, "gemini") || strings.Contains(low, "agentos") || strings.Contains(low, "aider") ||
					strings.Contains(low, "openhands") || strings.Contains(low, "ollama") {
					harvestRegKey(sk, add, addPort)
				}
				sk.Close()
			}
		}
	}
	return out
}

func harvestRegKey(k registry.Key, add func(string), addPort func(int)) {
	for _, name := range []string{"URL", "Endpoint", "BaseURL", "ServerURL", "HomePage", "InstallLocation", "DisplayIcon"} {
		v, _, err := k.GetStringValue(name)
		if err != nil {
			continue
		}
		add(v)
		// crude http harvest inside path-like values
		low := strings.ToLower(v)
		for _, proto := range []string{"http://127.0.0.1", "http://localhost", "https://127.0.0.1", "https://localhost"} {
			if i := strings.Index(low, proto); i >= 0 {
				j := i
				for j < len(v) {
					c := v[j]
					if c <= 32 || strings.ContainsRune("\"'<>) ],}", rune(c)) {
						break
					}
					j++
				}
				add(v[i:j])
			}
		}
	}
	for _, name := range []string{"Port", "ListenPort", "A2APort", "ACPPort"} {
		if n, _, err := k.GetIntegerValue(name); err == nil {
			addPort(int(n))
		} else if s, _, err := k.GetStringValue(name); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				addPort(n)
			}
		}
	}
}
