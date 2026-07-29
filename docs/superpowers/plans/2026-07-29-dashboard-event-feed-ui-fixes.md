# Dashboard Event Feed & Footer UI Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix five small dashboard UI issues — sticky auto-scroll losing track, the "↓ latest" jump button disappearing, per-message timestamps ticking instead of showing static durations, no visibility into idle/retry-backoff time, and the "thinking" badge showing while offline.

**Architecture:** All changes are confined to `pkg/web/dashboard/src` (React 18 + TypeScript + Vite, no external state library — plain hooks composed in `App.tsx`) plus two CSS files. `Maximizable` is changed to always portal (never switch element type) so component state survives maximize/restore. `EventFeedPanel` drops its ticking relative-time display for a static per-row computation. A new generic `useStatusDuration` hook (parameterized by a set of `StageStatus`) powers two new footer counters (Idle, Backoff), reusing the same accumulation logic. `use-event-feed.ts` gains a resync-on-reconnect step so those counters (and the feed generally) don't miss transitions during a brief WS outage. The "thinking" badge gets a `connected` guard.

**Tech Stack:** React 18.3, TypeScript 5.6 (strict), Vite 5.4, Vitest 2 + @testing-library/react 16, jsdom.

**Design doc:** `docs/superpowers/specs/2026-07-29-dashboard-event-feed-ui-fixes-design.md`

## Global Constraints

- Never bump the Go module version (not touched by this plan — frontend-only, but stated per project-wide instruction).
- After every task, run `npm run typecheck` and `npm test` from `pkg/web/dashboard/` and confirm both are clean before committing — no exceptions.
- No outdated/deprecated language constructs; match existing code style exactly (this codebase's own comments are in Russian — new comments must be too, written only where the *why* isn't obvious from the code).
- All git commit messages must be in Russian, and must NOT include a `Co-Authored-By` trailer.
- Work in small, focused files; each task's deliverable must be independently testable (existing suite green) before moving to the next task.

---

## File Structure

| File | Responsibility |
|---|---|
| `components/layout/Maximizable.tsx` | Modify — always portal (never Fragment↔Portal), preserves child state across maximize/restore |
| `components/layout/Maximizable.test.tsx` | Modify — add state-preservation regression test |
| `components/event-feed/EventFeedPanel.tsx` | Modify — static per-row duration instead of ticking relative time |
| `components/event-feed/EventFeedPanel.test.tsx` | Modify — add duration tests |
| `hooks/use-status-duration/use-status-duration.ts` | Create — generic `useStatusDuration(events, statuses)` hook |
| `hooks/use-status-duration/index.ts` | Create — barrel export |
| `hooks/use-status-duration/use-status-duration.test.ts` | Create |
| `components/footer/Footer.tsx` | Modify — add Idle/Backoff footer items |
| `components/footer/Footer.test.tsx` | Modify — add tests for new items |
| `skins/base/layout.css`, `public/skins/base/layout.css` | Modify — `.maximizable-anchor`/`.maximizable-inline` layout rules; `#footer` grid-template-columns widened to 5 tracks |
| `hooks/use-event-feed/use-event-feed.ts` | Modify — re-fetch `/api/events` history on reconnect (not just on mount) |
| `hooks/use-event-feed/use-event-feed.test.ts` | Modify — add resync-on-reconnect test |
| `types/stage.ts`, `types/index.ts` | Modify — add `IDLE_STATUSES`, `BACKOFF_STATUSES` constants (mirrors existing `ACTIVE_STAGE_STATUSES`) |
| `app/App.tsx` | Modify — wire `useStatusDuration` into `Footer`; gate "thinking" badge on `connected` |
| `app/App.test.tsx` | Modify — add thinking/offline test and idle-accumulation test |

---

### Task 1: `Maximizable` always portals — fixes sticky-scroll and jump-button state loss

**Files:**
- Modify: `pkg/web/dashboard/src/components/layout/Maximizable.tsx`
- Modify: `pkg/web/dashboard/src/components/layout/Maximizable.test.tsx`
- Modify: `pkg/web/dashboard/skins/base/layout.css`
- Modify: `pkg/web/dashboard/public/skins/base/layout.css`

**Interfaces:**
- Produces: `Maximizable` and `MaximizeProvider`/`useMaximize` keep their existing public signatures (`{ id: string; children: ReactNode }`, `MaximizeState`) — no consumer of this component needs to change.

- [ ] **Step 1: Write the failing regression test (state must survive maximize/restore)**

In `pkg/web/dashboard/src/components/layout/Maximizable.test.tsx`, change the top import line from:

```tsx
import { describe, it, expect } from 'vitest'
```

to:

```tsx
import { useState } from 'react'
import { describe, it, expect } from 'vitest'
```

Then append this (keep the existing `describe('Maximizable', ...)` block in the file untouched, add the following after it):

```tsx
function Counter() {
  const [count, setCount] = useState(0)
  return (
    <button onClick={() => setCount((c) => c + 1)}>{`clicks:${count}`}</button>
  )
}

describe('Maximizable state preservation', () => {
  it('preserves child component state across maximize and restore (no remount)', () => {
    render(
      <MaximizeProvider>
        <Maximizable id="feed">
          <Counter />
        </Maximizable>
        <Toggle id="feed" />
      </MaximizeProvider>,
    )

    fireEvent.click(screen.getByText('clicks:0'))
    expect(screen.getByText('clicks:1')).toBeInTheDocument()

    // Maximize — if Maximizable remounts its children, this resets to clicks:0.
    fireEvent.click(screen.getByText('toggle'))
    expect(screen.getByText('clicks:1')).toBeInTheDocument()

    fireEvent.click(screen.getByText('clicks:1'))
    expect(screen.getByText('clicks:2')).toBeInTheDocument()

    // Restore — same check in the other direction.
    fireEvent.click(screen.getByText('toggle'))
    expect(screen.getByText('clicks:2')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/components/layout/Maximizable.test.tsx`
Expected: FAIL — the new test's second assertion (`clicks:1` after toggling) fails because `Counter` remounts and resets to `clicks:0`.

