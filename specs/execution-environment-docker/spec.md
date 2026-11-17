# Execution environment: Docker re-exec, wrappers and prepared commands

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The host-side Docker re-exec of `afm run` (`docker.ReExec`, `reexecFlow`) and what crosses into the container.
- Container predicates, the Docker projection of commands, generated recipe wrappers, skill library mounts.
- The run's prepared command set.
- Siblings: processes/env/secrets — [../execution-environment/spec.md](../execution-environment/spec.md);
  CWD/worktrees/prompts — [../execution-environment-workspace/spec.md](../execution-environment-workspace/spec.md);
  managed browser and read-only isolation — [../execution-environment-browser/spec.md](../execution-environment-browser/spec.md).
- Out of scope: lifecycle hook semantics (`../lifecycle-hooks/spec.md`), file browser (`../http-api/spec.md`).

## Terms
- **Re-exec'd container**: afm's own container, `AFM_IN_DOCKER=1` (`ReExecedIntoContainer`).
- **autoShim / recipe**: `docker.agents` entry turning a command into a generated wrapper script.
- **Projection** (`DockerProjection`): used recipes, generated commands, extra helper commands, codex-state flag.

## Requirements

### EXE-R13 Prepared command set gates dynamic stages
At orchestrator construction the run records `client.command` and each initial stage command (deduplicated,
with config references, never secret values). A stage added during the run may only use a command in this
set; a nil set rejects every command.

Evidence: `engine:internal/execprep/execprep_test.go` `TestJournal_StoresConfigRefsNotSecretValues`, `TestSet_NilIsFailClosed`; `engine:internal/orchestrator/execprep_wire_test.go` `TestBuildPreparedCommandSet_UnconfiguredCommandNotPrepared`; `engine:internal/orchestrator/graph/dynamic_test.go` `TestDynamic_CommandMustBePrepared`

### EXE-R40 Two container predicates
`InContainer()` is true in afm's own or any foreign container; it disables Docker mode (no nested re-exec)
and host browser-open. `ReExecedIntoContainer()` is true only with `AFM_IN_DOCKER=1` and alone gates
consuming afm transport: wrappers, hook secrets, file-root manifest, worktree path, checked skill
libraries, browser descriptor. Outside a container Docker is on per `docker.enabled`, else `AFM_USE_DOCKER`.

Evidence: `afm:contracts/config/config_test.go` `TestInContainer`, `TestDockerConfig_IsDockerEnabled`; `afm:contracts/config/config.go` `InContainer`, `ReExecedIntoContainer`; `afm:contracts/docker/AGENTS.md`

#### EXE-S60 Foreign container
- GIVEN afm started in a CI container with a container marker and no `AFM_IN_DOCKER`
- WHEN the run prepares its environment
- THEN Docker mode is off and no wrappers or transport secrets are consumed

### EXE-R41 Shape of the re-exec
The host runs `docker run --rm --init` (`-it` only on a TTY), mounts the project at the same absolute path
as CWD plus `~/.claude` and `~/.afm` (not `~/.claude.json`), mounts non-recipe command binaries and extra
mounts read-only, and `~/.codex` read-only at `/tmp/host-codex` when codex is used and the dir exists. The
dashboard port is published on `127.0.0.1` only. Non-secret toggles (`AFM_DEBUG`, `AFM_SIDEAGENT`, …) pass
by value and only when the host set them. `docker` missing from `PATH` is an error.

Evidence: `afm:contracts/docker/launcher_test.go` `TestReExec_BuildsDockerArgs`, `TestReExec_DashboardAlwaysLoopback`, `TestReExec_ExtraMountPathResolution`, `TestReExec_CodexStateMount_SkippedWhenDirMissing`, `TestReExec_ForwardsSideAgentEnv`, `TestReExec_DockerNotFound`; `afm:contracts/docker/launcher.go` `ReExec`

### EXE-R42 Secrets cross only as bare `-e NAME`
Claude auth variables and `ANTHROPIC_BASE_URL` are forwarded bare when set. Recipe secrets
(`AFM_SECRET_<cmd>`), recipe system prompts (`AFM_SYSPROMPT_<cmd>`), lifecycle-hook secrets (position-named
transport vars, sorted keys) and the browser token are resolved on the host, set in afm's own environment
and passed as bare `-e NAME`; no secret value appears in `docker` argv and secret files are not mounted.

Evidence: `afm:contracts/docker/launcher_test.go` `TestReExec_PassthroughEnv`, `TestReExec_RecipeTransientEnv_NoMount`, `TestReExec_HookSecretTransport`, `TestReExec_BrowserTokenNotInArgv`; `afm:docs/docker.md`

#### EXE-S61 Recipe key never in argv
- GIVEN recipe `glm` with `auth.from: env:GLM_KEY` and `GLM_KEY=sk-x` in the project `secrets.env`
- WHEN afm re-execs into Docker
- THEN argv contains `-e AFM_SECRET_glm` and does not contain `sk-x`

### EXE-R43 Projection covers every command the run may execute
The projection includes stage commands, agent verify-step commands, `client.command`, helper profile
commands and commands of unfinished saved helper tasks (except tasks admitted under a since-changed recipe).
Only used recipes get wrappers and secrets; recipe commands are not mounted; the codex state mount also
follows helper-only codex use. The result is independent of map iteration order.

