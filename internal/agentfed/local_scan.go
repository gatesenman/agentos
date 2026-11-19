package agentfed

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultLocalPorts are common localhost ports where A2A/ACP agent hosts listen.
var DefaultLocalPorts = []int{
	// Agent OS / local management
	7800, 7801, 7802, 7803, 7804, 7805, 7806, 7807, 7808, 7809, 7810,
	17800, 17801, 27800,
	// Common agent / LLM / IDE hosts
	3000, 3001, 3210, 4000, 5000, 5173, 5678, 7860,
	8000, 8001, 8080, 8081, 8090, 8888, 8733, 9000, 9090, 9100,
	11434, // Ollama
	18789, // common ACP / sidecar
	3847, 4123, 7331, 7777, 9999,
}

// A2ACardPaths are well-known Agent Card locations relative to a base URL.
var A2ACardPaths = []string{
	"/.well-known/agent-card.json",
	"/.well-known/agent.json",
	"/agent-card.json",
	"/agent-card",
	"/agent.json",
}

// LocalHit is one discovered local A2A/ACP endpoint or installed CLI agent.
type LocalHit struct {
	Name         string          `json:"name"`
	Protocol     string          `json:"protocol"` // a2a | acp | local
	Endpoint     string          `json:"endpoint"`
	Description  string          `json:"description,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Card         json.RawMessage `json:"card,omitempty"`
	Source       string          `json:"source"` // port | listen | config | registry | cli | supervised
}

// ScanLocalOptions controls ScanLocal.
type ScanLocalOptions struct {
	Ports      []int         `json:"ports,omitempty"`
	HTTPClient *http.Client  `json:"-"`
	Timeout    time.Duration `json:"-"` // per-probe; default 600ms
	// ExtraBases are additional http://127.0.0.1:N bases (no path).
	ExtraBases []string `json:"extra_bases,omitempty"`
	// SkipConfig skips reading AppData / registry-derived config hints.
	SkipConfig bool `json:"skip_config,omitempty"`
	// SkipListen skips enumerating OS listening loopback ports.
	SkipListen bool `json:"skip_listen,omitempty"`
	// SkipCLI skips detecting installed agent CLIs on PATH.
	SkipCLI bool `json:"skip_cli,omitempty"`
	// MaxListenPorts caps how many discovered listen ports are probed (default 128).
	MaxListenPorts int `json:"max_listen_ports,omitempty"`
}

// ScanLocalResult is returned to API callers (ports_scanned for empty-state copy).
type ScanLocalResult struct {
	Agents       []LocalHit `json:"agents"`
	Count        int        `json:"count"`
	PortsScanned int        `json:"ports_scanned"`
	ListenPorts  int        `json:"listen_ports"`
	CLIFound     int        `json:"cli_found"`
}

// ScanLocal probes this machine for A2A agent cards and ACP /agents lists,
// merges OS loopback listeners, and surfaces installed agent CLIs.
func ScanLocal(ctx context.Context, opt ScanLocalOptions) ([]LocalHit, error) {
	res, err := ScanLocalDetailed(ctx, opt)
	if err != nil {
		return nil, err
	}
	return res.Agents, nil
}

// ScanLocalDetailed is ScanLocal plus ports_scanned / cli counters for the API.
func ScanLocalDetailed(ctx context.Context, opt ScanLocalOptions) (ScanLocalResult, error) {
	ports := opt.Ports
	if len(ports) == 0 {
		ports = append([]int{}, DefaultLocalPorts...)
	}
	listenN := 0
	if !opt.SkipListen {
		lp := enumerateListeningLoopbackPorts(ctx)
		listenN = len(lp)
		maxL := opt.MaxListenPorts
		if maxL <= 0 {
			maxL = 128
		}
		if len(lp) > maxL {
			lp = lp[:maxL]
		}
		ports = mergePorts(ports, lp)
	}
	per := opt.Timeout
	if per <= 0 {
		per = 600 * time.Millisecond
	}
	hc := opt.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: per}
	}

	bases := map[string]string{} // base -> source
	defaultSet := map[int]struct{}{}
	for _, p := range DefaultLocalPorts {
		defaultSet[p] = struct{}{}
	}
	for _, p := range ports {
		if p <= 0 || p > 65535 {
			continue
		}
		src := "port"
		if _, ok := defaultSet[p]; !ok {
			src = "listen"
		}
		bases[fmt.Sprintf("http://127.0.0.1:%d", p)] = src
	}
	for _, b := range opt.ExtraBases {
		b = strings.TrimRight(strings.TrimSpace(b), "/")
		if b != "" {
			bases[b] = "port"
		}
	}
	if !opt.SkipConfig {
		for _, b := range localConfigBases() {
			b = strings.TrimRight(strings.TrimSpace(b), "/")
			if b != "" {
				if _, ok := bases[b]; !ok {
					bases[b] = "config"
				}
			}
		}
		for _, b := range localRegistryBases() {
			b = strings.TrimRight(strings.TrimSpace(b), "/")
			if b != "" {
				if _, ok := bases[b]; !ok {
					bases[b] = "registry"
				}
			}
		}
	}

	type job struct {
		base, source string
	}
	jobs := make([]job, 0, len(bases))
	for b, src := range bases {
		jobs = append(jobs, job{b, src})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].base < jobs[j].base })

	var (
		mu   sync.Mutex
		hits []LocalHit
		seen = map[string]struct{}{} // protocol|endpoint
	)
	add := func(h LocalHit) {
		key := h.Protocol + "|" + strings.TrimRight(h.Endpoint, "/")
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		hits = append(hits, h)
	}

	// Discover CLIs in parallel with HTTP probes — users care about Claude/Codex first,
	// and PATH walks must not wait behind dozens of closed-port TCP probes.
	cliHits := 0
	var cliWG sync.WaitGroup
	if !opt.SkipCLI {
		cliWG.Add(1)
		go func() {
			defer cliWG.Done()
			cli := discoverInstalledCLIs()
			mu.Lock()
			cliHits = len(cli)
			mu.Unlock()
			for _, h := range cli {
				add(h)
			}
		}()
	}

	sem := make(chan struct{}, 12)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			// Quick TCP gate — skip closed ports without waiting full HTTP timeout twice.
			if uhost, uport, ok := loopbackHostPort(j.base); ok {
				d := net.Dialer{Timeout: 150 * time.Millisecond}
				c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(uhost, uport))
				if err != nil {
					return
				}
				_ = c.Close()
			}
			pctx, cancel := context.WithTimeout(ctx, per)
			defer cancel()
			if card, err := probeLocalA2A(pctx, j.base, hc); err == nil {
				rpc, _, _ := card.ResolveInterface()
				ep := firstNonEmpty(rpc, j.base)
				add(LocalHit{
					Name: firstNonEmpty(card.Name, "A2A Agent"), Protocol: ProtoA2A,
					Endpoint: ep, Description: card.Description, Capabilities: card.CapList(),
					Card: card.Raw, Source: j.source,
				})
			}
			if agents, err := DiscoverACP(pctx, j.base, Auth{Scheme: AuthNone}, hc); err == nil && len(agents) > 0 {
				raw, _ := json.Marshal(agents)
				for _, a := range agents {
					name := firstNonEmpty(a.Name, "ACP Agent")
					add(LocalHit{
						Name: name, Protocol: ProtoACP, Endpoint: j.base,
						Description: a.Description, Capabilities: CapListFromACP(a),
						Card: raw, Source: j.source,
					})
				}
			}
		}()
	}
	wg.Wait()
	cliWG.Wait()

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Protocol != hits[j].Protocol {
			return hits[i].Protocol < hits[j].Protocol
		}
		return hits[i].Endpoint < hits[j].Endpoint
	})
	if hits == nil {
		hits = []LocalHit{}
	}
	return ScanLocalResult{
		Agents:       hits,
		Count:        len(hits),
		PortsScanned: len(bases),
		ListenPorts:  listenN,
		CLIFound:     cliHits,
	}, nil
}

func loopbackHostPort(base string) (host, port string, ok bool) {
	base = strings.TrimPrefix(base, "http://")
	base = strings.TrimPrefix(base, "https://")
	if i := strings.IndexByte(base, '/'); i >= 0 {
		base = base[:i]
	}
	h, p, err := net.SplitHostPort(base)
	if err != nil {
		return "", "", false
	}
	if h != "127.0.0.1" && !strings.EqualFold(h, "localhost") && h != "::1" {
		return "", "", false
	}
	return h, p, true
}

func probeLocalA2A(ctx context.Context, base string, hc *http.Client) (AgentCard, error) {
	base = strings.TrimRight(base, "/")
	card, err := FetchAgentCard(ctx, base, Auth{Scheme: AuthNone}, hc)
	if err != nil {
		// Explicit well-known paths (FetchAgentCard already tries several; retry agent.json variants).
		var last error = err
		for _, p := range A2ACardPaths {
			c2, e2 := FetchAgentCard(ctx, base+p, Auth{Scheme: AuthNone}, hc)
			if e2 == nil {
				return c2, nil
			}
			last = e2
		}
		return AgentCard{}, last
	}
	return card, nil
}

// localConfigBases reads common agent-host config dirs for endpoint hints.
// Hard time budget + shallow walk: a deep ~/.cursor walk previously made
// 「探查本机」appear stuck for tens of seconds on developer machines.
func localConfigBases() []string {
	var out []string
	addURL := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return
		}
		low := strings.ToLower(s)
		if !(strings.Contains(low, "://127.0.0.1") || strings.Contains(low, "://localhost") || strings.Contains(low, "://[::1]")) {
			return
		}
		rest := s
		if i := strings.Index(rest, "://"); i >= 0 {
			rest = rest[i+3:]
		}
		hostport := rest
		if i := strings.IndexAny(hostport, "/?#"); i >= 0 {
			hostport = hostport[:i]
		}
		scheme := "http"
		if strings.HasPrefix(strings.ToLower(s), "https://") {
			scheme = "https"
		}
		out = append(out, scheme+"://"+hostport)
	}

	deadline := time.Now().Add(2500 * time.Millisecond)
	home, _ := os.UserHomeDir()
	dirs := []string{}
	if home != "" {
		// Prefer small, known config roots — do NOT walk entire ~/.cursor trees.
		dirs = append(dirs,
			filepath.Join(home, ".claude"),
			filepath.Join(home, ".codex"),
			filepath.Join(home, ".gemini"),
			filepath.Join(home, ".agentos"),
			filepath.Join(home, ".ollama"),
			filepath.Join(home, ".openhands"),
			filepath.Join(home, ".config", "claude"),
			filepath.Join(home, ".config", "codex"),
			filepath.Join(home, ".config", "ollama"),
		)
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			dirs = append(dirs,
				filepath.Join(la, "AnthropicClaude"),
				filepath.Join(la, "Claude"),
				filepath.Join(la, "AgentOS"),
			)
		}
		if ad := os.Getenv("APPDATA"); ad != "" {
			dirs = append(dirs,
				filepath.Join(ad, "Claude"),
				filepath.Join(ad, "AgentOS"),
			)
		}
	}
	skipDirName := map[string]struct{}{
		"node_modules": {}, ".git": {}, "Cache": {}, "cache": {}, "CachedData": {},
		"Code Cache": {}, "GPUCache": {}, "logs": {}, "log": {}, "tmp": {}, "temp": {},
		"projects": {}, "extensions": {}, "User": {}, // cursor/vscode heavy trees
	}
	seenDir := map[string]struct{}{}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if time.Now().After(deadline) {
			break
		}
		if _, ok := seenDir[d]; ok {
			continue
		}
		seenDir[d] = struct{}{}
		_ = filepath.WalkDir(d, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if time.Now().After(deadline) {
				return filepath.SkipAll
			}
			if de.IsDir() {
				base := de.Name()
				if _, skip := skipDirName[base]; skip && path != d {
					return filepath.SkipDir
				}
				rel, _ := filepath.Rel(d, path)
				if rel != "." && len(strings.Split(rel, string(os.PathSeparator))) > 2 {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(de.Name())
			if !(strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonc") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".cfg") || strings.HasSuffix(name, ".conf")) {
				return nil
			}
			info, err := de.Info()
			if err != nil || info.Size() > 256<<10 {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(b)
			for _, proto := range []string{"http://127.0.0.1", "http://localhost", "https://127.0.0.1", "https://localhost"} {
				start := 0
				for {
					i := strings.Index(strings.ToLower(text[start:]), proto)
					if i < 0 {
						break
					}
					i += start
					j := i
					for j < len(text) {
						c := text[j]
						if c <= 32 || strings.ContainsRune("\"'<>) ],}", rune(c)) {
							break
						}
						j++
					}
					addURL(text[i:j])
					start = j
				}
			}
			return nil
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