- [ ] **Step 3: Rewrite `Maximizable` to never remount — no portal at all, just a CSS class toggle**

An earlier version of this plan had `Maximizable` always call `createPortal`, switching only its `container` argument (anchor div ↔ `document.body`) between renders, reasoning that React would treat this as an in-place container update. **That premise was verified wrong**: a portal's `container` is part of its identity for React's reconciler — changing it between renders unmounts and remounts the portalled subtree just as much as switching between a Fragment and a Portal did. (Verified directly: a minimal repro with `createPortal(<Counter/>, containerA)` → rerender with `createPortal(<Counter/>, containerB)` loses `Counter`'s state in this project's actual vitest+jsdom+React 18.3.1 setup.)

The simplest correct fix removes portals from `Maximizable` entirely: render `children` in ONE stable `<div>` that never leaves its original position in the tree (same as the rest of the app), and toggle a CSS class on that same div to switch between normal in-flow layout and a fullscreen `position: fixed` overlay. Since the div's type and tree position never change — only its `className` — React only ever diffs props, never unmounts. (Verified via the same kind of repro: a `Counter` inside this pattern keeps its count across the toggle.)

This works safely in this codebase specifically because no ancestor between `Maximizable` and `document.body` establishes a CSS containing block or stacking context that would trap a `position: fixed` descendant: `react-resizable-panels`' `Panel`/`Group` set no `transform`/`filter`/`perspective`/`contain`/`will-change` and no `position` + non-`auto` `z-index` combination on their own root elements (checked directly in `node_modules/react-resizable-panels/dist/react-resizable-panels.js`), and the only such combination in this project's own CSS (`#stages-panel, #detail-panel, #feed-panel { position: relative; z-index: 1; }` in `layout.css`) targets IDs that are *descendants* of `Maximizable` (e.g. `<aside id="feed-panel">` inside `EventFeedPanel`), not ancestors — so they cannot trap the fixed overlay.

Replace the full contents of `pkg/web/dashboard/src/components/layout/Maximizable.tsx`:

```tsx
import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

type MaximizeState = { maximizedKey: string | null; toggle: (key: string) => void }
const MaximizeContext = createContext<MaximizeState>({ maximizedKey: null, toggle: () => {} })

export function MaximizeProvider({ children }: { children: ReactNode }) {
  const [maximizedKey, setMaximizedKey] = useState<string | null>(null)
  const toggle = useCallback((key: string) => {
    setMaximizedKey((cur) => (cur === key ? null : key))
  }, [])
  useEffect(() => {
    if (maximizedKey === null) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMaximizedKey(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [maximizedKey])
  return <MaximizeContext.Provider value={{ maximizedKey, toggle }}>{children}</MaximizeContext.Provider>
}

export function useMaximize(): MaximizeState {
  return useContext(MaximizeContext)
}

// Максимизация БЕЗ портала: один и тот же <div> всегда монтирован на своей
// исходной позиции в дереве — meняется только className (просто CSS
// position:fixed на maximize-overlay даёт полноэкранный вид, см. layout.css).
// Раньше здесь был createPortal (сначала Fragment↔Portal, потом
// anchor↔document.body) — в обоих случаях React видел смену идентичности
// узла на этой позиции и размонтировал детей при каждом toggle, сбрасывая их
// состояние (баг: стик-скролл ленты событий и видимость кнопки «↓ latest»
// терялись при maximize/restore). Без портала контейнер вообще не меняется —
// React только обновляет className, дети остаются смонтированными.
export function Maximizable({ id, children }: { id: string; children: ReactNode }) {
  const { maximizedKey } = useMaximize()
  const maximized = maximizedKey === id

  return (
    <div
      className={`maximizable-frame${maximized ? ' maximize-overlay' : ''}`}
      role={maximized ? 'dialog' : undefined}
      aria-modal={maximized ? true : undefined}
    >
      {children}
    </div>
  )
}
```

- [ ] **Step 4: Add the layout CSS for the new stable wrapper element**

`.panel-frame` (and its ancestors `.detail-column`, `.panel-frame-body > *`) rely on `height: 100%` percentage chains. The new `.maximizable-frame` wrapper div sits between `Panel` and `.panel-frame` now (where a zero-node `Fragment` used to sit), so it needs to pass a definite height through, matching the existing pattern used by `.panel-frame-body > *`.

Add to **both** `pkg/web/dashboard/skins/base/layout.css` and `pkg/web/dashboard/public/skins/base/layout.css` (these two files are kept identical in this repo — edit both), right after the `.panel-frame-body > *` block:

```css
/* Maximizable больше не портал: .maximizable-frame — единственная обёртка,
   всегда на своей исходной позиции в дереве. Не-maximized — обычный
   flex-контейнер, пробрасывающий высоту 100% дальше вниз к .panel-frame
   (иначе scroll-контейнеры внутри, напр. event-feed-scroll, схлопнутся).
   maximized добавляет .maximize-overlay (см. ниже) — тот же элемент, тот же
   DOM-узел, просто position:fixed вместо обычного потока. */
.maximizable-frame {
  height: 100%;
  min-height: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
}
```

- [ ] **Step 5: Run the test again to verify it passes, plus the full suite**

Run: `cd pkg/web/dashboard && npx vitest run src/components/layout/Maximizable.test.tsx`
Expected: PASS, both tests (the original toggle test and the new state-preservation test).

Run: `cd pkg/web/dashboard && npm run typecheck && npm test`
Expected: all pass (this change affects every panel using `Maximizable` — plan, dialog, log, feed — so the full suite must stay green).

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/layout/Maximizable.tsx \
        pkg/web/dashboard/src/components/layout/Maximizable.test.tsx \
        pkg/web/dashboard/skins/base/layout.css \
        pkg/web/dashboard/public/skins/base/layout.css
git commit -m "$(cat <<'EOF'
fix(dashboard): Maximizable больше не портал — состояние переживает maximize/restore
EOF
)"
```

---

### Task 2: Static per-row event duration instead of ticking relative time

**Files:**
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx`

