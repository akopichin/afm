# Lifecycle-хуки для AFM — дизайн

Дата: 2026-09-18
Статус: утверждён к реализации (Phase 1)

## Краткое описание

В AFM добавляется отдельный механизм **lifecycle-хуков** — наблюдателей за
событиями флоу и стадий. Он намеренно отделён от существующих `script_before`
и `script_after`:

- `script_before`/`script_after` участвуют в выполнении стадии и могут её
  заблокировать (сегодня в коде они внутренне называются «hooks» —
  `pkg/orchestrator/hooks.go`, FSM-события `EvHookFailed`/`EvHookResolved`,
  статус `StatusHookFailed`);
- lifecycle-хуки только наблюдают: отправляют уведомления, обновляют статусы во
  внешних системах, пишут метрики;
- ошибка lifecycle-хука **никогда** не меняет FSM, результат стадии или флоу.

Такое разделение не даёт сбою скрипта уведомлений/телеметрии сломать стадию и
исключает рекурсию вида `stage_failed -> ошибка хука -> stage_failed`.

### Терминология (разрешение коллизии «hook»)

Слово «hook» в коде уже занято блокирующими `script_before/after`. Чтобы не
плодить двусмысленность:

- Пакет — `pkg/lifecyclehooks`.
- YAML-поле — `hooks:` (в контексте нового механизма).
- В Go-коде везде префикс `Lifecycle*`: `LifecycleHook`, `LifecycleEvent`,
  `LifecycleDispatcher`, `LifecycleRunner`.
- Существующие `script_before`/`script_after` и их код **не трогаем**.
- События, отражающие жизненный цикл существующих script-хуков, называются
  `stage_script_before_*` и `stage_script_after_*` (а НЕ `stage_before_hook_*`),
  чтобы читатель не путал их с новым механизмом.

## Разбиение на фазы

Фича большая, поэтому реализуется тремя независимыми поставками. Каждая фаза —
отдельный план и отдельный набор коммитов.

### Phase 1 — observer-хуки, live best-effort

Самодостаточный работающий механизм: можно навесить реальные хуки (уведомления,
метрики, обновление внешних статусов) на все lifecycle-события, они срабатывают
вживую.

Входит:
- пакет `pkg/lifecyclehooks` (`types.go`, `validate.go`, `matcher.go`,
  `dispatcher.go`, `runner.go` — **без** `outbox.go`);
- четыре scope (global/project/flow/stage), детерминированный порядок и
  наследование, дедуп по id (см. ниже);
- `events: all` и `skip_events`;
- публичный каталог событий, закодированный один раз;
- JSON в stdin (`schema_version: 1`) и скалярные `AFM_*` env-переменные;
- timeout и ограниченные retries на хук;
- последовательное выполнение вызовов одного хука, параллельное — разных;
- логи `.afm/runs/<run-id>/hooks/<hook-id>.log`;
- предупреждение в dashboard при окончательном сбое хука (UI-notice, не меняет
  FSM);
- bounded flush очереди dispatcher перед нормальным выходом.

Не входит (сознательно): durable outbox, гарантия доставки после рестарта,
декларативные секреты/`env`, Docker-транспорт секретов, `[REDACTED]`,
`inherit_env`/минимальное окружение. В Phase 1 hook-процесс **наследует
окружение процесса AFM** — этого достаточно, чтобы отдать скрипту токен через
переменную shell или чтобы скрипт сам прочитал свой файл. Доставка — live
best-effort: пока afm жив, событие уедет; при краахе в момент до отправки оно
теряется без повтора.

### Phase 2 — durable-доставка

- `hook_events.jsonl` (envelope, fsync до запуска команды) и
  `hook_deliveries.jsonl` (пары `event_id + hook_id`);
- реконсиляция при старте: сверить `events.jsonl` с outbox по durable `seq` и
  восстановить отсутствующие FSM-производные lifecycle-события;
- повтор недоставленных пар после рестарта;
- гарантия **at-least-once** — только для FSM-производных событий (см. раздел
  «Гарантии доставки»).

