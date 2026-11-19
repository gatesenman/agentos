package server

import "testing"

func TestTrySetBusy(t *testing.T) {
	m := &envManager{busy: map[string]string{}}
	if prev, ok := m.trySetBusy("e1", "starting"); !ok || prev != "" {
		t.Fatalf("idle claim: ok=%v prev=%q", ok, prev)
	}
	if prev, ok := m.trySetBusy("e1", "stopping"); ok || prev != "starting" {
		t.Fatalf("busy claim: ok=%v prev=%q", ok, prev)
	}
	m.setBusy("e1", "")
	if prev, ok := m.trySetBusy("e1", "stopping"); !ok || prev != "" {
		t.Fatalf("after clear: ok=%v prev=%q", ok, prev)
	}
	if m.busy["e1"] != "stopping" {
		t.Fatalf("busy=%q", m.busy["e1"])
	}
}
