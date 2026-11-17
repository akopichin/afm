# Stage helpers: admission, limits and run-level holding

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Admission of a new helper task and of delegated children; quotas, depth, executor availability.
- Concurrency of helper turns and fixed size limits.
- Budget and provider usage-limit interplay (briefly) and when helpers keep the run open.
- Out of scope: budget rules themselves → [budgets-accounting-limits](../budgets-accounting-limits/spec.md);
  on_message queue room and loop limit → [helpers-on-message](../helpers-on-message/spec.md); executor resolution →
  [execution-environment](../execution-environment/spec.md); restart → [helpers-recovery](../helpers-recovery/spec.md).

## Terms
- **Quota**: `collaboration.limits.max_tasks`, a whole-run count of every task (top-level and delegated); unset = unlimited.
- **Command slot**: a lease on the semaphore of the executor command (`max_parallel`), taken via `SpawnOwned`.

## Requirements

### HLP-R70 Top-level admission
A new top-level task is admitted only if: collaboration is enabled; the request has an operation id that is not a
reserved prefix; the id has not committed before (else the committed task is returned, or an operation-id conflict for
a different payload); the stage exists, is not a script stage and is not `done`/`failed`; on_message rules hold; the
profile is registered and its resolved executor is available (the executor, read-only guarantee and binding are frozen
into the task); the depth policy allows depth 1; the budget admits creation. The commit re-checks the policy revision,
the quota and that the stage is still not terminal. A rejection writes nothing.

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestAttach_RejectedWhenDisabled`,
`TestAttach_RejectedForScriptStage`, `TestAttach_FreezesReadonlyEnforcementIntoSnapshot`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_RejectionsWriteNothing`,
`TestSpawnHelper_UnavailableProfileWritesNothing`, `TestSpawnHelper_RetryOfTheSameOperationReturnsTheSameIDs`;
`engine:internal/orchestrator/assistants_barrier_test.go` `TestAttachRequiredRacesWithComplete`;
`engine:internal/orchestrator/assistants_budget_test.go` `TestAttach_RefusedByBudgetCreatesNoTask`; code `admitTopLevelTask`.

#### HLP-S120 Attach races stage completion
- GIVEN a running stage and a required Attach decided on a snapshot
- WHEN the stage commits `done` before the Attach commit
- THEN the Attach is rejected `invalid_status` and the stage stays `done`.

### HLP-R71 Delegation admission
Children are admitted as one group or not at all: at most 8, depth within `max_depth` (flow > project > global > 3),
no `on_message`, no self-elevation (capabilities, depth, root stage, budget fields are refused; a child cannot get a
weaker read-only guarantee than its parent), only the task's own turn may create them, and a held ancestor blocks
creation. The group reserves quota atomically; quota is never reset per level.

Evidence: `engine:internal/orchestrator/assistants_delegate_test.go` `TestAdmission_DepthBoundary`,
`TestAdmission_GroupIsAllOrNothing`, `TestAdmission_ConcurrentParentsRespectSharedQuota`,
`TestAdmission_AncestorHoldBlocksChild`, `TestAdmission_OnlyOwnTaskCanGetChildren`; `afm:contracts/assistants/delegate_test.go`
`TestDelegate_ChildCountCapped`, `TestDelegate_RejectsSelfElevation`; `engine:internal/state/policy_test.go`
`TestQuota_NotResetPerDelegation`; `engine:internal/orchestrator/assistants_execution_test.go`
`TestDelegation_NoSelfElevationComparesResolvedExecutors`.

#### HLP-S121 Quota filled by the parent
- GIVEN `max_tasks: 1` and one helper already attached
- WHEN that helper answers with a delegation
- THEN no child is created and the helper gets a typed rejection note for its next turn.

### HLP-R72 Concurrency
Every helper turn runs under its executor command's slot. A helper inheriting the stage's command competes with the
stage agent (with `max_parallel: 1` it waits for it). `waiting_children`, `waiting_peer`, `watching` and `queued`
hold no slot; a resting on_message helper uses none. Pause, a lost stage result or a budget/provider hold noticed after
the slot is granted put the turn back to `queued` instead of leaving it `running`.

Evidence: `engine:internal/orchestrator/assistants_execution_test.go` `TestMaxParallelOne_InheritingHelperWaitsForTheStageAgent`,
`TestMaxParallelOne_LegacyHelperWaitsForItsStageCommand`; `engine:internal/orchestrator/assistants_continuation_test.go`
`TestLease_ReleasedBeforeWaitingChildren`, `TestSingleSlot_ChainCompletes`, `TestWaitingChildren_DoesNotCountInActiveInvocations`;
`engine:internal/orchestrator/assistants_barrier_test.go` `TestBarrier_SlotReleasedWhileWaiting`;
`engine:internal/orchestrator/watch_tasks_test.go` `TestCompletionHelper_PauseWhileWaitingForTheSlotRequeuesTheTurn`.

#### HLP-S122 Single slot
- GIVEN `max_parallel: 1` and a helper using the stage's command
- WHEN the stage agent is running
- THEN the helper turn waits for the slot and starts after the agent releases it.

### HLP-R73 Fixed size limits
The assembled helper prompt is at most 2 MiB (2,097,152 bytes; exactly 2 MiB fits); a larger one fails the turn before
the process starts. A task status reason is redacted, then cut to 2 KiB. A turn reply body is cut to the 16 KiB message
limit. Other limits: 200 unread per inbox / pending on_message requests, 200 messages per thread, 8 children per
delegation, 16 active loops.

