# Дизайн: единые константы имени команды/типа рецепта/auth env vars

**Дата:** 2026-07-28
**Статус:** согласован, готов к плану реализации

## Контекст

Продолжение обзора кодовой базы на дублирование (см. закрытые `docs/superpowers/specs/2026-07-28-plan-version-scan-dedup-design.md` и `docs/superpowers/specs/2026-07-28-phase-filename-source-of-truth-design.md`). Приоритет 1 — исходный пример, с которого начался весь обзор: `defaultCommand = "claude"` (`pkg/executor/executor.go:43`) и `claudeCommand = "claude"` (`pkg/docker/launcher.go:46`) — та же строка, независимо определённая в двух местах.

Копнув шире, нашлось ещё 3 связанных дублирования той же природы:
- `pkg/docker/wrapper.go` определяет свои `WrapperTypeOpenAI = "openai"` / `WrapperTypeCursor = "cursor"` — те же значения, что уже есть (неэкспортируемые) `recipeTypeOpenAI`/`recipeTypeCursor` в `pkg/config/config.go:72-73`.
- `pkg/docker/launcher.go` определяет свой `claudeAuthEnvVars` (3 значения) — идентичен уже экспортируемому `config.ClaudeAuthEnvVars` (`pkg/config/config.go:96-100`).
- Тот же `launcher.go` (строка ~305) содержит **третий**, отличающийся список из 4 значений (добавлен `ANTHROPIC_BASE_URL`) для другой цели — проброс env в Docker-контейнер, а не валидация auth.

