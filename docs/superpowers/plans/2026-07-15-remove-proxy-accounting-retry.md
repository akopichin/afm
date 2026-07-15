# Ретрай 529 + выпил proxy и accounting — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Добавить авто-ретрай на 529/502/503/504 и полностью удалить из кода built-in reverse proxy и подсистему accounting/подсчёта токенов.

**Architecture:** Две фазы. Фаза 1 — изолированная правка `orchestrator.Classify` (4 новых паттерна), даёт страховку от 529. Фаза 2 — синхронное удаление `pkg/proxy` и `pkg/accounting` (связаны типом `proxy.UsageRecord`, поэтому удаляются вместе) плюс всей threading-инфры в consumers, по порядку «снаружи внутрь», чтобы каждый переход компилировался.

**Tech Stack:** Go 1.26, React 18 + Vite + TypeScript (dashboard), bash, Docker.

**Спек:** `docs/superpowers/specs/2026-07-15-remove-proxy-accounting-retry-design.md`

## Global Constraints

- Все коммиты — на русском, БЕЗ `Co-Authored-By`.
- После каждой задачи: `go build ./...` компилируется, `go test ./...` зелёный, `~/go/bin/golangci-lint run` не выдаёт НОВЫХ замечаний на изменённых файлах.
- Версию Go в `go.mod` не трогать.
- Каждый переход между задачами оставляет репозиторий компилируемым (`go build ./...`).
- При сборке host-бинаря — `make install` (codesign для macOS 26; см. memory `afm-binary-update`).
- References на строки — актуальны на момент написания плана (commit `2a2f5af`); исполнитель сверяется с текущим кодом.

---

## Фаза 1 — ретрай

### Task 1: ретрай на 529/502/503/504 в orchestrator.Classify

**Files:**
- Modify: `pkg/orchestrator/errors.go:21-30` (константы) и `errors.go:72-80` (срез паттернов)
- Test: `pkg/orchestrator/errors_test.go`

**Interfaces:**
- Consumes: ничего (изолированная правка).
- Produces: `Classify` теперь возвращает `ClassRetryable` для ошибок, содержащих `api error: 529/502/503/504`.

- [ ] **Step 1: написать падающий тест**

В `pkg/orchestrator/errors_test.go` найти table-test `TestClassify` (по образцу существующих кейсов) и добавить в срез `cases` 5 записей:

```go
{"api error 529", errors.New("API Error: 529 Overloaded"), ClassRetryable},
{"api error 502", errors.New("API Error: 502 Bad Gateway"), ClassRetryable},
{"api error 503", errors.New("API Error: 503 Service Unavailable"), ClassRetryable},
{"api error 504", errors.New("API Error: 504 Gateway Timeout"), ClassRetryable},
{"api error 500 stays fatal", errors.New("API Error: 500 Internal Server Error"), ClassFatal},
```

- [ ] **Step 2: запустить тест — должен упасть**

```bash
go test ./pkg/orchestrator/... -run TestClassify -v
```
Expected: 4 кейса (529/502/503/504) FAIL — `Classify` вернул `ClassFatal`, ожидается `ClassRetryable`. Кейс 500 PASS (уже fatal).

- [ ] **Step 3: добавить константы**

В `pkg/orchestrator/errors.go` после `matchInternalServerError` (строка 29) добавить:

```go
	matchAPIError529 = "api error: 529"
	matchAPIError502 = "api error: 502"
	matchAPIError503 = "api error: 503"
	matchAPIError504 = "api error: 504"
```

- [ ] **Step 4: включить константы в срез паттернов**

В `Classify()` (строки 72-76) заменить срез:

```go
	for _, p := range []string{
		matchHitYourLimit, matchRateLimit, matchTooManyRequests,
		matchOverloaded, matchAtCapacity,
		matchHTTP500, matchStatus500, matchInternalServerError,
		matchAPIError529, matchAPIError502, matchAPIError503, matchAPIError504,
	} {
```

- [ ] **Step 5: запустить тест — должен пройти**

```bash
go test ./pkg/orchestrator/... -run TestClassify -v
```
Expected: PASS (все 5 новых кейсов + существующие).

- [ ] **Step 6: полный прогон + lint**

```bash
go build ./... && go test ./pkg/orchestrator/...
~/go/bin/golangci-lint run ./pkg/orchestrator/...
```
Expected: build OK, тесты зелёные, lint без новых замечаний.

