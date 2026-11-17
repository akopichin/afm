# Agent memory: live run path

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Memory work during `afm run`: per-stage background capture, one end-of-run finalize, best-effort failure handling, budget admission, memory agent processes, optional commit.
- The agent-memory domain is split into: configuration and injection (`../agent-memory/spec.md`), live run path (`../agent-memory-live/spec.md`), shared pipeline (`../agent-memory-pipeline/spec.md`) and offline rebuild (`../agent-memory-rebuild/spec.md`). Prefix `MEM` is shared; IDs are unique across them.
- Out of scope: general flow parsing (`../flow-config/spec.md`); budget rules (`../budgets-accounting/spec.md`).

## Terms
- **memory dir** — resolved directory of `memory.path`; holds `memory.md` (project file) and
  stage `reflect.file` targets.
- **dataset** — `<runDir>/<stageID>/reflect_dataset.yaml`, YAML output of the reflect agent.
- **capture** — reflect step producing a dataset. **finalize** — aggregate → prioritize →
  select High → update → publish.
- **target** — a rules file being finalized: a `reflect.file` group or `memory.md`.
- **memory lock** — interprocess lock keyed by canonical memory dir.

## Requirements

### MEM-R07 Live capture after a stage
After a stage finishes (after its after-hook), the engine captures its session in the background
only if memory is enabled, the stage has writable `reflect`, and it is not a script stage. Capture
writes only the dataset; it never builds rules files, never changes the FSM, and does not hold the
memory lock. Run exit waits for pending captures.

Evidence: engine:internal/orchestrator/reflection_test.go `TestMaybeRunReflection_NoOpWhenDisabled`,
`TestMaybeRunReflection_CapturesDatasetOnly`, `TestMaybeRunReflection_ModeR_DoesNotWrite`,
`TestMaybeRunReflection_ScriptSkipped`; engine:internal/orchestrator/hooks_test.go
`TestMaybeRunAfterHookThen_ReflectionSeesAfterHookLog`; engine:internal/orchestrator/reflection.go
`maybeRunReflection`; engine:internal/orchestrator/scheduling.go `shouldExit`

#### MEM-S05 Read-only reflect does not capture
- GIVEN memory enabled and a stage with `reflect: {file: a.md, mode: r}`
- WHEN the stage finishes
- THEN no reflect agent runs and no dataset is written

### MEM-R10 End-of-run finalize
When the run finalizes (not budget-blocked), the engine runs finalize once over all eligible
datasets (write-reflect non-script stages whose dataset file exists), under the memory lock, in a
fresh attempt dir `<runDir>/.memory-finalize/<id>`. Each `reflect.file` target is updated from
its stages' datasets; `memory.md` is updated from all non-empty eligible datasets only when
`memory.mode` allows write. Without datasets nothing runs. If `memory.commit` is true, the memory
dir is committed afterwards.

Evidence: engine:internal/orchestrator/reflection_test.go `TestEndOfRunMemory_BuildsPerStageFiles`,
`TestEndOfRunMemory_WritesProjectFile`, `TestEndOfRunMemory_ModeReadOnlyDoesNotWriteProject`,
`TestEndOfRunMemory_NoOpWithoutDatasets`, `TestEndOfRunMemory_GhostDatasetExcludedFromProjectAggregate`,
`TestEndOfRunMemory_CommitsWhenEnabled`; engine:internal/memorypipeline/rebuild_test.go
`TestFinalize_ProjectAggregateOnlyEligibleDatasets`; engine:internal/orchestrator/reflection.go
`runEndOfRunMemory`; engine:internal/orchestrator/orchestrator.go

#### MEM-S06 Read-only project memory
- GIVEN `memory.mode: r` and a stage with `reflect: {file: build.md, mode: rw}` that produced a dataset
- WHEN the run ends
- THEN `build.md` is updated and `memory.md` is left untouched

### MEM-R11 Live memory is best-effort
Live capture, lock, finalize and commit failures publish a `reflect_failed` notice (live and
durable notice path) and never change stage status or fail the run.

Evidence: engine:internal/orchestrator/reflection.go `reflectFailed`, `reflectNotice`;
engine:internal/memorypipeline/AGENTS.md; afm:docs/agent-memory.md (see MEM-G01)

### MEM-R12 Budget admission of memory work
Each memory AI call (reflect, aggregate, prioritize, update) is admitted by the budget right
before it runs. A refused capture is skipped silently; a refused finalize call ends memory work
with `ErrBudgetHeld`. A budget-blocked run gets no end-of-run memory.

Evidence: engine:internal/orchestrator/budget_invocation_test.go
`TestBudgetInvocation_MemoryCallsAreAdmittedOneByOne`; engine:internal/orchestrator/budget_edges_test.go
`TestBudgetAttempts_HardStopCancelsTheRunningMemoryCall`; engine:internal/orchestrator/reflection.go
`budgetedMemoryRunner`

### MEM-R17 Memory agents run isolated
Memory agents run as fresh-context client processes (configured command, extra args, reasoning)
without `AFM_STAGE_DIR`; their usage is attributed as memory jobs.

Evidence: engine:internal/memorypipeline/executor_test.go `TestNewExecRunner_InvokesCommandAndDeliversPrompt`,
`TestNewExecRunner_UsageAttribution`, `TestNewExecRunner_MeasuresEachCallAsAJob`;
engine:internal/memorypipeline/reasoning_test.go `TestNewExecRunner_ForwardsReasoning`;
engine:internal/orchestrator/agent_jobs_test.go `TestAgentJobs_MemoryAttribution`;
engine:internal/memorypipeline/executor.go

### MEM-R18 Git commit of memory changes
`memory.Commit` stages and commits only changes under the memory dir (`git add .` + pathspec `.`),
returns `false` without committing when nothing changed, and never sweeps unrelated staged files.
It never pushes.

Evidence: afm:contracts/memory/commit_test.go `TestCommit_AddsAndCommitsOnlyOnChange`,
`TestCommit_DoesNotSweepUnrelatedStagedFiles`; afm:contracts/memory/commit.go `Commit`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MEM-E08 | budget refuses a memory call | memory work skipped / `ErrBudgetHeld`, run unaffected | engine:internal/orchestrator/budget_invocation_test.go |

## Coverage gaps and discrepancies
- **MEM-G01** (untested): no orchestrator test asserts that a live capture/finalize/commit failure
  emits `reflect_failed` and leaves FSM and run outcome unchanged; only code
  (`reflectFailed`) and docs.
- **MEM-G02** (code≠doc): docs say finalize runs on "normal run completion"; code runs
  `runEndOfRunMemory` on any non-budget-blocked finalize, including runs ending in failure
  (`orchestrator.go`, before the `AllDone` check).
- **MEM-G03** (ambiguous): live commit uses `memory.Commit` (whole memory dir, no staged-change
  preflight) while rebuild commits only published targets via `CommitPaths` with a preflight;
  docs describe both as "scoped to the memory directory".
- **MEM-G04** (untested): a refused live capture (`maybeRunReflection` budget check) and skipping
  end-of-run memory for budget-blocked runs have no direct test.