Evidence: `engine:internal/orchestrator/assistants_context_test.go` `TestContext_TwoMiBBoundary`,
`TestContext_RespectsByteLimit`; `engine:internal/orchestrator/assistants_execution_test.go`
`TestHelperProtocolReason_RedactsBeforeTruncating`; `afm:contracts/agentcomm/limits.go` `MaxBodyBytes`, `MaxInboxUnread`,
`MaxThreadMessages`, `MaxActiveLoops`.

#### HLP-S123 Oversized context
- GIVEN a helper whose plan and inline artifacts exceed 2 MiB
- WHEN its turn is prepared
- THEN no process starts and the task fails with an `assistants: build context` reason.

### HLP-R74 Budget interplay
The budget controller is asked at creation (refusal: `spend_limit_reached` or `rates_missing`, nothing written) and
before every turn and every extra invocation (native fallback, format repair, delegation). A refusal is a durable hold,
not a failure: the task stays or returns to `queued` and resumes when the budget allows, keeping a produced answer to
format or a delegation to admit. A budget hold never takes away a result already produced.

Evidence: `engine:internal/orchestrator/assistants_budget_test.go` `TestMaybeScheduleTask_BudgetRefusalKeepsTaskQueuedAndLaterStarts`,
`TestHelperOutputOwned_IgnoresBudgetHold`, `TestAttach_RefusedByBudgetCreatesNoTask`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_FormatterUsageLimitStillParks`.

#### HLP-S124 Budget refuses a turn
- GIVEN a queued helper and an exhausted budget
- WHEN scheduling runs
- THEN the task stays `queued` (no `running` record) and starts once the budget is raised.

### HLP-R75 Provider usage limits
A turn that hits a provider usage limit is not a failure: the task returns to `queued` with a durable quota park (its
mesh identity, inbox and waits on it stay); after the wait it runs once. A limit that cannot be waited out also pauses
the task with the reason; Continue makes one immediate probe. A park holds that provider for stages and other helpers.

Evidence: `engine:internal/orchestrator/assistants_quota_test.go` `TestHelperQuota_RefusalParksQueuedTaskKeepingIdentityInboxAndMesh`,
`TestHelperQuota_UnwaitableLimitPausesWithReasonAndContinueProbesOnce`, `TestHelperQuota_ParkHoldsTheProviderForStagesAndOtherHelpers`,
`TestHelperQuota_StageReplyWaitSurvivesTheParkAndEndsPeerReplied`.

#### HLP-S125 Stage keeps waiting through a park
- GIVEN a stage waiting `reply_to` on a spawned helper
- WHEN the helper hits a usage limit and later succeeds
- THEN the stage's wait stays pending through the park and ends with the helper's reply.

### HLP-R76 Helpers and run exit
With collaboration enabled, the run finishes only when every stage is terminal, no task is a run blocker and no queued
turn is due. Blockers: any non-terminal task except `queued` ones, judged for on_message helpers by their queue (idle =
not a blocker, accepted request = blocker in any status). An advisory helper therefore keeps the run open after its
stage is `done`; a paused task with work waiting keeps it open until continued or cancelled; an idle run agent does not.

Evidence: `engine:internal/orchestrator/run_barrier_test.go` `TestRunBarrier_IdleRoleDoesNotHoldRun`,
`TestRunBarrier_AcceptedWorkHoldsRun_ShutdownBounded`, `TestRunBarrier_HeldRunAgentWithAcceptedMessageHoldsRun`;
`engine:internal/state/on_message_test.go` `TestRunBlocker_IdleOnMessageHelperDoesNotHoldTheRun`;
`engine:internal/orchestrator/assistants_quota_test.go` `TestHelperQuota_QueuedTurnDueCountsAParkedTask`;
code `AssistantsStateRunBlocker`, `queuedTurnDue`, `runStateAllowsExitCore`.

#### HLP-S126 Advisory helper outlives its stage
- GIVEN the last stage `done` and its advisory one-shot helper still running
- WHEN the stage completes
- THEN the run keeps running until the helper's turn commits, then finishes.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E50 | Collaboration disabled | `feature_disabled` | `TestAttach_RejectedWhenDisabled` |
| HLP-E51 | Unknown / unavailable profile | `capability_unavailable` (unknown) or availability reason, nothing written | `TestSpawnHelper_UnavailableProfileWritesNothing` |
| HLP-E52 | Quota exceeded at commit | `budget_exceeded` (`quota … exceeds max …`) | `TestReject_BudgetExceeded` |
| HLP-E53 | Depth exceeded | `delegation_depth_exceeded` | `TestAdmission_DepthBoundary` |
| HLP-E54 | Budget refuses creation | `spend_limit_reached` / `rates_missing` | `TestAttach_RefusedByBudgetCreatesNoTask` |
| HLP-E55 | Same operation id, different payload | operation-id conflict | `TestDelegation_RepeatMustMatchTheWholeRequest` |

## Coverage gaps and discrepancies
- **HLP-G50** (untested): HLP-S126 (advisory helper holding the run after the last stage) is covered for run agents and
  mesh waits in `run_barrier_test.go`, not for a stage-scoped advisory one-shot.
- **HLP-G51** (ambiguous): `max_tasks` has no default; unset means no whole-run task limit
  (`Preconditions.MaxQuota == nil`), while docs only show it as optional.
