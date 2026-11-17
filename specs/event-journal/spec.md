# Event journal: storage, locking and replay

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `events.jsonl` as the run's single source of truth; `state.json` as a derived cache
  (`engine:internal/state`, types in `afm:contracts/state`).
- The run writer lock, read-only replay (`state.LoadRunState`), torn-tail/corruption/unknown-record handling,
  run events and timers on replay, and reading older journals.
- Part of the event-journal family: commit rules (CAS, fences, storage-fatal, operations, notices, redaction) —
  [event-journal-commit](../event-journal-commit/spec.md).
- Out of scope: run resolution and offline ops ([run-lifecycle-resume](../run-lifecycle-resume/spec.md)),
  per-kind control payloads of helpers/mesh ([helpers](../helpers-lifecycle/spec.md), [agent-mesh](../agent-mesh/spec.md)).

## Terms
- **Record** — one JSON line: a stage transition, a run event (stage id empty, `flow_*` boundary) or a control
  record (`kind` + `schema_version`, e.g. `operation_committed`).
- **Torn tail** — a final line without a trailing newline.
- **Projection** — state derived by folding records (`RunState`, budget state, queues, timers).

## Requirements

### LOG-R01 Every record is durable before the write returns
Transitions, control records and run events are appended to `<run>/events.jsonl` and `fsync`ed before the write
call returns. All kinds share one strictly increasing `seq`. A crash after fsync loses nothing on reopen.

Evidence: `engine:internal/state/store_test.go` `TestApply_AppendsToEventsLog`, `TestApply_CrashAfterFsync_Recovers`;
`engine:internal/state/record_test.go` `TestAppendControl_FsyncsBeforeReturn`,
`TestAppendControl_AssignsMonotonicSeqSharedWithTransitions`; `engine:internal/state/run_lifecycle_test.go`
`TestStoreRunEventsAreDurableAndExcludedFromStageHistory`; `engine:internal/state/store.go` `applyTransition`,
`appendControlLocked`, `appendRunEvent`; `afm:docs/internals/event-log.md`.

### LOG-R02 state.json is a best-effort derived cache
After each record the Store rewrites `state.json` atomically (temp file, fsync, rename, directory fsync). A
snapshot failure only prints a warning: the record stays committed and the run continues. Read paths rebuild
state from `events.jsonl`, never from `state.json`.

Evidence: `engine:internal/state/store_test.go` `TestApply_WritesSnapshotJSON`;
`engine:internal/state/journal_regression_test.go` `TestSnapshotWriteFailureNotFatal`;
`engine:internal/state/record_test.go` `TestAppendControl_SnapshotWriteFailureIsNotFatal`;
`engine:internal/state/loadrunstate_test.go` `TestLoadRunState_RebuildsFromLogWhenSnapshotStale`;
`afm:cmd/afm/check_test.go` `TestCheckReadsStatusFromLogNotSnapshot`; `engine:internal/state/store.go` `writeSnapshot`.

#### LOG-S01 Stale snapshot
- GIVEN `state.json` says a stage is `running` while the journal ends with it `done`
- WHEN `afm check` or `LoadRunState` reads the run
- THEN the stage is reported `done`.

### LOG-R03 Exclusive writer lock for the Store lifetime
`state.Open` takes a non-blocking exclusive `flock` on `<run>/.lock` and holds it until `Close`; a second opener
(Store or `TryLockRun`) gets `ErrRunLocked`. The OS releases it on process exit. An I/O error while locking is not
reported as `ErrRunLocked`.

Evidence: `engine:internal/state/store_lock_test.go` `TestOpen_SecondProcessGetsRunLocked`,
`TestOpen_LockReleasedOnClose`; `engine:internal/state/runlock_test.go`
`TestTryLockRun_MutualExclusionWithStoreOpen_LockFirst`, `TestTryLockRun_ReleasedOnClose`,
`TestTryLockRun_IOErrorNotRunLocked`; `engine:internal/state/journal_regression_test.go`
`TestRunLockedForCliWhileRunActive`; `engine:internal/state/runlock.go`.

