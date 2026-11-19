package stacks

import (
	"encoding/json"
	"strings"
	"testing"
)

var wantStacks = []string{"linux-desktop", "openssh", "zh-locale", "chromium",
	"python", "node", "bun", "deno", "go", "java", "dotnet", "rust",
	"claude-code", "openai-codex-cli", "gemini-cli", "aider", "browser-use", "openhands"}

func TestCatalogValid(t *testing.T) {
	all := All()
	ids := map[string]bool{}
	tplIDs := map[string]string{}
	for _, s := range all {
		if ids[s.ID] {
			t.Errorf("duplicate stack id %s", s.ID)
		}
		ids[s.ID] = true
		if s.Name == "" || s.NameEn == "" || s.Description == "" || s.DescriptionEn == "" || s.Icon == "" {
			t.Errorf("%s: missing name/description/icon (zh+en)", s.ID)
		}
		if s.Category != "base" && s.Category != "runtime" && s.Category != "agent" {
			t.Errorf("%s: bad category %q", s.ID, s.Category)
		}
		if len(s.Sources) == 0 {
			t.Errorf("%s: no verification sources", s.ID)
		}
		if s.Detect["posix"] == "" || s.Detect["windows"] == "" {
			t.Errorf("%s: detect must cover posix and windows", s.ID)
		}
		for _, fam := range []string{FamilyLinux, FamilyWindows, FamilyDarwin, FamilyAlpine} {
			if len(s.Steps(fam)) == 0 {
				t.Errorf("%s: no setup steps for %s", s.ID, fam)
			}
		}
		for k := range s.Setup {
			if !contains(Families, k) {
				t.Errorf("%s: unknown setup family %q", s.ID, k)
			}
		}
		if s.DockerImage == "" {
			t.Errorf("%s: no docker_image suggestion", s.ID)
		}
		for _, dep := range s.Requires {
			if _, ok := Get(dep); !ok {
				t.Errorf("%s requires unknown stack %s", s.ID, dep)
			}
		}
		if len(s.AgentTemplates) == 0 {
			t.Errorf("%s: no agent_templates", s.ID)
		}
		for _, tp := range s.AgentTemplates {
			if tp.ID == "" || tp.Name == "" || len(tp.Command) == 0 || tp.Env == nil {
				t.Errorf("%s: incomplete template %+v", s.ID, tp)
			}
			if other, dup := tplIDs[tp.ID]; dup {
				t.Errorf("template id %s used by %s and %s", tp.ID, other, s.ID)
			}
			tplIDs[tp.ID] = s.ID
			for _, v := range tp.Env {
				if strings.HasPrefix(v, "${") {
					name := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")
					if !hasEnvVar(&s, name) {
						t.Errorf("%s/%s references undeclared env var %s", s.ID, tp.ID, name)
					}
				}
			}
		}
		for _, ev := range s.EnvVars {
			if strings.HasSuffix(ev.Name, "_API_KEY") && !ev.Secret {
				t.Errorf("%s: %s must be secret", s.ID, ev.Name)
			}
		}
	}
	for _, id := range wantStacks {
		if !ids[id] {
			t.Errorf("missing stack %s", id)
		}
	}
	// agent stacks must declare their API key
	for id, key := range map[string]string{"claude-code": "ANTHROPIC_API_KEY", "openai-codex-cli": "OPENAI_API_KEY", "gemini-cli": "GEMINI_API_KEY"} {
		s, _ := Get(id)
		if !hasEnvVar(s, key) {
			t.Errorf("%s: missing env var %s", id, key)
		}
	}
	// verified package names must be present
	for id, needle := range map[string]string{
		"openai-codex-cli": "@openai/codex", "gemini-cli": "@google/gemini-cli", "claude-code": "https://claude.ai/install.sh",
		"aider": "aider-chat", "browser-use": "browser-use playwright", "openhands": "uv tool install openhands",
	} {
		s, _ := Get(id)
		b, _ := json.Marshal(s.Setup)
		if !strings.Contains(string(b), needle) {
			t.Errorf("%s: setup does not mention %s", id, needle)
		}
	}
}

func hasEnvVar(s *Stack, name string) bool {
	for _, e := range s.EnvVars {
		if e.Name == name {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestViewShape checks GET /stacks JSON against docs/api-envs.md:
// [{id, name, icon, description, languages:[...], setup:{windows:[[cmd...]], linux:[[...]], darwin:[[...]], docker_image},
//
//	agent_templates:[{id,name,command:[...],env:{}}], detect:[cmd...]}]
func TestViewShape(t *testing.T) {
	b, err := json.Marshal(Views())
	if err != nil {
		t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != len(All()) {
		t.Fatalf("got %d views", len(arr))
	}
	for _, v := range arr {
		id, _ := v["id"].(string)
		for _, k := range []string{"id", "name", "icon", "description"} {
			if s, ok := v[k].(string); !ok || s == "" {
				t.Errorf("%s: %s must be non-empty string", id, k)
			}
		}
		if _, ok := v["languages"].([]any); !ok {
			t.Errorf("%s: languages must be array", id)
		}
		setup, ok := v["setup"].(map[string]any)
		if !ok {
			t.Fatalf("%s: setup must be object", id)
		}
		for _, fam := range []string{"windows", "linux", "darwin"} {
			steps, ok := setup[fam].([]any)
			if !ok || len(steps) == 0 {
				t.Errorf("%s: setup.%s must be non-empty [[cmd...]]", id, fam)
				continue
			}
			for _, st := range steps {
				argv, ok := st.([]any)
				if !ok || len(argv) < 2 {
					t.Errorf("%s: setup.%s step not an argv", id, fam)
					continue
				}
				for _, a := range argv {
					if _, ok := a.(string); !ok {
						t.Errorf("%s: argv element not string", id)
					}
				}
			}
		}
		if _, ok := setup["docker_image"].(string); !ok {
			t.Errorf("%s: setup.docker_image must be string", id)
		}
		tpls, ok := v["agent_templates"].([]any)
		if !ok || len(tpls) == 0 {
			t.Errorf("%s: agent_templates", id)
		}
		for _, tp := range tpls {
			m := tp.(map[string]any)
			if _, ok := m["command"].([]any); !ok {
				t.Errorf("%s: template command must be array", id)
			}
			if _, ok := m["env"].(map[string]any); !ok {
				t.Errorf("%s: template env must be object", id)
			}
			if _, ok := m["id"].(string); !ok {
				t.Errorf("%s: template id", id)
			}
			if _, ok := m["name"].(string); !ok {
				t.Errorf("%s: template name", id)
			}
		}
		det, ok := v["detect"].([]any)
		if !ok || len(det) != 3 || det[0] != "sh" {
			t.Errorf("%s: detect must be [sh -c script], got %v", id, v["detect"])
		}
		if svg, _ := v["icon_svg"].(string); !strings.HasPrefix(svg, "<svg") {
			t.Errorf("%s: icon_svg", id)
		}
	}
}
