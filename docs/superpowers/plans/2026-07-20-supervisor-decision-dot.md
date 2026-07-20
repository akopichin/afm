# Supervisor Decision Dot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Заменить инлайн-бейдж решения супервизора на точку в углу статус-бейджа с фиолетовым поповером причины по клику.

**Architecture:** Компонент `SupervisorDecision` перестаёт выводить причину текстом в потоке. Вместо этого он рендерит кликабельную точку (`<button>`), абсолютно спозиционированную в верхний-правый угол статус-бейджа, и открывает поповер с причиной по клику. Данные (поллинг `/api/stages/<id>/supervisor`) и типы не меняются.

**Tech Stack:** React 18 + TypeScript, Vite (outDir=`.`, `base: './'`), Vitest + @testing-library/react, CSS в `public/style.css`.

## Global Constraints

- Рабочая директория фронтенда: `pkg/web/dashboard`. Все пути ниже — относительно неё, если не указано иное.
- Тесты: `npm run test` (vitest run). Типы: `npm run typecheck`. Сборка: `npm run build`.
- Источник CSS — `public/style.css`. Корневой `style.css` — build-артефакт (`vite build` копирует `public/` в outDir `.`); его встраивает `pkg/web/embed.go` через `go:embed`. Руками корневой `style.css` не править.
- Не менять API/бэкенд, тип `Decision`, логику поллинга (3000 мс).
- Две визуальные темы: `standard` → `var(--amber)` (токен темы), `autonomous` → хардкод `#c084fc` в обеих темах. Отдельный `.theme-goga`-оверрайд не добавляется.
- Комментарии и коммиты — на русском. Co-Authored-By не добавлять.

---

### Task 1: Переписать компонент SupervisorDecision в точку + поповер

**Files:**
- Modify: `src/components/supervisor-decision/SupervisorDecision.tsx`
- Test: `src/components/supervisor-decision/SupervisorDecision.test.tsx` (создать)

**Interfaces:**
- Consumes: `fetch('/api/stages/<id>/supervisor')` → `{ decision?: string; reason?: string }` (без изменений).
- Produces: `SupervisorDecision({ stageId }: { stageId: string }): ReactElement | null`. Рендерит `<span class="supervisor-decision">` с `<button class="supervisor-dot autonomous|standard">` и (при открытии) `<div class="supervisor-popover" role="dialog">`. `aria-label` кнопки: `supervisor decision: <decision>`.

- [ ] **Step 1: Написать падающий тест**

Создать `src/components/supervisor-decision/SupervisorDecision.test.tsx`:

```tsx
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { SupervisorDecision } from './SupervisorDecision'

// Мокаем эндпоинт супервизора: null → 404 (решения нет), иначе {decision, reason}.
function mockFetch(decision: string | null, reason = ''): void {
  vi.spyOn(globalThis, 'fetch').mockImplementation(
    async () =>
      (decision == null
        ? { ok: false, json: async () => ({}) }
        : { ok: true, json: async () => ({ decision, reason }) }) as Response,
  )
}

describe('SupervisorDecision', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('ничего не рендерит, когда решения нет', async () => {
    mockFetch(null)
    const { container } = render(<SupervisorDecision stageId="s1" />)
    await waitFor(() => expect(container.querySelector('.supervisor-dot')).toBeNull())
  })

  test('рендерит точку с классом трека, когда решение есть', async () => {
    mockFetch('autonomous', 'reason text')
    const { container } = render(<SupervisorDecision stageId="s1" />)
    const dot = await waitFor(() => {
      const el = container.querySelector('.supervisor-dot')
      expect(el).not.toBeNull()
      return el as HTMLElement
    })
    expect(dot).toHaveClass('autonomous')
  })

  test('клик открывает поповер с заголовком решения и причиной', async () => {
    mockFetch('autonomous', 'because prompt requires it')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByText('supervisor: autonomous')).toBeInTheDocument()
    expect(screen.getByText('because prompt requires it')).toBeInTheDocument()
  })

  test('поповер закрывается повторным кликом и по Escape', async () => {
    mockFetch('standard', 'reason')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.click(dot)
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  test('поповер закрывается по клику вне', async () => {
    mockFetch('standard', 'reason')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  test('при пустой причине поповер показывает только заголовок', async () => {
    mockFetch('autonomous', '')
    render(<SupervisorDecision stageId="s1" />)
    const dot = await screen.findByRole('button', { name: /supervisor decision/i })
    fireEvent.click(dot)
    expect(screen.getByText('supervisor: autonomous')).toBeInTheDocument()
    expect(screen.getByRole('dialog').querySelector('.supervisor-popover-reason')).toBeNull()
  })
})
```

