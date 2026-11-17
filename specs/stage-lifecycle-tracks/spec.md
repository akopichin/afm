# Stage lifecycle: tracks, scripts, context, dynamic stages

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Which execution track a stage takes: planned, pre-planned, autonomous (`agents: [auto]`), script.
- `script_before` / `script_after` hooks and `hook_failed`; interactive normalization without a human surface.
- What the engine guarantees about dependency context and declared artifacts/inputs.
- Stages added at runtime: add-stage preview/commit, runtime registry, replay survival.
- Family (prefix `STG`, disjoint ranges): [stage-lifecycle](../stage-lifecycle/spec.md) `R01–R10`,
  [stage-lifecycle-recovery](../stage-lifecycle-recovery/spec.md) `R11–R19`, this file `R20+`.
- Out of scope: observer lifecycle hooks ([lifecycle-hooks](../lifecycle-hooks/spec.md)), flow parsing and
  input/artifact validation ([flow-config](../flow-config/spec.md)), the frozen registry on resume
  ([run-lifecycle-resume](../run-lifecycle-resume/spec.md) RUN-R23), questions ([dialog-approval-questions](../dialog-approval-questions/spec.md)).

## Terms
- **pre-planned** — a stage that needs no planning agent (`plan:` source, interactive without planning, auto, script).
- **registry** — runtime stage graph (`graph.Registry`): frozen flow stages plus committed dynamic stages.
- **sinks** — registry stages with no dependent.

## Requirements

### STG-R20 Track selection
A stage with `agents: [auto]` (or a legacy `autonomous.flag` in its stage dir) runs the autonomous track: no
`plan.md`, no approval, straight `pending → ready → running` with one agent. Script stages also go straight to
`ready`. Other stages without planning are pre-planned (STG-R22); the rest plan first.

Evidence: engine:internal/orchestrator/scheduling.go `executionKind`, `activateAutoStage`, `activateScriptStage`;
engine:internal/orchestrator/pause_continue_test.go `TestContinue_FromRetrying_AutonomousStage`;
engine:internal/orchestrator/integration_resume_test.go `TestResume_RevisingAutonomousStageUsesAutonomousFeedback`

### STG-R21 Autonomous completion requires a readable summary
An autonomous stage completes only when `execution_summary.md` in the stage dir has non-whitespace content. A
missing or empty file gets up to two post-exit author continuations (spent durably, surviving restart) plus at
most one live Stop-hook reminder; after that the stage fails naming the path. An unreadable file fails with its
file error. Verify and required helpers still gate completion afterwards.

Evidence: engine:internal/orchestrator/summary_recovery_test.go
`TestSummaryRecovery_TwoPostExitCorrectionsThenTheAuthorWritesIt`, `TestSummaryRecovery_ThirdMissingFailsWithPathAndReason`,
`TestSummaryRecovery_UnreadableSummaryFailsWithTheFileError`, `TestSummaryRecovery_RestartAfterIssuedLastCorrectionIsExhausted`,
`TestSummaryRecovery_ManualRetryStartsANewCycle`; engine:internal/orchestrator/recovery.go `executionProbe`

#### STG-S20 Summary written on the second continuation
- GIVEN an autonomous agent that exits twice without the summary and writes it on the second continuation
- WHEN the second continuation exits
- THEN the stage proceeds to its usual checks and completes; no third continuation is issued.

### STG-R22 Pre-planned activation
When its deps are done, a pre-planned stage gets its stage dir and `plan.md` — copied from `plan:` or, for an
interactive stage without planning, its `description` — and moves to `ready`, skipping the planning agent.
A failed mkdir/copy/write fails the stage.

Evidence: engine:internal/orchestrator/integration_test.go `TestIntegration_PreExistingPlan`;
engine:internal/orchestrator/scheduling_test.go `TestInteractiveStageUsesDescriptionAfterDependency`;
engine:internal/orchestrator/plan_source_test.go `TestResolvePlanSource`;
engine:internal/orchestrator/scheduling.go `activatePrePlannedStage`

