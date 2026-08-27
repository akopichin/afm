# AFM Development Guide

## Working directory: `.afm` and `--dir`

By default afm stores runs, flows, and config under `.afm/` in the working directory. The parent directory is resolved in `PersistentPreRunE` (`cmd/afm/main.go`) with priority **flag > env > `.`**: the `--dir` persistent flag, else the `AFM_DIR` env variable, else the current directory. All subcommands read the effective `.afm` path via `fmDir()` (`filepath.Join(rootDir, ".afm")`); `state.FindLatestRunDir(base, flowName)` takes the runs base as an explicit argument instead of hardcoding the path.

**Project root for agents: `flow.root_dir`.** afm assumes the afm-root (the parent of `.afm/`) == the project root. When that isn't true (e.g. Docker: sources in `/workspace`, `.afm/` in another directory), agents — inheriting the CWD of the afm process — resolve relative project paths (`docs/arch/…`) against the wrong root, and stages diverge: one writes a file, another can't find it. The `root_dir` field in `flow.yaml` (`flow.Flow.RootDir`) sets the agents' CWD: a relative path is resolved from the afm-root in `cmd/afm/run.go`, then threaded through `orchestrator.Options.RootDir` → `executor.Config.Dir` → `cmd.Dir` in `executor.run`. Empty → previous behavior (inherit CWD). `AFM_STAGE_DIR` (dialog files) stays anchored to the afm-root regardless of `root_dir`.

**Attributing `agent_action` to a stage.** Agent tool-action events get their `stageID` from the `OnAction` closure of the per-stage runner (`runnerFor`, `runner_factory.go`). The injected `o.runner` (tests, empty stageID) is used ONLY when `opts.Runner != nil` and the stage has no `command` of its own; in production every stage always gets a per-stage runner with the correct `s.ID` — otherwise the stage badge in the dashboard's event feed disappeared (`EventFeedPanel.tsx` renders the badge only when `stageId` is non-empty).

## State persistence & run lifecycle (reliability core)

The event log `.afm/runs/<run_id>/events.jsonl` is the **single trusted source of truth**. `state.json` is a derived cache (it carries `last_seq`); read paths (`afm check`, run lookup) read state from the log via `state.LoadRunState` (no flock), not trusting the snapshot.

