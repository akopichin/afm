# Run lifecycle: run directories, resume and offline control

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Which run directory an `Open` resumes or creates, run ids, and the read-only `SavedRun` preview.
- Run-level restart semantics (what a resumed process keeps), the writer lock seen by CLI commands, and the
  offline operations `ApproveOffline`, `RetryOffline`, `ReviseOffline`, `BudgetViewsOffline`.
- Part of the run-lifecycle family: engine ownership — [run-lifecycle](../run-lifecycle/spec.md); CLI —
  [run-lifecycle-cli](../run-lifecycle-cli/spec.md). Per-status resume rules belong to
  [stage-lifecycle](../stage-lifecycle/spec.md); journal locking/replay to [event-journal](../event-journal/spec.md).

## Terms
- **Run name** — `--worktree` name when given, else the flow name; prefix of run directory names.
- **Owes work** — a finished-looking run that still owes budget-held work or an after-hook
  (`orchestrator.RunOwesWork`).
- **SavedRun** — read-only view (journal state + frozen stage registry) of the run a start would resume.

## Requirements

### RUN-R20 Run resolution and ids
Unless `ForceNew` is set, `Open` resumes the latest directory under `RunsDir` whose name is
`<runName>-<YYYYMMDD-HHMMSS>[-<hex>]` (a sibling name such as `<runName>-2-…` never matches) when it has a stage
not `done` or owes work, and prints `afm: resuming run <id>`. Otherwise it creates
`<runName>-<timestamp>-<4 hex>`; two runs in the same second get distinct ids. Run directories are absolute.

Evidence: `engine:internal/engine/open_test.go` `TestResolveRun_WorktreeNameIsolatesRuns`,
`TestResolveRun_ForceNewSkipsResume`; `engine:internal/engine/runid_test.go` `TestNewRunID_UniqueWithinSameSecond`;
`engine:internal/state/findrun_test.go` `TestFindLatestRunDir_AnchorsPrefix_RejectsNumericSuffixSibling`;
`engine:internal/engine/budget_public_restart_integration_test.go` `TestBudgetPublic_FinishedRunIsNotResumed`,
`TestBudgetPublic_OwedAfterHookKeepsTheRun`; `engine:internal/engine/open.go` `resolveRun`, `newRunID`;
`afm:docs/internals/event-log.md`.

#### RUN-S20 Finished run is not resumed
- GIVEN the latest run of the flow finished with all stages `done` and owes nothing
- WHEN `Open` runs again
- THEN a new run directory is created; the old one is untouched.

#### RUN-S21 ForceNew
- GIVEN a resumable run awaiting approval
- WHEN `Open` runs with `ForceNew`
- THEN a new directory is created and the old journal is byte-identical.

### RUN-R21 An unreadable journal is never silently replaced
If the latest run's journal contains a record this build cannot read (`ErrUnsupportedRecord`), `Open` and the
Docker preview (`SavedRunFor`) fail with that error; no new run directory is created and the journal is left
unchanged. Any other open failure of the latest run (including `ErrRunLocked`) prints a warning and starts a new
run.

Evidence: `engine:internal/engine/budget_public_restart_integration_test.go`
`TestBudgetPublic_UnsupportedJournalIsNotReplacedByANewRun`; `engine:internal/engine/open.go` `resolveRun`.

### RUN-R22 SavedRun preview matches Open
`SavedRunFor` returns nil for a clear mode (`--worktree-clear[-force]`) and for no/unfinished-free runs; it reads
the run `Open` would resume without the writer lock, from the journal and the frozen stage registry, so host
and container derive identical Docker preparation.

Evidence: `engine:internal/engine/budget_public_restart_integration_test.go`
`TestBudgetPublic_DockerPreviewKeepsARunThatOwesWork`; `engine:internal/engine/docker_projection_test.go`
`TestDockerProjection_SavedTaskSameOnHostAndInContainer`, `TestResumeTaskCommands`;
`engine:internal/engine/docker_projection.go` `SavedRunFor`, `loadSavedRun`.

### RUN-R23 A resumed run keeps its own graph
A resumed run restores its frozen stage registry and folds in stages added during earlier processes from the
journal; an edited `flow.yaml` does not replace the resumed graph, and `flow.yaml` is never rewritten. Only
declared budgets are re-read from the current flow. `Result.Resumed` and the lifecycle event
(`flow_resumed` vs `flow_started`/`flow_repeated`) report a resume.

Evidence: `engine:internal/engine/restart_integration_test.go` `TestRestart_DynamicStageSurvivesResume`;
`engine:internal/engine/budget_public_restart_integration_test.go` `TestBudgetPublic_FirstDeclaredLimitAddedOnRestart`;
`engine:internal/engine/open.go` `Open` (`settleSkillRun`, `graph.Materialize`, `withDeclaredBudgets`).

