package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/api"
)

func TestRingOverflowOrder(t *testing.T) {
	r := NewRing(5)
	for i := 1; i <= 12; i++ {
		r.Add(api.LogLine{Seq: uint64(i)})
	}
	got := r.Tail(0, 0)
	if len(got) != 5 {
		t.Fatalf("len %d", len(got))
	}
	for i, l := range got {
		if l.Seq != uint64(8+i) {
			t.Fatalf("order: %v", got)
		}
	}
	if g := r.Tail(2, 0); len(g) != 2 || g[0].Seq != 11 || g[1].Seq != 12 {
		t.Fatalf("tail2 %v", g)
	}
	if g := r.Tail(0, 10); len(g) != 2 || g[0].Seq != 11 {
		t.Fatalf("since %v", g)
	}
	if g := r.Tail(10, 12); len(g) != 0 {
		t.Fatalf("since-all %v", g)
	}
}

func TestRingAddNewDedupe(t *testing.T) {
	r := NewRing(10)
	for _, s := range []uint64{1, 2, 3, 2, 3, 4, 4, 5} {
		r.AddNew(api.LogLine{Seq: s})
	}
	got := r.Tail(0, 0)
	if len(got) != 5 {
		t.Fatalf("dedupe %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("order %v", got)
		}
	}
}

func TestSplitText(t *testing.T) {
	long := strings.Repeat("é", MaxLineBytes) // 2 bytes per rune
	parts := SplitText(long)
	total := 0
	for _, p := range parts {
		if len(p) > MaxLineBytes {
			t.Fatalf("part too long %d", len(p))
		}
		if !utf8Valid(p) {
			t.Fatal("split mid-rune")
		}
		total += len(p)
	}
	if total != len(long) || len(parts) != 2 {
		t.Fatalf("parts %d total %d", len(parts), total)
	}
	if g := SplitText("a\xffb"); len(g) != 1 || g[0] != "a\uFFFDb" {
		t.Fatalf("utf8 %q", g)
	}
	if g := SplitText("x\r\ny"); len(g) != 2 || g[0] != "x" || g[1] != "y" {
		t.Fatalf("nl %q", g)
	}
	if g := SplitText("\x1b[31mred\x1b[0m"); g[0] != "\x1b[31mred\x1b[0m" {
		t.Fatalf("ansi %q", g)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

func TestStoreAppendTailSubscribe(t *testing.T) {
	s, err := NewStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mu sync.Mutex
	var seen []uint64
	cancel := s.Subscribe(func(l api.LogLine) { mu.Lock(); seen = append(seen, l.Seq); mu.Unlock() })
	for i := 0; i < 250; i++ {
		s.Append("a", api.StreamStdout, fmt.Sprint("line ", i))
	}
	cancel()
	cancel() // idempotent
	s.Append("a", api.StreamStdout, "after cancel")
	if len(seen) != 250 {
		t.Fatalf("seen %d", len(seen))
	}
	got := s.Tail("a", 10, 0)
	if len(got) != 10 || got[9].Seq != 251 || got[0].Seq != 242 {
		t.Fatalf("tail %v", got)
	}
	if got := s.Tail("b", 10, 0); len(got) != 0 {
		t.Fatalf("unknown agent %v", got)
	}
	// beyond the ring: read from file
	got = s.Tail("a", 200, 0)
	if len(got) != 200 || got[0].Seq != 52 || got[199].Seq != 251 || got[0].AgentID != "a" {
		t.Fatalf("file tail len=%d first=%v", len(got), got[0])
	}
	got = s.Tail("a", 500, 120)
	if len(got) != 131 || got[0].Seq != 121 {
		t.Fatalf("since from file len=%d", len(got))
	}
}

func TestConcurrentAppendOrder(t *testing.T) {
	s, _ := NewStore(t.TempDir(), 0)
	defer s.Close()
	var mu sync.Mutex
	var last uint64
	bad := 0
	s.Subscribe(func(l api.LogLine) {
		mu.Lock()
		if l.Seq != last+1 {
			bad++
		}
		last = l.Seq
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.Append("a", api.StreamStderr, "x")
			}
		}()
	}
	wg.Wait()
	if bad != 0 || last != 2000 {
		t.Fatalf("bad=%d last=%d", bad, last)
	}
}

func logFiles(t *testing.T, dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "a.log*"))
	sort.Strings(m)
	return m
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStoreWithOptions(dir, Options{RingSize: 50, MaxFileSize: 4096, MaxBackups: 5})
	text := strings.Repeat("x", 100)
	for i := 0; i < 2000; i++ { // ~280 KB total, far more than 6×4 KB
		s.Append("a", api.StreamStdout, fmt.Sprintf("%05d %s", i, text))
	}
	files := logFiles(t, dir)
	if len(files) != 6 {
		t.Fatalf("files %v", files)
	}
	for _, f := range files {
		fi, _ := os.Stat(f)
		if fi.Size() > 4096 {
			t.Fatalf("%s size %d", f, fi.Size())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.log"))
	if !strings.Contains(string(b), "01999 ") {
		t.Fatal("newest line missing from current file")
	}
	b, _ = os.ReadFile(filepath.Join(dir, "a.log.5"))
	if strings.Contains(string(b), "00000 ") {
		t.Fatal("oldest line should have been dropped")
	}
	s.Close()
	// reload across rotated files keeps newest ring lines in order
	s2, _ := NewStoreWithOptions(dir, Options{RingSize: 50, MaxFileSize: 4096, MaxBackups: 5})
	defer s2.Close()
	got := s2.Tail("a", 0, 0)
	if len(got) != 50 || got[49].Seq != 2000 || !strings.HasPrefix(got[49].Text, "01999 ") {
		t.Fatalf("reload len=%d last=%+v", len(got), got[len(got)-1])
	}
}

func TestRotationDefault10MB(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	s, _ := NewStore(dir, 0)
	defer s.Close()
	text := strings.Repeat("y", 1000)
	for i := 0; i < 12*1024; i++ { // ~12 MB
		s.Append("a", api.StreamStdout, text)
	}
	files := logFiles(t, dir)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	fi, _ := os.Stat(filepath.Join(dir, "a.log.1"))
	if fi.Size() > DefaultMaxFileSize || fi.Size() < DefaultMaxFileSize-2000 {
		t.Fatalf("rotated size %d", fi.Size())
	}
}

func TestReloadContinuesSeq(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir, 100)
	for i := 1; i <= 150; i++ {
		s.Append("a", api.StreamStdout, fmt.Sprint("old ", i))
	}
	s.Append("a", api.StreamStderr, "") // empty text survives
	s.Close()

	s2, _ := NewStore(dir, 100) // "node restart"
	defer s2.Close()
	got := s2.Tail("a", 0, 0)
	if len(got) != 100 || got[0].Seq != 52 || got[99].Seq != 151 || got[98].Text != "old 150" ||
		got[99].Stream != api.StreamStderr || got[99].Text != "" || got[0].AgentID != "a" {
		t.Fatalf("reload len=%d first=%+v last=%+v", len(got), got[0], got[len(got)-1])
	}
	l := s2.Append("a", api.StreamStdout, "new")
	if l.Seq != 152 {
		t.Fatalf("seq after reload %d", l.Seq)
	}
	// Remove drops memory; next access reloads from file and continues.
	s2.Remove("a")
	if l := s2.Append("a", api.StreamStdout, "again"); l.Seq != 153 {
		t.Fatalf("seq after remove %d", l.Seq)
	}
}

func TestReloadLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.log"), []byte(
		"2026-01-01T00:00:00Z [stdout] one\n2026-01-01T00:00:01Z [stderr] two\ngarbage\n"), 0o644)
	s, _ := NewStore(dir, 10)
	defer s.Close()
	got := s.Tail("a", 0, 0)
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || got[1].Text != "two" {
		t.Fatalf("legacy %+v", got)
	}
	if l := s.Append("a", "stdout", "three"); l.Seq != 3 {
		t.Fatalf("seq %d", l.Seq)
	}
}

func TestLongLineSplit(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir, 10)
	l := s.Append("a", api.StreamStdout, strings.Repeat("z", MaxLineBytes*2+10))
	if l.Seq != 3 || len(l.Text) != 10 {
		t.Fatalf("split last %d %d", l.Seq, len(l.Text))
	}
	s.Close()
	s2, _ := NewStore(dir, 10)
	defer s2.Close()
	if got := s2.Tail("a", 0, 0); len(got) != 3 || len(got[0].Text) != MaxLineBytes {
		t.Fatalf("reload long %d", len(got))
	}
}

// 10k lines/s must not block: p99 Append latency < 50 ms.
func TestAppendLatency(t *testing.T) {
	s, _ := NewStoreWithOptions(t.TempDir(), Options{MaxFileSize: 1 << 20})
	defer s.Close()
	s.Subscribe(func(api.LogLine) {})
	const n = 10000
	lat := make([]time.Duration, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		s.Append("a", api.StreamStdout, fmt.Sprintf("line %d some typical agent output text here", i))
		lat[i] = time.Since(t0)
	}
	total := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99 := lat[n*99/100]
	t.Logf("10k appends in %s, p99=%s", total, p99)
	if p99 > 50*time.Millisecond || total > time.Second {
		t.Fatalf("too slow: total %s p99 %s", total, p99)
	}
}

func BenchmarkAppend(b *testing.B) {
	s, _ := NewStore(b.TempDir(), 0)
	defer s.Close()
	for i := 0; i < b.N; i++ {
		s.Append("a", api.StreamStdout, "benchmark line of typical length for an agent")
	}
}
