# Budgets and accounting: limits and enforcement

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Budget declaration (`afm:contracts/budget` `Spec`), scopes and nesting.
- Admission of work, `hard`/`medium`/`info` policies, holds, money-rate and accounting-failure holds.
- Threshold notices, persistence and replay of budget decisions.
- Out of scope: usage collection (`../budgets-accounting/spec.md`), control API, exporter, run outcome and
  CLI (`../budgets-accounting-control/spec.md`), flow parsing (`../flow-config/spec.md`).

## Terms
- **Scope**: `run`, `stage:<id>`, `task:<id>` (helper or run agent). Chains: run → stage → task; run → run agent; task → task.
- **Hold**: budget-owned stop of a scope (reasons: limit reached, parent limit, `rates_missing`, `accounting_unavailable`).
- **Episode**: span between a limit becoming active and being raised/reset; thresholds fire once per episode.

## Requirements

### BUD-R20 Budget declarations are strict
A budget is `tokens` (`k`/`m` suffix), `money` (`$` suffix) or both, plus `stop_policy` (`hard` default,
`medium`, `info`). It is accepted on the flow, a stage, an `assistants:` entry, a `team` member and a
collaboration profile; unknown keys, empty or `null` budgets are rejected with the key named. An explicit
declaration replaces a profile default as a whole.

Evidence: `afm:contracts/budget/spec_test.go` `TestParseTokens`, `TestParseMoney`, `TestSpecValidate`;
`afm:contracts/flow/budget_test.go` `TestBudget_ParsesInEveryFlowContainer`, `TestBudget_RejectsInvalidDeclarations`,
`TestBudget_NullErrorNamesTheKey`; `afm:contracts/config/collaboration_budget_test.go` `TestProfileBudget_ParsesAndReplacesAsWhole`;
`engine:internal/orchestrator/budget_scope_test.go` `TestBudgetProfileInstancesHaveSeparateCountersAndExplicitReplacesProfile`

#### BUD-S20 Bare budget
- GIVEN a stage with `budget:` and no value
- WHEN the flow is loaded
- THEN loading fails and the error names the `budget` key

### BUD-R21 Admission checks every ancestor
Every start of work (stage agent, verify, repair turns, helper/run-agent turns, scripts, memory and
finalization AI calls, creation of dynamic stages/helpers/run agents) asks the controller; work starts
only if every scope on its path allows it. A child limit never lifts a parent limit; a run agent is not
under any stage. The Dashboard Assistant is outside budgets.

Evidence: `engine:internal/orchestrator/budget_policy_test.go` `TestBudgetTaskChainAdmission`, `TestBudgetResumeChecksEveryAncestor`;
`engine:internal/orchestrator/budget_control_test.go` `TestBudgetRunAgentScopeUnderTheRun`;
`engine:internal/orchestrator/admission_budget_test.go` `TestAdmitStageSpawn_ScriptStageAsksForScriptKind`;
`engine:internal/orchestrator/budget_aux_calls_test.go` `TestShellVerify_RefusedUnderAHardHold`

#### BUD-S21 Run budget overrides a generous stage budget
- GIVEN a run limit that is reached and a stage with an unspent stage limit
- WHEN the stage would start
- THEN admission is refused because of the parent

### BUD-R22 Refusal holds without failing; pending stages stay pending
A refusal first commits the hold, then stops the start. A started stage is paused; a `pending`/`ready`
stage keeps its status and is simply not admitted. No stage becomes failed, no retry attempt is spent,
`ExitOnFail` and auto-recovery do not react. Refused helpers create no task, or stay queued and start later.

Evidence: `engine:internal/orchestrator/admission_budget_test.go` `TestAdmitStageSpawn_RefusalPausesStageFromAllowedStatus`,
`TestAdmitStageSpawn_RefusalNeverPausesPendingStage`, `TestStartPlanningForUnblocked_BudgetRefusalKeepsStagePending`;
`engine:internal/orchestrator/assistants_budget_test.go` `TestAttach_RefusedByBudgetCreatesNoTask`,
`TestMaybeScheduleTask_BudgetRefusalKeepsTaskQueuedAndLaterStarts`;
`engine:internal/orchestrator/budget_aux_calls_test.go` `TestQuestionRepair_BudgetDoesNotSpendAttemptsAndCountsSurviveARestart`

