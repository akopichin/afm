# State persistence

Контракты журнала: [event log](../../docs/internals/event-log.md) и
[control records](../../docs/collaboration-journal.md).

- `Store` держит эксклюзивный flock весь срок жизни. `LoadRunState` читает без
  flock; CLI-мутации активного рана должны получать `ErrRunLocked`.
- Оборванный хвост без newline можно усечь. Битую полную запись с валидными
  записями после неё карантинируй, сохраняя оригинал. Неизвестные kind/schema
  control records отклоняй без «ремонта» или потери записи.
- Snapshot — кэш: sync файла до Close, rename, fsync каталога. Ошибка snapshot
  не равна ошибке авторитетного журнала и не должна останавливать ран.
- Сопоставление run-id учитывает границу имени флоу (`foo` не матчится с
  `foo-bar`). Поиск стадии/последнего рана использует состояние из журнала.
- `PausedFrom` сохраняется после Continue: это одновременно адрес восстановления
  и постоянный маркер уже пройденного `auto_run: false` gate.
- Pre-note хранится атомарно в `prenote.md`, не в FSM. Пустой текст удаляет файл;
  старт стадии не удаляет заметку.

## Persistent run timers

- Меняя учёт времени, сохраняй одинаковые live/replay-пути. Общий
  `accountIdleAndBackoff` вызывается ДО применения перехода и в `Store.Apply`,
  и в parser; control records тоже могут менять idle через helper-barrier.
- Timestamp перехода берётся один раз. Не проставляй `UpdatedAt` untouched
  pending-стадиям при создании состояния: это смещает общий idle-anchor.
- Run-level события имеют seq, но не попадают в stage `History()`. Terminal
  закрывает интервалы; resume задаёт новый anchor, исключая офлайн-перерыв.
- Idle — flow-wide; параллельные backoff-интервалы складываются независимо.
  Классификация `awaiting_assistants` использует `NeedsHumanDecision`: работающий
  helper считается активностью, ожидание решения человека — idle.

- Время стадий (`StageTiming`) — проекция журнала: один `syncStageTiming` после каждой записи любого вида
  (переход, control, run-событие), «до» берётся из сохранённого `OpenKind`, а не из повторной классификации.
  Класс — только в `classifyStageTime`. Crash-граница Run elapsed и стадий — время последней записи журнала.
  Ограничение: офлайн-переход CLI после crash (`EndedAt == nil`) тоже запись и сдвигает эту границу, так что
  простой между crash и командой попадёт в время стадий; закрытый ран (`EndedAt != nil`) таких переходов не считает.
  Стадия из миграции `state.json` (`legacy_load`) в любом статусе кроме `pending`/`ready` считается начатой с
  `Incomplete`: её прошлое не записано, и клиент должен показать «неизвестно», а не 0s.

## Durable completion barrier

Required helper-gate вычисляется из durable-проекции текущих поколений задач.
Используй общий `RequiredBlocker`/`NeedsHumanDecision` для completion, таймеров и
dashboard; наличие файла результата не заменяет committed verdict/waiver.
