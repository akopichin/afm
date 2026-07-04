Domain: launching and monitoring AI agent processes. Audience: `pkg/orchestrator`, `pkg/server`.

## Constructing and running

```go
exec := executor.New(executor.Config{
    Command:      cfg.Client.Command,
    ExtraArgs:    cfg.Client.ExtraArgs,
    IdleTimeout:  cfg.Executor.IdleTimeout,
    StageDir:     stageDir,      // enables AFM_STAGE_DIR file-based dialog protocol
    ProxyURL:     proxyURL,      // optional: injects ANTHROPIC_BASE_URL / AFM_PROXY_URL
    ProxyShimDir: proxyShimDir,  // optional: prepended to PATH
})

err := exec.RunAgent(ctx, agentType, stageName, prompt, logFile)
// or, for the planning phase specifically:
err := exec.RunPlanning(ctx, stageName, prompt, outFile, logFile)
```

`Runner` is an interface — tests substitute a fake implementation; production code always gets a real
`Executor` unless `Options.Runner` is explicitly overridden (see `pkg/orchestrator`).

## Reading back what the agent did

```go
paths := executor.WrittenFiles(logFile + ".jsonl")       // Write-tool file paths, in event order
items := executor.DialogTranscript(logFile + ".jsonl")   // assistant text + ask_user calls
for _, item := range items {
    if item.AskUserID != "" {
        // agent invoked ask_user with this ID
    } else {
        // item.Text is an assistant message
    }
}
```

Both readers tolerate a missing or unreadable log file — they return an empty slice rather than an
error, so callers do not need existence checks before calling them.

## Building CLI args

```go
args := executor.ResolveArgs(cfg.Client.ExtraArgs) // DefaultClaudeArgs() + extra, deduplicated
```

Always prefer `ResolveArgs` for interactive stages — `--verbose` must be present exactly once for
Claude Code's stream-json output to include `tool_use` events.