### LOG-R04 Read-only replay takes no lock
`LoadRunState(runDir)` replays `events.jsonl` without the writer lock and without modifying any file; `afm check`
and other read paths work while a run is active. It returns the prefix before a torn tail with no error.

Evidence: `afm:cmd/afm/check_test.go` `TestCheckDoesNotBlockOnActiveRunLock`, `TestCheck_LoadErrorMatrix`;
`engine:internal/state/state_loadrunstate_test.go` `TestLoadRunState_TornTailReturnsPrefixNoErr`;
`engine:internal/state/state.go` `LoadRunState`.

### LOG-R05 Torn tail is dropped, never repaired by guessing
A final line without newline — even valid JSON — is treated as uncommitted: `Open` truncates the file to the end
of the last newline-terminated record and continues; replay ignores it.

Evidence: `engine:internal/state/store_replay_test.go` `TestOpen_TornTailTruncates`,
`TestOpen_ValidLastLineWithoutNewline_TruncatedNotCorrupted`; `engine:internal/state/store_test.go`
`TestOpen_TruncatesPartialLine`; `engine:internal/state/journal_regression_test.go` `TestTornTailTruncatedSafely`;
`engine:internal/state/state.go` `parseEventLog`.

### LOG-R06 A corrupt complete record is quarantined, not truncated
An undecodable complete line, or a decodable record asserting an impossible projection, stops replay. `Open`
copies the journal to `events.jsonl.corrupt-<unixnano>`, leaves the original intact and fails with
`ErrCorruptLog`; `LoadRunState` returns `ErrCorruptLog`.

Evidence: `engine:internal/state/store_replay_test.go` `TestOpen_MidCorruptionQuarantines`;
`engine:internal/state/journal_regression_test.go` `TestCorruptCompleteLineQuarantined`;
`engine:internal/state/state_loadrunstate_test.go` `TestLoadRunState_MidCorruptionReturnsErr`;
`engine:internal/state/store.go` `Open`.

#### LOG-S02 Mid-log corruption
- GIVEN a journal with a garbage line followed by valid records
- WHEN the Store is opened
- THEN `Open` fails with `ErrCorruptLog` naming the quarantine file, which holds the original bytes.

### LOG-R07 Unknown discriminators fail closed without quarantine
A control record with an unknown required `kind` or unsupported `schema_version` (also inside budget payloads)
yields `ErrUnsupportedRecord`: `Open` fails, `LoadRunState` returns an empty state with the error, the journal is
not quarantined or changed, and nothing is skipped. Unknown optional fields on known kinds are ignored.

Evidence: `engine:internal/state/record_test.go` `TestParse_UnknownKindFailsClosed`,
`TestParse_UnsupportedSchemaVersionFailsClosed`; `engine:internal/engine/budget_public_restart_integration_test.go`
`TestBudgetPublic_UnsupportedJournalIsNotReplacedByANewRun`; `engine:internal/state/record.go` `decodeLine`;
`afm:docs/collaboration-journal.md` "How an unknown record is handled".

### LOG-R08 Legacy state.json migration
Only when `events.jsonl` is absent and `state.json` exists, `Open` writes synthetic `legacy_load` transitions
(pending → recorded status) for known stages and fsyncs them. Such stages report incomplete stage time.

Evidence: `engine:internal/state/store_test.go` `TestOpen_LegacyStateJSONFallback`;
`engine:internal/state/stage_timing_test.go` `TestStageTiming_LegacyFinishedStageIsIncomplete`,
`TestStageTiming_LegacyMigrationLiveEqualsReplay`; `engine:internal/state/store.go` `Open`.

### LOG-R09 Run events and stable start time
`BeginRun`/`EndRun(finished|failed|interrupted)` write run events that share `seq` but are excluded from stage
history; a second terminal event after the run ended is a no-op. The run start time is the time of the first
record of any kind; reopened runs keep `LastSeq` of the journal.

