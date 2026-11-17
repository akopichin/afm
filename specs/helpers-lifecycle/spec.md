# Stage helpers: kinds, triggers and lifecycle

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Helper task kinds (`trigger`), modes (`required`/`advisory`) and the four ways a helper is created.
- Lifecycle of `one_shot`, `checkpoints` and `on_completion` helpers; task status model.
- The three different "completions" (turn, helper, stage `on_completion` pass).
- Out of scope: barrier, waiver, Revise, task controls, delegation → [helpers-barrier](../helpers-barrier/spec.md);
  wait cycle (R08) → helpers-barrier; on_message and loops → [helpers-on-message](../helpers-on-message/spec.md); results →
  [helpers-results](../helpers-results/spec.md); staleness → [helpers-staleness](../helpers-staleness/spec.md);
  restart → [helpers-recovery](../helpers-recovery/spec.md); admission, limits → [helpers-admission](../helpers-admission/spec.md);
  waits → [agent-mesh-waits](../agent-mesh-waits/spec.md); stage FSM → [stage-lifecycle](../stage-lifecycle/spec.md).

## Terms
- **Task / helper**: an `assistants.Task` in its own lifecycle, separate from the stage FSM. Top-level = direct child of a stage.
- **Turn**: one helper process run, numbered (`TaskState.Turn`). A watcher turn is a **pass**.
- **Root generation**: per-stage counter; tasks of an older generation are superseded. **Task generation**: per-task counter bumped by Retry.
- **Gate closed**: task `completed` with last committed verdict `pass`, or waived.

Task statuses as implemented (`contracts/assistants.TaskStatus`):

| Status | Meaning | Holds run open (`RunBlocker`) |
|---|---|---|
| `waiting_parent` | attached to a `pending` stage; never auto-promoted | yes |
| `queued` | turn due or idle run agent / idle on_message | no (but `queuedTurnDue` blocks exit when a turn is due) |
| `running` | turn in progress (or between start commit and process) | yes |
| `watching` | resting watcher / on_message helper between turns | yes, except idle on_message |
| `waiting_children` / `waiting_peer` | delegated / yielded into a mesh wait; slot released | yes |
| `paused` (task hold) | manual or usage-limit hold | yes |
| `completed` / `failed` / `cancelled` | terminal (`Terminal()`); see HLP-G04 | no |

## Requirements

### HLP-R01 Triggers, modes and defaults
Valid triggers are `one_shot`, `checkpoints`, `on_completion`, `on_message`; modes `advisory`, `required`. An omitted
mode is `advisory`, an omitted trigger `one_shot`, an omitted title the profile title. Trigger (when it may run), mode
(whether it gates the stage) and verdict (what it concluded) are independent.

Evidence: `afm:contracts/flow/assistants_test.go` `TestStageAssistants_InvalidTriggerErrorListsEveryValue`,
`TestStageAssistants_ObjectAllFields`; `engine:internal/orchestrator/assistants_optional_verdict_test.go`
`TestHelperContract_FollowsModeAndDelegation`; `engine:internal/orchestrator/assistants_admission.go` `withDefaults`;
`afm:contracts/assistants/types.go` `Trigger`, `Mode`.

#### HLP-S01 Defaults
- GIVEN `assistants: reviewer` on an execution stage
- WHEN the stage starts execution
- THEN one task is created with mode `advisory`, trigger `one_shot`, title = profile title.

### HLP-R02 Four creation paths
A helper is created by (1) a `flow.yaml` declaration, attached when the stage spawns its execution agent (never for
planning), with operation id `stage-assistants:<stage>:<profile>:<rootGeneration>`; (2) a human Attach (HTTP, any
trigger, idempotent by `request_id`); (3) a stage agent's `afm agent spawn` — always `advisory`, trigger `one_shot` or
`on_message`, task + thread + request message in one commit; (4) a helper's `kind:"delegate"` answer (children). A
repeated operation id returns the same task; a different payload under it is an operation-id conflict.

Evidence: `engine:internal/orchestrator/assistants_declared_test.go` `TestDeclaredAssistants_IdempotentOnRetry`,
`TestDeclaredAssistants_NewGenerationAttachesNewTask`;
`engine:internal/orchestrator/agentcomm_spawn_test.go` `TestSpawnHelper_CreatesTaskAndRequestInOneCommit`,
`TestSpawnHelper_UnsupportedTriggerWritesNothing`; `engine:internal/orchestrator/assistants_execution_test.go`
`TestAttach_RepeatAfterConfigChangeKeepsTheBinding`; code `attachDeclaredAssistants`, `SpawnHelper`, `replayedTopLevelTask`.