**Interfaces:**
- Consumes: `AfmEvent` (`{ type, payload, stageId, timestamp, seq? }`) from `../../types` — unchanged.
- Produces: no external interface change — `EventFeedPanel`'s props (`{ events: AfmEvent[] }`) are unchanged; this task only changes what's rendered inside each row.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx` (inside the existing `describe('EventFeedPanel', ...)` block, alongside the existing tests):

```tsx
  test('shows a static gap-from-previous-event duration per row, not a live relative time', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00.000Z' },
      { type: 'agent_action', payload: { tool: 'read_file' }, stageId: '', timestamp: '2026-07-10T10:00:05.000Z' },
      { type: 'agent_action', payload: { tool: 'write_file' }, stageId: '', timestamp: '2026-07-10T10:01:35.000Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)
    const times = Array.from(container.querySelectorAll('.feed-time')).map((el) => el.textContent)

    // Первая строка — нет предыдущего события в ленте.
    expect(times[0]).toBe('—')
    // Вторая строка: 10:00:05 − 10:00:00 = 5s.
    expect(times[1]).toBe('5s')
    // Третья строка: 10:01:35 − 10:00:05 = 90s = 1m.
    expect(times[2]).toBe('1m')
  })

  test('does not re-render feed-time on a timer tick (no more live ticking)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-10T10:05:00.000Z') })

    const events: AfmEvent[] = [
      { type: 'agent_action', payload: {}, stageId: '', timestamp: '2026-07-10T10:00:00.000Z' },
      { type: 'agent_action', payload: {}, stageId: '', timestamp: '2026-07-10T10:00:10.000Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} />)
    const before = container.querySelector('.feed-time:last-child')?.textContent

    act(() => {
      vi.advanceTimersByTime(60_000)
    })

    const after = container.querySelector('.feed-time:last-child')?.textContent
    expect(after).toBe(before)
    expect(after).toBe('10s')

    vi.useRealTimers()
  })
```

Add the needed imports at the top of the file (extend the existing `vitest` import line and add `act`):

```tsx
import { act, render } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/event-feed/EventFeedPanel.test.tsx`
Expected: FAIL — current code renders `formatRelativeTime` ("Ns ago"-style ticking values based on `Date.now()`), not `—`/`5s`/`1m` deltas.

- [ ] **Step 3: Replace the ticking relative-time logic with a static per-row duration**

In `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`:

Replace the top of the file (imports + the tick effect + the render loop) — full replacement of lines 1–70 (everything from the imports through the closing of the `return`, i.e. up to but not including `function toFeedLine`):

```tsx
import { type ReactElement } from 'react'
import type { AfmEvent } from '../../types'
import { Maximizable } from '../layout/Maximizable'
import { PanelFrame } from '../panel-frame/PanelFrame'
import { useStickToBottom } from '../../hooks/use-stick-to-bottom'

type EventFeedPanelProps = {
  events: AfmEvent[]
}

type FeedLine = {
  msg: string
  msgClass: string
  statusClass: string
  entryClass: string
}