> **Открытые вопросы дизайна (по ревью codex 2026-09-18, Phase 2 ОТЛОЖЕНА).**
> Наивная модель «envelope + deliveries + реконсиляция по seq» не обеспечивает
> настоящую at-least-once. Перед реализацией Phase 2 нужно решить:
> 1. Обязательства доставки хранить durable по паре `event_id+hook_id` (+payload,
>    +seq), замороженные на момент emit — а не пересчитывать из текущих `d.hooks`
>    (иначе дрейф flow/config теряет/переадресует обязательства).
> 2. FSM-доставка не должна дропаться при переполнении очереди (иначе событие
>    теряется без краха: `Flush` видит `sent==done`, завершённый ран не
>    переоткрывается). Нужна недропаемая durable-очередь + учёт pending в `Flush`
>    + дренаж завершённых ранов на старте.
> 3. Ошибки outbox (`Open`/`Write`/`Sync`) — явное состояние (retry/pause/
>    гарантированный дренаж), а не молчаливая деградация в best-effort.
> 4. Replay должен усекать torn-tail до последнего полного `\n` перед append
>    (иначе следующая запись склеивается и теряется); полная битая строка в
>    середине — corruption, как в `state.Open`.
> 5. Порядок доставки — по `Transition.Seq`, а не по гонке append.
> 6. reconcile/redeliver — ДО live `flow_started/flow_resumed` и до открытия
>    HTTP API (dashboard принимает мутирующие запросы до `Orchestrator.Run`).
> Полный отчёт: `.superpowers/sdd/2026-09-18-lifecycle-hooks-phase1/codex-phase2-plan-review.txt`.

### Phase 3 — секреты и Docker

- пользовательский `env` со ссылками `env:`/`file:` (та же семантика, что у
  `auth.from` в Docker-режиме);
- вынос резолвера секретов из `pkg/docker/secrets.go` в нейтральный
  `pkg/secrets`, рефактор Docker-кода на новый пакет;
- Docker-транспорт: значение секрета передаётся через транзиентную
  `AFM_SECRET_<hook>` (bare `-e NAME`), внутри контейнера dispatcher
  перекладывает его в целевую переменную только для нужного hook-процесса;
  transport-переменные фильтруются из окружения обычных агентов/скриптов;
- `inherit_env: true` и минимальное окружение по умолчанию;
- редакция `[REDACTED]` в stdout/stderr и в dashboard;
- приоритеты `secrets.env` (project > global > env процесса).

Остальной документ описывает целевое состояние всех трёх фаз; там, где поведение
относится к Phase 2/3, это отмечено явно.

## Конфигурация

Хуки объявляются на четырёх уровнях:

1. глобальный `~/.afm/config.yaml`;
2. проектный `.afm/config.yaml`;
3. корень YAML-файла флоу;
4. внутри отдельной стадии.

Поле называется `hooks`, во множественном числе, поскольку содержит список.

### Глобальная или проектная конфигурация

```yaml
hooks:
  - id: notifications
    events: all
    skip_events:
      - stage_question_answered
      - stage_retry_started
    command: "bash /opt/afm-hooks/notify.sh"
    timeout: 30s
    retries: 2
```

### Конфигурация флоу и стадии

```yaml
name: release

hooks:
  - id: release-notifications
    events:
      - flow_started
      - flow_resumed
      - flow_finished
      - flow_failed
      - stage_failed
      - stage_question_asked
    command: "./scripts/notify-flow.sh"

stages:
  - id: deploy
    name: Deploy production

    hooks:
      - id: deploy-notifications
        events:
          - stage_execution_started
          - stage_finished
          - stage_failed
          - stage_script_before_failed
        command: "./scripts/notify-deploy.sh"
```

Поле `events` принимает либо скаляр `all`, либо список:

```yaml
events: all
```

```yaml
events: [stage_finished, stage_failed]
```

`skip_events` применяется после `events`:

```text
итоговые события = events - skip_events
```

Неизвестное имя события — ошибка конфигурации (не молча игнорируется, иначе
опечатка в правиле уведомлений останется незаметной).

## Наследование и порядок

Итоговый список хуков собирается в детерминированном порядке слоёв:

```text
глобальный config -> проектный config -> flow.yaml -> stage
```

- Глобальные, проектные и flow-хуки получают события всех стадий.
- Stage hook получает только события своей стадии.
- На одно событие можно назначить несколько хуков.
- Хуки **разных id** со всех слоёв складываются, а не заменяют друг друга.
- У каждого хука есть `id`, используемый в логах, диагностике и журнале
  доставки.
