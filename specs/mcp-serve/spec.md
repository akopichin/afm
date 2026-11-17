# MCP endpoint and stdio bridge

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `/mcp` of a single `afm run` (`afm:pkg/mcpserver`): enabling flags, permission groups, tools,
  resources, waits, subscriptions, artifact reads, shutdown.
- The stdio bridge `afm mcp serve --connect` (`afm:cmd/afm/mcp.go`).
- Out of scope: the host `afm serve`, run registry, launch/attach and `--task`
  (`../mcp-serve-host/spec.md`, `../mcp-serve-run/spec.md`); HTTP guards and REST approval (`../http-api/spec.md`,
  `../http-api-live/spec.md`).

## Terms
- **group**: permission set `reply`, `approve`, `stages`, `budget`; reads are always available.
- **RunBound**: a catalog tool whose control arguments carry `run_id`.
- **cursor**: opaque digest of the attention model returned by `afm_run_status`.

## Requirements

### MCP-R01 `/mcp` is on by default whenever the run has an API
With a positive port `/mcp` is mounted on the API port with all groups, in core and dashboard. `--mcp-allow`
narrows groups (`read` or empty = read-only; unknown names and `all` refused; `read` cannot be mixed). `--mcp-off`
disables it and cannot be combined with `--mcp`/`--mcp-allow`. Without an API, MCP is silently off, but explicit
`--mcp`/`--mcp-allow` are refused. All refusals happen before a run is created.

Evidence: `afm:cmd/afm/run_mode_test.go` `TestParseMCPSettings`; `afm:cmd/afm/run_mcp_test.go` `TestRunMCPRefusedFlagsCreateNothing`,
`TestRunMCPMountsByDefault`, `TestRunMCPReadOnlyKeyword`, `TestRunMCPAcceptsNamedGroups`, `TestRunMCPOff`;
`afm:pkg/mcpserver/groups_test.go` `TestGroupsDefaultIsReadOnly`, `TestGroupsRejectUnknownAndAll`, `TestGroupsReadKeywordCannotBeMixed`;
`afm:cmd/afm/mcp_integration_test.go` `TestMCPIntegrationServesBesideAPIInBothModes`; `afm:cmd/afm/run_mode.go` `parseMCPSettings`

#### MCP-S01 `--mcp` without API
- GIVEN `afm run --port 0 --mcp flow.yaml`
- WHEN the command starts
- THEN it fails with `--mcp needs the HTTP API` and no run directory is created

### MCP-R02 Disabled MCP answers JSON 404, never UI HTML
`/mcp` is always routed; with MCP off it answers 404 `{"error":"mcp_disabled"}`. The route runs `LocalGuard`
before the handler. Server `Shutdown` closes the MCP handler before `http.Server.Shutdown`.

Evidence: `afm:pkg/server/mcp_route_test.go` `TestMCPRouteDisabledAnswersJSON404`, `TestMCPRouteGuardRunsBeforeHandler`,
`TestMCPRouteShutdownClosesMCPBeforeHTTPServer`

### MCP-R03 The catalog is the single source of tools; groups are checked twice
`Catalog()`/`ResourceTemplates()` define names, schemas, groups and `RunBound`. A tool is listed only when its
group is enabled, and a call still checks the group (`not_allowed`).

Evidence: `afm:pkg/mcpserver/catalog_test.go` `TestToolsListMatchesCatalog`, `TestToolsListFollowsCatalogGroups`,
`TestCatalogGroupsAndRunBinding`, `TestResourceTemplatesMatchService`; `afm:pkg/mcpserver/tools_test.go`
`TestToolsListFollowsGroups`, `TestToolsGroupsCannotBeCalledWhenAbsent`; `afm:pkg/mcpserver/control_test.go` `TestControlNotAllowedWithoutGroup`

### MCP-R04 One service serves one run; control calls are fenced by `run_id`
The service is bound to one run for life; a repeat cycle gets a new service and sessions. A control call whose
`run_id` is not the served run fails `run_changed` and reaches no collaborator. Success returns `applied` and the
stage state read after the command.

Evidence: `afm:pkg/mcpserver/control_test.go` `TestControlRunMismatchReachesNothing`, `TestControlApproveStateIsReadAfterCommand`;
`afm:pkg/mcpserver/service_test.go` `TestCheckRunRejectsOtherRun`; `afm:cmd/afm/mcp_integration_test.go` `TestMCPIntegrationRepeatCycleChangesRun`

#### MCP-S02 repeat cycle
- GIVEN a client holding `run_id` of cycle 1
- WHEN cycle 2 starts and the client calls `afm_stage_decide` with the old id
- THEN it fails `run_changed` and nothing changes

### MCP-R05 Approve is only `ApproveExpected` with the revision the client read
`afm_stage_decide approve` requires `expected_revision` and a reviewable plan; it never uses legacy approve. Stale
revision → `approval_stale`, stage keeps waiting; concurrent approvals of one revision apply once; a plan revised or
edited after the read is stale. The plan revision is computed on original bytes even when text is redacted.

