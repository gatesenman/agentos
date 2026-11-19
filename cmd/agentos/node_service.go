package main

// `agentos node --install-service`: persist the connection settings to
// <data>/node.json (0600, so the token is not on a service command line) and
// register an auto-start service running `agentos node --data <data>`.
// Platform parts: node_service_unix.go (systemd / launchd), node_service_windows.go
// (per-user logon scheduled task).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// NodeFile is <node data>/node.json.
type NodeFile struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	Name   string `json:"name,omitempty"`
	VNC    string `json:"vnc,omitempty"`
	// Desktop "virtual": run the node's own Xvfb desktop (Linux)
	Desktop string `json:"desktop,omitempty"`
}

func loadNodeFile(dataDir string) (NodeFile, bool) {
	var nf NodeFile
	b, err := os.ReadFile(filepath.Join(dataDir, "node.json"))
	if err != nil || json.Unmarshal(b, &nf) != nil {
		return nf, false
	}
	return nf, true
}

func saveNodeFile(dataDir string, nf NodeFile) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(nf, "", "  ")
	return os.WriteFile(filepath.Join(dataDir, "node.json"), b, 0o600)
}

func installNodeService(dataDir string, nf NodeFile) error {
	if nf.Server == "" || nf.Token == "" {
		return errors.New("--install-service needs --server and --token")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if err := saveNodeFile(abs, nf); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return installNodeServicePlatform(exe, abs, nf)
}
