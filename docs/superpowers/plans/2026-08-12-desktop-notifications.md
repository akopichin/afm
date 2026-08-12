# Desktop Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fire a browser desktop notification when a dashboard stage transitions into (or is already sitting in) `awaiting_approval` / `awaiting_user_input` / `failed` / `hook_failed` while the tab is hidden, titled `Need Approve` / `You have a question` / `Stage failed`; clicking it focuses the tab, selects that stage, and closes the notification.

**Architecture:** Extend the existing `use-attention` selector pipeline to include failure statuses and to expose *which* stages need attention (not just a boolean), then add one new hook (`useDesktopNotifications`) that reconciles that list against an "already notified" set on every stages update and on `visibilitychange`, and a header toggle button that gates the whole thing behind an explicit, user-gesture-driven permission request.

**Tech Stack:** React + TypeScript (`pkg/web/dashboard/src`), Vitest + `@testing-library/react` for tests, the browser `Notification` API (no new npm dependency).

## Global Constraints

- No new npm dependency — the `Notification` API is a browser global, no package needed.
- No per-notification-kind opt-out — one on/off toggle for all three kinds.
- No question/error text preview in the notification body (deferred — would need an extra request per transition).
- No cross-tab deduplication — each tab keeps its own in-memory notified-state, same accepted limitation as the existing favicon-pulse feature.
- Extending `ATTENTION_STATUSES` to include `failed`/`hook_failed` is an intentional, in-scope behavior change to the already-shipped favicon-pulse/title-flash features too — not a bug to avoid.
- Spec: `docs/superpowers/specs/2026-08-12-desktop-notifications-design.md`.

---

### Task 1: Extend `use-attention` with failure statuses and a per-stage selector

**Files:**
- Modify: `pkg/web/dashboard/src/hooks/use-attention/use-attention.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-attention/index.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-attention/use-attention.test.ts`

**Interfaces:**
- Produces (consumed by Task 2): `AttentionKind = 'dialog' | 'plan' | 'failed'`, `AttentionEntry = { stage: Stage; kind: AttentionKind }`, `stagesNeedingAttention(stages: Stage[]): AttentionEntry[]`.
- `ATTENTION_STATUSES` now includes `'failed'` and `'hook_failed'` alongside the existing `'awaiting_user_input'`/`'awaiting_approval'` — `anyAwaiting` and `useAttention` (already consumed by `App.tsx`/`use-title-flash`/`use-favicon-pulse`) pick this up automatically since they read the same set/logic.

This task has no dependency on any other and can be verified in complete isolation.

- [ ] **Step 1: Write the failing tests**

Replace the full contents of `pkg/web/dashboard/src/hooks/use-attention/use-attention.test.ts` with:

```ts
import { describe, it, expect } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAttention, anyAwaiting, stagesNeedingAttention } from './use-attention'
import type { Stage } from '../../types'

const stage = (status: Stage['status'], id = 's'): Stage =>
  ({ id, name: 'n', status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false })

describe('useAttention', () => {
  it('dialog для awaiting_user_input, plan для awaiting_approval, failed для failed/hook_failed, null иначе', () => {
    expect(renderHook(() => useAttention(stage('awaiting_user_input'))).result.current).toEqual({ needsAttention: true, kind: 'dialog' })
    expect(renderHook(() => useAttention(stage('awaiting_approval'))).result.current).toEqual({ needsAttention: true, kind: 'plan' })
    expect(renderHook(() => useAttention(stage('failed'))).result.current).toEqual({ needsAttention: true, kind: 'failed' })
    expect(renderHook(() => useAttention(stage('hook_failed'))).result.current).toEqual({ needsAttention: true, kind: 'failed' })
    expect(renderHook(() => useAttention(stage('running'))).result.current).toEqual({ needsAttention: false, kind: null })
    expect(renderHook(() => useAttention(null)).result.current).toEqual({ needsAttention: false, kind: null })
  })
  it('anyAwaiting ищет по массиву стадий, включая failed/hook_failed', () => {
    expect(anyAwaiting([stage('running'), stage('awaiting_user_input')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('failed')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('hook_failed')])).toBe(true)
    expect(anyAwaiting([stage('running'), stage('done')])).toBe(false)
  })
})

describe('stagesNeedingAttention', () => {
  it('возвращает только стадии из ATTENTION_STATUSES с их kind, в исходном порядке', () => {
    const running = stage('running', 'a')
    const question = stage('awaiting_user_input', 'b')
    const plan = stage('awaiting_approval', 'c')
    const failed = stage('failed', 'd')
    const hookFailed = stage('hook_failed', 'e')
    const done = stage('done', 'f')

    expect(stagesNeedingAttention([running, question, plan, failed, hookFailed, done])).toEqual([
      { stage: question, kind: 'dialog' },
      { stage: plan, kind: 'plan' },
      { stage: failed, kind: 'failed' },
      { stage: hookFailed, kind: 'failed' },
    ])
  })

  it('пустой массив, если ни одна стадия не требует внимания', () => {
    expect(stagesNeedingAttention([stage('running'), stage('done')])).toEqual([])
  })
})
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-attention -t stagesNeedingAttention`
Expected: FAIL — `stagesNeedingAttention` doesn't exist yet (import error), and the `failed`/`hook_failed` cases in the `useAttention`/`anyAwaiting` tests return `null`/`false` instead of the expected values.

