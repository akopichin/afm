# afmsdk

`afmsdk` is a dependency-free Go module for controlling AFM through its local
dashboard API. Use `Connect` for an existing run, or `Client.Start` when your Go
program must launch and own an `afm run` subprocess. The module does not import
AFM's internal Go packages.

See the [site guide](../docs/sdk.md), the project's [changelog](../CHANGELOG.md),
and the [SDK release notes](release-notes.md).

## Install

The nested module is `github.com/akopichin/afm/sdk`. It has not been tagged as
`sdk/vX.Y.Z` yet. From a local checkout, use a `replace` directive:

```go
require github.com/akopichin/afm/sdk v0.0.0-00010101000000-000000000000

replace github.com/akopichin/afm/sdk => /path/to/afm/sdk
```

An existing dashboard is enough for `Connect`. `Client.Start` additionally
requires an `afm` executable on `PATH`, or `Config.Binary` pointing to it.

## Connect to a running dashboard

Start AFM with its dashboard on a known port, for example `afm run --port 9876`.
Then:

```go
package main

import (
	"context"
	"fmt"
	"log"

	afmsdk "github.com/akopichin/afm/sdk"
)

func main() {
	ctx := context.Background()
	run, err := afmsdk.Connect(ctx, 9876)
	if err != nil {
		log.Fatal(err)
	}
	status, err := run.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: %s (%d stages)\n", status.FlowName, status.RunState, len(status.StageViews))
}
```

`Connect` uses `127.0.0.1`, checks that the dashboard responds with a flow name
and start time, and does not start or own an AFM process. It does not check a
PID. `Wait` and `Cleanup` are only for a run launched by `Client.Start`.

## Launch a run

`Client.Start(ctx, flowPath, workDir)` starts `afm run --dir workDir --port
<picked> flowPath`, with `workDir` also as the subprocess's working directory.
It waits for the dashboard to respond. The flow path should be absolute when it
is outside `workDir`. The returned `Run` exposes `Dir()`, `Port()`, and `PID()`.

