# Plan: `web-dash`

> Источник: дизайн-документ `docs/arch/web-dash.md` (прошёл `design-review`, вердикт **success**, 17 фиксов,
> `goga lint` = exit 0 по 16 клеткам). Эталон поведенческого паритета — `app.js`.
> План скомпилирован навыком `goga-plan` → `goga-plan-by-design` (язык проекта `javascript`, клетки на TypeScript —
> авторитетный TS-источник: `.goga/usages/cooks/typescript.md`).

## Purpose

Переписать `pkg/web/dashboard` на React/Vite/TypeScript SPA (статическая сборка, без SSR) с полным
поведенческим паритетом текущему `app.js`. Реализация — **15 новых клеток** внутри `src/`, дочерних по отношению
к существующей клетке `.` (`pkg/web/dashboard`, контракт `DashboardAssets`):

- `src/types` (лист) — общие типы данных;
- 5 хуков: `src/hooks/use-status`, `src/hooks/use-event-feed`, `src/hooks/use-usage-data`,
  `src/hooks/use-stage-log`, `src/hooks/use-elapsed`;
- 8 компонентов: `src/components/flow-header`, `src/components/stages-list`, `src/components/log-panel`,
  `src/components/event-feed`, `src/components/plan-panel`, `src/components/dialog-channel`,
  `src/components/footer`, `src/components/consumption-panel`;
- корень `src/app`.

Стратегия — декомпозиция дизайн-решения в ralphex-задачи в порядке **листья → корень**; каждая клетка
реализуется полностью (infra → coding) до перехода к следующей; каждая coding-задача следует TDD
(contract tests → code → verify → logic tests → debug → re-verify → lint). Клетка `.` этим планом **не
изменяется** — её корневые конфиги уточняются на стадии `design/apply`; критичные сборочные ограничения
вынесены в `## Validation Commands` и `## Completion Criteria`.

## Context

### Contract Surface

> `location` (имя файла) — всегда на уровне директории клетки (без поддиректорий), по DSL `goga-cell`.
> Facade каждой клетки — barrel `index.ts`: все контрактные экспорты идут через него. Для TypeScript
> действуют правила `cooks/typescript.md` (PascalCase-типы/компоненты, camelCase-хуки, kebab-case `.ts`,
> именованные экспорты, strict).

**Клетка `src/types`**
- `Stage()` — Entity (только properties); `location: stage.ts`. Свойства: `id -> string`, `name -> string`,
  `status -> StageStatus` (10-значный union), `updatedAt -> string`.
- `AfmEvent()` — Entity; `location: afm-event.ts`. Свойства: `type -> string`, `payload -> unknown`,
  `stageId -> string`, `timestamp -> string`.
- `UsagePoint()` — Entity; `location: usage-point.ts`. Свойства: `timestamp -> string`, `metric -> 'tokens' | 'cost' | 'kb'`,
  `value -> number`.
- `DialogQuestion()` — Entity; `location: dialog-question.ts`. Свойства: `id -> string`, `phase -> string`,
  `text -> string`, `options -> Array<string>`, `allowCustom -> boolean`.
- `DialogAnswer()` — Entity; `location: dialog-answer.ts`. Свойства: `questionId -> string`, `value -> string`.
- `PlanComment()` — Entity; `location: plan-comment.ts`. Свойства: `line -> number`, `text -> string`.
- `LogEntry()` — Entity; `location: log-entry.ts`. Свойства: `timestamp -> string`, `message -> string`,
  `level -> 'debug' | 'info' | 'warn' | 'error'`.
- Производный экспорт контракта: `StageStatus` (union из 10 значений) и `STAGE_STATUSES` (readonly-массив
  значений, используется хуком `use-status` для проверки внешнего JSON — см. `dashboard-data-types`).

**Клетка `src/hooks/use-status`**
- `useStatus() -> status: { flowName: string, stages: Stage[], startedAt: string, refresh: () => void }` —
  Routine; `location: use-status.ts`. Импортирует `Stage` + `dashboard-data-types` из `src/types`.

**Клетка `src/hooks/use-event-feed`**
- `useEventFeed(url: string) -> feed: { events: AfmEvent[], connected: boolean }` — Routine;
  `location: use-event-feed.ts`. Импортирует `AfmEvent` + `dashboard-data-types` из `src/types`.

**Клетка `src/hooks/use-usage-data`**
- `useUsageData(metric: string, stageFilter: string | null) -> points: UsagePoint[]` — Routine;
  `location: use-usage-data.ts`. Импортирует `UsagePoint` + `dashboard-data-types` из `src/types`.

**Клетка `src/hooks/use-stage-log`**
- `useStageLog(stageId: string | null) -> entries: LogEntry[]` — Routine; `location: use-stage-log.ts`.
  Импортирует `LogEntry` + `dashboard-data-types` из `src/types`.

**Клетка `src/hooks/use-elapsed`**
- `useElapsed(startedAt: string) -> elapsedMs: number` — Routine; `location: use-elapsed.ts`. Без Imports.

**Клетка `src/components/flow-header`**
- `FlowHeader(flowName: string, connected: boolean) -> element: ReactElement` — Routine;
  `location: FlowHeader.tsx`. Без Imports.

**Клетка `src/components/stages-list`**
- `StagesList(stages: Stage[], selectedStageId: string | null, onSelect: (stageId: string) => void) -> element: ReactElement` —
  Routine; `location: StagesList.tsx`. Импортирует `Stage` + `dashboard-data-types` из `src/types`.

**Клетка `src/components/log-panel`**
- `LogPanel(entries: LogEntry[]) -> element: ReactElement` — Routine; `location: LogPanel.tsx`.
  Импортирует `LogEntry` + `dashboard-data-types` из `src/types`.

**Клетка `src/components/event-feed`**
- `EventFeedPanel(events: AfmEvent[]) -> element: ReactElement` — Routine; `location: EventFeedPanel.tsx`.
  Импортирует `AfmEvent` + `dashboard-data-types` из `src/types`.

**Клетка `src/components/plan-panel`**
- `MarkdownRenderer(source: string) -> element: ReactElement` — Routine; `location: MarkdownRenderer.tsx`.
  Обёртка над `markdown-it`.
- `PlanPanel(stage: Stage) -> element: ReactElement` — Entity (properties + methods); `location: PlanPanel.tsx`.
  Свойства: `planMarkdown -> string`, `comments -> PlanComment[]`. Методы: `approve() -> done:Promise<void>`,
  `sendRevision() -> done:Promise<void>`, `retry() -> done:Promise<void>`. Импортирует `Stage`, `PlanComment`
  + `dashboard-data-types` из `src/types`.

**Клетка `src/components/dialog-channel`**
- `DialogChannel(stage: Stage) -> element: ReactElement` — Entity (properties + methods);
  `location: DialogChannel.tsx`. Свойства: `history -> DialogQuestion[]`, `currentQuestion -> DialogQuestion | null`.
  Методы: `answer(questionId: string, value: string, fromOptions: boolean) -> done:Promise<void>`,
  `cancel() -> done:Promise<void>`. Импортирует `Stage`, `DialogQuestion` + `dashboard-data-types` из `src/types`.

**Клетка `src/components/footer`**
- `Footer(stages: Stage[], elapsedMs: number) -> element: ReactElement` — Routine; `location: Footer.tsx`.
  Импортирует `Stage` + `dashboard-data-types` из `src/types`.

**Клетка `src/components/consumption-panel`**
- `ConsumptionPanel(stages: Stage[]) -> element: ReactElement` — Entity (properties + methods);
  `location: ConsumptionPanel.tsx`. Свойства: `open -> boolean`, `metric -> 'tokens' | 'cost' | 'kb'`,
  `stageFilter -> string | null`, `points -> UsagePoint[]`. Методы: `switchMetric(metric)`, `setStageFilter(stageId)`.
  Импортирует `Stage`, `UsagePoint` + `dashboard-data-types` из `src/types` и `useUsageData` + `usage-data`
  из `src/hooks/use-usage-data`.

**Клетка `src/app`**
- `App() -> element: ReactElement` — Routine; `location: App.tsx`. Импортирует 8 компонентов, 4 хука и `Stage`
  (+ `dashboard-data-types`) из `src/types`. Корневая композиция.

### Re-exports

Facade-обязательство (именованные экспорты через barrel `index.ts` каждой клетки):

- `src/types` → `Stage`, `StageStatus`, `STAGE_STATUSES`, `AfmEvent`, `UsagePoint`, `DialogQuestion`,
  `DialogAnswer`, `PlanComment`, `LogEntry`.
- `src/hooks/use-status` → `useStatus` (и `FlowStatus` — производный тип ответа, реэкспортируется для потребителей).
- `src/hooks/use-event-feed` → `useEventFeed`.
- `src/hooks/use-usage-data` → `useUsageData`.
- `src/hooks/use-stage-log` → `useStageLog`.
- `src/hooks/use-elapsed` → `useElapsed`.
- `src/components/*` → соответствующий компонент (`FlowHeader`, `StagesList`, `LogPanel`, `EventFeedPanel`,
  `PlanPanel`, `MarkdownRenderer`, `DialogChannel`, `Footer`, `ConsumptionPanel`).
- `src/app` → `App`.

Иерархия: источники реэкспорта находятся на более низком уровне ФС, чем потребители (листья → корень), циклов нет.

### Usages Context

Глобальные практики (проектные, `.goga/usages/`):
- `conventions` (`.goga/usages/conventions.md`) — правила написания кода и тестов (ESM, async/await, JSDoc,
  null-safety `??`, DI). Действует как базовый конвеншен.