- Flow-события (`flow_*`) недопустимы внутри stage hook — ошибка конфигурации.

### Дедуп по `id` при коллизии слоёв

`id` уникален в пределах одного слоя (дубликат внутри слоя — ошибка). При
коллизии `id` **между** слоями действует precedence:

```text
global < project < flow < stage
```

Более специфичный слой заменяет одноимённый хук верхнего (project переопределяет
global, flow — project, stage — flow). Это распространяется и на слияние config —
поэтому `mergeFile` для `Config.Hooks` реализуется как keyed-merge по `id`
(project затирает global по совпадающему id, остальные складываются), а не как
слепой append. Разные `id` со всех слоёв в итоге складываются в один список.

## Публичный каталог событий

Lifecycle-хуки не получают сырые имена FSM (`EvStartRun`) или внутренние события
шины — это детали реализации. Каталог небольшой, семантический и стабильный,
закодирован в одном месте (`pkg/lifecyclehooks`), используется и валидацией, и
матчером.

### События флоу

```text
flow_started
flow_resumed
flow_finished
flow_failed
flow_interrupted
```

Мапинг на код (`Orchestrator.Run`):

- `flow_started` — новый логический запуск (`lastSeq == 0` при `state.Open`).
- `flow_resumed` — AFM открыл существующий незавершённый запуск
  (`lastSeq != 0`). Прокидывается через `Options.Resumed`.
- `flow_finished` — `Run` завершился штатно и все стадии `Done`.
- `flow_failed` — есть `Failed`-стадия, заблокировавшая завершение, либо
  сработал `setFatal` (fatal-путь выхода `Run`).
- `flow_interrupted` — контекст отменён без fatal (Ctrl+C / внешняя отмена).

### События стадии

```text
stage_planning_started
stage_plan_ready
stage_approved
stage_revision_started
stage_execution_started
stage_question_asked
stage_question_answered
stage_retry_scheduled
stage_retry_started
stage_paused
stage_resumed
stage_finished
stage_failed
```

Неоднозначное `stage_start` не вводим: явные `stage_planning_started` и
`stage_execution_started` точнее описывают момент.

Основной источник этих событий — `triggerWithSeq` (единственная точка, через
которую проходит каждый применённый FSM-переход). Мапинг FSM-событий на публичные
(ориентировочный, уточняется при реализации):

```text
EvStartPlanning     -> stage_planning_started
EvPlanReady         -> stage_plan_ready
EvApprove           -> stage_approved
EvRevise            -> stage_revision_started
EvStartRun          -> stage_execution_started
EvAskUser           -> stage_question_asked
EvUserAnswered      -> stage_question_answered
EvScheduleRetry     -> stage_retry_scheduled
EvResumeAfterRetry  -> stage_retry_started
EvManualRetry       -> stage_retry_started
EvPause             -> stage_paused
EvContinue          -> stage_resumed
EvComplete          -> stage_finished
EvFail              -> stage_failed
```

`stage_question_asked`/`stage_question_answered` для авто-отвеченных
(non-interactive) вопросов, которые проходят не через FSM, а через
dialog-поллер, эмитятся на границе операции в dialog-коде (не-FSM путь, см.
ниже).

### События скриптов и исполняемых script-хуков стадии

```text
stage_script_started
stage_script_finished
stage_script_failed

stage_script_before_started
stage_script_before_finished
stage_script_before_failed

stage_script_after_started
stage_script_after_finished
stage_script_after_failed
```

Семантика для скрипта с повторными попытками:

- `*_started` — один раз перед всей последовательностью попыток;
- `*_finished` — один раз после итогового успеха;
- `*_failed` — один раз после исчерпания всех попыток;
- ошибки отдельных попыток остаются в логах и не создают lifecycle-событий.

`events: all` = все публичные lifecycle-события. Высокочастотные и внутренние
события (`agent_action`, `script_output`, сигналы пробуждения event loop) в
`all` не входят.

## Данные события

Основной контракт — JSON в стандартном вводе команды. Данные не передаются
позиционными аргументами (проблемы с экранированием, ограничением размера и
command injection).

Пример payload:

```json
{
  "schema_version": 1,
  "event_id": "release-20260918-120000-ab12:transition:42:stage_failed",
  "event": "stage_failed",
  "occurred_at": "2026-09-18T12:04:31.381Z",
  "flow": {
    "name": "release",
    "run_id": "release-20260918-120000-ab12",
    "resumed": false,
    "run_dir": "/project/.afm/runs/release-20260918-120000-ab12",
    "root_dir": "/project"
  },
  "stage": {
    "id": "deploy",
    "name": "Deploy production",
    "from": "running",
    "to": "failed",
    "phase": "implementation"
  },
  "reason": "agent exited with status 1",
  "data": {}
}
```

`schema_version` присутствует с первой версии — позволяет развивать структуру,
не ломая пользовательские скрипты незаметно.

### Схема `event_id`

- FSM-производные: `<run-id>:transition:<seq>:<event-name>` — `seq` это durable
  sequence number перехода (`state.Transition.Seq`).
- Flow-события: `<run-id>:flow:<event-name>` (уникальны в пределах запуска).
- Не-FSM события стадии (`stage_script_*`, авто-ответы): `<run-id>:<stage-id>:<event-name>`.
  В пределах одной активации стадии событие уникально. При ручном retry стадии
  `stage_script_started` может повториться с тем же id — это допустимо, так как
  для не-FSM событий доставка best-effort (см. ниже).

### Переменные окружения

Часто используемые скаляры дополнительно передаются через env:

```text
AFM_HOOK_EVENT
AFM_HOOK_EVENT_ID
AFM_FLOW_NAME
AFM_RUN_ID
AFM_RUN_DIR
AFM_ROOT_DIR
AFM_STAGE_ID
AFM_STAGE_NAME
AFM_STAGE_FROM
AFM_STAGE_TO
```

Для событий уровня флоу переменные стадии пусты. `AFM_ROOT_DIR` — эффективный
`flow.root_dir` (при пустом `root_dir` — CWD процесса AFM). Полный payload в env
не помещается (ограничение размера, неудобство экранирования).

### Пользовательские переменные окружения и секреты (Phase 3)

Hook definition получает поле `env`. Ключ mapping — имя переменной в
hook-процессе, значение — ссылка на источник:

```yaml
hooks:
  - id: telegram
    events:
      - flow_finished
      - flow_failed
      - stage_failed
      - stage_question_asked
    command: "bash ./scripts/telegram-notify.sh"
    env:
      TELEGRAM_BOT_TOKEN: "env:TELEGRAM_BOT_TOKEN"
      TELEGRAM_CHAT_ID: "file:~/.afm/secrets/telegram-chat-id"
```

Два вида ссылки:

```text
env:NAME   — взять NAME из secrets.env, затем из окружения процесса AFM;
file:PATH  — прочитать значение из файла и обрезать пробелы/перевод строки.
```

Левая часть — **имя переменной в hook-процессе**, правая — **источник значения**.
Это та же семантика, что уже используется для `auth.from`. Резолвер выносится из
`pkg/docker/secrets.go` в нейтральный `pkg/secrets` и переиспользуется для agent
recipes и lifecycle-хуков.

Для `env:NAME` источники проверяются в порядке:

1. проектный `<afm-root>/.afm/secrets.env`;
2. глобальный `~/.afm/secrets.env`;
3. переменная окружения процесса AFM.

Более специфичный проектный слой имеет приоритет. Формат файлов — `KEY=VALUE`.

Имя и источник могут различаться (`TELEGRAM_BOT_TOKEN: "env:AFM_RELEASE_TELEGRAM_TOKEN"`) —
это позволяет разным хукам использовать разные credentials, не меняя скрипт.

Непрефиксованные значения в первой версии запрещены (источник обязан начинаться
с `env:` или `file:`). Отдельный `value:` можно добавить позднее.

Имя целевой переменной должно соответствовать `[A-Za-z_][A-Za-z0-9_]*`. Префикс
`AFM_` зарезервирован: пользовательский `env` не может переопределить
`AFM_HOOK_EVENT`, `AFM_RUN_ID`, transport-переменные и т. п.

Все ссылки разрешаются один раз при создании dispatcher, до `flow_started`.
Отсутствующая переменная/файл или пустое значение — ошибка конфигурации с
указанием `hook id` и имени целевой переменной.

Разрешённые значения:
- добавляются только в окружение конкретной hook-команды;
- не включаются в JSON payload;
- не пишутся в `hook_events.jsonl`/`hook_deliveries.jsonl`;
- не выводятся AFM в логах и сообщениях об ошибках;
- не попадают в аргументы `sh -c`/`docker run`;
- перекрывают одноимённую переменную базового окружения только для hook-процесса.