- [ ] **Step 3: Implement**

Replace the full contents of `pkg/web/dashboard/src/hooks/use-attention/use-attention.ts` with:

```ts
import { useMemo } from 'react'
import type { Stage, StageStatus } from '../../types'

export type AttentionKind = 'dialog' | 'plan' | 'failed'
export type Attention = { needsAttention: boolean; kind: AttentionKind | null }
export type AttentionEntry = { stage: Stage; kind: AttentionKind }

export const ATTENTION_STATUSES: ReadonlySet<StageStatus> = new Set<StageStatus>([
  'awaiting_user_input',
  'awaiting_approval',
  'failed',
  'hook_failed',
])

function attentionKindFor(status: StageStatus): AttentionKind | null {
  if (status === 'awaiting_user_input') return 'dialog'
  if (status === 'awaiting_approval') return 'plan'
  if (status === 'failed' || status === 'hook_failed') return 'failed'
  return null
}

export function anyAwaiting(stages: Stage[]): boolean {
  return stages.some((s) => ATTENTION_STATUSES.has(s.status))
}

export function stagesNeedingAttention(stages: Stage[]): AttentionEntry[] {
  return stages.reduce<AttentionEntry[]>((acc, stage) => {
    const kind = attentionKindFor(stage.status)
    if (kind !== null) acc.push({ stage, kind })
    return acc
  }, [])
}

export function useAttention(stage: Stage | null): Attention {
  return useMemo(() => {
    if (stage === null) return { needsAttention: false, kind: null }
    const kind = attentionKindFor(stage.status)
    return { needsAttention: kind !== null, kind }
  }, [stage])
}
```

Replace the full contents of `pkg/web/dashboard/src/hooks/use-attention/index.ts` with:

```ts
export { useAttention, anyAwaiting, stagesNeedingAttention, ATTENTION_STATUSES } from './use-attention'
export type { Attention, AttentionKind, AttentionEntry } from './use-attention'
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-attention`
Expected: PASS — all tests in the file.

- [ ] **Step 5: Run the full frontend suite to check for regressions, then lint and commit**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS. In particular `use-favicon-pulse.test.ts` and `use-title-flash.test.ts` (if it exists) must be unaffected — they consume `anyAwaiting`/`useAttention`'s *shape*, not the specific status set, so widening `ATTENTION_STATUSES` doesn't change their own test fixtures' outcomes.

