# HTTP API server and launch modes

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- How `afm run` exposes a run over HTTP: `--mode core|dashboard`, port rules, final hold, `/api/runtime`.
- Local-access guards of the API (listen address, `LocalGuard`, Origin, bearer tokens).
- `StageConfig` vs `StageView`, button labels resolved to prompts on the server.
- Out of scope: durable intent, run-scoped work, live updates and the SDK contract
  (`../http-api-live/spec.md`); file browser and verify-report reads (`../http-api-files/spec.md`); `/mcp` and `afm serve` (`../mcp-serve/spec.md`,
  `../mcp-serve-host/spec.md`).

## Terms
- **core / dashboard**: launch modes. Both serve the same `/api/*` and `/ws`; dashboard also mounts the Web UI.
- **ready**: `engine.Run.Ready` — transports bound and `Start` passed; not "port open".
- **final hold**: the time after the workflow result during which the API stays up.
- **StageConfig / StageView**: static stage settings vs runtime projection returned by `/api/status`.

## Requirements

### API-R01 Launch mode is parsed and validated before a run exists
`--mode` accepts `dashboard` (also empty) and `core`; any other value is refused. `core` with a
non-positive port (`--port 0` / `server.port: 0`) is refused. Both refusals happen before a run
directory, worktree or container is created.

Evidence: `afm:cmd/afm/run_mode_test.go` `TestParseLaunchMode`, `TestLaunchModeValidate`,
`TestRunMain_RefusedModeCreatesNothing`; `afm:cmd/afm/run_mode.go` `parseLaunchMode`, `launchMode.validate`; `afm:docs/core-mode.md`

#### API-S01 core without a port
- GIVEN `afm run --mode core --port 0 flow.yaml`
- WHEN the command starts
- THEN it exits with an error naming `--mode core needs the HTTP API` and `.afm/runs` stays empty

### API-R02 core serves the API without UI; the server package never imports the UI
The API server takes the UI as an optional `http.Handler` from the launcher. Without it `GET /` and
other UI paths answer 404 while `/api/*`, `/ws` and `/mcp` work. Core prints `api: http://localhost:<port>`
and never opens a browser tab.

Evidence: `afm:pkg/server/server_test.go` `TestServer_WithoutUIServesNoWebPaths`, `TestServer_UIHandlerServesRoot`;
`afm:pkg/server/boundary_test.go` `TestBoundary_ServerDoesNotImportWeb`; `afm:cmd/afm/run_dashboard_test.go`
`TestExecuteFlow_CoreServesAPIWithoutUI`, `TestStartDashboard_ServesWebUI`; `afm:sdk/integration_core_test.go`
`TestIntegration_CoreStartServesAPIOnly`, `TestIntegration_CoreDashboardKeepsUI`

#### API-S02 UI path in core
- GIVEN a core run on port P
- WHEN a client requests `GET http://127.0.0.1:P/`
- THEN the answer is 404 and `GET /api/status` answers 200

### API-R03 Both modes drive the same engine transitions and capabilities
Questions, manual approval, retry, pause/continue, graph commit and restart resume behave the same over
the API in core and dashboard; the capability set reported in status is identical.

Evidence: `afm:cmd/afm/core_mode_integration_test.go` `TestCoreMode_SameTransitionsInBothModes`,
`TestCoreMode_SameCapabilitiesInBothModes`, `TestCoreMode_QuestionAndManualApprovalOverHTTP`,
`TestCoreMode_RetryFailedStage`, `TestCoreMode_PauseAndContinue`, `TestCoreMode_GraphCommitAddsAndRunsStage`,
`TestCoreMode_RestartKeepsOpenQuestion`, `TestCoreMode_RestartKeepsCommittedDynamicStage`

#### API-S03 restart keeps an open question
- GIVEN a core run whose stage waits for a user answer
- WHEN the process is interrupted and `afm run --mode core` is started again on the flow
- THEN the resumed run exposes the same open question over the API

### API-R04 API start failure stops the run before agents start
If the listen port is busy, the process exits non-zero with an error naming `start API server`, no agent
starts, and the run lock is released.

Evidence: `afm:cmd/afm/core_mode_integration_test.go` `TestCoreMode_BusyPortFailsBeforeAgentsStart`;
`afm:cmd/afm/run_dashboard_test.go` `TestStartServer_CoreStartFailureNamesTheAPI`

#### API-S04 busy port
- GIVEN port P already bound by another listener
- WHEN `afm run --mode core --port P` starts
- THEN it exits non-zero, no agent ran, and the created run directory is unlocked

### API-R05 Final hold differs by mode
After a result, core holds the API until the run context is cancelled (SIGINT) and then exits 0;
connecting/disconnecting WebSocket clients does not end the hold. Dashboard holds at least a minimum
grace and up to a maximum while UI clients are connected. Between repeat cycles both hold a short fixed
grace on the same port; the last cycle of core is held until interrupt. `--exit-on-fail` exits 1 without
a hold. An interrupt between cycles exits 130.

