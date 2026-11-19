package agentfed

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// knownCLIAgents maps binary name → display metadata for local install hits.
var knownCLIAgents = []struct {
	Bin         string
	Name        string
	StackID     string
	Description string
	Command     []string
}{
	{"claude", "Claude Code", "claude-code", "本机已安装 Claude Code CLI（可注册为本地智能体）", []string{"claude"}},
	{"codex", "OpenAI Codex CLI", "openai-codex-cli", "本机已安装 Codex CLI", []string{"codex"}},
	{"gemini", "Gemini CLI", "gemini-cli", "本机已安装 Gemini CLI", []string{"gemini"}},
	{"aider", "Aider", "aider", "本机已安装 Aider", []string{"aider"}},
	{"openhands", "OpenHands", "openhands", "本机已安装 OpenHands", []string{"openhands", "serve"}},
	{"ollama", "Ollama", "ollama", "本机已安装 Ollama（本地模型服务）", []string{"ollama", "serve"}},
}

// discoverInstalledCLIs finds agent CLIs on PATH and common install dirs.
// Returns LocalHit with Protocol=local, Source=cli, Endpoint=local://<bin>.
func discoverInstalledCLIs() []LocalHit {
	augmentCLIPath()
	var hits []LocalHit
	seen := map[string]struct{}{}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	progIndex := indexProgramsBins()
	for _, a := range knownCLIAgents {
		path := lookPathCLI(a.Bin+exe, progIndex)
		if path == "" {
			path = lookPathCLI(a.Bin, progIndex)
		}
		if path == "" {
			continue
		}
		key := strings.ToLower(a.Bin)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		meta, _ := json.Marshal(map[string]any{
			"bin":      a.Bin,
			"path":     path,
			"stack_id": a.StackID,
			"command":  a.Command,
			"kind":     "cli",
		})
		hits = append(hits, LocalHit{
			Name:         a.Name,
			Protocol:     ProtoLocal,
			Endpoint:     "local://" + a.Bin,
			Description:  a.Description + " · " + path,
			Capabilities: []string{"cli", "local"},
			Card:         meta,
			Source:       "cli",
		})
	}
	return hits
}

// indexProgramsBins walks %LOCALAPPDATA%\Programs once and maps lower(base) → path.
func indexProgramsBins() map[string]string {
	out := map[string]string{}
	if runtime.GOOS != "windows" {
		return out
	}
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		return out
	}
	prog := filepath.Join(la, "Programs")
	_ = filepath.WalkDir(prog, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.IsDir() {
			rel, _ := filepath.Rel(prog, path)
			depth := 0
			if rel != "." {
				depth = len(strings.Split(rel, string(os.PathSeparator)))
			}
			base := strings.ToLower(de.Name())
			if base == "node_modules" || base == ".git" || base == "cache" {
				return filepath.SkipDir
			}
			if depth > 5 {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(de.Name())
		if _, ok := out[name]; !ok {
			out[name] = path
		}
		return nil
	})
	return out
}

func lookPathCLI(name string, progIndex map[string]string) string {
	if p, err := exec.LookPath(name); err == nil && p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	cands := []string{}
	if home != "" {
		cands = append(cands,
			filepath.Join(home, ".local", "bin", name),
			filepath.Join(home, ".cargo", "bin", name),
			filepath.Join(home, ".deno", "bin", name),
			filepath.Join(home, ".bun", "bin", name),
			filepath.Join(home, "AppData", "Local", "Programs", name, name),
		)
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			cands = append(cands,
				filepath.Join(la, "Programs", name, name+".exe"),
				filepath.Join(la, "Programs", name+".exe"),
				filepath.Join(la, "Programs", "1Code", "resources", "bin", name+".exe"),
				filepath.Join(la, "AnthropicClaude", name+".exe"),
				filepath.Join(la, "Claude", name+".exe"),
				filepath.Join(la, "nvm", name+".exe"),
				filepath.Join(la, "Microsoft", "WinGet", "Links", name+".exe"),
				filepath.Join(la, "Microsoft", "WindowsApps", name+".exe"),
			)
			// User-local bin used by many agent CLI installers (often missing from service PATH).
			if home != "" {
				cands = append(cands,
					filepath.Join(home, ".local", "bin", name+".exe"),
					filepath.Join(home, ".local", "bin", name+".cmd"),
				)
			}
			if ad := os.Getenv("APPDATA"); ad != "" {
				cands = append(cands,
					filepath.Join(ad, "npm", name+".cmd"),
					filepath.Join(ad, "npm", name+".ps1"),
					filepath.Join(ad, "npm", name+".exe"),
				)
			}
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			cands = append(cands, filepath.Join(pf, name, name+".exe"))
		}
		if progIndex != nil {
			if p := progIndex[strings.ToLower(name)]; p != "" {
				cands = append(cands, p)
			}
			if !strings.HasSuffix(strings.ToLower(name), ".exe") {
				if p := progIndex[strings.ToLower(name)+".exe"]; p != "" {
					cands = append(cands, p)
				}
			}
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// augmentCLIPath prepends common agent-CLI install dirs to PATH for this process
// so exec.LookPath finds tools even when AgentOS was started without the user PATH.
func augmentCLIPath() {
	home, _ := os.UserHomeDir()
	extra := []string{}
	if home != "" {
		extra = append(extra,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".deno", "bin"),
			filepath.Join(home, ".bun", "bin"),
		)
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			extra = append(extra,
				filepath.Join(la, "Programs", "1Code", "resources", "bin"),
				filepath.Join(la, "Microsoft", "WinGet", "Links"),
			)
		}
		if ad := os.Getenv("APPDATA"); ad != "" {
			extra = append(extra, filepath.Join(ad, "npm"))
		}
	}
	cur := os.Getenv("PATH")
	parts := []string{}
	seen := map[string]struct{}{}
	for _, d := range extra {
		if d == "" {
			continue
		}
		key := strings.ToLower(d)
		if _, ok := seen[key]; ok {
			continue
		}
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, d)
	}
	if len(parts) == 0 {
		return
	}
	sep := string(os.PathListSeparator)
	os.Setenv("PATH", strings.Join(parts, sep)+sep+cur)
}
