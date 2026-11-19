package ssh

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/agentos/agentos/internal/env"
	"golang.org/x/crypto/ssh"
)

// ShellQuote quotes s for POSIX sh.
func ShellQuote(s string) string {
	if s != "" && safeRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var safeRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// encodePS returns the base64 UTF-16LE form for powershell -EncodedCommand.
func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// winQuote quotes one argument per the MSVCRT CommandLineToArgvW rules.
func winQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	bs := 0
	for _, r := range s {
		switch r {
		case '\\':
			bs++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, bs*2+1))
		default:
			b.WriteString(strings.Repeat(`\`, bs))
		}
		bs = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, bs*2))
	b.WriteByte('"')
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// BuildCommand turns an ExecRequest into the command line sent over SSH for the
// given remote OS. Empty result means "start the login shell".
func BuildCommand(goos string, req env.ExecRequest, defaultShell string) (string, error) {
	for k := range req.Env {
		if !envNameRe.MatchString(k) {
			return "", fmt.Errorf("ssh: 环境变量名无效 %q", k)
		}
	}
	if goos == "windows" {
		if len(req.Env) == 0 && req.WorkDir == "" {
			if len(req.Cmd) == 0 {
				return defaultShell, nil
			}
			q := make([]string, len(req.Cmd))
			for i, a := range req.Cmd {
				q[i] = winQuote(a)
			}
			return strings.Join(q, " "), nil
		}
		var sb strings.Builder
		sb.WriteString("$ErrorActionPreference='Stop'; ")
		if req.WorkDir != "" {
			sb.WriteString("Set-Location -LiteralPath " + psQuote(req.WorkDir) + "; ")
		}
		for _, k := range sortedKeys(req.Env) {
			sb.WriteString("$env:" + k + "=" + psQuote(req.Env[k]) + "; ")
		}
		cmd := req.Cmd
		if len(cmd) == 0 {
			cmd = []string{"powershell.exe", "-NoLogo"}
		}
		sb.WriteString("& " + psQuote(cmd[0]))
		for _, a := range cmd[1:] {
			sb.WriteString(" " + psQuote(a))
		}
		sb.WriteString("; exit $LASTEXITCODE")
		return "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePS(sb.String()), nil
	}
	if len(req.Cmd) == 0 && len(req.Env) == 0 && req.WorkDir == "" {
		return defaultShell, nil
	}
	var parts []string
	if req.WorkDir != "" {
		parts = append(parts, "cd -- "+ShellQuote(req.WorkDir))
	}
	for _, k := range sortedKeys(req.Env) {
		parts = append(parts, "export "+k+"="+ShellQuote(req.Env[k]))
	}
	if len(req.Cmd) == 0 {
		if defaultShell != "" {
			parts = append(parts, "exec "+defaultShell)
		} else {
			parts = append(parts, `exec "${SHELL:-/bin/sh}" -l`)
		}
	} else {
		q := make([]string, len(req.Cmd))
		for i, a := range req.Cmd {
			q[i] = ShellQuote(a)
		}
		parts = append(parts, "exec "+strings.Join(q, " "))
	}
	return strings.Join(parts, " && "), nil
}

type execSession struct {
	s      *ssh.Session
	stdin  io.WriteCloser
	stdout *bufPipe
	stderr *bufPipe
	copies sync.WaitGroup
	once   sync.Once
}

func (e *sshEnv) Exec(ctx context.Context, req env.ExecRequest) (env.ExecSession, error) {
	o, err := e.remoteOS(ctx)
	if err != nil {
		return nil, err
	}
	cmdline, err := BuildCommand(o, req, e.cfg.Shell)
	if err != nil {
		return nil, err
	}
	cl, err := e.client(ctx)
	if err != nil {
		return nil, err
	}
	s, err := cl.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}
	es := &execSession{s: s, stdout: newBufPipe(8 << 20), stderr: newBufPipe(8 << 20)}
	fail := func(err error) (env.ExecSession, error) { s.Close(); return nil, fmt.Errorf("ssh: %w", err) }
	if req.TTY {
		cols, rows := req.Cols, req.Rows
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
		if err := s.RequestPty("xterm-256color", rows, cols, modes); err != nil {
			return fail(err)
		}
	}
	if es.stdin, err = s.StdinPipe(); err != nil {
		return fail(err)
	}
	out, err := s.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	serr, err := s.StderrPipe()
	if err != nil {
		return fail(err)
	}
	pump := func(dst *bufPipe, src io.Reader) {
		defer es.copies.Done()
		_, err := io.Copy(dst, src)
		if errors.Is(err, io.ErrClosedPipe) { // consumer gave up: keep draining so the channel never stalls
			io.Copy(io.Discard, src)
			err = nil
		}
		dst.CloseWithError(err)
	}
	es.copies.Add(2)
	go pump(es.stdout, out)
	go pump(es.stderr, serr)
	if cmdline == "" {
		err = s.Shell()
	} else {
		err = s.Start(cmdline)
	}
	if err != nil {
		return fail(err)
	}
	return es, nil
}

func (s *execSession) Stdin() io.WriteCloser { return s.stdin }
func (s *execSession) Stdout() io.Reader     { return s.stdout }
func (s *execSession) Stderr() io.Reader     { return s.stderr }

func (s *execSession) Resize(cols, rows int) error { return s.s.WindowChange(rows, cols) }

func (s *execSession) Wait() (int, error) {
	err := s.s.Wait()
	s.copies.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus(), nil
	}
	return -1, err
}

func (s *execSession) Close() error {
	s.once.Do(func() {
		s.s.Close()
		s.stdout.Close()
		s.stderr.Close()
	})
	return nil
}
