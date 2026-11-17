# Lifecycle hooks: events and delivery

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Which lifecycle events the engine emits and when (after CAS, flow start/end).
- Payload envelope, stable event IDs, terminal-stage analytics.
- Delivery: per-hook sequential workers, timeout/retries, flush/stop, failure isolation.
- Out of scope: declaration layers, validation, env/secrets, Docker transport,
  redaction, CWD → [../lifecycle-hooks-env/spec.md](../lifecycle-hooks-env/spec.md);
  blocking `script_before`/`script_after` → run lifecycle specs.
  IDs: this file uses R01–R19, S01–S29, E01–E09, G01–G09.

## Terms
- **Observer hook**: a `hooks:` entry; never changes FSM state.
- **Dispatcher**: `lifecyclehooks.Dispatcher`, owns per-hook queues and its own context.
- **Event ID**: idempotency key `AFM_HOOK_EVENT_ID` / `event_id`.

## Requirements

### HOOK-R01 Public event catalog
The catalog (`AllEvents`) has flow events (`flow_started`, `flow_resumed`,
`flow_repeated`, `flow_finished`, `flow_failed`, `flow_interrupted`), stage FSM
events, quota events and script events. `events: all` means this catalog and
never includes high-frequency internal events (agent actions, script output).

Evidence: afm:contracts/lifecyclehooks/types_test.go `TestAllEventsCatalog`, `TestAllEventsCatalog_IncludesFlowRepeated`, `TestAllEventsCatalog_IncludesQuotaEvents`; afm:docs/lifecycle-hooks.md

### HOOK-R02 Stage FSM events only after a committed transition
A stage lifecycle event is emitted only for an applied transition (seq > 0),
mapped from the FSM event; unmapped FSM events emit nothing. `EvVerifyFail`
maps to `stage_failed`; both retry FSM events map to `stage_retry_started`.

Evidence: engine:internal/orchestrator/lifecycle_emit_test.go `TestEmitLifecycleTransition_MapsFSMEvent`, `TestEmitLifecycleTransition_UnmappedFSMEventIgnored`; engine:internal/orchestrator/lifecycle_analytics_test.go `TestStageAnalytics_OnlyCommittedTerminalEvents`; engine:internal/orchestrator/lifecycle_emit.go `fsmLifecycleEvents`

#### HOOK-S01 CAS miss
- GIVEN a stage whose transition CAS is rejected
- WHEN the orchestrator attempts the transition
- THEN no lifecycle event is emitted for it

### HOOK-R03 Flow start/end events
A run emits exactly one start event — `flow_resumed` for a resumed run (even in
repeat mode), else `flow_repeated` for cycles after the first, else
`flow_started` — and one terminal event `flow_finished`, `flow_failed` or
`flow_interrupted`. The terminal event is emitted before the final flush.

Evidence: engine:internal/orchestrator/lifecycle_flow_test.go `TestRun_FlowEvents_Finished`, `TestRun_FlowEvents_Resumed`, `TestRun_FlowEvents_Repeated`, `TestRun_FlowEvents_ResumedBeatsRepeated`, `TestRun_FlowEvents_Failed`, `TestRun_FlowEvents_Interrupted`; engine:internal/orchestrator/orchestrator.go `setTerminalFlow`

#### HOOK-S02 Interrupted run still notifies
- GIVEN a hook on `flow_interrupted`
- WHEN the run context is cancelled
- THEN `flow_interrupted` is still delivered (dispatcher context is independent)

### HOOK-R04 Script and auto-answer events
Script stages and `script_before`/`script_after` emit started/finished/failed
events; a non-interactive auto-answer emits question asked/answered events with
distinct IDs per question, except for a parked stage.

Evidence: engine:internal/orchestrator/lifecycle_scripts_test.go `TestScriptStage_Events`, `TestScriptStage_FailureEvents`, `TestBeforeAfterHook_Events`, `TestBeforeHook_FailureEvent`; engine:internal/orchestrator/lifecycle_dialog_test.go `TestPollQuestions_AutoAnswerEmitsLifecycleEvents`, `TestPollQuestions_AutoAnswerParkedStageSkipsLifecycleEvents`, `TestPollQuestions_AutoAnswerDistinctQuestionsGetDistinctEventIDs`

### HOOK-R05 Stable event IDs
IDs are `<run>:transition:<seq>:<event>` for FSM transitions, `<run>:flow:<event>`
for flow events, and `<run>:<stage>[:<key>]:<event>` for non-FSM stage events.
All deliveries of one event share its ID.