- `typescript` (`.goga/usages/cooks/typescript.md`) — **авторитетный TS-источник**: strict, без `any` (кроме
  единственной точки приведения внешнего JSON), PascalCase-типы, kebab-case `.ts`, PascalCase `.tsx`.
- `react` (`.goga/usages/cooks/react.md`) — функциональные компоненты + хуки, без классов/Redux/Zustand/Context;
  cancelled-флаг под StrictMode; **раздел markdown-it** — переход с вендоренного `public/markdown-it.min.js`
  на npm-зависимость `markdown-it`, конфиг `{ html: false, linkify: true }`, экземпляр один на модуль.
- `vitest` (`.goga/usages/cooks/vitest.md`) — Vitest вместо Jest (точечное отступление от `conventions`,
  только внутри dashboard), jsdom, мок `fetch`/`WebSocket` на границе.
- `vite` (`.goga/usages/cooks/vite.md`) — конфигурация сборки; критичные ограничения в `## Validation Commands`.

### Imported Usages

- `dashboard-data-types` — из клетки `src/types`, путь `src/types/.usages/dashboard-data-types.md`.
  Соответствие источников данных `app.js` (`/api/status`, `/api/stages/{id}/{log,plan,dialog}`, `/api/usage`,
  WS `/ws`), полный набор из 10 статусов, импорт типов через barrel, единая точка приведения внешнего JSON.
  Потребляется всеми хуками и компонентами, импортирующими типы из `src/types`.
- `usage-data` — из клетки `src/hooks/use-usage-data`, путь `src/hooks/use-usage-data/.usages/usage-data.md`.
  Паттерн потребления `useUsageData`, проверка доступности метрики cost (`useUsageData('cost', null)` →
  интерпретация пустого ответа потребителем). Потребляется только `consumption-panel`.

### Local Usages

Локальные usage-файлы клетки (создаются в infra-задачах; Status — новые, переносятся из дизайн-документа
дословно):

| Файл | Категория | Связанные сущности | Создаётся в задаче |
|---|---|---|---|
| `src/types/.usages/dashboard-data-types.md` | источники данных / типы | все потребители типов | Task 1 |
| `src/hooks/use-status/.usages/status.md` | поллинг `/api/status` | `useStatus` | Task 3 |
| `src/hooks/use-event-feed/.usages/event-feed.md` | WS-лента + backoff | `useEventFeed` | Task 5 |
| `src/hooks/use-usage-data/.usages/usage-data.md` | поллинг `/api/usage` | `useUsageData` | Task 7 |
| `src/hooks/use-stage-log/.usages/stage-log.md` | поллинг лога стадии | `useStageLog` | Task 9 |
| `src/hooks/use-elapsed/.usages/elapsed.md` | секундомер 1с | `useElapsed` | Task 11 |
| `src/components/flow-header/.usages/flow-header.md` | шапка | `FlowHeader` | Task 13 |
| `src/components/stages-list/.usages/stages-list.md` | список стадий | `StagesList` | Task 15 |
| `src/components/log-panel/.usages/log-panel.md` | окно лога | `LogPanel` | Task 17 |
| `src/components/event-feed/.usages/event-feed-panel.md` | лента событий | `EventFeedPanel` | Task 19 |
| `src/components/plan-panel/.usages/plan-panel.md` | панель плана + markdown-it | `PlanPanel`, `MarkdownRenderer` | Task 21 |
| `src/components/dialog-channel/.usages/dialog-channel.md` | диалоговый канал | `DialogChannel` | Task 24 |
| `src/components/footer/.usages/footer.md` | футер | `Footer` | Task 26 |
| `src/components/consumption-panel/.usages/consumption-panel.md` | панель потребления | `ConsumptionPanel` | Task 28 |
| `src/app/.usages/app-composition.md` | корневая композиция | `App` | Task 30 |

### External Dependencies

Рантайм: `react` (>=18), `react-dom` (>=18), `markdown-it` (>=14) — единственная сторонняя рантайм-библиотека
рендера вместе с React. Dev: `vite` (>=5), `@vitejs/plugin-react`, `typescript` (>=5, strict),
`vitest` (>=2), `@testing-library/react`, `@testing-library/jest-dom`, `jsdom`. Нативные `WebSocket` и `fetch`.
Без SSR, без state-менеджеров (Redux/Zustand/Context), без charting-библиотек (SVG собственный).

## Facts

Дословно из дизайн-документа и сверки с `app.js` (поведенческий паритет):

- Эндпоинты: состояние флоу — `GET /api/status` (поля `flow_name`, `started_at`, `stages` — объект по id,
  `stage_order`, `stage_names`); лог стадии — `GET /api/stages/{id}/log` (**plain text**, поллинг 3000мс, не
  фильтрация WS); план — `GET /api/stages/{id}/plan` (**plain text**); диалог — `GET /api/stages/{id}/dialog`
  (JSON-массив записей с `phase`/`type`/`question`/`answer`/`id`/`options`/`allow_custom`); потребление —
  `GET /api/usage?metric=...&stage=...` (массив `{ timeBucket, value }`); WS — `/ws`.
- Действия: `POST /api/stages/{id}/{approve,revise,retry}`, `POST /api/stages/{id}/dialog/answer` (тело
  `{ id, phase, answer, from_options }`), `POST /api/stages/{id}/dialog/cancel`.
- WS-backoff: начальная задержка 1000мс, удвоение, потолок 10000мс, сброс к 1000мс после успешного открытия
  (`app.js`: `reconnectDelay = Math.min(reconnectDelay * 2, 10000)`, сброс `reconnectDelay = 1000`).
- Лента событий ограничена 200 последними записями.
- Полный набор из 10 статусов стадии: `pending`, `planning`, `awaiting_approval`, `revising`, `ready`,
  `running`, `done`, `failed`, `retrying`, `awaiting_user_input` (`done` — завершена, не `completed`).
  Прогресс считается по статусу `done`.
- Активные статусы (автовыбор стадии / `ACTIVE_STATUSES` в `app.js`): `planning`, `running`, `revising`,
  `retrying`, `awaiting_user_input`. Fallback автовыбора — первая стадия со статусом `failed`.
- Скрытие метрики «деньги»: опция `cost` скрывается, если `GET /api/usage?metric=cost` → пустой массив;
  если `cost` был активен и оказался недоступен — откат на `tokens`.
- elapsed — собственный 1с интервал (`useElapsed`), не привязан к циклу поллинга; при пустом/невалидном
  `startedAt` → `elapsedMs = 0`, отображение «--» — ответственность потребителя (футера).
- `use-stage-log` опрашивает лог **безусловно** (в `app.js` таймер лога ставился только для активных статусов) —
  осознанное упрощение, зафиксировано в дизайн-документе как известное допустимое отклонение.
- WS — канал обновления состояния: по значимым событиям (смена статуса стадии, `approved`, `revised`,
  `retry_scheduled`, `retry_exhausted`, `manual_retry`, `ask_user`, `user_answered`, `agent_completed`) корневая
  композиция вызывает `useStatus().refresh()`.
- `React.StrictMode` в dev монтирует эффекты дважды → флаг отмены (`cancelledRef`) и очистка таймеров/сокетов
  обязательны во всех хуках.
- DOM-якоря тем (сохранить для совместимости с `novacorps`/`goga`): `flow-name`, `ws-status` (тексты `LINK`/`OFFLINE`),
  `stages-list` (+ класс `selected`), `usage-panel`, `usage-toggle` (+ класс `open`).

## Gap Analysis

> Клетки уже материализованы в `src/` стадией `apply`; `goga lint` = exit 0 по 16 клеткам. ralphex-план описывает
> **целевое состояние по контракту** (исправленному дизайн-документу). Конкретные расхождения текущего `src/`
> с контрактом зафиксированы ниже и **не правятся** на этой стадии — их разрешает `fix-changes` после `plan-review`.

- **Отсутствуют тесты у 5 клеток**: `src/types`, `src/components/log-panel`, `src/components/event-feed`,
  `src/components/plan-panel`, `src/components/dialog-channel` (нет `*.test.ts(x)`). Остальные 10 клеток
  (`src/app`, `src/components/{consumption-panel,flow-header,footer,stages-list}`, `src/hooks/{use-elapsed,use-event-feed,use-stage-log,use-status,use-usage-data}`) тесты имеют.
- **Корневые конфиги уже соответствуют контракту** (вне правок плана, клетка `.`): `vite.config.ts` —
  `build.outDir='.'`, `build.emptyOutDir=false`, `base='./'`, `assetsDir='assets'`, конфиг Vitest (jsdom/globals/setupFiles);
  `package.json` — `"type":"module"`, deps `react`/`react-dom`/`markdown-it`, devDeps `vite`/`@vitejs/plugin-react`/
  `typescript`/`vitest`/`@testing-library/react` (+`@testing-library/jest-dom`, `jsdom`, `@types/*`), скрипты
  `clean:assets` (`rm -rf assets`) и `build` (вызывает `clean:assets` перед `vite build`); `index.html` — без
  вендоренного `<script src="markdown-it.min.js">`, `<div id="root">`, `<body class="theme-novacorps">`.
- **Вендоренный `public/markdown-it.min.js` уже удалён** (`public/` содержит только `favicon.svg`,
  `quarium-logo.png`, `style.css`, `style-goga.css`).
