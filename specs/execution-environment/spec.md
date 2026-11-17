# Execution environment: agent processes and child environment

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How the engine launches stage, verify and helper agent processes (argv, stdin, process group, stop).
- The child environment: inherited-variable filtering, per-mode secret policy, redaction, run-scoped PATH.
- Secret resolution (`secrets.env` layers, `env:`/`file:` refs).
- Sibling parts: CWD, `AFM_STAGE_DIR`, worktrees, skills and prompts —
  [../execution-environment-workspace/spec.md](../execution-environment-workspace/spec.md); Docker
  re-exec, wrappers and the prepared command set (EXE-R13) —
  [../execution-environment-docker/spec.md](../execution-environment-docker/spec.md); managed browser and
  read-only isolation — [../execution-environment-browser/spec.md](../execution-environment-browser/spec.md).
- Out of scope: verify verdicts, helper task lifecycle (`../verify/spec.md`, `../helpers-lifecycle/spec.md`).

## Terms
- **Mode**: `ModeAuthor` (stage agent), `ModeVerify`, `ModeHelper`; verify and helper are *isolated*.
- **Transport secret**: `AFM_SECRET_*`, `AFM_SYSPROMPT_*`, `AFM_HOOK_SECRET_*`, `AFM_MESH_TOKEN`.
- **Anchor**: the afm binary started with a hidden argument as the leader of the agent's process group.

## Requirements

### EXE-R01 Claude argv keeps the stream protocol
`New` defaults an empty command to `claude`. For `claude`, the stream-json protocol flags are prepended once
and user `client.extra_args` are kept intact (no token dedup); a non-claude command with no extra args gets
the defaults. `SessionID` adds `--session-id`, or `--resume` when `Resume`. `client.reasoning` adds
`--effort` only for `claude` and only if no `--effort` is already present.

Evidence: `engine:internal/executor/client_args_test.go` `TestResolveArgsPreservesRepeatedValues`; `engine:internal/executor/executor_test.go` `TestNewAppliesDefaultArgs`, `TestResumeFlagPassedAsArg`; `engine:internal/executor/reasoning_effort_test.go` `TestReasoningEffort_NotDuplicated`; `engine:internal/executor/executor.go` `New`, `runJob`

#### EXE-S01 Repeated user values survive
- GIVEN `client.extra_args` containing the same value for two different options
- WHEN the executor builds argv for `claude`
- THEN both occurrences remain, after the protocol flags

### EXE-R02 Prompt goes through stdin; afm owns the pipes
Agent prompts are written to the process stdin by an afm goroutine, never placed in argv. stdin/stdout/stderr
belong to afm, so a descendant holding stdin does not block the turn. Live stdout lines over 16 MiB are
truncated with a marker.

Evidence: `engine:internal/executor/agent_pipes_unix_test.go` `TestRun_ChildHoldingStdinDoesNotBlockTheTurn`; `engine:internal/executor/linereader_test.go` `TestAgentOutputLineReaderTruncatesOverlongLines`; `engine:internal/executor/agent_pipes.go`; `engine:internal/executor/AGENTS.md`

### EXE-R03 One process group per turn, killed at turn end
The agent and its descendants run in one process group led by the anchor; at the end of each turn the
group is killed before the call returns, including descendants that closed their pipes. Without an anchor
the agent leads its group and only the agent is signalled. Context cancel kills the group; the idle timer
(default 30 min, reset by every output line) kills it with `ErrIdleTimeout`.

Evidence: `engine:internal/executor/agent_pipes_unix_test.go` `TestRun_KillsDescendantThatClosedItsPipes`, `TestProcessGroup_WithoutAnchorTheAgentLeadsItsGroup`; `engine:internal/executor/executor_test.go` `TestIdleTimeout`; `engine:internal/executor/executor.go` `runJob`

#### EXE-S02 Background child does not outlive the turn
- GIVEN an agent that spawns a background child and exits
- WHEN the turn ends
- THEN the child is killed before `Run*` returns

### EXE-R04 Interrupt is graceful for agents, immediate for scripts
A signal on `InterruptCh` sends SIGINT to the agent group, escalates to SIGKILL after a grace period and
returns `ErrUserInterrupted`; a script's group is killed at once. A context already cancelled or an
interrupt already fired before `Start` means the process is never spawned.

Evidence: `engine:internal/executor/interrupt_test.go` `TestRunAgent_InterruptSendsSIGINTNotKill`, `TestRunAgent_PreFiredInterruptNeverStartsProcess`, `TestRunAgent_PreCancelledCtxNeverStartsProcess`