// Правая панель: лента событий флоу. Форматирование записей (текст и классы бейджа)
// совпадает с addFeedEntry в текущем app.js. Время у строки — статичное: разница с
// предыдущим событием в ленте («сколько заняло время между операциями»), а не
// тикающее «N секунд назад» — тикает только elapsed в футере.
export function EventFeedPanel({ events }: EventFeedPanelProps): ReactElement {
  // Автоскролл ленты к хвосту при появлении новых событий, пока пользователь
  // не уехал вверх сам. Кнопка «↓ к последнему» возвращается к актуальному.
  const feed = useStickToBottom<HTMLDivElement>()

  return (
    <Maximizable id="feed">
      <PanelFrame title="Event feed" maximizeId="feed">
        <aside id="feed-panel">
          <div id="feed-content" className="event-feed-scroll" ref={feed.ref}>
            {events.map((event, index) => {
              const ts = Date.parse(event.timestamp)
              const prevTs = index > 0 ? Date.parse(events[index - 1]?.timestamp ?? '') : NaN
              const line = toFeedLine(event)

              return (
                <div className={`feed-entry${line.entryClass !== '' ? ` ${line.entryClass}` : ''}`} data-ts={Number.isNaN(ts) ? 0 : ts} key={`${event.timestamp}-${event.type}`}>
                  <span className="feed-time">{formatEventGap(ts, prevTs)}</span>
                  <span className={line.msgClass}>
                    {event.stageId !== '' && (
                      <span className={`feed-stage-badge ${line.statusClass}`}>{event.stageId}</span>
                    )}
                    {line.msg}
                  </span>
                </div>
              )
            })}
            {!feed.stick && (
              <button type="button" className="jump-latest" onClick={feed.jumpToBottom}>
                ↓ latest
              </button>
            )}
          </div>
        </aside>
      </PanelFrame>
    </Maximizable>
  )
}
```

And replace the old `formatRelativeTime` function (near the bottom of the file) with:

```tsx
// Статичная длительность строки: разница между этим событием и предыдущим в
// ленте (не per-стадийно — по порядку отображения). Нет предыдущего (первая
// строка) или невалидный timestamp у любой из сторон — em dash.
function formatEventGap(tsMs: number, prevTsMs: number): string {
  if (Number.isNaN(tsMs) || Number.isNaN(prevTsMs)) return '—'

  const diffSec = Math.max(0, Math.floor((tsMs - prevTsMs) / 1000))

  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`

  return `${Math.floor(diffSec / 86400)}d`
}
```

(Leave `toFeedLine`, `extractStatusString`, `stringify`, and `isRecord` untouched.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/event-feed/EventFeedPanel.test.tsx`
Expected: PASS, all 5 tests (3 original + 2 new).

- [ ] **Step 5: Run the full suite and typecheck**

Run: `cd pkg/web/dashboard && npm run typecheck && npm test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx \
        pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx
git commit -m "$(cat <<'EOF'
fix(dashboard): статичная длительность строки event feed вместо тикающего «N секунд назад»
EOF
)"
```

---

### Task 3: Generic `useStatusDuration` hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-status-duration/use-status-duration.ts`
- Create: `pkg/web/dashboard/src/hooks/use-status-duration/index.ts`
- Create: `pkg/web/dashboard/src/hooks/use-status-duration/use-status-duration.test.ts`

**Interfaces:**
- Consumes: `AfmEvent` and `StageStatus` from `../../types`; `extractStageStatus` from `../../types`.
- Produces: `useStatusDuration(events: AfmEvent[], statuses: ReadonlySet<StageStatus>): number` — cumulative milliseconds (closed episodes + live delta for any currently-open episode, ticking every second). Exported from `hooks/use-status-duration` (barrel).

- [ ] **Step 1: Write the failing tests**

Create `pkg/web/dashboard/src/hooks/use-status-duration/use-status-duration.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import type { AfmEvent, StageStatus } from '../../types'
import { useStatusDuration } from './use-status-duration'

function statusEvent(stageId: string, status: StageStatus, timestamp: string): AfmEvent {
  return { type: 'stage_status_changed', payload: { status }, stageId, timestamp }
}

const TRACKED: ReadonlySet<StageStatus> = new Set(['awaiting_user_input', 'awaiting_approval', 'failed'])

describe('useStatusDuration', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  test('returns 0 when no stage ever entered a tracked status', () => {
    const events: AfmEvent[] = [statusEvent('s1', 'running', '2026-07-29T10:00:00.000Z')]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    expect(result.current).toBe(0)
  })

  test('accumulates a closed episode (entered tracked status, then left it)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:20.000Z') })
    const events: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s1', 'running', '2026-07-29T10:00:05.000Z'),
    ]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    // Эпизод закрыт (running не в TRACKED) — 5000мс, без живой добавки.
    expect(result.current).toBe(5000)
  })

  test('adds a live-ticking delta while an episode is still open', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const events: AfmEvent[] = [statusEvent('s1', 'failed', '2026-07-29T10:00:00.000Z')]
    const { result, rerender } = renderHook(() => useStatusDuration(events, TRACKED))

    expect(result.current).toBe(0)

    act(() => {
      vi.advanceTimersByTime(3000)
    })
    rerender()
    expect(result.current).toBe(3000)

    act(() => {
      vi.advanceTimersByTime(2000)
    })
    rerender()
    expect(result.current).toBe(5000)
  })

  test('sums concurrently-open episodes across different stages (no merge/dedup)', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:10.000Z') })
    const events: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s2', 'awaiting_approval', '2026-07-29T10:00:00.000Z'),
    ]
    const { result } = renderHook(() => useStatusDuration(events, TRACKED))
    // Оба открыты 10с → сумма 20000, не merge до 10000.
    expect(result.current).toBe(20000)
  })

  test('ignores non-stage_status_changed events and unparseable statuses without throwing', () => {
    const events: AfmEvent[] = [
      { type: 'agent_action', payload: { tool: 'Bash' }, stageId: 's1', timestamp: '2026-07-29T10:00:00.000Z' },
      { type: 'stage_status_changed', payload: {}, stageId: 's1', timestamp: '2026-07-29T10:00:01.000Z' },
    ]
    expect(() => renderHook(() => useStatusDuration(events, TRACKED))).not.toThrow()
  })

  test('keeps a completed episode counted even after it is trimmed out of the events array', () => {
    vi.useFakeTimers({ now: new Date('2026-07-29T10:00:00.000Z') })
    const closedEpisode: AfmEvent[] = [
      statusEvent('s1', 'awaiting_user_input', '2026-07-29T10:00:00.000Z'),
      statusEvent('s1', 'running', '2026-07-29T10:00:07.000Z'),
    ]
    const { result, rerender } = renderHook(({ events }) => useStatusDuration(events, TRACKED), {
      initialProps: { events: closedEpisode },
    })
    expect(result.current).toBe(7000)

    // Симулируем вытеснение старых событий из капа MAX_EVENTS=200: массив
    // больше не содержит closedEpisode вовсе, только новые события.
    const flushedAway: AfmEvent[] = [statusEvent('s2', 'running', '2026-07-29T10:05:00.000Z')]
    rerender({ events: flushedAway })

    // Уже учтённые 7000мс не теряются, хотя события о них исчезли из массива.
    expect(result.current).toBe(7000)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-status-duration/use-status-duration.test.ts`
Expected: FAIL with a module-not-found error (`./use-status-duration` doesn't exist yet).

- [ ] **Step 3: Implement the hook**

Create `pkg/web/dashboard/src/hooks/use-status-duration/use-status-duration.ts`:

```ts
import { useEffect, useRef, useState } from 'react'
import { extractStageStatus, type AfmEvent, type StageStatus } from '../../types'

const TICK_INTERVAL_MS = 1000

// Кумулятивное время (мс), которое хотя бы одна стадия провела в одном из
// статусов набора `statuses` — используется и для Idle (ждём пользователя:
// awaiting_user_input/awaiting_approval/failed), и для Backoff (retrying),
// см. IDLE_STATUSES/BACKOFF_STATUSES в types/stage.ts.
//
// Обрабатывает stage_status_changed ИНКРЕМЕНТАЛЬНО: каждое событие учитывается
// ровно один раз (ключ по stageId+timestamp+статусу), уже закрытые эпизоды
// остаются в accumulatedMs даже после того, как соответствующие события
// вытесняются из капа MAX_EVENTS=200 у useEventFeed — простой рескан всего
// массива при каждом изменении потерял бы старые эпизоды молча.
//
// Пока стадия открыта (её статус в `statuses`), к возвращаемому значению
// добавляется живая дельта (now − openedAt), тикающая раз в секунду — как
// useElapsed. Параллельно открытые эпизоды разных стадий суммируются, не
// мёржатся (см. дизайн-документ) — редкий кейс параллельных стадий может
// дать сумму чуть больше wall-clock elapsed, это осознанное упрощение.
export function useStatusDuration(events: AfmEvent[], statuses: ReadonlySet<StageStatus>): number {
  const processedKeys = useRef<Set<string>>(new Set())
  const openSince = useRef<Map<string, number>>(new Map())
  const [accumulatedMs, setAccumulatedMs] = useState(0)
  const [, forceTick] = useState(0)

  useEffect(() => {
    let delta = 0

    for (const event of events) {
      if (event.type !== 'stage_status_changed') continue

      const status = extractStageStatus(event.payload)
      if (status === null) continue

      const key = `${event.stageId}|${event.timestamp}|${status}`
      if (processedKeys.current.has(key)) continue
      processedKeys.current.add(key)

      const ts = Date.parse(event.timestamp)
      if (Number.isNaN(ts)) continue

      const isOpenStatus = statuses.has(status)
      const openedAt = openSince.current.get(event.stageId)

      if (isOpenStatus && openedAt === undefined) {
        openSince.current.set(event.stageId, ts)
      } else if (!isOpenStatus && openedAt !== undefined) {
        delta += ts - openedAt
        openSince.current.delete(event.stageId)
      }
    }

    if (delta !== 0) setAccumulatedMs((prev) => prev + delta)
  }, [events, statuses])

  useEffect(() => {
    const timer = setInterval(() => forceTick((t) => t + 1), TICK_INTERVAL_MS)
    return () => clearInterval(timer)
  }, [])

  let liveMs = 0
  const now = Date.now()
  for (const openedAt of openSince.current.values()) {
    liveMs += now - openedAt
  }

  return accumulatedMs + liveMs
}
```

Create `pkg/web/dashboard/src/hooks/use-status-duration/index.ts`:

```ts
export { useStatusDuration } from './use-status-duration'
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-status-duration/use-status-duration.test.ts`
Expected: PASS, all 6 tests.

- [ ] **Step 5: Typecheck and full suite**

Run: `cd pkg/web/dashboard && npm run typecheck && npm test`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-status-duration
git commit -m "$(cat <<'EOF'
feat(dashboard): хук useStatusDuration — кумулятивное время стадий в наборе статусов
EOF
)"
```

---

### Task 4: Resync `/api/events` history on WS reconnect

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts`

**Interfaces:**
- Produces: `useEventFeed(url: string): { events: AfmEvent[]; connected: boolean }` — signature unchanged; behavior gains a resync fetch on every reconnect after the first.

- [ ] **Step 1: Write the failing test**

Add to `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts` (inside the existing `describe('useEventFeed', ...)` block):

```ts
  test('re-fetches and merges /api/events after a reconnect completes (not just on initial mount)', () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]) })
    vi.stubGlobal('fetch', fetchMock)

    renderHook(() => useEventFeed('/ws'))
    expect(fetchMock).toHaveBeenCalledTimes(1)

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Первый onopen — это НЕ реконнект, лишнего фетча быть не должно.
    expect(fetchMock).toHaveBeenCalledTimes(1)

    act(() => {
      FakeWebSocket.last().emitClose()
    })
    act(() => {
      vi.advanceTimersByTime(1000) // реконнект через INITIAL_RECONNECT_DELAY_MS
    })
    expect(FakeWebSocket.instances).toHaveLength(2)

    act(() => {
      FakeWebSocket.last().emitOpen()
    })
    // Второй onopen — реконнект после close — досасывает историю ещё раз.
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-event-feed/use-event-feed.test.ts -t "re-fetches and merges"`
Expected: FAIL — `fetchMock` is still at 1 call after the reconnect's `onopen`.

- [ ] **Step 3: Extract the history fetch and call it again on reconnect**

In `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`, replace the body of the main `useEffect` (from `let cancelledFetch = false` through the `connect()` function definition) with:

```ts
  useEffect(() => {
    let cancelledFetch = false

    // Тянет /api/events и мёржит с уже накопленными live-событиями.
    // Вызывается один раз на монтировании (первичная история) и повторно на
    // каждом реконнекте после первого успешного open (см. hasConnectedBefore
    // ниже) — иначе транзишены, случившиеся, пока сокет был разорван, тихо
    // теряются: /ws не реплеит пропущенные сообщения, а без ресинка счётчики
    // Idle/Backoff (useStatusDuration) могли бы навсегда зависнуть «открытыми».
    function syncHistory() {
      fetch('/api/events')
        .then((r) => (r.ok ? r.json() : []))
        .then((raw: unknown) => {
          if (cancelledFetch || !Array.isArray(raw)) return
          const history = raw.map(toEvent)
          setEvents((prev) => mergeHistory(history, prev))
        })
        .catch(() => {
          // /api/events недоступен (старая сборка сервера, сетевая ошибка) —
          // деградируем к чистому live-потоку, как было до этой правки.
        })
    }

    syncHistory()

    let socket: WebSocket | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined
    let watchdogTimer: ReturnType<typeof setInterval> | undefined
    let reconnectDelay = INITIAL_RECONNECT_DELAY_MS
    let lastMessageAt = Date.now()
    let cancelled = false
    let hasConnectedBefore = false

    function connect() {
      socket = new WebSocket(url)
      lastMessageAt = Date.now()

      socket.onopen = () => {
        if (cancelled) return
        setConnected(true)
        reconnectDelay = INITIAL_RECONNECT_DELAY_MS

        if (hasConnectedBefore) syncHistory()
        hasConnectedBefore = true
      }
```

(Leave `socket.onclose` and `socket.onmessage` exactly as they are — only the block above, up to and including the new `socket.onopen` body, changes. The rest of the effect — watchdog setup, `connect()` call, and the cleanup return — is unchanged.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-event-feed/use-event-feed.test.ts`
Expected: PASS, all tests (existing + new).

- [ ] **Step 5: Typecheck and full suite**

Run: `cd pkg/web/dashboard && npm run typecheck && npm test`
Expected: all pass — this task doesn't touch `Footer` or `App.tsx`, so the suite stays fully green.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts \
        pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.test.ts
git commit -m "$(cat <<'EOF'
fix(dashboard): ресинк истории /api/events после реконнекта WS, не только при монтировании
EOF
)"
```

---

### Task 5: Idle/Backoff footer counters, wired end-to-end; hide "thinking" while offline

This task delivers Idle/Backoff as one unit — `Footer` changes and its `App.tsx`
wiring land in a single commit — because `Footer` requiring two new props and
`App.tsx` supplying them are two halves of one change: splitting them into
separate commits would leave the repo failing `typecheck`/`test` in between,
which the Global Constraints above forbid ("no exceptions").

**Files:**
- Modify: `pkg/web/dashboard/src/components/footer/Footer.tsx`
- Modify: `pkg/web/dashboard/src/components/footer/Footer.test.tsx`
- Modify: `pkg/web/dashboard/skins/base/layout.css`
- Modify: `pkg/web/dashboard/public/skins/base/layout.css`
- Modify: `pkg/web/dashboard/src/types/stage.ts`
- Modify: `pkg/web/dashboard/src/types/index.ts`
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Modify: `pkg/web/dashboard/src/app/App.test.tsx`

**Interfaces:**
- Consumes: `useStatusDuration` from `../hooks/use-status-duration` (Task 3); `connected` from `useEventFeed` (already threaded through `App.tsx`).
- Produces: `Footer` now requires two additional props: `idleMs: number`, `backoffMs: number` — its only caller, `App.tsx`, is updated in the same task (Step 8 below), so the repo is never left with a caller mismatch. `IDLE_STATUSES: ReadonlySet<StageStatus>` and `BACKOFF_STATUSES: ReadonlySet<StageStatus>` are exported from `types/stage.ts` (re-exported via `types/index.ts`) — the single source of truth for which statuses count as "waiting on the user" vs "automatic retry backoff".

- [ ] **Step 1: Write the failing tests**

`Footer` will require two new props (`idleMs`, `backoffMs`) once Step 3 lands, so every existing test's `render(<Footer .../>)` call needs them too, or the file won't compile. Replace the full contents of `pkg/web/dashboard/src/components/footer/Footer.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, test } from 'vitest'
import type { Stage } from '../../types'
import { Footer } from './Footer'

describe('Footer', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-theme')
    window.localStorage.clear()
  })

  test('renders done/total progress and formatted elapsed', () => {
    const stages: Stage[] = [
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
    ]

    render(
      <Footer stages={stages} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} idleMs={0} backoffMs={0} />,
    )

    expect(screen.getByText('1 / 2')).toBeInTheDocument()
    expect(screen.getByText('01:05')).toBeInTheDocument()
  })

  test('shows placeholder elapsed and started-at when startedAt is empty', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)

    // При пустом startedAt все поля (started-at, elapsed, idle, backoff) показывают плейсхолдер '--'.
    expect(document.getElementById('elapsed')).toHaveTextContent('--')
    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('shows placeholder started-at when startedAt is not a valid date', () => {
    render(<Footer stages={[]} startedAt="not-a-date" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('started-at')).toHaveTextContent('--')
  })

  test('formats elapsed with hours when duration is 1 hour or more', () => {
    render(
      <Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={3665000} idleMs={0} backoffMs={0} />,
    )

    expect(document.getElementById('elapsed')).toHaveTextContent('1:01:05')
  })

  test('shows 0 / 0 progress without dividing by zero when stages is empty', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(screen.getByText('0 / 0')).toBeInTheDocument()
    expect(document.getElementById('progress-fill')).toHaveStyle({ width: '0%' })
  })

  test('renders Idle and Backoff next to Elapsed', () => {
    render(
      <Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={65000} idleMs={5000} backoffMs={125000} />,
    )

    expect(document.getElementById('idle')).toHaveTextContent('00:05')
    expect(document.getElementById('backoff')).toHaveTextContent('02:05')
  })

  test('shows 0:00 for Idle/Backoff once the run has started, even when both are zero', () => {
    render(<Footer stages={[]} startedAt="2026-07-10T10:00:00Z" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('idle')).toHaveTextContent('00:00')
    expect(document.getElementById('backoff')).toHaveTextContent('00:00')
  })

  test('shows placeholder Idle/Backoff when startedAt is empty (run has not started)', () => {
    render(<Footer stages={[]} startedAt="" elapsedMs={0} idleMs={0} backoffMs={0} />)

    expect(document.getElementById('idle')).toHaveTextContent('--')
    expect(document.getElementById('backoff')).toHaveTextContent('--')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/footer/Footer.test.tsx`
Expected: FAIL to compile (TypeScript: `idleMs`/`backoffMs` missing) or, once props are stubbed, FAIL because the elements don't exist yet.

- [ ] **Step 3: Add the two footer items**

Replace the full contents of `pkg/web/dashboard/src/components/footer/Footer.tsx`:

```tsx
import type { ReactElement } from 'react'
import type { Stage } from '../../types'

type FooterProps = {
  stages: Stage[]
  startedAt: string
  elapsedMs: number
  // Idle — суммарное время, когда хоть одна стадия ждала пользователя
  // (awaiting_user_input/awaiting_approval/failed). Backoff — суммарное
  // время автоматического retry-backoff (retrying), без участия
  // пользователя. Оба — из useStatusDuration (см. app/App.tsx).
  idleMs: number
  backoffMs: number
}

// Футер: прогресс (доля done), время старта, elapsed/idle/backoff. Все три
// счётчика приходят уже готовыми (тик — забота вызывающих хуков). id
// progress-fill/progress-text/started-at/elapsed/idle/backoff сохранены для тем.
export function Footer({ stages, startedAt, elapsedMs, idleMs, backoffMs }: FooterProps): ReactElement {
  const total = stages.length
  const done = stages.filter((stage) => stage.status === 'done').length
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const hasStarted = startedAt !== ''

  return (
    <footer id="footer">
      <div className="footer-item">
        <span className="footer-label">Progress:</span>
        <div className="progress-bar">
          <div id="progress-fill" className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <span id="progress-text">{`${done} / ${total}`}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Started:</span>
        <span id="started-at">{formatClock(startedAt)}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Elapsed:</span>
        <span id="elapsed">{hasStarted ? formatDuration(elapsedMs) : '--'}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Idle:</span>
        <span id="idle">{hasStarted ? formatDuration(idleMs) : '--'}</span>
      </div>
      <div className="footer-item">
        <span className="footer-label">Backoff:</span>
        <span id="backoff">{hasStarted ? formatDuration(backoffMs) : '--'}</span>
      </div>
    </footer>
  )
}

function formatClock(startedAt: string): string {
  if (startedAt === '') return '--'

  const parsed = new Date(startedAt)
  if (Number.isNaN(parsed.getTime())) return '--'

  return formatTime(parsed)
}

function formatTime(date: Date): string {
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map(pad).join(':')
}

function formatDuration(ms: number): string {
  const sec = Math.floor(ms / 1000)
  const hours = Math.floor(sec / 3600)
  const minutes = Math.floor((sec % 3600) / 60)
  const seconds = sec % 60

  if (hours > 0) return `${hours}:${pad(minutes)}:${pad(seconds)}`

  return `${pad(minutes)}:${pad(seconds)}`
}

function pad(value: number): string {
  return value < 10 ? `0${value}` : String(value)
}
```

Note: `elapsedMs` formatting behavior changes very slightly for the empty-`startedAt` case — previously it read `startedAt === '' ? '--' : formatDuration(elapsedMs)`; the rewrite above (`hasStarted ? formatDuration(elapsedMs) : '--'`) is exactly equivalent, just reusing the new `hasStarted` local for all three ticking counters.

- [ ] **Step 4: Widen the footer CSS grid to fit 5 items**

In both `pkg/web/dashboard/skins/base/layout.css` and `pkg/web/dashboard/public/skins/base/layout.css`, find:

```css
#footer {
  display: grid;
  grid-template-columns: 1fr auto auto auto;
```

Replace with:

```css
#footer {
  display: grid;
  grid-template-columns: 1fr auto auto auto auto;
```

(Progress keeps the flexible `1fr` track; Started/Elapsed/Idle/Backoff each get an `auto` track — 5 tracks total for 5 `.footer-item`s.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/components/footer/Footer.test.tsx`
Expected: PASS, all tests (existing + new). (Note: `App.tsx` — the only caller of `<Footer>` — hasn't been updated yet at this point in the task; that's fine, this scoped `vitest run` doesn't type-check `App.tsx`. It gets wired in Step 9 below, before the task's one full-suite/typecheck run in Step 11.)

- [ ] **Step 6: Write the failing App.tsx tests (thinking/offline + Idle accumulation)**

Add to `pkg/web/dashboard/src/app/App.test.tsx` (inside the existing `describe('App', ...)` block):

```tsx
  test('hides the "thinking" badge while offline even if the selected stage is running', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Propose' },
      stages: { s1: { status: 'running', updated_at: '' } },
    }))

    render(<App />)

    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    // WS никогда не открывался в этом тесте — connected остаётся false.
    expect(screen.getByText('OFFLINE')).toBeInTheDocument()
    expect(screen.queryByText('thinking')).not.toBeInTheDocument()

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
    })

    await waitFor(() => expect(screen.getByText('LINK')).toBeInTheDocument())
    expect(screen.getByText('thinking')).toBeInTheDocument()
  })

  test('accumulates Idle time across an awaiting_user_input episode and shows it in the footer', async () => {
    mockFetchForStatus(() => ({
      flow_name: 'demo',
      stage_order: ['s1'],
      stage_names: { s1: 'Propose' },
      stages: { s1: { status: 'running', updated_at: '' } },
    }))

    render(<App />)
    await waitFor(() => expect(document.getElementById('detail-title')).toHaveTextContent('Propose'))

    expect(document.getElementById('idle')).toHaveTextContent('--')

    const ws = StubWebSocket.instances[StubWebSocket.instances.length - 1]
    act(() => {
      ws?.onopen?.()
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'stage_status_changed',
          data: { status: 'awaiting_user_input' },
          stage_id: 's1',
          timestamp: '2026-07-29T10:00:00.000Z',
        }),
      })
    })
    act(() => {
      ws?.onmessage?.({
        data: JSON.stringify({
          type: 'stage_status_changed',
          data: { status: 'running' },
          stage_id: 's1',
          timestamp: '2026-07-29T10:00:05.000Z',
        }),
      })
    })

    await waitFor(() => {
      expect(document.getElementById('idle')).toHaveTextContent('00:05')
    })
  })