```bash
cd /Users/alexander.kopichin/work/personal/afm
make lint
git add pkg/web/dashboard/src/hooks/use-attention/use-attention.ts pkg/web/dashboard/src/hooks/use-attention/index.ts pkg/web/dashboard/src/hooks/use-attention/use-attention.test.ts
git commit -m "$(cat <<'EOF'
feat(dashboard): failed/hook_failed — тоже attention, плюс stagesNeedingAttention

ATTENTION_STATUSES теперь включает failed/hook_failed — задевает уже
существующие favicon-pulse/title-flash (осознанно, по дизайну). Новый
stagesNeedingAttention(stages) отдаёт список (stage, kind) вместо
булева anyAwaiting — нужен для десктоп-уведомлений по конкретной стадии.
EOF
)"
```

---

### Task 2: `useDesktopNotifications` hook

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-desktop-notifications/use-desktop-notifications.ts`
- Create: `pkg/web/dashboard/src/hooks/use-desktop-notifications/index.ts`
- Create: `pkg/web/dashboard/src/hooks/use-desktop-notifications/use-desktop-notifications.test.ts`

**Interfaces:**
- Consumes: `stagesNeedingAttention`, `AttentionEntry`, `AttentionKind` from Task 1 (`../use-attention`); `Stage` from `../../types`.
- Produces (consumed by Task 3): `useDesktopNotifications(stages: Stage[], onFocusStage: (stageId: string) => void): { enabled: boolean; permission: NotificationPermissionState; requestEnable: () => void; disable: () => void }`, and the exported type `NotificationPermissionState = 'unsupported' | 'default' | 'granted' | 'denied'`.

This task depends on Task 1 only for the `stagesNeedingAttention` import to exist.

- [ ] **Step 1: Write the failing tests**

Create `pkg/web/dashboard/src/hooks/use-desktop-notifications/use-desktop-notifications.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useDesktopNotifications } from './use-desktop-notifications'
import type { Stage } from '../../types'

const stage = (status: Stage['status'], id = 's'): Stage =>
  ({ id, name: `Stage ${id}`, status, updatedAt: '', interactive: false, autonomous: false, autoApprove: false, hasDialog: false, showPlan: true, showDialog: false })

class MockNotification {
  static permission: NotificationPermission = 'granted'
  static requestPermission = vi.fn<[], Promise<NotificationPermission>>()
  static instances: MockNotification[] = []
  onclick: (() => void) | null = null
  close = vi.fn()
  title: string
  options?: NotificationOptions
  constructor(title: string, options?: NotificationOptions) {
    this.title = title
    this.options = options
    MockNotification.instances.push(this)
  }
}

function setIconLink(href: string): void {
  document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  const link = document.createElement('link')
  link.rel = 'icon'
  link.href = href
  document.head.appendChild(link)
}

