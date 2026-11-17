# Execution environment: managed browser and read-only isolation

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The managed Chrome DevTools capability (`afm_chrome`): enablement, host manager, per-stage injection,
  token transport, native vs Docker app URL.
- Which read-only guarantee backs verify and helper agents (OS sandbox vs container trust vs prompt only).
- Siblings: processes/env/secrets — [../execution-environment/spec.md](../execution-environment/spec.md);
  CWD/worktrees/prompts — [../execution-environment-workspace/spec.md](../execution-environment-workspace/spec.md);
  Docker re-exec — [../execution-environment-docker/spec.md](../execution-environment-docker/spec.md).
- Out of scope: verify verdict protocol (`../verify/spec.md`), helper admission flow (`../helpers-lifecycle/spec.md`).

## Terms
- **Descriptor**: host-side `browser.Descriptor` (endpoint, token, app URL, epoch, docker flag) for a run.
- **Invocation descriptor**: per-turn 0600 file with the token, referenced by `AFM_BROWSER_DESCRIPTOR`.
- **Supported command**: `claude`, the codex adapter, or a recipe of type `claude`/`codex`.

## Requirements

### EXE-R50 Enablement is tri-state and degrades in auto
`browser.enabled` is auto (unset), on or off; `AFM_BROWSER` overrides it. Off starts nothing. In auto a
failed preflight, launch or Docker image protocol probe disables the browser with a warning; with on it
fails the run. Docker mode compares the image's browser protocol version with the host's before starting.
Each start mints a fresh random epoch, also for a resumed run.

Evidence: `afm:cmd/afm/run_browser_test.go` `TestStartBrowser_OffWhenDisabled`, `TestProbeImageBrowserProtocol_IncompatibleSeverity`, `TestStartBrowser_DockerFreshAndResumedGetFreshEpoch`, `TestStartBrowser_NativeOwnsAndCleansUp`; `afm:contracts/config/browser.go` `EffectiveEnable`; `afm:cmd/afm/run_browser.go` `startBrowser`

#### EXE-S70 Image too old in auto mode
- GIVEN `browser.enabled` unset and an image whose protocol version differs from the host
- WHEN `afm run` re-execs into Docker
- THEN a `browser disabled` warning is printed and the run continues without `afm_chrome`

### EXE-R51 Only author stages get the browser, by one policy
`browserForStage` gives the capability to interactive, interactive-fallback and non-interactive author
runners when a descriptor exists, the stage has not set `browser: false`, and the command is supported. The
`<browser_tools>` prompt block uses the same policy. Verify, helper, JSON-fix and memory-pipeline runs never
get it, whatever the stage says.

Evidence: `engine:internal/orchestrator/browser_policy_test.go` `TestBrowserForStage_Policy`, `TestRunnerFor_InteractiveAndFallbackGetBrowser`, `TestRunnerForVerify_NoBrowser`; `engine:internal/executor/hardening_test.go` `TestHelperEnv_DropsBrowserTransport`; `engine:internal/prompts/builder_test.go` `TestBuild_BrowserRuntimeBlockPresent`, `TestBuild_NoBrowserBlockWhenNil`; `afm:contracts/browser/AGENTS.md`

#### EXE-S71 Opt-out stage
- GIVEN a descriptor and a `claude` stage with `browser: false`
- WHEN its runner is built
- THEN it has no browser MCP and its prompt has no `<browser_tools>` block

### EXE-R52 Per-stage session and filesystem roots
Each stage connects to its own endpoint (keyed by process epoch and stage ID), so parallel stages use
separate Chrome profiles and a retry reconnects to its own session. MCP filesystem roots are the absolute
project root (CWD when `root_dir` is empty) and the stage directory, never the whole filesystem.

Evidence: `afm:contracts/browser/manager_test.go` `TestManager_TwoStages_SeparateProfiles`, `TestManager_SameStageReconnect_ReusesSession`; `engine:internal/orchestrator/browser_policy_test.go` `TestBrowserForStage_RootsCoverProjectAndStageDir`; `afm:contracts/browser/mcp_test.go` `TestWriteMCPConfig_CarriesAllRootsNeverWholeFS`

### EXE-R53 Token stays in 0600 files
For an author turn the executor writes a 0600 invocation descriptor in a fresh temp dir, removed after the
turn, and exposes only `AFM_BROWSER_APP_URL`, `AFM_BROWSER_APP_PORT`, `AFM_BROWSER_DESCRIPTOR` and
`AFM_BROWSER_LAUNCHER`. `claude` gets `--mcp-config <file>`; the codex adapter gets no MCP config and reads the
descriptor. In Docker the token arrives as bare env, is read into the descriptor once, and all
`AFM_BROWSER_*` are removed from afm's environment. Every non-author spawn path strips `AFM_BROWSER_*`.

