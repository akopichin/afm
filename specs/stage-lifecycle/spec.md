# Stage lifecycle: FSM, scheduling, control

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Stage statuses and the transition table; durable CAS transitions and turn-epoch fencing.
- Dependency scheduling, command-semaphore leases, completion cascade and failure cascade.
- Pause/Continue, including the `auto_run: false` gate.
- Family (prefix `STG`, disjoint ranges): this file `R01–R10`; retry, restart recovery, exit-on-fail, run end —
  [stage-lifecycle-recovery](../stage-lifecycle-recovery/spec.md) (`R11–R19`); tracks, scripts, artifacts,
  dynamic stages — [stage-lifecycle-tracks](../stage-lifecycle-tracks/spec.md) (`R20+`).
- Out of scope: questions/approval/revise ([dialog-approval](../dialog-approval/spec.md)), helpers and
  `awaiting_assistants` gate ([helpers](../helpers-lifecycle/spec.md)), verify ([verify](../verify/spec.md)), mesh
  waits/`awaiting_peer` ([agent-mesh-waits](../agent-mesh-waits/spec.md)), budget and quota holds
  ([budgets-accounting](../budgets-accounting/spec.md)), offline CLI ([run-lifecycle-resume](../run-lifecycle-resume/spec.md)).

## Terms
- **FSM event** — named transition (`start_planning`, `approve`, `complete`, …) with an allowed From-set.
- **epoch** — stage ownership counter; a turn owns the stage while its epoch is current.
- **lease** — at most one held slot of a per-command semaphore (`max_parallel`).
- **PausedFrom** — status a pause returns to; kept after Continue as the passed-gate marker.

## Requirements

### STG-R01 Closed status set and transition table
A stage is in exactly one status of `AllStatuses()` (16 values). Every change goes through an FSM event whose
From-set must contain the current status; `fail` is allowed from any non-terminal status. `done` is an absolute
sink; `failed` is left only by `manual_retry` (→ `pending`).

Evidence: engine:internal/orchestrator/bus/fsm_test.go `TestFSM_Apply_LegalTransitions`,
`TestFSM_Apply_IllegalReturnsApplyFalse`, `TestFSM_DoneIsAbsoluteSink`, `TestFSM_FailedTerminalExceptManualRetry`;
engine:internal/orchestrator/bus/fsm.go `NewFSM`; afm:contracts/state/statuses.go `AllStatuses`

#### STG-S01 Illegal event is a no-op
- GIVEN a stage in `awaiting_approval`
- WHEN `pause` is applied
- THEN the apply reports not-applied, nothing is journaled, the status is unchanged.

### STG-R02 Transitions are durable CAS; a lost CAS is benign
A transition is applied by the Store under its lock against the current status (plus optional `ExpectFrom`,
`ExpectEpoch`, `ExpectStageSeq`, `Fence`) and is journaled before any side effect. A CAS miss or a closed Store
returns not-applied; a journal write failure is storage-fatal and stops the run.

Evidence: engine:internal/orchestrator/bus/fsm_test.go `TestFSM_CasRejectsStaleTransition`,
`TestFSM_Apply_ExpectFromRefusesAnotherStatus`, `TestFSM_Apply_AfterStoreClosed_IsBenignNotFatal`;
engine:internal/orchestrator/orchestrator.go `triggerChecked`; engine:internal/orchestrator/race_fixes_test.go
`TestCompleteStage_LostCASSkipsSideEffects`

### STG-R03 Turn results are fenced by epoch
A turn's outcome (completion, failure, retry, question) commits only while the turn still owns the stage epoch.
After Pause, Continue or Revise the old turn changes nothing, except handing off the Revise of its own turn.

Evidence: engine:internal/orchestrator/turn_grant_test.go `TestTurnGrant_AGrantReadBeforeATakeoverIsRefused`,
`TestTurnGrant_PauseContinueOfAQueuedRunnerRunsOneTurn`; engine:internal/engine/commands_integration_test.go
`TestCommands_PauseBeforeTurnEndThenContinueRunsNewTurnOnce`, `TestCommands_PauseAndReviseRacingTurnEnd`;
engine:internal/orchestrator/retry.go `failTurn`