describe('useDesktopNotifications', () => {
  beforeEach(() => {
    MockNotification.instances = []
    MockNotification.permission = 'granted'
    MockNotification.requestPermission = vi.fn().mockResolvedValue('granted')
    vi.stubGlobal('Notification', MockNotification)
    window.localStorage.setItem('afm-notifications-enabled', '1')
    setIconLink('/favicon.svg')
    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.clear()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  })

  it('уведомляет один раз на новый переход в attention, пока вкладка скрыта, и не дублирует на повторном рендере', () => {
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    expect(MockNotification.instances).toHaveLength(0)

    rerender({ stages: [stage('awaiting_approval')] })
    expect(MockNotification.instances).toHaveLength(1)
    expect(MockNotification.instances[0].title).toBe('Need Approve')

    rerender({ stages: [stage('awaiting_approval')] })
    expect(MockNotification.instances).toHaveLength(1)
  })

  it('не уведомляет, пока вкладка видима — но уведомляет при уходе со вкладки, если стадия всё ещё ждёt', () => {
    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })

    rerender({ stages: [stage('awaiting_user_input')] })
    expect(MockNotification.instances).toHaveLength(0)

    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))
    expect(MockNotification.instances).toHaveLength(1)
    expect(MockNotification.instances[0].title).toBe('You have a question')
  })

  it('уведомляет заново, если стадия вышла из attention и зашла снова', () => {
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('failed')] },
    })
    expect(MockNotification.instances).toHaveLength(1)

    rerender({ stages: [stage('retrying')] })
    rerender({ stages: [stage('failed')] })
    expect(MockNotification.instances).toHaveLength(2)
  })

  it('не уведомляет, если enabled=false (флаг не выставлен в localStorage)', () => {
    window.localStorage.removeItem('afm-notifications-enabled')
    const onFocusStage = vi.fn()
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    rerender({ stages: [stage('failed')] })
    expect(MockNotification.instances).toHaveLength(0)
  })

  it('requestEnable() при granted включает enabled и сохраняет в localStorage', async () => {
    window.localStorage.removeItem('afm-notifications-enabled')
    MockNotification.permission = 'default'
    const { result } = renderHook(() => useDesktopNotifications([stage('running')], vi.fn()))
    expect(result.current.enabled).toBe(false)

    await act(async () => {
      result.current.requestEnable()
      await Promise.resolve()
    })

    expect(result.current.enabled).toBe(true)
    expect(result.current.permission).toBe('granted')
    expect(window.localStorage.getItem('afm-notifications-enabled')).toBe('1')
  })

  it('клик по нотификации фокусирует окно, выбирает стадию и закрывает нотификацию', () => {
    const onFocusStage = vi.fn()
    const focusSpy = vi.spyOn(window, 'focus').mockImplementation(() => {})
    const { rerender } = renderHook(({ stages }) => useDesktopNotifications(stages, onFocusStage), {
      initialProps: { stages: [stage('running')] },
    })
    rerender({ stages: [stage('awaiting_approval', 'xyz')] })

    const n = MockNotification.instances[0]
    n.onclick?.()

    expect(focusSpy).toHaveBeenCalled()
    expect(onFocusStage).toHaveBeenCalledWith('xyz')
    expect(n.close).toHaveBeenCalled()
    focusSpy.mockRestore()
  })
})
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-desktop-notifications`
Expected: FAIL — the module doesn't exist yet.

- [ ] **Step 3: Implement**

Create `pkg/web/dashboard/src/hooks/use-desktop-notifications/use-desktop-notifications.ts`:

```ts
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Stage } from '../../types'
import { stagesNeedingAttention, type AttentionEntry, type AttentionKind } from '../use-attention'

export type NotificationPermissionState = 'unsupported' | 'default' | 'granted' | 'denied'

const STORAGE_KEY = 'afm-notifications-enabled'

const TITLES: Record<AttentionKind, string> = {
  plan: 'Need Approve',
  dialog: 'You have a question',
  failed: 'Stage failed',
}

function readInitialPermission(): NotificationPermissionState {
  return 'Notification' in window ? Notification.permission : 'unsupported'
}

function readInitialEnabled(): boolean {
  return window.localStorage.getItem(STORAGE_KEY) === '1'
}

function fireNotification(entry: AttentionEntry, onFocusStage: (stageId: string) => void): void {
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href
  const time = new Date().toLocaleTimeString()
  let n: Notification
  try {
    n = new Notification(TITLES[entry.kind], {
      body: `${entry.stage.name} — click to view\n${time}`,
      icon,
      tag: `afm-stage-${entry.stage.id}`,
    })
  } catch {
    // best-effort — некоторые окружения кидают синхронно (например нет
    // разрешения на ОС-уровне); не роняем остальной UI из-за нотификации.
    return
  }
  n.onclick = () => {
    window.focus()
    onFocusStage(entry.stage.id)
    n.close()
  }
}