- [ ] **Step 7: коммит**

```bash
git add pkg/orchestrator/errors.go pkg/orchestrator/errors_test.go
git commit -m "feat(orchestrator): ретрай на 529/502/503/504 в Classify

API Error: 5xx (raw-текст из glm-обёрток) не совпадал с паттернами → ClassFatal
вместо ретрая. Добавлены matchAPIError{529,502,503,504}. 500 намеренно fatal."
```

---

## Фаза 2 — выпил proxy + accounting

> Порядок «снаружу внутрь». После каждой задачи `go build ./...` компилируется.

### Task 2: убрать accounting из backend-consumers

**Files:**
- Modify: `cmd/afm/run.go:212-216,252` (NewAccountant + server.Config.Accountant), `cmd/afm/run.go:18` (импорт `pkg/accounting`)
- Modify: `pkg/server/server.go:33,55,98` (Config.Accountant, роут `/api/usage`)
- Delete: `pkg/server/usage_handler.go`, `pkg/server/usage_handler_test.go`
- Modify: `pkg/server/server_test.go` (убрать упоминания Accountant/usage)

**Interfaces:**
- Consumes: ничего.
- Produces: `pkg/accounting` больше не используется вне своего пакета; `pkg/server` не имеет `Config.Accountant` и роута `/api/usage`. `pkg/proxy` всё ещё используется в `run.go` (убирается в Task 4) и в `pkg/accounting` (пакет живёт до Task 6).

- [ ] **Step 1: убрать из `cmd/afm/run.go`**
  - Удалить импорт `"github.com/akopichin/afm/pkg/accounting"` (строка 18).
  - Удалить блок 212-216 (комментарий + `accountant := accounting.NewAccountant(...)`).
  - Удалить поле `Accountant: accountant` из `server.Config{...}` (строка ~252).

- [ ] **Step 2: убрать из `pkg/server/server.go`**
  - Удалить поле `Accountant Accountant` из `Config` (строка 33) и интерфейс `Accountant` (строка ~25, если объявлен тут; иначе в usage_handler.go — уйдёт с файлом).
  - Удалить `mux.Handle("/api/usage", UsageHandler(cfg.Accountant))` (строка 98).

- [ ] **Step 3: удалить файлы usage_handler**

```bash
git rm pkg/server/usage_handler.go pkg/server/usage_handler_test.go
```

- [ ] **Step 4: починить `pkg/server/server_test.go`**

Убрать любые ссылки на `Config.Accountant`, `/api/usage`, `UsageHandler`, mock-Accountant. Если тест целиком про usage — удалить тест-функцию.

- [ ] **Step 5: verify**

```bash
go build ./...
go test ./pkg/server/... ./cmd/afm/...
```
Expected: build OK, тесты зелёные. (run_test.go может ссылаться на accounting — починить аналогично.)

- [ ] **Step 6: коммит**

```bash
git add -A
git commit -m "refactor(accounting): убрать consumers из run.go и server

Удалены NewAccountant в run.go, Config.Accountant + роут /api/usage в server,
файлы usage_handler{,_test}. Пакет pkg/accounting пока живёт (удаляется в Task 6)."
```

### Task 3: убрать dashboard ConsumptionPanel

**Files:**
- Delete: `pkg/web/dashboard/src/components/consumption-panel/` (включая `ConsumptionPanel.tsx`, `index.ts`, тесты)
- Delete: `pkg/web/dashboard/src/hooks/use-usage-data/` (включая `use-usage-data.ts`, `index.ts`, тесты)
- Delete: `pkg/web/dashboard/src/types/usage-point.ts`
- Modify: `pkg/web/dashboard/src/types/index.ts:5` (убрать re-export `usage-point`)
- Modify: `pkg/web/dashboard/src/app/App.tsx:147` (убрать `<ConsumptionPanel>` + импорт)

**Interfaces:**
- Consumes: ничего (backend `/api/usage` уже убран в Task 2).
- Produces: dashboard без usage-панели.

- [ ] **Step 1: удалить компоненты и хуки**

```bash
git rm -r pkg/web/dashboard/src/components/consumption-panel pkg/web/dashboard/src/hooks/use-usage-data
git rm pkg/web/dashboard/src/types/usage-point.ts
```

