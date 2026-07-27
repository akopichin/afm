# Дизайн: agent_suggest — вбросить фразу агенту на любой активной стадии

## Контекст

Сейчас `Revise` жёстко привязан к одному моменту: стадия в `awaiting_approval`
(план готов, ждёт approve/revise). Пользователь хочет тот же принцип
(«останови, добавь фразу, продолжи с её учётом») на **любой активной стадии**,
не только на паузе перед approve — включая момент, когда агент реально
работает (`running`).

Фича экспериментальная — включается только явным флагом, по умолчанию
выключена.

## Экспериментальный флаг

Новая секция `experimental` в `config.yaml` (global + project, тот же
merge-порядок, что и у `docker.enabled`):

```yaml
experimental:
  agent_suggest: true
```

Плюс дублирующий env var `AFM_EXP_AGENT_SUGGEST=1` — используется, только
если `agent_suggest` не задан явно в конфиге (`nil`), по образцу существующего
`DockerConfig.Enabled`/`IsDockerEnabled()`:

```go
type ExperimentalConfig struct {
	AgentSuggest *bool `yaml:"agent_suggest"` // nil = смотрим AFM_EXP_AGENT_SUGGEST
}

func (e ExperimentalConfig) IsAgentSuggestEnabled() bool {
	if e.AgentSuggest != nil {
		return *e.AgentSuggest
	}
	return envFlag("AFM_EXP_AGENT_SUGGEST")
}
```

Флаг гейтит фичу на обоих концах:
- **Backend:** HTTP-хендлер `.../revise` при выключенном флаге ведёт себя так же,
  как сейчас (принимает только `awaiting_approval`) — расширенные статусы
  (`running`) отклоняются, если флаг выключен.
- **Frontend:** `/api/status` отдаёт новое поле `agent_suggest_enabled` (в
  `statusResponse`, по аналогии с уже существующими `stage_interactive`/
  `stage_autonomous`); кебаб-меню в `StagesList` рендерится только если это
  поле `true`. Без бэкенд-флага фронт даже не показывает кнопку.

## FSM: расширение `EvRevise`

Сейчас (`pkg/orchestrator/fsm.go`):
```go
EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval}, To: to(state.StatusRevising)},
```

Расширяем до:
```go
EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning}, To: to(state.StatusRevising)},
```

`planning`/`retrying` намеренно не добавляем — кнопка на фронте не
показывается в этих статусах (см. ниже), так что расширять FSM дальше
`awaiting_approval`+`running` не нужно.

## Механизм прерывания (только для `running` — `awaiting_approval` не меняется)

Для `awaiting_approval` ничего не меняется — там нет живого процесса, `Revise`
уже работает штатно (убить нечего, сразу пишем feedback и перезапускаем).

Для `running` — нужно остановить **реально идущий** subprocess агента, не
трогая общий `ctx` рана (его отмена уже используется для полного шатдауна и
шлёт SIGKILL через `exec.CommandContext` — нам нужен отдельный, более мягкий
канал именно для этого случая).

**Реестр прерываний.** Новое поле на `Orchestrator`, по аналогии с уже
существующим `activeAgents sync.Map`:
```go
interruptChans sync.Map // stageID string → chan struct{}
```
Канал создаётся непосредственно перед вызовом `RunAgent` внутри
`runWithRetry` (для КОНКРЕТНОЙ попытки — не на всё время жизни стадии) и
удаляется из map сразу по возврату из `RunAgent`, успешному или нет. Это
исключает гонку «сигнал долетел до уже завершившейся или ещё не начавшейся
попытки» — каждый запуск агента получает свежий канал.

**Доставка сигнала.** `executor.Config` получает новое поле
`InterruptCh <-chan struct{}`. Горутина, которая обычно читает stdout
построчно, дополнительно слушает этот канал; при получении сигнала —
`cmd.Process.Signal(syscall.SIGINT)` (не отменяет `ctx`, не шлёт SIGKILL).

**Почему SIGINT, а не точное отслеживание границы tool-вызова.** Разбирали
вариант с точным ожиданием завершения текущего `tool_use` (матчинг по ID с
`tool_result`), но `executor.parseStreamEvent` сейчас вообще не парсит
user-role/tool_result сообщения, а сами tool-вызовы иногда идут параллельно
(несколько `tool_use` в одном assistant-сообщении) — точный матчинг был бы
сложным и хрупким. SIGINT — практический компромисс: claude-процесс сам
грамотно завершает текущую атомарную операцию (запись файла — один syscall,
его практически не рвёт сигналом на середине) и выходит, вместо того чтобы
afm пыталась угадать границы снаружи.

**Что происходит дальше.** Once the subprocess exits (interrupted),
`runWithRetry`'s error-handling path видит его — но не как обычную
retryable/non-retryable ошибку, а по маркеру «прервано пользователем»
(отличаем добавлением явного sentinel-error, скажем `ErrUserInterrupted`,
возвращаемого исполнителем, когда он сам инициировал SIGINT — так
`runWithRetry` не путает это с реальным сбоем и не запускает обычный
retry-backoff/«retries exhausted», а уходит прямо в `EvRevise`).