// Десктоп-уведомления о стадиях, которым нужно действие (approve/question/fail),
// пока вкладка дашборда не активна. Один reconciliation-проход (reconcile)
// покрывает и "новый переход в attention, пока вкладка уже скрыта", и "стадия
// уже ждала, когда пользователь ушёл со вкладки" — оба случая вызывают одну и
// ту же функцию, разница только в том, что её триггерит (обновление stages или
// visibilitychange). Стадии, вышедшие из attention, забываются из
// notifiedStageIds, чтобы повторный заход (упала → retried → упала снова)
// уведомил заново.
export function useDesktopNotifications(
  stages: Stage[],
  onFocusStage: (stageId: string) => void,
): {
  enabled: boolean
  permission: NotificationPermissionState
  requestEnable: () => void
  disable: () => void
} {
  const [permission, setPermission] = useState<NotificationPermissionState>(readInitialPermission)
  const [enabled, setEnabled] = useState<boolean>(readInitialEnabled)
  const notifiedStageIds = useRef<Set<string>>(new Set())
  const stagesRef = useRef<Stage[]>(stages)
  const enabledRef = useRef<boolean>(enabled)
  const onFocusStageRef = useRef(onFocusStage)

  useEffect(() => {
    stagesRef.current = stages
  }, [stages])
  useEffect(() => {
    enabledRef.current = enabled
  }, [enabled])
  useEffect(() => {
    onFocusStageRef.current = onFocusStage
  }, [onFocusStage])

  const reconcile = useCallback((currentStages: Stage[]) => {
    const current = stagesNeedingAttention(currentStages)
    const currentIds = new Set(current.map((e) => e.stage.id))

    for (const id of notifiedStageIds.current) {
      if (!currentIds.has(id)) notifiedStageIds.current.delete(id)
    }

    if (!enabledRef.current || !document.hidden) return
    for (const entry of current) {
      if (notifiedStageIds.current.has(entry.stage.id)) continue
      notifiedStageIds.current.add(entry.stage.id)
      fireNotification(entry, onFocusStageRef.current)
    }
  }, [])

  useEffect(() => {
    reconcile(stages)
  }, [stages, enabled, reconcile])

  useEffect(() => {
    const onVisibility = () => {
      if (document.hidden) reconcile(stagesRef.current)
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [reconcile])

  const requestEnable = useCallback(() => {
    if (!('Notification' in window)) return
    void Notification.requestPermission().then((result) => {
      setPermission(result)
      if (result === 'granted') {
        setEnabled(true)
        window.localStorage.setItem(STORAGE_KEY, '1')
      }
    })
  }, [])

  const disable = useCallback(() => {
    setEnabled(false)
    window.localStorage.removeItem(STORAGE_KEY)
  }, [])

  return { enabled, permission, requestEnable, disable }
}
```

Create `pkg/web/dashboard/src/hooks/use-desktop-notifications/index.ts`:

```ts
export { useDesktopNotifications } from './use-desktop-notifications'
export type { NotificationPermissionState } from './use-desktop-notifications'
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-desktop-notifications`
Expected: PASS — all 7 tests.

- [ ] **Step 5: Run the full frontend suite, lint, commit**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS, no regressions elsewhere.

```bash
cd /Users/alexander.kopichin/work/personal/afm
make lint
git add pkg/web/dashboard/src/hooks/use-desktop-notifications
git commit -m "$(cat <<'EOF'
feat(dashboard): useDesktopNotifications — браузерные уведомления о стадиях

Один reconciliation-проход на обновление stages и на visibilitychange:
новый переход в attention, пока вкладка скрыта, — уведомляет; переход
пока видима — не уведомляет, но ловит уход со вкладки тем же проходом.
Иконка нотификации — живой href link[rel="icon"], без хардкода темы.
EOF
)"
```

---

### Task 3: Header toggle + `App.tsx` wiring

**Files:**
- Modify: `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx`
- Modify: `pkg/web/dashboard/src/components/flow-header/FlowHeader.test.tsx`
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Modify: `pkg/web/dashboard/skins/base/layout.css`

**Interfaces:**
- Consumes: `useDesktopNotifications`, `NotificationPermissionState` from Task 2 (`../../hooks/use-desktop-notifications`).
- No new interfaces produced — this is the last piece wiring the feature into the UI.

This task depends on Task 2 for the hook to exist.

- [ ] **Step 1: Write the failing tests**

In `pkg/web/dashboard/src/components/flow-header/FlowHeader.test.tsx`, change the import line from:

```ts
import { beforeEach, describe, expect, test } from 'vitest'
```

to:

```ts
import { beforeEach, describe, expect, test, vi } from 'vitest'
```

Then add these four tests at the end of the `describe('FlowHeader', ...)` block, right before its closing `})`:

```ts
  test('hides notifications button when unsupported', () => {
    render(<FlowHeader flowName="demo" connected={true} notificationsPermission="unsupported" />)
    expect(screen.queryByLabelText(/notifications/i)).not.toBeInTheDocument()
  })

  test('shows disabled notifications button with tooltip when denied', () => {
    render(<FlowHeader flowName="demo" connected={true} notificationsPermission="denied" />)
    const button = screen.getByLabelText('Enable desktop notifications')
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'Notifications blocked in browser settings')
  })

  test('clicking the off notifications button calls onRequestEnableNotifications', () => {
    const onRequestEnable = vi.fn()
    render(
      <FlowHeader
        flowName="demo"
        connected={true}
        notificationsPermission="default"
        notificationsEnabled={false}
        onRequestEnableNotifications={onRequestEnable}
      />,
    )
    fireEvent.click(screen.getByLabelText('Enable desktop notifications'))
    expect(onRequestEnable).toHaveBeenCalled()
  })

  test('clicking the on notifications button calls onDisableNotifications', () => {
    const onDisable = vi.fn()
    render(
      <FlowHeader
        flowName="demo"
        connected={true}
        notificationsPermission="granted"
        notificationsEnabled={true}
        onDisableNotifications={onDisable}
      />,
    )
    fireEvent.click(screen.getByLabelText('Disable desktop notifications'))
    expect(onDisable).toHaveBeenCalled()
  })
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `cd pkg/web/dashboard && npx vitest run src/components/flow-header`
Expected: FAIL — `FlowHeader` doesn't accept/render these props yet, so `getByLabelText('Enable desktop notifications')` etc. find nothing.

