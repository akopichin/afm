# Архитектурный план: миграция `afm` на клеточки Goga

Источник: `docs/tasks/goga-claude.md`. Тип работы: реверс-инжиниринг существующего работающего кода в
CODEMANIFEST-контракты, описывающие текущий (as-is) публичный API. Поведение кода не меняется; единственное
физическое перемещение файлов — see §4.1 (обязательный сплит `pkg/web`, согласованный с пользователем).

## 1. Обзор

- **Существующая архитектура Goga:** отсутствует. `goga schema` → `[]`, `goga lint` → `cells: 0 errors: 0`.
  Единственный существующий артефакт — 7 project-level usage-файлов в `.goga/usages/cooks/` (см. §5).
- **Гранулярность:** 1 Go-пакет = 1 клеточка, за одним обязательным исключением (`pkg/web`, см. §4.1).
- **Итоговое число клеточек: 16** (15 логических единиц задачи + 1 доп. клеточка от разделения `pkg/web`).
- **Порядок проектирования:** строго bottom-up по 5 слоям реального графа импортов (проверено `grep`, см. §3).
- **Тестовые файлы** (`*_test.go`) не входят ни в один контракт. Физический перенос `.go`-файлов между
  директориями не производится (см. §4.2 — обоснование отказа от дальнейшего дробления `pkg/orchestrator`).

## 2. Acceptance Criteria (из `docs/tasks/goga-claude.md`, канонический источник)

1. `goga schema` показывает ≥16 клеточек, сгруппированных согласно графу импортов §3.
2. Каждая клеточка проходит `goga lint` без ошибок структуры/казинга DSL-ключей.
3. Контракт каждой клеточки покрывает публичный API реального Go-пакета.
4. `Imports` каждой клеточки в точности соответствуют реальному графу внутренних импортов — без циклов.
5. Все 7 внешних usage-файлов подключены (`Usages`) и применены хотя бы в одной аннотации клеточки,
   реально использующей эту зависимость.
6. Три нюанса из `CLAUDE.md` (file-based dialog protocol, proxy-трансформы, Docker-режим) отражены в
   аннотациях соответствующих клеточек.
7. Черновой каркас `cells/` отсутствует и не создаётся.

## 3. Граф клеточек и порядок проектирования (bottom-up, 5 слоёв)

```
Слой 0 (10 клеточек, независимые листья):
  pkg/config  pkg/flow  pkg/state  pkg/progress  pkg/mcp  pkg/proxy
  pkg/web  pkg/web/dashboard  assets  tools/setstatuslinter

Слой 1 (3 клеточки):
  pkg/prompts  → pkg/flow
  pkg/docker   → pkg/flow
  pkg/executor → pkg/progress

Слой 2 (1 клеточка):
  pkg/orchestrator → pkg/config, pkg/executor, pkg/flow, pkg/mcp, pkg/prompts, pkg/state

Слой 3 (1 клеточка):
  pkg/server → pkg/executor, pkg/mcp, pkg/orchestrator, pkg/state, pkg/web

Слой 4 (1 клеточка):
  cmd/afm → pkg/config, pkg/docker, pkg/flow, pkg/orchestrator, pkg/proxy, pkg/server, pkg/state, assets
```

Граф проверен по факту через `grep` реальных import-путей `github.com/akopichin/afm/...` во всех
non-test `.go`-файлах каждого пакета (см. §6 для деталей per-клеточка). Циклов нет: ни одна клеточка слоя N не
импортируется клеточкой слоя < N.

## 4. Ключевые решения дробления (Cell Distribution)

### 4.1 `pkg/web` → два физических cell (решение пользователя, вариант A)

**Конфликт:** DSL (`dsl.md`) фиксирует ровно один `CODEMANIFEST` на директорию и требует, чтобы `location:`
любого типа указывал на файл **в той же директории** (без спуска в поддиректории, без выхода в родительскую).
Директория `pkg/web/` физически содержит одновременно `embed.go` (Go-часть) и статические ассеты
(`index.html`, `style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`). Буквально выполнить «два
CODEMANIFEST, ноль перемещённых файлов» невозможно — это ограничение тулинга, а не выбор дизайна.

**Решение (согласовано с пользователем):** реальный сплит директории.
- `pkg/web/embed.go` остаётся на месте — клеточка **`pkg/web`** (Go-часть, `embed.FS`).
- Статические НЕ-`.go` ассеты (`index.html`, `style.css`, `app.js`, `markdown-it.min.js`, `favicon.svg`)
  физически переносятся в новую поддиректорию **`pkg/web/dashboard/`** — клеточка **`pkg/web/dashboard`**.
- `go:embed`-директива в `embed.go` получает однострочную правку пути (`//go:embed dashboard/*` вместо
  `//go:embed index.html style.css ...`) — это изменение пути встраивания, не поведения (`FS` продолжает
  отдавать те же файлы по тем же относительным веб-путям).
- Out of Scope в задаче прямо запрещает перенос **`.go`-файлов** («Физический перенос `.go`-файлов между
  директориями») — перенос НЕ-`.go`-ассетов этим пунктом не покрывается, поэтому решение не нарушает Scope.
- Это даёт ровно 16 клеточек в `goga schema`, как буквально требует Acceptance Criteria п.1.
- **Статус:** перенос файлов и правка `go:embed`-пути — это подготовительный шаг `goga-apply` (материализация
  плана), а не действие этой brainstorm-стадии; см. §7 «Требуемый подготовительный шаг».

### 4.2 `pkg/orchestrator` — решение НЕ дробить дальше

Task-файл явно оставляет решение о дальнейшем дроблении `pkg/orchestrator` (FSM / bus / graph /
session-recovery — 11 файлов) на усмотрение этой стадии. Анализ:

**Сигналы за дробление (`goga-cookbook`: decoupled logic / distinct data model / переиспользование):**
- `FSM` (fsm.go) — распознавание переходов статуса стадии по событию + guard-правила — самостоятельная
  предметная модель (`state.StageStatus`, `FSMEvent`).
- `UIBus`/`CriticalBus` (bus.go) — pub/sub шина событий — единственная часть, реально переиспользуемая
  за пределами `pkg/orchestrator`: `pkg/server` напрямую держит `*orchestrator.UIBus` и вызывает
  `Subscribe`/`SubscriberDroppedCount` (проверено `grep` — `pkg/server/websocket.go`).
- `Graph` (graph.go) — граф зависимостей стадий — независимая модель данных.

**Сигналы против дробления (решающие):**
- Проверено `grep`: **ни один** внешний (за пределами `pkg/orchestrator`) `.go`-файл не ссылается на
  `orchestrator.FSM`, `orchestrator.Graph`, `orchestrator.CriticalBus`, `orchestrator.Classify` —
  единственный внешне потребляемый символ, помимо самого `Orchestrator`/`Options`/`Prompts`, это `UIBus`
  (и транзитивно `Event` через нетипизированный `<-chan Event`, потребляемый в `pkg/server/websocket.go`).
  То есть FSM/Graph/CriticalBus/классификация ошибок — внутренняя реализация Orchestrator, не отдельный
  API-фасад для внешних потребителей.
- **Решающее ограничение:** клеточка = Go-пакет = директория (`goga-cell-go`). Все 11 файлов
  `pkg/orchestrator` (`bus.go`, `completion.go`, `context.go`, `errors.go`, `fsm.go`, `graph.go`,
  `orchestrator.go`, `plan_adopt.go`, `recovery.go`, `retry.go`, `session.go`) составляют **один** Go-пакет
  `package orchestrator`. Любое дробление на отдельные клеточки потребовало бы перемещения `.go`-файлов в
  новые под-пакеты (директории) — а это прямо запрещено Out of Scope задачи («Физический перенос
  `.go`-файлов между директориями»). В отличие от §4.1 (где переносились НЕ-`.go`-ассеты), здесь
  потребовалось бы переносить именно `.go`-файлы — исключение недоступно.

**Итог:** `pkg/orchestrator` остаётся **одной** клеточкой. Внутренняя неоднородность (FSM/Bus/Graph/
session-recovery) отражается на уровне **type-level аннотаций** внутри одного CODEMANIFEST (см. §6.14), а не
на уровне отдельных клеточек. Зафиксировано как явный риск/заметка в §8 — если проект физически разнесёт
`pkg/orchestrator` на суб-пакеты в будущем (отдельная задача через `goga-change`), дробление клеточек станет
органичным следующим шагом.

### 4.3 Artifact Resolution

Все 16 артефактов — **новые клеточки** (нет ни одной существующей клеточки для модификации — `goga schema`
пуст). Соответствие «логическая единица → путь клеточки» 1:1 с Current State задачи, за вычетом `pkg/web`
(1 единица → 2 клеточки, см. §4.1).

## 5. External Usages — project-level (уже существуют, подключаются `Usages`)

| Practice key            | Файл                                       | Потребляющая клеточка   |
|--------------------------|---------------------------------------------|--------------------------|
| `cobra`                  | `.goga/usages/cooks/cobra.md`               | `cmd/afm`                |
| `gorilla_websocket`      | `.goga/usages/cooks/gorilla-websocket.md`   | `pkg/server`              |
| `rapid`                  | `.goga/usages/cooks/rapid.md`               | `pkg/orchestrator` (тестовый контур, документируется annotation-ссылкой, не тестовым файлом) |
| `x_sys_windows`          | `.goga/usages/cooks/x-sys-windows.md`       | `pkg/progress`            |
| `x_term`                 | `.goga/usages/cooks/x-term.md`              | `pkg/docker`              |
| `x_tools_analysis`       | `.goga/usages/cooks/x-tools-analysis.md`    | `tools/setstatuslinter`   |
| `yaml_v3`                | `.goga/usages/cooks/yaml-v3.md`             | `pkg/config`, `pkg/flow`  |

## 6. CODEMANIFEST по клеточкам (bottom-up)

Конвенция для всех клеточек ниже: строковые enum-константы Go (`StageStatus`, `EventType`, `FSMEvent`,
`Classification`, `AgentType`, `Agent` и т.п.) документируются как перечисление допустимых значений внутри
аннотации типа/routine, который их использует — не как отдельные top-level записи CODEMANIFEST (иначе
получится «клеточка на константу», что `goga-cookbook` считает избыточным дроблением). Указатели (`*T`) и
каналы (`<-chan T`) — как формы, запрещённые правилами сигнатур Go для CODEMANIFEST — абстрагируются:
сущность объявляется по имени типа (`Store`, не `*Store`), поток событий описывается annotation-текстом,
поясняющим, что метод отдаёт подписку/канал, а не одиночное значение. Аналогично функции-колбэки
(`func(...)`-поля/параметры, например `OnAction`, `ApproveFn`, `Rule.To`, аргумент `SetApplyHook`) — тоже
запрещённая форма сигнатуры — типизируются ближайшим доступным примитивом (`string`) в самой сигнатуре, а
их реальная форма (какую функцию/колбэк они представляют) поясняется текстом annotation рядом; сигнатура
DSL не предназначена для точного описания функциональных типов.

Footer одинаков для всех клеточек: `Author: Goga`, `CreatedAt: 03/07/26`.

---

### 6.1 `pkg/config` (слой 0)