Evidence: `afm:pkg/mcpserver/control_test.go` `TestControlApproveUsesApproveExpectedOnly`, `TestControlApproveNeedsRevisionAndApproval`,
`TestControlApproveRequiresTheCurrentRevision`, `TestControlApproveTwiceIsStale`, `TestControlApproveNonASCIIAndRedactedPlan`;
`afm:cmd/afm/mcp_integration_test.go` `TestMCPIntegrationConcurrentApproveAppliesOnce`, `TestMCPIntegrationReviseInvalidatesEarlierRevision`,
`TestMCPIntegrationPlanEditedAfterReadIsStale`

### MCP-R06 Control commands outlive the MCP request; reply binds to the current question
Cancelling a tool call does not cancel an admitted command. `afm_interaction_reply` binds to the current
`phase`/`question_id`; an answered question fails `question_not_pending`. Budget adjust is absolute and does not
resume; resume is a separate tool.

Evidence: `afm:pkg/mcpserver/control_test.go` `TestControlCancelledRequestDoesNotCancelCommand`, `TestControlReplyBindsToCurrentQuestion`,
`TestControlBudgetAdjustIsAbsoluteAndDoesNotResume`, `TestControlBudgetResume`; `afm:cmd/afm/mcp_integration_test.go`
`TestMCPIntegrationBudgetAdjustThenResume`, `TestMCPIntegrationClientCloseKeepsAgentRunning`

### MCP-R07 Errors reuse REST codes; MCP-only codes are few
Failed calls return `isError: true` with `{code, kind, message}` classified by the REST classifier
(`not_found`, `conflict`, `invalid`, `approval_*`). MCP-only codes: `not_allowed`, `run_changed`, `mcp_closed`,
`mcp_session_limit`, `question_not_pending`, `wait_capacity`.

Evidence: `afm:pkg/mcpserver/service_test.go` `TestToolErrorUsesRESTClassifier`; `afm:pkg/mcpserver/control_test.go`
`TestControlConflictsKeepRESTCodesAndReturnState`; `afm:pkg/mcpserver/errors.go`

### MCP-R08 Status exposes the attention model with a stable cursor
`afm_run_status` lists one pending action per open question/approval/failure/budget hold with allowed actions
(none when read-only; no approve without a consistent snapshot) and a cursor that changes only with the model.

Evidence: `afm:pkg/mcpserver/status_test.go` `TestStatusQuestionsInTwoStagesGiveTwoActions`, `TestStatusReadOnlyOffersNoActions`,
`TestStatusApprovalCarriesSnapshotRevision`, `TestStatusApprovalUnavailableOffersNoApprove`, `TestStatusCursorChangesOnlyWithModel`

### MCP-R09 `afm_run_wait` parks until the model changes, bounded
A stale cursor returns at once; a foreign/malformed cursor returns `reset: true`. Otherwise it wakes on bus impulses
(bursts coalesced), dropped events, a file-poll tick or plan revision change, or times out (default 30 s, max 60 s)
with a fresh status. At most 8 waits park (`wait_capacity`); cancelling a wait ends only the wait; `Close` ends all.

Evidence: `afm:pkg/mcpserver/wait_test.go` `TestWaitImmediateOnStaleCursor`, `TestWaitResetOnForeignOrBrokenCursor`,
`TestWaitCoalescesBurstIntoOneWake`, `TestWaitWakesOnDroppedEventsWithoutDelay`, `TestWaitWakesOnTickForFileChanges`,
`TestWaitTimeoutIsCapped`, `TestWaitCapacityLimit`, `TestWaitCancelEndsOnlyTheWait`, `TestWaitEndsOnClose`

#### MCP-S03 question arrives while waiting
- GIVEN a client parked in `afm_run_wait` with the current cursor
- WHEN a stage writes a question file
- THEN the wait returns `changed: true` with the new pending action

### MCP-R10 Subscriptions are best-effort hints
Only `status` and `cost` resources are subscribable; updates are coalesced and may be skipped. The notifier starts on
the first subscription or wait. Unknown resources are refused. Closing with open subscriptions does not hang.

Evidence: `afm:pkg/mcpserver/subscribe_test.go` `TestSubscribeStatusSendsCoalescedUpdate`, `TestSubscribeChangeRightAfterAckIsNotified`,
`TestSubscribeCostResourceUpdates`, `TestSubscribeRejectsUnknownResource`, `TestSubscribeWithoutSubscribersStartsNoNotifier`,
`TestCloseWithOpenSubscriptionDoesNotHang`

### MCP-R11 Artifacts are read by opaque id inside the run root
An id resolves only to a plan, summary, stage image or declared `./` artifact via `os.Root` of the run directory with
descriptor walks; symlinks in any component, parent swaps, devices, FIFOs, logs, session and dialog files are refused.
Text pages are 32 KiB default / 64 KiB max on rune boundaries; files > 4 MiB `too_large`; non-UTF-8 `not_text`;
secrets masked with `redacted: true`. The plan resource needs the current revision.

