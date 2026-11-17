# HTTP API: commands, live updates and SDK contract

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- What the engine guarantees to HTTP commands: durable intent before the response, CAS-gated
  work, work that outlives the request.
- Conditional approval over REST (`/api/stages/{id}/approval`).
- Live updates over `/ws` and history replay (`/api/events`) for reconnecting clients.
- The Go SDK (`afm:sdk`, `docs/sdk.md`) as a client of this contract.
- Out of scope: modes, guards, stage views (`../http-api/spec.md`); file browser
  (`../http-api-files/spec.md`); MCP (`../mcp-serve/spec.md`). Part of the `API` family.

## Terms
- **intent**: a user decision (answer, approve, revise, button…) that must survive a crash once acknowledged.
- **run context**: context owned by the engine run; cancelled only on run shutdown.
- **notices**: non-FSM UI notifications persisted in `notices.jsonl` and published live.

## Requirements

### API-R15 Intent is durable before the API answers
A dialog answer is written as `answer.json` before the engine is notified; the response is sent after.
If the engine refuses because the flow is review-paused, answer, dialog history and notice are rolled back
and the API answers 409 `flow_paused`. On any other notify error the answer stays durable and the API answers
500. Interactive stages accept answers only for the current (oldest open) question.

Evidence: `afm:pkg/server/command_contract_test.go` `TestCommandContract_DialogAnswer_Success`,
`TestCommandContract_DialogAnswer_NotifyFailures`, `TestCommandContract_DialogAnswer_InteractiveOrdering`,
`TestCommandContract_DialogAnswer_Duplicate`; `afm:AGENTS.md` (State persistence)

#### API-S12 generic notify failure
- GIVEN a stage awaiting user input with open question `q1`
- WHEN the client answers and the engine notification fails with a generic error
- THEN the API answers 500 and `answer.json` for `q1` remains on disk

#### API-S13 out-of-order interactive answer
- GIVEN an interactive stage with open questions `q1` then `q2`
- WHEN the client answers `q2`
- THEN the API answers 409 `answer_out_of_order` and nothing is written

### API-R16 Only the CAS winner starts work; a lost race is a visible no-op
Stage commands go through engine CAS. When the stage left the decided status (e.g. a concurrent Pause won),
the engine returns `ErrNotApplied` and the API answers 409; nothing is executed twice.

Evidence: `afm:pkg/server/command_contract_test.go` `actionErrorCases`, `TestCommandContract_StageCommands`;
`engine:internal/orchestrator/control_api.go` `ErrNotApplied`

### API-R17 Conditional approval is strict and revision-fenced
`GET /api/stages/{id}/approval` returns the plan snapshot with `run_id` and `revision`. `POST` requires exactly
`run_id` and non-empty `expected_revision` (unknown fields, trailing data, stage id in body → 400, engine not called)
and never falls back to legacy `/approve`. Stale revision, other run or unavailable approval → 409 with
`approval_stale` / `approval_run_changed` / `approval_unavailable`; two approvals of one revision apply once.
The route is off without stage actions; legacy `/approve` is unchanged.

Evidence: `afm:pkg/server/approval_test.go` `TestApprovalRejectsMalformedBody`, `TestApprovalConflictsReturn409WithCode`,
`TestApprovalApproveTwiceIsStale`, `TestApprovalApproveNeedsTheShownCurrentPlan`, `TestApprovalApproveKeepsEngineFenceAfterPreflight`,
`TestApprovalLegacyApproveUnchanged`, `TestApprovalRouteIsOffWithoutActions`, `TestApprovalContractGolden`;
`afm:cmd/afm/mcp_integration_test.go` `TestMCPIntegrationConcurrentApproveAppliesOnce`

#### API-S14 plan revised after read
- GIVEN a client read approval revision `rev1`
- WHEN the plan is revised to `rev2` and the client posts `expected_revision: rev1`
- THEN the API answers 409 `approval_stale` and the stage keeps waiting