- [ ] **Step 2: Запустить тест — убедиться, что падает**

Run: `npm run test -- SupervisorDecision`
Expected: FAIL — текущий компонент рендерит `.supervisor-badge`/`.supervisor-reason`, нет `.supervisor-dot`, `role="button"` и `role="dialog"`.

- [ ] **Step 3: Переписать компонент**

Заменить всё содержимое `src/components/supervisor-decision/SupervisorDecision.tsx`:

```tsx
import { useEffect, useRef, useState, type ReactElement } from 'react'

type Decision = { decision: string; reason: string } | null

type SupervisorDecisionProps = {
  stageId: string
}

// Решение супервизора для выбранной стадии. Точка в углу статус-бейджа; причина —
// в поповере по клику. Источник: /api/stages/<id>/supervisor (поллинг), не теряется
// при позднем подключении дашборда. См. также .status-badge-wrap в App.tsx.
export function SupervisorDecision({ stageId }: SupervisorDecisionProps): ReactElement | null {
  const [decision, setDecision] = useState<Decision>(null)
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLSpanElement>(null)

  // Загрузка решения (поллинг). Смена стадии сбрасывает открытый поповер.
  useEffect(() => {
    setOpen(false)
    let cancelled = false
    async function load(): Promise<void> {
      try {
        const res = await fetch(`/api/stages/${encodeURIComponent(stageId)}/supervisor`)
        if (!res.ok) {
          if (!cancelled) setDecision(null)
          return
        }
        const data = (await res.json()) as { decision?: string; reason?: string }
        if (cancelled) return
        setDecision(
          data.decision != null ? { decision: data.decision, reason: data.reason ?? '' } : null,
        )
      } catch {
        /* сеть/ответ — оставляем прошлое значение */
      }
    }
    void load()
    const timer = setInterval(load, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [stageId])

  // Закрытие поповера по клику вне и по Escape — слушатели активны только при open.
  useEffect(() => {
    if (!open) return
    function onDown(e: MouseEvent): void {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    function onKey(e: KeyboardEvent): void {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (decision == null) return null
  const trackClass = decision.decision === 'autonomous' ? 'autonomous' : 'standard'
  return (
    <span className="supervisor-decision" ref={rootRef}>
      <button
        type="button"
        className={`supervisor-dot ${trackClass}`}
        aria-label={`supervisor decision: ${decision.decision}`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      />
      {open && (
        <div className={`supervisor-popover ${trackClass}`} role="dialog">
          <div className="supervisor-popover-title">supervisor: {decision.decision}</div>
          {decision.reason !== '' && (
            <div className="supervisor-popover-reason">{decision.reason}</div>
          )}
        </div>
      )}
    </span>
  )
}
```

- [ ] **Step 4: Запустить тесты — убедиться, что проходят**

Run: `npm run test -- SupervisorDecision`
Expected: PASS (6 тестов).

- [ ] **Step 5: Проверить типы**

Run: `npm run typecheck`
Expected: без ошибок.

- [ ] **Step 6: Коммит**

```bash
git add src/components/supervisor-decision/SupervisorDecision.tsx src/components/supervisor-decision/SupervisorDecision.test.tsx
git commit -m "feat(dashboard): решение супервизора — точка + поповер по клику"
```

---

### Task 2: Обёртка в заголовке, CSS и сборка

**Files:**
- Modify: `src/app/App.tsx` (блок `stageHeader`, ~строки 121–127)
- Modify: `public/style.css` (секции `.status-badge` ~570 и `.supervisor-badge` ~1324–1348)

**Interfaces:**
- Consumes: `SupervisorDecision` из Task 1 (рендерит точку, абсолютно позиционируемую относительно `.status-badge-wrap`).
- Produces: разметка `<span class="status-badge-wrap">` вокруг статус-бейджа и индикатора; CSS-классы `.status-badge-wrap`, `.supervisor-decision`, `.supervisor-dot(.autonomous|.standard)`, `.supervisor-popover`, `.supervisor-popover-title`, `.supervisor-popover-reason`.