Evidence: `afm:pkg/mcpserver/artifacts_test.go` `TestArtifactAllowlistRejectsEverythingElse`, `TestArtifactRejectsSymlinkInEveryComponent`,
`TestArtifactParentSwapCannotRedirect`, `TestArtifactRejectsDeviceNode`, `TestDeclaredArtifactsHideSessionAndDialogFiles`,
`TestArtifactPagesAlignToRuneBoundaries`, `TestArtifactDefaultAndMaxPage`, `TestArtifactRejectsOversizeAndNonUTF8`,
`TestArtifactRedactionSetsFlagAndSurvivesPageSplit`, `TestArtifactPlanResourceNeedsCurrentRevision`

### MCP-R12 Sessions are bounded and `Close` drains everything
At most 16 sessions (concurrent creations respect it), idle sessions expire after 30 min. `Close` is idempotent,
rejects new requests (`mcp_closed`), ends SSE streams, interrupts blocked body reads and writes, and returns only after
every handler finished.

Evidence: `afm:pkg/mcpserver/service_test.go` `TestSessionCeiling`, `TestConcurrentSessionCreationsRespectCeiling`,
`TestIdleSessionExpiresAndFreesCapacity`, `TestCloseRejectsNewRequests`, `TestCloseEndsOpenSSEStream`, `TestCloseWaitsForRunningHandler`,
`TestCloseInterruptsUnfinishedRequestBody`, `TestCloseInterruptsBlockedResponseWrite`; `afm:cmd/afm/mcp_integration_test.go`
`TestMCPIntegrationShutdownWithActiveWait`

### MCP-R13 Events are paged by journal `seq`
`afm_events_read` returns stage transitions ordered by `seq` from `next_seq`, default 100, max 500, optional stage
filter; reasons are redacted.

Evidence: `afm:pkg/mcpserver/events_test.go` `TestEventsPageBySeq`, `TestEventsLimitDefaultsAndCaps`, `TestEventsStageFilterAndValidation`,
`TestEventsRedactReason`

### MCP-R14 The stdio bridge forwards without owning the run
`afm mcp serve --connect URL` accepts only loopback `http` URLs without userinfo (bare host:port → `/mcp`) and
`--transport stdio`; it refuses redirects. Stdout carries only protocol messages. It forwards tools, protocol
version and resource updates; stdin EOF deletes the session and exits 0 (also with a parked call); cancelled calls
are never sent later. A lost session or a run with `--mcp-off` exits 1 with a clear message and empty stdout.

Evidence: `afm:cmd/afm/mcp_test.go` `TestParseMCPConnect`, `TestMCPServeRefusesRedirects`, `TestMCPServeKeepsStdoutProtocolOnly`,
`TestMCPServeForwardsToolsAndProtocolVersion`, `TestMCPServeDeliversResourceUpdates`, `TestMCPServeStdinEOFDeletesSession`,
`TestMCPServeStdinEOFWithParkedCallDeletesSession`, `TestMCPServeCancelledCallIsNotSentLater`, `TestMCPServeSessionGoneFailsClearly`,
`TestMCPServeMCPDisabledFailsClearly`

#### MCP-S04 bridge to a run without MCP
- GIVEN a run started with `--mcp-off`
- WHEN `afm mcp serve --connect http://127.0.0.1:P` starts
- THEN it exits 1 naming `--mcp-off` and writes nothing to stdout

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MCP-E01 | `--mcp-off` with `--mcp`/`--mcp-allow` | startup error, nothing created | `TestParseMCPSettings` |
| MCP-E02 | unknown group / `all` / `read` mixed | `--mcp-allow:` error | `TestGroupsRejectUnknownAndAll` |
| MCP-E03 | MCP disabled | 404 `mcp_disabled` | `TestMCPRouteDisabledAnswersJSON404` |
| MCP-E04 | group off | `not_allowed` | `TestControlNotAllowedWithoutGroup` |
| MCP-E05 | wrong `run_id` | `run_changed` | `TestControlRunMismatchReachesNothing` |
| MCP-E06 | > 16 sessions / > 8 waits | `mcp_session_limit` / `wait_capacity` | `TestSessionCeiling`, `TestWaitCapacityLimit` |
| MCP-E07 | after `Close` | `mcp_closed` | `TestCloseRejectsNewRequests` |
| MCP-E08 | non-loopback `--connect` or redirect | bridge refuses | `TestParseMCPConnect`, `TestMCPServeRefusesRedirects` |

## Coverage gaps and discrepancies
- **MCP-G01** (doc-only): `revise` is not idempotent (repeated feedback sent twice); stated in `docs/mcp.md`, no test.
- **MCP-G02** (ambiguous): permission groups are not a security boundary; the run's own agents can reach `/mcp` with all
  groups by default (`docs/mcp.md` Local access). Behavior by design, no test asserts agent access.
- **MCP-G03** (untested): image limits (10 MiB, 8192 px, 20 per stage, 50 declared) are doc/code only beyond
  `TestArtifactImageInlineAndLink`/`TestArtifactImageAboveInlineLimitReadsAsResource`.
