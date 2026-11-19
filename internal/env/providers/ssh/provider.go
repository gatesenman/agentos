// Package ssh implements the "ssh" environment provider: an existing machine
// (Linux, macOS or Windows with OpenSSH) reached over SSH. Nothing is provisioned;
// exec uses SSH sessions (with pty), files use SFTP, VNC uses direct-tcpip.
package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
	"golang.org/x/crypto/ssh"
)

// Host key policies.
const (
	PolicyTOFU     = "tofu"     // default: accept the first key, then require it (fingerprint stored in host_key)
	PolicyStrict   = "strict"   // host_key must be configured and match
	PolicyInsecure = "insecure" // accept any key (testing only)
)

// Config is Record.Config. HostKey and OSDetected are written back by Create (and Open, for TOFU).
type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`    // x-secret
	PrivateKey string `json:"private_key,omitempty"` // x-secret, PEM / OpenSSH format
	// PrivateKeyPath: a private key file on the MANAGEMENT host (e.g. ~/.ssh/id_ed25519),
	// read at connect time when private_key is empty. Lets "本机 SSH" work with zero input.
	PrivateKeyPath string `json:"private_key_path,omitempty"`
	Passphrase     string `json:"passphrase,omitempty"` // x-secret
	HostKeyPolicy  string `json:"host_key_policy,omitempty"`
	HostKey        string `json:"host_key,omitempty"` // "SHA256:..." fingerprint
	OS             string `json:"os,omitempty"`       // auto | linux | darwin | windows
	OSDetected     string `json:"os_detected,omitempty"`
	StartCommand   string `json:"start_command,omitempty"` // runs on the MANAGEMENT host (e.g. wakeonlan MAC, VBoxManage startvm)
	StopCommand    string `json:"stop_command,omitempty"`  // runs on the REMOTE host over ssh (e.g. sudo shutdown -h now)
	VNCPort        int    `json:"vnc_port,omitempty"`      // remote VNC port on 127.0.0.1, default 5900
	// VirtualDesktop: when nothing listens on vnc_port, start a virtual desktop
	// for the ssh user (Xvfb + window manager + xterm + x11vnc -localhost) on
	// the remote host before relaying (Linux/BSD remotes with xvfb + x11vnc).
	VirtualDesktop bool   `json:"virtual_desktop,omitempty"`
	Shell          string `json:"shell,omitempty"` // optional default exec command
}

const schemaTmpl = `{
  "type": "object",
  "required": ["host", "user"],
  "properties": {
    "host":        {"type": "string", "title": "主机地址", "description": "IP 或域名", "default": "127.0.0.1"},
    "port":        {"type": "integer", "title": "端口", "default": 22, "minimum": 1, "maximum": 65535},
    "user":        {"type": "string", "title": "用户名", "default": %s},
    "password":    {"type": "string", "title": "密码", "x-secret": true, "format": "password"},
    "private_key": {"x-advanced": true, "type": "string", "title": "私钥", "x-secret": true, "format": "textarea", "description": "OpenSSH / PEM 格式私钥，与密码二选一"},
    "private_key_path": {"type": "string", "title": "私钥文件（管理端本机）", "default": %s, "description": "管理端本机上的私钥文件路径（如 ~/.ssh/id_ed25519）；未填写私钥/密码时使用"},
    "passphrase":  {"x-advanced": true, "type": "string", "title": "私钥口令", "x-secret": true, "format": "password"},
    "host_key_policy": {"x-advanced": true, "type": "string", "title": "主机密钥策略", "enum": ["tofu", "strict", "insecure"], "default": "tofu",
                    "description": "tofu: 首次连接记录指纹，之后必须一致；strict: 必须预先填写指纹；insecure: 不校验（仅测试）"},
    "host_key":    {"x-advanced": true, "type": "string", "title": "主机密钥指纹", "default": "", "description": "SHA256:...，首次连接自动记录"},
    "os":          {"x-advanced": true, "type": "string", "title": "操作系统", "enum": ["auto", "linux", "darwin", "windows"], "default": "auto"},
    "os_detected": {"type": "string", "title": "检测到的系统", "readOnly": true},
    "start_command": {"x-advanced": true, "type": "string", "title": "开机命令（在管理端执行）", "default": "", "description": "可选，例如 wakeonlan AA:BB:CC:DD:EE:FF"},
    "stop_command":  {"x-advanced": true, "type": "string", "title": "关机命令（在远程主机执行）", "default": "", "description": "可选，例如 sudo shutdown -h now"},
    "vnc_port":    {"x-advanced": true, "type": "integer", "title": "远程 VNC 端口", "default": 5900, "description": "远程主机 127.0.0.1 上的 VNC 服务，通过 SSH 隧道访问"},
    "virtual_desktop": {"x-advanced": true, "type": "boolean", "title": "虚拟桌面", "default": false, "description": "远程 VNC 端口无服务时，自动为该用户启动虚拟桌面（Xvfb + 窗口管理器 + x11vnc，仅监听远程 127.0.0.1；远程需安装 xvfb x11vnc fluxbox xterm）"},
    "shell":       {"x-advanced": true, "type": "string", "title": "默认终端命令", "default": "", "description": "留空使用登录 shell"}
  }
}`

type provider struct{}

func init() { env.Register(provider{}) }

// New returns the provider (also registered as "ssh" in init).
func New() env.Provider { return provider{} }

func (provider) Info() env.ProviderInfo {
	return env.ProviderInfo{
		Type:        "ssh",
		Name:        "SSH 远程主机",
		Kind:        env.KindVM,
		Group:       "虚拟机",
		Description: "通过 SSH 接入已有的 Linux / macOS / Windows 主机或虚拟机，支持终端、文件（SFTP）、VNC 远程桌面（SSH 隧道）和性能监控。",
		Capabilities: []env.Capability{env.CapLifecycle, env.CapExec, env.CapFiles,
			env.CapDesktopVNC, env.CapMetrics, env.CapInstallAgent},
		ConfigSchema: schema(),
	}
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("ssh: 配置格式错误: %w", err)
		}
	}
	if c.Port == 0 {
		c.Port = 22
	}
	switch c.HostKeyPolicy { // OpenSSH-style aliases
	case "accept-new", "accept_new":
		c.HostKeyPolicy = PolicyTOFU
	case "no", "off":
		c.HostKeyPolicy = PolicyInsecure
	case "yes":
		c.HostKeyPolicy = PolicyStrict
	}
	if c.HostKeyPolicy == "" {
		c.HostKeyPolicy = PolicyTOFU
	}
	if c.VNCPort == 0 {
		c.VNCPort = 5900
	}
	if c.OS == "" {
		c.OS = "auto"
	}
	if c.PrivateKey == "" && c.PrivateKeyPath != "" {
		if b, err := os.ReadFile(expandHome(c.PrivateKeyPath)); err == nil {
			c.PrivateKey = string(b) // in memory only; never written back to the record
		}
	}
	return c, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// CurrentUser is the management host's OS user name (domain stripped), used as
// the default ssh user for "本机 SSH".
func CurrentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		n := u.Username
		if i := strings.LastIndexAny(n, `\`); i >= 0 {
			n = n[i+1:]
		}
		return n
	}
	for _, k := range []string{"USER", "USERNAME", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// DefaultKeyPath returns the first existing ~/.ssh/id_ed25519|id_ecdsa|id_rsa ("" if none).
func DefaultKeyPath() string {
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		p := filepath.Join("~", ".ssh", n)
		if st, err := os.Stat(expandHome(p)); err == nil && !st.IsDir() {
			return "~/.ssh/" + n
		}
	}
	return ""
}

func schema() json.RawMessage {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return json.RawMessage(fmt.Sprintf(schemaTmpl, q(CurrentUser()), q(DefaultKeyPath())))
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("ssh: 请填写主机地址")
	}
	if c.User == "" {
		return errors.New("ssh: 请填写用户名")
	}
	if c.Port < 1 || c.Port > 65535 || c.VNCPort < 1 || c.VNCPort > 65535 {
		return errors.New("ssh: 端口无效")
	}
	if c.Password == "" && c.PrivateKey == "" {
		if c.PrivateKeyPath != "" {
			return fmt.Errorf("ssh: 无法读取私钥文件 %s", c.PrivateKeyPath)
		}
		return errors.New("ssh: 请填写密码、私钥或私钥文件")
	}
	if c.PrivateKey != "" {
		if _, err := c.signer(); err != nil {
			return err
		}
	}
	switch c.HostKeyPolicy {
	case PolicyTOFU, PolicyInsecure:
	case PolicyStrict:
		if c.HostKey == "" {
			return errors.New("ssh: strict 策略需要填写主机密钥指纹")
		}
	default:
		return fmt.Errorf("ssh: 未知主机密钥策略 %q", c.HostKeyPolicy)
	}
	switch c.OS {
	case "auto", "linux", "darwin", "windows":
	default:
		return fmt.Errorf("ssh: 未知操作系统 %q", c.OS)
	}
	return nil
}

func (c Config) signer() (ssh.Signer, error) {
	var s ssh.Signer
	var err error
	if c.Passphrase != "" {
		s, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
	} else {
		s, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
	}
	if err != nil {
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return nil, errors.New("ssh: 私钥已加密，请填写私钥口令")
		}
		return nil, fmt.Errorf("ssh: 私钥无效: %w", err)
	}
	return s, nil
}

func (c Config) addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// clientConfig builds the ssh client config; seen receives the server key fingerprint.
func (c Config) clientConfig(seen *string) (*ssh.ClientConfig, error) {
	var auths []ssh.AuthMethod
	if c.PrivateKey != "" {
		s, err := c.signer()
		if err != nil {
			return nil, err
		}
		auths = append(auths, ssh.PublicKeys(s))
	}
	if c.Password != "" {
		pw := c.Password
		auths = append(auths, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			a := make([]string, len(qs))
			for i := range a {
				a[i] = pw
			}
			return a, nil
		}))
	}
	return &ssh.ClientConfig{
		User: c.User,
		Auth: auths,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if seen != nil {
				*seen = fp
			}
			if c.HostKeyPolicy == PolicyInsecure || (c.HostKeyPolicy == PolicyTOFU && c.HostKey == "") {
				return nil
			}
			if fp != c.HostKey {
				return fmt.Errorf("ssh: 主机密钥不匹配（期望 %s，实际 %s），可能存在中间人攻击", c.HostKey, fp)
			}
			return nil
		},
		Timeout: 15 * time.Second,
	}, nil
}

// dial connects and authenticates; returns the client and the host key fingerprint.
func (c Config) dial(ctx context.Context) (*ssh.Client, string, error) {
	var fp string
	cc, err := c.clientConfig(&fp)
	if err != nil {
		return nil, "", err
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return nil, "", fmt.Errorf("ssh: 无法连接 %s: %w", c.addr(), err)
	}
	dl := time.Now().Add(20 * time.Second)
	if t, ok := ctx.Deadline(); ok && t.Before(dl) {
		dl = t
	}
	conn.SetDeadline(dl)
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	sc, chans, reqs, err := ssh.NewClientConn(conn, c.addr(), cc)
	stop()
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, fp, fmt.Errorf("ssh: 认证失败（用户名/密码/私钥错误）: %w", err)
		}
		return nil, fp, fmt.Errorf("ssh: 握手失败: %w", err)
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), fp, nil
}

func (provider) Validate(raw json.RawMessage) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	return c.validate()
}

func (provider) Test(ctx context.Context, raw json.RawMessage) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	cl, _, err := c.dial(ctx)
	if err != nil {
		return err
	}
	return cl.Close()
}

// Create only validates connectivity (no provisioning); it records the host key
// fingerprint (TOFU) and the detected OS into rec.Config.
func (provider) Create(ctx context.Context, rec *env.Record) error {
	c, err := parseConfig(rec.Config)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	cl, fp, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cl.Close()
	set := map[string]any{"host_key": fp}
	if osName, _, err := detectOS(ctx, cl); err == nil {
		set["os_detected"] = osName
	}
	raw, err := mergeConfig(rec.Config, set)
	if err != nil {
		return err
	}
	rec.Config = raw
	return nil
}

// mergeConfig sets keys on the raw JSON object, preserving other fields (incl. secrets).
func mergeConfig(raw json.RawMessage, set map[string]any) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	for k, v := range set {
		if v == "" {
			continue
		}
		m[k] = v
	}
	return json.Marshal(m)
}

// Open returns a lazily-connecting handle. Create normally stored host_key already;
// if it is still empty under TOFU, the first connection's key is pinned in memory
// for the life of the handle (not written back, to avoid racing the caller on rec).
func (provider) Open(ctx context.Context, rec *env.Record) (env.Env, error) {
	c, err := parseConfig(rec.Config)
	if err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &sshEnv{cfg: c}, nil
}
