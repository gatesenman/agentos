//go:build !windows

package virtual

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Desktop is a running virtual desktop.
type Desktop struct {
	Display string // ":N"
	VNCAddr string // 127.0.0.1:port
	opts    Options
	num     int
	port    int

	mu      sync.Mutex
	stopped bool
	procs   map[string]*exec.Cmd
	done    chan struct{}
	wg      sync.WaitGroup
}

// Start launches Xvfb, the window manager, an xterm and x11vnc and waits until
// the VNC port accepts connections.
func Start(o Options) (*Desktop, error) {
	if err := Check(); err != nil {
		return nil, err
	}
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.Height <= 0 {
		o.Height = 800
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.LogDir != "" {
		_ = os.MkdirAll(o.LogDir, 0o755)
	}
	d := &Desktop{opts: o, procs: map[string]*exec.Cmd{}, done: make(chan struct{})}
	num, xvfb, err := d.startXvfb(-1)
	if err != nil {
		return nil, err
	}
	d.num, d.Display = num, ":"+strconv.Itoa(num)
	d.procs["xvfb"] = xvfb
	port, err := freePort()
	if err != nil {
		d.Stop()
		return nil, err
	}
	d.port, d.VNCAddr = port, "127.0.0.1:"+strconv.Itoa(port)
	d.wg.Add(1)
	go d.superviseXvfb(xvfb)
	if wm := windowManager(); wm != "" {
		d.supervise("wm", wm)
	} else {
		o.Logf("virtual desktop: no window manager found (fluxbox/openbox/twm); running bare X")
	}
	d.supervise("x11vnc", "x11vnc", "-display", d.Display, "-rfbport", strconv.Itoa(port), "-localhost",
		"-forever", "-shared", "-nopw", "-noxdamage", "-quiet", "-nolookup")
	d.startOnce("xterm", "xterm", "-geometry", "100x28+60+60", "-title", "AgentOS "+o.Name)
	deadline := time.Now().Add(20 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", d.VNCAddr, 300*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			d.Stop()
			return nil, fmt.Errorf("virtual desktop: x11vnc did not listen on %s: %v (see %s)", d.VNCAddr, err, d.logPath("x11vnc"))
		}
		time.Sleep(150 * time.Millisecond)
	}
	o.Logf("虚拟桌面已启动 / virtual desktop started: DISPLAY=%s VNC=%s (%dx%d)", d.Display, d.VNCAddr, o.Width, o.Height)
	return d, nil
}

// Env returns the environment for programs that should use this desktop.
func (d *Desktop) Env() []string { return []string{"DISPLAY=" + d.Display} }

func (d *Desktop) logPath(name string) string {
	if d.opts.LogDir == "" {
		return ""
	}
	return filepath.Join(d.opts.LogDir, name+".log")
}

func (d *Desktop) output(name string) io.Writer {
	if p := d.logPath(name); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			return f
		}
	}
	return io.Discard
}

func (d *Desktop) command(name, prog string, args ...string) *exec.Cmd {
	c := exec.Command(prog, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DISPLAY=") {
			env = append(env, kv)
		}
	}
	if d.Display != "" {
		env = append(env, "DISPLAY="+d.Display)
	}
	c.Env = env
	c.Dir = os.TempDir()
	if h, err := os.UserHomeDir(); err == nil {
		c.Dir = h
	}
	w := d.output(name)
	c.Stdout, c.Stderr = w, w
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

// startXvfb starts Xvfb on display num, or on a free one chosen by the
// server itself (-displayfd) when num < 0.
func (d *Desktop) startXvfb(num int) (int, *exec.Cmd, error) {
	screen := fmt.Sprintf("%dx%dx24", d.opts.Width, d.opts.Height)
	args := []string{"-screen", "0", screen, "-nolisten", "tcp", "-noreset"}
	var r, w *os.File
	if num < 0 {
		var err error
		if r, w, err = os.Pipe(); err != nil {
			return 0, nil, err
		}
		args = append([]string{"-displayfd", "3"}, args...)
	} else {
		args = append([]string{":" + strconv.Itoa(num)}, args...)
	}
	c := d.command("xvfb", "Xvfb", args...)
	if w != nil {
		c.ExtraFiles = []*os.File{w}
	}
	if err := c.Start(); err != nil {
		if r != nil {
			r.Close()
			w.Close()
		}
		return 0, nil, fmt.Errorf("virtual desktop: start Xvfb: %w", err)
	}
	if w != nil {
		w.Close()
		ch := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(r).ReadString('\n')
			ch <- strings.TrimSpace(line)
		}()
		select {
		case s := <-ch:
			r.Close()
			n, err := strconv.Atoi(s)
			if err != nil {
				c.Process.Kill()
				c.Wait()
				return 0, nil, fmt.Errorf("virtual desktop: Xvfb did not report a display (see %s)", d.logPath("xvfb"))
			}
			num = n
		case <-time.After(20 * time.Second):
			r.Close()
			c.Process.Kill()
			c.Wait()
			return 0, nil, fmt.Errorf("virtual desktop: Xvfb start timeout")
		}
	} else {
		sock := fmt.Sprintf("/tmp/.X11-unix/X%d", num)
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(sock); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return num, c, nil
}

func (d *Desktop) isStopped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

func (d *Desktop) sleep(backoff time.Duration) bool {
	select {
	case <-d.done:
		return false
	case <-time.After(backoff):
		return true
	}
}

func (d *Desktop) superviseXvfb(c *exec.Cmd) {
	defer d.wg.Done()
	backoff := time.Second
	for {
		start := time.Now()
		c.Wait()
		if d.isStopped() {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		d.opts.Logf("virtual desktop: Xvfb %s exited, restarting in %s", d.Display, backoff)
		if !d.sleep(backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
		_, nc, err := d.startXvfb(d.num)
		if err != nil {
			d.opts.Logf("virtual desktop: %v", err)
			continue
		}
		d.mu.Lock()
		if d.stopped {
			d.mu.Unlock()
			nc.Process.Kill()
			nc.Wait()
			return
		}
		d.procs["xvfb"] = nc
		d.mu.Unlock()
		c = nc
	}
}

// supervise keeps prog running until Stop.
func (d *Desktop) supervise(name, prog string, args ...string) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		backoff := time.Second
		for {
			c := d.command(name, prog, args...)
			d.mu.Lock()
			if d.stopped {
				d.mu.Unlock()
				return
			}
			err := c.Start()
			if err == nil {
				d.procs[name] = c
			}
			d.mu.Unlock()
			start := time.Now()
			if err == nil {
				c.Wait()
			}
			if d.isStopped() {
				return
			}
			if time.Since(start) > 30*time.Second {
				backoff = time.Second
			}
			d.opts.Logf("virtual desktop: %s exited (%v), restarting in %s", name, err, backoff)
			if !d.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
}

// startOnce starts a convenience program (the initial xterm); it is not restarted.
func (d *Desktop) startOnce(name, prog string, args ...string) {
	if _, err := exec.LookPath(prog); err != nil {
		return
	}
	c := d.command(name, prog, args...)
	if c.Start() != nil {
		return
	}
	d.mu.Lock()
	d.procs[name] = c
	d.mu.Unlock()
	go c.Wait()
}

// Stop terminates all desktop processes. Safe to call more than once.
func (d *Desktop) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	close(d.done)
	procs := d.procs
	d.mu.Unlock()
	for _, c := range procs {
		if c.Process != nil {
			syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
		}
	}
	waited := make(chan struct{})
	go func() { d.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		for _, c := range procs {
			if c.Process != nil {
				syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			}
		}
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
