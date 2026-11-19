// Package stacks implements technology-stack templates (runtimes such as python/node/go
// and agent CLIs such as claude-code/codex) and an installer that sets them up inside
// any env.Env through its Exec capability. API shape: docs/api-envs.md "Stacks".
package stacks

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
)

//go:embed catalog.json
var catalogJSON []byte

// OS families a stack can provide setup steps for. "linux" means Debian/Ubuntu (apt),
// matching the frozen API key; alpine (apk) and rhel (dnf) are additive variants.
const (
	FamilyLinux   = "linux" // debian / ubuntu, apt
	FamilyAlpine  = "alpine"
	FamilyRHEL    = "rhel"
	FamilyDarwin  = "darwin"
	FamilyWindows = "windows"
)

// Families lists all OS family keys in display order.
var Families = []string{FamilyLinux, FamilyAlpine, FamilyRHEL, FamilyDarwin, FamilyWindows}

// EnvVar documents an environment variable an agent built on the stack needs.
type EnvVar struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	DescriptionEn string `json:"description_en,omitempty"`
	Secret        bool   `json:"secret"`
	Required      bool   `json:"required"`
}

// AgentTemplate is a ready-made agent definition (docs/api-envs.md agent_templates).
type AgentTemplate struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	NameEn         string            `json:"name_en,omitempty"`
	Command        []string          `json:"command"`
	CommandWindows []string          `json:"command_windows,omitempty"` // when the Windows argv differs
	Env            map[string]string `json:"env"`                       // "${VAR}" = fill from the stack's env_vars
	WorkDir        string            `json:"work_dir,omitempty"`        // hint; "~" = user home
	TTY            bool              `json:"tty,omitempty"`
	Ports          []int             `json:"ports,omitempty"`
}

// Stack is one catalog entry (internal form: setup/detect are shell scripts).
type Stack struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	NameEn         string              `json:"name_en"`
	Description    string              `json:"description"`
	DescriptionEn  string              `json:"description_en"`
	Category       string              `json:"category"` // base | runtime | agent
	Icon           string              `json:"icon"`
	Color          string              `json:"color"`
	Languages      []string            `json:"languages"`
	Requires       []string            `json:"requires,omitempty"`
	Homepage       string              `json:"homepage"`
	Sources        []string            `json:"sources"` // docs used to verify install commands
	Notes          string              `json:"notes,omitempty"`
	Detect         map[string]string   `json:"detect"` // "posix" | "windows" -> script
	Setup          map[string][]string `json:"setup"`  // family -> step scripts
	DockerImage    string              `json:"docker_image"`
	EnvVars        []EnvVar            `json:"env_vars"`
	AgentTemplates []AgentTemplate     `json:"agent_templates"`
}

type catalogFile struct {
	Version int     `json:"version"`
	Stacks  []Stack `json:"stacks"`
}

var (
	loadOnce sync.Once
	catalog  []Stack
	byID     map[string]*Stack
	loadErr  error
)

func load() {
	var f catalogFile
	if err := json.Unmarshal(catalogJSON, &f); err != nil {
		loadErr = fmt.Errorf("stacks: 解析内置模板目录失败: %w", err)
		return
	}
	catalog = f.Stacks
	byID = make(map[string]*Stack, len(catalog))
	for i := range catalog {
		byID[catalog[i].ID] = &catalog[i]
	}
}

// All returns the catalog in its defined order. It panics if the embedded catalog is invalid
// (guarded by tests).
func All() []Stack {
	loadOnce.Do(load)
	if loadErr != nil {
		panic(loadErr)
	}
	return catalog
}

// Get returns a stack by id.
func Get(id string) (*Stack, bool) {
	All()
	s, ok := byID[id]
	return s, ok
}

// Steps returns the setup scripts for an OS family, falling back from unknown linux
// variants to the debian ("linux") list.
func (s *Stack) Steps(family string) []string {
	if st, ok := s.Setup[family]; ok && len(st) > 0 {
		return st
	}
	return nil
}

// DetectScript returns the version-check script for the family.
func (s *Stack) DetectScript(family string) string {
	if family == FamilyWindows {
		return s.Detect["windows"]
	}
	return s.Detect["posix"]
}

// ---- API view (docs/api-envs.md Stacks shape) ----

