# Host `afm serve`: discovery, launch, attach and forwarding

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- The host `afm serve` (`afm:pkg/serve`, `afm:cmd/afm/serve.go`, `docs/serve.md`): single instance, ownership,
  liveness, launch, attach, cancel/release, forwarding gateway, `serve.yaml`.
- Out of scope: the run registry record and `--task` written/consumed by `afm run`
  (`../mcp-serve-run/spec.md`); the per-run `/mcp` service and stdio bridge
  (`../mcp-serve/spec.md`); HTTP modes (`../http-api/spec.md`). Part of the `MCP` family;
  IDs continue the shared numbering.

## Terms
- **managed**: launched by this host (has a receipt). **attached**: explicit `afm_run_attach`, or a manual run
  (registry record without `launch_id`) whose endpoint confirmed its `instance_id`. **discovered**: unverified.
- **identity**: `instance_id` + process start marker + fresh `/api/runtime` answer.
- **receipt**: durable per-`request_id` launch record in `~/.afm/serve`.

## Requirements

### MCP-R21 One host per user; its endpoint is published
A second `afm serve` exits 1 naming the first host's pid and endpoint. Default port 8100; `--port 0` picks a free port
written to `~/.afm/serve/host.json`. `--mcp-allow` accepts `reply,stages,approve,budget,runs` or `read`; default is all
(CLI decision). Invalid flags or config fail at startup.

Evidence: `afm:pkg/serve/app_test.go` `TestOpenRefusesASecondHostAndNamesTheFirst`, `TestEndpointIsPublishedInHostJSON`;
`afm:cmd/afm/serve_test.go` `TestServeRefusesASecondHost`, `TestServeRejectsBadFlagsAndConfig`; `afm:pkg/serve/gateway_test.go` `TestParseHostGroups`

### MCP-R22 Host owns only its state and never stops runs on its own exit
The host writes only under `~/.afm/serve`; it never deletes projects or run directories. SIGINT/SIGTERM of the host leave
every run running; a restarted host adopts them. The only foreign deletion is a registry record with proven exit, re-read
before removal; on startup each observation is saved before the record is removed.

Evidence: `afm:pkg/serve/app_test.go` `TestShutdownLeavesRunsRunningAndARestartAdoptsThem`, `TestOpenRemovesRecordsOfExitedProcessesOnly`;
`afm:pkg/serve/discovery_test.go` `TestReapRemovesOnlyProvenExit`, `TestReapKeepsARecordThatChangedMeanwhile`;
`afm:pkg/serve/binding_test.go` `TestStartupReapSavesTheExitedRecordsRunsFirst`, `TestStartupReapKeepsARecordItCouldNotSave`;
`afm:cmd/afm/serve_integration_test.go` `TestServe` ("host shutdown leaves the run", "host SIGKILL and restart reconnects")

#### MCP-S20 host restart
- GIVEN a managed core run started by the host
- WHEN the host receives SIGTERM and a new `afm serve` starts
- THEN the run kept running and is listed again as managed

### MCP-R23 Runs are classified by verified identity
`afm_runs_list` reports ownership, liveness (`active`, `active_orphaned`, `unreachable`, `stale`, `exited`,
`identity_mismatch`), launch state and actions. A record is accepted only when `/api/runtime` returns the same
`instance_id` and `run_id`; a forged or reused-port record is `identity_mismatch` and never controlled. A verified
manual run is `attached` from the listing alone.

Evidence: `afm:pkg/serve/discovery_test.go` `TestClassify`, `TestClassifyReprobesBeforeCallingARunStale`, `TestScanClassifiesEveryRecord`;
`afm:pkg/serve/inventory_test.go` `TestInventoryOwnership`, `TestVerifiedManualRunIsAttached`; `afm:pkg/serve/gateway_test.go`
`TestRunsListShowsOwnershipLivenessAndActions`; `TestServe` ("lists a verified manual run as attached")

### MCP-R24 Signals only to managed runs after identity re-check
Cancel (active workflow) and release (finished, held) apply only to managed runs; attached gets `not_managed` and is never
signalled. Before signalling the host re-checks the start marker (pidfd on Linux) and a fresh `/api/runtime`; a mismatch or
advanced run is `run_changed` with no signal. Native runs ignoring SIGINT are killed after the grace (re-checked); Docker runs
get SIGINT only (`stop_pending`). Wrong preconditions signal nothing.

