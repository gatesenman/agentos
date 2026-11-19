// Package docker implements the "docker" environment provider: each environment
// is one container managed through the Docker Engine API (plain net/http, no SDK).
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// DefaultImage is used when config.image is empty.
const DefaultImage = "ubuntu:24.04"

// DesktopImage is the default image when desktop=true. It is NOT on a public
// registry: when missing, Create builds it from the embedded
// images/desktop context (build.go). The tag is versioned so Dockerfile/entrypoint
// changes force a rebuild (old :latest fluxbox images are not reused).
// Manual equivalent:
//
//	docker build -t localhost/agentos-desktop:0.1.20 -t localhost/agentos-desktop:latest \
//	  internal/env/providers/docker/images/desktop
const DesktopImage = "localhost/agentos-desktop:0.1.20"

// DesktopImageAlias is also applied when building DesktopImage (convenience tag).
const DesktopImageAlias = "localhost/agentos-desktop:latest"

// DesktopDebianImage is Debian 13 (trixie) + XFCE + Plank. Separate tag so
// Ubuntu DesktopImage rebuilds are not forced when only the Debian variant changes.
const DesktopDebianImage = "localhost/agentos-debian-desktop:0.1.22"

// DesktopDebianImageAlias is also applied when building DesktopDebianImage.
const DesktopDebianImageAlias = "localhost/agentos-debian-desktop:latest"

// Label keys put on every managed container.
const (
	LabelEnv     = "agentos.env"
	LabelManaged = "agentos.managed"
)

// Config is the provider config (Record.Config). ContainerID/VNCPort are written by Create.
type Config struct {
	Host           string            `json:"host,omitempty"`
	Image          string            `json:"image,omitempty"`
	CPUs           float64           `json:"cpus,omitempty"`
	MemoryMB       int64             `json:"memory_mb,omitempty"`
	Volumes        []string          `json:"volumes,omitempty"` // "hostpath:/container/path[:ro]" or "volname:/path"
	Env            map[string]string `json:"env,omitempty"`
	Ports          []string          `json:"ports,omitempty"` // "8080:80", "127.0.0.1:8080:80", "80" (random host port), optional "/udp"
	GPU            bool              `json:"gpu,omitempty"`
	Desktop        bool              `json:"desktop,omitempty"`
	VirtualDesktop bool              `json:"virtual_desktop,omitempty"` // after linux-desktop stack: start Xvfb+x11vnc on demand
	Shell          string            `json:"shell,omitempty"`           // default exec command, default /bin/sh -l

	ContainerID string `json:"container_id,omitempty"`
	VNCPort     int    `json:"vnc_port,omitempty"` // host port published for 5900 (desktop only)
}

const schema = `{
  "type": "object",
  "properties": {
    "host":      {"x-advanced": true, "type": "string", "title": "Docker 地址", "default": "", "description": "留空使用本机 Docker（unix:///var/run/docker.sock 或 npipe:////./pipe/docker_engine）；远程示例 tcp://192.168.1.10:2375"},
    "image":     {"type": "string", "title": "镜像", "default": "ubuntu:24.04", "description": "本地不存在时自动拉取"},
    "cpus":      {"type": "number", "title": "CPU 核数", "minimum": 0, "default": 2, "description": "0 表示不限制"},
    "memory_mb": {"type": "integer", "title": "内存上限 (MB)", "minimum": 0, "default": 4096, "description": "0 表示不限制"},
    "volumes":   {"x-advanced": true, "type": "array", "title": "挂载卷", "default": [], "items": {"type": "string"}, "description": "主机路径:容器路径[:ro]"},
    "env":       {"x-advanced": true, "type": "object", "title": "环境变量", "default": {}, "additionalProperties": {"type": "string"}},
    "ports":     {"x-advanced": true, "type": "array", "title": "端口映射", "default": [], "items": {"type": "string"}, "description": "主机端口:容器端口，例如 8080:80"},
    "gpu":       {"x-advanced": true, "type": "boolean", "title": "使用 GPU", "default": false, "description": "需要 NVIDIA Container Toolkit"},
    "desktop":   {"type": "boolean", "title": "图形桌面", "default": false, "description": "使用带 XFCE 图形桌面（Xvfb + x11vnc）的镜像，通过 VNC 远程桌面访问（默认 Ubuntu 镜像 localhost/agentos-desktop:0.1.20；亦可设 image 为 localhost/agentos-debian-desktop:0.1.22，首次使用时自动构建）"},
    "virtual_desktop": {"x-advanced": true, "type": "boolean", "title": "按需虚拟桌面", "default": false, "description": "在普通（无桌面）镜像上安装 linux-desktop 技术栈后自动启用：打开桌面页签时启动 Xvfb + 窗口管理器 + x11vnc"},
    "shell":     {"x-advanced": true, "type": "string", "title": "默认终端命令", "default": "/bin/sh -l"},
    "container_id": {"type": "string", "title": "容器 ID", "readOnly": true},
    "vnc_port":  {"type": "integer", "title": "VNC 映射端口", "readOnly": true}
  }
}`

