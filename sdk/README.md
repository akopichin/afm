# afmsdk

A dependency-free Go module that lets a Go service launch an [afm](../README.md) flow as a subprocess, poll its progress, and drive approve/retry/revise while it's running — so services can build their own web endpoints ("watch progress in the browser") on top of afm without shelling out by hand.

See the project's [release notes](../release-notes.md) for the announcement, and this package's own [release-notes.md](./release-notes.md) for the SDK's own history.

## Why a subprocess, not an embedded orchestrator

`afmsdk.Client.Start` spawns `afm run --dir <isolated> --port <picked> <flowPath>` as a real subprocess — it does not import or embed afm's orchestrator. Two afm internals make this the right shape (both documented in more depth in [release-notes.md](./release-notes.md)):

- **Control while a run is live has to go through afm's own HTTP dashboard, not the `afm approve/retry/revise` CLI subcommands.** Those subcommands take an exclusive `flock` on the run directory, which the live `afm run` process already holds for as long as any stage sits in `awaiting_approval` — so they fail with a locked-run error while the run you'd want to control is exactly the one that's alive. The SDK always starts its own subprocess with `--port` and talks to that run's dashboard API instead.
- **Ctx cancellation sends `SIGINT`, never `SIGTERM`** — afm's own graceful shutdown only traps `SIGINT`.

Because of this, `afmsdk` imports **zero** `github.com/akopichin/afm/...` packages — it only needs the `afm` binary to exist (on `PATH`, or via `Config.Binary`) and talks to it as a black box over CLI flags plus its existing dashboard HTTP API (`GET /api/status`, `POST /api/stages/<id>/{approve,retry,revise}`).

## Install

This module isn't tagged yet (no `sdk/vX.Y.Z` release cut — see [release-notes.md](./release-notes.md) for the tagging convention this module will use once it is). Until then, consume it from a local checkout via a `replace` directive in your own `go.mod`:

```
require github.com/akopichin/afm/sdk v0.0.0-00010101000000-000000000000

replace github.com/akopichin/afm/sdk => /path/to/afm/sdk
```

You also need the `afm` binary itself installed and reachable — see the main [README's Installation section](../README.md#installation).

## Quick example

```go
client, err := afmsdk.New(afmsdk.Config{MaxConcurrent: 4})
if err != nil {
	log.Fatal(err)
}

runCtx, cancel := context.WithCancel(ctx)
defer cancel()

run, err := client.Start(runCtx, "flow.yaml", "/path/to/target/project")
if err != nil {
	log.Fatal(err)
}

for {
	status, err := run.Status(runCtx)
	if err != nil {
		log.Fatal(err)
	}
	if status.Done {
		break
	}
	if status.Failed {
		// afm keeps the subprocess alive on failure so a stage can be
		// retried; here we just give up and shut it down instead, since
		// Wait only returns once the process exits or ctx is cancelled.
		cancel()
		break
	}
	time.Sleep(2 * time.Second)
}

if err := run.Wait(runCtx); err != nil {
	log.Fatal(err)
}
if err := run.Cleanup(); err != nil {
	log.Fatal(err)
}
```

A more complete, runnable example — a small HTTP service wrapping `afmsdk` with `POST /runs`, `GET /runs/{id}/status`, `POST /runs/{id}/{approve,retry,revise}`, `DELETE /runs/{id}` — is exactly the shape this package exists to make easy; build one the same way if you want a manual sanity check against a real flow.

## API

- `Config{Binary string; MaxConcurrent int}` / `New(cfg Config) (*Client, error)` — `Binary` defaults to resolving `"afm"` from `PATH`; `MaxConcurrent` caps how many `afm run` subprocesses this `Client` will have active at once (0 = unlimited).
- `Client.Start(ctx, flowPath, workDir string) (*Run, error)` — `workDir` is used as both the subprocess's `--dir` (afm's own state directory) and its CWD (the target project agents actually operate on), creating `workDir` first if it doesn't exist. Blocks until the run's dashboard API is reachable.
- `Client.Attach(ctx, dir string, port int, pid int) (*Run, error)` — reconnect to a live afm subprocess that was started by an earlier process instance. `dir`, `port`, and `pid` are usually persisted when `Start` returns (e.g. in a database row), so a service can survive a restart: cancel the old process, restart the service, and call `Attach` to get a new `*Run` handle on the same subprocess. `Status`, `Approve`, `Retry`, and `Revise` work exactly as on a started run; `Wait` and `Cleanup` return an error (since this package never held the `*exec.Cmd` to manage across the restart).
- `Run.Status(ctx) (RunStatus, error)` — polls `GET /api/status`. `RunStatus{FlowName string; Stages map[string]StageStatus; Done, Failed bool}`. `StageStatus` mirrors afm's internal stage FSM values (`pending`, `planning`, `awaiting_approval`, `revising`, `ready`, `running`, `retrying`, `awaiting_user_input`, `done`, `failed`, `hook_failed`) as plain string constants — duplicated here on purpose so this module stays free of any afm-package dependency.
- `Run.Approve(ctx, stageID string) error`, `Run.Retry(ctx, stageID string) error`, `Run.Revise(ctx, stageID, feedback string) error` — POST to the live run's dashboard API. Errors carry the dashboard's response text; there are no typed/sentinel errors in v1.
- `Run.Wait(ctx) error` — blocks until the subprocess exits. Cancelling `ctx` sends `SIGINT` (afm's graceful-shutdown signal) and waits up to 15s before killing the process.
- `Run.Cleanup() error` — removes the run's isolated state directory. Must be called after `Wait` returns; calling it while the subprocess might still be alive returns an error instead of deleting files out from under a live afm process. Never called automatically.
- `Run.Dir() string` — the isolated `--dir` this run's afm state lives in (useful for debugging).
- `Run.Port() int` — the dashboard API port for this run (needed to reconnect via `Client.Attach` after a restart).
- `Run.PID() int` — the operating system process ID of the afm subprocess (needed to reconnect via `Client.Attach` after a restart).

## Scope (v1)

Deliberately out of scope for now — see [release-notes.md](./release-notes.md) for the reasoning behind each:

- No push/webhook notifications — `Status` is poll-only.
- No `Answer()` API for interactive (question/answer) stages — only autonomous (`agents: [auto]`) or script/planning-gated stages are supported end to end.
- No built-in `net/http.Handler` — wire `Status`/`Approve`/`Retry`/`Revise` into your own routes.
- No dynamic/in-memory flow definitions — `Start` takes a file path only.

## Testing

From this directory:

```bash
go test ./...                          # unit tests only (no real subprocess)
```

From the repo root:

```bash
make sdk-test              # same as above
make sdk-test-integration  # builds afm first, then also runs the 7 real-subprocess integration tests
make sdk-lint              # golangci-lint against this module
```

The integration tests (`TestIntegration_HappyPath`, `_StartUsesWorkDirAsAfmDir`, `_Approve`, `_Retry`, `_Revise`, `_MaxConcurrentBlocksExtraStarts`, `_AttachReconnectsToLiveRun`) spawn a real `afm run` subprocess end to end — they need a pre-built `afm` binary, resolved via `AFM_SDK_TEST_BINARY` or `../bin/afm` (what `make build` produces at the repo root), and `t.Skip` with a clear message if neither exists.