Evidence: `afm:pkg/serve/control_test.go` `TestCancelInterruptsAnActiveRun`, `TestReleaseInterruptsAFinishedHeldRun`, `TestPreconditions`,
`TestStartMarkerMismatchSendsNoSignal`, `TestEndpointOfAnotherInstanceSendsNoSignal`, `TestRunThatAdvancedSinceTheStatusIsNotSignalled`,
`TestAttachedRunsAreNeverSignalled`, `TestNativeRunThatIgnoresInterruptIsKilledAfterTheGrace`, `TestDockerRunIsNeverKilled`,
`TestKillIsRecheckedAgainstTheMarker`; `afm:pkg/serve/gateway_test.go` `TestAttachedRunCannotBeCancelled`

### MCP-R25 Launch is idempotent per `request_id` and never blindly restarted
`afm_run_start` creates a receipt (tmp+fsync+link) before work; the same `request_id` with the same params returns the same
launch (answered from the receipt even if inputs changed), different params → `request_conflict`; a failed start spends the id.
Unknown fate becomes `uncertain`, then `lost` (not if a pid was found); the host never restarts a child.

Evidence: `afm:pkg/serve/receipts_test.go` `TestCreateReceiptIsIdempotent`, `TestCreateReceiptConflictOnDifferentParams`,
`TestCreateReceiptSurvivesCrashBeforeLink`, `TestConcurrentCreateYieldsOneWinner`; `afm:pkg/serve/launch_test.go`
`TestStartIsIdempotentPerRequestID`, `TestReplayIsAnsweredFromTheReceiptAfterItsInputsChanged`, `TestRecoverUnknownLaunchIsLostNotRestarted`,
`TestSpawnFailureIsRecorded`; `afm:pkg/serve/binding_test.go` `TestUncertainExpiryKeepsAProcessFoundMeanwhile`

#### MCP-S21 duplicate request
- GIVEN a launch accepted for `request_id: r1`
- WHEN `afm_run_start` is called again with `r1` and identical params
- THEN the same `launch_id` is returned and no second child starts

### MCP-R26 Child processes are detached and receive no secrets in argv
The child `afm run` starts without the request context (cancelling the MCP call does not kill it), in its own session, stdin
`/dev/null`, log 0600, filtered env; the task goes as `--task @file`, removed when ready/ended. The child outlives the launcher.
Child MCP groups are limited to what the host allows; an empty host set means read-only children.

Evidence: `afm:pkg/serve/launch_test.go` `TestStartSpawnsADetachedChildWithTypedArgv`, `TestStartedChildSurvivesTheLauncher`,
`TestStartLimitsGroupsToWhatTheHostAllows`, `TestHostWithoutChildGroupsStartsReadOnlyRuns`, `TestRecoverRemovesPromptsWithoutAReceipt`;
`afm:pkg/serve/gateway_test.go` `TestCancelledStartRequestDoesNotKillTheChild`

### MCP-R27 Admission by checkout identity and limits
A launch conflicts with a live run of the same resolved checkout and effective run name (worktree, else flow name), using
the identity stored in the first receipt. `max_running` (default 4) and `max_starting` (default 2) are enforced
(`limit_running`/`limit_starting`). A launch not ready within `ReadyTimeout` is `failed` and frees its starting slot, but keeps
its running slot and identity until its process exit is proven; an orphaned Docker container holds its slot.

Evidence: `afm:pkg/serve/launch_test.go` `TestAdmissionComparesCheckoutAndRunName`, `TestAdmissionLimits`, `TestUnreadyEndpointFailsAtTheReadyDeadline`;
`afm:pkg/serve/admission_test.go` `TestAdmissionKeepsTheIdentityAcceptedAtLaunch`, `TestAdmissionTellsCheckoutsApart`,
`TestTimedOutLaunchHoldsItsRunningSlotUntilItsProcessExits`, `TestDockerOrphanHoldsItsSlotUntilTheContainerIsGone`

### MCP-R28 Attach and history survive process exit
`afm_run_attach` takes a loopback API URL, verifies identity and stores the binding in `attached.json` under one lock.
Repeat history lives in receipts and `attached.json` (bounded); every observation is merged before display; stale
observations never move the current run back. `afm_run_report` reads managed/attached runs from files after exit;
a merely discovered run needs attach first.

Evidence: `afm:pkg/serve/inventory_test.go` `TestAttach`, `TestAttachRefusals`, `TestAttachedFollowsTheRepeatedRun`,
`TestFindResolvesEarlierCyclesFromTheReceiptAfterExit`, `TestResolveActiveDiscoveredNeedsAttach`; `afm:pkg/serve/binding_test.go`
`TestMergeRunBinding`, `TestRegisteredAttachmentKeepsItsRunsAfterExit`, `TestADelayedReattachCannotMoveTheRunBack`;
`afm:pkg/serve/state_test.go` `TestUpdateAttachedKeepsEveryConcurrentUpdate`; `afm:pkg/serve/gateway_test.go` `TestReportReadsAFinishedRunFromItsFiles`

