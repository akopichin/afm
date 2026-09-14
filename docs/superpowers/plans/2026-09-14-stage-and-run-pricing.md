# План реализации: токены и стоимость по стадиям и run

**Цель:** после `afm run` показывать воспроизводимую оценку стоимости и раздельные токены (обычный input, cache read, cache write, output) для каждой стадии и всего run, включая Claude, GLM 5.3, Codex и внутренний memory pipeline.

**Главное архитектурное правило:** новый `pkg/accounting` владеет парсингом vendor usage, нормализацией токенов, тарифами, денежной арифметикой, агрегацией и форматированием. `executor`, `state`, CLI, server и frontend не складывают токены и не умножают их на цены — они только передают наблюдения в `accounting` и отображают готовые summary.

**Хранилище:** FSM reliability-core не меняется: `.afm/runs/<run>/events.jsonl` остаётся логом только переходов состояния. Accounting получает собственный append-only `.afm/runs/<run>/usage.jsonl`, которым целиком владеет `pkg/accounting` (append/fsync, replay, torn-tail recovery). В каждой usage-записи сохраняются нормализованные токены и снимок применённого тарифа, поэтому повторный `check/report` не зависит от текущей версии встроенного прайса.

**Стек:** Go, Claude stream-json, Codex JSONL, OpenAI Chat Completions SSE, React/TypeScript, Cobra, Vitest, `go test -race`.

## Что выяснено живым прогоном

14 сентября 2026 года был запущен реальный planning-only flow с тремя параллельными стадиями: `claude`, `glm53`, `codex`. Артефакты прогона находятся в игнорируемом каталоге `tmp/pricing-live/`, run — `pricing-live-20260914-141833-5a09`.

### Claude

Фактическая модель: `claude-opus-4-8`. Надёжный итог находится в terminal event `type=result`, а не в промежуточных `assistant` events: один и тот же `message.id` приходит несколько раз и его usage нельзя суммировать.

Итог реального вызова:

```text
input_tokens                    2
cache_creation_input_tokens    18 551
  ephemeral_1h_input_tokens    18 551
  ephemeral_5m_input_tokens    0
cache_read_input_tokens        15 940
output_tokens                  345
total_cost_usd                 0.202115
```

По официальному тарифу Opus 4.8 ($5 input, $10 cache write 1h, $0.50 cache read, $25 output за 1M tokens) локальный расчёт даёт ровно `$0.202115`. Следовательно, для настоящего Claude terminal result можно использовать как authoritative usage и как контрольный `reported_cost`, но показываемая стоимость всё равно считается нашим пакетом.

