# Dialog and approval: plans, notes, revise

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Plan approval: legacy `Approve`, conditional `ApprovalSnapshot` / `ApproveExpected`, `auto_approve`, headless policy.
- Revise (restart with note) from `awaiting_approval`, `running`, `awaiting_assistants`; agent-note feed record;
  stage buttons; pre-notes for pending stages.
- Family (prefix `DLG`, disjoint ranges): this file `R01–R19`; agent questions and user answers —
  [dialog-approval-questions](../dialog-approval-questions/spec.md) (`R20+`).
- Out of scope: FSM and Pause/Continue ([stage-lifecycle](../stage-lifecycle/spec.md)), offline
  `afm approve` / `afm revise` ([run-lifecycle-resume](../run-lifecycle-resume/spec.md) RUN-R25), helper
  generations after Revise ([helpers](../helpers-lifecycle/spec.md)), verify ([verify](../verify/spec.md)), HTTP mapping
  ([http-api](../http-api/spec.md)).

## Terms
- **waiting** — one entry into `awaiting_approval`, identified by the seq of its `plan_ready` transition.
- **revision** — opaque token `v1:<seq>:<sha256(domain, run, stage, seq, plan bytes)>`; compare for equality only.
- **planning-only stage** — a stage without an implementation agent; approving it completes it.
- **HumanControl** — a human surface exists to approve/retry (afm: server port > 0).

## Requirements

### DLG-R01 A ready plan waits for approval
When the planning agent finishes with a complete `plan.md`, the stage commits `plan_ready` → `awaiting_approval`
(also from `retrying`/`awaiting_user_input`). A complete plan found on restart is adopted the same way.

Evidence: engine:internal/orchestrator/orchestrator.go `onAgentCompleted`; engine:internal/orchestrator/recovery.go
`recoverPlan`; engine:internal/orchestrator/integration_resume_test.go `TestIntegration_ResumeFromPlanningWithExistingPlan`

### DLG-R02 Legacy Approve
`Approve` requires `awaiting_approval` at the command boundary, then commits `approve` (→ `ready`) or, for a
planning-only stage, `complete` (→ `done`, `script_after` runs once). Only the CAS winner triggers scheduling; a
loser returns success without effects. Work is spawned under the run context, not the request context.

Evidence: engine:internal/engine/commands_integration_test.go `TestCommands_ConcurrentApproveStartsImplementationOnce`;
engine:internal/orchestrator/approve_test.go `TestApproveStage_DurableTransition`;
engine:internal/orchestrator/lifecycle_scripts_test.go `TestIntegration_PlanningOnlyStage_RunsScriptAfterOnApprove`;
engine:internal/orchestrator/runctx_test.go `TestApprove_SpawnsAgentUnderRunCtx_NotRequestCtx`;
engine:internal/orchestrator/control_api.go `approveStage`, `commitApproval`