### MCP-R29 Gateway forwards the single-run catalog by `run_id`
Host tools are the run catalog plus a required `run_id`, offered by host groups and checked per call. Forwarding works for
managed and attached runs only. A session is accepted after `afm_run_status` in the same session confirms `run_id` and
`instance_id`; a stale id gets `run_changed`, never the next run's data. After a broken transport, control is not retried
(`outcome_unknown`); read-only is retried once. Waits per run are bounded.

Evidence: `afm:pkg/serve/gateway_test.go` `TestGatewayOffersToolsByGroup`, `TestForwardedToolsRequireRunID`,
`TestForwardingRefusesRunsTheHostDoesNotOwn`, `TestStaleRunIDGetsRunChangedNotTheNextRunsData`, `TestSessionIsRefusedForAnotherInstanceOfTheSameRun`,
`TestControlIsAppliedOnceWhenTheConnectionBreaks`, `TestReadOnlyCallIsRetriedOnceAfterABrokenConnection`, `TestGroupIsCheckedOnEveryCall`,
`TestWaitsPerRunAreBounded`; `TestServe` ("run_changed after a repeat cycle")

#### MCP-S22 broken connection during control
- GIVEN a forwarded `afm_stage_decide` whose transport breaks mid-call
- WHEN the gateway handles the failure
- THEN it returns `outcome_unknown` and does not resend the call

### MCP-R30 Subscriptions are relayed per host session
One upstream subscription per resource; a disconnecting host client releases its subscriptions; a lost run session notifies
subscribers once; reconnect replays subscriptions of the same run id only; `Close` cancels forwarded calls and run sessions
before host sessions, so a parked wait does not block shutdown.

Evidence: `afm:pkg/serve/gateway_test.go` `TestSubscriptionsAreRelayedOncePerResource`, `TestDisconnectedHostClientReleasesItsSubscriptions`,
`TestLostSessionNotifiesSubscribersOnce`, `TestGatewayCloseDoesNotWaitForParkedForwardedWaits`; `afm:pkg/serve/subscriptions_test.go`
`TestRepeatDoesNotCarryOldRunSubscriptions`, `TestReconnectReplaysSubscriptionsOfTheSameRun`, `TestLastUnsubscribeCannotUndoANewerSubscribe`

### MCP-R31 Loopback URLs only; transport errors never leak the URL
All run URLs pass `ParseLoopbackURL` (http, loopback, no userinfo); redirects are not followed; transport failures surface as
`*RemoteError` with fixed text.

Evidence: `afm:pkg/serve/endpoint_test.go` `TestParseLoopbackURL`, `TestParseLoopbackURLErrorsNeverEchoCredentials`,
`TestProbeRuntimeNeverFollowsRedirects`, `TestProbeRuntimeRefusesNonLoopback`; `afm:pkg/serve/remote_errors_test.go` `TestProbeRuntimeHidesTransportErrors`,
`TestAttachHidesTransportErrors`; `afm:pkg/serve/gateway_test.go` `TestAttachRefusalDoesNotEchoCredentials`

### MCP-R32 `serve.yaml` allowlists what can start
Only listed projects (absolute existing dirs) and pipelines can be started; ids are lowercase/digits/dashes; a missing file is an
empty config (list/attach still work). `retention.release_after` is off by default and releases only after continuous observation.

Evidence: `afm:pkg/serve/config_test.go` `TestParseConfigFull`, `TestParseConfigDefaults`, `TestParseConfigRejects`, `TestLoadConfigMissingFileIsEmpty`;
`afm:pkg/serve/control_test.go` `TestSweepIsOffByDefault`, `TestSweepReleasesAfterContinuousObservation`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MCP-E20 | second host | exit 1 naming pid and endpoint | `TestServeRefusesASecondHost` |
| MCP-E21 | same `request_id`, other params | `request_conflict` | `TestCreateReceiptConflictOnDifferentParams` |
| MCP-E22 | same checkout + run name live | `conflict` | `TestAdmissionComparesCheckoutAndRunName` |
| MCP-E23 | over limits | `limit_running` / `limit_starting` | `TestAdmissionLimits` |
| MCP-E24 | cancel/release attached run | `not_managed` | `TestAttachedRunCannotBeCancelled` |
| MCP-E25 | cancel finished / release running | `PreconditionError`, no signal | `TestPreconditions` |
| MCP-E26 | forwarded call with stale `run_id` | `run_changed` | `TestStaleRunIDGetsRunChangedNotTheNextRunsData` |

## Coverage gaps and discrepancies
- **MCP-G20** (untested): only Linux is verified (`docs/serve.md`); macOS/Docker hosts not exercised; `TestServe` skips Windows.
- **MCP-G21** (doc-only): "budgets cannot be set at start" is stated in `docs/serve.md`; no test asserts the absence.