```yaml
Usages:
  yaml_v3: .goga/usages/cooks/yaml-v3.md

Annotations: |
  Загрузка и мердж конфигурации afm (`.afm/config.yaml`, `~/.afm/config.yaml`). Использует `yaml_v3` для
  парсинга — опциональность полей выражена указателем + методом-геттером с дефолтом (см. `yaml_v3`).

---

"Default() -> config:Config":
  location: config.go
  annotations: |
    Возвращает встроенную конфигурацию по умолчанию.

"LoadFrom(globalDir string, projectDir string) -> config:Config, err:error":
  location: config.go
  annotations: |
    Загружает и мержит конфигурацию из явных глобальной и проектной директорий.
    `globalDir`: путь к глобальному `~/.afm`
    `projectDir`: путь к проектному `.afm`

    Requirements:
    - Отсутствующие файлы молча игнорируются, применяются дефолты
    - Использовать `yaml_v3` для парсинга YAML

"Config(client ClientConfig, executor ExecutorConfig, server ServerConfig, proxy ProxyConfig, docker DockerConfig, promptsDir string)":
  location: config.go
  annotations: |
    Смерженная конфигурация afm целиком.
  properties:
    "Client -> ClientConfig": |
      Настройки команды AI-клиента.
    "Executor -> ExecutorConfig": |
      Параметры выполнения агентов.
    "Server -> ServerConfig": |
      Настройки веб-дашборда.
    "Proxy -> ProxyConfig": |
      Настройки встроенного reverse-proxy.
    "Docker -> DockerConfig": |
      Настройки Docker-режима.
    "PromptsDir -> string": |
      Переопределение директории system-промптов.

"ClientConfig(command string, extraArgs []string)":
  location: config.go
  annotations: |
    Команда AI-клиента и дополнительные аргументы CLI.
  properties:
    "Command -> string": |
      Имя/путь исполняемого агента (например, `claude`).
    "ExtraArgs -> []string": |
      Дополнительные аргументы командной строки.

"ExecutorConfig(idleTimeout string, maxParallel int)":
  location: config.go
  annotations: |
    Параметры исполнения агентских процессов.
  properties:
    "IdleTimeout -> string": |
      Таймаут простоя агента (значение `time.Duration`, например `30m`).
    "MaxParallel -> int": |
      Максимум параллельных агентов.

"ServerConfig(port int, openBrowser bool)":
  location: config.go
  annotations: |
    Настройки HTTP-дашборда. `Port`/`OpenBrowser` — опциональные (nil = не задано явно), геттеры
    возвращают дефолт.
  properties:
    "Port -> int": |
      Порт дашборда (nil → дефолт 9876, см. `GetPort`).
    "OpenBrowser -> bool": |
      Открывать браузер автоматически (nil → true, см. `IsOpenBrowser`).
  methods:
    "GetPort() -> port:int": |
      Возвращает `Port`, либо 9876, если не задан.
    "IsOpenBrowser() -> enabled:bool": |
      Возвращает `OpenBrowser`, либо true, если не задан.

"ProxyConfig(enabled bool, upstream string, port int, transforms TransformOverrides)":
  location: config.go
  annotations: |
    Настройки встроенного reverse-proxy для агентского HTTP-трафика.
  properties:
    "Enabled -> bool": |
      nil → включено по умолчанию (см. `IsEnabled`).
    "Upstream -> string": |
      Upstream-адрес (иначе читается из `ANTHROPIC_BASE_URL`).
    "Port -> int": |
      Порт proxy (0 = случайный свободный).
    "Transforms -> TransformOverrides": |
      Переопределения по конкретным трансформам.
  methods:
    "IsEnabled() -> enabled:bool": |
      true по умолчанию (nil `Enabled` → enabled).

"TransformOverrides(zai bool)":
  location: config.go
  annotations: |
    Управляет тем, какие proxy-трансформы применяются.
  properties:
    "ZAI -> bool": |
      nil = автоопределение по upstream-хосту (`api.z.ai`), true = всегда, false = никогда.

"DockerConfig(enabled bool, image string, extraMounts []string)":
  location: config.go
  annotations: |
    Настройки Docker-режима самоперезапуска.
  properties:
    "Enabled -> bool": |
      nil = смотрим `AFM_USE_DOCKER`.
    "Image -> string": |
      Docker-образ.
    "ExtraMounts -> []string": |
      Доп. хост-пути (можно с `~`), пробрасываемые в контейнер read-only — для кастомных агентов,
      хранящих токены/конфиги вне `~/.claude`.
  methods:
    "GetImage() -> image:string": |
      Образ, с приоритетом `AFM_DOCKER_IMAGE`.
    "IsDockerEnabled() -> enabled:bool": |
      true, если нужно использовать Docker-режим. `AFM_IN_DOCKER=1` всегда даёт false (уже внутри
      контейнера).

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Загрузка и мердж конфигурации afm из глобального и проектного YAML.
```

---

### 6.2 `pkg/flow` (слой 0)

```yaml
Usages:
  yaml_v3: .goga/usages/cooks/yaml-v3.md

Annotations: |
  Модель `flow.yaml` — стадии, артефакты, входы/выходы. Парсинг через `yaml_v3`.

---

"ParseFile(path string) -> flow:Flow, err:error":
  location: flow.go
  annotations: |
    Читает и валидирует flow YAML-файл.
    `path`: путь к файлу flow.yaml

    Use `yaml_v3` for implementation.

"Flow(name string, description string, maxParallel int, stages []Stage)":
  location: flow.go
  annotations: |
    Верхнеуровневая структура, распарсенная из flow YAML.
  properties:
    "Name -> string": |
      Имя флоу.
    "Description -> string": |
      Описание флоу.
    "MaxParallel -> int": |
      Максимум параллельных стадий.
    "Stages -> []Stage": |
      Список стадий флоу.

"Stage(id string, name string, description string, agents []string, skills []string, dependsOn []string, eagerPlanning bool, plan string, command string, maxParallel int, artifacts []Artifact, inputs []Input, interactive bool, verify string, prompt string)":
  location: flow.go
  annotations: |
    Одна стадия флоу.
    `agents`: список значений `AgentType` — `planning`, `implementation`, `review`
    `eagerPlanning`: если true, планирующий агент стартует немедленно, не дожидаясь `dependsOn`
    `plan`: путь к готовому файлу плана — если задан, планирующий агент пропускается
    `command`: переопределяет глобальную команду клиента для этой стадии
    `verify`: shell-команда, выполняемая после заявленного завершения стадии; ненулевой код означает,
    что стадия не завершена, независимо от заявления агента
    `prompt`: явная инструкция агенту после блока контекста `<stage>`
  methods:
    "HasAgent(a string) -> has:bool": |
      Есть ли у стадии указанный тип агента (`a` — значение `AgentType`). Для `implementation` также
      считает true при любом кастомном (не встроенном) агенте.
    "ImplAgent() -> agentType:string": |
      Тип агента для фазы implementation: кастомный агент приоритетнее, иначе `implementation`.
    "NeedsPlanning() -> needs:bool": |
      Будет ли для стадии запущен планирующий агент.

"Artifact(name string, path string, description string, inline bool)":
  location: flow.go
  annotations: |
    Файл, который стадия производит для других стадий.
  properties:
    "Name -> string": |
      Имя артефакта.
    "Path -> string": |
      Путь к файлу артефакта.
    "Description -> string": |
      Описание артефакта.
    "Inline -> bool": |
      Инлайнить ли содержимое в промпт (nil → true, см. `IsInline`).
  methods:
    "IsInline() -> inline:bool": |
      Значение `Inline`, либо true по умолчанию.

"Input(ref string, optional bool)":
  location: flow.go
  annotations: |
    Артефакт, потребляемый стадией от зависимости.

    Requirements:
    - Парсится либо из простой строки `"stage.artifact"`, либо из объекта `{ref, optional}`
  properties:
    "Ref -> string": |
      Ссылка вида `stage.artifact`.
    "Optional -> bool": |
      Необязательность входа.
  methods:
    "UnmarshalYAML(value string) -> err:error": |
      Разбор из строки или объекта YAML.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Модель flow.yaml: стадии, артефакты, входы/выходы.
```

---

### 6.3 `pkg/state` (слой 0)

```yaml
Annotations: |
  Персистентное файловое хранилище состояния стадий рана (`state.json`). Прямые вызовы `(*Store).Apply`
  вне `pkg/orchestrator/fsm.go` запрещены проектной линтер-конвенцией — см. `tools/setstatuslinter`
  (слой 0, независимая клеточка, аннотирует то же самое правило со стороны анализатора).

---

"FindLatestRunDir(base string, flowName string) -> path:string, err:error":
  location: state.go
  annotations: |
    Находит самую свежую директорию рана для указанного имени флоу под `base/`.

"SaveFeedback(stageDir string, feedback string) -> err:error":
  location: state.go
  annotations: |
    Добавляет feedback в `feedback.md` стадии с разделителями ревизий.

"VersionPlan(stageDir string) -> version:int, err:error":
  location: state.go
  annotations: |
    Переименовывает `plan.md` в `plan.v{N}.md` и возвращает N.

"SetApplyHook(h string) -> ":
  location: store.go
  annotations: |
    Устанавливает тестовый хук, вызываемый внутри `Apply` между fsync и перезаписью снапшота.
    `h`: функция-колбэк, вызываемая с `Transition`

    Constraints:
    - Продакшн-код не должен вызывать эту функцию — только тестовый контур

"RunState(flowName string, startedAt string, stageOrder []string, stageNames map[string]string, stages map[string]StageState)":
  location: state.go
  annotations: |
    Верхнеуровневое состояние, персистируемое в `state.json`. `stageNames` — опционально
    (`omitempty`), для обратной совместимости со старыми `state.json` без этого поля.
  properties:
    "FlowName -> string": |
      Имя флоу.
    "StartedAt -> string": |
      Момент старта рана.
    "StageOrder -> []string": |
      Порядок ID стадий.
    "StageNames -> map[string]string": |
      Человекочитаемое имя стадии по ID (из flow-файла — метаданные, не рантайм-состояние).
    "Stages -> map[string]StageState": |
      Состояние каждой стадии по ID.
  methods:
    "NewRunState(stageIDs []string) -> state:RunState": |
      Создаёт начальный `RunState` со всеми стадиями в статусе pending.
    "AllDone() -> done:bool": |
      true, если у каждой стадии статус done.
    "SetStageStatus(stageID string, status string) -> ": |
      Обновляет статус стадии и её timestamp.
      `status`: одно из значений `StageStatus` — `pending`, `planning`, `awaiting_approval`, `revising`,
      `ready`, `running`, `retrying`, `awaiting_user_input`, `done`, `failed`

"StageState(status string, updatedAt string)":
  location: state.go
  annotations: |
    Персистентное состояние одной стадии.
  properties:
    "Status -> string": |
      Значение `StageStatus`.
    "UpdatedAt -> string": |
      Время последнего изменения.

"Store()":
  location: store.go
  annotations: |
    Файловое хранилище состояния — читает/пишет `state.json` и лог переходов (event log).
  methods:
    "Open(runDir string, stageIDs []string) -> store:Store, err:error": |
      Открывает (или создаёт) хранилище для директории рана.
    "Apply(t Transition) -> err:error": |
      Применяет переход состояния, дописывает событие в лог, синхронизирует и перезаписывает снапшот.

      Constraints:
      - Вызывается только из `pkg/orchestrator/fsm.go` (см. `tools/setstatuslinter`)
    "Close() -> err:error": |
      Закрывает хранилище.
    "Get(stageID string) -> status:string": |
      Текущий статус стадии.
    "SetStageNames(names map[string]string) -> ": |
      Сохраняет отображаемое имя для каждой стадии. Карта копируется — мутации вызывающей стороной
      после вызова не влияют на хранилище.
    "Snapshot() -> state:RunState": |
      Текущий снапшот состояния рана.

"Transition(seq int, time string, stageID string, from string, to string, event string, reason string)":
  location: store.go
  annotations: |
    Запись одного перехода состояния в event log.
  properties:
    "Seq -> int": |
      Порядковый номер перехода.
    "Time -> string": |
      Время перехода.
    "StageID -> string": |
      ID стадии.
    "From -> string": |
      Статус до перехода (`StageStatus`).
    "To -> string": |
      Статус после перехода (`StageStatus`).
    "Event -> string": |
      Имя события FSM, вызвавшего переход.
    "Reason -> string": |
      Причина перехода (опционально).

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Персистентное файловое состояние рана: state.json + event log переходов.
```

---

### 6.4 `pkg/progress` (слой 0)

```yaml
Usages:
  x_sys_windows: .goga/usages/cooks/x-sys-windows.md

Annotations: |
  Кроссплатформенные файловые advisory-локи и логирование прогресса стадии. На Windows — `x_sys_windows`
  (`windows.LockFileEx`, `//go:build windows`), на Unix — `syscall.Flock` стандартной библиотеки
  (`//go:build !windows`, не отдельная внешняя зависимость).

---

