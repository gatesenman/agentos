package agentosremote

import (
	"embed"
	"regexp"
	"strings"
)

//go:embed scripts/install.sh scripts/install.ps1
var scriptsFS embed.FS

// Script returns an embedded install script ("install.sh" | "install.ps1").
func Script(name string) ([]byte, bool) {
	switch name {
	case "install.sh", "install.ps1":
		b, err := scriptsFS.ReadFile("scripts/" + name)
		return b, err == nil
	}
	return nil, false
}

var binRe = regexp.MustCompile(`^agentos-(linux|windows|darwin)-(amd64|arm64)(\.exe)?$`)

// ParseBinaryName validates a /download/<file> name and returns goos/goarch.
func ParseBinaryName(file string) (goos, goarch string, ok bool) {
	m := binRe.FindStringSubmatch(file)
	if m == nil || (m[1] == "windows") != (m[3] == ".exe") {
		return "", "", false
	}
	return m[1], m[2], true
}

// BinaryName returns "agentos-<goos>-<goarch>[.exe]".
func BinaryName(goos, goarch string) string {
	n := "agentos-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// Commands are the per-platform one-liners returned by POST /envs/{id}/join.
type Commands struct {
	Windows string `json:"windows"`
	Linux   string `json:"linux"`
	MacOS   string `json:"macos"`
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// JoinCommands builds the install commands: download the agentos binary from
// <serverURL>/download and run `agentos node --server URL --token T --name N`
// (with --install-service when service is true: installs an auto-start
// service / logon task and returns).
func JoinCommands(serverURL, token, name string, service bool) Commands {
	return JoinCommandsDesktop(serverURL, token, name, service, "")
}

// JoinCommandsDesktop is JoinCommands with a node desktop mode ("virtual":
// Linux/macOS nodes run their own Xvfb desktop; ignored on Windows, which
// uses the built-in screen stream).
func JoinCommandsDesktop(serverURL, token, name string, service bool, desktop string) Commands {
	u := strings.TrimRight(serverURL, "/")
	ps := "$env:AGENTOS_SERVER=" + psq(u) + "; $env:AGENTOS_TOKEN=" + psq(token) + "; $env:AGENTOS_NODE_NAME=" + psq(name) + "; "
	if service {
		ps += "$env:AGENTOS_INSTALL_SERVICE='1'; "
	}
	ps += "irm " + psq(u+"/download/install.ps1") + " | iex"
	sh := "curl -fsSL " + shq(u+"/download/install.sh") + " | sh -s -- --server " + shq(u) + " --token " + shq(token) + " --name " + shq(name)
	if desktop == "virtual" {
		sh += " --desktop virtual"
	}
	if service {
		sh += " --install-service"
	}
	return Commands{Windows: ps, Linux: sh, MacOS: sh}
}