Evidence: `engine:internal/state/run_lifecycle_test.go` `TestStoreRunEventsAreDurableAndExcludedFromStageHistory`;
`engine:internal/state/store_test.go` `TestOpen_RestoresStartedAtFromFirstEvent`, `TestSnapshot_IncludesLastSeq`;
`engine:internal/state/store.go` `BeginRun`, `EndRun`.

### LOG-R10 Timers are projections equal live and on replay
Idle, backoff, elapsed and per-stage time are computed from record times (wall clock, monotonic part dropped) by
the same functions live and on replay. A resume opens a new anchor so offline downtime is not counted; after a
crash the boundary is the last record; an offline CLI transition does not extend idle.

Evidence: `engine:internal/state/run_lifecycle_test.go` `TestRunEvents_CloseAndResumeIdleWithoutCountingDowntime`,
`TestRunEvents_CloseAndResumeBackoffWithoutCountingDowntime`, `TestRunEvents_OfflineStageTransitionDoesNotExtendIdle`,
`TestRunEvents_CrashResumeCountsOnlyDurableActiveTime`; `engine:internal/state/store_test.go`
`TestOpen_ResumeReconstructsIdleAndBackoffFromReplay`; `engine:internal/state/replaytime_test.go`
`TestLoadRunState_PreservesTransitionTime`; `engine:internal/state/AGENTS.md` "Persistent run timers".

#### LOG-S03 Downtime between processes
- GIVEN a run closed while idle and reopened an hour later
- WHEN idle time is read after resume
- THEN it excludes the hour the process was not running.

### LOG-R11 New builds read older journals
Journals without control records, and records lacking later additive fields, replay with zero values and their
original semantics (e.g. legacy `reply_to` waits stay answerable by any sender).

Evidence: `engine:internal/state/record_test.go` `TestParse_LegacyLogUnchanged`; `engine:internal/state/recovery_test.go`
`TestRecovery_OldRunWithoutControlRecords`; `engine:internal/state/usage_limit_test.go`
`TestQuota_OldJournalReplaysWithoutQuota`; `engine:internal/state/mesh_waits_test.go`
`TestMesh_LegacyReplyToWaitKeepsAnySenderRule` (fixtures `testdata/mesh-pre-*`);
`afm:docs/collaboration-journal.md` "Additive fields".

### LOG-R12 Fresh runs record the board protocol first
A run with an empty journal records `mesh_board_protocol_set` (`typed_v1`) before any agent starts; a resumed
run without it stays legacy and the marker is refused there.

Evidence: `engine:internal/state/mesh_board_protocol_test.go` `TestRecordBoardTaskProtocolRefusesOldRun`,
`TestRecordBoardTaskProtocolRefusesReopenedOldRun`;
`engine:internal/engine/open.go` `recordFreshRunProtocol`.

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| LOG-E01 | Run already open by another process | `ErrRunLocked` | `TestOpen_SecondProcessGetsRunLocked` |
| LOG-E02 | Corrupt complete line | `ErrCorruptLog` + `.corrupt-<ts>` copy (Open) | `TestOpen_MidCorruptionQuarantines` |
| LOG-E03 | Unknown kind/schema | `ErrUnsupportedRecord`, journal untouched | `TestParse_UnknownKindFailsClosed` |
| LOG-E04 | `LoadRunState` without `events.jsonl` | file-not-found error | `engine:internal/state/state.go` `LoadRunState` |

## Coverage gaps and discrepancies
- **LOG-G01** (doc-only): `afm:docs/engine.md` says "Recovery tests use copies of 2.x journals"; the baseline
  engine has no 2.x journal fixtures, only legacy-shape unit fixtures (`testdata/mesh-pre-*`, inline logs).
- **LOG-G02** (doc-only): "older binaries cannot read collaboration journals" is a statement about other
  binaries; not testable here.