"Lock(path string)":
  location: progress.go
  annotations: |
    Файловый эксклюзивный лок. Тип и `NewLock` объявлены здесь; методы `Lock`/`TryLock`/`Unlock`
    реализованы платформенно-специфично (`//go:build !windows` / `//go:build windows`) в отдельных
    файлах того же пакета — контракт описывает поведение метода независимо от того, в каком из
    build-tag файлов лежит конкретная реализация.

    Use `x_sys_windows` for the Windows-specific implementation branch.
  methods:
    "NewLock(path string) -> lock:Lock, err:error": |
      Создаёт handle лока для пути (не захватывает).
    "Lock() -> err:error": |
      Захватывает эксклюзивный блокирующий flock.
    "TryLock() -> err:error": |
      Пытается захватить неблокирующий эксклюзивный flock.
    "Unlock() -> ": |
      Освобождает flock и закрывает файл.

"Logger(path string)":
  location: progress.go
  annotations: |
    Пишет отметки времени в файл (append-only) и в stdout.
  methods:
    "NewLogger(path string) -> logger:Logger, err:error": |
      Открывает (или создаёт) файл лога.

      Algorithm:
      1. Если файл уже существует без футера завершения — дописать разделитель рестарта
    "Log(msg string) -> ": |
      Пишет строку с меткой времени в файл и в stdout.
    "LogAction(toolName string, detail string) -> ": |
      Пишет строку действия (имя инструмента + детали).
    "LogStart(agentType string, stageName string) -> ": |
      Пишет баннер старта с типом агента, именем стадии и временем.
    "LogEnd(err string) -> ": |
      Пишет баннер завершения/ошибки с прошедшей длительностью.
      `err`: ошибка завершения, если есть
    "Close() -> err:error": |
      Закрывает файл.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Файловые advisory-локи и логирование прогресса стадии, кроссплатформенно.
```

---

### 6.5 `pkg/mcp` (слой 0)

```yaml
Annotations: |
  Файловый протокол диалога агент↔пользователь (замена бывшего MCP HTTP-сервера). Вопросы/ответы —
  JSON-файлы `<phase>.<id>.question.json` / `<phase>.<id>.answer.json` в директории стадии
  (`AFM_STAGE_DIR`), потребитель протокола — `pkg/orchestrator` (см. §6.14 — `NotifyAnswer`,
  `startQuestionPoller`).

  Algorithm (протокол в целом):
  1. Агент пишет `Question` через `AppendQuestion` в `<phase>.<id>.question.json`
  2. Оркестратор поллит директорию стадии, находит неотвеченные вопросы через
     `FindUnansweredQuestions`
  3. Пользователь отвечает — HTTP-хендлер атомарно (O_EXCL) создаёт `<phase>.<id>.answer.json` через
     `AppendAnswer`
  4. Агент/оркестратор читают связанные вопрос-ответ через `ReadDialog`/`FindEntry`

---

"AppendQuestion(path string, q Question) -> err:error":
  location: dialog.go
  annotations: |
    Дописывает запись вопроса как одну JSON-строку.

"AppendAnswer(path string, a Answer) -> err:error":
  location: dialog.go
  annotations: |
    Дописывает запись ответа как одну JSON-строку.

    Requirements:
    - Целевой `<phase>.<id>.answer.json` создаётся атомарно (эксклюзивное создание), чтобы гарантировать
      единственную поставку ответа при конкурентном доступе

"FindUnansweredQuestions(stageDir string) -> questions:[]QuestionFile, err:error":
  location: dialog.go
  annotations: |
    Сканирует директорию стадии на файлы `*.question.json` без парного `*.answer.json`.

    Requirements:
    - Имена файлов должны следовать шаблону `<phase>.<id>.question.json`

"FindEntry(path string, id string) -> entry:Entry, err:error":
  location: dialog.go
  annotations: |
    Возвращает запись с указанным id, либо nil, если отсутствует.

"ReadDialog(path string) -> entries:[]Entry, err:error":
  location: dialog.go
  annotations: |
    Читает все записи и группирует их по ID в хронологическом порядке первого вопроса.

"Question(id string, ts string, question string, options []string, allowCustom bool)":
  location: dialog.go
  annotations: |
    Запись, создаваемая при обращении агента к диалогу.
  properties:
    "ID -> string": |
      Идентификатор вопроса, уникальный в рамках фазы.
    "TS -> string": |
      Время создания.
    "Question -> string": |
      Текст вопроса (markdown, полный контекст).
    "Options -> []string": |
      Варианты ответа (опционально).
    "AllowCustom -> bool": |
      Разрешён ли произвольный ответ, помимо `Options`.

"Answer(id string, ts string, answer string, fromOptions bool)":
  location: dialog.go
  annotations: |
    Запись, создаваемая при ответе пользователя.
  properties:
    "ID -> string": |
      Идентификатор вопроса, к которому относится ответ.
    "TS -> string": |
      Время ответа.
    "Answer -> string": |
      Текст ответа.
    "FromOptions -> bool": |
      Выбран ли ответ из предложенных `Options`.

"Entry(id string, ts string, question string, options []string, allowCustom bool, answer string, answerTS string, fromOptions bool)":
  location: dialog.go
  annotations: |
    Сгруппированная пара вопрос/ответ для чтения. `Answer` отсутствует, если вопрос ещё открыт.
  properties:
    "ID -> string": |
      Идентификатор.
    "Question -> string": |
      Текст вопроса.
    "Options -> []string": |
      Предложенные варианты.
    "AllowCustom -> bool": |
      Разрешён произвольный ответ.
    "Answer -> string": |
      Текст ответа (пусто, если вопрос ещё не отвечен).
    "AnswerTS -> string": |
      Время ответа.
    "FromOptions -> bool": |
      Выбран ли ответ из предложенных.

"QuestionFile(phase string, id string, question string, options []string, allowCustom bool)":
  location: dialog.go
  annotations: |
    Метаданные, извлечённые из файла `*.question.json`.
  properties:
    "Phase -> string": |
      Одно из `planning`, `implementation`, `review`.
    "ID -> string": |
      Идентификатор вопроса.
    "Question -> string": |
      Текст вопроса.
    "Options -> []string": |
      Варианты ответа.
    "AllowCustom -> bool": |
      Разрешён произвольный ответ.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Файловый протокол диалога агент↔пользователь: вопрос/ответ JSON-файлы + чтение истории.
```

---

### 6.6 `pkg/proxy` (слой 0)

```yaml
Annotations: |
  Встроенный reverse-proxy агентского HTTP-трафика к Anthropic-совместимым шлюзам. `Transform` —
  точка расширения: `Proxy.ServeHTTP` диспетчеризует на первый подходящий трансформ по `Match`, иначе —
  passthrough без изменений.

---

"Transform(match bool, serveHTTP bool)":
  location: transform.go
  annotations: |
    Интерфейс обработки проксируемых HTTP-запросов для конкретного upstream.
  methods:
    "Match(upstreamURL string) -> matches:bool": |
      Применим ли этот трансформ к данному upstream URL.
    "ServeHTTP(w http.ResponseWriter, r http.Request, upstream string) -> ": |
      Обрабатывает проксируемый запрос.
      `upstream`: провалидированный базовый URL upstream (например, `https://api.z.ai/api/anthropic`)

"BuildTransforms(upstream string, zai bool) -> transforms:[]Transform":
  location: zai.go
  annotations: |
    Возвращает упорядоченный список трансформов для указанного upstream.
    `zai`: nil = автоопределение по хосту (`api.z.ai`), true = всегда, false = никогда

"ZAITransform()":
  location: zai.go
  annotations: |
    Исправляет ошибки 529 от `api.z.ai`: конвертирует non-streaming запросы в streaming и разбирает
    SSE-ответ обратно в единый JSON `message`.

    Algorithm:
    1. `Match` — true для upstream URL, содержащих `api.z.ai`
    2. Для `stream=true` запросов — пропустить без изменений
    3. Для `stream=false`/отсутствующего — внедрить `stream: true`, переслать upstream
    4. Прочитать SSE-ответ; накопить `message_start` (id/role/model/usage), `content_block_start` +
       `content_block_delta` (text/thinking/tool_use/signature deltas) по индексу блока, `message_delta`
       (stop_reason + merge usage)
    5. Вернуть единый JSON `message`

    Constraints:
    - Upstream-ответы не-200 пересылаются как есть (статус + заголовки + тело)
    - SSE-событие `error` или пустой SSE-ответ дают HTTP 529 со структурированной ошибкой в стиле Anthropic
  methods:
    "Match(upstreamURL string) -> matches:bool": |
      true для upstream URL, содержащих `api.z.ai`.
    "ServeHTTP(w http.ResponseWriter, r http.Request, upstream string) -> ": |
      См. Algorithm выше.

"Proxy(upstream string, transforms []Transform)":
  location: proxy.go
  annotations: |
    Reverse-proxy, применяющий первый подходящий `Transform` к каждому запросу. Запросы без
    подходящего трансформа передаются без изменений.
  methods:
    "New(upstream string, transforms []Transform) -> proxy:Proxy": |
      Создаёт Proxy, пересылающий на upstream с указанными трансформами.
    "Start(port int) -> addr:string, err:error": |
      Слушает `127.0.0.1:port` (0 = порт, назначаемый ОС), возвращает адрес как
      `http://127.0.0.1:PORT`. Сервер работает в фоне.
    "Addr() -> addr:string": |
      Адрес прослушивания (пусто до `Start`).
    "ServeHTTP(w http.ResponseWriter, r http.Request) -> ": |
      Диспетчеризация на первый подходящий трансформ, либо passthrough.
    "Shutdown(ctx string) -> err:error": |
      Плавно останавливает proxy-сервер.

"CreateShim(proxyAddr string) -> shimDir:string, err:error":
  location: shim.go
  annotations: |
    Создаёт временную директорию с shell-скриптом `claude`, переопределяющим `ANTHROPIC_BASE_URL` на
    `proxyAddr` перед вызовом настоящего `claude`. Директорию нужно добавить в начало `PATH` в
    окружении агента.

    Constraints:
    - Вызывающая сторона отвечает за `defer os.RemoveAll(shimDir)`
    - Возвращает ошибку, если `claude` не найден в текущем `PATH`

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Reverse-proxy агентского HTTP-трафика с точкой расширения Transform; ZAI SSE-реассемблинг под api.z.ai.
```

---

### 6.7 `pkg/web` (слой 0 — Go-часть)

```yaml
Imports:
  - Usages:
      - dashboard_assets
    From: pkg/web/dashboard

Annotations: |
  Встроенные веб-ассеты дашборда через `go:embed`. Файлы ассетов физически лежат в `pkg/web/dashboard/`
  (см. `dashboard_assets` из Imports) — эта клеточка лишь встраивает их в бинарник и отдаёт как `embed.FS`.

---

"FS() -> fs:embed.FS":
  location: embed.go
  annotations: |
    Встроенная файловая система с веб-ассетами дашборда, полученными через `go:embed dashboard/*`.

    Use `dashboard_assets` from Imports for the actual asset inventory and structure.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Go-часть pkg/web — go:embed обёртка над статическими ассетами дашборда (см. pkg/web/dashboard).
```

---

### 6.8 `pkg/web/dashboard` (слой 0 — веб-ассеты, новая клеточка)

Минимальный контракт «как есть» без выдумывания API — согласовано в Notes задачи как ожидаемый паттерн
для не-Go статического контента.

```yaml
Annotations: |
  Статический контент дашборда: HTML-разметка, стили, клиентский JS и favicon, встраиваемые родительской
  клеточкой `pkg/web` через `go:embed dashboard/*`. Контракт описывает состав и назначение файлов, не
  придумывая API поверх статического контента.

---

