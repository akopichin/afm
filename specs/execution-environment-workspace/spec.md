# Execution environment: agent workspace, worktrees, skills and prompts

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Where agents run: afm-root, `flow.root_dir`, worktree, and the absolute `AFM_STAGE_DIR`.
- Per-flow git worktrees: enablement, creation/reuse, flow lock.
- Skill library resolution and the prompt the agent receives (high level).
- Siblings: processes, env, secrets — [../execution-environment/spec.md](../execution-environment/spec.md);
  Docker and wrappers — [../execution-environment-docker/spec.md](../execution-environment-docker/spec.md);
  browser and isolation — [../execution-environment-browser/spec.md](../execution-environment-browser/spec.md).
- Out of scope: dialog protocol details (`../dialog-approval/spec.md`), memory content (`../agent-memory/spec.md`).

## Terms
- **afm-root**: parent of `.afm/`; chosen by `--dir` > `AFM_DIR` > `.`.
- **Effective root** (`EffectiveRoot`): worktree, else `root_dir` from afm-root, else afm-root; never the CWD.
- **Agent root**: the agents' CWD — worktree, else resolved `root_dir`, else empty (inherit afm's CWD).

## Requirements

### EXE-R20 afm-root selection
afm-root is `--dir`, else `AFM_DIR`, else `.`, and paths derived from it are made absolute. Before a Docker
re-exec afm appends `--dir=<absolute afm-root>`, which wins over a relative user flag in the container.

Evidence: `afm:cmd/afm/main_test.go` `TestResolveRootDir`, `TestAbsRootDir_IsAbsolute`; `afm:cmd/afm/main.go` `resolveRootDir`; `afm:cmd/afm/run_environment.go` `reexecFlow`; `afm:docs/config-reference.md`

### EXE-R21 Agent CWD
With a worktree the agent CWD is the worktree; otherwise `root_dir` (relative joined to the absolute
afm-root); with no `root_dir` `Config.Dir` is empty and the agent inherits afm's CWD. Shell-verify steps and
script stages run in the same root.

Evidence: `engine:internal/executor/executor_test.go` `TestRunAgentRunsInConfiguredDir`; `engine:internal/engine/agent_environment_test.go` `TestResolveAgentRoot_MatchesRunInlineLogic`; `engine:internal/engine/run_environment_test.go` `TestPrepareRunEnvironment_RelativeAfmRootGivesAbsolutePaths`; `engine:internal/orchestrator/verify_shell_dir_test.go` `TestRunVerification_ShellStepRunsInProjectRoot`; `engine:internal/orchestrator/hooks_test.go` `TestExecScript_RunsInRootDirAndPublishesOutput`

#### EXE-S30 Relative root_dir
- GIVEN afm-root `/p` and `root_dir: app`
- WHEN a stage agent starts without worktrees
- THEN its CWD is `/p/app`

### EXE-R22 Absolute agent root for consumers outside the process
`AbsoluteAgentRoot` always returns an absolute path; empty `root_dir` yields afm's current CWD. Lifecycle
hook metadata and browser MCP filesystem roots use the same rule (empty root → CWD).

Evidence: `engine:internal/engine/agent_environment_test.go` `TestAbsoluteAgentRoot_EmptyRootDirReturnsCWD`, `TestLifecycleRootDir_EmptyReturnsAbsoluteCWD`; `engine:internal/orchestrator/browser_policy_test.go` `TestBrowserForStage_EmptyRootDirUsesCWD`

### EXE-R23 Effective root and worktree base
`WorktreeBase` is `root_dir` against afm-root, else afm-root — never the CWD. `EffectiveRoot` is the worktree
when present, else `WorktreeBase`. It is the base for relative `skills_dir`, not the agent CWD.

Evidence: `afm:cmd/afm/run_worktree_test.go` `TestWorktreeBase_UsesRootDirWhenSet`, `TestWorktreeBase_FallsBackToAfmRoot`; `engine:internal/engine/run_skills_docker_test.go` `TestEffectiveRoot`; `engine:internal/engine/run_skills_test.go` `TestPrepareRunEnvironment_SkillsBase`

### EXE-R24 AFM_STAGE_DIR is absolute and independent of CWD
Every author turn gets `AFM_STAGE_DIR=<absolute run dir>/<stage id>`, even for a relative run dir and paths
with spaces. Interactive rules print the same absolute path. Shell-verify sees an absolute `AFM_STAGE_DIR`.

Evidence: `engine:internal/orchestrator/stagedir_test.go` `TestStageDir_IsAbsoluteForRelativeRunDir`, `TestStageDir_SpacesInPathPreserved`, `TestRunnerConfig_StageDirIsAbsolute`; `engine:internal/executor/executor_test.go` `TestRunSetsStageDir`; `engine:internal/orchestrator/verify_shell_dir_test.go` `TestRunVerification_RealShellStepSeesAbsoluteStageDir`; `engine:internal/prompts/builder_test.go` `TestBuild_InteractivePrintsAbsolutePathAndNowhereElse`

#### EXE-S31 Worktree CWD, stage dir in run dir
- GIVEN a run with a worktree at `/p/.worktrees/f`
- WHEN the stage agent writes `$AFM_STAGE_DIR/q1.json`
- THEN the file lands in `/p/.afm/runs/<run>/<stage>/`, not in the worktree

### EXE-R25 Worktree enablement
Worktrees are on with a non-empty `--worktree=<name>`, else per `flow.worktrees`, else
`AFM_WORKTREES`/`worktrees.enabled`, default off. `--worktree-clear` and `--worktree-clear-force` are
mutually exclusive and require worktrees enabled.