Runner заменяет точные значения известных секретов на `[REDACTED]` перед
сохранением stdout/stderr и публикацией в dashboard (защита от `set -x`/
`echo "$TOKEN"`, но не абсолютная — автор скрипта всё равно не должен печатать
секреты).

По умолчанию (Phase 3) hook-процесс не наследует окружение AFM целиком: runner
формирует минимальное окружение (`PATH`, `HOME`, locale, tmp, proxy/certs),
служебные `AFM_*` и явно объявленный `env`. Полное наследование включается явно:

```yaml
inherit_env: true
```

В outbox сохраняются лишь имена целевых переменных или id hook definition; при
повторной доставке секрет разрешается заново из актуального источника (позволяет
ротировать токен без переписывания журнала).

> Замечание про Phase 1: до Phase 3 поля `env`/`inherit_env` не поддерживаются;
> hook-процесс наследует окружение AFM, и токен отдаётся скрипту пользователем
> самостоятельно.

#### Поведение в Docker (Phase 3)

Ссылка `file:~/.afm/secrets/telegram-token` означает файл на хосте. В
Docker-режиме ссылки разрешаются хостовым launcher до `docker run` (как для agent
recipes). Значение передаётся в контейнер транзиентной bare-переменной
`-e NAME`; внутри контейнера dispatcher перекладывает его в целевую переменную
только при запуске нужного hook-процесса. Transport-переменные фильтруются из
окружения обычных агентов, script stages и других хуков.

### Пример hook-скрипта

```bash
#!/usr/bin/env bash
set -euo pipefail

payload="$(cat)"
event="$(jq -r '.event' <<<"$payload")"
stage="$(jq -r '.stage.id // "-"' <<<"$payload")"

printf 'AFM event=%s stage=%s\n' "$event" "$stage"
```

Команда статична и запускается через `sh -c` (как уже принято для скриптов AFM).
Значения события никогда не подставляются внутрь строки команды. Для Bash —
явно: `command: "bash ./scripts/afm-hook.sh"`.

Рабочая директория по умолчанию — эффективный `flow.root_dir` (тот же CWD, что у
агентов и `execScript`). Глобальный скрипт должен быть доступен через `PATH`
либо задан абсолютным путём.

## Выполнение и обработка ошибок

Lifecycle-хуки — наблюдатели. Типичная настройка:

```yaml
timeout: 30s
retries: 2
```

Если команда окончательно завершилась с ошибкой:

- статусы стадии и флоу не меняются;
- ошибка пишется в лог хука;
- в dashboard появляется предупреждение (UI-notice, не FSM);
- выполнение флоу продолжается.

`failure_policy: fail_stage` не добавляем: создаёт рекурсивную семантику и
пересекается по назначению со `script_before`/`script_after`/script stages.
Команды, которым нужно управлять стадией, используют эти существующие механизмы.

Stdout/stderr хука сохраняются в директории запуска:

```text
.afm/runs/<run-id>/hooks/<hook-id>.log
```

Вызовы одного hook definition выполняются последовательно (один потребитель видит
события в правильном порядке). Разные hook definitions выполняются параллельно.
При нормальном завершении флоу AFM ограниченное время ждёт опустошения очереди
dispatcher перед выходом (bounded flush, интегрируется с существующим
`WaitAgents()`/LIFO-shutdown; `flow_finished`/`flow_failed` эмитятся до flush).

## Гарантии доставки

Модель — **at least once**, но с явной оговоркой по источнику события.

### FSM-производные события (полная гарантия, Phase 2)

1. Каждое событие получает стабильный `event_id` с durable `seq`.
2. До запуска команды событие дописывается в `hook_events.jsonl` с `fsync`.
3. После успеха в `hook_deliveries.jsonl` пишется пара `event_id + hook_id`.
4. После рестарта недоставленные пары запускаются повторно.
5. Внешний получатель использует `AFM_HOOK_EVENT_ID` как ключ идемпотентности.

Lifecycle-событие создаётся только после durable-применения FSM-перехода.
Не применённый CAS-переход событие не порождает. Между сохранением перехода в
`events.jsonl` и добавлением envelope в `hook_events.jsonl` остаётся окно
падения — при старте dispatcher сверяет `events.jsonl` с outbox по `seq` и
восстанавливает отсутствующие lifecycle-события. `events.jsonl` — источник
истины; outbox хранит состояние доставки, а не состояние флоу.

