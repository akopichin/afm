# Go SDK

Use the `afmsdk` Go module to connect a service to a running AFM dashboard, or
to launch and manage an `afm run` subprocess. For an existing dashboard on port
`9876`, the minimum read is:

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

See the [SDK README](https://github.com/akopichin/afm/blob/main/sdk/README.md)
for module setup, a complete `Connect` example, API details and checks. To
operate the same dashboard from a private Telegram chat, see the
[Telegram bot guide](telegram-bot.md).