- [ ] **Step 2: убрать re-export из `types/index.ts`**

Удалить строку `export * from './usage-point';` (или аналогичную).

- [ ] **Step 3: убрать из `app/App.tsx`**

Удалить импорт `ConsumptionPanel` и строку монтирования `<ConsumptionPanel stages={stages} />` (~строка 147). Убрать `stages` из пропсов, если использовался только тут.

- [ ] **Step 4: verify (npm build + test)**

```bash
cd pkg/web/dashboard && npm run build && npm test -- --run
```
Expected: build OK, тесты зелёные (убрать `ConsumptionPanel.test.tsx`/`use-usage-data.test.ts`, если остались ссылки — починить).

- [ ] **Step 5: коммит**

```bash
git add -A
git commit -m "refactor(dashboard): убрать ConsumptionPanel и use-usage-data

Панель потребления удалена вместе с хуком опроса /api/usage (endpoint убран в Task 2)."
```

### Task 4: убрать proxy threading (run.go + orchestrator + executor + docker)

**Files:**
- Modify: `cmd/afm/run.go:153-210,218-229` (proxy start + wrapper-dir + orchestrator.Options)
- Modify: `pkg/orchestrator/orchestrator.go:71-72,121-122,236-244,259-260,288-289,302-303` (Options.ProxyURL/ShimDir, proxyForCmd, 4 executor.New)
- Delete: `pkg/orchestrator/proxy_cmd_test.go`
- Modify: `pkg/executor/executor.go:27-28,410-451` (Config.ProxyURL/ShimDir, LookPath + env injection)
- Modify: `pkg/docker/wrapper.go:21-40` (hostOf, ResolveBaseURL) + тесты
- Modify: `cmd/afm/run.go` `buildWrapperSpec` (строки 451-465)

**Interfaces:**
- Consumes: Task 2 (run.go уже без accounting).
- Produces: `pkg/proxy` больше не используется в `cmd/afm` и `pkg/orchestrator`/`pkg/executor` (но всё ещё импортируется `pkg/accounting` → пакет живёт до Task 6). `executor.New` не принимает ProxyURL/ProxyShimDir. `buildWrapperSpec(cmd, recipe)` — без proxy-параметров.

- [ ] **Step 1: убрать блок proxy start из `cmd/afm/run.go`**

Удалить строки 153-182 (блок `if cfg.Proxy.IsEnabled() ...` + объявление `proxyAddr`/`proxyUpstream`/`proxyOn`). Заменить `proxyAddr`/`proxyOn` использования ниже на прямые значения.

Удалить строки 184-210 (wrapperSpecs/claude-shim/`docker.CreateWrappers`/`proxyShimDir`). Generated-врапперы (autoShim) при этом **остаются** — пересоздать wrapper-dir без proxy-shim:

```go
// Единый wrapper-dir: generated-врапперы (autoShim, только внутри контейнера).
var wrapperSpecs []docker.WrapperSpec
generatedAgents := map[string]bool{}
if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
	if err := cfg.Docker.ValidateAgents(); err != nil {
		return err
	}
	for cmd := range docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents) {
		generatedAgents[cmd] = true
		wrapperSpecs = append(wrapperSpecs, buildWrapperSpec(cmd, cfg.Docker.Agents[cmd]))
	}
}
var proxyShimDir string
if len(wrapperSpecs) > 0 {
	wd, err := docker.CreateWrappers(wrapperSpecs)
	if err != nil {
		return fmt.Errorf("create wrappers: %w", err)
	}
	proxyShimDir = wd
	defer os.RemoveAll(wd) //nolint:errcheck
}
```
> `proxyShimDir` переименовать в `wrapperDir` (рекомендуется) или оставить имя — но поле ниже убрать. Имя сохранить, если минимум правок; лучше переименовать для ясности.

- [ ] **Step 2: `buildWrapperSpec` — убрать proxy-параметры**

Заменить (строки 451-465) на:

```go
// buildWrapperSpec строит WrapperSpec из recipe: прямой upstream URL bake'ится
// во враппер (прокси удалён — host-match не нужен).
func buildWrapperSpec(cmd string, recipe config.AgentRecipe) docker.WrapperSpec {
	return docker.WrapperSpec{
		Type:         recipe.Type,
		Command:      cmd,
		AuthTo:       recipe.Auth.EnvVarName(),
		BaseURL:      recipe.URL,
		Model:        recipe.Model,
		HasSysPrompt: recipe.SystemPrompt != "",
	}
}
```

