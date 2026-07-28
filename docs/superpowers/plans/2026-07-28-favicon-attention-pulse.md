# Пульсирующая иконка вкладки при ожидании действия пользователя — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Когда хотя бы одна стадия текущего прогона ждёт действия пользователя и вкладка дашборда в фоне, иконка вкладки браузера должна мигать амберным бейджем поверх текущей иконки (любого скина), пока пользователь не вернётся на вкладку.

**Architecture:** Новый React-хук `useFaviconPulse(active)` в `pkg/web/dashboard/src/hooks/use-favicon-pulse/` копирует структуру уже существующего `useTitleFlash` (эффект, подписка на `visibilitychange`, restore на cleanup), но вместо `document.title` мутирует `href` у `<link rel="icon">`. Сама композиция бейджа (canvas + Image) вынесена в отдельную чистую async-функцию `compositeAttentionBadge`, которую хук вызывает и которую тесты хука мокают — сам canvas/Image в jsdom не работает и не тестируется пиксельно. Хук подключается в `App.tsx` на существующий глобальный сигнал `anyAwaiting(stages)` (уже вычислен и используется для точки в шапке).

**Tech Stack:** React 18 + TypeScript (strict), Vite, Vitest + @testing-library/react (jsdom environment).

## Global Constraints

- Сигнал пульсации — `anyAwaiting(stages)` (глобальный, по всем стадиям прогона), НЕ сигнал выбранной стадии.
- Пульсировать только пока `document.hidden === true`; при возврате на вкладку — немедленно restore оригинальной иконки.
- Интервал мигания — `700` мс (полный цикл вкл/выкл ~1.4с).
- Бейдж рисуется ПОВЕРХ текущей иконки (`<link rel="icon">.href` на момент активации), не заменяет её отдельным ассетом — работает с любым скином без доп. файлов.
- Цвет бейджа — значение CSS-переменной `--amber` активного скина через `getComputedStyle(document.documentElement)`, фолбэк `#e5d442`, если переменная пуста.
- Размер canvas — `32×32`; бейдж — кружок в правом нижнем углу, радиус `6`, с тёмным кольцом-обводкой (радиус `8`, `rgba(0,0,0,0.55)`) для контраста на любом фоне иконки.
- Ошибка загрузки/декодирования иконки (canvas недоступен, `Image.onerror`) — фича молча не пульсирует, без падения остального UI.
- Нет `<link rel="icon">` в DOM — хук ничего не делает.
- `compositeAttentionBadge` не тестируется пиксельно юнит-тестами (canvas/Image не работают в jsdom без пакета `canvas`, которого нет в devDependencies) — тестируется только через мок в тестах хука.

---

### Task 1: `compositeAttentionBadge` — композиция бейджа поверх текущей иконки

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-favicon-pulse/composite-attention-badge.ts`

**Interfaces:**
- Consumes: ничего из предыдущих задач (первая задача).
- Produces: `export async function compositeAttentionBadge(iconHref: string): Promise<string>` — резолвится data URL (`canvas.toDataURL()`) готового изображения (иконка + бейдж) или реджектится, если иконку не удалось загрузить/отрисовать. Кеширует результат по `iconHref` на уровне модуля (повторный вызов с тем же href не перерисовывает canvas).

Эта задача без автотеста — `Image.onload`/`getContext('2d')` не работают в jsdom (тестовое окружение проекта, пакет `canvas` не установлен), поэтому unit-тест здесь либо зависнет (onload никогда не сработает), либо будет тестировать не то поведение. Функция проверяется вручную в браузере в Task 4. Вместо теста — строгий typecheck и явная защита от `ctx === null`.

- [ ] **Step 1: Написать реализацию**

```typescript
// pkg/web/dashboard/src/hooks/use-favicon-pulse/composite-attention-badge.ts

const BADGE_SIZE = 32
const BADGE_RADIUS = 6
const BADGE_RING_RADIUS = 8
const BADGE_CENTER = BADGE_SIZE - BADGE_RING_RADIUS - 1
const FALLBACK_AMBER = '#e5d442'

const badgeCache = new Map<string, Promise<string>>()

function readAmberColor(): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue('--amber').trim()
  return value !== '' ? value : FALLBACK_AMBER
}

function loadImage(href: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error(`favicon-pulse: failed to load icon "${href}"`))
    img.src = href
  })
}

