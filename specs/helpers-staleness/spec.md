# Stage helpers: stale, duplicate and invalid results

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- When a helper turn's outcome is accepted, discarded or turned into a requeue (turn ownership).
- Late results after Pause, Cancel, supersession, stage completion; CAS misses and duplicates.
- Answer validation and how a turn's outcome is classified.
- Out of scope: what an accepted commit contains and who is told → [helpers-results](../helpers-results/spec.md);
  root generations and Revise → [helpers-barrier](../helpers-barrier/spec.md); output repair internals →
  [verify-verdict](../verify-verdict/spec.md).

## Terms
- **Turn ownership**: the task is `running`, with the same `Turn`, `HoldEpoch`, `TaskGeneration` and `RootGeneration`
  as when the turn started, its root generation is the stage's current one, and it carries no hold other than a budget hold.
- **Finalized by other**: the task became terminal (Cancel, supersession) while its process ran.

## Requirements

### HLP-R40 Every outcome commits under turn ownership
Result, delegation, protocol error, process failure, usage-limit park, budget park and yield park each commit only if
turn ownership holds, checked inside the Store lock as the operation's precondition (`helperTurnGuard`). A Pause →
Continue in between is detected through `HoldEpoch`. A formatted (repaired) answer is also fenced by an ancestor
hold; a locally complete answer only by the task's own hold.

Evidence: `engine:internal/orchestrator/output_recovery_test.go` `TestHelperTurnCommit_PausedTurnNeverCommits`,
`TestHelperOutputRecovery_AncestorHoldAllowsLocalResult`, `TestHelperOutputRecovery_AncestorHoldFencesFormattedResult`;
`engine:internal/orchestrator/assistants_budget_test.go` `TestHelperOutputOwned_IgnoresBudgetHold`; code `helperOutputOwned`.

#### HLP-S80 Pause + Continue during the answer
- GIVEN a turn that produced a valid answer
- WHEN the task is paused and continued before the commit
- THEN the old turn commits nothing; the task is requeued and a new turn runs.

### HLP-R41 What happens to a discarded outcome
A rejected ownership guard on a still-live task requeues it (`re-queued: paused while running`); the answer is not
recorded and `result.json` keeps its previous content. If the task was cancelled or superseded, the late outcome adds
no status and no result. After a root-generation bump the old task stays `cancelled`, its late `pass` does not count
for the new attempt's gate, and an on_message request it was working on gets no outcome.

Evidence: `engine:internal/orchestrator/assistants_lifecycle_outcomes_test.go` `TestHelperTurnOutcomes_CancelledByOtherWins`;
`engine:internal/orchestrator/assistants_cascade_test.go` `TestLateResult_AfterGenerationBump_DoesNotResurrectCancelledTask`;
`engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_NewStageAttemptCancelsTheHelperAndFencesItsLateResult`;
`engine:internal/orchestrator/assistants_test.go` `TestPause_ImmediateContinueBeforeClassificationStillRequeues`.

#### HLP-S81 Cancel races the answer
- GIVEN a running turn
- WHEN CancelTask commits `cancelled` and then the process returns a `pass`
- THEN the task stays `cancelled` with no `task_result`.

### HLP-R42 Stage status does not revoke ownership
A stage reaching `done` or `failed` does not invalidate a running helper turn: its result is committed (a one-shot
completes; an on_message request is answered) and never reopens or changes the stage. Only a root-generation bump, a
hold or a cancel fences it.

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestParentDone_ClosesNewTriggers`;
`engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_AcceptedWorkFinishesAfterTheStageIsDone`,
`TestOnMessage_FailedStageFinishesAcceptedWorkAndRetryReopens`.

#### HLP-S82 Answer after a stage failure
- GIVEN an on_message request running
- WHEN its stage fails and then the turn returns a result
- THEN the result and reply are committed and the stage stays `failed`.

### HLP-R43 CAS misses are no-ops, storage errors are fatal
Every task transition is an operation with a fresh random id and an expected-from status. Losing the race (status
moved, task held/terminal, closable condition gone) is a silent no-op: the winner already acted. A
`*state.StorageError` ends the run. Scheduling spawns only for the winner of `queued → running`; a child finishing
concurrently with siblings resumes its parent once.

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestFinishTaskFrom_StorageErrorSetsFatal`,
`TestLostWakeup_ContinueDuringRequeueStillSchedules`, `TestPauseTask_SurvivesImmediateRequeueRace`;
`engine:internal/orchestrator/assistants_crash_recovery_test.go` `TestDuplicateChildResult_OnlyResumesParentOnce`;
code `handleTaskOperationErr`, `maybeScheduleTask`.

#### HLP-S83 Two schedulers race
- GIVEN a queued task
- WHEN Continue and a requeue both call scheduling
- THEN exactly one `start_task` commits and one process starts.

