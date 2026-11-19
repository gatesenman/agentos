package agentosremote

import (
	"strings"
	"testing"
)

func TestJoinCommandsAndNames(t *testing.T) {
	c := JoinCommands("http://h:7800/", "aosj_x'y", "PC 1", true)
	if !strings.Contains(c.Linux, "/download/install.sh") || !strings.Contains(c.Linux, `'aosj_x'\''y'`) || !strings.HasSuffix(c.Linux, "--install-service") {
		t.Fatal(c.Linux)
	}
	if !strings.Contains(c.Windows, "AGENTOS_TOKEN='aosj_x''y'") || !strings.Contains(c.Windows, "AGENTOS_INSTALL_SERVICE") {
		t.Fatal(c.Windows)
	}
	for _, ok := range []string{"agentos-linux-amd64", "agentos-windows-amd64.exe", "agentos-darwin-arm64"} {
		if _, _, good := ParseBinaryName(ok); !good {
			t.Fatal(ok)
		}
	}
	for _, bad := range []string{"agentos-linux-amd64.exe", "agentos-windows-amd64", "../x", "agentos-plan9-amd64"} {
		if _, _, good := ParseBinaryName(bad); good {
			t.Fatal(bad)
		}
	}
	if _, ok := Script("install.sh"); !ok {
		t.Fatal("script")
	}
}

func TestJoinCommandsDesktop(t *testing.T) {
	c := JoinCommandsDesktop("http://h:7800", "aosj_x", "n", true, "virtual")
	if !strings.Contains(c.Linux, "--desktop virtual --install-service") {
		t.Fatalf("linux: %s", c.Linux)
	}
	if strings.Contains(JoinCommands("http://h:7800", "aosj_x", "n", false).Linux, "--desktop") {
		t.Fatal("desktop flag without desktop mode")
	}
}