type provider struct{}

func init() { env.Register(provider{}) }

// New returns the provider (also registered as "docker" in init).
func New() env.Provider { return provider{} }

// ProvisionTimeout implements env.ProvisionTimeouter: first-time desktop
// image build (apt install XFCE) commonly exceeds the server default 10m,
// especially behind CN mirrors / slow links.
func (provider) ProvisionTimeout(rec *env.Record) time.Duration {
	var c Config
	_ = json.Unmarshal(rec.Config, &c)
	if c.Desktop || c.Image == DesktopImage || c.Image == DesktopDebianImage ||
		strings.HasPrefix(c.Image, "localhost/agentos-desktop") ||
		strings.HasPrefix(c.Image, "localhost/agentos-debian-desktop") {
		return 45 * time.Minute
	}
	return 15 * time.Minute
}

func (provider) Info() env.ProviderInfo {
	return env.ProviderInfo{
		Type:        "docker",
		Name:        "Docker 容器",
		Kind:        env.KindDocker,
		Group:       "容器",
		Description: "在本机或远程 Docker 中创建隔离容器作为智能体运行环境，可选图形桌面（VNC）。",
		Capabilities: []env.Capability{env.CapLifecycle, env.CapExec, env.CapFiles,
			env.CapDesktopVNC, env.CapMetrics, env.CapInstallAgent},
		ConfigSchema: json.RawMessage(schema),
	}
}

func parseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("docker: 配置格式错误: %w", err)
		}
	}
	if c.Image == "" {
		if c.Desktop {
			c.Image = DesktopImage
		} else {
			c.Image = DefaultImage
		}
	}
	return c, nil
}

func (provider) Validate(raw json.RawMessage) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	return c.validate()
}

func (c Config) validate() error {
	if c.CPUs < 0 || c.MemoryMB < 0 {
		return errors.New("docker: cpus / memory_mb 不能为负数")
	}
	if c.MemoryMB > 0 && c.MemoryMB < 6 {
		return errors.New("docker: memory_mb 至少为 6")
	}
	for _, v := range c.Volumes {
		if p := strings.Split(v, ":"); len(p) < 2 {
			return fmt.Errorf("docker: 挂载卷格式错误 %q（应为 主机路径:容器路径[:ro]）", v)
		}
	}
	for _, p := range c.Ports {
		if _, _, _, _, err := parsePort(p); err != nil {
			return err
		}
	}
	for k := range c.Env {
		if k == "" || strings.Contains(k, "=") {
			return fmt.Errorf("docker: 环境变量名无效 %q", k)
		}
	}
	if _, err := newClient(c.Host); err != nil {
		return err
	}
	return nil
}