### Не-FSM события (best-effort — сознательное ограничение)

`flow_*`, `stage_script_*` и авто-ответы **не имеют** durable seq-источника, по
которому их можно было бы реконструировать после краха. Они пишутся в outbox на
фактической границе операции (`*_started` — перед операцией, `*_finished`/
`*_failed` — после определения результата). Если AFM упадёт в окне между
операцией и записью envelope — такое событие теряемо и не восстанавливается.

Это документированное ограничение: **at-least-once держится только для
FSM-производных событий**; для остальных доставка best-effort. Инвестиция в
отдельный durable-источник для не-FSM событий сознательно не делается (нет
источника истины для реконсиляции; усложнение не оправдано).

> В Phase 1 outbox отсутствует вовсе — доставка всех событий live best-effort.
> Durable-часть (пп. 1–5 выше и реконсиляция) появляется в Phase 2 и относится
> только к FSM-производным событиям.

## План реализации (структура)

Пакет `pkg/lifecyclehooks`:

```text
pkg/lifecyclehooks/
  types.go       LifecycleHook, LifecycleEvent, Payload, EventSelector
  validate.go    проверка событий, id и конфигурации
  matcher.go     all, skip_events и stage scope
  dispatcher.go  очереди, retries, timeout и shutdown
  runner.go      запуск команды, stdin/env и логи
  outbox.go      durable-события и журнал доставки (Phase 2)
```

Структура конфигурации:

```go
type LifecycleHook struct {
    ID         string
    Events     EventSelector
    SkipEvents []EventType
    Command    string
    Env        map[string]SecretRef // Phase 3
    InheritEnv bool                 // Phase 3
    Timeout    time.Duration
    Retries    int
}
```

Итоговая конфигурация передаётся в orchestrator с метаданными флоу:

```go
type Options struct {
    // Существующие поля...
    FlowName string
    RunID    string
    Resumed  bool
    Hooks    *lifecyclehooks.LifecycleDispatcher
}
```

Точки интеграции:

- `config.Config.Hooks` — глобальные и проектные хуки; merge в `mergeFile`
  реализуется keyed-merge по `id` (project затирает global по id);
- `flow.Flow.Hooks` — хуки флоу; `flow.Stage.Hooks` — хуки стадии;
- сборка dispatcher комбинирует слои с дедупом по id и precedence
  `global < project < flow < stage`;
- `triggerWithSeq` (`pkg/orchestrator/orchestrator.go`) преобразует успешно
  применённые FSM-переходы в публичные lifecycle-события;
- `runScriptStage` (`agents.go`), `runBeforeHook`/`runAfterHook` (`hooks.go`)
  создают события script stage и script-before/after на своих границах;
- dialog-поллер создаёт события вопросов после durable-принятия вопроса/ответа
  (включая авто-ответы non-interactive стадий);
- `Orchestrator.Run` создаёт `flow_*` события и выполняет bounded flush
  dispatcher перед нормальным выходом.

Lifecycle dispatcher **не** подписывается на `UIBus`: он lossy (дропает событие
при переполнении буфера подписчика), best-effort и содержит
presentation-oriented/высокочастотные сообщения. Хукам нужен собственный прямой
dispatcher (и durable outbox в Phase 2).

## Рекомендуемый объём Phase 1

- observer-only lifecycle-хуки;
- global/project/flow/stage scopes с дедупом по id;
- детерминированный порядок и наследование слоёв;
- `events: all` и `skip_events`;
- публичный каталог событий (закодирован один раз);
- JSON в stdin и скалярные `AFM_*` env;
- timeout и ограниченные retries;
- стабильные `event_id`;
- логи и предупреждения dashboard, не меняющие FSM;
- live best-effort доставка (без outbox);
- bounded flush на нормальном выходе.

Отложено: durable outbox и at-least-once (Phase 2); пользовательский `env`/
секреты, вынос `pkg/secrets`, Docker-транспорт, `[REDACTED]`, `inherit_env`
(Phase 3); блокирующие/меняющие состояние failure policies, wildcard-паттерны
событий и дополнительные фильтры стадий (по мере появления реальных сценариев).