- **`use-status` (эталон реализации)**: текущий `src/hooks/use-status/use-status.ts` уже соответствует контракту —
  `POLL_INTERVAL_MS = 3000`, `cancelledRef`, `refresh()`, `normalizeStatus` (`stage_order`/`stage_names`/`flow_name`/
  `started_at`), `STAGE_STATUSES`-проверка внешнего JSON. Используется как референс при реализации.
- **Известное отклонение от `app.js`** (по контракту, не дефект): `use-status` опрашивает `/api/status` по
  таймеру 3000мс, тогда как `app.js` делает `loadState` событийно (WS + задержки 300/1500мс при реконнекте).
  Дизайн-документ фиксирует 3000мс-поллинг + `refresh()` как контракт (`design-review` — success).

---

## Tasks

> **Правило порядка**: задачи каждой клетки выполняются до начала следующей; внутри coding-задачи contract-тесты
> пишутся первыми (TDD). Порядок клеток — листья → корень (`types` → хуки → компоненты → `app`).
> **CRITICAL: `CODEMANIFEST`-файлы — read-only.** Контракт не правится; если реализация расходится с контрактом —
> правится реализация, никогда контракт.

### Task 1: `src/types` — структура клетки и barrel (infrastructure)

Клетка-лист без зависимостей. Создать структуру клетки: barrel `index.ts`, реэкспортирующий все 7 типов плюс
`StageStatus` и `STAGE_STATUSES`; локальный usage-файл `dashboard-data-types.md`. CODEMANIFEST клетки — read-only
(уже существует). `location` каждого типа — отдельный файл на уровне директории клетки.

**Usages relevant to this task:**
- `conventions`: ESM-импорты, именованные экспорты; barrel — единая точка импорта типов.
- `typescript`: PascalCase-типы без префикса `I`, union-типы для статусов/метрик/уровней; kebab-case `.ts`.
- `dashboard-data-types` (создаваемый локальный usage): соответствие источников `app.js`, полный набор статусов,
  единая точка приведения внешнего JSON.

**CRITICAL: `CODEMANIFEST` — read-only. Не модифицировать.**

- [ ] Создать barrel `src/types/index.ts`: именованные реэкспорты `Stage`, `StageStatus`, `STAGE_STATUSES`,
  `AfmEvent`, `UsagePoint`, `DialogQuestion`, `DialogAnswer`, `PlanComment`, `LogEntry` (по одному `export ...`
  на файл/производный тип).
- [ ] Создать `src/types/.usages/dashboard-data-types.md` (содержание дословно из дизайн-документа: Domain,
  «Источники данных (соответствие app.js)», «Импорт типов», «Типизация ответа fetch», «Использование union-типов»).
- [ ] Verify facade accessibility: `node --input-type=module -e "import('./src/types/index.ts')"` (после сборки TS)
  или `npx tsc --noEmit` — без ошибок, все 9 экспортов разрешимы.
- [ ] Lint: `/tmp/goga-venv/bin/goga lint` — 0 ошибок (CODEMANIFEST `src/types` не тронут).

### Task 2: `src/types` — определения типов (TDD coding)

Реализовать 7 plain-data типов (без поведения) в их `location`-файлах + производный `StageStatus`/`STAGE_STATUSES`.
Типы описывают форму уже распарсенного JSON; единственная точка приведения типа — в вызывающем хуке, не здесь.

**Usages relevant to this task:**
- `typescript`: strict, без `any`; union-литералы для `Stage.status`/`UsagePoint.metric`/`LogEntry.level`.
- `dashboard-data-types`: набор значений статусов (10), соответствие полей сервера.

- [ ] **Contract tests**: `src/types/stage.test.ts` (или общий `src/types/types.test.ts`) — тип `Stage` имеет поля
  `id/name/status/updatedAt`; `StageStatus` — union ровно из 10 значений; `STAGE_STATUSES` — readonly-массив
  содержит все 10 (runtime-проверка `as readonly string[]`); `UsagePoint.metric` ∈ tokens|cost|kb;
  `LogEntry.level` ∈ debug|info|warn|error. (Ожидаемо падают — типов ещё нет.)
- [ ] **Code**: `stage.ts` — `StageStatus` (union 10 значений) + `Stage` + `STAGE_STATUSES` (readonly array).
- [ ] **Code**: `afm-event.ts` (`AfmEvent`: `type`, `payload: unknown`, `stageId`, `timestamp`),
  `usage-point.ts` (`timestamp`, `metric`, `value`), `dialog-question.ts` (`id`, `phase`, `text`,
  `options: Array<string>`, `allowCustom`), `dialog-answer.ts` (`questionId`, `value`),
  `plan-comment.ts` (`line: number`, `text`), `log-entry.ts` (`timestamp`, `message`, `level`).
- [ ] **Interface verification**: `npx vitest run src/types` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — объект каждого типа компилируется и соответствует форме; граница — пустые/нулевые
  значения полей (`''`, `0`, `[]`) допустимы типом; негатив — значение статуса вне union даёт ошибку типа
  (`tsc --noEmit`).
- [ ] **Debugging**: `npx vitest run src/types && npx tsc --noEmit` — фиксить реализацию, не тесты, до зелёного.
- [ ] **Contract re-verification**: barrel реэкспортирует все 9 имён; union-наборы полны (10 статусов, 3 метрики,
  4 уровня); поведение — только форма данных, без логики.
- [ ] **Lint**: `npx tsc --noEmit` — без ошибок; `goga lint` — 0 ошибок.

### Task 3: `src/hooks/use-status` — структура клетки и barrel (infrastructure)

Хук состояния флоу. Создать barrel `src/hooks/use-status/index.ts` (экспорт `useStatus` и `FlowStatus`) и локальный
usage `status.md`. Imports: `Stage`, `dashboard-data-types` из `src/types` (клетка-зависимость уже реализована — Task 1–2).

**Usages relevant to this task:**
- `react`: структура хука, cancelled-флаг под StrictMode.
- `typescript`: единственная точка приведения внешнего JSON.
- `dashboard-data-types`: источник `/api/status`, полный набор статусов.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/hooks/use-status/index.ts`: `export { useStatus } from './use-status'` + `export type { FlowStatus }`.
- [ ] Создать `src/hooks/use-status/.usages/status.md` (дословно из дизайн-документа: Domain, «Базовое использование»,
  «Особенности»).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 4: `src/hooks/use-status` — реализация `useStatus` (TDD coding)

**Контракт** (`useStatus() -> { flowName, stages, startedAt, refresh }`): периодический опрос `GET /api/status`
плюс немедленное обновление через `refresh()`. Поля: `flowName` ← `flow_name`, `stages` — нормализованный массив
из объекта по id в порядке `stage_order` (fallback `Object.keys(stages).sort()`), имена ← `stage_names`, время ←
`updated_at`; `startedAt` ← `started_at`; `refresh` — триггер немедленного ре-запроса (источник правды для
WS-обновлений). `stages` — пустой массив до первого ответа.

**Algorithm (дословно):**
1. При монтировании выполнить запрос `GET /api/status`.
2. Распарсить JSON, нормализовать к форме status: `stages` — объект по id → массив в порядке `stage_order`
   (fallback `Object.keys(stages).sort()`), имена — из `stage_names`, время — из `updated_at`.
3. Если хук не отменён — обновить состояние.
4. Запланировать повторный опрос по таймеру (3000мс).
5. При размонтировании — выставить флаг отмены и остановить таймер.

**Requirements:** флаг отмены для игнорирования устаревших ответов; `stages` — пустой массив, не undefined/null;
`refresh()` инициирует внеочередной запрос, не дожидаясь таймера. **Constraints:** не запускать параллельные
пересекающиеся запросы поверх уже запланированного таймера.

**Usages relevant to this task:**
- `react`: cancelled-флаг под StrictMode; `useEffect` + `useRef` + `useState`.
- `typescript`: единственная точка приведения (`const data: unknown = await response.json()` → `normalizeStatus`).
- `dashboard-data-types`: набор статусов, нормализация.
- `vitest`: мок `fetch` на границе.

- [ ] **Contract tests**: `src/hooks/use-status/use-status.test.ts` — хук возвращает объект с ключами
  `flowName`/`stages`/`startedAt`/`refresh`; `refresh` — функция; до первого ответа `stages` === `[]`,
  `flowName` === `''`. (Падают.)
- [ ] **Code**: `src/hooks/use-status/use-status.ts` — `FlowStatus`-тип, `useStatus`, `normalizeStatus`,
  валидация `STAGE_STATUSES` для внешнего `status`; `cancelledRef` guard; `POLL_INTERVAL_MS = 3000`.
- [ ] **Interface verification**: `npx vitest run src/hooks/use-status` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — mock `fetch` отдаёт `{ flow_name, stages, stage_order, stage_names, started_at }`,
  хук нормализует в правильный порядок с именами; `refresh()` внеочередной запрос; негатив — `fetch` бросает /
  `!response.ok` — состояние не падает, остаётся прежним; граница — пустой `stages`/`stage_order`,
  некорректный `status` → fallback `'pending'`.
- [ ] **Debugging**: `npx vitest run src/hooks/use-status` — фиксить реализацию до зелёного.
- [ ] **Contract re-verification**: сигнатура `{ flowName, stages, startedAt, refresh }`; 3000мс-поллинг +
  `refresh`; cancelled-guard; пустой массив по умолчанию.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 5: `src/hooks/use-event-feed` — структура клетки и barrel (infrastructure)

Хук WS-подписки с backoff. Создать barrel (`useEventFeed`) и usage `event-feed.md`. Imports: `AfmEvent`,
`dashboard-data-types` из `src/types`.

**Usages relevant to this task:**
- `react`: структура WS-хука с экспоненциальным backoff; cancelled-флаг.
- `typescript`: единственная точка приведения данных входящего сообщения.
- `dashboard-data-types`: типы WS-событий, их роль как канала обновления состояния.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/hooks/use-event-feed/index.ts` (`export { useEventFeed }`).
- [ ] Создать `src/hooks/use-event-feed/.usages/event-feed.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 6: `src/hooks/use-event-feed` — реализация `useEventFeed` (TDD coding)

**Контракт** (`useEventFeed(url) -> { events: AfmEvent[], connected: boolean }`): WebSocket с автопереключением,
накопленные события и статус соединения. `connected` — true при открытом соединении.

**Algorithm (дословно):**
1. При монтировании открыть WebSocket по `url`.
2. При открытии — `connected` true, сброс задержки переподключения.
3. При входящем сообщении — распарсить JSON, привести к `AfmEvent`, добавить в `events`.
4. При закрытии — `connected` false; если не отменён — запланировать переподключение, удвоить задержку с
   ограничением сверху.
5. При размонтировании — отмена, остановка таймера, закрытие соединения.

**Requirements:** начальная задержка 1000мс, удвоение, потолок 10000мс; сброс к 1000мс после успешного открытия;
единственная точка приведения типа; `events` ограничены 200 последними записями. **Constraints:** не открывать
более одного активного соединения на один вызов хука.

**Usages relevant to this task:**
- `react`: WS-хук с backoff (референс — раздел «WebSocket-хук» в `react`); cancelled-флаг + очистка таймера/сокета.
- `typescript`: приведение данных сообщения к `AfmEvent`.
- `dashboard-data-types`: поля `AfmEvent` (`stageId` ← `stage_id` сервера).
- `vitest`: мок `WebSocket` на границе (тестовый класс с `onopen/onclose/onmessage`).

- [ ] **Contract tests**: `src/hooks/use-event-feed/use-event-feed.test.ts` — возвращает `{ events, connected }`;
  `events` — массив; `connected` стартует `false`.
- [ ] **Code**: `src/hooks/use-event-feed/use-event-feed.ts` — `useEventFeed`, backoff-цикл (`reconnectDelay`:
  1000 → `Math.min(*2, 10000)`, сброс в `onopen`), обрезка `events` до 200, `cancelled`-флаг, cleanup.
- [ ] **Interface verification**: `npx vitest run src/hooks/use-event-feed` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `onopen` → `connected=true`, задержка сброшена; `onmessage` → событие добавлено
  (приведение `{ type, data, stage_id }` → `AfmEvent`); негатив — `onclose` → `connected=false`, запланирован
  реконнект; граница — удвоение до потолка 10000мс (3+ закрытия), обрезка 200+, cleanup закрывает сокет и чистит
  таймер (StrictMode double-mount — нет дублей).
- [ ] **Debugging**: `npx vitest run src/hooks/use-event-feed` — до зелёного.
- [ ] **Contract re-verification**: backoff 1000→10000+сброс; cap 200; `{ events, connected }`; одно соединение.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 7: `src/hooks/use-usage-data` — структура клетки и barrel (infrastructure)

Хук поллинга метрик. Создать barrel (`useUsageData`) и usage `usage-data.md`. Imports: `UsagePoint`,
`dashboard-data-types` из `src/types`.

**Usages relevant to this task:**
- `react`: cancelled-флаг; перезапуск таймера при смене аргументов.
- `typescript`: единственная точка приведения внешнего JSON.
- `dashboard-data-types`: потребление `UsagePoint`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/hooks/use-usage-data/index.ts` (`export { useUsageData }`).
- [ ] Создать `src/hooks/use-usage-data/.usages/usage-data.md` (дословно из дизайн-документа — включая раздел
  «Проверка доступности метрики деньги»).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 8: `src/hooks/use-usage-data` — реализация `useUsageData` (TDD coding)

