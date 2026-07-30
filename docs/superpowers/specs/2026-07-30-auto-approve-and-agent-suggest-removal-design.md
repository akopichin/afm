# Удаление флага agent_suggest + стейдж-флаг auto_approve

**Дата:** 2026-07-30
**Статус:** design (одобрен к реализации)

## Цель

Две независимые задачи:

1. **Убрать экспериментальный флаг/env `agent_suggest`.** Функциональность (revise/"добавить заметку агенту" на `running`-стейдже) остаётся, но перестаёт быть опциональной — включена всегда, без конфига и без env-переменной.
2. **Добавить стейдж-флаг `auto_approve: bool`** (дефолт `false`). Если `true` — план стейджа апрувится автоматически сразу как только готов, без участия человека и без вопроса на дашборде. Нужно для запуска флоу в CI, где часть стейджей требует ревью человеком, а часть — нет.

---

## Часть 1: удаление `agent_suggest`

Чистое удаление тумблера — переключаемое поведение становится безусловным, никакого редизайна логики.

### `pkg/config`

- Удалить `ExperimentalConfig` (struct с единственным полем `AgentSuggest *bool`), `Config.Experimental`, `IsAgentSuggestEnabled()`, ветку merge/overlay для `Experimental.AgentSuggest`, чтение `AFM_EXP_AGENT_SUGGEST`.
- Убрать секцию `experimental.agent_suggest` из `config.example.yaml` и упоминания в `README.md`.

### `pkg/server`

- Удалить `Config.AgentSuggestEnabled`, `Server.agentSuggestEnabled`, `statusResponse.AgentSuggestEnabled`/`agent_suggest_enabled`.
- `handleRevise` (`handlers.go`): условие апскейлится до безусловного —
  ```go
  allowed := st.Status == state.StatusAwaitingApproval || st.Status == state.StatusRunning
  ```
  (текст ошибки для отклонённого случая упрощается — убрать упоминание флага).

### `cmd/afm/run.go`

- Убрать `AgentSuggestEnabled: cfg.Experimental.IsAgentSuggestEnabled()` из `server.Config{...}`.

### Тесты

- Удалить: `TestIsAgentSuggestEnabled` (`pkg/config/config_test.go`), `TestHandleRevise_RunningRequiresAgentSuggestFlag`, `TestHandleStatus_AgentSuggestEnabledReflectsConfig` (`pkg/server/handlers_test.go`).
- В `pkg/orchestrator/agent_suggest_test.go`/`agent_suggest_race_test.go` — убрать любую настройку/проверку флага, оставить сценарии revise-во-время-running как безусловные (сами файлы/имя фичи не переименовываем — это по-прежнему логически "agent_suggest"-функциональность, просто без тумблера).

### Frontend (`pkg/web/dashboard/src`)

- `hooks/use-status/use-status.ts`: убрать `agentSuggestEnabled` из типа `FlowStatus`, `EMPTY_STATUS`, парсинг в `normalizeStatus` (`obj.agent_suggest_enabled`).
- `app/App.tsx`: убрать деструктуризацию и проброс пропа в `<StagesList>`.
- `components/stages-list/StagesList.tsx`: убрать проп из `StagesListProps` и сигнатуры; условие рендера кебаб-меню (строка ~135) упрощается до `KEBAB_STATUSES.has(stage.status)`.
- Обновить `App.test.tsx` (убрать `agent_suggest_enabled` из мока ответа `/api/status`) и `StagesList.test.tsx` (убрать проп из всех вызовов, удалить/переписать тест "shows the kebab menu only when agentSuggestEnabled...", т.к. условие больше не существует).

---

## Часть 2: `auto_approve` на стейдже

### Модель (`pkg/flow/flow.go`)

Новое поле `Stage`:
```go
AutoApprove bool `yaml:"auto_approve"` // default false
```

Валидация: **новых ограничений не вводим**. На типах стейджей, которые никогда не доходят до `awaiting_approval` (`agents: [auto]`, преднастроенный `plan:`-путь, `interactive: true`), флаг — безобидный no-op, как и остальные несвязанные поля на этих типах стейджей сегодня.

### Где флаг реально влияет (`pkg/orchestrator`)

`EvPlanReady` (переход в `awaiting_approval`) сегодня стреляет из ровно 3 мест. Во все три добавляется одна и та же проверка через общий небольшой хелпер (что-то вроде `autoApproveIfConfigured(ctx, stageID, stage)`), чтобы не тройной лог-строкой:

1. **`onAgentCompleted`, `phasePlanning`-ветка** (`orchestrator.go:364`) — обычный путь после того, как planning-агент естественно завершился. В скоупе есть только `stageID` (строка) — стейдж достаётся через `o.graph.Stage(id)` (тот же вызов, что уже делает `approveStage` внутри себя).
2. **`recovery.go:106`** (ветка `Retrying` в `startPlanningForPending`) — resume после краша, когда `plan.md` уже был записан до падения. Полный `s flow.Stage` уже в скоупе (переменная цикла). Сегодня здесь нет вообще никакой авто-апрув-ветки (даже headless) — это будет первая проверка такого рода в этом месте.
3. **`recovery.go:155`** (`default`-ветка того же свитча) — тот же случай для стейджей в неопределённом статусе при рестарте. Аналогично, `s` уже в скоупе, авто-апрув-ветки сегодня нет.

Проверка ставится **до** существующей headless/`DashboardURL`/`RequireApproval`-ветки и коротко замыкает её: если `stage.AutoApprove == true` → сразу `o.approveStage(ctx, stageID)` и `return`/`continue`, **независимо** от того, подключён ли дашборд, и независимо от `--require-approval`. Это соответствует решению: `auto_approve` побеждает всегда, а не только в headless-режиме.

### API (`pkg/server`)

По образцу уже существующего `stage_interactive` (см. `StageInteractive`/`statusResponse.StageInteractive`):

- Новое поле `statusResponse.StageAutoApprove map[string]bool \`json:"stage_auto_approve,omitempty"\``.
- Заполняется один раз при старте в `cmd/afm/run.go` из `flow.Stage.AutoApprove`, тем же способом, что и `stageInteractive` (`map[stageID]bool`), пробрасывается через `server.Config`/`Server` так же, как `StageInteractive`.

### Frontend

- `types/stage.ts`: новое поле `autoApprove: boolean` на типе `Stage`.
- `use-status.ts`: парсинг `stage_auto_approve` в `normalizeStatus`/`toStage`, по образцу `interactive`.
- `PlanPanel.tsx`:
  - `showActions = isReview && !stage.autoApprove` — кнопки Approve/Revise никогда не показываются для авто-апрувнутого стейджа, даже если поллинг поймает его в момент короткого перехода через `awaiting_approval`.
  - Новое `showAutoApprovedBadge = stage.autoApprove && planMarkdown.trim() !== ''` — рендерит статичный бейдж "Auto-approved" на месте actions-row. Текст плана остаётся видимым и рендерится как обычно (уже существующее поведение для стейджа, прошедшего approval).

Итоговый UX в CI: план сгенерирован → мгновенно автоапрувнут → флоу идёт дальше; в дашборде (если кто-то смотрит) виден план с бейджем "Auto-approved" вместо кнопок — без ожидания и без клика.

## Тестирование

- **`pkg/config`**: удаление `TestIsAgentSuggestEnabled`, проверка что `experimental`-секция больше не парсится (или явно игнорируется как неизвестное поле, без падения).
- **`pkg/server`**: `handleRevise` разрешает revise на `running`-стейдже без всякого флага (безусловный тест взамен старого gated-теста). Новый тест: `statusResponse` отдаёт `stage_auto_approve` для стейджей с `auto_approve: true`.
- **`pkg/orchestrator`** (интеграционные):
  - Стейдж с `auto_approve: true`, дашборд НЕ поднят (headless) → план апрувится сразу, без разницы с текущим headless-поведением по факту, но через новый путь.
  - Стейдж с `auto_approve: true`, дашборд поднят → план всё равно апрувится мгновенно, никакого ожидания клика (проверяем, что `awaiting_approval` либо не наблюдается вовсе, либо стейдж почти сразу переходит в `ready`/`running`).
  - Стейдж с `auto_approve: true` + флоу запущен с `--require-approval` → апрувится (auto_approve побеждает).
  - Краш во время `Retrying`/pending с уже записанным `plan.md` у стейджа с `auto_approve: true` → после рестарта апрувится автоматически через `recovery.go`-путь.
  - Стейдж без `auto_approve` (дефолт `false`) → поведение не меняется (обычный gate).
- **Frontend**: `PlanPanel.test` — для `stage.autoApprove === true` кнопки Approve/Revise не рендерятся, рендерится бейдж "Auto-approved"; для `false` — старое поведение без изменений.

## Вне охвата

- Валидация несовместимых комбинаций (`auto_approve` + `agents: [auto]`/`interactive`/preset `plan:`) — по дизайну это no-op, не ошибка.
- Какое-либо предупреждение в CLI `afm approve` для уже авто-апрувнутого стейджа — он просто больше не в `awaiting_approval`, команда ведёт себя как для любого другого уже прошедшего approval стейджа (ошибка "not awaiting approval").
- Переименование внутренних имён/тестов/комментариев, упоминающих `agent_suggest` как название фичи (revise-while-running) — убираем только тумблер, не сам концепт.
