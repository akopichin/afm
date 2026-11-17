# Agent memory: offline rebuild

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `afm memory rebuild [flow.yaml]`: run selection, preconditions, lock order, capture/finalize
  over a completed historical run, dry-run, commit decision, attempt manifest, Docker re-exec.
- The agent-memory domain is split into: configuration and injection (`../agent-memory/spec.md`),
  live run path (`../agent-memory-live/spec.md`), shared pipeline (`../agent-memory-pipeline/spec.md`)
  and offline rebuild (this file). Prefix `MEM` is shared; this file uses numbers 30+.
- Out of scope: general flow parsing (`../flow-config/spec.md`); Docker re-exec mechanics beyond
  rebuild-specific checks.

## Terms
- **attempt dir** — `<runDir>/memory-rebuild/<YYYYMMDD-HHMMSS>-<2hex>/`, holding staged datasets,
  distill logs, candidates, backups and `manifest.json`.
- **canonical dataset** — `<runDir>/<stageID>/reflect_dataset.yaml`.
- **run name** — `--worktree` value if set, else the flow name; run dirs are prefixed by it.

## Requirements

### MEM-R30 Preconditions
Rebuild uses the current flow file (explicit arg, or resolved from `.afm/flows`) and current
prompts. It fails when `memory.path` is empty or when no non-script stage has writable `reflect`
(`memory.mode: w` alone is not enough).

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_MissingMemoryConfig`,
`TestRebuildHandler_NoWritableTarget`, `TestMemoryRebuildCmd_Registered`;
afm:cmd/afm/memory_rebuild.go `rebuildHandler`, `hasWritableTarget`

### MEM-R31 Automatic run selection
Without `--run`, rebuild picks the newest run whose name has the run-name prefix, all stages done,
and whose stage set exactly equals the current flow's stage IDs; newer drifted runs are skipped.
The selected run id is printed.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_PersistentDirResolvesRunsDir`,
`TestRebuildHandler_DryRun_NoFSMMutation`; afm:cmd/afm/memory_rebuild.go
(`FindLatestCompletedRunDir`); afm:docs/agent-memory.md

### MEM-R32 Explicit `--run`
`--run <id>` must be a bare directory name (no separators, not `.`/`..`), must belong to the run
name, and must be a real (non-symlink) directory under the runs dir. It bypasses the completed-run
filter, so an incomplete run or a stage-set mismatch is then a hard error listing sorted missing
and extra stage IDs.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestResolveExplicitRunDir`,
`TestResolveExplicitRunDir_RejectsSymlink`, `TestRebuildHandler_ExplicitRunIncomplete`,
`TestRebuildHandler_StageSetMismatch`, `TestCheckStageSetMatchesExact_SortedMissingAndExtra`;
afm:cmd/afm/memory_rebuild.go `resolveExplicitRunDir`, `checkStageSetMatchesExact`

#### MEM-S30 Stage set drift with explicit run
- GIVEN a completed run with stages `a,b` and a current flow with stages `a,c`
- WHEN `afm memory rebuild --run <id>` runs
- THEN it fails with `run stage set does not match current flow: missing [c], extra [b]`

### MEM-R33 Worktree targeting
`--worktree <name>` uses `<project-root>/.worktrees/<name>` as the agent root (and thus the base for
a relative `memory.path`) and `<name>` as the run name. A missing worktree is an error.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_WorktreeMissingFails`,
`TestRebuildHandler_WorktreeUsesWorktreeRunsAndMemoryDir`, `TestMemoryRebuildCmd_HasWorktreeFlag`;
afm:cmd/afm/memory_rebuild.go

### MEM-R34 Lock order and active-run rejection
Rebuild takes the run lock (non-blocking) before anything else that locks; a run held by a live
`afm run` is rejected immediately with `run <id> is active`. Capture runs without the memory lock;
then rebuild blocks on the memory lock and holds it through finalize and commit.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_ActiveRunLocked`;
afm:cmd/afm/memory_rebuild.go `rebuildHandler`; engine:internal/memorypipeline/AGENTS.md

#### MEM-S31 Live run holds the run
- GIVEN `afm run` currently holds the selected run's lock
- WHEN `afm memory rebuild` runs
- THEN it exits non-zero with `run <id> is active; stop `afm run` or wait` without running agents

### MEM-R35 Run state is never mutated
Rebuild does not change `events.jsonl` or the FSM; it writes only under the attempt dir and, when
publishing, memory targets and regenerated canonical datasets.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_DryRun_NoFSMMutation`;
afm:docs/agent-memory.md (see MEM-G31)

