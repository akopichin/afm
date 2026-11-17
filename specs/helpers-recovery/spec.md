# Stage helpers: recovery after interruption

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How helper state is rebuilt from `events.jsonl` and what a resumed run does with each task status.
- In-flight turns, pending on_completion passes, interrupted Revise bumps, waits, quota parks and loops after restart.
- Out of scope: admission and limits → [helpers-admission](../helpers-admission/spec.md); stage resume →
  [run-lifecycle-resume](../run-lifecycle-resume/spec.md); journal format → [event-journal](../event-journal/spec.md).

## Terms
- **Settle** (`settleAssistantsAfterRestart`): first startup half; starts nothing. **Resume**
  (`resumeAssistantsAfterRestart`): second half; starts what is ready.
- **TurnInbox**: messages handed to a running turn; consumed only when the turn ran to its end.

## Requirements

### HLP-R60 Helper state is a journal projection
Tasks, statuses, turns, on_message queues and outcomes, results/verdicts, holds, waivers, quota parks and generations
are rebuilt by replaying control records; `state.json` and task files are caches. The on_message queue is decided at
apply time identically live and on replay, and replay never refuses a recorded message.

Evidence: `engine:internal/state/on_message_test.go` `TestOnMessageQueue_ClassifiesMessagesLiveAndOnReplay`;
`engine:internal/state/assistants_projection_test.go` `TestReplay_TaskCreatedThenStatusChanges`, `TestReplay_HoldsRestored`; `engine:internal/orchestrator/agentcomm_wait_completion_test.go`
`TestWaitCompletion_ReplayParity`; code `applyTaskStatusChanged`, `advanceRequests`.

#### HLP-S100 Queue survives restart
- GIVEN m1 active and m2 pending in the journal
- WHEN the run is resumed
- THEN the projection has the same active and pending requests in the same order.

### HLP-R61 Two-phase startup
Settle (only with collaboration enabled): rebinds watcher collectors of non-terminal watchers, moves every `running`
task to `queued` (`resumed after restart`), cancels unstarted on_completion passes of `failed` stages. Then the run
decides waits on requests idle agents left unanswered. Resume: closes closable on_message helpers (also with
collaboration disabled — no model call), then, if enabled, schedules `queued`, re-checks `waiting_children` and
`waiting_peer` readiness, issues owed completion passes for `watching` tasks and wakes on_message helpers with
requests. `paused` and `waiting_parent` are left as they are. Completion passes are deferred until settle finished.

Evidence: `engine:internal/orchestrator/assistants_crash_recovery_test.go` `TestCrash_QueuedChildNeverSpawnedBeforeCrash_RecoveredOnRestart`,
`TestCrash_WakeBeforeTheSettleStartsThePassOnce`; `engine:internal/orchestrator/completion_pass_failure_test.go`
`TestCompletionHelper_ParentFailureRecoveredAtStartup`; code `recoverAssistants`, `completionPassDeferred`.

#### HLP-S101 Closed with collaboration off
- GIVEN a required on_message helper whose only request passed and a stage that entered `awaiting_assistants` just before a crash
- WHEN the run resumes with collaboration disabled
- THEN the helper is closed `completed` with the pass and the stage becomes `done`.

