# Agent mesh: wait registration and admission

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `afm agent wait` / `wait-status`: conditions, ready vs yield, batches, deduplication.
- Admission refusals: unknown/closed targets, speculative waits, self-waits, unanswerable waits, cycles.

Siblings: how waits end (deadlines, cancellation, left-unanswered, owner recovery) —
[`../agent-mesh-wait-endings/spec.md`](../agent-mesh-wait-endings/spec.md); yield, parking and exit fence —
[`../agent-mesh-yield/spec.md`](../agent-mesh-yield/spec.md); core — [`../agent-mesh/spec.md`](../agent-mesh/spec.md).
On-completion helper start rules — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md). IDs: `MESH` R30–R39.

## Terms
- **Wait** — durable `mesh_wait_registered` record of a waiter on a condition (`pending`, `ready`, `cancelled`, `expired`, `run_closed`).
- **Batch** — one `reply_to_all` call: one `reply_to` wait per request sharing a `batch_id` and deadline.
- **Speculative wait** — a wait whose producer is not known yet (unpublished board key, unopened thread).

## Requirements

### MESH-R30 Wait never blocks
`afm agent wait` registers a durable wait and returns at once: `ready` if the condition already holds, otherwise `yield` (end the turn). Conditions: `reply_to`, `reply_to_all`, `addressed_message`, `task_result`, `board_key`, `stage_status`; a missing parameter yields a hint naming the flag.

Evidence: `afm:cmd/afm/agent_test.go` `TestAgentCLI_Wait_NeverBlocksAndReportsYieldOrReady`, `TestAgentWait_HelpExplainsOutcomes`; `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_WaitAlreadySatisfiedIsReadyImmediately`, `TestMesh_ReplyToWithoutAnyMessageIDKeepsTheConditionHint`; `engine:host/agent.go` `NewAgentWaitCmd`

#### MESH-S30 Reply already there
- GIVEN a request whose recipient already replied
- WHEN the sender registers `reply_to` on it
- THEN the receipt is `ready` and the turn continues

