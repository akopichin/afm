Domain: correctly wiring host uid/gid and HOME ordering when re-executing afm in Docker mode.
Audience: `cmd/afm/run.go`'s Docker-mode startup path.

## Passing host identity into ReExecConfig

```go
cfg := docker.ReExecConfig{
    Image:         cfg.Docker.GetImage(),
    ProjectDir:    projectDir,
    Commands:      docker.ScanCommands(f, clientCommand),
    DashboardPort: dashboardPort,
    ExtraMounts:   cfg.Docker.ExtraMounts,
    ExtraArgs:     os.Args[1:],
    ClientCommand: clientCommand,
}
if err := docker.ReExec(cfg); err != nil {
    return err
}
```

`ReExec` derives `AFM_HOST_UID`/`AFM_HOST_GID` from the current process (`os.Getuid`/`os.Getgid`) —
callers do not set these directly.

## Why HOME ordering matters

The container entrypoint drops root privileges via `gosu` to the host uid/gid before the afm process
starts. `gosu` resets `HOME` to `/` for a uid with no `/etc/passwd` entry — it does **not** preserve
whatever `HOME` was set to beforehand. Consequently the entrypoint script must set `HOME=/home/afm`
**after** invoking `gosu`, not before:

```sh
# correct: HOME set as part of the command gosu execs
gosu "$AFM_HOST_UID:$AFM_HOST_GID" env HOME=/home/afm afm ...

# incorrect: HOME set before gosu, gosu clobbers it back to "/"
export HOME=/home/afm
gosu "$AFM_HOST_UID:$AFM_HOST_GID" afm ...
```

Getting this ordering wrong makes agents look for `~/`-rooted files (auth tokens, skills, config)
under `/` instead of `/home/afm`, failing with "file not found" even though the volume mounts are
correct.

## Auth check before re-exec

Call `CheckClaudeDockerAuth(clientCommand)` before `ReExec` when `clientCommand == "claude"` — macOS
Keychain-backed OAuth sessions are invisible inside the Linux container, so a long-lived
`CLAUDE_CODE_OAUTH_TOKEN` (or `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`) must already be present in
the environment.
