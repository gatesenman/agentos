package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/agentos/agentos/internal/api"
)

// Optional per-agent resource limits, read from the spec env (no contract change).
const (
	EnvLimitMemMB  = "AGENTOS_LIMIT_MEM_MB"  // memory limit of the whole tree in MiB
	EnvLimitCPUPct = "AGENTOS_LIMIT_CPU_PCT" // CPU cap in percent of one core (like ProcStats.CPUPercent)
)

type limits struct {
	memBytes uint64
	cpuPct   int
}

func (l limits) any() bool { return l.memBytes > 0 || l.cpuPct > 0 }

func parseLimits(env map[string]string) (limits, []string) {
	var l limits
	var warns []string
	if v := strings.TrimSpace(env[EnvLimitMemMB]); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			warns = append(warns, fmt.Sprintf("忽略无效的 %s / ignoring invalid %s=%q", EnvLimitMemMB, EnvLimitMemMB, v))
		} else {
			l.memBytes = n << 20
		}
	}
	if v := strings.TrimSpace(env[EnvLimitCPUPct]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			warns = append(warns, fmt.Sprintf("忽略无效的 %s / ignoring invalid %s=%q", EnvLimitCPUPct, EnvLimitCPUPct, v))
		} else {
			l.cpuPct = n
		}
	}
	return l, warns
}

// buildCmd prepares (but does not start) the command for spec: working
// directory, environment and executable resolution using the spec's PATH.
func (s *local) buildCmd(spec api.AgentSpec) (*exec.Cmd, error) {
	ws := s.workspace(spec.ID)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, fmt.Errorf("创建工作区失败 / cannot create workspace: %w", err)
	}
	dir, err := resolveWorkDir(ws, spec.WorkDir)
	if err != nil {
		return nil, err
	}
	env := os.Environ()
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return nil, fmt.Errorf("无效的环境变量名 / invalid env var name %q", k)
		}
		env = append(env, k+"="+spec.Env[k])
	}
	env = append(env,
		"AGENTOS_AGENT_ID="+spec.ID,
		"AGENTOS_AGENT_NAME="+spec.Name,
		"AGENTOS_WORKSPACE="+ws,
	)
	path, err := resolveCommand(spec.Command, dir, env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{
		Path: path,
		Args: append([]string{spec.Command}, spec.Args...),
		Dir:  dir,
		Env:  env, // exec de-duplicates: later entries win
	}
	return cmd, nil
}

// resolveWorkDir: empty => workspace; relative => must stay inside the
// workspace (created on demand); absolute => must exist.
func resolveWorkDir(ws, wd string) (string, error) {
	if wd == "" {
		return ws, nil
	}
	if filepath.IsAbs(wd) {
		fi, err := os.Stat(wd)
		if err != nil || !fi.IsDir() {
			return "", fmt.Errorf("工作目录不存在 / work_dir does not exist: %s", wd)
		}
		return wd, nil
	}
	if !filepath.IsLocal(wd) {
		return "", fmt.Errorf("工作目录必须位于工作区内 / relative work_dir must stay inside the workspace: %q", wd)
	}
	dir := filepath.Join(ws, wd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建工作目录失败 / cannot create work_dir: %w", err)
	}
	return dir, nil
}

// envLookup returns the last value of key in env (case-insensitive on Windows).
func envLookup(env []string, key string) string {
	val := ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			val = v
		}
	}
	return val
}

// resolveCommand finds the executable. Commands containing a path separator
// are taken relative to dir; bare names are searched in the PATH of env
// (i.e. the spec's PATH when it sets one), never in the current directory.
func resolveCommand(command, dir string, env []string) (string, error) {
	if strings.ContainsAny(command, `/\`) || filepath.IsAbs(command) {
		p := command
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if f, err := findExecutable(p, env); err == nil {
			return f, nil
		} else {
			return "", fmt.Errorf("无法执行 / cannot execute %q: %v", p, err)
		}
	}
	pathEnv := envLookup(env, "PATH")
	for _, d := range filepath.SplitList(pathEnv) {
		if d == "" || !filepath.IsAbs(d) {
			continue // relative PATH entries are ignored for safety
		}
		if f, err := findExecutable(filepath.Join(d, command), env); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("找不到命令 / command not found: %q (PATH=%s)", command, pathEnv)
}

func findExecutable(p string, env []string) (string, error) {
	if runtime.GOOS == "windows" {
		exts := []string{".com", ".exe", ".bat", ".cmd"}
		if pe := envLookup(env, "PATHEXT"); pe != "" {
			exts = nil
			for _, e := range strings.Split(strings.ToLower(pe), ";") {
				if e = strings.TrimSpace(e); e != "" {
					if e[0] != '.' {
						e = "." + e
					}
					exts = append(exts, e)
				}
			}
		}
		ext := strings.ToLower(filepath.Ext(p))
		for _, e := range exts {
			if ext == e {
				return p, checkFile(p)
			}
		}
		for _, e := range exts {
			if checkFile(p+e) == nil {
				return p + e, nil
			}
		}
		return "", fs.ErrNotExist
	}
	return p, checkFile(p)
}

func checkFile(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fs.ErrNotExist
		}
		return err
	}
	if fi.IsDir() {
		return errors.New("is a directory")
	}
	if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}
