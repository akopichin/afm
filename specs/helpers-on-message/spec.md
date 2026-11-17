# Stage helpers: on_message helpers and declared loops

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `trigger: on_message` stage helpers: admission, the durable request queue, one turn per request, closing.
- Their interplay with stage Pause, failure, Retry and the required gate.
- Declared helper loops (`assistants[].loop` → `engine.Spec.AssistantLoops`).
- Out of scope: reply/result commit mechanics → [helpers-results](../helpers-results/spec.md); generic loops →
  [agent-mesh-loops](../agent-mesh-loops/spec.md); restart → [helpers-recovery](../helpers-recovery/spec.md);
  gate semantics → [helpers-barrier](../helpers-barrier/spec.md).

## Terms
- **Request**: a non-passive message addressed to the helper, queued in `PendingRequests`; at most one is `ActiveRequest`.
- **Continuation**: a message that answers the helper's own message sent during the active request, or resolves its wait.
- **Outcome**: `result` or `failed`, recorded on the status record that ends the active request.
- **Idle**: on_message helper, not terminal, no active and no pending request (`OnMessageIdle`).

## Requirements

### HLP-R20 Admission
`on_message` is accepted only for a top-level helper of a started stage; it is refused for a run agent and for
delegated children/continuations (`invalid_request`) and for a `pending` stage (`invalid_status`). The helper rests in
`watching`: no process, no command slot, no pending wait. `afm agent spawn --trigger on_message` returns `task_id` and
`participant_id` but no `message_id`.

Evidence: `engine:internal/orchestrator/assistants_on_message_test.go` `TestOnMessageAdmission_AttachRestsInWatchingWithoutStarting`,
`TestOnMessageAdmission_DelegationRejected`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_OnMessageRestsWithoutARequest`.

#### HLP-S40 Spawned on_message helper has no request
- GIVEN a stage agent
- WHEN it spawns a helper with `--trigger on_message`
- THEN the task is `watching`, the receipt has no `message_id`, and no turn starts.

### HLP-R21 What is a request
Any message addressed to the helper is a request — `afm agent send`, a human post addressed to it, a Board
assignment, a loop tick — except passive turn results (`AutoResult`) and continuations of the active request. Queueing
is decided when the message record is applied, identically live and on replay; a repeated send queues once.

Evidence: `engine:internal/state/on_message_test.go` `TestOnMessageQueue_ClassifiesMessagesLiveAndOnReplay`,
`TestOnMessageQueue_RepeatedSendQueuesOnce`;
`engine:internal/orchestrator/assistants_on_message_lifecycle_test.go` `TestOnMessage_BoardAssignmentIsARequest`,
`TestOnMessage_LoopTickIsARequest`, `TestOnMessage_CallbackContinuesTheRequest`; code `RunStateQueueOnMessageRequests`.

#### HLP-S41 Callback answer continues the request
- GIVEN a helper working on m1 that asked the stage agent a question and yielded
- WHEN the stage agent replies to that question
- THEN no new request is queued; the helper's next turn continues m1.

### HLP-R22 One turn per request, in order
Every commit that can give the helper work (a message to it, end of its request, Continue, Retry) calls a CAS
`watching → queued` that succeeds only with a request due for the current root generation. A turn names its request
(the active one, else the oldest pending); the projection refuses a turn start or outcome out of queue order. Requests
never run in parallel. After the outcome commits, the helper returns to `watching` and the next request is woken.

Evidence: `engine:internal/orchestrator/assistants_on_message_lifecycle_test.go`
`TestOnMessage_EachRequestGetsItsOwnTurnResultAndReply`, `TestOnMessage_NoRequestNoTurn`,
`TestOnMessage_RequestDuringTheFinishStartsTheNextTurn`;
`engine:internal/state/on_message_test.go` `TestOnMessageQueue_StatusRecordsFollowTheQueue`; code `wakeOnMessageTask`,
`checkRequestProgress`.

#### HLP-S42 Two requests
- GIVEN an idle helper
- WHEN m1 and then m2 are sent while m1's turn runs
- THEN m2 waits; m1 gets turn 1 with its own result and reply, then m2 gets turn 2.

### HLP-R23 Turn outcomes keep or end only the request
A yield, delegation, usage-limit park or Pause keeps the request active for the next turn. A failed turn (process
error, timeout, protocol error, repeated delegation rejection) ends only its request with outcome `failed`, replies the
reason to the sender, and the helper takes the next request. A process that never started, or a turn whose preparation
fails (e.g. context over 2 MiB), fails the whole task; `failed` keeps the queue, and RetryTask returns the helper to
`watching` to continue the same requests in order — refused for a helper of a superseded stage attempt.

Evidence: `engine:internal/orchestrator/assistants_on_message_lifecycle_test.go` `TestOnMessage_FailedTurnFailsOnlyItsRequest`,
`TestOnMessage_QuotaParkKeepsTheRequest`, `TestOnMessage_PauseOfTheActiveTurnKeepsItsRequest`;
`engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_RetryOfAFailedHelperResumesItsRequests`,
`TestOnMessage_RetryAfterAStartFailureWorksOnTheStoppedRequest`;
code `failHelperTurn`, `prepareHelperTurn`.

#### HLP-S43 Failed turn, next request
- GIVEN m1 and m2 queued
- WHEN m1's turn returns an invalid answer
- THEN m1's outcome is `failed` with no result or verdict, its sender gets a reply with the reason, and m2 runs.

### HLP-R24 Stage Pause gates turns
While the stage is `paused` (or the helper serves a superseded attempt) no turn starts; the start record is refused
under the Store lock and the task returns to `queued` with reason `re-queued: stage paused`. Messages are still
accepted and wait. Leaving the pause wakes the helpers that have requests.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_StagePauseHoldsRequestsUntilContinue`,
`TestOnMessage_StagePausedRightBeforeTheProcessStarts`;
code `onMessageGated`, `resumeOnMessageHelpers`.

