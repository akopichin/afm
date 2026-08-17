# Стадии со статусом `paused`

## Context

Стадии сейчас никогда не ждут пользователя без причины, порождённой самим
агентом (`awaiting_approval`, `awaiting_user_input`) или сбоем
(`failed`, `hook_failed`). Нет способа:

1. Заранее пометить стадию как требующую ручного подтверждения перед
   стартом, даже если у неё нет плана на согласование и нет вопроса от
   агента.
2. Аккуратно остановить уже выполняющуюся стадию по требованию
   пользователя (сейчас единственный способ прервать бегущего агента —
   `Revise` с текстовым фидбеком, который всегда перезапускает агента; нет
   способа "просто встать и не продолжать, пока не скажут").

Существующая инфраструктура, на которую это ложится:

- **FSM стадий** — `pkg/state/state.go` (`StageStatus` enum + `AllStatuses()`,
  единственный источник для автогенерации TS-типов) и
  `pkg/orchestrator/bus/fsm.go` (`rules map[FSMEvent]Rule`, `GuardCtx`,
  `FSM.Apply`, `ruleAllowsFrom`).
- **`InterruptCh`** — единственный существующий механизм graceful-остановки
  живого агента: `pkg/executor/executor.go`'s `run()` (`select` на
  `<-e.cfg.InterruptCh` → `SIGINT` → `interruptGracePeriod = 15s` → force
  `Kill()`, возвращает sentinel `executor.ErrUserInterrupted`). Канал
  регистрируется на всё время `runWithRetry` (`pkg/orchestrator/retry.go`,
  `interruptChans sync.Map` в `orchestrator.go`) — то есть живёт не только
  во время самого вызова агента, но и во время ретрай-бэкоффа
  (`time.After(retryBackoff)`) внутри той же функции. Сейчас единственный
  отправитель — `Revise()` (`pkg/orchestrator/control_api.go`) для
  `running`-стадии.
- **`--resume`** полностью выводится из наличия файла
  `<phase>.session.json` на диске (`pkg/orchestrator/runner_factory.go`) —
  ничего специально сохранять для резюма не нужно, executor сам решит
  `--resume` vs `--session-id`.
- **`recovery.go`'s `startPlanningForPending`** — единый диспетчер "по
  сохранённому статусу стадии понять, какую функцию перезапустить"
  (используется при рестарте afm). Это ровно та логика, которая нужна и
  для "Continue" после ручной паузы — переиспользуем её, а не дублируем.
- **`notices.jsonl`** (`pkg/orchestrator/stagefiles/notices.go`) — канал
  non-FSM UI-уведомлений, уже используется для `EventAutoAnswered`.

## Goal

Новый статус стадии `paused`, для трёх сценариев:

1. **`auto_run: false` в flow.yaml** — когда стадия (любого типа, включая
   скриптовую) впервые становится готова к активации (зависимости
   удовлетворены), она не стартует сама, а уходит в `paused` с явной
   панелью "ждёт подтверждения" в дашборде. Кнопка **Continue** запускает
   её как обычно. Срабатывает только один раз — при первой активации;
   повторные ретраи после `failed` не паузятся снова.
2. **Ручная пауза** — пункт "Pause" в кебаб-меню стадии, доступен для
   статусов `running`, `planning`, `revising`, `retrying`. Пауза
   аккуратно останавливает текущего агента (тот же SIGINT+grace, что уже
   есть) и переводит стадию в `paused`. **Continue** резюмит агента через
   `--resume` его прерванной сессии.
3. **Скриптовые стадии** — mid-script graceful stop архитектурно не
   поддержан (`RunScript` не принимает `InterruptCh`). Для них пункт
   "Pause" скрыт, пока скрипт выполняется; пауза возможна только через
   `auto_run: false` (сценарий 1), то есть строго до запуска.

## Non-goals

- Не строим отдельный UI для "запланировать паузу после текущего скрипта"
  — mid-script пауза недоступна вообще, без частичных решений.
- Не меняем поведение `Revise` — пауза не переиспользует и не путает
  смысл `revising` (запрос на отдельный статус — осознанный выбор, см.
  обсуждение вариантов A/B ниже).
- Не добавляем per-stage настройку "пауза с фидбеком" — Continue всегда
  просто продолжает без дополнительного текста от пользователя.

## Design

### 1. `StageStatus` и хранение `PausedFrom`

`pkg/state/state.go`:

```go
const (
	...
	StatusRetrying          StageStatus = "retrying"
	StatusPaused            StageStatus = "paused"
	StatusAwaitingUserInput StageStatus = "awaiting_user_input"
	...
)
```

добавляется в `AllStatuses()` (автогенерация TS-типа подхватит).

`StageState` получает новое поле:

```go
type StageState struct {
	Status     StageStatus `json:"status"`
	UpdatedAt  time.Time   `json:"updated_at"`
	// PausedFrom — статус, из которого стадия ушла в paused. Заполняется
	// один раз, при первом входе в paused, и НЕ очищается на выходе из
	// paused (Continue) — namely это совмещает две роли:
	//   1. Пока стадия в paused — куда резюмиться (running/planning/
	//      revising/retrying/pending).
	//   2. После Continue — постоянная метка "эта стадия уже проходила
	//      цикл паузы хотя бы раз", используется auto_run-гейтом (см. §3),
	//      чтобы гейт срабатывал только при самой первой активации, а не
	//      при каждом повторном заходе в pending после failed→retry.
	PausedFrom StageStatus `json:"paused_from,omitempty"`
}
```

`SetStageStatusAt` (единственное место, где `StageState` перезаписывается
целиком — и в живом `Store.Apply`, и в replay `parseEventLog`, оба идут
через один и тот же метод, так что доп. call site не нужен):

```go
func (rs *RunState) SetStageStatusAt(stageID string, status StageStatus, t time.Time) {
	pausedFrom := rs.Stages[stageID].PausedFrom
	if status == StatusPaused {
		pausedFrom = rs.Stages[stageID].Status // "from" на момент перехода
	}
	rs.Stages[stageID] = StageState{Status: status, UpdatedAt: t, PausedFrom: pausedFrom}
}
```

Новый accessor на `Store` (рядом с `Get`):

```go
func (s *Store) PausedFrom(stageID string) StageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Stages[stageID].PausedFrom
}
```

`isIdle()` — `paused` считается простоем, как `awaiting_approval`/
`awaiting_user_input`:

```go
case StatusAwaitingUserInput, StatusAwaitingApproval, StatusPaused:
	return true
```

(`accountIdleAndBackoff` не трогаем отдельно — он уже читает `isIdle`
через единую функцию.)

### 2. FSM: `EvPause` / `EvContinue`

`pkg/orchestrator/bus/fsm.go`:

```go
const (
	...
	EvPause    FSMEvent = "pause"
	EvContinue FSMEvent = "continue"
)

type GuardCtx struct {
	Stage      flow.Stage
	Phase      string
	PausedFrom state.StageStatus // только для EvContinue
}
```

```go
EvPause: {
	From: []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying},
	To:   to(state.StatusPaused),
},
EvContinue: {
	From: []state.StageStatus{state.StatusPaused},
	To:   func(ctx GuardCtx) state.StageStatus { return ctx.PausedFrom },
},
```

`PausedFrom` для `EvContinue` читается из `Store.PausedFrom(stageID)`
вызывающим кодом (`Orchestrator.Continue`, см. §4) и кладётся в `GuardCtx`
— тот же паттерн, что уже используется для `Phase` в `phaseDispatch`
(`EvUserAnswered`/`EvResumeAfterRetry`).

### 3. `auto_run: false` в `flow.yaml`

`pkg/flow/flow.go`, новое поле `Stage`:

```go
// AutoRun, если явно false, приостанавливает стадию сразу при первой
// активации (когда её depends_on выполнены) вместо немедленного старта —
// стадия уходит в paused с PausedFrom=pending и ждёт Continue. nil
// (не задано) или true — прежнее поведение, немедленный старт. Гейт
// срабатывает один раз: см. StageState.PausedFrom.
AutoRun *bool `yaml:"auto_run,omitempty"`
```

```go
func (s Stage) AutoRunDisabled() bool {
	return s.AutoRun != nil && !*s.AutoRun
}
```

Гейт — маленькая проверка, вызываемая в начале каждого из мест, где
`pending`-стадия с удовлетворёнными зависимостями активируется:
`tryActivatePrePlanned` (scheduling.go), `startPlanningForPending`'s ветка
для уже готовых стадий (recovery.go), и симметрично для нового `Continue`
(§4, случай `PausedFrom == pending` эту же проверку обходит намеренно —
Continue есть подтверждение пользователя, гейт не должен сработать
повторно):

```go
func (o *Orchestrator) shouldGateAutoRun(s flow.Stage) bool {
	return s.AutoRunDisabled() && o.opts.Store.PausedFrom(s.ID) == ""
}
```

В `tryActivatePrePlanned` и в соответствующей ветке `startPlanningForPending`
перед вызовом `activateAutoStage`/`activateScriptStage`/`plan-copy+EvReady`
(для `!NeedsPlanning()`) или `EvStartPlanning`+`startWithSupervisor`/
`runPlanningAgent` (для `NeedsPlanning()`) добавляется:

```go
if o.shouldGateAutoRun(s) {
	o.Trigger(s.ID, bus.EvPause, bus.GuardCtx{}, "auto_run: false")
	continue // или return, по месту
}
```