#### BUD-S22 Pending stage under a reached limit
- GIVEN a `pending` stage whose run limit is reached
- WHEN scheduling runs
- THEN the stage stays `pending` (not paused) and no agent starts

### BUD-R23 `hard` interrupts the scope subtree only
At 100% of a `hard` limit nothing new starts in the scope, running agents of the scope and its subtree are
interrupted, scripts are killed (Linux) and the stage paused. Stages outside the scope keep running.

Evidence: `engine:internal/orchestrator/budget_policy_test.go` `TestBudgetHardRunPausesRunningStagesOnly`,
`TestBudgetHardStageLeavesNeighboursRunning`; `engine:internal/orchestrator/script_budget_unix_test.go`
`TestRunScriptStage_HardBudgetStopKillsScriptAndPausesStage`; `engine:internal/orchestrator/budget_edges_test.go`
`TestBudgetAttempts_HardStopCancelsTheRunningMemoryCall`

#### BUD-S23 Neighbour unaffected
- GIVEN stages `a` (hard stage limit) and `b` running in parallel
- WHEN `a` reaches its limit
- THEN `a` is paused and held, `b` continues

### BUD-R24 `medium` drains a durably fixed work set
At 100% of a `medium` limit the set of already started work is journaled; it may finish including verify,
repairs, child helpers and in-flight task requests; new stages, messages and loop ticks do not start and
`View.Draining` is set. A local `hard` limit, rates or accounting holds are stronger; a medium hold turns hard
when the set is empty.

Evidence: `engine:internal/orchestrator/budget_policy_test.go` `TestBudgetMediumFixesTheWorkSet`,
`TestBudgetMediumWorkAllowsOnlyRequestsInFlight`; `engine:internal/orchestrator/budget_medium_test.go`
`TestBudgetMediumTaskSetIsFixedFromTheJournalAndReplayed`; `engine:internal/orchestrator/budget_decide_test.go`
`TestBudgetMediumHoldBecomesHardWithoutNewUsage`, `TestBudgetMoneyAndAccountingHoldsOverrideAMediumHold`

### BUD-R25 `info` only warns; overshoot is kept
An `info` limit produces notices only; an `info`-only run is not limited. The call that crosses a limit keeps
its result and late spend is still counted. With both metrics set, either reaching 100% counts.

Evidence: `engine:internal/orchestrator/budget_recovery_test.go` `TestBudgetLateUsageAfterResumeCountsWithoutHold`,
`TestBudgetAccountingFailureUnderInfoOnlyWarns`; `afm:contracts/budget/spend_test.go` `TestEvaluate`

### BUD-R26 Money limits require explicit rates
A money limit requires a complete explicit card for the command/model of all work under it. A known model
without one fails `Open`/start with `budget_rates_missing`; dynamic work is refused and the money scope holds
`rates_missing`; a model seen only in a result (unpriced) holds money scopes until that model is fixed. Past
records are never repriced.

Evidence: `engine:internal/orchestrator/budget_rates_test.go` `TestBudgetDynamicRatesRefusalHoldsMoneyScopes`,
`TestBudgetResumeAfterUnpricedResultNeedsThatModelFixed`, `TestBudgetMoneyChecksKnownTaskCommands`;
`engine:internal/engine/budgets_test.go` `TestBudgets_MoneyWithoutRatesIsRefused`;
`afm:contracts/accounting/explicit_rates_test.go` `TestCheckExplicitRates`

#### BUD-S24 Unpriced result
- GIVEN a money limit and an agent whose result reports an unrated model
- WHEN the record is written
- THEN money scopes hold with `rates_missing` and resume is refused until rates for that model exist

### BUD-R27 Accounting failure holds limited scopes
If usage cannot be read or written while a `hard`/`medium` limit applies, every such scope holds with
`accounting_unavailable`; unlimited and `info` scopes continue. Enabling a limit on a run without accounting
holds. Storage failures of budget decisions are never swallowed.

Evidence: `engine:internal/orchestrator/budget_recovery_test.go` `TestBudgetAccountingFailureHoldsLimitedScopes`,
`TestBudgetOpenWithoutAccountingHoldsLimitedScopes`; `engine:internal/orchestrator/budget_rates_test.go`
`TestBudgetUpdateEnablingALimitWithoutAccountingHolds`; `engine:internal/orchestrator/budget_decide_test.go`
`TestBudgetStorageFailureIsNeverSwallowed`

