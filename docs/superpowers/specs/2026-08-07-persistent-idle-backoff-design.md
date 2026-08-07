# Persistent IDLE/BACKOFF footer metrics — design

Date: 2026-08-07

## Context

The dashboard footer shows `PROGRESS` / `STARTED` / `ELAPSED` / `IDLE` / `BACKOFF`.
`PROGRESS` comes straight from the latest `/api/status` poll, and `STARTED`/`ELAPSED`
are already restart-safe: `STARTED` is the server's persisted `RunState.StartedAt`
(`pkg/state/state.go`), and `ELAPSED` (`useElapsed`, `pkg/web/dashboard/src/hooks/use-elapsed/`)
is just `Date.now() - Date.parse(startedAt)` ticking every second — a page reload
re-fetches the same anchor and recomputes correctly.

`IDLE` and `BACKOFF` are not restart-safe. Both are computed entirely client-side
by two hooks (`useIdleTime`, `useStatusDuration`, `pkg/web/dashboard/src/hooks/`)
that incrementally replay `stage_status_changed` events held in the browser's
`events` array (from `useEventFeed`). This accumulation survives *within* one
open tab (each event is processed exactly once, keyed by `stageId+timestamp+status`,
so events already outside `useEventFeed`'s 200-event cap don't get "un-counted"),
but a page reload resets the accumulator to zero and can only replay whatever
`GET /api/events` still has in its own 200-event cap
(`pkg/server/events_handler.go`) — on a long-running flow, older transitions
are gone, and `IDLE`/`BACKOFF` silently undercount from then on.

Separately, both metrics tick via a plain `setInterval`/`Date.now()` regardless
of whether the dashboard's WebSocket is actually connected
(`connected`, returned by `useEventFeed` and already wired to `FlowHeader`'s
`LINK`/`OFFLINE` badge, but not consulted by these timers) — while
disconnected, the displayed numbers keep advancing on a stale assumption that
nothing has changed, which may not be true.

## Goals

1. `IDLE` and `BACKOFF` survive a dashboard reload/restart with the same
   accuracy `STARTED`/`ELAPSED` already have — reopening the dashboard days
   later, or from a different browser, must show correct cumulative values,
   not values reconstructed from a capped in-memory replay.
2. `IDLE` and `BACKOFF` stop advancing while the WebSocket is disconnected,
   and resume from a corrected value once reconnected.
3. `ELAPSED` (and `STARTED`) are explicitly out of scope — confirmed
   already correct and independent of event history, left ticking through
   disconnects.
4. The dialog/event-feed's static per-row time gaps are explicitly out of
   scope — confirmed non-ticking by design already, nothing to persist.

## Design

### 1. Backend: durable idle/backoff tracking in `pkg/state`

`RunState` (`pkg/state/state.go`) gains exactly two new persisted fields:

```go
IdleAccumulatedMs    int64 `json:"idle_accumulated_ms"`
BackoffAccumulatedMs int64 `json:"backoff_accumulated_ms"`
```

`idle_since` and `backoff_open_since` (see API below) need no persisted field
at all — they're derived on read from data `RunState` already has. The start
of the *current* idle window is always exactly the latest `Stages[].UpdatedAt`
across all stages (whichever stage's transition most recently made the flow
idle); the start of a stage's *current* backoff episode is always exactly
that stage's own `UpdatedAt` while its `Status` is `retrying`. Two `RunState`
methods compute these on demand: `IdleSince() *time.Time` and
`BackoffOpenSince() []time.Time`.

A single shared helper, called `accountIdleAndBackoff(rs, stageID, to, t)`,
updates the two accumulators using `rs.Stages` as it was *before* the
transition is applied. It is called from **both** places a `Transition`
mutates a `RunState`:

- `Store.Apply` (`pkg/state/store.go:257`) — the live path, used while a run
  is actively executing.
- `parseEventLog` (`pkg/state/state.go`) — the replay path shared by
  `Store.Open` (resuming a run's in-memory state from `events.jsonl` when
  the process restarts) *and* `LoadRunState` (read-only access, e.g. `afm
  check`). Note this is a different function from `Apply` — replay does not
  go through `Apply` at all, it mutates `RunState` directly — so both call
  sites need the same accounting logic for resume to reconstruct the same
  numbers a live run would have had.