`Run.Wait(ctx)` waits for the subprocess to exit (in core mode that happens only
after `Shutdown`; see [Core mode](#core-mode)). If `ctx` is cancelled, it
sends `SIGINT`, waits up to 15 seconds, then kills the process if needed. A
failed AFM stage can keep the process alive for a retry, so cancel the context
before waiting if your service is giving up. `Run.Cleanup()` removes **the
entire `workDir`** after the process has exited. Use it only for a directory
your program created solely for this run; it is never called automatically.

`Client.Attach(ctx, dir, port, pid)` reconnects to a previously launched
process when you have retained all three values. It checks the PID and dashboard
port, but it does not own that subprocess, so `Wait` and `Cleanup` are
unavailable on the attached handle. `Config.MaxConcurrent` limits subprocesses
started by one `Client` (zero means unlimited).

## Core mode

`Config.Mode` chooses what `Client.Start` launches. The zero value and
`ModeDashboard` pass no `--mode` flag, so older `afm` binaries keep working.
`ModeCore` adds `--mode core`: the HTTP API without the Web UI (see
[Core mode](../docs/core-mode.md)). An unknown mode is rejected by `New`.

```go
func runCore(ctx context.Context, flowPath, workDir string) error {
	client, err := afmsdk.New(afmsdk.Config{Mode: afmsdk.ModeCore})
	if err != nil {
		return err
	}
	run, err := client.Start(ctx, flowPath, workDir)
	if err != nil {
		return err
	}
	// Stop the core on every return path, including a failed WaitResult; use a
	// context of its own, not one cancelled by the failed request. Do not call
	// log.Fatal after this point: it skips deferred calls.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := run.Shutdown(stopCtx); err != nil {
			log.Print(err)
		}
	}()

	status, err := run.WaitResult(ctx)
	if err != nil {
		return err
	}
	fmt.Println(status.RunState)
	return nil
}
```

- **Start.** For `ModeCore`, `Start` returns after `/api/runtime` reports mode
  `core`, protocol 1 and `ready: true`, `/api/status` has the same `run_id`, and
  the run directory exists. Any failure (an old `afm` that rejects `--mode`, an
  early exit, a timeout, cancellation) stops the process, frees the
  `MaxConcurrent` slot and keeps `workDir`. There is no silent fallback to the
  dashboard. `Run.Mode()` after `Start` is the mode that was launched; `Connect`
  and `Attach` read it from `/api/runtime` and leave it empty for an `afm` without
  that endpoint.
  An unknown mode or protocol gives `ErrIncompatibleRuntime`.
- **WaitResult.** `Run.WaitResult(ctx)` returns the workflow's final `RunStatus`
  (`RunState` is `RunStateFinished`, `RunStateFailed` or `RunStateInterrupted`),
  not the process exit. A failed stage in a running workflow is not final: you can
  `Retry` and keep waiting. It waits for `ready` first, so the result of an
  earlier process on the same directory is never returned. If the API starts
  serving another run (for example the next `repeat` cycle), it returns
  `*RunChangedError{Expected, Current}`; use `errors.As`. If `afm` becomes
  unreachable before a final status, the error wraps `ErrResultUnavailable` (and
  the `*exec.ExitError` for an owned process: 1 with `--exit-on-fail`, 130 when
  interrupted). A `Connect` handle tolerates an unreachable API for 30 seconds. A
  cancelled `ctx` only stops the waiting.
- **Shutdown.** `Run.Shutdown(ctx)` works only for a process started by this
  `Client`; otherwise it returns `ErrNotOwned`. It sends SIGINT and waits until
  `ctx` ends (30 seconds when `ctx` has no deadline, longer than afm's own drain);
  then it kills the process and the error wraps `ErrKilled`. The result is `nil`
  for exit code 0, otherwise `*exec.ExitError` (130 when an active workflow was
  interrupted). Repeated and concurrent calls return the same result. A kill
  cannot remove a Docker container.
- **Wait and slot.** Core keeps running after the result, so
  `Wait(context.Background())` does not return until `Shutdown`, and the
  `MaxConcurrent` slot stays taken until the process exits. `Wait` and `Cleanup`
  on a `Connect`/`Attach` handle return an error matching `ErrNotOwned`.
- **Output.** The SDK keeps only the last 4 KiB of afm's output for error
  messages, and a child holding the pipes cannot block `Shutdown` for more than 5
  seconds.

## Read and control a run

All methods below take a `context.Context` and use the local dashboard API:

| Method | Purpose |
|---|---|
| `Status` | Read flow name, run ID, start/end time, run state, last sequence, capabilities, stage views, and the stage-status map. `Done` means every stage is done and there is at least one; `Failed` means a stage is failed or hook-failed. |
| `Plan(stageID)` | Read the complete plan bytes. |
| `Dialog(stageID)`, `Answer(stageID, DialogAnswer)` | Read stage dialog and answer a question by ID and phase. |
| `Discussion(stageID, cursor, limit)`, `PostDiscussion(stageID, DiscussionPost)` | Read and post in a stage discussion. |
| `Participants`, `Threads`, `Inbox`, `Thread`, `StageMessages` | Read Agent Mesh people, conversations, and paged messages. |
| `Send(MeshSendRequest)` | Send an Agent Mesh message with a caller-supplied stable request ID for safe retries after an ambiguous response. |
| `AssistantProfiles`, `AttachAndSend(AttachAndSendRequest)` | Find available profiles and attach an agent with an optional first message. |
| `Approve`, `Retry`, `Revise`, `Pause`, `Continue` | Send the corresponding stage control request. |
| `Budgets`, `Budget(scope)`, `UpdateBudget(BudgetUpdate)`, `ResumeBudget(scope, revision)` | Read the spending limits, change one and continue a held scope. See [Budgets](#budgets). |

`StageView` contains each stage's runtime status and UI flags such as
`ShowPlan`, `ShowDialog`, and `Buttons`. `RunStatus.Capabilities` tells a client
which optional APIs are available. Stage statuses include `awaiting_approval`,
`awaiting_user_input`, `awaiting_assistants`, `awaiting_peer`, and `paused`, as
well as the normal pending/running/done/failure states. Check the status and
capabilities before showing an action. The dashboard has no atomic stage
sequence precondition for controls, so a fresh read cannot guarantee that the
stage stays unchanged until a write.

Rejected dashboard requests return `*afmsdk.HTTPError` with `StatusCode` and a
bounded `Body`; use `errors.As` to inspect it. Oversized successful responses
wrap `afmsdk.ErrResponseTooLarge` for `errors.Is`. JSON responses are limited
to 8 MiB, plan responses to 16 MiB, and error bodies to 4 KiB.

## Budgets

A run with spending limits serves them through `Budgets` (every scope) and `Budget` (one scope: `run`,
`stage:<id>` or `task:<id>`, with a `YAML` block when the limit can be saved to the flow file). A `BudgetView`
carries the engine's decision: effective limit, spend, per-metric level, `Hold` (why work is stopped), parent
limits and the `Actions` that make sense now. Display it; do not recompute admission.

```go
detail, err := run.Budget(ctx, "stage:build")
if err != nil {
	return err // errors.Is(err, afmsdk.ErrBudgetsUnavailable) for an afm without budgets
}
res, err := run.UpdateBudget(ctx, afmsdk.BudgetUpdate{
	Scope:            "stage:build",
	ExpectedRevision: detail.View.Revision, // 0 is a valid revision
	Override: afmsdk.BudgetOverride{
		Kind: afmsdk.BudgetOverrideLimit,
		Spec: &afmsdk.BudgetSpec{Tokens: 1_000_000, StopPolicy: afmsdk.BudgetPolicyHard},
	},
	Resume: true, // continue the held scope after saving
})
var budgetErr *afmsdk.BudgetError
if errors.As(err, &budgetErr) && budgetErr.Code == "budget_still_limited" {
	// The limit was saved (res.View is the new revision) but work is still stopped: raise it further.
}
```

`BudgetOverride` kinds are `none` (use the declared limit), `limit` (`Spec` required) and `unlimited` (drop the
scope's own limit; parent limits still apply). `SaveToYAML` also writes a limit to the flow file; the outcome is
`BudgetUpdateResult.Export`, whose failure does not undo the saved override.

Refusals are `*afmsdk.BudgetError{Status, Code, Message}`; branch on `Code`: `budget_invalid`,
`budget_unknown_scope`, `budget_revision_conflict`, `budget_still_limited`, `budget_rates_missing`,
`budget_closed`. `UpdateBudget` returns the saved result **and** a `*BudgetError` when the override was saved
but continuing was refused. `RunStatus.Capabilities.Budgets` and `.BudgetExport` say what the run serves; an afm
without budgets answers `ErrBudgetsUnavailable`.

## Scope and checks

There are no push/webhook notifications, built-in `net/http.Handler`, or
dynamic in-memory flow definitions. Poll `Status` or the relevant message API
as needed; `Start` takes a flow file path.

From the repository root, use `make sdk-test sdk-lint`. Integration tests that
launch a real subprocess use `make sdk-test-integration`; they require an AFM
binary from `AFM_SDK_TEST_BINARY` or `bin/afm` after `make build`.
