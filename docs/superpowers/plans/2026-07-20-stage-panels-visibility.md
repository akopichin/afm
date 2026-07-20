# Stage Panels Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Скрывать раздел plan у автономных стейджей и раздел dialog у стейджей, где диалог невозможен, — только для конкретного стейджа.

**Architecture:** Backend отдаёт в `/api/status` два per-stage флага — `stage_interactive` (статический конфиг флоу) и `stage_autonomous` (рантайм, по `autonomous.flag`). Frontend читает их в модель `Stage` и условно рендерит панели `plan`/`dialog` в `DashboardLayout`. Правила: plan показывается iff `!autonomous`; dialog — iff `interactive || autonomous`.

**Tech Stack:** Go (net/http, encoding/json) для backend; React 18 + TypeScript, Vite, Vitest + @testing-library/react, react-resizable-panels v4 для frontend.

## Global Constraints

- Go-версию в `go.mod` НЕ менять.
- Backend-тесты: `make test` (или `go test ./pkg/server/...`); линт: `make lint`.
- Frontend рабочая директория: `pkg/web/dashboard`. Тесты `npm run test`, типы `npm run typecheck`, сборка `npm run build`. Источник CSS — `public/style.css`; корневой `style.css`/`assets/`/`index.html` — build-артефакты (руками не править).
- Правила видимости: **plan** iff `!autonomous`; **dialog** iff `interactive || autonomous`. Когда стадия не выбрана (`selectedStage === null`) — обе панели показываются (нейтральное состояние, как сейчас).
- Не менять логику супервизора, запись `autonomous.flag`, содержимое plan/диалога, тип `Decision`, поллинг.
- Комментарии в коде и коммиты — на русском. Co-Authored-By не добавлять.

---

### Task 1: Backend — `stage_interactive` и `stage_autonomous` в `/api/status`

**Files:**
- Modify: `pkg/server/handlers.go` (функция `handleStatus`, ~строки 27-31)
- Modify: `pkg/server/server.go` (struct `Server` ~24-37, struct `Config` ~40-51, функция `New` ~53+)
- Modify: `cmd/afm/run.go` (вызов `server.New`, ~строки 233-249)
- Test: `pkg/server/handlers_test.go`

**Interfaces:**
- Produces: JSON-ответ `/api/status` дополнительно содержит `stage_interactive: {<id>: bool}` и `stage_autonomous: {<id>: bool}` (обе `omitempty`). Существующие поля (`flow_name`, `stage_order`, `stage_names`, `stages`, …) сохранены. `server.Config` получает поле `StageInteractive map[string]bool`.

- [ ] **Step 1: Написать падающий тест**

В `pkg/server/handlers_test.go` добавить:

```go
func TestHandleStatus_IncludesInteractiveAndAutonomous(t *testing.T) {
	srv, runDir := setupTestServer(t)
	srv.stageInteractive = map[string]bool{testStageID: true}
	// пометить стадию автономной
	if err := os.WriteFile(filepath.Join(runDir, testStageID, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatalf("write flag: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var resp statusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp.Stages[testStageID]; !ok {
		t.Error("embedded RunState: stage missing from status")
	}
	if !resp.StageInteractive[testStageID] {
		t.Errorf("stage_interactive[%q] = false, want true", testStageID)
	}
	if !resp.StageAutonomous[testStageID] {
		t.Errorf("stage_autonomous[%q] = false, want true", testStageID)
	}
}
```

- [ ] **Step 2: Запустить тест — убедиться, что не компилируется/падает**

Run: `go test ./pkg/server/ -run TestHandleStatus_IncludesInteractiveAndAutonomous`
Expected: FAIL — нет типа `statusResponse` и поля `srv.stageInteractive`.

- [ ] **Step 3: Добавить поле в Server и Config**

В `pkg/server/server.go` в struct `Server` добавить поле (рядом с `runDir`):

```go
	stageInteractive map[string]bool // id стадии → interactive (статический конфиг флоу)
```

В struct `Config` добавить:

```go
	StageInteractive map[string]bool
```

В функции `New` при инициализации `&Server{...}` добавить:

```go
		stageInteractive: cfg.StageInteractive,
```

- [ ] **Step 4: Расширить handleStatus**

В `pkg/server/handlers.go` заменить функцию `handleStatus` на:

```go
// statusResponse расширяет снапшот двумя per-stage картами для UI:
// stage_interactive (статический конфиг флоу) и stage_autonomous (рантайм,
// по наличию autonomous.flag в директории стадии).
type statusResponse struct {
	state.RunState
	StageInteractive map[string]bool `json:"stage_interactive,omitempty"`
	StageAutonomous  map[string]bool `json:"stage_autonomous,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	rs := s.store.Snapshot()
	autonomous := make(map[string]bool, len(rs.Stages))
	for id := range rs.Stages {
		if _, err := os.Stat(filepath.Join(s.runDir, id, "autonomous.flag")); err == nil {
			autonomous[id] = true
		}
	}
	resp := statusResponse{
		RunState:         rs,
		StageInteractive: s.stageInteractive,
		StageAutonomous:  autonomous,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
```

(Импорты `os`, `path/filepath`, `state` уже присутствуют в файле.)

- [ ] **Step 5: Запустить тесты — убедиться, что проходят**

Run: `go test ./pkg/server/`
Expected: PASS (включая существующие `TestHandleStatus`, `TestHandleStatus_IncludesStageNames`).

- [ ] **Step 6: Прокинуть карту interactive из run.go**

В `cmd/afm/run.go` перед вызовом `srv := server.New(server.Config{` (около строки 233) добавить построение карты:

```go
				stageInteractive := make(map[string]bool, len(f.Stages))
				for _, st := range f.Stages {
					stageInteractive[st.ID] = st.Interactive
				}
```

И в литерал `server.Config{...}` добавить поле:

```go
					StageInteractive: stageInteractive,
```

- [ ] **Step 7: Сборка и линт**

Run: `make build && make lint`
Expected: сборка успешна, линт без ошибок.

- [ ] **Step 8: Коммит**

```bash
git add pkg/server/handlers.go pkg/server/server.go pkg/server/handlers_test.go cmd/afm/run.go
git commit -m "feat(server): stage_interactive и stage_autonomous в /api/status"
```

---

### Task 2: Frontend — модель Stage и парсинг статуса

**Files:**
- Modify: `pkg/web/dashboard/src/types/stage.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts`
- Modify: `pkg/web/dashboard/src/app/App.tsx` (только литерал `NO_STAGE`, ~строка 58)
- Modify: `pkg/web/dashboard/src/components/footer/Footer.test.tsx` (литералы Stage, ~9-10)
- Modify: `pkg/web/dashboard/src/components/stages-list/StagesList.test.tsx` (литералы Stage, ~9-10)
- Modify: `pkg/web/dashboard/src/hooks/use-attention/use-attention.test.ts` (фабрика Stage, ~7)
- Test: `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts`

**Interfaces:**
- Consumes: JSON `/api/status` с `stage_interactive` / `stage_autonomous` (Task 1).
- Produces: тип `Stage` с обязательными полями `interactive: boolean`, `autonomous: boolean`. `useStatus` проставляет их из карт (дефолт `false`).

- [ ] **Step 1: Написать падающий тест**

В `pkg/web/dashboard/src/hooks/use-status/use-status.test.ts` добавить тест парсинга флагов. Сначала прочитай существующий файл, чтобы повторить его стиль вызова `normalizeStatus`/`useStatus`. Добавь тест-кейс, проверяющий: при ответе с `stage_interactive: { s1: true }` и `stage_autonomous: { s2: true }` стадия `s1` получает `interactive: true, autonomous: false`, стадия `s2` — `interactive: false, autonomous: true`, а при отсутствии карт обе — `false`. Пример ассерта (адаптируй под структуру файла):

```ts
test('парсит stage_interactive и stage_autonomous с дефолтом false', () => {
  const raw = {
    flow_name: 'demo',
    stage_order: ['s1', 's2', 's3'],
    stages: { s1: { status: 'running' }, s2: { status: 'pending' }, s3: { status: 'pending' } },
    stage_interactive: { s1: true },
    stage_autonomous: { s2: true },
  }
  const { stages } = normalizeStatus(raw)
  const byId = Object.fromEntries(stages.map((s) => [s.id, s]))
  expect(byId.s1.interactive).toBe(true)
  expect(byId.s1.autonomous).toBe(false)
  expect(byId.s2.interactive).toBe(false)
  expect(byId.s2.autonomous).toBe(true)
  expect(byId.s3.interactive).toBe(false)
  expect(byId.s3.autonomous).toBe(false)
})
```

Если `normalizeStatus` не экспортируется, экспортируй его из `use-status.ts` (named export) для теста.

- [ ] **Step 2: Запустить тест — убедиться, что падает**

Run: `npm run test -- use-status`
Expected: FAIL — `Stage` не имеет полей `interactive`/`autonomous`, `normalizeStatus` их не проставляет.

- [ ] **Step 3: Добавить поля в тип Stage**

В `src/types/stage.ts` в тип `Stage` добавить два обязательных поля:

```ts
export type Stage = {
  id: string
  name: string
  status: StageStatus
  updatedAt: string
  interactive: boolean
  autonomous: boolean
}
```

- [ ] **Step 4: Парсить флаги в useStatus**

В `src/hooks/use-status/use-status.ts`:

1. В `normalizeStatus` извлечь карты рядом с `namesObj`:

```ts
  const interactiveObj = isRecord(obj.stage_interactive) ? obj.stage_interactive : {}
  const autonomousObj = isRecord(obj.stage_autonomous) ? obj.stage_autonomous : {}
```

2. Передать флаги в `toStage`:

```ts
  const stages: Stage[] = order.map((id) =>
    toStage(id, stagesObj[id], namesObj[id], interactiveObj[id] === true, autonomousObj[id] === true),
  )
```

3. Обновить сигнатуру и тело `toStage`:

```ts
function toStage(
  id: string,
  raw: unknown,
  nameRaw: unknown,
  interactive: boolean,
  autonomous: boolean,
): Stage {
  const obj = isRecord(raw) ? raw : {}

  const status: StageStatus = isStageStatus(obj.status) ? obj.status : 'pending'
  const updatedAt = typeof obj.updated_at === 'string' ? obj.updated_at : ''
  const name = typeof nameRaw === 'string' ? nameRaw : ''

  return { id, name, status, updatedAt, interactive, autonomous }
}
```

4. Если `normalizeStatus` ещё не экспортирован — добавить `export` (для теста из Step 1).

- [ ] **Step 5: Обновить литералы Stage (иначе typecheck красный)**

`src/app/App.tsx` — `NO_STAGE`:

```ts
  const NO_STAGE: Stage = { id: '', name: '', status: 'pending', updatedAt: '', interactive: false, autonomous: false }
```

`src/components/footer/Footer.test.tsx` — оба литерала:

```ts
      { id: 's1', name: '', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: '', status: 'running', updatedAt: '', interactive: false, autonomous: false },
```

`src/components/stages-list/StagesList.test.tsx` — оба литерала:

```ts
      { id: 's1', name: 'Propose', status: 'done', updatedAt: '', interactive: false, autonomous: false },
      { id: 's2', name: 'Plan', status: 'running', updatedAt: '', interactive: false, autonomous: false },
```

`src/hooks/use-attention/use-attention.test.ts` — фабрика (строка ~7):

```ts
  ({ id: 's', name: 'n', status, updatedAt: '', interactive: false, autonomous: false })
```

Если в тесте `use-status.test.ts` есть ожидаемый объект `Stage` (около строки 37) — добавь в него `interactive`/`autonomous` соответственно ответу.

- [ ] **Step 6: Запустить тесты и типы — убедиться, что зелено**

Run: `npm run test && npm run typecheck`
Expected: все тесты PASS, типы без ошибок.

- [ ] **Step 7: Коммит**

```bash
git add src/types/stage.ts src/hooks/use-status/use-status.ts src/app/App.tsx src/components/footer/Footer.test.tsx src/components/stages-list/StagesList.test.tsx src/hooks/use-attention/use-attention.test.ts src/hooks/use-status/use-status.test.ts
git commit -m "feat(dashboard): модель Stage с interactive/autonomous из /api/status"
```

---

### Task 3: Frontend — условный рендер panel/dialog

**Files:**
- Modify: `pkg/web/dashboard/src/app/App.tsx` (вычисление видимости + пропсы в `DashboardLayout`)
- Modify: `pkg/web/dashboard/src/components/layout/DashboardLayout.tsx`
- Test: `pkg/web/dashboard/src/components/layout/DashboardLayout.test.tsx` (создать)

**Interfaces:**
- Consumes: `Stage.interactive` / `Stage.autonomous` (Task 2).
- Produces: `DashboardLayout` рендерит строки-панели `plan`/`dialog` только когда соответствующий проп не `null`; `defaultLayout` и storage-ключ строк зависят от набора присутствующих панелей.

- [ ] **Step 1: Написать падающий тест DashboardLayout**

Создать `src/components/layout/DashboardLayout.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { DashboardLayout } from './DashboardLayout'

function renderLayout(overrides: Partial<Record<'plan' | 'dialog', null>>) {
  render(
    <DashboardLayout
      stages={<div>STAGES</div>}
      stageHeader={<div>HEADER</div>}
      plan={overrides.plan === null ? null : <div>PLAN</div>}
      dialog={overrides.dialog === null ? null : <div>DIALOG</div>}
      log={<div>LOG</div>}
      feed={<div>FEED</div>}
    />,
  )
}

describe('DashboardLayout', () => {
  test('показывает все панели по умолчанию', () => {
    renderLayout({})
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    expect(screen.getByText('LOG')).toBeInTheDocument()
  })

  test('скрывает plan, когда plan=null', () => {
    renderLayout({ plan: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.getByText('DIALOG')).toBeInTheDocument()
    expect(screen.getByText('LOG')).toBeInTheDocument()
  })

  test('скрывает dialog, когда dialog=null', () => {
    renderLayout({ dialog: null })
    expect(screen.getByText('PLAN')).toBeInTheDocument()
    expect(screen.queryByText('DIALOG')).toBeNull()
    expect(screen.getByText('LOG')).toBeInTheDocument()
  })

  test('скрывает обе, когда plan=null и dialog=null', () => {
    renderLayout({ plan: null, dialog: null })
    expect(screen.queryByText('PLAN')).toBeNull()
    expect(screen.queryByText('DIALOG')).toBeNull()
    expect(screen.getByText('LOG')).toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Запустить тест — убедиться, что падает**

Run: `npm run test -- DashboardLayout`
Expected: FAIL — сейчас панели рендерятся всегда, `queryByText('PLAN')` при `plan=null` найдёт... на самом деле `null` не отрендерит текст, но Panel всё равно смонтирован пустым; главное — тесты со скрытием падают, т.к. панель `plan`/`dialog` присутствует как пустой Panel и layout не сокращён. Зафиксируй фактический вывод как RED.

- [ ] **Step 3: Условный рендер строк в DashboardLayout**

В `src/components/layout/DashboardLayout.tsx` заменить построение вертикальной группы. Вместо фиксированных трёх `<Panel>` собрать список присутствующих строк и рендерить панели с разделителями между ними; `defaultLayout` и storage-ключ — только по присутствующим id.

Заменить константу `DEFAULT_ROWS` и блок вертикальной `Group` на:

```tsx
// Доли строк для полного набора; при скрытии панелей берутся только присутствующие id.
const ROW_SHARES: Record<string, number> = { plan: 30, dialog: 45, log: 25 }

// ...внутри DashboardLayout, вместо прежнего useSavedLayout('afm-rows', ...) и Group:

  // Присутствующие строки в порядке plan → dialog → log. plan/dialog опускаются,
  // когда проп null (автономная/неинтерактивная стадия). log присутствует всегда.
  const rowPanels: Array<{ id: string; node: ReactNode; minSize: string }> = []
  if (plan !== null) rowPanels.push({ id: 'plan', node: plan, minSize: '15' })
  if (dialog !== null) rowPanels.push({ id: 'dialog', node: dialog, minSize: '15' })
  rowPanels.push({ id: 'log', node: log, minSize: '10' })

  // Storage-ключ и defaultLayout зависят от набора панелей: у каждого набора
  // своя сохранённая раскладка, ключи всегда совпадают с присутствующими id
  // (иначе react-resizable-panels распределяет доли по несуществующим панелям).
  const rowsStorageId = `afm-rows-${rowPanels.map((p) => p.id).join('-')}`
  const rowsFallback: Layout = Object.fromEntries(rowPanels.map((p) => [p.id, ROW_SHARES[p.id]]))
  const rows = useSavedLayout(rowsStorageId, rowsFallback)
```

И заменить вертикальную `Group` на рендер из `rowPanels` с разделителями между панелями:

```tsx
          <Group
            orientation="vertical"
            className="detail-rows"
            defaultLayout={rows.defaultLayout ?? rowsFallback}
            onLayoutChanged={rows.onLayoutChanged}
          >
            {rowPanels.map((p, i) => (
              <Fragment key={p.id}>
                {i > 0 && <Separator className="resize-handle resize-handle-h" />}
                <Panel id={p.id} minSize={p.minSize}>
                  {p.node}
                </Panel>
              </Fragment>
            ))}
          </Group>
```

Добавить в импорт из `'react'`: `Fragment` (и `type ReactNode` уже импортирован). Удалить старую константу `DEFAULT_ROWS`, если она больше не используется. `DEFAULT_COLS` и горизонтальная группа — без изменений.

- [ ] **Step 4: Запустить тест DashboardLayout — PASS**

Run: `npm run test -- DashboardLayout`
Expected: PASS (4 теста).

- [ ] **Step 5: Вычислить видимость в App и передать пропсы**

В `src/app/App.tsx` перед `return` добавить вычисление (рядом с `stageForPanels`):

```ts
  // Видимость панелей для выбранной стадии. Когда стадия не выбрана — показываем обе
  // (нейтральное состояние). plan скрыт у автономной стадии (нет plan.md); dialog
  // скрыт только когда диалог невозможен: не interactive и не autonomous (автономный
  // трек диалоговый даже при interactive:false).
  const showPlan = selectedStage === null || !selectedStage.autonomous
  const showDialog = selectedStage === null || selectedStage.interactive || selectedStage.autonomous
```

Заменить пропсы `plan`/`dialog` в `<DashboardLayout ...>`:

```tsx
            plan={showPlan ? <PlanPanel stage={stageForPanels} attention={attention.kind === 'plan'} /> : null}
            dialog={showDialog ? <DialogChannel stage={stageForPanels} attention={attention.kind === 'dialog'} /> : null}
```

- [ ] **Step 6: Прогнать тесты и типы**

Run: `npm run test && npm run typecheck`
Expected: все PASS, типы чисты (включая существующий `App.test.tsx`).

- [ ] **Step 7: Сборка**

Run: `npm run build`
Expected: успешна; обновлены корневой `style.css`/`assets/`/`index.html`.

- [ ] **Step 8: Визуальная проверка**

Запустить afm-ран с дашбордом (или dev-сервер с моком `/api/status`) и убедиться в обеих темах (novacorps + goga):
- автономная стадия (есть `autonomous.flag`) — раздел **plan** скрыт, **dialog** виден (у автономной стадии есть диалог);
- стадия `interactive:false` без autonomous — раздел **dialog** скрыт, **plan** виден;
- стадия `interactive:true` — обе видны;
- при переключении между стадиями раскладка строк не ломается, разделители соответствуют присутствующим панелям.

- [ ] **Step 9: Коммит**

```bash
git add src/app/App.tsx src/components/layout/DashboardLayout.tsx src/components/layout/DashboardLayout.test.tsx style.css assets index.html
git commit -m "feat(dashboard): скрытие panel/dialog по типу стадии (autonomous/interactive)"
```

---

## Self-Review

- **Spec coverage:** backend-флаги `stage_interactive`/`stage_autonomous` (Task 1), модель Stage + парсинг (Task 2), правила видимости plan/dialog + условный рендер + персистентность лейаута (Task 3), тайминг (Global Constraints: null-стадия показывает обе), тесты backend/frontend/визуал — все разделы спеки покрыты.
- **Placeholder scan:** плейсхолдеров нет; код и команды приведены. Единственные «прочитай существующий файл» указания (use-status.test стиль) сопровождены конкретным примером ассерта и точными изменениями.
- **Type consistency:** поля `interactive`/`autonomous` (обязательные) согласованы между `Stage`, `toStage`, литералами и тестами; `statusResponse`/`stage_interactive`/`stage_autonomous` — между backend, тестом и парсером; пропсы `plan`/`dialog` (nullable) и `rowsStorageId`/`ROW_SHARES` согласованы между App, DashboardLayout и его тестом.
