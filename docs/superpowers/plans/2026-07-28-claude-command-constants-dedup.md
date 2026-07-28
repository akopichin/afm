# Единые константы ClaudeCommand/RecipeType*/ClaudeAuthEnvVars — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Убрать дублирование строки `"claude"` (имя CLI-бинарника по умолчанию, оно же значение recipe-типа) и связанных с ней auth env var списков, независимо реализованных в 4 местах 3 пакетов, сведя всё к константам в `pkg/config`.

**Architecture:** `pkg/config` — единственный из четырёх затронутых пакетов без внутренних (`afm/pkg/*`) зависимостей (чистый leaf); `pkg/docker` уже импортирует `pkg/config`. `pkg/executor` сегодня не зависит ни от чего внутреннего — этот план добавляет ему новую, безопасную (без риска цикла — `config` ничего из `afm` не импортирует) зависимость от `pkg/config`.

**Tech Stack:** Go 1.26.4, стандартная библиотека — без новых внешних зависимостей.

## Global Constraints

- `config.ClaudeCommand = "claude"` — новая экспортируемая константа в `pkg/config/config.go`, заменяет неэкспортируемый `recipeTypeClaude`. Используется и как имя CLI-бинарника по умолчанию, и как значение `AgentRecipe.Type`/`WrapperSpec.Type` для «claude»-рецепта — это один и тот же референт (recipe-тип «claude» буквально означает «сгенерировать враппер, exec'ающий этот бинарник»), а не случайное совпадение двух разных словарей — поэтому одна константа, а не две.
- `config.RecipeTypeOpenAI = "openai"`, `config.RecipeTypeCursor = "cursor"` — экспортированные версии существующих неэкспортируемых `recipeTypeOpenAI`/`recipeTypeCursor`. Значения и вся логика валидации не меняются, только видимость.
- `config.ClaudeAuthEnvVars` (уже экспортирована, без изменений) переиспользуется в `pkg/docker/launcher.go` вместо локальной копии `claudeAuthEnvVars`.
- Список env vars для проброса в Docker-контейнер (`pkg/docker/launcher.go`, отдельная от auth-валидации цель) строится как `append([]string{"ANTHROPIC_BASE_URL"}, config.ClaudeAuthEnvVars...)`, а не переписывается вручную — если `ClaudeAuthEnvVars` когда-нибудь получит новый элемент, проброс подхватит его автоматически.
- `pkg/docker/wrapper.go` теряет свои `WrapperTypeOpenAI`/`WrapperTypeCursor` — заменяются на `config.RecipeTypeOpenAI`/`config.RecipeTypeCursor` везде, включая белобоксовый `wrapper_test.go` (5 вхождений).
- Публичные сигнатуры всех затронутых функций не меняются — только источник констант.
- Никаких внешних потребителей `docker.WrapperTypeOpenAI`/`docker.WrapperTypeCursor` за пределами `pkg/docker` нет (проверено по всему репо) — переименование безопасно, без residual-ссылок.

---

### Task 1: `pkg/config/config.go` — `ClaudeCommand` + экспорт `RecipeTypeOpenAI`/`RecipeTypeCursor`

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go` (новый тест `TestAgentRecipe_ClaudeType`)

**Interfaces:**
- Consumes: ничего из предыдущих задач (первая задача).
- Produces: `const ClaudeCommand = "claude"`, `const RecipeTypeOpenAI = "openai"`, `const RecipeTypeCursor = "cursor"` в пакете `config` — используются во всех последующих задачах.

- [ ] **Step 1: Заменить блок констант recipe-типов**

Текущий блок (строки ~70-74):

```go
// Допустимые значения AgentRecipe.Type.
const (
	recipeTypeClaude = "claude"
	recipeTypeOpenAI = "openai"
	recipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
)
```

Заменить на:

```go
// ClaudeCommand — каноническое имя CLI-агента по умолчанию. Одновременно:
// (а) имя реального бинарника, который ищется в PATH/exec'ается, когда
// команда стадии не указана явно; (б) значение AgentRecipe.Type/
// WrapperSpec.Type для «claude»-рецепта (пустой Type тоже означает claude) —
// это не совпадение, а один и тот же референт: recipe-тип "claude" в
// буквальном смысле означает «сгенерировать враппер, exec'ающий этот бинарник».
const ClaudeCommand = "claude"

// RecipeTypeOpenAI/RecipeTypeCursor — остальные допустимые значения
// AgentRecipe.Type/WrapperSpec.Type (экспортированы, чтобы pkg/docker/wrapper.go
// могло их переиспользовать вместо собственных WrapperTypeOpenAI/WrapperTypeCursor).
const (
	RecipeTypeOpenAI = "openai"
	RecipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
)
```

- [ ] **Step 2: Обновить оба использования в `Validate()`**

Текущая строка ~116:
```go
	case "", recipeTypeClaude, recipeTypeOpenAI, recipeTypeCursor:
```
→
```go
	case "", ClaudeCommand, RecipeTypeOpenAI, RecipeTypeCursor:
```

Текущая строка ~128:
```go
	if r.Type == recipeTypeOpenAI || r.Type == recipeTypeCursor {
```
→
```go
	if r.Type == RecipeTypeOpenAI || r.Type == RecipeTypeCursor {
```

- [ ] **Step 3: Обновить `Default()`**

Текущая строка ~236:
```go
		Client:   ClientConfig{Command: "claude"},
```
→
```go
		Client:   ClientConfig{Command: ClaudeCommand},
```

- [ ] **Step 4: Собрать и прогнать пакет**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./pkg/config/... && go test ./pkg/config/... -v`
Expected: чистая сборка, все существующие тесты (включая `TestAgentRecipe_OpenAIType`, `TestAgentRecipe_CursorType`, `TestDockerAutoShim_ParseAndValidate`) проходят без изменений в этих тестах — они используют строковые литералы `"openai"`/`"cursor"`, а не имена констант, так что переименование их не задевает.

- [ ] **Step 5: Написать тест, документирующий инвариант claude-типа**

Добавить в `pkg/config/config_test.go`, рядом с `TestAgentRecipe_OpenAIType`/`TestAgentRecipe_CursorType`:

```go
func TestAgentRecipe_ClaudeType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "empty Type behaves as claude",
			recipe: config.AgentRecipe{
				Type:  "",
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:CLAUDE_CODE_OAUTH_TOKEN"},
			},
		},
		{
			name: "explicit ClaudeCommand as Type behaves the same as empty",
			recipe: config.AgentRecipe{
				Type:  config.ClaudeCommand,
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:CLAUDE_CODE_OAUTH_TOKEN"},
			},
		},
		{
			name: "claude: auth.to restricted to ClaudeAuthEnvVars (unlike openai/cursor)",
			recipe: config.AgentRecipe{
				Type:  config.ClaudeCommand,
				Model: "claude-sonnet-4-5",
				Auth:  config.RecipeAuth{From: "env:TOKEN", To: "env:SOME_OTHER_VAR"},
			},
			errSub: "is not one of",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.recipe.Validate()
			if tc.errSub == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("Validate() = %v, want error containing %q", err, tc.errSub)
			}
		})
	}
}
```

(`"strings"` уже импортирован в `config_test.go` — используется существующими тестами с `errSub`/`strings.Contains`, включая `TestAgentRecipe_OpenAIType`. Новых импортов не требуется.)

- [ ] **Step 6: Запустить новый тест**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/config/... -run TestAgentRecipe_ClaudeType -v`
Expected: PASS — все 3 сабтеста.

- [ ] **Step 7: Прогнать весь пакет + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/config/... -v && go vet ./pkg/config/...`
Expected: всё зелёное.

- [ ] **Step 8: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): ClaudeCommand + экспорт RecipeTypeOpenAI/RecipeTypeCursor"
```

---

### Task 2: `pkg/executor/executor.go` — использовать `config.ClaudeCommand`

**Files:**
- Modify: `pkg/executor/executor.go`

**Interfaces:**
- Consumes: `config.ClaudeCommand` из Task 1.
- Produces: ничего нового для последующих задач.

- [ ] **Step 1: Добавить импорт `config`**

Текущий блок импорта в начале `pkg/executor/executor.go`:

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)
```

Заменить на (добавлена строка `"github.com/akopichin/afm/pkg/config"`, алфавитный порядок перед `progress`):

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/progress"
)
```

- [ ] **Step 2: Удалить `defaultCommand`, использовать `config.ClaudeCommand`**

Текущая строка ~43:
```go
const defaultCommand = "claude"
```
удаляется целиком.

Текущие строки ~96-98:
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

- [ ] **Step 3: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка (подтверждает отсутствие цикла зависимостей и что `defaultCommand` больше нигде в файле не используется).

- [ ] **Step 4: Прогнать пакет executor + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/executor/... -v && go vet ./pkg/executor/...`
Expected: всё зелёное — `executor_test.go` чёрнобоксовый (`package executor_test`), не ссылается на `defaultCommand` напрямую, изменений в тестах не требуется.

- [ ] **Step 5: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/executor/executor.go
git commit -m "refactor(executor): дефолтная команда берётся из config.ClaudeCommand"
```

---

### Task 3: `pkg/docker/launcher.go` — использовать `config.ClaudeCommand`/`config.ClaudeAuthEnvVars`

**Files:**
- Modify: `pkg/docker/launcher.go`

**Interfaces:**
- Consumes: `config.ClaudeCommand`, `config.ClaudeAuthEnvVars` из Task 1 (`pkg/docker` уже импортирует `pkg/config` — новых импортов не требуется).
- Produces: ничего нового для последующих задач.

- [ ] **Step 1: Удалить локальные `claudeCommand`/`claudeAuthEnvVars`**

Текущий блок (строки ~46-55):

```go
const claudeCommand = "claude"

// claudeAuthEnvVars — env vars, через которые claude CLI принимает токены в Docker.
// macOS хранит OAuth-токены в Keychain, который недоступен из Linux-контейнера;
// поэтому auth должна идти через один из этих env vars.
var claudeAuthEnvVars = []string{
	"CLAUDE_CODE_OAUTH_TOKEN", // long-lived token: `claude setup-token`
	"ANTHROPIC_API_KEY",       // API-ключ Anthropic
	"ANTHROPIC_AUTH_TOKEN",    // auth token для кастомных шлюзов
}
```

удаляется целиком (обе декларации).

- [ ] **Step 2: Заменить использования в `CheckClaudeDockerAuth`**

Текущая строка ~62:
```go
	if clientCommand != claudeCommand && clientCommand != "" {
```
→
```go
	if clientCommand != config.ClaudeCommand && clientCommand != "" {
```

Текущая строка ~65:
```go
	for _, key := range claudeAuthEnvVars {
```
→
```go
	for _, key := range config.ClaudeAuthEnvVars {
```

- [ ] **Step 3: Заменить использование в `ScanCommands`**

Текущая строка ~194:
```go
		if cmd == "" || cmd == claudeCommand {
```
→
```go
		if cmd == "" || cmd == config.ClaudeCommand {
```

- [ ] **Step 4: Заменить список проброса env vars в Docker-контейнер**

Текущая строка ~305:
```go
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN"} {
```
→
```go
	dockerForwardEnvVars := append([]string{"ANTHROPIC_BASE_URL"}, config.ClaudeAuthEnvVars...)
	for _, key := range dockerForwardEnvVars {
```

(`append` на свежий литерал `[]string{"ANTHROPIC_BASE_URL"}` не аляйзит backing-array `config.ClaudeAuthEnvVars` — безопасно. Порядок значений в итоговом списке меняется, но порядок здесь не имеет значения — цикл просто проверяет `os.Getenv(key) != ""` для каждого.)

- [ ] **Step 5: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка.

- [ ] **Step 6: Прогнать пакет docker + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/docker/... -v && go vet ./pkg/docker/...`
Expected: всё зелёное — `launcher_test.go` чёрнобоксовый (`package docker_test`), не ссылается на `claudeCommand`/`claudeAuthEnvVars` напрямую, изменений в тестах не требуется.

- [ ] **Step 7: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/docker/launcher.go
git commit -m "refactor(docker): launcher.go использует config.ClaudeCommand/ClaudeAuthEnvVars"
```

---

### Task 4: `pkg/docker/wrapper.go` + `wrapper_test.go` — использовать `config.RecipeTypeOpenAI`/`RecipeTypeCursor`/`ClaudeCommand`

**Files:**
- Modify: `pkg/docker/wrapper.go`
- Modify: `pkg/docker/wrapper_test.go`

**Interfaces:**
- Consumes: `config.RecipeTypeOpenAI`, `config.RecipeTypeCursor`, `config.ClaudeCommand` из Task 1.
- Produces: ничего для последующих задач (финальная задача плана).

`pkg/docker/wrapper.go` сегодня НЕ импортирует `pkg/config` (только stdlib) — нужно добавить импорт. `wrapper_test.go` — белобоксовый тест (`package docker`), тоже нужен новый импорт.

- [ ] **Step 1: Добавить импорт `config` в `wrapper.go`**

Текущий блок импорта:

```go
import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)
```

Заменить на:

```go
import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/config"
)
```

- [ ] **Step 2: Удалить `WrapperTypeOpenAI`/`WrapperTypeCursor`**

Текущий блок (строки ~12-19):

```go
// WrapperTypeOpenAI — WrapperSpec.Type value that selects the OpenAI-compatible
// template (OPENAI_* vars + exec openai-as-claude). Empty / "claude" selects the
// claude template.
const WrapperTypeOpenAI = "openai"

// WrapperTypeCursor — Cursor Cloud Agents API template (CURSOR_* vars + exec
// cursor-as-claude). Cursor не имеет синхронного /chat/completions — это
// асинхронный run-based API, поэтому использует свой адаптер, не claude.
const WrapperTypeCursor = "cursor"
```

удаляется целиком (обе константы вместе с их doc-комментариями — объяснения по существу дублируют комментарии, уже стоящие непосредственно на местах использования ниже, см. Step 3, так что информация не теряется).

- [ ] **Step 3: Заменить 3 использования в `wrapper.go`**

Строка ~66, внутри `CreateWrappers`:
```go
		if s.Type != WrapperTypeOpenAI && s.Type != WrapperTypeCursor {
```
→
```go
		if s.Type != config.RecipeTypeOpenAI && s.Type != config.RecipeTypeCursor {
```

Строка ~101, внутри `generateWrapper` (комментарий на следующей строке про openai-as-claude не трогается):
```go
	if s.Type == WrapperTypeOpenAI {
```
→
```go
	if s.Type == config.RecipeTypeOpenAI {
```

Строка ~118, внутри `generateWrapper` (комментарий про cursor Cloud Agents API не трогается):
```go
	if s.Type == WrapperTypeCursor {
```
→
```go
	if s.Type == config.RecipeTypeCursor {
```

- [ ] **Step 4: Заменить `exec.LookPath("claude")`**

Текущая строка ~65:
```go
			p, err := exec.LookPath("claude")
```
→
```go
			p, err := exec.LookPath(config.ClaudeCommand)
```

- [ ] **Step 5: Собрать проект**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./...`
Expected: чистая сборка.

- [ ] **Step 6: Обновить `wrapper_test.go`**

Текущий блок импорта:

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

Заменить на (добавлена группа с внутренним импортом):

```go
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
)
```

Заменить все 5 вхождений (используй поиск по `WrapperTypeOpenAI`/`WrapperTypeCursor` в файле, чтобы найти все, включая ориентировочные строки ~146, 185, 198, 211, 252):
- `WrapperTypeOpenAI` → `config.RecipeTypeOpenAI`
- `WrapperTypeCursor` → `config.RecipeTypeCursor`

- [ ] **Step 7: Прогнать пакет docker + go vet**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go test ./pkg/docker/... -v && go vet ./pkg/docker/...`
Expected: всё зелёное, включая тесты в `wrapper_test.go` (`TestCreateWrappers*` и др.).

- [ ] **Step 8: Полный прогон проекта**

Run: `cd /Users/alexander.kopichin/work/personal/afm && go build ./... && go vet ./... && go test ./... -race`
Expected: все пакеты зелёные — это последняя задача плана, финальная проверка, что новые зависимости (`executor`→`config`, `wrapper.go`→`config`) не сломали ничего в остальном проекте.

- [ ] **Step 9: Commit**

```bash
cd /Users/alexander.kopichin/work/personal/afm
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "refactor(docker): wrapper.go использует config.RecipeTypeOpenAI/RecipeTypeCursor/ClaudeCommand"
```
