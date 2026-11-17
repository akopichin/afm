# Agent mesh: yield, parking and exit fence

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- What a turn that ended by yielding into `afm agent wait` does: stage `awaiting_peer`, task `waiting_peer`.
- Continuation rules when answers arrive before/after process exit; counter-questions.
- Fencing of stale exits by Pause/Continue/Revise; crash recovery of parks; completion markers with pending waits.

Siblings: [`../agent-mesh-waits/spec.md`](../agent-mesh-waits/spec.md),
[`../agent-mesh-wait-endings/spec.md`](../agent-mesh-wait-endings/spec.md), [`../agent-mesh/spec.md`](../agent-mesh/spec.md).
Helper result contract when a helper turn yields — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md). IDs: `MESH` R47–R54.

## Terms
- **Park** — transition into `awaiting_peer` carrying `peer_waits` (the waits the turn ended for).
- **Invocation** — one agent process run, identified by its mesh token (`WithMeshInvocation`).

## Requirements

### MESH-R47 Waits belong to the live invocation
A stage agent's wait is decided by the token of its invocation under the Store lock: a request from a finished invocation or an earlier attempt (before Pause/Continue/Revise) registers nothing.

Evidence: `afm:cmd/afm/run_agent_mesh_wait_test.go` `TestAgentMeshWait_IsDecidedAgainstTheInvocationOfItsToken`; `engine:internal/orchestrator/AGENTS.md` (WithMeshInvocation)

### MESH-R48 Yield parks instead of completing
A stage turn that exits with an open wait parks in `awaiting_peer` (no false completion, no retry as incomplete); a helper/run-agent turn parks in `waiting_peer`, releasing its slot, not as a protocol error. Without an open wait behaviour is unchanged. A held (paused) waiting task does not resume until Continue; cancelling a parked task works.

Evidence: `engine:internal/orchestrator/agentcomm_yield_test.go` `TestRunWithRetry_MeshWaitParksAwaitingPeerNotFalseCompletion`, `TestRunWithRetry_NoOpenWaitKeepsExistingBehavior`; `engine:internal/orchestrator/agentcomm_peer_test.go` `TestHelperYield_ParksWaitingPeerNotProtocolError`, `TestHelperYield_ReleasesSlotForQueuedPeer`, `TestPauseTask_HeldWaitingPeerDoesNotResumeUntilContinue`, `TestCancelTask_CancelsTaskParkedInWaitingPeer`

#### MESH-S47 Stage yields
- GIVEN a stage agent whose `afm agent wait` returned `yield`
- WHEN its process exits without a reply
- THEN the stage is `awaiting_peer` and neither completion nor verify runs

### MESH-R49 Continuation on answer
A reply (or wait ending) resumes a parked stage exactly once with a "peer replied" / "wait ended" continuation; concurrent resume attempts start one turn; a resume when not parked is a no-op. Separate `peer_waits` are alternatives, a batch is all-of.

Evidence: `engine:internal/orchestrator/agentcomm_yield_test.go` `TestMesh_SendWakesStageWaitAndPublishesPeerReplied`, `TestMesh_ResumeAfterPeerReply_NoOpWhenNotAwaitingPeer`; `engine:internal/orchestrator/agentcomm_peer_test.go` `TestTryResumePeer_ConcurrentCallsResumeExactlyOnce`, `TestHelperWait_ResumesOnReplyAndCompletes`; `engine:internal/orchestrator/agentcomm_continuation_test.go` `TestPeerContinuation_BatchItemsAreAllOfAndOtherWaitsAnyOf`

### MESH-R50 Answers before exit
An answer (or wait end) arriving before the yielding process exits still yields a new turn rather than completion; an answer the agent already read in that turn is not a reason for another turn; a wait already `ready` at registration does not make the turn a yield.

Evidence: `engine:internal/orchestrator/agentcomm_yield_exit_test.go` `TestYieldExit_AnswersBeforeExitContinueTheStage`, `TestYieldExit_WaitEndedBeforeExitContinuesAsWaitEnded`, `TestYieldExit_AnswerReadInTheTurnIsNotAContinuation`, `TestYieldExit_ReadyAtRegistrationIsNotAYield`; `engine:internal/orchestrator/agent_jobs_test.go` `TestAgentJobs_HelperTurnAnsweredBeforeItsExitIsYielded`

#### MESH-S48 Answer read in the same turn
- GIVEN an agent yields, then reads the answer via `afm agent inbox` before exiting
- WHEN the process exits
- THEN no extra continuation turn is started for that answer

