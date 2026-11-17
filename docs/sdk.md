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

### Initial task and instance id

`Client.StartWithOptions(ctx, flowPath, workDir, afmsdk.StartOptions{Task: "..."})` starts the
run with an [initial task](core-mode.md#initial-task). The SDK passes it as `--task @-` on stdin,
so the text is not in the process list. A task always starts a new run. `Start` is the same
call with no options.

`Run.InstanceID()` returns the id that the run's `/api/runtime` reports, the same one as in its
`~/.afm/active-runs` record, so a caller can tell this process from another that later listens on
the same port. It is empty for an `afm` that predates the field.

## Conditional approval

`Run.Approve` approves whatever plan is waiting. To approve only a plan the caller
has read, use `ApprovalSnapshot` and `ApproveExpected`
(`GET`/`POST /api/stages/<id>/approval`):

```go
snap, err := run.ApprovalSnapshot(ctx, "plan")
if err != nil {
	return err
}
// Show snap.PlanMarkdown to a reviewer, then:
_, err = run.ApproveExpected(ctx, "plan", afmsdk.ApproveExpectedRequest{
	RunID:            snap.RunID,
	ExpectedRevision: snap.Revision,
})
var refused *afmsdk.ApprovalError
if errors.As(err, &refused) && refused.Code == afmsdk.ApprovalCodeStale {
	// The plan was revised after it was read: read it again.
}
```

Refusals are `*afmsdk.ApprovalError` with `ApprovalCodeStale`,
`ApprovalCodeRunChanged` or `ApprovalCodeUnavailable`. The same operation backs
the [MCP](mcp.md#approving-a-plan) approve tool.

## Budgets

`Budgets`, `Budget`, `UpdateBudget` and `ResumeBudget` read and change the
[spending limits](budgets.md) of a run, in dashboard and core mode; see the
[README](https://github.com/akopichin/afm/blob/main/sdk/README.md#budgets).

See the [SDK README](https://github.com/akopichin/afm/blob/main/sdk/README.md)
for module setup, a complete `Connect` example, API details and checks. To
operate the same dashboard from a private Telegram chat, see the
[Telegram bot guide](telegram-bot.md).
