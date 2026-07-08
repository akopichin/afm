# [ARCHITECTURE_PLAN]

## Topic

Stage-level consumption accounting (tokens / cost / KB) with a dashboard panel. Source task: `docs/tasks/master.md`. Saved at `docs/arch/master.md` (topic `master` = current branch name, per the pipeline's `docs/arch/<topic>.md` convention).

## Implementation Order

Leaves → root, matching `goga schema`'s live dependency graph (verified: only `cmd/afm`/`pkg/server`/`pkg/orchestrator` depend on `pkg/state`; only `pkg/server`/`pkg/orchestrator` depend on `pkg/executor`; neither leaf depends on anything):

1. **`pkg/state`** (modify) — no Imports, one additive method (`Store.History`). Designed first because `pkg/accounting` needs it.
2. **`pkg/proxy`** (modify) — no Imports, self-contained usage capture. Independent of every other cell in this plan (subtask 1 is deliberately self-sufficient).
3. **`pkg/config`** (modify) — no Imports, self-contained `pricing:` section. Independent of every other cell in this plan.
4. **`pkg/accounting`** (**create new**) — imports `pkg/proxy` (`UsageRecord`), `pkg/config` (`PricingConfig`/`ModelPricing`), `pkg/state` (`Store`/`Transition`). Designed after all three because it consumes their types.
5. **`pkg/server`** (modify) — imports `pkg/accounting` (`Accountant`), in addition to its existing imports. Designed after `pkg/accounting` exists.
6. **`pkg/web/dashboard`** (modify) — zero Go imports (static bundle); designed last because its panel calls the `/api/usage` endpoint `pkg/server` exposes.
7. **`cmd/afm`** (modify) — a real cell (confirmed via `goga schema`, has its own `cmd/afm/CODEMANIFEST`), imports `Proxy`/`BuildTransforms`/`CreateShim` from `pkg/proxy` and `Server` from `pkg/server`. Designed last because it threads the `usageLogPath` arg added to `proxy.New` and constructs the `Accountant` passed into `server.Config`.

## Artifacts

### 1. `pkg/state` (modify — diff only, one new method on existing `Store` entity)

**Diff:** add one method to the existing `"Store()"` entity block, after `Snapshot`:

```yaml
    "History() -> transitions:[]Transition, err:error": |
      Возвращает полную историю переходов состояния, накопленную при Open (replay событий из
      events.jsonl). Read-only — использует уже реплеенный в памяти лог, не открывает файл повторно.

      Requirements:
      - Порядок транзакций — по возрастанию Seq, как в event log
      - `Seq` присваивается в том же порядке, в котором записывается `Time` (append-only event log) —
        возрастание `Seq` подряд подразумевает неубывание `Time`
```

No other change to `pkg/state/CODEMANIFEST` — header (Usages/Annotations), all other entities/methods, and the footer are unchanged.

No `.usages/` change needed — `store_facade.md`'s existing "Reading current state" section already documents the `Store` read-only API pattern (`Get`/`Snapshot`); `History` is a same-domain addition. *(Optional follow-up, not required for this plan: a design/implementation stage may add a one-line example for `History()` to that file.)*

### 2. `pkg/proxy` (modify)

**Full CODEMANIFEST:**

```yaml
Usages:
  conventions: .goga/usages/conventions.md
  golang: golang

Annotations: |
  Встроенный reverse-proxy агентского HTTP-трафика к Anthropic-совместимым шлюзам. `Transform` —
  точка расширения: Proxy.ServeHTTP диспетчеризует на первый подходящий трансформ по Match, иначе —
  passthrough без изменений. Инспектирует usage каждого ответа (все апстримы, SSE и JSON) через
  `ParseUsage`, независимо от применённого Transform, и пишет `UsageRecord` через `AppendUsageRecord`.

  Use `conventions` for code writing rules and testing. Use `golang` for implementation.

---

"Transform()":
  location: transform.go
  annotations: |
    Интерфейс обработки проксируемых HTTP-запросов для конкретного upstream.
  methods:
    "Match(upstreamURL string) -> matches:bool": |
      Применим ли этот трансформ к данному upstream URL.
    "ServeHTTP(w http.ResponseWriter, r http.Request, upstream string)": |
      Обрабатывает проксируемый запрос.
      `upstream`: провалидированный базовый URL upstream (например, https://api.z.ai/api/anthropic)

"BuildTransforms(upstream string, zai bool) -> transforms:[]Transform":
  location: zai.go
  annotations: |
    Возвращает упорядоченный список трансформов для указанного upstream.
    `zai`: nil = автоопределение по хосту (api.z.ai), true = всегда, false = никогда

    `zai`: tri-state — declared as bool here but the real Go parameter is *bool; nil (unset)
    means auto-detect by upstream host (api.z.ai), true forces the transform on, false forces it off.

"ZAITransform()":
  location: zai.go
  annotations: |
    Исправляет ошибки 529 от api.z.ai: конвертирует non-streaming запросы в streaming и разбирает
    SSE-ответ обратно в единый JSON message.

    Algorithm:
    1. Match — true для upstream URL, содержащих api.z.ai
    2. Для stream=true запросов — пропустить без изменений
    3. Для stream=false/отсутствующего — внедрить stream: true, переслать upstream
    4. Прочитать SSE-ответ; накопить message_start (id/role/model/usage), content_block_start +
       content_block_delta (text/thinking/tool_use/signature deltas) по индексу блока, message_delta
       (stop_reason + merge usage)
    5. Вернуть единый JSON message

    Constraints:
    - Upstream-ответы не-200 пересылаются как есть (статус + заголовки + тело)
    - SSE-событие error или пустой SSE-ответ дают HTTP 529 со структурированной ошибкой в стиле Anthropic

    Note: usage-захват для ответа этого трансформа не требует отдельной логики — Proxy.ServeHTTP
    оборачивает http.ResponseWriter уже переданный сюда как `w`, поэтому JSON, который этот метод
    пишет в `w`, перехватывается тем же универсальным механизмом (см. Proxy.ServeHTTP).
  methods:
    "Match(upstreamURL string) -> matches:bool": |
      true для upstream URL, содержащих api.z.ai.
    "ServeHTTP(w http.ResponseWriter, r http.Request, upstream string)": |
      См. Algorithm выше.

"Proxy(upstream string, transforms []Transform, usageLogPath string)":
  location: proxy.go
  annotations: |
    Reverse-proxy, применяющий первый подходящий `Transform` к каждому запросу. Запросы без
    подходящего трансформа передаются без изменений. Инспектирует usage каждого ответа независимо
    от Transform (см. ServeHTTP Algorithm).
  methods:
    "New(upstream string, transforms []Transform, usageLogPath string) -> proxy:Proxy": |
      Создаёт Proxy, пересылающий на upstream с указанными трансформами.
      `usageLogPath`: путь к usage.jsonl рана; пустая строка отключает запись usage (например, в тестах).
    "Start(port int) -> addr:string, err:error": |
      Слушает 127.0.0.1:port (0 = порт, назначаемый ОС), возвращает адрес как
      http://127.0.0.1:PORT. Сервер работает в фоне.
    "Addr() -> addr:string": |
      Адрес прослушивания (пусто до Start).
    "ServeHTTP(w http.ResponseWriter, r http.Request)": |
      Диспетчеризация на первый подходящий трансформ, либо passthrough.

      Algorithm (usage capture, применяется единообразно для любого пути — Transform или passthrough):
      1. Обернуть `w` внутренним неэкспортируемым tee-writer'ом: каждый `Write` немедленно уходит в
         оригинальный `w` (стриминг клиенту не блокируется) и одновременно копируется во внутренний буфер
      2. Выполнить диспетчеризацию как обычно (Transform.ServeHTTP или passthrough), передав обёрнутый writer
      3. По завершении обработчика вызвать `ParseUsage` с накопленным буфером и Content-Type ответа
      4. Собрать `UsageRecord` (добавив байты запроса/ответа), записать через `AppendUsageRecord` в
         `usageLogPath`

      Requirements:
      - Ошибки захвата/разбора/записи usage (`ParseUsage`/`AppendUsageRecord`) логируются и пропускаются —
        не изменяют и не блокируют уже отправленный клиенту ответ (байты форвардятся до разбора)
    "Shutdown(ctx string) -> err:error": |
      Плавно останавливает proxy-сервер.

"CreateShim(proxyAddr string) -> shimDir:string, err:error":
  location: shim.go
  annotations: |
    Создаёт временную директорию с shell-скриптом claude, переопределяющим ANTHROPIC_BASE_URL на
    `proxyAddr` перед вызовом настоящего claude. Директорию нужно добавить в начало PATH в
    окружении агента.

    Constraints:
    - Вызывающая сторона отвечает за defer os.RemoveAll(shimDir)
    - Возвращает ошибку, если claude не найден в текущем PATH

"UsageRecord(timestamp string, model string, inputTokens int, outputTokens int, cacheCreationTokens int, cacheReadTokens int, requestBytes int, responseBytes int)":
  location: usage.go
  annotations: |
    Данные об одном перехваченном запросе к апстриму: модель, токены (input/output/cache), объём
    запроса/ответа в байтах, момент запроса.

    Note: `Timestamp` — реальный Go-тип time.Time, сериализуется как RFC3339.
  properties:
    "Timestamp -> string": |
      Момент запроса (RFC3339).
    "Model -> string": |
      Имя модели upstream'а.
    "InputTokens -> int": |
      Входные токены.
    "OutputTokens -> int": |
      Выходные токены.
    "CacheCreationTokens -> int": |
      Токены создания кэша.
    "CacheReadTokens -> int": |
      Токены чтения из кэша.
    "RequestBytes -> int": |
      Размер тела запроса в байтах.
    "ResponseBytes -> int": |
      Размер тела ответа в байтах.

"ParseUsage(contentType string, body string) -> record:UsageRecord, err:error":
  location: usage.go
  annotations: |
    Разбирает тело проксируемого ответа в `UsageRecord`, обобщая существующую логику разбора SSE-
    ответов на все апстримы и оба режима ответа.
    `contentType`: заголовок Content-Type ответа
    `body`: накопленные байты тела ответа

    Algorithm:
    1. Если `contentType` содержит "text/event-stream" — разобрать `body` как SSE-поток (в стиле
       существующего SSE-парсинга): накопить usage из message_start/message_delta
    2. Иначе — распарсить `body` как единый JSON, извлечь `model` и `usage` напрямую (покрывает и
       non-streaming Anthropic-ответы, и переупакованный ZAITransform-ответ)

    Requirements:
    - Байты запроса/ответа добавляются в `UsageRecord` вызывающей стороной (Proxy.ServeHTTP), не
      этой функцией — `ParseUsage` заполняет только модель+токены

    Constraints:
    - Не-200 ответ или отсутствие поля usage — возвращает `err`, не строит нулевую запись

"AppendUsageRecord(path string, record UsageRecord) -> err:error":
  location: usage.go
  annotations: |
    Добавляет `record` как одну JSON-строку в файл `path` (например, .afm/runs/<run>/usage.jsonl).

    Algorithm:
    1. Открыть `path` в режиме добавления (создать при отсутствии)
    2. Сериализовать `record` в JSON, записать одну строку, закрыть файл

    Requirements:
    - Один вызов — одна операция open-write-close (без держания хендла между вызовами), см. `pkg/state`
      `SaveFeedback` для аналогичного паттерна

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Reverse-proxy агентского HTTP-трафика с точкой расширения Transform; ZAI SSE-реассемблинг под api.z.ai;
  захват usage (токены/KB) для всех ответов через ParseUsage/AppendUsageRecord.
```

**`.usages/` files:**

**File:** `pkg/proxy/.usages/usage_log.md` (**new**)
```md
Domain: consuming the per-run usage log written by the proxy's response inspection. Audience: `pkg/accounting`.

## File location and format

`.afm/runs/<run>/usage.jsonl` — one JSON object per line, written by `AppendUsageRecord` for every
proxied response that yielded a parseable `usage` field:

```json
{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
```

## Reading the log

```go
records, err := proxy.LoadUsageRecords(filepath.Join(runDir, "usage.jsonl"))
// missing file (proxy was never started this run) -> empty slice, nil error — not an error case
```

Records are written in request-completion order — no assumption of strict timestamp ordering across
concurrent requests (parallel subagents), sort by `Timestamp` if a strict order is required.

## Absence signals "no active proxy this run"

An empty or missing `usage.jsonl` means real-time per-request capture wasn't available for the run —
consumers should fall back to the jsonl `result`-event reader for tokens/cost, and simply have no KB
figure at all (KB is proxy-only, no fallback source exists).
```

**File:** `pkg/proxy/.usages/proxy_facade.md` (**modify** — one-line update to the `New` call)

Diff: change
```go
p := proxy.New(upstream, transforms)
```
to
```go
p := proxy.New(upstream, transforms, filepath.Join(runDir, "usage.jsonl")) // "" disables usage-log writing
```
Rest of the file (shim creation, threading `addr`/`shimDir` into `orchestrator.Options`) is unchanged.

### 3. `pkg/config` (modify)

**Full CODEMANIFEST:**

```yaml
Usages:
  conventions: .goga/usages/conventions.md
  golang: golang
  yaml_v3: .goga/usages/cooks/yaml-v3.md

Annotations: |
  Загрузка и мердж конфигурации afm (.afm/config.yaml, ~/.afm/config.yaml). Использует `yaml_v3` для
  парсинга — опциональность полей выражена указателем + методом-геттером с дефолтом (см. `yaml_v3`).
  `PricingConfig` следует тому же паттерну, что `ProxyConfig`/`ServerConfig`.

  Use `conventions` for code writing rules and testing. Use `golang` for implementation.

---

"Default() -> config:Config":
  location: config.go
  annotations: |
    Возвращает встроенную конфигурацию по умолчанию.

"LoadFrom(globalDir string, projectDir string) -> config:Config, err:error":
  location: config.go
  annotations: |
    Загружает и мержит конфигурацию из явных глобальной и проектной директорий.
    `globalDir`: путь к глобальному ~/.afm
    `projectDir`: путь к проектному .afm

    Requirements:
    - Отсутствующие файлы молча игнорируются, применяются дефолты
    - Использовать `yaml_v3` для парсинга YAML

"Config(client ClientConfig, executor ExecutorConfig, server ServerConfig, proxy ProxyConfig, docker DockerConfig, pricing PricingConfig, promptsDir string)":
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
    "Pricing -> PricingConfig": |
      Опциональная таблица цен по модели для дерайва стоимости потребления.
    "PromptsDir -> string": |
      Переопределение директории system-промптов.

"ClientConfig(command string, extraArgs []string)":
  location: config.go
  annotations: |
    Команда AI-клиента и дополнительные аргументы CLI.
  properties:
    "Command -> string": |
      Имя/путь исполняемого агента (например, claude).
    "ExtraArgs -> []string": |
      Дополнительные аргументы командной строки.

"ExecutorConfig(idleTimeout string, maxParallel int)":
  location: config.go
  annotations: |
    Параметры исполнения агентских процессов.
  properties:
    "IdleTimeout -> string": |
      Таймаут простоя агента (значение time.Duration, например 30m).
    "MaxParallel -> int": |
      Максимум параллельных агентов.

"ServerConfig(port int, openBrowser bool)":
  location: config.go
  annotations: |
    Настройки HTTP-дашборда. Port/OpenBrowser — опциональные (nil = не задано явно), геттеры
    возвращают дефолт.
  properties:
    "Port -> int": |
      Порт дашборда (nil → дефолт 9876, см. GetPort).
    "OpenBrowser -> bool": |
      Открывать браузер автоматически (nil → true, см. IsOpenBrowser).
  methods:
    "GetPort() -> port:int": |
      Возвращает Port, либо 9876, если не задан.
    "IsOpenBrowser() -> enabled:bool": |
      Возвращает OpenBrowser, либо true, если не задан.

"ProxyConfig(enabled bool, upstream string, port int, transforms TransformOverrides)":
  location: config.go
  annotations: |
    Настройки встроенного reverse-proxy для агентского HTTP-трафика.
  properties:
    "Enabled -> bool": |
      nil → включено по умолчанию (см. IsEnabled).
    "Upstream -> string": |
      Upstream-адрес (иначе читается из ANTHROPIC_BASE_URL).
    "Port -> int": |
      Порт proxy (0 = случайный свободный).
    "Transforms -> TransformOverrides": |
      Переопределения по конкретным трансформам.
  methods:
    "IsEnabled() -> enabled:bool": |
      true по умолчанию (nil Enabled → enabled).

"TransformOverrides(zai bool)":
  location: config.go
  annotations: |
    Управляет тем, какие proxy-трансформы применяются.
  properties:
    "ZAI -> bool": |
      nil = автоопределение по upstream-хосту (api.z.ai), true = всегда, false = никогда.

"DockerConfig(enabled bool, image string, extraMounts []string)":
  location: config.go
  annotations: |
    Настройки Docker-режима самоперезапуска.
  properties:
    "Enabled -> bool": |
      nil = смотрим AFM_USE_DOCKER.
    "Image -> string": |
      Docker-образ.
    "ExtraMounts -> []string": |
      Доп. хост-пути (можно с ~), пробрасываемые в контейнер read-only — для кастомных агентов,
      хранящих токены/конфиги вне ~/.claude.
  methods:
    "GetImage() -> image:string": |
      Образ, с приоритетом AFM_DOCKER_IMAGE.
    "IsDockerEnabled() -> enabled:bool": |
      true, если нужно использовать Docker-режим. AFM_IN_DOCKER=1 всегда даёт false (уже внутри
      контейнера).

"PricingConfig(models map[string]ModelPricing)":
  location: config.go
  annotations: |
    Опциональная таблица цен по модели. `Models` nil/пусто → метрика денег скрыта полностью (не
    показывается частично).
  properties:
    "Models -> map[string]ModelPricing": |
      Ставки по имени модели, ключ — точное имя модели.
  methods:
    "GetModelPricing(model string) -> pricing:ModelPricing, ok:bool": |
      Точный поиск по имени модели.

      Constraints:
      - Без fuzzy/prefix-подбора — неизвестное имя модели даёт ok:false, не ближайшее совпадение

"ModelPricing(inputPerMtok float64, outputPerMtok float64, cachePerMtok float64)":
  location: config.go
  annotations: |
    Цены модели в $ за миллион токенов, по категориям.
  properties:
    "InputPerMtok -> float64": |
      Цена за 1M входных токенов.
    "OutputPerMtok -> float64": |
      Цена за 1M выходных токенов.
    "CachePerMtok -> float64": |
      Цена за 1M кэш-токенов (cache read/write не различаются на уровне цены в этой версии).

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  Загрузка и мердж конфигурации afm из глобального и проектного YAML, включая опциональный pricing.
```

**`.usages/` files:**

**File:** `pkg/config/.usages/config_facade.md` (**modify** — add a snippet under "Reading optional fields safely")

Diff — append after the existing `image := cfg.Docker.GetImage()` line:
```go
pricing, ok := cfg.Pricing.GetModelPricing("claude-sonnet-5")
if !ok {
    // no pricing configured for this model (or the pricing: section is absent entirely) —
    // omit the cost metric, show tokens/KB only
}
```

### 4. `pkg/accounting` (**create new**)

**Full CODEMANIFEST:**

```yaml
Imports:
  - Types:
      - UsageRecord
    From: pkg/proxy
  - Types:
      - PricingConfig
      - ModelPricing
    From: pkg/config
  - Types:
      - Store
      - Transition
    Usages:
      - store_facade
    From: pkg/state

Usages:
  conventions: .goga/usages/conventions.md
  golang: golang

Annotations: |
  Аккаунтинг потребления ресурсов (токены/деньги/KB) по стейджам: комбинирует per-request `UsageRecord`
  (импорт pkg/proxy, real-time при активном прокси) с jsonl result-событиями (авторитетные токены +
  total_cost_usd, фолбэк без прокси) и атрибуцией по времени через историю переходов `Store` (метод
  History, импорт pkg/state).
  Стоимость дерайвится из `PricingConfig` (импорт pkg/config) — единый механизм, опционально.

  Use `store_facade` from Imports for reading stage transition history via `Store`.
  Use `conventions` for code writing rules and testing. Use `golang` for implementation.

---

"Accountant(runDir string, store Store, pricing PricingConfig)":
  location: accountant.go
  annotations: |
    Фасад запроса потребления для одного рана. Конструируется один раз, держит переданные `runDir`,
    `store` и `pricing` как внутреннее состояние (без экспортируемых свойств — аналогично `Proxy`/
    `Executor`).
  methods:
    "NewAccountant(runDir string, store Store, pricing PricingConfig) -> accountant:Accountant": |
      Конструктор.
    "Query(metric string, stage string) -> aggregates:[]UsageAggregate, err:error": |
      `metric`: одно из tokens|cost|kb.
      `stage`: пусто = все стейджи.

      Algorithm:
      1. `LoadUsageRecords` из `RunDir`/usage.jsonl (отсутствие файла → пустой список, не ошибка)
      2. `LoadStageWindows(store)`
      3. Для каждого stageID из `store.Snapshot().StageOrder` — `ReadResultUsage` по его
         phase.jsonl-файлам (planning/implementation/review)
      4. `Aggregate(records, windows, resultUsages, pricing, metric, stage)`

      Requirements:
      - metric=cost при пустом `pricing.Models` → пустой список результатов, не ошибка

      Constraints:
      - Не мутирует `Store`/events.jsonl — read-only потребитель

"DeriveCost(inputTokens int, outputTokens int, cacheTokens int, pricing ModelPricing) -> costUsd:float64":
  location: cost.go
  annotations: |
    Переводит токены в стоимость по ставкам `pricing`.
    Вызывающая сторона отвечает за получение `pricing` через `PricingConfig.GetModelPricing` заранее —
    эта функция не проверяет наличие цены.

    Algorithm:
    1. Умножить `inputTokens` на `InputPerMtok`, разделить на миллион (ставка задана за миллион токенов)
    2. То же для `outputTokens` на `OutputPerMtok` и для `cacheTokens` на `CachePerMtok`
    3. Просуммировать три величины в `costUsd`

"StageWindow(stageID string, start string, end string)":
  location: attribution.go
  annotations: |
    Временное окно выполнения стейджа.
  properties:
    "StageID -> string": |
      ID стейджа.
    "Start -> string": |
      Начало окна (RFC3339).
    "End -> string": |
      Конец окна (RFC3339). Пусто — стейдж ещё выполняется.

"LoadStageWindows(store Store) -> windows:[]StageWindow, err:error":
  location: attribution.go
  annotations: |
    Строит окна выполнения стейджей из истории переходов.

    Algorithm:
    1. Вызвать `store.History()` — получить полную историю `Transition`, отсортированную по
       возрастанию поля Time
    2. Для каждого stageID сопоставить первый переход в статус running со следующим по времени
       терминальным переходом (done либо failed) той же стадии → `StageWindow` (Start/End из их Time)
    3. Стейдж с переходом в running, но без последующего терминального перехода — `End`=""

"LoadUsageRecords(path string) -> records:[]UsageRecord, err:error":
  location: reader.go
  annotations: |
    Читает usage.jsonl (формат владеет pkg/proxy, см. `UsageRecord`) построчно.

    Constraints:
    - Отсутствующий файл → пустой список, err=nil (прокси не был активен этот ран — не ошибка)

"AttributeStage(record UsageRecord, windows []StageWindow) -> stageID:string, ok:bool":
  location: attribution.go
  annotations: |
    Сопоставляет `record`'s Timestamp с окном стейджа.

    Algorithm:
    1. Найти все `windows`, чей интервал [Start, End) (End пусто = не закрыт, считать открытым до
       текущего момента) содержит `record`'s Timestamp
    2. Ровно одно совпадение → вернуть его StageID, ok:true
    3. Ноль совпадений или более одного совпадения (перекрывающиеся окна) → ok:false

    Constraints:
    - Неоднозначное перекрытие окон (параллельные top-level стейджи) сознательно даёт ok:false —
      точный алгоритм разрешения уточняется на design-стадии, см. Risks в docs/tasks/master.md

"ReadResultUsage(jsonlPath string) -> usage:ResultUsage, ok:bool":
  location: reader.go
  annotations: |
    Читает терминальное result-событие из phase.jsonl (формат владеет pkg/executor — файловая
    конвенция, не импортируется как Go-тип: поля total_cost_usd, usage с вложенными input_tokens/
    output_tokens/cache_creation_input_tokens/cache_read_input_tokens, session_id, duration_ms).

    Algorithm:
    1. Прочитать `jsonlPath` построчно, найти последнюю строку с полем type равным "result"
    2. Разобрать её поля в `ResultUsage`

    Constraints:
    - ok:false — файл отсутствует, нечитаем, либо не содержит result-события (аналогично
      pkg/executor.WrittenFiles' "отсутствующий лог → пусто")

"ResultUsage(stageID string, inputTokens int, outputTokens int, cacheCreationTokens int, cacheReadTokens int, totalCostUsd float64, durationMs int, sessionID string)":
  location: reader.go
  annotations: |
    Авторитетные токены + стоимость + метаданные из result-события одной фазы стейджа.
  properties:
    "StageID -> string": |
      ID стейджа, к которому относится эта фаза.
    "InputTokens -> int": |
      Входные токены (авторитетные, из клиента).
    "OutputTokens -> int": |
      Выходные токены.
    "CacheCreationTokens -> int": |
      Токены создания кэша.
    "CacheReadTokens -> int": |
      Токены чтения из кэша.
    "TotalCostUsd -> float64": |
      Стоимость, посчитанная клиентом (используется только для кросс-чека, не как отображаемая метрика).
    "DurationMs -> int": |
      Длительность фазы в миллисекундах.
    "SessionID -> string": |
      ID сессии клиента.

"Aggregate(records []UsageRecord, windows []StageWindow, resultUsages []ResultUsage, pricing PricingConfig, metric string, stage string) -> aggregates:[]UsageAggregate":
  location: aggregate.go
  annotations: |
    Строит агрегаты потребления.

    Algorithm:
    1. Атрибутировать каждый `record` через `AttributeStage`; отбросить неатрибутированные
    2. Отфильтровать по `stage` (пусто = все стейджи)
    3. Если у стейджа нет proxy-записей (нет активного прокси этот ран) — использовать
       соответствующие `resultUsages` как fallback-источник токенов (metric=tokens только: `ResultUsage`
       не содержит `Model`, поэтому `pricing.GetModelPricing` для него невозможен — при metric=cost
       такой стейдж не попадает в агрегат, `TotalCostUsd` не используется как источник отображаемой
       стоимости, см. Constraints)
    4. При metric=cost получить `ModelPricing` через `pricing.GetModelPricing(record.Model)`; при
       ok=false — пропустить запись (не включать в агрегат)
    5. Сгруппировать по стейджу и временному бакету, просуммировать значения выбранной метрики

    Constraints:
    - `TotalCostUsd` из `ResultUsage` никогда не суммируется в `UsageAggregate` — единственный
      источник отображаемой стоимости — дерайв из токенов через `PricingConfig` (см. Description задачи,
      docs/tasks/master.md)

"UsageAggregate(stageID string, timeBucket string, metric string, value float64)":
  location: aggregate.go
  annotations: |
    Одна строка агрегата: стейдж × временной бакет × метрика → значение.
  properties:
    "StageID -> string": |
      ID стейджа.
    "TimeBucket -> string": |
      Начало временного бакета (RFC3339).
    "Metric -> string": |
      Одно из tokens|cost|kb.
    "Value -> float64": |
      Значение метрики за бакет.

---

Author: Goga
CreatedAt: 07/07/26
Description: |
  Аккаунтинг потребления ресурсов (токены/деньги/KB) по стейджам: атрибуция, дерайв стоимости,
  агрегация по стейджу/времени/метрике.
```

**`.usages/` files:**

**File:** `pkg/accounting/.usages/query_usage.md` (**new**)
```md
Domain: querying stage-level consumption aggregates. Audience: `pkg/server`.

## Constructing an Accountant

```go
accountant := accounting.NewAccountant(runDir, store, cfg.Pricing)
// store: the same *state.Store instance pkg/server already holds in its Config
// cfg.Pricing: zero-value PricingConfig is fine — Query("cost", ...) just returns an empty slice
```

## Querying aggregates

```go
aggregates, err := accountant.Query("tokens", "") // all stages, tokens metric
if err != nil {
    return err
}

aggregates, err = accountant.Query("cost", "design") // one stage, cost metric
// empty aggregates + nil err is a valid, expected result when no pricing: config is set —
// callers must not treat an empty slice as an error
```

## Handling the "pricing not configured" case

```go
aggregates, _ := accountant.Query("cost", "")
if len(aggregates) == 0 {
    // hide the money toggle in the API response / let the dashboard hide it client-side
}
```
```

### 5. `pkg/server` (modify)

**Full CODEMANIFEST:**

```yaml
Imports:
  - Types:
      - UIBus
      - Event
    From: pkg/orchestrator
  - Types:
      - Store
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
  - Types:
      - Accountant
    Usages:
      - query_usage
    From: pkg/accounting

Usages:
  conventions: .goga/usages/conventions.md
  golang: golang
  gorilla_websocket: .goga/usages/cooks/gorilla-websocket.md

Annotations: |
  HTTP + WebSocket дашборд, отдающий события оркестратора в UI, встраивает pkg/web. WebSocket-стриминг
  событий использует `gorilla_websocket`.

  Хендлер диалога (/api/stages/<id>/dialog) собирает UI-ленту вопросов/ответов, комбинируя
  ReadDialog -> []`Entry` (импорт pkg/mcp) с DialogTranscript -> []`TranscriptItem` (импорт
  pkg/executor) для сопоставления записей диалога с текстом ассистента; принимает ответ пользователя
  через AppendAnswer (`Answer`, импорт pkg/mcp).

  Use `query_usage` from Imports for querying consumption aggregates via `Accountant`.
  Use `conventions` for code writing rules and testing. Use `golang` for implementation.

---

"Config(port int, runDir string, store Store, uiBus UIBus, approveFn string, reviseFn string, retryFn string, dialogAnswerFn string, dialogCancelFn string, accountant Accountant)":
  location: server.go
  annotations: |
    Настройки сервера.
  properties:
    "Port -> int": |
      Порт HTTP-сервера.
    "RunDir -> string": |
      Директория рана.
    "Store -> Store": |
      Хранилище состояния (импорт pkg/state).
    "UIBus -> UIBus": |
      Событийная шина для WebSocket-стриминга (импорт pkg/orchestrator).
    "ApproveFn -> string": |
      Колбэк утверждения плана стадии.

      Сигнатура: func(ctx context.Context, stageID string) error.
    "ReviseFn -> string": |
      Колбэк отправки фидбека ревизии.

      Сигнатура: func(ctx context.Context, stageID string, feedback string) error.
    "RetryFn -> string": |
      Колбэк повтора проваленной стадии.

      Сигнатура: func(ctx context.Context, stageID string) error.
    "DialogAnswerFn -> string": |
      Колбэк приёма ответа диалога (file-based dialog protocol, см. pkg/mcp, pkg/orchestrator).

      Сигнатура: func(stageID string, phase string, qID string, answer string, fromOptions bool) error.
    "DialogCancelFn -> string": |
      Колбэк отмены диалога.

      Сигнатура: func(stageID string) error.
    "Accountant -> Accountant": |
      Фасад запроса потребления ресурсов (импорт pkg/accounting), используется хендлером /api/usage.

"Server(cfg Config)":
  location: server.go
  annotations: |
    HTTP-сервер дашборда и API.

    Algorithm (WebSocket-стриминг, см. `gorilla_websocket`):
    1. websocket.Upgrader создаётся один раз на пакет, CheckOrigin разрешает любой источник
       (локальный дашборд)
    2. Обработчик апгрейдит соединение, подписывается на `UIBus` (Subscribe)
    3. Пишет каждое `Event` как JSON-текстовое сообщение, пока подписка открыта
    4. При превышении буфера подписчика отслеживается SubscriberDroppedCount

    Requirements:
    - Ответ HTTP-хендлера диалога (/api/stages/<id>/dialog/answer) записывает <phase>.<id>.answer.json
      атомарно (эксклюзивное создание, O_EXCL) — критический путь, см. pkg/mcp
    - Хендлеры approve/revise/retry/dialog-cancel проверяют текущий StageStatus стадии перед действием
      через Store.Snapshot (approve/revise допустимы только при StageStatus = awaiting_approval,
      retry — только при failed, dialog/cancel — только при awaiting_user_input). Хендлер ответа на
      диалог (dialog/answer) StageStatus НЕ проверяет — он гейтится наличием файла
      <phase>.<id>.question.json (404, если файла нет) и отклоняет повторный ответ через O_EXCL
      (409 Conflict, если answer.json уже существует)
    - Хендлер ответа на диалог валидирует ответ против options вопроса, если allow_custom=false
      (по умолчанию allow_custom=true, если поле не задано): пустой options при allow_custom=false —
      400; ответ вне списка options — 400
    - Корень (/) отдаётся статикой из `FS` (импорт pkg/web) через http.FileServer
    - GET /api/usage?metric=tokens|cost|kb&stage=<id> вызывает `UsageHandler(cfg.Accountant)`;
      отсутствие секции pricing в конфиге — не ошибка: `Accountant.Query` возвращает пустой список
      агрегатов для metric=cost, хендлер отвечает 200 с пустым списком, не ошибкой
  methods:
    "New(cfg Config) -> server:Server": |
      Создаёт сервер.
    "Start() -> addr:string, err:error": |
      Запускает HTTP-сервер, возвращает фактический адрес.
    "Handler() -> handler:http.Handler": |
      Возвращает HTTP-обработчик (для тестов).
    "Shutdown(ctx string) -> err:error": |
      Плавно останавливает сервер.

"UsageHandler(accountant Accountant) -> handler:http.Handler":
  location: usage_handler.go
  annotations: |
    Фабрика HTTP-обработчика для /api/usage.

    Algorithm:
    1. Разобрать query-параметры `metric` (дефолт "tokens") и `stage` (дефолт "" = все стейджи)
    2. Валидировать `metric` ∈ {tokens, cost, kb}; иначе — 400
    3. Вызвать `accountant.Query(metric, stage)`
    4. При ошибке — 500; иначе — 200 и JSON-массив `UsageAggregate`

---

Author: Goga
CreatedAt: 03/07/26
Description: |
  HTTP + WebSocket дашборд afm: API стадий, дозвод/ревизия/ретрай, диалог, стриминг событий,
  агрегаты потребления ресурсов.
```

**`.usages/` files:**

**File:** `pkg/server/.usages/server_facade.md` (**modify** — add `Accountant` to the `server.New` example)

Diff — change:
```go
srv := server.New(server.Config{
    Port: cfg.Server.GetPort(), RunDir: runDir, Store: store, UIBus: o.UIBus(),
    ApproveFn:      o.Approve,
    ReviseFn:       o.Revise,
    RetryFn:        o.Retry,
    DialogAnswerFn: o.NotifyAnswer,
    DialogCancelFn: cancelFn,
})
```
to:
```go
srv := server.New(server.Config{
    Port: cfg.Server.GetPort(), RunDir: runDir, Store: store, UIBus: o.UIBus(),
    ApproveFn:      o.Approve,
    ReviseFn:       o.Revise,
    RetryFn:        o.Retry,
    DialogAnswerFn: o.NotifyAnswer,
    DialogCancelFn: cancelFn,
    Accountant:     accounting.NewAccountant(runDir, store, cfg.Pricing),
})
```
Rest of the file (the callback-coupling note) is unchanged.

### 6. `pkg/web/dashboard` (modify)

**Diff** to the existing `"DashboardAssets()"` entity's `annotations` Requirements list — add one bullet after the existing 5:

```yaml
    - app.js добавляет панель потребления: выезжающая слева-направо панель по кнопке-стрелке, свич
      метрик (токены/деньги/KB — опция "деньги" скрывается на клиенте, если /api/usage?metric=cost
      возвращает пустой массив), фильтр по стейджам (из существующего списка стейджей), и
      hand-rolled SVG-график временного ряда; без новой сторонней JS-библиотеки (markdown-it.min.js
      остаётся единственным исключением)
```

No other change to header, other entities, or footer. No `.usages/` file change (`dashboard_assets.md` already states new files in the directory are automatically part of the embedded bundle; the panel is additions to existing files, not new files).

### 7. `cmd/afm` (modify — diff only, one annotation update on the existing `newRunCmd` routine)

**Diff:** append one sentence to the existing `"newRunCmd()"` routine's `annotations`, after the existing "Algorithm (built-in reverse proxy...)" block's step 2 (`CreateShim` error handling), before step 3 (defer Shutdown):

```yaml
    2b. Proxy строится с новым usageLogPath-аргументом (filepath.Join(runDir, "usage.jsonl")) — см.
        pkg/proxy `New`; после старта Proxy конструируется `Accountant` (accounting.NewAccountant,
        импорт нового pkg/accounting) из runDir/store/cfg.Pricing и передаётся в server.Config —
        см. pkg/server `server_facade`
```

Also add `Accountant`/`NewAccountant` to the existing `Imports` block's `pkg/accounting`-sourced entry (new — `cmd/afm` did not previously import `pkg/accounting`):

```yaml
  - Types:
      - Accountant
    Usages:
      - query_usage
    From: pkg/accounting
```

No other change to header, other entities, or footer.

## Dependency Map

```
pkg/state ────────────(Store, Transition)──────────────┐
pkg/proxy ────────────(UsageRecord)─────────────────────┼──▶ pkg/accounting ──(Accountant)──▶ pkg/server
pkg/config ───────────(PricingConfig, ModelPricing)─────┘         │        ▲                     ▲
                                                                   │        │                     │
                                                                   └────────┴──── cmd/afm ─────────┘
                                                                        (Accountant; also Proxy.New
                                                                         3-arg signature change)
pkg/web/dashboard ───────────────(HTTP call to /api/usage, not a Go import)────────────────────────┘
```

Textual form:

| Source | Imported types | Target |
|---|---|---|
| `pkg/state` | `Store`, `Transition` | `pkg/accounting` |
| `pkg/proxy` | `UsageRecord` | `pkg/accounting` |
| `pkg/config` | `PricingConfig`, `ModelPricing` | `pkg/accounting` |
| `pkg/accounting` | `Accountant` | `pkg/server` |
| `pkg/accounting` | `Accountant` | `cmd/afm` (new edge — `cmd/afm` did not previously import `pkg/accounting`) |

No circular dependency: `pkg/state`, `pkg/proxy`, `pkg/config` remain leaves (zero new incoming edges beyond `pkg/accounting`, which itself is never imported by any of them). `pkg/accounting` never imports `pkg/orchestrator` or `pkg/server`. `pkg/web/dashboard` gains zero new Go edges. `cmd/afm` was already a dependent of `pkg/proxy`/`pkg/config`/`pkg/server`/`pkg/state` (per `goga schema --depends-on`); it gains one new edge (`pkg/accounting`) and must update its existing `proxy.New(...)` call site for the added `usageLogPath` argument (breaking signature change, see `cmd/afm` artifact diff above).

## Acceptance Criteria Mapping

Traces every Acceptance Criterion from `docs/tasks/master.md` to the cell/type responsible:

| Criterion (`docs/tasks/master.md`) | Covered by |
|---|---|
| All-models capture, ≥2 upstreams | `pkg/proxy`'s uniform `ParseUsage` + tee-capture wrapping in `Proxy.ServeHTTP`, applied regardless of which `Transform` (or none) handles the request |
| Passthrough also captured, not only `ZAITransform` | Same tee-capture wraps the `http.ResponseWriter` passed to *every* dispatch path (`Transform.ServeHTTP` or `passthroughTo`) uniformly |
| Stage attribution; dashboard stage-filter | `pkg/accounting`'s `LoadStageWindows`/`AttributeStage`/`Aggregate`; `stage` query param on `pkg/server`'s `UsageHandler`/`/api/usage`; stage filter in the `pkg/web/dashboard` panel |
| Pricing optional — no config hides money metric | `PricingConfig.Models` nil/empty → `Accountant.Query("cost", ...)` returns an empty `[]UsageAggregate`; dashboard hides the money toggle when that response is empty |
| Metric switch (tokens/money/KB incl. chart axes) | `UsageAggregate.Metric` field threads the selected metric end-to-end from `Aggregate` through `UsageHandler` to the dashboard panel's switch |
| Time-series charts with stage filter | `UsageAggregate.TimeBucket` + hand-rolled SVG chart in the `pkg/web/dashboard` panel, filtered via the same `stage` param |
| New `/api/usage` endpoint; old endpoints unbroken | `UsageHandler` registered as an additive route; no existing `pkg/server.Config` field or handler signature removed or changed, only added to |
| Clean build/vet/tests/lint, no Go downgrade | No new external dependency introduced anywhere in this plan; every new signature follows `goga-cell-go`'s allowed-type list (verified in Phase 10) |

## Verification Checklist

- [ ] `goga lint` reports 0 errors on all 7 touched/created CODEMANIFESTs (including `cmd/afm`).
- [ ] `goga schema` shows `pkg/accounting` as a new cell depending only on `pkg/proxy`, `pkg/config`, `pkg/state` — and confirms no cell among `pkg/state`/`pkg/proxy`/`pkg/config` gained a new dependency (they must remain leaves).
- [ ] `goga schema --depends-on pkg/orchestrator` does **not** list `pkg/accounting` (the forbidden cross-import never happens).
- [ ] Every new/changed Go signature in this plan uses only `string`/`int`/`float64`/`bool`/`error`/`[]T`/`map[string]T` (or a precedented stdlib exception like `http.ResponseWriter`/`http.Handler`) — no pointers/`interface{}`/variadics introduced, per `goga-cell-go`.
- [ ] `pkg/state/CODEMANIFEST`'s new `History()` method is additive only — no existing `Store` method signature changed.
- [ ] `pkg/proxy.New`'s signature change (`usageLogPath` added) is reflected everywhere `proxy.New` is called — `cmd/afm/run.go` is the only production call site, updated per this plan's `cmd/afm` CODEMANIFEST diff and the updated `pkg/proxy/.usages/proxy_facade.md`.
- [ ] `pkg/config.Config`'s new `Pricing` field does not break existing YAML unmarshaling of configs without a `pricing:` section (nil map is the expected default, per `yaml_v3` convention already used by every other optional section).
- [ ] `pkg/server.Config`'s new `Accountant` field is threaded through the one production call site (`cmd/afm/run.go`, per this plan's `cmd/afm` CODEMANIFEST diff and the updated `server_facade.md`) and any test helper that constructs `server.Config` directly.
- [ ] `handlers_test.go` (existing `pkg/server` tests) still passes unmodified — the new `/api/usage` route and `Config.Accountant` field are purely additive.
- [ ] Each of the 8 Acceptance Criteria in `docs/tasks/master.md` maps to a concrete artifact in this plan (see the Acceptance Criteria Mapping section above).
- [ ] Explicitly-deferred design questions (parallel-stage attribution algorithm, `usage.jsonl` rotation policy, on-demand-vs-cached aggregation, chart-library-vs-hand-rolled) are carried forward into the design stage — not silently dropped.