#### HLP-S02 Retry does not duplicate a declared helper
- GIVEN a declared helper attached for root generation 0
- WHEN the stage retries without a generation bump
- THEN the attach replays the committed operation and no second task appears.

### HLP-R03 Initial status
Attached to a `pending` stage, a task stays `waiting_parent` and is not started when the stage starts. Attached to a
started stage: `one_shot` → `queued` (scheduled at once); `checkpoints`, `on_completion`, `on_message` → `watching`.

Evidence: `engine:internal/orchestrator/assistants_test.go` `TestAttach_ToPendingStageStaysWaitingParent`;
`engine:internal/orchestrator/assistants_on_message_test.go` `TestOnMessageAdmission_AttachRestsInWatchingWithoutStarting`;
code `admitTopLevelTask`.

#### HLP-S06 Attach to a pending stage
- GIVEN a required one_shot helper attached while its stage is `pending`
- WHEN the stage starts and its agent finishes
- THEN the task is still `waiting_parent` and the stage stays `awaiting_assistants`.

### HLP-R04 one_shot lifecycle
A `one_shot` task runs exactly one turn: `queued → running → completed` (result committed in the same record) or
`failed`. It does not start again by itself. Its root stage finishing meanwhile does not stop it; its later result is
committed and never reopens the stage. Its outcome never moves the stage FSM except through the barrier ([HLP-R10](../helpers-barrier/spec.md)).

Evidence: `engine:internal/orchestrator/watch_tasks_test.go` `TestTrigger_OneShotRunsOnce`;
`engine:internal/orchestrator/assistants_test.go` `TestLifecycle_HappyPath`, `TestParentDone_ClosesNewTriggers`;
code `commitHelperResult`.

#### HLP-S03 Late advisory result after stage done
- GIVEN an advisory one_shot turn running
- WHEN the stage reaches `done` and then the turn returns `pass`
- THEN the task becomes `completed` with the result and the stage stays `done`.

### HLP-R05 checkpoints watcher
A `checkpoints` task is fed the stage agent's output; a collector (debounce 3s, cooldown 5s, ≥200 new bytes, batch
50, at most 20 passes) commits a durable checkpoint and requeues the task. A successful non-final pass returns to
`watching`; the final pass ends `completed`. When the stage's own work ends (`EvAwaitAssistants`/`EvComplete`) a
`required` watcher gets one forced final pass; an advisory one gets none. Stage failure stops new passes and keeps
earlier results.

Evidence: `engine:internal/orchestrator/watch_tasks_test.go` `TestTrigger_RequiredWatcherHasFinalBoundedPass`,
`TestTrigger_ParentFailedStopsNewPasses`; `engine:internal/watch/collector.go`
`DefaultConfig`; code `finalizeWatchersForStage`, `commitAndFire`.

#### HLP-S07 Forced final pass of a required watcher
- GIVEN a required `checkpoints` watcher resting in `watching`
- WHEN the stage agent finishes and the stage enters `awaiting_assistants`
- THEN one final pass runs, the task ends `completed`, and its verdict decides the gate.

### HLP-R06 on_completion: start only after the stage result
A stage's top-level `on_completion` helper starts only while its stage, in the current root generation, is
`awaiting_assistants` or `done` (or paused from `awaiting_assistants`); the check is repeated in the start commit and
after the command slot is granted. Output, messages, Continue, restart and loop ticks never start it early. Its pass is
issued by the live completion, a Continue, a restart, or an Attach to a stage that already has its result. It runs
exactly one pass and then ends `completed` (or `failed`). A delegated child with this trigger is not gated.

Evidence: `engine:internal/orchestrator/watch_tasks_test.go` `TestCompletionHelper_DoesNotRunBeforeParentResult`,
`TestCompletionHelper_IssuedOncePerPass`, `TestCompletionHelper_PauseDuringThePassDoesNotRunItAgain`,
`TestCompletionHelper_AttachedAfterTheStageResultRunsOnce`, `TestCompletionHelper_ReviseBeforeStartCommitRejectsTheStart`,
`TestCompletionHelper_DelegatedChildIsNotGated`;
`engine:internal/orchestrator/agentcomm_wait_completion_test.go` `TestWaitCompletion_HelperStartsOnlyAfterVerificationPasses`;
code `completionHelperGated`, `issueCompletionPasses`.

