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

`Run.Wait(ctx)` waits for the subprocess to exit. If `ctx` is cancelled, it
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

## Scope and checks

There are no push/webhook notifications, built-in `net/http.Handler`, or
dynamic in-memory flow definitions. Poll `Status` or the relevant message API
as needed; `Start` takes a flow file path.

From the repository root, use `make sdk-test sdk-lint`. Integration tests that
launch a real subprocess use `make sdk-test-integration`; they require an AFM
binary from `AFM_SDK_TEST_BINARY` or `bin/afm` after `make build`.
