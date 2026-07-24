# Микроанимации дашборда (A–D) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить ненавязчивые информативные микроанимации (группы A–D) в дашборд afm, работающие во всех трёх скинах.

**Architecture:** Анимации живут в общих `base/*.css` на токенах/`currentColor` (скины coffee/goga/novacorps наследуют без изменений). Часть работает с существующей разметкой (CSS-only), часть триггерится one-shot классами из React-компонентов. reduced-motion уже покрыт глобально в `base/reset.css`.

**Tech Stack:** CSS (custom properties, keyframes), React/TypeScript, Vite (сборка `public/*` → корень), Go `//go:embed`.

**Спека:** `docs/superpowers/specs/2026-07-24-dashboard-microanimations-design.md`.

## Global Constraints

- Правки CSS — только в `public/skins/base/*.css`; `skins/base/*.css`, `index.html`, `assets/*` регенерирует `npm run build` (коммитим вместе).
- Всё через токены (`var(--*)`/`currentColor`) — никаких хардкод-цветов; goga/novacorps НЕ трогаем (наследуют).
- Только `transform`/`opacity`/`box-shadow`/`border-color`/`background` — без layout-shift.
- one-shot на событие (кроме C2 thinking / C3 reconnect — тихие циклы «пока идёт состояние»).
- reduced-motion НЕ трогаем — уже глобально в `reset.css`; новые правила гасятся автоматически.
- Декоративные спаны (ripple, галочка, thinking, connector, dot-check) — `aria-hidden="true"`; доступные имена кнопок не меняем (чтобы тесты `getByRole('button',{name:...})` не падали).
- Не менять go.mod; логотип не трогать; A3 (прогресс) не трогать (уже готов); `git push` не выполнять; коммиты на русском.

## File Structure

- **CSS (public/skins/base/):** `stages-list.css` (A1/A4/D), `event-feed.css` (A2), `plan-panel.css` (B1/B2/C2-стили), `header.css` (C3), `layout.css` (C1). `dialog-channel.css` — B3 использует существующий `dialog-flash`; B1 использует общий `.btn`-морф из plan-panel.css.
- **React (src/):** `stages-list/StagesList.tsx` (A1/D + разметка), `plan-panel/PlanPanel.tsx` (B1), `dialog-channel/DialogChannel.tsx` (B1/B3), `app/App.tsx` (C2).

---

### Task 1: CSS-слой анимаций в base/*.css

**Files:**
- Modify: `pkg/web/dashboard/public/skins/base/stages-list.css`
- Modify: `pkg/web/dashboard/public/skins/base/event-feed.css`
- Modify: `pkg/web/dashboard/public/skins/base/plan-panel.css`
- Modify: `pkg/web/dashboard/public/skins/base/header.css`
- Modify: `pkg/web/dashboard/public/skins/base/layout.css`
- Regenerated: соответствующие `skins/base/*.css`

**Interfaces:**
- Produces классы/keyframes, которые триггерят Tasks 2–4: `.stage-item.just-done` (A1/D), `.stage-connector` + `.dot-check` (разметка из Task 2), `.btn.ok` + `.btn-ripple`/`.btn-label`/`.btn-done` (Task 3), `.thinking`/`.td` (Task 4). CSS-only группы (A2/A4/B2/C1/C3) работают сразу с существующей разметкой.

- [ ] **Step 1: `stages-list.css` — A1 (переход в done), A4 (вход активной), D (коннекторы)**

Добавить в конец файла:

```css
/* === Motion (A1/A4/D) — one-shot переходы, токены/currentColor === */

/* A1 — статус-точка при переходе стадии в done: заливка + галочка + вспышка */
.status-dot .dot-check {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  font-size: 9px;
  color: var(--bg);
  opacity: 0;
  transform: scale(0.4);
  pointer-events: none;
}
.stage-item.just-done .status-dot { animation: dotComplete 0.6s ease; }
.stage-item.just-done .status-dot::after { transform: scale(1); }
.stage-item.just-done .status-dot .dot-check { animation: dotCheck 0.6s ease forwards; }
@keyframes dotComplete {
  0%   { box-shadow: 0 0 0 0 currentColor; }
  40%  { box-shadow: 0 0 0 5px transparent; }
  100% { box-shadow: 0 0 0 0 transparent; }
}
@keyframes dotCheck {
  0%   { opacity: 0; transform: scale(0.4); }
  55%  { opacity: 1; transform: scale(1.2); }
  100% { opacity: 1; transform: scale(1); }
}

/* A4 — вход активной стадии */
.stage-item.active { animation: stageActivate 0.4s ease; }
@keyframes stageActivate {
  from { opacity: 0.35; transform: translateX(-4px); }
  to   { opacity: 1; transform: none; }
}

/* D — коннектор между стадиями + одноразовое «пробегание» импульса при done.
   Явный дочерний элемент (не ::before/::after — те заняты .active-декором).
   left подобран под центр status-dot; при визуальной проверке допускается нудж. */
.stage-connector {
  position: absolute;
  left: 10px;
  top: 26px;
  width: 2px;
  height: 16px;
  border-radius: 2px;
  background: var(--mint-soft);
  pointer-events: none;
}
.stage-connector::after {
  content: "";
  position: absolute;
  left: 0;
  top: 0;
  width: 2px;
  height: 8px;
  border-radius: 2px;
  background: var(--amber);
  box-shadow: 0 0 6px var(--amber);
  opacity: 0;
}
.stage-item.just-done .stage-connector::after { animation: connTravel 0.6s ease-in; }
@keyframes connTravel {
  0%   { transform: translateY(0); opacity: 1; }
  100% { transform: translateY(14px); opacity: 0; }
}
```

- [ ] **Step 2: `event-feed.css` — A2 (вход новых строк)**

Добавить в конец файла:

```css
/* A2 — вход новых строк ленты (только новые монтируются → анимируются) */
.feed-entry { animation: feedIn 0.25s ease; }
@keyframes feedIn {
  from { opacity: 0; transform: translateY(-6px); }
  to   { opacity: 1; transform: none; }
}
```

- [ ] **Step 3: `plan-panel.css` — B2 (комментарий), B1 (морф кнопок), C2 (thinking-стили)**

Добавить в конец файла:

```css
/* B2 — плавный въезд границы комментария + pop маркера (общие классы плана и диалога) */
.plan-line { transition: background 0.1s, border-color 0.25s ease, margin-left 0.25s ease, padding-left 0.25s ease; }
.plan-line.has-comment .line-comment-marker { animation: markerPop 0.35s ease; }
@keyframes markerPop {
  0%   { transform: scale(0.3); }
  60%  { transform: scale(1.4); }
  100% { transform: scale(1); }
}

/* B1 — success-морф кнопок (класс .ok навешивается из React на ~1.2с) */
.btn { position: relative; overflow: hidden; }
.btn .btn-label { transition: opacity 0.2s; }
.btn .btn-done {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  opacity: 0;
  transition: opacity 0.2s;
}
.btn.ok .btn-label { opacity: 0; }
.btn.ok .btn-done { opacity: 1; }
.btn .btn-ripple {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: color-mix(in srgb, currentColor 45%, transparent);
  transform: translate(-50%, -50%) scale(0);
  pointer-events: none;
}
.btn.ok .btn-ripple { animation: btnRipple 0.5s ease-out; }
@keyframes btnRipple {
  to { transform: translate(-50%, -50%) scale(30); opacity: 0; }
}

/* C2 — индикатор «агент думает» (в шапке детали при running) */
.thinking {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: 8px;
  font-size: 9px;
  letter-spacing: 0.15em;
  text-transform: uppercase;
  color: var(--amber);
}
.thinking .td { width: 4px; height: 4px; border-radius: 50%; background: currentColor; animation: thinkBlink 1s infinite; }
.thinking .td:nth-child(2) { animation-delay: 0.15s; }
.thinking .td:nth-child(3) { animation-delay: 0.3s; }
@keyframes thinkBlink { 0%, 100% { opacity: 0.25; } 50% { opacity: 1; } }
```

