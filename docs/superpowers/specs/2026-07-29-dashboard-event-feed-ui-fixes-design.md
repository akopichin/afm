# Dashboard: event feed & footer UI fixes

Five small, independent fixes to `pkg/web/dashboard/src` (React 18 + TypeScript,
Vite, no external state library — plain hooks composed in `App.tsx`).

## 1. Sticky auto-scroll loses track / "↓ latest" button stops appearing

**Symptom:** the event feed auto-scrolls to new messages, but at some point
(user unsure exactly when — possibly after toggling a panel's maximize/restore
button) it stops re-sticking even after the user scrolls back to the bottom.
The floating "↓ latest" jump button, which should appear whenever the user has
scrolled away from the bottom, also sometimes stops appearing at all on large
flows or after a restart.

**Root cause:** `Maximizable` (`pkg/web/dashboard/src/components/layout/Maximizable.tsx`)
conditionally returns either a plain fragment (`<>{children}</>`) or
`createPortal(<div className="maximize-overlay">{children}</div>, document.body)`
depending on whether this panel is currently maximized. These are structurally
different element trees at the same tree position, so React unmounts and
remounts everything inside — including `EventFeedPanel` and its
`useStickToBottom` hook — every time maximize/restore is toggled. The hook's
`stick` state resets to its default (`true`) on remount, silently undoing
whatever scroll-position tracking had accumulated, and the code comment in
`Maximizable.tsx` claiming "instance is preserved, state is not lost" is
incorrect for this reason.

This is the most likely explanation for both symptoms, since it's the only
remount path in this code, but it has not been confirmed as the exact
repro the user observed.

**Fix:** make `Maximizable` always render its children through
`createPortal`, targeting either `document.body` (maximized) or a stable
placeholder `<div>` kept mounted inline at the panel's normal position
(restored) — never switching between a fragment and a portal. React treats a
change of a portal's `containerInfo` as an update (moving the DOM subtree),
not a remount, so the component tree and all of its hook state survive the
toggle. This fixes the bug for every panel that uses `Maximizable` (plan,
dialog, log, feed), not just the event feed.