### BUD-R28 Threshold notices fire once per episode
`budget_warning` at 80% and `budget_limit_reached` at 100% go live and to `notices.jsonl` after the durable
threshold record; a jump past 100% emits only the limit notice. One notice per scope, metric, level and
episode: reconnects, parallel plans and restarts do not repeat it; a policy-only change does not repeat the
warning, a new limit may. `budget_updated` / `budget_resumed` accompany overrides and releases.

Evidence: `engine:internal/orchestrator/budget_spend_test.go` `TestBudgetThresholdsFireOncePerEpisode`,
`TestBudgetJumpPastLimitSkipsWarning`, `TestBudgetPolicyChangeDoesNotRepeatWarningNewLimitDoes`,
`TestBudgetParallelWarningIsPublishedOnce`; `engine:internal/orchestrator/budget_decide_test.go`
`TestBudgetLimitNoticeRepeatsInANewEpisodeOfTheSameLimit`

### BUD-R29 Decisions are journaled; spend is rebuilt on Open
Scope declarations, overrides with revisions, holds, thresholds and the medium set are events in
`events.jsonl`; spend is rebuilt from `usage.jsonl` per `Open`. `Open` decides usage written before its
decision (crash window) before starting work, and attributes child usage to all ancestors. An unchanged
declaration keeps its runtime override; a changed declaration drops only that scope's override. A run without
budgets writes no budget records.

Evidence: `engine:internal/orchestrator/budget_recovery_test.go` `TestBudgetOpenDecidesUsageThatHadNoDecision`,
`TestBudgetBaseReconciliationAtOpen`, `TestBudgetRunWithoutBudgetsWritesNothing`; `engine:internal/orchestrator/budget_scope_test.go`
`TestBudgetChangedDeclarationDropsOnlyItsOwnOverrideAfterReopen`, `TestBudgetOpenAttributesChildUsageToEveryAncestorFromTheJournal`;
`afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_InterruptWhileHeldThenReopenKeepsSpendAndLimit`

#### BUD-S25 Restart with edited flow limit
- GIVEN a held stage with a runtime override, and the user raises that stage's limit in `flow.yaml`
- WHEN `afm run` reopens the run
- THEN the new declaration replaces the override and the hold is released if the limit allows work

### BUD-R30 Manual continue cannot bypass a budget hold
A manual Continue of a budget-held stage, or of a stage whose ancestor is held, is refused. A budget resume
releases only the budget hold, never manual pauses, approvals or helper gates. Stale stop decisions do not
pause work resumed after them.

Evidence: `engine:internal/orchestrator/stage_hold_budget_test.go` `TestContinue_ManualContinueOfBudgetHeldStageIsRefused`,
`TestContinue_AncestorHoldStopsManualContinue`; `engine:internal/orchestrator/budget_pause_test.go`
`TestBudgetLostPauseLeavesNoReceiptAndResumeKeepsTheManualPause`, `TestBudgetOldDecisionDoesNotPauseAfterResume`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| BUD-E20 | Invalid budget (unknown key, no metric, bad suffix, `null`) | Flow/config load fails naming the key | `afm:contracts/flow/budget_test.go` `TestBudget_RejectsInvalidDeclarations` |
| BUD-E21 | Money budget over a model without explicit full rates | `afm validate` fails naming scope, command, model, missing rates; engine `Open` fails `budget_rates_missing` | `afm:cmd/afm/validate_test.go` `TestValidateFlowFile_MissingRatesNameScopeCommandModelAndCategories`, `TestValidateFlowFile_MoneyRatesMatchEngineOpen` |
| BUD-E22 | Manual Continue of budget-held stage | Refused, stage stays held | `engine:internal/orchestrator/stage_hold_budget_test.go` `TestContinue_ManualContinueOfBudgetHeldStageIsRefused` |

## Coverage gaps and discrepancies
- **BUD-G20** (ambiguous): docs say scripts are terminated "(Linux)"; behavior on macOS for hard stops of
  scripts is not specified by tests (`script_budget_unix_test.go` only).
- **BUD-G21** (doc-only): "one hold shown per scope" (`View.Hold`) is a documented limitation without a direct test.
- **BUD-G22** (doc-only): downgrade to an engine without budgets on a journal with budget records is unsupported; untested by design.
