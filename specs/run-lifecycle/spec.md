# Run lifecycle: engine ownership

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The embedding API `Open → Start → Wait → Shutdown → Close` of one run (`engine:engine/api.go`,
  `engine:internal/engine`): what each step owns, refuses and releases.
- Readiness, drain and the clean shutdown order (cancel run ctx → wait agents → persist end → close Store).
- Run-scoped context for work started by commands.
- Sibling parts of this domain: run resolution, resume and offline control —
  [run-lifecycle-resume](../run-lifecycle-resume/spec.md); afm CLI composition, modes and exit codes —
  [run-lifecycle-cli](../run-lifecycle-cli/spec.md).
- Out of scope: journal format and durability ([event-journal](../event-journal/spec.md)), stage statuses and
  scheduling ([stage-lifecycle](../stage-lifecycle/spec.md)), helpers ([helpers](../helpers-lifecycle/spec.md)), mesh
  ([agent-mesh](../agent-mesh/spec.md)).

## Terms
- **Run** — `engine.Run`, the owner of Store, accounting, jobs journal, environment, lifecycle dispatcher,
  browser, orchestrator and Assistant for one run directory.
- **Transports** — dashboard/HTTP/workspace servers owned by the caller, never by the engine.
- **Admission** — the `StageControl` gate that accepts stage/budget commands; closed by `Shutdown`.
- **Drained** — workflow returned, accepted commands finished, Assistant and Mesh bridge stopped.

## Requirements

### RUN-R01 Open prepares without starting work
`Open` creates or reopens the run and everything it needs, but starts no agent. It calls `OnRunCreated`
exactly once with the run id as soon as the run directory exists. A failing `Open` releases everything it
acquired (Store lock, PATH/wrapper links, browser exactly once), leaves the run files on disk, and the next
`Open` of the same flow succeeds.

Evidence: `engine:internal/engine/lifecycle_integration_test.go` `TestOpenFailure_RollsBackAndAllowsReopen`,
`TestOpenFailure_RemovesGeneratedWrappers`, `TestOpenFailure_ReleasesBrowserOnce`,
`TestRunLifecycle_OpenWithoutStartAndDoubleClose`; `engine:internal/engine/open.go` `Open`, `release`;
`engine:internal/engine/AGENTS.md`.

#### RUN-S01 Broken lifecycle hook secret
- GIVEN a hook whose `env` references a missing secret file
- WHEN `Open` runs
- THEN it fails, `OnRunCreated` was called once with the created run id, the Store is unlocked, PATH is
  unchanged, and a following `Open` reuses that run and completes.

### RUN-R02 Validation inside Open precedes run creation
`Open` validates declared assistant loops before resolving or creating a run directory. Interactive stages are
normalized for the `HumanControl` policy on a copy; the caller's flow is not mutated. Collaboration policy, team
and declared assistants are checked and budgets prepared before the orchestrator can start an agent.

Evidence: `engine:internal/engine/normalize_test.go` `TestNormalizeInteractiveStagesDoesNotMutateFlow`;
`engine:internal/engine/budgets_test.go` `TestBudgets_MoneyWithoutRatesIsRefused`;
`engine:internal/engine/open.go` `Open` (`ValidateAssistantLoops`, `PrepareBudget`).

### RUN-R03 Start is single-shot and binds endpoints
`Start` may succeed once; a second call returns `ErrAlreadyStarted` and does not start a second workflow.
After `Shutdown` or `Close` began, `Start` returns `ErrClosed` (also when `Shutdown` ran before any `Start`).
The agent API URL is given only to `Start`; empty keeps the mesh unavailable.

Evidence: `engine:internal/engine/lifecycle_integration_test.go` `TestRunLifecycle_SecondStartIsRejected`;
`engine:internal/engine/lifecycle_undrained_test.go` `TestShutdown_BeforeStartDoesNotAllowStart`;
`engine:internal/engine/lifecycle_model_test.go` `TestClose_BeforeStartDoesNotAllowStart`;
`engine:internal/engine/run.go` `Start`.

#### RUN-S02 Double start
- GIVEN a started run
- WHEN `Start` is called again
- THEN it returns `ErrAlreadyStarted`, the run completes, and the agent ran exactly once.