### STG-R23 Script stages
A `script:` stage has no agent: it runs once deps are done, ends `done` on exit 0 and `failed` otherwise. Failure
publishes a durable `script_failed` notice with a stderr tail (≤20 lines / 4 KiB); full stderr stays in
`<stage>/<phase>.stderr.log`. Revise, Button and pre-note are rejected; manual Retry reruns it via `ready`; a
script left `running` by a crash is resumed on restart.

Evidence: engine:internal/orchestrator/integration_script_test.go `TestIntegration_ScriptStage_HappyPath`,
`TestIntegration_RetryFailedScriptStage`, `TestRunScriptStage_Failure_PublishesScriptFailedNotice`;
engine:internal/orchestrator/recovery_script_running_test.go `TestRecovery_ResumesScriptStageStuckRunning`;
engine:internal/engine/commands_integration_test.go `TestCommands_RejectedInTheWrongStatus`

### STG-R24 `script_before` / `script_after` hooks
Hooks run before/after any stage's own content and are retried with 1s/2s/3s waits (up to 4 runs). A
`script_before` still failing moves the stage `running → hook_failed`; `RetryHook` reruns the cycle, `SkipHook`
proceeds to the content. A failing `script_after` raises the same Retry/Skip notice but the stage stays `done`; an
owed after-hook resumes after restart. Pause during the before-hook prevents the main launch.

Evidence: engine:internal/orchestrator/hooks_test.go `TestRunScriptWithRetry_ExhaustsAfterFourAttempts`,
`TestRunScriptWithRetry_RespectsBackoffTiming`, `TestRunBeforeHook_BlocksThenRetrySucceeds`, `TestRunBeforeHook_BlocksThenSkip`,
`TestRunAfterHook_FailsThenSkip_StageStaysDone`; engine:internal/orchestrator/bus/fsm_test.go `TestFSM_HookFailedTransitions`;
engine:internal/orchestrator/hooks.go `runScriptWithRetry`

#### STG-S21 Skip a failing before-hook
- GIVEN a stage whose `script_before` fails all attempts
- WHEN the user sends SkipHook
- THEN the stage returns to `running` and its agent starts without the hook.

### STG-R25 Interactive normalization without a human surface
Before the orchestrator is built, if the run has no `HumanControl`, every `interactive: true` stage is run as
non-interactive with a stderr warning; the parsed flow slice is copied, not mutated. afm sets `HumanControl`
exactly when the configured server port is > 0.

Evidence: engine:internal/engine/normalize_test.go `TestNormalizeInteractiveStagesDoesNotMutateFlow`;
engine:internal/engine/environment.go `normalizeInteractiveStages`; afm:cmd/afm/run.go `humanControl`

### STG-R26 Dependency context in prompts
Planning and execution prompts include, for each direct dependency, its `plan.md`, or `execution_summary.md` for
an autonomous dependency.

Evidence: engine:internal/orchestrator/plan_source_test.go `TestCollectDependencyPlans_StandardPlan`,
`TestCollectDependencyPlans_AutonomousFallback`; engine:internal/orchestrator/integration_test.go
`TestIntegration_PlanningPromptIncludesDependencyPlan`

### STG-R27 Artifacts and inputs
For each `inputs` ref the producer's artifact is inlined (`inline: true`, default) or passed as an absolute path.
`./path` is relative to the producer's stage dir; other relative paths to the agents' project root. A missing
optional input is skipped. A missing required input makes collection fail: the prompt gets no Artifacts section,
a warning is logged and the stage still runs.

Evidence: engine:internal/orchestrator/orchestrator_test.go `TestCollectArtifacts_Inline`,
`TestCollectArtifacts_NonInline`, `TestCollectArtifacts_MissingRequired`, `TestCollectArtifacts_MissingOptional`;
engine:internal/orchestrator/stagefiles/context.go `CollectArtifacts`; engine:internal/orchestrator/agents.go
`collectArtifactsNoticing`; afm:docs/artifacts.md

### STG-R28 Adding a stage at runtime
`PreviewAddStage` validates without mutation. `CommitAddStage` re-validates under the Store lock and writes one
durable record (definition, extra deps, order). A draft is always `agents: [auto]` with a safe unique id and a
command prepared at run start. `insert_before` requires a never-started pending target; `append` depends on all
current sinks (or an explicit set). A repeated `RequestID` returns the original result. The new stage starts
only through the normal activation paths.