#### HLP-S44 Paused stage
- GIVEN a paused stage and its idle helper
- WHEN m1 is sent, then the stage is continued
- THEN m1 is queued without a turn during the pause and gets its turn after Continue.

### HLP-R25 Closing (system only)
Only the system closes an on_message helper, and only while it is idle in `watching` for the current generation:
when the stage is `done` — `completed`, reason `closed: stage <id> is done`, no result, no verdict; when the stage is
`awaiting_assistants` and the helper is required with last outcome `result` + verdict `pass` — `completed` (closing the
gate). The close is a CAS re-checked under the Store lock; a request accepted meanwhile keeps the helper. Accepted
requests are answered after the stage is `done`, then the helper closes. A closed/terminal helper refuses new
messages with `invalid_status`.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_StageDoneClosesAnUnusedHelper`,
`TestOnMessage_AcceptedWorkFinishesAfterTheStageIsDone`, `TestOnMessage_RequestRacingTheCloseIsTakenOrRefused`; code `settleOnMessageTask`, `onMessageCloseReason`.

#### HLP-S45 Accepted work outlives stage done
- GIVEN m1 accepted and running
- WHEN the stage becomes `done`
- THEN m1 is answered, the run waits for it, and then the helper is `completed` with the close reason.

### HLP-R26 Required on_message gate
A required on_message helper blocks its stage from the moment it is attached, before any request, until its last
request ended with `result` + `pass` (then it is closed `completed`) or it is waived. Last outcome `failed`,
`needs_changes`, `inconclusive` or no request: the stage stays `awaiting_assistants` and `NeedsHumanDecision` is true
while the helper is idle; while a request is open it is machine work. A new request after a pass reopens the gate.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_RequiredHelperWithoutARequestWaitsForOne`,
`TestOnMessage_RequiredNeedsChangesThenPass`, `TestOnMessage_RequiredPassThenFailedRequestKeepsTheGateOpen`;
`engine:internal/state/on_message_test.go` `TestNeedsHumanDecision_IdleRequiredOnMessageHelper`.

#### HLP-S46 needs_changes then pass
- GIVEN a required helper and a stage in `awaiting_assistants`
- WHEN m1 ends `needs_changes` and then m2 ends `pass`
- THEN the stage waits after m1 and becomes `done` after m2, with the helper `completed`.