- [ ] **Step 4: `header.css` — C3 (пульс реконнекта)**

Добавить в конец файла:

```css
/* C3 — мягкий пульс индикатора соединения, пока WS не подключён */
#ws-status.disconnected { animation: wsReconnect 1.1s ease-in-out infinite; }
@keyframes wsReconnect { 0%, 100% { opacity: 0.5; } 50% { opacity: 1; } }
```

- [ ] **Step 5: `layout.css` — C1 (кроссфейд темы на поверхностях)**

Добавить в конец файла (только контейнеры без собственных background-переходов — интерактивные `.btn`/`.stage-item` не трогаем, у них свои transition):

```css
/* C1 — плавный кроссфейд при смене темы (data-theme) на крупных поверхностях */
#main, #header, #footer, #stages-panel, #plan-section, #dialog-section, #feed-panel, .markdown-body {
  transition: background-color 0.28s ease, border-color 0.28s ease, color 0.28s ease;
}
```

- [ ] **Step 6: Сборка + визуальная проверка CSS-only групп**

Run:
```bash
cd pkg/web/dashboard && npm run build
```
Expected: сборка проходит; `skins/base/*.css` регенерированы. Проверить, что новые правила попали в билд:
```bash
grep -c 'feedIn\|dotComplete\|stage-connector\|btnRipple\|wsReconnect' pkg/web/dashboard/skins/base/stages-list.css pkg/web/dashboard/skins/base/event-feed.css pkg/web/dashboard/skins/base/plan-panel.css pkg/web/dashboard/skins/base/header.css
```
Expected: ненулевые совпадения в соответствующих файлах.

- [ ] **Step 7: Коммит**

```bash
git add pkg/web/dashboard/public/skins/base pkg/web/dashboard/skins/base
git commit -m "feat(dashboard): CSS-слой микроанимаций в base (A1/A2/A4/B1/B2/C1-C3/D)"
```

---

### Task 2: StagesList — A1 (переход в done) + D (коннекторы) + разметка

**Files:**
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.tsx`
- Test: `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx`

**Interfaces:**
- Consumes: CSS-классы из Task 1 (`.just-done`, `.dot-check`, `.stage-connector`).
- Produces: one-shot `just-done` на стадии при переходе `→ done`; `.dot-check` внутри `.status-dot`; `.stage-connector` между стадиями.

- [ ] **Step 1: Переписать StagesList — prev-status трекинг + разметка**

Полный новый файл:

```tsx
import { useEffect, useRef, useState, type ReactElement } from 'react'
import type { Stage } from '../../types'
import { ATTENTION_STATUSES } from '../../hooks/use-attention'

type StagesListProps = {
  stages: Stage[]
  selectedStageId: string | null
  onSelect: (stageId: string) => void
}