### API-R18 HTTP-started work runs under the run context
Agents spawned by approve/retry/revise/button and work started by mesh sends run under the engine run context
(or a non-cancelling copy before the run started), never under `r.Context()`; the handler returning does not
stop them.

Evidence: `engine:internal/orchestrator/runctx_test.go` `TestApprove_SpawnsAgentUnderRunCtx_NotRequestCtx`;
`engine:internal/orchestrator/agentcomm_attach_test.go` `TestSend_StartedWorkSurvivesTheRequestContext`;
`engine:internal/orchestrator/paths.go` `runContext`

#### API-S15 request cancelled after approve
- GIVEN a stage awaiting approval
- WHEN `POST /approve` succeeds and the request context is cancelled
- THEN the spawned agent keeps running until it finishes or the run shuts down

### API-R19 `/ws` streams live events with keepalive
`/ws` sends bus events to every connected client, a `{"type":"heartbeat"}` message every 30 s, and closes clients
silent beyond the 60 s pong wait. The server tracks connected clients (used by the dashboard drain) and closes them on
`Shutdown`.

Evidence: `afm:pkg/server/websocket_test.go` `TestWebSocket_ReceivesEvents`; `afm:pkg/server/websocket_keepalive_test.go`
`TestWebSocket_SendsHeartbeat`, `TestWebSocket_ClosesSilentClient`; `afm:pkg/server/websocket_clients_test.go`
`TestServer_ConnectedClients_TracksOpenConnections`, `TestServer_Shutdown_ClosesWebSocketClients`

### API-R20 Reconnecting clients rebuild history from durable logs
`GET /api/events` replays stage transitions from the journal plus notices from `notices.jsonl` (dialog Q&A
deduplicated by content, quota and verify notices included), with a global cap of 10000 and 2000 per stage filter;
early-stage Q&A survives the global cap. Live `/ws` delivery is not replayed; the client re-reads history.

Evidence: `afm:pkg/server/events_handler_test.go` `TestHandleEvents_ReplaysTransitionsAndNotices`,
`TestReconstructNotices_DedupsDialogQuestionAndAnswerByContent`, `TestHandleEvents_EarlyStageQAndASurviveGlobalLimit`,
`TestHandleEvents_GlobalCapRaisedTo10000`, `TestHandleEvents_StageFilterCapsAt2000`, `TestHandleEvents_QuotaNoticesReplayFromNoticesLog`

#### API-S16 reconnect after missed notice
- GIVEN a dialog question notice was published while the client was disconnected
- WHEN the client reconnects and calls `GET /api/events`
- THEN the question appears once in the replayed feed

### API-R21 SDK `Connect` is a non-owning loopback client
`afmsdk.Connect(ctx, port)` talks to `127.0.0.1:<port>`, rejects invalid ports and non-AFM servers, sends an
`Origin` equal to its base URL, applies a request timeout and owns no process (`Shutdown` of a connected run
refuses). Non-2xx answers are `*afmsdk.HTTPError` with the status code.

Evidence: `afm:sdk/connect_test.go` `TestConnectRejectsInvalidPortAndNonAFM`, `TestConnectRequestTimeout`,
`TestPlanOversizeAndHTTPStatus`; `afm:sdk/process_test.go` `TestNotOwnedRun`; `afm:sdk/integration_core_test.go`
`TestIntegration_CoreConnectHandleOwnsNothing`; `afm:sdk/connect.go`

### API-R22 SDK core start returns only when the runtime is compatible and ready
With `ModeCore`, `Start` runs `afm run --mode core` and returns after `/api/runtime` reports `core`, protocol 1 and
`ready: true` and status shows the same run id. Missing runtime endpoint, other mode or protocol fail with
`ErrIncompatibleRuntime` (no dashboard fallback); never-ready, foreign run id or early exit fail with output.
Dashboard mode tolerates a missing runtime endpoint.

