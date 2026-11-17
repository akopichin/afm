# Stage helpers: required barrier, waivers and task controls

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How required helpers gate stage completion (`awaiting_assistants`) and when the gate is re-checked; the
  `wait_cycle` refusal of a stage agent waiting on its own not-yet-started on_completion helper.
- What a helper failure means for its stage; Revise and root generations; waivers.
- Task Pause/Continue/Cancel/Retry, delegation, collaboration disabled.
- Out of scope: kinds, triggers, status table → [helpers-lifecycle](../helpers-lifecycle/spec.md); on_message gate
  details → [helpers-on-message](../helpers-on-message/spec.md); result commit →
  [helpers-results](../helpers-results/spec.md); staleness → [helpers-staleness](../helpers-staleness/spec.md);
  restart → [helpers-recovery](../helpers-recovery/spec.md);
  stage FSM and exit_on_fail → [stage-lifecycle](../stage-lifecycle/spec.md).

## Terms
- **Gate closed**: the task is `completed` and its last committed verdict is `pass`, or it is waived.
- **Required chain**: a task is reached from the stage only through `required` links; an advisory link cuts the chain.
- **Root generation**: per-stage counter; only tasks of the current one are considered by the barrier.

## Requirements

### HLP-R08 No wait on one's own pending on_completion helper
A stage agent's wait that could only end after its own not-yet-started `on_completion` helper (directly, in a batch,
or through other agents or dependent stages) is refused with `wait_cycle` before it is recorded; nothing is parked.

Evidence: `engine:internal/orchestrator/agentcomm_wait_completion_test.go` `TestWaitCompletion_OwnHelperIsACycle`,
`TestWaitCompletion_BatchWithOneOwnHelperRequestIsRefused`, `TestWaitCompletion_NoFalseCycle`; code `completionPassPending`.

#### HLP-S09 Refused wait does not park the stage
- GIVEN a stage agent with a pending on_completion helper
- WHEN it runs `afm agent wait --condition task_result` on that helper
- THEN the call is refused `wait_cycle`, no wait is recorded, and the stage stays `running`.

### HLP-R10 Required-helper barrier
When the stage executor finishes (after verify), completion checks `RequiredBlocker`: it walks the stage's current-
generation top-level tasks and, through required edges only, their current children; the first task whose gate is not
closed blocks. If one blocks, the stage commits `awaiting_assistants` (author and verify are not rerun); otherwise it
commits `done` through the normal cascade. The `done` commit re-checks the gate under the Store lock. Advisory tasks
and everything below an advisory link never block. The barrier is honoured even with collaboration disabled.

Evidence: `engine:internal/orchestrator/assistants_barrier_test.go` `TestBarrier_GenerationAwareCas`,
`TestAttachRequiredRacesWithComplete`, `TestCompletionGoesThroughCascade`, `TestBarrier_AfterHookNotRunEarly`,
`TestBarrier_VerifyStillIndependentGate`, `TestCollaborationDisabledBeforeResumeDoesNotSkipRequirement`,
`TestBarrier_AdvisoryCompletionHelperVerdictDoesNotHoldTheStage`; `afm:contracts/assistants/tree.go` `RequiredGateOpen`,
`TaskGateClosed`; code `completeStageFenced`.

#### HLP-S05 needs_changes keeps the stage waiting
- GIVEN a required helper whose last result is `needs_changes`
- WHEN the stage agent finishes
- THEN the stage is `awaiting_assistants` and `NeedsHumanDecision` is true until a pass or a waiver.

### HLP-R11 Barrier re-check points
`maybeCompleteAfterAssistants` re-attempts completion after every task terminal transition, after a waiver, on
startup and on Continue of an `awaiting_assistants` stage; it is a no-op for any other stage status.

Evidence: `engine:internal/orchestrator/assistants_barrier_test.go` `TestBarrier_RestoredAfterRestart`,
`TestContinueDoesNotDoubleSpawnOrBypass`; `engine:internal/orchestrator/assistants_waiver_test.go`
`TestWaiver_UnblocksStageThroughNormalCascade`; code `finishTaskTurnRequest` (afterFinish), `maybeCompleteAfterAssistants`.

