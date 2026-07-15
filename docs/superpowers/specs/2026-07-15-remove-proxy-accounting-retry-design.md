# Дизайн: ретрай 529 + выпил built-in proxy + выпил accounting

**Дата:** 2026-07-15
**Подход:** A — один спек, две фазы (retry → proxy+accounting вместе).

## Контекст и мотивация

Три связанные задачи:

1. **Авто-ретрай на транзиентные HTTP-ошибки 529/502/503/504.** Сейчас `API Error: 529` (raw text из glm-обёрток) не совпадает ни с одним паттерном в `Classify()` → классифицируется как `ClassFatal` → stage падает вместо ретрая. Подробно: ~~`docs/port-529-transient-retry.md`~~ (реализуется в Фазе 1, затем удаляется).

2. **Выпил built-in reverse proxy (`pkg/proxy`).** Прокси делал три вещи: (a) ZAI-transform (non-streaming→streaming reassembly для обхода 529 от `api.z.ai`); (b) `captureUsage` → `usage.jsonl`; (c) маршрутизация трафика + claude-shim. (a) избыточно после Фазы 1 — ретрай ловит 529 реактивно; (b) нужно только accounting (Фаза 2); (c) не нужно — autoShim-врапперы bake'ят прямой upstream-URL.

3. **Выпил accounting / подсчёта токенов (`pkg/accounting`).** После удаления прокси теряется источник данных (`usage.jsonl`); accounting оставлять бессмысленно. Пользователь явно: «пока не нужно, потом сделаем по другому».

**Связанность:** `pkg/accounting` жёстко импортирует тип `proxy.UsageRecord` (`reader.go`, `aggregate.go`, `attribution.go`). Удалить `pkg/proxy` без правки accounting нельзя (не скомпилируется) → Фаза 2 удаляет обе подсистемы синхронно.

## Фаза 1 — ретрай (изолированная, первой)

### `pkg/orchestrator/errors.go`
Добавить 4 константы после `matchInternalServerError`:
```go
matchAPIError529 = "api error: 529"
matchAPIError502 = "api error: 502"
matchAPIError503 = "api error: 503"
matchAPIError504 = "api error: 504"
```
Включить их в срез паттернов внутри `Classify()`.

**500 намеренно не ретрается:** `matchHTTP500`/`matchStatus500` уже покрывают транзиентные 500-е через JSON-путь; `API Error: 500` как raw-текст — потенциально детерминированная серверная ошибка.

### `pkg/orchestrator/errors_test.go`
5 кейсов: 529/502/503/504 → `ClassRetryable`; 500 → `ClassFatal`.

### Не трогать
- `retry.go` (`RetryBackoff = 5s/10s/30s`) — backoff достаточен.
- `executor.go` (`firstErr` по подстроке "Error") — корректно ловит "API Error: 529".

### Критерий готовности Фазы 1
`go test ./pkg/orchestrator/... -run TestClassify -v` зелёный, включая новые кейсы. Ретрай работает ДО удаления ZAI-transform — окно «голого 529» отсутствует.

## Фаза 2 — выпил proxy + accounting (единым проходом)

### Удалить пакеты целиком
- `pkg/proxy/` — `proxy.go`, `transform.go`, `zai.go`, `usage.go` + тесты + `CODEMANIFEST`/`.usages/`.
- `pkg/accounting/` — `accountant.go`, `reader.go`, `aggregate.go`, `attribution.go`, `cost.go` + тесты + `CODEMANIFEST`/`.usages/`.

### Backend-интеграции

**`cmd/afm/run.go`** (главная точка старта):
- Удалить блок старта прокси (158-182): `BuildTransforms`, `proxy.New`, `p.Start`, `defer p.Shutdown`, `usageLogPath`, переменные `proxyAddr`/`proxyUpstream`/`proxyOn`.
- Удалить `wrapperSpecs`/`proxyShimDir` (187-210): claude-shim (`WrapperSpec{Command:"claude", BaseURL:proxyAddr}`) и `docker.CreateWrappers` — прокси больше нет, shim не нужен.
- Удалить `NewAccountant(...)` (216) и `server.Config{Accountant}`.
- `orchestrator.Options` — без `ProxyURL`/`ProxyShimDir`. `GeneratedAgents` **остаётся** (нужно autoShim для generated-aware маршрутизации/ScanCommands — не связано с прокси).
- Импорт `pkg/proxy`, `pkg/accounting` убрать.

**`pkg/orchestrator/orchestrator.go`**:
- Удалить поля `Options.ProxyURL`, `Options.ProxyShimDir`, метод `proxyForCmd`.
- Убрать прокси-поля из 4 `executor.New` call-site'ов (`:121-122`, `:259-260`, `:288-289`, `:302-303`).
- Удалить `pkg/orchestrator/proxy_cmd_test.go`.