- [ ] **Step 3: Implement**

Replace the full contents of `pkg/web/dashboard/src/components/flow-header/FlowHeader.tsx` with:

```tsx
import type { ReactElement } from 'react'
import { useThemeMode } from '../../hooks/use-theme-mode'
import type { NotificationPermissionState } from '../../hooks/use-desktop-notifications'

type FlowHeaderProps = {
  flowName: string
  connected: boolean
  attention?: boolean
  description?: string
  notificationsPermission?: NotificationPermissionState
  notificationsEnabled?: boolean
  onRequestEnableNotifications?: () => void
  onDisableNotifications?: () => void
}

// Шапка дашборда: декоративный логотип, имя флоу и индикатор WebSocket-соединения.
// id flow-name и ws-status и классы ws-status connected/disconnected сохранены для
// совместимости со скинами (см. skins/base/header.css и skins/<name>/index.css).
// Когда attention=true — хотя бы одна стадия ждёт действия пользователя — рядом с
// именем флоу загорается пульсирующая amber-точка. Плюс переключатель dark/light —
// не зависит от пропсов компонента и активного скина.
// description — опциональный подзаголовок (описание флоу из его конфигурации),
// помогает отличить несколько параллельно запущенных пайплайнов друг от друга;
// рендерится второй строкой под именем флоу, не добавляя новую колонку в грид шапки.
// notificationsPermission/notificationsEnabled + on*Notifications — состояние и
// колбэки десктоп-уведомлений (useDesktopNotifications в App.tsx); кнопка вообще
// не рендерится при permission='unsupported' (браузер без Notification API).
export function FlowHeader({
  flowName,
  connected,
  attention = false,
  description,
  notificationsPermission = 'unsupported',
  notificationsEnabled = false,
  onRequestEnableNotifications,
  onDisableNotifications,
}: FlowHeaderProps): ReactElement {
  const hasDescription = description !== undefined && description.trim() !== ''
  const statusText = connected ? 'LINK' : 'OFFLINE'
  const statusClass = connected ? 'connected' : 'disconnected'
  const { mode, toggle } = useThemeMode()

  return (
    <header id="header">
      <span className="logo" aria-hidden="true">
        <span className="l-ring">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1">
            <polygon points="12,1 22,7 22,17 12,23 2,17 2,7" />
          </svg>
        </span>
        <span className="l-arc">
          <svg viewBox="0 0 24 24" fill="none" strokeWidth="1" strokeLinecap="round">
            <path d="M 12 3 A 9 9 0 0 1 21 12" />
          </svg>
        </span>
        <span className="l-core">
          <svg viewBox="0 0 24 24" stroke="none">
            <circle cx="12" cy="12" r="2.4" />
          </svg>
        </span>
      </span>
      <h1>afm</h1>
      <div className="flow-name-wrap">
        <div id="flow-name" className="flow-name">{flowName}</div>
        {hasDescription && <div id="flow-description" className="flow-description">{description}</div>}
      </div>
      {attention && <span className="attention-dot" aria-label="Action needed" />}
      <div id="ws-status" className={`ws-status ${statusClass}`} title="WebSocket">{statusText}</div>
      <button
        type="button"
        className="icon-btn"
        onClick={toggle}
        aria-label={mode === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
      >
        {mode === 'dark' ? '☾' : '☀'}
      </button>
      {notificationsPermission !== 'unsupported' && (
        <button
          type="button"
          className={notificationsEnabled ? 'icon-btn icon-btn-on' : 'icon-btn'}
          onClick={notificationsEnabled ? onDisableNotifications : onRequestEnableNotifications}
          disabled={notificationsPermission === 'denied'}
          aria-label={notificationsEnabled ? 'Disable desktop notifications' : 'Enable desktop notifications'}
          title={notificationsPermission === 'denied' ? 'Notifications blocked in browser settings' : undefined}
        >
          {notificationsEnabled ? '🔔' : '🔕'}
        </button>
      )}
    </header>
  )
}
```