### EXE-R05 Verify and helper runs are fresh and isolated
`RunVerifyAgent` and `RunHelperAgent` clear `SessionID`, `Resume`, `StageDir` and `DialogPhase`, never get the
managed browser, and set `CODEX_VERIFY=1` (an inherited `CODEX_VERIFY` is always stripped, so author runs
never carry it). A helper receives `AFM_SCRATCH_DIR`; `AFM_STREAM_EXTENSION=1` only when its activity has a
consumer. A helper inherits only model/effort flags (`--model`, `--fallback-model`, `--effort`) from
`client.extra_args`.

Evidence: `engine:internal/executor/verify_agent_test.go` `TestRunVerifyAgent_FreshSessionNoResumeNoStageDir`, `TestRunVerifyAgent_SetsCodexVerifyEnv`; `engine:internal/executor/hardening_test.go` `TestHelperRun_FreshSessionAlways`, `TestHelperEnv_DropsBrowserTransport`; `engine:internal/executor/dialog_phase_env_test.go` `TestRunHelperAgent_NoDialogPhase`; `engine:internal/executor/helper_runtime_test.go` `TestHelperClaudeArgs`, `TestStreamExtensionEnv_OnlyForHelperWithActivityConsumer`

#### EXE-S03 Verifier cannot ask the human
- GIVEN a verify step run from a stage that has a dialog phase
- WHEN the verifier process starts
- THEN its environment has no `AFM_DIALOG_PHASE` and no `AFM_STAGE_DIR`

### EXE-R06 Invocation variables are never inherited
Every agent child gets afm's environment minus `CLAUDECODE`, `AFM_STAGE_DIR`, `AFM_DIALOG_PHASE`,
`AFM_SCRATCH_DIR`, the output-schema file, `CODEX_VERIFY`, `AFM_STREAM_EXTENSION`, and every `AFM_BROWSER_*`,
`AFM_MESH_*` and `AFM_STOP_HOOK_*` variable; the executor re-adds only what this invocation's config sets.

Evidence: `engine:internal/executor/executor_test.go` `TestRunStripsInheritedStageDir`; `engine:internal/executor/dialog_phase_env_test.go` `TestRunAgent_StripsInheritedDialogPhase`; `engine:internal/executor/browser_test.go` `TestFilterInheritedEnv_StripsBrowserTransport`; `engine:internal/executor/executor.go` `filterInheritedEnv`

### EXE-R07 Isolated runs drop other agents' transport secrets
An author run keeps all transport secrets. A verify or helper run drops every transport secret except its
own `AFM_SECRET_<command>` (a helper also keeps its own `AFM_SYSPROMPT_<command>`). Shell-verify commands drop
all transport secrets and browser transport, and get an absolute `AFM_STAGE_DIR`.

Evidence: `engine:internal/executor/verify_agent_test.go` `TestRunVerifyAgent_KeepsOwnRecipeSecretButStripsOthers`, `TestRunAgent_KeepsCrossAgentTransportSecrets`; `engine:internal/executor/hardening_test.go` `TestHelperEnv_KeepsOwnSecret`; `engine:internal/orchestrator/verify_shell_security_test.go` `TestRunVerifyShellCommand_StripsCrossAgentTransportSecrets`

#### EXE-S04 Reviewer recipe sees only its own key
- GIVEN `AFM_SECRET_glm` and `AFM_SECRET_codex` in afm's environment
- WHEN a verify step runs command `codex`
- THEN the child has `AFM_SECRET_codex` and not `AFM_SECRET_glm`

### EXE-R08 Claude author credentials and foreign isolated agents
A verify or helper run whose command is not bare `claude` drops the Claude auth variables
(`config.ClaudeAuthEnvVars`) and `ANTHROPIC_BASE_URL`. A bare-`claude` verifier or helper keeps them. A helper
with wrapper runtime context (`HelperWrapperRuntime`) keeps them; verify ignores that flag.

Evidence: `engine:internal/executor/verify_agent_test.go` `TestVerifyEnv_ClaudeVerifierKeepsClaudeCredentials`, `TestVerifyEnv_CodexVerifierStillStripsClaudeCredentials`; `engine:internal/executor/helper_runtime_test.go` `TestHelperRuntimeEnv_Wrapper`, `TestVerifyEnv_UnchangedByHelperRuntime`

### EXE-R09 Helper output is redacted at write time
Everything `RunHelperAgent` writes or reports (raw `.jsonl`, action log, live activity, stderr log, answer)
passes the run's redactor, including JSON-escaped spellings and secrets split across stderr writes. The
run's secret list covers `secrets.env` layers, recipe and hook refs and provider credentials; values shorter
than 4 bytes are not redacted. An author's own mesh token is redacted from its raw output and logs.

Evidence: `engine:internal/executor/hardening_test.go` `TestRunHelperAgent_RedactsEverythingItWrites`, `TestRunHelperAgent_RedactsSecretsAcrossStderrFraming`; `engine:internal/executor/mesh_redaction_test.go` `TestRunAgent_MeshTokenRedactedFromRawOutputAndLogs`; `engine:internal/engine/agent_environment.go` `loadRedactionSecrets`

