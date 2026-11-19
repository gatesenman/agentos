package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSealerAndSplit(t *testing.T) {
	dir := t.TempDir()
	s, err := loadSealer(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys := secretKeys(json.RawMessage(`{"properties":{"host":{"type":"string"},"password":{"type":"string","x-secret":true}}}`))
	pub, sec, err := splitConfig(json.RawMessage(`{"host":"h","password":"p@ss"}`), keys)
	if err != nil || strings.Contains(string(pub), "p@ss") || string(sec["password"]) != `"p@ss"` {
		t.Fatalf("%s %v %v", pub, sec, err)
	}
	sealed, err := s.sealSecrets(sec, "env1")
	if err != nil || strings.Contains(sealed, "p@ss") {
		t.Fatal(sealed, err)
	}
	s2, _ := loadSealer(dir) // same key file
	back, err := s2.openSecrets(sealed, "env1")
	if err != nil || string(back["password"]) != `"p@ss"` {
		t.Fatal(back, err)
	}
	if _, err := s2.openSecrets(sealed, "other-env"); err == nil {
		t.Fatal("aad must bind ciphertext to the env id")
	}
	if got := string(mergeConfig(pub, back)); !strings.Contains(got, "p@ss") || !strings.Contains(got, `"host":"h"`) {
		t.Fatal(got)
	}
}
