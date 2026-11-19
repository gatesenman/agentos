//go:build !windows

package virtual

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestVirtualDesktop(t *testing.T) {
	if err := Check(); err != nil {
		t.Skip(err)
	}
	a, err := Start(Options{Name: "a", LogDir: t.TempDir(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := Start(Options{Name: "b", Width: 800, Height: 600})
	if err != nil {
		t.Fatal(err)
	}
	if a.Display == b.Display || a.VNCAddr == b.VNCAddr {
		t.Fatalf("desktops not separate: %s %s / %s %s", a.Display, a.VNCAddr, b.Display, b.VNCAddr)
	}
	c, err := net.DialTimeout("tcp", a.VNCAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	banner, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.HasPrefix(banner, "RFB ") {
		t.Fatalf("banner %q", banner)
	}
	if _, err := exec.LookPath("xdpyinfo"); err == nil {
		cmd := exec.Command("xdpyinfo")
		cmd.Env = append(os.Environ(), "DISPLAY="+b.Display)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "800x600") {
			t.Fatalf("xdpyinfo %s: %v %s", b.Display, err, out)
		}
	}
	b.Stop()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat("/tmp/.X11-unix/X" + strings.TrimPrefix(b.Display, ":")); err == nil {
		t.Errorf("display %s still present after Stop", b.Display)
	}
	if c, err := net.DialTimeout("tcp", b.VNCAddr, time.Second); err == nil {
		c.Close()
		t.Errorf("vnc %s still listening after Stop", b.VNCAddr)
	}
}