**`pkg/executor/executor.go`**:
- Удалить `Config.ProxyURL`, `Config.ProxyShimDir`.
- Удалить env-логику: strip `ANTHROPIC_BASE_URL` при `ProxyURL` (426), inject `ANTHROPIC_BASE_URL`+`AFM_PROXY_URL` (435-437), prepend shim-dir к PATH (439-451), `LookPath` в shim-dir (411-415).
- Агент наследует окружение как есть: glm-обёртки используют собственный `ANTHROPIC_BASE_URL`.

**`pkg/docker/wrapper.go`**:
- Удалить `ResolveBaseURL` и `hostOf` (host-match для прокси больше не нужен).
- `buildWrapperSpec` в `run.go` упрощается: убрать параметры `proxyOn`/`proxyUpstream`/`proxyAddr`, `BaseURL: recipe.URL` напрямую (прямой upstream bake'ится во враппер).
- Обновить `wrapper_test.go` (убрать `TestHostOf`/`TestResolveBaseURL`) и `cmd/afm/run_test.go` (`buildWrapperSpec` без proxy-параметров).

**`pkg/server/`**:
- Удалить `pkg/server/usage_handler.go` + `usage_handler_test.go`.
- Убрать роут `/api/usage` из `server.go` (`:98`), поле `Config.Accountant` (`:33`, `:55`).
- Обновить `server_test.go`.

### Config

**`pkg/config/config.go`**:
- Удалить типы `ProxyConfig`, `TransformOverrides`, `PricingConfig`, `ModelPricing`, `AccountingConfig` + геттеры (`IsEnabled`, `GetBucketMinutes`, `GetModelPricing`) + парсинг в `mergeFile` (`:343-386`).
- Удалить поля `Proxy`/`Pricing`/`Accounting` из `Config`.
- Обновить `config_test.go` (убрать тесты proxy/pricing/accounting).

**Миграция конфигов (важно):** `yaml.Unmarshal` без `KnownFields` → **lenient**, unknown ключи игнорируются. Старые конфиги с `proxy:`/`pricing:`/`accounting:` продолжат парситься без ошибки — секции молча перестанут учитываться. Никаких breaking changes, миграция не требуется.

### Frontend (dashboard)

`pkg/web/dashboard/src/`:
- Удалить `components/consumption-panel/` (`ConsumptionPanel.tsx` + `index.ts` + тесты).
- Удалить `hooks/use-usage-data/` (`use-usage-data.ts` + `index.ts` + тесты).
- Удалить `types/usage-point.ts`; убрать re-export из `types/index.ts`.
- Убрать `<ConsumptionPanel>` из `app/App.tsx` (`:147`).

### Доки и конфиги

- `config.example.yaml` + `~/.afm/config.yaml`: убрать секции `proxy`, `pricing`, `accounting`.
- Удалить `docs/port-529-transient-retry.md` (реализован в Фазе 1).
- Удалить устаревшие: `docs/superpowers/plans/2026-06-29-proxy.md`, `docs/superpowers/specs/2026-06-29-proxy-design.md`, `docs/2026-07-04-design-stage-token-burn-report.md`.
- `CLAUDE.md`: убрать секцию «Built-in Reverse Proxy» (и связанные env-var таблицы, debugging, common changes) + упоминания `usage`/pricing/accounting.
- `release-notes.md`: запись 2026-07-15 (retry + удаление proxy/accounting, с пометкой backward compat для конфигов).

### Поведение после (edge cases — проверено)

- **`autoShim: false` (Docker):** glm-обёртки (смонтированные binaries) **уже шли напрямую** на z.ai — они сами `export ANTHROPIC_BASE_URL` (clobbering executor injection) и `exec claude` без шима. Прокси для них не использовался. Удаление нейтрально: executor просто перестаёт inject/strip прокси-env (был мёртв для glm-обёрток). Cursor (`type:cursor`) вообще не зависит от прокси (адаптер дёргает `api.cursor.com` напрямую).
- **`autoShim: true` (Docker):** generated-врапперы теперь bake'ят **прямой** upstream-URL (без host-match на прокси). Работает идентично — агент идёт напрямую, 529 ловится ретраем (Фаза 1).
- **Не-Docker mode:** `client.command` (glm52) на хосте — без executor env-инъекции использует свой `ANTHROPIC_BASE_URL`; 529 → ретрай.
- **plain `command: claude`:** без прокси-шима идёт напрямую на Anthropic через `~/.claude` auth (в Docker монтируется). Единственное реальное изменение, но не use-case пользователя (glm52/cursor).

## Проверка

```bash
# Фаза 1
go test ./pkg/orchestrator/... -run TestClassify -v

# Фаза 2 (компиляция без proxy/accounting)
go build ./...
go test ./...
~/go/bin/golangci-lint run ./...
```

Smoke (опционально, end-to-end): запустить flow с `command: cursor` и `command: glm52` через `autoShim: true` — убедиться, что агенты отвечают напрямую (529, если возникнет, ретраится).

## Что НЕ входит (YAGNI / явно отложено)

- Учёт потребления «по-другому» — пользователь отложил («потом сделаем по другому»).
- Стриминг-SSE из Cloud Agents / прогресс cursor — не относится.
- Доработка `is_error:true` для честной сигнализации ошибок cursor run — отдельная задача.