Evidence: `afm:cmd/afm/run_core_lifetime_test.go` `TestExecuteFlow_CoreHoldsUntilContextCancel`,
`TestExecuteFlow_CoreExitOnFailDoesNotHold`, `TestCoreChild_ExitOnFailExitsOneWithoutHold`,
`TestCoreChild_InterruptInFinalHoldExitsZero`, `TestCoreChild_RepeatTwoCyclesThenFinalHold`,
`TestCoreChild_InterruptBetweenCyclesExits130`, `TestCoreChild_BrokenFlowAtLastCycleIsAnErrorAfterTheHold`;
`afm:cmd/afm/run_dashboard_drain_test.go` `TestWaitForDashboardDrain_*`; `afm:cmd/afm/run.go` `holdAfterResult`

#### API-S05 core holds the final state
- GIVEN a core run whose workflow finished
- WHEN a WebSocket client connects and disconnects
- THEN `GET /api/runtime` still answers 200, stdout contains `api: holding the final state until interrupted`, and only SIGINT ends the process with code 0

#### API-S06 interrupt drains an active assistant turn
- GIVEN a core run in its final hold with an active Assistant turn
- WHEN SIGINT arrives
- THEN the turn is drained before the process exits

Evidence for S06: `afm:cmd/afm/run_core_lifetime_test.go` `TestCoreChild_InterruptDrainsActiveAssistantTurn`

### API-R06 `/api/runtime` reports mode, run identity and readiness
`GET /api/runtime` answers `Cache-Control: no-store` with exactly `protocol_version` (1), `mode`, `run_id`
(always present, may be empty) and `ready`, plus `instance_id` when set. A zero `RuntimeInfo` reads as
`dashboard`, not ready. `run_id` equals the status `run_id`. Non-GET methods get 405. No paths, tokens or PID.

Evidence: `afm:pkg/server/runtime_test.go` `TestRuntime_ExactFieldSet`, `TestRuntime_RunIDPresentWhenEmpty`,
`TestRuntime_ModeDefaultsToDashboard`, `TestRuntime_Ready`, `TestRuntime_PostRejected`,
`TestRuntime_RunIDMatchesStatus`, `TestRuntime_InstanceID`; `afm:pkg/server/runtime.go`

### API-R07 Readiness comes from the engine, not from the open port
`ready` is true only after the engine run of this process started (for a resume, after the new start is
saved) and until shutdown begins; a resumed run never reports the previous result while `ready` is true.

Evidence: `afm:sdk/result_test.go` `TestWaitResult_IgnoresStaleResultUntilReady`; `afm:sdk/ready_test.go`
`TestStartCore_Ready`; `afm:pkg/server/AGENTS.md`; `afm:docs/core-mode.md`

#### API-S07 server listens before ready
- GIVEN a core process whose listener is bound but `engine.Run.Start` has not returned
- WHEN a client reads `/api/runtime`
- THEN `ready` is `false`

### API-R08 Listen address and Docker re-exec
Natively the API binds `127.0.0.1:<port>`; inside a container it binds `:<port>` and the host publishes
`127.0.0.1:<port>:<port>`. The Docker re-exec carries `--mode` and `--port` to the child.

Evidence: `afm:pkg/server/side_agent_test.go` `TestDashboard_ListenAddrMatrix`; `afm:cmd/afm/run_core_lifetime_test.go`
`TestDockerReExec_CarriesModeAndPort`; `afm:pkg/server/server.go` `dashboardListenAddr`

### API-R09 Guarded routes admit only local requests
`/mcp` and `/api/stages/{id}/approval` go through `LocalGuard`: methods GET/POST/DELETE only, loopback `Host`
(`127.0.0.1`, `localhost`, `[::1]`), absent or same-origin loopback `Origin`, body ≤ 1 MiB. The bind address
is not trusted. Collaboration/mesh/budget/graph/side-agent mutations require a present same-host `Origin`;
owner-wait routes require the owner bearer token and agent-mesh routes the agent bearer; these refusals are
identical in core and dashboard.

Evidence: `afm:pkg/server/local_guard_test.go` `TestLocalGuardMatrix`, `TestLocalGuardIgnoresBindAddress`,
`TestLocalGuardLimitsBody`; `afm:pkg/server/approval_test.go` `TestApprovalGuardedByLocalHost`;
`afm:cmd/afm/core_mode_integration_test.go` `TestCoreMode_TransportRefusals`; `afm:pkg/server/side_agent.go` `sameOriginOK`