// SetupView matches {windows:[[cmd...]], linux:[[...]], darwin:[[...]], docker_image} plus
// the additive alpine/rhel variants.
type SetupView struct {
	Windows     [][]string `json:"windows"`
	Linux       [][]string `json:"linux"`
	Darwin      [][]string `json:"darwin"`
	Alpine      [][]string `json:"alpine,omitempty"`
	RHEL        [][]string `json:"rhel,omitempty"`
	DockerImage string     `json:"docker_image"`
}

// View is the GET /stacks element.
type View struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	NameEn         string          `json:"name_en"`
	Icon           string          `json:"icon"`
	IconSVG        string          `json:"icon_svg"`
	Color          string          `json:"color"`
	Description    string          `json:"description"`
	DescriptionEn  string          `json:"description_en"`
	Category       string          `json:"category"`
	Languages      []string        `json:"languages"`
	Requires       []string        `json:"requires,omitempty"`
	Homepage       string          `json:"homepage,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	Setup          SetupView       `json:"setup"`
	AgentTemplates []AgentTemplate `json:"agent_templates"`
	Detect         []string        `json:"detect"`
	DetectWindows  []string        `json:"detect_windows"`
	EnvVars        []EnvVar        `json:"env_vars"`
}

// View converts a stack into the API representation; every step becomes a full argv
// (sh -c ... / powershell -Command ...) runnable through env.Exec.
func (s *Stack) View() View {
	steps := func(fam string) [][]string {
		out := [][]string{}
		for _, sc := range s.Steps(fam) {
			out = append(out, StepArgv(fam, sc))
		}
		return out
	}
	v := View{
		ID: s.ID, Name: s.Name, NameEn: s.NameEn, Icon: s.Icon, IconSVG: IconSVG(s), Color: s.Color,
		Description: s.Description, DescriptionEn: s.DescriptionEn, Category: s.Category,
		Languages: s.Languages, Requires: s.Requires, Homepage: s.Homepage, Notes: s.Notes,
		Setup: SetupView{
			Windows: steps(FamilyWindows), Linux: steps(FamilyLinux), Darwin: steps(FamilyDarwin),
			DockerImage: s.DockerImage,
		},
		AgentTemplates: s.AgentTemplates,
		Detect:         DetectArgv(FamilyLinux, s.DetectScript(FamilyLinux)),
		DetectWindows:  DetectArgv(FamilyWindows, s.DetectScript(FamilyWindows)),
		EnvVars:        s.EnvVars,
	}
	if a := steps(FamilyAlpine); len(a) > 0 {
		v.Setup.Alpine = a
	}
	if r := steps(FamilyRHEL); len(r) > 0 {
		v.Setup.RHEL = r
	}
	if v.EnvVars == nil {
		v.EnvVars = []EnvVar{}
	}
	return v
}

// Views returns the whole catalog in API form.
func Views() []View {
	all := All()
	out := make([]View, 0, len(all))
	for i := range all {
		out = append(out, all[i].View())
	}
	return out
}

// IconSVG renders a small monogram badge (24x24) in the stack's brand color; the UI may
// substitute a real logo by the "icon" key.
func IconSVG(s *Stack) string {
	label := monogram(s)
	bg := s.Color
	if bg == "" {
		bg = "#6B7280"
	}
	fg := "#FFFFFF"
	if isLight(bg) {
		fg = "#111111"
	}
	size := "11"
	if len([]rune(label)) > 2 {
		size = "8"
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24"><rect width="24" height="24" rx="5" fill="%s"/><text x="12" y="16" text-anchor="middle" font-family="Segoe UI,Arial,sans-serif" font-weight="700" font-size="%s" fill="%s">%s</text></svg>`,
		html.EscapeString(bg), size, fg, html.EscapeString(label))
}

var monograms = map[string]string{
	"python": "Py", "node": "JS", "bun": "Bun", "deno": "De", "go": "Go", "java": "Jv", "dotnet": ".N",
	"rust": "Rs", "claude-code": "CC", "openai-codex-cli": "Cx", "gemini-cli": "Gm", "aider": "Ai",
	"browser-use": "BU", "openhands": "OH",
}

func monogram(s *Stack) string {
	if m, ok := monograms[s.ID]; ok {
		return m
	}
	r := []rune(strings.TrimSpace(s.NameEn))
	if len(r) > 2 {
		r = r[:2]
	}
	return string(r)
}

func isLight(hex string) bool {
	var r, g, b int
	if _, err := fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b); err != nil {
		return false
	}
	return (r*299+g*587+b*114)/1000 > 160
}

// sortedKeys is a small helper used by tests and errors.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
