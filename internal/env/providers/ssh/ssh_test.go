package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentos/agentos/internal/env"
	"golang.org/x/crypto/ssh"
)

func run(t *testing.T, e env.Env, req env.ExecRequest, stdin string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := e.Exec(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	io.WriteString(s.Stdin(), stdin)
	s.Stdin().Close()
	var o, eb bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&eb, s.Stderr()); close(done) }()
	io.Copy(&o, s.Stdout())
	<-done
	code, err := s.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return o.String(), eb.String(), code
}

// exercise runs the common exec/files/desktop/metrics checks against a live env.
func exercise(t *testing.T, e env.Env, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := e.Status(ctx)
	if err != nil || st.State != env.StateRunning {
		t.Fatalf("status %+v %v", st, err)
	}
	o, er, code := run(t, e, env.ExecRequest{Cmd: []string{"sh", "-c", `cat; echo "$FOO|$(pwd)|$1"; echo err >&2; exit 5`, "sh", "it's a \"test\""},
		Env: map[string]string{"FOO": "b a'r"}, WorkDir: dir}, "in\n")
	if o != "in\nb a'r|"+dir+"|it's a \"test\"\n" || er != "err\n" || code != 5 {
		t.Fatalf("exec: %q %q %d", o, er, code)
	}
	// files
	data := bytes.Repeat([]byte("0123456789"), 50000)
	p := dir + "/sub/dir/f.bin"
	if err := e.PutFile(ctx, p, bytes.NewReader(data), 0o640); err != nil {
		t.Fatal(err)
	}
	rc, err := e.GetFile(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("roundtrip %d bytes", len(got))
	}
	ls, err := e.ListDir(ctx, dir+"/sub/dir")
	if err != nil || len(ls) != 1 || ls[0].Name != "f.bin" || ls[0].Mode != 0o640 || ls[0].Size != int64(len(data)) {
		t.Fatalf("ls %+v %v", ls, err)
	}
	if ls, _ := e.ListDir(ctx, dir+"/sub"); len(ls) != 1 || !ls[0].Dir {
		t.Fatalf("ls dir %+v", ls)
	}
	if _, err := e.GetFile(ctx, dir+"/nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := e.ListDir(ctx, dir+"/nope"); err == nil {
		t.Fatal("missing dir")
	}
	// metrics (/proc on linux)
	m, err := e.Metrics(ctx)
	if err != nil || m.MemTotal == 0 || m.MemUsed == 0 || m.DiskTotal == 0 {
		t.Fatalf("metrics %+v %v", m, err)
	}
	if m2, err := e.Metrics(ctx); err != nil || m2.CPUPercent < 0 || m2.CPUPercent > 100 {
		t.Fatalf("metrics2 %+v %v", m2, err)
	}
	if err := e.Start(ctx); err != env.ErrUnsupported {
		t.Fatalf("start: %v", err)
	}
	if err := e.Stop(ctx); err != env.ErrUnsupported {
		t.Fatalf("stop: %v", err)
	}
	if _, err := e.Desktop(ctx, "screen"); err != env.ErrUnsupported {
		t.Fatal(err)
	}
}

// fakeVNC listens on 127.0.0.1 and sends an RFB banner.
func fakeVNC(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("RFB 003.008\n"))
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func checkVNC(t *testing.T, e env.Env) {
	c, err := e.Desktop(context.Background(), "vnc")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := make([]byte, 12)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "RFB 003.008\n" {
		t.Fatalf("vnc %q %v", b, err)
	}
}