```

Note: `useStatus`'s `normalizeStatus` (`hooks/use-status/use-status.ts:87`) reads `startedAt` from `obj.started_at`, defaulting to `''` when absent. The mock payload above has no `started_at` field, so `startedAt` stays `''` for the whole test (matching the existing "renders the flow name..." test's payload shape) — `#idle` reads the `'--'` placeholder branch first, then the WS-driven accumulation is asserted from its text changing to `'00:05'`.

- [ ] **Step 7: Run tests to verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/app/App.test.tsx -t "thinking|Idle"`
Expected: FAIL — the "thinking" badge shows regardless of `connected` (no guard yet), and `App.tsx` doesn't compute `idleMs` at all yet (it's not wired below yet), so `#idle` never gets a live value from WS events in this test.

- [ ] **Step 8: Add `IDLE_STATUSES`/`BACKOFF_STATUSES` to `types/stage.ts`**

In `pkg/web/dashboard/src/types/stage.ts`, add after the existing `ACTIVE_STAGE_STATUSES` block:

```ts
// Idle — стадия ждёт действия ПОЛЬЗОВАТЕЛЯ: открытый диалог, ожидание
// одобрения плана, или упавшая стадия (ждёт ручного retry). Backoff —
// автоматическая пауза перед авто-ретраем, без участия пользователя.
// Оба набора питают useStatusDuration в app/App.tsx (см. дизайн-документ
// docs/superpowers/specs/2026-07-29-dashboard-event-feed-ui-fixes-design.md).
export const IDLE_STATUSES: ReadonlySet<StageStatus> = new Set(['awaiting_user_input', 'awaiting_approval', 'failed'])
export const BACKOFF_STATUSES: ReadonlySet<StageStatus> = new Set(['retrying'])
```

