# Дизайн: фаза planning ждёт depends_on

Дата: 2026-06-12
Статус: одобрен

## Проблема

Сейчас оркестратор запускает планирование для всех pending-стейджей сразу при
старте (`startPlanningForPending`, recovery.go — «planning runs eagerly before
deps are done»). От `depends_on` зависит только фаза implementation
(`startReadyStages` + `Graph.ReadyStages`).

Следствия:

- планировщик зависимого стейджа не видит планы, артефакты и код своих
  зависимостей — `CollectDependencyPlans` и `CollectArtifacts` собирают пустоту;
- все планинги выполняются асинхронно одновременно, даже для стейджей,
  которые могут вообще не понадобиться (если зависимость упадёт).

## Решение

По умолчанию planning стейджа запускается только когда все его `depends_on`
находятся в статусе `done` — та же логика готовности, что у implementation.
Старое поведение возвращается флагом на стейдже.

## 1. Модель (pkg/flow)

В `Stage` добавляется поле `EagerPlanning bool` с yaml-тегом `eager_planning`:

```yaml
stages:
  - id: api
    depends_on: [core]
    eager_planning: true   # опционально: планировать сразу, не дожидаясь core
```

По умолчанию `false` — planning ждёт зависимостей. Дополнительная валидация не
нужна: флаг безвреден для стейджей без зависимостей (их planning и так
стартует сразу).

## 2. Оркестратор (pkg/orchestrator)

### Гейт при старте

В `startPlanningForPending` (recovery.go), в ветке `default` (pending), перед
запуском planning добавляется проверка `s.EagerPlanning || o.depsDone(s)`.
Если зависимости не готовы — стейдж остаётся в `pending`, горутина не
запускается.

Восстановительные ветки (`running`, `retrying`, `revising`,
`awaiting_user_input`) не меняются: раз стейдж уже был в работе, он
возобновляется как раньше.

### Догонка по событиям

Новый метод `startPlanningForUnblocked(ctx)`: обходит стейджи в `pending` с
`NeedsPlanning()`, у которых `depsDone()` истинно, и для каждого запускает
`runPlanningAgent` (через семафор, как все агенты).

Точки вызова — рядом с существующими `tryActivatePrePlanned`:

- `onAgentCompleted`, ветка implementation — зависимость стала `done`;
- `onApproved`, ветка planning-only стейджа — он стал `done`;
- конец `startPlanningForPending` — recovery, когда зависимость уже `done`
  из сохранённого состояния.

## 3. Ошибки и краевые случаи

- **Падение зависимости.** Гейченный стейдж стоит в `pending`; существующий
  `failBlockedStages` переводит его в `failed`. Работает без изменений и даже
  выгоднее текущего поведения: не тратится planning у обречённых стейджей.
- **Дубль запуска.** `startPlanningForUnblocked` может быть вызван дважды
  подряд (два события). Защита: `o.Trigger(s.ID, EvStartPlanning, ...)`
  вызывается синхронно в event loop до запуска горутины; FSM разрешает
  `EvStartPlanning` только из `pending`/`retrying`/`revising`, поэтому
  повторный вызов вернёт `ok == false` и стейдж пропускается. Повторный
  `EvStartPlanning` внутри `runPlanningAgent` остаётся безопасным no-op —
  так уже работает recovery-путь.
- **`max_parallel`.** Семафоры не меняются, лимиты параллелизма работают как
  раньше.
- **`eager_planning: true`.** Planning стартует при старте оркестратора, как
  в текущем поведении; implementation по-прежнему ждёт `depends_on`.

## 4. Тесты

- Unit (orchestrator/recovery): стейдж B (`depends_on: [A]`) не получает
  `EvStartPlanning` при старте; после `done` у A — получает.
- Unit: `eager_planning: true` — planning стартует сразу.
- Интеграционный (по образцу `integration_test.go`): цепочка A→B — planning B
  запускается только после `done` A; падение A → B `failed` без планирования.
- flow_test: парсинг yaml с `eager_planning`.

## 5. Документация

Обновить `README.md` и `example-flow.yaml`: описать новое поведение по
умолчанию и флаг `eager_planning`.
