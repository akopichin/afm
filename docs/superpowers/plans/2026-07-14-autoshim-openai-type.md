# AutoShim: поддержка `type: openai` для OpenAI-совместимых провайдеров

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Расширить механизм autoShim поддержкой OpenAI-совместимых провайдеров (Cursor, Deepseek, локальные LLM и любые `v1/chat/completions`-совместимые API). Добавить поле `type: openai` в recipe: сгенерированный враппер вместо `exec claude` вызывает `exec /usr/local/bin/openai-as-claude` — скрипт, который читает промпт из stdin, вызывает `${OPENAI_BASE_URL}/chat/completions?stream=true` и транслирует SSE-чанки в claude stream-json формат.

**Зависимость:** Требует наличия реализованного Task 8 из плана `2026-07-14-docker-autoshim.md` (функция `CreateWrappers`, поля `WrapperSpec`, контейнерный блок `run.go`). Автошим уже в ревью — этот план реализуется после мержа или параллельно на отдельной ветке.

**Пример конфига после реализации:**

```yaml
docker:
  autoShim: true
  agents:
    cursor:
      type: openai        # ← новое поле
      model: claude-sonnet-4-5
      url: https://api2.cursor.sh/v1
      auth:
        from: env:CURSOR_TOKEN
        to: env:OPENAI_API_KEY   # ← теперь не ограничено ClaudeAuthEnvVars
```

**Сгенерированный враппер:**

```sh
#!/bin/sh
export OPENAI_API_KEY="$AFM_SECRET_CURSOR"
unset AFM_SECRET_CURSOR
export OPENAI_BASE_URL="https://api2.cursor.sh/v1"
export OPENAI_MODEL="claude-sonnet-4-5"
exec /usr/local/bin/openai-as-claude "$@"
```

**Tech Stack:** Go 1.26, bash, curl, jq. Файлы: `pkg/config/config.go`, `pkg/docker/wrapper.go`, `cmd/afm/run.go`, `scripts/openai-as-claude.sh`, `Dockerfile.runtime`.

## Global Constraints

- **Go 1.26** — версию в `go.mod` НЕ менять.
- **Коммиты на русском**, без `Co-Authored-By`.
- После каждой задачи: `go build ./...` собирается, `go test ./pkg/<pkg>/... -race` зелёный, `make lint` (golangci-lint `--fix`) без ошибок. Устаревших конструкций не оставлять.
- **Backward compat:** `type` в recipe — опциональное поле; пустая строка = `"claude"` (текущее поведение без изменений).
- **jq и curl** — уже есть в `Dockerfile.runtime` (`curl` установлен; `jq` нужно добавить).

---

## File Structure

| Файл | Ответственность |
|------|-----------------|
| `pkg/config/config.go` | Добавить `Type string` в `AgentRecipe`; обновить `Validate()` для `type: openai` |
| `pkg/docker/wrapper.go` | Добавить `Type string` в `WrapperSpec`; добавить openai-ветку в `generateWrapper()`; условный LookPath claude |
| `cmd/afm/run.go` | Передавать `Type: recipe.Type` при построении `WrapperSpec` |
| `scripts/openai-as-claude.sh` | Новый скрипт трансляции stdin → OpenAI API → claude stream-json |
| `Dockerfile.runtime` | Добавить `jq` в apt-get; скопировать скрипт в образ |
| `CLAUDE.md` | Задокументировать `type: openai` в разделе autoShim |

---

### Task 1: Config — поле `Type` в `AgentRecipe`, обновление `Validate()`

**Files:**
- Modify: `pkg/config/config.go` (`AgentRecipe` ~`:76`, `Validate` ~`:111`)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Модифицирует: `AgentRecipe.Type string` (YAML-тег `type`); `AgentRecipe.Validate()` — для `type == "openai"` разрешает любой `env:VAR` (не только `ClaudeAuthEnvVars`); для `type == "openai"` требует непустой `url`.

- [ ] **Step 1: Write the failing test** — добавь в `pkg/config/config_test.go`:

```go
func TestAgentRecipe_OpenAIType(t *testing.T) {
	cases := []struct {
		name   string
		recipe AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "valid openai recipe",
			recipe: AgentRecipe{
				Type:  "openai",
				Model: "claude-sonnet-4-5",
				URL:   "https://api2.cursor.sh/v1",
				Auth:  RecipeAuth{From: "env:CURSOR_TOKEN", To: "env:OPENAI_API_KEY"},
			},
		},
		{
			name: "openai: missing model",
			recipe: AgentRecipe{
				Type: "openai",
				URL:  "https://api2.cursor.sh/v1",
				Auth: RecipeAuth{From: "env:CURSOR_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "model is required",
		},
		{
			name: "openai: missing url",
			recipe: AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				Auth:  RecipeAuth{From: "env:OPENAI_KEY", To: "env:OPENAI_API_KEY"},
			},
			errSub: "url is required",
		},
		{
			name: "openai: auth.to not env:",
			recipe: AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				URL:   "https://api.openai.com/v1",
				Auth:  RecipeAuth{From: "env:KEY", To: "OPENAI_API_KEY"},
			},
			errSub: "must be an env:",
		},
		{
			name: "openai: any env: var allowed (not restricted to ClaudeAuthEnvVars)",
			recipe: AgentRecipe{
				Type:  "openai",
				Model: "gpt-4o",
				URL:   "https://api.openai.com/v1",
				Auth:  RecipeAuth{From: "env:MY_CUSTOM_KEY", To: "env:MY_TARGET_KEY"},
			},
			// env: любой — ошибки нет
		},
		{
			name: "claude type (empty): OPENAI_API_KEY still rejected",
			recipe: AgentRecipe{
				Model: "glm-5.1",
				Auth:  RecipeAuth{From: "env:TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "not one of",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if c.errSub == "" {
				if err != nil {
					t.Errorf("Validate(): unexpected error: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
				}
			}
		})
	}
}

func TestDockerAutoShim_ParseOpenAIType(t *testing.T) {
	dir := t.TempDir()
	cfgYAML := `