Evidence: `afm:cmd/afm/run_worktree_test.go` `TestWorktreesEnabled_Precedence`, `TestResolveWorktree_ClearWithoutWorktreesIsAnError`, `TestResolveWorktree_BothClearFlagsIsAnError`

### EXE-R26 Worktree creation and reuse
The worktree lives at `<WorktreeBase>/.worktrees/<name>` on branch `<name>` (name = `--worktree` or flow
name, validated as branch and path component). `Ensure` creates or reuses it as-is (uncommitted work kept);
`ClearWorktree` discards uncommitted work, `ClearAll` also recreates the branch from HEAD. A non-repo base, a
branch checked out elsewhere or a foreign directory at the path is an error, not a repair. `.worktrees/` is
added to `info/exclude`, best-effort.

Evidence: `afm:contracts/worktree/ensure_test.go` `TestEnsure_CreatesWorktreeAndBranch`, `TestEnsure_ReusesWithUncommittedChanges`, `TestEnsure_BranchCheckedOutElsewhere`, `TestEnsure_ClearAll_DropsCommits`, `TestEnsure_ExcludeFailureIsNotFatal`; `afm:contracts/worktree/plan_test.go` `TestPlanFor_RejectsUnsafeNames`

### EXE-R27 Flow lock and container hand-off
The host takes a per-worktree-name lock before the first mutating git call and holds it for the whole run;
a second run or clear on the same name fails while it is held. A re-exec'd container does not lock or touch
git: it reads the worktree path from the transport env.

Evidence: `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_RefusesClearWhileFlowLockHeld`, `TestResolveWorktree_LocksByWorktreeNameNotFlowName`, `TestResolveWorktree_InContainerReadsTransportEnv`; `afm:contracts/worktree/flowlock_test.go` `TestTryLockFlow_BusyWhileHeld`

### EXE-R28 Skill library resolution
Each stage, helper and team member gets the nearest explicit `skills_dir` (own, flow, config); a relative
path resolves from the effective root. Libraries must be clean absolute directories and are checked before
any agent starts; a resumed run keeps the libraries saved in its registry.

Evidence: `engine:internal/engine/run_skills_test.go` `TestWithSkillLibraries_NearestExplicitValueWins`, `TestCheckSkillLibraries`, `TestSettleSkillRun`; `engine:internal/engine/run_skills_assistants_test.go` `TestWithSkillLibraries_StageAssistants`

### EXE-R29 Prompt assembly
`prompts.Build` emits `<system_rules>` (template or autonomous template, output contract, interactive rules,
image-output contract, optional browser and agent-communication blocks), then global prompt, project memory,
context, `<stage>` with description and skills, and stage prompt/plan/feedback blocks. User-supplied text is
tag-escaped. The completion contract (`.done` / `execution_summary.md`) is added by code on every attempt,
also when `prompts_dir` overrides a template.

Evidence: `engine:internal/prompts/builder_test.go` `TestBuild_HasSystemRulesAndStageBlocks`, `TestBuild_PromptEscapesTags`, `TestBuild_IncludesImageOutputContract`; `engine:internal/orchestrator/completion_contract_prompt_test.go` `TestContractSurvivesPromptOverride`; `afm:docs/internals/prompts.md`

### EXE-R30 Skills block is a prompt contract
The `<skills>` block lists required skills in order with native-first loading and an exact-name library
fallback, or the catalog alone with no requirement; it carries metadata and `SKILL.md` paths, not bodies.
Verify prompts do not include the stage's skills.

Evidence: `engine:internal/prompts/skills_test.go` `TestSkillsBlock_RequiredAndLibrary`, `TestSkillsBlock_LibraryOnlyRequiresNothing`, `TestSkillsBlock_EscapesUserValues`, `TestBuildVerify_NoSkillsOfTheStage`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| EXE-E10 | Clear flag with worktrees off | `--worktree-clear requires worktrees to be enabled` | `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_ClearWithoutWorktreesIsAnError` |
| EXE-E11 | Both clear flags | `mutually exclusive` | `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_BothClearFlagsIsAnError` |
| EXE-E12 | Flow lock busy | run refused, message names the worktree | `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_RefusesClearWhileFlowLockHeld` |
| EXE-E13 | Base not a git repo / branch checked out elsewhere / stray dir | `worktree: …` error, nothing mutated | `afm:contracts/worktree/ensure_test.go` `TestEnsure_NotAGitRepository`, `TestEnsure_PathOccupiedByStrayDirectory` |
| EXE-E14 | Invalid worktree name | rejected before any run dir is created | `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_RejectsInvalidName` |
| EXE-E15 | `skills_dir` relative/unclean/missing | `skills_dir … must be a clean absolute path` / library error; run does not start | `engine:internal/engine/run_skills_test.go` `TestCheckSkillLibraries` |

## Coverage gaps and discrepancies
- **EXE-G10** (code≠doc): `afm:docs/internals/prompts.md` places `Build()` in `pkg/prompts/builder.go` and its
  structure omits `<global_prompt>`, `<project_memory>`, `<image_output>`, `<browser_tools>` and the agent
  communication blocks; the code is `engine:internal/prompts/builder.go`.
- **EXE-G11** (untested): EXE-S31 (worktree CWD + stage dir in the run dir) is not covered by one test;
  the parts are tested separately.
- **EXE-G12** (untested): agent CWD for helper and team-member runs is code-only (shares `Config.Dir`).
- **EXE-G13** (ambiguous): empty `root_dir` makes the CWD depend on where afm was started, while
  `WorktreeBase`/`EffectiveRoot` use afm-root; `--dir` without `root_dir` runs agents outside afm-root.