Evidence: `engine:internal/executor/browser_test.go` `TestSetupBrowserMCP_Claude_ArgsDescriptorEnv`, `TestSetupBrowserMCP_Codex_NoMCPConfig`, `TestJSONQueryEnv_StripsBrowserTransport`; `afm:cmd/afm/run_browser_test.go` `TestDescriptorFromEnv_OnlyInContainer`; `afm:contracts/browser/env_test.go` `TestUnsetTransportVars`; `engine:internal/orchestrator/browser_policy_test.go` `TestSanitizedShellVerifyEnv_StripsBrowserTransport`

### EXE-R54 Host manager launches Chrome lazily and authenticates
Chrome runs on the host via remote-debugging-pipe and starts only on a real tool call; a WebSocket without
the right token gets 401 and starts nothing. Concurrent first connects launch once; launch errors are not
cached; Close waits for an in-flight launch. CDP request IDs are remapped per connection; unknown responses
are dropped. Profiles left by a dead owner are swept on the next start.

Evidence: `afm:contracts/browser/manager_test.go` `TestManager_WSWithoutTokenIs401_NoChrome`, `TestManager_ConcurrentFirstConnect_SingleLaunch`, `TestManager_LaunchErrorNotCached`, `TestManager_CloseDuringLaunch`, `TestManager_SweepsStaleOwnedProfiles`; `afm:contracts/browser/proxy_test.go` `TestProxy_RemapsIDsAcrossReconnect`, `TestProxy_UnknownIDResponseIsDropped`

### EXE-R55 App URL differs between native and Docker
Native: endpoint `127.0.0.1:<manager port>`, app URL `http://127.0.0.1:<browser.app_port>`. Docker: endpoint
`host.docker.internal:<port>`, a free host loopback port published as `127.0.0.1:<host>:<app_port>`, and the
agent is told to bind `0.0.0.0:$AFM_BROWSER_APP_PORT` and open `$AFM_BROWSER_APP_URL`.

Evidence: `afm:contracts/docker/launcher_test.go` `TestReExec_BrowserTokenNotInArgv`; `afm:cmd/afm/run_browser.go` `browserEndpointAndAppURL`; `engine:internal/prompts/builder.go` `Build`

### EXE-R56 Read-only guarantee depends on adapter and place
A helper profile is admitted on the host only for the codex read-only adapter (OS sandbox: `CODEX_VERIFY=1`
→ `codex exec -s read-only`, never bypassed), recorded as enforcement `os`. In any container (`InContainer`,
not only afm's own) every command is admitted with enforcement `none`: the container is the boundary, and
the codex adapter under `AFM_IN_DOCKER=1` runs `danger-full-access`. A read-only prompt alone never makes
bare `claude` or other commands read-only.

Evidence: `afm:contracts/config/verify_resolve_test.go` `TestHelperCapability_HostNonCodexIsUnavailable`, `TestHelperCapability_HostCodexIsAvailable`, `TestHelperCapability_ContainerAllowsAnyCommandWithoutOsGuarantee`, `TestHelperCapability_PredicateUsesInContainerNotReExec`; `engine:internal/executor/codex_verify_test.go` `TestCodexAsClaude_VerifyMode_ReadOnlyNoBypass`, `TestCodexAsClaude_VerifyMode_DockerFullAccess`

#### EXE-S72 Host claude helper refused
- GIVEN a helper profile with command `claude` on the host
- WHEN admission checks it
- THEN it is unavailable with "no verifiable read-only policy …"; nothing falls back to an unrestricted run

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| EXE-E30 | `enabled: true` and preflight/launch fails | run fails with the browser error | `afm:cmd/afm/run_browser.go` `degradeOrError` |
| EXE-E31 | Image protocol mismatch | on: `browser transport protocol mismatch …`; auto: warning, browser off | `afm:cmd/afm/run_browser_test.go` `TestProbeImageBrowserProtocol_IncompatibleSeverity` |
| EXE-E32 | WS without/with wrong token | HTTP 401, Chrome not started | `afm:contracts/browser/manager_test.go` `TestManager_WSWrongTokenIs401_NoChrome` |
| EXE-E33 | Host helper command without OS sandbox | profile unavailable, task never starts | `afm:contracts/config/verify_resolve_test.go` `TestHelperCapability_NoSilentFallback` |

## Coverage gaps and discrepancies
- **EXE-G30** (untested): the Docker `<browser_tools>` variant (bind `0.0.0.0`) has no prompt test; only the
  native block is tested.
- **EXE-G31** (doc-only): "JSON-fix and memory-pipeline runs never get the browser" (`afm:docs/browser.md`) —
  they build configs without `BrowserMCP`; no dedicated test.
- **EXE-G32** (doc-only): a same-user process in the container can read another stage's descriptor or
  PID 1's environ; isolation is per container by design (`afm:docs/browser.md`).
- **EXE-G33** (untested): EXE-E30 hard failure with `enabled: true` is code-only.