In `pkg/web/dashboard/src/types/index.ts`, extend the existing status-constants export line:

```ts
export {
  STAGE_STATUSES,
  STAGE_STATUS_LABELS,
  ACTIVE_STAGE_STATUSES,
  IDLE_STATUSES,
  BACKOFF_STATUSES,
} from './stage'
```

- [ ] **Step 9: Wire the hooks and the `connected` guard into `App.tsx`**

In `pkg/web/dashboard/src/app/App.tsx`:

Add to the imports (alongside the existing hook imports):

```tsx
import { useStatusDuration } from '../hooks/use-status-duration'
```

And extend the existing type-only import line:

```tsx
import { ACTIVE_STAGE_STATUSES, BACKOFF_STATUSES, IDLE_STATUSES, SIGNIFICANT_EVENT_TYPES, STAGE_STATUS_LABELS } from '../types'
```

Add the two hook calls right after the existing `const elapsedMs = useElapsed(startedAt)` line:

```tsx
  const idleMs = useStatusDuration(events, IDLE_STATUSES)
  const backoffMs = useStatusDuration(events, BACKOFF_STATUSES)
```

Update the `Footer` call:

```tsx
      <Footer stages={stages} startedAt={startedAt} elapsedMs={elapsedMs} idleMs={idleMs} backoffMs={backoffMs} />
```