### HLP-R62 In-flight turns are re-run once
A turn the crash (or a run shutdown) cut short did not consume its input: it is re-run once for the same input and,
for an on_message helper, the same request; its external side effects may repeat. A shutdown never records such a
turn as failed or parked.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_RestartAtEachBoundary`
(subtests "turn cut short", "task started, turn not recorded"); `engine:internal/orchestrator/assistants_cascade_test.go`
`TestRunEnd_AdvisoryTasksDoNotBlockForever`; `engine:internal/orchestrator/assistants_lifecycle_outcomes_test.go`
`TestHelperTurnOutcomes_ShutdownBeatsFailureAndQuota`.

#### HLP-S102 Turn cut short
- GIVEN an on_message turn recorded as started for m1
- WHEN the process dies and the run resumes
- THEN exactly one new turn runs for m1, its prompt still carries m1, and m1 gets one result.

### HLP-R63 Every boundary of an on_message request recovers
After a restart: an idle helper stays idle; a request whose wake, task start or turn record was lost gets exactly one
turn; a request under a paused stage keeps waiting; a close the crash pre-empted is made.

Evidence: `engine:internal/orchestrator/assistants_on_message_settle_test.go` `TestOnMessage_RestartAtEachBoundary`.

#### HLP-S103 Lost wake
- GIVEN m1 committed to the queue but the helper still `watching`
- WHEN the run resumes
- THEN m1 is answered in turn 1.

### HLP-R64 Pending completion passes run exactly once
An owed on_completion pass (or required checkpoints final pass) survives a restart and is issued exactly once whether
the crash came before the stage's result, between the result and the pass, or while the stage completed during
settle. A `failed` stage's unstarted pass is cancelled at startup even if the failure predates the restart.

Evidence: `engine:internal/orchestrator/assistants_crash_recovery_test.go` `TestCrash_CompletionPassSurvivesARestart`,
`TestCrash_StageCompletedAtStartupIssuesThePassOnce`, `TestCrash_StageCompletingBeforeTheSettleIssuesThePassOnce`;
`engine:internal/orchestrator/budget_r7_assistants_test.go` `TestBudgetR7_RequiredCheckpointCrashBeforeFinalQueue`.

#### HLP-S104 Crash after awaiting_assistants
- GIVEN a stage committed `awaiting_assistants` and its on_completion helper still `watching`
- WHEN the run resumes
- THEN one pass starts; the stage agent and verify do not run again.

### HLP-R65 Barrier and Revise intent restored
A stage found in `awaiting_assistants` (at startup or on Continue) only re-checks the barrier; a done marker never
bypasses it. A stage found `revising` after a Revise from `awaiting_assistants` first finishes that Revise's generation
bump (keyed by its seq, never twice) and the cancellation of superseded tasks, then spawns; declared helpers attach once.
A barrier-failure Retry finishes its bump the same way.

Evidence: `engine:internal/orchestrator/assistants_barrier_test.go` `TestBarrier_RestoredAfterRestart`,
`TestResumeByDoneMarkerDoesNotBypassBarrier`; `engine:internal/orchestrator/revise_bump_recovery_test.go`
`TestReviseRecovery_CrashBeforeTheBumpFinishesIt`, `TestReviseRecovery_CrashAfterTheBumpDoesNotBumpAgain`,
`TestReviseRecovery_FinishesTheCancellationAfterTheBump`; `engine:internal/orchestrator/assistants_declared_test.go`
`TestDeclaredAssistants_RecoveredRevisingReattaches`, `TestDeclaredAssistants_CrashBeforeSpawnAttachesOnce`;
`engine:internal/orchestrator/exit_on_fail_internal_test.go` `TestExitOnFailReplay`.

#### HLP-S105 Crash inside Revise
- GIVEN EvRevise from `awaiting_assistants` committed but no bump
- WHEN the run resumes
- THEN the root generation advances once, old open tasks are cancelled, and the new attempt attaches fresh helpers.

### HLP-R66 Waits, children, quota parks and executors
`waiting_children` resumes if all current children finished before the crash; `waiting_peer` resumes only if its wait
is ready (a still-pending wait leaves it untouched). A usage-limit park waits for its remaining time and then runs one
turn. A task keeps the executor frozen at admission across config changes; a changed recipe fails the task with a reason.

Evidence: `engine:internal/orchestrator/assistants_crash_recovery_test.go`
`TestCrash_ChildResultRecordedBeforeParentContinuation_RecoveredOnRestart`, `TestCrash_HelperWaitResolvedBeforeRequeue_RecoveredOnRestart`,
`TestCrash_HelperWaitStillPending_LeftAloneOnRestart`; `engine:internal/orchestrator/assistants_continuation_test.go`
`TestWaitingChildren_RestoredAfterRestart`; `engine:internal/orchestrator/assistants_quota_test.go`
`TestHelperQuota_RestartWaitsTheRemainderThenRunsExactlyOnce`; `engine:internal/orchestrator/assistants_execution_test.go`
`TestRestart_TaskKeepsItsExecutorAcrossConfigChange`, `TestRestart_RecipeChangeFailsTheTask`.

#### HLP-S106 Pending peer wait
- GIVEN a helper in `waiting_peer` whose wait is still pending
- WHEN the run resumes
- THEN the helper stays `waiting_peer` until a live answer resumes it.

### HLP-R67 Loops on resume
Committed loops keep their saved interval and prompt; editing or removing the YAML changes nothing; a stopped loop
stays stopped. The scheduler skips slots missed during downtime and waits for the next future slot.

Evidence: `engine:internal/orchestrator/assistants_declared_loop_test.go` `TestDeclaredLoop_AtomicReplayStopAndNewGeneration`;
`engine:internal/orchestrator/agentcomm_loop_scheduler_test.go` `TestLoopScheduler_RestartSkipsDowntimeAndWaitsForTheNextFutureSlot`.

#### HLP-S107 Downtime
- GIVEN a 5-minute loop and a run down for 20 minutes
- WHEN it resumes
- THEN no catch-up ticks are sent; the next tick is at the next future slot.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E40 | Recipe/executor changed for an existing task | task `failed` with reason on next turn | `TestRestart_RecipeChangeFailsTheTask` |
| HLP-E41 | Revise bump cannot be finished at recovery | stage not respawned, warning logged | code `resumeStageAtStatus` (`finishReviseInvalidation`) |

## Coverage gaps and discrepancies
- **HLP-G40** (doc vs behavior): docs say a turn running at crash "runs again … an action it took outside afm may
  repeat"; there is no idempotency for helper side effects.
- **HLP-G41** (untested): with collaboration disabled on resume, `running` tasks are not moved to `queued` (settle
  returns early) and stay `running` in the journal with no process; the run-level barrier is skipped when disabled,
  so they do not hold the run but are never settled. Check `settleAssistantsAfterRestart` + `runStateAllowsExitCore`.
- **HLP-G42** (untested): HLP-E41 (bump failure at recovery leaves the stage in `revising`) has no test.