In `pkg/web/dashboard/skins/base/layout.css`, right after the existing `.icon-btn:hover { border-color: var(--mint); }` rule, add:

```css
.icon-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.icon-btn-on {
  border-color: var(--mint);
  color: var(--mint);
}
```

In `pkg/web/dashboard/src/app/App.tsx`, add this import alongside the existing hook imports (after the `useFaviconPulse` import line):

```ts
import { useDesktopNotifications } from '../hooks/use-desktop-notifications'
```

Change:

```ts
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)
  useFaviconPulse(anyAttention)
```

to:

```ts
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)
  useFaviconPulse(anyAttention)
  const {
    enabled: notificationsEnabled,
    permission: notificationsPermission,
    requestEnable: onRequestEnableNotifications,
    disable: onDisableNotifications,
  } = useDesktopNotifications(stages, setSelectedStageId)
```

Change the `<FlowHeader ... />` line:

```tsx
      <FlowHeader flowName={flowName} connected={connected} attention={anyAttention} description={description} />
```

to:

```tsx
      <FlowHeader
        flowName={flowName}
        connected={connected}
        attention={anyAttention}
        description={description}
        notificationsPermission={notificationsPermission}
        notificationsEnabled={notificationsEnabled}
        onRequestEnableNotifications={onRequestEnableNotifications}
        onDisableNotifications={onDisableNotifications}
      />
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `cd pkg/web/dashboard && npx vitest run`
Expected: PASS — all tests across the whole frontend suite, including the four new `FlowHeader` tests and every test from Tasks 1–2.

- [ ] **Step 5: Typecheck, build, lint, commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm/pkg/web/dashboard
npx tsc --noEmit
cd /Users/alexander.kopichin/work/personal/afm
make build
make lint
git add pkg/web/dashboard/src/components/flow-header pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/skins/base/layout.css
git commit -m "$(cat <<'EOF'
feat(dashboard): кнопка десктоп-уведомлений в шапке + подключение к App

Колокольчик рядом с переключателем темы: скрыт при unsupported,
disabled+tooltip при denied, вкл/выкл — requestEnable/disable из
useDesktopNotifications. Клик по нотификации выбирает стадию через
setSelectedStageId.
EOF
)"
```

---

### Task 4: Manual live verification in a real browser

**Files:** none (verification only, nothing to commit from this task).

This task has no automated test — it's the real-world proof that the feature works end-to-end in an actual running dashboard, not just against mocked `Notification`/`document.hidden` in Vitest's jsdom. Do this after Tasks 1–3 are merged, with a freshly built dashboard.

