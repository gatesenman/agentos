package stacks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) f(level, text string) {
	l.mu.Lock()
	l.lines = append(l.lines, level+": "+text)
	l.mu.Unlock()
}
func (l *logRec) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestDetectOSFamilies(t *testing.T) {
	for fam, want := range map[string]string{"linux": FamilyLinux, "alpine": FamilyAlpine, "rhel": FamilyRHEL, "darwin": FamilyDarwin, "windows": FamilyWindows, "unknown": "linux-nixos"} {
		osi, err := DetectOS(context.Background(), newFake(fam))
		if err != nil {
			t.Fatalf("%s: %v", fam, err)
		}
		if osi.Family != want {
			t.Errorf("%s: family %q want %q", fam, osi.Family, want)
		}
	}
	osi, _ := DetectOS(context.Background(), newFake("windows"))
	if osi.Arch != "AMD64" || osi.Version != "10.0.22631.0" {
		t.Errorf("windows probe parse: %+v", osi)
	}
	osi, _ = DetectOS(context.Background(), newFake("alpine"))
	if !osi.Root || osi.Arch != "aarch64" {
		t.Errorf("alpine probe parse: %+v", osi)
	}
}

func TestInstallPerFamily(t *testing.T) {
	cases := []struct {
		fam, stack string
		want       []string
		needle     string // must appear in an executed step script
	}{
		{"linux", "python", []string{"python#0"}, "apt-get install -y --no-install-recommends python3 python3-pip python3-venv"},
		{"alpine", "node", []string{"node#0"}, "apk add --no-cache nodejs npm"},
		{"rhel", "go", []string{"go#0"}, "dnf install -y golang"},
		{"darwin", "deno", []string{"deno#0"}, "brew install deno"},
		{"windows", "python", []string{"python#0"}, "Install-Pkg 'Python.Python.3.12' 'python3'"},
		{"windows", "rust", []string{"rust#0", "rust#1"}, "rustup default stable"},
		{"linux", "openai-codex-cli", []string{"node#0", "openai-codex-cli#0"}, "npm_global @openai/codex"},
		{"windows", "gemini-cli", []string{"node#0", "gemini-cli#0"}, "npm install -g @google/gemini-cli"},
		{"linux", "claude-code", []string{"claude-code#0", "claude-code#1"}, "curl -fsSL https://claude.ai/install.sh | bash"},
		{"linux", "aider", []string{"aider#0", "aider#1"}, "aider-chat@latest"},
		{"windows", "openhands", []string{"openhands#0", "openhands#1", "openhands#2"}, "uv tool install openhands --python 3.12"},
	}
	for _, c := range cases {
		f := newFake(c.fam)
		var l logRec
		if err := Install(context.Background(), f, c.stack, l.f); err != nil {
			t.Fatalf("%s/%s: %v\n%s", c.fam, c.stack, err, l.String())
		}
		if !reflect.DeepEqual(f.steps, c.want) {
			t.Errorf("%s/%s: steps %v want %v", c.fam, c.stack, f.steps, c.want)
		}
		found := false
		for _, argv := range f.calls {
			sc := scriptOf(argv)
			if c.fam == "windows" {
				if argv[0] == "powershell.exe" && !contains(argv, "-EncodedCommand") {
					t.Errorf("windows exec must use -EncodedCommand: %v", argv[:5])
				}
			} else if argv[0] != "sh" || argv[1] != "-c" {
				t.Errorf("posix exec must be sh -c: %v", argv[:2])
			}
			if strings.Contains(sc, c.needle) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s/%s: no step contained %q", c.fam, c.stack, c.needle)
		}
		if !strings.Contains(l.String(), "安装完成") {
			t.Errorf("%s/%s: missing completion log:\n%s", c.fam, c.stack, l.String())
		}
		// idempotent: second run executes no steps
		f.steps = nil
		if err := Install(context.Background(), f, c.stack, nil); err != nil || len(f.steps) != 0 {
			t.Errorf("%s/%s: second install err=%v steps=%v", c.fam, c.stack, err, f.steps)
		}
	}
}

func TestInstallSkipsWhenPresent(t *testing.T) {
	f := newFake("linux", "node", "gemini-cli")
	var l logRec
	if err := Install(context.Background(), f, "gemini-cli", l.f); err != nil {
		t.Fatal(err)
	}
	if len(f.steps) != 0 {
		t.Fatalf("steps ran: %v", f.steps)
	}
	if !strings.Contains(l.String(), "已安装（1.2.3），跳过") {
		t.Errorf("log: %s", l.String())
	}
}

func TestInstallErrors(t *testing.T) {
	ctx := context.Background()
	if err := Install(ctx, newFake("linux"), "cobol", nil); !errors.Is(err, ErrUnknownStack) {
		t.Errorf("unknown stack: %v", err)
	}
	if err := Install(ctx, newFake("unknown"), "python", nil); !errors.Is(err, ErrUnsupportedOS) {
		t.Errorf("unknown distro: %v", err)
	}
	f := newFake("linux")
	f.failStep, f.failOut = "python#0", "sudo: a password is required"
	err := Install(ctx, f, "python", nil)
	if err == nil || !strings.Contains(err.Error(), "退出码 100") || !strings.Contains(err.Error(), "免密 sudo") {
		t.Errorf("fail step: %v", err)
	}
	f = newFake("linux")
	f.failStep, f.failOut = "node#0", "E: Unable to locate package nodejs"
	err = Install(ctx, f, "openai-codex-cli", nil)
	if err == nil || !strings.Contains(err.Error(), "Node.js") || !strings.Contains(err.Error(), "找不到该软件包") {
		t.Errorf("dep failure: %v", err)
	}
	if contains(f.steps, "openai-codex-cli#0") {
		t.Errorf("must stop after dependency failure: %v", f.steps)
	}
	old := StepTimeout
	StepTimeout = 100 * time.Millisecond
	defer func() { StepTimeout = old }()
	f = newFake("darwin")
	f.hangStep = "go#0"
	err = Install(ctx, f, "go", nil)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Errorf("timeout: %v", err)
	}
}

func TestDetectAll(t *testing.T) {
	f := newFake("windows", "python", "claude-code")
	ds, osi, err := Detect(context.Background(), f)
	if err != nil || osi.Family != FamilyWindows {
		t.Fatal(err, osi)
	}
	got := map[string]Detected{}
	for _, d := range ds {
		got[d.ID] = d
	}
	if len(ds) != len(All()) || !got["python"].Installed || got["python"].Version != "1.2.3" || !got["claude-code"].Installed || got["node"].Installed {
		t.Errorf("detect: %+v", ds)
	}
}

func TestEncodePSRoundTrip(t *testing.T) {
	s := "Write-Host '安装 ✓'\n\"quoted\" $env:Path"
	if decodePS(encodePS(s)) != s {
		t.Fatal("roundtrip")
	}
}