// TestInProcessServer: REAL ssh protocol against an in-process x/crypto/ssh + sftp server.
func TestInProcessServer(t *testing.T) {
	signer, keyPEM := genKey(t)
	srv := startTestServer(t, signer.PublicKey())
	vnc := fakeVNC(t)
	p := New()
	ctx := context.Background()

	bad, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": srv.port(), "user": "u", "password": "wrong"})
	if err := p.Test(ctx, bad); err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("bad password: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": srv.port(), "user": "u", "private_key": keyPEM, "vnc_port": vnc})
	if err := p.Test(ctx, raw); err != nil {
		t.Fatal(err)
	}
	rec := &env.Record{ID: "s1", Config: raw}
	if err := p.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	json.Unmarshal(rec.Config, &cfg)
	if cfg.HostKey != srv.hostFP || cfg.OSDetected != "linux" || cfg.PrivateKey == "" {
		t.Fatalf("config after create: %+v", cfg)
	}
	e, err := p.Open(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	exercise(t, e, t.TempDir())
	checkVNC(t, e)

	// pty + resize bookkeeping on the server
	s, err := e.Exec(ctx, env.ExecRequest{Cmd: []string{"sleep", "0.5"}, TTY: true, Cols: 90, Rows: 20})
	if err != nil {
		t.Fatal(err)
	}
	s.Resize(132, 43)
	io.Copy(io.Discard, s.Stdout())
	s.Wait()
	s.Close()
	srv.mu.Lock()
	if srv.ptyReqs != 1 || len(srv.resizes) != 1 || srv.resizes[0] != "132x43" {
		t.Fatalf("pty %d resizes %v", srv.ptyReqs, srv.resizes)
	}
	srv.mu.Unlock()

	// host key mismatch is refused
	cfg.HostKey = "SHA256:AAAA"
	raw2, _ := json.Marshal(cfg)
	if err := p.Test(ctx, raw2); err == nil || !strings.Contains(err.Error(), "主机密钥不匹配") {
		t.Fatalf("mismatch: %v", err)
	}
	cfg.HostKeyPolicy, cfg.HostKey = PolicyStrict, ""
	raw3, _ := json.Marshal(cfg)
	if err := p.Validate(raw3); err == nil {
		t.Fatal("strict without key should fail")
	}
	// reconnect after the connection drops
	se := e.(*sshEnv)
	se.mu.Lock()
	se.cl.Close()
	se.mu.Unlock()
	if o, _, _ := run(t, e, env.ExecRequest{Cmd: []string{"echo", "again"}}, ""); o != "again\n" {
		t.Fatalf("reconnect %q", o)
	}
}

