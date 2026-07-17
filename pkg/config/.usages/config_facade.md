Domain: loading and consuming the merged afm configuration. Audience: `pkg/orchestrator` and `cmd/afm`.

## Loading configuration

```go
cfg, err := config.LoadFrom(globalDir, projectDir)
if err != nil {
    return err
}
// missing files are silently ignored — defaults apply
```

Use `config.Default()` directly when no on-disk override is needed (e.g. tests).

## Reading optional fields safely

Several sub-configs use pointer fields with a getter that supplies the default — always prefer the
getter over reading the field directly:

```go
port := cfg.Server.GetPort()          // 9876 if Server.Port is nil
openBrowser := cfg.Server.IsOpenBrowser() // false if Server.OpenBrowser is nil (browser is opt-in)
theme := cfg.EffectiveTheme()         // normalized dashboard theme: "goga" or "novacorps" (empty/unknown → novacorps)
dockerEnabled := cfg.Docker.IsDockerEnabled() // checks AFM_USE_DOCKER if Docker.Enabled is nil
image := cfg.Docker.GetImage()        // AFM_DOCKER_IMAGE takes priority if set
```

## Consuming sub-configs

```go
clientCmd := cfg.Client.Command       // e.g. "claude"
extraArgs := cfg.Client.ExtraArgs
idleTimeout := cfg.Executor.IdleTimeout
maxParallel := cfg.Executor.MaxParallel
extraMounts := cfg.Docker.ExtraMounts // []string, no getter — read directly
```

`Config.PromptsDir` overrides the built-in prompts directory (see `assets.ReadPrompt`'s `overrideDir`
parameter) — pass it straight through when loading prompts.