Источник тарифов: [Anthropic pricing](https://platform.claude.com/docs/en/about-claude/pricing).

### GLM 5.3 через Claude-compatible transport

Фактическая модель: `glm-5.3`. Terminal result сообщил:

```text
input_tokens                    48 583
cache_creation_input_tokens    0
cache_read_input_tokens        47 616
output_tokens                  1 130
total_cost_usd                 0.294973
```

`$0.294973` в точности получается по тарифу Claude Opus, а не GLM. По тарифу Z.AI GLM 5.3 ($1.40 input, $0.26 cached input, $4.40 output за 1M) оценка этих же токенов — `$0.08536836`. Значит, `total_cost_usd` Claude CLI нельзя считать достоверным для custom provider/model. В ledger он сохраняется только как `reported_cost_usd`; UI, `check` и `report` используют исключительно `estimated_cost_usd`, рассчитанный `pkg/accounting` по фактическому model id.

Источники: [Z.AI pricing](https://docs.z.ai/guides/overview/pricing), [Z.AI cache semantics](https://docs.z.ai/guides/capabilities/cache).

### Codex

Текущий `scripts/codex-as-claude.sh` теряет usage: он преобразует ответ в synthetic Claude result без model/usage. Реальный `codex exec --json --ephemeral --sandbox read-only` завершился публичным событием:

```json
{
  "type": "turn.completed",
  "usage": {
    "input_tokens": 22005,
    "cached_input_tokens": 10752,
    "cache_write_input_tokens": 0,
    "output_tokens": 8,
    "reasoning_output_tokens": 0
  }
}
```

В OpenAI/Codex schema `input_tokens` уже включает cached input. Поэтому нормализация такая:

```text
uncached_input = max(input_tokens - cached_input_tokens - cache_write_input_tokens, 0)
```

`reasoning_output_tokens` — информационное подмножество `output_tokens`, повторно к total и cost не прибавляется. Публичный `turn.completed` не содержит model; adapter получает его в порядке: явно заданный `CODEX_MODEL` → безопасное top-level поле `model` из `$CODEX_HOME/config.toml`/`~/.codex/config.toml` → пустое значение и статус `unpriced`. Читать внутренние rollout/session JSONL для учёта нельзя.

Источники: [Codex non-interactive JSONL](https://developers.openai.com/codex/noninteractive), [GPT-5.6-Codex pricing](https://developers.openai.com/api/docs/models/gpt-5.6-sol), [Codex/Work rate card](https://help.openai.com/en/articles/20001415).

### Вывод для продукта

- Usage берём из terminal result каждого процесса, а не из промежуточных assistant messages.
- Vendor-reported cost никогда не является нашим total: он диагностический и может быть рассчитан не для той модели.
- Cost называется **Estimated cost / list-price estimate**, потому что Claude/Codex subscription, credits, negotiated pricing и gateway markup не видны AFM.
- Нулевая стоимость допустима только при явном нулевом тарифе. Неизвестный model/rate — `unpriced`, отсутствующий usage — `unmetered`; оба состояния видимы пользователю.

## Целевая модель данных

### Нормализованные токены

```go
type Tokens struct {
    UncachedInput    uint64 `json:"uncached_input"`
    CacheRead        uint64 `json:"cache_read"`
    CacheWrite5m     uint64 `json:"cache_write_5m"`
    CacheWrite1h     uint64 `json:"cache_write_1h"`
    CacheWriteOther  uint64 `json:"cache_write_other"`
    Output           uint64 `json:"output"`
    ReasoningOutput  uint64 `json:"reasoning_output,omitempty"` // subset of Output
}
```

Все производные значения (`InputTotal`, `CacheWriteTotal`, `Total`, `CacheHitRatio`) — методы `pkg/accounting`, не сохранённые дубли.

- Anthropic schema: `input_tokens` — uncached; cache read/create добавляются отдельно. `cache_creation` разбивается на 5m/1h, остаток попадает в `CacheWriteOther`.
- OpenAI/Codex schema: cached/write вычитаются из inclusive `input_tokens`; underflow не маскируется — значение clamp'ится к нулю и observation получает warning.
- OpenAI Chat Completions: `prompt_tokens` — inclusive, `prompt_tokens_details.cached_tokens`/`cache_write_tokens` — его подмножества; `completion_tokens` включает reasoning.
- Reasoning tokens отображаются в деталях, но никогда не суммируются поверх output.

### Деньги и тариф

Для v1 `pkg/accounting` использует `float64` rates/cost в `USD / 1M tokens`: это list-price estimate, а не платёжный ledger. Округление выполняется только в presentation formatter; сырое значение в `usage.jsonl` сохраняется с достаточной JSON-точностью. Денежные операции и форматирование всё равно остаются внутри accounting, поэтому при появлении реальной необходимости fixed-point можно заменить без изменений в consumers.

```go
type RateCard struct {
    Input          Rate `yaml:"input"`
    CacheRead      Rate `yaml:"cache_read"`
    CacheWrite     Rate `yaml:"cache_write"`     // fallback для unknown TTL
    CacheWrite5m   Rate `yaml:"cache_write_5m"`
    CacheWrite1h   Rate `yaml:"cache_write_1h"`
    Output         Rate `yaml:"output"`
}
```

Resolver использует небольшой, но необходимый precedence:

1. config override для `channel/model`;
2. config override для `model`;
3. встроенный тариф для `channel/model`;
4. встроенный тариф для `model`;
5. `unpriced` с причиной `unknown_model` или `incomplete_rate`.

Ось `channel` не откладывается: уже сейчас один `gpt-5.6-sol` может прийти через Codex/Work (cache write не тарифицируется) или OpenAI API (другая billing policy). Это наблюдаемый текущий случай, а не гипотетическое расширение. При этом config остаётся простым: обычный model override и, только при необходимости, более точный channel/model override. Тарифы в record сохраняются целиком вместе с `pricing_source`, `pricing_as_of` и resolved key.

Минимальные проверяемые built-ins первой версии:

| key | input | cache read | cache write 5m | cache write 1h | output |
|---|---:|---:|---:|---:|---:|
| `claude-opus-4-8` | 5.00 | 0.50 | 6.25 | 10.00 | 25.00 |
| `glm-5.3` | 1.40 | 0.26 | 0.00¹ | 0.00¹ | 4.40 |
| `codex/gpt-5.6-sol` | 4.00 | 0.40 | 0.00² | 0.00² | 20.00 |

¹ Только пока официальный Z.AI pricing помечает cache storage как limited-time free; `pricing_as_of` делает это условие явным. ² Для Codex/Work list-price estimate; API-вызов той же модели должен использовать отдельный `openai-api/...` key/override.

Встроенная таблица не пытается угадывать все модели. Неизвестные или внутренние model ids требуют override; это безопаснее, чем молча применить тариф похожего имени. Alias допустим только явный и покрытый тестом (например, provider добавил date suffix к тому же публичному SKU).

### Usage record в отдельном `usage.jsonl`

```json
{
  "record_version": 1,
  "started_at": "2026-09-14T14:20:01.000Z",
  "finished_at": "2026-09-14T14:20:31.123Z",
  "stage_id": "backend",
  "phase": "implementation",
  "channel": "claude-cli",
  "schema": "anthropic",
  "model": "glm-5.3",
  "metered": true,
  "priced": true,
  "tokens": {
    "uncached_input": 48583,
    "cache_read": 47616,
    "cache_write_5m": 0,
    "cache_write_1h": 0,
    "cache_write_other": 0,
    "output": 1130
  },
  "rate": {
    "key": "glm-5.3",
    "source": "builtin",
    "as_of": "2026-09-14",
    "input": "1.40",
    "cache_read": "0.26",
    "cache_write_5m": "0",
    "cache_write_1h": "0",
    "output": "4.40"
  },
  "estimated_cost_usd": 0.08536836,
  "reported_cost_usd": "0.294973",
  "warnings": ["reported_cost_differs_from_estimate"]
}
```

Одна строка — один process invocation. Retry, Continue/restart, Revise и каждый ответ интерактивной стадии создают новую строку; их можно посчитать и упорядочить по `started_at`, но v1 не хранит отдельный mutable `attempt` counter. Это убирает состояние и реконструкцию нумерации из replay, не теряя stage/phase totals или coverage.

Если процесс завершился без terminal usage, всё равно записывается `usage` record с `metered:false`, model/channel hint и причиной (`missing_terminal_usage`, `unsupported_provider`, `interrupted_before_usage`). Это даёт честный coverage и не превращает неизвестную стоимость в `$0`. Битый synthetic envelope также становится `unmetered` с parse warning; ошибка учёта не маскирует исходный exit status агента.

Для run-level memory шагов `stage_id` пустой, а `scope` равен `run_overhead`; они входят в total и отдельную строку report. `reflect` привязывается к исходной стадии. Script hooks и обычные `script:` stages не являются LLM-вызовами и usage records не создают.

## Конфигурация

```yaml
pricing:
  models:
    glm-5.3:
      input: 1.40
      cache_read: 0.26
      cache_write: 0
      output: 4.40
  channels:
    codex:
      gpt-5.6-sol:
        input: 4.00
        cache_read: 0.40
        cache_write: 0
        output: 20.00
```

Rate fields — decimal scalar/string в USD/1M tokens. Поля override представлены pointers/optional values: явный `0` отличается от отсутствующего поля и действительно отключает стоимость категории. Частичный override накладывается на built-in; для неизвестной модели все реально встреченные категории должны иметь тариф, иначе invocation остаётся `unpriced` целиком, без misleading partial total.

Секретов и сетевого обновления тарифов нет. Built-ins версионированы в бинарнике; изменение тарифов — обычный PR с source URL, `as_of` и fixture. Это обеспечивает offline run и воспроизводимость.

## Инкременты поставки

Исходная оценка roadmap в 2–3 дня реалистична только для тонкого первого среза, а не для всех поверхностей и provider edge cases. Реализацию следует вести двумя independently shippable инкрементами:

1. **Core/launch number:** Tasks 1–5, обычные stage runners из Task 6, `afm check` из Task 7 и `afm report` из Task 8. Результат уже даёт durable per-stage/run cost для Claude, GLM и Codex без изменений FSM-core.
2. **Полнота пункта 5:** memory attribution из Task 6, server read model, dashboard, документация и полный live acceptance. Этот срез нельзя считать необязательным v2: пользователь запросил стоимость по стадиям и общую, а roadmap явно требует dashboard; разбивка нужна лишь для снижения размера одного merge/риска внедрения.

Fixed-point money, explicit attempt numbers и time-series charts остаются за пределами обоих инкрементов до появления реального требования.

## План работ

### Task 1. Ядро `pkg/accounting`: типы, normalizer, деньги и агрегаты

**Файлы:**

- Create: `pkg/accounting/types.go`
- Create: `pkg/accounting/cost.go`
- Create: `pkg/accounting/collector.go`
- Create: `pkg/accounting/pricing.go`
- Create: `pkg/accounting/summary.go`
- Test: `pkg/accounting/*_test.go`
- Test fixtures: `pkg/accounting/testdata/{claude-opus-4-8,glm-5.3,codex-gpt-5.6-sol,openai-chat}.jsonl`

- [ ] Добавить `Schema`, `SourceHint`, raw vendor DTO, `Tokens`, `Observation`, `UsageRecord`, `Summary`, `Coverage` и `RateCard` без импорта `config`, `state`, `server` или `flow`.
- [ ] Реализовать `Collector.Observe(line []byte)` и `Collector.Finish(exitErr error) Observation`: terminal result authoritative; промежуточный assistant usage используется только для model discovery, но не суммируется.
- [ ] Для synthetic OpenAI adapter result разрешить массив `upstream_usages`: каждый upstream response нормализуется отдельно, затем `accounting` агрегирует. Shell scripts не складывают токены.
- [ ] Реализовать Anthropic/OpenAI/OpenAI-chat normalizers и warning при inconsistent inclusive counters.
- [ ] Реализовать `float64` cost calculation в USD/1M tokens, formatter (`$0.0854`, `<$0.0001`, `—`) и reconciliation reported/estimated cost с небольшим rounding tolerance. Не вводить fixed-point до появления требования к платёжной точности.
- [ ] Реализовать immutable built-in registry и overlay resolver с precedence выше.
- [ ] Реализовать `Ledger.Add`/`SummaryByStage`/`RunSummary`: только здесь складываются token categories, cost и coverage.
- [ ] Unit-тестами зафиксировать реальные значения живого прогона: Claude `$0.202115`, GLM `$0.08536836` и mismatch reported cost, Codex inclusive-input subtraction, reasoning не удваивается, repeated assistant id не удваивает usage.
- [ ] Property/table tests: stage/run aggregation содержит один и тот же набор invocation records; расхождение их округлённых display values не допускается; zero rate priced; unknown rate unpriced; missing result unmetered.

### Task 2. Pricing config и model/channel hints

**Файлы:**

- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go`
- Modify: `config.example.yaml`
- Modify: `pkg/docker/wrapper.go`
- Test: `pkg/docker/wrapper_test.go`

- [ ] Добавить `Config.Pricing accounting.PricingConfig`. Направление зависимости только `config -> accounting`; обратного импорта нет.
- [ ] Валидировать отрицательные/непарсящиеся rates и разрешить явные нули. Частичный rate card проверяется resolver'ом по реально встреченным категориям; не включать глобальный strict-YAML mode ради этой секции.
- [ ] При генерации wrapper экспортировать несекретные `AFM_USAGE_CHANNEL` и `AFM_USAGE_MODEL` из recipe type/model. Они являются hint, stream observation имеет приоритет.
- [ ] Для прямых stage commands без recipe не угадывать model по имени бинарника. Native Claude узнаётся из stream, Codex adapter — по правилам ниже; иначе invocation может быть metered, но unpriced.
- [ ] Добавить пример overrides и пояснить, что это list-price estimate, а не банковское списание.

### Task 3. Сохранить raw usage в adapters, не считать его там

**Риск сопровождения №1:** synthetic JSON — нетипизированный контракт shell ↔ Go сразу для трёх adapters. Любое новое/переименованное поле должно сначала появиться в golden fixtures и parser tests. Неизвестный или битый envelope всегда деградирует в видимый `unmetered`, никогда в нулевую стоимость и никогда не ломает обработку текстового ответа агента.

**Файлы:**

- Modify: `scripts/codex-as-claude.sh`
- Modify: `scripts/openai-as-claude.sh`
- Modify: `scripts/openai-agent-as-claude.sh`
- Modify: adapter tests рядом с текущими wrapper/script tests

- [ ] `codex-as-claude.sh`: захватывать `turn.completed.usage` и включать без преобразований в synthetic `type=result` как `usage_schema:"openai"`, `channel:"codex"`; сохранить `cached_input_tokens`, `cache_write_input_tokens`, `reasoning_output_tokens`.
- [ ] Определять Codex model: `CODEX_MODEL` → безопасный top-level TOML `model` → отсутствует. Не читать rollout/session files и не выводить остальные поля config.
- [ ] `openai-as-claude.sh`: посылать `stream_options:{include_usage:true}`, сохранять final SSE chunk `.model/.usage`, эмитить их в synthetic result с `usage_schema:"openai_chat"` и `channel:"openai-api"`.
- [ ] `openai-agent-as-claude.sh`: включить `include_usage` на каждом внутреннем API turn и приложить в terminal result массив raw `upstream_usages`. Не использовать `jq add` для токенов или денег: это ответственность `accounting`.
- [ ] Error/timeout paths по возможности эмитят terminal result с уже полученными upstream usages и failure subtype; если процесс убит раньше, `Collector.Finish` создаст unmetered observation.
- [ ] Golden tests проверяют сохранение полей и отсутствие API keys/prompt contents в synthetic usage envelope.
- [ ] Добавить contract-version field (`usage_contract_version:1`) в synthetic envelope. Collector принимает известную версию, а неизвестную помечает `unmetered`/warning — без best-effort разбора несовместимой схемы.

### Task 4. Отдельный durable `accounting.Store`

**Файлы:**

- Create: `pkg/accounting/store.go`
- Create: `pkg/accounting/store_test.go`
- Modify: `cmd/afm/run.go`
- Explicitly do not modify: `pkg/state/store.go`, `pkg/state/state.go`, `events.jsonl` parser/tests

- [ ] Реализовать `accounting.Open(runDir, resolver)` для единственного live writer и `accounting.Load(runDir)` для read-only consumers. Оба работают только с `<runDir>/usage.jsonl`; каждая строка имеет явный `record_version`.
- [ ] Live Store держит mutex, append'ит одну полностью рассчитанную JSON line, делает `fsync`, и только после успеха применяет record к in-memory ledger. Единственность writer уже обеспечена run-level `.lock`, удерживаемым `state.Store`; второй flock не нужен.
- [ ] Replay реализует безопасное усечение последнего non-newline tail при live Open. Read-only Load одновременно с живым writer просто игнорирует такой tail, ничего не меняя. Битая полная строка возвращает `ErrCorruptUsage` и не изменяет оригинал; FSM resume при этом не блокируется — accounting отключается с явным warning/`unavailable` status.
- [ ] Отсутствующий `usage.jsonl` означает пустой accounting snapshot, а не ошибку — так читаются старые run.
- [ ] `Append(observation, stageID, phase, scope)` просит только `pkg/accounting` resolve'ить rate, построить record и пересчитать summary. В record нет общего FSM seq или attempt counter.
- [ ] Ошибка append/fsync не превращается в agent error и не инициирует retry/`EvFail`: usage — observability, а не управляющее состояние. Store запоминает ошибку, прекращает дальнейшие записи, а API/CLI показывают `accounting unavailable/incomplete`.
- [ ] Проверить replay, concurrent append serialization, torn tail, corrupt complete line, write/fsync failure, restart с теми же totals и старый run без usage.

### Task 5. Executor collection на каждый process invocation

**Файлы:**

- Modify: `pkg/executor/executor.go`
- Modify: `pkg/executor/executor_test.go`
- Modify: `pkg/executor/runner.go` только если понадобится документировать callback contract

- [ ] Добавить в `executor.Config` canonical `Phase`, `UsageHint` и `OnUsage func(accounting.Observation)`.
- [ ] В `RunPlanning` и `RunAgent` создавать новый collector на каждый вызов и отдавать ему исходную stdout JSONL line одновременно с существующей записью raw log. Collector видит строку **до** `truncate_output`; truncation применяется только к человекочитаемому action/detail.
- [ ] Через `defer` ровно один раз вызвать `Finish` и `OnUsage`, включая success, process error, timeout и user interrupt. Script path usage callback не вызывает.
- [ ] Accounting callback не меняет возвращаемую ошибку агента: сбой telemetry-store обрабатывается самим callback/store и не маскирует process exit status.
- [ ] Не добавлять attempt в `executor.Runner` и десятки test doubles: каждая observation уже соответствует ровно одному вызову.
- [ ] Tests: два последовательных вызова создают две observations; append-log со старыми result lines не пересчитывается; interrupt без result даёт `metered:false`; огромный usage envelope не портится `truncate_output`.

### Task 6. Orchestrator wiring и полное покрытие LLM-вызовов

**Файлы:**

- Modify: `pkg/orchestrator/runner_factory.go`
- Modify: `pkg/orchestrator/orchestrator.go`
- Modify: `pkg/orchestrator/retry.go`
- Modify: `pkg/orchestrator/options.go` при необходимости
- Modify: `pkg/memorypipeline/types.go`
- Modify: `pkg/memorypipeline/executor.go`
- Modify: `pkg/orchestrator/reflection.go`
- Test: `pkg/orchestrator/*usage*_test.go`, `pkg/memorypipeline/executor_test.go`

- [ ] Все production runners, включая default/shared runner и interactive fallback, получают canonical phase, source hint и один общий `o.recordUsage` callback.
- [ ] `o.recordUsage` вызывает `accounting.Store.Append`; при ошибке логирует и переводит accounting store в `unavailable`, не вызывает `setFatal`, не меняет FSM и не запускает agent retry.
- [ ] Per-stage phases: `planning`, `implementation`, `review`, `autonomous`. Каждая новая строка отражает отдельный process invocation.
- [ ] Явно проверить все пути интерактивной стадии: initial start, restart из `onUserAnswered`, Continue и Revise — каждый новый процесс должен получить `OnUsage`, иначе totals будут занижены.
- [ ] Расширить `memorypipeline.AgentConfig` callback'ом/hint, `AgentSpec` — `StageID`. `reflect` получает id исходной стадии и phase `memory_reflect`.
- [ ] `aggregate`, `prioritize`, `update` получают пустой stage id, scope `run_overhead`, phases `memory_aggregate`, `memory_prioritize`, `memory_update`; run total включает их, stage total — нет.
- [ ] `afm memory rebuild` вне контекста конкретного run не включать в run totals. Документировать это явно вместо записи usage в произвольный старый run.
- [ ] Integration test с initial call + user answer + retry + revise доказывает четыре records, правильную stage/phase attribution и сумму. Отдельный storage-failure test доказывает, что run продолжается, а accounting помечен incomplete/unavailable.

### Task 7. Read model для CLI/API без дублирования арифметики

**Файлы:**

- Modify: `pkg/server/stageview.go`
- Modify: `pkg/server/server.go`
- Modify: `pkg/server/handlers.go`
- Modify: `pkg/server/stageview_test.go`, `pkg/server/server_test.go`
- Modify: `cmd/afm/check.go`, `cmd/afm/check_test.go`

- [ ] Не менять `state.LoadRunState`: CLI сначала читает FSM из `events.jsonl`, затем независимо вызывает единственный read-only `accounting.Load` для `usage.jsonl`.
- [ ] Live server получает интерфейс snapshot-provider от `accounting.Store`. `StageView` получает готовый `accounting.Summary`; `statusResponse` — run summary, отдельный `run_overhead` summary и accounting health/coverage.
- [ ] API summary явно отдаёт: uncached input, cache read, cache write 5m/1h/other, output, reasoning subset, total tokens, cache hit ratio, estimated cost, metered/priced/unmetered/unpriced invocation counts и model ids.
- [ ] `afm check` добавляет компактные `TOKENS`, `CACHE`, `EST. COST`; CACHE показывает read/write, например `R 47.6k / W 18.6k`. После строк печатается `TOTAL`, включая run overhead.
- [ ] Если coverage неполный, рядом с total выводится предупреждение вроде `2 unmetered, 1 unpriced`; `$0.00` не используется для неизвестной цены.
- [ ] `cmd` и `server` не делают `+` над token/cost fields и не форматируют деньги сами — только выбирают готовый summary/formatter output.

### Task 8. `afm report <run>`

**Файлы:**

- Create: `cmd/afm/report.go`
- Create: `cmd/afm/report_test.go`
- Modify: `cmd/afm/main.go`
- Create: `pkg/accounting/report.go` и tests (если markdown renderer относится к accounting)

- [ ] Добавить `afm report [run]`: аргумент принимает run id или путь; без аргумента — последний run. Чтение read-only, без flock: status/duration из `events.jsonl`, usage/cost из `accounting.Load(usage.jsonl)`.
- [ ] Markdown содержит: stage, phases/invocation count, models, uncached input, cache read, cache write, output, total tokens, estimated cost, duration/status; затем `Run overhead` и `Total`.
- [ ] Раздел `Coverage and pricing` перечисляет unmetered/unpriced invocations, model ids, применённые rate keys, source/as-of и reported-vs-estimated mismatches.
- [ ] По умолчанию писать markdown в stdout для redirect/copy-paste; флаг `-o/--output` можно добавить только если он соответствует существующим CLI conventions.
- [ ] Golden test строится на паре fixture `events.jsonl` + `usage.jsonl`, а не на `state.json`; report старого run без usage должен быть валиден и честно говорить `No usage data`.

### Task 9. Dashboard: стоимость и cache visibility

**Файлы:**

- Modify: `pkg/web/dashboard/src/types/stage.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-status/use-status.ts`
- Modify: `pkg/web/dashboard/src/components/run-metrics/RunMetrics.tsx`
- Modify: `pkg/web/dashboard/src/components/run-metrics/run-metrics.css`
- Modify: компонент строки стадии в `pkg/web/dashboard/src/components/stages-list/`
- Tests: соответствующие `*.test.tsx`/`use-status.test.ts`

- [ ] Вынести TS `UsageSummary`/`TokenBreakdown`, маппить отсутствующие поля старого backend в пустой summary с `hasData:false`.
- [ ] В строке стадии показывать компактный badge estimated cost. Tooltip/popover: model(s), input uncached, cache read, cache write по TTL, output, cache hit и coverage.
- [ ] В `RunMetrics` добавить `Cost`; на узком layout он участвует в существующем overflow popover. Run cost уже готов на backend, React ничего не суммирует.
- [ ] Не показывать `$0.00` для unpriced/unmetered: `—` + объяснение. При частичном coverage допустимо `$0.0854+` и warning, только если такой presentation string произведён accounting API.
- [ ] Cache read и cache write визуально различимы; generic `cached tokens` без направления не использовать.
- [ ] UI tests покрывают exact cost, zero-cost priced, unpriced, unmetered, mixed coverage, separate R/W cache и backward-compatible пустой response.

### Task 10. Сквозная проверка и документация

**Файлы:**

- Create: `docs/accounting.md`
- Modify: `docs/config-reference.md` или актуальный config reference
- Modify: `README.md` только короткой ссылкой/примером при необходимости
- Add/update: deterministic fake-agent fixtures для E2E

- [ ] Документировать token semantics по schema, formula, cache TTL, `reported` vs `estimated`, subscription caveat, override syntax и `unmetered/unpriced`.
- [ ] Fake agent E2E: Claude-style result, Codex/OpenAI-style result, interactive answer restart, retry, unknown model и missing result; проверить `usage.jsonl` → restart/replay → `/api/status` → `check` → `report`, не меняя содержимое/семантику `events.jsonl`.
- [ ] Повторить живой flow Claude + GLM 5.3 + Codex после реализации. Acceptance assertions:
  - Claude estimate совпадает с reported cost для зафиксированного тарифа;
  - GLM estimate использует Z.AI rate, а mismatch с Claude-reported cost виден только в diagnostics;
  - Codex имеет model, inclusive input корректно разделён на uncached/cache read;
  - run summary агрегирует тот же набор invocation records, что stage summaries + run overhead, и их display total совпадает;
  - restart не меняет totals или число invocations;
  - ни один terminal result не посчитан дважды.
- [ ] Выполнить `go test ./...`, `go test -race ./...`, `make lint`, frontend lint/test/build и `make generate-check`.

## Что намеренно не входит в эту реализацию

- `max_cost` и перевод стадии в `awaiting_approval`: это отдельное изменение FSM с вопросами preflight/reservation, гонками параллельных стадий и поведением уже запущенного агента. Сначала нужен достоверный ledger; budget guard планируется отдельным документом поверх него.
- Исторический time-series usage endpoint и график по времени. Для пункта 5 нужны stage/run totals; старый proxy-based подход неоднозначно приписывал параллельный трафик стадиям. Новая привязка делается в точке конкретного executor invocation.
- Попытка определить фактическое списание subscription/credits. AFM показывает прозрачную list-price estimate и позволяет настроить реальные договорные rates override'ом.
- Ретроактивный расчёт старых run по сырым phase JSONL: он может задвоить повторяющиеся assistant events и не содержит usage у нынешних adapters. Старые run показывают `No usage data`.

## Критерий готовности пункта 5

Пункт завершён, когда для нового run с Claude, GLM 5.3 и Codex:

1. каждый LLM process invocation при доступном accounting store имеет ровно одну durable usage/unmetered запись с `stage/phase`; при сбое store интерфейсы явно показывают incomplete/unavailable;
2. `afm check`, dashboard и `afm report` показывают одинаковые per-stage и total значения;
3. input явно разделён на uncached, cache read и cache write (5m/1h/other, если провайдер сообщил);
4. неизвестные данные видны как unmetered/unpriced, а не как ноль;
5. после restart число invocations, token totals и отображаемая стоимость не меняются;
6. вся нормализация, арифметика, агрегация и monetary formatting находятся в `pkg/accounting`.