### MEM-R36 Offline capture into the attempt workspace
Capture iterates write-reflect stages and writes generated datasets into
`<attempt>/stages/<id>/reflect_dataset.yaml`; a valid canonical dataset is reused, an invalid one
is regenerated, and `--force-reflect` always regenerates. Canonical datasets are replaced only during
publication, and only when generated and changed. Capture requires a manifest path.

Evidence: engine:internal/memorypipeline/rebuild_test.go `TestCaptureAll_WritesStagingAndAdvancesManifest`,
`TestCaptureAll_ReusesValidCanonical`, `TestCaptureAll_ForceReflectRegeneratesIntoStaging`,
`TestCaptureAll_InvalidCanonicalRegenerates`, `TestCaptureAll_RequiresManifestPath`; engine:internal/memorypipeline/rebuild.go `CaptureAll`

### MEM-R37 Dry run
`--dry-run` runs agents and writes attempt artifacts (staging, logs, candidates, manifest with
status `dry_run`) and prints `would change` diffs, but publishes no memory file or dataset, does not
create the memory dir, and never commits.

Evidence: engine:internal/memorypipeline/rebuild_test.go `TestFinalize_DryRunLeavesTargetsUntouched`,
`TestFinalize_DryRunDoesNotCreateMemoryDir`; afm:cmd/afm/memory_rebuild_test.go
`TestRebuildHandler_DryRun_NoFSMMutation`, `TestEffectiveCommit_Matrix`; afm:cmd/afm/memory_rebuild.go `printReport`

#### MEM-S32 Dry run with commit enabled in flow
- GIVEN `memory.commit: true` in the flow
- WHEN `afm memory rebuild --dry-run` runs
- THEN no file under the memory dir changes and no commit is created

### MEM-R38 Commit decision
Effective commit: `--dry-run` → false; else an explicit `--commit`/`--no-commit` (only when passed
with value true) wins; else `memory.commit`. Passing both forcing flags is an error.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestEffectiveCommit_Matrix`,
`TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive`, `TestParseCommitFlags_NoCommitFalseDoesNotForceNoCommit`;
afm:cmd/afm/memory_rebuild.go `effectiveCommit`, `parseCommitFlags`

### MEM-R39 Commit preflight and scoped commit
With commit enabled, rebuild checks before capture and again under the memory lock that no changes
are already staged under the memory dir (git found from the nearest existing ancestor; a non-git
ancestor is a no-op). After publication it commits only the changed, published targets via
`CommitPaths` with message `chore(memory): rebuild from <runID>`, recording `commit_created` and
the SHA in the manifest; nothing published means no commit.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestCommitPreflight_RejectsPreStagedChangesUnderMemDir`,
`TestCommitPreflight_WorksWhenMemDirDoesNotExistYet`,
`TestCommitPreflight_NonGitAncestorIsNoOp`, `TestMaybeCommit_SuccessRecordsCommitCreatedAndSHA`,
`TestMaybeCommit_NoPublishedChangesIsNoOp`; afm:contracts/memory/commit_test.go
`TestCommitPaths_CommitsOnlyGivenPaths`, `TestCommitPaths_NothingToCommit`

### MEM-R40 Attempt manifest lifecycle
The CLI writes `manifest.json` as `running` with metadata (run, flow and prompt SHA-256, memory
dir, mode, max_rules, effective commit). The pipeline advances it through capturing → distilling
→ promoting (recording each `Published` target/dataset) → `published` or `awaiting_commit` (or
`dry_run`); the CLI writes the final `completed`. Any early return turns a non-terminal manifest
into `failed`, or `cancelled` when the context is cancelled.

Evidence: engine:internal/memorypipeline/operation_test.go `TestWriteManifestLoadManifest_RoundTrip`,
`TestNewRunningManifest_StatusAndMeta`, `TestMarkManifestCompleted`,
`TestFinalizeManifestOnExit_CancelledWhenCtxCancelled`, `TestFinalizeManifestOnExit_FailedWhenCtxLiveAndErrSet`,
`TestFinalizeManifestOnExit_NonTerminalPromoting`;
afm:cmd/afm/memory_rebuild_test.go `TestMaybeCommit_FailureMarksManifestFailed`

### MEM-R41 Partial publication is recoverable and warned
If a write fails mid-promotion, the manifest stays `promoting` with already-published entries
marked and old content backed up in the attempt dir. A later rebuild prints a warning naming
attempts left in `promoting` and proceeds in a fresh attempt dir.

