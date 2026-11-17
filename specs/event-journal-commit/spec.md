# Event journal: commit rules, notices and redaction

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How a change becomes durable: CAS transitions and their fences (epoch, stage seq, generation, fence
  predicate), multi-effect operations with idempotency, durable intent before returning to the API, and the
  split between benign CAS misses and storage-fatal failures.
- The `notices.jsonl` side journal for non-FSM UI notices, and redaction of secrets before persistence.
- Part of the event-journal family: storage, locking and replay — [event-journal](../event-journal/spec.md).
- Out of scope: which commands exist and their statuses ([stage-lifecycle](../stage-lifecycle/spec.md)), helper
  and mesh payload rules ([helpers](../helpers-lifecycle/spec.md), [agent-mesh](../agent-mesh/spec.md)).

## Terms
- **CAS miss** — `state.ErrConcurrentChange`: the precondition no longer holds; nothing is written.
- **Storage-fatal** — a failed journal write/fsync (`*StorageError`), or a committed record that the live
  projection cannot apply.
- **Operation** — `Store.ApplyRunOperation`: one `operation_committed` control record carrying several effects.
- **Epoch** — per-stage execution counter of turn ownership; **generation** — per-stage retry counter.

## Requirements

### LOG-R20 Stage transitions are CAS under the Store lock
`Store.Apply` commits only if the stage is in `From`; otherwise it returns `ErrConcurrentChange` and writes
nothing (a stale `From` cannot leave `done`). `Seq` and `Time` are assigned by the commit itself.

Evidence: `engine:internal/state/store_test.go` `TestApply_RejectsWrongFrom`,
`TestApply_StaleFromAgainstDone_RejectedAndStaysDone`; `engine:internal/orchestrator/bus/fsm_test.go`
`TestFSM_CasRejectsStaleTransition`; `engine:internal/state/store.go` `applyTransition`.

### LOG-R21 Fences are checked inside the commit
Optional `ExpectEpoch`, `ExpectStageSeq`, `Fence` (a predicate over the projection) and the budget's
`Generation`/hold conditions are evaluated under the same lock immediately before writing; a failed fence is a
CAS miss with an empty journal effect. Turn results act only while the turn still owns its epoch.

Evidence: `engine:internal/state/transition_fence_test.go` `TestExitOnFailTransitionFence`;
`engine:internal/state/stage_execution_test.go` `TestStageEpoch_FencedTransition`,
`TestStageEpoch_CountsTakeoversAndReplays`; `engine:internal/state/usage_limit_test.go`
`TestQuotaPark_FencesApplyInStore`; `engine:internal/state/operation_proposal_precondition_test.go`
`TestOperation_StageGenerationPreconditionRejectsStale`; `engine:internal/state/budget_pause_test.go`
`TestBudgetPause_ZeroEpochIsAFence`; `engine:internal/orchestrator/AGENTS.md`.

#### LOG-S20 Rejecting fence
- GIVEN stage `a` is `pending`
- WHEN `Apply(pending → failed)` carries a fence returning false
- THEN it returns `ErrConcurrentChange`, `a` stays `pending` and `LastSeq` stays 0.

### LOG-R22 Only the CAS winner starts work
Side effects of a transition (spawning agents, after-hooks, clearing sessions, announcing/waking on a wait)
run only for the caller whose commit succeeded; a CAS miss is a no-op, not an error for the run. Concurrent
competing commands have exactly one winner.

Evidence: `engine:internal/orchestrator/race_fixes_test.go` `TestCompleteStage_LostCASSkipsSideEffects`,
`TestRetry_LostCASDoesNotClearInteractiveSessions`; `engine:internal/orchestrator/approval_test.go`
`TestConditionalApproval_ConcurrentReviseHasOneWinner`; `engine:internal/orchestrator/agentcomm_wait_winner_test.go`
`TestWaitResolution_OnlyTheWinnerAnnouncesAndWakes`; `engine:internal/orchestrator/fatal_test.go`
`TestTrigger_ConcurrentChangeIsBenign`; `engine:internal/orchestrator/exit_on_fail_internal_test.go`
`TestExitOnFailCASMissIsNotAFailure`.