### RUN-R24 Run-level restart semantics
A restart preserves `awaiting_approval` until approved, then resumes. Failed stages are reset to `pending`
before anything else when `auto_recover` is on (default), honouring `depends_on`; with `auto_recover: false` a
failed stage waits for Retry. Interrupted active stages are restarted (details in stage-lifecycle).

Evidence: `engine:internal/engine/restart_integration_test.go` `TestRestart_AwaitingApprovalWaitsThenResumes`,
`TestRestart_FailedStageWaitsForRetry`; `engine:internal/orchestrator/recovery_auto_test.go`
`TestAutoRecover_ResumesFailedStageWithoutManualRetry`, `TestAutoRecover_Disabled_LeavesFailedStageUntouched`,
`TestAutoRecover_RespectsDependsOnOrder`; `afm:docs/stage-lifecycle.md` "Resume on restart".

#### RUN-S22 Restart with a failed stage, auto_recover off
- GIVEN a stopped run with a `failed` stage and `auto_recover: false`
- WHEN the run is reopened and started
- THEN the stage stays `failed` until a Retry, then completes.

### RUN-R25 Offline control goes through the journal only
`ApproveOffline` (`awaiting_approval → ready`, event `cli_approve`), `RetryOffline` (`failed → pending`,
`cli_retry`) and `ReviseOffline` (versions the plan, saves feedback, `awaiting_approval → revising`,
`cli_revise`, reason = feedback) open the latest run containing the stage with the writer lock and apply one
CAS transition. They never start an orchestrator or agent. A wrong status returns `*StatusError` (with an
actionable explanation for `awaiting_assistants`) and changes nothing.

Evidence: `engine:internal/engine/offline_test.go` `TestOfflineTransitions`, `TestOfflineWrongStatus`,
`TestOfflineUnknownStage`; `afm:cmd/afm/offline_control_test.go` `TestOfflineApprove`, `TestOfflineRetry`,
`TestOfflineRevise`, `TestOfflineRevise_SecondRoundIncrementsVersions`, `TestOfflineRevise_MissingPlanChangesNothing`,
`TestOfflineAwaitingAssistantsExplanation`; `engine:internal/engine/offline.go`.

### RUN-R26 An active run rejects offline mutation
While another process holds the run, every offline mutation returns `ErrRunActive` (which also matches
`state.ErrRunLocked`) before writing anything: journal bytes and `feedback.md` are untouched. The afm CLI shows
`run is active`.

Evidence: `engine:internal/engine/offline_test.go` `TestOfflineRejectsActiveRun`;
`afm:cmd/afm/offline_control_test.go` `TestOfflineActiveRunIsRejectedWithoutTouchingJournal`;
`afm:cmd/afm/errrunlocked_test.go` `TestApproveErrRunLockedMessage`, `TestRetryErrRunLockedMessage`,
`TestReviseErrRunLockedMessage`; `engine:internal/engine/errors.go` `ErrRunActive`.

#### RUN-S23 Approve while run is live
- GIVEN a run whose Store is open in another process
- WHEN `afm approve <stage>` runs
- THEN it fails with a message containing `run is active` and the journal is unchanged.

### RUN-R27 Offline budget views equal live views
`BudgetViewsOffline(runDir)` folds the budget journal and `usage.jsonl` read-only and yields the same views as the
live `Budgets().List`.

Evidence: `engine:internal/engine/budgets_test.go` `TestBudgets_LiveViewsEqualOfflineViews`;
`engine:internal/engine/budgets.go` `BudgetViewsOffline`.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| RUN-E20 | Latest journal has unknown kind/schema | `Open`/`SavedRunFor` fail `ErrUnsupportedRecord`, no new run | `TestBudgetPublic_UnsupportedJournalIsNotReplacedByANewRun` |
| RUN-E21 | Offline op on a live run | `ErrRunActive` (`errors.Is` `state.ErrRunLocked`) | `TestOfflineRejectsActiveRun` |
| RUN-E22 | Offline op, wrong stage status | `*StatusError`, nothing written | `TestOfflineWrongStatus` |
| RUN-E23 | Stage in no run | error `no active run found for stage` | `TestOfflineUnknownStage` |
| RUN-E24 | Latest run with corrupt journal | offline lookup fails with `ErrCorruptLog`, not an older run | `engine:internal/state/state_loadrunstate_test.go` `TestFindLatestRunForStage_CorruptLatestRunSurfaced` |

## Coverage gaps and discrepancies
- **RUN-G20** (untested): when the latest run is locked by a live process, `resolveRun` warns and creates a new
  run directory (`engine:internal/engine/open.go` `resolveRun` default branch); no test covers it.
- **RUN-G21** (ambiguous): offline lookup `FindLatestRunForStage` scans all run directories of all flows,
  newest by lexicographic name, and picks the first containing the stage id — not scoped to a flow name.
- **RUN-G22** (code≠doc): `afm:docs/internals/event-log.md` says the CLI fails with "run is locked"; the
  message is `run is active: run directory is locked by another afm process`.