- **Idle** is a flow-wide state, not per-stage — ported directly from the
  existing `isIdle()` state machine in `use-idle-time.ts`: idle if any stage
  is in `awaiting_user_input`/`awaiting_approval`, OR if some stage is
  `failed` and no stage is actively `running`/`planning`/`revising`
  (`retrying` intentionally does not count as "active" — it's passive
  backoff, not agent work, per the existing TS comment). If the state
  *before* a transition was idle, the gap since the last transition
  (`maxUpdatedAt` over all stages) is added to `IdleAccumulatedMs`.
- **Backoff** sums time each stage individually spends in `retrying`
  (`BACKOFF_STATUSES` in `pkg/web/dashboard/src/types/stage.ts`), and — like
  the existing `useStatusDuration` — sums *parallel* open episodes rather
  than merging them (an existing, intentional simplification carried over
  as-is). When a stage's transition leaves `retrying`, the elapsed time
  since that stage's own `UpdatedAt` (i.e., since it entered `retrying`) is
  added to `BackoffAccumulatedMs`.

**No backfill needed.** `LoadRunState` (`state.go:98`) and `Store.Open`
already reconstruct `RunState` by replaying every historical `Transition` in
`events.jsonl` through `parseEventLog`. Since idle/backoff accounting lives
inside that same shared function, replaying an existing run's full log with
the new code correctly reconstructs accurate cumulative values from
scratch — runs started before this change ships get correct numbers the
next time their state is loaded or resumed, with no migration or
special-casing.

### 2. API surface

`/api/status`'s run-state payload (`pkg/server/handlers.go`, `handleStatus`)
gains four fields, serialized from the `RunState` additions above:

```json
{
  "idle_accumulated_ms": 12345,
  "idle_since": "2026-08-07T10:02:00Z",
  "backoff_accumulated_ms": 6789,
  "backoff_open_since": ["2026-08-07T10:05:00Z"]
}
```

`idle_since` is omitted/null when not currently idle. `backoff_open_since`
is a plain array of ISO timestamps, one per stage currently in `retrying`
(order doesn't matter — the frontend only sums `now - t` over the array).

### 3. Frontend: hooks shrink to anchor+tick math

`useIdleTime` and `useStatusDuration` are deleted along with their
event-replay reducers (`processedKeys`, `statusByStage`/`openSince` refs) —
they no longer need `events` at all. In their place, two small hooks
mirroring `useElapsed`'s shape exactly:

```ts
function useIdleMs(idleAccumulatedMs: number, idleSince: string | null, connected: boolean): number
function useBackoffMs(backoffAccumulatedMs: number, backoffOpenSince: string[], connected: boolean): number
```

Each ticks once a second via `setInterval`, computing
`accumulated + liveDelta` where `liveDelta` is `now - Date.parse(since)`
(idle) or the sum of `now - Date.parse(t)` over the open array (backoff).
Both take the new `connected` parameter (see below).

### 4. Freeze while offline

`App.tsx` already destructures `connected` from `useEventFeed`
(`App.tsx:64`), currently only passed to `FlowHeader`. The new hooks stop
advancing their live delta while `connected === false` — the displayed
value simply holds at whatever it last computed. The moment the WebSocket
reconnects, `useStatus`'s existing poll/refresh fetches a fresh
`/api/status`, which carries an already-correct `idle_accumulated_ms`/
`idle_since` (the server kept tracking the whole time, regardless of any
client's connection state) — the hooks resume ticking from that corrected
anchor. No client-side reconciliation logic is needed; the server was never
not tracking.

`ELAPSED` is unchanged — it keeps ticking through disconnects, per the
earlier decision that it's already accurate independent of event history.

## Testing

**Go (`pkg/state`):** unit tests on `Store.Apply`/`LoadRunState` covering:
idle turning on for a question status, idle turning on for
failed-with-nothing-active, idle turning back off when the cascading-failed
scenario's active agent starts running elsewhere (the exact bug the
existing TS comment documents fixing), backoff accumulating a single
episode, and backoff summing two parallel retrying stages. Also a
replay-from-scratch test: apply a sequence of transitions, restart a fresh
`Store` from the resulting `events.jsonl` via `LoadRunState`, and confirm
`IdleAccumulatedMs`/`BackoffAccumulatedMs` match what the live run had.

**Frontend:** `useIdleMs`/`useBackoffMs` get straightforward anchor-math
tests (accumulated + live delta, clamped/frozen when `connected=false`,
resuming correctly when `connected` flips back to `true` with a new
accumulated/since pair simulating a fresh `/api/status` fetch). Existing
`Footer.test.tsx` / `App.test.tsx` coverage is updated for the new prop
shapes but the visual contract (what the footer displays) is unchanged.
