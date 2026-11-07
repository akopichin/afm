# Engine extraction: `pkg/engine` owns one run

Stage 1 of the new-AFM plan. `pkg/engine` becomes the single owner of a run's
resources and commands. `cmd/afm` and `pkg/server` are adapters over it.
User-visible behavior, the HTTP/WS wire format, `events.jsonl`, `state.json`,
flow/config schemas and the SDK are unchanged. Development rules live in
[pkg/engine/AGENTS.md](../../../pkg/engine/AGENTS.md).

Out of scope: `afm run --mode core` and new CLI flags (stage 2), SDK core mode
(stage 3), splitting the Orchestrator internals, several runs per process.

## Decisions

**D1. Lifecycle `Open → Start → Wait → Shutdown → Close`.** `Open` builds
everything a run needs up to and including the orchestrator, but starts no
agents; a failed `Open` releases what it took and keeps the run files. `Start`
binds the agent API URL, starts the Assistant and its mesh bridge, then runs the
workflow under a run-scoped context. The Assistant and bridge are deliberately
not bound to that context, so the conversation continues after the workflow
ends. `Shutdown` runs while transports are still up because the Assistant and
agents call the mesh API over HTTP; `Close` runs after `srv.Shutdown` because
the server reads the journal and Store until the end. `Close` releases the
Store, jobs and accounting only when everything is drained; otherwise it
returns `ErrNotDrained` and leaves the Store open (the process exit releases
the flock). A write into a closed journal is worse than a temporary leak.

**D2. Explicit policies instead of the dashboard URL.** The orchestrator's
`DashboardURL` used to decide three things: mesh transport, plan approval and
the exit rule. It is split into `AgentAPIURL` (transport only) and
`HumanControl` (someone can approve plans and retry while the run stays open).
`cmd/afm` derives both from "server port > 0", so the ordinary CLI behaves as
before, while stage 2 can offer an HTTP API without a UI but with human control.

**D3. Complete stage and dialog commands.** `StageControl` implements the
commands the server used to assemble from several internal calls: approve,
retry, pause/continue, buttons, hooks, revise (with the `agent_note` notice),
dialog answer, pre-note and attachment save. Validation and file writes moved
from HTTP handlers into it. Errors are typed (`*engine.Error` with a `Kind`),
and the server maps `Kind` to the same status codes and bodies as before.
`NewStageControl` is exported so server tests keep their fakes under the real
engine logic.

**D4. Narrow API groups.** `Run` exposes small interfaces listing exactly the
methods adapters call, implemented by the Orchestrator and pinned by
compile-time assertions, instead of publishing the Orchestrator. A guard test
closes the type-assertion bypass.

**D5. Read-only server.** `pkg/server` takes a state reader and an event
stream instead of the mutable Store and the UI bus. All writes (pre-note,
answer files, notices, attachments, UI publishes) go through the engine.
File-based UI projections (plan, dialogs, artifacts, verify report) stay in the
server as read-only functions over the run directory.

**D6. Offline commands.** `ApproveOffline`, `RetryOffline` and `ReviseOffline`
change a stopped run through the journal with no orchestrator or agents. An
active run is rejected via the run lock (`ErrRunActive`) without touching the
journal. CLI messages stay byte-for-byte identical.

**D7. Explicit paths and shared helpers.** The engine receives the afm-root through `Paths`
and never reads `--dir` or `AFM_DIR`. The process CWD is still used in two
preserved places: without `root_dir` and a worktree, agents inherit it, and it
fills the lifecycle-hook metadata. Helpers shared by `run`,
`run_repeat`, `memory rebuild`, `validate` and the Docker re-exec have one
owner in the engine. Signals, worktree locking, the browser opener, owner token
and URL printing stay in `cmd/afm`.

## Boundary

`pkg/engine` does not import `cmd/afm`, `pkg/server`, `pkg/server/workspace`,
`pkg/web` or cobra. Check: `go list -deps ./pkg/engine`. Transport resources
enter through `Options` as functions or interfaces (file resolver, browser
starter). The only direct `Store.Apply` is the offline path, because the
orchestrator FSM would rename the `cli_*` events.

## Verification

Journal, FSM, shutdown order and the HTTP contract are covered by engine tests
that drive a real run (`TestDirectConsumer_*` is the library consumer without
`pkg/server`), the server parity tests and a live acceptance against a baseline
binary built from the pre-change commit.

The Docker re-exec is covered by regression tests that build argv/env and
check that no secret reaches argv. It was not run live in the agent container,
which has no Docker access. Run it on the host before merging:

```bash
afm --dir <fixture> run <flow> --port <port>
```

with `docker.enabled: true`, a worktree and a lifecycle hook that uses an
`env:` secret. Expect the run to start inside the container and the secret to
be absent from `ps`, logs and notices.

## Deferred

- Run modes and the second entry point: stage 2.
- Splitting the Orchestrator internals into smaller services.