**Важный нюанс, выявленный в обсуждении:** не всё, что выглядит как «одна и та же строка claude», — на самом деле один и тот же концепт. `AgentRecipe.Type`/`WrapperSpec.Type == "claude"` (тег стратегии генерации враппера) и «имя реального CLI-бинарника по умолчанию» — это в данном случае действительно один и тот же референт (recipe-тип «claude» в буквальном смысле означает «сгенерировать враппер, который в итоге exec'ает бинарник claude»), поэтому здесь безопасно свести к ОДНОЙ константе. Это отличается от случая `autonomousLabel` (предыдущая задача), где два разных, не связанных по смыслу словаря («имя фазы» и «лейбл supervisor-track») случайно совпадали по строке — там пришлось оставить два разных смысла раздельными. Здесь — реально один смысл, поэтому дублирование безопасно устранять слиянием в одну константу.

**Проверено отдельно (по вопросу пользователя):** сама логика резолва «какая команда реально запускается / нужен ли ей враппер-шim» уже консолидирована в одном пайплайне (`docker.UsedRecipes` → `buildWrapperSpec` в `cmd/afm/run.go`, единственное место, собирающее `docker.WrapperSpec{}`, — уже отрефакторено в прошлом с явным комментарием об этом → `docker.CreateWrappers`/`generateWrapper` → `docker.ScanCommands`). Дублирования в этой логике нет, эта спека её не трогает — только константы, которые эта логика использует.

## Решение

Всё сводится к `pkg/config` — единственному пакету среди затронутых, который сегодня уже не имеет ни одной внутренней (`afm/pkg/*`) зависимости (чистый leaf), при этом `pkg/docker` уже импортирует `pkg/config`. `pkg/executor` сегодня не зависит ни от `config`, ни от `docker` — этот рефакторинг добавляет ему новую, безопасную (без риска цикла) зависимость от `pkg/config`.

### 1. `pkg/config/config.go`

```go
// ClaudeCommand — каноническое имя CLI-агента по умолчанию. Одновременно:
// (а) имя реального бинарника, который ищется в PATH/exec'ается, когда
// команда стадии не указана явно; (б) значение AgentRecipe.Type/
// WrapperSpec.Type для «claude»-рецепта (пустой Type тоже означает claude) —
// это не совпадение, а один и тот же референт: recipe-тип "claude" в
// буквальном смысле означает «сгенерировать враппер, exec'ающий этот бинарник».
const ClaudeCommand = "claude"

// RecipeTypeOpenAI/RecipeTypeCursor — остальные допустимые значения
// AgentRecipe.Type/WrapperSpec.Type (экспортированы из recipeTypeOpenAI/
// recipeTypeCursor, чтобы pkg/docker/wrapper.go могло их переиспользовать
// вместо собственных WrapperTypeOpenAI/WrapperTypeCursor).
const (
	RecipeTypeOpenAI = "openai"
	RecipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
)
```

- `recipeTypeClaude` (строка 71) удаляется — заменяется на `ClaudeCommand` везде, где использовался (строка 116 switch, единственное место).
- `recipeTypeOpenAI`/`recipeTypeCursor` переименовываются в экспортированные (строки 72-73, 116, 128) — просто рескоуп видимости, значения и логика не меняются.
- Бинарный литерал `Client: ClientConfig{Command: "claude"}` (строка 236, `Default()`) → `Client: ClientConfig{Command: ClaudeCommand}`.
- `ClaudeAuthEnvVars` (строки 94-100) — без изменений, уже экспортирована.

### 2. `pkg/executor/executor.go`

```go
import (
	...
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/progress"
)
```

`const defaultCommand = "claude"` (строка 43) удаляется. Единственное место использования:
```go
if cfg.Command == "" {
    cfg.Command = defaultCommand
}
```
→
```go
if cfg.Command == "" {
    cfg.Command = config.ClaudeCommand
}
```

### 3. `pkg/docker/launcher.go`

`const claudeCommand = "claude"` (строка 46) удаляется. Оба использования:
```go
if clientCommand != claudeCommand && clientCommand != "" {
```
→
```go
if clientCommand != config.ClaudeCommand && clientCommand != "" {
```
и (строка ~194)
```go
if cmd == "" || cmd == claudeCommand {
```
→
```go
if cmd == "" || cmd == config.ClaudeCommand {
```

`var claudeAuthEnvVars = []string{...}` (строки 51-55) удаляется целиком. Единственное использование (цикл `for _, key := range claudeAuthEnvVars`) →
```go
for _, key := range config.ClaudeAuthEnvVars {
```

Список для проброса env в контейнер (строка ~305, отдельная цель — не auth-валидация, а «что прокинуть в docker run»):
```go
for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"} {
```
→
```go
dockerForwardEnvVars := append([]string{"ANTHROPIC_BASE_URL"}, config.ClaudeAuthEnvVars...)
for _, key := range dockerForwardEnvVars {
```
(`append` на свежий литерал `[]string{"ANTHROPIC_BASE_URL"}` не аляйзит backing-array `config.ClaudeAuthEnvVars` — безопасно.) Если `config.ClaudeAuthEnvVars` когда-нибудь получит 4-й auth-var, этот список автоматически начнёт его пробрасывать — это семантически верно (любой принимаемый auth-var должен пробрасываться).

`pkg/docker` уже импортирует `pkg/config` — новых импортов не требуется.

### 4. `pkg/docker/wrapper.go` (+ `wrapper_test.go`)

`const WrapperTypeOpenAI = "openai"` и `const WrapperTypeCursor = "cursor"` (строки 12-19) удаляются. Все использования в `wrapper.go` заменяются на `config.RecipeTypeOpenAI`/`config.RecipeTypeCursor`.

`exec.LookPath("claude")` (строка 65) → `exec.LookPath(config.ClaudeCommand)`.

Новый импорт `"github.com/akopichin/afm/pkg/config"` в `wrapper.go`.

`wrapper_test.go` — белобоксовый тест (`package docker`), ссылается на `WrapperTypeOpenAI`/`WrapperTypeCursor` в 5 местах (строки 146, 185, 198, 211, 252) — заменяются на `config.RecipeTypeOpenAI`/`config.RecipeTypeCursor`, новый импорт `config` добавляется в тестовый файл.

Никаких внешних потребителей `docker.WrapperTypeOpenAI`/`docker.WrapperTypeCursor` за пределами `pkg/docker` нет (проверено grep'ом по всему репо) — переименование безопасно.

## Совместимость с существующими тестами

- `pkg/config`: `recipeTypeClaude`/`recipeTypeOpenAI`/`recipeTypeCursor` нигде не используются в `config_test.go` напрямую (тесты идут через YAML-строки) — изменений в тестах не требуется, кроме проверки что существующие тесты (`TestRecipe*`, если есть) продолжают проходить.
- `pkg/executor`: `executor_test.go` — чёрнобоксовый пакет (`package executor_test`), не ссылается на `defaultCommand` напрямую — изменений не требуется.
- `pkg/docker`: `launcher_test.go` — чёрнобоксовый (`package docker_test`), не ссылается на `claudeCommand`/`claudeAuthEnvVars` напрямую — изменений не требуется. `wrapper_test.go` — белобоксовый, требует замены 5 вхождений (см. выше).

## Тестирование

- Новый тест в `pkg/config`: `TestClaudeCommandMatchesDefaultRecipeType` (или расширение существующего теста Validate) — подтверждает, что `AgentRecipe{Type: ""}` и `AgentRecipe{Type: config.ClaudeCommand}` оба проходят валидацию как claude-тип (документирует инвариант «пустой Type == ClaudeCommand»).
- Существующие тесты (`pkg/config`, `pkg/executor`, `pkg/docker`) должны продолжать проходить без функциональных изменений — это чистый рефакторинг видимости/источника констант, поведение не меняется нигде, кроме списка env vars для Docker-проброса (который теперь автоматически включает любой будущий auth-var — расширение, не сужение).

## Out of scope

- Остальные пункты категории 1 из исходного обзора: имена тем скина (`goga`/`novacorps`/`coffee`, дублируются в `pkg/config`/`pkg/server`), `.afm`/`runs`-путь (`filepath.Join(fmDir(), "runs")`, 5 мест в `cmd/afm`), идентичный таймаут по умолчанию (30 мин) в двух местах — отдельные последующие спеки.
- Фронтендовая часть обзора (`EventFeedPanel.tsx` hardcoded status-классы, CSS-дублирование статусов) — не Go, отдельный трек.