- [ ] **Step 3: убрать ProxyURL/ProxyShimDir из `orchestrator.Options`**

Удалить поля `ProxyURL`, `ProxyShimDir` из `Options` (строки 71-72). В `cmd/afm/run.go` убрать `ProxyURL:`/`ProxyShimDir:` из `orchestrator.New(orchestrator.Options{...})`.

- [ ] **Step 4: удалить `proxyForCmd` и прокси-поля в `executor.New` call sites**

В `pkg/orchestrator/orchestrator.go`:
- Удалить метод `proxyForCmd` (строки 236-244) и его вызовы.
- В 4 `executor.New(executor.Config{...})` (121-122, 259-260, 288-289, 302-303) убрать поля `ProxyURL:`/`ProxyShimDir:`. Заменить вызовы `proxyForCmd(...)` на прямое использование `e.opts.WrapperDir`/команды (свериться с текущей логикой `proxyForCmd` — она решает generated vs mounted; GeneratedAgents остаётся).

Удалить `pkg/orchestrator/proxy_cmd_test.go`:
```bash
git rm pkg/orchestrator/proxy_cmd_test.go
```

- [ ] **Step 5: убрать ProxyURL/ProxyShimDir из `executor.Config` + env-инъекцию**

В `pkg/executor/executor.go`:
- Удалить поля `ProxyURL`, `ProxyShimDir` из `Config` (27-28).
- Упростить резолв команды (410-415): убрать `if e.cfg.ProxyShimDir != ""` — если нужен wrapper-dir, передавать через абсолютный путь команды в `Config.Command` (Caller responsibility) ИЛИ оставить `Config.WrapperDir` без proxy-семантики. Свериться с тем, как `runnerFor` передаёт команду — минимально оставить `LookPath` в wrapper-dir, если `WrapperDir` поле сохраняется.
- Удалить env-логику (419-451): убрать `case ProxyURL && ANTHROPIC_BASE_URL` (426-427), убрать блок inject `ANTHROPIC_BASE_URL`+`AFM_PROXY_URL` (435-437), убрать prepend shim-dir к PATH (439-451). Оставить только `AFM_STAGE_DIR` (432-434).

- [ ] **Step 6: убрать `ResolveBaseURL`/`hostOf` из `pkg/docker/wrapper.go`**

Удалить функции `hostOf` (21-27) и `ResolveBaseURL` (29-40). Обновить `wrapper_test.go`: удалить `TestHostOf` и `TestResolveBaseURL`.

- [ ] **Step 7: verify**

```bash
go build ./...
go test ./pkg/orchestrator/... ./pkg/executor/... ./pkg/docker/... ./cmd/afm/...
~/go/bin/golangci-lint run ./pkg/orchestrator/... ./pkg/executor/... ./pkg/docker/... ./cmd/afm/...
```
Expected: build OK, тесты зелёные, lint без новых замечаний. `pkg/proxy` всё ещё компилируется (используется `pkg/accounting`).

- [ ] **Step 8: коммит**

```bash
git add -A
git commit -m "refactor(proxy): убрать threading прокси из run/orchestrator/executor/docker

Удалены proxy start в run.go, Options.ProxyURL/ShimDir + proxyForCmd в orchestrator,
Config.ProxyURL/ShimDir + env-инъекция в executor, ResolveBaseURL/host-match в docker.
buildWrapperSpec bake'ит прямой recipe.URL. Пакет pkg/proxy ещё живёт (Task 6)."
```

### Task 5: config cleanup (proxy/pricing/accounting)

**Files:**
- Modify: `pkg/config/config.go` — удалить `ProxyConfig`, `TransformOverrides`, `PricingConfig`, `ModelPricing`, `AccountingConfig` + геттеры + парсинг в `mergeFile` (343-386) + поля в `Config`
- Modify: `pkg/config/config_test.go` — убрать тесты proxy/pricing/accounting

**Interfaces:**
- Consumes: Task 2 + Task 4 (run.go уже не использует `cfg.Proxy`/`cfg.Pricing`/`cfg.Accounting`).
- Produces: `Config` без полей Proxy/Pricing/Accounting.

- [ ] **Step 1: удалить типы и геттеры из `pkg/config/config.go`**

