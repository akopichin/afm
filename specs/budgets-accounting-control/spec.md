# Budgets and accounting: control, export and run outcome

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `Run.Budgets()` (`List`/`View`/`Update`/`Resume`), revisions and errors; HTTP `/api/budgets*`.
- `Options.BudgetExporter` and afm's `flow.yaml` budget export; `BudgetViewsOffline`.
- `Result.Budget`, `afm run` exit 75, repeat suppression, Budgets section of `afm check`/`afm report`.
- Out of scope: enforcement (`../budgets-accounting-limits/spec.md`), usage accounting
  (`../budgets-accounting/spec.md`), flow parsing (`../flow-config/spec.md`).

## Terms
- **Revision**: configuration counter of a scope; only overrides change it.
- **Override**: runtime `limit` / `unlimited` / `none` (drop runtime change) on top of the declared limit.
- **HumanControl**: run policy meaning a person can act (dashboard or `--mode core`).

## Requirements

### BUD-R40 Views carry the engine's decision
A `View` holds effective and declared limit, spend, level per metric, hold, draining, parent limits and the
valid `actions`; clients display it and never recompute admission. Budget DTOs decode strictly (exact keys,
no `null`, explicit required fields).

Evidence: `afm:contracts/budget/view_test.go` `TestViewJSONRoundTrip`, `TestUpdateRequestStrict`, `TestResumeRequestStrict`;
`afm:contracts/budget/strict_json_test.go` `TestBudgetDTOsRequireExactKeys`, `TestRejectNullField`, `TestUpdateAndResumeRequireExplicitFields`

### BUD-R41 Update is revision-guarded and never starts work
`Update` replaces the override under `ExpectedRevision`; of two concurrent editors one wins, the other gets
`budget_revision_conflict`. A no-op update keeps the revision; holds and thresholds never bump it. Lowering a
limit or changing policy applies at once. `Update` alone never starts work even if the hold no longer applies.

Evidence: `engine:internal/orchestrator/budget_control_test.go` `TestBudgetUpdateStaleRevisionConflicts`,
`TestBudgetUpdateTwoEditorsOneWinner`, `TestBudgetUpdateNoopKeepsRevision`; `engine:internal/orchestrator/budget_policy_test.go`
`TestBudgetLoweredLimitAppliesImmediately`; `engine:internal/engine/budgets_test.go` `TestBudgets_UpdateReplacesOverrideAndBumpsRevision`

#### BUD-S40 Two tabs edit the same scope
- GIVEN two clients holding revision 3 of `stage:build`
- WHEN both call `Update` with `ExpectedRevision: 3`
- THEN one succeeds (revision 4) and the other gets `budget_revision_conflict`

### BUD-R42 Resume checks ancestors and continues once
`Resume` releases only the budget hold after checking every ancestor; a still-limited scope fails with
`budget_still_limited`. Two concurrent resumes continue the work once.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_ResumeWhileStillLimitedIsRefused`;
`engine:internal/orchestrator/budget_policy_test.go` `TestBudgetResumeRefusedWhileAncestorStillHeld`;
`engine:internal/orchestrator/budget_control_test.go` `TestBudgetDoubleResumeContinuesOnce`

### BUD-R43 Budget commands share stage-command admission
After `Shutdown` or while finalizing, budget methods return `KindClosed`. Unknown scopes are `KindNotFound`;
bad requests and missing rates are `KindInvalid`. `Update` may declare a lazy (not yet started) scope.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_ShutdownClosesAdmission`, `TestBudgets_FinalizingRunIsClosed`,
`TestBudgets_UnknownAndInvalidScopes`; `engine:internal/orchestrator/budget_control_test.go` `TestBudgetUpdateDeclaresLazyScope`,
`TestBudgetUpdateRejectsBadRequests`

