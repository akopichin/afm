# Agent mesh: loops (scheduled prompts)

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Creating, listing and stopping loops (`/loop` in the Feed, `afm agent loop`, `/api/{mesh,agent-mesh}/loop(s)`).
- Tick scheduling: fixed slots, skips, restart behaviour, conversations and limits.
- Lifetime and end reasons; helper reactivation by ticks.

Siblings: [`../agent-mesh/spec.md`](../agent-mesh/spec.md), [`../agent-mesh-messaging/spec.md`](../agent-mesh-messaging/spec.md).
Declared helper loops' attach rules and helper gates — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md). IDs: `MESH` R80–R89.

## Terms
- **Loop** — durable schedule `(creator, target, every, prompt, until_stage?, origin_stage?)` with short ID `L1, L2…`.
- **Slot** — creation time + k·every; a **tick** is a delivered slot.

## Requirements

### MESH-R80 Creating a loop
Loops require collaboration. The target must already exist and be eligible (run agent or helper, not the stage's main agent, Assistant or a finished target); `every` ≥ 1s; prompt ≤ 15,872 bytes, redacted. The human must name exactly one target; an agent without `--to` targets itself. An `until_stage` already ended is rejected. Creation is idempotent per `request_id`; alias resolution is frozen at creation.

Evidence: `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_UserCreatesForRunAgent`, `TestLoop_AgentWithoutTargetLoopsItself`, `TestLoop_UserNeedsOneExplicitTarget`, `TestLoop_RejectsIneligibleTargets`, `TestLoop_RejectsAnEndedStageBound`, `TestLoop_RejectsBadInput`, `TestLoop_PromptIsRedacted`, `TestLoop_CreateIsIdempotent`, `TestLoop_RejectedWhenAssistantsDisabled`; `afm:cmd/afm/agent_loop_test.go` `TestAgentLoopCLI_AddWithoutTargetSchedulesSelf`, `TestAgentLoopCLI_InvalidFlagsNeverReachTheService`; `afm:cmd/afm/run_mesh_loop_test.go` `TestLoopAdapters_RetryOverHTTPMakesOneLoop`

#### MESH-S80 Agent self-loop
- GIVEN an agent invocation
- WHEN it runs `afm agent loop add --every 5m --prompt "Check logs"` without `--to`
- THEN a loop targeting itself is created and returned as JSON

### MESH-R81 Identity and origin
The caller is the token's participant (body never names the actor); an agent's origin stage is derived server-side, the CLI never sends one; the dashboard sends its Feed's `origin_stage`. Prompts may come from a file or stdin.

Evidence: `afm:cmd/afm/agent_loop_test.go` `TestAgentLoopCLI_TokenIdentifiesTheCaller`, `TestAgentLoopCLI_AddNeverSendsOriginStage`, `TestAgentLoopCLI_PromptFromFileAndStdin`; `afm:cmd/afm/run_mesh_loop_test.go` `TestLoopAdapters_BothSurfacesReachOneService`; `engine:host/run_agent_mesh.go` `ApplyLoopCommand`

### MESH-R82 Active limit
At most 16 active loops per run; reversibly ended (suspended) loops keep their slot until stopped; ended loops free it. Short IDs are never reused in a run.

Evidence: `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_AtMostSixteenActive`, `TestLoop_EndedLoopsFreeTheirSlots`, `TestLoop_ReversiblyEndedLoopsKeepTheirSlots`; `afm:contracts/agentcomm/limits.go` `MaxActiveLoops`

### MESH-R83 List and stop
Agents list and stop only loops they created; the human lists and stops all. Stop is idempotent, works after a reversible end, persists across restart, and waits for an in-flight tick; an already delivered tick or running turn may finish.

Evidence: `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_StopOwnership`, `TestLoop_StopIsIdempotent`, `TestLoop_ListScopes`, `TestLoop_StopWorksAfterReversibleEnd`; `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_StopEndsTicks`, `TestLoopScheduler_StopWaitsForAnInFlightTick`; `afm:cmd/afm/run_mesh_loop_test.go` `TestLoopAdapters_StopIsTheCreatorsOrTheHumans`

### MESH-R84 Fixed slots and skips
Slots are anchored to the creation commit time. A slot is skipped (no journal write) when the target is busy, queued, held/paused, review-paused, or still has this loop's previous tick unread; a durable mesh wait does not block a tick. A late scheduler serves only the latest due slot; after restart it waits for the next future slot (no catch-up). Ticks never overlap.

Evidence: `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_TicksOnSlotsFixedToCreation`, `TestLoopScheduler_LateWakeServesTheLatestSlotOnly`, `TestLoopScheduler_SkipsABusyTargetWithoutJournalWrite`, `TestLoopScheduler_AHelperParkedOnAMeshWaitStillGetsTheTick`, `TestLoopScheduler_ReviewPauseSuspendsTicks`, `TestLoopScheduler_NoOverlapOfAnUnfinishedTick`, `TestLoopScheduler_RestartSkipsDowntimeAndWaitsForTheNextFutureSlot`; `engine:internal/orchestrator/agentcomm_loop_delivery_test.go` `TestLoopTick_SkippedSlotsLeaveNoTrace`

#### MESH-S81 Busy slot
- GIVEN a 5m loop and a target turn started at 12:05 still running at 12:10
- WHEN the 12:10 slot arrives
- THEN nothing is delivered or journaled; the 12:15 slot is delivered if the target is ready

### MESH-R85 Atomic tick delivery
A tick commits the message, delivery receipt, slot cursor and any helper restart in one operation under a deterministic, reserved operation id; retries never duplicate and a journal failure does not advance the cursor. A storage-fatal delivery ends the run; a refused tick is not fatal; a commit-time readiness loss is quiet.

Evidence: `engine:internal/orchestrator/agentcomm_loop_delivery_test.go` `TestLoopTick_RetryNeverDuplicates`, `TestLoopTick_IsRecordedUnderItsDeterministicOperationID`, `TestLoopTick_JournalFailureDoesNotAdvanceTheCursor`, `TestLoopTick_CommitTimeCheckLosesTheRaceQuietly`; `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_StorageFatalDeliveryEndsTheRun`, `TestLoopScheduler_ARefusedTickIsNotFatal`, `TestLoopScheduler_TickOperationIDsAreReserved`

### MESH-R86 Loop conversations
The first tick opens the loop's own thread, never reused by ordinary sends; ticks carry a `⟳ Loop Ln · every …` header and the creator's identity; replies link to the tick. A self-loop tick gives the agent a turn without an automatic reply (ordinary self-sends stay invalid). A helper-created loop outlives its creator. A full (200 messages) or closed thread ends the loop visibly.

Evidence: `engine:internal/orchestrator/agentcomm_loop_delivery_test.go` `TestLoopTick_DeliversOneLabelledMessageAndALinkedTurn`, `TestLoopTick_FirstTickOpensTheLoopsOwnThread`, `TestLoopTick_OwnThreadIsNeverReusedForAnOrdinarySend`, `TestLoopTick_RunAgentSelfLoopGetsATurnWithoutAReply`, `TestLoopTick_OrdinarySelfSendStaysRejected`, `TestLoopTick_HelperCreatedLoopOutlivesItsCreator`, `TestLoopTick_ClosedThreadEndsTheLoopVisibly`; `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_ThreadRoom`

### MESH-R87 Lifetime and end reasons
Run-agent loops last until the run ends unless bound to a stage; helper loops end with the helper's stage. Implicit ends (stage end, cancelled target, superseded helper/new stage attempt, finished flow) are derived from state, not journaled. Failed helpers or failed bound stages get no ticks; Retry can reactivate an unstopped loop. Loops never hold a finished flow open and get no ticks once every stage is terminal.

Evidence: `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_EndReasons`, `TestLoop_EndsWhenTheFlowIsFinished`, `TestLoop_CancelledRunAgentEndsItsLoop`, `TestLoop_NewStageAttemptSupersedesTheHelper`, `TestLoop_SupersededHelperEndsFinally`; `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_StageEndEndsABoundLoop`, `TestLoopScheduler_ResumesAfterAFailedStageIsRetried`, `TestLoopScheduler_DormantLoopsDoNotHoldTheFlowOpen`, `TestLoopScheduler_NoTickOnceEveryStageIsTerminal`

### MESH-R88 Helper reactivation by ticks
A tick restarts a helper at rest (finished one-shot, resting watcher, or parked on a mesh wait) reusing its task, participant and role; failed helpers, helpers with an open required gate, and on_completion helpers before their stage result are not restarted. Restart claims race safely against watcher checkpoint fires.

Evidence: `engine:internal/orchestrator/agentcomm_loop_helper_test.go` `TestLoopHelper_FinishedHelperRunsAgainOnEachEligibleSlot`, `TestLoopHelper_RestingWatcherRunsAgainOnEachEligibleSlot`, `TestLoopHelper_FailedHelperIsNotRestarted`, `TestLoopHelper_RequiredHelperWithOpenGateIsNotRestarted`, `TestLoopHelper_TickSkipsACompletionHelperBeforeItsStageResult`, `TestLoopHelper_RestartClaimsTheWatcherSoAConcurrentPollCannotAlsoFire`

### MESH-R89 Scheduler ownership and declared loops
The scheduler runs only in a run with collaboration enabled and not poisoned, is owned by `Run` (timers end with the run, not the HTTP request), and `Run` waits for an in-flight tick before returning. A helper's declared `loop:` is created atomically with the helper (creator `user`), counts toward the limit (over-limit rejects the helper), and is recreated per new generation.

Evidence: `engine:internal/orchestrator/collaboration_test.go` `TestStartCollaborationLoops_SchedulerOnlyForAnAssistantsRunThatIsNotPoisoned`, `TestRun_WaitsForAnInFlightLoopTickBeforeReturning`; `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_TimersEndWithTheRunNotTheRequest`, `TestLoopScheduler_CreateRacesRunFinalization`; `engine:internal/orchestrator/assistants_declared_loop_test.go` `TestDeclaredLoop_AtomicReplayStopAndNewGeneration`, `TestDeclaredLoop_LimitAtomicallyRejectsHelper`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E80 | Collaboration off | `feature_disabled` | `TestLoop_RejectedWhenAssistantsDisabled` |
| MESH-E81 | Ineligible/finished target, bad interval/prompt, ended bound | rejected with reason | `TestLoop_RejectsIneligibleTargets`, `TestLoop_RejectsBadInput` |
| MESH-E82 | 17th active loop | rejected | `TestLoop_AtMostSixteenActive` |
| MESH-E83 | Agent stops another's loop | `actor_not_allowed` | `TestLoop_StopOwnership` |
| MESH-E84 | CLI: both/neither `--prompt`/`--prompt-file` | `invalid_request`, never reaches service | `TestAgentLoopCLI_InvalidFlagsNeverReachTheService` |

## Coverage gaps and discrepancies
- **MESH-G80** (doc-only): dashboard `/loop` parsing (first `@handle` is target, `--until-stage-end`, `/loops` stays a message) is frontend behaviour, not covered by engine tests.
- **MESH-G81** (ambiguous): whole-flow `repeat` not carrying loops is doc-only here (`afm:docs/loops.md`).
