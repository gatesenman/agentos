package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// mockEngine is a tiny fake Docker Engine API (MOCK) covering pull/create/start/
// inspect/exec (hijacked, multiplexed)/archive/stats.
type mockEngine struct {
	mu      sync.Mutex
	pulled  bool
	created map[string]any
	files   map[string][]byte
	execs   map[string][]string
}

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func (m *mockEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/_ping":
		io.WriteString(w, "OK")
	case strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		if !m.pulled {
			http.Error(w, `{"message":"No such image"}`, 404)
			return
		}
		io.WriteString(w, `{}`)
	case p == "/images/create":
		if r.URL.Query().Get("fromImage") != "alpine" || r.URL.Query().Get("tag") != "3" {
			http.Error(w, `{"message":"bad ref"}`, 400)
			return
		}
		m.pulled = true
		io.WriteString(w, "{\"status\":\"Pulling\"}\n{\"status\":\"Done\"}\n")
	case p == "/containers/create":
		json.NewDecoder(r.Body).Decode(&m.created)
		io.WriteString(w, `{"Id":"c1"}`)
	case p == "/containers/c1/start":
		w.WriteHeader(204)
	case p == "/containers/c1/json":
		io.WriteString(w, `{"Id":"c1","State":{"Status":"running","Running":true},"Config":{"Image":"alpine:3","Labels":{"agentos.env":"e1"}},"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"10.0.0.2"}}}}`)
	case p == "/containers/c1/exec":
		var cfg struct{ Cmd []string }
		json.NewDecoder(r.Body).Decode(&cfg)
		id := "x" + string(rune('0'+len(m.execs)))
		m.execs[id] = cfg.Cmd
		io.WriteString(w, `{"Id":"`+id+`"}`)
	case strings.HasPrefix(p, "/exec/") && strings.HasSuffix(p, "/start"):
		if r.Header.Get("Upgrade") != "tcp" {
			http.Error(w, "no upgrade", 400)
			return
		}
		cmd := m.execs[strings.Split(p, "/")[2]]
		io.Copy(io.Discard, r.Body)
		conn, brw, _ := w.(http.Hijacker).Hijack()
		go func() {
			defer conn.Close()
			brw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			brw.Flush()
			if len(cmd) > 0 && cmd[0] == "cat" { // echo stdin back on stdout until EOF
				in, _ := io.ReadAll(brw)
				conn.Write(frame(1, string(in)))
				return
			}
			conn.Write(frame(1, "hello "))
			conn.Write(frame(2, "oops\n"))
			conn.Write(frame(1, "world\n"))
		}()
	case strings.HasPrefix(p, "/exec/") && strings.HasSuffix(p, "/json"):
		io.WriteString(w, `{"Running":false,"ExitCode":3}`)
	case p == "/containers/c1/archive" && r.Method == "PUT":
		tr := tar.NewReader(r.Body)
		h, err := tr.Next()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(tr)
		m.files[r.URL.Query().Get("path")+"/"+h.Name] = b
	case p == "/containers/c1/archive" && r.Method == "GET":
		b, ok := m.files[r.URL.Query().Get("path")]
		if !ok {
			http.Error(w, `{"message":"Could not find the file"}`, 404)
			return
		}
		tw := tar.NewWriter(w)
		tw.WriteHeader(&tar.Header{Name: "f", Size: int64(len(b)), Mode: 0o644, Typeflag: tar.TypeReg})
		tw.Write(b)
		tw.Close()
	case p == "/containers/c1/stats":
		io.WriteString(w, `{"cpu_stats":{"cpu_usage":{"total_usage":2000},"system_cpu_usage":20000,"online_cpus":2},
		 "precpu_stats":{"cpu_usage":{"total_usage":1000},"system_cpu_usage":10000},
		 "memory_stats":{"usage":1000,"limit":4000,"stats":{"inactive_file":200}}}`)
	default:
		http.Error(w, `{"message":"unhandled `+r.Method+` `+p+`"}`, 404)
	}
}