### HLP-R44 Answer acceptance
An answer must decode strictly as one `schema_version:1` object of `kind:"result"` (or `"delegate"` for a
delegate-capable profile). A required task must give a verdict; an advisory one may omit it (the engine never invents
one). Bounded local repair, then a format-only model call, may fix format, but the candidate is accepted only if it keeps
verdict, findings and recommendations; accepted repairs are marked `repaired` with a `helper_result_repaired` notice. A
`pass` while a required child is open is rejected. The server stamps `ContextVersion` and redacts the result. A rejected
answer fails the turn with *result contract not met* and keeps the raw answer.

Evidence: `engine:internal/orchestrator/assistants_optional_verdict_test.go`
`TestHelperOptionalVerdict_RequiredWithoutVerdictKeepsTheGateClosed`, `TestHelperOptionalVerdict_NeutralResultIsTheSameOnEveryPath`,
`TestCommitHelperResult_RequiredGateRejectsAVerdictlessAnswerByItself`; `engine:internal/orchestrator/assistants_test.go`
`TestLifecycle_MalformedResult`, `TestLifecycle_RepairedPassClosesRequiredGate`;
`engine:internal/orchestrator/assistants_continuation_test.go` `TestContinuation_ParentPassRejectedWithOpenRequiredChild`.

#### HLP-S84 Required answer without verdict
- GIVEN a required one-shot helper
- WHEN it answers `{"schema_version":1,"kind":"result","summary":"ok"}`
- THEN the task is `failed` (*result contract not met*) and the stage gate stays open.

### HLP-R45 Outcome classification order
A finished helper process is classified in this order: run shutdown cut the turn short → task stays `running` (retried
on resume); budget refused the next invocation → `queued`; provider usage limit → `queued` with a quota park (or
paused if it cannot be waited out); runner error → `failed`; interrupted or finalized by other → nothing; timeout →
`failed`; process not OK while paused/clarifying/held → requeue, else `failed`; turn yielded into a pending mesh wait
→ `waiting_peer`; protocol error → `failed`; otherwise the answer goes to acceptance (HLP-R44). Native-schema mode: a
forced gate-closing or undecodable payload in a non-yielding turn that the model's last text does not confirm is re-run
once with the schema in the prompt only.

Evidence: `engine:internal/orchestrator/assistants_lifecycle_outcomes_test.go` `TestHelperTurnOutcomes_TerminalStatusAndReason`,
`TestHelperTurnOutcomes_InterruptedLeavesTaskRunning`, `TestHelperTurnOutcomes_ShutdownBeatsFailureAndQuota`,
`TestHelperTurnOutcomes_PreparationFailureSkipsAgent`; `engine:internal/orchestrator/assistants_optional_verdict_test.go`
`TestHelperNativeRerun_ByTaskContract`; `engine:internal/orchestrator/agentcomm_wait_helper_edge_test.go`
`TestHelperTurn_AlreadyPendingWaitThatEndsBeforeExitIsStillAYield`; code `classifyHelperFailure`.

#### HLP-S85 Shutdown beats failure
- GIVEN a helper turn running
- WHEN the run is interrupted and the process returns an error or a usage limit
- THEN the task stays `running` with no quota park and no failure, to be requeued on resume.

### HLP-R46 Observations never change a verdict
A project-tree fingerprint change across a helper turn publishes `helper_tree_modified` naming the helper; it never
alters the task's status or verdict. Activity entries are best-effort and never change status or the gate.

Evidence: `engine:internal/orchestrator/assistants_workspace_test.go` `TestHelperTreeModified_NamesTheAgentAndKeepsVerdict`;
`engine:internal/orchestrator/assistant_activity_fsm_test.go` `TestAssistantActivity_DoesNotChangeBusinessEvents`; code `runHelperTask`.

#### HLP-S86 Helper edits files
- GIVEN a helper whose turn changes the working tree and answers `pass`
- WHEN the turn commits
- THEN the verdict stays `pass` and the feed gets a tree-modified notice.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E30 | Ownership lost before commit | no record; live task requeued | `TestHelperTurnCommit_PausedTurnNeverCommits` |
| HLP-E31 | Invalid / verdictless required answer | task `failed`, raw kept, on_message: request `failed` | `TestLifecycle_MalformedResult` |
| HLP-E32 | `pass` with open required child | turn rejected as contract not met | `TestContinuation_ParentPassRejectedWithOpenRequiredChild` |
| HLP-E33 | Journal write failure on any task op | run ends storage-fatal | `TestFinishTaskFrom_StorageErrorSetsFatal` |

## Coverage gaps and discrepancies
- **HLP-G30** (code≠doc): `docs/collaboration.md` says a later result of an earlier attempt "is ignored"; for a Revise
  of a running stage nothing is superseded (see HLP-G01 in [helpers-barrier](../helpers-barrier/spec.md)) and the
  result counts.
- **HLP-G31** (untested): HLP-R42 for a one-shot whose stage *failed* (not done) has no dedicated test.
- **HLP-G32** (code≠comment): `ErrTaskContextTooLarge` says admission is rejected; in code it fails the turn at
  preparation (`prepareHelperTurn`), after the task exists.