// Левая панель: список стадий с выбором активной. На переходе стадии в done
// показываем one-shot анимацию точки (A1) и «пробегание» импульса по коннектору (D)
// — для этого запоминаем предыдущий статус каждой стадии и держим transient-набор
// just-done, который очищается через 700мс (чуть дольше 600мс-анимаций).
export function StagesList({ stages, selectedStageId, onSelect }: StagesListProps): ReactElement {
  const prevStatus = useRef<Record<string, string>>({})
  const [justDone, setJustDone] = useState<Set<string>>(new Set())

  useEffect(() => {
    const newly: string[] = []
    for (const stage of stages) {
      const prev = prevStatus.current[stage.id]
      if (prev !== undefined && prev !== 'done' && stage.status === 'done') {
        newly.push(stage.id)
      }
      prevStatus.current[stage.id] = stage.status
    }
    if (newly.length === 0) return

    setJustDone((prev) => {
      const next = new Set(prev)
      newly.forEach((id) => next.add(id))
      return next
    })
    const timer = window.setTimeout(() => {
      setJustDone((prev) => {
        const next = new Set(prev)
        newly.forEach((id) => next.delete(id))
        return next
      })
    }, 700)
    return () => window.clearTimeout(timer)
  }, [stages])

  return (
    <aside id="stages-panel">
      <h2>Stages</h2>
      <ul id="stages-list" className="stages-list">
        {stages.map((stage, index) => (
          <li
            key={stage.id}
            className={`stage-item${stage.id === selectedStageId ? ' active' : ''}${justDone.has(stage.id) ? ' just-done' : ''}`}
            data-stage-id={stage.id}
            data-status={stage.status}
            data-attention={ATTENTION_STATUSES.has(stage.status) ? 'true' : undefined}
            onClick={() => onSelect(stage.id)}
          >
            <span className="status-dot" data-status={stage.status}>
              <span className="dot-check" aria-hidden="true">✓</span>
            </span>
            <span className="stage-label">
              <span className="stage-id">{stage.id}</span>
              {stage.name !== '' && <span className="stage-name">{stage.name}</span>}
            </span>
            {stage.status === 'awaiting_user_input' && <span className="dialog-badge">💬</span>}
            {index < stages.length - 1 && <span className="stage-connector" aria-hidden="true" />}
          </li>
        ))}
      </ul>
    </aside>
  )
}
```

- [ ] **Step 2: Тест — переход в done навешивает `just-done`**

Прочитать `StagesList.test.tsx`, убедиться что существующие тесты (структура/выбор/attention) не сломаны новой разметкой (`.dot-check`/`.stage-connector` — новые дочерние span'ы; статус-точка перестала быть пустой). Затем добавить тест:

```tsx
test('переход стадии в done навешивает one-shot класс just-done', async () => {
  const base: Stage[] = [
    { id: 's1', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
  ]
  const { container, rerender } = render(<StagesList stages={base} selectedStageId={null} onSelect={() => {}} />)
  expect(container.querySelector('.stage-item.just-done')).toBeNull()

  const done: Stage[] = [{ ...base[0]!, status: 'done' }]
  rerender(<StagesList stages={done} selectedStageId={null} onSelect={() => {}} />)
  expect(container.querySelector('.stage-item.just-done')).not.toBeNull()
})
```
(Импорты `render`, `rerender` — из `@testing-library/react`; `Stage` — из `../../types`. Если файл ещё не импортирует нужное — добавить.)

- [ ] **Step 3: typecheck + тесты**

Run:
```bash
cd pkg/web/dashboard && npm run typecheck && npm test 2>&1 | tail -4
```
Expected: tsc clean; все тесты (включая новый и существующие StagesList) зелёные.

- [ ] **Step 4: Коммит**

```bash
git add pkg/web/dashboard/src/components/stages-list/StagesList.tsx pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx
git commit -m "feat(dashboard): анимация статус-точки и коннекторов стадий (A1/D)"
```

---

### Task 3: Кнопки (B1) + диалог glow (B3)

**Files:**
- Modify: `pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx`
- Modify: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx`
- Test: `pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx`

**Interfaces:**
- Consumes: `.btn.ok`/`.btn-ripple`/`.btn-label`/`.btn-done` (Task 1), `.dialog-flash` (уже в base).
- Produces: success-морф на Approve/Send revision/Retry (PlanPanel) и ▸ SEND/Send feedback (DialogChannel); glow рамки диалога на новый pending-вопрос.

- [ ] **Step 1: PlanPanel — обёртка кнопок в морф + состояние `flash`**

В `PlanPanel` добавить состояние и хелпер (рядом с прочими useState):

```tsx
const [clicked, setClicked] = useState<'approve' | 'revise' | 'retry' | null>(null)
function flashButton(which: 'approve' | 'revise' | 'retry') {
  setClicked(which)
  window.setTimeout(() => setClicked(null), 1200)
}
```

В обработчиках вызвать `flashButton(...)` первой строкой: в `approve()` → `flashButton('approve')`, в `sendRevision()` → `flashButton('revise')`, в `retry()` → `flashButton('retry')`.

Заменить содержимое трёх кнопок (Approve, Send revision, Retry) на морф-разметку. Approve:
```tsx
<button id="btn-approve" className={`btn btn-approve${clicked === 'approve' ? ' ok' : ''}`} type="button" disabled={busy} onClick={approve}>
  <span className="btn-ripple" aria-hidden="true" />
  <span className="btn-label">Approve</span>
  <span className="btn-done" aria-hidden="true">✓ Approved</span>
</button>
```
Send revision (сохранить динамическую подпись в `.btn-label`):
```tsx
<button id="btn-revise" className={`btn btn-revise${clicked === 'revise' ? ' ok' : ''}`} type="button" disabled={busy || commentCount === 0} onClick={sendRevision}>
  <span className="btn-ripple" aria-hidden="true" />
  <span className="btn-label">{commentCount > 0 ? `Send revision (${commentCount})` : 'Send revision'}</span>
  <span className="btn-done" aria-hidden="true">✓ Sent</span>
</button>
```
Retry:
```tsx
<button id="btn-retry" className={`btn btn-retry${clicked === 'retry' ? ' ok' : ''}`} type="button" disabled={busy} onClick={retry}>
  <span className="btn-ripple" aria-hidden="true" />
  <span className="btn-label">Retry</span>
  <span className="btn-done" aria-hidden="true">✓</span>
</button>
```
`.btn-ripple`/`.btn-done` — `aria-hidden`, поэтому доступное имя кнопки = текст `.btn-label` (прежнее): существующие тесты `getByRole('button',{name:'Approve'})` / `'Send revision'` / `'Send revision (1)'` / `'Retry'` продолжают работать.

- [ ] **Step 2: DialogChannel — морф SEND/Send feedback + flash на новый вопрос**

Добавить состояние:
```tsx
const [clickedSend, setClickedSend] = useState(false)
const [flash, setFlash] = useState(false)
```
В `sendAnswer()` и `sendFeedback()` первой строкой: `setClickedSend(true); window.setTimeout(() => setClickedSend(false), 1200)`.

B3 — one-shot glow при появлении нового pending-вопроса (по смене `pending.id`):
```tsx
useEffect(() => {
  if (pending === null) return
  setFlash(true)
  const t = window.setTimeout(() => setFlash(false), 1400)
  return () => window.clearTimeout(t)
}, [pending?.id])
```
Навесить класс на контейнер pending:
```tsx
<div id="dialog-pending" className={`dialog-pending${flash ? ' dialog-flash' : ''}`}>
```
Обернуть подписи кнопок ▸ SEND и Send feedback в морф-разметку (аналогично PlanPanel), сохранив `.btn-label` текст «▸ SEND» / «Send feedback (N)» и добавив `aria-hidden` ripple + `.btn-done`:
```tsx
<button className={`btn btn-send${clickedSend ? ' ok' : ''}`} type="button" onClick={sendAnswer}>
  <span className="btn-ripple" aria-hidden="true" />
  <span className="btn-label">▸ SEND</span>
  <span className="btn-done" aria-hidden="true">✓ Sent</span>
</button>
```
и для feedback-кнопки:
```tsx
<button className={`btn btn-send${clickedSend ? ' ok' : ''}`} type="button" onClick={sendFeedback}>
  <span className="btn-ripple" aria-hidden="true" />
  <span className="btn-label">{`Send feedback (${commentCount})`}</span>
  <span className="btn-done" aria-hidden="true">✓ Sent</span>
</button>
```
(«CANCEL STAGE» и line-comment-кнопки Add/Update/Delete/Cancel НЕ трогаем.)

- [ ] **Step 3: Тест — flash на новый вопрос**

В `DialogChannel.test.tsx` добавить тест: при появлении pending-вопроса контейнер `#dialog-pending` получает класс `dialog-flash`.
```tsx
test('новый pending-вопрос даёт one-shot класс dialog-flash', async () => {
  const pending = { id: 'q1', phase: 'p1', question: 'Pick', answer: null, options: ['A'], allow_custom: true }
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(jsonResponse([pending]))
  const { container } = render(<DialogChannel stage={makeStage()} />)
  await waitFor(() => expect(container.querySelector('#dialog-pending.dialog-flash')).not.toBeNull())
})
```
(`jsonResponse`/`makeStage` — уже определены в файле.)

- [ ] **Step 4: typecheck + тесты**

Run:
```bash
cd pkg/web/dashboard && npm run typecheck && npm test 2>&1 | tail -4
```
Expected: tsc clean; все тесты зелёные (в т.ч. существующие PlanPanel/DialogChannel — доступные имена кнопок сохранены).

- [ ] **Step 5: Коммит**

```bash
git add pkg/web/dashboard/src/components/plan-panel/PlanPanel.tsx pkg/web/dashboard/src/components/dialog-channel/DialogChannel.tsx pkg/web/dashboard/src/components/dialog-channel/DialogChannel.test.tsx
git commit -m "feat(dashboard): success-морф кнопок и glow диалога на новый вопрос (B1/B3)"
```

---

### Task 4: App — индикатор «агент думает» (C2)

**Files:**
- Modify: `pkg/web/dashboard/src/app/App.tsx`

**Interfaces:**
- Consumes: `.thinking`/`.td` (Task 1).
- Produces: индикатор thinking в шапке детали при `selectedStage.status === 'running'`.

- [ ] **Step 1: Добавить индикатор в detail header**

В `App.tsx`, в блоке `stageHeader` (внутри `<span className="status-badge-wrap">`, после `<span id="detail-status" …>…</span>` и до `SupervisorDecision`), добавить:
```tsx
{selectedStage.status === 'running' && (
  <span className="thinking" aria-hidden="true">
    <span className="td" />
    <span className="td" />
    <span className="td" />
    thinking
  </span>
)}
```

- [ ] **Step 2: typecheck + тесты + сборка**

Run:
```bash
cd pkg/web/dashboard && npm run typecheck && npm test 2>&1 | tail -4 && npm run build 2>&1 | tail -3
```
Expected: tsc clean; все тесты зелёные; сборка проходит (регенерирует `index.html`/`assets`).

- [ ] **Step 3: Go-сборка**

Run:
```bash
cd /Users/alexander.kopichin/work/personal/afm && go build ./pkg/web/... ./cmd/...
```
Expected: без ошибок.

- [ ] **Step 4: Коммит**

```bash
git add pkg/web/dashboard/src/app/App.tsx pkg/web/dashboard/index.html pkg/web/dashboard/assets
git commit -m "feat(dashboard): индикатор «агент думает» на running-стадии (C2)"
```

---

## Self-Review

**Spec coverage:**
- A1 (dot→done) → Task 1 Step 1 (CSS) + Task 2 (trigger). ✓
- A2 (feed entrance) → Task 1 Step 2. ✓
- A3 (progress) — уже готов, не трогаем (Global Constraints). ✓
- A4 (active entrance) → Task 1 Step 1. ✓
- B1 (button morph) → Task 1 Step 3 (CSS) + Task 3 (markup/state). ✓
- B2 (comment pop/border) → Task 1 Step 3. ✓
- B3 (dialog flash) → Task 3 Step 2 (existing `dialog-flash`). ✓
- C1 (theme crossfade) → Task 1 Step 5. ✓
- C2 (thinking) → Task 1 Step 3 (CSS) + Task 4. ✓
- C3 (reconnect) → Task 1 Step 4. ✓
- D (connectors + travel) → Task 1 Step 1 + Task 2 markup/trigger. ✓
- Кроссскинность (goga/novacorps наследуют) — всё в base на токенах. ✓

**Placeholder scan:** полный код приведён во всех шагах; grep-проверки и ожидаемый вывод конкретны. Нет TBD. ✓

**Type/consistency:** классы согласованы между CSS и React — `just-done`, `dot-check`, `stage-connector` (Task1↔Task2); `btn.ok`/`btn-ripple`/`btn-label`/`btn-done` (Task1↔Task3); `dialog-flash` (base↔Task3); `thinking`/`td` (Task1↔Task4). Доступные имена кнопок сохранены через `.btn-label` + `aria-hidden` на декоре — существующие тесты не ломаются. ✓