### HLP-R27 Failed stage and Retry
A `failed` stage cancels nothing: accepted requests are finished (holding the run until then), new requests are
refused, and an idle helper does not hold the run. A stage Retry re-opens the helper for requests.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go`
`TestOnMessage_FailedStageFinishesAcceptedWorkAndRetryReopens`, `TestOnMessage_FailedStageRunEndsAfterTheLastRequest`;
`engine:internal/state/on_message_test.go` `TestOnMessageQueue_RetriedStageTakesRequestsAgain`.

#### HLP-S47 Failed stage with work in flight
- GIVEN m1 running and the stage fails
- WHEN m2 is sent
- THEN m2 is refused `invalid_status`, m1 is still answered, and the run then ends.

### HLP-R28 Queue room and terminal drop
At most 200 (`MaxInboxUnread`) requests may wait, read-ahead ones included; the next one is refused `budget_exceeded`
and not recorded. Continuations have their own room of 200 unread. A terminal status other than `failed` drops the
queue and records the newest dropped request as last outcome `failed`; replay never refuses.

Evidence: `engine:internal/state/on_message_test.go` `TestOnMessageQueue_BacklogCountsRequestsAlreadyRead`,
`TestOnMessageQueue_ContinuationsHaveTheirOwnRoom`, `TestOnMessageQueue_TerminalStatusDropsTheQueue`,
`TestOnMessageQueue_FailedHelperKeepsItsRequestsForTheRetry`.

#### HLP-S48 Full queue
- GIVEN 200 pending requests
- WHEN a 201st message is sent
- THEN it is refused `budget_exceeded` and nothing is written.

### HLP-R29 Declared helper loops
AFM converts `assistants[].loop {every, prompt}` into `Spec.AssistantLoops`; `Open` rejects duplicates, a missing or
non-`on_message` declared helper, a non-agent stage, intervals < 1s and empty/oversized prompts. The helper and its
loop are created in one commit at attach (never in planning or with collaboration off); the 16-active-loop limit is
checked in that commit and rejects the whole attach (declared-helper warning path). Ticks use fixed slots from creation
(first one interval later), never overlap, skip busy/paused slots and downtime without catch-up; the prompt is redacted
before persistence. Committed loops keep their parameters on resume; a new root generation gets a new helper and loop;
the loop ends with stage completion, helper cancel/supersession, or a full/closed thread.

Evidence: `engine:internal/orchestrator/assistants_declared_loop_test.go` `TestValidateAssistantLoops`,
`TestDeclaredLoop_AtomicReplayStopAndNewGeneration`, `TestDeclaredLoop_LimitAtomicallyRejectsHelper`,
`TestDeclaredLoop_DisabledAndPlanningDoNotAttach`; `engine:internal/orchestrator/agentcomm_loop_scheduler_test.go`
`TestLoopScheduler_NoOverlapOfAnUnfinishedTick`, `TestLoopScheduler_RestartSkipsDowntimeAndWaitsForTheNextFutureSlot`;
`afm:cmd/afm/run_assistant_loops_test.go` `TestDeclaredAssistantLoops_InvalidEveryNamesStageAndProfileNotPrompt`.

#### HLP-S49 Loop limit rejects the helper
- GIVEN 16 active loops
- WHEN a stage with a looped on_message helper starts execution
- THEN neither the helper nor its loop is created and the stage runs without it.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E10 | on_message for run agent / child / continuation | `invalid_request` | `TestOnMessageAdmission_RunAgentRejected` |
| HLP-E11 | on_message attach to pending stage | `invalid_status` | `TestOnMessageAdmission_PendingStageRejected` |
| HLP-E12 | New request to closed/terminal helper or done/failed stage | `invalid_status`, nothing queued | `TestOnMessageQueue_RefusesNewRequestsOnceClosed` |
| HLP-E13 | 201st pending request | `budget_exceeded` | `TestOnMessageQueue_BacklogCountsRequestsAlreadyRead` |
| HLP-E14 | RetryTask of a superseded-attempt helper | `invalid_status` | `TestOnMessage_RetryRefusesAHelperOfASupersededAttempt` |

## Coverage gaps and discrepancies
- **HLP-G10** (code≠doc): engine `docs/assistant-loops.md` says AFM's parser does not accept `assistants[].loop`;
  AFM parses it (`afm:contracts/flow/assistants.go`, `afm:cmd/afm/run_assistant_loops.go`).
- **HLP-G11** (ambiguous): a `task_result` wait on a stage-scoped on_message helper resolves only when the task is
  terminal, not per request (`AssistantsStateMeshWaitSatisfied`); docs steer to `reply_to`, no test covers it.
- **HLP-G12** (code≠doc): `docs/collaboration.md` says a failed turn ends only its request, but a preparation failure
  (context > 2 MiB, workspace error) fails the whole task (`prepareHelperTurn`); untested for on_message.