Evidence: engine:internal/orchestrator/graph_mutate_test.go `TestPreview_NoMutation`,
`TestCommit_SingleOperationForDefinitionAndDependencies`, `TestCommit_DuplicateOperationIgnored`,
`TestCommit_SharedAdmissionSerialization`, `TestAppend_RejectedAfterRunFinalizationStarted`;
engine:internal/orchestrator/graph/dynamic_test.go `TestInsert_RejectedWhenTargetStartedBetweenPreviewAndApply`,
`TestAppend_DependsOnAllSinks`, `TestDynamic_CommandMustBePrepared`;
engine:internal/orchestrator/graph_mutate_integration_test.go `TestCommit_NewStageStartsViaExistingPaths`,
`TestAppend_RaceWithLastStageCompletion`

### STG-R29 Runtime graph is the registry and survives replay
Scheduling, recovery, verify context, lifecycle payloads and `StageControl` read stages from the registry, not
the parsed `flow.Stages`. On resume the registry is rebuilt from `graph_registry.json`, the journal's dynamic
additions in commit order and `StageOrder`; a commit cut off before scheduling still runs once. The user's
`flow.yaml` is never rewritten.

Evidence: engine:internal/engine/restart_integration_test.go `TestRestart_DynamicStageSurvivesResume`;
afm:cmd/afm/run_dynamic_stage_test.go `TestRestart_OriginalFlowYamlUnchanged`;
engine:internal/orchestrator/registry_migration_test.go `TestRecovery_UsesRegistryNotOptsStages`;
engine:internal/orchestrator/graph_mutate_integration_test.go `TestCrashRightAfterCommitBeforeScheduler`;
engine:internal/orchestrator/graph/dynamic.go `Materialize`; engine:internal/engine/stage_control.go `StageControlFor`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| STG-E20 | Draft id unsafe/duplicate, unknown dep, unprepared command, missing target | rejection codes `invalid_stage_id`, `duplicate_stage_id`, `unknown_dependency`, `command_not_prepared`, `target_not_found` | engine:internal/orchestrator/graph/dynamic_test.go `TestValidateDraftShape_RejectsUnsafeOrDuplicateID` |
| STG-E21 | Commit after run finalization began | rejected `run_finalizing`; stage not added | `TestAppend_RejectedAfterRunFinalizationStarted` |
| STG-E22 | Add-stage disabled | `feature_disabled` for preview and commit | engine:internal/orchestrator/graph_mutate_test.go `TestAddStageDisabled_RejectsPreviewAndCommit` |
| STG-E23 | Commit without `RequestID` / budget hold | `ErrRequestIDRequired` / `ErrBudgetHeld` | engine:internal/orchestrator/graph_mutate.go `CommitAddStage` |
| STG-E24 | RetryHook/SkipHook with no hook awaiting | `KindInvalid` "has no hook awaiting a decision" | engine:internal/orchestrator/control_api.go `RetryHook` |
| STG-E25 | Plan copy / stage dir creation fails | stage `failed` ("copy plan failed", "mkdir failed") | engine:internal/orchestrator/scheduling.go `activatePrePlannedStage` |

## Coverage gaps and discrepancies
- **STG-G20** (code≠doc): `script-stages.md` says hooks make "3 attempts with 1s/2s/3s backoff"; code runs the
  hook up to 4 times (initial + 3 retries) — `TestRunScriptWithRetry_ExhaustsAfterFourAttempts`.
- **STG-G21** (code≠doc): `artifacts.md` says a missing required artifact "fails the consuming stage"; code only
  logs and drops the whole Artifacts section, also hiding inputs that exist. No test covers the stage outcome.
- **STG-G22** (untested): E23 and E24 have no dedicated tests.
- **STG-G23** (doc-only): the absolute-path rule for `inline: false` relative to a worktree/`root_dir` is covered
  only by docs and `resolveArtifactPath`, not by a project-root test.