Evidence: afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_RoutesAndBuildsEventIDs`, `TestDispatcher_FlowEventID`, `TestDispatcher_NonFSMStageEventID`, `TestDispatcher_KeyedNonFSMStageEventID`

### HOOK-R06 Payload envelope
The payload (JSON on stdin) has `schema_version` 1, `event_id`, `event`,
`occurred_at`, `flow` (name, run_id, resumed, run_dir, root_dir; plus `cycle`,
`max_cycles`, `previous_run_id` only in repeat mode), `stage` (absent for flow
events) and `reason`. Secrets never appear in the payload.

Evidence: afm:contracts/lifecyclehooks/types_test.go `TestBuildPayload`, `TestBuildPayload_RepeatCycles`, `TestBuildPayload_RepeatJSON`; afm:docs/lifecycle-hooks.md

### HOOK-R07 Terminal analytics in `data.analytics`
Only `stage_finished`/`stage_failed` carry `data.analytics` (version 1),
frozen at the transition seq: cumulative across retries and helpers, unknown
values `null` (not 0) with a reason, no money keys whatever the accounting
settings. A script-only stage is a proven zero. Unavailable stores still deliver
the event. Other events have no `data`.

Evidence: afm:contracts/lifecyclehooks/types_test.go `TestBuildPayload_AnalyticsInData`, `TestBuildPayload_NoAnalyticsKeepsDataAbsent`, `TestStageAnalytics_HasNoMoneyKeys`; engine:internal/orchestrator/lifecycle_analytics_test.go `TestStageAnalytics_CumulativeAcrossRetriesAndHelpers`, `TestStageAnalytics_StageMovedOnBeforeTheSnapshot`, `TestStageAnalytics_StageWithoutAgentsIsAProvenZero`, `TestStageAnalytics_UnavailableStoresStillDeliverTheEvent`, `TestStageAnalytics_NoMoneyWhateverTheAccountingSettings`

#### HOOK-S03 Retried stage
- GIVEN a stage that failed, was retried and finished
- WHEN `stage_finished` is delivered
- THEN its analytics include both attempts (scope `stage_cumulative`)

### HOOK-R08 Redelivery is byte-identical
Analytics are copied at Emit and the payload is rebuilt per delivery, so retries
of one event ID are byte-for-byte equal and later mutation does not leak in.

Evidence: afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_AnalyticsFrozenAndCopiedPerDelivery`

### HOOK-R09 Per-hook sequential, cross-hook parallel delivery
Each hook has one worker consuming its queue in order; different hooks run in
parallel. A stage-scoped hook receives only its own stage's events.

Evidence: afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_SequentialPerHook`; afm:contracts/lifecyclehooks/validate_test.go `TestMatches`, `TestRegisteredHookScope`

### HOOK-R10 Bounded queue drops, never blocks
Each hook queue holds 256 deliveries; on overflow the delivery is dropped and
reported via `OnError`. Emit never blocks the orchestrator; Emit after Stop is a no-op.

Evidence: afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_QueueOverflowDropsNotBlocks`, `TestDispatcher_ConcurrentEmitStopNoPanic`; afm:contracts/lifecyclehooks/dispatcher.go `hookQueueSize`

### HOOK-R11 Timeout and retries per delivery
Each attempt runs `sh -c <command>` bounded by `timeout` (default 30 s); timeout
kills the whole process group. A failed attempt is retried `retries` times
(default 0) with 1 s backoff.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestRunCommand_RetriesThenSuccess`, `TestRunCommand_FinalFailure`, `TestRunCommand_TimeoutKillsProcessGroup`; afm:contracts/lifecyclehooks/runner.go `DefaultHookTimeout`

### HOOK-R12 Failure is isolated from the run
A final hook failure (or overflow) never changes FSM, stage result or flow
outcome. It is published as a live and durable (`notices.jsonl`)
`lifecycle_hook_failed` notice with hook ID, event ID and redacted error.

Evidence: afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_FinalFailureReportsOnError`; engine:internal/orchestrator/lifecycle_notice_test.go `TestPublishLifecycleHookFailure_Notice`; afm:cmd/afm/core_mode_integration_test.go `TestCoreMode_FailingObserverHookKeepsTransitions`

#### HOOK-S04 Failing notifier
- GIVEN a hook whose command always exits 1
- WHEN stages transition and the flow finishes
- THEN all transitions occur as without the hook and a dashboard warning appears

### HOOK-R13 Bounded flush on every exit, then stop
On run end (including errors and Open rollback) the engine flushes queues for up
to 10 s, prints a warning on timeout, then stops the dispatcher.

Evidence: engine:internal/engine/hooks.go `stopLifecycleDispatcher`; engine:internal/engine/run.go; engine:internal/engine/open.go; afm:contracts/lifecyclehooks/dispatcher.go `FlushTimeout`

### HOOK-R14 Hook output goes to a per-hook log
Each attempt appends a header and streamed stdout/stderr to
`<runDir>/hooks/<hook-id>.log`.

Evidence: afm:contracts/lifecyclehooks/runner_test.go `TestRunCommand_HeaderRoutedThroughRedactor`; engine:internal/engine/hooks.go `prepareLifecycleDispatcher`

### HOOK-R15 Delivery is live best-effort
Events are not persisted for redelivery; an event not delivered before a crash is
not replayed after restart.

Evidence: afm:contracts/lifecyclehooks/AGENTS.md; afm:docs/lifecycle-hooks.md

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| HOOK-E01 | Hook fails after all retries | `lifecycle_hook_failed` notice; run unaffected | engine:internal/orchestrator/lifecycle_notice_test.go `TestPublishLifecycleHookFailure_Notice` |
| HOOK-E02 | Queue overflow | delivery dropped, `OnError` reported | afm:contracts/lifecyclehooks/dispatcher_test.go `TestDispatcher_QueueOverflowDropsNotBlocks` |
| HOOK-E03 | Flush exceeds 10 s | stderr warning, dispatcher stopped | engine:internal/engine/hooks.go `stopLifecycleDispatcher` |

## Coverage gaps and discrepancies
- **HOOK-G01** (untested): the 10 s flush and its timeout warning have no test.
- **HOOK-G02** (doc-only): no-replay-after-crash (HOOK-R15) is asserted only by docs.
- **HOOK-G03** (untested): emission of `stage_awaiting_quota`/`stage_quota_reset`
  is covered by the mapping table and catalog, not by an end-to-end delivery test.
