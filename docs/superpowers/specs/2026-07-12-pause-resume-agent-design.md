# Дизайн: пауза и возобновление агента из UI (с комментарием)

- Дата: 2026-07-12
- Статус: черновик, ожидает ревью
- Ветка: `theme` (master — основная)

## Контекст и цель

Пользователи просят фичу: **по кнопке из UI остановить бегущего агента, написать ему комментарий и возобновить выполнение с учётом комментария.**

Подтверждённые требования (сессия brainstorming):

1. **Оба сценария:**
   - **Steer (мгновенная корректировка)** — агент идёт не туда, пользователь прерывает, пишет «делай через Postgres», агент тут же возобновляется и учитывает это.
   - **Pause & resume later** — остановить сейчас (конец дня / подумать / контекст переполнен), опционально оставить заметку, возобновить позже отдельным действием.
2. **Масштаб: все агенты, включая обёртки** (`glm51`, `ai-free.claude-glm` и т.п.).

### Текущее состояние (пробел)

- **Нет способа остановить бегущего агента точечно.** Остановка процесса — только жёсткая (`os.Process.Kill()` = SIGKILL) в `pkg/executor/executor.go:471,476`, по idle-timeout или по `ctx.Done()`. Контекст в `cmd/afm/run.go:234` ловит только `os.Interrupt` — убивает **весь** ран, а не одну стадию. Мягкого interrupt (SIGINT/SIGTERM) нет.
- `activeAgents` (`pkg/orchestrator/orchestrator.go:88`) — `sync.Map` флагов `stageID → struct{}`. **Ссылки на процесс/cancel-func нет** — послать сигнал конкретной горутине нельзя.
- **Зато есть готовый примитив возобновления** — диалоговый протокол: `<stageDir>/<phase>.session.json` хранит UUID сессии claude; `runnerFor` (`orchestrator.go:248`) ставит `Resume: sessionExists(...)`; executor прокидывает `--resume <id>`; `onUserAnswered` (`orchestrator.go:568-634`) уже умеет перезапускать агента с `--resume`, скармливая ему ответ. Этот паттерн — модель для resume с комментарием.
- FSM (`pkg/state/state.go`, `pkg/orchestrator/fsm.go`): 10 статусов, терминальные `done`/`failed`. **Нет `paused`.**

### Не-цели (v1)

- Истинное продолжение сессии для не-claude агентов (impossible — нет общей модели session/resume). Resume для них = рестарт фазы с заметкой.
- Гарантия атомичности работы агента при прерывании mid-tool-call (присуще любому interrupt).
- CLI-команды `afm pause`/`afm resume` (фича инициируется из UI; CLI можно добавить позже как тривиальное расширение).

## Выбранный подход

**Статус `paused` + мягкий SIGINT + единый resume через существующий `runnerFor`/session.**

Альтернативы (отвергнуты на brainstorming):
- *Reuse `awaiting_user_input` + диалог* — диалог agent-initiated (агент сам ждёт); здесь агент работает, и interrupt-инфра всё равно нужна. Гибрид ломает `detectDialogViolation` и путает семантику.
- *Жёсткий stop + retry-with-feedback* — SIGKILL не даёт claude сохранить сессию → resume вырождается в рестарт даже для интерактивных claude-стадий, теряя главную ценность (продолжить, а не переделать).

## Изменения по компонентам

### FSM (`pkg/state/state.go`, `pkg/orchestrator/fsm.go`)

Новый статус `StatusPaused = "paused"` (не терминальный).

| Событие | From | To |
|---|---|---|
| `EvInterrupt` | `planning`, `running`, `revising`, `retrying` | `paused` |
| `EvResume` | `paused` | `phaseDispatch` (`planning`/`revising` → `planning`, иначе `running`) |
| `EvComplete` | + `paused` | `done` |