Evidence: engine:internal/memorypipeline/rebuild_test.go `TestFinalize_SecondWriteFailsLeavesPromoting`;
engine:internal/memorypipeline/operation_test.go `TestScanUnfinishedPromotions_FindsOnlyPromoting`,
`TestNewUniqueAttemptDir_RetriesOnCollision`;
afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_StalePromotingManifestWarns`

### MEM-R42 Strict failure reporting
Unlike live memory, any capture, finalize, commit or manifest error makes rebuild exit non-zero
(`capture failed (see <attempt>)`, `rebuild failed (see <attempt>)`, `memory files updated but
commit failed`). Output shows only counts, paths and diffs, never agent stdout or prompts.

Evidence: afm:cmd/afm/memory_rebuild_test.go `TestMaybeCommit_FailureMarksManifestFailed`;
afm:cmd/afm/memory_rebuild.go `rebuildHandler`, `printReport`; engine:internal/memorypipeline/AGENTS.md

### MEM-R43 Docker re-exec
If Docker mode is enabled and the process is not already inside a container, rebuild re-execs into
the container before taking any lock, forwarding its options, an absolute flow path and an absolute
`--dir`. Before re-exec, flow/run/memory paths must be reachable in the container (project dir or
`docker.extra_mounts`), and the memory dir must be under the project dir (extra mounts are
read-only). A claude-type client needs auth for the container.

Evidence: afm:cmd/afm/memory_rebuild_docker_test.go `TestRebuildDockerReExec_SkippedWhenDockerDisabled`,
`TestRebuildDockerReExec_SkippedWhenInDocker`, `TestRebuildDockerReExec_ClaudeCommandRequiresAuth`,
`TestRebuildDockerArgs_ReconstructsFromOptions`, `TestRebuildDockerPreflight_RelativePathOutsideRootFails`, `TestRebuildDockerMemoryWritable_ExternalPathFailsEvenIfMounted`, `TestRebuildHandler_DockerEnabled_SkipsReExecInsideContainer`;
afm:cmd/afm/memory_rebuild_docker.go

#### MEM-S33 External memory dir under Docker
- GIVEN Docker enabled and `memory.path` resolving outside the project dir but inside an extra mount
- WHEN `afm memory rebuild` runs on the host
- THEN it fails with `docker: memory.path resolves to ... outside the project directory` before re-exec

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MEM-E30 | flow has no `memory.path` | `flow "<name>" has no memory.path — nothing to build` | afm:cmd/afm/memory_rebuild_test.go `TestRebuildHandler_MissingMemoryConfig` |
| MEM-E31 | no writable reflect stage | `no writable memory target (need a stage with reflect.mode w|rw)` | `TestRebuildHandler_NoWritableTarget` |
| MEM-E32 | bad / foreign / symlinked `--run` | `invalid run id` / `does not belong to` / `not found` | `TestResolveExplicitRunDir`, `TestResolveExplicitRunDir_RejectsSymlink` |
| MEM-E33 | explicit run incomplete | `run <id> is not completed (some stages are not done)` | `TestRebuildHandler_ExplicitRunIncomplete` |
| MEM-E34 | run locked by live `afm run` | `run <id> is active; stop `afm run` or wait` | `TestRebuildHandler_ActiveRunLocked` |
| MEM-E35 | pre-staged changes under memory dir, commit on | `memory dir <dir> already has staged changes; ...` | `TestCommitPreflight_RejectsPreStagedChangesUnderMemDir` |
| MEM-E36 | `--commit` and `--no-commit` both true | `--commit and --no-commit are mutually exclusive` | `TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive` |
| MEM-E37 | worktree missing | `worktree "<name>" not found at <path>` | `TestRebuildHandler_WorktreeMissingFails` |
| MEM-E38 | Docker path unreachable / memory outside project | `docker: ...` preflight error, no re-exec | afm:cmd/afm/memory_rebuild_docker_test.go |

## Coverage gaps and discrepancies
- **MEM-G30** (code≠doc nuance): with empty `root_dir` and no `--worktree`, rebuild uses
  `AbsoluteAgentRoot` (process CWD) as the memory base, while `afm run` falls back to afm-root;
  docs state this, but no test pins the divergence.
- **MEM-G31** (untested): "rebuild never mutates `events.jsonl`/FSM" is checked only for the dry-run
  path (`TestRebuildHandler_DryRun_NoFSMMutation`), not for a publishing rebuild.
- **MEM-G32** (untested): the second commit preflight under the memory lock (race with another
  writer staging changes) has no test.
- **MEM-G33** (untested): auto-selection skipping a newer drifted run has no afm-side test (resolver
  is `FindLatestCompletedRunDir` in the engine).