### EXE-R10 Secret resolution precedence
`docker.LoadSecretLayers` reads `~/.afm/secrets.env` then `<afm-root>/.afm/secrets.env` (later file wins), or
only `docker.secrets_file` when set. `secrets.ResolveRef("env:VAR")` takes a non-empty value from those layers,
else the process environment; `file:PATH` reads a trimmed non-empty file (`~` expanded). Any other form is an
error. Missing files are ignored.

Evidence: `afm:contracts/docker/secrets_test.go` `TestLoadSecretLayers_DefaultAndOverride`; `afm:contracts/secrets/secrets_test.go` `TestResolveRef_EnvAndFile`, `TestLoadSecrets_MergesLayers`

#### EXE-S05 Project layer wins
- GIVEN `K=global` in the global and `K=project` in the project `secrets.env`, and `K=env` in the process
- WHEN `env:K` is resolved
- THEN the value is `project`

### EXE-R11 Child settings belong to the run
The engine prepends the run's command bridge (a temp dir symlinking this afm) only to child `PATH`, and uses
the run's executable as the process-group anchor; afm's own `PATH`, CWD and environment are not changed. No
bridge is created when `afm` on `PATH` is already this binary. Generated wrapper scripts resolve through
`WrapperDir`, prepended to `PATH` and used to resolve the command path, only for generated commands.

Evidence: `engine:internal/runenv/environment_test.go` `TestChildSettingsStayWithTheirRun`; `engine:internal/engine/run_environment_test.go` `TestPrepareRunEnvironment_SkipsShimWhenPathAlreadyHasThisAfm`; `engine:internal/executor/executor_test.go` `TestRunSetsWrapperDirInPATH`

### EXE-R12 Child afm processes start clean
afm processes started by `afm serve` get the parent environment without transport secrets, browser
transport, `AFM_IN_DOCKER` or any run-scoped `AFM_*` invocation variable; git commands of pipeline fetches drop
transport secrets.

Evidence: `afm:pkg/childenv/childenv_test.go` `TestForAFMChildDropsContainerAndRunScopedVars`, `TestStripTransportDropsSecretTransport`; `afm:pkg/serve/launch.go`; `afm:pkg/pipeline/git.go`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| EXE-E01 | Binary missing / not executable / bad argv | `ErrAgentStart`, classified fatal (no retry) | `engine:internal/executor/executor_start_test.go` `TestRun_StartFailure_IsSentinel` |
| EXE-E02 | No output for `IdleTimeout` | group killed, `idle timeout after …` (`ErrIdleTimeout`) | `engine:internal/executor/executor_test.go` `TestIdleTimeout` |
| EXE-E03 | User Pause/Revise during a turn | `ErrUserInterrupted`; not a failure | `engine:internal/executor/interrupt_test.go` `TestRunAgent_InterruptSendsSIGINTNotKill` |
| EXE-E04 | `env:VAR` found nowhere / empty `file:` | `secret not found: env:VAR …` / `secret file is empty` | `afm:contracts/secrets/secrets_test.go` `TestResolveRef_EnvAndFile` |
| EXE-E05 | Ref not `env:` or `file:` | `auth.from must be env:VAR or file:PATH` | `afm:contracts/secrets/secrets.go` `ResolveRef` |
| EXE-E06 | Bridge symlink fails | run fails `engine command bridge: …` | `engine:internal/engine/environment.go` `installCommandBridge` |
| EXE-E07 | Browser/mesh descriptor write fails | turn fails `browser mcp setup:` / `mesh descriptor setup:` | `engine:internal/executor/executor.go` `runJob` |

## Coverage gaps and discrepancies
- **EXE-G01** (ambiguous): `Executor.RunJSONQuery` puts the prompt in argv, keeps transport secrets, sets no
  `Dir`; no production caller (only the `Runner` interface). Confirm it stays unused.
- **EXE-G03** (code≠doc): afm `AGENTS.md` names `pkg/secrets`; the resolver lives in `afm:contracts/secrets`
  plus `afm:contracts/docker` `LoadSecretLayers`.
- **EXE-G04** (untested): EXE-S05 ordering — the test writes only a project file (no `HOME` redirect); global
  layer and process-env fallback order are code-only.
- **EXE-G05** (untested): process-group behaviour is tested on Unix only; `executor_windows.go` has no tests.
- **EXE-G06** (doc-only): "no secrets in logs" for author runs relies on not printing env; only helper and
  mesh output is redacted. `--debug` prompt logs are unredacted.
- **EXE-G07** (untested): EXE-E06 and EXE-E07 error paths are code-only.