### DLG-R03 auto_approve and headless policy
`auto_approve: true` approves the plan as soon as it is ready, regardless of `HumanControl` and
`RequireApproval`. Otherwise, without `HumanControl`: `RequireApproval` fails the stage ("approval required but no
dashboard running …"), else the plan is auto-approved. With `HumanControl` the stage waits.

Evidence: engine:internal/orchestrator/auto_approve_test.go `TestAutoApprove_WinsOverRequireApproval`,
`TestAutoApprove_DefaultFalse_RequireApprovalStillFails`, `TestAutoApprove_WithDashboardAttached_SkipsManualClick`,
`TestAutoApprove_RecoveryDefaultCase_NoBusHelperNeeded`; engine:internal/orchestrator/control_api.go `autoApproveIfConfigured`

#### DLG-S01 Headless CI
- GIVEN no dashboard, `--require-approval`, a stage without `auto_approve`
- WHEN its plan is ready
- THEN the stage is `failed` with the approval-required reason.

### DLG-R04 Approval snapshot binds the plan to a revision
`ApprovalSnapshot` returns `RunID`, `StageID`, the exact bytes of `plan.md` and a revision, read in one step under
the Store lock. It requires that the stage's latest transition is the `plan_ready` that entered the waiting and
that `plan.md` is a regular file ≤ 4 MiB; `run.Capabilities().ConditionalApproval` reports support.

Evidence: engine:internal/orchestrator/approval_test.go `TestConditionalApproval_SnapshotBindsPlanAndRevision`,
`TestConditionalApproval_SnapshotRefusesStageOutsideWaiting`, `TestConditionalApproval_SnapshotUnavailableWithoutReadablePlan`,
`TestConditionalApproval_LegacyLoadWaitingHasNoRevision`; engine:internal/orchestrator/approval.go `ApprovalSnapshot`

### DLG-R05 ApproveExpected commits only the revision that was read
`ApproveExpected` checks the revision inside the durable transition (`ExpectFrom`, `ExpectStageSeq`, plan-hash
`Fence`), so a competing revise, plan edit or approval wins cleanly and the loser changes nothing. A lost CAS is
re-checked under the lock: changed → `approval_stale`, closing run → `closed`, unchanged → one more attempt.
Success returns `ApprovedRevision` and the approval's `TransitionSeq`; side effects run once.

Evidence: engine:internal/orchestrator/approval_test.go `TestConditionalApproval_ApprovesReadVersion`,
`TestConditionalApproval_StateChangeBeforeCommitIsStale`, `TestConditionalApproval_PlanChangeBeforeCommitIsStale`,
`TestConditionalApproval_ConcurrentReviseHasOneWinner`, `TestConditionalApproval_ConcurrentClientsOneTransition`,
`TestConditionalApproval_LegacyApproveWinsAtCommit`, `TestConditionalApproval_PlanningOnlyAfterHookRunsOnce`;
engine:internal/engine/approval_integration_test.go `TestConditionalApproval_ConcurrentClientsStartImplementationOnce`

#### DLG-S02 Revise during review
- GIVEN a reviewer holding a snapshot revision
- WHEN another client revises the plan, then the reviewer calls ApproveExpected
- THEN the result is `approval_stale` and the stage is not approved; it replans with the feedback.

### DLG-R06 What changes a revision
A revision changes with a new waiting or different plan bytes and differs across runs and stages; events of
other stages, messages, helper activity and a restart with the same data dir do not change it. A repeat of a
committed approval reports `approval_stale`; the result is not a deduplication receipt.

Evidence: engine:internal/orchestrator/approval_test.go `TestConditionalApproval_PlanBytesDefineRevision`,
`TestConditionalApproval_OtherActivityKeepsRevision`, `TestConditionalApproval_RepeatAfterSuccessIsStale`,
`TestConditionalApproval_RevisionOfAnotherRunIsStale`; engine:internal/engine/approval_integration_test.go
`TestConditionalApproval_RestartKeepsRevision`, `TestConditionalApproval_ReviseMakesRevisionStale`

### DLG-R07 Revise (restart with note)
`Revise` accepts non-empty feedback for a non-script stage in `running`, `awaiting_approval` or
`awaiting_assistants`. It commits `revise` with `ExpectFrom` the decided status before returning, then saves
`feedback.md`. From `awaiting_approval` the current plan is renamed `plan.v<N>.md` and planning reruns with the
feedback; from `running` the live agent is interrupted and the same phase restarts with it; from
`awaiting_assistants` the helper generation is bumped and a fresh execution pass starts. A lost CAS reports
`applied=false`.

Evidence: engine:internal/orchestrator/approve_test.go `TestRevise_DurableTransition`;
engine:internal/orchestrator/integration_resume_test.go `TestIntegration_ReviseFeedsBackOriginalPlanContent`;
engine:internal/orchestrator/revise_awaiting_assistants_test.go `TestRevise_FromAwaitingAssistantsStartsExecutorWithFeedback`;
engine:internal/orchestrator/retry_test.go `TestRunWithRetry_LateCompletionDoesNotCompleteRevisingStage`;
engine:internal/orchestrator/control_api.go `Revise`

#### DLG-S03 Pause wins over a racing note
- GIVEN a running stage and a concurrent Pause committed after the Revise status read
- WHEN Revise applies
- THEN it is not applied, no feedback is delivered and the client gets a conflict.

### DLG-R08 Agent note in the feed
An applied Revise publishes one `agent_note` payload `{id: <revise seq>, text: <snippet>}` live and appends the
same payload to `notices.jsonl`; a not-applied Revise publishes nothing and the caller keeps its text.

Evidence: afm:pkg/server/agent_note_feed_test.go `TestHandleRevise_EmitsAgentNoteOnApplied`,
`TestHandleRevise_NoOpDoesNotEmitAgentNote`, `TestHandleRevise_ScriptStageRejected`;
engine:internal/engine/stage_control.go `StageControl.Revise`

### DLG-R09 Stage buttons
`Button` takes a declared button name for a non-script stage in `awaiting_approval` or `running`; the prompt is
resolved from the flow by the engine and delivered through Revise. A Revise that does not apply is a conflict.

Evidence: engine:internal/orchestrator/button_test.go `TestButton_DeliversPromptViaRevise`,
`TestButton_UnknownNameIsNoOp`; afm:pkg/server/command_contract_test.go `TestCommandContract_Button`;
engine:internal/engine/stage_control.go `StageControl.Button`

### DLG-R10 Pre-note for a pending stage
`SetPreNote` stores a note atomically in `<stage>/prenote.md` for a `pending` non-script stage; empty text removes
it. It is added to the stage's first fresh prompt only (feedback paths do not repeat it). If the stage started
during the write, the command returns a conflict.

Evidence: engine:internal/engine/commands_integration_test.go `TestCommands_PreNoteLifecycle`,
`TestCommands_PreNoteRacingStageStartConflicts`; engine:internal/orchestrator/integration_prenote_test.go
`TestIntegration_PreNoteReachesFreshPrompt`; engine:internal/state/state.go `SavePreNote`

### DLG-R11 Plan and question line comments are text
The dashboard composes line comments on a plan into Revise feedback and on a question into a free-text answer
(`from_options=false`); the engine sees only that text and never treats it as approval or option choice.

Evidence: afm:docs/dashboard.md; engine:docs/dialog.md; engine:internal/engine/dialog_integration_test.go
`TestCommands_LegacyRestrictedQuestionAcceptsFeedback`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| DLG-E01 | Approve/Revise/Button/Answer/Continue during a review pause | `KindConflict`, `flow_paused`; ApproveExpected commits nothing | engine:internal/orchestrator/approval_test.go `TestConditionalApproval_ReviewPauseRejectsApprove` |
| DLG-E02 | Stale revision (left waiting, new waiting, plan changed, unknown token) | `KindConflict`, `approval_stale` | `TestConditionalApproval_StaleAfterRevise` |
| DLG-E03 | `RunID` is another run | `KindConflict`, `approval_run_changed` | `TestConditionalApproval_RequestIdentityErrors` |
| DLG-E04 | No consistent snapshot (legacy waiting, plan missing/irregular/>4 MiB, ops not wired) | `KindConflict`, `approval_unavailable` | `TestConditionalApproval_UnreadablePlanIsUnavailableNotStale`, engine:internal/engine/approval_integration_test.go `TestConditionalApproval_CapabilityAndRequestErrors` |
| DLG-E05 | Run closing with a matching revision | `KindClosed` | `TestConditionalApproval_ClosedRunIsNotStale` |
| DLG-E06 | Storage failure at commit | `KindInternal`, not a conflict | `TestConditionalApproval_StorageFailureIsNotAConflict` |
| DLG-E07 | Snapshot of a stage not awaiting approval / unknown stage | `KindInvalid` / `KindNotFound` | `TestConditionalApproval_SnapshotRefusesStageOutsideWaiting` |
| DLG-E08 | Revise/Button: wrong status, script stage, empty text, unknown button | `KindInvalid` | engine:internal/engine/commands_integration_test.go `TestCommands_RejectedInTheWrongStatus` |
| DLG-E09 | Revise/Button not applied (lost race) | `KindConflict` "stage no longer accepts …" | afm:pkg/server/command_contract_test.go `TestCommandContract_Revise` |
| DLG-E10 | Pre-note on non-pending or script stage / stage started during save | `KindInvalid` / `KindConflict` | `TestCommands_PreNoteLifecycle`, `TestCommands_PreNoteRacingStageStartConflicts` |

## Coverage gaps and discrepancies
- **DLG-G01** (doc-only): the approval guarantee ends at commit — implementation re-reads `plan.md` without
  re-checking the revision (engine:docs/approval.md). No test pins this boundary.
- **DLG-G02** (code≠doc): engine `Revise` also accepts `awaiting_assistants`, but `Button` does not
  (`buttonAllowed`); dashboard docs list only Restart with note for that status. Intentional per code, undocumented for buttons.
- **DLG-G03** (doc-only): DLG-R11 is a frontend contract; the engine has no notion of line comments.
- **DLG-G04** (ambiguous): legacy `Approve` losing its CAS reports success to the caller (no conflict), unlike
  Revise/Button; only the stage state tells whether it took effect.
