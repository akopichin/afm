# Release Notes

Newest features at the top, older ones further down. Dates follow commits to `fix`/`master`.

## 2026-07-29

### Fix: event feed's sticky auto-scroll and "↓ latest" button lost their state on maximize/restore
- `Maximizable` (the component behind every panel's fullscreen toggle) used to switch between rendering `<>{children}</>` and `createPortal(...)` depending on maximize state — two structurally different element types at the same tree position, so React unmounted and remounted everything inside on every toggle. That silently reset the event feed's `useStickToBottom` state: scrolling up to read history, then maximizing/restoring the panel, snapped the feed back to the bottom and hid the "↓ latest" jump button — exactly backwards from what a user reviewing history would want.
- **First attempt was wrong, caught mid-implementation:** the original plan kept `createPortal` but only switched its `container` argument (an anchor div ↔ `document.body`), reasoning React would treat that as an in-place update. It doesn't — verified directly with a minimal repro that a portal's container is part of its identity for React's reconciler, so changing it between renders remounts the subtree just as much as switching Fragment↔Portal did.
- **Actual fix:** dropped portals from `Maximizable` entirely. A single stable `<div>` never leaves its position in the tree; only its `className` toggles between normal in-flow layout and a fullscreen `position: fixed` overlay (`maximize-overlay`, unchanged CSS). Since the div's type and position never change, React only ever diffs props — never unmounts. Safe in this codebase specifically because no ancestor between `Maximizable` and `document.body` sets `transform`/`filter`/`perspective`/`contain`/`will-change` or a `position` + non-`auto` `z-index` combination that would trap the fixed overlay (checked directly against `react-resizable-panels`' own styles and this project's CSS).
- Verified end-to-end in a real Chrome tab against a locally built Docker image (`local/afm:dev`, same `Dockerfile.runtime` the real release uses) driving a live `afm run`: scrolled the feed to the top, maximized the panel, restored it, and confirmed both the scroll position and the jump button survived the round-trip — the exact regression this fixes.
- Spec/plan: `docs/superpowers/specs/2026-07-29-dashboard-event-feed-ui-fixes-design.md`, `docs/superpowers/plans/2026-07-29-dashboard-event-feed-ui-fixes.md`.

### Fix: event feed timestamps ticked "N seconds ago" instead of showing how long each step took
- Each row used to recompute a live "N seconds/minutes ago" relative time every 5 seconds via a shared `setInterval` — noisy, and it couldn't answer "how long did that step actually take."
- **Fix:** each row now shows a static duration computed once at render — the gap between that event and the previous one in the feed (`5s`, `1m`, …), across all stages in display order. The first row (no predecessor) or an unparseable timestamp shows an em dash. No more ticking.
- Verified live: watched a specific row's printed duration stay frozen (e.g. `3m`) across 10+ real seconds, confirming the old live-ticking behavior is gone, while a fresh row correctly showed the real elapsed gap since the previous event.

### New: Idle and Backoff counters in the footer
- Previously there was no way to see, at a glance, how much of a run's wall-clock time was spent waiting on you vs. burned on automatic retry backoff.
- **New footer counters**, next to the existing `Elapsed`: **Idle** — time the flow was genuinely waiting on you; **Backoff** — cumulative time in automatic `retrying` backoff, no user involved (`useStatusDuration(events, BACKOFF_STATUSES)`, `pkg/web/dashboard/src/hooks/use-status-duration/` — a generic per-status-set duration accumulator, processing `stage_status_changed` incrementally so a closed episode survives the frontend's capped 200-event buffer).
- **Idle's real-world semantics, found and fixed same day:** the first version summed each stage's own time in `awaiting_user_input`/`awaiting_approval`/`failed` independently. In a real run this produced a wrong reading: retrying a stage (agent genuinely working) while *downstream* stages sat cascaded-`failed` (`blocked_by_dep`, waiting on the upstream stage — see the `runAutonomousAgent`/downstream-retry fix in the 2026-07-27 entry) kept Idle climbing the whole time, even though nothing was actually waiting on the user.
- **Fix:** Idle is now a single flow-wide condition, not a per-stage sum — `useIdleTime(events)` (`pkg/web/dashboard/src/hooks/use-idle-time/`): `idle(t) = a question is open on any stage (awaiting_user_input/awaiting_approval) OR (a stage is failed AND no agent is currently active — running/planning/revising)`. A pending question always counts, regardless of what else is running; a merely-`failed` stage stops counting the instant any agent becomes active elsewhere, and resumes the moment nothing is. (`retrying` counts as neither a question nor "active" — it's passive backoff, tracked separately.) Same incremental, truncation-surviving processing as `useStatusDuration`, now walking the whole stage timeline instead of one stage's own episodes.
- Verified live against a locally built Docker image, plus a dedicated regression test (`App.test.tsx`, "a cascaded-failed downstream stage does not count as Idle while another stage is actively running"): Idle stays at `0:00` while an upstream stage runs with a downstream stage failed, and only starts climbing once the upstream stage finishes and nothing is left active.

### Fix: "thinking" indicator kept showing while the dashboard was offline
- The badge next to a running stage's status only checked `selectedStage.status === 'running'` — it stayed on even when the WebSocket had dropped and the dashboard had no idea what the agent was actually doing.
- **Fix:** the condition now also requires `connected` (already computed by `useEventFeed` for the header's `LINK`/`OFFLINE` badge).
- Verified live: force-killed the dashboard's WebSocket while a stage was genuinely `running` (confirmed via a separate `/api/status` poll, independent of the browser) — the "thinking" badge disappeared immediately alongside the header flipping to `OFFLINE`, and reappeared the moment the connection came back, with the stage still `running` throughout — isolating the fix from a simple "stage finished" confound.

## 2026-07-28

### New: pulsing browser-tab favicon while a stage waits for you
- Previously the only signal that a stage needed your input while the dashboard tab was in the background was a flashing `document.title` (`useTitleFlash`) — easy to miss unless you were already scanning your browser's tab list.
- **New:** while at least one stage in the run is `awaiting_user_input`/`awaiting_approval` (the same global `anyAwaiting(stages)` signal already driving the header's attention dot) *and* the tab is backgrounded (`document.hidden`), the tab's favicon now pulses — an amber (`--amber`, so it matches whatever skin is active) badge circle blinks on/off over whatever the current favicon happens to be, every 700ms. Stops and restores immediately the moment the tab regains focus.
- **Mechanism:** `compositeAttentionBadge` (`pkg/web/dashboard/src/hooks/use-favicon-pulse/composite-attention-badge.ts`) draws the badge onto a 32×32 canvas over the current `<link rel="icon">`'s image and returns a `toDataURL()` data URL, cached per href. `useFaviconPulse` (same directory) mirrors `useTitleFlash`'s timer/visibility-listener shape, toggling the `<link>`'s `href` between the original and the composited badge. No per-skin assets needed — it composites over whatever icon is currently active.
- Verified end-to-end in a real Chrome tab (not just jsdom, which can't run canvas/`Image` here) via `chrome-devtools` MCP tooling: the badge renders correctly, the href genuinely toggles on the 700ms cadence while hidden, restores and stays restored once visible again, and repaints in a different color when `--amber` is changed live — confirming the color really comes from the active skin's CSS variable, not a hardcoded value.
- A final-review pass caught and fixed a real race: the pulse interval used to start asynchronously (inside the badge-compositing promise's `.then`), so a fast hidden→visible→hidden bounce during the first-ever activation (real image load in flight) could leave an orphaned interval running — pulsing the favicon even after the tab regained focus. Fixed with an idempotency guard (only one interval can ever be live) plus a regression test that reproduces the exact race.
- Spec/plan: `docs/superpowers/specs/2026-07-28-favicon-attention-pulse-design.md`, `docs/superpowers/plans/2026-07-28-favicon-attention-pulse.md`.

### Fix: agent-written `question.json` no longer silently dropped on invalid JSON
- `FindUnansweredQuestions` used to try one narrow repair heuristic (an unescaped `"` inside a string value) and, on any other kind of malformed JSON, silently `continue` — the question file vanished from the poller's view, the stage never reached `awaiting_user_input`, and the agent was left polling for an answer that could never arrive. Root-caused from a real incident: an agent dropped the opening quote before the `options` key (`...,options":[...]` instead of `...,"options":[...]`), and the stage hung in `running` for 12+ minutes with zero trace anywhere in the UI or logs.
- **Fix:** replaced the narrow heuristic with [`github.com/kaptinlin/jsonrepair`](https://github.com/kaptinlin/jsonrepair), which covers a much wider class of agent JSON mistakes (missing key quotes, missing commas, stray unescaped quotes, etc.) — verified against the actual incident's payload. If even that can't recover the file, the question now surfaces as a fallback stub (`Continue anyway` / `Cancel stage` options, raw file contents shown for inspection) instead of vanishing — the stage always reaches `awaiting_user_input`, never hangs silently again.
- `go.mod`: `go 1.26` → `go 1.26.4` (the new dependency's minimum; toolchain fetched automatically via `GOTOOLCHAIN=auto`, no manual Go upgrade needed).

## 2026-07-27

### Fix: kebab menu clipped by the scrollable stages panel
- The new `agent_suggest` kebab (⋮) dropdown was positioned `absolute` relative to its row, nested inside `#stages-panel`, which scrolls (`overflow-y: auto`) — a stage row near the bottom of the visible list opened a dropdown that got visibly cut off by the panel's edge (both axes: setting only `overflow-y` makes `overflow-x` compute to `auto` too, per the CSS overflow spec).
- **Fix:** the dropdown now renders through a `createPortal` into `document.body` (the same technique `Maximizable` already uses for full-screen panels), positioned from the button's `getBoundingClientRect()` — immune to any ancestor's scroll/clip. Closes on an outside click or on scroll (a fixed-position portal doesn't track its anchor while scrolling, so closing rather than repositioning keeps a one-item menu simple).

### Fix: dashboard occasionally failed to auto-advance to the next active stage
- `useStatus`'s polling (every 3s) and its WS-triggered `refresh()` both fire independent `fetch('/api/status')` calls — under a burst of near-simultaneous transitions (e.g. a planning-only stage completing and unblocking its dependent in the same instant), multiple requests can be in flight together, and their responses aren't guaranteed to resolve in the order they were sent. If an older request resolved *after* a newer one, it silently rolled the displayed state back to a stale snapshot — and since the sidebar's auto-advance-on-`done` is a one-shot edge trigger, a stale snapshot missing the newly-active downstream stage meant the UI was stuck showing the finished stage until the user clicked away manually.
- **Fix:** `useStatus.load()` now tags each request with a monotonic generation counter and discards any response that isn't the most recently *issued* request by the time it resolves — the freshest request always wins, regardless of network arrival order.

### New (experimental): `agent_suggest` — send a note to a running stage
- Previously `Revise` only worked at the `awaiting_approval` checkpoint. With the new `experimental.agent_suggest` config flag (or env `AFM_EXP_AGENT_SUGGEST=1`), a stage can also be redirected while it's actively `running`, via the dashboard's new kebab (⋮) menu or `POST /api/stages/{id}/revise`.
- **Mechanism:** the FSM's `EvRevise` now also accepts `running` (previously only `awaiting_approval`). `Revise` durably transitions the stage to `revising`, saves the note to `feedback.md`, and signals a fresh per-attempt interrupt channel (`Orchestrator.interruptChans`). The executor forwards a real `SIGINT` to the agent subprocess (`executor.Config.InterruptCh`, sentinel `executor.ErrUserInterrupted`) instead of the abrupt SIGKILL a context-cancel would give — the agent finishes its current step, then exits cleanly. `runWithRetry` detects the sentinel and restarts the same phase through one of three new `run<Phase>WithFeedback` functions (implementation/review/autonomous — planning already had one), folding the note into the phase's retry context.
- **Recovery, and a live-run race, closed:** crash-recovery (`recovery.go`) now detects which phase was actually interrupted (by `*.session.json` mtime, extended to the autonomous track) instead of always assuming planning. A subtler race — the agent completing naturally at almost the same instant a note was sent — used to strand the stage in `revising` forever until the next process restart; `onAgentCompleted` now reconciles that case the same way crash-recovery already did.
- Off by default. With the flag off, the dashboard doesn't show the kebab menu and the HTTP endpoint rejects `running` with 400 — behavior for `awaiting_approval` is unchanged either way. `/api/status` gained `agent_suggest_enabled` to drive the frontend gate.
- Verified live end-to-end with a real `afm` binary and a real subprocess (not just `go test`): a real `SIGINT` was delivered (not SIGKILL), `feedback.md` was persisted with the exact note, the restarted phase's agent actually saw the note, and a concurrent double-click on "send" was rejected cleanly (second call gets 400, no double-signal, no corrupted feedback file).
- Spec/plan: `docs/superpowers/specs/2026-07-27-agent-suggest-design.md`, `docs/superpowers/plans/2026-07-27-agent-suggest.md`.

### Fix: a stage further down a failed dependency chain couldn't be retried
- `runAutonomousAgent` never created its own stage directory or wrote `autonomous.flag` (unlike `runPlanningAgent`/`runReviewAgent`, which both do). When a stage failed and cascaded a `blocked_by_dep` failure to stages further down the chain, retrying those downstream stages hit `open .../plan.md: no such file or directory` for any that were on the autonomous track — the directory/flag it needed simply didn't exist yet.
- **Fix:** `runAutonomousAgent` now creates its `stageDir` and writes `autonomous.flag` up front, mirroring the other phase runners.

### Fix: misplaced `question.json` no longer hangs a stage forever
- `relocateMisplacedQuestions` used to find a misplaced dialog question by parsing `Write` tool-use events out of the agent's stream-json log — so a question wasn't found (and the stage hung forever in what looked like `running`) whenever the agent created the file via Bash (`echo`/heredoc) instead of the `Write` tool, or a wrapper's console-diagnostics happened to interleave badly with the parser.
- **Fix:** the poller now scans the filesystem directly (stage dir + the flow's `root_dir`, non-recursive, throttled) for both known ways an agent can hide a question file from it: writing outside `$AFM_STAGE_DIR` entirely, or naming it by the stage's own ID instead of the canonical phase prefix (`planning`/`implementation`/`review`/`autonomous_execution`). Either way, the file is normalized to the canonical name plus a dangling symlink at the path the agent is actually polling, so the bash wait-loop finds its answer. A follow-up fix (`ownAnswer` check) stops the poller from reopening a question from a *previous* phase that was already answered.

### Fix: dashboard event feed no longer empties on page refresh
- The event feed lived only in the browser's WebSocket session state — reloading the dashboard (or a flaky connection) lost all history, even though the run was still going.
- **Fix:** new `GET /api/events` replays history from `events.jsonl` (+ a new `notices.jsonl` sidecar for `agent_completed`/`context_warning`, which — like `supervisor.jsonl` — doesn't touch the durable `events.jsonl` source of truth) and the frontend now fetches it on mount, merging with whatever the live WebSocket already delivered (capped at 200 entries, deduplicated).
- **Live events now carry a real sequence number.** `Store.Apply`/`FSM.Apply` thread the transition's actual log-assigned `seq` all the way to the WebSocket-published event (previously only the REST replay had it), so the frontend's merge/dedup keys on the real `seq` when present — a firmer join than the earlier content-based fallback (still used for event types with no underlying transition, like `agent_action`).

### Fix: Docker mode no longer orphans the container on Ctrl-C
- When `afm` runs in Docker mode and the host process received `SIGINT`/`SIGTERM`, the signal wasn't forwarded into the container — the host process could exit while the container (and the agents inside it) kept running, orphaned.
- **Fix:** the Docker launcher now installs a signal handler before starting the container process and forwards `SIGINT`/`SIGTERM` to it, then waits for it to actually exit before returning.

## 2026-07-24

### Dashboard micro-animations (subtle, informative, cross-theme)
- Added a layer of small, one-shot-on-event animations that carry information (what changed / where to look / that work is progressing), not decoration. All live in the shared `base/*.css` via design tokens, so every theme (coffee/goga/novacorps) inherits them; all respect `prefers-reduced-motion` (motion off, functionality intact).
- **Flow clarity:** a stage's status dot animates when it finishes (fill + check + flash) with a pulse "travelling" down the connector to the next stage; new event-feed rows fade in; the newly-active stage fades in. (The progress bar's smooth fill + shimmer already existed.)
- **Feedback on your actions:** Approve/Send/Retry buttons show a ripple + "✓" success morph; a plan/dialog line's comment marker pops and its amber border slides in; the dialog frame glows once when the agent asks a new question.
- **Atmosphere:** the palette cross-fades when you toggle light/dark; a compact "thinking" indicator appears while the selected stage is running; the connection dot pulses while the WebSocket is reconnecting.

### New default dashboard theme: `coffee` (warm coffee / valve-glow)
- The dashboard's default skin is now **`coffee`** — a warm coffee palette with a "valve-glow" amber dark mode (glowing filament accents on active stages, progress, and running status) and a cream "latte" light mode; the user-dialog accent is a matcha green. Light/dark is toggled inside the dashboard, independent of the theme.
- All three built-in themes are selectable via `theme:` in `.afm/config.yaml`: `coffee` (default), `goga`, `novacorps`. Empty/unknown falls back to `coffee`. The previous default `novacorps` is still available with `theme: novacorps`.
- Implementation notes: a skin is a single token file (`skins/coffee/index.css`) that imports the shared `base/*.css` and supplies the palette plus a recolor of base's hardcoded accent fills. The server's skin selection (`pkg/server`, `pkg/config` `EffectiveTheme`) was updated so the baked-in default and the `theme:`-driven rewrite agree on `coffee`, and so `goga`/`novacorps`/custom `skin_dir` all keep working.

### Fix: dialog question and review plan no longer mangle code blocks / tables
- The pending dialog question and the review-mode plan render markdown **line by line** (so each line is clickable for comments). That path only handled inline markdown, so multi-line blocks broke: the dialog question had **no** fenced-code handling at all — an agent's ` ```diff `/YAML contract came out as stray literal `` ```diff ``/`-old`/`` ``` `` paragraphs, so it looked "cut" and no contract was readable; markdown tables (in both the plan and the question) rendered each `| … |` row as a separate paragraph with the `|---|` separator shown literally ("мешанина"). This regressed when per-line commenting replaced the old full-markdown rendering of the question.
- **Fix:** a shared block parser (`parseLineBlocks`/`nextLineBlock` in `plan-panel/markdown.ts`) now collapses fenced code **and** GFM tables into a single block (full `md.render`), anchored to their first source line — click-to-comment is preserved, and code/tables render intact in both the dialog question and the review plan. Added `.line-content table` styling.

### Fix: event feed no longer floods with duplicate status rows
- Consecutive identical stage-status changes (e.g. a stream of "TASK-REVIEW → ready") are now collapsed in the feed. The backend records each transition once; reconnect/debounce repeats no longer pile up as separate rows with drifting timestamps.

### Fix: a completed stage stays selected when you click it
- The auto-advance logic fired on every selection change and yanked you off any `done` stage, so clicking an earlier finished stage during a run bounced you straight back to the active one — you couldn't inspect its log/plan/dialog. It now advances only when the **currently selected** stage itself transitions to `done`; manually opening a finished stage sticks.

### Fix: finished flow no longer shows the last stage as "in progress"
- The animated amber selection indicator in the sidebar (pulsing bar + running scanline) played for any selected stage regardless of status, so after a flow finished its last (selected) stage looked perpetually in progress. The selection indicator is now static for `done`/`failed` stages (selection is still visible, just without the activity animation).

## 2026-07-23

### Dashboard: remove a line comment with an ✕
- A comment left on a plan line (or a dialog question line) now has an ✕ button in its header — click it to remove the comment in one click, without reopening the edit form. Previously the only way to drop a comment was: click the line → open the form → "Delete".
- In the dialog, removing the last remaining comment brings back the normal answer UI (option buttons + ▸ SEND) instead of "Send feedback" — the switch is driven by the comment count.

### Fix: retry context no longer clipped by `truncate_output`
- When a stage was retried (after a rate-limit / server error), the "previously completed actions" block in the retry prompt was built from the human-readable `<phase>.log`, whose per-action detail is truncated by `executor.truncate_output`. With a small `truncate_output`, a retried agent saw an abbreviated view of its own prior work.
- **Fix:** retry context is now built from the raw, untruncated `<phase>.jsonl` stream. `truncate_output` still applies to the log and dashboard event feed as designed — only the retry continuation prompt sees the full detail.

### New: explicit warning when a dependency's plan is missing
- `CollectDependencyPlans` used to silently substitute `(plan not available)` into a stage's prompt when a dependency's `plan.md` / `execution_summary.md` was missing or empty — the operator had no way to notice the downstream stage was running with degraded context.
- A `context_warning` event is now published to the dashboard event feed (distinct amber styling), naming the dependency whose plan was missing. The stage still runs; the loss of context is just no longer invisible.

### New config: `executor.truncate_output` (default: no truncation)
- Agent tool-action output (text blocks, Bash commands) logged to `<phase>.log` and the `agent_action` event feed was previously always truncated at hardcoded lengths (100 chars for text, 80 for Bash/other tool details) — permanently, not just a display convenience (the full-screen dashboard view and the API don't recover it; only the raw `<phase>.jsonl` stream kept the untruncated original).
- New `executor.truncate_output` config (default `0` = no truncation; set to `N` to cap logged text/Bash-command detail at `N` chars, matching the old hardcoded behavior when set to 100 or 80).

### Fix: empty stage badge in dashboard event feed
- For a stage without its own `command` (uses the default client), `agent_action` events (Bash/Read/Skill/text) went out with an empty `stageId` — the dashboard didn't render the stage badge, even though status-change rows had one. Cause: `runnerFor` returned a shared runner whose `OnAction` was bound to an empty stageID.
- **Fix:** each stage now gets a per-stage runner with the correct stageID (the injected runner stays test-only). This also fixes attribution for parallel stages.

### New flow field `root_dir` — project root for agents
- `flow.yaml` can now set `root_dir` — the working directory (CWD) agents run in. Needed when the `afm` process's CWD doesn't match the project root (e.g. Docker setup: sources in `/workspace`, `.afm/` elsewhere). Without it, relative project paths (`docs/arch/…`) resolved against different roots for different stages → one stage writes a file, another can't find it.
- A relative `root_dir` resolves from the afm root (`--dir`); empty keeps prior behavior. `AFM_STAGE_DIR` (dialog files) stays tied to the afm root regardless. See README, "The flow.yaml file" section.

## 2026-07-22

### GitHub CI + automatic release on push to main
- Tag `v0.5.6` synced with upstream; further development happens in this (public GitHub) repo.
- `.github/workflows/ci.yml`: `validate` job (build+test+lint) on every push/PR; on push to `main`, the `auto-release-tag` job automatically bumps the patch version and pushes a tag using `RELEASE_TOKEN` (not the default `GITHUB_TOKEN` — otherwise the tag wouldn't trigger another workflow, a GitHub safeguard against loops).
- `.github/workflows/release.yml`: triggered by any `v*.*.*` tag (auto or manual `make release-minor/major`) — builds a multi-arch (`linux/amd64`+`linux/arm64`, via `docker/setup-qemu-action`) docker image to Docker Hub, plus `goreleaser`: cross-platform binaries, GitHub Release, Homebrew cask.
- `scripts/release.sh` simplified — no longer builds docker itself, only bumps the version and pushes the tag; all building now lives in `release.yml` (a single release entry point regardless of tag source).
- New: `brew install --cask akopichin/afm` (tap `akopichin/homebrew-afm`, `.goreleaser.yml` → `homebrew_casks:`). The post-install hook strips `com.apple.quarantine` and ad-hoc signs the binary (`codesign -f -s -`) — without both steps macOS kills the downloaded binary (`SIGKILL`); codesign alone isn't enough.

## 2026-07-21

### Hard autonomous track: `agents: [auto]`
- A stage's YAML can now statically declare the autonomous track — `agents: [auto]`. The stage runs directly via the autonomous agent, **skipping the supervisor's LLM decision and the fallback** to normal phases (no `plan.md`, no approval, dialog still available, writes `execution_summary.md`). For cases where the supervisor doesn't always classify autonomy correctly.
- Validation: `auto` must be the stage's only agent; `auto` + `supervisor: true` → parse error (conflicting intents). See `docs/superpowers/specs/2026-07-21-auto-phase-design.md`.

### Dashboard: comments on questions (like on plans)
- The dialog channel now supports clicking question lines and leaving per-line comments — same as the plan panel. Once at least one comment exists, the options and free-text answer field are hidden and a single **"Send feedback"** button appears: clicking it sends the comments (line quote + text) to the agent as the answer.
- **Ctrl/Cmd+Enter** in the dialog answer field submits the answer.
- **Dark theme by default** (previously taken from the system; the manual toggle is kept).
- **Full-screen logs** — an expand button like dialog/plan have; lines are truncated less, and not truncated at all in expanded view.
- **Project description in the header** — `/api/status` returns `description` from the flow root; useful for telling multiple running pipelines apart.
- The "↓ to latest" button is now labeled "↓ latest"; the dialog window scrolls to the end when expanded.

### Reliability: `events.jsonl` integrity
- **A truncated last record missing the trailing `\n`** (newline lost on crash) is now safely truncated instead of extending the log with a null byte and then quarantining it.
- **Corruption in the middle of the log** is now handled consistently on the `afm run` and `afm check`/run-lookup paths (single parser): the read path no longer silently falls back to a stale prefix — it signals `ErrCorruptLog`; `FindLatestRunForStage` no longer silently falls back to an older run.

### Internal: orchestrator refactor + test harness
- `pkg/orchestrator/orchestrator.go` was split from a god-file (1625→~400 lines) into focused files (concurrency/dialog_poller/agents/scheduling/control_api/supervisor_track/runner_factory) — a pure move, no behavior change. The phase domain vocabulary moved into `pkg/flow` (single source of truth). Deduplicated the autonomous-track helper (`startWithSupervisor`) and phase lists (`dialogPhases`).
- Added a scenario-driven integration test harness with a "synthetic" model (`pkg/orchestrator/scenario_*_test.go`): declarative happy-path and failure scenarios (mis-prefixed/misplaced question, stuck dialog, corrupt log, wrong supervisor track) to catch regressions across versions. Also closed coverage gaps (`afm check`/`list`, ErrRunLocked CLI, resume-from-revising, MaxParallel).

## 2026-07-20

### Fix: `startReadyStages` could launch the implementation agent for an autonomous stage
- Retrying a failed autonomous stage (`retryStage`) transitions it `Pending → Ready` via `EvReady`, then itself takes `EvStartRun`. In the narrow window between these two transitions, a concurrent call to `startReadyStages` from another event-loop branch (e.g. `onAgentCompleted` for a sibling stage) could win the `EvStartRun` CAS first — and `startReadyStages` blindly launched `runImplementationAgent` for any Ready stage, without checking `autonomous.flag`. `runImplementationAgent` reads `plan.md`, which an autonomous stage doesn't have → the stage failed with `open .../plan.md: no such file or directory` instead of re-running autonomously.
- **Fix:** `startReadyStages` now checks `isAutonomousStage` before spawning and launches `runAutonomousAgent` for such stages — symmetric with the existing checks in `recovery.go` and in `retryStage`.
- The regression reproduced reliably (5/5) in `TestIntegration_RetryFailedAutonomousStaysAutonomous`; after the fix the test is green (5/5 in a row, `-race`).

### goga-accept: CODEMANIFEST sync (second pass)
- `assets.ReadPrompt`: the manifest didn't reflect the third return value `fromOverride bool` (prompt source, for logging).
- `pkg/docker.ScanCommands`: the manifest didn't reflect the third parameter `generated map[string]bool` (commands covered by the autoShim wrapper).
- `pkg/orchestrator.Orchestrator.Retry`: the annotation didn't describe the retry branch for autonomous stages or the session/jsonl cleanup for interactive ones.
- `cmd/afm`: the manifest documented a nonexistent local helper `findLatestRunDir(stageID)` — approve/retry/revise actually call `state.FindLatestRunForStage` (added to `pkg/state/CODEMANIFEST`).
- `pkg/web/dashboard/src/app`: `App()` didn't document the `status === 'failed'` exception for hiding `showPlan` on an autonomous stage — the Retry button in PlanPanel must stay available for a failed autonomous stage too.

### UI: theme toggle moved from footer to header
- The dark/light button (`useThemeMode`, ☾/☀ icon) moved from `Footer.tsx` to `FlowHeader.tsx` — now in the header's top-right corner, next to the WS connection indicator, instead of the footer at the bottom. Behavior and appearance unchanged (same `.icon-btn`, same `aria-label`, same `localStorage` persistence), only the render location changed.
- `skins/base/header.css` (source `public/skins/base/header.css`, copied to root by `vite build`): added a column to `#header { grid-template-columns }` and spacing `#header .icon-btn { margin-left: 4px }` for the new button.
- `Footer.test.tsx`/`FlowHeader.test.tsx`: the theme-toggle test moved along with the button.
- CODEMANIFEST `footer`/`flow-header` synced with the code (`goga lint`/`goga contract` confirm no drift).

### Data race on package-level globals (websocket keepalive + retry)
- **`pkg/server` (websocket):** `wsPongWait`/`wsPingPeriod`/`wsWriteWait` were package-level `var`s that tests mutated between connections. Server goroutines (`PongHandler` in `readPump`, the ticker in `writePump`) read them on every iteration → data race against test writes (goroutines from a previous connection were still alive). **Fix:** snapshot the values into locals at the start of `readPump`/`writePump` (keepalive is fixed for the connection's lifetime — no need to re-read globals). `TestWebSocket_ClosesSilentClient` is now green under `-race`.
- **`pkg/orchestrator` (retry):** `RetryBackoff`/`MaxRetries` had the same problem. Agent goroutines (`runWithRetry`) outlive `Run()`'s return (it doesn't wait for them), and tests restored the globals in `t.Cleanup` → a race in `TestIntegration_RetryExhausted` under `-race`. **Fix:** `RetryBackoff`/`MaxRetries` are snapshotted per-instance in `New()`/`NewSupervisor()` (`Orchestrator.maxRetries`/`retryBackoff`, `Supervisor.maxRetries`/`retryBackoff`); `runWithRetry` and `EvaluateStage` read the instance's immutable fields instead of the globals. The package-level `RetryBackoff`/`MaxRetries` remain as defaults, including for tests.

### Lint: goconst + identical-switch-branches
- `supervisor.go`: the literal `"autonomous_execution"` replaced with the constant `phaseAutonomous` (goconst).
- `orchestrator.go`: identical `case phaseImplementation` and `case phaseAutonomous` branches (both did running→done) merged (revive `identical-switch-branches`).
- `golangci-lint run ./...` = 0 issues; `setstatuslinter` clean.

### Test: stale `TestIntegration_DialogViolationDetected` → relocate
- The test checked **old** behavior (fail-fast via `detectDialogViolation` when `question.json` was written outside stageDir), replaced by auto-relocate in commit `2a759dd`. Because of that, the mock (emitting a Write event with no real file) made the stage hang → 15s timeout. **Fix:** rewritten as `TestIntegration_MisplacedQuestionRelocated` — the mock creates the file in the wrong directory, and the test checks that the poller relocates it into stageDir and the stage moves to `awaiting_user_input`. `CLAUDE.md` docs updated to match the current relocate behavior.

### goga-accept: CODEMANIFEST sync
- `pkg/state/CODEMANIFEST`: `SetApplyHook(h TransitionCallback)` → `SetApplyHook(h)` — the `TransitionCallback` type doesn't exist in the code (a dangling reference; the real signature is `func SetApplyHook(h func(Transition))`); aligned with the sibling convention `SetExecFunc(f)`.

## 2026-07-16

### Supervisor agent — auto-assessing stage autonomy
- A stage with `supervisor: true` is assessed at start by an LLM supervisor: can it run **autonomously** (a single step through the attached skill — writes `execution_summary.md`, skipping planning/approval/review) or does it need the standard multi-phase cycle. The decision is written to `<runDir>/supervisor.jsonl` (`events.jsonl` is untouched).
- **Fallback safety:** any LLM-call error (timeout, bad JSON) → safe fallback to base phases. 529/502/503/504 are survived via retry (see below). The supervisor can **only shorten** phases, never extend them. Inline artifacts → always the standard track.
- **Config:** `flow.supervisor_command` (e.g. `glm47`) > `config.supervisor.command` > `config.client.command`; `stage.supervisor: true` + optional `stage.supervisor_prompt`. Prompt template: `assets/prompts/autonomous.md`.
- **FSM:** new transition `EvSupervisorApproved` (planning→ready), bus event `EventSupervisorDecision`. Resuming autonomous stages (`autonomous.flag` + `execution_summary.md`) handled in `recovery.go`. `CollectDependencyPlans` reads `execution_summary.md` instead of `plan.md` for autonomous dependencies.
- **Robust parsing:** `Supervisor.parseDecision` extracts the decision JSON from: raw JSON / claude envelope `{"type":"result","result":"…"}` / claude event array `[…]` / ```json fences. Covers both `claude --output-format json` shapes (container — single envelope, host — array).
- Spec/plan: `docs/superpowers/specs/2026-07-16-supervisor-agent-design.md`, `docs/superpowers/plans/2026-07-16-supervisor-agent.md`.

### Fix: supervisor LLM call + claude wrappers for `--output-format json`
- **RunJSONQuery** (`pkg/executor`) inherited `e.cfg.ExtraArgs`, which `executor.New` defaults to `DefaultClaudeArgs` (`--print --output-format stream-json --verbose --dangerously-skip-permissions`). That `stream-json` conflicted with the supervisor's `--output-format json` → claude exited 1. **Fix:** a clean invocation `-p <prompt> --output-format json` without ExtraArgs, plus stderr captured into the error (diagnostics).
- **docker wrapper** (`pkg/docker/wrapper.go`): `--include-partial-messages` is now added **only when `output-format=stream-json`** (previously always — which gave `requires --output-format=stream-json` for the json call). The same fix applied to host wrappers `glm47/51/52` (ai-free): partial only with stream-json, not with `-p`.
- Live validation: `afm run` in docker mode with `feature.yaml` (`supervisor: true`, `supervisor_command: glm47`) — `supervisor.jsonl` now contains a real decision (`decision=standard`, reasoning); before the fix it silently fell back on every stage.

### Supervisor: UI visibility + retries + autonomous dialog
- **Supervisor now retries transient errors** (529/502/503/504) with the same `RetryBackoff`×`MaxRetries` as stage agents (`EvaluateStage`), instead of falling back immediately. On non-retryable errors it still fails straight to fallback.
- **`validateDecision` is strict only for autonomous** (`== ["autonomous_execution"]`); for the standard phase it's advisory (`DetermineStagePhases` returns base phases anyway). Removed a false fallback that triggered when the LLM wrote base phases as a single string `"planning implementation"` (an artifact of Go-slice rendering) — a valid decision no longer gets hidden from the log/UI. `BasePhases` now renders as JSON `["planning","implementation"]`.
- **Supervisor decision is now published to the UI for both tracks** (`EventSupervisorDecision` + `supervisor.jsonl`); previously the standard track wasn't published — the UI never saw the resolution. In the dashboard, `supervisor_decision` gets its own highlight (`.feed-entry.supervisor`) in the Event feed.
- **Autonomous-agent logs now visible in the middle "Log" panel**: `handleLog` (`/api/stages/<id>/log`) and `buildDialogEntries` now read `autonomous.log`/`autonomous.jsonl` (previously only planning/implementation/review → the panel was empty for autonomous stages).
- **The autonomous phase supports dialog**: `phaseAutonomous = "autonomous_execution"` — a single phase with no planning/impl/review (the skill does everything itself, writes `execution_summary.md`), BUT the skill can still ask the user via the same file-based dialog protocol (questions `autonomous_execution.q<N>.*`, a valid phase, resume via `onUserAnswered`). The runner gets `AFM_STAGE_DIR`, the prompt includes `<interactive_rules>`.
- **Persistent supervisor-decision badge** in the stage header in the dashboard (the decision stays visible even after the event scrolls out of the feed).
- **Fix for host-mode `supervisor_command`**: the wrapper is now generated even if no stage uses the command as an agent; the secret is resolved in the host branch too (`UsedRecipes`).

## 2026-07-15

### Retry on 529/502/503/504 + removal of proxy and accounting
- `orchestrator.Classify` now classifies `API Error: 529/502/503/504` (raw text from glm wrappers) as `ClassRetryable` (previously `ClassFatal` → the stage failed). 500 stays fatal.
- The built-in reverse proxy (`pkg/proxy`) is fully removed: the ZAI transform is redundant after adding retry, and routing isn't needed (autoShim wrappers bake in the direct upstream URL). Removed the associated threading infra in `run.go`/`orchestrator`/`executor`/`docker`.
- Token accounting (`pkg/accounting`) is fully removed: it lost its data source without the proxy. Removed `/api/usage`, the dashboard `ConsumptionPanel`, and config `proxy`/`pricing`/`accounting`.
- **Backward compat:** `yaml.Unmarshal` is lenient → old configs with `proxy`/`pricing`/`accounting` still parse (the sections are silently ignored). `autoShim:false` is neutral (glm wrappers already went direct). Usage accounting is deferred.

### claude wrappers: bounded retry + stream-json + `--bare` (config `claude_bare`)
- **Bounded retry loop** (`pkg/orchestrator/retry.go`): fixed `RetryBackoff=5s` × `MaxRetries=15` (as in ralphex), replacing the previous exponential `[5s,10s,30s]` (4 attempts). z.ai 529 is transient; this survives the overload window. Confirmed: claude sends `stream:true` itself (via `--output-format stream-json`), so force-streaming isn't needed.
- **`--output-format stream-json` + `--include-partial-messages`** added to generated claude wrappers (`pkg/docker/wrapper.go`): covers non-interactive stages (which the executor doesn't pass ExtraArgs to) and gives partial deltas. `--output-format` is deduplicated (interactive stages already get it via the executor).
- **`--bare` + config `client.claude_bare`**: `--bare` = claude Code minimal mode (skips CLAUDE.md/hooks/skills/memory), body ~4 KB instead of ~127 KB (lower load on z.ai). **BUT `--bare` breaks the Skill tool** — goga-* skills stop resolving (the agent has to imitate them itself). So **default is `claude_bare: false`** (skills matter more). `claude_bare: true` is for flows WITHOUT skills.

### `type: cursor` — Cursor Cloud Agents API
- The Cursor Cloud API (`api.cursor.com`) **has no** synchronous OpenAI `/v1/chat/completions` (returns 404) — it's a **Cloud Agents API**: an async, run-based API where "chat" means starting a cloud code agent. So `type: openai` (which hits `${BASE_URL}/chat/completions`) **never worked and cannot work** with Cursor. The historical note about "Cursor via `api2.cursor.sh`" under `type: openai` was wrong — removed.
- New recipe type `type: cursor` → a wrapper with `CURSOR_*` env (`CURSOR_API_KEY`/`CURSOR_BASE_URL`/`CURSOR_MODEL`) and `exec /usr/local/bin/cursor-as-claude`. The adapter: reads the prompt from stdin → `POST /v1/agents` (no-repo, `mode:"agent"`) → polls `GET /agents/{id}/runs/{runId}` until a terminal status (`FINISHED`/`ERROR`/`CANCELLED`/`EXPIRED`) → emits claude stream-json (an `assistant` envelope with the `result` text + a `result` event) → archives the agent (best-effort, to avoid leaving clutter). `model: auto` (or empty) → the `model` field is omitted, Cursor uses its default.
- `auth.to` for cursor is any `env:VAR` (conventionally `CURSOR_API_KEY`); `url` is required (`https://api.cursor.com/v1`). Doesn't require `claude` in PATH (unlike openai). Requires `jq`+`curl` in the image. Tests: `TestAgentRecipe_CursorType`, `TestCreateWrappers_CursorTemplate`/`_CursorNoClaudeRequired`.
- **Note:** the first response takes ~30–90s (cloud-VM startup when the agent is created); the run itself is fast afterward (`durationMs` in seconds). Tolerable for interactive dialog, but not instant.

## 2026-07-14

### Docker `autoShim` — generated wrappers without mounting
- With the flag `docker.autoShim: true` afm **generates claude-compatible wrappers** for recipe agents (`docker.agents.<cmd>`) directly in the container — without `-v` mounting the host binary and without `extra_mounts` for tokens. The real wrappers (`glm47`/`glm51`/`glm52`/`deepseek-v4`) are "model+url+auth+sysprompt → `exec claude`", so they're described as a recipe and regenerated.
- **Recipe:** `model` (required → `ANTHROPIC_DEFAULT_{HAIKU,SONNET,OPUS}_MODEL`, one for all 3 tiers), `url` (gateway), `system_prompt` (`file:<path>` → `--append-system-prompt-file`), `auth.from` (`env:VAR` | `file:<path>` — where afm reads the secret on the host) + `auth.to` (`env:<VAR>` ∈ {`CLAUDE_CODE_OAUTH_TOKEN`,`ANTHROPIC_API_KEY`,`ANTHROPIC_AUTH_TOKEN`}).
- **Data flow:** the host reads the secret and sysprompt content from host-only files → transient env `AFM_SECRET_<CMD>`/`AFM_SYSPROMPT_<CMD>` (bare-form `-e`, value doesn't end up in the `docker run` argv); the container gets `url`/`model`/`auth.to` from the mounted `config.yaml`. The wrapper bakes in `ANTHROPIC_BASE_URL` (by host-match against `proxy.upstream` — z.ai routes through the proxy for 529-protection, deepseek goes direct), substitutes the secret from the transient env, `unset`s it, and `exec`s the absolute `claude`.
- **Unified wrapper-dir** (`docker.CreateWrappers`) = claude proxy-shim + generated wrappers; `proxy.CreateShim` removed. `orchestrator.proxyForCmd` is now generated-aware (`generated` → self-route via the baked `BASE_URL`, wrapper-dir on PATH). `docker.ScanCommands` skips generated (not mounted); `docker.UsedRecipes` — secrets are resolved only for recipes actually used in the flow (no false fail-fast / no leaking secrets of unused agents). Missing secret → fail-fast with the agent's name. `afm-init` adds `.afm/secrets.env` to `.gitignore`.
- **Bonus:** a recipe can describe a docker-only agent whose binary doesn't exist on the host (e.g. `deepseek-v4`) — `autoShim` generates it in the container.
- Spec: `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

### `type: openai` — OpenAI-compatible providers
- A recipe with `type: openai` → a wrapper with `OPENAI_*` env (`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL`) and `exec /usr/local/bin/openai-as-claude` — a bash translator: reads the prompt from stdin, calls `${OPENAI_BASE_URL}/chat/completions` (stream=true), translates SSE into claude stream-json. Supports Cursor (`api2.cursor.sh`), DeepSeek, local LLMs, and any OpenAI-compatible endpoints.
- `auth.to` for openai is any `env:VAR` (NOT limited to ClaudeAuthEnvVars); `url` is required. Requires `jq`+`curl` in the image (added to `Dockerfile.runtime`).
- Backward compat: empty `type` (or `"claude"`) = previous claude behavior; unknown `type` value → validation error.

### Fix: generated wrapper wasn't found (executor LookPath)
- `exec.Command` resolved the bare command (`glm47`) via `LookPath` against the parent process's (afm's) PATH, while the wrapper-dir (`ProxyShimDir`) was only added to the child's env → `start glm47: executable file not found`. The executor now resolves the command to `ProxyShimDir/<cmd>` (absolute path); for mounted binaries it falls back to the bare name. Regression test `TestRunAgentResolvesWrapperCommand`. Without this fix autoShim didn't work end-to-end.

## 2026-07-13

### Dashboard on React
- The web dashboard rewritten from vanilla JS (`app.js` + `markdown-it.min.js`) to **React 18 + Vite + TypeScript** (`pkg/web/dashboard/src`); markdown-it is bundled in, no more separate file.
- **`go:embed` restricted** to only the served static assets (`index.html`, `assets/`, styles, icons). Previously `dashboard/*` pulled `node_modules` (~96 MB) into the binary — the binary was 163 MB; now **14 MB**.
- **Frontend build wired into `make`**: the `web` target (`npm run build`) is now a prerequisite of `build`/`install`/`docker-build` — the web is always rebuilt and compiled into the binary.
- **`Dockerfile.runtime` multi-stage**: a node stage builds React, a go stage embeds it. `make release-*` now also builds the web (the release image always ships the current dashboard from source).
- `.dockerignore`: `**/node_modules/` excluded from the docker context.

### WebSocket keepalive
- The server pings connections (gorilla `PingMessage` + `SetReadDeadline` 60s + `PongHandler`) and drops "dead" clients; app-level `{"type":"heartbeat"}` every 30s (`pkg/server/websocket.go`, single-writer via `select`).
- Client (`use-event-feed`): auto-reconnect with backoff (existing) + **watchdog** (silence >75s → forced reconnect); heartbeat refreshes liveness but doesn't show up in the event feed.

### Resizable layout and maximize
- Panels built on **`react-resizable-panels`**: 3 columns (`stages | central | feed`) and vertical splits `plan/dialog/log` within central; sizes persisted to `localStorage`. Default 15/60/25 (columns), 30/45/25 (rows).
- **Maximize** (⛶ icon) for plan/dialog/feed panels to full screen via a React portal; internal state (scroll, input) is preserved, `Esc`/✕ collapses.

### "Waiting for user" signal
- For statuses `awaiting_user_input`/`awaiting_approval`: a pulsing stage element in the sidebar + a dot in the header + panel glow + `document.title` blinking in a background tab + auto-scroll of the central column to the waiting panel.

### Auto-scroll for dialog and feed
- Dialog and event feed stay pinned to the bottom as content arrives, until the user scrolls up themselves (a "↓ to latest" button); if a question is awaiting an answer, the dialog scrolls to it (both on load and on a new question).

### Dialog: only Q/A, no agent "thoughts"
- The dialog section no longer includes agent `text` blocks from the stream-json log (for GLM these are thinking-aloud that duplicated the log panel) — only questions/answers. Reasoning context stays in `LogPanel`. Answer-option buttons highlight the selection (`selected`).

### goga theme after the React migration
- `style-goga.css` rebuilt as `@import "style.css"` + goga design-tokens (previously a separate 1100-line file for vanilla-DOM — broke after the React migration). Both themes now share the structure from `style.css`, goga differs only by palette + overrides; the themes no longer diverge.
- goga overrides: "goga" logo (teal), plain background with no novacorps grid or `.ray`, panels on `--bg-elev`.
- `pkg/server/server.go`: CSS swap for `href="./style.css"` (Vite `base: './'`) — fixes style switching for goga.

### Server tests under React
- `TestServerServesMarkdownIt` → `TestServerServesReactBundle` (markdown-it is in the bundle); `TestServer_IndexDefaultTheme`/`_IndexGogaTheme` updated for the built React `index.html` (`./style.css`, `#root`, theme-class).

## 2026-07-09

### Dashboard theme `goga`
- A second web-dashboard theme, enabled via the `theme: goga` flag in `~/.afm/config.yaml` (top-level). Visually inspired by qarium.ru/goga: dark-blue background `#0A0E1A`, teal accent `#20D4BF`, sans-serif font, rounded corners, no neon decoration. Default theme is `novacorps` (the previous hi-tech mint one). Unknown value → warning + `novacorps`.
- Self-contained `pkg/web/dashboard/style-goga.css` (styled from scratch; `style.css`/`index.html` for the default untouched). Theme delivery — server-side replace of `style.css`→`style-goga.css` and a `<body>` class when serving `/` (no FOUC, no `/api/config`).
- Quarium logo + "Goga" title in the goga theme (CSS: Nova hexagon hidden, `background quarium-logo.png`, `h1`→"Goga" teal via `::before`).
- Consumption-chart palette (`app.js USAGE_COLORS`) read from CSS tokens with a mint fallback — chart is teal in goga, unchanged in novacorps.
- UI translated to English for both themes (`index.html`, `app.js`, CSS `content`).

### `open_browser` defaults to `false`
- `server.open_browser` (in `~/.afm/config.yaml`) now defaults to `false`: the browser is NOT opened automatically on dashboard startup — the URL is printed to the log with a `→ open this URL in your browser to follow the run` hint. `server.open_browser: true` restores the previous auto-open. Works for local runs and Docker (host-side opener).
- Note: macOS 26 "binary-signing issues" (SIGKILL of an unsigned binary) are NOT related to browser opening — fixed by `make install` (ad-hoc codesign), not this flag.

## 2026-07-08

### Global `prompt` (root-level)
- **Root-level field `prompt:`** in `flow.yaml` — a shared instruction injected into the system prompt of **every stage and every phase** (planning/implementation/review): rendered as a `<global_prompt>…</global_prompt>` block right after `</system_rules>`.
- Not to be confused with `stage.prompt` (2026-07-02) — that one addresses a specific stage after `</stage>`; the root one is shared across the whole run.
- Optional: empty/absent → the block isn't written, output is byte-identical to before (backward compatible). Content is escaped (`escapeTags`) — XML tags can't be injected.
- Threading: `flow.Flow.Prompt` → `orchestrator.Options.GlobalPrompt` → `prompts.Inputs.GlobalPrompt` → `Build` (5 call sites in orchestrator).

### Reverse-proxy: silent usage for non-200
- `captureUsage` no longer logs a warning for responses without a usage field — non-200 (errors, 429/529 rate-limit) are silently skipped (`pkg/proxy/proxy.go`). Previously every failed proxy response cluttered the log with an invalid-usage warning.

### Docker image versioning (SemVer + auto-bump)
- `make release-{patch,minor,major}` — versioned release: pushes the immutable `akopichin/afm:vX.Y.Z` and a rolling `:latest`. The tag auto-bumps from the last git tag (`scripts/release.sh`); the git tag is created locally after a successful push.
- Version baked into the binary: `afm --version` (including `docker run … afm --version`).
- `make docker-push` stays dev-only `:latest`. The `dockers` section removed from `.goreleaser.yml` (docker is now the Makefile's job).

## 2026-07-07

### Per-stage consumption accounting (Consumption / Accounting)
- afm tracks agent consumption (tokens / cost / KB) and attributes it to run stages. New package `pkg/accounting`: stage execution windows (`StageWindow`/`LoadStageWindows`), reading `usage.jsonl` and terminal result events, aggregation by metric and time bucket, deriving cost from tokens (`DeriveCost`), a query facade `Accountant.Query`.
- Data source — the reverse-proxy: uniformly captures usage from proxied responses (`UsageRecord`/`ParseUsage` → `usage.jsonl`), `proxy.New` accepts `usageLogPath`. No-double-counting rule: a stage with a proxy record doesn't get a result-usage fallback.
- **Config**: `pricing.models.<model>` (`input_per_mtok`/`output_per_mtok`/`cache_per_mtok`, USD per million tokens; nil/empty → cost hidden, exact model-name match, no fuzzy matching); `accounting.bucket_minutes` (aggregation bucket width, default 5).
- **HTTP**: `GET /api/usage?metric=tokens|cost|kb&stage=<id>` (`UsageHandler` from `Config.Accountant`).
- **Dashboard**: a consumption panel in `pkg/web/dashboard`.

## 2026-07-05

### fix(dialog): interactive stage while awaiting an answer
- An interactive stage no longer fails while waiting for a user answer: the agent may exit before the answer arrives, but the stage stays in `awaiting_user_input` (not `failed`) — `NotifyAnswer` restarts the agent after the answer.

## 2026-07-02

### Stage field `prompt`
- **`stage.prompt`** — an optional field: an explicit instruction to the agent, placed in a separate `<prompt>…</prompt>` block right after the stage context (`</stage>`).
- Unlike `description` (task background/context), this is a direct instruction on what to do. Content is escaped (`escapeTags`) — XML tags (`</stage>`, `</prompt>`, `<plan>`) can't be injected.
- The builder reads `Stage.Prompt` directly (like `description`/`skills`) — no separate `prompts.Inputs.Prompt` field or threading through `Build()` calls in orchestrator.

### Stage `name` in the dashboard
- **`RunState.stage_names`** (id→name, `omitempty`) threaded through the existing `/api/status`; populated from the flow file in `run.go` (works for both new and resumed runs). `SetStageNames`/`Snapshot()` copy the map (`maps.Clone`) — later mutations by caller code don't corrupt store state.
- **UI**: the left panel shows `id` (large, uppercase) + `name` below it in small text; the central panel's title is `name`, else `id`. Stages without `name` look as before.
- **README**: the `name` field corrected to optional (validation doesn't require it); added descriptions of `prompt` and `name` display in the dashboard.

## 2026-07-01

### Embedded skills in the binary
- **`afm install-skills`** — Claude skills (`/afm`, `/afm-check`, `/afm-init`, `/afm-retry`, `/afm-review`) embedded in the binary via `assets.SkillsFS`.
- One-command install: `afm install-skills [--skills-dir <path>] [--force]`. Idempotent — without `--force` existing files are skipped, with `--force` they're overwritten.
- `install.sh` delegates skill installation to the binary with an interactive `[Y/n]` prompt (default is to install).
- `install.sh` UX: explicit error + a `make build` hint if `bin/afm` isn't built; a "Done!" block only shown when skills are installed.

### Docker mode: stabilizing interactive flows
- Runs under **host-uid** (gosu entrypoint): no root-owned writes, volume files belong to the host user; claude allows `--dangerously-skip-permissions`.
- `isatty` check (`golang.org/x/term`) — `-it` only added on a real TTY.
- Dashboard port forwarding; browser opens on the host (host-side opener); `IS_SANDBOX=1`; `extra_mounts` for custom-agent tokens; HOME set after gosu.
- Security: secrets not in argv (`-e KEY` with no value); absolute `--dir` in the container; `.dockerignore`.

## 2026-06-30

### Docker mode
- **afm automatically re-execs itself inside Docker** (`docker.enabled` in config or `AFM_USE_DOCKER=1`).
- `Dockerfile.runtime` (ubuntu 24.04 + node 22 + python 3.12 + go 1.26 + gosu); `make docker-build/push/run`; goreleaser docker.
- Auto-mounting: project + `.afm/`, `~/.claude/`, `~/.afm/`, non-standard agents (`command: glm51` → binary mounted `:ro`); `extra_mounts` for configs/tokens.
- Dashboard reachable from the host (port forwarding `-p`).

### `--dir` and the rename to afm
- Flag **`--dir`** (`AFM_DIR`) — custom directory for `.afm` (runs, flows, config); priority flag > env > current directory.
- Rename **flowmanager → afm**: binary, command, env `AFM_*`, skills `/afm*`. (Repo directory and git name unchanged; module path stays the same.)

## 2026-06-29

### Built-in reverse-proxy
- **The built-in proxy** intercepts agents' HTTP traffic to Anthropic-compatible gateways and applies transforms.
- **`ZAITransform`** — a workaround for `api.z.ai` 529: rewrites a non-streaming request into streaming, collects the SSE, and reassembles it into a single Anthropic JSON response.
- **`CreateShim`** — support for wrapper commands (`glm51` and others): the shim wraps `claude`, the proxy address still reaches the real client even if the wrapper overwrites `ANTHROPIC_BASE_URL`.
- `ProxyConfig` in config: `proxy.enabled/upstream/port/transforms.zai` (nil/absent → enabled, auto-detects `api.z.ai` by host).
