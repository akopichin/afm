# Lifecycle hooks: declaration, environment and secrets

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Hook declaration layers, ID override, validation and event selection.
- Hook process environment: minimal base, `inherit_env`, `AFM_*` variables, path
  semantics across process/container boundaries, CWD.
- Secret resolution (`ResolveLifecycleEnv`), Docker transport, redaction.
- Out of scope: event catalog, payload and delivery →
  [../lifecycle-hooks/spec.md](../lifecycle-hooks/spec.md).
  IDs: this file uses R30–R49, S30–S49, E10–E19, G10–G19.

## Terms
- **Layer**: global config < project config < flow root < stage.
- **Secret ref**: `env:NAME` or `file:PATH` value of a hook `env` entry.
- **Transport var**: transient `HookSecretTransportPrefix`-named variable that
  carries a resolved secret from host afm into its Docker re-exec.

## Requirements

### HOOK-R30 Layers combine; same ID replaces in place
The effective list is the union of global, project, flow and stage hooks. A hook
whose ID reappears in a more specific layer replaces the earlier one at the
earlier position. The same ID in two different stages is a flow error.

Evidence: afm:contracts/lifecyclehooks/combine_test.go `TestCombine_LayersAppend`, `TestCombine_LaterLayerOverridesByID`, `TestCombine_StageScopeOverridesGlobal`; afm:contracts/config/config_test.go `TestHooksMerge_KeyedByID`; afm:contracts/flow/flow_test.go `TestParseHooks_Errors`; afm:cmd/afm/run_lifecycle.go `combineRunHooks`

#### HOOK-S30 Flow overrides global
- GIVEN global hook `notify` and flow hook `notify` with a different command
- WHEN hooks are combined
- THEN one `notify` hook with the flow command remains, at the global position

### HOOK-R31 Layer validation
Within a layer each hook needs a non-empty ID that is a single safe path
component and unique, a command, and `events` = `all` or a non-empty list of
known events. Unknown `events`/`skip_events` names, negative `retries`/`timeout`,
and flow-level events in a stage hook are rejected.

Evidence: afm:contracts/lifecyclehooks/validate_test.go `TestValidateLayer`; afm:contracts/lifecyclehooks/types_test.go `TestEventSelectorUnmarshal`, `TestHookUnmarshal`; afm:contracts/flow/flow_test.go `TestParseHooks_Errors`; afm:contracts/config/config_test.go `TestHooksMerge_InvalidEventFails`

#### HOOK-S31 Typo in event name
- GIVEN `events: [flow_finsihed]`
- WHEN the config or flow is loaded
- THEN loading fails with "unknown event"

### HOOK-R32 Event selection is events minus skip_events
A hook matches an event when it is in `events` (or `events: all`) and not in
`skip_events`, and, for a stage hook, the event belongs to its stage.

Evidence: afm:contracts/lifecyclehooks/validate_test.go `TestMatches`, `TestRegisteredHookScope`; afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_FlowRepeatedSelector`

### HOOK-R33 Env names and refs are validated
`env` keys must match `[A-Za-z_][A-Za-z0-9_]*`, must not start with `AFM_`
(case-insensitive), and must not collide after uppercasing. Values must be
`env:NAME` or `file:PATH`; literals are rejected.

Evidence: afm:contracts/lifecyclehooks/validate_test.go `TestValidateLayer_HookEnv`, `TestValidateLayer_HookEnvCaseCollision`; afm:contracts/flow/flow_test.go `TestParseHooks_EnvAndInheritEnv`

### HOOK-R34 Secrets resolve fail-fast before the run starts
`ResolveLifecycleEnv` resolves all refs during `Open`, before the dispatcher is
built and before `flow_started`. `env:NAME` uses project `secrets.env` > global
`secrets.env` > process env; `file:` reads a trimmed file. A missing variable,
missing file or empty value fails the run, naming hook and variable but not the
value. Hooks without `env` never read secrets files.

Evidence: afm:contracts/lifecyclehooks/env_test.go `TestResolveHookEnv`, `TestResolveHookEnv_MissingIsError`; engine:internal/engine/agent_environment_test.go `TestLoadHookSecretLayers_ProjectOverridesGlobal`; engine:internal/engine/hooks_test.go `TestRunLifecycleMissingTransportFailsBeforeDispatcher`, `TestRunLifecycleWithoutEnvSkipsSecretsFile`; engine:internal/engine/hooks.go `ResolveLifecycleEnv`

#### HOOK-S32 Missing secret
- GIVEN a hook with `env: {TOKEN: "env:NOPE"}` and `NOPE` defined nowhere
- WHEN the run opens
- THEN it fails before any stage starts and the error names the hook and `TOKEN`

### HOOK-R35 Docker transport for hook secrets
Host afm resolves secrets before `docker run` and passes them as bare `-e` transport
vars indexed by (combined hook index, sorted var index); values never appear in
argv. Inside the re-exec, values are read from transport, then all transport
vars are unset before any child starts. A missing or empty transport var fails.

Evidence: afm:contracts/docker/launcher_test.go `TestReExec_HookSecretTransport`, `TestReExec_HookNoEnv_NoTransport`, `TestReExec_HookSecretMissingFailFast`; afm:cmd/afm/run_preparation_test.go `TestRunLifecycleTransportUsesCombinedHookOrder`; afm:contracts/lifecyclehooks/env_test.go `TestResolveHookEnvFromTransport_EmptyIsError`, `TestUnsetTransportVars_RemovesOnlyHookSecretPrefix`, `TestTransportName_InjectiveByIndex`

#### HOOK-S33 Container child cannot see transport
- GIVEN a re-execed afm with hook transport vars set
- WHEN `ResolveLifecycleEnv` returns
- THEN hooks hold the values and no transport var remains in the process env

### HOOK-R36 Repeat mode re-resolves natively, not in Docker
Native repeat re-resolves refs before each new run. In Docker repeat mode the
transport is read once; changing identity, order or refs of env hooks fails
before the next cycle's run dir with a "restart `afm run`" error.

Evidence: afm:cmd/afm/run_repeat_test.go `TestRepeatLoop_ReloadAppliesNewHook`, `TestCarryHookEnv`, `TestRepeatLoop_ContainerHookEnvChangeFailsBeforeNewRun`

### HOOK-R37 Minimal environment by default
Without `inherit_env` the hook gets only PATH, HOME, TMPDIR, locale, proxy and
cert variables, then `AFM_*` event vars, then resolved env (last wins). With
`inherit_env: true` it gets the full process env minus hook/agent/system-prompt
transport, browser transport and inherited `AFM_FLOW_CYCLE`.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestBuildEnv_Modes`, `TestBuildEnv_InheritEnv_StripsBrowserTransport`, `TestBuildEnv_FlowCycle`; afm:contracts/lifecyclehooks/runner.go `minimalBaseEnv`, `stripTransportVars`