Evidence: `engine:internal/engine/docker_projection_test.go` `TestProjectDocker_StableAcrossMapIterationOrder`, `TestProjectDocker_CodexStateForAHelperOnlyAdapter`; `afm:contracts/docker/launcher_test.go` `TestScanCommands_VerifyOnlyCommandIsMounted`, `TestUsedRecipes_FiltersUnusedAgents`, `TestUsedRecipeCommands_IncludesHelperProfileCommands`; `engine:internal/engine/run_environment_test.go` `TestRestart_TaskOfAChangedRecipeNeedsNoWrapperOrSecret`

### EXE-R44 Wrappers exist only in the re-exec'd autoShim container
Inside a re-exec'd container with autoShim, the engine generates one wrapper per used recipe from
`BuildWrapperSpec` (type, auth var, base URL, model, system-prompt presence, bare flag, max turns,
reasoning) into a run-owned wrapper dir removed on close. A wrapper reads its secret and unsets it before
exec. Every recipe alias records a recipe identity that helper tasks compare before each turn. On the host
no wrappers are generated.

Evidence: `engine:internal/engine/run_test.go` `TestBuildWrapperSpec`; `afm:contracts/docker/wrapper_test.go` `TestCreateWrappers_AgentTemplate`, `TestCreateWrappers_CodexWithAuth`, `TestCreateWrappers_NoClaudeHardError`; `engine:internal/engine/run_environment_test.go` `TestPrepareRunEnvironment_RecipeIdentity`, `TestPrepareRunEnvironment_WrapperForSavedTask`

### EXE-R45 Skill libraries are checked and mounted by the host
Before `docker run`, `PrepareSkillMounts` requires every library to exist, mounts libraries outside the
project read-only at the same path, and accepts a mount only under a root allowed by config (`skills_dir`
or `docker.extra_mounts`), never `/`, home or project parents. A library the container would resolve to a
different directory, or a path with `:`, is rejected. The checked list is forwarded; in the container a
library not in it is refused with a restart error.

Evidence: `engine:internal/engine/run_skills_docker_test.go` `TestPrepareSkillMounts`, `TestCheckSkillMounts`, `TestCheckSkillMounts_SourceMustExist`, `TestPrepareSkillMounts_RejectsALibraryTheContainerResolvesDifferently`; `engine:internal/engine/run_skills_test.go` `TestSettleSkillRun_ContainerTakesOnlyHostCheckedLibraries`; `afm:contracts/docker/launcher_test.go` `TestReExec_ForwardsSkillLibraries`

### EXE-R46 Worktree crosses as a path
The host passes the worktree path by value; the container uses it as the effective root without git work
and without an extra mount. An absolute `root_dir` outside afm-root is rejected before Docker starts.

Evidence: `afm:contracts/docker/launcher_test.go` `TestReExec_ForwardsWorktreeEnv`, `TestReExec_WorktreeNeedsNoExtraMount`; `afm:cmd/afm/run_worktree_test.go` `TestResolveWorktree_RefusesOutsideProjectDirUnderDocker`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| EXE-E08 | Dynamic stage uses an unprepared command | stage insertion rejected | `engine:internal/orchestrator/graph/dynamic_test.go` `TestDynamic_CommandMustBePrepared` |
| EXE-E20 | `claude` in Docker without an auth env var | re-exec refused with setup instructions | `afm:contracts/docker/launcher.go` `CheckClaudeDockerAuth`; `afm:cmd/afm/memory_rebuild_docker_test.go` |
| EXE-E21 | Recipe or hook secret unresolvable | `agent <cmd>: secret not found …` / `lifecycle hook … env …`; no container | `afm:contracts/docker/launcher_test.go` `TestReExec_RecipeMissingSecretFailFast`, `TestReExec_HookSecretMissingFailFast` |
| EXE-E22 | Codex recipe without `auth` | allowed, no secret var | `afm:contracts/docker/launcher_test.go` `TestReExec_CodexRecipeNoAuth_DoesNotFail` |
| EXE-E23 | Recipe `system_prompt` unreadable | silent fallback to the default system prompt | `afm:contracts/docker/launcher.go` `ReExec` |
| EXE-E24 | Skill library missing / not allowed / `:` | run refused before `docker run` | `engine:internal/engine/run_skills_docker_test.go` `TestCheckSkillMounts` |
| EXE-E25 | `docker` not on PATH | `docker not found in PATH` | `afm:contracts/docker/launcher_test.go` `TestReExec_DockerNotFound` |

## Coverage gaps and discrepancies
- **EXE-G02** (untested): `execprep.Prepare` fail-fast is unreachable in production (resolver always ok);
  optional helper-profile commands are never passed in; verify-only commands are not in the prepared set.
- **EXE-G20** (untested): EXE-E23 silent system-prompt fallback has no test.
- **EXE-G21** (doc-only): HOME set after `gosu` and the entrypoint copying `/tmp/host-codex` live in the image
  entrypoint, not Go code (`afm:contracts/docker/AGENTS.md`, `afm:docs/docker.md`).
- **EXE-G22** (doc-only): bare `-e` hides values from argv, but transport secrets stay in afm's process
  environment (and PID 1's initial environ in the container); the container is the trust boundary.
- **EXE-G23** (untested): `CheckClaudeDockerAuth` is tested only via `afm memory rebuild`, not `afm run`.