#### HLP-S11 Late pass after awaiting_assistants
- GIVEN a stage in `awaiting_assistants` blocked by one required task
- WHEN that task commits `completed` with verdict `pass`
- THEN the same commit's follow-up re-runs completion and the stage becomes `done`, then dependents start.

### HLP-R12 Failure of a helper versus the stage
An advisory helper's failure never changes the stage. A failed/cancelled non-waived required task leaves the stage in
`awaiting_assistants` needing a human decision (Retry task, Waive, Revise). Only with `exit_on_fail`, completion
instead fails the stage with `required assistant failed: <task> (<status>)`, re-checked at commit; a later stage Retry
bumps the root generation so the dead task no longer blocks.

Evidence: `engine:internal/orchestrator/exit_on_fail_internal_test.go` `TestExitOnFailRequiredAssistantDuringWait`,
`TestExitOnFailAdvisoryAssistant`, `TestExitOnFailWaivedAssistant`, `TestExitOnFailBarrierRetryStartsNewGeneration`;
`engine:internal/orchestrator/watch_tasks_test.go` `TestWatcherUI_AdvisoryErrorIsNotBlockingAttention`;
code `failOnDeadRequiredAssistant`, `finishBarrierFailureBump`.

#### HLP-S12 Dead required helper without exit_on_fail
- GIVEN `exit_on_fail` off and a required helper that ended `failed`
- WHEN the stage agent finishes
- THEN the stage stays `awaiting_assistants` with `NeedsHumanDecision` true; nothing fails the stage.

#### HLP-S13 Dead required helper with exit_on_fail
- GIVEN `exit_on_fail` on and the same failed required helper
- WHEN completion is attempted
- THEN the stage is `failed` with reason `required assistant failed: <task> (failed)`.

### HLP-R13 Revise and the root generation
Revise from `awaiting_assistants` bumps the stage's root generation (key: the Revise's seq), cancels the stage's
pending mesh waits in the same record, then cancels every non-terminal task of older generations with descendants;
earlier verdicts stop counting and declared helpers re-attach as new tasks. Revise from any other status bumps nothing.

Evidence: `engine:internal/orchestrator/revise_awaiting_assistants_test.go` `TestRevise_InvalidatesPriorPass`,
`TestRevise_CancelsStaleAssistantTurns`; `engine:internal/orchestrator/revise_bump_recovery_test.go`
`TestReviseRecovery_ReviseOfARunningStageDoesNotBump`; `engine:internal/orchestrator/assistants_declared_test.go`
`TestDeclaredAssistants_ReviseReattaches`; code `reviseAwaitingAssistants`, `bumpRootGenerationKeyed`.

#### HLP-S14 Revise from awaiting_assistants supersedes the old pass
- GIVEN a required helper that passed and a stage in `awaiting_assistants`
- WHEN the user Revises the stage
- THEN the root generation advances, open old tasks are `cancelled`, and the old `pass` no longer closes the new
  attempt's gate.

### HLP-R14 Waiver
Only a `user` actor may waive, with a non-empty reason; the waiver is its own durable record, idempotent, closes the
task's gate regardless of status/verdict, and unblocks the stage only through the normal completion path.

Evidence: `engine:internal/orchestrator/assistants_waiver_test.go` `TestWaiver_RequiredByUserOnly`,
`TestWaiver_IsSeparateAuditedOperation`, `TestWaiver_RecordedWithActorAndReason`; code `WaiveTask`.

#### HLP-S15 Waiver completes the stage
- GIVEN a required helper with `needs_changes` and the stage in `awaiting_assistants`
- WHEN a user waives it with a reason
- THEN a `task_waived` record is written and the stage becomes `done` through the normal cascade.

### HLP-R15 Task Pause, Continue, Cancel, Retry
Pause puts a hold on the task (whole subtree via ancestor walk) and interrupts live turns; an interrupted turn returns
to `queued`, not `failed`. Continue clears only that task's own manual hold and reschedules. Cancel marks the task and
its non-terminal descendants `cancelled`, keeping terminal history. RetryTask accepts only `failed`, bumps the task
generation, cancels its non-terminal descendants, and requeues it.