### HOOK-R38 AFM_* event variables
Hooks receive `AFM_HOOK_EVENT`, `AFM_HOOK_EVENT_ID`, `AFM_FLOW_NAME`,
`AFM_RUN_ID`, `AFM_RUN_DIR`, `AFM_ROOT_DIR`, `AFM_STAGE_ID/NAME/FROM/TO` (empty
for flow events) and `AFM_FLOW_CYCLE` only in repeat mode.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestRunCommand_Success_StdinAndEnv`, `TestAfmVars_FlowCycle`

### HOOK-R39 Env paths are absolute at the process boundary
`AFM_RUN_DIR` and `AFM_ROOT_DIR` are absolutized when set, even if afm used
relative paths; an unset root stays empty.

Evidence: afm:contracts/lifecyclehooks/env_abs_test.go `TestHookEnv_RunDirAndRootDirAreAbsolute`, `TestHookEnv_EmptyRootDirStaysEmpty`; afm:contracts/lifecyclehooks/runner.go `absEnvPath`

#### HOOK-S34 Relative run dir
- GIVEN afm started with `--dir .` so the run dir is relative
- WHEN a hook runs
- THEN `AFM_RUN_DIR` is an absolute path

### HOOK-R40 Command CWD and no interpolation
The command runs as `sh -c <command>` in the effective agent root (`flow.root_dir`
/ worktree, else the process CWD), with the run's child-env wrapper applied. Event
values are passed only via stdin and env, never interpolated into the command.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestRunCommand_Success_StdinAndEnv`; afm:contracts/lifecyclehooks/runner.go `execOne`; engine:internal/engine/agent_environment.go `lifecycleRootDir`

### HOOK-R41 Secrets are redacted in logs and errors
Resolved values are replaced with `[REDACTED]` in the streamed hook log
(including headers) and in the error returned to `OnError`/notices.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestRunCommand_RedactsSecretInLog`, `TestRunCommand_HeaderRoutedThroughRedactor`; afm:contracts/lifecyclehooks/env_test.go `TestSecretValues_FeedsRedactor`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HOOK-E10 | Unsafe/duplicate ID, unknown event, flow event in stage hook | config/flow load error | afm:contracts/lifecyclehooks/validate_test.go `TestValidateLayer` |
| HOOK-E11 | Same hook ID in two stages | flow validation error | afm:contracts/flow/flow_test.go `TestParseHooks_Errors` ("dup id across stages") |
| HOOK-E12 | Bad env name, `AFM_` prefix, case collision, literal source | load error | afm:contracts/lifecyclehooks/validate_test.go `TestValidateLayer_HookEnv` |
| HOOK-E13 | Unresolvable secret / missing transport var | run fails before `flow_started` | engine:internal/engine/hooks_test.go `TestRunLifecycleMissingTransportFailsBeforeDispatcher` |
| HOOK-E14 | Docker repeat changes env hooks | cycle error "restart `afm run`" | afm:cmd/afm/run_repeat_test.go `TestRepeatLoop_ContainerHookEnvChangeFailsBeforeNewRun` |

## Coverage gaps and discrepancies
- **HOOK-G10** (code≠doc): `contracts/lifecyclehooks/AGENTS.md` says
  "Payload/env paths are absolute", but `absEnvPath` only absolutizes env vars;
  the JSON `flow.run_dir`/`root_dir` are passed as given (code comment says so).
- **HOOK-G11** (untested): hook CWD equals the effective root (worktree) is not
  asserted end-to-end; `lifecycleRootDir` fallback to process CWD is untested.