### BUD-R44 Export order: commit, export, then resume
With `Options.BudgetExporter`, `Update` commits the override, exports outside run locks, then optionally
resumes. A failed export returns `Export.status=failed` with nil error, keeps the override and hold, and skips
resume. Export is `unavailable` for dynamic scopes, `unlimited`/`none` overrides and without an exporter.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_ExportSuccessContinuesHeldRun`,
`TestBudgets_ExportFailureKeepsOverrideAndHold`, `TestBudgets_WithoutExporterSaveIsUnavailableAndEditingWorks`,
`TestBudgets_DynamicScopeIsNotExportable`, `TestBudgets_ExporterSavesDeclaredLimitOnly`

### BUD-R45 afm writes only the budget block of `flow.yaml`
afm's exporter edits only the declaration's `budget` text (inline `budget: {…}` replaced; key-line comment
kept; CRLF, final newline and file mode preserved) and re-validates the result. It refuses symlinks,
unwritable files, a budget changed on disk since read (per declaration), non-unique declarations, anchors /
aliases / merge keys, inline stages or members, comments inside the replaced block, and helpers declared by
profile name only. Errors never contain absolute paths.

Evidence: `afm:cmd/afm/run_budget_export_test.go` `TestBudgetExport_ReplacesInlineValue`, `TestBudgetExport_KeepsCRLFAndMissingFinalNewline`,
`TestBudgetExport_FileModeIsKept`, `TestBudgetExport_FreshnessIsPerDeclaration`, `TestBudgetExport_Refusals`,
`TestBudgetExport_FileSystemRefusals`, `TestBudgetExport_ErrorsNeverCarryAbsolutePaths`, `TestBudgetExport_InvalidFlowFileIsNotSaved`;
`afm:cmd/afm/run_budget_export.go`

#### BUD-S41 Anchored stage
- GIVEN a stage declared with a YAML anchor
- WHEN the user saves a custom limit with save to `flow.yaml`
- THEN export reports failed ("anchors, aliases or merge keys"), the run override stays, the file is unchanged

### BUD-R46 A restarted run re-reads declared limits from the file
After an export the file is the declared base; a later runtime-only change is replaced by the file value on reopen.

Evidence: `afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_RuntimeChangeAfterExportIsReplacedByTheFileOnReopen`;
`afm:cmd/afm/run_budget_export_test.go` `TestBudgetExport_RememberedExportIsTheNewBase`

### BUD-R47 HTTP routes mirror the engine API
`GET /api/budgets`, `GET /api/budgets/view?scope=`, `POST /api/budgets/update`, `POST /api/budgets/resume` are
served only with `capabilities.budgets` (else 404). GETs never mutate. POSTs require same origin, ≤16 KiB, no
unknown fields. Engine error codes are kept; a saved update with a refused resume is `200` with `resume_error`;
a cancelled request does not cancel an accepted update; unexpected errors do not leak details.

Evidence: `afm:pkg/server/budgets_test.go` `TestBudgets_CapabilityOffIs404AndFlagsFalse`, `TestBudgets_GetNeverMutates`,
`TestBudgets_PostRejectsForeignOrigin`, `TestBudgets_UpdateRejectsOversizedBody`, `TestBudgets_EngineErrorsKeepTheirCode`,
`TestBudgets_UpdateWithRefusedResumeKeepsSavedResult`, `TestBudgets_CancelledRequestDoesNotCancelUpdate`,
`TestBudgets_UnexpectedErrorDoesNotLeakDetails`

### BUD-R48 Offline views equal live views
`engine.BudgetViewsOffline(runDir)` returns views of declared scopes without the writer lock, starts nothing,
never offers `save_to_yaml`, and matches live `View` results for the same journal.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_LiveViewsEqualOfflineViews`