### STG-R04 Dependency scheduling
A pending stage starts planning (or, without planning, is activated to `ready`) only when every `depends_on` is
`done`; `ready → running` also requires deps done. Only the winner of the durable `start_planning`/`start_run`
CAS spawns work. `eager_planning: true` starts planning at flow start; implementation still waits for deps.

Evidence: engine:internal/orchestrator/integration_test.go `TestIntegration_SequentialDependencies`,
`TestIntegration_PlanningWaitsForDependencies`, `TestIntegration_EagerPlanningStartsImmediately`,
`TestIntegration_PrePlannedStageWaitsForDeps`; engine:internal/orchestrator/scheduling.go
`startPlanningForUnblocked`, `startReadyStage`, `activatePrePlannedStage`

#### STG-S02 Independent stages run in parallel
- GIVEN two stages without dependencies
- WHEN the run starts
- THEN both start without waiting for each other (subject to STG-R05).

### STG-R05 Command semaphore and transferable lease
Each command has a semaphore sized by the smallest per-stage `max_parallel` for it, else global `max_parallel`
(≤0 = unlimited); a command no stage declared gets a bounded dynamic cap (global limit or 4). A stage agent holds
one lease; `SwapTo` the same command is a no-op, another command releases before acquiring. The start check
(pause/permission) is re-run after the slot is granted; a cancelled wait starts nothing.

Evidence: engine:internal/orchestrator/maxparallel_test.go `TestMaxParallelThrottling`;
engine:internal/orchestrator/concurrency/lease_test.go `TestSpawnAgentLease_SameCommandSwapTo_NoSelfDeadlock`,
`TestLease_SwapTo_CrossCommandReleasesBeforeAcquiring`; engine:internal/orchestrator/concurrency/concurrency_test.go
`TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore`, `TestSpawnOwned_UnknownCommandSharesTheRunLimit`,
`TestSpawnOwned_SameOwnerRunsOneAtATime`; engine:internal/orchestrator/concurrency/concurrency.go `New`

### STG-R06 Completion path and cascade
A finished main executor completes through the shared completion path: if a required helper blocks, the stage
goes to `awaiting_assistants`; otherwise `complete` is committed with the gate re-checked at commit time, then
`script_after` and reflection run, and dependents are scheduled. A lost completion CAS runs no side effects.

Evidence: engine:internal/orchestrator/race_fixes_test.go `TestCompleteStage_LostCASSkipsSideEffects`;
engine:internal/orchestrator/pause_continue_test.go `TestContinue_RecoveredCompletion_UnblocksDependent`;
engine:internal/orchestrator/orchestrator.go `completeStageFenced`, `onAgentCompleted`

### STG-R07 Failure cascade to dependents
Whenever a stage fails, every `pending` stage with a `failed` dependency is moved to `failed` (`blocked_by_dep`),
transitively, and is never planned. A dependent retried while its dependency is still failed is failed again.

Evidence: engine:internal/orchestrator/integration_failure_test.go `TestIntegration_FailedDependencyCascade`,
`TestIntegration_NoPlanningForDoomedStage`; engine:internal/orchestrator/fail_blocked_retry_test.go
`TestFailBlockedStages_RetryBetweenPassesIsFailedAgain`; engine:internal/orchestrator/scheduling.go `failBlockedStages`

#### STG-S03 Transitive cascade
- GIVEN `a → b → c` and `a` fails
- WHEN the cascade runs
- THEN `b` and `c` are `failed` and no planning agent ran for them.

### STG-R08 Pause
`Pause` is accepted for `running`, `planning`, `revising`, `retrying`, `awaiting_assistants`, `awaiting_peer`,
`awaiting_quota`. It first commits `paused` (recording PausedFrom), then interrupts the live agent or retry
backoff and invalidates a pending before-hook launch. A running script stage cannot be paused.