Update the "thinking" badge condition:

```tsx
                    {selectedStage.status === 'running' && connected && (
                      <span className="thinking" aria-hidden="true">
```

(Only the condition on this line changes — everything inside the `<span className="thinking">` block stays the same.)

- [ ] **Step 10: Run tests to verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/app/App.test.tsx`
Expected: PASS, all tests (existing + the two new ones).

- [ ] **Step 11: Full suite, typecheck — everything green**

Run: `cd pkg/web/dashboard && npm run typecheck && npm test`
Expected: all pass, no remaining failures anywhere. This is the task's one and only full-suite/typecheck checkpoint — it covers the Footer change (Steps 1–5) and the App.tsx wiring (Steps 6–10) together, so the repo goes from green to green in one step, never dipping into a red intermediate state.

- [ ] **Step 12: Manual smoke check (per this project's UI-change convention)**

Run: `cd pkg/web/dashboard && npm run build` (from repo root: `make build` also works and additionally builds the Go binary).
Then start a real flow with `afm run <some-flow.yaml>` (or reuse any flow already used for manual testing in this repo) and open the dashboard in a browser:
- Confirm the footer now shows Progress / Started / Elapsed / Idle / Backoff in one row without visual overflow or wrapping.
- Toggle the event feed panel's maximize (⛶) button back and forth a few times while scrolled up in the feed; confirm the "↓ latest" button's visibility and the auto-scroll behavior survive the toggle (this is the best available proxy for the original, not-fully-reproduced bug from the design doc).
- Trigger (or simulate via devtools' Network → Offline) a WebSocket disconnect while a stage is `running`; confirm the "thinking" indicator disappears and the header badge shows `OFFLINE`; reconnect and confirm "thinking" reappears.

- [ ] **Step 13: Commit**

```bash
git add pkg/web/dashboard/src/components/footer/Footer.tsx \
        pkg/web/dashboard/src/components/footer/Footer.test.tsx \
        pkg/web/dashboard/skins/base/layout.css \
        pkg/web/dashboard/public/skins/base/layout.css \
        pkg/web/dashboard/src/types/stage.ts \
        pkg/web/dashboard/src/types/index.ts \
        pkg/web/dashboard/src/app/App.tsx \
        pkg/web/dashboard/src/app/App.test.tsx
