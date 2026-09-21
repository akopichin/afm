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

## Stage buttons: predefined kebab-menu prompts

A stage can declare named one-click actions in `flow.yaml` — each carries a canned prompt, and clicking it in the dashboard delivers that prompt to the stage's **live** agent through the exact same path as the free-text "Add note for agent" (`Revise`). Written once in the flow, reusable with a single click. Unlike a pre-note (which targets a `pending` stage's first start), a button targets an already-running agent and restarts it with the prompt as feedback.

```yaml
stages:
  - name: build
    buttons:
      Run linter: "Запусти golangci-lint и почини все замечания"
      Rebuild:    "Пересобери проект с нуля и убедись что тесты зелёные"
```

- **Ordered `Buttons` type, not a `map`.** `flow.Stage.Buttons` is `type Buttons []Button` (`Button{Label, Prompt string}`, `pkg/flow/flow.go`) with a custom `UnmarshalYAML(*yaml.Node)` that walks the mapping node's `Content` in pairs — a plain `map[string]string` would iterate randomly and shuffle the menu. So **menu order == YAML declaration order**. Helpers: `Prompt(label) string` (`""` if no such button) and `Labels() []string`. `UnmarshalYAML` walks the raw `MappingNode` itself, so yaml.v3's duplicate-key rejection doesn't fire — both duplicates survive into the slice and are caught by `validate()` instead. Validation in `Flow.validate()`: empty label, empty prompt, duplicate label within a stage, and `buttons` on a `script:` stage are all parse errors (`stage %q: ...`). `buttons` omitted → `nil` → previous behavior.
- **Click == Revise of the live agent; the client sends only the button *name*.** `POST /api/stages/{id}/button` `{"name":"Run linter"}` (`handleStageButton`, `pkg/server/handlers.go`; route in `server.go`) → the gate (status `running`/`awaiting_approval` AND `!isScript`, else `400`; unknown name rejected `400` via the server's labels-only `stageButtons` map before the orchestrator is touched) → `StageActions.Button(ctx, stageID, name)`. The orchestrator (`(*Orchestrator).Button`, `control_api.go`) resolves `o.graph.Stage(id).Buttons.Prompt(name)` and delegates to the existing `Revise` (`feedback.md` + `EvRevise` + interrupt → agent restarts). **Client-supplied text is never trusted** — the prompt is resolved server-side from the flow. Repeated clicks are allowed (each is another Revise). No new agent-execution machinery: `Button` is a thin name→prompt→`Revise` wrapper.
- **The server keeps a labels-only `stageButtons map[string][]string`** (`Server`/`Config`, populated in `cmd/afm/run.go` from `st.Buttons.Labels()`, next to `stageIsScript`/`stageDependsOn`) purely to (a) reject a click whose name isn't declared and (b) feed `StageView.Buttons` (JSON `buttons`, omitempty, `pkg/server/stageview.go`) — threaded through `buildStageViews`. The orchestrator owns the actual prompt text; the HTTP layer never sees it.
- **Dashboard:** `StagesList` renders one menu item per `stage.buttons` entry, in a sub-block below "Add note for agent" separated by `.stage-kebab-divider`, gated by the same `running`/`awaiting_approval` + `!isScript` condition as the note item (hidden when `buttons` is empty). Fire-immediately (no modal — mirrors the `onPause` wiring): `App.tsx`'s `handleButton` → `triggerStageButton` (`api/run-client.ts`) → `POST .../button`. `buttons: string[]` lives on the `Stage` type (`src/types/stage.ts`), mapped in `use-status.ts`.
- **Verified live** (afm locally, mock agent, real dashboard via Chrome DevTools): both buttons appear in declaration order under "Add note for agent"; clicking "Run linter" writes `revision 1` = its prompt into `feedback.md` and the prompt reaches the re-planning agent's actual stdin; "Rebuild from scratch" writes `revision 2` = its own distinct prompt (per-name routing); an unknown button name → `400`. Tests: `TestParseButtons_{LabelToPrompt,DeclarationOrderPreserved,NoButtonsParsesUnchanged}`/`TestValidateButtons_{EmptyLabel,EmptyPrompt,DuplicateLabel,CannotCombineWithScript}` (`pkg/flow`), `TestButton_{DeliversPromptViaRevise,UnknownNameIsNoOp}` (`pkg/orchestrator`), `TestHandleStageButton_{Success,UnknownName,WrongStatus,ScriptStage,MissingName,NonexistentStage}`/`TestBuildStageViews_IncludesButtons` (`pkg/server`), plus `StagesList.test.tsx`/`use-status.test.ts`/`run-client.test.ts` frontend cases. Spec: `docs/superpowers/specs/2026-08-27-stage-custom-buttons-design.md`.

## Lifecycle hooks (Phase 1 + Phase 3: секреты и env)

Observer-only внешние уведомления/метрики о жизненном цикле рана и стадий — команды (`sh -c`), получающие JSON-payload события в stdin. Отличие от `script_before`/`script_after` (см. "The flake…"/`hooks.go`): те **блокируют** запуск стадии и умеют менять её FSM (`EvHookFailed`/`EvHookResolved`); lifecycle-хуки **никогда не трогают FSM** — сбой хука (после всех retry) только логируется и всплывает live+durable dashboard-предупреждением `lifecycle_hook_failed`, run/стадия продолжаются как ни в чём не бывало. Пакет: `pkg/lifecyclehooks/*`. Спека: `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md`.

- **Конфигурация — 4 слоя, keyed-merge по id.** Global config (`~/.afm/config.yaml`) → project config (`.afm/config.yaml`) → flow (`flow.yaml` верхнего уровня) → stage (`stages[].hooks`). Global+project уже смёржены в `cfg.Hooks` самим `config.LoadFrom` (`pkg/config/config.go`'s `mergeFile`, keyed по `Hook.ID` — более специфичный слой заменяет одноимённый хук целиком); flow- и stage-слои добавляются позже, в `cmd/afm/run.go`, через `lifecyclehooks.Combine`:
  ```yaml
  # flow.yaml
  hooks:
    - id: notify-slack
      events: all
      skip_events: [stage_question_asked]
      command: "curl -sf -X POST $SLACK_WEBHOOK -d @-"
      timeout: 10s
      retries: 2

  stages:
    - id: build
      hooks:
        - id: build-metric
          events: [stage_finished, stage_failed]
          command: "./scripts/report-metric.sh"
  ```
  `Hook{ID, Events, SkipEvents, Command, Timeout, Retries}` (`pkg/lifecyclehooks/types.go`). `events` — scalar-or-list (`EventSelector`, та же идиома, что `flow.Input`): `all` либо явный список имён; `skip_events` вычитает из выбора (`Hook.Matches`, `matcher.go`). `timeout` по умолчанию `DefaultHookTimeout = 30s` (`runner.go`) — заметно меньше 5-минутного дефолта script-стадий: наблюдатель не должен иметь права держать процесс долго. `retries` по умолчанию `0` (одна попытка, `attempts := h.Retries+1`), пауза между попытками — фиксированный `hookRetryBackoff = 1s`.
- **Каталог событий — 27 имён** (`pkg/lifecyclehooks/types.go`, `AllEvents()`): 5 flow (`flow_started`, `flow_resumed`, `flow_finished`, `flow_failed`, `flow_interrupted`) + 13 stage/FSM (`stage_planning_started`, `stage_plan_ready`, `stage_approved`, `stage_revision_started`, `stage_execution_started`, `stage_question_asked`, `stage_question_answered`, `stage_retry_scheduled`, `stage_retry_started`, `stage_paused`, `stage_resumed`, `stage_finished`, `stage_failed`) + 9 script/не-FSM (`stage_script_started`, `stage_script_finished`, `stage_script_failed`, `stage_script_before_started`, `stage_script_before_finished`, `stage_script_before_failed`, `stage_script_after_started`, `stage_script_after_finished`, `stage_script_after_failed`). `IsFlowEvent` отмечает первые 5 как недопустимые в stage-скоупе.
- **Precedence по id — плоская замена, не scope-aware routing** (сознательное отклонение от ревью-рекомендации, см. self-review задачи: scope-aware routing противоречил бы утверждённой спеке "Наследование и порядок"). `lifecyclehooks.Combine(layers...)` (`combine.go`) держит единый namespace id по всем слоям: хук с тем же id из более позднего (специфичного) слоя **заменяет** ранний целиком, включая скоуп стадии, на исходной позиции; новый id — дописывается в порядке появления. Валидация (`ValidateLayer`, `validate.go`, вызывается на каждом слое отдельно — `config.LoadFrom`, `flow.validate()`): дубль id **внутри одного слоя** — ошибка парсинга; `id` обязан быть безопасным одиночным компонентом пути (без `/`, `\`, NUL, `..`/`.` — иначе `id: ../events.jsonl` дописал бы лог хука в авторитетный `events.jsonl`); flow-событие в `events`/`skip_events` **stage**-хука — ошибка валидации (`stageScoped=true` гейт). Отдельно в `Flow.validate()` (`pkg/flow/flow.go`): одинаковый id хука в **разных** стадиях — ошибка ("duplicate hook id %q across stages", молчаливое схлопывание через `Combine` было бы реальной проблемой, не смягчённой валидацией слоя).
- **Payload-контракт (stdin, JSON).** `Payload{SchemaVersion:1, EventID, Event, OccurredAt, Flow{Name,RunID,Resumed,RunDir,RootDir}, Stage *StageInfo{ID,Name,From,To,Phase}, Reason, Data}` (`BuildPayload`, `types.go`); `Stage` — `nil` для flow-событий. `event_id` — три схемы, по стабильности гарантии (`Dispatcher.eventID`, `dispatcher.go`): FSM-переходы (`Seq>0`) → `<run>:transition:<seq>:<event>` (durable, из `state`-seq); прочие flow-события → `<run>:flow:<event>`; прочие stage-события (script/авто-ответы, без seq) → `<run>:<stageID>:<event>` (best-effort — повтор возможен при retry стадии). Плюс **10 `AFM_*` env-переменных** в процессе хука (`buildEnv`, `runner.go`): `AFM_HOOK_EVENT`, `AFM_HOOK_EVENT_ID`, `AFM_FLOW_NAME`, `AFM_RUN_ID`, `AFM_RUN_DIR`, `AFM_ROOT_DIR`, `AFM_STAGE_ID`, `AFM_STAGE_NAME`, `AFM_STAGE_FROM`, `AFM_STAGE_TO` (последние 4 — пустые строки для flow-событий, не отсутствуют).
- **Runner (`runCommand`/`execOne`, `runner.go`): `sh -c h.Command`**, `Stdin` = маршалленный JSON payload, `cmd.Dir` = эффективный `root_dir` (та же величина, что CWD агентов — `DispatcherConfig.RootDir`, в `cmd/afm/run.go` резолвится через `lifecycleRootDir(agentRootDir)`: `agentRootDir` если задан `flow.root_dir`, иначе CWD процесса afm). Окружение процесса собирает `buildEnv` — Phase 1 просто наследовала `os.Environ()` целиком + `AFM_*`; Phase 3 заменила это на minimal-by-default + опциональный `env:`/`inherit_env` (см. "Phase 3: секреты и env lifecycle-хуков" ниже). Вывод стримится (не буферизуется в памяти — шумный хук за весь таймаут не должен исчерпать память afm) в `.afm/runs/<run_id>/hooks/<id>.log` (append, с заголовком попытки `=== <ts> event=... id=... stage=... attempt=N ===` и финальной строкой `=== attempt N finished: <err> ===`). Таймаут-килл — **process-group** (`Setpgid:true` + `syscall.Kill(-pid, SIGKILL)` через `cmd.Cancel`), тот же урок, что `pkg/executor.killProcessGroup` (см. секцию про флейк ниже): не-exec'нутый внук хук-скрипта иначе держал бы afm до своего конца.
- **Dispatcher (`pkg/lifecyclehooks/dispatcher.go`): один воркер на хук** — доставки одному хуку строго последовательны, между разными хуками — параллельно. Каждый хук — своя буферизованная очередь `hookQueueSize = 256`; `Emit` неблокирующий: полная очередь → доставка дропается, `OnError(hookID, eventID, err)` репортит переполнение (тот же callback, что финальный сбой доставки после исчерпания retry) → в `cmd/afm/run.go` это `orch.PublishLifecycleHookFailure` → `bus.EventLifecycleHookFailed` (`"lifecycle_hook_failed"`) live в UI-шину + durable в `notices.jsonl` (`stagefiles.AppendNotice`) — фид рендерит его `tone:'warning'` (не `danger`, как блокирующий `hook_failed`), см. `feed-view-model.ts`. **Собственный контекст, независимый от run-ctx** (`context.Background()` в `Start`, не `runCtx`) — событие `flow_interrupted` должно дойти до хуков даже после отмены рана. `Flush(timeout)` — bounded-ожидание опустошения очередей (`FlushTimeout = 10s`); в `cmd/afm/run.go` вызывается в `defer` на **любом** выходе из `RunE` (в т.ч. по ошибке `orch.Run`), затем `Stop()` (закрывает очереди + воркеров, идемпотентен). `Emit`/`Stop` синхронизированы через `sync.RWMutex` — `Emit` после `Stop` тихий no-op (поздние эмиттеры вроде question-поллера не паникуют на закрытом канале).
- **Эмиссия FSM-событий — `triggerWithSeq`, не отдельный путь.** `(*Orchestrator).triggerWithSeq` (`orchestrator.go`) вызывает `emitLifecycleTransition` ТОЛЬКО для успешно применённого перехода (после CAS, с реальным `seq`) — CAS-мисс (событие не применилось) не эмитит ничего. `fsmLifecycleEvents` (`lifecycle_emit.go`) — таблица `bus.FSMEvent → lifecyclehooks.EventType` (`EvStartPlanning→stage_planning_started`, …, `EvComplete→stage_finished`, `EvFail→stage_failed`); flow-терминальные события (`flow_started/resumed/finished/failed/interrupted`) — отдельный путь: `flow_started`/`flow_resumed` эмитятся сразу на входе в `Run()`, а терминальный исход (`finished`/`failed`/`interrupted`) только запоминается по ходу события-цикла (`o.setTerminalFlow`, классификатор учитывает `hasFailedStage` — отмена ctx поверх рана с зависшей `failed`-стадией — это `flow_failed`, не `flow_interrupted`) и реально эмитится+флашится в `defer o.finalizeLifecycle()`, который по LIFO-порядку `defer`-ов срабатывает ПОСЛЕ `WaitAgents()` — терминальное событие уходит хукам, когда все агентские горутины уже точно остановлены. Не-FSM границы (script-стадии, `script_before`/`script_after`, авто-ответы) идут через `emitStageEvent` (без `seq`, `event_id` строит сам dispatcher).
- **Гарантии Phase 1 — live best-effort, не durable.** Если afm упал между `Emit` и завершением доставки — событие теряется (очередь в памяти, не журнал). Durable outbox (гарантированная доставка после падения/ретрая) остаётся будущей работой (см. спеку, раздел "Phase 1 vs Phase 2/3"): `docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md`. Секреты/env (`env:`/`inherit_env`, allowlist-окружение, `[REDACTED]`-маскирование, docker-транспорт) — реализованы в Phase 3, см. ниже.
- **Ключевые точки кода (Phase 1):** `pkg/lifecyclehooks/types.go` (каталог событий, `Hook`/`Payload`/`EventSelector`), `validate.go` (`ValidateLayer`), `combine.go` (`Combine`), `matcher.go` (`Hook.Matches`/`RegisteredHook.MatchesEvent`), `runner.go` (`runCommand`/`buildEnv`, `DefaultHookTimeout`), `dispatcher.go` (`Dispatcher`, `hookQueueSize`, `FlushTimeout`); `pkg/orchestrator/lifecycle_emit.go` (`emitLifecycle`/`emitLifecycleTransition`/`emitStageEvent`/`PublishLifecycleHookFailure`); `pkg/orchestrator/orchestrator.go` (`triggerWithSeq`, flow-терминальные `emitLifecycle` в `Run`); `pkg/flow/flow.go` (`Stage.Hooks`, cross-stage duplicate-id check в `validate()`); `pkg/config/config.go` (`Config.Hooks`, `mergeFile` keyed-merge); `cmd/afm/run.go` (сборка слоёв `Combine`, `lifecycleRootDir`, `Flush`+`Stop` в `defer`).

### Phase 3: секреты и env lifecycle-хуков

Хук может объявить `env:` — дополнительные переменные окружения своего процесса, значения которых берутся из секретов (webhook-токены, API-ключи), а не хранятся в самом `flow.yaml`. Контракт по умолчанию — **минимальное окружение**, не полное наследование afm-процесса (это осознанное отклонение Phase 1 от "унаследовать всё", закреплённое спекой; см. "Контракт окружения" ниже).

```yaml
# flow.yaml
hooks:
  - id: notify-telegram
    events: [flow_finished, flow_failed]
    command: 'curl -sf -X POST "https://api.telegram.org/bot$TG_TOKEN/sendMessage" -d chat_id=$TG_CHAT -d text="run done"'
    env:
      TG_TOKEN: "file:~/.afm/secrets/telegram-token"
      TG_CHAT: "env:AFM_NOTIFY_CHAT_ID"
```

- **`Hook.Env map[string]SecretRef`** (`pkg/lifecyclehooks/types.go`) — имя целевой переменной → ссылка на источник, `SecretRef` — строка вида `env:NAME` | `file:PATH`, **та же семантика**, что `auth.from` у docker agent-recipes (`docker.autoShim`). `Hook.InheritEnv bool` (yaml `inherit_env`) переключает базовое окружение хука (см. ниже); дефолт `false`. `Hook.ResolvedEnv map[string]string` (`yaml:"-"`) — резолвнутые значения, только в памяти, никогда не сериализуются.
- **`pkg/secrets` — нейтральный резолвер, вынесенный из `pkg/docker`.** `secrets.ResolveRef(ref, loaded)` резолвит `env:VAR` (сначала карта `loaded`, затем `os.Getenv`) и `file:PATH` (с раскрытием `~` через `ExpandHome`, содержимое файла — `TrimSpace`); `secrets.LoadSecrets(paths)` парсит `KEY=VALUE`-файлы (later-wins). Пакет не зависит ни от `pkg/config`, ни от `pkg/docker` — оба потребителя (docker agent-recipes и lifecycle hooks) делят один резолвер вместо двух копий одной и той же логики.
- **Валидация имён — `validate.go`.** Имя целевой переменной должно матчить `[A-Za-z_][A-Za-z0-9_]*`; префикс `AFM_` зарезервирован **регистронезависимо** (`up := strings.ToUpper(varName)`, отклоняется независимо от регистра — Windows-окружение само по себе регистронезависимо, дыра в case-sensitive проверке позволила бы `afm_run_id` тихо подменить системную переменную); имена, коллизирующие после ASCII-uppercase с другим именем того же хука — тоже ошибка. Источник (`env:NAME`/`file:PATH` с непустым хвостом) валидируется по форме на этапе парсинга флоу; **существование** источника не проверяется здесь — это Task 3/резолв в рантайме.
- **Резолв — один раз при сборке dispatcher'а, fail-fast до `flow_started`.** `cmd/afm/run.go`: `combinedHooks := lifecyclehooks.Combine(hookLayers...)` собирается **до** докер-ветки (единственный источник итоговых хуков что для host-резолва, что для `docker.ReExec`, что для in-container резолва — иначе транспортные индексы `hookIdx`/`varIdx` разошлись бы между хостом и контейнером). Хост (не-докер): `lifecyclehooks.ResolveHookEnv(hook, secretsMap)` → `pkg/secrets.ResolveRef`; ошибка резолва оборачивается `fmt.Errorf("lifecycle hooks: %w", …)` и возвращается из `RunE` **до** создания `Dispatcher`/эмиссии `flow_started` — ран не стартует с недорезолвленным секретом. **Ошибка называет id хука и имя переменной, но никогда не значение** (`ResolveHookEnv`/`ResolveHookEnvFromTransport`, `pkg/lifecyclehooks/env.go`: `fmt.Errorf("hook %q env %q: %w", h.ID, varName, err)` — сам `err` от `secrets.ResolveRef` тоже не несёт значения, только путь/имя источника).
- **`secrets.env` — три слоя, `project > global > env процесса`.** `loadHookSecretLayers(afmRoot)` (`cmd/afm/agent_environment.go`) переиспользует `docker.LoadSecretLayers("", afmRoot)` (тот же хелпер, что уже обслуживал секреты docker agent-recipes — список путей не дублируется): `~/.afm/secrets.env` (global) читается первым, `<afmRoot>/.afm/secrets.env` (project) — вторым, и как более поздний в списке (`secrets.LoadSecrets`, "later wins") перекрывает одноимённые ключи; приоритет над обоими — `os.Getenv` внутри самого `secrets.ResolveRef` (карта `loaded` проверяется первой, `os.Getenv` — fallback). **Ленивая загрузка**: файлы `secrets.env` читаются только если хотя бы у одного хука из `combinedHooks` непустой `Env` (`anyEnv`-гейт) — иначе нечитаемый/отсутствующий `secrets.env` заблокировал бы обычный Phase 1-хук без секретов, у которого их и не спрашивали.
- **Контракт окружения (IMPORTANT — spec-дефолт, не Phase 1 "унаследовать всё").** `buildEnv(h, cfg, p)` (`runner.go`): `h.InheritEnv == false` (дефолт) → `minimalBaseEnv()` — жёсткий allowlist `PATH`/`HOME`/`TMPDIR`/`LANG`/`LC_ALL`/`LC_CTYPE`/`HTTP(S)_PROXY`/`NO_PROXY` (+lowercase-варианты)/`SSL_CERT_FILE`/`SSL_CERT_DIR`, взятых из `os.Environ()` хоста; `h.InheritEnv == true` → `stripTransportVars(os.Environ())` — полное окружение afm-процесса, но с вырезанными transport-префиксами `AFM_HOOK_SECRET_`/`AFM_SECRET_`/`AFM_SYSPROMPT_` (чтобы секреты ДРУГИХ хуков или autoShim-агентов, живущие в окружении самого afm, не утекли в хук-процесс просто потому что он попросил наследование). В обоих случаях поверх всегда добавляются `afmVars(cfg,p)` (10 `AFM_*` из Phase 1) и последними — `h.ResolvedEnv` (резолвнутые секреты, перекрывают одноимённые базовые/`AFM_*`). Это применяется **единообразно** — что на хосте, что в докере (`ResolvedEnv` заполняется до `buildEnv` независимо от транспорта).
- **`[REDACTED]`-редакция в логе хука И в возвращаемой ошибке/notice.** `newRedactingWriter(logFile, secretValues(h))` (`redact.go`) оборачивает `cmd.Stdout`/`cmd.Stderr` (и заголовок попытки, и финальную строку `=== attempt N finished: … ===`) хука, если у него есть `ResolvedEnv` — заголовок тоже идёт через `sink` (редактор при наличии секретов), а не напрямую в файл, defense-in-depth: сами поля заголовка (`event`/`id`/`stage`) секретами не являются, но ни одна строка лога хука не должна обходить редактор, если он активен. Потоковый bounded-редактор: буфер держит ≤ `maxLen-1` сырых байт (возможное начало секрета на границе `Write`), `redactAll` заменяет секреты **фиксированной точкой** (повторный проход, пока строка меняется — замена одного секрета может синтезировать другой из окружающего контекста), маркер выбирает `redactMarker` из `["[REDACTED]", "[[hook-secret-redacted]]", "\x00REDACTED\x00"]` — первый кандидат, безопасный по трём условиям: (а) сам не содержит ни одного секрета целиком, (б) ни один его непустой суффикс не является префиксом какого-либо секрета (иначе удержание хвоста на границе `Write` могло бы задеть байты самого маркера), (в) сам не встречается как подстрока ни в одном секрете — иначе редактирование ОДНОГО секрета рядом с уже сброшенным (в предыдущем `Write`) контекстом могло бы побайтово ВОССТАНОВИТЬ значение ДРУГОГО секрета, который содержит этот маркер как часть своего значения (пример: секреты `"QR"` и `"a[REDACTED]b"` — запись `"aQ"`+`"Rb"` без условия (в) давала бы в логе `"a[REDACTED]b"`, т.е. значение второго секрета целиком; codex, финальный ревью Phase 3). Fallback — пустая строка (секрет вырезается без замены), если безопасного маркера нет — пустая строка условию (в) удовлетворяет тривиально (не может быть непустой подстрокой). **Ошибка тоже санитизируется до возврата** (`execOne`: `err = errors.New(redactString(err.Error(), secretValues(h)))`) — `OnError` → `PublishLifecycleHookFailure` → `bus.EventLifecycleHookFailed` в live-шину и `notices.jsonl` никогда не видят сырой секрет из stderr команды. **Best-effort по спеке**: это защита от секрета, попавшего в лог/ошибку "как есть" (например echo переменной) — скрипт, который сам кодирует/разбивает секрет по символам перед выводом, редактор не поймает (не парсер вывода, а поиск подстрок).
- **Docker-транспорт — резолв на хосте, transient bare `-e` по индексам, без монтирования файлов.** `pkg/docker/launcher.go`'s `ReExec`: если `UsesLifecycleSecrets(cfg.Hooks)` (хотя бы у одного хука непустой `Env`), хост загружает те же `secrets.env`-слои (`LoadSecretLayers(cfg.SecretsFile, cfg.ProjectDir)`) и резолвит `Hook.Env` **до** `docker run`, передавая каждое значение как `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>` — `hookIdx` — позиция хука в итоговом `combinedHooks`, `varIdx` — позиция имени переменной в `lifecyclehooks.SortedEnvKeys(hook)` (отсортированные ключи `Hook.Env`, детерминизм: map-итерация в Go не гарантирует порядок, а хост и контейнер обязаны посчитать один и тот же индекс). **Инъективность по индексам, не по `sanitize(hookID)`** — codex-фикс #1: два хука с id, схлопывающимися под нормализацию (пунктуация/регистр, напр. `foo-bar`/`foo_bar`), раньше могли получить транспортное имя друг друга; индексная схема делает коллизию структурно невозможной. Значение уходит в env самого afm-процесса перед `execFunc` (не в `argv` — не светится в `ps aux`/истории), сам процесс **exec'ает** `docker`, так что временная переменная не переживает `docker run`. **Секретные файлы в контейнер не монтируются** — только уже резолвнутое значение как env.
- **Ограничение изоляции в Docker-режиме — секретный файл под уже смонтированным каталогом всё ещё виден в контейнере.** Транспорт выше (bare `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>`) решает только задачу "значение секрета не должно попасть в `argv`/окружение агента/`ps aux`" — он НЕ добавляет никакой новой изоляции файловой системы. afm в Docker-режиме уже монтирует `~/.afm` и каталог проекта read-write в контейнер (существовало до Phase 3 — ради skills/memory/config/autoShim, см. "Docker Mode" ниже). Значит `file:`-источник (или `secrets.env`), физически лежащий под одним из этих каталогов, по-прежнему читаем изнутри контейнера ЛЮБЫМ агентом/скриптом, исполняющимся под тем же системным пользователем — Phase 3 этого не меняет и не может изменить, не сузив существующую модель монтирования `~/.afm`/проекта (что вне scope: сломало бы autoShim/skills/memory). Это соответствует явной оговорке спеки: *"Абсолютной изоляции секретов от кода, исполняемого под тем же системным пользователем, lifecycle hooks не обещают"*. Кто нуждается в более сильной изоляции — держите секретные файлы ВНЕ смонтированных каталогов (`~/.afm`, каталог проекта) или используйте `env:`-источник из окружения ХОСТ-процесса afm (значение резолвится на хосте ДО `docker run`, сам файл-носитель в контейнер не попадает).
- **In-container: чтение из транспорта, затем единая точка изоляции.** С `AFM_IN_DOCKER=1` хуки резолвятся НЕ из `secrets.env` (в контейнере их может и не быть смонтировано), а из транспортных переменных: `lifecyclehooks.ResolveHookEnvFromTransport(hookIdx, hook)` (`env.go`) читает `AFM_HOOK_SECRET_<hookIdx>_<varIdx>` для каждого ключа из `SortedEnvKeys` — отсутствующая/пустая транспортная переменная — тоже fail-fast ошибка (тот же контракт: никогда не отдать частично резолвнутый `ResolvedEnv`). Сразу после того как **все** хуки резолвили `ResolvedEnv` из транспорта — и **до** `Orchestrator.Run`/любого запуска агента или стадии — `lifecyclehooks.UnsetTransportVars()` (`env.go`) проходит по `os.Environ()` и снимает **каждую** переменную с префиксом `AFM_HOOK_SECRET_`. Это единая точка изоляции, а не перечисление spawn-сайтов (codex-фикс #2): после вызова транспортных переменных в окружении afm-процесса нет вовсе, так что ни агент, ни script-стадия, ни `RunVerify`/`RunJSONQuery`, ни git, ни другой хук не могут их унаследовать — не нужно аудировать каждое место, где afm порождает дочерний процесс.
- **Секреты никогда не попадают в payload/журнал/`argv`/лог хука** (за вычетом best-effort редакции, см. выше): `Hook.ResolvedEnv` — только в памяти (`yaml:"-"`, не сериализуется в `events.jsonl`/снапшот), не входит в JSON stdin-payload хука (`BuildPayload` его не читает); на докер-транспорте значение идёт bare env-переменной, а не аргументом команды; сам секрет попадает в лог хука/ошибку только если СКРИПТ хука сам его туда выведет — и тогда его перехватывает редактор.
- **Ключевые точки кода (Phase 3):** `pkg/secrets/secrets.go` (`ResolveRef`/`LoadSecrets`/`ExpandHome`); `pkg/lifecyclehooks/env.go` (`ResolveHookEnv`/`ResolveHookEnvFromTransport`/`TransportName`/`SortedEnvKeys`/`UnsetTransportVars`/`HookSecretTransportPrefix`); `pkg/lifecyclehooks/redact.go` (`newRedactingWriter`/`redactAll`/`redactMarker`/`redactString`/`secretValues`); `pkg/lifecyclehooks/runner.go` (`buildEnv`/`minimalBaseEnv`/`stripTransportVars`, редактор в `execOne`); `pkg/lifecyclehooks/validate.go` (имя переменной, резерв `AFM_`); `cmd/afm/run.go` (сборка `combinedHooks` до докер-ветки, host-резолв/in-container резолв, `UnsetTransportVars` перед `Orchestrator.Run`); `cmd/afm/agent_environment.go` (`loadHookSecretLayers`); `pkg/docker/launcher.go` (`UsesLifecycleSecrets`, транспорт `-e AFM_HOOK_SECRET_<hookIdx>_<varIdx>` в `ReExec`).

## AI-verify: independent verification gate

`verify` on a stage is a **stage-completion gate**, not a new FSM status, not a
new `agents` phase, and not a rename of `review`: after a stage's agent finishes
and the existing file-probe passes (`.done`/artifacts, or `execution_summary.md`
for `agents:[auto]`), afm runs the declared verify steps — shell and/or a
dedicated read-only AI reviewer — before the stage is actually accepted as done.
Spec: `tmp/AFM_AI_VERIFY_IMPLEMENTATION_PLAN_v2.md` (design doc, not literally
mirrored by the shipped code in every detail — read the code, not the doc, for
exact symbol names).

- **`flow.VerifySpec`/`flow.VerifyStep`** (`pkg/flow/verify.go`) normalizes all
  three YAML forms (scalar string, one object, a list of objects) into
  `Steps []VerifyStep`, each either `Kind: VerifyShell` (`Run`) or
  `Kind: VerifyAgent` (`Command`+optional `Prompt`), plus an optional `Timeout`.
  `UnmarshalYAML` only rejects structural problems (unknown fields, nested
  lists, bad duration, empty list); `VerifySpec.validate(stageID)` — called from
  `Stage.validate()` in `pkg/flow/flow.go` — enforces the business rules with
  1-based step indices in the error text (`stage %q: verify[%d]: run and command
  are mutually exclusive`, etc.). `agent` is explicitly rejected as an unknown
  field with a hint to use `command` — it's not a second synonym.
  `Stage.validate()` also rejects `verify` on a `script:` stage (pre-existing
  rule, unchanged) and on a planning-only stage (new: verify gates *execution*
  results, so a stage with no implementation/review/autonomous phase can't
  declare one). `Stage.VerifyAgentCommands()` returns the de-duplicated list of
  agent aliases used by a stage's verify steps — used by Docker
  mounting/wrapper-generation discovery so a verifier-only command (e.g. author
  is `claude`, verifier is `codex`) still gets its binary mounted/shimmed even
  though it never appears in `Stage.Command`.
- **`config.SupportedVerifyCommand`/`ValidateVerifySpecs`** (`pkg/config/verify_resolve.go`)
  gate v1 to **codex only**, checked at preflight (`cmd/afm/run.go`, before
  `flow_started`/any stage runs) — not deferred to first use. A bare
  `codex-as-claude` (the real read-only shim) is accepted directly; a
  `docker.agents` recipe is accepted only if its `Type == RecipeTypeCodex` **and**
  `codexRecipesShimmed` is true (i.e. this run will actually generate the
  autoShim wrapper — Docker mode with `autoShim: true`). A bare `codex` binary
  (no recipe) is explicitly **not** supported — on the host it would run directly
  through `PATH` and doesn't understand `CODEX_VERIFY`/read-only mode at all, so
  the read-only guarantee would silently not hold (this was C1 in code review).
  `claude` is not supported either — it has no real read-only mode; `VerifyMode`
  only sets `CODEX_VERIFY=1`, which claude ignores, so it would run with
  `--dangerously-skip-permissions` and could edit the very artifact it's
  supposed to review.
- **`(*Orchestrator).RunVerification`** (`pkg/orchestrator/verify.go`) runs ONE
  fail-fast pass over `s.Verify.Steps` for a stage that already passed its file
  probe — no internal correction loop, no recursive re-invocation of the author;
  correction is entirely the caller's job via the pre-existing incomplete-retry
  path. It's wired in via **`gateWithVerify`**, which wraps an existing
  `completionCheck` (`CheckCompletion`/`CheckAutonomousCompletion`) so that
  `RunVerification` only fires when the base file-probe already passed AND
  `phase` is one of `phaseImplementation`/`phaseReview`/`phaseAutonomous` — never
  for planning (belt-and-suspenders on top of the parse-time rejection above).
  GET/status/refresh calls into `completionCheck()` from other code paths (see
  `retry.go`) never accidentally trigger a paid AI run, because `RunVerification`
  only runs once per real invocation of the gate, not on every probe.
- **Machine result contract — `pkg/orchestrator/verify` package (`result.go`).**
  `DecodeModelResult` is a strict, side-effect-free JSON decoder (`ModelResult`
  with `SchemaVersion`/`Verdict`/`Summary`/`Findings`): rejects markdown code
  fences, duplicate JSON keys at any nesting depth, unknown fields
  (`DisallowUnknownFields`), trailing content after the document, an
  unsupported `SchemaVersion` (only `SupportedSchemaVersion = 1`), an invalid
  `Verdict` enum value, and verdict/findings inconsistency
  (`validateVerdictConsistency`: `pass` can't carry a blocking finding,
  `needs_changes` requires ≥1, `inconclusive` requires a non-empty `Summary`).
  Capped at `MaxResultBytes = 256 * 1024`, checked on the FULL input before
  parsing (a truncated-but-valid-looking prefix must never be silently
  accepted). `Finding.Path`/`LineStart`/`LineEnd` are pointers — a finding is
  allowed to not be anchored to a specific file/line (e.g. "a required artifact
  is missing entirely").
- **File namespace — `pkg/orchestrator/stagefiles/verify.go`.** Under
  `<stageDir>/verify/`: `feedback.md` (active machine feedback, capped at
  `feedbackBudgetBytes = 12 * 1024` via `truncateUTF8` — a UTF-8-safe cut, never
  splitting a rune, with an explicit truncation marker; the on-disk `report.md`
  is NEVER truncated) and, per pass, `<verification-id>/{manifest.json,report.md,
  step-NN/{...}}`. `NewVerificationID()` mints a fresh id per pass (`v-<ts>-<hex>`,
  a package-var seed swappable in tests) — distinct from the retry `attempt`
  counter, which resets on every new agent invocation; the verification id only
  changes when the stage restarts verification from scratch. `SaveRawResult`
  (pre-validation) vs `SaveAcceptedResult` (post-validation, step-NN/result.json)
  distinguishes "the verifier answered something" from "AFM accepted it" — a step
  can have a raw result without an accepted one (parse/consistency failure).
  `WriteActiveFeedback`/`LoadActiveFeedback` round-trip a provenance HTML comment
  (`<!-- afm-verify verification_id=... step=... -->`) so callers can check
  whether cached feedback is still fresh; `ClearActiveFeedback` removes only the
  pointer file after a full pass — the per-pass history under
  `verify/<id>/` is never deleted. `ReportRelPath(verID)` is the ONLY function
  that knows the on-disk report path shape, reused by both the writer
  (`WriteReport`) and the HTTP reader (`pkg/server/verify_report.go`'s
  `handleVerifyReport`, `GET /api/stages/{id}/verify/{verID}/report`) — the
  server resolves it through `os.OpenRoot(runDir)` so a client-supplied
  `verID`/stage id can never escape the run directory via `..`/symlink; the
  `report_path` carried in the `verify_result` event/notice is for display only,
  never fed back to the server as a literal filesystem path.
- **Read-only Codex adapter — `scripts/codex-as-claude.sh`.** A dedicated
  `CODEX_VERIFY=1` mode (set only by `executor.RunVerifyAgent`, never by
  `RunPlanning`/`RunAgent`): never passes
  `--dangerously-bypass-approvals-and-sandbox` or any full-access flag, always
  requests the CLI's own `-s read-only` sandbox (ignores `CODEX_SANDBOX` in this
  mode), never escalates on error, and captures the exact final answer via
  `--output-last-message` (detected via `codex exec --help` at script run
  time — the flag exists on newer codex CLI releases) instead of aggregating
  intermediate `agent_message` events the way the normal (non-verify) path does.
- **`executor.Config.VerifyMode`/`RunVerifyAgent`** (`pkg/executor/executor.go`):
  a fresh session (no `--resume`), no `AFM_STAGE_DIR` (the verifier is not a
  dialog participant). Environment is filtered, not just extended: strips any
  inherited `CODEX_VERIFY` before re-adding it (`CODEX_VERIFY=1` only under
  `VerifyMode`), strips `IsCrossAgentTransportSecret` matches (lifecycle-hook/
  other-agent transport secrets riding in the afm process's own env, see
  Lifecycle Hooks Phase 3 above — a verifier must not be able to reflect a
  DIFFERENT agent's or hook's secret in its report), and strips
  `isClaudeAuthorCredential` matches (the AUTHOR's own claude auth must not leak
  into a differently-authenticated verifier invocation) while explicitly keeping
  the verifier's OWN resolved secret (`isVerifierOwnSecret(kv, e.cfg.Command)`).
  The equivalent filter for the shell-step path is
  `sanitizedShellVerifyEnv()`/`executor.IsCrossAgentTransportSecret` in
  `pkg/orchestrator/verify.go` — same filter function, reused rather than
  duplicated, applied to `cmd.Env` for the shell subprocess (shell verify has no
  recipe secret of its own to preserve, unlike the agent path).
- **Command-slot lease swap — `pkg/orchestrator/concurrency/lease.go`.** A stage
  spawned normally holds a `*Lease` on its author's command semaphore slot
  (`o.stageLeases`, populated by the spawn path). Each AI verify step calls
  `Lease.SwapTo(ctx, st.Command)`: same command → no-op fast path (never
  release+reacquire the sole slot on `max_parallel:1`, which would otherwise
  self-deadlock against a waiter for that exact slot); different command →
  release-before-acquire, cancelable via `ctx`. This is the single mechanism
  that prevents the AB-BA deadlock two stages could otherwise hit swapping
  author↔verifier command slots concurrently — afm never holds two command
  slots at once for one stage. `RunVerification`'s `defer` swaps the lease back
  to the author's command **only** on a `*VerifyRejectedError` outcome (the one
  case where `runWithRetry` immediately relaunches the author on the SAME
  lease, in the SAME call) — on every other outcome (pass/exec-error/
  inconclusive/timeout/interruption) `run(ctx,s)` is about to return anyway and
  `SpawnAgentLease` releases whatever the lease holds, so a blocking swap-back
  there would risk hanging forever waiting on a slot some OTHER stage holds
  (found in the 5th code-review round). `pauseAwareVerifyCtx` layers a
  Pause()/Revise() `interruptChans` signal on top of a context deadline so
  waiting for a busy verifier slot, or the verify subprocess itself, is
  interruptible the same way a normal agent run is.
- **FSM — `bus.EvVerifyFail`** (`pkg/orchestrator/bus/fsm.go`): an atomic
  sibling of `EvFail`, added specifically because a bare `Trigger(EvFail)` after
  a **read-then-act** status check (`verifyOutcomeStillOwned`,
  `pkg/orchestrator/retry.go`) left a TOCTOU window — `EvFail`'s `From == nil`
  accepts any non-terminal status, so a concurrent Pause()/Revise() that
  committed in that exact window would be silently overwritten by a stale verify
  outcome. `EvVerifyFail`'s `From` is deliberately restricted to
  `{Running, AwaitingApproval, Retrying, AwaitingUserInput}` — the same set
  `EvComplete` uses — so the store's CAS itself atomically rejects a stale
  transition instead of relying on the earlier read. It deliberately does
  **not** include `StatusPlanning`: planning never runs verify (see
  `gateWithVerify` above), so a verify-driven fail must never even attempt this
  transition against a planning stage — `commitVerifyFailure` is only called via
  `isVerifyDrivenError` (`pkg/orchestrator/errors.go`), which checks
  `errors.As` against `*VerifyRejectedError`/`*VerifyExecError` specifically, so
  a generic (non-verify) terminal completion error still goes through the
  ordinary `EvFail` path.
- **Typed errors — `pkg/orchestrator/errors.go`.** `VerifyRejectedError` embeds
  `*stagefiles.IncompleteWorkError` (via `Unwrap`) so it flows through the
  EXISTING incomplete-retry classification (`Classify` → `ClassIncomplete`) with
  no new retry path — `ReportID`/`Step` are metadata for the error text and the
  UI, not part of the classification. `VerifyExecError` is a distinct
  classification (`ClassVerifyFailed`), checked via `errors.As` **before** the
  substring-based `Classify` heuristics — a verifier's free-text summary
  containing something like "rate limit" must never be mistaken for a
  transport-retryable error.
- **Observability — non-FSM, live + durable, not in `flow.Phases()`.**
  `emitVerifyStarted`/`emitVerifyResult` (`pkg/orchestrator/verify.go`) publish
  `bus.EventVerifyStarted`/`bus.EventVerifyResult` (`"verify_started"`/
  `"verify_result"`) live via `o.ui.Publish` AND durably via
  `stagefiles.AppendNotice` into `notices.jsonl` — same pattern as
  `EventAutoAnswered` (see "Auto-answering questions" above) — so a client that
  reloads after the event still sees it via `notices.jsonl` replay. Usage/cost
  accounting for a verify agent call is tagged with the execution-purpose label
  `verifyExecutionLabel = "verify"` — deliberately NOT added to `flow.Phases()`
  (that list is the general runtime-phase vocabulary used for resume/recovery
  routing; verify is an orthogonal pass that only runs after a phase's own
  completion, not a phase itself). Dashboard: `feed-view-model.ts`'s
  `mapVerifyEvent` renders `Verify step N · <alias>` on start, then the outcome
  (pass=success tone, needs_changes/inconclusive=warning, any exec/interrupt
  error=danger tone) with a report link when `report_path` was non-empty.
- **Invariants worth re-checking before touching this code:** (1) verify never
  calls `Trigger(EvComplete)`/activates dependent stages itself — only the
  owning execution path's normal completion does, after `RunVerification`
  returns nil; (2) a stale/expired step timeout (`flow.VerifyStep.Timeout`) must
  never be silently accepted as a pass merely because the underlying `select`
  raced a done-channel against `<-ctx.Done()` in the verifier's favor —
  `verifyStepDeadlineRace` re-checks `stepCtx.Err()` immediately after ANY
  reported success, for both the shell and agent paths; (3) an interruption
  (Pause/Revise/full run cancellation) must never be recorded as a
  `needs_changes` verdict — `runVerifyShellCommand`'s and the agent path's
  interrupted/timeout branches are checked and returned BEFORE the "non-zero
  exit ⇒ synthetic needs_changes" branch, in that order; (4) a storage failure
  while persisting a result/report/feedback is `*VerifyExecError` (fail-closed),
  never a silently-accepted pass and never a silently-dropped rejection —
  `persistVerifyRejection` is the single ordered commit path (result → report →
  manifest → active feedback → THEN emit+return) for both the shell-derived
  synthetic rejection (`shellRejectionResult`) and a real agent `needs_changes`.

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
- **Dashboard: the dialog panel is gated by the actual presence of history, not just the stage type.** `App.tsx`'s `showDialog` used to be `interactive || autonomous` — a stage could have a real dialog history (an auto-answered question) and still not show the `DialogChannel` panel, because the panel wasn't mounted for it at all. A third signal `stage_has_dialog` was added (`/api/status`, the same file-presence pattern as `stage_autonomous`/`autonomous.flag` — checking for the existence of any `<phase>.dialog.jsonl` in the stage directory). `DialogChannel.tsx`'s own internal gate `hasContent` also accounts for `stage.hasDialog` (not just `entries.length > 0`) — otherwise a test mock with an empty `/dialog` fetch masks the bug, while in reality there's a window where `/api/status` already knows about the dialog but the panel's own polling hasn't pulled in `entries` yet. **The layout gate (`buildStageViews`'s `showDialog`, `pkg/server/stageview.go`) MUST match `DialogChannel`'s `hasContent`, or a reserved-but-empty dialog row renders as a full-height void in the center column (the "breaks when clicking a stage" bug — clicking a done `agents:[auto]` stage showed an empty center with not even the "Nothing to show for this stage" fallback, because `rowPanels` was non-empty).** So `showDialog = hasDialog || status == awaiting_user_input` — NOT `interactive || autonomous || hasDialog`: `hasDialog` covers auto-answered/history, `awaiting_user_input` covers a live question before `<phase>.dialog.jsonl` exists (and the "status outran the /dialog fetch" race). `interactive`/`autonomous` alone no longer reserve the row — that was exactly what produced the empty void for stages that never actually had a dialog.
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

**Dashboard:** the port from `server.port` is forwarded to the host via `-p <port>:<port>`, otherwise the UI is inaccessible outside the container. **Browser:** by default (`server.open_browser` absent/`false`) it is NOT opened — the dashboard URL is printed to the log with the hint `→ open this URL in your browser to follow the run`. With `server.open_browser: true` the browser is opened by a host-side opener: afm inside the Linux container can't open a browser on the macOS host itself (`runtime.GOOS=linux` → `xdg-open` without a display), so a separate helper process is launched on the host BEFORE re-exec, polls the forwarded port and calls `open`/`xdg-open`. Inside any container the `openBrowser` call is skipped (`config.InContainer()` — see "Container auto-detection" below; catches both afm's own re-exec via `AFM_IN_DOCKER=1` and a foreign container by marker file, where `xdg-open` is equally useless).

**Privileges (important):** the container starts as root, but the entrypoint (`docker-entrypoint.sh` + `gosu`) immediately drops privileges to the host uid/gid (`AFM_HOST_UID/GID`, passed from `os.Getuid/Getgid`) and sets `HOME=/home/afm`. So afm and the agents run as the same user as on the host — all writes to `~/.claude`, `~/.afm`, the project directory and `extra_mounts` belong to the host user, not root (no root-owned files and no permission conflicts with the host's claude). Under a non-root user, claude allows `--dangerously-skip-permissions` without `IS_SANDBOX`.

### Container auto-detection (`InContainer` vs `ReExecedIntoContainer`)

afm decides "am I in a container?" from **two** signals, deliberately split into two predicates in `pkg/config/config.go` because the old single `AFM_IN_DOCKER=1` check was conflating two different jobs:

- **`config.InContainer()`** — generic "running inside ANY container?" It is true if `AFM_IN_DOCKER=1` (afm's own re-exec) **OR** a conventional container marker file exists: `/.dockerenv` (Docker) or `/run/.containerenv` (Podman). This governs the **recursion guard** (`IsDockerEnabled()` returns `false` in any container) and the **browser-open skip**. So if someone drops afm into their OWN container (no `AFM_IN_DOCKER`) with Docker mode configured, afm now detects the marker and runs natively instead of attempting a nested docker re-exec — **auto-detect wins even over an explicit `docker.enabled: true` / `AFM_USE_DOCKER=1`** (same effect `AFM_IN_DOCKER=1` always had).
- **`config.ReExecedIntoContainer()`** — strict "is THIS process afm's own Docker re-exec?" It is the literal `AFM_IN_DOCKER=1` and nothing else. ONLY the transport consumers may key off it — autoShim wrapper generation, lifecycle-hook transport secrets (`AFM_HOOK_SECRET_*`), the `AFM_DOCKER_FILE_ROOTS` manifest decode, and the verify autoShim preflight — because those read env/state that only `docker.ReExec` produces. A foreign container has the marker but NONE of that transport, so feeding it generic containment would be wrong.

Consequence for **autoShim in a foreign container**: wrappers are NOT generated there (that path stays on `ReExecedIntoContainer()`), so a recipe-agent stage expects its binary already installed in that container. This combination is exotic and was already broken before (it attempted docker-in-docker); afm now runs natively rather than re-exec'ing. The launcher's `-e AFM_IN_DOCKER=1` (the *producer* of the transport protocol) is unchanged. Marker detection uses the injectable `containerMarkerPresent`/`containerMarkerPaths` seam so tests never consult the real `/.dockerenv` (a CI run inside a container would otherwise flip results).

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `AFM_USE_DOCKER=1` | Enable Docker mode without editing the config (overridden by container auto-detection: ignored inside any container) |
| `AFM_IN_DOCKER=1` | Set by afm's OWN re-exec inside the container — prevents recursion AND marks the transport protocol as available (don't touch). A foreign container that never sets this is still detected by marker file — see "Container auto-detection" below |
| `AFM_HOST_UID` / `AFM_HOST_GID` | Passed inside; the entrypoint drops root to this uid/gid (`gosu`), so writes to volumes belong to the host user |
| `AFM_DOCKER_IMAGE` | Override the image (e.g. for a local build) |
| `AFM_DEBUG` | Forwarded into the container by value (`-e AFM_DEBUG=…`, not a secret), so the re-exec inside also logs the agent input; on the host it's set by the `--debug` flag in `PersistentPreRunE` |
| `AFM_ACCOUNTING` | Forwarded into the container by value (`-e AFM_ACCOUNTING=…`, not a secret) when set — the cost-display switch is resolved INSIDE the container (dashboard server + `afm check`/`report` call `config.AccountingConfig.IsEnabled` there), so the host env must cross over. `=0` hides cost display, `=1` overrides a config `accounting.enabled: false`; unset → config decides |
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

## Docker Project File Browser

A **Docker-only**, strictly **read-only** file browser in the dashboard: browse the project mount + explicitly-allowed extra mounts, view syntax-highlighted files, see a per-file `HEAD → working tree` diff, and insert `[AFM file: "<container-path>"]` references into plan/question review comments. On a host run it is entirely inert: no manifest → empty workspace → `capabilities.file_browser=false` → `/api/files/*` returns `404`, no UI. Spec: `docs/superpowers/specs/2026-09-03-docker-project-file-browser-design.md`, plan: `docs/superpowers/plans/2026-09-03-docker-project-file-browser.md`.

### Enabling + the allowlist manifest (host → container)

- **`docker.file_browser.enabled` (`*bool`, default `true`)** — the browser is ON unless explicitly turned off. `IsEnabled()` resolves in priority order **env `AFM_FILE_BROWSER` > config > default `true`**: `AFM_FILE_BROWSER=1/true` force-enables and `=0/false` force-disables regardless of config (empty/unset → config decides); config `enabled: false` disables, `nil` (unset)/`true` keep it on. `IsEnabled()` is read only host-side in `cmd/afm/run.go` (before re-exec), so the env decision governs whether the file-root manifest is forwarded into the container at all — no need to forward `AFM_FILE_BROWSER` itself. `mergeFile` (`pkg/config/config.go`) must copy `overlay.Docker.FileBrowser.Enabled` like every other `Docker.*` field, or a project-layer `file_browser: {enabled: true}` is silently dropped on merge (found by review; regression test `TestLoadFrom_FileBrowserEnabledMergesAcrossLayers`). Env-priority regression: `TestFileBrowser_EnvOverridesConfig`.
- **`docker.extra_mounts` is scalar-or-object** — `config.ExtraMount{Path, Name, Browse}` / `config.ExtraMounts` with a custom `UnmarshalYAML` accepting both a legacy scalar string (→ `browse:false`, stays private) and a `{path, name, browse}` mapping. Only `browse:true` mounts are browseable. This is the safe default: an afm upgrade never exposes a credential mount to the browser. `Validate()` (wired into `LoadFrom`) rejects empty/duplicate paths.
- **The host launcher is the single source of browseable roots.** `pkg/docker/manifest.go`'s `BuildFileRootManifest(projectContainerPath, mounts)` emits a versioned `FileRootManifest` (project root always + one `extra-N` per `browse:true` mount, using the CONTAINER path, `mount_read_only:true`; NEVER credential/service mounts). It is base64-`RawURLEncoding`-encoded and passed as `-e AFM_DOCKER_FILE_ROOTS=<enc>` (`FileRootsEnvVar`) ONLY when `FileBrowserEnabled && len(Roots)>0`. In-container, `cmd/afm/run.go` decodes it (gated on `AFM_IN_DOCKER=1`), maps `FileRootManifestEntry{ContainerPath}` → `workspace.Root{Path}`, and builds `workspace.New(roots)` → `server.Config.Workspace`. Paths aren't secrets; encoding is just for unambiguous transport. A decode/`New` error is logged (existing stderr idiom) and non-fatal — the browser just stays off.
- **`resolveMountPath(projectRoot, path, home)` (`pkg/docker/launcher.go`) is the ONE path-resolution helper**, reused by BOTH the `-v` mount loop (host=`$HOME`, container=`/home/afm`) AND `BuildFileRootManifest` (via `containerPathFor` = `resolveMountPath(_, _, containerHome)`): `~`→`expandHome`; relative→`filepath.Join(projectRoot, path)`; absolute→unchanged. Without this, a relative `browse:true` mount (`../shared`) diverges — the `-v` would emit invalid `../shared` while the manifest resolved `/work/shared`, so the browser would point at an unmounted path (found by review; `TestReExec_ExtraMountPathResolution`). In afm Docker mode the project is mounted at the same absolute path host and container, so relative/absolute resolve identically both sides; only `~` differs by home.
- **Loopback bind when the browser is on.** Port publish is `-p 127.0.0.1:<port>:<port>` (not `0.0.0.0`) iff `FileBrowserEnabled && len(Roots)>0` — the arbitrary-file GET API must not be LAN-reachable. Because `file_browser` now defaults ON, this loopback bind is the default for Docker runs; a run that must be LAN-reachable has to explicitly disable the browser (`file_browser: {enabled: false}` or `AFM_FILE_BROWSER=0`), which restores the `0.0.0.0` publish.

### `pkg/server/workspace` — the secure filesystem (the security boundary)

A self-contained package (never inlined into the 688-line `handlers.go`). Domain vocabulary is defined ONCE in `types.go` (`Root` internal-with-`Path`, `RootView` serialized-without-`Path`, `Entry`/`Page`/`File`/`Reference`/`Diff` with JSON tags matching the HTTP shapes so handlers encode them directly). `fs.go` holds the `FS` interface + sentinel errors; `roots.go` holds `validateRelPath`.

- **`validateRelPath` (`roots.go`) is the only path-input gate.** Checks the RAW path for `..` segments BEFORE `path.Clean` (Clean turns `a/../b`→`b`, which would sneak past a post-clean check); rejects absolute/NUL; `.git`/`.afm` segments → `ErrNotFound` (hidden). Every `FS` method funnels through `resolve(rootID, relPath)` (`workspace.go`) = root lookup (`byID`, miss→`ErrInvalidRootOrPath`) + `validateRelPath` — written once, not per method.
- **Linux `openat2`, no check-then-open.** `access_linux.go`: each root opened as an `O_DIRECTORY|O_PATH|O_CLOEXEC` dir fd; every child open via `unix.Openat2` with `RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS` (ELOOP/EXDEV→`ErrSymlink`, ENOENT→`ErrNotFound`). `New` probes `openat2` once; on `ENOSYS` (kernel < 5.6) or non-Linux (`access_other.go` stubs) it degrades to **zero roots** (capability off) — never a weaker fallback. Kernel enforces containment; no string math.
- **List/Read/Reference/Diff.** `list.go`: secure-open the dir fd, `ReadDir` in bounded batches capped at `maxDirEntries` (`Page.Truncated`), hide `.git`/`.afm`, dirs-first case-insensitive sort, opaque `root+path`-bound cursor; only REGULAR files are `selectable:true` (FIFO/socket/device listed but not selectable). `content.go`: `Read`/`Reference` open `O_RDONLY|O_NONBLOCK` (a FIFO would otherwise block the goroutine forever — `ctx` can't cancel a blocked `openat2`) and require `Mode().IsRegular()`; `Read` caps at 2 MiB (`maxContentBytes`, → `ErrTooLarge`), NUL/`!utf8.Valid`→`ErrBinary`, sets `ETag`; `Reference` skips the size/binary gate (a large/binary file is still referenceable). `detectLanguage` (`language.go`, single definition) maps `.go/.ts/.tsx/.js/.jsx/.mjs/.cjs/.py/.pyi` → grammar, else `plain`. `buildMarker(abs)` = `[AFM file: <json-encoded-path>]` (JSON-encoded so quotes/backslash/newline are safe).
- **Diff (`diff.go`) reads git objects ONLY from a repo inside the root.** `Read` runs FIRST (its `openat2` validates the path, rejecting `..`/symlink/`.git`) before any git. Repo discovery locates `.git` via the secure fd (a symlink `.git` is refused structurally); a gitfile's `gitdir:` is resolved and REQUIRED (via `EvalSymlinks` + separator-terminated prefix) to live inside the root, else `ErrDiffUnavailable`; git runs with an explicit verified `--git-dir` (never `-C` re-discovery). Baseline is size-checked with `cat-file -s` BEFORE reading — an oversize baseline skips the in-memory `udiff.Unified` (`go-udiff`) entirely (no OOM). No shell, 3 s timeout, HEAD blob only. Real infra errors (timeout, git-not-found) → `ErrReadFailed`, not a bogus "added". Binary-no-repo → `ErrDiffUnavailable` (repo discovery before the binary short-circuit). The `.git`-escape, OOM, FIFO, and binary-order fixes came from an external review and are proven in a real Linux container (`TestDiff_GitSymlinkRefused`/`TestDiff_GitfileOutsideRootRefused`/`TestDiff_OversizeBaselineTruncated`/`TestReadReference_FIFO_DoesNotHang`). **The Linux-tagged workspace tests run only in Docker/CI, not on a macOS dev host — verify with `GOOS=linux go build/vet/test -c ./pkg/server/workspace/` and run them in a `golang` container.**

### HTTP: `/api/files/*` (GET-only, JSON errors)

`server.Server` holds a `workspace.FS`; `Shutdown` closes it. `handleStatus` sets `capabilities.file_browser = workspace != nil && len(Roots())>0`. `files_handlers.go`'s `routeFiles` (registered `mux.HandleFunc("/api/files/", …)`) returns `404` for non-GET OR nil/zero-root workspace, sets `X-Content-Type-Options: nosniff`, and dispatches `roots`/`tree`/`reference`/`content`/`diff`. Errors go through a scoped `writeFilesError(w, status, code)` → `{"error":<code>}` JSON (the ONLY place the server deviates from its plain-text `http.Error` convention — the rest is unchanged); `filesErrStatus` maps sentinels: `invalid_root_or_path`400, `not_found`404, `diff_unavailable`409, `file_too_large`413, `binary_file`415, `symlink_not_supported`422, else `read_failed`500. Error bodies never carry an absolute path or raw git stderr; `filesRoots` returns `RootView` (no `Path`). `content` supports `If-None-Match`→`304`.

### Dashboard: `components/file-browser/*` (standalone, invoked via a provider)

- `api/files-client.ts`: typed `getRoots/getTree/getReference/getContent/getDiff` + `FilesApiError{code,status}` (parsed from the JSON error body). `use-status.ts` maps `capabilities.file_browser`→`capabilities.fileBrowser`.
- `FileBrowserProvider` wraps the app (mounted once in `App.tsx`, threaded `enabled={capabilities.fileBrowser}`) and exposes `openBrowser()` + `pickFiles(onInsert)` + `enabled`. `FileBrowserModal` = roots + lazy `FileTree` (generation-guarded against a root-switch race + retryable pagination) | `FileViewer`/`DiffViewer` (FILE/DIFF tabs) | selection chips + Copy/Insert. `FileViewer` is the ONLY `dangerouslySetInnerHTML`, fed exclusively by `highlight()` (`highlight.js/lib/core`, four grammars, `escapeHtml` for plain) — source never touches the Markdown renderer (no XSS). `DiffViewer` renders lines as plain React nodes. A live `modifiedAt` + Reload (If-None-Match, 304 keeps content) covers files an agent is editing.
- **Capability gates the whole UI, centrally.** The header folder button AND the "Attach project file" button inside `PasteableTextarea`'s `AttachFileButton` render only when `enabled` — so a host run (capability false) shows no picker and makes no `/api/files/*` call (the button-in-comment gap was a review finding). `allowFileReferences` is set only on `PlanPanel`'s line comment and `DialogChannel`'s per-line question comment (NOT the custom-answer box, NOT AgentNote). An in-flight `getReference` is generation-guarded so a stale response can't restore a selection after a run change / submit / file switch.
- **Fully theme-aware.** `skins/base/file-browser.css` is tokenized (no hardcoded colors — `var(--amber/--coral/--mint/--violet/--ink*/--panel-bg/--bg-elev)`), including the hljs token rules; every skin (`base`/`coffee`/`goga`/`novacorps`) `@import`s it and defines those tokens in both `data-theme=dark`/`light` blocks, so it adapts to all skins × light/dark for free.

### Reference marker → feedback (no new machinery)

Selecting files inserts `[AFM file: "<absolute container path>"]` markers at the caret (same `use-caret-insert` splice as image paste) into the comment. They ride the EXISTING `revise`/`dialog-answer` chains → `feedback.md`/`answer.json` → the agent, which reads the path with its own tools. No bytes copied, no event-log/FSM change. Absolute container path (not project-relative) so it resolves regardless of `flow.root_dir`. Verified live (real image, real container, real browser): folder button, roots excluding the credential mount, highlighting, `HEAD→worktree` diff, `../`→400 / `.git`→404, and a picker-inserted reference reaching `feedback.md`.

## Dashboard shell — UX redesign (single-workspace + graphite)

The dashboard was reworked from a three-column resizable layout into a **stage rail + one unified workspace with tabs**. The old `FlowHeader`/`Footer`/`DashboardLayout`/`EventFeedPanel` are gone; `PanelFrame`/`Maximizable` survive only because `PlanPanel`/`DialogChannel` still wrap in them. Behavior (hooks, API calls, FSM) is unchanged — this was a presentation refactor. Spec/plan: `tmp/ux-redesign-plan.md` (working doc, gitignored).

- **Default skin is `graphite`** (`pkg/config`'s `ThemeGraphite`), replacing `coffee`. `theme: coffee` is a legacy alias normalizing to graphite (deprecation warning). The default is encoded in three coupled places that must change together: the HTML literal (`index.html`/`index.dev.html`), `config.ThemeGraphite`, and the server replace-anchor (`pkg/server/server.go`) — the string-replace silently no-ops if they diverge. `skins/graphite/index.css` authors the graphite palette; `skins/base/tokens.css` derives semantic tokens from legacy vars so every skin gets them.
- **Semantic token layer** (`skins/base/tokens.css`, imported first by every skin): `--surface-*`/`--text-*`/`--accent-primary(-rgb)`/`--attention(-rgb)`/`--success(-rgb)`/`--danger(-rgb)`/`--separator-subtle`/`--radius-*`/`--font-*`/`--focus-ring`/`--overlay-scrim`/`--shadow-overlay`. New components use ONLY these (theme-aware across all skins × dark/light for free); legacy `--bg`/`--ink`/`--mint`/`--amber`/`--coral` bridge to them.
- **Shell composition (`app/App.tsx`)**: `GlobalHeader` (brand + flow name/desc left, `RunMetrics` — Started/Elapsed/Idle/Backoff, tabular-nums — center, Files/theme/notifications/connection right) → `ReviewBanner` → `<main>` `DashboardShell` (`StagesList` rail + `.workspace`). Footer removed; progress (done/total) moved into the rail head (`StagesList` `progressDone`/`progressTotal`). **The top level is a flex column** — `#root { display:flex; flex-direction:column; height:100% }`, `#main { flex:1; min-height:0 }` (NOT the old `calc(100vh - 61px)`): when `ReviewBanner` is visible the layout absorbs it instead of returning a page-level scroll (the mobile drawer/scrim are `position:absolute` within `.dashboard-body`, not `fixed top:61px`, so they too sit below header+banner).
- **The stage rail is resizable** (`DashboardShell`): a draggable `.rail-resizer` between rail and workspace sets `--rail-width` on `.dashboard-body` (default `RAIL_DEFAULT=300`, clamped `[190,560]`, persisted to `localStorage` `afm.railWidth`, double-click resets, arrow keys on the `role="separator"` for a11y). `#stages-panel` has `flex:1` so it fills the widened column (else the grip floats past the panel edge). Below 900px the rail is a slide-over **drawer** — `matchMedia`-driven `inert` when closed (keeps its buttons out of the tab order), focus moves into it on open and back to the ☰ toggle on close; the resizer is hidden.
- **The `useWorkspaceView` reducer IS the source of truth** (`hooks/use-workspace-view/`, `workspaceReducer`): views are `feed`/`attention`/`plan-history`/`dialog-history`. `App` calls it (no more parallel `activeTab`/`lastAttentionKey` state) with a `suppressed` signal that is true while an editable element is focused OR any overlay is open (Files / AgentNote / pre-note / ReviewNotes) — a new arrival then only glows, never steals focus (rule 9). `AttentionItem` carries `{stageId, kind, episode}` where `episode` = `stage.updatedAt`, so a **repeat** attention of the same stage/kind (e.g. a second dialog question) is a fresh arrival that auto-opens even if polling missed the intermediate `running` snapshot — the same episode-key idea `use-desktop-notifications` uses. Resolve-advance + auto-open-once are in the reducer; App consumes its result.
- **Tabs = Feed + a contextual `detail` tab + (separately) a glowing `beacon`.** The `detail` tab always reflects the *actual* workspace body — the stage's own attention when `view==='attention'`, otherwise the selected stage's name/history — so history is never disguised as attention (rule 13). Any unresolved attention that isn't currently shown is a **separate `beacon` tab** (glow + kind + count, never `active`); clicking it opens that attention. `WorkspaceHeader`'s `attentionNav` prev/next (`N / M`) steps the queue. A **Plan | Dialog** switch (`.history-switch`) appears in history view when the stage has both artifacts, so a finished stage's plan stays reachable. Title flash / favicon / header dot track GLOBAL unresolved attention, not just the workspace stage.
- **Feed (`components/feed-workspace/`)**: `FeedWorkspace` renders a messenger view of the REAL event log (no fabricated text — plan §6). `feed-view-model.ts` maps each event → `FeedItem` (actor `agent`/`user`/`system` → side left/right, tone, kind); `agent_action tool="text"` renders as **agent prose** (the agent's thoughts/narrative — this is where they live, driven by events, not by any raw log), other tools as compact mono rows; `groupFeedItems` merges consecutive same-side+stage items into bubbles. Feed is the single view — stick-to-bottom + Jump to latest; the only control is the scope switch **This stage | All** (`useFeedScope`, `afm-feed-scope`), shown when a stage is selected. `base/feed-workspace.css` replaced `base/event-feed.css`. **The old Feed/Log segmented toggle and the raw-log view were removed** (`useFeedMode`/`afm-feed-mode`, `useStageLog`, `LogEntry`, `base/log-panel.css`, and the backend `GET /api/stages/{id}/log` handler are all gone) — agent thoughts already surface in the Feed, so the separate raw-log mode was redundant.
- **Approval/Question** are the existing `PlanPanel`/`DialogChannel` (logic unchanged) rendered in the workspace; `base/*.css` scopes them under `.workspace-body` into wide documents with a fixed/sticky bottom action bar and the redundant `PanelFrame` title hidden. Options render full-width, normal case.
- **Input submit + attachments live in the shared `PasteableTextarea`** (used by plan/question line comments, the custom dialog answer, and `AgentNoteModal`): `Cmd/Ctrl+Enter` submits via one `onSubmit` prop (so every field behaves the same — a regression here was the round-4 report); the paperclip is a **menu** with *Choose project file…* (gated on the Docker file-browser capability) and *Upload image…* (always available — the attachment upload endpoint works in host mode too, `useImagePaste.uploadFiles`). A failed upload stays as a retryable chip with a visible reason. `useImagePaste` guards a stage-change race (a slow upload started for stage A never lands in stage B's answer) via a `stageId` generation. Two raw `<textarea>`s that don't use the shared component (`ReviewNotesModal` edit, `FileViewer` line comment) wire `Cmd/Ctrl+Enter` directly.
- **Docker file browser** (`FileBrowserModal`): mobile tree slide-over (`inert` when closed, focus into it on open / back to the ☰ toggle on close, Escape closes the tree before the whole overlay), a visible attention shortcut in the header back to a waiting question/approval, and a focus trap that starts on the visible Close (not a `display:none` mobile toggle).
- **Hardened across four review rounds + live verification** (graphite/goga, real browser + real run): single-workspace attention (auto-open, beacon, prev/next, repeat questions), resizable rail, mobile drawer a11y, keyboard nav, AA contrast, attachment menu, `Cmd/Ctrl+Enter`, no page-scroll under the review banner. `pkg/web/dashboard` frontend suite is green; the redesign is presentation-only over an unchanged orchestrator/FSM/API. Shipped as **v1.0.0**.

## Agent memory (directory store + pattern-extraction chain) — v3

Each stage runs an agent in an isolated context — whatever it learns (an API's real behavior, a required build flag, a rule it broke and had to correct) evaporates when the stage finishes. Agent memory is an opt-in pipeline that runs after a stage completes, distills its session into a small set of **project patterns**, and merges them into a human-readable Markdown rules file. v3 **replaces** v2's structured-YAML-store model entirely: no `Finding`/`evidence`/`confirm_count`/`first_seen`/`last_seen` metadata, no per-stage relevance retrieval, no consolidator agent, no session store. What v3 keeps from v2 is only the scheduling/lifecycle envelope (background, best-effort, serialized, never touches the FSM). Supersedes v2. Spec: `docs/superpowers/specs/2026-08-28-agent-memory-v3-design.md`, plan: `docs/superpowers/plans/2026-08-28-agent-memory-v3.md`.

- **Enabling: `memory.path` is a DIRECTORY, not a file.** `flow.MemoryConfig` (`pkg/flow/flow.go`) has `path` (relative to `root_dir` — **non-empty enables the whole feature**), `max_rules` (default `25`, set in `ParseFile`), `commit` (default `false`). `cmd/afm/run.go` resolves `memDir` (absolute, against `agentRootDir` if `flow.root_dir` is set, else `rootDir`) and passes it as `Options.MemoryDir` — orchestrator code never sees `flow.MemoryConfig.Path` directly, only the resolved absolute `MemoryDir` (`""` = disabled). The project-wide file is always `<MemoryDir>/memory.md` (`memory.ProjectFile`, fixed name).

- **Per-stage opt-in is an OBJECT, `Stage.Reflect *Reflect`, not a bool.** `flow.Reflect{File, Mode string}` (`pkg/flow/flow.go`): `File` is the stage's own memory file, resolved relative to `MemoryDir` (`memory.StageFile(dir, file)`, may be `"sub/dir/file.md"`); `Mode` is `"r"`/`"w"`/`"rw"`, defaulted to `"rw"` in `ParseFile` when empty. `(*Reflect).CanRead()`/`CanWrite()` gate injection and the write chain respectively. `Flow.validate()` rejects `reflect` on any stage when `memory.path` is empty ("reflect requires memory.path"), an empty `file`, or an invalid `mode`. A `script:` stage may declare `reflect` — its **write** chain is silently skipped at runtime (`stage.IsScript()` in `maybeRunReflection`, no agent session to reflect on), but its `r` injection (if `mode` allows reading) still applies.

- **The write chain is FOUR build steps — three LLM agents plus one code step — but they run at END-OF-RUN, not per-stage.** After a stage completes, `(*Orchestrator).maybeRunReflection` (`pkg/orchestrator/reflection.go`, gate `MemoryDir != "" && stage.Reflect != nil && stage.Reflect.CanWrite() && !stage.IsScript()`) runs ONLY step 1 (reflect/capture) in the background: it writes the stage's `reflect_dataset.yaml` and STOPS. The build chain (steps 2-5, the shared `distill` helper) is deferred to `runEndOfRunMemory` — so memory is built once, seeing every stage together, instead of N times mid-flow (deliberate: distilling after each stage is wasteful and each pass only sees one stage). The five steps of the distill pipeline:
  1. **reflect** (`memoryKindReflect`, prompt `assets/prompts/reflect.md`) — fresh-context agent, reads the stage's session (`<phase>.log`+summary/plan, PLUS any direct user input in the stage dir — `*.dialog.jsonl` user dialog answers and `prenote.md`/`feedback.md` user notes, if present) and writes an RL-style dataset to `<stageDir>/reflect_dataset.yaml`: one YAML doc with `project_level`/`session_level`, each item `{prompt, chosen, rejected, source}` (`source` ∈ `user`/`log`) in block-literal style (`source` is a plain scalar). **User input is prioritized over logs (strong bias).** Direct user input (dialog answers, prenote/feedback) is framed as the highest-priority signal, and each item is tagged `source: user` when derived from it (else `log`); `source: user` wins when an insight draws on both. That provenance rides through the chain: `aggregate` prefixes a user-derived pattern's line with `[user]`, and `prioritize` sends `[user]` patterns to High by default (log-only patterns fill Medium/Low unless a core architectural/global-correctness rule clearly outweighs them), then strips the marker so nothing leaks into `high.md`/`memory.md`. Consequence: user-derived rules land in High, and since `update` drops Low-then-Medium to meet `max_rules`, they're de-facto protected from eviction — no separate mechanism needed. **P1 kept from v2:** a hard EXCLUDE list in the prompt keeps `execution_summary.md` format, `$AFM_STAGE_DIR`, dialog/question naming, plan-approval/autonomous/retry/backoff mechanics, and "read the memory files" itself OUT of the dataset — memory stays project-specific, not framework noise.
  2. **aggregate** (`memoryKindAggregate`, `assets/prompts/aggregate.md`) — reads the dataset(s), extracts mutually-exclusive project-level patterns with no specific citations, writes a numbered `name — description` list to `<logDir>/patterns.md`; a pattern derived from ≥1 `source: user` item gets a `[user]` line prefix (provenance class, not a citation — carries the user-input priority forward).
  3. **prioritize** (`memoryKindPrioritize`, `assets/prompts/prioritize.md`) — assigns every pattern to exactly one of High/Medium/Low (all three tiers must be used), with a **strong bias**: `[user]`-prefixed patterns → High by default, log-only → Medium/Low unless a core rule clearly outweighs them; strips the `[user]` marker from the output; writes clean `## High`/`## Medium`/`## Low` sections to `<logDir>/prioritized.md`.
  4. **code select-High (no LLM)** — `memory.SelectHigh(prioritized string) string` (`pkg/memory/select.go`) parses `prioritized.md` and returns just the `## High` section; written to `<logDir>/high.md` via `memory.AtomicWrite`. Empty High section → the chain stops here with a `reflectNotice`, no `update` call.
  5. **update** (`memoryKindUpdate`, `assets/prompts/update.md`) — fresh-context agent, reads `high.md` + the existing target file (may not exist yet — treated as empty), merges High patterns into existing patterns where they group well, re-tiers internally, caps at `max_rules` (drops Low first, then Medium, to preserve High — the `<MAX_RULES>` token in the prompt is substituted with `strconv.Itoa(spec.maxRules)`), and rewrites the target file in place, English only, in the format `# Project rules` / repeated `## [Pattern Name]` blocks with **priority encoded ONLY by block order** (no tier headings or words in the output).
  This shared chain lives in `(*Orchestrator).distill(ctx, stageName, datasets, logDir, targetFile)` (`reflection.go`), serialized by `o.reflectMu` (one pipeline writing shared files at a time) — now called ONLY from `runEndOfRunMemory` (see below): once per reflect-writing stage (`targetFile` = the stage's own `Reflect.File`, `datasets` = just that stage's) and once for the project file (`targetFile` = `memory.md`, `datasets` = all). All steps go through the seam `o.runMemoryAgent func(ctx, memoryAgentSpec) error` (defaulted to `execMemoryAgent`); tests override it via `stubMemoryAgentByKind` (`reflection_test.go`) keyed by `spec.kind`. Agent isolation unchanged from v2: fresh session (no `--resume`), no `AFM_STAGE_DIR`, separate `reflect.log`/`aggregate.log`/`prioritize.log`/`update.log` per step under the stage dir (end-of-run: under the run dir). `reflect_dataset.yaml`/`patterns.md`/`prioritized.md`/`high.md` stay on disk as auditable byproducts.

- **End-of-run pass builds EVERYTHING (per-stage files AND the shared file), steps 2-5 only.** `(*Orchestrator).runEndOfRunMemory(ctx)` (renamed from v2's `runFinalReflectionOnce`, same trigger point in `Run()`'s event loop, guarded by `o.finalReflectDone`) does two things, in order: **(1)** for each stage in `o.opts.Stages` with `reflect:` write mode, `!IsScript()`, and its `reflect_dataset.yaml` present on disk → `distill([that dataset]) → memory.StageFile(...)` (its own file), gated by the stage's `reflect.mode` (NOT the global `memory.mode`), iterated in declaration order for determinism; **(2)** if `memory.mode` allows project writes (`CanWriteProject()`) → glob `<RunDir>/*/reflect_dataset.yaml` and `distill(all) → memory.ProjectFile(MemoryDir)` (`<MemoryDir>/memory.md`). Step 1/reflect is NEVER re-run here (the datasets were captured per-stage during the run). If `memory.commit == true`, `memory.Commit(MemoryDir, "chore(memory): update project memory")` (`pkg/memory/commit.go`) runs afterward — best-effort, notice-only on failure. **Consequence of building at end-of-run:** per-stage memory files, like `memory.md`, are NOT visible mid-run — an earlier stage's reflection reaches a later stage only on the NEXT run's injection, never within the same run. This is deliberate (build once, seeing all stages, instead of N wasteful mid-flow passes).

- **Injection (read side) — opt-in, gated by two axes.** `(*Orchestrator).memoryBlockForStage(s flow.Stage) string` (`pkg/orchestrator/memory_inject.go`, wired as `prompts.Inputs.MemoryBlock` in every `runXxxAgent`/`*WithFeedback` in `agents.go`, recomputed per stage per `prompts.Build` call):
  - **Participation gate** — `o.opts.Memory.UseFor(s.MemoryUse)`: the stage's own `memory_use` (`Stage.MemoryUse *bool`) if set, else the global `memory.memory_use` (`MemoryConfig.MemoryUse`, **default `false`**). If it resolves false → returns `""` (nothing injected). This makes injection opt-in.
  - If participating: names `memory.ProjectFile(MemoryDir)` (the shared `memory.md`) only if `o.opts.Memory.CanReadProject()` (global `memory.mode` is `r`/`rw`) AND the file exists; PLUS `memory.StageFile(MemoryDir, s.Reflect.File)` only if `s.Reflect.CanRead()` (`reflect.mode` `r`/`rw`) AND that file exists. `""` when nothing applies.
  - Two independent mode axes: `memory.mode` governs the shared `memory.md`; `reflect.mode` governs the stage's OWN file. `memory_use` is the master read switch per stage. The agent reads the named absolute path(s) itself — no inlining, no relevance slicing (v2's `Select`/`Render`/`retrieval_threshold`/`core_confirm_count` are gone).

- **`pkg/memory` (rewritten, small, no LLM):** `paths.go` — `ProjectFile(dir)`/`StageFile(dir, rel)` path helpers plus `AtomicWrite(path, data)` (temp+rename, `MkdirAll` on the parent); `selecthigh.go` — `SelectHigh(prioritized string) string` (deterministic parse of the prioritize step's `## High` section); `commit.go` — `Commit(dir, message) (committed bool, err error)` — `git -C <dir> add .` + a `-- .`-scoped `diff --cached --quiet`/`commit` so a memory commit never sweeps unrelated staged files elsewhere in the repo; no push, `committed=false, err=nil` when there's nothing staged. The heavy v2 types (`Finding`/`Store`/`Reconcile`/`Evict`/`Select`-by-metadata/`Load`/`Save`) are deleted — v3 has no structured store to load/save/evict, agents write the Markdown files directly.

- **Serialization + shutdown bookkeeping — unchanged from v2.** `o.reflectMu` still serializes one write-chain at a time across shared files; `o.pendingReflections atomic.Int32` still gates `shouldExit()`; `SpawnDetached` still keeps these agents off the `max_parallel` semaphore and out of the active-agent marker, tracked in `agentWG` for clean shutdown.

- **Best-effort, never touches the FSM — unchanged from v2.** Any step failing aborts the rest of the chain for that stage; a `reflect_failed` notice is surfaced live + durably (`o.reflectFailed`/`o.reflectNotice` → `bus.EventReflectFailed` + `stagefiles.AppendNotice`); the stage's own FSM/status is untouched either way. No crash recovery mid-chain.

- **Three overridable embedded prompts, not two.** `orchestrator.Prompts` carries `Reflect`/`Aggregate`/`Prioritize`/`Update` (`Consolidator` removed). `cmd/afm/run.go`'s `loadPrompts` loads `reflect.md`/`aggregate.md`/`prioritize.md`/`update.md`; `consolidator.md` (and the earlier v1 `updater.md`/`compressor.md`) are deleted. `buildMemoryPrompt(Prompts, memoryAgentSpec)` (`pkg/orchestrator/memory_prompts.go`) has one case per kind (`memoryKindReflect`/`Aggregate`/`Prioritize`/`Update`), each appending the concrete absolute file-I/O instructions (which files to read, the EXACT file to write, "do not modify any other file") to the loaded template.

- **Read/write control (v3.1):** `memory.mode` (`MemoryConfig.Mode`, `r`/`w`/`rw`, defaulted to `rw` in `ParseFile`) governs the **shared `memory.md`** lifecycle: `CanReadProject()` (r/rw) gates injecting it; `CanWriteProject()` (w/rw) gates whether `runEndOfRunMemory` (`reflection.go`) writes it (with `mode: r` the end-of-run pass distills nothing into `memory.md` — but per-stage `reflect` files, governed by each stage's `reflect.mode`, are still built by the same end-of-run pass). `memory.memory_use` (global, **default false**) + `Stage.MemoryUse *bool` (per-stage override, nil=inherit) form the participation gate for **reading** only — writing (per-stage via `reflect`, global via `memory.mode`) is unaffected by `memory_use`. Validation: `memory.mode` must be `r`/`w`/`rw` if set.
- **Config knobs (`flow.MemoryConfig`), defaulted in `ParseFile` when memory is enabled:** `max_rules` (patterns kept per file, project and per-stage, default `25`, enforced inside the `update` prompt via `<MAX_RULES>` substitution — **no code-side cap on top**, YAGNI: if the agent overshoots, the next `update` call self-corrects), `commit` (default `false`). **Removed from v2:** `project_file`, `max_findings`, `retrieval_threshold`, `core_confirm_count`, `final_reflect` (the end-of-run pass now always runs unconditionally when memory is enabled and ≥1 dataset exists — no separate flag).

### `afm memory rebuild` — offline backfill via `pkg/memorypipeline` (shared two-phase engine)

`afm memory rebuild` (`cmd/afm/memory_rebuild.go`) rebuilds memory from a completed run's session logs without starting a new run or touching that run's FSM/event log. It reuses the SAME distill chain described above (reflect → aggregate → prioritize → select-High → update), but the chain itself lives in a standalone package, `pkg/memorypipeline`, factored out so the live end-of-run path and the offline CLI command share one implementation instead of diverging.

- **`pkg/memorypipeline.Pipeline` — a two-phase Capture/Finalize engine, not a single call.** `Pipeline.CaptureStage`/`CaptureAll` (`rebuild.go`) is phase 1: for each write-reflect stage it either reuses a valid canonical `reflect_dataset.yaml` (default) or runs the `reflect` agent fresh (`--force-reflect`, or no valid canonical present) and writes the result into the attempt's staging directory — **no shared memory lock is held during this phase**, only the caller's run lock. `Pipeline.Finalize` (phase 2) runs `aggregate`→`prioritize`→select-High→`update` per target (each stage's own `reflect.file`, chained seed-to-seed in declaration order for stages sharing one file, plus the project-wide `memory.md` if `memory.mode` allows project writes) and performs the staged promotion — **the caller MUST already hold the memory lock before calling `Finalize`** (`Finalize` itself never acquires it; it assumes the directory is already guarded, `pkg/memorypipeline/rebuild.go`'s `FinalizeRequest` doc comment). `Pipeline.DistillTarget` (`distill.go`) is the reusable single-target chain both `Finalize` and (indirectly, via the live orchestrator's `distill` helper today reimplementing the same steps) rely on. This split exists so the expensive/parallelizable reflect step (phase 1, no lock needed — nothing shared is written yet) is never serialized behind the memory lock, while the actual read-modify-write of shared files (phase 2) always is.

- **Attempt-workspace + manifest + staged promotion — an offline transaction, not in-place rewrites.** Every rebuild invocation gets a fresh, never-reused attempt directory under `<runDir>/memory-rebuild/<attempt-id>/` (`NewUniqueAttemptDir`, `manifest.go`/CLI's `newAttemptID`), holding staged datasets, per-target distill artifacts (`patterns.md`/`prioritized.md`/`high.md`), and `manifest.json` (`Manifest`/`OperationMeta`, `manifest.go`) — the full provenance of the attempt (run id, flow path + SHA-256, prompt SHA-256 per kind, root/memory dir, effective commit decision) plus its outcome (per-dataset/per-target results, errors, commit result). The manifest's `Status` walks a fixed lifecycle (`running` → `capturing` → `distilling` → `promoting` → `awaiting_commit`/`published` → CLI-owned `completed`/`failed`, or `dry_run`/`cancelled`) — `nonTerminalStatuses` names the ones a crash/interrupt can leave stuck, and `FinalizeManifestOnExit` (a `defer` in the CLI handler) closes any such stuck manifest into `failed`/`cancelled` rather than leaving it looking permanently "running". Promotion itself (writing the real target files under the memory directory) only happens after ALL targets in the attempt have successfully distilled — a mid-attempt failure never leaves some targets updated and others not; `--dry-run` runs the full pipeline (so its printed diff is accurate) but skips the promotion write entirely.

- **The shared memory lock — mandatory for every writer, not just rebuild.** `pkg/memorypipeline.AcquireMemoryLock(ctx, memoryDir)` (`lock.go`) takes an interprocess `flock`-backed lock (`pkg/progress.Lock`) at a **fixed path derived from the memory directory's content**, not from the directory itself: `MemoryLockPath` computes `~/.afm/locks/memory-<sha256 of the canonicalized absolute memory dir path>.lock`. The path is deliberately OUTSIDE the memory directory (which may not exist yet on a first rebuild, and must not have lock-file noise become part of the content it protects). **Ancestor-symlink canonicalization:** `canonicalizeExistingAncestor` (`rebuild.go`) resolves symlinks only up to the deepest EXISTING ancestor of the (possibly not-yet-created) memory dir, then appends the remaining non-existent suffix unresolved — so two differently-spelled paths that alias the same real directory (e.g. one goes through a symlinked parent) hash to the SAME lock file, and a not-yet-created memory dir still gets a stable, deterministic lock path. **Run-then-memory lock order everywhere:** both the live orchestrator's end-of-run memory pass and `afm memory rebuild` take the run lock first (or, for rebuild, the read-only `state.RunLock`) and the memory lock second, for as short a window as possible — never the reverse — so the two paths can never deadlock against each other. `AcquireMemoryLock` polls every 100ms and blocks (not a hard fail) on contention (`progress.ErrLockBusy`), honoring `ctx` cancellation — waiting for another writer (live run or another rebuild) to finish is the normal, expected outcome, not an error.

- **The CLI owns the lock window explicitly: capture → acquire memory lock → re-preflight → finalize → commit.** `rebuildHandler` (`cmd/afm/memory_rebuild.go`) calls `pipe.CaptureAll` (phase 1) BEFORE acquiring the memory lock — deliberately, so the (potentially slow, one-agent-call-per-stage) reflect work never holds the lock. Only once datasets are captured does it call `memorypipeline.AcquireMemoryLock`, and — now that it actually holds the lock — it RE-RUNS the commit preflight check (`commitPreflight`, checked once fail-fast before capture to avoid wasted work, and again here because the git state could have changed while capture was running unlocked) before calling `pipe.Finalize` and, if commit is effectively enabled, `maybeCommit`. The lock is released via `defer` after the whole finalize+commit sequence — i.e. the window during which no other writer (live run or concurrent rebuild) can touch the memory directory spans exactly finalize+commit, not capture.

- **Strict (CLI) vs. best-effort (live orchestrator) is a deliberate asymmetry, not an oversight.** The live end-of-run memory pass (`(*Orchestrator).runEndOfRunMemory`, see above) is best-effort BY DESIGN: a stage's own success/failure must never depend on whether its memory got written, so any step failing there just surfaces a `reflect_failed` notice and moves on — nothing about the run's outcome (exit code, FSM) reflects a memory failure. `afm memory rebuild`, by contrast, IS the whole operation the user invoked — there is no run to protect from collateral damage, so it fails LOUDLY: a capture error, a finalize/distill error, a failed commit, or a manifest write error all propagate as a non-zero process exit with a descriptive message (and the manifest is left in `failed`, not silently swallowed). Don't "fix" a rebuild failure by making it best-effort like the live path — that would hide the exact class of problem (a bad historical run, a broken prompt, a git conflict) the command exists to surface to the user running it explicitly.
