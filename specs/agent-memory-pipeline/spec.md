# Agent memory: shared pipeline

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The engine memory pipeline shared by live and rebuild paths (`internal/memorypipeline`): capture sources, dataset format, distill chain, rules format, staged publication, memory lock.
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

### MEM-R08 Capture sources and dataset reuse
Capture reads the stage dir's agent-session files plus supplemental user-input files (dialog,
feedback, artifacts), excluding memory artifacts and symlinks. A stage without agent-session
sources fails capture. A valid existing canonical dataset is reused unless forced. Reflect output
must be a fresh regular file of at most 10 MiB that passes dataset validation.

Evidence: engine:internal/memorypipeline/sources_test.go `TestSourceInventory_ClassifiesAgentSessionFiles`,
`TestSourceInventory_ClassifiesSupplementalFiles`, `TestSourceInventory_ExcludesMemoryArtifactsAndDir`,
`TestSourceInventory_SkipsSymlinks`; engine:internal/memorypipeline/rebuild_test.go
`TestCaptureStage_ReuseValidCanonical`, `TestCaptureStage_ScriptOnlyNoAgentSessionStepError`;
engine:internal/memorypipeline/rebuild.go `CaptureStage`

### MEM-R09 Dataset format
A dataset is exactly one YAML mapping with both `project_level` and `session_level` keys and no
other top-level keys; each item has non-empty `prompt`/`chosen`/`rejected` and `source` of `user`
or `log`. Both sections may be empty.

Evidence: engine:internal/memorypipeline/dataset_test.go `TestValidateDataset_Valid`,
`TestValidateDataset_BothSectionsEmptyIsValid`, `TestValidateDataset_MissingSessionLevelRejected`,
`TestValidateDataset_UnknownTopKeyRejected`, `TestValidateDataset_BadSourceRejected`,
`TestValidateDataset_TrailingDocumentRejected`, `TestValidateDataset_OversizeRejected`;
engine:internal/memorypipeline/dataset.go `ParseDataset`

### MEM-R13 Distill chain and no-High preservation
For each target, finalize seeds a candidate from the current target file, then runs aggregate →
prioritize → code-only selection of the `## High` section → update. All-empty datasets or no High
section leave the candidate equal to the seed (prior rules preserved). Stages sharing one
`reflect.file` (equivalent spellings collapse) are processed in stage order, each seeded by the
previous candidate.

Evidence: engine:internal/memorypipeline/distill_test.go `TestDistillTarget_SeedsBeforeAggregate_NoHighKeepsCandidate`,
`TestDistillTarget_EmptyDatasetsNoOp`, `TestDistillTarget_ChainOrderAndStaging`;
engine:internal/memorypipeline/rebuild_test.go `TestFinalize_SharedReflectFileChainsThroughCandidate`,
`TestFinalize_NoHighFirstStagePreservesSeedInSharedGroup`, `TestBuildStageGroups_EquivalentSpellingsCollapseIntoOneGroup`;
engine:internal/orchestrator/reflection_test.go `TestEndOfRunMemory_EmptyDatasetKeepsPriorRules`;
afm:contracts/memory/selecthigh_test.go `TestSelectHigh`, `TestSelectHigh_NoHigh`

#### MEM-S07 Empty dataset keeps rules
- GIVEN an existing `build.md` and a captured dataset with both sections empty
- WHEN finalize runs
- THEN no aggregate agent runs and `build.md` keeps its content

### MEM-R14 Rules file format
An updated candidate must be valid UTF-8, at most 1 MiB, start with `# Project rules`, contain at
least one `## <Pattern>` block, no other top-level heading, no tier heading (`## High` etc.), and
no more than `max_rules` pattern blocks. Priority is encoded only by order.

Evidence: engine:internal/memorypipeline/distill_test.go `TestValidateRules`,
`TestValidateRules_InvalidUTF8Rejected`, `TestValidateRules_OversizeRejected`;
engine:internal/memorypipeline/rules.go `ValidateRules`

### MEM-R15 Staged, per-file atomic publication
All candidates are prepared before any publication. Only changed targets are written; each write
is atomic per file (temp + fsync + rename + parent fsync), not across the set. Before writing, the
old target is backed up into the attempt dir and target containment is rechecked: a symlinked
target, a dangling-symlink ancestor, or an escape from the memory dir is rejected. A context
cancel after preparation but before promotion writes nothing.

Evidence: engine:internal/memorypipeline/rebuild_test.go `TestFinalize_PublishOnlyChanged`,
`TestFinalize_CtxCancelledBeforePromotionNoWrite`, `TestFinalize_SecondWriteFailsLeavesPromoting`,
`TestFinalize_SymlinkedTargetParentRejected`, `TestValidateTargetUnderMemoryDir_SymlinkedFinalFile`,
`TestValidateTargetUnderMemoryDir_DanglingSymlinkParent`; afm:contracts/memory/paths_test.go
`TestAtomicWrite_ReplacesExistingAndLeavesNoTempFile`; afm:contracts/memory/paths.go `AtomicWrite`

### MEM-R16 Memory lock
The memory lock file lives at `~/.afm/locks/memory-<sha256(canonical abs memory dir)>.lock`,
independent of trailing slashes and symlink aliases (even for a not-yet-existing dir). Acquire
blocks while held until release or context cancel; an already-cancelled context never acquires;
non-busy I/O errors return immediately.

Evidence: engine:internal/memorypipeline/lock_test.go `TestMemoryLockPath_StableForTrailingSlash`,
`TestMemoryLockPath_SymlinkAliasSameKey_EvenWhenMemoryDirDoesNotExist`,
`TestAcquireMemoryLock_Serializes_SecondBlocksUntilCtxCancel`, `TestAcquireMemoryLock_AlreadyCancelledCtx_NeverAcquires`,
`TestAcquireMemoryLock_NonBusyErrorPropagatesImmediately`; engine:internal/memorypipeline/lock.go

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MEM-E04 | no agent-session sources | capture `StepError` `no agent-session sources in <dir>`; live → `reflect_failed` notice | engine:internal/memorypipeline/rebuild_test.go `TestCaptureStage_ScriptOnlyNoAgentSessionStepError` |
| MEM-E05 | invalid dataset / rules candidate | `StepError` for reflect/update; target not published | engine:internal/memorypipeline/dataset_test.go; distill_test.go `TestValidateRules` |
| MEM-E06 | aggregate wrote nothing / update left symlink | `StepError`, nothing published | engine:internal/memorypipeline/distill_test.go `TestDistillTarget_AggregateWritesNothingRejected`, `TestDistillTarget_UpdateSymlinkCandidateRejected` |
| MEM-E07 | target escapes memory dir or is a symlink | finalize error, no write | engine:internal/memorypipeline/rebuild_test.go `TestFinalize_SymlinkedTargetParentRejected` |

## Coverage gaps and discrepancies
- **MEM-G05** (doc-only): "afm dialog/completion mechanics are excluded from project patterns" is
  enforced only by prompt text (`prompts.go`, `protocol_constants.go`), not by code checks.
