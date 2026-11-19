package agentfed

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// enumerateListeningLoopbackPorts returns TCP ports with a listener on
// 127.0.0.1, ::1, or 0.0.0.0/:: (reachable via loopback).
func enumerateListeningLoopbackPorts(ctx context.Context) []int {
	seen := map[int]struct{}{}
	add := func(p int) {
		if p > 0 && p < 65536 {
			seen[p] = struct{}{}
		}
	}
	switch runtime.GOOS {
	case "windows":
		for _, p := range listenPortsWindows(ctx) {
			add(p)
		}
	default:
		for _, p := range listenPortsProcNet("/proc/net/tcp") {
			add(p)
		}
		for _, p := range listenPortsProcNet("/proc/net/tcp6") {
			add(p)
		}
		if len(seen) == 0 {
			for _, p := range listenPortsSS(ctx) {
				add(p)
			}
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func listenPortsProcNet(path string) []int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []int
	sc := bufio.NewScanner(f)
	if sc.Scan() { // header
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "0A" && fields[3] != "0a" {
			continue
		}
		local := fields[1]
		ipPort := strings.Split(local, ":")
		if len(ipPort) < 2 {
			continue
		}
		portHex := ipPort[len(ipPort)-1]
		ipHex := strings.Join(ipPort[:len(ipPort)-1], "")
		p64, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		if !procNetIsLoopbackOrAny(ipHex) {
			continue
		}
		out = append(out, int(p64))
	}
	return out
}

func procNetIsLoopbackOrAny(ipHex string) bool {
	ipHex = strings.ToUpper(ipHex)
	switch len(ipHex) {
	case 8:
		return ipHex == "0100007F" || ipHex == "00000000"
	case 32:
		if ipHex == "00000000000000000000000000000000" || ipHex == "00000000000000000000000001000000" {
			return true
		}
		b := make([]byte, 16)
		for i := 0; i < 16; i++ {
			v, err := strconv.ParseUint(ipHex[i*2:i*2+2], 16, 8)
			if err != nil {
				return false
			}
			b[i] = byte(v)
		}
		ip := net.IP(b)
		return ip.IsLoopback() || ip.IsUnspecified()
	default:
		return false
	}
}

func listenPortsSS(ctx context.Context) []int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ss", "-H", "-lnt")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.CommandContext(ctx, "netstat", "-lnt")
		out, err = cmd.Output()
		if err != nil {
			return nil
		}
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Proto") {
			continue
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			host, port, err := net.SplitHostPort(normalizeListenAddr(f))
			if err != nil {
				continue
			}
			if !isLoopbackOrAnyHost(host) {
				continue
			}
			p, err := strconv.Atoi(port)
			if err == nil {
				ports = append(ports, p)
			}
		}
	}
	return ports
}

func normalizeListenAddr(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "*:") {
		return "0.0.0.0" + s[1:]
	}
	return s
}

func isLoopbackOrAnyHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "*" || host == "0.0.0.0" || host == "::" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

func listenPortsWindows(ctx context.Context) []int {
	// Prefer netstat: fast, no Get-NetTCPConnection module dependency, works
	// from service PATH. PowerShell was previously tried first and often returned
	// empty (listen_ports=0) while AgentOS itself was listening on 7800.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if ports := listenPortsWindowsNetstat(ctx); len(ports) > 0 {
		return ports
	}
	ps := `Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.LocalAddress -in @('127.0.0.1','::1','0.0.0.0','::') } | Select-Object -ExpandProperty LocalPort -Unique`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, err := strconv.Atoi(line); err == nil {
			ports = append(ports, p)
		}
	}
	return ports
}

func listenPortsWindowsNetstat(ctx context.Context) []int {
	cmd := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		u := strings.ToUpper(line)
		if !strings.Contains(u, "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		addr := fields[1]
		// netstat may show "127.0.0.1:7800" or "[::1]:7800"
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if !isLoopbackOrAnyHost(host) {
			continue
		}
		p, err := strconv.Atoi(port)
		if err == nil {
			ports = append(ports, p)
		}
	}
	return ports
}

func mergePorts(base []int, extra ...[]int) []int {
	seen := map[int]struct{}{}
	for _, p := range base {
		if p > 0 && p < 65536 {
			seen[p] = struct{}{}
		}
	}
	for _, list := range extra {
		for _, p := range list {
			if p > 0 && p < 65536 {
				seen[p] = struct{}{}
			}
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}
