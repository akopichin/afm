# Stage lifecycle: retry, recovery, exit

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Automatic retry of agent process failures and manual Retry of a failed stage.
- Per-status restart recovery and `auto_recover`.
- Engine side of exit-on-fail and of the workflow's natural end.
- Family (prefix `STG`, disjoint ranges): FSM, scheduling, Pause/Continue —
  [stage-lifecycle](../stage-lifecycle/spec.md) (`R01–R10`); this file `R11–R19`; tracks, scripts, artifacts,
  dynamic stages — [stage-lifecycle-tracks](../stage-lifecycle-tracks/spec.md) (`R20+`).
- Out of scope: verify correction budget ([verify](../verify/spec.md)), required-helper failure
  ([helpers](../helpers-lifecycle/spec.md)), usage-limit parks ([budgets-accounting](../budgets-accounting/spec.md)),
  run resolution and offline Retry ([run-lifecycle-resume](../run-lifecycle-resume/spec.md)).

## Terms
- **retry context** — excerpt of the failed attempt's log prepended to the rerun's prompt.
- **auto_recover** — config switch (default true) resetting `failed` stages on start.

## Requirements

### STG-R11 Automatic retry of agent process failures
A classified-retryable failure moves the stage to `retrying` and reruns the phase with a retry context: known
transient (rate limit, server, network) up to 15 retries; an unrecognized failure of a started process up to 3;
a process that could not start fails at once. Delay is `5s·2^attempt` capped at 60s, minus up to 20% jitter.

Evidence: engine:internal/orchestrator/retry_budget_test.go `TestTransportRetryDecision_BudgetBoundaries`,
`TestRunWithRetry_UnknownErrorUsesLimitedBudget`, `TestRunWithRetry_KnownTransientUsesFullBudget`,
`TestRunWithRetry_StartFailureFailsImmediately`, `TestRunWithRetry_NoticeShowsActualDelay`;
engine:internal/orchestrator/retry_backoff_test.go `TestBackoffFor_Schedule`, `TestJittered_WithinBounds`;
engine:internal/orchestrator/retry.go `MaxRetries`, `MaxUnknownRetries`, `backoffFor`

#### STG-S04 Exhaustion fails the stage
- GIVEN an agent that keeps returning a server error
- WHEN the retry budget is spent
- THEN the stage is `failed` and unstarted dependent stages remain pending.

### STG-R12 Manual Retry of a failed stage
`Retry` is accepted only for `failed`. Exactly one caller wins the `manual_retry` CAS (→ `pending`); only the
winner clears interactive phase sessions and restarts: script/auto stages go to `ready`; a stage with `plan.md`
skips planning; otherwise planning restarts. A retried stage whose deps are not done stays `pending`.

Evidence: engine:internal/orchestrator/race_fixes_test.go `TestRetry_LostCASDoesNotClearInteractiveSessions`;
engine:internal/orchestrator/scheduling_test.go `TestRetryWithExistingPlanWaitsForDependencies`;
engine:internal/orchestrator/retry_cascade_test.go `TestIntegration_ManualRetryAutonomousWaitsForDeps`;
engine:internal/orchestrator/integration_retry_test.go `TestIntegration_ManualRetryWaitsForDeps`;
engine:internal/orchestrator/scheduling.go `retryStage`

### STG-R13 Restart recovery per status
On start: `auto_recover` (default true) resets every `failed` stage to `pending`; `done`, `failed`,
`awaiting_approval`, `ready`, `paused`, `awaiting_peer` are left as is; `planning` adopts a complete `plan.md`
or replans; `running`/`revising` restart (revising with its feedback); `retrying` re-enters via `ready` (or completes
by probe when no verify); `awaiting_user_input` and `hook_failed` resume their wait; `awaiting_assistants`
re-checks the gate. A fresh start runs the before-hook; a resumed agent does not.

