# Stage helpers: result publication and reply delivery

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The exact moment a helper result becomes durable and visible, and what is in that commit.
- Which requester gets a reply, when, and what a stage agent / wait caller observes.
- Undeliverable replies and failure replies.
- Out of scope: rejection of stale or invalid results → [helpers-staleness](../helpers-staleness/spec.md);
  wait semantics → [agent-mesh-waits](../agent-mesh-waits/spec.md); message limits →
  [agent-mesh-messaging](../agent-mesh-messaging/spec.md); journal format → [event-journal](../event-journal/spec.md).

## Terms
- **Finish operation**: the single `finish_task` journal operation that ends a turn (`finishTaskTurnRequest`).
- **Turn reply**: a passive (`AutoResult`) mesh message with id `turn-reply:<task>/<turn>/<message>` answering a request.
- **Post-commit work**: the `externalWork` callback `Store.ApplyRunOperation` runs after the record is fsynced and the lock released.

## Requirements

### HLP-R30 One atomic finish record
A turn's result commits as one operation containing, together: the `task_status_changed` (new status, turn number,
on_message request id and outcome), the `task_result`, the planned turn replies, the mirror of a child's result into its
parent's delegate thread, and cancellations of waits whose answer is dropped. Status is never `completed` without its
result; failure, cancel and requeue records carry no `task_result`. Wait conditions satisfied by this record
(`reply_to` on the answered request, `task_result` on a now-terminal task) are resolved in the same record.

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestLifecycle_FailurePathHasNoResultEvent`,
`TestCancel_HasNoResultEvent`; `engine:internal/orchestrator/output_recovery_test.go` `TestHelperOutputRecovery_WatcherAndSingleCommit`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_ResultRepliesOnceAndItsBatchItemBecomesReady`;
code `commitHelperResult`, `finishTaskTurnRequest`, `AssistantsStateResolveTaskResultWaits`.

#### HLP-S60 Spawned helper result
- GIVEN a stage agent waiting `reply_to` on its spawn request
- WHEN the helper's turn returns a valid result
- THEN one journal operation holds `completed`, the `task_result` and the reply, and the wait is `ready` in it.

### HLP-R31 Result files precede the commit
Before the finish operation, the engine writes `turn-<N>-output.recovery.json`, `turn-<N>-result.json` and atomically
`result.json`. If the commit is rejected, `result.json` is restored to its previous bytes (or removed); a failed restore
is run-fatal. Readers of the result (`afm agent helper-result`) use the journal, never `result.json`.

