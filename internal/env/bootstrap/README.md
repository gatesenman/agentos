# internal/env/bootstrap — install an Agent OS node into an environment

```go
import "github.com/agentos/agentos/internal/env/bootstrap"

res, err := bootstrap.InstallAgentOS(ctx, e /* env.Env */, bootstrap.Options{
    ServerURL: "http://172.17.0.1:7800", // as reachable FROM the env
    JoinToken: tok,                      // node token
    NodeName:  rec.Name,
    NodeID:    "",                       // optional: pre-seed node id -> set rec.NodeID before it connects
    DistDir:   "/opt/agentos/dist",      // agentos-<os>-<arch>[.exe] (scripts/build.sh layout)
    DownloadBase: "",                    // or https://host/dist (server downloads, then uploads)
    OSHint:    "",                       // "", linux, darwin, windows
    Logf:      func(f string, a ...any) { /* env.log event */ },
})
// res: {OS, Arch, InstallDir, BinaryPath, LogPath, PID, NodeID, Method}
```

The API layer should depend on the interface:

```go
type Installer interface {
    Install(ctx context.Context, e env.Env, opts Options) (*Result, error)
}
var inst bootstrap.Installer = bootstrap.DefaultInstaller{}
```

Works with any `env.Env` implementing `Exec` + `PutFile` (+ `GetFile` to read the node id back),
i.e. the `docker` and `ssh` providers.

## What it does
1. Detects OS/arch with `uname -sm` (fallback: Windows `cmd /c ver`).
2. Picks the binary: `DistDir/agentos-<os>-<arch>[.exe]`, else `DownloadBase/<same name>`,
   else the running executable if the target matches the server's own GOOS/GOARCH.
3. Stops a previous node installed in the same dir (idempotent re-install).
4. Uploads to `InstallDir` (default `$HOME/.agentos` / `%LOCALAPPDATA%\AgentOS`):
   `agentos[.exe]`, `run-node.sh|.cmd` (0700, holds `AGENTOS_TOKEN` so the token is not in argv),
   optional `data/node-id`.
5. Starts `agentos node --server URL --name NAME --data <dir>/data` detached:
   * Linux/macOS: `systemd-run --unit agentos-node -p Restart=always` when root on a systemd host,
     otherwise `setsid nohup` (or plain `nohup`). Log: `<dir>/node.log`, pid: `<dir>/node.pid`.
   * Windows: `Invoke-CimMethod Win32_Process Create` (escapes the OpenSSH session job object).
6. Returns once the process stayed alive for `Options.Wait` (default 2s) and `data/node-id` was read.
   It does NOT wait for the WS hello; the caller binds `Result.NodeID` and waits for the node
   to come online via the hub. On early exit, the error carries the last 20 log lines.

Not persistent across reboots of the target unless systemd-run was used (Windows: add schtasks later).

## Desktop prerequisites

`install-agentos` installs only the `agentos` binary. For a controllable desktop inside the env:

- docker: create the env with `desktop: true` (image `localhost/agentos-desktop`, built automatically; contains
  Xvfb + XFCE + x11vnc + xdotool). The bootstrapped node uses that container's VNC (127.0.0.1:5900) and `DISPLAY=:0`.
- ssh / other Linux hosts: install `xvfb x11vnc fluxbox xterm xdotool` (Debian/Ubuntu `apt install ...`), then either set
  `virtual_desktop: true` on the ssh env (desktop started on demand for the ssh user) or run the node with
  `agentos node --desktop virtual` (own Xvfb + WM + x11vnc; also via `install.sh --desktop virtual`).
