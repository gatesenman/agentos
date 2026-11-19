package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testServer is an in-process SSH server (x/crypto/ssh) with exec, shell,
// pty-req/window-change bookkeeping, the sftp subsystem and direct-tcpip.
type testServer struct {
	addr    string
	hostFP  string
	ln      net.Listener
	mu      sync.Mutex
	ptyReqs int
	resizes []string
}

func genKey(t *testing.T) (ssh.Signer, string) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ssh.NewSignerFromKey(priv)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, string(pem.EncodeToMemory(blk))
}

func startTestServer(t *testing.T, clientPub ssh.PublicKey) *testServer {
	hostKey, _ := genKey(t)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "u" && string(pw) == "pw" {
				return nil, nil
			}
			return nil, io.EOF
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if clientPub != nil && string(k.Marshal()) == string(clientPub.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{addr: ln.Addr().String(), hostFP: ssh.FingerprintSHA256(hostKey.PublicKey()), ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveConn(c, cfg)
		}
	}()
	return s
}

func (s *testServer) port() int {
	_, p, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(p)
	return n
}

func (s *testServer) serveConn(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, rq, err := nc.Accept()
			if err == nil {
				go s.session(ch, rq)
			}
		case "direct-tcpip":
			var p struct {
				Host  string
				Port  uint32
				OHost string
				OPort uint32
			}
			ssh.Unmarshal(nc.ExtraData(), &p)
			tc, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
			if err != nil {
				nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, rq, _ := nc.Accept()
			go ssh.DiscardRequests(rq)
			go func() { io.Copy(ch, tc); ch.CloseWrite() }()
			go func() { io.Copy(tc, ch); tc.Close() }()
		default:
			nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func exitStatus(ch ssh.Channel, code int) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	ch.SendRequest("exit-status", false, b)
	ch.Close()
}

func (s *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			s.mu.Lock()
			s.ptyReqs++
			s.mu.Unlock()
			r.Reply(true, nil)
		case "window-change":
			var w struct{ Cols, Rows, W, H uint32 }
			ssh.Unmarshal(r.Payload, &w)
			s.mu.Lock()
			s.resizes = append(s.resizes, strconv.Itoa(int(w.Cols))+"x"+strconv.Itoa(int(w.Rows)))
			s.mu.Unlock()
		case "env":
			r.Reply(true, nil)
		case "exec", "shell":
			var cmdline string
			if r.Type == "exec" {
				var p struct{ Cmd string }
				ssh.Unmarshal(r.Payload, &p)
				cmdline = p.Cmd
			}
			r.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", cmdline)
			if r.Type == "shell" {
				cmd = exec.Command("/bin/sh")
			}
			cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
			in, _ := cmd.StdinPipe()
			go func() { io.Copy(in, ch); in.Close() }()
			go func() {
				code := 0
				if err := cmd.Run(); err != nil {
					code = 255
					if ee, ok := err.(*exec.ExitError); ok {
						code = ee.ExitCode()
					}
				}
				exitStatus(ch, code)
			}()
			go func() {
				for r := range reqs {
					if r.Type == "window-change" {
						var w struct{ Cols, Rows, W, H uint32 }
						ssh.Unmarshal(r.Payload, &w)
						s.mu.Lock()
						s.resizes = append(s.resizes, strconv.Itoa(int(w.Cols))+"x"+strconv.Itoa(int(w.Rows)))
						s.mu.Unlock()
					}
					if r.WantReply {
						r.Reply(false, nil)
					}
				}
			}()
			return
		case "subsystem":
			var p struct{ Name string }
			ssh.Unmarshal(r.Payload, &p)
			if p.Name != "sftp" {
				r.Reply(false, nil)
				continue
			}
			r.Reply(true, nil)
			go ssh.DiscardRequests(reqs)
			srv, _ := sftp.NewServer(ch)
			srv.Serve()
			srv.Close()
			exitStatus(ch, 0)
			return
		default:
			if r.WantReply {
				r.Reply(false, nil)
			}
		}
	}
}