Evidence: engine:internal/orchestrator/pause_continue_test.go `TestPause_RunningStage_StopsAgentAndTransitionsToPaused`,
`TestPause_RetryingStage_CancelsBackoffImmediately`, `TestPause_AwaitingApproval_IsNoOp`;
engine:internal/orchestrator/hooks_test.go `TestWithBeforeHook_SkipsMainFnIfPausedDuringHook`;
engine:internal/engine/stage_control.go `StageControl.Pause`; engine:internal/orchestrator/control_api.go `Pause`

### STG-R09 Continue
`Continue` of a `paused` stage restores PausedFrom: from `pending` it activates/plans the stage, otherwise it
resumes through the same path as restart recovery ([STG-R13](../stage-lifecycle-recovery/spec.md)). Status read, CAS and "continued in this process" are
one step, so Continue racing startup recovery never spawns twice. A budget-held stage is not continued by hand.

Evidence: engine:internal/orchestrator/pause_continue_test.go `TestContinue_FromPending_StartsScriptStage`,
`TestContinue_FromRevising_ResumesWithFeedback`, `TestContinue_FromRetrying_AutonomousStage`,
`TestContinue_NotPaused_IsNoOp`; engine:internal/orchestrator/continue_startup_race_test.go
`TestContinue_RacesStartupRecovery_NeverDoubleSpawns`; engine:internal/orchestrator/race_fixes_test.go
`TestPauseContinue_DuringBeforeHook_LaunchesOnce`

### STG-R10 `auto_run: false` gates the first activation only
A stage with `auto_run: false` is paused (from `pending`) the moment it becomes eligible; it starts only on
Continue. Because PausedFrom survives Continue, the gate never fires again in the run, and restart never
auto-resumes a paused stage.

Evidence: engine:internal/orchestrator/pause_continue_test.go `TestIntegration_AutoRunGatesRegularStage`,
`TestIntegration_AutoRunGatesScriptStage`, `TestIntegration_AutoRunGateFiresOnceOnly`,
`TestIntegration_ResumeLeavesPausedStageUntouched`; engine:internal/orchestrator/scheduling.go `shouldGateAutoRun`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| STG-E01 | Approve/Retry/Continue in a wrong status | `KindInvalid` "stage is X, not Y"; status unchanged | engine:internal/engine/commands_integration_test.go `TestCommands_RejectedInTheWrongStatus` |
| STG-E02 | Pause of a non-pausable status | `KindInvalid` "cannot be paused" | engine:internal/engine/stage_control.go `StageControl.Pause` |
| STG-E03 | Pause of a running script stage | `KindConflict` | `TestCommands_RejectedInTheWrongStatus` |
| STG-E04 | Unknown stage / unsafe id | `KindNotFound` / `KindInvalid` | same; engine:internal/engine/stage_control.go `ValidStageID` |
| STG-E05 | Any forward command during a review pause | `KindConflict`, code `flow_paused` | engine:internal/orchestrator/control_api.go `rejectIfReviewPaused` |
| STG-E06 | Command after `Shutdown` began | `KindClosed` | engine:internal/engine/stage_control.go `enter` |
| STG-E07 | Journal write fails during a transition | run stops storage-fatal | engine:internal/orchestrator/orchestrator.go `triggerChecked` |

## Coverage gaps and discrepancies
- **STG-G01** (code≠doc): FSM allows `pause` from `pending`/`awaiting_user_input`, but the public `Pause`
  rejects them; only the `auto_run` gate and budget use those edges. `stage-lifecycle.md` does not say so.
- **STG-G02** (untested): engine-level rejection E02 (non-pausable status) has no direct StageControl test.
- **STG-G04** (ambiguous): a `failed` dependent is not reset when its upstream is retried; it needs its own
  Retry (or `auto_recover`). Docs do not state this.