**Контракт** (`useUsageData(metric, stageFilter) -> UsagePoint[]`): поллинг `GET /api/usage` для метрики и
(опционально) фильтра по стадии. Ответ — массив `{ timeBucket, value }`; приведение: `timestamp ← timeBucket`,
`metric ← параметр запроса` (один на весь ряд), `value ← value`.

**Algorithm (дословно):**
1. При монтировании и изменении `metric`/`stageFilter` выполнить запрос `GET /api/usage` с этими параметрами.
2. Распарсить JSON; привести к `UsagePoint`.
3. Если не отменён — обновить состояние.
4. Запланировать повторный опрос (10000мс).
5. При размонтировании или смене `metric`/`stageFilter` — отмена, остановка таймера.

**Requirements:** пустой массив — валидный результат (интерпретацию «скрыть деньги» выполняет потребляющая клетка);
живой апдейт каждые 10000мс. **Constraints:** не выполнять новый запрос по таймеру, если параметры не изменились —
следующий тик использует те же параметры.

**Usages relevant to this task:**
- `react`: cancelled-флаг; эффект с зависимостями `[metric, stageFilter]`.
- `typescript`: приведение массива серверных объектов к `UsagePoint`.
- `dashboard-data-types`: источник `/api/usage`.
- `vitest`: мок `fetch`.

- [ ] **Contract tests**: `src/hooks/use-usage-data/use-usage-data.test.ts` — возвращает массив; до ответа `[]`.
- [ ] **Code**: `src/hooks/use-usage-data/use-usage-data.ts` — `useUsageData`, приведение `{ timeBucket, value }` →
  `UsagePoint` (`metric` ← аргумент), 10000мс-поллинг, эффект на `[metric, stageFilter]`, `cancelled`.
- [ ] **Interface verification**: `npx vitest run src/hooks/use-usage-data` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — mock `fetch` отдаёт `[{ timeBucket, value }]`, хук приводит к `UsagePoint` с
  правильным `metric`; негатив — `fetch` бросает → состояние не падает; граница — пустой массив (валидно),
  `stageFilter: null` (без фильтра), смена `metric` перезапускает опрос.
- [ ] **Debugging**: `npx vitest run src/hooks/use-usage-data` — до зелёного.
- [ ] **Contract re-verification**: `{ timeBucket→timestamp, value }`, `metric` один на ряд; 10000мс; `[]` по умолчанию.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 9: `src/hooks/use-stage-log` — структура клетки и barrel (infrastructure)

Хук поллинга лога. Создать barrel (`useStageLog`) и usage `stage-log.md`. Imports: `LogEntry`,
`dashboard-data-types` из `src/types`.

**Usages relevant to this task:**
- `react`: cancelled-флаг; перезапуск таймера при смене `stageId`.
- `typescript`: единственная точка приведения (строковый парсинг plain-text ответа).
- `dashboard-data-types`: источник `/api/stages/{id}/log`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/hooks/use-stage-log/index.ts` (`export { useStageLog }`).
- [ ] Создать `src/hooks/use-stage-log/.usages/stage-log.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 10: `src/hooks/use-stage-log` — реализация `useStageLog` (TDD coding)

**Контракт** (`useStageLog(stageId) -> LogEntry[]`): поллинг `GET /api/stages/{stageId}/log`. Эндпоинт отдаёт
**plain text**, не JSON.

**Algorithm (дословно):**
1. При монтировании и изменении `stageId` выполнить запрос `GET /api/stages/{stageId}/log`.
2. Прочитать ответ как текст; оставить только строки вида «HH:MM:SS text …» (фильтрация tool/banner-строк как в
   `renderLog` текущего `app.js`) и разобрать их в `LogEntry` (`timestamp`, `message`, `level='info'`).
3. Если не отменён — обновить состояние.
4. Запланировать повторный опрос (3000мс).
5. При размонтировании или смене `stageId` — отмена, остановка таймера.

**Requirements:** `stageId === null` → пустой массив без запроса; эндпоинт plain text — строковый парсинг,
единственная точка приведения; флаг отмены; источник лога — отдельный поллинг, не фильтрация WS. **Constraints:**
не выполнять запрос и не держать таймер, пока `stageId === null`; хук опрашивает безусловно (отклонение от
актив-gating в `app.js` — осознанное, зафиксировано в дизайне).

**Usages relevant to this task:**
- `react`: эффект на `[stageId]`; cancelled-флаг.
- `typescript`: `response.text()`, regex-фильтрация → `LogEntry`.
- `dashboard-data-types`: `/api/stages/{id}/log` (plain text).
- `vitest`: мок `fetch` с `text()`.

- [ ] **Contract tests**: `src/hooks/use-stage-log/use-stage-log.test.ts` — возвращает массив; `stageId=null` → `[]`
  без запроса (fetch не вызывается).
- [ ] **Code**: `src/hooks/use-stage-log/use-stage-log.ts` — `useStageLog`, regex-фильтр «HH:MM:SS …»,
  разбор в `LogEntry` (`level='info'`), 3000мс-поллинг, gate на `stageId`, `cancelled`.
- [ ] **Interface verification**: `npx vitest run src/hooks/use-stage-log` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — mock `text()` возвращает «12:00:01 text starting\n12:00:02 text running\nbanner
  line», хук оставляет только log-строки (с токеном `text` после timestamp, как фильтр `/^\d{2}:\d{2}:\d{2}\s+text\s/`
  в `app.js renderLog`); первая разбирается в `LogEntry` с `timestamp='12:00:01'`, `message='starting'`,
  `level='info'`; tool/banner-строки без `text` отброшены; негатив — `fetch` бросает → прежнее состояние; граница —
  `stageId=null` (нет запроса/таймера), пустой текст, смена `stageId` перезапускает.