// Рисует амберный (по факту — цвет var(--amber) активного скина) бейдж-кружок
// поверх текущей иконки вкладки, какой бы она ни была (дефолтная/скиновая) —
// не заменяет иконку отдельным ассетом, а компонует поверх неё через canvas.
// Кешируется по iconHref: повторная активация пульса на той же иконке не
// перерисовывает canvas заново.
export async function compositeAttentionBadge(iconHref: string): Promise<string> {
  const cached = badgeCache.get(iconHref)
  if (cached !== undefined) return cached

  const promise = (async () => {
    const img = await loadImage(iconHref)
    const canvas = document.createElement('canvas')
    canvas.width = BADGE_SIZE
    canvas.height = BADGE_SIZE
    const ctx = canvas.getContext('2d')
    if (ctx === null) {
      throw new Error('favicon-pulse: 2d canvas context unavailable')
    }

    ctx.drawImage(img, 0, 0, BADGE_SIZE, BADGE_SIZE)

    ctx.beginPath()
    ctx.arc(BADGE_CENTER, BADGE_CENTER, BADGE_RING_RADIUS, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(0, 0, 0, 0.55)'
    ctx.fill()

    ctx.beginPath()
    ctx.arc(BADGE_CENTER, BADGE_CENTER, BADGE_RADIUS, 0, Math.PI * 2)
    ctx.fillStyle = readAmberColor()
    ctx.fill()

    return canvas.toDataURL()
  })()

  badgeCache.set(iconHref, promise)
  promise.catch(() => badgeCache.delete(iconHref))
  return promise
}
```

- [ ] **Step 2: Typecheck**

Run: `cd pkg/web/dashboard && npm run typecheck`
Expected: без ошибок (в т.ч. `strict`/`noUnusedLocals`/`noUnusedParameters` из `tsconfig.json` — в файле не должно остаться неиспользуемых импортов/переменных).

- [ ] **Step 3: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-favicon-pulse/composite-attention-badge.ts
git commit -m "feat(dashboard): compositeAttentionBadge — бейдж поверх текущей иконки вкладки"
```

---

### Task 2: `useFaviconPulse` — хук с таймером/visibility-логикой (TDD)

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.ts`
- Test: `pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.test.ts`

**Interfaces:**
- Consumes: `compositeAttentionBadge(iconHref: string): Promise<string>` из Task 1 (`./composite-attention-badge`) — в тестах мокается через `vi.mock`.
- Produces: `export function useFaviconPulse(active: boolean): void` — эффект без возвращаемого значения, мутирует `href` у `document.querySelector('link[rel="icon"]')`.

- [ ] **Step 1: Написать падающие тесты**

```typescript
// pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.test.ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useFaviconPulse } from './use-favicon-pulse'
import { compositeAttentionBadge } from './composite-attention-badge'

vi.mock('./composite-attention-badge', () => ({
  compositeAttentionBadge: vi.fn(),
}))

const mockComposite = vi.mocked(compositeAttentionBadge)
const BADGE_HREF = 'data:image/png;base64,BADGE'

function setIconLink(href: string): HTMLLinkElement {
  document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  const link = document.createElement('link')
  link.rel = 'icon'
  link.href = href
  document.head.appendChild(link)
  return link
}

describe('useFaviconPulse', () => {
  beforeEach(() => {
    Object.defineProperty(document, 'hidden', { value: true, configurable: true })
    mockComposite.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())
  })

  it('не трогает href и не зовёт compositeAttentionBadge при active=false', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    renderHook(() => useFaviconPulse(false))

    await vi.advanceTimersByTimeAsync(3000)

    expect(link.href).toBe(original)
    expect(mockComposite).not.toHaveBeenCalled()
  })

  it('мигает href между оригиналом и бейджем, пока вкладка скрыта и active=true', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    renderHook(() => useFaviconPulse(true))

    await vi.advanceTimersByTimeAsync(0) // дать промису compositeAttentionBadge зарезолвиться
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(original)
  })

  it('останавливает пульс и восстанавливает href, когда вкладка снова видима', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    Object.defineProperty(document, 'hidden', { value: false, configurable: true })
    document.dispatchEvent(new Event('visibilitychange'))

    expect(link.href).toBe(original)

    await vi.advanceTimersByTimeAsync(2000)
    expect(link.href).toBe(original) // таймер остановлен, больше не мигает
  })

  it('восстанавливает href и чистит таймер при размонтировании', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockResolvedValue(BADGE_HREF)

    const { unmount } = renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(700)
    expect(link.href).toBe(BADGE_HREF)

    unmount()
    expect(link.href).toBe(original)

    await vi.advanceTimersByTimeAsync(2000)
    expect(link.href).toBe(original)
  })

  it('не падает и не пульсирует, если compositeAttentionBadge реджектится', async () => {
    vi.useFakeTimers()
    const link = setIconLink('/favicon.svg')
    const original = link.href
    mockComposite.mockRejectedValue(new Error('load failed'))

    renderHook(() => useFaviconPulse(true))
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(3000)

    expect(link.href).toBe(original)
  })

  it('ничего не делает, если в DOM нет link[rel="icon"]', async () => {
    vi.useFakeTimers()
    document.querySelectorAll('link[rel="icon"]').forEach((el) => el.remove())

    expect(() => renderHook(() => useFaviconPulse(true))).not.toThrow()
    await vi.advanceTimersByTimeAsync(3000)
    expect(mockComposite).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Убедиться, что тесты падают (модуля `use-favicon-pulse.ts` ещё нет)**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-favicon-pulse/use-favicon-pulse.test.ts`
Expected: FAIL — `Cannot find module './use-favicon-pulse'` (или аналогичная ошибка резолва импорта).

- [ ] **Step 3: Реализовать хук**

```typescript
// pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.ts
import { useEffect } from 'react'
import { compositeAttentionBadge } from './composite-attention-badge'

const PULSE_INTERVAL_MS = 700

// Пульсирует favicon амберным бейджем, пока вкладка в фоне (document.hidden)
// и active=true (хотя бы одна стадия прогона ждёт действия пользователя) —
// тот же паттерн, что useTitleFlash использует для document.title, но для
// <link rel="icon">.href.
export function useFaviconPulse(active: boolean): void {
  useEffect(() => {
    if (!active) return
    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (link === null) return

    const original = link.href
    let toggle = false
    let timer: ReturnType<typeof setInterval> | undefined
    let cancelled = false

    const stop = () => {
      if (timer !== undefined) clearInterval(timer)
      timer = undefined
      link.href = original
    }

    const onVisibility = () => {
      if (!document.hidden) {
        stop()
        return
      }
      void compositeAttentionBadge(original)
        .then((badgeHref) => {
          if (cancelled || !document.hidden) return
          timer = setInterval(() => {
            toggle = !toggle
            link.href = toggle ? badgeHref : original
          }, PULSE_INTERVAL_MS)
        })
        .catch(() => {
          // Не удалось построить бейдж (ошибка загрузки иконки в canvas) —
          // просто не пульсируем, без падения остального UI.
        })
    }

    document.addEventListener('visibilitychange', onVisibility)
    if (document.hidden) onVisibility()

    return () => {
      cancelled = true
      document.removeEventListener('visibilitychange', onVisibility)
      stop()
    }
  }, [active])
}
```

- [ ] **Step 4: Запустить тесты, убедиться что все проходят**

Run: `cd pkg/web/dashboard && npx vitest run src/hooks/use-favicon-pulse/use-favicon-pulse.test.ts`
Expected: PASS — все 6 тестов зелёные.

- [ ] **Step 5: Typecheck**

Run: `cd pkg/web/dashboard && npm run typecheck`
Expected: без ошибок.

- [ ] **Step 6: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.ts pkg/web/dashboard/src/hooks/use-favicon-pulse/use-favicon-pulse.test.ts
git commit -m "feat(dashboard): useFaviconPulse — таймер/visibility-логика пульсации favicon"
```

---

### Task 3: Барrel-экспорт + подключение в `App.tsx`

**Files:**
- Create: `pkg/web/dashboard/src/hooks/use-favicon-pulse/index.ts`
- Modify: `pkg/web/dashboard/src/app/App.tsx:17-71` (добавить импорт и вызов хука рядом с `useTitleFlash`)

**Interfaces:**
- Consumes: `useFaviconPulse(active: boolean): void` из Task 2 (`../hooks/use-favicon-pulse`); `anyAttention` — уже существующая переменная в `App.tsx` (строка 70, `anyAwaiting(stages)`).
- Produces: ничего для последующих задач (последняя задача этого плана).

- [ ] **Step 1: Создать barrel-экспорт**

```typescript
// pkg/web/dashboard/src/hooks/use-favicon-pulse/index.ts
export { useFaviconPulse } from './use-favicon-pulse'
```

- [ ] **Step 2: Подключить хук в App.tsx**

В `pkg/web/dashboard/src/app/App.tsx` добавить импорт рядом с существующим импортом `useTitleFlash` (строка 18):

```typescript
import { useTitleFlash } from '../hooks/use-title-flash'
import { useFaviconPulse } from '../hooks/use-favicon-pulse'
```

И вызов хука сразу после `useTitleFlash(attention.needsAttention)` (строка 71), обновив комментарий над `anyAttention` (строки 66-68), который сейчас описывает `anyAttention` только как источник точки в шапке:

Было:
```typescript
  // Attention-сигнал выбранной стадии: kind='dialog' (awaiting_user_input) или
  // 'plan' (awaiting_approval). needsAttention кормит title-flash для фоновой
  // вкладки, anyAttention — точку в шапке (хотя бы одна стадия ждёт юзера).
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)
```

Стало:
```typescript
  // Attention-сигнал выбранной стадии: kind='dialog' (awaiting_user_input) или
  // 'plan' (awaiting_approval). needsAttention кормит title-flash для фоновой
  // вкладки, anyAttention — точку в шапке И пульс favicon (хотя бы одна
  // стадия прогона ждёт юзера, а не только выбранная).
  const attention = useAttention(selectedStage)
  const anyAttention = anyAwaiting(stages)
  useTitleFlash(attention.needsAttention)
  useFaviconPulse(anyAttention)
```

- [ ] **Step 3: Typecheck**

Run: `cd pkg/web/dashboard && npm run typecheck`
Expected: без ошибок.

- [ ] **Step 4: Прогнать весь фронтенд-тестсьют (убедиться, что App.test.tsx не сломан)**

Run: `cd pkg/web/dashboard && npm test`
Expected: PASS — все существующие тесты (включая `App.test.tsx`, 13 тестов) по-прежнему зелёные. `App.test.tsx` рендерит `<App />` без реального `index.html`, поэтому `link[rel="icon"]` в тестовом DOM отсутствует — `useFaviconPulse` там становится no-op по ветке "нет link в DOM" (Task 2), без побочных эффектов на существующие тесты.

- [ ] **Step 5: Commit**

```bash
git add pkg/web/dashboard/src/hooks/use-favicon-pulse/index.ts pkg/web/dashboard/src/app/App.tsx
git commit -m "feat(dashboard): подключаем useFaviconPulse к anyAttention в App"
```

---

### Task 4: Ручная проверка в браузере

**Files:** нет изменений кода — только проверка.

**Interfaces:**
- Consumes: полную фичу из Task 1-3.
- Produces: ничего (финальная задача).

- [ ] **Step 1: Собрать и запустить дашборд**

```bash
cd pkg/web/dashboard && npm run dev
```

(или через `make build && ./bin/afm run <любой flow с interactive-стадией>` — что удобнее для доступа к реальному `awaiting_user_input`)

- [ ] **Step 2: Довести стадию до `awaiting_user_input` или `awaiting_approval`**

Открыть дашборд в браузере, дождаться (или спровоцировать через мок-агента, как в Go-интеграционных тестах `pkg/orchestrator`) статуса `awaiting_user_input`/`awaiting_approval` у любой стадии.

- [ ] **Step 3: Переключиться на другую вкладку браузера**

Ожидаемое поведение: иконка вкладки дашборда начинает мигать — виден амберный (или цвета `--amber` активного скина) кружок-бейдж поверх обычной иконки, примерно раз в 700мс.

- [ ] **Step 4: Вернуться на вкладку дашборда**

Ожидаемое поведение: иконка немедленно возвращается к обычному виду и больше не мигает, пока вкладка активна — даже если стадия всё ещё ждёт действия.

- [ ] **Step 5: Проверить на втором скине**

Скин выбирается на сервере один раз при старте (`theme: goga` в `.afm/config.yaml`, см. `pkg/server/server.go:236 builtinSkinName` — рантайм-переключателя скина в UI нет, только light/dark тема через localStorage). Перезапустить `afm run`/dashboard-сервер с `theme: goga` в конфиге и повторить Step 2-4 — бейдж должен использовать `--amber` этого скина (у `goga` это синий `#3882F6`, а не жёлто-амберный), подтверждая скин-агностичность реализации. Заодно закрывает Minor-находку ревью Task 1 (кеш `compositeAttentionBadge` ключуется только по `iconHref`, не по цвету скина): некритично, потому что скин фиксируется на сервере до перезапуска, поэтому в рамках одной вкладки `--amber` не меняется под одним и тем же href.

- [ ] **Step 6: Финальный прогон всего тестсьюта и коммит (если Step 3/4/5 выявили правки)**

Если ручная проверка потребовала правок — внести их, прогнать `npm test && npm run typecheck` ещё раз, закоммитить отдельным коммитом с описанием, что именно поправлено.
