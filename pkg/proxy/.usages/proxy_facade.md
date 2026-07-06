Domain: starting the built-in reverse proxy and wiring agents to use it. Audience: `cmd/afm`.

## Resolving upstream and starting the proxy

```go
if cfg.Proxy.IsEnabled() { // nil Enabled → enabled by default
    upstream := cfg.Proxy.Upstream
    if upstream == "" {
        upstream = os.Getenv("ANTHROPIC_BASE_URL")
    }
    if upstream == "" {
        // no-op: skip proxy entirely, log info
    } else {
        transforms := proxy.BuildTransforms(upstream, cfg.Proxy.Transforms.ZAI)
        p := proxy.New(upstream, transforms)
        addr, err := p.Start(cfg.Proxy.Port) // 0 = OS-assigned port
        if err != nil {
            return fmt.Errorf("start proxy: %w", err) // fatal
        }
        defer p.Shutdown(ctx)
    }
}
```

## Creating the shim so wrapper commands still route through the proxy

```go
shimDir, err := proxy.CreateShim(addr)
if err != nil {
    // non-fatal: log a warning, env-var injection still applies to non-wrapper commands
} else {
    defer os.RemoveAll(shimDir)
}
```

Pass both `addr` and `shimDir` into `orchestrator.Options.ProxyURL`/`ProxyShimDir` (`cmd/afm` never
touches `executor.Config` directly) — `pkg/orchestrator` forwards them to every internal
`executor.Config.ProxyURL`/`ProxyShimDir` it builds (see `pkg/executor`'s `runner_facade`), so every
spawned agent process routes through the proxy — including wrapper commands that clobber
`ANTHROPIC_BASE_URL` themselves, since the shim's `claude` script re-injects the proxy address via
`PATH` precedence.