### MESH-R31 Condition semantics
A new `reply_to` wait is satisfied only by a reply from a recipient of the awaited message (the human's reply counts only to a message addressed to the human); legacy waits keep "any reply". `addressed_message` without a thread is the next message anyone sends the waiter; `task_result` wants a result after registration; `stage_status` resolves as soon as the stage is at that status.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_StageStatusWaitResolves`, `TestWakingExceptBatchAnswers_ReplyOfANonRecipientStillWakes`; `engine:internal/orchestrator/agentcomm_delivery_test.go` `TestTaskResultWait_RunAgentTurn`; `engine:internal/orchestrator/agentcomm_exit_fence_test.go` `TestWait_RetryOfALegacyReplyToWaitReplays`; `engine:internal/orchestrator/agentcomm_wait.go` `waitRules`; `afm:docs/collaboration-journal.md` (`recipient_reply_only`)

### MESH-R32 Target validation
Unknown message, task or stage → `not_found`; a closed thread or a stage already finished in another status → `invalid_status`; a speculative wait without `--deadline-seconds` → `invalid_request`. A condition that already holds is admitted regardless.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_WaitOnMissingOrClosedTargetRejected`, `TestMesh_WaitOnBoardKeyWithNoProducerRequiresDeadline`, `TestMesh_WaitAdmissionRejectsTerminallyImpossibleWaits`; `engine:internal/orchestrator/agentcomm_wait.go` `validateWaitTarget`, `requireDeadline`

#### MESH-S31 Unpublished board key
- GIVEN board key `k` was never published
- WHEN an agent waits on `board_key k` without a deadline
- THEN it is refused, telling it to pass `--deadline-seconds`

### MESH-R33 Self-only and unanswerable waits
A wait only the waiter itself could satisfy is refused (`wait_cycle`). A wait whose every possible answerer has finished, gave the message up as undeliverable, or left it unanswered is refused (`invalid_status`).

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_WaitAdmissionRejectsTerminallyImpossibleWaits`; `engine:internal/orchestrator/agentcomm_unread_test.go` `TestUnreadMessage_WaitOnMessageWithoutReaderIsRejected`; `engine:internal/orchestrator/agentcomm_left_unanswered_test.go` `TestLeftUnanswered_AdmissionRefusesAWaitOnAnUnansweredRequest`; `engine:internal/orchestrator/agentcomm_wait.go` `validateWaitAdmission`, `canStillAnswer`

### MESH-R34 Cycle detection
A wait is refused with `wait_cycle` when every answerable target already (transitively) waits on the waiter, or the chain exceeds 12 hops. A helper or run agent's pending waits count together (it resumes after all), a stage's separate waits are alternatives, a batch is one all-of unit, and parent→child delegation is an edge. The check runs under the commit lock, so of two concurrent cyclic waits one is admitted.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_WaitMutualCycleRejected`, `TestMesh_WaitCycleCheckRejectsChainLongerThanItsHops`, `TestMesh_WaitCycleCheckAnyGroupMemberMayAnswer`, `TestMesh_WaitCycleTaskRequiresAllPendingWaits`; `engine:internal/orchestrator/agentcomm_admission_test.go` `TestWait_ConcurrentCycleAdmitsOne`; `engine:internal/orchestrator/agentcomm_recovery_test.go` `TestMesh_ParentChildMeshWaitCycleRejected`, `TestMesh_WaitCycleAfterReplayUsesAllPendingWaits`

#### MESH-S32 Mutual wait
- GIVEN A waits `reply_to` on a message only B can answer
- WHEN B waits `reply_to` on a message only A can answer
- THEN B's wait is refused with `wait_cycle`

### MESH-R35 Stage dependencies in the wait graph
Unfinished `depends_on` of not-yet-started stages are edges (list built inside the commit from the registry plus journal dynamic stages): a dependency waiting on its dependent is `wait_cycle`, including an eagerly planning dependent; the decision is the same after restart.

Evidence: `engine:internal/orchestrator/agentcomm_wait_stage_deps_test.go` `TestWaitStageDeps_DependencyWaitingForItsPendingDependentIsACycle`, `TestWaitStageDeps_DependentThatPlansEagerlyIsStillACycle`, `TestWaitStageDeps_UsesTheRuntimeRegistry`, `TestWaitStageDeps_DecisionSurvivesARestart`

### MESH-R36 Own on-completion helper: "would never end"
A stage agent's wait on a not-yet-started `on_completion` helper of its stage (or dependents) is refused with `wait_cycle` and reason "this wait would never end: on_completion helper(s) … start only after stage(s) … produced a result". Callbacks of the stage's own one-shot and `on_message` helpers stay admissible; a started helper has no such edge; a refused batch stays refused.

Evidence: `engine:internal/orchestrator/agentcomm_wait_completion_test.go` `TestWaitCompletion_OwnHelperIsACycle`, `TestWaitCompletion_SpecificReason`, `TestWaitCompletion_BatchWithOneOwnHelperRequestIsRefused`, `TestWaitCompletion_CallbackEscapeOnlyForOwnHelpers`, `TestWaitCompletion_NoFalseCycle`, `TestWaitCompletion_AdmissionDecisionSurvivesRestart`; `engine:internal/orchestrator/agentcomm_wait.go` `completionCycleReason`

### MESH-R37 Batch waits (`reply_to_all`)
A batch takes 1–16 distinct requests sent by the waiter; registration is all-or-nothing (malformed list rejected by the CLI before the service; foreign, unknown, unanswerable or cyclic item refuses the whole set naming it). Early replies count. The waiter resumes once, after every item answered, was cancelled or expired; `wait-status` reports `pending` / `expired` / otherwise `ready` with per-item reasons. A batch survives restart.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_BatchWaitRegistersEveryRequestAndCountsEarlyAnswers`, `TestMesh_BatchWaitWakesTheStageOnlyAfterTheLastAnswer`, `TestMesh_BatchWaitMixOfAnswersAndCancellationsWakesOnce`, `TestMesh_BatchWaitReceiptOfAnsweredAndExpiredItemsIsExpired`; `engine:internal/orchestrator/agentcomm_admission_test.go` `TestMeshBatchWait_UnknownOrForeignRequestRefusesTheWholeSet`, `TestMeshBatchWait_CycleRefusesTheWholeSet`; `afm:cmd/afm/agent_test.go` `TestAgentWait_ReplyToAllRefusesAMalformedBatchBeforeTheService`, `TestAgentWaitStatus_ListsBatchesOrOpensOne`; `engine:internal/orchestrator/agentcomm_recovery_test.go` `TestMesh_BatchWaitRecoveryResumesTheStageOnceAfterTheLastAnswer`

#### MESH-S33 Partial answers
- GIVEN a stage parked on a batch of three requests
- WHEN two recipients reply
- THEN the stage stays parked; the third reply resumes it once

### MESH-R38 Batches versus single waits and questions
A stage's single waits remain alternatives beside a batch; a separate incoming question may give an early turn while the batch stays pending and the stage parks on the same batch.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_SingleWaitStaysAnAlternativeToABatch`, `TestMesh_PlainWaitsAreStillAnyOf`, `TestMesh_BatchWaitQuestionGivesAnEarlyTurnAndTheStageParksOnTheSameBatch`

### MESH-R39 Duplicate single waits
Repeating an equivalent pending single wait (same waiter and condition, any operation id) returns the original `wait_id`, `yield`, `already_pending: true`; it writes nothing and does not extend or arm a deadline. Concurrent equivalent registrations yield one wait. Batches are never merged.

Evidence: `engine:internal/orchestrator/agentcomm_wait_dedup_test.go` `TestWait_EquivalentPendingWaitUnderAnotherOperationIsTheSameWait`, `TestWait_DuplicateNeverArmsADeadline`, `TestWait_ConcurrentEquivalentRegistrationsYieldOneWait`, `TestWait_SingleWaitNextToABatchOnTheSameMessageIsItsOwnWait`; `afm:cmd/afm/agent_test.go` `TestAgentWait_RepeatedSingleWaitReportsTheSameWaitAsAlreadyPending`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E30 | Unknown message/task/stage | `not_found` | `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_WaitOnMissingOrClosedTargetRejected` |
| MESH-E31 | Closed thread; stage finished otherwise | `invalid_status` | same |
| MESH-E32 | Speculative wait, no deadline | `invalid_request` "(pass --deadline-seconds)" | `TestMesh_WaitOnBoardKeyWithNoProducerRequiresDeadline` |
| MESH-E33 | Only the waiter could satisfy | `wait_cycle` "would wait on itself forever" | `engine:internal/orchestrator/agentcomm_wait.go` |
| MESH-E34 | Cycle / >12 hops | `wait_cycle` | `TestMesh_WaitMutualCycleRejected` |
| MESH-E35 | Own pending on_completion helper | `wait_cycle` "this wait would never end: …" | `TestWaitCompletion_SpecificReason` |
| MESH-E36 | Batch item not sent by waiter | `actor_not_allowed` | `engine:internal/orchestrator/agentcomm_wait.go` `validateBatchAdmission` |
| MESH-E37 | `board_key` in `assistants` mode | `feature_disabled` | `engine:internal/orchestrator/agentcomm_board_access_test.go` `TestBoardOperationsRefusedInAssistantsMode` |

## Coverage gaps and discrepancies
- **MESH-G30** (code≠doc): the service knows no `reply_to_all` kind; the CLI maps it to `reply_to` + `message_ids`, and `reply_to --message-ids` is also a batch (`afm:cmd/afm/agent_test.go` `TestAgentWait_ReplyToWithMessageIDsIsABatchToo`); docs mention only `reply_to_all`.
- **MESH-G31** (ambiguous): dedup is decided per waiter+condition; a retry of a dedup-answered operation id after the first wait ended registers a new wait (no event was written) — by design in `alreadyPendingWait`, not tested directly.