**Known limitation to state upfront:** browser automation (Chrome DevTools MCP) cannot observe a real OS-level notification popup — that's outside the page's DOM, rendered by the OS/browser chrome. What *is* verifiable, and is exactly the contract this feature is responsible for, is: the real running app constructs a real `Notification` object with the right title/body/icon at the right moment, and its `onclick` handler does the right thing. Verify that, and note the OS-popup rendering itself as an accepted, unautomatable gap — don't claim to have seen a literal popup.

- [ ] **Step 1: Build and run a real flow**

```bash
cd /Users/alexander.kopichin/work/personal/afm
make build
go build -o ~/go/bin/afm-dev ./cmd/afm
```

Set up a minimal flow with at least one stage that can reach `awaiting_approval` (e.g. `agents: [planning]`, any command) in a scratch directory, run it with `~/go/bin/afm-dev run flow.yaml`, note the dashboard port.

- [ ] **Step 2: Open the dashboard and confirm the button renders**

Navigate to the dashboard URL with the Chrome DevTools MCP tools. Take a snapshot; confirm a notifications button is present in the header (real Chrome supports the `Notification` API, so `notificationsPermission` should resolve to `'default'`, not `'unsupported'`) with `aria-label="Enable desktop notifications"`.

- [ ] **Step 3: Enable notifications and confirm the permission flow**

Click the button. Chrome's permission prompt behavior under automation may auto-resolve (commonly to `'granted'` for `http://localhost` origins in a fresh automated profile, or it may require an explicit grant — check what actually happens). After clicking, use `evaluate_script` to confirm:

```js
() => ({ permission: Notification.permission, stored: localStorage.getItem('afm-notifications-enabled') })
```

Expected: `permission: 'granted'`, `stored: '1'`, and re-take a snapshot to confirm the button's `aria-label` flipped to `"Disable desktop notifications"`.

- [ ] **Step 4: Verify a real transition fires a real `Notification` construction**

Use `evaluate_script` to install a spy on the real `window.Notification` constructor *before* triggering a transition (wrapping, not replacing, so the real app code path still runs):

```js
() => {
  window.__notifications = []
  const Original = window.Notification
  function Spy(title, options) {
    window.__notifications.push({ title, options })
    return new Original(title, options)
  }
  Spy.permission = Original.permission
  Spy.requestPermission = Original.requestPermission.bind(Original)
  window.Notification = Spy
}
```

Force the tab hidden (mirrors exactly what the unit tests do, but now against the real running app):

```js
() => {
  Object.defineProperty(document, 'hidden', { value: true, configurable: true })
  document.dispatchEvent(new Event('visibilitychange'))
}
```

Then drive the real stage to `awaiting_approval` (or `failed`) through the real API — e.g. `curl -X POST localhost:<port>/api/stages/<id>/retry` on an already-failed stage, or just wait for the planning stage already in the flow to reach `awaiting_approval` naturally — and poll `evaluate_script(() => window.__notifications)` until it's non-empty (up to ~5s, matching the dashboard's poll interval).

Expected: `window.__notifications` contains one entry with `title` matching the stage's new status (`"Need Approve"` for `awaiting_approval`, etc.) and `options.body` containing the stage's name.

- [ ] **Step 5: Verify the click handler**

From the same page, call the real notification's `onclick` (the spy still returns a *real* `Notification` instance, so this exercises the actual handler installed by `fireNotification`):

```js
() => {
  // grab a reference kept from the reconcile call if needed, or re-derive
  // via a second spy capturing the instance itself, not just title/options
}
```

(Adjust the Step 4 spy to also push the constructed instance — `window.__notifications.push({ title, options, instance })` — so this step can call `window.__notifications[0].instance.onclick()` directly.) Confirm via a follow-up snapshot that the previously-hidden stage is now the selected one in the sidebar (the `onFocusStage` → `setSelectedStageId` wiring worked).

- [ ] **Step 6: Clean up, report back**

Stop the running flow/container. No commit for this task. If any step doesn't match the expected behavior, capture what actually happened (exact `evaluate_script` output, screenshot) before concluding the feature doesn't work — the goal is evidence, not an assumption either way.