func TestMockEngine(t *testing.T) {
	m := &mockEngine{files: map[string][]byte{}, execs: map[string][]string{}}
	srv := httptest.NewServer(m)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := strings.Replace(srv.URL, "http://", "tcp://", 1)
	rec := &env.Record{ID: "e1", Config: json.RawMessage(`{"host":"` + host + `","image":"alpine:3","gpu":true,"cpus":1.5,"memory_mb":64,"volumes":["/a:/b:ro"],"ports":["8080:80"],"extra":"keep"}`)}
	p := New()
	if err := p.Test(ctx, rec.Config); err != nil {
		t.Fatal(err)
	}
	if err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if !m.pulled {
		t.Fatal("image not pulled")
	}
	hc := m.created["HostConfig"].(map[string]any)
	if hc["NanoCpus"].(float64) != 1.5e9 || hc["Memory"].(float64) != 64<<20 || hc["DeviceRequests"] == nil ||
		m.created["Labels"].(map[string]any)[LabelEnv] != "e1" {
		t.Fatalf("create body %+v", m.created)
	}
	var raw map[string]any
	json.Unmarshal(rec.Config, &raw)
	if raw["container_id"] != "c1" || raw["extra"] != "keep" {
		t.Fatalf("config %s", rec.Config)
	}
	e, _ := p.Open(ctx, rec)
	defer e.Close()
	st, err := e.Status(ctx)
	if err != nil || st.State != env.StateRunning || st.Address != "10.0.0.2" {
		t.Fatalf("status %+v %v", st, err)
	}
	s, err := e.Exec(ctx, env.ExecRequest{Cmd: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	o, er, code := readAll(t, s)
	if o != "hello world\n" || er != "oops\n" || code != 3 {
		t.Fatalf("exec %q %q %d", o, er, code)
	}
	s, _ = e.Exec(ctx, env.ExecRequest{Cmd: []string{"cat"}})
	io.WriteString(s.Stdin(), "ping")
	s.Stdin().Close() // half-close must reach the daemon
	o, _, _ = readAll(t, s)
	if o != "ping" {
		t.Fatalf("stdin echo %q", o)
	}
	if err := e.PutFile(ctx, "/d/f.txt", strings.NewReader("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := e.GetFile(ctx, "/d/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "content" {
		t.Fatalf("file %q", b)
	}
	if _, err := e.GetFile(ctx, "/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing file err %v", err)
	}
	mt, err := e.Metrics(ctx)
	if err != nil || mt.CPUPercent != 20 || mt.MemUsed != 800 || mt.MemTotal != 4000 {
		t.Fatalf("metrics %+v %v", mt, err)
	}
}

func TestDemuxIgnoredStderr(t *testing.T) {
	var in bytes.Buffer
	big := strings.Repeat("e", 1<<20)
	in.Write(frame(2, big))
	in.Write(frame(1, "out"))
	out, errp := newBufPipe(1<<10), newBufPipe(1<<10)
	errp.Close() // consumer not interested in stderr
	done := make(chan error, 1)
	go func() { done <- demux(bufio.NewReader(&in), out, errp); out.CloseWithError(nil) }()
	b, _ := io.ReadAll(out)
	if err := <-done; string(b) != "out" || err != nil {
		t.Fatalf("got %q %v", b, err)
	}
}

func TestParsers(t *testing.T) {
	for _, c := range []struct{ in, n, t string }{{"ubuntu:24.04", "ubuntu", "24.04"}, {"ubuntu", "ubuntu", "latest"},
		{"localhost:5000/a/b", "localhost:5000/a/b", "latest"}, {"r:1/x:2", "r:1/x", "2"}, {"a@sha256:ff", "a@sha256:ff", ""}} {
		if n, tg := splitImage(c.in); n != c.n || tg != c.t {
			t.Errorf("splitImage(%s)=%s,%s", c.in, n, tg)
		}
	}
	for _, s := range []string{"80", "8080:80", "127.0.0.1:8080:80/udp", "127.0.0.1::80"} {
		if _, _, _, _, err := parsePort(s); err != nil {
			t.Errorf("parsePort(%s): %v", s, err)
		}
	}
	for _, s := range []string{"", "x", "1:2:3:4", "80/foo", "0", "70000"} {
		if _, _, _, _, err := parsePort(s); err == nil {
			t.Errorf("parsePort(%s) should fail", s)
		}
	}
	fi := parseStatList("12|81a4|1700000000|a b.txt\n4096|41ed|1700000000|dir\nbad\n")
	if len(fi) != 2 || fi[0].Name != "a b.txt" || fi[0].Mode != 0o644 || fi[0].Dir || !fi[1].Dir || fi[1].Mode != 0o755 {
		t.Fatalf("%+v", fi)
	}
	if u, tot := parseDF("Filesystem 1024-blocks Used Available Capacity Mounted on\noverlay 100 40 60 40% /\n"); u != 40960 || tot != 102400 {
		t.Fatal(u, tot)
	}
	if err := New().Validate(json.RawMessage(`{"volumes":["nocolon"]}`)); err == nil {
		t.Fatal("expected volume error")
	}
	if err := New().Validate(json.RawMessage(`{"host":"ssh://x"}`)); err == nil {
		t.Fatal("expected host error")
	}
	var sch map[string]any
	if err := json.Unmarshal(New().Info().ConfigSchema, &sch); err != nil {
		t.Fatal(err)
	}
}
