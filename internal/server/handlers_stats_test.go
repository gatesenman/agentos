package server

import (
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
)

func TestStatsRing(t *testing.T) {
	r := newStatsRing[api.ProcStats]()
	if _, ok := r.latest(); ok {
		t.Fatal("empty ring has latest")
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < statsHistory+30; i++ {
		r.push(api.ProcStats{Time: base.Add(time.Duration(i) * time.Second), Procs: i})
	}
	all := r.since(time.Time{}, procTime)
	if len(all) != statsHistory || all[0].Procs != 30 || all[len(all)-1].Procs != statsHistory+29 {
		t.Fatalf("ring order/len wrong: len=%d first=%d last=%d", len(all), all[0].Procs, all[len(all)-1].Procs)
	}
	for i := 1; i < len(all); i++ {
		if !all[i].Time.After(all[i-1].Time) {
			t.Fatal("not oldest-first")
		}
	}
	l, _ := r.latest()
	if l.Procs != statsHistory+29 {
		t.Fatalf("latest %d", l.Procs)
	}
	// since is exclusive
	got := r.since(base.Add(140*time.Second), procTime)
	if len(got) != 9 || got[0].Procs != 141 {
		t.Fatalf("since: len=%d %+v", len(got), got)
	}
	var nilRing *statsRing[api.ProcStats]
	if s := nilRing.since(time.Time{}, procTime); s == nil || len(s) != 0 {
		t.Fatal("nil ring must give empty non-nil slice")
	}
}