Пауза **недоступна** из `awaiting_user_input` (там своя пауза-диалог), `awaiting_approval`/`ready` (агент не бежит), `done`/`failed` (терминальные).

`EvComplete: paused → done` нужен для гонки «агент завершился естественно ровно в момент паузы» — чтобы легитимное завершение не застряло в `paused`.

### Executor: плавный interrupt (`pkg/executor/executor.go`, `run()`, стр. 394-481)

Go 1.26 → используем stdlib `os/exec.Cmd.Cancel` + `WaitDelay`:

1. Агент в **собственной группе процессов** (чтобы сигнал доставался и обёртке, и дочернему claude):
   ```go
   cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
   ```
2. Прерывание через ctx-cancel, но **SIGINT + grace**, не мгновенный SIGKILL:
   ```go
   cmd.Cancel    = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
   cmd.WaitDelay = 10 * time.Second   // не вышел за grace → os/exec сам SIGKILL
   ```
3. Убираем ручной `case <-ctx.Done(): cmd.Process.Kill()` (его делает os/exec через `Cancel`). В `case <-done` детектим interrupt: по завершении процесса определяем, **вызван ли выход сигналом** (exit-by-signal / код возврата), и только тогда возвращаем сенстинел `errInterrupted`. Проверка `ctx.Err() != nil` недостаточна: она ложно сработает, если агент завершился естественно ровно в окне cancel (см. «Ключевые предположения и риски»).
4. Idle-таймер остаётся жёстким (повисший агент), но тоже бьём по группе: `syscall.Kill(-pid, syscall.SIGKILL)`.

Прерывание на уровне оркестратора = **отмена per-stage контекста** (не всего рана).

### Оркестратор: tracking процесса + рефактор спавна (`pkg/orchestrator/orchestrator.go`)

- `activeAgents` хранит `*agentHandle{ cancel context.CancelFunc; phase string }` вместо `struct{}`.
- **Один хелпер** вместо ~6 копий боилерплейта (onUserAnswered×3, onRevised, onManualRetry, retry.go):
  ```go
  func (o *Orchestrator) runAgent(parent context.Context, st flow.Stage, phase string,
                                    body func(context.Context, flow.Stage)) {
      ctx, cancel := context.WithCancel(parent)
      sem := o.semFor(st); sem.acquire()
      o.markAgentActive(st.ID, cancel, phase)
      defer func() { o.markAgentDone(st.ID); sem.release() }()
      body(ctx, st)   // body получает per-stage ctx
  }
  ```
  Спавн-сайты сводятся к `go o.runAgent(ctx, *stage, phase, o.runXAgent)`. Это и единая точка регистрации per-stage cancel/phase, и устранение дублирования (улучшение кода, в котором работаем).

#### `Interrupt(stageID, comment)`

1. Атомарно (O_EXCL/temp+rename) пишем комментарий в `<stageDir>/<phase>.usernote.md`, если непустой; дублируем в `<phase>.dialog.jsonl` через существующий `appendLine` (история для UI).
2. Читаем фазу из handle.
3. `Trigger(EvInterrupt)` → `paused` (синхронно). Если переход отвергнут (стадия успела стать `done`/`failed`) → abort с понятным сообщением (агент завершился сам).
4. `publish(EventInterrupt)` для UI (опционально — статус-чейндж уже течёт через `stage_status_changed`).
5. `handle.cancel()` → executor шлёт SIGINT группе → процесс выходит → `run()` возвращает `errInterrupted`.

Порядок «сначала `paused`, потом `cancel`» критичен: горутина по выходу видит `paused`.

#### Подавление завершения при паузе

Маршрутизация по итогам прогона:

- **Выход вызван interrupt** (`errors.Is(err, errInterrupted)`): сайт публикации `EventAgentCompleted` **не публикует** событие (это была чистая пауза, не завершение). Горутина освобождает семафор и выходит.
- **Естественное завершение** (даже если оно гоняется с окном паузы — статус уже `paused`): публикует `EventAgentCompleted` как обычно → `onAgentCompleted` → `EvComplete: paused → done`. Стадия корректно доходит до `done`, не застревая.
- `runWithRetry` (`pkg/orchestrator/retry.go`, сейчас `ctx.Done → EvFail "cancelled during retry"`): единая проверка — **если `currentStatus == paused` → чистый возврат** (пауза), иначе прежний `EvFail` (shutdown рана). Покрывает и backoff-слип, и прерывание mid-execution (через `errInterrupted`).

#### `Resume(stageID, comment)`

1. Дописываем комментарий в `usernote.md` (если есть) + в `dialog.jsonl`.
2. `Trigger(EvResume, phase)` → `phaseDispatch`.
3. Event-loop хендлер стартует агента через `runAgent` (зеркало `onUserAnswered`): `runnerFor` с `--resume` (если `session.json` есть), builder внедряет `usernote`.

### Доставка комментария агенту (`pkg/prompts/`)

`runnerFor` уже ставит `Resume = sessionExists(...)`. На resume-прогоне:

- **Интерактивный claude (`Resume=true`)**: stdin = **только** обрамлённая заметка (новый ход разговора), не весь промпт фазы заново:
  ```
  [user note during pause]
  <note>

  Take this into account and continue.
  ```
- **Прочие (`Resume=false`)**: stdin = полный промпт фазы + `\n\n[user note]\n<note>`.
- Заметка **поглощается один раз**: после прочтения `usernote.md` удаляется (миррует с паттерном `answer.json`).
- **Resume без комментария**: `--resume` требует входа → шлём минимальное `"Continue."`.

### API (`pkg/server/server.go`, `handlers.go`, `cmd/afm/run.go`)

По образцу `/dialog/cancel`. В `routeStages` (`server.go:128`):

| Метод | Путь | Тело | Валидация статуса | Колбэк |
|---|---|---|---|---|
| POST | `/api/stages/<id>/interrupt` | `{comment?: string}` | ∈ {planning, running, revising, retrying} | `InterruptFn(ctx, stageID, comment)` |
| POST | `/api/stages/<id>/resume` | `{comment?: string}` | == `paused` | `ResumeFn(ctx, stageID, comment)` |

Поля `Server`/`Config`: `interruptFn`/`InterruptFn`, `resumeFn`/`ResumeFn`. Wire в `run.go` → `orch.Interrupt`/`orch.Resume`. Хендлеры: `extractStageID` + `isValidStageID` + snapshot → проверка статуса (иначе 400) → вызов колбэка → JSON `{ok, status}`.

### UI (`pkg/web/dashboard/`)

Без нового WS-типа: смена статуса течёт через `stage_status_changed` → `handleEvent` → ре-рендер; кнопки диктуются статусом.

- **`⏸ Pause`** — видна в активных interruptible статусах. Клик → одна переиспользуемая модалка с `<textarea>` и двумя действиями:
  - **Pause** → `POST /interrupt {comment}` (стадия → `paused`).
  - **Pause & steer** → `POST /interrupt {comment}`, затем `POST /resume` (мелькает `paused` → снова бежит с заметкой). Steer одним кликом.
- **`▶ Resume`** — видна только в `paused`. Клик → модалка (опц. комментарий) → `POST /resume {comment}`.
- `paused`: добавить в `statusLabels` + акцентный цвет (амбер из существующих CSS-переменных).
- Заметки видны в панели диалога (через `dialog.jsonl`).

## Recovery и edge-cases

