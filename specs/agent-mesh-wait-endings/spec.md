# Agent mesh: how waits end

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Wait resolution: one winner among answer, deadline, cancellation and closure.
- Waits that become unanswerable, including requests left unanswered by an idle agent.
- Interrupted vs finished runs; owner (operator) wait listing and cancellation.

Siblings: registration/admission — [`../agent-mesh-waits/spec.md`](../agent-mesh-waits/spec.md); yield and
parking — [`../agent-mesh-yield/spec.md`](../agent-mesh-yield/spec.md); core — [`../agent-mesh/spec.md`](../agent-mesh/spec.md).
Helper gates and verdicts — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md). IDs: `MESH` R40–R46.

## Terms
- **Left unanswered** — a request handed to a task agent that went idle without answering it.
- **Owner token** — run-owner bearer token for `/api/mesh/stages/<id>/waits[/cancel]`, distinct from mesh tokens.

## Requirements

### MESH-R40 Exactly one terminal outcome
A pending wait ends once: the first durable resolution (reply, expiry, cancel, closure) wins; later ones are no-ops and a reply losing to a timeout is still delivered. Only the winner announces and wakes the waiter.

Evidence: `engine:internal/orchestrator/agentcomm_deadline_test.go` `TestMesh_WaitDeadline_RaceReplyFirst_ReplyWinsAndExpiryIsHarmlessNoOp`, `TestMesh_WaitDeadline_RaceTimeoutFirst_TimeoutWinsAndReplyStillDelivered`, `TestMesh_WaitDeadline_RaceConcurrent_ExactlyOneOutcomeReplyNeverLost`; `engine:internal/orchestrator/agentcomm_wait_winner_test.go` `TestWaitResolution_OnlyTheWinnerAnnouncesAndWakes`; `engine:internal/orchestrator/agentcomm_recovery_test.go` `TestMesh_TwoSimultaneousRepliesOneWaitResolutionBothMessagesDelivered`

### MESH-R41 Deadlines
An expired wait becomes `expired` and resumes its waiter (stage via "peer wait ended", helper back to work); a paused stage or held task is not resumed by expiry. On restart overdue waits resolve synchronously and future ones are re-armed. A batch deadline expires every pending item and wakes once. Silence never fabricates a passing verdict.

Evidence: `engine:internal/orchestrator/agentcomm_deadline_test.go` `TestMesh_WaitDeadline_ExpiresStageWaitAndResumesViaPeerWaitEnded`, `TestMesh_WaitDeadline_DoesNotResumeStageAlreadyPaused`, `TestMesh_RecoverMeshWaitDeadlines_OverdueResolvesStageSynchronouslyAndResumes`, `TestMesh_RecoverMeshWaitDeadlines_FutureRearmsTimer`, `TestMesh_BatchWaitDeadlineExpiresEveryPendingItem`; `engine:internal/orchestrator/agentcomm_limits_test.go` `TestMesh_SilenceOnExpiredWaitNeverFabricatesASuccessfulVerdict`

#### MESH-S40 Expiry while paused
- GIVEN a stage parked on a wait and then paused
- WHEN the wait's deadline passes
- THEN the wait is `expired` but the stage is not resumed until Continue

### MESH-R42 Agent cancellation
`CancelWait` takes one wait, a batch, or a list; an agent may cancel only its own waits (the human any). Cancelling is idempotent, a racing reply that won is kept, and a real cancellation resumes the waiter once.

Evidence: `engine:internal/orchestrator/agentcomm_wait_test.go` `TestMesh_CancelWaitIsIdempotent`, `TestMesh_CancelWaitRejectsNonOwner`, `TestMesh_CancelWaitByBatchEndsThePendingItemsAtomically`; `engine:internal/orchestrator/agentcomm_deadline_test.go` `TestMesh_CancelWait_ResumesAwaitingPeerStageViaPeerWaitEnded`, `TestMesh_CancelWait_SecondCallDoesNotDoubleResume`, `TestMesh_CancelWait_RaceWithReply_ReplyWinsIsNotOverridden`

### MESH-R43 Waits that become unanswerable
When every possible answerer of a pending `reply_to`/`addressed_message` wait finishes (helper/run agent failed or cancelled, stage finished unanswered, message undeliverable) or its thread closes, the wait is cancelled with a reason naming them and the waiter continues; a parked stage gets its "wait ended" turn. Restart re-checks; one remaining answerer keeps the wait.

Evidence: `engine:internal/orchestrator/agentcomm_turn_failure_test.go` `TestReviewWait_StageContinuesWhateverTheReviewerDoes`, `TestReviewWait_RecoveryReleasesAWaitOnAFinishedReviewer`; `engine:internal/orchestrator/agentcomm_messages_test.go` `TestMesh_CloseThreadResolvesDependentWaits`; `engine:internal/orchestrator/agentcomm_wait_winner_test.go` `TestCloseThread_WakesTheStageParkedOnItsReply`; `engine:internal/orchestrator/agentcomm_unread_test.go` `TestUnreadMessage_RecoverStageWhoseWaitWasCancelled`

### MESH-R44 Left-unanswered requests
A request read by a task agent that goes idle without replying is unanswerable: waits on it are cancelled with a reason, never made ready by a turn result, Board done or message text. The turn start (`start_task`) cancels, in the same commit, waits on requests that agent abandoned; finish, quota park, pause and resume checks only add timeliness. A wait stays while another recipient can answer; `addressed_message` waits stay pending; a queued `on_message` helper request is not abandoned. Startup heals old journals once.

