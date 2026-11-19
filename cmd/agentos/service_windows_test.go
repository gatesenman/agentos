//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func writeUTF16(t *testing.T, path, s string) {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfUTF16NodeMode(t *testing.T) {
	dir := t.TempDir()
	writeUTF16(t, filepath.Join(dir, "agentos.conf"),
		"# c\r\n[agentos]\r\nmode=node\r\nserver=http://10.0.0.1:7800\r\ntoken=aos_x\r\nname=节点一\r\n")
	o, err := parseSvcFlags("run", []string{"--data", dir})
	if err != nil {
		t.Fatal(err)
	}
	if o.mode != "node" || o.server != "http://10.0.0.1:7800" || o.token != "aos_x" || o.nodeName != "节点一" {
		t.Fatalf("%+v", o)
	}
	// flags override the file
	o, err = parseSvcFlags("run", []string{"--data", dir, "--name", "x", "--mode", "server"})
	if err != nil || o.mode != "console" || o.nodeName != "x" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestConfRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseSvcFlags("install", []string{"--data", dir, "--mode", "node"}); err == nil {
		t.Fatal("node mode without server/token accepted")
	}
	o, err := parseSvcFlags("install", []string{"--data", dir, "--listen", ":9999", "--with-node=false"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConf(o.confPath, o); err != nil {
		t.Fatal(err)
	}
	o2, err := parseSvcFlags("run", []string{"--data", dir})
	if err != nil || o2.mode != "console" || o2.listen != ":9999" || o2.withNode {
		t.Fatalf("%+v %v", o2, err)
	}
}