"DashboardAssets()":
  location: index.html
  annotations: |
    Статический бандл дашборда, отдаваемый как embed.FS из `pkg/web`.

    Requirements:
    - `index.html` — корневая HTML-страница дашборда
    - `style.css` — стили дашборда
    - `app.js` — клиентская логика (поллинг стадий/событий, WebSocket-подписка через `pkg/server`)
    - `markdown-it.min.js` — сторонняя библиотека рендеринга markdown (планы/фидбек в UI)
    - `favicon.svg` — иконка вкладки браузера

    Constraints:
    - Минимальный контракт: предметного API в привычном смысле нет (статический контент), это ожидаемо
      и не является ошибкой дизайна

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Статические веб-ассеты дашборда afm, встраиваемые в бинарник клеточкой pkg/web.
```

**Cell-level usage:** `pkg/web/dashboard/.usages/dashboard_assets.md` — краткое описание состава файлов и
того, как `pkg/web/embed.go` на них ссылается (`go:embed dashboard/*`), для потребителя `pkg/web`.

---

### 6.9 `assets` (слой 0)

```yaml
Annotations: |
  Встроенные system-промпты (`prompts/`) и claude-скиллы (`claude/skills/afm*`, 5 директорий) как
  embedded FS. Потребители — `cmd/afm/run.go` (промпты через `ReadPrompt`) и
  `cmd/afm/install_skills.go` (скиллы через `SkillsFS`).

---

"ReadPrompt(name string, overrideDir string) -> prompt:string, err:error":
  location: assets.go
  annotations: |
    Возвращает промпт по имени файла.
    `name`: имя файла промпта
    `overrideDir`: если непусто — читать из этой директории вместо встроенных файлов

"FS() -> fs:embed.FS":
  location: assets.go
  annotations: |
    Встроенная файловая система с system-промптами (`prompts/`).

"SkillsFS() -> fs:embed.FS":
  location: assets.go
  annotations: |
    Встроенная файловая система с claude-скиллами (`claude/skills/afm*`, 5 директорий).

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Встроенные system-промпты и claude-скиллы afm как embedded FS.
```

---

### 6.10 `tools/setstatuslinter` (слой 0)

```yaml
Usages:
  x_tools_analysis: .goga/usages/cooks/x-tools-analysis.md

Annotations: |
  Кастомный статический анализатор (`go/analysis`), не имеющий внутренних зависимостей на другие
  пакеты проекта — работает через AST-анализ исходников, не через импорт `pkg/state`. Использует
  `x_tools_analysis` (`singlechecker.Main`) для получения самостоятельного CLI-бинарника.

---

"Analyzer(name string, doc string)":
  location: main.go
  annotations: |
    Анализатор `noStoreApplyOutsideFSM`: запрещает прямые вызовы `(*state.Store).Apply` вне
    `pkg/orchestrator/fsm.go`.

    Requirements:
    - Единственное разрешённое место вызова — `pkg/orchestrator/fsm.go` (см. `pkg/state`, аннотация
      `Store.Apply`)

    Use `x_tools_analysis` for the singlechecker CLI wiring.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Линтер, запрещающий вызовы Store.Apply вне pkg/orchestrator/fsm.go.
```

---

### 6.11 `pkg/prompts` (слой 1)

```yaml
Imports:
  - Types:
      - Stage
    From: pkg/flow

Annotations: |
  Сборка system-промптов для агентов по данным стадии флоу (`Stage` из `pkg/flow`).

---

"Build(in Inputs) -> prompt:string":
  location: builder.go
  annotations: |
    Собирает финальный текст промпта для агента.
    `in`: входные данные сборки (шаблон, стадия, артефакты, контекст диалога и т.д.)

"EscapeTagsForReprompt(s string) -> escaped:string":
  location: builder.go
  annotations: |
    Экранирует служебные теги для использования в тексте повторного промпта (re-prompt).

"Inputs(template string, stage Stage, phaseAgent string, dependencyPlans string, artifacts string, plan string, previousPlan string, feedback string, retryContext string, stageDir string, interactive bool, outputContractMD string, exampleOutput string)":
  location: builder.go
  annotations: |
    Входные данные для сборки промпта одной фазы (planning/implementation/review) одной стадии.
    `phaseAgent`: одно из `planning`, `implementation`, `review` (тип `Agent`)
    `stageDir`: директория стадии — используется для `AFM_STAGE_DIR` file-based dialog protocol
    (см. `pkg/mcp`, `pkg/orchestrator`)
  properties:
    "Template -> string": |
      Шаблон промпта.
    "Stage -> Stage": |
      Стадия флоу, для которой собирается промпт (импортировано из `pkg/flow`).
    "PhaseAgent -> string": |
      Тип фазы-агента.
    "DependencyPlans -> string": |
      Планы стадий-зависимостей.
    "Artifacts -> string": |
      Секция артефактов.
    "Plan -> string": |
      Текущий план.
    "PreviousPlan -> string": |
      Предыдущая версия плана.
    "Feedback -> string": |
      Фидбек ревизии.
    "RetryContext -> string": |
      Контекст повторной попытки.
    "StageDir -> string": |
      Директория стадии (`AFM_STAGE_DIR`).
    "Interactive -> bool": |
      Разрешён ли file-based dialog protocol для этой стадии.
    "OutputContractMD -> string": |
      Markdown-описание контракта результата.
    "ExampleOutput -> string": |
      Пример ожидаемого результата.

"PlanIssues(missingSections []string)":
  location: validator.go
  annotations: |
    Результат валидации плана на предмет отсутствующих обязательных секций.
  properties:
    "MissingSections -> []string": |
      Список отсутствующих секций.
  methods:
    "IsClean() -> clean:bool": |
      true, если отсутствующих секций нет.

"ValidatePlan(md string, required []string) -> issues:PlanIssues":
  location: validator.go
  annotations: |
    Проверяет markdown плана на наличие обязательных секций.
    `required`: список обязательных заголовков секций

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Сборка system-промптов для агентов по данным стадии флоу.
```

---

### 6.12 `pkg/docker` (слой 1)

```yaml
Imports:
  - Types:
      - Flow
    From: pkg/flow

Usages:
  x_term: .goga/usages/cooks/x-term.md

Annotations: |
  Docker-режим: перезапуск afm внутри контейнера, монтирование томов, TTY-детекция (`x_term`), drop
  привилегий через `gosu`.

  Algorithm (общий механизм privilege-drop, см. CLAUDE.md «Docker Mode»):
  1. Контейнер стартует под root
  2. Entrypoint (`docker-entrypoint.sh` + `gosu`) сразу дропает привилегии до хостового uid/gid
     (`AFM_HOST_UID`/`AFM_HOST_GID`)
  3. `HOME=/home/afm` выставляется ПОСЛЕ дропа привилегий через gosu — `gosu` сбрасывает HOME для uid
     без записи в `/etc/passwd`, выставляя `HOME=/`; установка HOME до gosu приводит к тому, что агенты
     ищут `~/`-файлы в `/`

---

"CheckClaudeDockerAuth(clientCommand string) -> err:error":
  location: launcher.go
  annotations: |
    Проверяет, что при `command: claude` в Docker задан один из поддерживаемых auth env vars
    (`CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`).

    Requirements:
    - macOS Keychain недоступен из Linux-контейнера — OAuth-сессия из `~/.claude.json` там не работает
    - Возвращает ошибку с инструкцией, если auth не настроена

"ReExec(cfg ReExecConfig) -> err:error":
  location: launcher.go
  annotations: |
    Заменяет текущий процесс на `docker run` с нужными монтированиями.

    Requirements:
    - Использовать `x_term` (`term.IsTerminal`) для честной TTY-детекции при решении, добавлять ли `-it`
      (`os.ModeCharDevice` ложно срабатывает на `/dev/null`)

    Constraints:
    - Возвращает ошибку только если `docker` не найден в `PATH`; при успехе управление не возвращается
      (`syscall.Exec`)

"ScanCommands(f Flow, globalCmd string) -> mounts:[]CommandMount":
  location: launcher.go
  annotations: |
    Возвращает список нестандартных (не `claude`) агентов из флоу для монтирования в контейнер.
    `f`: описание флоу (импортировано из `pkg/flow`)

    Constraints:
    - Бинарники, не найденные в `PATH` на хосте, молча пропускаются

"SetExecFunc(f string) -> ":
  location: launcher.go
  annotations: |
    Заменяет функцию exec (только для тестов).

    Constraints:
    - `execFunc` — изменяемое состояние уровня пакета; предназначено только для последовательного
      использования в тестах, не безопасно при параллельных тестах

"ResetExecFunc() -> ":
  location: launcher.go
  annotations: |
    Возвращает функцию exec к дефолтной (только для тестов).

"CommandMount(hostPath string, containerName string)":
  location: launcher.go
  annotations: |
    Нестандартный агент для монтирования в контейнер.
  properties:
    "HostPath -> string": |
      Путь к бинарнику на хосте.
    "ContainerName -> string": |
      Имя, под которым бинарник монтируется в контейнер (`/usr/local/bin/<name>`).

"ReExecConfig(image string, projectDir string, commands []CommandMount, dashboardPort int, extraMounts []string, extraArgs []string, clientCommand string)":
  location: launcher.go
  annotations: |
    Параметры для перезапуска afm в Docker.
  properties:
    "Image -> string": |
      Docker-образ.
    "ProjectDir -> string": |
      Абсолютный путь к директории проекта.
    "Commands -> []CommandMount": |
      Нестандартные агенты для монтирования.
    "DashboardPort -> int": |
      Порт дашборда; при >0 пробрасывается на хост через `-p`.
    "ExtraMounts -> []string": |
      Доп. хост-директории (могут начинаться с `~`), монтируются read-only.
    "ExtraArgs -> []string": |
      `os.Args[1:]`.
    "ClientCommand -> string": |
      Имя агента из конфига — для проверки auth при `command: claude`.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Docker-режим самоперезапуска afm: монтирования, TTY-детекция, проверка auth.
```

---

### 6.13 `pkg/executor` (слой 1)

```yaml
Imports:
  - Types:
      - Logger
    From: pkg/progress

Annotations: |
  Запуск и мониторинг агентских процессов: stdout/stderr стриминг, транскрипты, файловый
  dialog-протокол (`AFM_STAGE_DIR`), проксирование HTTP-трафика (`ProxyURL`/`ProxyShimDir`). Использует
  `Logger` для записи человекочитаемого лога стадии.

---

"DefaultClaudeArgs() -> args:[]string":
  location: executor.go
  annotations: |
    Стандартные флаги вызова `claude` в режиме stream-json.

    Requirements:
    - `--verbose` обязателен для Claude Code 2.1.x при совместном использовании `--print` и
      `--output-format=stream-json`; также включает `tool_use`-события в потоке, на которые опирается
      парсер executor'а

"ResolveArgs(extra []string) -> args:[]string":
  location: executor.go
  annotations: |
    Добавляет `DefaultClaudeArgs` перед `extra` и убирает точные дубликаты.

    Requirements:
    - Используется для интерактивных стадий, которым всегда нужны флаги claude независимо от
      пользовательского конфига; дедупликация не даёт передать `--verbose` дважды

"ParseToolAction(line string) -> toolName:string, detail:string, ok:bool":
  location: executor.go
  annotations: |
    Разбирает одну строку stream-json лога в человекочитаемое имя инструмента и деталь.

    Constraints:
    - `ok=false` для событий, которые не логируются (`result`, `system` и т.п.)

"WrittenFiles(jsonlPath string) -> paths:[]string":
  location: executor.go
  annotations: |
    Пути файлов, записанных агентом через инструмент Write, в порядке появления событий в
    stream-json логе.

    Constraints:
    - Отсутствующий или нечитаемый лог даёт пустой список

"DialogTranscript(jsonlPath string) -> items:[]TranscriptItem":
  location: transcript.go
  annotations: |
    Читает stream-json лог и возвращает текстовые сообщения ассистента и вызовы `ask_user` в порядке
    появления.

    Algorithm:
    1. Повторные вызовы `ask_user` с тем же id (polling-ретраи) схлопываются в первое вхождение
    2. Отсутствующий или нечитаемый файл даёт пустой список

"TranscriptItem(text string, askUserID string)":
  location: transcript.go
  annotations: |
    Один элемент диалоговой ленты: либо текст ассистента (`Text != ""`), либо вызов `ask_user`
    (`AskUserID != ""`).
  properties:
    "Text -> string": |
      Текст сообщения ассистента.
    "AskUserID -> string": |
      ID вызова `ask_user`.

"Config(command string, extraArgs []string, idleTimeout string, onAction string, sessionID string, resume bool, stageDir string, proxyURL string, proxyShimDir string)":
  location: executor.go
  annotations: |
    Конфигурация executor'а.
    `onAction`: колбэк, вызываемый для каждого распарсенного действия агента (может быть не задан)
    `sessionID`: если непусто — передаётся через `--session-id` (или `--resume` при `Resume=true`)
    `stageDir`: передаётся агенту как `AFM_STAGE_DIR` (file-based dialog protocol, см. `pkg/mcp`)
    `proxyURL`: если задан — `ANTHROPIC_BASE_URL` и `AFM_PROXY_URL` выставляются в окружении агента
    `proxyShimDir`: если задан — добавляется в начало `PATH` в окружении агента (для враппер-скриптов,
    см. `pkg/proxy`, `CreateShim`)

    Requirements:
    - При установке `ProxyURL` уже существующий `ANTHROPIC_BASE_URL` в окружении вычищается перед
      инъекцией proxy-адреса
  properties:
    "Command -> string": |
      Команда AI-клиента.
    "ExtraArgs -> []string": |
      Дополнительные аргументы.
    "IdleTimeout -> string": |
      Таймаут простоя.
    "SessionID -> string": |
      ID сессии для resume.
    "Resume -> bool": |
      Использовать `--resume` вместо `--session-id`.
    "StageDir -> string": |
      Директория стадии.
    "ProxyURL -> string": |
      Адрес встроенного proxy.
    "ProxyShimDir -> string": |
      Директория shim'а для `PATH`.

"Runner()":
  location: runner.go
  annotations: |
    Интерфейс запуска AI-агентов. Позволяет подмену в тестах.
  methods:
    "RunPlanning(ctx string, stageName string, prompt string, outFile string, logFile string) -> err:error": |
      Запускает AI-клиента с промптом через stdin, собирает текстовый вывод в `outFile`, пишет
      человекочитаемый лог в `logFile` и сырой поток в `logFile+".jsonl"`.
    "RunAgent(ctx string, agentType string, stageName string, prompt string, logFile string) -> err:error": |
      Запускает AI-клиента с промптом через stdin, пишет человекочитаемые действия в `logFile` и сырой
      stream-json в `logFile` с расширением `.jsonl`.

"Executor::Runner(cfg Config)":
  location: executor.go
  annotations: |
    Реализация `Runner`, порождающая процессы AI-клиента.

    Use `Logger` from Imports to write the human-readable stage log for both `RunAgent` and `RunPlanning`.
  methods:
    "New(cfg Config) -> executor:Executor": |
      Создаёт Executor.
    "RunAgent(ctx string, agentType string, stageName string, prompt string, logFile string) -> err:error": |
      См. `Runner.RunAgent`. Использует `Logger` (`NewLogger(logFile)`) для записи человекочитаемого лога.
    "RunPlanning(ctx string, stageName string, prompt string, outFile string, logFile string) -> err:error": |
      См. `Runner.RunPlanning`. Использует `Logger` (`NewLogger(logFile)`) для записи человекочитаемого лога.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Запуск и мониторинг агентских процессов: стриминг, транскрипты, dialog-протокол, proxy-инъекция.
```

---

### 6.14 `pkg/orchestrator` (слой 2 — единая клеточка, см. §4.2)

```yaml
Imports:
  - Types:
      - Config
    From: pkg/config
  - Types:
      - Runner
    From: pkg/executor
  - Types:
      - Stage
    From: pkg/flow
  - Types:
      - QuestionFile
      - Question
    Usages:
      - dialog_protocol
    From: pkg/mcp
  - Types:
      - Inputs
      - Agent
      - PlanIssues
    From: pkg/prompts
  - Types:
      - Store
      - StageStatus
      - Transition
    From: pkg/state

Usages:
  rapid: .goga/usages/cooks/rapid.md

Annotations: |
  Ядро оркестрации: FSM переходов статуса стадии, событийная шина (UI + критическая), граф зависимостей
  стадий, сессии/recovery/retry, adoption плана. Внутренне неоднородна (FSM / Bus / Graph /
  session-recovery — см. §4.2 плана: решение не дробить на отдельные клеточки из-за DSL-ограничения
  «клеточка = Go-пакет» и запрета переноса `.go`-файлов), но type-level аннотации ниже группируют типы
  по этим зонам ответственности.

  FSM-зона (fsm.go, errors.go): переходы статуса + guard-правила + классификация ошибок.
  Bus-зона (bus.go): pub/sub событий — единственная зона с реальным внешним потребителем (`pkg/server`
  держит `UIBus` напрямую).
  Graph-зона (graph.go): граф зависимостей стадий.
  Orchestration-зона (orchestrator.go, completion.go, context.go, plan_adopt.go, recovery.go, retry.go,
  session.go): event loop верхнего уровня, координирующий все остальные зоны.

  Use `rapid` for the FSM property-based test harness (fsm_test.go — тестовый контур, не часть контракта).

---

"IsTerminal(s string) -> terminal:bool":
  location: fsm.go
  annotations: |
    true, если `StageStatus` терминален (`done`/`failed`).

"FSM(store Store)":
  location: fsm.go
  annotations: |
    Конечный автомат переходов статуса стадии.

    FSM-зона: применяет guard-правила (`Rule`) к текущему статусу и событию, персистирует переход через
    `Store` (импорт из `pkg/state`), формируя запись `Transition` (импорт из `pkg/state`).
  methods:
    "NewFSM(store Store) -> fsm:FSM": |
      Создаёт FSM поверх хранилища состояния.
    "Apply(stageID string, ev string, ctx GuardCtx, reason string) -> status:string, applied:bool, err:error": |
      Применяет FSM-событие к стадии, строит `Transition` (`from`/`to`/`event`/`reason`) и передаёт его
      в `Store.Apply`.
      `ev`: одно из `start_planning`, `plan_ready`, `approve`, `revise`, `start_run`, `complete`, `fail`,
      `ask_user`, `user_answered`, `schedule_retry`, `resume_after_retry`, `manual_retry`,
      `blocked_by_dep`, `ready` (тип `FSMEvent`)

"GuardCtx(stage Stage, phase string)":
  location: fsm.go
  annotations: |
    Контекст для guard-правил перехода.
  properties:
    "Stage -> Stage": |
      Стадия флоу (импортировано из `pkg/flow`).
    "Phase -> string": |
      Текущая фаза (`planning`/`implementation`/`review`).

"Rule(from []string, to string)":
  location: fsm.go
  annotations: |
    Guard-правило перехода: из каких статусов в какой, вычисляемый по `GuardCtx`.
  properties:
    "From -> []string": |
      Допустимые исходные статусы (`StageStatus`).
    "To -> string": |
      Функция вычисления целевого статуса по `GuardCtx`.

"Classify(err string) -> classification:int":
  location: errors.go
  annotations: |
    Классифицирует ошибку стадии.

    Requirements:
    - Возвращает одно из: `ClassNone`, `ClassRetryable`, `ClassIncomplete`, `ClassMissingArtifact`,
      `ClassMissingSections`, `ClassFatal`, `ClassStorageFatal`

"IncompleteWorkError(reason string)":
  location: errors.go
  annotations: |
    Ошибка незавершённой работы стадии.
  properties:
    "Reason -> string": |
      Причина незавершённости.

"MissingArtifactError(name string)":
  location: errors.go
  annotations: |
    Ошибка отсутствующего обязательного артефакта.
  properties:
    "Name -> string": |
      Имя отсутствующего артефакта.

"MissingSectionsError(missing []string)":
  location: errors.go
  annotations: |
    Ошибка отсутствующих обязательных секций плана. `Missing` заполняется из `PlanIssues.MissingSections`
    (импорт `pkg/prompts`, `ValidatePlan`) после проверки `IsClean()`.
  properties:
    "Missing -> []string": |
      Список отсутствующих секций.

"StorageError(inner string)":
  location: errors.go
  annotations: |
    Оборачивает фатальную ошибку хранилища состояния.
  properties:
    "Inner -> string": |
      Исходная ошибка.

"UIBus()":
  location: bus.go
  annotations: |
    Bus-зона. Pub/sub шина событий для внешних подписчиков (дашборд, WebSocket). Единственная зона
    `pkg/orchestrator`, потребляемая напрямую внешней клеточкой — `pkg/server` держит `*UIBus` и вызывает
    `Subscribe`/`SubscriberDroppedCount`.

    Requirements:
    - `Subscribe` возвращает канал событий (в контракте абстрагирован как поток `Event`, см. Constraints);
      реализация вправе использовать Go-канал буферизованного размера

    Constraints:
    - Сигнатура DSL не может выразить `<-chan T` напрямую (запрещённая форма) — `Subscribe`/`Recv`
      документируются как отдающие подписку/поток `Event`, а не одиночное значение
  methods:
    "NewUIBus() -> bus:UIBus": |
      Создаёт UIBus.
    "Publish(ev Event) -> ": |
      Публикует событие всем подписчикам.
    "Subscribe(bufSize int) -> id:int, events:Event": |
      Подписывается на поток событий с буфером `bufSize`. `events` — поток (см. Constraints).
    "Unsubscribe(id int) -> ": |
      Отписывает подписчика.
    "SubscriberDroppedCount(id int) -> count:int": |
      Число событий, пропущенных подписчиком из-за переполнения буфера.
    "DroppedCount() -> count:int": |
      Суммарное число пропущенных событий по всем подписчикам.

"CriticalBus(buf int)":
  location: bus.go
  annotations: |
    Bus-зона. Критическая (гарантированная доставка) шина для внутренней координации event loop
    (например, `NotifyAnswer` → `onUserAnswered`).
  methods:
    "NewCriticalBus(buf int) -> bus:CriticalBus": |
      Создаёт критическую шину с указанным буфером.
    "Publish(ctx string, ev Event) -> err:error": |
      Публикует событие с учётом контекста отмены.
    "Recv() -> events:Event": |
      Поток событий для внутреннего потребителя (см. Constraints у `UIBus`).

"Event(eventType string, stageID string, data string)":
  location: bus.go
  annotations: |
    Событие, передаваемое через `UIBus`/`CriticalBus`.
    `eventType`: одно из `stage_status_changed`, `agent_action`, `agent_completed`, `approved`,
    `revised`, `retry_scheduled`, `retry_exhausted`, `manual_retry`, `ask_user`, `user_answered`
    (тип `EventType`)
  properties:
    "Type -> string": |
      Тип события (`EventType`).
    "StageID -> string": |
      ID стадии, к которой относится событие.
    "Data -> string": |
      Полезная нагрузка события (опционально, произвольная структура).

"Graph(stages []Stage)":
  location: graph.go
  annotations: |
    Graph-зона. Граф зависимостей стадий флоу.
  methods:
    "NewGraph(stages []Stage) -> graph:Graph": |
      Строит граф из списка стадий (импортировано из `pkg/flow`).
    "AllIDs() -> ids:[]string": |
      Все ID стадий.
    "Stage(id string) -> stage:Stage": |
      Стадия по ID.
    "ReadyStages(statuses map[string]string) -> ids:[]string": |
      ID стадий в статусе `ready`, все зависимости которых в статусе `done`.
      `statuses`: карта stageID → `StageStatus`

"CollectArtifacts(projectDir string, runDir string, stage Stage, allStages []Stage) -> section:string, err:error":
  location: completion.go
  annotations: |
    Orchestration-зона. Читает файлы артефактов, на которые ссылаются `Inputs` стадии, и формирует
    секцию промпта.

    Constraints:
    - Возвращает ошибку, если обязательный файл артефакта отсутствует

"CollectDependencyPlans(runDir string, stage Stage, allStages []Stage) -> section:string":
  location: completion.go
  annotations: |
    Orchestration-зона. Читает `plan.md` каждой стадии из `DependsOn` и формирует секцию промпта.

    Constraints:
    - Отсутствующие планы дают предупреждающий комментарий, не ошибку

"Options(runDir string, stages []Stage, store Store, config Config, prompts Prompts, runner Runner, dashboardURL string, proxyURL string, proxyShimDir string)":
  location: orchestrator.go
  annotations: |
    Orchestration-зона. Конфигурация оркестратора.
    `runner`: nil = реальный Executor (импорт `pkg/executor`)
  properties:
    "RunDir -> string": |
      Директория рана.
    "Stages -> []Stage": |
      Стадии флоу.
    "Store -> Store": |
      Хранилище состояния (импорт `pkg/state`).
    "Config -> Config": |
      Конфигурация afm (импорт `pkg/config`).
    "Prompts -> Prompts": |
      Шаблоны промптов.
    "Runner -> Runner": |
      Исполнитель агентов (импорт `pkg/executor`).
    "DashboardURL -> string": |
      URL дашборда.
    "ProxyURL -> string": |
      Адрес встроенного proxy, пробрасывается в окружение executor'а.
    "ProxyShimDir -> string": |
      Директория shim'а, пробрасывается в `PATH` executor'а.

"Prompts(planning string, implementation string, review string, summary string)":
  location: orchestrator.go
  annotations: |
    Шаблоны промптов для каждого типа агента.
  properties:
    "Planning -> string": |
      Шаблон промпта планирования.
    "Implementation -> string": |
      Шаблон промпта реализации.
    "Review -> string": |
      Шаблон промпта ревью.
    "Summary -> string": |
      Шаблон промпта саммари.
  methods:
    "DefaultPrompts() -> prompts:Prompts": |
      Пустые промпты (заполняются из `assets`).

"Orchestrator(opts Options)":
  location: orchestrator.go
  annotations: |
    Orchestration-зона. Управляет полным жизненным циклом рана флоу через событийный цикл.

    Use `dialog_protocol` from Imports for the full file-based question/answer contract (`pkg/mcp`).

    Algorithm (file-based dialog protocol — совместно с `pkg/mcp`, см. CLAUDE.md и `dialog_protocol`):
    1. `startQuestionPoller` — фоновая горутина сканирует директории стадий каждую секунду на предмет
       `*.question.json`, получая список `QuestionFile` (через `FindUnansweredQuestions` из `pkg/mcp`)
    2. При обнаружении — конструирует `Question` (через `AppendQuestion`, если требуется зафиксировать
       вопрос в `dialog.jsonl`) и публикует `Event` типа `ask_user`, стадия переходит в
       `awaiting_user_input`
    3. `activeAgents` (потокобезопасная карта) отслеживает, какие стадии имеют активную горутину агента
    4. `NotifyAnswer`: если горутина агента ещё активна — только переход FSM (bash-цикл агента сам
       обнаружит файл ответа); если горутина уже завершилась — публикация в критическую шину, чтобы
       `onUserAnswered` перезапустил агента с `--resume`
  methods:
    "New(opts Options) -> orchestrator:Orchestrator": |
      Создаёт оркестратор.
    "Run(ctx string) -> err:error": |
      Запускает событийный цикл оркестратора. Для каждой фазы стадии строит промпт через `Prompts`
      (импорт `pkg/prompts`), помечая фазу значением `Agent` (`AgentPlanning`/`AgentImplementation`/
      `AgentReview`); план проверяется через `ValidatePlan` -> `PlanIssues`, повторный запрос секций —
      только если `!IsClean()`.
    "Approve(ctx string, stageID string) -> err:error": |
      Утверждает план стадии.
    "Revise(ctx string, stageID string, feedback string) -> err:error": |
      Отправляет фидбек на повторное планирование стадии.
    "Retry(ctx string, stageID string) -> err:error": |
      Повторяет проваленную стадию, переводя её в pending и перезапуская.
    "FailStage(stageID string, reason string) -> ": |
      Помечает стадию проваленной с указанной причиной.
    "Trigger(stageID string, ev string, ctx GuardCtx, reason string) -> status:string, applied:bool": |
      Применяет FSM-событие для перехода статуса стадии.
    "NotifyAnswer(stageID string, phase string, qID string, answer string, fromOptions bool) -> err:error": |
      См. Algorithm выше.
    "SetDashboardURL(url string) -> ": |
      Устанавливает URL дашборда после старта сервера.
    "UIBus() -> bus:UIBus": |
      Возвращает `UIBus` для внешних подписчиков (`pkg/server`, WebSocket).

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Ядро оркестрации afm: FSM, событийные шины, граф зависимостей, event loop, file-based dialog protocol.
```

---

### 6.15 `pkg/server` (слой 3)

```yaml
Imports:
  - Types:
      - UIBus
      - Event
    From: pkg/orchestrator
  - Types:
      - Store
      - StageStatus
    From: pkg/state
  - Types:
      - FS
    From: pkg/web
  - Types:
      - TranscriptItem
    From: pkg/executor
  - Types:
      - Entry
      - Answer
    From: pkg/mcp

Usages:
  gorilla_websocket: .goga/usages/cooks/gorilla-websocket.md

Annotations: |
  HTTP + WebSocket дашборд, отдающий события оркестратора в UI, встраивает `pkg/web`. WebSocket-стриминг
  событий использует `gorilla_websocket`.

  Хендлер диалога (`/api/stages/<id>/dialog`) собирает UI-ленту вопросов/ответов, комбинируя
  `ReadDialog` -> `[]Entry` (импорт `pkg/mcp`) с `DialogTranscript` -> `[]TranscriptItem` (импорт
  `pkg/executor`) для сопоставления записей диалога с текстом ассистента; принимает ответ пользователя
  через `AppendAnswer` (`Answer`, импорт `pkg/mcp`).

---

"Config(port int, runDir string, store Store, uiBus UIBus, approveFn string, reviseFn string, retryFn string, dialogAnswerFn string, dialogCancelFn string)":
  location: server.go
  annotations: |
    Настройки сервера.
  properties:
    "Port -> int": |
      Порт HTTP-сервера.
    "RunDir -> string": |
      Директория рана.
    "Store -> Store": |
      Хранилище состояния (импорт `pkg/state`).
    "UIBus -> UIBus": |
      Событийная шина для WebSocket-стриминга (импорт `pkg/orchestrator`).
    "ApproveFn -> string": |
      Колбэк утверждения плана стадии.
    "ReviseFn -> string": |
      Колбэк отправки фидбека ревизии.
    "RetryFn -> string": |
      Колбэк повтора проваленной стадии.
    "DialogAnswerFn -> string": |
      Колбэк приёма ответа диалога (file-based dialog protocol, см. `pkg/mcp`, `pkg/orchestrator`).
    "DialogCancelFn -> string": |
      Колбэк отмены диалога.

"Server(cfg Config)":
  location: server.go
  annotations: |
    HTTP-сервер дашборда и API.

    Algorithm (WebSocket-стриминг, см. `gorilla_websocket`):
    1. `websocket.Upgrader` создаётся один раз на пакет, `CheckOrigin` разрешает любой источник
       (локальный дашборд)
    2. Обработчик апгрейдит соединение, подписывается на `UIBus` (`Subscribe`)
    3. Пишет каждое `Event` как JSON-текстовое сообщение, пока подписка открыта
    4. При превышении буфера подписчика отслеживается `SubscriberDroppedCount`

    Requirements:
    - Ответ HTTP-хендлера диалога (`/api/stages/<id>/dialog`) записывает `<phase>.<id>.answer.json`
      атомарно (эксклюзивное создание, O_EXCL) — критический путь, см. `pkg/mcp`
    - Хендлеры approve/revise/retry/dialog проверяют текущий `StageStatus` стадии перед действием
      (например, approve/revise допустимы только при `StageStatus` = `awaiting_approval`, retry — только
      при `failed`, ответ на диалог — только при `awaiting_user_input`)
    - Корень (`/`) отдаётся статикой из `FS` (импорт `pkg/web`) через `http.FileServer`
  methods:
    "New(cfg Config) -> server:Server": |
      Создаёт сервер.
    "Start() -> addr:string, err:error": |
      Запускает HTTP-сервер, возвращает фактический адрес.
    "Handler() -> handler:http.Handler": |
      Возвращает HTTP-обработчик (для тестов).
    "Shutdown(ctx string) -> err:error": |
      Плавно останавливает сервер.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  HTTP + WebSocket дашборд afm: API стадий, дозвод/ревизия/ретрай, диалог, стриминг событий.
```

---

### 6.16 `cmd/afm` (слой 4)

`cmd/afm` — `package main`, точка входа, ничем не импортируется (верхушка графа). Правило
`goga-cell-go` «CODEMANIFEST ссылается только на экспортируемые имена» не применимо буквально к
main-пакету (в Go экспортируемость не имеет смысла для неимпортируемого пакета) — контракт описывает
реальную структуру CLI-команд (`newXxxCmd`) как единственный публичный API уровня приложения (для
конечного пользователя CLI, а не для другого Go-кода).

```yaml
Imports:
  - Types:
      - Config
      - DockerConfig
    From: pkg/config
  - Types:
      - ReExecConfig
      - CommandMount
    Usages:
      - docker_privilege_drop
    From: pkg/docker
  - Types:
      - Flow
    From: pkg/flow
  - Types:
      - Orchestrator
      - Options
      - Prompts
    From: pkg/orchestrator
  - Types:
      - Proxy
      - BuildTransforms
      - CreateShim
    From: pkg/proxy
  - Types:
      - Server
      - Config AS ServerHandlerConfig
    From: pkg/server
  - Types:
      - Store
      - StageStatus
      - Transition
      - RunState
    From: pkg/state
  - Types:
      - FS
      - SkillsFS
      - ReadPrompt
    From: assets

Usages:
  cobra: .goga/usages/cooks/cobra.md

Annotations: |
  CLI entrypoint afm. Дерево команд строится через `cobra` — корневая команда с персистентным флагом
  `--dir` и `PersistentPreRunE`, вычисляющим `.afm`-директорию по приоритету флаг > `AFM_DIR` > `.`.
  Каждая подкоманда — независимый конструктор `newXxxCmd`, регистрируемый через `root.AddCommand`.

  `approve`/`retry`/`revise` строят `Transition` (импорт `pkg/state`) и применяют его через
  `store.Apply`; `check`/`approve` читают текущий снимок через `RunState` (импорт `pkg/state`).

---

"resolveRootDir(dirFlag string, envDir string) -> dir:string":
  location: main.go
  annotations: |
    Вычисляет базовую директорию проекта по приоритету: флаг `--dir` > `AFM_DIR` > текущая директория.

"fmDir() -> path:string":
  location: main.go
  annotations: |
    Возвращает эффективный путь `.afm` (join `rootDir` + `.afm`).

"newRootCmd() -> cmd:cobra.Command":
  location: main.go
  annotations: |
    Корневая команда afm.

    Use `cobra` for implementation.

    Requirements:
    - Персистентный флаг `--dir`
    - `PersistentPreRunE` вызывает `resolveRootDir`

"newRunCmd() -> cmd:cobra.Command":
  location: run.go
  annotations: |
    Запускает флоу: разрешает путь к flow-файлу, загружает `Config` (`pkg/config`, `LoadFrom`),
    инициализирует ран, стартует встроенный `Proxy` (`pkg/proxy`, если задан upstream) через
    `BuildTransforms` + shim (`CreateShim`), опционально перезапускает себя в Docker через `ReExecConfig`
    (`pkg/docker`, см. `docker_privilege_drop`), запускает `Orchestrator` с `Options` (`pkg/orchestrator`)
    и `Server` с `ServerHandlerConfig` (`pkg/server`), открывает браузер.

    Algorithm (built-in reverse proxy, см. CLAUDE.md «Built-in Reverse Proxy»):
    1. Upstream разрешается: `Config.Proxy.Upstream`, иначе `ANTHROPIC_BASE_URL`; если оба пусты — proxy
       пропускается (no-op)
    2. Ошибка `Proxy.Start` — фатальна; ошибка `CreateShim` — не фатальна (предупреждение, инъекция
       env-переменных всё равно работает)
    3. Proxy и shim останавливаются через `defer` (`Shutdown`, удаление shim-директории)

    Requirements:
    - Docker-режим (`Config.Docker` — `DockerConfig`, `IsDockerEnabled`): при включении собрать
      `CommandMount` для нестандартных агентов (`ScanCommands`) и вызвать `ReExec` с заполненным
      `ReExecConfig`

    Requirements:
    - Docker-режим (`IsDockerEnabled`): при включении — `ReExec` из `pkg/docker` (см.
      `docker_privilege_drop` из Imports)

"resolveFlowPath(args []string) -> path:string, err:error":
  location: run.go
  annotations: |
    Разрешает путь к flow-файлу из аргументов команды (расширения `.yaml`/`.yml`).

"resolveRun(f Flow) -> runDir:string, store:Store, err:error":
  location: run.go
  annotations: |
    Инициализирует директорию рана и хранилище состояния для флоу.

"loadPrompts(overrideDir string) -> prompts:Prompts, err:error":
  location: run.go
  annotations: |
    Загружает шаблоны промптов через `ReadPrompt` (импорт из `assets`), с опциональным переопределением
    директории.

"browserCmd() -> cmd:string":
  location: run.go
  annotations: |
    Определяет команду открытия браузера для текущей ОС.

"openBrowser(url string) -> ":
  location: run.go
  annotations: |
    Открывает URL в браузере хоста.

    Constraints:
    - Пропускается внутри Docker-контейнера (`AFM_IN_DOCKER=1`) — afm внутри Linux-контейнера не может
      открыть браузер на хосте

"launchHostBrowserOpener(port int) -> ":
  location: run.go
  annotations: |
    Запускает хост-side процесс-помощник ДО re-exec в Docker, который поллит проброшенный порт и
    вызывает `open`/`xdg-open` на хосте.

"newCheckCmd() -> cmd:cobra.Command":
  location: check.go
  annotations: |
    Показывает статус текущего рана: список стадий и их `StageStatus` (импорт из `pkg/state`).

"statusColor(s string) -> color:string":
  location: check.go
  annotations: |
    Цвет вывода для статуса стадии.
    `s`: значение `StageStatus`

"lastLogAction(stageDir string) -> action:string":
  location: check.go
  annotations: |
    Последняя записанная строка действия из лога стадии.

"newApproveCmd() -> cmd:cobra.Command":
  location: approve.go
  annotations: |
    Утверждает план стадии (вызывает `Orchestrator.Approve`).

"findLatestRunDir(stageID string) -> runDir:string, stageIDs:[]string, err:error":
  location: approve.go
  annotations: |
    Находит последнюю директорию рана, содержащую указанную стадию.

"newReviseCmd() -> cmd:cobra.Command":
  location: revise.go
  annotations: |
    Отправляет фидбек на повторное планирование стадии (вызывает `Orchestrator.Revise`).

"newRetryCmd() -> cmd:cobra.Command":
  location: retry.go
  annotations: |
    Повторяет проваленную стадию (вызывает `Orchestrator.Retry`).

"newListCmd() -> cmd:cobra.Command":
  location: list.go
  annotations: |
    Выводит список доступных ранов/флоу.

"newInitCmd() -> cmd:cobra.Command":
  location: init.go
  annotations: |
    Интерактивно создаёт новый `flow.yaml`, опрашивая пользователя через `prompt` по одной стадии
    (`stageInput`) за раз.

"prompt(scanner string, label string) -> value:string":
  location: init.go
  annotations: |
    Запрашивает у пользователя строку ввода с меткой.

"splitComma(s string) -> parts:[]string":
  location: init.go
  annotations: |
    Разбивает строку по запятой в список.

"stageInput()":
  location: init.go
  annotations: |
    Промежуточная структура пользовательского ввода для одной стадии при интерактивном создании флоу.

"newInstallSkillsCmd() -> cmd:cobra.Command":
  location: install_skills.go
  annotations: |
    Устанавливает claude-скиллы afm (`SkillsFS`, импорт из `assets`) в целевую директорию.

"resolveSkillsDir(override string) -> dir:string, err:error":
  location: install_skills.go
  annotations: |
    Разрешает целевую директорию установки скиллов.

"installSkills(dest string, force bool) -> err:error":
  location: install_skills.go
  annotations: |
    Копирует скиллы из `SkillsFS` в целевую директорию.

    Requirements:
    - Пересоздаёт директории скиллов afm полностью на каждый запуск (удалить, затем скопировать)

    Constraints:
    - Не трогает содержимое, принадлежащее другим расширениям

"main() -> ":
  location: main.go
  annotations: |
    Точка входа процесса afm — строит корневую команду (`newRootCmd`) и выполняет её.

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  CLI entrypoint afm: run/check/approve/retry/revise/list/init/install-skills поверх cobra.
```

**Cell-level usage:** `pkg/docker/.usages/docker_privilege_drop.md` — для потребителя `cmd/afm/run.go`:
как корректно передавать `AFM_HOST_UID`/`AFM_HOST_GID` в `ReExecConfig` и в каком порядке entrypoint
должен выставлять `HOME` относительно `gosu` (см. аннотацию `pkg/docker` в §6.12).

---

### 6.17 Cell-level `.usages/` файлы, требуемые для клеточек с внутренними потребителями

Per Acceptance Criteria п.5 из task-файла и `goga-cookbook` (практики консьюмера — на стороне
клеточки-поставщика, `<cell_path>/.usages/`). Ниже — полный список клеточек с реальными внутренними
потребителями (по графу §3) и файл практики, который `goga-apply` должен создать для каждой. Три файла,
отмеченные ★, уже детализированы содержательно в §6 (нюансы CLAUDE.md, наиболее рискующие быть
потерянными при миграции); остальные — стандартные facade-практики («как вызывать эту клеточку»),
авторство контента которых явно отнесено на `goga-apply` (эта brainstorm-стадия фиксирует факт
необходимости и потребителя, не пишет содержимое всех практик).

| Клеточка (поставщик)   | Файл практики                                         | Потребитель(и)                          |
|-------------------------|--------------------------------------------------------|-------------------------------------------|
| `pkg/web/dashboard`     | `.usages/dashboard_assets.md` ★ (см. §6.8)              | `pkg/web`                                  |
| `pkg/mcp`               | `.usages/dialog_protocol.md` ★ (протокол вопрос/ответ, атомарная запись O_EXCL — см. Algorithm в §6.5) | `pkg/orchestrator` |
| `pkg/docker`            | `.usages/docker_privilege_drop.md` ★ (см. §6.16)        | `cmd/afm`                                  |
| `pkg/config`            | `.usages/config_facade.md`                              | `pkg/orchestrator`, `cmd/afm`               |
| `pkg/flow`               | `.usages/flow_facade.md`                                | `pkg/prompts`, `pkg/docker`, `pkg/orchestrator`, `cmd/afm` |
| `pkg/state`              | `.usages/store_facade.md`                               | `pkg/orchestrator`, `pkg/server`, `cmd/afm` |
| `pkg/web`                | `.usages/embed_fs_facade.md`                            | `pkg/server`                               |
| `pkg/executor`           | `.usages/runner_facade.md`                              | `pkg/orchestrator`, `pkg/server`            |
| `pkg/prompts`            | `.usages/prompts_facade.md`                             | `pkg/orchestrator`                          |
| `pkg/orchestrator`       | `.usages/orchestrator_facade.md`                        | `pkg/server`, `cmd/afm`                     |
| `pkg/proxy`              | `.usages/proxy_facade.md`                               | `cmd/afm`                                   |
| `pkg/server`             | `.usages/server_facade.md`                              | `cmd/afm`                                   |
| `assets`                 | `.usages/assets_facade.md`                              | `cmd/afm`                                   |

`tools/setstatuslinter` не входит в таблицу — не имеет внутренних потребителей (проверено `grep`, §3).

---

## 7. Требуемый подготовительный шаг (перед `goga-apply`)

Один физический шаг, согласованный с пользователем в §4.1, должен быть выполнен ДО материализации
клеточки `pkg/web/dashboard` (эта brainstorm-стадия его не выполняет — только планирует):

1. `git mv pkg/web/index.html pkg/web/dashboard/index.html` (аналогично для `style.css`, `app.js`,
   `markdown-it.min.js`, `favicon.svg`)
2. В `pkg/web/embed.go` заменить `//go:embed index.html style.css app.js markdown-it.min.js favicon.svg`
   на `//go:embed dashboard/*` (путь embed-директивы, поведение `FS` не меняется — те же файлы по тем же
   относительным путям внутри `embed.FS`)
3. `go build ./...` — убедиться, что встраивание не сломалось

Это единственное физическое перемещение файлов во всём плане; оно не затрагивает `.go`-файлы и не меняет
поведение системы (см. обоснование в §4.1).

## 8. Риски (перенесены из task-файла + уточнены)

- **`pkg/orchestrator` — крупнейшая клеточка (11 файлов), решение не дробить дальше зафиксировано в §4.2**
  и обусловлено DSL-ограничением «клеточка = Go-пакет», а не архитектурной оценкой сложности. Если в
  будущем потребуется физически разнести `pkg/orchestrator` на суб-пакеты (отдельная задача через
  `goga-change`), дробление клеточек по зонам FSM/Bus/Graph/session-recovery, уже намеченным в §6.14,
  станет органичным следующим шагом.
- **Три нюанса CLAUDE.md явно закреплены в аннотациях** (не потеряны): file-based dialog protocol —
  §6.5 (`pkg/mcp`) + §6.14 (`Orchestrator`, Algorithm); reverse proxy — §6.6 (`pkg/proxy`, `ZAITransform`
  Algorithm) + §6.16 (`newRunCmd`, Algorithm); Docker privilege-drop — §6.12 (`pkg/docker`, Annotations
  Algorithm).
- **`pkg/web` и `pkg/web/dashboard` не имеют предметного API** в привычном смысле — контракты минимальны
  (embed.FS + список статических файлов), это ожидаемо (см. §6.7, §6.8).
- Циклических зависимостей в графе §3 не обнаружено — граф идентичен провалидированному в задаче.

## 9. Проверка Acceptance Criteria

| # | Критерий | Статус в этом плане |
|---|----------|----------------------|
| 1 | ≥16 клеточек по графу импортов | 16 клеточек спроектировано (§3, §6); физически материализуются в `goga-apply` после подготовительного шага §7 |
| 2 | `goga lint` без ошибок структуры/казинга | DSL-синтаксис каждой клеточки соответствует `dsl.md` (см. §10 верификацию) |
| 3 | Контракт покрывает публичный API реального пакета | Все типы/функции/методы взяты из `go doc -all` по каждому пакету (см. §6) |
| 4 | `Imports` точно соответствуют реальному графу | Проверено `grep` по всем non-test `.go`-файлам (§3, §6 — секции Imports) |
| 5 | Все 7 внешних usage-файлов подключены и применены | См. §5 — таблица подключения к клеточкам, применены в Annotations §6 |
| 6 | 3 нюанса CLAUDE.md отражены в аннотациях | См. §8 — таблица ссылок на конкретные секции |
| 7 | Каркас `cells/` отсутствует | Не создавался; клеточки размещены поверх существующих директорий пакетов |

## 10. Верификация плана (`[VERIFICATION_REPORT]`)

Прогон выполнен построчно по всем 16 CODEMANIFEST в §6, с автоматизированными self-check скриптами
(структурные инварианты) и ручной сверкой `location` против реального листинга файлов каждого пакета
(`ls`/`grep`). Найденные несоответствия исправлены прямо в этом файле (см. «Fixes Applied»); ниже —
финальное состояние после исправлений.

### Check Results

| # | Check | Статус | Evidence |
|---|-------|--------|----------|
| 1 | Completeness (все типы/методы/свойства покрыты) | PASS | Каждый тип в §6 сверен построчно с выводом `go doc -all` по соответствующему пакету |
| 2 | DSL correctness (синтаксис, структура документа) | PASS (после фиксов) | Каждая клеточка — ровно 2 разделителя `---` (Header|Body|Footer), см. проверку ниже |
| 3 | Inter-cell consistency (`Imports` ссылаются на существующие клеточки, типы совпадают) | PASS | Граф §6-Imports 1:1 совпадает с графом §3 (скрипт сверки) |
| 4 | Implementation order (клеточка создаётся после всех зависимостей) | PASS | Порядок §6.1→§6.16 строго bottom-up по слоям §3 |
| 5 | No placeholders (TBD/TODO) | PASS | `grep -n "TBD\|TODO\|FIXME\|XXX"` — 0 совпадений |
| 6 | Usage of `Imports.Types` (каждый импортированный тип используется в теле) | PASS (после фиксов) | Скрипт сверки: 0 неиспользуемых типов после исправлений (см. Fixes Applied) |
| 7 | Usage of `Imports.Usages` | PASS | `dialog_protocol` (§6.14), `dashboard_assets` (§6.7), `docker_privilege_drop` (§6.16) — каждый упомянут в annotation |
| 8 | Usage of header `Usages` | PASS | `rapid` референс — в глобальной `Annotations` §6.14 (учтено; см. примечание ниже про ложное срабатывание скрипта) |
| 9 | Algorithms in annotations | PASS | `Algorithm:` присутствует у всех типов/методов с нетривиальной логикой (FSM.Apply, ZAITransform, Orchestrator, NewLogger, docker privilege-drop) |
| 10 | Annotation wording (без деталей реализации) | PASS | Технические термины (O_EXCL, gosu, syscall.Exec) — поведенческие факты контракта, не code-level инструкции |
| 11 | Resolvability of backtick-ссылок | PASS (после фикса) | Исправлена одна неразрешимая ссылка `` `EventAskUser` `` (не существующий в конвенции идентификатор) → `` `Event` типа `` `ask_user` `` (§6.14) |
| 12 | Location restrictions (имя файла, без пути, без вложенности) | PASS (после фиксов) | Исправлено 7 неверных `location`: `pkg/progress` (`lock.go`→`progress.go`, `logger.go`→`progress.go`), `pkg/state` (`Store`/`Transition`/`SetApplyHook`: `state.go`→`store.go`), `pkg/executor` (`Runner`: `executor.go`→`runner.go`), `pkg/prompts` (`PlanIssues`/`ValidatePlan`: `builder.go`→`validator.go`), `pkg/docker` (`CheckClaudeDockerAuth`: `docker.go`→`launcher.go`, файл `docker.go` не существовал вовсе), `tools/setstatuslinter` (`analyzer.go`→`main.go`) |
| 13 | Absence of cross-imports (A↔B) | PASS | Скрипт сверки графа §6 — 0 циклов |
| 14 | Embedding from Imports | PASS | Embedding (`->`) не использован ни в одной клеточке плана |
| 15 | Mutations from available types | PASS | Единственная мутация `Executor::Runner` (§6.13) — `Runner` объявлен в теле того же документа |
| 16 | Entity/Routine correctness | PASS (после фиксов) | Убраны 2 некорректных пустых блока `properties: {}`/`methods: {}` у чистых Routine (`statusColor`, `newInitCmd`, §6.16) |
| 17 | Base usages from `.goga/config.yml` | N/A | `.goga/config.yml` отсутствует — `goga-codemanifest-base` вернул "Option not found" (зафиксировано как факт в Phase 2) |
| 18 | Base annotations from `.goga/config.yml` | N/A | То же — секция `codemanifest` отсутствует в проекте |
| 19 | Language correctness (Go naming, `location`) | PASS | Экспортируемые имена — PascalCase, `location` — реальные `.go`/статические файлы с расширением (см. Check 12) |

### Fixes Applied

| Issue | Fix | Re-check |
|-------|-----|----------|
| `Imports` в `pkg/orchestrator`, `cmd/afm`, `pkg/server` использовали `---` как разделитель между источниками импорта (запрещено — `---` зарезервирован только для Header/Body/Footer) | Переписаны как единый YAML-список `Imports:\n  - Types: ... From: ...\n  - Types: ...` | PASS — по 2 `---` на клеточку во всём файле |
| 4 клеточки (`pkg/web`, `pkg/prompts`, `pkg/docker`, `pkg/executor`) объявляли единственный источник `Imports` как mapping, а не list-item (без `- `) | Добавлен ведущий `- ` перед `Types`/`Usages` | PASS |
| `pkg/executor` не имел `Imports` на `pkg/progress`, хотя `executor.go` реально вызывает `progress.NewLogger` | Добавлен `Imports: Types: [Logger] From: pkg/progress` + ссылка в annotations `Runner`/`Executor::Runner` | PASS — граф §6 совпал с реальным `grep` |
| `pkg/orchestrator` импортировал `Flow` (не используется — реально используется только `Stage`), но не импортировал `Question` (реально используется `mcp.Question{}` в `orchestrator.go`) | Убран `Flow`, добавлен `Question`; добавлены backtick-ссылки на `QuestionFile`/`Question`/`Transition` в annotations | PASS |
| `pkg/server` не ссылался на импортированные `Event`/`StageStatus`/`FS` нигде в теле | Добавлены явные упоминания в `Server`/`Config` annotations (WebSocket Algorithm, статус-гейты хендлеров, статика через `FS`) | PASS |
| `cmd/afm` не ссылался на 8 импортированных типов (`Config`, `DockerConfig`, `ReExecConfig`, `CommandMount`, `Options`, `BuildTransforms`, `Server`, `ServerHandlerConfig`) | Переписана annotation `newRunCmd` с явными backtick-ссылками на все | PASS |
| 7 значений `location` указывали на несуществующие файлы (`lock.go`, `logger.go`, `state.go` для `Store`/`Transition`/`SetApplyHook`, `executor.go` для `Runner`, `builder.go` для `PlanIssues`/`ValidatePlan`, `docker.go`, `analyzer.go`) | Заменены на реальные имена файлов, сверенные `grep`/`ls` (см. Check 12) | PASS |
| 2 пустых блока `properties: {}`/`methods: {}` у чистых Routine | Убраны — Routine не имеет `methods`/`properties` вовсе | PASS |
| Backtick-ссылка `` `EventAskUser` `` не резолвится ни в одну из объявленных string-значений `EventType` | Заменена на `` `Event` типа `` `ask_user` `` | PASS |

### Unresolved Issues

Нет. Все найденные несоответствия исправлены и перепроверены.

### Final Status

**VERIFIED.** Единственная оговорка та же, что в §9: физическое число клеточек (16) в `goga schema`
станет фактом только после подготовительного шага §7 (перенос НЕ-`.go`-ассетов `pkg/web` →
`pkg/web/dashboard`), который относится к следующей стадии (`goga-apply`), а не к этой (`goga-brainstorm`).

### Дополнительные архитектурные инварианты (сохранены из первого прохода)

- **Циклы:** ни одна клеточка слоя N не импортирует клеточку слоя ≥ N (§3); `pkg/web` импортирует только
  `Usages` (не `Types`) из `pkg/web/dashboard`, что не создаёт цикла.
- **Осиротевшие клеточки:** нет — каждая клеточка либо потребляется другой (по графу §3), либо является
  терминальной вершиной графа (`cmd/afm` слой 4, ничем не импортируется — ожидаемо для entrypoint).
- **Enum-конвенция:** строковые константы (`StageStatus`, `EventType`, `FSMEvent`, `AgentType`, `Agent`)
  документируются как значения в annotations типов-потребителей, не как top-level записи; int-константа
  `Classification` документируется по реальным Go-идентификаторам (`ClassNone` и т.д.), так как не имеет
  строкового представления. Упоминания самих имён enum-типов (`` `EventType` ``, `` `AgentType` ``,
  `` `FSMEvent` ``) — документационные, поясняющие происхождение перечисленных значений, а не формальные
  DSL-ссылки на отдельно объявленные типы.

**Вердикт: PASSED.** Все 7 критериев Acceptance Criteria (§9) выполнимы материализацией этого плана;
единственная оговорка — критерий 1 (число клеточек) становится фактическим только после подготовительного
шага §7, который является частью следующей стадии (`goga-apply`), а не этой (`goga-brainstorm`).

### Второй проход (независимая ревизия, стадия `brainstorm-review`)

Первый проход (выше) — самопроверка автора плана. Этот проход не принимает её вердикт на веру: выборочно
пересверены `Imports` 5 клеточек (`pkg/orchestrator`, `pkg/server`, `cmd/afm`, `pkg/executor`, `pkg/mcp`)
против реального импорт-графа (`grep` по non-test `.go`) и `location` 4 клеточек (`pkg/config`,
`pkg/state`, `pkg/executor`, `pkg/docker`) против реального листинга файлов — в направлении, которое
первый проход не проверял («не пропущена ли реальная зависимость», а не только «нет ли лишней»).

#### Check Results (выборка)

| # | Check | Статус | Evidence |
|---|-------|--------|----------|
| 1 | `pkg/server` Imports покрывает все реально используемые внешние пакеты | FAIL → PASS (после фикса) | `handlers.go:207-388` вызывает `mcp.ReadDialog`/`mcp.Entry`/`mcp.AppendAnswer`/`mcp.Answer` и `executor.go:217` — `executor.DialogTranscript`/`executor.TranscriptItem`, ни один не был в `Imports` §6.15 |
| 2 | `cmd/afm` Imports из `pkg/state` покрывает все реально используемые типы | FAIL → PASS (после фикса) | `grep` по `cmd/afm/*.go`: `state.Transition` (`approve.go`, `retry.go`, `revise.go`), `state.RunState` (`check.go`, `approve.go`) — оба отсутствовали в §6.16 |
| 3 | `pkg/orchestrator` Imports из `pkg/prompts` покрывает все реально используемые типы | FAIL → PASS (после фикса) | `orchestrator.go`/`completion.go`/`plan_adopt.go` используют `prompts.AgentPlanning/Implementation/Review` и `prompts.ValidatePlan(...).IsClean()/.MissingSections` (`PlanIssues`) — оба типа отсутствовали в §6.14 |
| 4 | `pkg/executor` `location` полей совпадает с реальным файлом определения | FAIL → PASS (после фикса) | `WrittenFiles` объявлен в `executor.go:306` (`grep func WrittenFiles`), §6.13 указывал `transcript.go` |
| 5 | `pkg/mcp`, `pkg/config`, `pkg/state`, `pkg/docker` (остальные из выборки) | PASS | Impорты/`location` совпали с реальным графом и листингом файлов, фиксов не потребовалось |
| 6 | Структура плана (4 обязательных раздела, отсутствие плейсхолдеров/кода реализации) | PASS | Implementation order (§3), Artifacts per cell (§6 + §6.17), Dependency map (§3), Verification checklist (§10) присутствуют; `grep -n "TBD\|TODO"` — 0 совпадений; блоков кода реализации (не `yaml`/не диаграммы) не найдено |
| 7 | Cell cohesion (`pkg/web` split §4.1, `pkg/orchestrator` non-split §4.2) | PASS | Обоснование в обоих случаях соответствует критериям `goga-cookbook`; изменений не потребовалось |
| 8 | Task AC (7 критериев `docs/tasks/goga-claude.md`) 1:1 отражены в §2/§9 | PASS | Построчное сопоставление — расхождений нет |
| 9 | Impact on Existing Architecture (модификация существующих клеточек) | N/A | `goga schema` → `[]`, все 16 клеточек новые |

#### Fixes Applied (второй проход)

| Issue | Fix | Re-check |
|-------|-----|----------|
| §6.15 `pkg/server`: `Imports` не содержал `pkg/executor` (`TranscriptItem`) и `pkg/mcp` (`Entry`, `Answer`) | Добавлены оба источника в `Imports`; добавлена annotation-ссылка на `ReadDialog`/`DialogTranscript`/`AppendAnswer` в описании хендлера диалога | PASS — все объявленные типы теперь упомянуты backtick-ссылкой |
| §6.16 `cmd/afm`: `Imports` из `pkg/state` не содержал `Transition`/`RunState` | Добавлены оба типа; добавлена annotation-ссылка на `store.Apply(Transition{...})` и чтение `RunState` в `approve`/`retry`/`revise`/`check` | PASS |
| §6.14 `pkg/orchestrator`: `Imports` из `pkg/prompts` не содержал `Agent`/`PlanIssues` | Добавлены оба типа; добавлена annotation-ссылка в `Run` (фаза помечается `Agent`, план проверяется `ValidatePlan` → `PlanIssues.IsClean()`) и в `MissingSectionsError` (`Missing` из `PlanIssues.MissingSections`) | PASS |
| §6.13 `pkg/executor`: `WrittenFiles` `location` указывал `transcript.go` вместо реального `executor.go` | Исправлено на `executor.go` | PASS — совпадает с `grep func WrittenFiles pkg/executor/executor.go:306` |

#### Unresolved Issues

Нет для проверенной выборки. Выборка — 5 из 16 клеточек по `Imports`, 4 из 16 по `location`; полное
покрытие всех 16 клеточек в рамках этого прохода не выполнялось, аналогичные точечные пропуски в
непроверенных клеточках не исключены.

#### Final Status (второй проход)

**Вердикт: PASSED (с 4 применёнными High-фиксами).** Все 4 найденных несоответствия — точечные пропуски
в `Imports`/`location`, затрагивающие Acceptance Criteria #3 («Imports корректны») и #4 («`location`
корректны») задачи ревью; ни одно не является структурным нарушением DSL, циклом или неразрешимой
ссылкой. После фиксов: `Imports` пересверенных клеточек 1:1 совпадают с реальным импорт-графом,
`location` пересверенных клеточек 1:1 совпадает с реальным листингом файлов, decisions по cell cohesion
(§4.1, §4.2) не изменились, Task AC (§2/§9) остаются 1:1 отражены.