If the button-disappearing symptom (#5) persists after this fix, that
indicates a separate cause inside `use-stick-to-bottom.ts`'s scroll/
MutationObserver logic, to be debugged separately at that point.

## 2. Per-row timestamps: static duration instead of a live "N seconds ago" ticker

**Current behavior:** `EventFeedPanel` ticks a shared `setInterval` every 5s
(`RELATIVE_TIME_REFRESH_MS`) to recompute `formatRelativeTime` for every row,
showing "how long ago" each event happened. This is noisy and doesn't help
answer "how long did a given operation take."

**Change:**
- Remove the tick entirely (`setTick` state + its `setInterval`).
- Each row instead shows a static duration computed once at render:
  `timestamp(event N) − timestamp(event N−1)`, where "previous event" is the
  prior entry in the already-chronological `events` array (across all
  stages, matching the visual order of the feed — not scoped per-stage).
- The first row in the array has no predecessor and shows an em dash (`—`).
- Reuse the existing short-duration bucket formatting (`Ns`/`Nm`/`Nh`/`Nd`).

This is a pure render-time computation over already-available data — no new
state, no ticking, no backend change.

## 3. Footer: Idle and Backoff counters next to Elapsed

**Goal:** make it visible how much of a run's wall-clock time was spent
waiting on the user vs. waiting on automatic retry backoff, alongside the
existing `Elapsed` counter.

**Two new footer counters:**
- **Idle** — cumulative time any stage spent in `awaiting_user_input`,
  `awaiting_approval`, or `failed` (a stopped stage waiting for a manual
  retry is "waiting on the user" just as much as an open dialog question).
- **Backoff** — cumulative time any stage spent in `retrying` (automatic
  retry backoff, no user action involved) — tracked separately since it
  answers a different question ("how much time did the flow itself burn
  retrying") than Idle does.

**Implementation — one generic hook, parameterized by status set:**

```ts
function useStatusDuration(events: AfmEvent[], statuses: ReadonlySet<StageStatus>): number
```

- Walks `stage_status_changed` events (via the existing `extractStageStatus`),
  tracking a per-stage "opened at" timestamp whenever that stage's status
  enters the given `statuses` set, and accumulating `closedAt − openedAt`
  into a running total when it leaves the set (to any other status).
- Processes events **incrementally** — tracks how far it has already
  consumed the `events` array (not a from-scratch rescan) — so a completed
  episode stays counted in the total even after the event buffer is later
  trimmed (see cap note below). Each call site holds its own independent
  accumulator; `Idle` and `Backoff` don't share state even though they scan
  the same `events` array.
- While any stage currently has an open episode, the returned value adds a
  live `now − openedAt` delta, ticking every second (same cadence as the
  existing `useElapsed`).
- Concurrent stages both inside the status set at once: durations are
  simply summed, not merged/deduped. This is a deliberate simplification —
  in the rare case of overlapping episodes across parallel stages, the
  counter could nominally read slightly higher than wall-clock elapsed.

**Footer UI:** two new items, `Idle:` and `Backoff:`, next to `Elapsed:`,
using the same `M:SS` / `H:MM:SS` formatting; both show `0:00` once the run
has started (mirroring how `Elapsed` shows `--` only before `startedAt`
exists).

**Known limitations, both stemming from the same 200-event cap:**
- `/api/events` is capped server-side to the last 200 events
  (`maxReplayEvents` in `pkg/server/events_handler.go`), matching the
  frontend's own `MAX_EVENTS` cap in `use-event-feed.ts` — this is a hard
  ceiling on the backend replay endpoint itself, not just a frontend memory
  buffer. On a page refresh, both counters reconstruct purely from whatever
  is in that last-200-event batch; on a very long run, wait/backoff episodes
  further back than that are permanently unrecoverable after a refresh.
  `Elapsed` is unaffected by this, since it's derived from the run's
  absolute `startedAt`, not from events.
- A WS reconnect (brief network blip, no page refresh) does **not**
  currently re-fetch `/api/events` — `useEventFeed`'s history fetch only
  runs once, on mount. If a stage's status actually changed while the
  socket was down, that transition is simply never delivered (`/ws` doesn't
  replay missed messages), which could leave a counter's open episode stuck
  accumulating past when it should have closed. **Fix included in this
  change:** when `connected` flips `false → true`, re-run the same
  `/api/events` fetch + `mergeHistory` merge that already runs on mount, to
  backfill anything missed during the outage. This reuses existing
  merge/dedup logic and benefits the event feed's accuracy generally, not
  just these two counters.

## 4. "Thinking" indicator showing while offline

**Fix:** the badge in `App.tsx` currently renders whenever
`selectedStage.status === 'running'`. Add the existing `connected` flag
(already computed by `useEventFeed` and threaded through `App.tsx` for the
`FlowHeader` badge) to the condition:
`selectedStage.status === 'running' && connected`. No new state — the badge
disappears immediately when the socket drops and reappears once `connected`
is `true` again (assuming the stage is still `running`).

## Summary of touched files

| File | Change |
|---|---|
| `components/layout/Maximizable.tsx` | Always portal; stable placeholder container when not maximized — fixes #1 and (likely) #5 |
| `components/event-feed/EventFeedPanel.tsx` | Remove ticking relative time; static delta-from-previous-event duration per row |
| `hooks/use-status-duration/` (new) | Generic `useStatusDuration(events, statuses)` hook, used for both Idle and Backoff |
| `components/footer/Footer.tsx` | Add `Idle` and `Backoff` footer items |
| `app/App.tsx` | Wire the two new hooks into `Footer`; gate "thinking" badge on `connected` |
| `hooks/use-event-feed/use-event-feed.ts` | Re-fetch `/api/events` and merge on reconnect (`connected` false→true transition) |
