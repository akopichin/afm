# Desktop notifications for stages needing action

## Context

The dashboard already has a small, well-factored "attention" pipeline for
telling a backgrounded tab something needs the user:

- `pkg/web/dashboard/src/hooks/use-attention/use-attention.ts` — the single
  source of truth. `ATTENTION_STATUSES` (`awaiting_user_input`,
  `awaiting_approval` only) feeds `anyAwaiting(stages)` (run-wide boolean) and
  `useAttention(stage)` (per-stage `{ needsAttention, kind: 'dialog' | 'plan' | null }`
  for whichever stage is currently *selected*).
- `pkg/web/dashboard/src/app/App.tsx:63-66` wires it up: `attention =
  useAttention(selectedStage)` feeds `useTitleFlash` (flashes `document.title`
  every 1500ms while hidden); `anyAttention = anyAwaiting(stages)` feeds both
  the header's `.attention-dot` (`FlowHeader.tsx:50`) and
  `useFaviconPulse` (composites an amber badge onto the live
  `link[rel="icon"]` href every 700ms while hidden,
  `hooks/use-favicon-pulse/use-favicon-pulse.ts`).
- **Gap:** `failed`/`hook_failed` are not in `ATTENTION_STATUSES` at all —
  today a failed stage doesn't flash the title, pulse the favicon, or light
  the header dot. `ACTIVE_STAGE_STATUSES` (`pkg/web/dashboard/src/types/stage.ts:53-61`)
  already includes `hook_failed` for auto-select purposes, but that's a
  separate concept.
- There is no `document.visibilityState`/focus tracking beyond the `document.hidden`
  + `visibilitychange` pattern `useTitleFlash`/`useFaviconPulse` already use
  (verified: no other visibility API usage in the dashboard). There is no
  Notification API usage, no service worker, no Permissions API usage
  anywhere in `pkg/web/dashboard/src` today — this part of the feature is
  greenfield.
- The tab favicon is a single static file (`favicon.svg`) with no
  light/dark variants — `useThemeMode` (`hooks/use-theme-mode/use-theme-mode.ts`)
  only toggles CSS colors via `data-theme`, never swaps the icon. So "the
  notification icon must match the tab icon" is satisfied by reading the
  live `link[rel="icon"]` href at fire-time (exactly what `useFaviconPulse`
  already does to find "the current favicon"), not by hardcoding a
  theme-specific path — this is correct today and stays correct if a
  theme-dependent favicon is ever added later.
- `Stage` (`types/stage.ts:18-29`) already carries `id`/`name`/`status` per
  stage — sufficient to build a per-stage notification without any new
  backend field or extra request.

## Goal

While the dashboard tab is not visible, fire one browser `Notification` per
*new* stage transition into `awaiting_approval` / `awaiting_user_input` /
`failed` (or `hook_failed`) — titled exactly `Need Approve` / `You have a
question` / `Stage failed` — plus one more check the moment the tab becomes
hidden, for any stage already sitting in one of those states at that moment.
Clicking a notification focuses the window, selects that stage, and closes
the notification.

## Non-goals

- No per-kind opt-out — one on/off toggle covers all three notification
  kinds, matching what was asked.
- No question/error text preview in the notification body — stage name and
  the time the notification fired are enough for the MVP; fetching the
  actual question/error text would require an extra request per transition
  that the lightweight `/api/status` poll doesn't carry, deferred.
- No cross-tab deduplication. Two browser tabs open on the same run can each
  independently notify for the same stage if both are hidden at the same
  time — each tab keeps its own in-memory "already notified" state, exactly
  the same accepted limitation `docs/superpowers/specs/2026-07-28-favicon-attention-pulse-design.md`
  already documents for the favicon pulse.
- No service worker / push support for a fully *closed* tab — this is a
  "tab open, not focused" feature. A closed tab has no JS context to hold a
  `Notification` object at all.
- No custom notification sound — whatever the OS/browser does by default for
  a `Notification` applies.

## Design

### 1. Extend the attention pipeline to include failure