- [ ] **Debugging**: `npx vitest run src/hooks/use-stage-log` — до зелёного.
- [ ] **Contract re-verification**: plain-text парсинг; regex-фильтр; `null`→`[]`; 3000мс; безусловный опрос.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 11: `src/hooks/use-elapsed` — структура клетки и barrel (infrastructure)

Секундомер. Без Imports. Создать barrel (`useElapsed`) и usage `elapsed.md`.

**Usages relevant to this task:**
- `react`: interval-based effect + cleanup.
- `typescript`: типизация.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/hooks/use-elapsed/index.ts` (`export { useElapsed }`).
- [ ] Создать `src/hooks/use-elapsed/.usages/elapsed.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 12: `src/hooks/use-elapsed` — реализация `useElapsed` (TDD coding)

**Контракт** (`useElapsed(startedAt) -> elapsedMs: number`): секундомер от `startedAt`, тикает собственным
интервалом раз в секунду.

**Algorithm (дословно):**
1. При монтировании вычислить `elapsedMs` как разницу текущего времени и `startedAt`.
2. Запустить интервал 1000мс, на каждом тике перевычислять `elapsedMs`.
3. При размонтировании — остановить интервал.

**Requirements:** собственный односекундный интервал (как `elapsedTimer` в `app.js`), не привязанный к циклу
поллинга; при пустом/невалидном `startedAt` → `elapsedMs = 0` (отображение «--» — ответственность потребителя).
**Constraints:** не зависит от ре-рендера родителя для обновления значения.

**Usages relevant to this task:**
- `react`: `setInterval` в `useEffect`, cleanup.
- `typescript`: типизация `number`.

- [ ] **Contract tests**: `src/hooks/use-elapsed/use-elapsed.test.ts` — возвращает `number`; невалидный/пустой
  `startedAt` → `0`.
- [ ] **Code**: `src/hooks/use-elapsed/use-elapsed.ts` — `useElapsed`, 1000мс-интервал, перевычисление
  `Date.now() - startedAt`, `startedAt` невалиден → `0`, cleanup.
- [ ] **Interface verification**: `npx vitest run src/hooks/use-elapsed` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `startedAt` в прошлом → `elapsedMs > 0`; негатив/граница — пустой `startedAt`,
  невалидная дата → `0`. fake-timers — тик 1000мс увеличивает значение.
- [ ] **Debugging**: `npx vitest run src/hooks/use-elapsed` — до зелёного.
- [ ] **Contract re-verification**: 1000мс-интервал; невалид → `0`; не зависит от родителя.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 13: `src/components/flow-header` — структура клетки и barrel (infrastructure)

Чистый презентационный компонент. Без Imports. Создать barrel (`FlowHeader`) и usage `flow-header.md`.

**Usages relevant to this task:**
- `react`: только функциональные компоненты, без state-библиотек.
- `typescript`: PascalCase `.tsx`, strict.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/flow-header/index.ts` (`export { FlowHeader }`).
- [ ] Создать `src/components/flow-header/.usages/flow-header.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 14: `src/components/flow-header` — реализация `FlowHeader` (TDD coding)

**Контракт** (`FlowHeader(flowName, connected) -> ReactElement`): шапка — название флоу и индикатор WS.

**Requirements:** индикатор показывает текст `LINK`, когда `connected` истинно, и `OFFLINE` — когда ложно;
разметка сохраняет id `flow-name` и `ws-status` для совместимости с темой `goga`.

**Usages relevant to this task:**
- `react`: функциональный компонент, props-тип.
- `typescript`: PascalCase `.tsx`, strict props-тип.
- `vitest`: RTL `render`/`screen`.

- [ ] **Contract tests**: `src/components/flow-header/FlowHeader.test.tsx` — компонент рендерится, принимает
  `flowName`/`connected`.
- [ ] **Code**: `src/components/flow-header/FlowHeader.tsx` — `FlowHeader`, props-тип, id `flow-name`/`ws-status`,
  текст `LINK`/`OFFLINE`.
- [ ] **Interface verification**: `npx vitest run src/components/flow-header` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `connected=true` → текст `LINK`, `flowName` в `#flow-name`; `connected=false` →
  `OFFLINE`; граница — пустой `flowName`.
- [ ] **Debugging**: `npx vitest run src/components/flow-header` — до зелёного.
- [ ] **Contract re-verification**: id `flow-name`/`ws-status`, `LINK`/`OFFLINE`, чистый (без своего состояния).
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 15: `src/components/stages-list` — структура клетки и barrel (infrastructure)

Список стадий. Imports: `Stage`, `dashboard-data-types` из `src/types`. Создать barrel (`StagesList`) и usage `stages-list.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `Stage`, набор статусов для подписей.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/stages-list/index.ts` (`export { StagesList }`).
- [ ] Создать `src/components/stages-list/.usages/stages-list.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 16: `src/components/stages-list` — реализация `StagesList` (TDD coding)

**Контракт** (`StagesList(stages, selectedStageId, onSelect) -> ReactElement`): список стадий с выбором активной.

**Requirements:** стадия с `id === selectedStageId` выделяется классом `selected`; сохраняется id `stages-list`;
подписи статусов — из полного набора статусов afm.

**Usages relevant to this task:**
- `react`: функциональный компонент, `onClick` → `onSelect`.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: подписи статусов.
- `vitest`: RTL `fireEvent.click`.

- [ ] **Contract tests**: `src/components/stages-list/StagesList.test.tsx` — рендерит `ul#stages-list`; принимает
  `stages`/`selectedStageId`/`onSelect`.
- [ ] **Code**: `src/components/stages-list/StagesList.tsx` — `StagesList`, `#stages-list`, класс `selected` на
  выбранной, `onClick={() => onSelect(stage.id)}`.
- [ ] **Interface verification**: `npx vitest run src/components/stages-list` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — клик по стадии вызывает `onSelect(id)`; выбранная имеет класс `selected`;
  граница — пустой `stages` (пустой список без ошибки), `selectedStageId=null` (никто не выделен).
- [ ] **Debugging**: `npx vitest run src/components/stages-list` — до зелёного.
- [ ] **Contract re-verification**: `#stages-list`, класс `selected`, `onSelect(stage.id)`.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 17: `src/components/log-panel` — структура клетки и barrel (infrastructure)

Окно лога. Imports: `LogEntry`, `dashboard-data-types` из `src/types`. Создать barrel (`LogPanel`) и usage `log-panel.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `LogEntry`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/log-panel/index.ts` (`export { LogPanel }`).
- [ ] Создать `src/components/log-panel/.usages/log-panel.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 18: `src/components/log-panel` — реализация `LogPanel` (TDD coding)

**Контракт** (`LogPanel(entries) -> ReactElement`): окно операционного лога выбранной стадии.

**Requirements:** хронологический порядок без пересортировки; уровень записи визуально различим.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `vitest`: RTL.

- [ ] **Contract tests**: `src/components/log-panel/LogPanel.test.tsx` — рендерит контейнер лога; принимает `entries`.
- [ ] **Code**: `src/components/log-panel/LogPanel.tsx` — `LogPanel`, рендер `entries` как есть (без сортировки),
  визуальное различение `level`.
- [ ] **Interface verification**: `npx vitest run src/components/log-panel` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — несколько записей рендерятся в исходном порядке; граница — пустой `entries`
  (валидное состояние, без ошибки), разные `level`.
- [ ] **Debugging**: `npx vitest run src/components/log-panel` — до зелёного.
- [ ] **Contract re-verification**: хронологический порядок; визуальное различение уровня; пустой массив — валиден.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 19: `src/components/event-feed` — структура клетки и barrel (infrastructure)

Лента событий. Imports: `AfmEvent`, `dashboard-data-types` из `src/types`. Создать barrel (`EventFeedPanel`) и
usage `event-feed-panel.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `AfmEvent`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/event-feed/index.ts` (`export { EventFeedPanel }`).
- [ ] Создать `src/components/event-feed/.usages/event-feed-panel.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 20: `src/components/event-feed` — реализация `EventFeedPanel` (TDD coding)

**Контракт** (`EventFeedPanel(events) -> ReactElement`): лента событий флоу.

**Requirements:** хронологический порядок без пересортировки; тип события визуально различим; растущий список
рендерится как есть (без виртуализации/пагинации).

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `vitest`: RTL.

- [ ] **Contract tests**: `src/components/event-feed/EventFeedPanel.test.tsx` — рендерит ленту; принимает `events`.
- [ ] **Code**: `src/components/event-feed/EventFeedPanel.tsx` — `EventFeedPanel`, рендер `events` как есть,
  визуальное различение `type`.
- [ ] **Interface verification**: `npx vitest run src/components/event-feed` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — несколько событий в исходном порядке, разные `type` визуально различимы;
  граница — пустой `events`.
- [ ] **Debugging**: `npx vitest run src/components/event-feed` — до зелёного.
- [ ] **Contract re-verification**: хронологический порядок; различение `type`; без виртуализации.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 21: `src/components/plan-panel` — структура клетки и barrel (infrastructure)

Панель плана с markdown. Imports: `Stage`, `PlanComment`, `dashboard-data-types` из `src/types`. Создать barrel
(`PlanPanel`, `MarkdownRenderer`) и usage `plan-panel.md`.

**Usages relevant to this task:**
- `react`: раздел markdown-it — npm-зависимость `markdown-it`, конфиг `{ html:false, linkify:true }`, экземпляр
  один на модуль; поведение режима ревью из `renderPlanReview`/`formatLine`/`decorateCheckboxes` `app.js`.