git commit -m "$(cat <<'EOF'
feat(dashboard): счётчики Idle/Backoff в футере через useStatusDuration; thinking скрывается offline
EOF
)"
```

---

## Self-Review

**1. Spec coverage:**
- #1 (sticky scroll loses track) → Task 1 (Maximizable always-portal).
- #5 (jump button disappears) → Task 1 (same root cause fix); Step 12 of Task 5 adds a manual check since the exact repro was never confirmed.
- #2 (ticking per-row time → static duration) → Task 2.
- #3 (Idle + Backoff footer counters, incremental accumulation, reconnect resync) → Tasks 3, 4, 5.
- #4 (thinking hidden offline) → Task 5, Step 9.
- All "known limitations" from the design doc (200-event cap, reconnect gap) are addressed by Task 4 or explicitly documented in code comments (Task 3's hook docstring) rather than silently ignored.

**2. Placeholder scan:** No TBD/TODO markers; every step has literal code, not descriptions. The one caveat note in Task 5 Step 6 (about `started_at` shape) is a real, actionable fallback instruction, not a placeholder — it names the exact condition and the exact fix.

**3. Type consistency:** `useStatusDuration(events: AfmEvent[], statuses: ReadonlySet<StageStatus>): number` is defined once in Task 3 and used identically in Task 5 (`useStatusDuration(events, IDLE_STATUSES)` / `useStatusDuration(events, BACKOFF_STATUSES)`). `Footer`'s prop list (`stages`, `startedAt`, `elapsedMs`, `idleMs`, `backoffMs`) is defined and consumed within the same Task 5. `formatEventGap(tsMs, prevTsMs)` naming is consistent within Task 2 (only used there). Footer element ids (`idle`, `backoff`) match between Task 5's implementation and its own tests.

**4. Task-boundary conflict re-check (post pre-flight-scan fix):** Tasks 4 and 5 were reordered/merged from the original 6-task draft specifically so no task ends with `npm run typecheck`/`npm test` red — Task 4 (reconnect resync) is fully self-contained and green at its end; Task 5 now carries both the `Footer` prop change and its `App.tsx` caller update to a single commit, with its one full-suite checkpoint (Step 11) placed after both halves land. No task below Task 5 depends on an intermediate broken state.