// parsePort parses "[ip:][hostPort:]containerPort[/proto]".
func parsePort(s string) (ip, hostPort, ctrPort, proto string, err error) {
	proto = "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s, proto = s[:i], s[i+1:]
		if proto != "tcp" && proto != "udp" && proto != "sctp" {
			return "", "", "", "", fmt.Errorf("docker: 端口协议无效 %q", proto)
		}
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		ctrPort = parts[0]
	case 2:
		hostPort, ctrPort = parts[0], parts[1]
	case 3:
		ip, hostPort, ctrPort = parts[0], parts[1], parts[2]
	default:
		return "", "", "", "", fmt.Errorf("docker: 端口映射格式错误 %q", s)
	}
	if ctrPort == "" {
		return "", "", "", "", fmt.Errorf("docker: 端口映射格式错误 %q", s)
	}
	for _, p := range []string{hostPort, ctrPort} {
		if p != "" {
			if n, e := strconv.Atoi(p); e != nil || n <= 0 || n > 65535 {
				return "", "", "", "", fmt.Errorf("docker: 端口无效 %q", s)
			}
		}
	}
	return ip, hostPort, ctrPort, proto, nil
}

func (provider) Test(ctx context.Context, raw json.RawMessage) error {
	c, err := parseConfig(raw)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	cl, _ := newClient(c.Host)
	defer cl.close()
	resp, err := cl.do(ctx, "GET", "/_ping", nil, nil, "")
	if err != nil {
		return fmt.Errorf("无法连接 Docker: %w", err)
	}
	resp.Body.Close()
	return nil
}