Удалить объявления: `ProxyConfig`, `TransformOverrides` (строки ~52-71), `PricingConfig`/`ModelPricing`/`GetModelPricing` (~205-223), `AccountingConfig`/`IsEnabled`/`GetBucketMinutes` (~226-248). Удалить поля `Proxy`, `Pricing`, `Accounting` из `Config`.

- [ ] **Step 2: убрать парсинг в `mergeFile`**

Удалить блоки парсинга `proxy.*` (343-354), `pricing.models` (378-380), `accounting.*` (381-386) из `mergeFile`.

- [ ] **Step 3: починить `pkg/config/config_test.go`**

Удалить тесты: `TestProxyConfig_IsEnabled`, `TestProxyConfigMerge`, `TestProxyConfigMergeDefaults`, `TestPricingConfig_*`, `TestAccountingConfig_GetBucketMinutes`, `TestLoadFrom_PricingAccounting*`, `TestConfigPricingAccountingFieldsExists` и упоминания `cfg.Proxy`/`Pricing`/`Accounting` в `TestLoadFrom_DockerConfig` и др.

- [ ] **Step 4: verify**

```bash
go build ./...
go test ./pkg/config/...
~/go/bin/golangci-lint run ./pkg/config/...
```
Expected: build OK, тесты зелёные. (Проверить, что нигде больше нет ссылок на `cfg.Proxy`/`cfg.Pricing`/`cfg.Accounting` — `grep -rn "cfg\.\(Proxy\|Pricing\|Accounting\)" pkg/ cmd/` пусто.)

- [ ] **Step 5: коммит**

```bash
git add -A
git commit -m "refactor(config): убрать секции proxy/pricing/accounting

Удалены ProxyConfig/TransformOverrides/PricingConfig/ModelPricing/AccountingConfig
+ геттеры + парсинг в mergeFile. yaml.Unmarshal lenient → старые конфиги
с этими секциями продолжают парситься (ключи игнорируются)."
```

### Task 6: удалить пакеты pkg/accounting и pkg/proxy

**Files:**
- Delete: `pkg/accounting/` (целиком)
- Delete: `pkg/proxy/` (целиком)

**Interfaces:**
- Consumes: Task 2-5 (никто больше не импортирует эти пакеты).
- Produces: репозиторий без `pkg/accounting` и `pkg/proxy`.

- [ ] **Step 1: проверить, что нет dangling импортов**

```bash
grep -rn "ai-flow-manager/pkg/proxy\|ai-flow-manager/pkg/accounting" --include="*.go" .
```
Expected: пусто (все consumers убраны в Task 2-5).

- [ ] **Step 2: удалить пакеты**

```bash
git rm -r pkg/proxy pkg/accounting
```

- [ ] **Step 3: verify**

```bash
go build ./...
go test ./...
~/go/bin/golangci-lint run ./...
```
Expected: build OK, все тесты зелёные, lint без новых замечаний.

- [ ] **Step 4: коммит**

```bash
git commit -m "refactor: удалить pkg/proxy и pkg/accounting

Обе подсистемы выпилены полностью. 529 теперь ловится ретраем (Фаза 1),
учёт потребления отложен (потом сделаем по другому)."
```

### Task 7: доки, config.example, конфиг пользователя, release-notes

**Files:**
- Modify: `config.example.yaml` — убрать секции `proxy`, `pricing`, `accounting`
- Modify: `~/.afm/config.yaml` — убрать секции `proxy`, `pricing`, `accounting`
- Delete: `docs/port-529-transient-retry.md`, `docs/superpowers/plans/2026-06-29-proxy.md`, `docs/superpowers/specs/2026-06-29-proxy-design.md`, `docs/2026-07-04-design-stage-token-burn-report.md`
- Modify: `CLAUDE.md` — убрать секцию «Built-in Reverse Proxy» (+ env-таблицы, debugging, common changes) и упоминания usage/pricing/accounting
- Modify: `release-notes.md` — запись 2026-07-15

**Interfaces:**
- Consumes: Task 1-6.
- Produces: актуальные доки/конфиги.

- [ ] **Step 1: config.example.yaml**

Удалить секции `proxy:` (со всем содержимым + комментарием), `pricing:`, `accounting:`.

- [ ] **Step 2: ~/.afm/config.yaml (конфиг пользователя)**