Так как `pending` — статус, из которого FSM обычно выходит безвозвратно
(кроме `EvManualRetry` после `failed`, который снова временно кладёт
стадию в `pending`), гейт естественным образом не мешает последующим
ретраям: к моменту второго прохода через `pending` `PausedFrom` уже
непусто (стадия прошла через `paused` хотя бы раз), и `shouldGateAutoRun`
возвращает `false`.

### 4. Continue — переиспользование диспетчера `recovery.go`

Новый метод в `pkg/orchestrator/control_api.go`, рядом с `Approve`/
`Revise`/`Retry` — синхронный, как и они (переход фиксируется в логе до
возврата):

```go
func (o *Orchestrator) Continue(reqCtx context.Context, stageID string) error {
	if o.currentStatus(stageID) != state.StatusPaused {
		return nil
	}
	stage := o.graph.Stage(stageID)
	if stage == nil {
		return nil
	}
	pausedFrom := o.opts.Store.PausedFrom(stageID)
	to, ok := o.Trigger(stageID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "")
	if !ok {
		return nil
	}

	ctx := o.runContext(reqCtx) // не reqCtx — иначе HTTP-хендлер убьёт агента при возврате
	if pausedFrom == state.StatusPending {
		// Кейсы 1/3: стадия ещё не запускалась вообще — ровно та активация,
		// которую auto_run-гейт пропустил (см. §3).
		if o.activateAutoStage(*stage) || o.activateScriptStage(*stage) {
			return nil
		}
		o.activatePlannedOrPlanningStage(ctx, *stage) // общий хелпер, см. ниже
		return nil
	}

	// Кейсы 2: живой агент был прерван (running/planning/revising) или
	// стадия ждала ретрая (retrying) — резюмируем ТЕМ ЖЕ диспетчером,
	// которым recovery.go резюмирует стадию после рестарта afm: для этой
	// стадии сейчас ровно та же ситуация — "процесс не бежит, а
	// сохранённый статус говорит, что он должен бежать".
	o.resumeStageAtStatus(ctx, *stage, to)
	return nil
}
```

`activatePlannedOrPlanningStage` — вынесенная в отдельную функцию версия
хвоста `tryActivatePrePlanned` (plan-copy + `EvReady` для `!NeedsPlanning()`
без Plan-файла... на деле для кейса 1/3 стадия либо auto/script (уже
обработаны выше), либо обычная с планировщиком) и ветки `default:` из
`startPlanningForPending` (`EvStartPlanning` + `startWithSupervisor`) —
рефакторинг, устраняющий дублирование, а не новая логика.

`resumeStageAtStatus(ctx, s, status)` — вынесенная из `recovery.go`
`startPlanningForPending`'s `switch current` (случаи `StatusRunning`,
`StatusRevising`, `StatusRetrying`, `default` = planning) в отдельную
переиспользуемую функцию, вызываемую:
- из `startPlanningForPending` (как раньше, при рестарте afm);
- из `Continue` выше (при ручном резюме после паузы).

Это единственный содержательный рефакторинг `recovery.go` в этой фиче —
логика "какую горутину поднять для стадии в данном статусе" не должна
жить в двух местах.

### 5. Ручная пауза — `Pause()` и расширение `InterruptCh`

`pkg/orchestrator/control_api.go`, рядом с `Revise`:

```go
// Pause синхронно переводит стадию в paused и (если у неё сейчас живой
// агент или она ждёт ретрая) сигнализирует через тот же interruptChans,
// которым уже пользуется Revise — единственная разница в том, что делает
// runWithRetry, проснувшись: Revise планирует restart с фидбеком,
// Pause — нет, статус уже paused, ничего перезапускать не нужно.
func (o *Orchestrator) Pause(stageID string) error {
	current := o.currentStatus(stageID)
	switch current {
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
	default:
		return nil
	}
	if stage := o.graph.Stage(stageID); stage != nil && stage.IsScript() && current == state.StatusRunning {
		return nil // mid-script pause не поддержан, см. Non-goals
	}

	if _, ok := o.Trigger(stageID, bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		return nil
	}
	if ch, ok := o.interruptChans.Load(stageID); ok {
		select {
		case ch.(chan struct{}) <- struct{}{}:
		default: // уже сигнализирован — не блокируемся
		}
	}
	return nil
}
```

Два места в `pkg/orchestrator/retry.go`'s `runWithRetry`, которые слушают
`interruptCh`, получают понимание "это была пауза, а не revise":

**a) Во время работы агента** — `ErrUserInterrupted` уже возвращается
executor'ом на любой сигнал `InterruptCh`, независимо от причины. Различаем
по статусу, который уже успел проставить `Pause()`/`Revise()` синхронно
ДО сигнала в канал:

```go
if errors.Is(err, executor.ErrUserInterrupted) {
	if o.currentStatus(s.ID) == state.StatusPaused {
		return // Pause() уже зафиксировал переход — просто останавливаемся
	}
	onUserInterrupted() // прежнее поведение Revise
	return
}
```

**b) Во время ретрай-бэкоффа** (`retrying`) — `interruptCh` сейчас не
слушается в этом select вообще (только `time.After`/`ctx.Done()`), хотя
канал уже живой (зарегистрирован на весь `runWithRetry`). Добавляется
третья ветка:

```go
select {
case <-time.After(retryBackoff):
case <-interruptCh:
	return // Pause() уже перевёл стадию в paused, ничего резюмить не нужно
case <-ctx.Done():
	...
}
```

`Revise` не может достать стадию в `retrying` (её precondition — только
`awaiting_approval`/`running`), так что здесь сигнал в `interruptCh`
всегда означает именно `Pause()` — неоднозначности нет.

### 6. HTTP API и `run-client.ts`

`pkg/server/handlers.go`, по образцу `handleRetry`:

- `POST /api/stages/{id}/pause` — precondition: статус ∈ `{running,
  planning, revising, retrying}`, и если стадия скриптовая — статус ≠
  `running` (иначе 409 "pause is not supported mid-script execution").
- `POST /api/stages/{id}/continue` — precondition: статус == `paused`.

`run-client.ts`: `pauseStage(id)`, `continueStage(id)`.

`Orchestrator.Pause`/`Orchestrator.Continue` также добавляются в интерфейс
`actions`, который `pkg/server` уже держит для `Approve`/`Revise`/`Retry`
— иначе новым хендлерам нечего будет вызывать.

### 7. UI

**Кебаб-меню** (`StagesList.tsx`): `KEBAB_STATUSES` расширяется до
`{running, awaiting_approval, planning, revising, retrying}`; новый пункт
"Pause", видимый когда статус ∈ `{running, planning, revising, retrying}`
И `!(stage.isScript && stage.status === 'running')` (поле `isScript`
нужно прокинуть в `Stage`/`/api/status`, если его там ещё нет — сверить
при реализации). "Add note for agent" остаётся видимым только там же, где
и сейчас (`{running, awaiting_approval}`), т.к. это отдельный список
условий, не завязанный на общий `KEBAB_STATUSES`.

**Панель паузы**: `paused` рендерится тем же способом, что и
`awaiting_approval`/`awaiting_user_input` — отдельная видная панель (не
просто строка в event feed) с текстом, зависящим от `PausedFrom`:

- `pending` → "Стадия приостановлена перед первым запуском"
- `running`/`planning`/`revising` → "Стадия приостановлена вручную во
  время выполнения"
- `retrying` → "Стадия приостановлена во время ожидания повтора"

и кнопкой **Continue** → `continueStage(id)`.

Дублирующая запись в `notices.jsonl` при входе в `paused` — тот же
паттерн, что `EventAutoAnswered` (`stagefiles.AppendNotice` + live
`o.ui.Publish`), чтобы клиент, подключившийся после паузы, тоже увидел
её в истории event feed.

### 8. Recovery при рестарте afm

`paused` — no-op ветка в `startPlanningForPending`, рядом с уже
существующими `StatusDone`/`StatusFailed`/`StatusAwaitingApproval`:
рестарт afm не резюмирует паузу сам, только явный клик Continue.

## Testing

- Unit на FSM-таблицу: `EvPause` разрешён только из `{running, planning,
  revising, retrying}`, запрещён из терминальных и из `awaiting_*`;
  `EvContinue` разрешён только из `paused` и корректно вычисляет `To` по
  `PausedFrom` для всех пяти возможных исходных значений.
- `state_test.go`: `SetStageStatusAt` — `PausedFrom` заполняется при входе
  в `paused` и переживает последующие переходы без изменений;
  `isIdle`/`accountIdleAndBackoff` учитывают `paused` как простой.
- Интеграционные тесты (по аналогии с `TestFullDialogCycle`):
  - Кейс 1/3: `auto_run: false` на обычной и на скриптовой стадии —
    стадия уходит в `paused` при первой активации, Continue запускает её,
    повторный `failed → retry` не паузит снова.
  - Кейс 2: `Pause` на `running`-стадии с живым мок-агентом — агент
    получает SIGINT, стадия становится `paused`; `Continue` резюмит
    агента через `--resume` той же сессии (проверить `--resume` в
    аргументах мока).
  - `Pause` на `retrying` — таймер бэкоффа отменяется немедленно, без
    ожидания `retryBackoff`.
  - `Pause` на скриптовой стадии в `running` — 409 от хендлера, статус не
    меняется.
  - Рестарт afm посреди `paused` — статус остаётся `paused` после
    `Store.Open`/`LoadRunState`.