### MESH-R51 Counter-questions keep the wait
A question from the awaited party (e.g. a helper's callback) wakes the waiter with a distinct "peer question" event while the original wait stays pending; this survives crash recovery and child-to-waiting-parent questions.

Evidence: `engine:internal/orchestrator/agentcomm_peer_question_test.go` `TestWait_SurvivesCounterQuestion`, `TestWait_SurvivesCounterQuestion_ChildQuestionsWaitingParent`, `TestMesh_SendCounterQuestionPublishesPeerQuestionNotReply`, `TestWait_SurvivesCounterQuestion_CrashRecovery`

### MESH-R52 Exit fence
Pause, Continue or Revise during a yielding process's exit wins: the stale exit neither parks nor completes, a stale completion event is ignored, a Revise of that turn starts exactly one feedback turn (a Revise of a newer turn is not handed off), and resuming an earlier park is a no-op.

Evidence: `engine:internal/orchestrator/agentcomm_exit_fence_test.go` `TestExitFence_StaleExitNeverCompletesTheReplacementsPark`, `TestExitFence_StaleCompletionEventIsIgnored`, `TestExitFence_OneFeedbackRunPerRevise`, `TestExitFence_ReviseOfANewerTurnIsNotHandedOff`, `TestExitFence_ResumeOfAnEarlierParkIsANoOp`; `engine:internal/orchestrator/agentcomm_yield_exit_test.go` `TestYieldExit_PauseContinueDuringExitIsStale`, `TestYieldExit_ReviseDuringExitRunsTheFeedbackTurn`

### MESH-R53 Park recovery and pause
A crash after `yield` but before parking parks the stage on the recorded waits on restart (no rerun, no marker completion); if the answer already arrived it continues. Ready waits from a past episode are ignored. A parked stage can be paused, keeps its waits, and Continue resumes it only if an answer is there.

Evidence: `engine:internal/orchestrator/agentcomm_park_lifecycle_test.go` `TestParkLifecycle_CrashAfterYieldKeepsWaiting`, `TestParkLifecycle_CrashAfterYieldWithTheAnswerContinues`, `TestParkLifecycle_PauseAndContinueAParkedStage`; `engine:internal/orchestrator/agentcomm_yield_exit_test.go` `TestYieldExit_RecoveryAfterCrashContinuesOnTheRecordedEpisode`; `engine:internal/orchestrator/agentcomm_yield_test.go` `TestMesh_RecoverMeshWaits_IgnoresStaleReadyWaitFromPastEpisode`

#### MESH-S49 Crash between yield and park
- GIVEN `afm agent wait` returned `yield` and afm crashed before the park was committed
- WHEN `afm run` resumes
- THEN the stage parks on those waits instead of rerunning or completing from a marker

### MESH-R54 Completion marker with pending waits
A fresh `.done` / `execution_summary.md` written while peer waits are pending does not complete the stage: it stays `awaiting_peer`, and a durable `completion_marker_pending_wait` notice (with wait details in `notices.jsonl`) is published once per pending episode (a batch is one episode; remembered across restart; an old marker with a genuine yield is quiet); completion and verify have not run. A continuation that is not a resumed session gets the earlier turns' context even if afm restarted before it started.

Evidence: `engine:internal/orchestrator/completion_marker_notice_test.go` `TestCompletionMarker_FreshMarkerWithPendingWaitPublishesOneNotice`, `TestCompletionMarker_OldMarkerWithGenuineYieldIsQuiet`, `TestCompletionMarker_OneNoticePerPendingEpisode`; `engine:internal/orchestrator/completion_marker_episode_test.go` `TestCompletionMarker_BatchIsOneEpisode`, `TestCompletionMarker_EpisodeIsRememberedAcrossARestart`; `engine:internal/orchestrator/agentcomm_continuation_test.go` `TestPeerContinuation_CrashBeforeTheAgentStartedKeepsTheContext`; `engine:internal/orchestrator/AGENTS.md` (parkInterruptedTurn); `afm:docs/collaboration.md` (Duplicate waits and owner recovery)

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E47 | Wait from a finished/stale invocation | not registered (refused) | `TestAgentMeshWait_IsDecidedAgainstTheInvocationOfItsToken` |
| MESH-E48 | Marker with pending waits | no completion; `completion_marker_pending_wait` notice | `TestCompletionMarker_FreshMarkerWithPendingWaitPublishesOneNotice` |

## Coverage gaps and discrepancies
- **MESH-G47** (doc-only): docs say the Feed shows only the event name and not the wait list; dashboard rendering is not covered by engine tests.
- **MESH-G48** (doc-only): "A turn that ends by waiting for a colleague needs no answer" for helpers in native schema mode is specified in docs; helper side belongs to `../helpers-lifecycle/spec.md`.