Evidence: `engine:internal/orchestrator/assistants_cascade_test.go` `TestPause_CascadesToSubtree`,
`TestContinue_DoesNotClearOtherHolds`, `TestCancel_CascadesAndKeepsHistory`, `TestRetryTask_RejectsNonFailedTask`,
`TestRetryTask_InvalidatesOwnSubtreeNotSiblings`; `engine:internal/orchestrator/assistants_test.go`
`TestPause_RunningTaskRequeuesForResumeInsteadOfFailing`.

#### HLP-S16 Pause of a running helper
- GIVEN a helper turn running
- WHEN the user pauses the task
- THEN the process is interrupted, the task is `queued` with a hold (not `failed`), and Continue runs a new turn.

### HLP-R16 Delegation
A `delegate`-capable profile may answer `kind:"delegate"` (≤8 children, no `on_message`); children are admitted
all-or-nothing in the same commit that moves the parent to `waiting_children` (slot released). The parent resumes
once, when every current-generation child is terminal (any outcome). A delegation never closes a gate; a parent `pass`
with an open required child is rejected. A rejected delegation is returned to the model as a follow-up; the third
consecutive rejection fails the task (an on_message helper fails only the request).

Evidence: `engine:internal/orchestrator/assistants_continuation_test.go` `TestWaitingChildren_PersistedWithExpectedSet`,
`TestWaitingChildren_FailedChildDelivered`, `TestContinuation_ParentPassRejectedWithOpenRequiredChild`,
`TestLease_ReleasedBeforeWaitingChildren`; `engine:internal/orchestrator/assistants_reject_test.go`
`TestReject_RepeatedDelegationIsBoundedNotInfinite`, `TestReject_NoSilentPartialExecution`;
`engine:internal/orchestrator/assistants_crash_recovery_test.go` `TestDuplicateChildResult_OnlyResumesParentOnce`.

#### HLP-S17 Parent resumes once
- GIVEN a parent in `waiting_children` with two children
- WHEN both children finish at the same time (one `failed`)
- THEN the parent is resumed exactly once and sees both outcomes.

### HLP-R17 Collaboration disabled
With collaboration disabled Attach is rejected `feature_disabled`, declared helpers are skipped with a warning (the
stage runs), and no task is scheduled; existing durable requirements still gate completion (HLP-R10).

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestAttach_RejectedWhenDisabled`;
`engine:internal/orchestrator/assistants_declared_test.go` `TestDeclaredAssistants_DisabledCollaborationIsNoOp`.

#### HLP-S18 Declared helper with collaboration off
- GIVEN `AFM_COLLABORATION=0` and a stage declaring a helper
- WHEN the stage starts execution
- THEN no task is created, a warning is logged, and the stage runs normally.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E04 | Stage agent waits on own pending on_completion helper | `wait_cycle`, wait not recorded | `TestWaitCompletion_SpecificReason` |
| HLP-E05 | Waiver by non-user actor | `actor_not_allowed`, gate stays open | `TestWaiver_RequiredByUserOnly` |
| HLP-E06 | RetryTask on non-failed task | `invalid_status` | `TestRetryTask_RejectsNonFailedTask` |

## Coverage gaps and discrepancies
- **HLP-G01** (code≠doc): `docs/collaboration.md` ("When it is closed") says Restart with note cancels the earlier
  attempt's helper; code bumps the generation only for Revise from `awaiting_assistants` (and barrier-failure Retry).
  A Revise of a running stage keeps all helpers (`TestReviseRecovery_ReviseOfARunningStageDoesNotBump`).
- **HLP-G05** (ambiguous): system cancellations (generation supersession) reuse reason `cancelled by user` (`cancelOneTask`).
- **HLP-G06** (untested): the consecutive-delegation-rejection counter is in memory only and resets on restart.
- **HLP-G07** (untested): `WaiveTask` does not check `mode: required`; waiving an advisory task is accepted.