1. **Рестарт afm во время паузы**: `paused` персистентен в `state.json`. В `pkg/orchestrator/recovery.go` добавить `paused` в набор «не перезапускать» (он не transient) — стадия ждёт ручного resume. `session.json` для `--resume` переживает рестарт (хранится у claude).
2. **Прерывание mid-tool-call** (агент дописывает файл): SIGINT + grace 10с дают claude дойти до чистой точки, но атомичность его работы не гарантируется — присуще любому interrupt. `--resume` позволяет claude восстановиться. Документируем как ограничение.
3. **Пауза во время `retrying` (backoff)**: cancel рвёт `runWithRetry`; проверка `status==paused` → чистый возврат (не `EvFail`). Resume перезапускает фазу, счётчик retry сбрасывается (ручное вмешательство превосходит авт учёёт).
4. **Pause из `paused` / терминальных**: `EvInterrupt` не разрешён → хендлер 400.
5. **Не-claude агент не переживает interrupt**: resume рестартует фазу с заметкой (best-effort).
6. **Гонка «завершился в момент паузы»**: естественное завершение публикует `EventAgentCompleted` → `EvComplete: paused → done`.

## Тестирование

- **FSM (unit)**: правила `EvInterrupt`/`EvResume`; отказ из `awaiting_user_input`/`done`/`failed`; `EvComplete: paused → done`.
- **Executor (unit)**: `Cancel` шлёт SIGINT группе (дочерний процесс фиксирует сигнал); `errInterrupted` при ctx-cancel, `nil` при естественном выходе; idle остаётся SIGKILL.
- **Orchestrator (unit)**: `Interrupt` пишет usernote + `paused` + cancel; горутина с `errInterrupted` не публикует `EventAgentCompleted`; гонка «завершился в момент паузы» → `done`; `Resume` стартует через `runAgent`; `runWithRetry` при `status==paused` не фейлит.
- **Integration (по образцу `TestFullDialogCycle`)**: реальный bash mock-агент через `stage.Command` — pause mid-loop → assert SIGINT получен + статус `paused` + `usernote.md` на диске; resume → mock читает usernote, продолжает. Интерактивные стадии игнорируют внедрённый `Runner` (см. CLAUDE.md), поэтому тест гоняет реальный bash через `stage.Command`.
- **Handlers (unit)**: валидация `/interrupt`, `/resume` (статусы → 400), атомарная запись `usernote.md`.

## Ключевые предположения и риски

- **claude сохраняет сессию при SIGINT так, что `--resume` подхватывает контекст.** Косвенное подтверждение: `recovery.go` (`resumeInteractiveAgent`) именно так восстанавливает прерванные перезапуском агентов. **Первый шаг реализации — спайк**, проверяющий поведение headless `claude --print ... --resume <id>` после SIGINT. Если предположение не подтвердится — fallback: для claude тоже рестарт фазы с заметкой (как для не-claude), потеряв продолжение разговора.
- `Cmd.Cancel`/`WaitDelay` требуют Go 1.20+; в `go.mod` — 1.26 (версию не меняем).
- **Надёжно отличить «выход от SIGINT» от «естественного выхода в окне cancel»** — тонкость os/exec: грубая проверка `ctx.Err()` misклассифицирует естественное завершение как паузу (и застрянет в `paused` с готовой работой). Детект должен опираться на причину выхода (exit-by-signal / код возврата), а не на состояние ctx. Точный механизм — за деталями реализации; fallback на worst case (редкая гонка → resume доставит стадию до `done`).

## Ограничения (v1)

- Resume не-claude агентов = рестарт фазы (не продолжение сессии).
- Прерывание mid-tool-call не гарантирует атомичность работы агента.
- Счётчик retry сбрасывается при resume после паузы во время retry.
- Steer = pause→resume с кратковременной вспышкой `paused` (не атомарная операция на сервере).

## За пределами v1

- CLI `afm pause`/`afm resume` (тривиально поверх `orch.Interrupt`/`Resume`).
- Abort-stage из `paused` напрямую (сейчас `FailStage` уже работает из любого нетерминального статуса, но без UI-кнопки).
- Сохранение/просмотр истории заметок как полноценной ленты (сейчас — в `dialog.jsonl`).