### RUN-R04 Wait reports the workflow outcome
`Wait` returns `ErrNotStarted` before `Start` and `ErrClosed` after stores are closed. Cancelling the ctx given to
`Wait` ends only the wait (`ctx.Err()`); the run continues. Every repeated `Wait` returns the same `Result`.
`Result.Completed` is the persisted verdict (run status `finished` and all stages `done`); a run that ends with
a failed stage but no orchestrator error has `Err == nil` and `Completed == false`.

Evidence: `engine:internal/engine/lifecycle_integration_test.go` `TestWait_CancelDoesNotCancelRun`,
`TestWait_RepeatedWaitKeepsFailure`, `TestRunLifecycle_OpenWithoutStartAndDoubleClose`;
`engine:internal/engine/run.go` `Wait`, `runWorkflow`.

#### RUN-S03 Cancelled wait
- GIVEN a started run whose agent is held
- WHEN the ctx of `Wait` is cancelled
- THEN `Wait` returns `context.Canceled`, the stage stays `running`, and a later `Wait` returns the completed
  result twice identically.

### RUN-R05 Ready reflects a persisted start of this process
`Ready()` is true only after `Start` succeeded and the orchestrator persisted `BeginRun` of this process, and
until `Shutdown` or `Close` begins. A refused `Start`, an unpersisted `BeginRun`, or a timed-out/active
`Shutdown` leave it false; a finished workflow alone does not clear it.

Evidence: `engine:internal/engine/ready_test.go` `TestRun_Ready`, `TestRun_NotReadyAfterRefusedStart`,
`TestRun_NotReadyAfterTimedOutShutdown`, `TestRun_NotReadyWhileShutdownIsActive`,
`TestRun_NotReadyUntilBeginRunPersisted`; `engine:internal/engine/run.go` `Ready`; `afm:docs/core-mode.md`.

### RUN-R06 Shutdown drains in a fixed order
`Shutdown` closes admission, cancels the workflow and waits for it, waits for accepted commands, then drains the
Assistant and the historical Mesh bridge. Only the first `Shutdown` after a successful workflow waits for an
active Assistant turn; a repeated, concurrent or cancelled-then-retried attempt, or one after a failed workflow,
uses the bounded emergency drain (5 s). Once drained, the fact is never undone; later `Shutdown` is a no-op.

Evidence: `engine:internal/engine/lifecycle_model_test.go` `TestShutdown_FirstAttemptStaysGraceful`,
`TestShutdown_CancelledShutdownThenDeferredIsBounded`, `TestShutdown_CancelledInWaitIdleThenDeferredIsBounded`,
`TestShutdown_ConcurrentAttemptsNeverUndoDrain`; `engine:internal/engine/lifecycle_undrained_test.go`
`TestShutdown_RetryAfterEmergencyDrainIsBounded`; `engine:internal/engine/lifecycle_drain_invariant_test.go`
`TestShutdown_ObservedDrainSurvivesRepeatedAttempts`, `TestRunSource_DrainIsOneWay`;
`engine:internal/engine/budgets_test.go` `TestBudgets_ShutdownClosesAdmission`; `engine:internal/engine/run.go` `drain`.

#### RUN-S04 Commands after Shutdown
- GIVEN a run whose `Shutdown` has started
- WHEN a budget update or stage command arrives
- THEN it is rejected with `KindClosed` and does not run.

### RUN-R07 Close never closes an undrained Store
`Close` drains first if needed (bounded by 30 s). It always releases hooks, browser and environment on its first
call; it closes jobs journal, accounting and Store only when the run is drained, otherwise returns
`ErrNotDrained` and keeps the journals open. `Close` is idempotent and safe concurrently; `Close` before `Start`
releases the writer lock and keeps the run directory.

Evidence: `engine:internal/engine/lifecycle_undrained_test.go` `TestShutdownClose_StuckAssistantKeepsStoresOpen`,
`TestShutdownClose_UndrainedBridgeKeepsStoresOpen`; `engine:internal/engine/lifecycle_model_test.go`
`TestClose_DrainErrorStillReleasesAuxiliaries`, `TestClose_ConcurrentCloseReleasesOnce`;
`engine:internal/engine/lifecycle_integration_test.go` `TestRunLifecycle_CloseBeforeStartReleasesRun`;
`engine:internal/engine/run.go` `Close`, `closeStores`.

#### RUN-S05 Stuck Assistant
- GIVEN an Assistant turn that ignores interruption
- WHEN `Shutdown` then `Close` are called
- THEN `Close` returns `ErrNotDrained`, the Store stays locked and writable, and auxiliaries are released.