## Персист фразы + перезапуск фазы с фидбеком

Фраза сохраняется на диск как `feedback.md` в каталоге стадии — тем же путём,
что и сейчас (`state.SaveFeedback`). Дальше нужны новые функции-раннеры по
образцу уже существующего `runPlanningWithFeedback`:

- `runImplementationWithFeedback` — читает `feedback.md`, собирает промпт как
  обычный `runImplementationAgent`, но добавляет фразу как retry-context;
  идёт через тот же `runnerFor` → если стадия `Interactive`, автоматически
  получает `--resume <session-id>` (существующий механизм `sessionExists`/
  `loadOrCreateSession` в `runnerFor` не меняется — просто снова
  срабатывает).
- `runAutonomousWithFeedback` — аналогично, для автономного трека.

Три параллельные функции (planning/implementation/autonomous), а не одна
обобщённая — согласуется с уже существующим паттерном в кодовой базе
(`runPlanningAgent`/`runImplementationAgent`/`runAutonomousAgent` тоже не
имеют общей обёртки: у каждой фазы своя специфика проверки завершения —
`plan.md` vs `.done` vs `execution_summary.md`).

## Recovery после краша в `revising`

Сейчас (`recovery.go`):
```go
case state.StatusRevising:
    o.spawnAgent(ctx, s, o.runPlanningWithFeedback) // жёстко закодировано
```

Меняем на определение активной фазы по mtime `*.session.json`
(`detectInterruptedPhase`, уже существует для `resumeInteractiveAgent`) —
только теперь ещё и для автономного трека (нужно добавить проверку
`autonomous_execution.session.json` в `detectInterruptedPhase`, сейчас она
проверяет только planning/implementation/review). По найденной фазе — вызов
соответствующего `run<Phase>WithFeedback`.

## HTTP / API

Существующий endpoint `POST /api/stages/{id}/revise` — без изменений в
адресе и форме тела запроса (`{"feedback": "..."}`). Меняется только
серверная проверка статуса: было `!= awaiting_approval` → return nil;
становится `!= awaiting_approval && != running` (и то — только когда
`Config.Experimental.IsAgentSuggestEnabled()`; при выключенном флаге
поведение как сейчас, один статус).

## UI

**Кебаб (⋮).** На каждом `<li class="stage-item">` в `StagesList.tsx`,
видим только когда: `agent_suggest_enabled` (из `/api/status`) **и** статус
стадии ∈ {`running`, `awaiting_approval`}. Клик открывает маленькое
выпадающее меню (пока один пункт: «Добавить поправку агенту»).

**Модалка.** Клик по пункту меню — предупреждение («агент доведёт текущее
действие до конца и перезапустится с учётом этой фразы») + textarea + кнопки
Отмена/Отправить. Стилизуется через существующие CSS-переменные скина
(coffee/goga/novacorps/base) — ни один цвет не хардкодится, чтобы тема
работала и в тёмном, и в светлом варианте на всех скинах. Новый переиспользуемый
компонент (в кодовой базе сейчас нет обобщённого modal — ближайшие аналоги
`SupervisorDecision.tsx`/`Maximizable.tsx` не подходят по форме), назовём
условно `ConfirmPromptModal` — implementation plan зафиксирует точное имя и
расположение файла.

## Тестирование

- Backend: unit-тест на `ExperimentalConfig.IsAgentSuggestEnabled()`
  (nil→env, explicit true/false → приоритет над env — как у
  `IsDockerEnabled`).
- Backend: интеграционный тест — стадия `running` (реальный оркестратор,
  фейковый `Runner`, скрипт, который блокируется, пока не получит SIGINT) →
  вызов `Revise` → подтверждение, что процесс получил именно SIGINT (не
  SIGKILL/ctx-отмену), что `feedback.md` записан, что стадия ушла в
  `revising` → `running` (через `run<Phase>WithFeedback`) → `done`.
- Backend: `EvRevise` теперь разрешён из `running` — тест FSM-правила.
- Backend: recovery.go — тест, что краш в `revising` для автономной стадии
  резюмится через `runAutonomousWithFeedback`, а не жёстко через
  `runPlanningWithFeedback`.
- Frontend: кебаб скрыт при `agent_suggest_enabled: false` или в
  «неактивных» статусах; модалка открывается/закрывается; сабмит шлёт
  `POST .../revise` с текстом.

## Самопроверка спека

- **Плейсхолдеров нет** — все секции содержат конкретные решения, не TBD.
- **Внутренняя непротиворечивость** — `awaiting_approval` явно помечен как
  «без изменений в механике прерывания» везде, где это могло быть неясным.
- **Масштаб** — фича шире одной кнопки (FSM, recovery, 2 новых раннера,
  interrupt-канал, конфиг), но это один связный кусок функциональности —
  не требует декомпозиции на отдельные спеки.
- **Неоднозначность** — имя нового modal-компонента и точный copy
  предупреждения оставлены на усмотрение implementation plan (не
  архитектурные решения, не блокируют дизайн).