- [ ] **Step 1: Обернуть статус-бейдж и индикатор в App.tsx**

В `src/app/App.tsx` найти в `stageHeader`:

```tsx
                  <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
                    {STAGE_STATUS_LABELS[selectedStage.status]}
                  </span>
                  {selectedStageId != null && <SupervisorDecision stageId={selectedStageId} />}
```

Заменить на:

```tsx
                  <span className="status-badge-wrap">
                    <span id="detail-status" className="status-badge" data-status={selectedStage.status}>
                      {STAGE_STATUS_LABELS[selectedStage.status]}
                    </span>
                    {selectedStageId != null && <SupervisorDecision stageId={selectedStageId} />}
                  </span>
```

- [ ] **Step 2: Обновить CSS в public/style.css**

Удалить старые правила бейджа (около строк 1324–1348): блок `.supervisor-badge { ... }`, `.supervisor-badge .supervisor-icon`, `.supervisor-badge .supervisor-reason`, `.supervisor-badge.autonomous`, `.supervisor-badge.standard`.

На их место добавить:

```css
/* Supervisor-decision: точка в углу статус-бейджа + поповер причины по клику. */
.status-badge-wrap {
  position: relative;
  display: inline-flex;
}
.supervisor-decision {
  display: contents;
}
.supervisor-dot {
  position: absolute;
  top: -3px;
  right: -3px;
  width: 8px;
  height: 8px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 0 6px currentColor;
  cursor: pointer;
}
.supervisor-dot.standard { color: var(--amber); }
.supervisor-dot.autonomous { color: #c084fc; }

.supervisor-popover {
  position: absolute;
  top: calc(100% + 6px);
  right: -3px;
  z-index: 50;
  max-width: 280px;
  padding: 8px 10px;
  border-radius: 8px;
  border: 1px solid #c084fc;
  background: rgba(192, 132, 252, 0.12);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
  color: var(--ink-hi);
  font-size: 11px;
  line-height: 1.4;
  text-align: left;
}
.supervisor-popover-title {
  margin-bottom: 4px;
  font-weight: 600;
  color: #c084fc;
}
.supervisor-popover-reason {
  font-weight: 400;
  opacity: 0.9;
  white-space: normal;
}
```

- [ ] **Step 3: Прогнать тесты и типы (регрессия App)**

Run: `npm run test && npm run typecheck`
Expected: все тесты PASS (включая существующий `App.test.tsx` — структура обёртки не проверяется в его ассертах), типы без ошибок.

- [ ] **Step 4: Собрать дашборд (регенерирует корневой style.css + assets)**

Run: `npm run build`
Expected: сборка успешна; обновлены `style.css`, `assets/index-*.js` в корне `dashboard/`.

- [ ] **Step 5: Визуальная проверка в обеих темах**

Запустить дашборд (dev или через afm-ран со стадией, у которой есть `supervisor.jsonl`). Убедиться:
- в углу RUNNING-бейджа видна точка (фиолетовая для autonomous, жёлтая для standard в novacorps);
- клик открывает фиолетовый поповер с `supervisor: <decision>` и причиной;
- поповер закрывается повторным кликом, кликом вне и Esc;
- в теме goga (`body class="theme-goga"`) standard-точка синяя (`#3882F6`), autonomous — фиолетовая; поповер фиолетовый.

- [ ] **Step 6: Коммит**

```bash
git add src/app/App.tsx public/style.css style.css assets index.html
git commit -m "feat(dashboard): точка решения супервизора в углу статус-бейджа + стили поповера"
```

---

## Self-Review

- **Spec coverage:** разметка-обёртка (Task 2/Step 1), компонент точка+поповер+взаимодействие (Task 1), удаление `.supervisor-reason` (Task 2/Step 2), CSS точки/поповера (Task 2/Step 2), совместимость тем (Global Constraints + Task 2/Step 5), сборка (Task 2/Step 4), тесты (Task 1) — все разделы спеки покрыты.
- **Placeholder scan:** плейсхолдеров нет; весь код и команды приведены целиком.
- **Type consistency:** класс трека (`autonomous`/`standard`), `aria-label` (`supervisor decision: …`), `role="dialog"`, классы `.supervisor-dot`/`.supervisor-popover(-title|-reason)`, обёртка `.status-badge-wrap` согласованы между компонентом, тестами, App.tsx и CSS.