// TestLocalSSHD: REAL OpenSSH sshd (unprivileged, random port) incl. a real pty.
func TestLocalSSHD(t *testing.T) {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
	}
	if _, err := os.Stat(sshd); err != nil || os.Getenv("AGENTOS_TEST_SSHD") == "0" {
		t.Skip("sshd not available")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	hostSigner, hostPEM := genKey(t)
	os.WriteFile(filepath.Join(dir, "host_key"), []byte(hostPEM), 0o600)
	cliSigner, cliPEM := genKey(t)
	os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(cliSigner.PublicKey()), 0o600)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	sftpServer := "/usr/lib/openssh/sftp-server"
	if _, err := os.Stat(sftpServer); err != nil {
		sftpServer = "internal-sftp"
	}
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s/host_key
PidFile %s/sshd.pid
AuthorizedKeysFile %s/authorized_keys
StrictModes no
UsePAM no
PasswordAuthentication no
AllowTcpForwarding yes
Subsystem sftp %s
`, port, dir, dir, dir, sftpServer)
	os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(conf), 0o600)
	cmd := exec.Command(sshd, "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	var logb syncBuffer
	cmd.Stderr = &logb
	if err := cmd.Start(); err != nil {
		t.Skipf("start sshd: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 300; i++ { // up to 30s on a loaded box
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	user := os.Getenv("USER")
	if user == "" {
		out, _ := exec.Command("id", "-un").Output()
		user = strings.TrimSpace(string(out))
	}
	vnc := fakeVNC(t)
	raw, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": port, "user": user, "private_key": cliPEM,
		"host_key_policy": "strict", "host_key": ssh.FingerprintSHA256(hostSigner.PublicKey()), "vnc_port": vnc})
	p := New()
	rec := &env.Record{ID: "real", Config: raw}
	if err := p.Create(context.Background(), rec); err != nil {
		t.Fatalf("%v\nsshd log:\n%s", err, logb.String())
	}
	e, _ := p.Open(context.Background(), rec)
	defer e.Close()
	exercise(t, e, t.TempDir())
	checkVNC(t, e)

	// real pty: stty sees the size, resize applies, stderr merges into stdout
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := e.Exec(ctx, env.ExecRequest{Cmd: []string{"sh", "-c", "stty size; read x; stty size; echo $TERM; echo e >&2"}, TTY: true, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	s.Resize(120, 40)
	time.Sleep(300 * time.Millisecond)
	io.WriteString(s.Stdin(), "go\n")
	var o bytes.Buffer
	io.Copy(&o, s.Stdout())
	code, _ := s.Wait()
	s.Close()
	if !strings.Contains(o.String(), "30 100") || !strings.Contains(o.String(), "40 120") || !strings.Contains(o.String(), "xterm-256color") ||
		!strings.Contains(o.String(), "e\r\n") || code != 0 {
		t.Fatalf("pty out %q code %d", o.String(), code)
	}
	// interactive login shell without a command
	s, err = e.Exec(ctx, env.ExecRequest{TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(s.Stdin(), "echo shell-$((6*7)); exit 3\n")
	o.Reset()
	io.Copy(&o, s.Stdout())
	code, _ = s.Wait()
	s.Close()
	if !strings.Contains(o.String(), "shell-42") || code != 3 {
		t.Fatalf("shell out %q code %d", o.String(), code)
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"abc": "abc", "a b": "'a b'", "it's": `'it'\''s'`, "": "''"} {
		if ShellQuote(in) != want {
			t.Errorf("ShellQuote(%q)=%s", in, ShellQuote(in))
		}
	}
	for in, want := range map[string]string{`a`: `a`, `a b`: `"a b"`, `a"b`: `"a\"b"`, `C:\x y\`: `"C:\x y\\"`} {
		if winQuote(in) != want {
			t.Errorf("winQuote(%q)=%s", in, winQuote(in))
		}
	}
	if c, _ := BuildCommand("windows", env.ExecRequest{Cmd: []string{"cmd", "/c", "echo hi"}}, ""); c != `cmd /c "echo hi"` {
		t.Error(c)
	}
	if c, _ := BuildCommand("windows", env.ExecRequest{Cmd: []string{"x"}, WorkDir: `C:\a`}, ""); !strings.HasPrefix(c, "powershell -NoProfile -NonInteractive -EncodedCommand ") {
		t.Error(c)
	}
	if _, err := BuildCommand("linux", env.ExecRequest{Cmd: []string{"x"}, Env: map[string]string{"A;rm": "1"}}, ""); err == nil {
		t.Error("bad env name accepted")
	}
	if o, a := ParseUname("Darwin arm64\n"); o != "darwin" || a != "arm64" {
		t.Error(o, a)
	}
	if o, a := ParseUname("Linux x86_64"); o != "linux" || a != "amd64" {
		t.Error(o, a)
	}
	m, err := parseWindowsMetrics("12 16000000 4000000 500000000000 100000000000\r\n")
	if err != nil || m.CPUPercent != 12 || m.MemUsed != 12000000*1024 || m.DiskUsed != 400000000000 {
		t.Fatalf("%+v %v", m, err)
	}
	dm := parseDarwinMetrics("CPU usage: 3.33% user, 5.0% sys, 91.67% idle\n@@@\n17179869184\n@@@\nMach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:      100000.\nPages inactive:  100000.\n@@@\nFilesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s1 1000 400 600 40% /\n")
	if dm.CPUPercent < 8.3 || dm.CPUPercent > 8.4 || dm.MemTotal != 17179869184 || dm.MemUsed != 17179869184-200000*16384 || dm.DiskTotal != 1024000 {
		t.Fatalf("%+v", dm)
	}
}

// syncBuffer is a goroutine-safe buffer for subprocess output read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