Evidence: `engine:internal/orchestrator/output_recovery_test.go` `TestHelperTurnCommit_ResultCache`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestHelperResult_NeverReadsAnUncommittedResultFile`;
code `persistTaskResult`.

#### HLP-S61 Rejected commit restores the cache
- GIVEN a previous accepted `result.json`
- WHEN a new turn's commit loses to a Pause
- THEN `result.json` again holds the previous bytes and no `task_result` is recorded.

### HLP-R32 Ordering: journal, then notices and deliveries
Nothing is announced before the finish record is durable. Post-commit work runs in this order: the turn's committed
hook; the live `assistant_task_status` notice (also appended best-effort to `notices.jsonl`); announcements of the
delivered replies; announcement and wake of waits the record cancelled; parent resume (`tryResumeParent`) for a
terminal child; release of waits nobody can answer; for a terminal status the stage barrier re-check and an event-loop
wake; then resumption of the waits made ready (`notifyAndResumeWaits`). A replayed (idempotent) operation runs its
post-commit work again.

Evidence: code `engine:internal/orchestrator/assistants.go` `finishTaskTurnRequest` (`afterFinish`),
`engine:internal/state/operation.go` `ApplyRunOperation`, `engine:internal/orchestrator/notices.go` `publishNotice`.

#### HLP-S62 Crash between commit and notice
- GIVEN a finish record fsynced
- WHEN the process dies before post-commit work
- THEN on resume the result is in the projection, and the waits it resolved are already `ready` (see
  [helpers-recovery](../helpers-recovery/spec.md)); the live notice may be missing.

### HLP-R33 Who gets a reply
An on_message helper replies to the request its turn worked on; a run agent to the message that woke the turn (and
to requests earlier yielded turns still owe); a `one_shot` helper spawned by a stage agent to its spawn request. A
declared, human-attached or delegated helper posts no reply (a child's result is mirrored to its parent instead). The
body is the summary trimmed to the message limit, a verdict line only when a verdict was given, the findings count and
where the full result is.

Evidence: `engine:internal/orchestrator/agentcomm_turn_reply_test.go` `TestTurnReply_AnswersTheWakingMessage`,
`TestTurnReply_NoWakingInputNoReply`; `engine:internal/orchestrator/assistants_optional_verdict_test.go`
`TestTurnReplyBody_VerdictSegmentOnlyWhenGiven`; `engine:internal/orchestrator/assistants_on_message_lifecycle_test.go`
`TestOnMessage_ReplyToAllWaitGetsEachRequestsAnswer`; code `planTurnReplies`, `turnReplyBody`.

#### HLP-S63 Dashboard-attached helper
- GIVEN a helper attached from the dashboard
- WHEN it completes with a result
- THEN no mesh reply is written; the result is visible as the task's committed result and status notice.

### HLP-R34 Replies are passive
A turn reply lands in the recipient's inbox but wakes nobody and does not extend a running stage turn; it resolves
`reply_to` waits on the answered message. A stage agent parked in `awaiting_peer` on that wait resumes in a new turn;
a live stage agent reads it with `afm agent inbox`; every stage turn's prompt lists its earlier requests with their
answers and `<your_helpers>` (≤20 own helpers of the current generation, id/title/status).

Evidence: `engine:internal/orchestrator/agentcomm_turn_reply_test.go` `TestPassive_DoesNotExtendAStageTurn`,
`TestPassive_WaitingAgentIsResumedOnlyByWhatItWaitsFor`; `engine:internal/orchestrator/agentcomm_wait_helper_edge_test.go`
`TestStageWaitOnSpawnedHelperSurvivesCallbackAndFinishesOnTheResult`; `engine:internal/orchestrator/agentcomm_spawn_test.go`
`TestStageInbox_ListsTheHelpersTheStageSpawned`, `TestStageInbox_DropsTheHelpersOfAnOldRootGeneration`.

#### HLP-S64 Callback does not end the stage's wait
- GIVEN a stage waiting `reply_to` on a spawned helper that sends it a callback question
- WHEN the stage answers the callback and yields again
- THEN its original wait stays pending and ends only with the helper's result reply.

### HLP-R35 Reading a helper's result
`HelperResult` returns status and the last committed result read in one Store lock (never torn); `Result` is nil while
running and for failed/cancelled tasks (the reason says why). With a request id it reports that on_message request as
`pending`, `active`, `result` (with that turn's result) or `failed` (with reason). Only the caller stage's own helpers of
the current attempt are visible; anything else is `not_found`.

Evidence: `engine:internal/orchestrator/agentcomm_spawn_test.go` `TestHelperResult_StatusAndResultNeverTornAcrossTheFinishCommit`,
`TestHelperResult_FailedHelperHasTheReasonAndNoResult`, `TestHelperResult_RefusesWhatIsNotTheCallersOwnHelper`;
`engine:internal/orchestrator/agentcomm_helper_request_test.go` `TestHelperRequestOf_ReportsEachStageOfARequest`,
`TestHelperResult_OnMessageHelperHiddenFromOtherStageAndOldGeneration`.

#### HLP-S65 Request selector
- GIVEN an on_message helper that answered m1 and is working on m2
- WHEN the stage agent reads `helper-result --message-id m1` and then `m2`
- THEN m1 is `result` with m1's answer, m2 is `active` with its turn and no result.

### HLP-R36 Explicit reply wins; undeliverable replies are dropped
If the helper answered the request itself during the turn, the automatic reply is dropped. If a reply cannot be
delivered (full thread/inbox, closed thread, finished recipient), the finish is retried without it; `reply_to` waits
nobody else can answer are cancelled in that record with reason `the answer to it could not be delivered: …`, the result
is still committed, and a `context_warning` notice `turn result of task <id> not delivered` goes to the feed. A rejected
parent mirror never blocks the child's finish.

Evidence: `engine:internal/orchestrator/assistants_on_message_lifecycle_test.go` `TestOnMessage_ExplicitReplyReplacesTheAutomaticOne`,
`TestOnMessage_UndeliveredReplyKeepsTheResult`; `engine:internal/orchestrator/agentcomm_turn_reply_test.go`
`TestTurnReply_OwnReplyRacingTheFinishWins`, `TestTurnReply_UndeliverableReplyFinishesTheTurnWithoutIt`,
`TestDelegateMirror_RejectedMirrorStillFinishesTheChild`; code `undeliveredReplyWaitEffects`, `noticeResultNotDelivered`.

#### HLP-S66 Full conversation
- GIVEN a request in a thread with no room left
- WHEN the helper's result commits
- THEN the result is recorded, no reply exists, and the requester's wait is `cancelled` naming the delivery failure.

### HLP-R37 Failure replies and ended waits
An on_message helper replies to the request of every failed turn; a stage-agent-spawned one-shot and a run agent
reply only when the failed turn ran to its end. The reply has the reason, the redacted unvalidated answer cut to fit and
the raw answer's path; a one-shot's reply ends with *task failed*.
A task that ends without a reply (process failure, cancel) leaves waits on it unanswerable; they are cancelled with a
reason in one commit after the finish, never resolved as `ready`.

Evidence: `engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_InvalidAnswerFailsButRepliesWithTheRawAnswer`,
`TestSpawnHelper_FailedHelperCancelsOnlyItsItemWithTheReason`, `TestSpawnHelper_CancelledHelperCancelsItsItem`;
code `failAnsweredTurn`, `releaseUnanswerableWaits`.

#### HLP-S67 Cancelled spawned helper
- GIVEN a stage waiting `reply_to_all` on two spawned helpers
- WHEN one helper is cancelled
- THEN only its batch item is `cancelled` with the reason; the other item still waits for its reply.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E20 | Reply undeliverable | result kept, wait `cancelled`, `context_warning` notice | `TestOnMessage_UndeliveredReplyKeepsTheResult` |
| HLP-E21 | `helper-result` for another stage's / old attempt's helper | `not_found` | `TestHelperResult_RefusesWhatIsNotTheCallersOwnHelper` |
| HLP-E22 | Restore of `result.json` after rejected commit fails | run ends storage-fatal | code `commitHelperResult` (`restoreLatest`) |

## Coverage gaps and discrepancies
- **HLP-G20** (untested): the post-commit order in HLP-R32 (notice before reply announcement before wait resume) is
  code-only; no test asserts it, and `notices.jsonl` append is best-effort, so a crash can lose the status notice.
- **HLP-G21** (untested): HLP-E22 restore-failure fatality has no test.
- **HLP-G22** (ambiguous): idempotent replay of a committed finish re-runs post-commit work (`ApplyRunOperation`
  contract); finish operation ids are random, so this applies only to Attach/spawn replays — not verified end to end.
