package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitialAdminPasswordFile(t *testing.T) {
	dir := t.TempDir()
	serverDir := filepath.Join(dir, "server")
	if err := writeInitialAdminPasswordFile(serverDir, "admin123"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(serverDir, "initial-admin-password.txt")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "admin123\n" {
		t.Fatalf("got %q", got)
	}
	// Sync after reset must overwrite.
	if err := writeInitialAdminPasswordFile(serverDir, "new-secret"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if got := string(b); got != "new-secret\n" {
		t.Fatalf("after reset got %q", got)
	}
	if err := writeInitialAdminPasswordFile("", "x"); err != nil {
		t.Fatal(err)
	}
	if err := writeInitialAdminPasswordFile(serverDir, ""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if got := string(b); got != "new-secret\n" {
		t.Fatalf("empty pw should no-op, got %q", got)
	}
}
