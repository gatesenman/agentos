package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// realDocker returns a provider config for the local daemon or skips.
// Set AGENTOS_TEST_DOCKER=0 to force skip.
func realDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTOS_TEST_DOCKER") == "0" {
		t.Skip("AGENTOS_TEST_DOCKER=0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := New().Test(ctx, nil); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
}

func readAll(t *testing.T, s env.ExecSession) (string, string, int) {
	t.Helper()
	var o, e bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&e, s.Stderr()); close(done) }()
	io.Copy(&o, s.Stdout())
	<-done
	code, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

// TestRealDocker exercises the provider against a real Docker daemon (REAL).
func TestRealDocker(t *testing.T) {
	realDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p, _ := env.Get("docker")
	rec := &env.Record{ID: "test-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", ""),
		Config: json.RawMessage(`{"image":"ubuntu:24.04","memory_mb":256,"cpus":0.5,"env":{"FOO":"bar"},"ports":["127.0.0.1::8080"]}`)}
	if err := p.Validate(rec.Config); err != nil {
		t.Fatal(err)
	}
	if err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	json.Unmarshal(rec.Config, &cfg)
	if cfg.ContainerID == "" || cfg.Image != "ubuntu:24.04" {
		t.Fatalf("config not updated: %s", rec.Config)
	}
	e, err := p.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	defer e.Delete(env.WithPurge(context.Background()))

	st, err := e.Status(ctx)
	if err != nil || st.State != env.StateRunning || st.Address == "" {
		t.Fatalf("status %+v %v", st, err)
	}

	// non-tty exec: stdout/stderr demux, env, workdir, stdin, exit code
	s, err := e.Exec(ctx, env.ExecRequest{Cmd: []string{"sh", "-c", "cat; echo out-$FOO-$X-$(pwd); echo err >&2; exit 7"},
		Env: map[string]string{"X": "y"}, WorkDir: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(s.Stdin(), "in\n")
	s.Stdin().Close()
	o, er, code := readAll(t, s)
	s.Close()
	if o != "in\nout-bar-y-/tmp\n" || er != "err\n" || code != 7 {
		t.Fatalf("exec got %q %q %d", o, er, code)
	}

	// tty exec + resize
	s, err = e.Exec(ctx, env.ExecRequest{Cmd: []string{"sh", "-c", "sleep 0.5; stty size; echo e >&2"}, TTY: true, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	o, _, code = readAll(t, s)
	s.Close()
	if !strings.Contains(o, "40 120") || !strings.Contains(o, "e") || code != 0 {
		t.Fatalf("tty got %q %d", o, code)
	}

	// files
	data := bytes.Repeat([]byte("agentos\n"), 10000)
	if err := e.PutFile(ctx, "/opt/x/y/data.txt", bytes.NewReader(data), 0o750); err != nil {
		t.Fatal(err)
	}
	rc, err := e.GetFile(ctx, "/opt/x/y/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("getfile mismatch %d", len(got))
	}
	if _, err := e.GetFile(ctx, "/nope"); err == nil {
		t.Fatal("expected error")
	}
	ls, err := e.ListDir(ctx, "/opt/x/y")
	if err != nil || len(ls) != 1 || ls[0].Name != "data.txt" || ls[0].Size != int64(len(data)) || ls[0].Mode != 0o750 || ls[0].Dir {
		t.Fatalf("listdir %+v %v", ls, err)
	}
	ls, err = e.ListDir(ctx, "/opt/x")
	if err != nil || len(ls) != 1 || !ls[0].Dir {
		t.Fatalf("listdir dir %+v %v", ls, err)
	}
	if _, err := e.ListDir(ctx, "/does/not/exist"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("listdir missing: %v", err)
	}

	m, err := e.Metrics(ctx)
	if err != nil || m.MemTotal != 256<<20 || m.MemUsed == 0 || m.DiskTotal == 0 {
		t.Fatalf("metrics %+v %v", m, err)
	}
	if _, err := e.Desktop(ctx, "vnc"); err != env.ErrUnsupported {
		t.Fatalf("desktop on plain image: %v", err)
	}

	if err := e.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.State != env.StateStopped {
		t.Fatalf("after stop %+v", st)
	}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx); err != nil { // idempotent (304)
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.State != env.StateRunning {
		t.Fatalf("after start %+v", st)
	}
	// delete without purge keeps the (stopped) container
	if err := e.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.State != env.StateStopped {
		t.Fatalf("after non-purge delete %+v", st)
	}
	pctx := env.WithPurge(ctx)
	if err := e.Delete(pctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.State != env.StateError {
		t.Fatalf("after purge delete %+v", st)
	}
	if err := e.Delete(pctx); err != nil {
		t.Fatal(err)
	}
}

// TestRealDockerDesktop needs the localhost/agentos-desktop image (REAL, skipped if absent).
func TestRealDockerDesktop(t *testing.T) {
	realDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cl, _ := newClient("")
	if err := cl.json(ctx, "GET", "/images/"+DesktopImage+"/json", nil, nil, nil); err != nil {
		t.Skipf("desktop image not built: %v", err)
	}
	p := New()
	rec := &env.Record{ID: "test-desk-" + time.Now().Format("150405"), Config: json.RawMessage(`{"desktop":true}`)}
	if err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	e, _ := p.Open(ctx, rec)
	defer e.Close()
	defer e.Delete(env.WithPurge(context.Background()))
	var cfg Config
	json.Unmarshal(rec.Config, &cfg)
	if cfg.VNCPort == 0 {
		t.Fatalf("no vnc port: %s", rec.Config)
	}
	var lastErr error
	for i := 0; i < 60; i++ {
		c, err := e.Desktop(ctx, "vnc")
		if err == nil {
			c.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 12)
			_, err = io.ReadFull(c, buf)
			c.Close()
			if err == nil {
				if !strings.HasPrefix(string(buf), "RFB 003.") {
					t.Fatalf("bad banner %q", buf)
				}
				return
			}
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("vnc never came up: %v", lastErr)
}