- `typescript`: PascalCase `.tsx`, strict.
- `dashboard-data-types`: потребление `Stage`/`PlanComment`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/plan-panel/index.ts` (`export { PlanPanel, MarkdownRenderer }`).
- [ ] Создать `src/components/plan-panel/.usages/plan-panel.md` (дословно из дизайн-документа — включая «Рендер
  markdown отдельно» и «Особенности»).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 22: `src/components/plan-panel` — реализация `MarkdownRenderer` (TDD coding)

**Контракт** (`MarkdownRenderer(source) -> ReactElement`): обёртка над npm-пакетом `markdown-it`.

**Requirements:** конфигурация `html=false, linkify=true`; экземпляр создаётся один раз на модуль. **Constraints:**
никакой другой сторонней библиотеки рендера markdown, кроме `markdown-it`.

**Usages relevant to this task:**
- `react`: раздел markdown-it (конфиг дословно, `dangerouslySetInnerHTML`).
- `typescript`: PascalCase `.tsx`.
- `vitest`: RTL.

- [ ] **Contract tests**: `src/components/plan-panel/MarkdownRenderer.test.tsx` — рендерит HTML из markdown-`source`.
- [ ] **Code**: `src/components/plan-panel/MarkdownRenderer.tsx` — `import MarkdownIt from 'markdown-it'`,
  `const md = new MarkdownIt({ html: false, linkify: true })` (один на модуль), `md.render(source)` через
  `dangerouslySetInnerHTML`.
- [ ] **Interface verification**: `npx vitest run src/components/plan-panel` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `# Заголовок` → `<h1>`; `linkify` превращает URL в ссылку; негатив/граница —
  пустой `source`; `html:false` — сырой HTML-тег в source не интерпретируется (экранируется).
- [ ] **Debugging**: `npx vitest run src/components/plan-panel` — до зелёного.
- [ ] **Contract re-verification**: конфиг `{html:false, linkify:true}`; один экземпляр; только `markdown-it`.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 23: `src/components/plan-panel` — реализация `PlanPanel` (TDD coding)

**Контракт** (`PlanPanel(stage) -> ReactElement`, Entity): загрузка markdown плана, рендер (обычный или режим
ревью), действия Approve/Send revision/Retry, комментарии к строкам. План — `GET /api/stages/{stage.id}/plan`
(plain text): обычный режим — через `MarkdownRenderer`, режим ревью (`stage.status === 'awaiting_approval'`) —
построчно с номерами строк и комментариями.