Evidence: `engine:internal/orchestrator/agentcomm_left_unanswered_test.go` `TestLeftUnanswered_ReadMidTurnWithoutReply`, `TestLeftUnanswered_OneOfTwoReadRequestsIsNotClosedByTheResult`, `TestLeftUnanswered_WaitStaysWhileAnotherRecipientCanAnswer`, `TestLeftUnanswered_AddressedMessageWaitStaysPending`, `TestLeftUnanswered_StartupHealsAnOldJournalOnce`; `engine:internal/orchestrator/agentcomm_left_unanswered_start_test.go` `TestLeftUnanswered_TurnStartSettlesEveryWaitInItsOwnCommit`, `TestLeftUnanswered_QuotaParkEndsTheWaitWithoutAnotherTurn`; `engine:internal/orchestrator/agentcomm_left_unanswered_run_test.go` `TestLeftUnanswered_BoardTaskIncident`, `TestLeftUnanswered_PausedMidTurnClosesTheRequestItRead`; `engine:internal/orchestrator/AGENTS.md`

#### MESH-S41 Read but not answered
- GIVEN a run agent reads a request mid-turn and ends the turn without replying
- WHEN its next turn starts (or it goes idle)
- THEN the sender's `reply_to` wait is cancelled with a reason and the sender resumes

### MESH-R45 Interrupted vs finished run
An interrupted run (Ctrl+C, fatal error) keeps waits pending and their deadlines run on after resume. Only a finalized run closes remaining waits as `run_closed`, after agents drained and without resuming anyone; closing is idempotent.

Evidence: `engine:internal/orchestrator/agentcomm_deadline_test.go` `TestMesh_CloseRemainingMeshWaits_ResolvesPendingToRunClosedWithoutResuming`, `TestMesh_CloseRemainingMeshWaits_IsIdempotentAndLeavesSatisfiedWaitsAlone`; `engine:internal/orchestrator/agentcomm_wait.go` `closeMeshWaitsIfFinalized`

### MESH-R46 Owner wait recovery
With an owner token, `GET /api/mesh/stages/<id>/waits` lists that stage's waits read-only, and `POST …/waits/cancel` (`request_id`, `wait_ids`, `reason`) cancels selected waits of that one stage in one operation with one continuation; a repeated `request_id` is idempotent per stage; a winning answer stays. Cancellation resumes normal processing only: verify, required helper gates and Revise still apply. The token is 256-bit, issued per dashboard start via the controlling terminal or `AFM_OWNER_TOKEN_FD`, never reaches children; without a channel (including Docker) routes are 404, a bad token 401 before origin/body checks.

Evidence: `engine:internal/orchestrator/agentcomm_cancel_waits_test.go` `TestCancelWaitList_EndsSelectedWaitsInOneOperationWithOneContinuation`, `TestCancelWaitList_RequiresOneWaiterThatIsTheNamedStage`, `TestCancelWaitList_RepeatOfARequestIdIsANoOp`, `TestCancelWaitList_WhilePausedIsDurableAndContinueResumesOnce`, `TestCancelWaitList_OwnerOperationIdsAreReservedForTheHuman`; `engine:internal/orchestrator/agentcomm_owner_recovery_test.go` `TestOwnerRecovery_VerifyRejectionAfterCancelStillBlocksCompletion`, `TestOwnerRecovery_RequiredHelperGateStillBlocksAfterCancel`, `TestOwnerRecovery_RestartAfterCancellationContinuesOnce`; `afm:pkg/server/owner_waits_test.go` `TestOwnerWaits_AuthenticationComesBeforeOriginAndBodyChecks`, `TestOwnerWaits_UnavailableWithoutTokenOrActions`; `afm:cmd/afm/run_owner_waits_test.go` `TestProvisionOwnerToken_ReachesNoChildProcess`, `TestProvisionOwnerToken_InADockerReExecNeverUsesTheTerminal`

#### MESH-S42 Unanswerable request
- GIVEN a stage parked in `awaiting_peer` on a wait nobody will answer
- WHEN the owner cancels it with a fresh `request_id`
- THEN the stage resumes once and still passes through verify and helper gates before completing

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E40 | Agent cancels another's wait | `actor_not_allowed` | `TestMesh_CancelWaitRejectsNonOwner` |
| MESH-E41 | Mixed wait/batch/list selection, duplicate ids | `invalid_request`, nothing changed | `TestCancelWaitList_RejectsMalformedAndMixedSelectionsWithoutChangingAnything` |
| MESH-E42 | Owner list names another stage's wait | refused | `TestCancelWaitList_RequiresOneWaiterThatIsTheNamedStage` |
| MESH-E43 | Unknown stage/batch/wait | `not_found` | `engine:internal/orchestrator/agentcomm_wait.go` `StageWaits`, `CancelWait` |
| MESH-E44 | Owner route without/with wrong token | 404 if no channel, else 401 | `afm:pkg/server/owner_waits_test.go` |

## Coverage gaps and discrepancies
- **MESH-G40** (doc-only): docs say Docker mode provides no owner token channel; code test covers the re-exec terminal case (`TestReleaseOwnerTokenForDocker_ClosesTheDescriptorAndSaysItIsNotSupported`), but no end-to-end Docker run.
- **MESH-G41** (untested): `run_closed` closure ordering "before EndRun persists terminal status" is only a code comment in `closeRemainingMeshWaits`.