#### API-S08 foreign Origin on a mesh mutation
- GIVEN a core run
- WHEN `POST /api/mesh/board` carries `Origin: http://evil.example`
- THEN the answer is 403 `forbidden_origin`

### API-R10 StageConfig and StageView are separate; display order is a stable topological order
Status carries `StageView`s built from `StageConfig` plus runtime state. Views are ordered by dependencies with
the original order as tie-breaker (unknown deps ignored) without changing `RunState.StageOrder`; dynamic stages
take part. `PausedFrom` is shown only while paused; plan is hidden for pending and script stages; the dialog panel
is advertised only with real history or awaiting input. `Buttons` keep YAML order.

Evidence: `afm:pkg/server/stageview_test.go` `TestBuildStageViews_OrdersAndComputesCapabilities`,
`TestTopoOrder_*`, `TestBuildStageViews_HidesPlanForPendingAndScriptStages`, `TestBuildStageViews_ShowDialogOnlyWithContent`,
`TestBuildStageViews_IsScriptAndPausedFrom`, `TestBuildStageViews_IncludesButtons`, `TestBuildStageViews_NameFromRuntimeConfig`;
`afm:pkg/server/stageview.go`

### API-R11 Buttons are sent by label; the prompt is resolved server-side
`POST /api/stages/{id}/button` takes only `{"name": label}`; the label must be one declared for the stage
(case-sensitive) and the stage must be `awaiting_approval` or `running` and not a script. The engine resolves
the prompt from the runtime registry and delivers it via Revise; if Revise is not applied the API answers 409.

Evidence: `afm:pkg/server/command_contract_test.go` `TestCommandContract_Button`; `engine:internal/orchestrator/button_test.go`
`TestButton_DeliversPromptViaRevise`, `TestButton_UnknownNameIsNoOp`; `engine:internal/orchestrator/control_api.go` `Button`

#### API-S09 unknown label
- GIVEN a running stage with buttons `Fix`, `Polish`
- WHEN the client posts `{"name":"fix"}`
- THEN the answer is 400 `unknown button` and nothing reaches the agent

### API-R12 Pre-note only on pending non-script stages
`POST /api/stages/{id}/note` writes `prenote.md` for a pending stage; whitespace clears it idempotently; a
non-pending stage gets 400.

Evidence: `afm:pkg/server/command_contract_test.go` `TestCommandContract_PreNote`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| API-E01 | `--mode` other than dashboard/core | error `--mode "x": want dashboard or core`, nothing created | `TestParseLaunchMode` |
| API-E02 | `--mode core` with port ≤ 0 | startup error, no run | `TestRunMain_RefusedModeCreatesNothing` |
| API-E03 | port busy | non-zero exit, `start API server` | `TestCoreMode_BusyPortFailsBeforeAgentsStart` |
| API-E04 | non-loopback Host on guarded route | 403 `forbidden_host` | `TestLocalGuardMatrix` |
| API-E05 | foreign Origin | 403 `forbidden_origin` | `TestLocalGuardMatrix`, `TestCoreMode_TransportRefusals` |
| API-E06 | guarded body > 1 MiB / bad method | 413 `body_too_large` / 405 `method_not_allowed` | `TestLocalGuardLimitsBody` |
| API-E07 | missing/wrong bearer on owner/agent routes | 401 (`WWW-Authenticate: Bearer` for owner) | `TestCoreMode_TransportRefusals` |
| API-E08 | button: wrong status / script / unknown label | 400 with text reason | `TestCommandContract_Button` |
| API-E09 | stage command after concurrent Pause won | 409 `stage no longer accepts this action` | `actionErrorCases` |
| API-E10 | stage command while flow review-paused | 409 `{"error":"flow_paused"}` | `actionErrorCases` |
| API-E11 | `POST /api/runtime` | 405, `no-store` | `TestRuntime_PostRejected` |

## Coverage gaps and discrepancies
- **API-G01** (untested): core stops cleanly only on SIGINT; `afm run` subscribes only to `os.Interrupt`, so
  SIGTERM kills without drain (`afm:cmd/afm/run.go` `signal.NotifyContext`). Doc-stated, no test.
- **API-G02** (ambiguous): `/ws` upgrader has `CheckOrigin` returning true and stage routes
  (`/approve`, `/retry`, `/button`, …) have no Origin check; docs say "mutating browser requests need a
  matching Origin". Only `LocalGuard`/`sameOriginOK` routes enforce it.
- **API-G03** (code≠doc): `docs/core-mode.md` says a refused port "creates no run"; for a busy port a run
  directory is created (lock released) — `TestCoreMode_BusyPortFailsBeforeAgentsStart`.
- **API-G04** (doc-only): `docs/core-mode.md` runtime example and `pkg/server/AGENTS.md` omit `instance_id`,
  which code emits when set (`TestRuntime_InstanceID`).