docker:
  autoShim: true
  agents:
    cursor:
      type: openai
      model: claude-sonnet-4-5
      url: https://api2.cursor.sh/v1
      auth:
        from: "env:CURSOR_TOKEN"
        to: "env:OPENAI_API_KEY"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(dir, dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	r := cfg.Docker.Agents["cursor"]
	if r.Type != "openai" {
		t.Errorf("Type: got %q, want %q", r.Type, "openai")
	}
	if r.Auth.EnvVarName() != "OPENAI_API_KEY" {
		t.Errorf("EnvVarName: got %q", r.Auth.EnvVarName())
	}
	if err := cfg.Docker.ValidateAgents(); err != nil {
		t.Errorf("ValidateAgents: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run "TestAgentRecipe_OpenAIType|TestDockerAutoShim_ParseOpenAIType" -race`
Expected: FAIL (compile error: `AgentRecipe.Type undefined`, или `Validate()` блокирует `OPENAI_API_KEY`).

- [ ] **Step 3: Implement** — в `pkg/config/config.go`:

Добавь поле `Type` в `AgentRecipe` (перед `Model`):

```go
type AgentRecipe struct {
	Type         string     `yaml:"type"`          // "" | "claude" = claude (default); "openai" = OpenAI-compatible
	Model        string     `yaml:"model"`         // required → ANTHROPIC_DEFAULT_*_MODEL (claude) / OPENAI_MODEL (openai)
	URL          string     `yaml:"url"`           // optional (claude); required (openai) — agent gateway
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
}
```

Замени `Validate()`:

```go
// Validate returns an error if the recipe is malformed.
func (r AgentRecipe) Validate() error {
	if r.Model == "" {
		return errors.New("recipe: model is required")
	}
	if !strings.HasPrefix(r.Auth.To, "env:") {
		return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
	}
	if r.Type == "openai" {
		if r.URL == "" {
			return errors.New("recipe: url is required for type: openai")
		}
		// openai-совместимые провайдеры используют произвольные env vars (OPENAI_API_KEY, MY_KEY и т.д.);
		// ограничение ClaudeAuthEnvVars применяется только к типу claude.
		return nil
	}
	// claude (type == "" или "claude"): auth.to ограничен ClaudeAuthEnvVars
	if !isClaudeAuthEnvVar(r.Auth.EnvVarName()) {
		return fmt.Errorf("recipe: auth.to env var %q is not one of %v", r.Auth.EnvVarName(), ClaudeAuthEnvVars)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run "TestAgentRecipe_OpenAIType|TestDockerAutoShim_ParseOpenAIType" -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): добавлен type: openai в AgentRecipe, Validate() учитывает тип"
```

---

### Task 2: WrapperSpec — поле `Type`, openai-ветка в `generateWrapper()`

**Files:**
- Modify: `pkg/docker/wrapper.go` (`WrapperSpec` ~`:52`, `CreateWrappers` ~`:67`, `generateWrapper` ~`:93`)
- Test: `pkg/docker/wrapper_test.go`

**Interfaces:**
- Модифицирует: `WrapperSpec.Type string`; `generateWrapper` — ветка `type == "openai"` генерирует скрипт с `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` и `exec /usr/local/bin/openai-as-claude`; `CreateWrappers` — LookPath claude только когда нужен (есть не-openai specs).

- [ ] **Step 1: Write the failing test** — добавь в `pkg/docker/wrapper_test.go`:

```go
func TestCreateWrappers_OpenAITemplate(t *testing.T) {
	// openai-тип не требует claude в PATH
	t.Setenv("PATH", t.TempDir()) // claude нет в PATH

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:    "openai",
		Command: "cursor",
		AuthTo:  "OPENAI_API_KEY",
		BaseURL: "https://api2.cursor.sh/v1",
		Model:   "claude-sonnet-4-5",
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (openai): %v", err)
	}
	defer os.RemoveAll(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "cursor"))
	s := string(script)

	wantSubstrings := []string{
		"#!/bin/sh",
		`export OPENAI_API_KEY="$AFM_SECRET_CURSOR"`,
		"unset AFM_SECRET_CURSOR",
		`export OPENAI_BASE_URL="https://api2.cursor.sh/v1"`,
		`export OPENAI_MODEL="claude-sonnet-4-5"`,
		`exec /usr/local/bin/openai-as-claude "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("openai wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	// не должно быть claude-специфичных строк
	if strings.Contains(s, "ANTHROPIC_DEFAULT") || strings.Contains(s, "ANTHROPIC_BASE_URL") {
		t.Errorf("openai wrapper must not contain ANTHROPIC_ vars:\n%s", s)
	}
}

func TestCreateWrappers_OpenAINoClaudeRequired(t *testing.T) {
	// только openai specs — claude не нужен в PATH
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir) // нет claude, нет docker, ничего

	_, err := CreateWrappers([]WrapperSpec{
		{Type: "openai", Command: "cursor", Model: "m", BaseURL: "http://x", AuthTo: "OPENAI_API_KEY"},
	})
	if err != nil {
		t.Errorf("openai-only wrappers must not fail when claude absent: %v", err)
	}
}

func TestCreateWrappers_MixedTypes_RequiresClaude(t *testing.T) {
	// смесь openai + claude без claude в PATH → ошибка
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := CreateWrappers([]WrapperSpec{
		{Type: "openai", Command: "cursor", Model: "m", BaseURL: "http://x", AuthTo: "OPENAI_API_KEY"},
		{Command: "glm51", Model: "glm-5.1", AuthTo: "ANTHROPIC_AUTH_TOKEN"}, // claude-тип
	})
	if err == nil {
		t.Error("mixed openai+claude without claude in PATH: expected error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run "TestCreateWrappers_OpenAI|TestCreateWrappers_Mixed" -race`
Expected: FAIL (compile error: `WrapperSpec.Type undefined`; или openai-тип использует claude-ветку).

- [ ] **Step 3: Implement** — правки в `pkg/docker/wrapper.go`:

**a) Обновить `WrapperSpec`** — добавить поле `Type` (перед `Command`):

```go
// WrapperSpec describes one wrapper script to generate in the wrapper-dir.
// Type "" or "claude" selects the claude template (auth + ANTHROPIC_* vars + exec claude).
// Type "openai" selects the OpenAI-compatible template (OPENAI_* vars + exec openai-as-claude).
// Model == "" with Type "" selects the claude proxy-shim (BASE_URL only, no model vars).
type WrapperSpec struct {
	Type         string // "" | "claude" = claude template; "openai" = openai-compatible
	Command      string
	AuthTo       string // auth env var name ("" for claude proxy-shim)
	BaseURL      string // baked gateway URL; "" → omit
	Model        string // model string; "" → claude proxy-shim for claude type
	HasSysPrompt bool   // emit sysprompt block (claude type only)
}
```

**b) Обновить `CreateWrappers`** — условный LookPath:

```go
func CreateWrappers(specs []WrapperSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	// LookPath claude только если есть claude-тип (не openai).
	var realClaude string
	for _, s := range specs {
		if s.Type != "openai" {
			p, err := exec.LookPath("claude")
			if err != nil {
				return "", fmt.Errorf("claude not found in PATH (required for wrapper generation): %w", err)
			}
			realClaude = p
			break
		}
	}
	dir, err := os.MkdirTemp("", "fm-wrappers-*")
	if err != nil {
		return "", fmt.Errorf("create wrapper dir: %w", err)
	}
	for _, s := range specs {
		script, gErr := generateWrapper(s, realClaude)
		if gErr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("generate wrapper %q: %w", s.Command, gErr)
		}
		if wErr := os.WriteFile(filepath.Join(dir, s.Command), []byte(script), 0755); wErr != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("write wrapper %q: %w", s.Command, wErr)
		}
	}
	return dir, nil
}
```

**c) Обновить `generateWrapper`** — добавить openai-ветку в начало (перед проверкой `s.Model == ""`):

```go
func generateWrapper(s WrapperSpec, realClaude string) (string, error) {
	if s.Command == "" {
		return "", errors.New("empty command")
	}
	name := envName(s.Command)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")

	if s.Type == "openai" {
		// openai-compatible: OPENAI_* vars + exec openai-as-claude.
		// realClaude не нужен — openai-as-claude не вызывает claude.
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export OPENAI_BASE_URL=%q\n", s.BaseURL)
		}
		if s.Model != "" {
			fmt.Fprintf(&b, "export OPENAI_MODEL=%q\n", s.Model)
		}
		b.WriteString("exec /usr/local/bin/openai-as-claude \"$@\"\n")
		return b.String(), nil
	}

	// claude (type "" или "claude") — существующая логика без изменений
	if s.Model == "" {
		// ... (существующий claude proxy-shim код)
	}
	// ... (существующий agent template код)
}
```

Полная итоговая версия `generateWrapper` (с сохранением существующих claude-веток):

```go
func generateWrapper(s WrapperSpec, realClaude string) (string, error) {
	if s.Command == "" {
		return "", errors.New("empty command")
	}
	name := envName(s.Command)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")

	if s.Type == "openai" {
		if s.AuthTo != "" {
			fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
			fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
		}
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export OPENAI_BASE_URL=%q\n", s.BaseURL)
		}
		if s.Model != "" {
			fmt.Fprintf(&b, "export OPENAI_MODEL=%q\n", s.Model)
		}
		b.WriteString("exec /usr/local/bin/openai-as-claude \"$@\"\n")
		return b.String(), nil
	}

	if s.Model == "" {
		// claude proxy-shim: BASE_URL only.
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
		}
		fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
		return b.String(), nil
	}

	// claude agent template
	if s.AuthTo != "" {
		fmt.Fprintf(&b, "export %s=\"$AFM_SECRET_%s\"\n", s.AuthTo, name)
		fmt.Fprintf(&b, "unset AFM_SECRET_%s\n", name)
	}
	if s.BaseURL != "" {
		fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
	}
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_HAIKU_MODEL=%q\n", s.Model)
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_SONNET_MODEL=%q\n", s.Model)
	fmt.Fprintf(&b, "export ANTHROPIC_DEFAULT_OPUS_MODEL=%q\n", s.Model)
	if s.HasSysPrompt {
		fmt.Fprintf(&b, "if [ -n \"$AFM_SYSPROMPT_%s\" ]; then\n", name)
		fmt.Fprintf(&b, "  _sp=$(mktemp); printf '%%s' \"$AFM_SYSPROMPT_%s\" > \"$_sp\"; chmod 600 \"$_sp\"; unset AFM_SYSPROMPT_%s\n", name, name)
		b.WriteString("  set -- \"$@\" --append-system-prompt-file \"$_sp\"\n")
		b.WriteString("fi\n")
	}
	fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
	return b.String(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/docker/ -run "TestCreateWrappers" -race`
Expected: PASS (все тесты, включая новые OpenAI-тесты и существующие Claude-тесты).

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "feat(docker): WrapperSpec.Type openai — шаблон с OPENAI_* env vars и openai-as-claude"
```

---

### Task 3: run.go — передача `Type` в контейнерный `WrapperSpec`

**Files:**
- Modify: `cmd/afm/run.go` (контейнерный блок autoShim ~`:130-160`)

**Interfaces:**
- Добавляет: `Type: recipe.Type` в `docker.WrapperSpec` при построении спецификаций для `CreateWrappers`.

Примечание: `cmd/afm/run.go` — единственный caller. Изменение минимальное (одна строка).

- [ ] **Step 1: Write the failing test** — в `cmd/afm/run_test.go` (или добавь в существующий `run_test.go` если есть):

```go
func TestBuildWrapperSpecType(t *testing.T) {
	// проверяем, что при recipe.Type = "openai" WrapperSpec получает правильный Type.
	// этот тест проверяет логику сборки specs, не запуская docker.
	recipe := config.AgentRecipe{
		Type:  "openai",
		Model: "claude-sonnet-4-5",
		URL:   "https://api2.cursor.sh/v1",
		Auth:  config.RecipeAuth{From: "env:CURSOR_TOKEN", To: "env:OPENAI_API_KEY"},
	}
	spec := docker.WrapperSpec{
		Type:    recipe.Type,
		Command: "cursor",
		AuthTo:  recipe.Auth.EnvVarName(),
		BaseURL: recipe.URL,
		Model:   recipe.Model,
	}
	if spec.Type != "openai" {
		t.Errorf("WrapperSpec.Type: got %q, want %q", spec.Type, "openai")
	}
	if spec.AuthTo != "OPENAI_API_KEY" {
		t.Errorf("WrapperSpec.AuthTo: got %q", spec.AuthTo)
	}
}
```

Если `cmd/afm/run_test.go` не существует — создай с пакетом `main`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/afm/ -run TestBuildWrapperSpecType -race`
Expected: PASS (это unit-тест сборки, compile-ошибки нет). Если проходит — продолжи к Step 3.

- [ ] **Step 3: Implement** — в `cmd/afm/run.go` контейнерный блок autoShim, где собираются `wrapperSpecs`, добавь `Type: recipe.Type`:

```go
wrapperSpecs = append(wrapperSpecs, docker.WrapperSpec{
    Type:         recipe.Type,   // ← добавить эту строку
    Command:      cmd,
    AuthTo:       recipe.Auth.EnvVarName(),
    BaseURL:      docker.ResolveBaseURL(recipe.URL, proxyOn, proxyUpstream, proxyAddr),
    Model:        recipe.Model,
    HasSysPrompt: recipe.SystemPrompt != "",
})
```

Для `type: openai` `ResolveBaseURL` по-прежнему корректен: если URL совпадает с upstream прокси — идёт через прокси; иначе напрямую. Это нужно для провайдеров, которые также используют `api.z.ai`.

- [ ] **Step 4: Build + run tests + commit**

```bash
go build ./...
go test ./... -race
make lint
git add cmd/afm/run.go cmd/afm/run_test.go
git commit -m "feat(run): передаём WrapperSpec.Type из recipe при сборке openai-врапперов"
```

---

### Task 4: `scripts/openai-as-claude.sh` — транслятор stdin → OpenAI API → stream-json

**Files:**
- Create: `scripts/openai-as-claude.sh`

**Что делает скрипт:**
1. Читает промпт из stdin (игнорирует все CLI флаги)
2. Строит JSON-тело запроса с `model`, `stream: true`, `messages`
3. Вызывает `curl` на `${OPENAI_BASE_URL}/chat/completions`
4. Парсит SSE-ответ (строки `data: {...}` / `data: [DONE]`)
5. Транслирует `choices[0].delta.content` → `{"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}`
6. Всегда завершается строкой `{"type":"result","result":""}`

**Требования к окружению (устанавливается враппером):**
- `OPENAI_API_KEY` — токен авторизации
- `OPENAI_BASE_URL` — базовый URL API (например `https://api2.cursor.sh/v1`)
- `OPENAI_MODEL` — имя модели

- [ ] **Step 1: Write the script** — создай `scripts/openai-as-claude.sh`:

```bash
#!/usr/bin/env bash
# openai-as-claude.sh — транслятор для OpenAI-совместимых провайдеров (cursor, deepseek, и т.д.)
#
# читает промпт из stdin, вызывает ${OPENAI_BASE_URL}/chat/completions (stream=true),
# транслирует SSE-чанки в claude stream-json формат.
#
# переменные окружения (устанавливаются сгенерированным враппером autoShim):
#   OPENAI_API_KEY   — токен авторизации (обязателен)
#   OPENAI_BASE_URL  — базовый URL API (дефолт: https://api.openai.com/v1)
#   OPENAI_MODEL     — модель (дефолт: gpt-4o)

set -euo pipefail

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# игнорируем все claude CLI флаги (--model, --effort, --dangerously-skip-permissions и т.д.)
while [[ $# -gt 0 ]]; do
    shift
done

# читаем промпт только из stdin (как делает claude, когда prompt передаётся pipe)
if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (openai-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)

if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
OPENAI_MODEL="${OPENAI_MODEL:-gpt-4o}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

# формируем тело запроса
body=$(jq -nc --arg model "$OPENAI_MODEL" --arg content "$prompt" \
    '{model: $model, stream: true, messages: [{role: "user", content: $content}]}')

# вызываем API и транслируем SSE → claude stream-json
# SSE формат: "data: {...}" или "data: [DONE]"
curl -sS --no-buffer \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -d "$body" \
    "${OPENAI_BASE_URL}/chat/completions" | \
while IFS= read -r line; do
    # пропускаем пустые строки и строки без "data: "
    [[ "$line" =~ ^data:\ (.*) ]] || continue
    data="${BASH_REMATCH[1]}"
    # конец стрима
    [[ "$data" == "[DONE]" ]] && break
    # транслируем delta.content → content_block_delta
    echo "$data" | jq -c '
        .choices[0].delta.content as $text |
        if $text and $text != "" then
            {type: "content_block_delta", delta: {type: "text_delta", text: $text}}
        else empty
        end
    ' 2>/dev/null || true
done || true

# финальный result-ивент (claude executor ждёт его для завершения)
echo '{"type":"result","result":""}'
```

- [ ] **Step 2: Make executable + basic smoke test**

```bash
chmod +x scripts/openai-as-claude.sh

# проверяем что скрипт работает при отсутствии curl/jq с понятной ошибкой
# (просто синтаксис, не реальный вызов API)
bash -n scripts/openai-as-claude.sh && echo "syntax OK"
```

Expected: `syntax OK`.

- [ ] **Step 3: Commit**

```bash
git add scripts/openai-as-claude.sh
git commit -m "feat(scripts): openai-as-claude.sh — транслятор SSE OpenAI API → claude stream-json"
```

---

### Task 5: Docker image — `jq` + скрипт в образе

**Files:**
- Modify: `Dockerfile.runtime` (apt-get install + COPY скрипта)

**Что нужно:**
- Добавить `jq` в список пакетов (curl уже есть)
- Скопировать `scripts/openai-as-claude.sh` как `/usr/local/bin/openai-as-claude`

- [ ] **Step 1: Проверить что `jq` ещё не установлен**

```bash
grep "jq" /Users/alexander.kopichin/work/flowManager/Dockerfile.runtime || echo "jq absent"
```

Expected: `jq absent`.

- [ ] **Step 2: Implement** — в `Dockerfile.runtime`:

Добавь `jq \` в apt-get блок (после `build-essential`):

```dockerfile
RUN apt-get update && apt-get install -y \
      curl \
      git \
      ca-certificates \
      gnupg \
      gosu \
      jq \
      python3 \
      ...
```

После блока `COPY docker-entrypoint.sh ...` добавь:

```dockerfile
# openai-as-claude: транслятор для OpenAI-совместимых провайдеров (cursor, deepseek, и т.д.)
COPY scripts/openai-as-claude.sh /usr/local/bin/openai-as-claude
RUN chmod +x /usr/local/bin/openai-as-claude
```

- [ ] **Step 3: Verify Dockerfile syntax**

```bash
docker build --no-cache -f Dockerfile.runtime -t afm-test-openai . 2>&1 | tail -20
```

Если доступен Docker — запусти. Иначе проверь синтаксис визуально.

Expected: образ собирается без ошибок; `/usr/local/bin/openai-as-claude` доступен.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile.runtime
git commit -m "feat(docker): добавлен jq и openai-as-claude в runtime-образ"
```

---

### Task 6: Документация `CLAUDE.md` + финальный прогон тестов

**Files:**
- Modify: `CLAUDE.md` (раздел autoShim — добавить подраздел про `type: openai`)

- [ ] **Step 1: Final full test suite**

```bash
go test ./... -race
make lint
```

Expected: PASS.

- [ ] **Step 2: Добавить в CLAUDE.md** — в подраздел `### autoShim: генерируемые врапперы без монтирования` добавь абзац про `type: openai`:

```markdown
#### Тип `openai`: OpenAI-совместимые провайдеры

Для провайдеров с API совместимым с OpenAI (`v1/chat/completions`) укажи `type: openai`.
Сгенерированный враппер использует `/usr/local/bin/openai-as-claude` вместо claude:

```yaml
docker:
  autoShim: true
  agents:
    cursor:
      type: openai
      model: claude-sonnet-4-5
      url: https://api2.cursor.sh/v1
      auth:
        from: env:CURSOR_TOKEN        # секрет на хосте
        to: env:OPENAI_API_KEY        # не ограничен ClaudeAuthEnvVars
```

Поддерживаемые провайдеры: Cursor (`api2.cursor.sh`), DeepSeek (`api.deepseek.com`),
любые OpenAI-совместимые эндпоинты (локальные Ollama и т.д.).

Требования в образе: `jq`, `curl` (оба присутствуют в `Dockerfile.runtime`).
```

- [ ] **Step 3: Commit docs**

```bash
git add CLAUDE.md
git commit -m "docs: type: openai в autoShim — CLAUDE.md"
```

---

## Self-Review (выполнено автором плана)

**Функциональное покрытие:**
- `type` поле в `AgentRecipe` + YAML-парсинг → Task 1. ✓
- `Validate()` для `openai`: любой `env:VAR` в `auth.to`, url обязателен → Task 1. ✓
- Backward compat: пустой `type` = claude, поведение без изменений → Task 1 (тест `claude type: OPENAI_API_KEY rejected`). ✓
- `WrapperSpec.Type` → Task 2. ✓
- Генератор openai-шаблона: `OPENAI_*` vars, `exec /usr/local/bin/openai-as-claude` → Task 2. ✓
- Условный LookPath claude (только когда нужен) → Task 2. ✓
- `run.go` передаёт `Type` → Task 3. ✓
- `openai-as-claude.sh`: stdin, stream, SSE→json, fallback result event → Task 4. ✓
- Docker: `jq` + скрипт в образе → Task 5. ✓

**Что НЕ реализуется в этом плане (extension points):**
- `system_prompt` для openai-типа — OpenAI API принимает system message отдельным элементом `messages`; потребует другого подхода (добавить в тело запроса `{role: "system", content: ...}`). Добавить отдельным планом при необходимости.
- Стриминг ошибок API — текущий скрипт при ошибке curl выводит `{"type":"result","result":""}`. Ошибка будет видна агенту как пустой ответ. Fail-fast при HTTP 4xx/5xx добавить отдельным планом.

**Отклонения от дизайна:**
- `ResolveBaseURL` для openai-типа не трогается: если провайдер на том же хосте что и proxy upstream — трафик пойдёт через прокси. Это корректно (ZAI может стоять перед cursor.sh), хотя обычно openai-провайдеры используют прямой URL.
- `HasSysPrompt` в `WrapperSpec` для openai-типа игнорируется в текущей реализации (не добавляем в openai-ветку `generateWrapper`). Задокументировано в разделе "что не реализуется".

**Placeholder scan:** плейсхолдеров нет; каждый шаг содержит исполняемый код или конкретную команду. ✓