**Requirements:** режим ревью (`awaiting_approval`): спецсекции «## Assumptions»/«## Acceptance Criteria»
сворачиваемые, каждая строка с номером, чекбоксы `[x]`/`[ ]` стилизованы, code-блоки (```) отдельными блоками,
клик по строке открывает форму комментария; при `stage.status === 'done'` все «- [ ]» → «- [x]» (mark-all-done);
Approve/Send revision — только для `awaiting_approval`; Retry — только для `failed`. **Свойства:**
`planMarkdown -> string` (внутреннее состояние из `GET .../plan`), `comments -> PlanComment[]` (накапливаются,
отправляются одним `feedback`-полем ревизии). **Методы:** `approve()` → `POST /api/stages/{stage.id}/approve`;
`sendRevision()` → `POST /api/stages/{stage.id}/revise`, собирает `feedback` из комментариев (формат «Line N: text»,
как `buildFeedbackString` в `app.js`); `retry()` → `POST /api/stages/{stage.id}/retry`. Обновление состояния — через
хук `useStatus`, без оптимистичного апдейта.

**Usages relevant to this task:**
- `react`: режим ревью из `renderPlanReview`/`formatLine`/`decorateCheckboxes` `app.js`; состояние в хуках компонента.
- `typescript`: PascalCase `.tsx`; `PlanComment` для комментариев.
- `dashboard-data-types`: `/api/stages/{id}/plan` (plain text), действия approve/revise/retry.
- `vitest`: мок `fetch`.

- [ ] **Contract tests**: `src/components/plan-panel/PlanPanel.test.tsx` — компонент рендерится по `stage`; mock
  `fetch` на `/plan` отдаёт markdown.
- [ ] **Code**: `src/components/plan-panel/PlanPanel.tsx` — `PlanPanel`, загрузка плана (`fetch .../plan` → `text()`),
  режим ревью (номера строк, чекбоксы, code-блоки, сворачиваемые спецсекции, форма комментария), mark-all-done при
  `done`, действия по статусу; `comments` + `buildFeedbackString` в `sendRevision`.
- [ ] **Interface verification**: `npx vitest run src/components/plan-panel` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `awaiting_approval` → режим ревью с номерами строк + кнопки Approve/Send revision;
  `failed` → кнопка Retry; `done` → все `- [ ]` заменены на `- [x]`; `sendRevision` отправляет `feedback` из
  комментариев; негатив/граница — пустой план, статус без действий (кнопки скрыты), `fetch` бросает.
- [ ] **Debugging**: `npx vitest run src/components/plan-panel` — до зелёного.
- [ ] **Contract re-verification**: режим ревью только при `awaiting_approval`; mark-all-done при `done`;
  approve/revise/retry по статусам; `feedback` из комментариев; без оптимистичного апдейта.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 24: `src/components/dialog-channel` — структура клетки и barrel (infrastructure)

Диалоговый канал. Imports: `Stage`, `DialogQuestion`, `dashboard-data-types` из `src/types`. Создать barrel
(`DialogChannel`) и usage `dialog-channel.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `Stage`/`DialogQuestion`, источник `/api/stages/{id}/dialog`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/dialog-channel/index.ts` (`export { DialogChannel }`).
- [ ] Создать `src/components/dialog-channel/.usages/dialog-channel.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 25: `src/components/dialog-channel` — реализация `DialogChannel` (TDD coding)

**Контракт** (`DialogChannel(stage) -> ReactElement`, Entity): история вопросов/ответов по фазам, текущий вопрос
(опции и/или свободный ответ), отмена. История и текущий вопрос — `GET /api/stages/{stage.id}/dialog` (JSON-массив
записей с `phase`/`type`/`question`/`answer`/`id`/`options`/`allow_custom`). Текущий вопрос — последняя запись с
`answer=null` (записи `agent_text` вопросом не считаются).

**Requirements:** история группируется по `phase`; записи `agent_text` рендерятся как сообщения агента; опции
`currentQuestion` — массив строк (label = значение), `allow_custom` разрешает свободный текст, выбор опции и ввод
текста взаимоисключающие; видимость — канал показан, если есть история либо `stage.status === 'awaiting_user_input'`.
**Свойства:** `history -> DialogQuestion[]`, `currentQuestion -> DialogQuestion | null`. **Методы:**
`answer(questionId, value, fromOptions)` → `POST /api/stages/{stage.id}/dialog/answer` телом
`{ id, phase, answer, from_options }`; `cancel()` → `POST /api/stages/{stage.id}/dialog/cancel`.

**Usages relevant to this task:**
- `react`: функциональный компонент; поведение из `loadDialog`/`renderDialog`/`renderPendingQuestion` `app.js`.
- `typescript`: PascalCase `.tsx`; приведение JSON-ответа.
- `dashboard-data-types`: `/api/stages/{id}/dialog`, поля записи.
- `vitest`: мок `fetch`.

- [ ] **Contract tests**: `src/components/dialog-channel/DialogChannel.test.tsx` — компонент рендерится по `stage`;
  mock `fetch` на `/dialog` отдаёт массив записей.
- [ ] **Code**: `src/components/dialog-channel/DialogChannel.tsx` — `DialogChannel`, загрузка диалога, разбор
  `history`/`currentQuestion`, группировка по `phase`, рендер `agent_text`, формы опции/свободного ответа,
  `answer` (тело `{id, phase, answer, from_options}`), `cancel`, видимость.
- [ ] **Interface verification**: `npx vitest run src/components/dialog-channel` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `currentQuestion` с опциями → выбор опции вызывает `answer(id, value, true)`;
  `allow_custom` → свободный текст вызывает `answer(id, text, false)`; `cancel` → POST `/dialog/cancel`;
  граница — нет истории и статус ≠ `awaiting_user_input` (канал скрыт), `currentQuestion=null`, записи `agent_text`.
- [ ] **Debugging**: `npx vitest run src/components/dialog-channel` — до зелёного.
- [ ] **Contract re-verification:** тело `answer` `{id, phase, answer, from_options}`; `currentQuestion` = последняя
  без ответа; видимость; взаимоисключающие опция/текст.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 26: `src/components/footer` — структура клетки и barrel (infrastructure)

Футер. Imports: `Stage`, `dashboard-data-types` из `src/types`. Создать barrel (`Footer`) и usage `footer.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `Stage`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/footer/index.ts` (`export { Footer }`).
- [ ] Создать `src/components/footer/.usages/footer.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 27: `src/components/footer` — реализация `Footer` (TDD coding)

**Контракт** (`Footer(stages, elapsedMs) -> ReactElement`): прогресс, время старта, elapsed. Принимает готовый
`elapsedMs` от `useElapsed`.

**Requirements:** прогресс — доля стадий со статусом `done` от общего числа `stages`; `startedAt` и `elapsed` в
человекочитаемом формате; elapsed пересчитывается вызывающим хуком каждую секунду; при невалидном `elapsedMs` (0)
— отображение «--» (ответственность футера, как `updateElapsed` в `app.js`).

**Usages relevant to this task:**
- `react`: функциональный компонент.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: прогресс по статусу `done`.
- `vitest`: RTL.

- [ ] **Contract tests**: `src/components/footer/Footer.test.tsx` — рендерит футер; принимает `stages`/`elapsedMs`.
- [ ] **Code**: `src/components/footer/Footer.tsx` — `Footer`, прогресс `done`/`total`, человекочитаемый elapsed,
  «--» при `elapsedMs === 0`.
- [ ] **Interface verification**: `npx vitest run src/components/footer` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — 2 из 4 стадий `done` → прогресс 0.5; граница — пустой `stages` (прогресс 0 без
  деления на 0), `elapsedMs=0` → «--».
- [ ] **Debugging**: `npx vitest run src/components/footer` — до зелёного.
- [ ] **Contract re-verification**: прогресс по `done`; «--» при 0; чистый компонент.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 28: `src/components/consumption-panel` — структура клетки и barrel (infrastructure)

Панель потребления. Imports: `Stage`, `UsagePoint`, `dashboard-data-types` из `src/types`; `useUsageData`,
`usage-data` из `src/hooks/use-usage-data`. Создать barrel (`ConsumptionPanel`) и usage `consumption-panel.md`.

**Usages relevant to this task:**
- `react`: функциональный компонент; поведение из `loadUsage`/`probeUsageCost`/`renderUsageChart`/`openUsagePanel`
  `app.js`.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `Stage`/`UsagePoint`.
- `usage-data` (imported): потребление `useUsageData`, проверка доступности cost.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/components/consumption-panel/index.ts` (`export { ConsumptionPanel }`).
- [ ] Создать `src/components/consumption-panel/.usages/consumption-panel.md` (дословно из дизайн-документа).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 29: `src/components/consumption-panel` — реализация `ConsumptionPanel` (TDD coding)

**Контракт** (`ConsumptionPanel(stages) -> ReactElement`, Entity): выезжающая слева панель — метрики
tokens/cost/kb, фильтр по стейджам, hand-rolled SVG-график. Данные — через `useUsageData` по текущим
`metric`/`stageFilter`; опция `cost` скрывается, если пробный запрос `useUsageData('cost', null)` пуст.

**Requirements:** панель выезжает/прячется по кнопке-стрелке (`#usage-toggle`) — внутреннее состояние `open`;
`className «open»` на `aside#usage-panel`, `aria-hidden` на тело панели; опция `cost` скрывается, если данные по
`cost` пусты (проверка через `useUsageData('cost', null)` при монтировании); если `cost` был активен и оказался
недоступен — откат на `tokens`. **Свойства:** `open`, `metric`, `stageFilter`, `points`. **Методы:**
`switchMetric(metric)`, `setStageFilter(stageId)`.

**Usages relevant to this task:**
- `react`: состояние `open`/`metric`/`stageFilter`; SVG-рендер.
- `typescript`: PascalCase `.tsx`.
- `usage-data`: `useUsageData` для ряда и для пробы cost.
- `vitest`: мок `fetch` (через `useUsageData`).

- [ ] **Contract tests**: `src/components/consumption-panel/ConsumptionPanel.test.tsx` — рендерит `aside#usage-panel`
  и `#usage-toggle`; принимает `stages`.
- [ ] **Code**: `src/components/consumption-panel/ConsumptionPanel.tsx` — `ConsumptionPanel`, состояние
  `open`/`metric`/`stageFilter`, `useUsageData(metric, stageFilter)`, проба `useUsageData('cost', null)` при
  монтировании (скрытие/откат cost), hand-rolled SVG-график по `points`, `switchMetric`/`setStageFilter`.
- [ ] **Interface verification**: `npx vitest run src/components/consumption-panel` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — клик `#usage-toggle` переключает `open` (класс `open` на `#usage-panel`); смена
  метрики через `switchMetric`; граница — `useUsageData('cost', null)` пуст → опция cost скрыта, откат активной
  cost на tokens; пустой ряд (график без точек).
- [ ] **Debugging**: `npx vitest run src/components/consumption-panel` — до зелёного.
- [ ] **Contract re-verification:** `#usage-panel`/`#usage-toggle` + класс `open`; скрытие/откат cost; SVG собственный.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 30: `src/app` — структура клетки и barrel (infrastructure)

Корневая композиция. Imports: 8 компонентов, 4 хука, `Stage` + `dashboard-data-types`. Создать barrel (`App`) и
usage `app-composition.md`.

**Usages relevant to this task:**
- `react`: только функциональные компоненты, состояние в хуках верхнего компонента, без Redux/Zustand/Context.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: потребление `Stage`.

**CRITICAL: `CODEMANIFEST` — read-only.**

- [ ] Создать `src/app/index.ts` (`export { App } from './App'`).
- [ ] Создать `src/app/.usages/app-composition.md` (дословно из дизайн-документа — включая пример `src/main.tsx`).
- [ ] Verify facade: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 31: `src/app` — реализация `App` (TDD coding)

**Контракт** (`App() -> ReactElement`, Routine): корневая композиция — шапка, список стадий, панель деталей, лента
событий, футер, панель потребления; владеет состоянием выбора текущей стадии.

**Algorithm (дословно):**
1. Получить состояние флоу через `useStatus` (`flowName`, `stages`, `startedAt`, `refresh`), ленту событий и статус
   соединения через `useEventFeed`, лог выбранной стадии через `useStageLog`, прошедшее время через `useElapsed`.
2. Хранить `selectedStageId`; при его отсутствии — автовыбор активной стадии (статусы `planning`, `running`,
   `revising`, `retrying`, `awaiting_user_input`), иначе первая стадия со статусом `failed`.
3. Распределить данные по клеткам: `flowName` → `FlowHeader`, `stages` → `StagesList`/`Footer`/`ConsumptionPanel`,
   события → `EventFeedPanel`, лог → `LogPanel`, план через `PlanPanel` и диалог через `DialogChannel` — по выбранной
   стадии, `elapsedMs` → `Footer`.
4. При значимых WebSocket-событиях (смена статуса стадии, `approved`, `revised`, `retry_scheduled`,
   `retry_exhausted`, `manual_retry`, `ask_user`, `user_answered`, `agent_completed`) вызывать `refresh()` хука
   `useStatus`.

**Requirements:** лог стадии — отдельный источник (`useStageLog`), не фильтрация WS.

**Usages relevant to this task:**
- `react`: композиция; `React.StrictMode` (обёртка в `main.tsx`); без глобального состояния.
- `typescript`: PascalCase `.tsx`.
- `dashboard-data-types`: автовыбор по активным статусам.
- `vitest`: мок `fetch`/`WebSocket`, RTL.

- [ ] **Contract tests**: `src/app/App.test.tsx` — `App` рендерится без падения; монтирует все подобласти.
- [ ] **Code**: `src/app/App.tsx` — `App`, вызовы `useStatus`/`useEventFeed`/`useStageLog`/`useElapsed`, состояние
  `selectedStageId` + автовыбор (активные → fallback `failed`), эффект на события `useEventFeed` → `refresh()`,
  распределение пропсов; `main.tsx` рендерит `<App />` (в `React.StrictMode`).
- [ ] **Interface verification**: `npx vitest run src/app` — contract-тесты проходят.
- [ ] **Logic tests**: позитив — `stages` с активной стадией → `selectedStageId` авто-выбран; нет активных → первая
  `failed`; значимое WS-событие → вызван `refresh()`; негатив/граница — пустые `stages`, `startedAt` невалиден,
  лог выбранной стадии.
- [ ] **Debugging**: `npx vitest run src/app` — до зелёного.
- [ ] **Contract re-verification:** все 4 хука вызваны только в `App` (кроме `useUsageData` в consumption-panel,
  план/диалог грузятся в своих клетках); автовыбор; WS → `refresh()`; без глобального состояния.
- [ ] **Lint**: `npx tsc --noEmit`; `goga lint` — 0 ошибок.

### Task 32: Integration — App↔`useStatus.refresh()` по WS-событиям и автовыбор стадии (integration)

Кросс-сущностный сценарий: значимое WS-событие → `refresh()` → ре-рендер; автовыбор активной стадии и fallback на `failed`.

**Usages relevant to this task:**
- `vitest`: мок `WebSocket` (событие с `stage_id`), мок `fetch` (`/api/status`).
- `react`: `render` всей `App` (или композиции хуков).

- [ ] Создать `src/app/integration.ws-refresh.test.tsx` (или расширить `App.test.tsx`).
- [ ] Сценарий: `useEventFeed` получает событие `stage_status_changed` (или `approved`) → `App` вызывает
  `useStatus.refresh()` → `fetch('/api/status')` повторяется.
- [ ] Сценарий: `stages` без `selectedStageId` → автовыбор активной стадии (`running`/`planning`/...); нет активных →
  первая `failed`.
- [ ] Run validation: `npx vitest run src/app`.

### Task 33: Integration — `use-stage-log` plain-text парсинг (integration)

Кросс-сущностный: эндпоинт `/log` отдаёт plain text; хук фильтрует/парсит в `LogEntry`; `null` `stageId` — нет запроса.

**Usages relevant to this task:**
- `vitest`: мок `fetch` с `text()`.
- `react`: `renderHook`.

- [ ] Создать/расширить `src/hooks/use-stage-log/use-stage-log.test.ts` интеграционными кейсами.
- [ ] Сценарий: реалистичный plain-text лог (mix «HH:MM:SS text …» + tool/banner-строки без токена `text`) →
  корректные `LogEntry` (`level='info'`), tool/banner-строки отброшены по фильтру `/^\d{2}:\d{2}:\d{2}\s+text\s/`
  (как `renderLog` в `app.js`).
- [ ] Сценарий (граница): `stageId=null` → `fetch` не вызван, `entries=[]`; смена `stageId` перезапускает опрос.
- [ ] Run validation: `npx vitest run src/hooks/use-stage-log`.

### Task 34: Integration — `plan-panel` режим ревью и действия (integration)

Кросс-сущностный: режим ревью при `awaiting_approval`, mark-all-done при `done`, действия по статусам, `feedback`
из комментариев.

**Usages relevant to this task:**
- `vitest`: мок `fetch` (`/plan` plain text, POST `/approve`/`/revise`/`/retry`).
- `react`: RTL.

- [ ] Создать/расширить `src/components/plan-panel/PlanPanel.test.tsx`.
- [ ] Сценарий: `stage.status='awaiting_approval'` → режим ревью (номера строк, чекбоксы, code-блоки), кнопки
  Approve/Send revision; клик по строке → форма комментария; `sendRevision` → POST `/revise` с `feedback` формата
  «Line N: text».
- [ ] Сценарий: `stage.status='done'` → все «- [ ]» → «- [x]`; `failed` → Retry; прочие статусы → действия скрыты.
- [ ] Run validation: `npx vitest run src/components/plan-panel`.

### Task 35: Integration — `dialog-channel` answer (опция/текст) и cancel (integration)

Кросс-сущностный: выбор опции vs свободный текст → тело `{ id, phase, answer, from_options }`; cancel.

**Usages relevant to this task:**
- `vitest`: мок `fetch` (`/dialog` JSON, POST `/dialog/answer`, `/dialog/cancel`).
- `react`: RTL.

- [ ] Создать/расширить `src/components/dialog-channel/DialogChannel.test.tsx`.
- [ ] Сценарий: `currentQuestion` с опциями → выбор опции → POST `/dialog/answer` с `from_options:true`;
  `allow_custom` → свободный текст → `from_options:false`; записи `agent_text` — сообщения агента; видимость
  (история или `awaiting_user_input`).
- [ ] Сценарий: cancel → POST `/dialog/cancel`.
- [ ] Run validation: `npx vitest run src/components/dialog-channel`.

### Task 36: Integration — `consumption-panel` slide-out и скрытие cost (integration)

Кросс-сущностный: выезжающая панель (`#usage-toggle`/`#usage-panel`+`open`), скрытие/откат cost, собственный SVG.

**Usages relevant to this task:**
- `vitest`: мок `fetch` (`/api/usage`).
- `react`: RTL.

- [ ] Создать/расширить `src/components/consumption-panel/ConsumptionPanel.test.tsx`.
- [ ] Сценарий: клик `#usage-toggle` → класс `open` на `#usage-panel`; `useUsageData('cost', null)` пуст → опция cost
  скрыта, активная cost откатывается на tokens; смена метрики/фильтра → новый ряд; SVG рендерится по `points`.
- [ ] Run validation: `npx vitest run src/components/consumption-panel`.

### Task 37: Integration — идемпотентность под `React.StrictMode` double-mount (integration)

Кросс-сущностный: все хуки (`use-status`, `use-event-feed`, `use-usage-data`, `use-stage-log`, `use-elapsed`) под
double-mount не дают дублей побочных эффектов (сокетов/таймеров/запросов).

**Usages relevant to this task:**
- `vitest`: мок `fetch`/`WebSocket`, fake timers.
- `react`: `renderHook` в `StrictMode`.

- [ ] Создать `src/hooks/strictmode.test.tsx` (общий для хуков).
- [ ] Сценарий: каждый хук в `StrictMode` (двойной mount→unmount→mount) — ровно одно активное соединение/таймер,
  cleanup корректен (нет утечек, нет записи состояния от устаревшего запроса).
- [ ] Run validation: `npx vitest run src/hooks`.

---

## Validation Commands

> Управляющие команды `npm`/`vite`/`vitest`/`tsc` здесь перечислены как обязательные проверки; они исполняются на
> стадиях `apply`/`fix-changes` (не на стадии `plan`). Стадия `plan` запускает только `goga lint`/`goga schema`.

- `npx vitest run`: прогон всех тестов (компоненты + хуки + integration) — Vitest вместо Jest (отступление от
  `conventions`, действует только внутри dashboard).
- `npx tsc --noEmit`: typecheck в strict-режиме (без `any`, кроме единственной точки приведения внешнего JSON).
- `npm run clean:assets`: `rm -rf assets` — очистка сгенерированной папки `assets/` (обязателен, т.к. при
  `emptyOutDir=false` хешированные `assets/*.js`/`*.css` от прошлых сборок не удаляются автоматически и молча
  раздули бы бинарник через `//go:embed dashboard/*`). Запускается перед каждым `npm run build` (встроено в скрипт `build`).
- `npm run build`: сборка кладёт `index.html` + `assets/*` прямо в корень `dashboard/` (`build.outDir='.'`,
  `build.emptyOutDir=false`, `base='./'`) — совместимо с `pkg/web/embed.go` (`//go:embed dashboard/*` + `fs.Sub`)
  без правок.
- `/tmp/goga-venv/bin/goga lint`: CODEMANIFEST-линт (read-only проверка) — **0 ошибок по 16 клеткам**.
- `/tmp/goga-venv/bin/goga schema`: граф клеток (зависимости/`location`/`From`-пути без опечаток, циклов нет).
- Проверка фасада: `npx tsc --noEmit` подтверждает, что все экспорты из barrel `index.ts` каждой клетки разрешимы
  (именованные экспорты `useStatus`/`useEventFeed`/`useUsageData`/`useStageLog`/`useElapsed`/`FlowHeader`/`StagesList`/
  `LogPanel`/`EventFeedPanel`/`PlanPanel`/`MarkdownRenderer`/`DialogChannel`/`Footer`/`ConsumptionPanel`/`App` + типы).
- **Build & tooling-ограничения (обязательны)**: `vite.config.ts` — `build.outDir='.'` и `build.emptyOutDir=false`;
  `package.json` — `"type":"module"`, deps ровно `react`/`react-dom`/`markdown-it`, devDeps `vite`/`@vitejs/plugin-react`/
  `typescript`/`vitest`/`@testing-library/react`/`@testing-library/jest-dom`/`jsdom` (и `@types/*` под типы),
  скрипт `clean:assets`; `index.html` — без `<script src="markdown-it.min.js">`;
  `public/markdown-it.min.js` удалён; без SSR, без state-менеджеров (Redux/Zustand/Context), без charting-библиотек.

---

## Completion Criteria

- [ ] Каждая контрактная сущность реализована в корректном `location` (см. Contract Surface).
- [ ] Каждая контрактная сущность доступна с фасада (barrel `index.ts` клетки) — именованный экспорт.
- [ ] Свойства/методы соответствуют объявленному API (сигнатуры — дословно из дизайн-документа).
- [ ] Описания (Algorithm/Requirements/Constraints) отражены в поведении реализации.
- [ ] Контрактные зависимости удовлетворены: `Imports` корректно указывают на `From: src/types` /
  `From: src/hooks/use-usage-data` / `From: src/components/*`, путей без опечаток, циклов нет.
- [ ] Реэкспорты доступны с фасада (9 из `src/types`, по одному на каждую клетку-хук/компонент/`app`).
- [ ] Каждая coding-задача прошла TDD (contract tests → code → verification → logic tests → debugging →
  re-verification → lint).
- [ ] В каждой coding-задаче есть contract-тесты (facade/API/signature) и logic-тесты (позитив/негатив/граница).
- [ ] Integration-тесты покрывают кросс-сущностные сценарии (App↔`refresh`/автовыбор; `use-stage-log` plain-text;
  `plan-panel` ревью+действия; `dialog-channel` answer/cancel; `consumption-panel` slide-out/cost; StrictMode).
- [ ] Границы системы не расширены за пределы 15 `src/`-клеток: не созданы новые Cells, не выведены сущности в
  приват, facade не нарушен, `location` соблюдён.
- [ ] **`CODEMANIFEST` не модифицированы** (read-only); клетка `.` (`DashboardAssets`), `pkg/web/embed.go`, версия
  go в `go.mod`, `pkg/server` — планом **не затронуты**.
- [ ] Все команды валидации проходят: `npx vitest run` (зелёный), `npx tsc --noEmit` (без ошибок), `npm run build`
  (бандл в корне `dashboard/`), `goga lint` = exit 0 (16 клеток).
- [ ] Поведенческий паритет с `app.js`: WS-backoff 1000→10000мс + сброс; эндпоинты (`/api/status`,
  `/api/stages/{id}/{log,plan,dialog}`, `/api/usage`); поллинг лога 3000мс; полный набор из 10 статусов; скрытие
  cost при пустом `/api/usage?metric=cost`; `useElapsed` (1с); автовыбор активной стадии (иначе первая `failed`);
  `dialog/cancel`; WS как канал обновления состояния; идемпотентность под `React.StrictMode` double-mount.
- [ ] Обе темы (`novacorps`, `goga`) совместимы — CSS-классы и id (`flow-name`, `ws-status`, `stages-list`,
  `usage-panel`, `usage-toggle`, классы `selected`/`open`) сохранены.
- [ ] Каждая Usages-ссылка упомянута минимум в одной задаче (`conventions`, `typescript`, `react`, `vitest`, `vite`,
  `dashboard-data-types`, `usage-data`, и все 15 локальных `.usages/`).
- [ ] Для каждой планируемой `.usages/` есть задача создания (Task 1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 24, 26, 28, 30).
