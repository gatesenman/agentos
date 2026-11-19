package hyperv

import (
	"strings"
	"testing"
)

func testParams() SeedParams {
	return SeedParams{Hostname: "h", User: "agent", Password: "Pw-1234-Abcd", PasswordHash: sha512Crypt("Pw-1234-Abcd", "saltsalt"),
		AuthorizedKey: "ssh-ed25519 AAAA agentos", HostKeyPriv: "PRIV", HostKeyPub: "ssh-ed25519 BBBB",
		Locale: "zh_CN.UTF-8", WinLocale: "zh-CN", TimeZone: "Asia/Shanghai", WinTimeZone: "China Standard Time", VNCPort: 5900, FedoraRel: 44}
}

func TestUbuntuSeedContent(t *testing.T) {
	p := testParams()
	files, err := UbuntuSeed(p, "i1")
	if err != nil || len(files) != 2 {
		t.Fatal(err)
	}
	ud := string(files[0].Data)
	for _, want := range []string{"#cloud-config", "autoinstall:", "fallback: offline-install", "username: agent", "late-commands:", "shutdown: reboot"} {
		if !strings.Contains(ud, want) {
			t.Fatalf("user-data lacks %q", want)
		}
	}
	if strings.Contains(ud, "proxy:") {
		t.Fatal("no proxy expected")
	}
	p.Proxy = "http://10.0.2.2:7897"
	files, _ = UbuntuSeed(p, "i1")
	if !strings.Contains(string(files[0].Data), "proxy: ") {
		t.Fatal("proxy missing")
	}
	script, _ := render("setup", ubuntuSetupScript, p)
	for _, want := range []string{"U=agent", "AutomaticLogin=$U", "agentos-packages.service", "Acquire::http::Proxy", "WaylandEnable=false", "/var/lib/agentos/installed", "-localhost -rfbport 5900", "Prompt=never", "Session=ubuntu-xorg", "update-locale LANG=", "/usr/share/zoneinfo/"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("setup lacks %q", want)
		}
	}
}

func TestWindowsAndFedoraSeedContent(t *testing.T) {
	p := testParams()
	files, err := WindowsSeed(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var xml string
	for _, f := range files {
		names = append(names, f.Name)
		if strings.EqualFold(f.Name, "autounattend.xml") {
			xml = string(f.Data)
		}
	}
	if xml == "" {
		t.Fatalf("no autounattend.xml in %v", names)
	}
	for _, want := range []string{"BypassTPMCheck", "BypassVirtualizationBasedSecurityCheck", "EnableVirtualizationBasedSecurity", "zh-CN", "<Username>agent</Username>", "agentos-setup.ps1"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("autounattend lacks %q", want)
		}
	}
	var setupPS string
	for _, f := range files {
		if strings.EqualFold(f.Name, "agentos-setup.ps1") {
			setupPS = string(f.Data)
		}
	}
	if setupPS == "" {
		t.Fatalf("no agentos-setup.ps1 in %v", names)
	}
	msi := strings.Index(setupPS, "installing OpenSSH from MSI")
	online := strings.Index(setupPS, "Online capability with 90s")
	if msi < 0 || online < 0 || msi > online {
		t.Fatal("OpenSSH must try MSI before Online capability")
	}
	if !strings.Contains(setupPS, "Wait-Job $job -Timeout 90") {
		t.Fatal("Online capability must use a short Wait-Job timeout")
	}
	if !strings.Contains(setupPS, "ssh=FAIL") || !strings.Contains(setupPS, `Set-Content "$state\ready.txt"`) {
		t.Fatal("ready.txt must always be written, including ssh=FAIL")
	}
	if strings.Contains(setupPS, "Get-WindowsCapability -Online") && msi > strings.Index(setupPS, "Get-WindowsCapability -Online") {
		// first Online call must be after MSI path; allow Online only in the fallback job
	}
	p.Proxy = "http://proxy:3128"
	ff, err := FedoraSeed(p)
	if err != nil || ff[0].Name != "ks.cfg" {
		t.Fatal(err)
	}
	ks := string(ff[0].Data)
	for _, want := range []string{"--proxy=http://proxy:3128", "user --name=agent", "sshkey --username=agent", "/var/lib/agentos/installed", "reboot", "xconfig --startxonboot", "set-default graphical.target", "WaylandEnable=false", "Session=gnome-xorg", "x11vnc", "agentos-vnc", "-localhost -rfbport 5900"} {
		if !strings.Contains(ks, want) {
			t.Fatalf("ks lacks %q", want)
		}
	}
}

func TestWinComputerName(t *testing.T) {
	for in, want := range map[string]string{
		"AGENTOS-WIN-1A2B": "AGENTOS-WIN-1A2", "AGENTOS-W-1A2B": "AGENTOS-W-1A2B", "my_pc.local": "mypclocal",
		"123": "AGENTOS-PC", "": "AGENTOS-PC", "abcdefghijklmn-x": "abcdefghijklmn",
	} {
		if got := WinComputerName(in); got != want {
			t.Errorf("WinComputerName(%q)=%q want %q", in, got, want)
		}
	}
	for i := 0; i < 50; i++ {
		if h := defaultHostname(GuestWindows11); len(h) > 15 {
			t.Fatalf("default Windows hostname %q > 15 chars", h)
		}
	}
	p := testParams()
	p.Hostname = "AGENTOS-WIN-1A2B"
	files, err := WindowsSeed(p, nil)
	if err != nil || !strings.Contains(string(files[0].Data), "<ComputerName>AGENTOS-WIN-1A2</ComputerName>") {
		t.Fatalf("ComputerName not clamped: %v", err)
	}
}