- **flock between processes.** `state.Open` takes an exclusive `flock` on `<runDir>/.lock` for the entire lifetime of the `Store`. A live `afm run` holds it; CLI `approve`/`retry`/`revise` against an active run fail with `state.ErrRunLocked` and a clear message. The flock is released by the OS on process exit — a crashed run leaves no "stuck" lock. `afm check` (read-only, no flock) is not blocked by a live run.
- **Non-destructive replay.** A truncated tail (last record without `\n`, crash during append) is safely truncated. A corrupt **complete** line in the middle of the log (with valid records after it) → quarantined into `events.jsonl.corrupt-<ts>` + `state.ErrCorruptLog`, the original is NOT touched (we never truncate destructively).
- **Durable snapshot.** `writeSnapshot` does `f.Sync()` before Close and fsyncs the parent directory after Rename. A snapshot write error is non-fatal (it's a cache), but read paths take state from the log anyway.
- **Unique run-id** — `<flow>-<timestamp>-<rand4hex>` (no collisions within the same second). `state.FindLatestRunDir` anchors the prefix (after `<flow>-` there must be a digit — `foo` doesn't match `foo-bar`); `state.FindLatestRunForStage` is the single point for finding a run by stage from the log.
- **Storage-fatal terminates the run.** `Trigger` via `errors.As(*StorageError)` distinguishes a real log-write failure (→ `setFatal` + cancel run-ctx → `Run` returns an error) from the benign `ErrConcurrentChange` (CAS-mismatch, silent no-op) and `ErrNoRule` (log-and-drop, doesn't fail the run).
- **Clean shutdown.** All agent goroutines are launched via `spawnAgent` (semaphore + activity marker + `agentWG`). On exit `Run`: `cancel()` (LIFO — earlier) → `waitAgents()` (bounded 10s) → then `store.Close()`. Agent completions are published under the run-ctx (not `context.Background()`) — they don't block forever on a dead bus.
- **Durable approve/revise/retry.** `Approve`/`Revise`/`Retry` are synchronous: the durable transition is committed to the log BEFORE returning (a crash doesn't lose intent — recovery resumes `ready`/`revising`/`running`). Headless auto-approve is handled inline (no blocking self-publish in the event loop). **Important:** HTTP-initiated approve/revise/retry spawn the agent under the **run-scoped ctx** (`runContext`), not under `r.Context()` — otherwise net/http cancels the ctx when the handler returns and the agent is killed instantly. Spawns reachable both from the HTTP goroutine and from the event loop are guarded by the CAS result of `Trigger` (no double launch).
- **`startReadyStages` honors `autonomous.flag`.** The CAS-guard on `EvStartRun` only prevents re-launching the SAME agent — but it doesn't guarantee that the code that won the race knows which agent to launch. `retryStage` for an autonomous stage moves it `Pending → Ready` via `EvReady` and then takes `EvStartRun` itself — in the narrow window between these two transitions, a concurrent call to `startReadyStages` from another event-loop branch (e.g. `onAgentCompleted` for another stage) could win the CAS first and blindly launch `runImplementationAgent` (which reads `plan.md`, which an autonomous stage doesn't have → "no such file or directory" crash). So before spawning, `startReadyStages` checks `isAutonomousStage` and launches `runAutonomousAgent` for such stages — symmetric to the existing checks in `recovery.go` and in `retryStage` itself.
- **Hard autonomous track: `agents: [auto]`.** A stage can statically declare an autonomous track in YAML — `agents: [auto]` (type `flow.AgentAuto`, detected by `Stage.IsAuto()`). Such a stage goes to `runAutonomousAgent` DIRECTLY — this is the only way onto the autonomous track (the LLM-supervisor, which used to be able to make the same decision dynamically, was removed as redundant: practice showed a static `agents: [auto]` is enough). Activation is the shared helper `activateAutoStage` (writes `autonomous.flag` + `EvReady`, WITHOUT `plan.md`), called from BOTH activation paths of a no-planning stage: `tryActivatePrePlanned` (scheduling.go) and `startPlanningForPending` (recovery.go) — otherwise, on a fresh/zero-dep start, the recovery branch would try to copy a nonexistent `plan.md`. `startReadyStages` additionally honors `stage.IsAuto()` (a safety net in case the flag wasn't written). A short-circuit in `flow.HasAgent`/`ImplAgent` prevents treating `auto` as a custom implementation agent. Validation in `ParseFile`: `auto` must be the only agent. Spec/plan: `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Persistent IDLE/BACKOFF footer metrics

The dashboard footer (`PROGRESS`/`STARTED`/`ELAPSED`/`IDLE`/`BACKOFF`) survives reload and afm restart with the same accuracy `STARTED`/`ELAPSED` already had, and does not tick while the WebSocket is offline.

- **Accumulators in `RunState`, not events.** `pkg/state/state.go` stores `IdleAccumulatedMs`/`BackoffAccumulatedMs` (`int64`, persisted into the snapshot and restored by replaying `events.jsonl`). `IdleSince() *time.Time`/`BackoffOpenSince() []time.Time` are not persisted — they're computed on read from `Stages[].UpdatedAt` (idle — the maximum across all stages; backoff — the `UpdatedAt` of each stage whose `Status == StatusRetrying`).
- **One helper, two call sites.** `accountIdleAndBackoff(rs, stageID, to, t)` updates both accumulators using the state of `rs.Stages` BEFORE applying the transition. It's called from `Store.Apply` (live path) AND from `parseEventLog` (replay path of `Store.Open`/`LoadRunState`) — these are **different** functions, `parseEventLog` does not go through `Apply`. Forgetting one of the two call sites means the numbers diverge between a live run and its restart.
- **Idle is a flow-wide state**, not per-stage: idle if any stage is in `awaiting_user_input`/`awaiting_approval`, OR there's a `failed` stage and no stage is `running`/`planning`/`revising` (`retrying` does NOT count as active — it's passive backoff, not agent work).
- **Backoff is summed in parallel**, not merged: if several stages are in `retrying` at once, their intervals are added independently (the same simplified model the old `useStatusDuration` had).
- **`Store.Apply` takes `t.Time` once.** Before the fix, `SetStageStatus` called `time.Now()` again internally after fsync — the discrepancy between the logged transition time and the time used for accounting corrupted accuracy by the duration of the fsync. `SetStageStatusAt(t.Time)` uses exactly the timestamp that went into the log.
- **`NewRunState` does not stamp `UpdatedAt` for pending stages.** Otherwise, on every `Store.Open` (including on resume, when "now" is the moment of process restart, not the moment of the real last transaction), an untouched pending stage would get `UpdatedAt = time.Now()` and would always dominate the real historical transitions in `maxUpdatedAt()` — silently corrupting `IdleSince`/undercounting Idle after every restart.
- **API:** `/api/status` returns `idle_accumulated_ms`, `idle_since` (omitempty), `backoff_accumulated_ms`, `backoff_open_since` (`[]time.Time`, empty array if there are no open episodes right now).
- **Frontend — anchor + tick, no event-replay.** `useIdleMs`/`useBackoffMs` (`pkg/web/dashboard/src/hooks/`) compute `accumulated + (now - since)`, like the already-working `useElapsed`, and take `connected: boolean` — when `false` the ticker freezes at the last value; on reconnect the `useStatus` poll pulls in the anchor already corrected by the server, so no client-side reconciliation is needed. They replaced `useIdleTime`/`useStatusDuration` (event-replay over `useEventFeed`'s 200-event cache — on long runs old transitions fell out of the cache and IDLE/BACKOFF silently undercounted after reload).
- Spec/plan: `docs/superpowers/specs/2026-08-07-persistent-idle-backoff-design.md`, `docs/superpowers/plans/2026-08-07-persistent-idle-backoff.md`.

### Stage order in the dashboard — topological, not declaration order

The stage list on the left of the dashboard is rendered in the order returned by `GET /api/status` (`stages []StageView`) — this used to be exactly `state.RunState.StageOrder`, i.e. the declaration order in `flow.yaml`. A stage declared in YAML before its own dependency (for flow readability) was drawn above it, even though it actually starts only once the dependency finishes — the list didn't reflect the execution graph.

- **`buildStageViews` (`pkg/server/stageview.go`) recomputes the order via `topoOrder`** — a stable variant of Kahn's algorithm: the ready-node queue is seeded and refilled in the order of the original `StageOrder`, so independent stages with no relation between them keep their mutual declaration order (they aren't shuffled by map iteration), while a dependent stage renders right after ALL of its `depends_on`, not before unrelated neighbors just because it was declared earlier.
- **Example:** `stage1 (deps:[stage2]), stage2, stage3, stage4, stage5, stage6 (deps:[stage2,3,4,5])` in the declaration → renders as `stage2, stage3, stage4, stage5, stage1, stage6`.
- **`state.RunState.StageOrder` is not touched** — it's the authoritative order for `state`/`scheduling` (log replay, CAS transitions, etc.); `topoOrder` is a pure display layer on top of it, living only in `pkg/server`.
- The new `Server.stageDependsOn`/`Config.StageDependsOn` (`pkg/server/server.go`) is populated from `flow.Stage.DependsOn` in `cmd/afm/run.go`, next to the existing `stageInteractive`/`stageAutoApprove`.
- A defensive fallback in `topoOrder`: if the result doesn't cover all ids (a cycle or a reference to a nonexistent stage), it returns the original order as-is — unreachable in practice, `flow.ParseFile`'s `detectCycles` already rejects such flows at parse time.

### Auto-advancing the selected stage in the dashboard — retry on every poll, not a one-shot check

`App.tsx` holds the user's selection (`selectedStageId`) and automatically advances it to the next active stage when the stage currently being followed finishes — but it doesn't move the user if they manually opened an already-finished stage (to look at its plan/log/dialog).

- **`wasLive` — a per-selection flag, not per-tick.** Advancement used to be checked EXACTLY on the tick when the selected stage transitioned `!done → done` (comparing with the previous status). On script stages (`Stage.IsScript()`, `pkg/flow/flow.go`) `running` can last a fraction of a second — several stages in a row can pass `running → done` BETWEEN two `/api/status` polls (every 3s). By the time the frontend finally sees "stage1 became done", stage2 is also already done — there's nothing left among `ACTIVE_STAGE_STATUSES` to look for, and the old check gave up FOREVER (the next poll no longer re-ran it, since the status was already done) — the selection stuck on stage1, even though stage3/4 was actually working.
- **Fix:** `wasLive.current` lives as long as the current `selectedStageId` doesn't change (it's reset only on selection change, not on every poll). As long as the stage is done and was seen "live" under this selection, the search for the next active stage repeats on EVERY poll, not once. It self-corrects within one polling cycle instead of sticking irreversibly. A manual click on an already-finished stage never sets `wasLive` → its selection isn't touched (previous behavior preserved).

## Paused Stage Status

The new stage status `paused` covers three scenarios: (1) `auto_run: false` in `flow.yaml` gates the first activation of a stage (the stage doesn't start on its own, it waits for Continue); (2) manual pause via the "Pause" item in the stage's kebab menu, available for `running`/`planning`/`revising`/`retrying`; (3) script stages (`script:`) can only be paused via (1) — a mid-script graceful stop is architecturally unsupported (`RunScript` doesn't accept an interrupt channel).

- **The `auto_run` field in `flow.yaml`** (`flow.Stage.AutoRun *bool`, yaml `auto_run,omitempty`) is a boolean pointer, not a plain `bool`: you need to distinguish "field not set" (`nil`) from "set to `false`", otherwise an omission in YAML is indistinguishable from an explicit disable. `nil` (not set) or `true` — previous behavior, the stage starts on its own as soon as its `depends_on` are satisfied. Only an explicit `auto_run: false` moves the stage straight to `paused` (`PausedFrom: pending`) instead of starting — it waits for a Continue click in the dashboard. Legal on a stage of ANY type: regular (planning/implementation/review), `agents: [auto]`, `script:` — the gate fires before the launch type is determined. Checked by the helper `(Stage) AutoRunDisabled() bool` (`pkg/flow/flow.go`); the gate is applied via `(o *Orchestrator) shouldGateAutoRun` (`scheduling.go`) at both first-activation points (`tryActivatePrePlanned` and the recovery path `startPlanningForPending`). **It fires once**, only on the very first activation of a stage — see `PausedFrom` below for how this is guaranteed.

- **`state.StageState.PausedFrom` — a dual-purpose field, not cleared on exit from paused.** It stores the status the stage left when it went to pause (`running`/`planning`/`revising`/`retrying`/`pending`). While the stage is `paused` — that's where to resume to. After Continue (when the status is no longer `paused`) the field stays non-empty FOREVER — it's a permanent marker "this stage has already gone through the pause cycle at least once", which `shouldGateAutoRun` (`scheduling.go`) uses so that `auto_run:false` fires only on the very first activation, not on every re-entry into `pending` after `failed`→retry. `pkg/server/stageview.go`'s `StageView.PausedFrom` (JSON `paused_from`) does NOT inherit this permanence — it's populated only when `Status == StatusPaused`, otherwise an empty string, so as not to expose in the API a stale value from a stage that left pause long ago.
- **`Continue` == "pretend afm just restarted and found this stage in status `PausedFrom`".** `Orchestrator.Continue` (`control_api.go`) for `PausedFrom == pending` re-runs normal activation (`tryActivatePrePlanned`+`startPlanningForUnblocked`, the gate won't let itself through again thanks to the non-empty `PausedFrom`); for the other four statuses — `resumeStageAtStatus` (`recovery.go`), the SAME dispatcher `startPlanningForPending` already uses to resume a stage after an afm crash. A manual pause and "the process crashed, afm was restarted" are the same situation from the scheduler's point of view: "the process implied by this status is not running right now — launch it".
- **Manual pause reuses `interruptChans`** — the same channel/mechanism (SIGINT + 15s grace) `Revise` already uses for agent_suggest on a `running` stage. `Orchestrator.Pause` (`control_api.go`) synchronously commits `EvPause` to the log (before signaling the channel) — the durable transition earlier than the async effect, the same pattern as `Approve`/`Revise`/`Retry`. On `ErrUserInterrupted`, `runWithRetry` (`retry.go`) checks `currentStatus == paused` to distinguish "this is a Pause" (resume nothing, the transition already happened) from "this is a Revise" (restart with feedback) — both use the same channel. For status `retrying` (a passive backoff timer, no live process) a `case <-interruptCh:` branch was added to `runWithRetry`'s backoff-select — `Revise` can't reach there (its precondition is only `awaiting_approval`/`running`), so a signal in that select unambiguously means `Pause`.
- **`withBeforeHook` (`hooks.go`) re-checks the status after `script_before`.** `script_before` runs as a bare shell script WITHOUT an interrupt channel (that's registered only inside `runWithRetry`, for the main agent, AFTER the hook) — `Pause()` could successfully move the stage to `paused` while the hook was still running in the background, and on its completion `mainFn` would launch on top of an already-paused stage (plus a second time — if the user managed to press Continue earlier). Fix: `withBeforeHook` doesn't call `mainFn` if `currentStatus(s.ID) == StatusPaused` after the hook.
- **The "Pause while queued behind the semaphore" race is closed centrally in `concurrency.Manager.SpawnAgent`, not with patches across the calling code.** Initially (found by a separate "are there any FSM leaks?" audit) this race was fixed with a pinpoint `currentStatus == paused` check at the start of `runWithRetry` — a working but scattered fix across two places (the second being `withBeforeHook`). In answer to "why are there several leak holes, can't we have one control point?" the logic was moved one level down: `Manager` (`pkg/orchestrator/concurrency/concurrency.go`) got a field `shouldRun func(stageID string) bool` that `SpawnAgent` checks RIGHT AFTER `sem.acquire()`+`markActive`, BEFORE calling `run(ctx, s)` — i.e. as close as possible to the point where the goroutine actually wakes after a (possibly long) wait for a slot in the `max_parallel` semaphore. `orchestrator.New` passes the closure `func(id string) bool { return opts.Store.Get(id) != state.StatusPaused }` to `concurrency.New` (the only production call site, `pkg/orchestrator/orchestrator.go`). This is the single point for ANY agent launch path (planning/implementation/review/autonomous, fresh or `*WithFeedback` resume, via `startReadyStages`/`retryStage`/`resumeStageAtStatus`/HTTP handlers) — since all of them are already required to go through `SpawnAgent` (that's its original purpose, see "Clean shutdown" above). The check in `runWithRetry` became redundant and was removed along with its test; the check in `withBeforeHook` **remains** — it protects a structurally different window (the execution time of the `script_before` hook itself, which lies INSIDE `run(ctx,s)`, already past the single SpawnAgent check, where `shouldRun` can't look). The regression test for the race itself is `TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore` (`pkg/orchestrator/concurrency/concurrency_test.go`), which reproduces the real scenario via `ChannelSemaphore` (the same technique as the existing `TestSpawnAgent_BlocksOnFullSemaphore`): a goroutine queues on a busy semaphore, `shouldRun` flips to false, the semaphore frees up — `run` must not be called.
- **`resumeStageAtStatus`'s `StatusRetrying` branch checks autonomous/`agents:[auto]`**, symmetric to the existing check in the neighboring `StatusRunning` branch — without it, Continue after a manual pause of an autonomous stage during retry backoff sent it into `EvStartPlanning`+a planning agent, even though autonomous stages never have a `plan.md` or planning at all (a bug specific to the new Continue path — previously this `resumeStageAtStatus` branch was only called from `startPlanningForPending`, which an autonomous stage never reaches: the first switch of the same function intercepts it).
- **`pkg/server/stageview.go`'s `ShowPlan` accounts for `paused`, not just `failed`** (`showPlan := !autonomous || failed || paused`) — otherwise an autonomous stage (`agents:[auto]`), especially with `interactive: true`, rendered only `DialogChannel` when paused, with not a single visible Continue button (PlanPanel, where it lives, wasn't mounted at all). Found on a real user flow — the rule for `showPlan` was written in a world where an autonomous stage could reach `failed`, but not `paused`.
- **`use-attention.ts`**: `paused` is part of `ATTENTION_STATUSES`/`AttentionKind` (the header "Action needed" dot, favicon pulse, title flash, desktop notification titled "Stage paused" in `use-desktop-notifications.ts`'s `Record<AttentionKind, string>` — TS won't let you forget to add the new kind there, the exhaustiveness-check of the type itself).
- **Found by a live manual run (a real glm47 agent, a real browser): `Continue()` could hang dependent stages forever.** `resumeStageAtStatus`'s "already finished, recovered from disk" fast-paths (autonomous `execution_summary.md` / script `.done`, both the `StatusRetrying` and `StatusRunning` branches) finalized the stage itself with a bare `Trigger(EvComplete)` + `maybeRunAfterHook`, bypassing the cascade `failBlockedStages`/`startPlanningForUnblocked`/`startReadyStages`/`tryActivatePrePlanned` that normal agent completion (`onAgentCompleted` → `completeStage`) always carries. From the bootstrap path (`startPlanningForPending`) this was harmless — that one runs the whole cascade ONCE after looping over ALL stages of the flow, so skipping the cascade inside one iteration broke nothing. But `Continue()` resumes exactly one stage and returns immediately — if it hit the fast-path (a typical scenario: the agent already finished writing `execution_summary.md`, and `Pause()` fired a fraction of a second later), nobody ever re-evaluated the stages waiting on it as a dependency — they hung in `pending` forever, until you manually restart the whole afm process. Fix: both "recovered" fast-path branches in `resumeStageAtStatus` now call the same `completeStage(ctx, s.ID, status, reason)` (which gained a `reason` parameter, so as not to lose text like "recovered execution_summary.md" in the log), instead of duplicating its body by hand. The analogous branch inside `startPlanningForPending`'s own loop was NOT touched — it's provably safe thanks to the post-loop cascade, and touching it would mean calling `startReadyStages` etc. twice without a driving test for a real problem. Regression: `TestContinue_RecoveredCompletion_UnblocksDependent` (`pause_continue_test.go`) — runs `orch.Continue` on a stage with an already-written `execution_summary.md` and checks that the DEPENDENT stage also reaches `done`, not just the resumed one; the test initially flaked (it passed for the wrong reason due to a race between `go func(){ orch.Run(ctx) }()` and the immediate `orch.Continue()`) — stabilized with an explicit `time.Sleep` after starting `Run`, to guarantee it hits the `Continue()` path, not the bootstrap-loop cascade.
- Spec/plan: `docs/superpowers/specs/2026-08-17-paused-stage-status-design.md`, `docs/superpowers/plans/2026-08-17-paused-stage-status.md`.

## Pre-note: a note for a stage before it starts

You can attach a note to a stage that hasn't **started yet** (`pending`) — as work progresses you sometimes realize the next stage needs to take something into account, and you can add it right away without waiting for it to launch. The note is spliced into the agent's context on its **first** start — as part of the original task, not a correction to work already begun (unlike `agent_suggest`/`Revise`, which restarts an ALREADY-running agent via `feedback.md`).

- **A separate file `<stageDir>/prenote.md`, not an event and not FSM.** The key simplification: a pre-note doesn't change the stage's status and doesn't create new FSM events — `pending` stays `pending`. `state.SavePreNote`/`state.LoadPreNote` (`pkg/state/state.go`) write/read the file directly (the same technique `SaveFeedback` uses to write `feedback.md`). One editable field: saving **replaces** the text (not append, unlike `feedback.md` with its revision separators), saving empty/whitespace-only **deletes** the file (the user changed their mind → cleared the field). The write is atomic (temp+rename); `stageDir` may not exist yet (`MkdirAll` inside `SavePreNote`) — a pending stage often has no directory on disk. It lives within the current run: it survives afm reload/restart (the file on disk), but a new run doesn't inherit it; after the stage starts, the file is NOT deleted — the 📝 indicator stays as a marker "a note was applied".
- **The splice is the shared helper `(*Orchestrator).preNoteBlock(stageDir)`** (`pkg/orchestrator/agents.go`): it reads `prenote.md` and returns the block `\n\n## User note (added before this stage started)\n\n<text>` or `""`. It's called on the first (fresh) start of ALL four agent runners — `runPlanningAgent`/`runImplementationAgent`/`runReviewAgent`/`runAutonomousAgent` — and is appended to `RetryContext` exactly the way the `*WithFeedback` runners append their `feedbackNote`. The `*WithFeedback` runners do NOT splice the pre-note (that's a restart, not a first start).
- **HTTP `POST /api/stages/{id}/note`** `{"note":"..."}` (`handleStageNote`, `pkg/server/handlers.go`; route in `server.go`): the gate — status `pending` AND the stage isn't a script (a script has no agent to splice into — symmetric to the `!isScript` gate on "Add note for agent"), otherwise `400`. An empty `note` → deletion. The write goes directly through `state.SavePreNote` (not through `StageActions` — the orchestrator doesn't participate in the write, it only needs the READ side at start). The text is returned in the `pre_note` field (omitempty) of `StageView`/`GET /api/status` (`pkg/server/stageview.go`) — it both prefills the edit modal and serves as the signal for the 📝 indicator.
- **Dashboard:** `AgentNoteModal` got a `prenote` variant (`variant`/`initialNote`) — its own copy line "…added to the agent's context when the stage starts", prefill with the current text, a "Save" button, empty submit allowed (= delete). `StagesList`: `canPreNote(stage)` (`pending` && !`isScript`) adds the menu item "Add note (before start)" / "Edit note (before start)" (the label depends on whether a note exists); `hasKebab(stage)` shows the kebab on a pending stage ONLY if pre-note is available — otherwise a pending script would draw an empty menu. The 📝 indicator (`prenote-badge`) is in the stage row when `preNote` is non-empty. `App.tsx` keeps a separate `preNoteModalStageId` (it doesn't reuse `noteModalStageId` from `agent_suggest`): a different modal variant, a different handler, and `handleSubmitPreNote` on error (e.g. the stage managed to leave `pending` → `400`) doesn't silently close the modal.
- **Verified live** (afm locally, mock agent, real dashboard via Chrome DevTools): the textarea/kebab/indicator/prefill/clear, writing `prenote.md`, and — most importantly — the splice into the real prompt of an autonomous agent (the block `## User note (added before this stage started)` in `autonomous.prompt.log` under `--debug`). Tests: `TestSavePreNote_SaveLoadClear` (`pkg/state`), `TestHandleStageNote_{SaveAndClear,RejectsNonPending,RejectsScriptStage}` (`pkg/server`), `TestIntegration_PreNoteReachesFreshPrompt` (`pkg/orchestrator`), plus frontend cases in `StagesList.test.tsx`/`use-status.test.ts`.

## File-Based Dialog Protocol (Interactive Stages)

The interactive dialog system was refactored from an MCP HTTP server to a file-based protocol starting with the planning-depends-on-ref branch. This enables agents to ask users questions and receive answers through simple file I/O instead of HTTP.

### Architecture

**Agent writes question:**
- Agent writes: `$AFM_STAGE_DIR/<phase>.<id>.question.json`
- Example: `planning.q1.question.json`
- Format: `{"id":"q1", "question":"...", "options":[...], "allow_custom":true/false}`

**Agent polls for answer:**
- Bash loop in agent script: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
- When file appears, agent reads it and continues
- Format: `{"id":"q1", "answer":"...", "from_options":true/false}`

**Orchestrator polls for questions:**
- `startQuestionPoller()` launches goroutine that scans every 1 second
- Detects `*.question.json` files in stage directories
- Publishes `EventAskUser` to transition stage to `awaiting_user_input`
- UI dashboard polls `/api/stages/<id>/dialog` to fetch questions

**HTTP handler processes answer:**
1. Validates phase is one of: `planning`, `implementation`, `review`
2. Validates ID is safe filename component (no path traversal)
3. Checks question.json exists (else 404)
4. Rejects if answer.json already exists (409 Conflict)
5. **Atomically writes** answer.json (O_EXCL exclusive create) — critical path
6. Appends to dialog.jsonl for UI history (best-effort, non-critical)
7. Calls `NotifyAnswer()` to either transition FSM (if agent active) or restart agent (if exited)

### Key Files

| File | Responsibility |
|------|-----------------|
| `pkg/mcp/dialog.go` | `FindUnansweredQuestions()`, `QuestionFile` type, `appendLine()` for dialog.jsonl |
| `pkg/orchestrator/orchestrator.go` | `startQuestionPoller()`, `NotifyAnswer()`, `pollQuestions()`, active agent tracking |
| `pkg/executor/executor.go` | Passes `AFM_STAGE_DIR` environment variable to agent process |
| `pkg/server/handlers.go` | `handleDialogAnswer()` with atomic write pattern (O_EXCL) |
| `pkg/prompts/builder.go` | Interactive rules instruction in system prompt |

### Implementation Details

**Agent Activity Tracking**
- `Orchestrator.activeAgents` is a `sync.Map` tracking which stages have running agent goroutines
- Goroutine acquires semaphore → calls `markAgentActive(stageID)` → deferred `markAgentDone(stageID)`
- If user answers while agent is active: `NotifyAnswer()` transitions FSM, agent bash loop detects file
- If user answers after agent exited: `NotifyAnswer()` publishes to critical bus → `onUserAnswered()` restarts agent

**Question File Naming**
- Format: `<phase>.<id>.question.json`
- Phase must be: `planning`, `implementation`, or `review`
- ID must pass `isValidDialogID()` check (safe filename, no path traversal)
- Enforces: alphanumeric + underscore only

**Answer Delivery Guarantee**
- Answer.json is written atomically (O_EXCL exclusive create) BEFORE FSM transition
- Agent bash loop will always find the file if still running
- If agent already exited, restart with `--resume` flag to re-read answer

**Dialog History**
- `<phase>.dialog.jsonl` appended for UI (best-effort, NOT critical)
- Stores: `{"timestamp":"...", "phase":"...", "role":"assistant|user", "message":"..."}`
- If append fails, agent continues anyway (answer.json already safe on disk)

### Deleted Components

- `pkg/mcp/server.go` — MCP HTTP server (replaced by polling)
- `pkg/mcp/server_test.go` — MCP server tests
- `pkg/orchestrator/mcp_notifier.go` — MCP event notifier

### Environment Variables

| Variable | Purpose | Set By |
|----------|---------|--------|
| `AFM_STAGE_DIR` | Stage directory for question/answer files | `executor.New()` when `StageDir` configured |
| `AFM_DIR` | Parent directory for `.afm` (used when `--dir` is not set) | CLI flag `--dir` / env, resolved in `PersistentPreRunE` |
| `AFM_DEBUG` | Log the exact agent input (prompt) to `<run>/debug.log` + per-stage `<run>/<stage>/<phase>.prompt.log`. Off by default. | CLI flag `--debug` / env (flag > env), resolved in `PersistentPreRunE`; wired via `Options.Debug` → `executor.Config.Debug`/`RunDir`/`StageID` |

### Testing File-Based Dialog Locally

Mock agents must implement their own polling:
```bash
while [ ! -f "$AFM_STAGE_DIR/<phase>.q1.answer.json" ]; do 
  sleep 0.5
done
answer=$(cat "$AFM_STAGE_DIR/<phase>.q1.answer.json" | jq -r '.answer')
```

Write question.json with proper schema:
```json
{
  "id": "q1",
  "question": "What should we do?",
  "options": ["Option A", "Option B"],
  "allow_custom": true
}
```

### Debugging Interactive Stages

Check stage directory for dialog files:
```bash
ls -la .afm/runs/<run_id>/<stage_id>/
# Look for: planning.q1.question.json, planning.q1.answer.json, planning.dialog.jsonl
```

Common patterns:
- **Agent waiting:** `*.question.json` exists, but no corresponding `*.answer.json` yet
- **Answer received:** Both `*.question.json` and `*.answer.json` exist, agent should have exited
- **Dialog history:** Check `*.dialog.jsonl` for full Q&A history (safe to ignore if missing)
- **Agent error / hung:** Agent stdout (tool actions) is in `<phase>.log`; agent **stderr** (claude diagnostics, e.g. `stream-json requires --verbose`) is in `<phase>.stderr.log`. The bash polling loop times out after the executor's idle timeout (30 min default).
- **Misplaced question (auto-relocate/normalize):** `relocateMisplacedQuestions` (`pkg/orchestrator/orchestrator.go`, reads Write events from `<phase>.jsonl`) fixes two ways to "hide" a question file from the poller, both leading to the stage hanging forever: **(1) wrong directory** — `*.question.json` written OUTSIDE `$AFM_STAGE_DIR` (a GLM-4.7 bug: path from CWD instead of env); **(2) wrong prefix** — the file is inside stageDir but named after the stage id (e.g. `commit-changes.q1.question.json`) instead of the canonical phase (`planning.q1.question.json`), while `FindUnansweredQuestions` only matches `planning`/`implementation`/`review`/`autonomous_execution`. In both cases the file is normalized to the canonical name `<phase>.<id>.question.json` (the correct phase is taken from whose `<phase>.jsonl` the Write was found in, not from the wrong prefix) + a dangling symlink is created at the path the agent polls (its directory + its prefix) → the canonical `<stageDir>/<phase>.<id>.answer.json`, so the bash polling loop finds the answer. The stage goes to `awaiting_user_input` instead of hanging. The first layer of defense is the prompt itself (`pkg/prompts/builder.go`, `<interactive_rules>`) with a targeted constraint "the prefix is the phase, NOT the stage id". (The previous behavior — fail-fast via `detectDialogViolation` — was replaced by relocate.) On a manual retry, `<phase>.session.json` and `<phase>.jsonl` are cleared.

- **Reusing a question id after it's answered — `pollQuestions`'s `processed` map must forget answered keys.** Found by dissecting a real production stage (`agents:[auto]`, the `goga-brainstorm` revision cycle): the prompt requires "never reuse an ID within a phase", but the real agent reused the same id (`q2`) anyway for a SECOND, semantically different question after the first `q2` was answered and the agent resumed work with the user's feedback (confirmed byte-for-byte against `autonomous.log`: `Write q2.question.json` → `cat q2.answer.json` → edits per feedback → `Write q2.question.json` AGAIN → `rm -f q2.answer.json; while ...`). `processed[stageID|phase|id]` — an in-process in-memory map living for the whole lifetime of the polling goroutine — once set to `true`, was never reset, so the second, genuinely unanswered appearance of `q2` was INVISIBLE to the poller FOREVER: no `EvAskUser`, no `dialog.jsonl` entry, the stage hangs in `running` with a real unanswered `question.json` on disk. A page reload doesn't cure it — the bug is server-side, in-memory, not in the frontend; only a full restart of `afm run` helped (the map is recreated from scratch). Fix: before processing each tick, `pollQuestions` removes from `processed` any key of this stage that's no longer in the current unanswered list (`mcp.FindUnansweredQuestions`) — i.e. as soon as a question is really answered and disappears from the unanswered set, its key is forgotten, and a repeat appearance of the SAME id as unanswered triggers `EvAskUser` again. Regression test: `TestPollQuestions_ReusedIDAfterAnswerAsksAgain`.

- **Malformed `question.json` — a torn-read race, not (only) an agent error; a unified repair mechanism "library → fresh agent → terminal fallback" for ALL stage types.** Dissecting a real log: `FindUnansweredQuestions` couldn't parse `question.json` even through `jsonrepair`. A byte-for-byte comparison showed: the "corrupt" preview captured in `dialog.jsonl` is an exact prefix of the LATER valid, complete content of the same file, which never actually changed — i.e. the poller read the file while the agent's `Write` tool hadn't yet reached disk (a torn read), not that the agent really broke the JSON.
  - **The root cause of the incident that prompted the rewrite: the malformed branch was `interactive`-only, and a non-interactive (`agents:[auto]`) stage hung FOREVER.** In production the stage `architecture-review` (`agents:[auto]`, WITHOUT `interactive:true`) reached question `q4` whose `question.json` didn't parse even after jsonrepair. The old `pollQuestions` invoked the repair state machine only under `if stage.Interactive`, and for non-interactive did `continue` — while the auto-answer branch (`PickAutoAnswer`), which the old comment counted on ("non-interactive is tolerant anyway"), sits LOWER and requires an already-parsed question: the flow never reached it on `Malformed`. Result: `answer.json` was never written, the agent's bash loop polled the file for hours (in the log — from 03:27 to 07:28+, every ~10 min), the stage hung in `running`. A human effectively "nudged" the agent by hand via the kebab menu ("fix it"), and it rewrote the JSON — which suggested the final solution: the repair mechanism must be unified for both stage types, and the interactive/non-interactive difference only matters in the TERMINAL step.
  - **`QuestionFile.Malformed bool`** (`pkg/mcp/dialog.go`) — for "didn't parse even after repair", `FindUnansweredQuestions` does NOT touch the file on disk (only this branch; "repair worked" still persists the fixed JSON), it just marks `Malformed: true`. The decision "race or real breakage" is left to the caller.
  - **A unified state machine in `handleMalformedQuestion`** (`pkg/orchestrator/dialog_poller.go`), invoked for ANY stage type (gate `if stage != nil`, no `Interactive`): (1) **jsonrepair (library)** — already ran inside `FindUnansweredQuestions`; reaching here → it couldn't cope; (2) **grace tick** — the first appearance of corrupt bytes is just remembered (`malformedQuestionState.lastRaw`); if it's a torn read — on the next tick (1s) the file is already fully written, parses, `Malformed` is no longer set, and the state machine isn't involved at all; (3) **fresh repair agent** — the same corrupt bytes on the SECOND tick (write complete) → `spawnJSONFix` (`runJSONFixAgent`) launches a SEPARATE isolated agent with a clean context and the single task "read this one file, fix the JSON, save it valid under the same id" (`buildJSONFixPrompt`); up to `maxJSONFixAttempts` (3) times; (4) **exhausted** → terminal fallback: interactive → `giveUpOnMalformedQuestion` persists a valid stub (`Options:nil`, `AllowCustom:true`, the raw text as the explanation) + `EvAskUser` to the human; non-interactive → `autoAnswerMalformed` writes `answer.json` from `PickAutoAnswer`'s no-options fallback, so the stage does NOT hang (the FSM is untouched). The frontend already supports an options-less question (`DialogChannel.tsx`).
  - **Why a fresh agent, not an in-context nudge to the same agent (the previous implementation).** The old mechanism sent the agent, via its own `answer.json`, a request to rewrite the file (a nudge). The user chose the fresh-agent approach: an agent with a clean context isn't distracted by its main task and sees only the corrupt file, so it's more reliable (and fewer attempts are needed — 3 vs the old 5 nudges). A side but important win: the fresh agent rewrites `question.json` IN PLACE and does NOT write any synthetic `answer.json` — which means a whole class of bugs "stale tracking state deletes a real answer that arrived later" (which the old nudge path had to work around via `unblockRewrittenMalformedQuestions`) became impossible by construction. Reconciliation collapsed to the tiny `reconcileMalformedFixes`: drop the key as soon as the file parses again (or is gone).
  - **Isolation of the fresh agent (`runJSONFixAgent`):** a fresh session (`SessionID`/`Resume` not set — the point is precisely a clean context, unlike `runnerFor`, which does `--resume` for interactive); WITHOUT `StageDir`/`AFM_STAGE_DIR` (the agent isn't a dialog participant, it only edits one file at an absolute path from the prompt, it can't write a new `question.json` itself); via **`concurrency.SpawnDetached`**, NOT `SpawnAgent` — the stage's main agent is blocked waiting for an answer to this very question and holds the command semaphore slot, so `SpawnAgent` would (1) jam on the full semaphore and (2) overwrite the main agent's active marker via `markActive`. `SpawnDetached` accounts for the goroutine in `agentWG` (clean shutdown) but doesn't take the semaphore or touch the active marker. The fix agent's completion is caught via a done channel (`malformedQuestionState.done`); no separate timeout is needed — `RunAgent` is already bounded by the idle timeout. The fix agent's log is a separate `<phase>.<id>.jsonfix.log`, so its tool actions don't land in the stage's `<phase>.jsonl` (event feed / `WrittenFiles`).
  - Tests: `TestFindUnansweredQuestions_UnrepairableJSON_MarkedMalformed` (file untouched); `TestPollQuestions_MalformedQuestion_{GraceTickHidesFromUser,ResolvesSilentlyIfWriteCompletes,StableSpawnsFixAgent,FixAgentRepairsQuestion,RealAnswerSurvivesAfterRecovery,FixAgentExhaustionShowsStub,NonInteractiveFixThenAutoAnswer,NonInteractiveFixFailsThenAutoAnswerFallback}`; `TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions`; `TestDialogGet_SkipsMalformedPendingQuestion` (the guarantee-visibility fallback in `handlers.go` also filters `Malformed`); `TestSpawnDetached_TracksWaitGroupWithoutSemaphoreOrActiveMarker` (`concurrency`); the integration test `TestIntegration_UnrepairableQuestionFallsBackToStub` (one script in two roles: as the main agent it writes broken JSON and sleeps, as the fix agent it's launched without `AFM_STAGE_DIR` and fails immediately — a fast model of "the separate agent couldn't either"). Tests inject the fix agent via a test `o.spawnJSONFix` stub (`injectFixStub`), without launching a real process.

- **Dissecting a third real log revealed an architectural pattern, not a single bug: "stage completion" was decided in SEVERAL independent places instead of one.** The `brainstorm` stage in production reached 15/15 questions, the agent honestly wrote `execution_summary.md` and exited the process correctly (`exit 0`) — but the stage hung FOREVER in `awaiting_user_input`, because somewhere EARLIER in its life (id reuse, see above) a single abandoned, never-answered `question.json` remained. `hasOpenQuestion()` doesn't distinguish this: "the question is still relevant" and "the question is a multi-hour tail nobody will ever answer" are indistinguishable to it.
  - **`runWithRetry`'s open-question gate** (retry.go), on `err == nil` from the agent, distinguishes a "stale" question from a "live" one by the CURRENT FSM status at the moment the agent returns: if the stage is NOT yet in `AwaitingUserInput` (the question was just created live by this same call), we hold the stage via `EvAskUser` as before — completion isn't considered at all, so as not to rush a user who's really waiting to answer. If the stage is ALREADY in `AwaitingUserInput` (the independent question poller managed to move it there while the agent was still running — it outran the agent's own exit), the question is treated as a stale tail, and `completionCheck()` decides: satisfied — we publish `EventAgentCompleted`; not — we just return, publishing nothing. Regression: `TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion` (explicitly moves the stage into `AwaitingUserInput` via `EvAskUser` before calling `runWithRetry`, reproducing the race for real, not just by the presence of files on disk).
  - **`onAgentCompleted` (orchestrator.go) has its OWN `hasOpenQuestion` check, WITHOUT a gate on `s.Interactive`/`phase`** — the first version of this cleanup considered it a pure duplicate of the check in `runWithRetry` and removed it entirely; a later analysis of the regression `TestIntegration_PlanningWithOpenQuestionWaits` (a non-interactive planning stage with a fake first open question suddenly drove straight to `done`, bypassing `awaiting_user_input`) showed it is NOT a duplicate — the check in `runWithRetry` has a gate `(s.Interactive || phase == phaseAutonomous)`, specifically so it doesn't touch the FSM for non-interactive stages (that's the auto-answerer's job, see "Auto-answering questions" below); the check in `onAgentCompleted` never had such a gate — it catches phases/stages the `runWithRetry` gate lets pass (e.g. a non-interactive planning stage whose agent synchronously writes a question and exits immediately, without waiting for the poller). It was restored with the SAME "stale/live" logic as in `runWithRetry`: it's skipped (doesn't hold the stage) if the current status is ALREADY `AwaitingUserInput` — the same "poller outran the agent" race, the same stale tail.
  - **`completeStage`'s precondition and the FSM rules for `EvComplete`/`EvPlanReady` didn't recognize `AwaitingUserInput` as a valid source of a transition.** Even after fixing the two gates above, if the question poller manages to independently move the stage into `AwaitingUserInput` (because of the same abandoned question) BEFORE the agent process manages to return `nil` — i.e. DURING the same `runWithRetry` call, concurrently with the polling goroutine — then by the time `runWithRetry` publishes `EventAgentCompleted`, the current status is ALREADY `AwaitingUserInput`, not `Running`. `completeStage` and `EvComplete`/`EvPlanReady` recognized only `Running`/`Planning`/`Retrying` as a transition source — they silently dropped the transition, and the stage hung FOREVER (the agent process is already gone to try again). Both rules were extended to include `AwaitingUserInput`. Found and confirmed by a live browser test (the `stale-question-verify` flow) and two regression integration tests: `TestIntegration_PollerRaceDoesNotStrandCompletedStage` (autonomous/implementation) and `TestIntegration_PollerRaceDoesNotStrandPlanningStage` (planning — the same pattern, found by an audit by analogy, not a separate live incident).
  - **General takeaway for future similar races**: if some fact about "is the stage ready" is computed through the filesystem (`FindUnansweredQuestions`, `Check*Completion`), and a DECISION based on that fact is made in more than one place — those places WILL GUARANTEED diverge on the next edit to one of them. The only reliable defense is a single canonical decision point (here: `runWithRetry`); everything else consumes its RESULT (events), rather than recomputing the same fact anew. Plus: the FSM transition table must list ALL statuses a concurrent poller can actually reach, not just the "normal", sequential path — `EvComplete`/`EvPlanReady`'s `From` lists modeled a synchronous world where the poller couldn't outrun the current agent call.

### Polling Latency

- Orchestrator polls every **1 second** for new questions
- Answer detection is immediate (bash loop checks file existence continuously)
- UI dashboard polls `/api/stages/<id>/dialog` every ~2 seconds
- Total latency: question visible in UI within ~2-3 seconds of agent writing it

### Common Changes

When adding new interactive features:
1. Ensure agent writes `<phase>.<id>.question.json` in correct format
2. Ensure agent polls correctly: `while [ ! -f "$AFM_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
3. Update handler validation in `pkg/server/handlers.go` if phase names change
4. Add integration tests. Note: interactive stages (`stage.Interactive=true`) **ignore** the injected `Runner` — `runnerFor` always builds a real `executor.New(...)` driven by `stage.Command`. So interactive tests run a real bash script via `stage.Command` (see `TestFullDialogCycle`, `TestIntegration_MisplacedQuestionRelocated`), not `eagerProbeRunner` (which only applies to non-interactive stages)
5. Verify atomic write pattern (O_EXCL) is preserved in handlers

### Auto-answering questions in non-interactive stages

A skill/agent may use the file-based dialog protocol regardless of whether the stage is marked `interactive: true` — simply because the skill itself always does so when it needs a clarification. Previously this hung a non-interactive stage forever (nobody answered the question) or crashed it ("missing artifact or incomplete"). Now afm answers on its own.

- **`AFM_STAGE_DIR` — for all stages.** `runner_factory.go`'s `runnerFor` no longer gates `StageDir` on the condition `phase == phaseAutonomous` — it's set for ANY non-interactive stage, otherwise the agent physically has nowhere to write `question.json`.
- **`pollQuestions` branches on `stage.Interactive`.** For `!stage.Interactive` (default, including `agents: [auto]` — the only exception is an explicit `interactive: true`) the stage does NOT go to `awaiting_user_input`: `mcp.PickAutoAnswer` picks an answer (a marker `(recommended)`/`(default)`/`(рекомендую)`/`(рекомендуется)`/`(по умолчанию)`, case-insensitive substring, first-option-with-any-marker wins — the marker is searched in option order, not in marker order; with no options — the fixed text "Make the most relevant decision autonomously or offer answer options"), `mcp.WriteAnswer` atomically (O_EXCL) writes `answer.json` + best-effort appends to `dialog.jsonl` with the label `AutoAnswered: true` (a single write point, reused both by the HTTP handler `handleDialogAnswer` with `autoAnswered=false` and by the poller — not duplicated). The stage's FSM is not touched at all (no `EvAskUser`/`Trigger`).
- **`bus.EventAutoAnswered` — live PLUS notices.jsonl.** This is not an FSM transition, so it's not in `events.jsonl` (the durable log). It's published live via `o.ui.Publish` — but, like `EventAgentCompleted`/`EventContextWarning`/`EventScriptOutput`, it MUST be duplicated via `stagefiles.AppendNotice(runDir, stageID, ..., data)` into the run-level `notices.jsonl`: otherwise a client that connects or reloads the page AFTER the auto-answer will never see that line in the event feed (`/api/events`'s `reconstructNotices` replays precisely from `notices.jsonl`, not from `events.jsonl`). Forgetting `AppendNotice` for a new non-FSM UI event is an easily reproducible bug; there's a ready regression test for this class of error: `TestExecScript_PersistsOutputToNotices`.
- **Dashboard: the dialog panel is gated by the actual presence of history, not just the stage type.** `App.tsx`'s `showDialog` used to be `interactive || autonomous` — a stage could have a real dialog history (an auto-answered question) and still not show the `DialogChannel` panel, because the panel wasn't mounted for it at all. A third signal `stage_has_dialog` was added (`/api/status`, the same file-presence pattern as `stage_autonomous`/`autonomous.flag` — checking for the existence of any `<phase>.dialog.jsonl` in the stage directory), `showDialog = interactive || autonomous || hasDialog`. `DialogChannel.tsx`'s own internal gate `hasContent` also accounts for `stage.hasDialog` (not just `entries.length > 0`) — otherwise a test mock with an empty `/dialog` fetch masks the bug, while in reality there's a window where `/api/status` already knows about the dialog but the panel's own polling hasn't pulled in `entries` yet.
- **`AutoAnswered bool`** — a new field on `mcp.Answer`/`mcp.Entry` (json: `auto_answered`), NOT a string `role` — the scenario is binary (human/afm), a two-value enum is redundant. Threaded through `dialogUIEntry`/`questionUIEntry` (`pkg/server/handlers.go`) into GET `/dialog`. The frontend (`DialogChannel.tsx`) renders it with a separate class `qa-auto` + a `⚙` badge (`title="Answered automatically by afm"`) on answered history entries with this flag.
- Spec/plan: `docs/superpowers/specs/2026-08-07-non-interactive-auto-answer-design.md`, `docs/superpowers/plans/2026-08-07-non-interactive-auto-answer.md`.

## The flake "storage failure: write events.jsonl: invalid argument" — process-group kill, not an OS quirk

For a long time this looked like random environment instability during `go test ./pkg/orchestrator/...` — it failed on DIFFERENT, unrelated tests, with the error `write events.jsonl: invalid argument`, resembling a genuine OS-level EINVAL. The real cause is a combination of two bugs, one in the test infrastructure, one in production.

- **The error text masked the source.** `(*os.File)` in Go is nil-safe: calling a method (`Write`) on a `nil` receiver doesn't panic but quietly returns `fs.ErrInvalid`, whose `.Error()` is literally `"invalid argument"`, indistinguishable by eye from a real `syscall.EINVAL`. `state.Store.Apply` calls `s.eventsLog.Write(data)`; if `Store.Close()` managed to null out `s.eventsLog` BEFORE an agent goroutine still writing to the store called `Apply` — the error looks like a mysterious OS flake, not like "we closed the file too early".
- **Root cause #1 (test infrastructure, ~35 places in 11 files of the `pkg/orchestrator` package): `go func() { _ = orch.Run(ctx) }()` without waiting for completion.** Tests launched `Run` in a fire-and-forget goroutine, waited for the desired stage STATUS (`waitForStatus`/an event subscription), then returned — while `t.Cleanup(func(){ store.Close() })` fired without waiting for the `Run` goroutine itself to actually return. `Run`'s own clean-shutdown guarantee (`defer o.concurrency.WaitAgents()` — see "Clean shutdown" above) was NOT engaged, because the test never waited for `Run()` to return, only for a terminal-looking status — and a status can look terminal BEFORE the agent goroutine, still writing to the store (e.g. `EvFail` on context cancellation), has actually finished. Fix: a single test helper `runOrchestratorAsync` (`pkg/orchestrator/testrun_helper_test.go`) — starts `Run` in a goroutine and registers a `t.Cleanup` that `cancel()`s the context and WAITS on the `done` channel (closed after `Run` returns), with an honest `t.Error` after 10s instead of silently giving up. Order matters: `t.Cleanup` callbacks run LIFO, so registering the wait LATER than registering `store.Close()` (which also must be via `t.Cleanup`, not a bare `defer` — a bare `defer` would fire BEFORE any `t.Cleanup`, defeating the fix) guarantees the wait runs before the store is closed.
- **Root cause #2 (a real production bug, `pkg/executor/executor.go`): `cmd.Process.Kill()` kills only the direct child, not the whole process group.** If a `stage.Command` script spawns a grandchild process without replacing itself with it via `exec` (e.g. a multi-line script whose last line is `sleep 30`), that grandchild inherits the stdout pipe created by `cmd.StdoutPipe()`. On context cancellation/idle timeout/expired grace-period SIGINT, the executor killed only the wrapper script (the direct child) — the orphaned grandchild kept living and holding the pipe open, so `lineReader`, reading from that pipe, never saw EOF, and `<-done` in `executor.run` blocked for the ENTIRE REMAINING life of the orphan (observed as 11–31s delays — exactly the length of `sleep N` in the test scripts). Fix: `killProcessGroup(cmd, sig)` — `syscall.Kill(-cmd.Process.Pid, sig)` (a negative PID = the whole process group), plus `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` before `Start()`, so the script becomes the leader of ITS OWN group (otherwise `-pid` would resolve to afm's own group, which would hit unrelated processes). Applied at all process-kill points in `run()`: idle timeout, context cancellation, both steps of the SIGINT interruption (the soft signal and the forced fallback).
- This is a real, not just a test, bug: on canceling `afm run` (Ctrl+C, `Pause()`, a normal shutdown) with a `stage.Command` script that spawns non-exec'd children, "Clean shutdown"'s `waitAgents()` (bounded 10s, see above) could hit its timeout, leaving such a goroutine alive LONGER than the declared 10 seconds — `waitAgents()` itself wouldn't hang (it has its own bound), but the agent goroutine would technically keep running in the background after `Run()` had already returned.
- Found NOT from production incident logs, but by systematically investigating the session's own flake (see `superpowers:systematic-debugging`) — the first hypothesis (an OS/sandbox quirk) was rejected by isolation via `git stash` (the flake reproduced on a clean tree too) and by a stress test with artificial parallel load (the flake frequency grew with the load on the machine — a typical sign of a real race, not a random EINVAL).

## Docker Mode

afm can automatically re-exec itself inside Docker when Docker mode is enabled.

### Enabling

Via config (`.afm/config.yaml` or `~/.afm/config.yaml`):
```yaml
docker:
  enabled: true
  image: akopichin/afm:latest   # optional, this is the default
```

Or via an environment variable:
```bash
AFM_USE_DOCKER=1 afm run flow.yaml
```

### What is mounted automatically

| Host | Container | Purpose |
|------|-----------|---------|
| `$(pwd)` (absolute path) | the same path | Project + `.afm/` (runs, flows, config) |
| `~/.claude/` | `/home/afm/.claude` | Auth, skills, memory (= `$HOME/.claude` in the container) |
| `~/.afm/` | `/home/afm/.afm` | afm global config |
| Non-standard agents from the flow | `/usr/local/bin/<cmd>` (`:ro`) | Custom commands |
| `docker.extra_mounts` | `~`-paths → `/home/afm/…`, others — the same path (`:ro`) | Tokens/configs for custom agents (e.g. `~/.ai-free`) |

`~/.claude.json` is deliberately **NOT** mounted — claude creates a fresh container-local config (`/home/afm/.claude.json`). Trying to mount it `:ro` led to a crash (`corrupted: JSON Parse error`), because claude updates the file via atomic rename.

**Auth for `command: claude` in Docker:** macOS stores OAuth tokens in the Keychain (`Claude Safe Storage`), which is inaccessible from a Linux container. So `claude` inside Docker reports `not logged in`. The solution is to pass the token via an env var:
1. Generate a long-lived token: `claude setup-token` → save it in `~/.zshrc` as `export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-si-...`
2. The launcher automatically forwards `CLAUDE_CODE_OAUTH_TOKEN` into the container (if set in env).
   `ANTHROPIC_API_KEY` (API key) and `ANTHROPIC_AUTH_TOKEN` are also supported.

**Dashboard:** the port from `server.port` is forwarded to the host via `-p <port>:<port>`, otherwise the UI is inaccessible outside the container. **Browser:** by default (`server.open_browser` absent/`false`) it is NOT opened — the dashboard URL is printed to the log with the hint `→ open this URL in your browser to follow the run`. With `server.open_browser: true` the browser is opened by a host-side opener: afm inside the Linux container can't open a browser on the macOS host itself (`runtime.GOOS=linux` → `xdg-open` without a display), so a separate helper process is launched on the host BEFORE re-exec, polls the forwarded port and calls `open`/`xdg-open`. Inside the container the `openBrowser` call is skipped (`AFM_IN_DOCKER=1`).

**Privileges (important):** the container starts as root, but the entrypoint (`docker-entrypoint.sh` + `gosu`) immediately drops privileges to the host uid/gid (`AFM_HOST_UID/GID`, passed from `os.Getuid/Getgid`) and sets `HOME=/home/afm`. So afm and the agents run as the same user as on the host — all writes to `~/.claude`, `~/.afm`, the project directory and `extra_mounts` belong to the host user, not root (no root-owned files and no permission conflicts with the host's claude). Under a non-root user, claude allows `--dangerously-skip-permissions` without `IS_SANDBOX`.

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `AFM_USE_DOCKER=1` | Enable Docker mode without editing the config |
| `AFM_IN_DOCKER=1` | Set inside the container — prevents recursion (don't touch) |
| `AFM_HOST_UID` / `AFM_HOST_GID` | Passed inside; the entrypoint drops root to this uid/gid (`gosu`), so writes to volumes belong to the host user |
| `AFM_DOCKER_IMAGE` | Override the image (e.g. for a local build) |
| `AFM_DEBUG` | Forwarded into the container by value (`-e AFM_DEBUG=…`, not a secret), so the re-exec inside also logs the agent input; on the host it's set by the `--debug` flag in `PersistentPreRunE` |
| `ANTHROPIC_API_KEY` | Forwarded in bare form `-e KEY` (without a value — not exposed in `ps aux`/history) |
| `ANTHROPIC_AUTH_TOKEN` | Same |
| `ANTHROPIC_BASE_URL` | Same |
| `CLAUDE_CODE_OAUTH_TOKEN` | Long-lived OAuth token for `command: claude` (generated via `claude setup-token`) |

### Publishing a new image

A versioned release (SemVer, auto-bump) — pushes the immutable `akopichin/afm:vX.Y.Z` and the rolling `:latest`:

```bash
make release-patch   # v1.2.3 → v1.2.4  (bugfix)
make release-minor   # v1.2.3 → v1.3.0  (new feature, backward compatible)
make release-major   # v1.2.3 → v2.0.0  (breaking change)
```

`scripts/release.sh` reads the latest SemVer git tag, bumps the level and
**only** creates+pushes a new git tag (`git push origin vX.Y.Z`) — the actual
build (docker image, binaries, GitHub Release, Homebrew cask) no longer
happens in the script. Pushing the tag triggers `.github/workflows/release.yml`,
which does all the actual work. Additionally: any push to
`main` automatically bumps and pushes the next patch tag via
`.github/workflows/ci.yml` (the `auto-release-tag` job) — `make release-patch`
by hand is rarely needed, mostly for minor/major.

**A release is always multi-arch (`linux/amd64` + `linux/arm64`).**
`release.yml` builds and pushes via `docker buildx build --platform
linux/amd64,linux/arm64 --push` in a single step (a separate `docker push` for
the manifest list won't work — the images aren't loaded into the local daemon),
with prior QEMU registration (`docker/setup-qemu-action`) to emulate arm64 on
the amd64 GitHub Actions runner. The version is baked into the binary
via `--build-arg AFM_VERSION`: `docker run akopichin/afm:vX.Y.Z afm
--version` shows the tag.

`make docker-build`/`docker-push` — dev-only, a local **single-arch** build
(fast iteration without a release). For a real release (multi-arch + binaries +
Homebrew) it's enough to push the `vX.Y.Z` tag (by hand via `make release-*`
or automatically on push to `main`) — CI takes care of the rest.

### The claude-code CLI version in the image — pinned, bump by hand after testing

`Dockerfile.runtime`'s `ARG CLAUDE_CODE_VERSION` pins the version of
`@anthropic-ai/claude-code` installed inside the image. The line used to be
`npm install -g @anthropic-ai/claude-code` without a version — each rebuild
silently pulled in whatever npm release was current at that moment, and
different built tags (`v0.5.x`, built on different days) could run flows on
different CLI versions with different agent-loop behavior (e.g. the
model decides not to continue a turn without an explicit tool-call differently
than before) — a difference undetectable from afm's code, only from the fact of
different runtime behavior of the same flow between releases.

**Bump rule:** do NOT update `CLAUDE_CODE_VERSION` blindly on every
claude-code npm release. Update it only once the new version has already been
tested by hand (`AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=...` or
`make docker-build` + a manual flow run) together with an afm release being
prepared for a tag — and only then include the bump in that same release commit.
The current published version: `npm view @anthropic-ai/claude-code version`.

### Debugging

```bash
# See exactly what will be launched
AFM_USE_DOCKER=1 AFM_DOCKER_IMAGE=local/afm:dev afm run flow.yaml

# Enter the container manually (privileges are dropped to your uid via the entrypoint)
docker run --rm -it \
  -v $(pwd):/project \
  -v ~/.claude:/home/afm/.claude \
  -v ~/.afm:/home/afm/.afm \
  -e AFM_HOST_UID=$(id -u) -e AFM_HOST_GID=$(id -g) \
  akopichin/afm:latest bash
```

### Non-standard agents (not claude)

If a flow specifies `command: glm51` (or another non-claude binary), afm automatically:
1. Finds the binary via `which glm51`
2. Mounts it into the container: `-v /path/to/glm51:/usr/local/bin/glm51:ro`

Binaries not found in PATH on the host are silently skipped.

Limitations:
- Only the agent's binary/script file itself is mounted into the container (`:ro`). If the wrapper script calls third-party dependencies (node/python/sibling scripts/files like `~/.glmrc`), they won't be carried over — use agents whose dependencies are already in the image.
- `command` in the flow must be a name from `PATH` (a base name), not an absolute path: only `filepath.Base(cmd)` is mounted, and inside the container the host's absolute path would be looked up.
- If a script agent reads its tokens/configs from home (e.g. the GLM wrappers `glm51`/`glm52`/`ai-free.claude-glm` — from `~/.ai-free/claude-glm/`), add that directory to `docker.extra_mounts`, otherwise the agent will crash with "file not found".

### autoShim: generated wrappers without mounting

With `docker.autoShim: true` afm generates claude-compatible wrappers for the agents
described in `docker.agents.<cmd>` (recipe: `model`/`url`/`system_prompt`/`auth`),
right inside the container — without `-v` mounting the host binary and without `extra_mounts`
for tokens. The secret and the system_prompt content are read on the host and passed into
the container as transient env (`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`); the container takes
`url`/`model` from the mounted `config.yaml`.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

- `auth.to` ∈ {`env:CLAUDE_CODE_OAUTH_TOKEN`, `env:ANTHROPIC_API_KEY`, `env:ANTHROPIC_AUTH_TOKEN`}.
- Without a recipe (with `autoShim: true`) the command is mounted `:ro` as before.
- `url` is baked into the wrapper as `ANTHROPIC_BASE_URL` (z.ai, deepseek — directly, without a proxy).
- See the spec `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

#### Type `openai`: OpenAI-compatible providers

For providers with a **genuine** OpenAI-compatible API (`v1/chat/completions`), specify `type: openai`.
The generated wrapper uses `/usr/local/bin/openai-as-claude` instead of claude:

```yaml
docker:
  autoShim: true
  agents:
    deepseek:
      type: openai
      model: deepseek-chat
      url: https://api.deepseek.com/v1
      auth:
        from: env:DEEPSEEK_KEY        # secret on the host
        to: env:OPENAI_API_KEY        # not restricted to ClaudeAuthEnvVars
```

Supported providers: DeepSeek (`api.deepseek.com`), OpenAI, local Ollama/any
endpoints with `POST /v1/chat/completions` (including SSE streaming). **Important:** Cursor does
NOT belong here — see `type: cursor` below; IdeaLab also does NOT belong here — that provider
needs a real tool-loop, see `type: openai-agent` below.

It supports multimodal `[Screenshot: <path>]` insertions from the dashboard the same
way as `openai-agent` (see below) — the only marker-delivery path available here
is the initial prompt itself; the script doesn't run a loop and doesn't read
dialog answers on its own.

Image requirements: `jq`, `curl` (both present in `Dockerfile.runtime`).

#### Type `openai-agent`: OpenAI-compatible providers with a real tool-loop

`type: openai` (above) gives the model only text — fine for planning/review
stages, but not for `agents: [auto]`/`interactive: true` stages, which need to
actually write files, run scripts and answer dialog questions.
`type: openai-agent` — for providers whose `/chat/completions`
supports genuine OpenAI-style function calling (`tools`/`tool_choice`,
including streaming `tool_calls` with standard index-addressing of fragments).
The generated wrapper uses `/usr/local/bin/openai-agent-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    idealab:
      type: openai-agent
      model: qwen3-max
      url: https://idealab.alibaba-inc.com/api/openai/v1
      max_turns: 40          # optional; the script's default is 40
      auth:
        from: "file:~/.ai-free/claude-glm/token-idealab"
        to: "env:OPENAI_API_KEY"
    balian:
      # Balian/DashScope (Alibaba Cloud "百炼" Model Studio) — the same
      # compatible-mode /chat/completions, the same streaming tool_calls format.
      # model: model availability depends on the key — on the tested key
      # only qwen-plus and qwen3.5/3.6/3.7-plus work; qwen3.8-max/qwen3-max/
      # qwen-max/qwen-turbo/qwen3-coder-* give Model.AccessDenied. qwen3.7-plus
      # thinks by default (300+ reasoning tokens even for a trivial answer,
      # the adapter doesn't read reasoning_content — just extra tokens/latency);
      # qwen-plus from the same provider answers without thinking mode.
      type: openai-agent
      model: qwen3.7-plus
      url: https://dashscope.aliyuncs.com/compatible-mode/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-balian"
        to: "env:OPENAI_API_KEY"
```

The model is given exactly one tool — `bash` (command → stdout+stderr+exit
code). No separate read/write/skill tools: reading and writing
files, running `./scripts/*.sh`, polling dialog files
(`<phase>.<id>.answer.json`) — the model does all of that itself via `bash`,
exactly the way a regular shell script would. The skill convention (`<skills>name</skills>`
in the prompt, see the "File-Based Dialog Protocol" section above) isn't natively
supported by the third-party provider — the adapter's system prompt explicitly teaches the model
to read `.claude/skills/<name>/SKILL.md` itself via `bash` when a skill is mentioned.

Each tool call is immediately printed to stdout as
`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"..."}}]}}`
— the same shape as a real Claude `Bash` tool_use, so the dashboard
shows a live action feed rather than silence until the very end of the stage; this also
resets the 30-minute `idle_timeout` between turns. `max_turns` (default 40)
limits the number of API calls per stage; when the limit is reached the
script exits cleanly (exit 0) with a note in the text — afm treats
this as an ordinary incomplete autonomous run (no `execution_summary.md` →
retry), not as a separate error. A failure of the API request itself (network, non-2xx) —
that's `exit 1`, the stage fails immediately, unlike `openai-as-claude.sh`
(which on a `curl` failure swallows the error into an empty success — there it's
safe for one-shot text, here a silent "success" would mask a
genuinely incomplete tool-loop).

A known (not new) limitation: if the model hangs on dialog polling
longer than 30 minutes (the human takes a long time to answer), the same `idle_timeout`
documented for the file-based dialog protocol above will fire — it's a
property of the mechanism itself, not a specific of this type.

**Multimodal screenshots (`[Screenshot: <path>]`).** A screenshot pasted in the dashboard
(see "paste a clipboard screenshot" in the release notes) reaches
`openai`/`openai-agent` not as a path-you-have-to-read-yourself, but as a
real image: the adapter finds the marker itself, base64-encodes the file and
substitutes an `image_url` block instead of/together with the text — it works only if
the configured model is genuinely multimodal (there's no separate `vision:` flag
in the recipe, the shim simply always tries to embed a found image). For
`type: openai-agent` this covers both paths the marker can reach the
agent through: the initial prompt (revise/note) and the text the model
reads itself via `bash cat` of a dialog answer inside the loop — the second
case is substituted as a separate user message right after the tool result, not
into the tool result itself (multimodal `tool` content isn't guaranteed to be
supported by all providers).

Image requirements: `jq`, `curl` (both already in `Dockerfile.runtime`).

#### Type `cursor`: Cursor Cloud Agents API

The Cursor Cloud API (`api.cursor.com`) **does not have** a synchronous `v1/chat/completions` (responds 404) —
it's the **Cloud Agents API**: an asynchronous run-based API where a chat = launching a cloud code agent.
So Cursor uses a separate type and the adapter `cursor-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    cursor:
      type: cursor
      model: auto                    # auto/empty → Cursor default; otherwise model.id from GET /v1/models
      url: https://api.cursor.com/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-cursor"   # secret on the host (CRSR_…)
        to: env:CURSOR_API_KEY         # any env:VAR; CURSOR_API_KEY by convention
```

The adapter `cursor-as-claude`: creates a no-repo Cloud Agent (`POST /v1/agents`, `mode:"agent"`),
polls the run until a terminal status, emits claude stream-json with the `result` text and
archives the agent (so as not to breed clutter). `system_prompt` for cursor is **not used**
(the adapter doesn't pass it).

A quirk: the first response takes ~30–90s (a cloud VM starting when the agent is created); after that the run is fast.
The token is a user API key from Cursor Dashboard → API Keys (prefix `crsr_`). Image requirements: `jq`, `curl`.

#### Type `codex`: OpenAI Codex CLI (ChatGPT-plan OAuth, no secret in the config)

Unlike `claude`/`openai`/`cursor`, `codex` has **no** `AFM_SECRET_<CMD>` auth model:
`auth` in the recipe is optional (`AgentRecipe.Validate()` — the only exception among the three types
where a missing `Auth` isn't an error). Authorization goes through the ChatGPT-plan OAuth state `~/.codex`
on the host: `docker.ReExec` mounts it `:ro` to a temporary container path **only if** the flow
actually uses codex (`docker.UsesCodex` — the command `codex-as-claude` directly or a recipe
`type: codex` somewhere in `docker.agents`), and `docker-entrypoint.sh`, still as root before `gosu`,
copies the mounted directory into `$HOME/.codex` (already writable) — codex can update `auth.json`
(refresh token) inside the container without touching the host file; the container is ephemeral, so the token update
doesn't survive recreation.

```yaml
docker:
  autoShim: true
  agents:
    codex:
      type: codex
      model: gpt-5-codex          # optional; "" / "default" → CODEX_MODEL is not set,
                                   # codex / ~/.codex/config.toml decides
      # auth: not specified — authorization via the mounted ~/.codex
```

The generated wrapper resolves the absolute path to the real `codex` binary **before** the
wrapper's directory (which holds a file also named `codex`) enters `PATH` — otherwise the adapter
`codex-as-claude` (which itself calls a bare `codex`) would pick up itself via PATH and go
into recursion; the resolved path is passed to the adapter via `CODEX_BIN`. The adapter (`scripts/codex-as-claude.sh`)
runs `codex exec --json --dangerously-bypass-approvals-and-sandbox` (the container's sandbox is
isolated anyway), accumulates `agent_message` events into a single `assistant` claude
stream-json envelope (`CODEX_VERBOSE=1` — also include command output in the accumulation). The recipe's
`system_prompt` isn't used for codex. Image requirement: `jq`.

`command: codex-as-claude` can also be used directly in a flow stage (without an autoShim recipe) —
`docker.UsesCodex` detects this path too for gating the `~/.codex` mount.

### The uv version in the image — pinned, bump by hand

`Dockerfile.runtime`'s `ARG UV_VERSION` pins the version of `uv` (the fast
astral-sh Python package manager) installed into the image. The binary is
copied from the official `ghcr.io/astral-sh/uv:<version>` image
(`COPY --from=… /uv /uvx /usr/local/bin/`) — faster and more reliable than the
curl installer, and unlike `python3`/`pip`/`venv` (installed via apt) it isn't
tied to Ubuntu's package feed. Pinned for the same reason as
`CLAUDE_CODE_VERSION`: a rebuild must not silently pull a newer release. Bump it
by hand; the current published version is at
`https://github.com/astral-sh/uv/releases/latest`.

### Known gotchas (Docker mode)

- **gosu resets HOME for a uid not present in `/etc/passwd`** → sets `HOME=/`. So in `docker-entrypoint.sh` HOME is set **after** gosu (`gosu uid:gid env HOME=/home/afm afm …`), not before. Otherwise agents look for `~/` files in `/` (a bug: the token was looked up in `//.ai-free/…`).
- **`:ro` single-file bind-mount + atomic rename = corruption.** Applications that rewrite a config via temp+rename (claude and `~/.claude.json`) can't update a `:ro` mount and mark it as corrupted. Don't mount `:ro` what an application writes — let it create a fresh container-local file.
- **`os.ModeCharDevice` ≢ TTY.** `/dev/null` is also a char device, so the heuristic `Stdin.Stat().Mode()&ModeCharDevice` falsely added `-it` in a non-TTY → `docker run` failed with "the input device is not a TTY". The honest check is `golang.org/x/term.IsTerminal`.