### LOG-R23 User intent is durable before the command returns
Approve, revise, retry and similar commands commit their transition (fsync) before returning to the caller, so a
crash right after the response does not lose the intent; recovery continues from the committed state.

Evidence: `engine:internal/orchestrator/approve_test.go` `TestApproveStage_DurableTransition`,
`TestRevise_DurableTransition`; `engine:internal/orchestrator/discussion_chat_test.go`
`TestDiscussion_InterruptIntentIsDurableAndReplayed`; `afm:docs/stage-lifecycle.md`;
`afm:docs/internals/event-log.md` "Durable intent".

#### LOG-S21 Approve then crash
- GIVEN a stage in `awaiting_approval`
- WHEN approve returns and the process dies immediately
- THEN the reopened journal shows the stage approved (`ready` or later).

### LOG-R24 Storage failure ends the run; CAS miss does not
A journal write/fsync failure is returned as a typed `*StorageError`, never as a rejection or idempotency
conflict. The orchestrator records the first fatal error and cancels the run; it is preferred over other stop
reasons. A storage failure on conditional approval is reported as storage, not as a conflict.

Evidence: `engine:internal/state/journal_regression_test.go` `TestStorageFatalEndsRun`;
`engine:internal/state/record_test.go` `TestAppendControl_StorageErrorIsTyped`;
`engine:internal/orchestrator/fatal_test.go` `TestSetFatal_RecordsFirstErrorAndCancels`;
`engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_StorageFatalDeliveryEndsTheRun`;
`engine:internal/orchestrator/exit_on_fail_internal_test.go` `TestExitOnFailStopErrorPrefersStorageFatal`;
`engine:internal/orchestrator/approval_test.go` `TestConditionalApproval_StorageFailureIsNotAConflict`.

### LOG-R25 Writes after Close are benign
After `Store.Close`, `Apply`, `AppendControl`, operations and run events return `ErrStoreClosed` without touching
the file, so a shutdown race is not classified as storage-fatal.

Evidence: `engine:internal/state/store_test.go` `TestApply_AfterClose_ReturnsErrStoreClosed`;
`engine:internal/state/record_test.go` `TestAppendControl_AfterCloseReturnsError`; `engine:internal/state/store.go`
`ErrStoreClosed`.

### LOG-R26 Control records are validated before they are written
A control record is checked against the live projection (and budget fold) before the write; an inadmissible
record is rejected and never reaches the journal, so replay cannot be poisoned by it. Format limits are checked
on admission only, never on replay.

Evidence: `engine:internal/state/operation_test.go` `TestOperation_StaleRevisionRejected`;
`engine:internal/state/mesh_board_test.go` `TestMesh_BoardStaleExpectedVersionRejected`;
`engine:internal/state/store.go` `appendControlLocked`; `afm:docs/collaboration-journal.md` "Board task records".

### LOG-R27 Operations are atomic and idempotent
All effects of an operation commit in one record or none. Repeating an operation id with the same content returns
the prior result — also after restart and after the result cache evicted it; the same id with different content
is rejected. While the run is finalizing, operations are rejected `run_finalizing`; the finalize decision and
admission share one lock. External work runs outside the Store lock; concurrent writers are serialized.

Evidence: `engine:internal/state/operation_test.go` `TestOperation_AllEffectsCommittedTogether`,
`TestIdempotency_SameIdSameContentReturnsPriorResult`, `TestIdempotency_SameIdDifferentContentRejected`,
`TestIdempotency_SurvivesRestart`, `TestIdempotency_ReplayAfterEvictionAndRestart`,
`TestOperation_RejectedWhenRunFinalizing`, `TestFinalizeIf_DecidesUnderTheAdmissionLock`,
`TestOperation_DoesNotHoldLockDuringExternalWork`, `TestOperation_ConcurrentWritersSerialized`;
`engine:internal/state/operation.go` `ApplyRunOperation`.

