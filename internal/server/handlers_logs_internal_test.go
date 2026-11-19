package server

import (
	"testing"

	"github.com/agentos/agentos/internal/api"
)

func TestLogDedupe(t *testing.T) {
	var d logDedupe
	var out []uint64
	// initial tail 5..8, live stream overlaps 7..10, then node log reset to 1..2
	for _, s := range []uint64{5, 6, 7, 8, 7, 8, 9, 10, 10, 1, 2, 2} {
		if d.accept(s) {
			out = append(out, s)
		}
	}
	want := []uint64{5, 6, 7, 8, 9, 10, 1, 2}
	if len(out) != len(want) {
		t.Fatalf("got %v", out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("got %v", out)
		}
	}
}

func TestRecordLogsDedupe(t *testing.T) {
	s := New(Config{}, nil, nil)
	ch, cancel := s.bus.Subscribe(nil)
	defer cancel()
	s.recordLogs(api.LogBatch{AgentID: "a", Lines: []api.LogLine{{Seq: 1}, {Seq: 2}}})
	s.recordLogs(api.LogBatch{AgentID: "a", Lines: []api.LogLine{{Seq: 2}, {Seq: 3}}})
	if n := len(ch); n != 3 {
		t.Fatalf("published %d events", n)
	}
	if l := s.logRings["a"].Tail(0, 0); len(l) != 3 || l[2].Seq != 3 || l[0].AgentID != "a" {
		t.Fatalf("ring %v", l)
	}
}