Evidence: `afm:sdk/ready_test.go` `TestStartCore_Ready`, `TestStartCore_Refusals`, `TestStartCore_EarlyExitErrorHasOutput`,
`TestStartDashboard_ReadyWithoutRuntimeEndpoint`, `TestConnectAndAttach_DetectMode`; `afm:sdk/mode_test.go` `TestRunArgs_ByMode`

### API-R23 SDK `WaitResult` and `Shutdown`
`WaitResult` returns the first `finished`/`failed`/`interrupted` run state of the same run (a failed stage is not a
result; stale results before `ready` are ignored), returns `*RunChangedError` when the run id changes, and its
cancellation does not stop the run. `Shutdown` sends SIGINT, kills after the grace (`ErrKilled`), and every caller
sees the same outcome. In core `Wait` returns only after `Shutdown`.

Evidence: `afm:sdk/result_test.go` `TestWaitResult_TerminalStates`, `TestWaitResult_FailedStageIsNotTheResult`,
`TestWaitResult_IgnoresStaleResultUntilReady`, `TestWaitResult_RunChanged`, `TestWaitResult_CancelDoesNotStopTheRun`;
`afm:sdk/process_test.go` `TestShutdown_IgnoredSigintIsKilled`, `TestShutdown_ConcurrentCallersAgree`;
`afm:sdk/integration_core_test.go` `TestIntegration_CoreInstantFlowResultAndShutdown`, `TestIntegration_CoreCancelledWaitResultLeavesCore`

#### API-S17 run changed during wait
- GIVEN `WaitResult` observed run `a` running
- WHEN the next status reports run `b` finished
- THEN `WaitResult` returns `RunChangedError{Expected:"a", Current:"b"}`

### API-R24 SDK task, instance id and approval errors
`StartWithOptions{Task}` passes the task as `--task @-` on stdin (not in argv). `Run.InstanceID()` returns the
runtime `instance_id` (empty for older afm). `ApproveExpected` refusals are `*ApprovalError` with
`ApprovalCodeStale` / `RunChanged` / `Unavailable`; other failures stay `*HTTPError`.

Evidence: `afm:sdk/options_test.go` `TestStartWithOptions_TaskReachesAfmThroughStdin`, `TestRun_InstanceID`,
`TestRun_InstanceIDEmptyForOlderAfm`; `afm:sdk/approval_test.go` `TestApproveExpected_AppliedAndStale`,
`TestApproveExpected_RunChangedCode`, `TestApprovalError_NonCodeFailuresStayHTTPErrors`, `TestApprovalTypes_MatchContractGolden`;
`afm:sdk/integration_core_test.go` `TestIntegration_CoreStartWithTask`, `TestIntegration_CoreConditionalApproval`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| API-E15 | answer while flow review-paused | 409 `flow_paused`, answer rolled back | `TestCommandContract_DialogAnswer_NotifyFailures` |
| API-E16 | interactive answer not for current question | 409 `answer_out_of_order` | `TestCommandContract_DialogAnswer_InteractiveOrdering` |
| API-E17 | current-question lookup I/O error | 500 `current_question_lookup_failed` | same |
| API-E18 | malformed approval body | 400, engine not called | `TestApprovalRejectsMalformedBody` |
| API-E19 | approval stale / run changed / unavailable | 409 with that code | `TestApprovalConflictsReturn409WithCode` |
| API-E20 | SDK core against incompatible afm | `ErrIncompatibleRuntime` | `TestStartCore_Refusals` |

## Coverage gaps and discrepancies
- **API-G06** (ambiguous): a generic notify error after a durable answer yields 500 although the intent is
  saved; the test labels it "current behavior". A client retry may hit `Duplicate`.
- **API-G07** (doc-only): "non-FSM notices are published live and to `notices.jsonl` through a shared path"
  (`afm:AGENTS.md`) is verified only on the replay side here; the engine publish path is not covered in afm tests.
- **API-G08** (untested): live `/ws` has no sequence/resume token; reconnect correctness relies on clients
  calling `/api/events` (dashboard behavior, see `afm:pkg/web/dashboard`).