func splitImage(ref string) (name, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref, ""
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// ensureImage pulls ref when it is not present locally.
func ensureImage(ctx context.Context, cl *client, ref string) error {
	err := cl.json(ctx, "GET", "/images/"+ref+"/json", nil, nil, nil)
	if err == nil {
		return nil
	}
	if !isStatus(err, 404) {
		return err
	}
	if ref == DesktopImage { // not on any registry: build from embedded Ubuntu XFCE Dockerfile
		return buildDesktopImage(ctx, cl)
	}
	if ref == DesktopDebianImage { // Debian 13 + XFCE + Plank
		return buildDesktopDebianImage(ctx, cl)
	}
	name, tag := splitImage(ref)
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	env.Logf(ctx, "info", "拉取镜像 / pulling image %s", ref)
	resp, err := cl.do(ctx, "POST", "/images/create", q, nil, "")
	if err != nil {
		return fmt.Errorf("拉取镜像 %s 失败: %w", ref, err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	last := ""
	for {
		var m struct {
			Error  string `json:"error"`
			Status string `json:"status"`
			ID     string `json:"id"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				env.Logf(ctx, "info", "镜像就绪 / image ready: %s", ref)
				return nil
			}
			return fmt.Errorf("拉取镜像 %s 失败: %w", ref, err)
		}
		if m.Error != "" {
			return fmt.Errorf("拉取镜像 %s 失败: %s", ref, m.Error)
		}
		// per-layer milestones only (not the byte-progress stream)
		if m.Status != "" && m.Status != "Downloading" && m.Status != "Extracting" && m.Status != "Waiting" {
			line := m.Status
			if m.ID != "" {
				line = m.ID + ": " + m.Status
			}
			if line != last {
				env.Logf(ctx, "info", "%s", line)
				last = line
			}
		}
	}
}

func containerName(id string) string {
	n := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, id)
	return "agentos-" + n
}

func (c Config) createBody(id string, cl *client) map[string]any {
	envs := []string{}
	for k, v := range c.Env {
		envs = append(envs, k+"="+v)
	}
	exposed := map[string]any{}
	bindings := map[string][]map[string]string{}
	for _, p := range c.Ports {
		ip, hp, cp, proto, _ := parsePort(p)
		key := cp + "/" + proto
		exposed[key] = struct{}{}
		bindings[key] = append(bindings[key], map[string]string{"HostIp": ip, "HostPort": hp})
	}
	if c.Desktop {
		exposed["5900/tcp"] = struct{}{}
		// never expose the unauthenticated VNC beyond the daemon host's loopback;
		// for a remote (tcp://) daemon Desktop relays through docker exec.
		ip := "127.0.0.1"
		bindings["5900/tcp"] = []map[string]string{{"HostIp": ip, "HostPort": ""}}
	}
	hc := map[string]any{
		"Binds":         c.Volumes,
		"PortBindings":  bindings,
		"Init":          true,
		"RestartPolicy": map[string]any{"Name": "unless-stopped"},
	}
	if c.CPUs > 0 {
		hc["NanoCpus"] = int64(c.CPUs * 1e9)
	}
	if c.MemoryMB > 0 {
		hc["Memory"] = c.MemoryMB << 20
	}
	if c.GPU {
		hc["DeviceRequests"] = []map[string]any{{"Driver": "", "Count": -1, "Capabilities": [][]string{{"gpu"}}}}
	}
	body := map[string]any{
		"Image":        c.Image,
		"Env":          envs,
		"Labels":       map[string]string{LabelEnv: id, LabelManaged: "1"},
		"ExposedPorts": exposed,
		"HostConfig":   hc,
		"Tty":          false,
		"OpenStdin":    false,
	}
	if !c.Desktop {
		// Keep a plain image alive regardless of its default CMD; exits promptly on SIGTERM.
		body["Entrypoint"] = []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"}
		body["Cmd"] = []string{}
	}
	return body
}

// Create pulls the image if needed, creates and starts the container, and stores
// container_id (and vnc_port for desktop) back into rec.Config.
func (provider) Create(ctx context.Context, rec *env.Record) error {
	c, err := parseConfig(rec.Config)
	if err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	cl, _ := newClient(c.Host)
	defer cl.close()
	if err := ensureImage(ctx, cl, c.Image); err != nil {
		return err
	}
	name := containerName(rec.ID)
	env.Logf(ctx, "info", "创建容器 / creating container %s (%s)", name, c.Image)
	var created struct{ Id string }
	err = cl.json(ctx, "POST", "/containers/create", url.Values{"name": {name}}, c.createBody(rec.ID, cl), &created)
	if isStatus(err, 409) { // left over from an interrupted Create of the same env?
		var ins inspect
		if e := cl.json(ctx, "GET", "/containers/"+name+"/json", nil, nil, &ins); e == nil && ins.Config.Labels[LabelEnv] == rec.ID {
			created.Id, err = ins.ID, nil
		}
	}
	if err != nil {
		return fmt.Errorf("创建容器失败: %w", err)
	}
	c.ContainerID = created.Id
	if err := cl.json(ctx, "POST", "/containers/"+created.Id+"/start", nil, nil, nil); err != nil {
		cl.json(context.Background(), "DELETE", "/containers/"+created.Id, url.Values{"force": {"1"}}, nil, nil)
		return fmt.Errorf("启动容器失败: %w", err)
	}
	if c.Desktop {
		var ins inspect
		if err := cl.json(ctx, "GET", "/containers/"+created.Id+"/json", nil, nil, &ins); err == nil {
			c.VNCPort = ins.hostPort("5900/tcp")
		}
	}
	raw, err := mergeConfig(rec.Config, map[string]any{"container_id": c.ContainerID, "vnc_port": c.VNCPort})
	if err != nil {
		return err
	}
	rec.Config = raw
	return nil
}

// mergeConfig sets keys on the raw JSON object, preserving unknown fields.
func mergeConfig(raw json.RawMessage, set map[string]any) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	for k, v := range set {
		if v == nil || v == 0 || v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return json.Marshal(m)
}

func (provider) Open(ctx context.Context, rec *env.Record) (env.Env, error) {
	c, err := parseConfig(rec.Config)
	if err != nil {
		return nil, err
	}
	cl, err := newClient(c.Host)
	if err != nil {
		return nil, err
	}
	id := c.ContainerID
	if id == "" {
		id = containerName(rec.ID)
	}
	return &dockerEnv{cl: cl, cfg: c, id: id}, nil
}
