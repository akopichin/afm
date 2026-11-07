# Go SDK

Use the `afmsdk` Go module to connect a service to a running AFM dashboard, or
to launch and manage an `afm run` subprocess. For an existing dashboard on port
`9876`, the minimum read is (this also works against `afm run --mode core`,
see [Core mode](core-mode.md)):

```go
run, err := afmsdk.Connect(ctx, 9876)
if err != nil {
	return err
}
status, err := run.Status(ctx)
if err != nil {
	return err
}
fmt.Println(status.FlowName, status.RunState)
```

`Connect` uses the local loopback address and does not own the AFM process.
`Run.Status` includes stage views, capabilities, run identity and state. The
SDK also reads plans, dialog, discussion and Agent Mesh conversations; answers
questions, sends messages, attaches profile-backed agents, and controls stages
with approve, retry, revise, pause and continue.

For a run your service creates, `Client.Start(ctx, flowPath, workDir)` starts an
`afm` subprocess and waits for its dashboard. The SDK uses `workDir` both as
the subprocess working directory and its `--dir`. `Run.Cleanup()` deletes that
whole directory after the process exits, so supply a dedicated temporary
directory if you intend to call it. Dashboard HTTP errors expose a status code
through `*afmsdk.HTTPError`.

## Core mode

Set `Config.Mode` to `afmsdk.ModeCore` to start `afm run --mode core`: the API
without the Web UI (see [Core mode](core-mode.md)). `Start` returns only after
`/api/runtime` reports `core`, protocol 1 and `ready: true`; a binary that does
not know `--mode` makes `Start` fail with its error instead of falling back to
the dashboard. `WaitResult` returns the workflow's final status, and `Shutdown`
stops the process this client started:

```go
client, err := afmsdk.New(afmsdk.Config{Mode: afmsdk.ModeCore})
if err != nil {
	return err
}
run, err := client.Start(ctx, flowPath, workDir)
if err != nil {
	return err
}
defer func() {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = run.Shutdown(stopCtx)
}()
status, err := run.WaitResult(ctx)
if err != nil {
	return err
}
fmt.Println(status.RunState) // finished, failed or interrupted
```

Core keeps running after the result, so `Run.Wait` does not return until
`Shutdown`. Details and errors are in the SDK README.

See the [SDK README](https://github.com/akopichin/afm/blob/main/sdk/README.md)
for module setup, a complete `Connect` example, API details and checks. To
operate the same dashboard from a private Telegram chat, see the
[Telegram bot guide](telegram-bot.md).