`ATTENTION_STATUSES` gains `failed` and `hook_failed`. `AttentionKind` gains
a third value, `'failed'`, mapped from either of those two statuses in
`useAttention`. This is a deliberate, small behavior change to the
*already-shipped* favicon-pulse/title-flash features too (per your call): a
failed stage now also flashes the title (if it's the selected stage) and
pulses the favicon / lights the header dot (run-wide) — consistent with
treating failure as just another kind of "the run needs you."

New selector, alongside the existing `anyAwaiting`:

```ts
export type AttentionEntry = { stage: Stage; kind: AttentionKind }

export function stagesNeedingAttention(stages: Stage[]): AttentionEntry[] {
  return stages
    .filter((s) => ATTENTION_STATUSES.has(s.status))
    .map((s) => ({
      stage: s,
      kind: s.status === 'awaiting_user_input' ? 'dialog'
          : s.status === 'awaiting_approval' ? 'plan'
          : 'failed',
    }))
}
```

`anyAwaiting` stays (still used for the header dot / favicon pulse gating —
a boolean is all those two need); `stagesNeedingAttention` is what the new
notification hook consumes for its per-stage detail.

### 2. `useDesktopNotifications` — the reconciliation hook

New hook, `pkg/web/dashboard/src/hooks/use-desktop-notifications/use-desktop-notifications.ts`:

```ts
export type NotificationPermissionState = 'unsupported' | 'default' | 'granted' | 'denied'

export function useDesktopNotifications(
  stages: Stage[],
  onFocusStage: (stageId: string) => void,
): {
  enabled: boolean
  permission: NotificationPermissionState
  requestEnable: () => void
  disable: () => void
}
```

**State:**
- `enabled: boolean` — persisted to `localStorage['afm-notifications-enabled']`
  (`'1'`/absent), read once at mount, mirroring `use-theme-mode`'s
  `afm-mode` pattern exactly.
- `permission` — derived from `'Notification' in window ? Notification.permission
  : 'unsupported'`, re-read after `requestEnable()` resolves.
- `notifiedStageIds: Set<string>` (a `useRef`, not React state — it's
  write-only bookkeeping, no re-render should depend on it) — every stage id
  a notification has already fired for, for its *current* attention episode.

**The reconciliation pass** (one function, called from two places):

```ts
function reconcile(stages: Stage[]) {
  const current = stagesNeedingAttention(stages)
  const currentIds = new Set(current.map((e) => e.stage.id))

  // стадия вышла из attention — забываем её, чтобы следующий заход в
  // attention (например: упала → retried → упала снова) снова уведомил.
  for (const id of notifiedStageIds.current) {
    if (!currentIds.has(id)) notifiedStageIds.current.delete(id)
  }

  if (!enabled || !document.hidden) return
  for (const entry of current) {
    if (notifiedStageIds.current.has(entry.stage.id)) continue
    notifiedStageIds.current.add(entry.stage.id)
    fireNotification(entry, onFocusStage)
  }
}
```

Called via `useEffect(() => reconcile(stages), [stages])` (covers "new
transition while already hidden") **and** from a `visibilitychange`
listener that calls `reconcile(stagesRef.current)` when `document.hidden`
becomes `true` (covers "already waiting when the user leaves the tab" — per
your answer). Because a stage that entered attention *while visible* is
never added to `notifiedStageIds` (the `!document.hidden` early-return skips
it), the later `visibilitychange` pass picks it up as still-unnotified —
one code path, two triggers, no separate "was this already true before I
left" bookkeeping needed. A `useRef` mirror of `stages` is required for the
visibilitychange listener to read fresh data without re-subscribing the
listener on every poll tick.

**Firing one notification:**

```ts
function fireNotification(entry: AttentionEntry, onFocusStage: (id: string) => void) {
  const titles: Record<AttentionKind, string> = {
    plan: 'Need Approve',
    dialog: 'You have a question',
    failed: 'Stage failed',
  }
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href
  const time = new Date().toLocaleTimeString()
  let n: Notification
  try {
    n = new Notification(titles[entry.kind], {
      body: `${entry.stage.name} — click to view\n${time}`,
      icon,
      tag: `afm-stage-${entry.stage.id}`,
    })
  } catch {
    return // best-effort — some environments throw synchronously, never crash the app over this
  }
  n.onclick = () => {
    window.focus()
    onFocusStage(entry.stage.id)
    n.close()
  }
}
```

`tag` scopes replacement to the same stage at the OS/browser level — cheap
extra safety on top of the `notifiedStageIds` guard, not load-bearing.
`Date.now()`/`new Date()` here is fine (this is runtime UI code, not a
Workflow script) — no determinism constraint applies.

**`requestEnable()`** must run synchronously inside the button's `onClick`
(a user gesture) per browser requirements:

```ts
function requestEnable() {
  if (!('Notification' in window)) return
  void Notification.requestPermission().then((result) => {
    setPermission(result)
    if (result === 'granted') {
      setEnabled(true)
      localStorage.setItem('afm-notifications-enabled', '1')
    }
  })
}
```

`disable()` just flips `enabled` to `false` and removes the localStorage
key — it does not revoke the browser's already-granted permission (there is
no API to do that from JS); re-clicking the toggle after a manual disable
skips the permission prompt (`Notification.permission` is already
`'granted'`) and goes straight back to `enabled = true`.

### 3. Header toggle

`FlowHeader.tsx` gains a bell button next to the existing dark/light toggle,
reusing the existing `.icon-btn` class (same outline style, no new base
CSS needed beyond a small `.icon-btn:disabled` rule and an "on" state
modifier):

- `permission === 'unsupported'` → button not rendered at all (progressive
  enhancement — no point showing a control that can never work).
- `permission === 'denied'` → rendered `disabled`, `title="Notifications blocked in browser settings"`.
- `permission === 'default'` or (`'granted'` and `!enabled`) → "off" glyph,
  click calls `requestEnable()`.
- `permission === 'granted'` and `enabled` → "on" glyph (visually
  distinguished via a modifier class using the same mint accent the hover
  state already uses), click calls `disable()`.

`App.tsx` wires it: `const { enabled, permission, requestEnable, disable } =
useDesktopNotifications(stages, setSelectedStageId)`, passed down to
`FlowHeader` as new props alongside the existing `attention` prop.

### Testing

- `use-attention.test.ts`: extend for `failed`/`hook_failed` → `kind: 'failed'`,
  `anyAwaiting` now true for those statuses too.
- `use-desktop-notifications.test.ts`, same mocking style as
  `use-favicon-pulse.test.ts` (fake `link[rel="icon"]` in `document.head`,
  `Object.defineProperty(document, 'hidden', ...)` + dispatched
  `visibilitychange`, `vi.useFakeTimers()` where needed): mock the global
  `Notification` (constructor spy + static `permission`/`requestPermission`),
  and cover — new transition while hidden fires once; transition while
  visible does not fire, but a later `visibilitychange`-to-hidden does;
  resolving the attention (stage leaves the set) then re-entering it later
  fires again; `enabled=false` never fires regardless of visibility;
  `requestEnable()` on grant sets `enabled` + persists; clicking a fired
  notification calls `window.focus`, `onFocusStage(stageId)`, and closes it.
- `FlowHeader.test.tsx` (already exists, covers the theme toggle today —
  extend it): the `unsupported`/`denied`/`default-or-off`/`on` button states
  render the right glyph/label/disabled-ness, and clicking calls
  `requestEnable`/`disable` as appropriate.