### BUD-R49 `Result.Budget` reports held and exhausted scopes
`Result.Budget` is nil when budgets changed nothing; otherwise `Held` (work still blocked; run is
`interrupted`/`budget_exceeded`, resumable) and `Exhausted` (scopes whose hard/medium limit was reached, even on
a completed run). Without HumanControl a held run ends instead of waiting; with it the run stays open. A budget
never cancels the run context or closes the Store.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_ResultReportsHeldRunWithoutHumanControl`,
`TestBudgets_ResultReportsExhaustedScopeOfCompletedRun`; `engine:internal/orchestrator/run_exit_budget_test.go`
`TestRun_BudgetBlockedRunWithoutHumanControlEndsInterrupted`, `TestRun_BudgetHeldRunWithHumanControlStaysOpen`

### BUD-R50 `afm run` exits 75 when held without a controller
With `--port 0` (no API) a run held by a hard/medium limit exits with code 75, lists held scopes and tells
the user to raise the limit; rerunning resumes the same run. With dashboard or `--mode core` the API stays up
and update + resume continue the same run.

Evidence: `afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_HeldRunWithoutAPIExits75`,
`TestBudgetIntegration_HoldKeepsAPIUpAndUpdateResumesTheSameRun`; `afm:cmd/afm/exit.go` `budgetHeldExitCode`

#### BUD-S42 Headless held run
- GIVEN `afm run --port 0` with a stage limit of 100 tokens that the agent overshoots
- WHEN the run is held
- THEN the process prints "held by a budget limit … held: limit reached" and exits 75

### BUD-R51 A budget outcome stops `repeat`
A cycle whose result carries a budget outcome (hard/medium) does not start another `repeat` cycle and prints
`afm: repeat stopped: budget limit reached`; an incomplete cycle keeps its failure status.

Evidence: `afm:cmd/afm/run_budget_test.go` `TestCycleLoop_BudgetOutcomeEndsRepeat`, `TestCycleLoop_IncompleteBudgetCycleKeepsFailure`;
`afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_RepeatStopsAfterTheLimitIsReached`; `afm:cmd/afm/run_repeat.go`

### BUD-R52 CLI Budgets section is journal-based
`afm check` and `afm report` print a Budgets section (scope, spent/limit, policy, state) read offline from the
journal, independent of accounting display; money columns appear only for money limits; no budgets means no
section; a read failure is one note (report: `unavailable_reason`, exit 0).

Evidence: `afm:cmd/afm/budget_display_test.go` `TestBudgetDisplay_RowStates`, `TestBudgetDisplay_MoneyColumnOnlyForMoneyLimit`,
`TestBudgetDisplay_NoViewsOmitsTheSection`; `afm:cmd/afm/check_test.go` `TestCheck_BudgetReadFailureIsOneNote`;
`afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_CheckAndReportShowTheHeldRun`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| BUD-E40 | Stale `expected_revision` | `budget_revision_conflict` / HTTP 409 | `engine:internal/orchestrator/budget_control_test.go` `TestBudgetUpdateStaleRevisionConflicts` |
| BUD-E41 | Resume of a still-limited scope | `budget_still_limited` / HTTP 409 | `engine:internal/engine/budgets_test.go` `TestBudgets_ResumeWhileStillLimitedIsRefused` |
| BUD-E42 | Unknown scope | `budget_unknown_scope` / HTTP 404 | `engine:internal/engine/budgets_test.go` `TestBudgets_UnknownAndInvalidScopes` |
| BUD-E43 | Money override without explicit rates | `budget_rates_missing` / HTTP 400 | `engine:internal/orchestrator/budget_control_test.go` `TestBudgetUpdateMoneyNeedsRates` |
| BUD-E44 | Run shutting down | `KindClosed` / HTTP 503 `budget_closed` | `engine:internal/engine/budgets_test.go` `TestBudgets_ShutdownClosesAdmission` |
| BUD-E45 | Foreign Origin / body > 16 KiB | HTTP 403 `forbidden_origin` / 413 `request_too_large` | `afm:pkg/server/budgets_test.go` `TestBudgets_PostRejectsForeignOrigin`, `TestBudgets_UpdateRejectsOversizedBody` |
| BUD-E46 | Held run without API | `afm run` exit 75 | `afm:cmd/afm/budget_integration_test.go` `TestBudgetIntegration_HeldRunWithoutAPIExits75` |

## Coverage gaps and discrepancies
- **BUD-G40** (doc-only): Ctrl+C on a held run with dashboard/core exits 130 (afm `docs/budgets.md`); no budget-specific test seen.
- **BUD-G41** (doc-only): `afm agent spawn` helpers get only the profile default budget (`SpawnHelperRequest` has no budget field); untested.
- **BUD-G42** (untested here): dashboard budget window behavior (Save and continue, conflict handling, draft retention) lives in frontend tests not reviewed.