Evidence: engine:internal/orchestrator/recovery_auto_test.go `TestAutoRecover_ResumesFailedStageWithoutManualRetry`,
`TestAutoRecover_Disabled_LeavesFailedStageUntouched`; engine:internal/orchestrator/integration_resume_test.go
`TestIntegration_ResumeFromRetrying`, `TestIntegration_ResumeFromPlanningWithExistingPlan`,
`TestIntegration_ResumeFromRevising`; engine:internal/engine/restart_integration_test.go
`TestRestart_AwaitingApprovalWaitsThenResumes`, `TestRestart_FailedStageWaitsForRetry`;
engine:internal/orchestrator/recovery.go `startPlanningForPending`, `resumeStageAtStatus`

#### STG-S11 Approval and answers survive a restart
- GIVEN one stage in `awaiting_approval` and one in `awaiting_user_input` when the process stops
- WHEN `afm run` resumes the run
- THEN the first still waits for Approve (no agent starts) and the second shows its open question again.

### STG-R14 Exit-on-fail inside the engine
With `ExitOnFail`, the first `failed` committed while the run context is alive is recorded (reason redacted) and
cancels the run; later failures and CAS misses do not replace it; `retrying` and `hook_failed` do not trigger it.
Finalization refuses while any stage is failed. Without `auto_recover`, a resumed run stops on the earliest
failed stage before any agent starts. CLI exit codes: [run-lifecycle-cli](../run-lifecycle-cli/spec.md) RUN-R45.

Evidence: engine:internal/orchestrator/exit_on_fail_internal_test.go
`TestExitOnFailCommittedTransitionRecordsFirstFailure`, `TestExitOnFailCASMissIsNotAFailure`,
`TestExitOnFailConcurrentFailures`, `TestExitOnFailSecretsAreRedacted`, `TestExitOnFailFinalizeRejectsLateTransition`;
engine:internal/orchestrator/exit_on_fail_test.go `TestExitOnFailNonTerminalBoundaries`,
`TestExitOnFailStartupBeforeAgentStarts`, `TestExitOnFailStartupAutoRecover`;
engine:internal/orchestrator/exit_on_fail.go `applyTransition`, `startupStop`

#### STG-S12 Retrying is not a failure
- GIVEN `ExitOnFail` and a stage whose agent hits a retryable error
- WHEN the stage is `retrying`
- THEN the run continues; only a committed `failed` stops it.

### STG-R15 Natural end of the run
Without `HumanControl`, the workflow ends after accepted work drains when every stage is terminal or waiting on a failed dependency chain. Any stage with its own failure makes the run end as failed. With `HumanControl`, a natural end requires all stages to be done, so a failed stage remains available for Retry.

Evidence: engine:internal/orchestrator/integration_failure_test.go `TestIntegration_DashboardStaysAliveOnAllFailed`,
`TestIntegration_DashboardRetryAfterAllFailed`; engine:internal/orchestrator/scheduling.go `runStateAllowsExitCore`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| STG-E11 | Retry of a non-failed stage | `KindInvalid`; nothing changes | engine:internal/engine/commands_integration_test.go `TestCommands_RejectedInTheWrongStatus` |
| STG-E12 | Concurrent Retry loses the CAS | silent no-op, sessions untouched | engine:internal/orchestrator/race_fixes_test.go `TestRetry_LostCASDoesNotClearInteractiveSessions` |
| STG-E13 | Retry: no `plan.md` and no `plan:` source, no planning agent | stage `failed` "no plan.md and no plan source configured" | engine:internal/orchestrator/scheduling.go `retryStage` |
| STG-E14 | Exit-on-fail with a failed stage, `auto_recover: false`, on resume | run stops before any agent, `StageFailedError` | `TestExitOnFailStartupBeforeAgentStarts` |

## Coverage gaps and discrepancies
- **STG-G11** (untested): the 15 / 3 retry counts and delay schedule are tested at decision level
  (`transportRetryDecision`, `backoffFor`); no end-to-end test runs the full 15 retries.
- **STG-G12** (untested): E13 (retry without plan source) has no dedicated test.
- **STG-G13** (code≠doc): `stage-lifecycle.md` says interrupted `retrying` stages "restart"; code first tries to
  park a yielded turn on its waits and, for stages without planning and verify, completes from the execution probe.