#### LOG-S22 Double click from two tabs
- GIVEN two identical requests with the same operation id
- WHEN both reach the Store
- THEN one record is committed and both callers get the same result.

### LOG-R28 Non-FSM notices go to notices.jsonl
UI notices that are not FSM transitions are published live and appended as one JSON line
(`time`, `type`, `stage_id`, `data`) to the run-level `<run>/notices.jsonl` for reconnecting clients. The append is
best-effort (errors dropped) except `WriteNotice`, which reports failure for notices whose live copy must not
outrun the durable one; writes are serialized process-wide. Notices never change stage state.

Evidence: `engine:internal/orchestrator/stagefiles/notices_test.go` `TestAppendNotice`,
`TestAppendNotice_ConcurrentWritesNoCorruption`; `engine:internal/orchestrator/summary_recovery_test.go`
`TestSummaryRecovery_DurableNoticesOncePerEvent`; `engine:internal/orchestrator/notices.go` `publishNotice`;
`engine:internal/engine/stage_control.go` `publish`; `engine:internal/orchestrator/stagefiles/notices.go`.

### LOG-R29 Secrets are redacted before persistence
Known secret values are redacted before a record is committed: mesh message bodies, thread titles, task
titles/instructions/results, loop prompts, board task texts, failure reasons. Mesh notices carry ids, not bodies,
so secrets reach neither the live bus nor `notices.jsonl`. A record committed earlier replays unchanged when a new
secret appears later.

Evidence: `engine:internal/orchestrator/agentcomm_messages_test.go` `TestMesh_SendRedactsSecretsInBody`,
`TestMesh_OpenThreadRedactsSecretBeforeCommit`, `TestMesh_UpdateThreadTitleRedactsSecretBeforeCommit`;
`engine:internal/orchestrator/redaction_test.go` `TestRedaction_TaskCreatedTitleAndInstruction`,
`TestRedaction_TaskResult`; `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_PromptIsRedacted`;
`engine:internal/orchestrator/agentcomm_board_tasks_messages_test.go` `TestBoardTaskRedactsSecrets`;
`engine:internal/orchestrator/exit_on_fail_internal_test.go` `TestExitOnFailSecretsAreRedacted`;
`engine:internal/orchestrator/agentcomm_limits_test.go` `TestMesh_SecretsAbsentFromLiveNoticeAndNoticesJournal`;
`engine:internal/orchestrator/replay_test.go` `TestReplay_SendSurvivesANewSecret`.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| LOG-E20 | `From`/fence/epoch/generation mismatch | `ErrConcurrentChange`, nothing written | `TestApply_RejectsWrongFrom` |
| LOG-E21 | Write/fsync failure | `*StorageError`, run cancelled with first fatal | `TestStorageFatalEndsRun` |
| LOG-E22 | Write after `Close` | `ErrStoreClosed` (benign) | `TestApply_AfterClose_ReturnsErrStoreClosed` |
| LOG-E23 | Same op id, different content | rejected (`ErrOperationIDConflict`) | `TestIdempotency_SameIdDifferentContentRejected` |
| LOG-E24 | Operation while finalizing | `OperationRejectedError{Code: run_finalizing}` | `TestOperation_RejectedWhenRunFinalizing` |

## Coverage gaps and discrepancies
- **LOG-G20** (ambiguous): `afm:AGENTS.md` asks for non-FSM notices that survive reconnect to be durable, but
  `AppendNotice` drops write errors and does not fsync; only `WriteNotice` callers learn about failure.
- **LOG-G21** (untested): `Store.Apply` returns plain errors on write/fsync failure; `bus.FSM` wraps them as
  `*StorageError`. The only transition write-fault injection (`SetJournalWriteFault`) is in
  `engine:internal/orchestrator/approval_test.go`; an end-to-end "transition write fails → run ends" test for the
  general scheduling path is absent (only `setFatal` unit and control-record paths are covered).