#### HLP-S04 Required on_completion gates the stage once
- GIVEN a required on_completion helper and a stage whose agent finished and verify passed
- WHEN the stage enters `awaiting_assistants`
- THEN the helper runs one pass; on `pass` the stage becomes `done`, otherwise it stays `awaiting_assistants`.

### HLP-R07 on_completion and stage failure
When the stage fails, each not-yet-started pass of the current generation is cancelled with reason
`parent stage failed before completion: <stage>, seq=<n>` (no verdict, no waiver needed), on every path: the failure
transition, restart, scheduling, each return to `queued`. A stage Retry restores exactly those passes to `watching` in a
new task generation; a user-cancelled pass or a started-and-failed helper is not restored.

Evidence: `engine:internal/orchestrator/completion_pass_failure_test.go`
`TestCompletionHelper_ParentFailureCancelsThePendingPass`, `TestCompletionHelper_ParentFailureCancelsAQueuedPass`,
`TestCompletionHelper_ParentFailureRetryRestoresThePass`, `TestCompletionHelper_ParentFailureRetryRestoresNothingElse`; code `cancelCompletionPassOfFailedStage`, `restoreCompletionPassesAfterRetry`.

#### HLP-S08 Failure cancels, Retry restores
- GIVEN an on_completion helper in `watching` and its stage running
- WHEN the stage fails, then is retried
- THEN the task is `cancelled` with the `parent stage failed before completion:` reason, then `watching` again with a
  bumped task generation, and it starts when the new attempt has its result.

### HLP-R09 Turn end, helper end and the stage pass are distinct
(a) A **turn** ends when its outcome commits: to `watching` (watcher non-final pass, on_message request done),
`queued` (run agent idle, pause/budget/usage-limit requeue), `waiting_peer`, `waiting_children`, or a terminal status.
(b) A **helper** ends only on a terminal status: `one_shot`/`on_completion` after their one turn, a watcher after its
final pass, an on_message helper only when the system closes it, any task on failure or cancel. (c) A stage's
**on_completion pass** is the single turn of an on_completion helper issued after the stage agent's own result; it is
case (a)+(b) at once and is the only trigger tied to the stage's result.

Evidence: `engine:internal/orchestrator/assistants_lifecycle.go` `commitHelperResult`;
`engine:internal/orchestrator/assistants_on_message_lifecycle_test.go` `TestOnMessage_EachRequestGetsItsOwnTurnResultAndReply`;
`engine:internal/orchestrator/watch_tasks_test.go` `TestTrigger_OnCompletionRunsAtStageResultReady`.

#### HLP-S10 Request done is not helper done
- GIVEN an on_message helper that has answered request m1
- WHEN the turn commits its result
- THEN the request's outcome is `result`, the task is back in `watching` (not terminal), and the stage's own
  status is unchanged.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HLP-E01 | Attach to `done`/`failed` stage | `invalid_status`, nothing written | `TestAttachRequiredRacesWithComplete` |
| HLP-E02 | Attach to script stage | `invalid_request` | `TestAttach_RejectedForScriptStage` |
| HLP-E03 | Spawn trigger not one_shot/on_message, or non-stage actor | `invalid_request` / `actor_not_allowed`, nothing written | `TestSpawnHelper_UnsupportedTriggerWritesNothing` |

## Coverage gaps and discrepancies
- **HLP-G02** (code≠comment): `resumeAssistantsAfterRestart` says `waiting_parent` "is reactivated by the stage's own
  startup path"; no code promotes it (Attach doc and `TestAttach_ToPendingStageStaysWaitingParent` agree it never starts).
- **HLP-G03** (ambiguous/untested): a stage Pause gates only on_message and on_completion turns
  (`stageGateRequeueReason`); running/queued `one_shot` and `checkpoints` helpers keep running. No test pins this.
- **HLP-G04** (code≠contract doc): `TaskStatus.Terminal` says a terminal task never changes, but a loop tick requeues a
  `completed` non-on_message helper (`loopRestartStatus`; `TestLoopHelper_FinishedHelperRunsAgainOnEachEligibleSlot`).
- **HLP-G08** (untested/ambiguous): an advisory `checkpoints` watcher resting in `watching` when its stage reaches
  `done` gets no final pass and no code path found ends it; `RunBlocker` counts `watching` as blocking, so it may hold
  the run open. Check `finalizeWatcherOnCompletion` + `runStateAllowsExitCore`.
- **HLP-G09** (untested): HLP-S06 (a `waiting_parent` required task blocks the barrier forever) follows from
  `RequiredBlocker`; only the doc ("can prevent the stage from finishing") states it.