### RUN-R08 Workflow end order inside the orchestrator
On return of the orchestrator run, the run ctx is cancelled first, then all agents are awaited, budget work
ends, remaining mesh waits are closed only if the run really finalized, the terminal run event
(`finished`/`failed`/`interrupted`) is persisted, and lifecycle hooks are flushed. An interrupted run leaves
pending waits pending and its interrupted turns are retried on resume.

Evidence: `engine:internal/orchestrator/shutdown_test.go` `TestRun_CancelDrainsAgents`,
`TestSpawnAgent_WaitAgentsBlocksUntilDone`; `engine:internal/orchestrator/run_interrupt_test.go`
`TestInterruptedRun_WaitsStayPendingAndResolveAfterResume`, `TestFinalizedRun_ClosesPendingWaits`,
`TestInterruptedRun_TaskMidTurnIsRetriedOnResume`; `engine:examples/minimal/integration_test.go`
`TestCancellationReapsChildrenAndResumes`; `engine:internal/orchestrator/orchestrator.go` `Run`.

#### RUN-S06 Interrupt and resume
- GIVEN a run with two live child agents
- WHEN the process is interrupted
- THEN both children are reaped before exit, and a new process resumes the same run directory.

### RUN-R09 Work started by a command lives under the run context
Agents, turns and deliveries started by an API command run under the run context, never under the request
context; a request context that ends when the handler returns does not cancel them. Without a running
workflow the fallback is a non-cancelling copy of the caller ctx.

Evidence: `engine:internal/orchestrator/runctx_test.go` `TestApprove_SpawnsAgentUnderRunCtx_NotRequestCtx`;
`engine:internal/orchestrator/agentcomm_attach_test.go` `TestSend_StartedWorkSurvivesTheRequestContext`;
`afm:pkg/server/budgets_test.go` `TestBudgets_CancelledRequestDoesNotCancelUpdate`;
`engine:internal/orchestrator/paths.go` `runContext`.

### RUN-R10 Run outcome for budgets
`Result.Budget` is set when the budget held work or a hard/medium limit was reached. Without `HumanControl` a
held run ends its workflow and reports `Held`; with it, the hold is managed without shutdown.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_ResultReportsHeldRunWithoutHumanControl`,
`TestBudgets_ResultReportsExhaustedScopeOfCompletedRun`; `engine:examples/minimal/integration_test.go`
`TestBudgetHoldIsManagedWithoutShutdown`; `engine:internal/engine/run.go` `Result`.

### RUN-R11 Engine boundary
The engine does not import AFM CLI, HTTP server, dashboard or Cobra, and does not read process roots
(`--dir`, `AFM_DIR`, CWD); all paths come through `Paths`.

Evidence: `engine:internal/engine/boundary_test.go` `TestBoundary_EngineDoesNotImportTransports`;
`engine:internal/engine/run.go` `Paths`.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| RUN-E01 | Second `Start` | `ErrAlreadyStarted` | `TestRunLifecycle_SecondStartIsRejected` |
| RUN-E02 | `Start` after `Shutdown`/`Close` | `ErrClosed` | `TestClose_BeforeStartDoesNotAllowStart` |
| RUN-E03 | `Wait` before `Start` | `ErrNotStarted` | `TestRunLifecycle_OpenWithoutStartAndDoubleClose` |
| RUN-E04 | `Wait`/`Shutdown` after stores closed | `ErrClosed` | same |
| RUN-E05 | `Close` while not drained | `ErrNotDrained` (wraps drain error), Store open | `TestShutdownClose_StuckAssistantKeepsStoresOpen` |
| RUN-E06 | Command after admission closed | `*Error{Kind: KindClosed}` | `TestBudgets_ShutdownClosesAdmission` |

## Coverage gaps and discrepancies
- **RUN-G01** (untested): the 30 s `closeDrain` bound of `Close` on a still running workflow is not exercised
  with its real value (`engine:internal/engine/run.go` `closeDrain`).
- **RUN-G02** (doc-only): "a late bootstrap failure is not rolled back; the caller exits" is stated in
  `engine:internal/engine/AGENTS.md` only.
- **RUN-G03** (code≠doc, naming): `engine:internal/engine/AGENTS.md` refers to `pkg/engine`; the package is
  `internal/engine` (public facade `engine/`).