Удалить секции `proxy:` (поле `upstream`), `pricing:`, `accounting:`.

- [ ] **Step 3: удалить устаревшие docs**

```bash
git rm docs/port-529-transient-retry.md docs/superpowers/plans/2026-06-29-proxy.md docs/superpowers/specs/2026-06-29-proxy-design.md docs/2026-07-04-design-stage-token-burn-report.md
```

- [ ] **Step 4: CLAUDE.md**

Удалить целиком секцию `## Built-in Reverse Proxy` (Architecture, Key Files, How ZAI works, Wrapper commands, Environment Variables, Config, Debugging, Common Changes, Known limitations). Убрать упоминания `usage`/pricing/accounting в других местах (поиск `grep -niE "proxy|usage|pricing|accounting|529" CLAUDE.md`).

- [ ] **Step 5: release-notes.md**

Добавить запись сверху (после заголовка):

```markdown
## 2026-07-15

### Ретрай на 529/502/503/504 + удаление proxy и accounting
- `orchestrator.Classify` теперь классифицирует `API Error: 529/502/503/504` (raw-текст из glm-обёрток) как `ClassRetryable` (раньше `ClassFatal` → stage падал). 500 остаётся fatal.
- Полностью удалён built-in reverse proxy (`pkg/proxy`): ZAI-transform избыточен после ретрая, маршрутизация не нужна (autoShim-врапперы bake'ят прямой upstream-URL). Убрана threading-инфра в `run.go`/`orchestrator`/`executor`/`docker`.
- Полностью удалён accounting/подсчёт токенов (`pkg/accounting`): терял источник данных без прокси. Убраны `/api/usage`, dashboard `ConsumptionPanel`, config `proxy`/`pricing`/`accounting`.
- **Backward compat:** `yaml.Unmarshal` lenient → старые конфиги с `proxy`/`pricing`/`accounting` продолжают парситься (секции молча игнорируются). `autoShim:false` нейтрален (glm-обёртки уже шли напрямую). Учёт потребления отложен.
```

- [ ] **Step 6: verify build**

```bash
go build ./... && make build
```
Expected: build OK.

- [ ] **Step 7: коммит**

```bash
git add -A
git commit -m "docs: актуализация под удаление proxy/accounting + ретрай 529

Убраны секции proxy/pricing/accounting из config.example и CLAUDE.md (Built-in
Reverse Proxy), удалены устаревшие docs (port-529, proxy spec/plan, burn-report),
запись в release-notes. Конфиг пользователя приведён в соответствие."
```

---

## Self-Review

**1. Spec coverage:**
- Фаза 1 (retry 529/502/503/504, 500 fatal) → Task 1. ✓
- Удалить `pkg/proxy` + `pkg/accounting` → Task 6. ✓
- Backend consumers (run.go, orchestrator, executor, server) → Task 2, 4. ✓
- docker ResolveBaseURL/buildWrapperSpec → Task 4. ✓
- config cleanup → Task 5. ✓
- dashboard ConsumptionPanel → Task 3. ✓
- docs/configs cleanup → Task 7. ✓
- edge cases (autoShim:false нейтрален, lenient YAML) → зафиксированы в спеке + release-notes (Task 7). ✓

**2. Placeholder scan:** TBD/TODO нет. Все шаги содержат конкретные файлы/строки/команды. Для Task 4 Step 4/5 (proxyForCmd/env-injection) указан принцип + «свериться с текущей логикой» — это намеренно: точный diff зависит от того, как `runnerFor` передаёт команду, и требует проверки по коду в момент исполнения (рекомендуется исполнительскому сабагенту прочитать `orchestrator.go` перед правкой).

**3. Type consistency:** `buildWrapperSpec(cmd, recipe)` — сигнатура без proxy-параметров используется единообразно в Task 4 Step 1 и Step 2. `WrapperSpec.BaseURL = recipe.URL` — прямо. `Options` без `ProxyURL`/`ProxyShimDir` — единообразно. `Config` без `Proxy`/`Pricing`/`Accounting` — Task 5.

**Компилируемость переходов:** Task 2 (accounting consumers убраны) → `pkg/accounting` живёт, компилируется. Task 4 (proxy threading убран) → `pkg/proxy` живёт (используется accounting), компилируется. Task 5 (config) → поля не нужны. Task 6 (удалить пакеты) → никто не ссылается. ✓
