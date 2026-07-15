# Docker `autoShim` — автогенерация claude-врапперов Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** По флагу `docker.autoShim: true` afm генерирует claude-совместимые врапперы для recipe-агентов внутри Docker-контейнера — без монтирования хост-бинарников и без `extra_mounts` для токенов.

**Architecture:** Recipe (`docker.agents.<cmd>` с `model`/`url`/`auth`/`system_prompt`) описывает враппер. На хосте launcher читает секрет и контент sysprompt из host-only файлов и передаёт их в контейнер как transient env (`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`). В контейнере `run.go` читает `url`/`model`/`auth.to` из смонтированного `config.yaml`, генерирует враппер (`CreateWrappers`) в единый wrapper-dir (вместе с claude proxy-shim) и кладёт его на PATH через `Options.ProxyShimDir`. Сгенерированный враппер bake'ит `ANTHROPIC_BASE_URL` (по host-match с proxy upstream) и model, подставляет секрет из transient env, `unset`'ит его и `exec`'ит абсолютный `claude`. executor и orchestrator правятся минимально: `proxyForCmd` получает generated-aware ветку, executor — без правок.

**Tech Stack:** Go 1.26, `net/url`, `os/exec`, `testing`. Существующие пакеты: `pkg/config`, `pkg/docker`, `pkg/proxy`, `pkg/orchestrator`, `cmd/afm`.

## Global Constraints

- **Go 1.26** — версию в `go.mod` НЕ менять.
- **Коммиты на русском**, без `Co-Authored-By`.
- После каждой задачи: `go build ./...` собирается, `go test ./pkg/<pkg>/... -race` зелёный, `make lint` (golangci-lint `--fix`) без ошибок. Устаревших конструкций не оставлять.
- **Recipe-валидация:** `model` обязателен; `auth.to` обязан быть `env:<VAR>` где `VAR` ∈ `{CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN}`.
- **transient env:** только `AFM_SECRET_<CMD>` + опц. `AFM_SYSPROMPT_<CMD>`. `AFM_URL_<CMD>` НЕ передаётся (url читается из cfg в контейнере).
- **Секреты в argv:** только bare-form `-e KEY` (значение через `os.Setenv`→`os.Environ()`, как существующий паттерн `pkg/docker/launcher.go:225`). Значение секрета никогда не появляется в `docker run` argv.
- `<CMD>` в env-имени = `envName(cmd)` (uppercase, не-`[A-Z0-9_]`→`_`).
- Спек: `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.

---

## File Structure

| Файл | Ответственность |
|------|-----------------|
| `pkg/config/config.go` | `AgentRecipe`/`RecipeAuth`/`ClaudeAuthEnvVars`, `DockerConfig.AutoShim/Agents/SecretsFile`, `IsAutoShim()`, `ValidateAgents()`, merge. |
| `pkg/docker/wrapper.go` (новый) | `hostOf`, `ResolveBaseURL`, `envName`, `WrapperSpec`, `CreateWrappers`, `generateWrapper`. |
| `pkg/docker/secrets.go` (новый) | `homeDir`, `LoadSecrets`, `ResolveAuthValue`, `ResolveSystemPrompt`, `loadSecretLayers`. |
| `pkg/docker/launcher.go` | `ReExecConfig.Recipes/SecretsFile`, резолв секретов → transient bare `-e`, `ScanCommands(...,generated)`, `UsedRecipeCommands`. |
| `pkg/orchestrator/orchestrator.go` | `Options.GeneratedAgents`, `proxyForCmd(...,generated)`. |
| `cmd/afm/run.go` | host-блок: validate+`ScanCommands(generated)`+передача `Recipes`/`SecretsFile` в `ReExec`; container-блок: refactor proxy-блока → `CreateWrappers` + `Options.GeneratedAgents`. |
| `pkg/proxy/shim.go` | Удаляется в Task 8 (логика поглощена `docker.CreateWrappers`); `shim_test.go` удаляется. |
| `cmd/afm/init.go` | Добавляет `.afm/secrets.env` в project `.gitignore`. |

---

### Task 1: Config — типы recipe, валидация, merge, IsAutoShim

**Files:**
- Modify: `pkg/config/config.go` (struct `DockerConfig` ~`:73`, `mergeFile` ~`:209`)
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.AgentRecipe{Model,URL,SystemPrompt,Auth}`; `config.RecipeAuth{From,To}`; `func (RecipeAuth) EnvVarName() string`; `var ClaudeAuthEnvVars []string`; `func (DockerConfig) IsAutoShim() bool`; `func (DockerConfig) ValidateAgents() error`. YAML-теги: `autoShim`, `agents`, `secrets_file`, `model`, `url`, `system_prompt`, `auth`, `from`, `to`.

- [ ] **Step 1: Write the failing test** — добавь в `pkg/config/config_test.go`:

```go
func TestDockerAutoShim_ParseAndValidate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `
docker:
  autoShim: true
  secrets_file: ~/.afm/secrets.env
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      system_prompt: "file:~/.ai-free/claude-glm/system-prompt.md"
      auth:
        from: "file:~/.ai-free/claude-glm/token"
        to: "env:ANTHROPIC_AUTH_TOKEN"
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(dir, dir) // global=project=dir → один файл
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !cfg.Docker.IsAutoShim() {
		t.Error("IsAutoShim: expected true")
	}
	r := cfg.Docker.Agents["glm51"]
	if r.Model != "glm-5.1" {
		t.Errorf("Model: got %q", r.Model)
	}
	if r.Auth.EnvVarName() != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("EnvVarName: got %q", r.Auth.EnvVarName())
	}
	if cfg.Docker.SecretsFile != "~/.afm/secrets.env" {
		t.Errorf("SecretsFile: got %q", cfg.Docker.SecretsFile)
	}
	if err := cfg.Docker.ValidateAgents(); err != nil {
		t.Errorf("ValidateAgents: %v", err)
	}
}

func TestDockerAutoShim_ValidationErrors(t *testing.T) {
	cases := []struct {
		name   string
		recipe AgentRecipe
		errSub string
	}{
		{"missing model", AgentRecipe{Auth: RecipeAuth{To: "env:ANTHROPIC_AUTH_TOKEN"}}, "model is required"},
		{"auth.to not env", AgentRecipe{Model: "m", Auth: RecipeAuth{To: "ANTHROPIC_AUTH_TOKEN"}}, "must be an env:"},
		{"auth.to not in list", AgentRecipe{Model: "m", Auth: RecipeAuth{To: "env:RANDOM"}}, "not one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.recipe.Validate()
			if err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("Validate(): got %v, want substring %q", err, c.errSub)
			}
		})
	}
}

func TestDockerAutoShim_MergeLayers(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()
	_ = os.WriteFile(filepath.Join(global, "config.yaml"), []byte("docker:\n  agents:\n    glm51:\n      model: glm-5.1\n      auth: {to: \"env:ANTHROPIC_AUTH_TOKEN\"}\n"), 0644)
	_ = os.WriteFile(filepath.Join(project, "config.yaml"), []byte("docker:\n  agents:\n    glm52:\n      model: glm-5.2\n      auth: {to: \"env:ANTHROPIC_AUTH_TOKEN\"}\n"), 0644)
	cfg, err := LoadFrom(global, project)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if _, ok := cfg.Docker.Agents["glm51"]; !ok {
		t.Error("merge: glm51 from global layer missing")
	}
	if _, ok := cfg.Docker.Agents["glm52"]; !ok {
		t.Error("merge: glm52 from project layer missing")
	}
	if len(cfg.Docker.Agents) != 2 {
		t.Errorf("merge: expected 2 agents, got %d", len(cfg.Docker.Agents))
	}
}
```

(Если в `config_test.go` ещё нет импортов `os`/`path/filepath`/`strings` — добавь.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/ -run TestDockerAutoShim -race`
Expected: FAIL / compile error (`AgentRecipe undefined`, `IsAutoShim undefined`).

- [ ] **Step 3: Implement** — в `pkg/config/config.go`:

Добавь типы (после `DockerConfig` или перед ним):

```go
// AgentRecipe describes how to generate a claude-compatible wrapper for a custom
// agent command (e.g. glm51) inside Docker, so the host binary is not mounted.
// See docs/superpowers/specs/2026-07-14-docker-autoshim-design.md.
type AgentRecipe struct {
	Model        string     `yaml:"model"`         // required → ANTHROPIC_DEFAULT_*_MODEL
	URL          string     `yaml:"url"`           // optional; agent gateway (literal)
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
}

// RecipeAuth describes where afm reads the secret on the host (From) and which
// claude auth env var the generated wrapper exports (To).
type RecipeAuth struct {
	From string `yaml:"from"` // "env:VAR" | "file:<path>"
	To   string `yaml:"to"`   // "env:<VAR>"; VAR ∈ ClaudeAuthEnvVars
}

// EnvVarName strips the "env:" prefix from Auth.To.
func (a RecipeAuth) EnvVarName() string { return strings.TrimPrefix(a.To, "env:") }

// ClaudeAuthEnvVars — env vars through which claude accepts tokens in a Linux
// container (macOS Keychain is unavailable there).
var ClaudeAuthEnvVars = []string{
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
}

func isClaudeAuthEnvVar(name string) bool {
	for _, v := range ClaudeAuthEnvVars {
		if v == name {
			return true
		}
	}
	return false
}

// Validate returns an error if the recipe is malformed.
func (r AgentRecipe) Validate() error {
	if r.Model == "" {
		return errors.New("recipe: model is required")
	}
	if !strings.HasPrefix(r.Auth.To, "env:") {
		return errors.New("recipe: auth.to must be an env: reference (e.g. env:ANTHROPIC_AUTH_TOKEN)")
	}
	if !isClaudeAuthEnvVar(r.Auth.EnvVarName()) {
		return fmt.Errorf("recipe: auth.to env var %q is not one of %v", r.Auth.EnvVarName(), ClaudeAuthEnvVars)
	}
	return nil
}
```

Расширь `DockerConfig`:

```go
type DockerConfig struct {
	Enabled     *bool                  `yaml:"enabled"` // nil = смотрим AFM_USE_DOCKER
	Image       string                 `yaml:"image"`
	AutoShim    *bool                  `yaml:"autoShim"`     // nil = выкл (mount :ro как раньше)
	Agents      map[string]AgentRecipe `yaml:"agents"`       // recipe-агенты для генерации
	SecretsFile string                 `yaml:"secrets_file"` // опц.; дефолт global ~/.afm + project .afm
	ExtraMounts []string               `yaml:"extra_mounts"`
}

// IsAutoShim reports whether wrapper auto-generation is enabled.
func (d DockerConfig) IsAutoShim() bool { return d.AutoShim != nil && *d.AutoShim }

// ValidateAgents validates every recipe. Called only when autoShim is enabled.
func (d DockerConfig) ValidateAgents() error {
	for name, r := range d.Agents {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("docker.agents.%s: %w", name, err)
		}
	}
	return nil
}
```

В `mergeFile` добавь (рядом с существующим `overlay.Docker.*` блоком, после `ExtraMounts`):

```go
	if overlay.Docker.AutoShim != nil {
		dst.Docker.AutoShim = overlay.Docker.AutoShim
	}
	if overlay.Docker.SecretsFile != "" {
		dst.Docker.SecretsFile = overlay.Docker.SecretsFile
	}
	if overlay.Docker.Agents != nil {
		if dst.Docker.Agents == nil {
			dst.Docker.Agents = map[string]AgentRecipe{}
		}
		for k, v := range overlay.Docker.Agents {
			dst.Docker.Agents[k] = v // per-key overlay: проектный слой дополняет/переопределяет глобальный
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/config/ -run TestDockerAutoShim -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): recipe/autoShim для docker-агентов — типы, валидация, merge"
```

---

### Task 2: Wrapper helpers — hostOf, ResolveBaseURL, envName

**Files:**
- Create: `pkg/docker/wrapper.go`
- Test: `pkg/docker/wrapper_test.go` (internal package `docker`, чтобы видеть `hostOf`/`envName`)

**Interfaces:**
- Produces: `func hostOf(rawURL string) string` (unexported); `func ResolveBaseURL(agentURL string, proxyOn bool, proxyUpstream, proxyAddr string) string`; `func envName(cmd string) string` (unexported).

- [ ] **Step 1: Write the failing test** — `pkg/docker/wrapper_test.go`:

```go
package docker

import "testing"

func TestEnvName(t *testing.T) {
	cases := map[string]string{
		"glm51":               "GLM51",
		"deepseek-v4":         "DEEPSEEK_V4",
		"ai-free.claude-glm":  "AI_FREE_CLAUDE_GLM",
		"":                    "",
	}
	for in, want := range cases {
		if got := envName(in); got != want {
			t.Errorf("envName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://api.z.ai/api/anthropic": "api.z.ai",
		"http://127.0.0.1:39217":         "127.0.0.1:39217",
		"not-a-url":                      "not-a-url", // fallback
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveBaseURL(t *testing.T) {
	const upstream = "https://api.z.ai/api/anthropic"
	const proxyAddr = "http://127.0.0.1:39217"
	cases := []struct {
		name                              string
		agentURL                          string
		proxyOn                           bool
		want                              string
	}{
		{"proxy off → direct", "https://api.z.ai/api/anthropic", false, "https://api.z.ai/api/anthropic"},
		{"same host → proxy", "https://api.z.ai/api/anthropic", true, proxyAddr},
		{"diff host → direct", "https://api.deepseek.com/v1", true, "https://api.deepseek.com/v1"},
		{"empty agentURL → empty", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveBaseURL(c.agentURL, c.proxyOn, upstream, proxyAddr)
			if got != c.want {
				t.Errorf("ResolveBaseURL: got %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run "TestEnvName|TestHostOf|TestResolveBaseURL" -race`
Expected: FAIL / compile error (`hostOf undefined`).

- [ ] **Step 3: Implement** — `pkg/docker/wrapper.go`:

```go
package docker

import (
	"net/url"
	"strings"
)

// hostOf returns the host[:port] of a URL, or the input unchanged if it cannot
// be parsed. Used by host-match to decide whether an agent's gateway goes
// through the proxy (same host as proxy upstream) or direct.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// ResolveBaseURL implements proxy host-match (spec P2′): if the proxy is on and
// the agent's URL host matches the proxy upstream host, route through the proxy
// (ZAI 529 protection); otherwise go direct.
func ResolveBaseURL(agentURL string, proxyOn bool, proxyUpstream, proxyAddr string) string {
	if !proxyOn || proxyUpstream == "" {
		return agentURL
	}
	if hostOf(agentURL) == hostOf(proxyUpstream) {
		return proxyAddr
	}
	return agentURL
}

// envName sanitizes a command name into an uppercase env-var suffix: only
// [A-Z0-9_] allowed, everything else → '_'. Used for AFM_SECRET_<NAME> and
// AFM_SYSPROMPT_<NAME> transient env vars.
func envName(cmd string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(cmd) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/docker/ -run "TestEnvName|TestHostOf|TestResolveBaseURL" -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "feat(docker): хелперы врапперов — hostOf, ResolveBaseURL (host-match), envName"
```

---

### Task 3: CreateWrappers — генерация врапперов (агент + claude-shim)

**Files:**
- Modify: `pkg/docker/wrapper.go` (добавить `WrapperSpec`, `CreateWrappers`, `generateWrapper`)
- Test: `pkg/docker/wrapper_test.go` (добавить тесты)

**Interfaces:**
- Produces: `type WrapperSpec struct { Command, AuthTo, BaseURL, Model string; HasSysPrompt bool }`; `func CreateWrappers(specs []WrapperSpec) (string, error)`.
- Consumes: `envName` (Task 2).
- Примечание: `pkg/proxy/shim.go` (`CreateShim`) пока НЕ трогать — удаляется в Task 8, после того как `run.go` перестанет его звать.

- [ ] **Step 1: Write the failing test** — добавь в `pkg/docker/wrapper_test.go`:

```go
package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubClaudeOnPATH кладёт fake-claude в temp-dir и prepend'ит его к PATH.
// Возвращает абсолютный путь к fake-claude.
func stubClaudeOnPATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return bin
}

func TestCreateWrappers_AgentTemplate(t *testing.T) {
	realClaude := stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{
		Command:     "glm51",
		AuthTo:      "ANTHROPIC_AUTH_TOKEN",
		BaseURL:     "http://127.0.0.1:39217",
		Model:       "glm-5.1",
		HasSysPrompt: true,
	}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer os.RemoveAll(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "glm51"))
	s := string(script)
	wantSubstrings := []string{
		"#!/bin/sh",
		`export ANTHROPIC_AUTH_TOKEN="$AFM_SECRET_GLM51"`,
		"unset AFM_SECRET_GLM51",
		`export ANTHROPIC_BASE_URL="http://127.0.0.1:39217"`,
		`export ANTHROPIC_DEFAULT_HAIKU_MODEL="glm-5.1"`,
		`export ANTHROPIC_DEFAULT_SONNET_MODEL="glm-5.1"`,
		`export ANTHROPIC_DEFAULT_OPUS_MODEL="glm-5.1"`,
		`"$AFM_SYSPROMPT_GLM51"`,      // sysprompt guard
		"--append-system-prompt-file",
		"exec " + realClaude + ` "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("agent wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
}

func TestCreateWrappers_NoSysPromptOmitsBlock(t *testing.T) {
	stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Command: "glm52", AuthTo: "ANTHROPIC_AUTH_TOKEN", BaseURL: "https://x", Model: "glm-5.2"}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer os.RemoveAll(dir)
	s := string(mustRead(t, filepath.Join(dir, "glm52")))
	if strings.Contains(s, "AFM_SYSPROMPT") {
		t.Errorf("sysprompt block should be absent without HasSysPrompt:\n%s", s)
	}
}

func TestCreateWrappers_ClaudeShimTemplate(t *testing.T) {
	realClaude := stubClaudeOnPATH(t)
	dir, err := CreateWrappers([]WrapperSpec{{Command: "claude", BaseURL: "http://127.0.0.1:9999"}})
	if err != nil {
		t.Fatalf("CreateWrappers: %v", err)
	}
	defer os.RemoveAll(dir)
	s := string(mustRead(t, filepath.Join(dir, "claude")))
	if !strings.Contains(s, `export ANTHROPIC_BASE_URL="http://127.0.0.1:9999"`) {
		t.Errorf("claude-shim should set BASE_URL:\n%s", s)
	}
	if strings.Contains(s, "ANTHROPIC_DEFAULT") || strings.Contains(s, "AFM_SECRET") {
		t.Errorf("claude-shim must not emit agent-only lines:\n%s", s)
	}
	if !strings.Contains(s, "exec "+realClaude+` "$@"`) {
		t.Errorf("claude-shim should exec real claude:\n%s", s)
	}
}

func TestCreateWrappers_NoClaudeHardError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // claude нет в PATH
	_, err := CreateWrappers([]WrapperSpec{{Command: "glm51", Model: "m"}})
	if err == nil {
		t.Fatal("expected error when claude not in PATH")
	}
}

func TestCreateWrappers_EmptySpecsNoop(t *testing.T) {
	dir, err := CreateWrappers(nil)
	if err != nil || dir != "" {
		t.Errorf("empty specs: got (%q,%v), want (\"\",nil)", dir, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run TestCreateWrappers -race`
Expected: FAIL / compile error (`CreateWrappers undefined`, `WrapperSpec undefined`).

- [ ] **Step 3: Implement** — добавь в `pkg/docker/wrapper.go` (дополни импорты `errors`, `fmt`, `os`, `os/exec`, `path/filepath`):

```go
// WrapperSpec describes one wrapper script to generate in the wrapper-dir.
// Model != "" selects the agent template (auth + model + optional sysprompt);
// Model == "" selects the claude proxy-shim template (BASE_URL only).
type WrapperSpec struct {
	Command      string
	AuthTo       string // claude auth env var name; "" for claude-shim
	BaseURL      string // baked ANTHROPIC_BASE_URL; "" → omit the export
	Model        string // → ANTHROPIC_DEFAULT_*_MODEL; "" → claude-shim template
	HasSysPrompt bool   // emit sysprompt block (reads $AFM_SYSPROMPT_<NAME>)
}

// CreateWrappers creates a temp dir with one executable script per spec, named
// after spec.Command, and returns its path. realClaude is resolved once via
// exec.LookPath("claude") (absolute path → bypasses the wrapper-dir on PATH,
// avoiding recursion). Caller must defer os.RemoveAll(dir). Empty specs → ("", nil).
func CreateWrappers(specs []WrapperSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	realClaude, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude not found in PATH (required for wrapper generation): %w", err)
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

func generateWrapper(s WrapperSpec, realClaude string) (string, error) {
	if s.Command == "" {
		return "", errors.New("empty command")
	}
	name := envName(s.Command)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if s.Model == "" {
		// claude proxy-shim: BASE_URL only.
		if s.BaseURL != "" {
			fmt.Fprintf(&b, "export ANTHROPIC_BASE_URL=%q\n", s.BaseURL)
		}
		fmt.Fprintf(&b, "exec %s \"$@\"\n", realClaude)
		return b.String(), nil
	}
	// agent template
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

Run: `go test ./pkg/docker/ -run TestCreateWrappers -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "feat(docker): CreateWrappers — генерация врапперов (агент + claude-shim)"
```

---

### Task 4: secrets.go — парсинг secrets.env + резолв секрета/sysprompt

**Files:**
- Create: `pkg/docker/secrets.go`
- Test: `pkg/docker/secrets_test.go` (external package `docker_test`)

**Interfaces:**
- Produces: `func homeDir() string`; `func LoadSecrets(paths []string) (map[string]string, error)`; `func ResolveAuthValue(from string, secrets map[string]string) (string, error)`; `func ResolveSystemPrompt(ref string) (string, error)`; `func loadSecretLayers(override, projectDir string) (map[string]string, error)`.
- Consumes: `expandHome` (`pkg/docker/launcher.go:251`).

- [ ] **Step 1: Write the failing test** — `pkg/docker/secrets_test.go`:

```go
package docker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
)

func TestLoadSecrets_MergesLayers(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	os.WriteFile(filepath.Join(d1, "a.env"), []byte("TOKEN=global\nSHARED=g\n"), 0600)
	os.WriteFile(filepath.Join(d2, "b.env"), []byte("SHARED=project\nOTHER=p\n"), 0600)
	m, err := docker.LoadSecrets([]string{filepath.Join(d1, "a.env"), filepath.Join(d2, "b.env")})
	if err != nil {
		t.Fatal(err)
	}
	if m["TOKEN"] != "global" || m["OTHER"] != "p" || m["SHARED"] != "project" {
		t.Errorf("merge: %#v", m)
	}
}

func TestLoadSecrets_MissingFileIgnored(t *testing.T) {
	m, err := docker.LoadSecrets([]string{filepath.Join(t.TempDir(), "nope.env")})
	if err != nil {
		t.Fatalf("missing file should be ignored: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %#v", m)
	}
}

func TestResolveAuthValue(t *testing.T) {
	tokFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokFile, []byte("abc123\n"), 0600)

	// env: form — из map
	if v, err := docker.ResolveAuthValue("env:TOKEN", map[string]string{"TOKEN": "from-map"}); err != nil || v != "from-map" {
		t.Errorf("env:map: v=%q err=%v", v, err)
	}
	// env: form — fallback на os.Getenv
	t.Setenv("MYENV", "from-os")
	if v, err := docker.ResolveAuthValue("env:MYENV", nil); err != nil || v != "from-os" {
		t.Errorf("env:os: v=%q err=%v", v, err)
	}
	// env: form — отсутствует
	if _, err := docker.ResolveAuthValue("env:NOPE_NOPE", nil); err == nil {
		t.Error("missing env should error")
	}
	// file: form
	if v, err := docker.ResolveAuthValue("file:"+tokFile, nil); err != nil || v != "abc123" {
		t.Errorf("file: v=%q err=%v", v, err)
	}
	// file: missing
	if _, err := docker.ResolveAuthValue("file:"+filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Error("missing file should error")
	}
}

func TestResolveSystemPrompt(t *testing.T) {
	spFile := filepath.Join(t.TempDir(), "sp.md")
	os.WriteFile(spFile, []byte("you are glm"), 0600)
	if v, err := docker.ResolveSystemPrompt("file:" + spFile); err != nil || v != "you are glm" {
		t.Errorf("got v=%q err=%v", v, err)
	}
	if v, err := docker.ResolveSystemPrompt(""); err != nil || v != "" {
		t.Errorf("empty ref: v=%q err=%v", v, err)
	}
	if _, err := docker.ResolveSystemPrompt("file:"+filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing sysprompt should error")
	}
	if _, err := docker.ResolveSystemPrompt("env:X"); err == nil {
		t.Error("non-file: ref should error")
	}
}

func TestLoadSecretLayers_DefaultAndOverride(t *testing.T) {
	// project layer переопределяет
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, ".afm", "secrets.env"), []byte("K=project\n"), 0600)
	m, err := docker.LoadSecretLayers("", proj)
	if err != nil {
		t.Fatal(err)
	}
	if m["K"] != "project" {
		t.Errorf("default project layer: %#v", m)
	}
	// override
	ov := filepath.Join(t.TempDir(), "ov.env")
	os.WriteFile(ov, []byte("K=override\n"), 0600)
	m2, _ := docker.LoadSecretLayers(ov, proj)
	if m2["K"] != "override" {
		t.Errorf("override layer: %#v", m2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run "TestLoadSecrets|TestResolveAuthValue|TestResolveSystemPrompt|TestLoadSecretLayers" -race`
Expected: FAIL / compile error (`docker.LoadSecrets undefined`, etc.).

- [ ] **Step 3: Implement** — `pkg/docker/secrets.go`:

```go
package docker

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// homeDir returns the host home directory (best-effort).
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// LoadSecrets parses one or more KEY=VALUE files (later files override earlier)
// into a map. Missing files are ignored. Blank lines and '#' comments skipped.
func LoadSecrets(paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		f, err := os.Open(expandHome(p, homeDir()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open secrets %s: %w", p, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read secrets %s: %w", p, err)
		}
	}
	return out, nil
}

// ResolveAuthValue resolves an auth.from reference ("env:VAR" | "file:PATH")
// against the loaded secrets map (env: form, map then os.Getenv) or by reading
// the file (file: form). ~ is expanded against the host home dir.
func ResolveAuthValue(from string, secrets map[string]string) (string, error) {
	switch {
	case strings.HasPrefix(from, "env:"):
		key := strings.TrimPrefix(from, "env:")
		if v, ok := secrets[key]; ok && v != "" {
			return v, nil
		}
		if v := os.Getenv(key); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret not found: env:%s (checked secrets file and process env)", key)
	case strings.HasPrefix(from, "file:"):
		path := expandHome(strings.TrimPrefix(from, "file:"), homeDir())
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("secret not found: %s: %w", from, err)
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("secret file is empty: %s", path)
	default:
		return "", fmt.Errorf("auth.from must be env:VAR or file:PATH, got %q", from)
	}
}

// ResolveSystemPrompt reads the content of a "file:PATH" system_prompt
// reference. Empty ref → ("", nil) (no sysprompt). ~ expanded against host home.
func ResolveSystemPrompt(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if !strings.HasPrefix(ref, "file:") {
		return "", fmt.Errorf("system_prompt must be file:PATH, got %q", ref)
	}
	path := expandHome(strings.TrimPrefix(ref, "file:"), homeDir())
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("system_prompt not found: %s: %w", path, err)
	}
	return string(data), nil
}

// loadSecretLayers resolves the secrets map from the default layers (global
// ~/.afm/secrets.env + project <projectDir>/.afm/secrets.env) or from override.
// Loaded only on the host.
func loadSecretLayers(override, projectDir string) (map[string]string, error) {
	var files []string
	if override != "" {
		files = []string{override}
	} else {
		files = []string{
			filepath.Join(homeDir(), ".afm", "secrets.env"),
			filepath.Join(projectDir, ".afm", "secrets.env"),
		}
	}
	return LoadSecrets(files)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/docker/ -run "TestLoadSecrets|TestResolveAuthValue|TestResolveSystemPrompt|TestLoadSecretLayers" -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/secrets.go pkg/docker/secrets_test.go
git commit -m "feat(docker): secrets.go — парсинг secrets.env и резолв секрета/sysprompt на хосте"
```

---

### Task 5: ScanCommands(skip generated) — не монтировать generated-команды

**Files:**
- Modify: `pkg/docker/launcher.go` (`ScanCommands` ~`:129`)
- Modify: `pkg/docker/launcher_test.go` (все 5 вызовов `ScanCommands`)
- Modify: `cmd/afm/run.go:73` (caller — передать `nil`, реальный set в Task 6)

**Interfaces:**
- Produces: `func ScanCommands(f *flow.Flow, globalCmd string, generated map[string]bool) []CommandMount`.

- [ ] **Step 1: Write the failing test** — добавь в `pkg/docker/launcher_test.go`:

```go
func TestScanCommands_SkipsGenerated(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "glm51")
	os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	f := &flow.Flow{
		Name: "test",
		Stages: []flow.Stage{
			{ID: "s1", Command: "glm51"}, // generated → не монтировать
		},
	}
	mounts := docker.ScanCommands(f, "claude", map[string]bool{"glm51": true})
	if len(mounts) != 0 {
		t.Errorf("generated command must not be mounted, got %d: %v", len(mounts), mounts)
	}

	// тот же flow, но glm51 не generated → монтируется
	mounts2 := docker.ScanCommands(f, "claude", nil)
	if len(mounts2) != 1 {
		t.Errorf("non-generated command must be mounted, got %d", len(mounts2))
	}
}
```

- [ ] **Step 2: Run test + adjust existing callers to verify it fails**

Сначала обнови существующие вызовы `ScanCommands` в `launcher_test.go` (5 штук) и `cmd/afm/run.go:73` на 3-аргументную форму с `nil`:

`cmd/afm/run.go:73`:
```go
				cmds := docker.ScanCommands(f, cfg.Client.Command, nil)
```

`launcher_test.go` — заменить `docker.ScanCommands(f, "...")` → `docker.ScanCommands(f, "...", nil)` во всех 5 местах (`TestScanCommands_SkipsClaude`, `_FindsBinary`, `_DeduplicatesCommands`, `_GlobalCmdMounted`, `_SkipsMissingBinary`).

Run: `go test ./pkg/docker/ -run TestScanCommands -race`
Expected: FAIL (новый тест: `ScanCommands` 2-аргументный → compile error, либо `generated` не учитывается).

- [ ] **Step 3: Implement** — замени `ScanCommands` в `pkg/docker/launcher.go`:

```go
// ScanCommands возвращает список нестандартных (не claude, не generated) агентов
// из flow, которые нужно смонтировать в Docker-контейнер. generated — команды с
// recipe (autoShim): они генерируются в контейнере, бинарник не монтируется.
// Бинарники, не найденные в PATH, молча пропускаются.
func ScanCommands(f *flow.Flow, globalCmd string, generated map[string]bool) []CommandMount {
	seen := make(map[string]bool)
	var mounts []CommandMount

	addCmd := func(cmd string) {
		if cmd == "" || cmd == "claude" || generated[cmd] || seen[cmd] {
			return
		}
		seen[cmd] = true
		hostPath, err := exec.LookPath(cmd)
		if err != nil {
			return
		}
		mounts = append(mounts, CommandMount{
			HostPath:      hostPath,
			ContainerName: filepath.Base(cmd),
		})
	}

	addCmd(globalCmd)
	for _, s := range f.Stages {
		addCmd(s.Command)
	}
	return mounts
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/docker/ ./cmd/afm/ -race`
Expected: PASS (все ScanCommands-тесты + cmd/afm тесты).

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/launcher.go pkg/docker/launcher_test.go cmd/afm/run.go
git commit -m "feat(docker): ScanCommands пропускает generated-команды (autoShim)"
```

---

### Task 6: Launcher — резолв секретов recipe → transient env + host-блок run.go

**Files:**
- Modify: `pkg/docker/launcher.go` (`ReExecConfig`, `ReExec`; добавить импорт `pkg/config`; добавить `UsedRecipeCommands`)
- Modify: `cmd/afm/run.go` (Docker-блок `:65-95`: validate + `ScanCommands(generated)` + передача `Recipes`/`SecretsFile`)
- Test: `pkg/docker/launcher_test.go` (добавить тест резолва)

**Interfaces:**
- Consumes: `config.AgentRecipe` (Task 1), `ResolveAuthValue`/`ResolveSystemPrompt`/`loadSecretLayers`/`envName` (Tasks 2,4).
- Produces: `ReExecConfig.Recipes map[string]config.AgentRecipe`; `ReExecConfig.SecretsFile string`; `func UsedRecipeCommands(f *flow.Flow, globalCmd string, recipes map[string]config.AgentRecipe) map[string]bool`.

- [ ] **Step 1: Write the failing test** — добавь в `pkg/docker/launcher_test.go`:

```go
func TestReExec_RecipeTransientEnv_NoMount(t *testing.T) {
	// секрет в host-only файле
	tokFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tokFile, []byte("secret-value\n"), 0600)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	// fake docker
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1", Auth: config.RecipeAuth{From: "file:" + tokFile, To: "env:ANTHROPIC_AUTH_TOKEN"}},
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image:       "akopichin/afm:latest",
		ProjectDir:  "/tmp/proj",
		ExtraArgs:   []string{"run", "flow.yaml"},
		Recipes:     recipes,
		SecretsFile: "",
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}

	argsStr := strings.Join(capturedArgs, " ")
	// transient bare-form -e AFM_SECRET_GLM51 (без значения в argv)
	if !strings.Contains(argsStr, "-e AFM_SECRET_GLM51") {
		t.Errorf("missing transient -e AFM_SECRET_GLM51: %s", argsStr)
	}
	// значение секрета НЕ в argv
	if strings.Contains(argsStr, "secret-value") {
		t.Errorf("secret value leaked into argv: %s", argsStr)
	}
	// AFM_URL нет (url читается из cfg в контейнере)
	if strings.Contains(argsStr, "AFM_URL_") {
		t.Errorf("AFM_URL_ must not be passed: %s", argsStr)
	}
	// cleanup transient env
	os.Unsetenv("AFM_SECRET_GLM51")
}

func TestReExec_RecipeMissingSecretFailFast(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"glm51": {Model: "glm-5.1", Auth: config.RecipeAuth{From: "file:" + filepath.Join(t.TempDir(), "nope"), To: "env:ANTHROPIC_AUTH_TOKEN"}},
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj", Recipes: recipes,
	})
	if err == nil {
		t.Fatal("expected fail-fast on missing secret")
	}
	if !strings.Contains(err.Error(), "glm51") {
		t.Errorf("error should name the agent: %v", err)
	}
}
```

(Добавь импорт `"github.com/akopichin/afm/pkg/config"` в `launcher_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/docker/ -run "TestReExec_Recipe" -race`
Expected: FAIL / compile error (`ReExecConfig.Recipes undefined`).

- [ ] **Step 3: Implement launcher.go** — добавь импорт `"github.com/akopichin/afm/pkg/config"` и расширь `ReExecConfig`:

```go
type ReExecConfig struct {
	Image         string
	ProjectDir    string
	Commands      []CommandMount
	DashboardPort int
	ExtraMounts   []string
	ExtraArgs     []string
	ClientCommand string
	Recipes       map[string]config.AgentRecipe // autoShim: команды с recipe → генерируются, секрет → transient env
	SecretsFile   string                        // опц. override для default-слоёв secrets.env
}
```

Добавь helper `UsedRecipeCommands` (рядом с `ScanCommands`):

```go
// UsedRecipeCommands returns the recipe keys that are actually referenced as a
// stage command or the global client command. Only these get generated wrappers.
func UsedRecipeCommands(f *flow.Flow, globalCmd string, recipes map[string]config.AgentRecipe) map[string]bool {
	used := map[string]bool{}
	check := func(cmd string) {
		if cmd == "" || cmd == "claude" {
			return
		}
		if _, ok := recipes[cmd]; ok {
			used[cmd] = true
		}
	}
	check(globalCmd)
	if f != nil {
		for _, s := range f.Stages {
			check(s.Command)
		}
	}
	return used
}
```

В `ReExec`, после блока передачи auth-env (`launcher.go:225-229`, цикл `for _, key := range []string{...}`), добавь резолв секретов recipe:

```go
	// autoShim: резолвим секреты recipe на хосте и передаём в контейнер как
	// transient bare-form env (значение в env afm, не в argv docker). Generated
	// врапперы внутри контейнера читают $AFM_SECRET_<CMD>/$AFM_SYSPROMPT_<CMD> и
	// unset'ят их до exec claude. Команды с recipe НЕ монтируются (ScanCommands).
	if len(cfg.Recipes) > 0 {
		secrets, err := loadSecretLayers(cfg.SecretsFile, cfg.ProjectDir)
		if err != nil {
			return err
		}
		for cmd, recipe := range cfg.Recipes {
			name := envName(cmd)
			val, vErr := ResolveAuthValue(recipe.Auth.From, secrets)
			if vErr != nil {
				return fmt.Errorf("agent %s: %w", cmd, vErr)
			}
			_ = os.Setenv("AFM_SECRET_"+name, val) // процесс далее exec'нет docker; утечки в argv нет
			args = append(args, "-e", "AFM_SECRET_"+name)
			if recipe.SystemPrompt != "" {
				sp, spErr := ResolveSystemPrompt(recipe.SystemPrompt)
				if spErr != nil {
					fmt.Fprintf(os.Stderr, "warning: agent %s: %v; skipping system prompt\n", cmd, spErr)
				} else if sp != "" {
					_ = os.Setenv("AFM_SYSPROMPT_"+name, sp)
					args = append(args, "-e", "AFM_SYSPROMPT_"+name)
				}
			}
		}
	}
```

- [ ] **Step 4: Wire host-блок run.go** — в `cmd/afm/run.go` Docker-блок (`:65-95`) замени блок между `CheckClaudeDockerAuth` и `ReExec`:

```go
				if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
					return err
				}
				if cfg.Docker.IsAutoShim() {
					if err := cfg.Docker.ValidateAgents(); err != nil {
						return err
					}
				}
				var generatedForMount map[string]bool
				var recipes map[string]config.AgentRecipe
				if cfg.Docker.IsAutoShim() {
					recipes = cfg.Docker.Agents
					generatedForMount = docker.UsedRecipeCommands(f, cfg.Client.Command, recipes)
				}
				cmds := docker.ScanCommands(f, cfg.Client.Command, generatedForMount)
```

И в `docker.ReExec(docker.ReExecConfig{...})` добавь поля (рядом с `ClientCommand`):

```go
						Recipes:     recipes,
						SecretsFile: cfg.Docker.SecretsFile,
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/docker/ ./cmd/afm/ -race`
Expected: PASS.

- [ ] **Step 6: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/docker/launcher.go pkg/docker/launcher_test.go cmd/afm/run.go
git commit -m "feat(docker,run): резолв секретов recipe на хосте → transient env; UsedRecipeCommands"
```

---

### Task 7: orchestrator — GeneratedAgents + proxyForCmd generated-aware

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (`Options` ~`:62`, `proxyForCmd` `:234`, 4 call sites `:114`,`:249`,`:274`,`:292`)
- Test: `pkg/orchestrator/proxy_cmd_test.go` (internal package `orchestrator`)

**Interfaces:**
- Produces: `Options.GeneratedAgents map[string]bool`; `func proxyForCmd(cmd, proxyURL, shimDir string, generated bool) (string, string)`.

- [ ] **Step 1: Write the failing test** — `pkg/orchestrator/proxy_cmd_test.go`:

```go
package orchestrator

import "testing"

func TestProxyForCmd(t *testing.T) {
	const proxy = "http://127.0.0.1:1"
	const shim = "/shim"
	cases := []struct {
		cmd            string
		generated      bool
		wantURL, wantShim string
	}{
		{"claude", false, "", ""},     // claude — OAuth, напрямую
		{"claude", true, "", ""},      // claude никогда не generated
		{"glm51", false, proxy, shim}, // mounted не-claude → прокси+shim (как раньше)
		{"glm51", true, "", shim},     // generated → self-route (BASE_URL bake'нут), wrapper-dir на PATH
	}
	for _, c := range cases {
		gotURL, gotShim := proxyForCmd(c.cmd, proxy, shim, c.generated)
		if gotURL != c.wantURL || gotShim != c.wantShim {
			t.Errorf("proxyForCmd(%q, gen=%v) = (%q,%q), want (%q,%q)",
				c.cmd, c.generated, gotURL, gotShim, c.wantURL, c.wantShim)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestProxyForCmd -race`
Expected: FAIL / compile error (`proxyForCmd expects 3 args`).

- [ ] **Step 3: Implement** — `pkg/orchestrator/orchestrator.go`:

В `Options` добавь поле (после `ProxyShimDir`):
```go
	GeneratedAgents map[string]bool // autoShim: команды с generated-враппером (self-route)
```

Замени `proxyForCmd` (`:234-239`):
```go
// proxyForCmd возвращает ProxyURL и ProxyShimDir для команды cmd.
// claude использует OAuth-токен и ходит напрямую — прокси не нужен.
// generated (autoShim) — враппер bake'ит ANTHROPIC_BASE_URL сам, поэтому
// executor НЕ инжектит ProxyURL; но wrapper-dir должен быть на PATH, чтобы
// команда разрешилась в сгенерированный скрипт.
func proxyForCmd(cmd, proxyURL, shimDir string, generated bool) (string, string) {
	if cmd == "claude" {
		return "", ""
	}
	if generated {
		return "", shimDir
	}
	return proxyURL, shimDir
}
```

Обнови 4 call site, чтобы передавать флаг generated:
- `:114`:
```go
		globalProxyURL, globalShimDir := proxyForCmd(opts.Config.Client.Command, opts.ProxyURL, opts.ProxyShimDir, opts.GeneratedAgents[opts.Config.Client.Command])
```
- `:249`:
```go
		pURL, pShim := proxyForCmd(s.Command, o.opts.ProxyURL, o.opts.ProxyShimDir, o.opts.GeneratedAgents[s.Command])
```
- `:274`:
```go
	pURL, pShim := proxyForCmd(cmd, o.opts.ProxyURL, o.opts.ProxyShimDir, o.opts.GeneratedAgents[cmd])
```
- `:292`:
```go
	pURL, pShim := proxyForCmd(s.Command, o.opts.ProxyURL, o.opts.ProxyShimDir, o.opts.GeneratedAgents[s.Command])
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/proxy_cmd_test.go
git commit -m "feat(orchestrator): proxyForCmd generated-aware + Options.GeneratedAgents"
```

---

### Task 8: run.go container-блок — CreateWrappers + Options wiring + удаление proxy.CreateShim

**Files:**
- Modify: `cmd/afm/run.go` (proxy-блок `:129-163`; `orchestrator.New` `:171-181`)
- Delete: `pkg/proxy/shim.go`, `pkg/proxy/shim_test.go`

**Interfaces:**
- Consumes: `docker.CreateWrappers`/`WrapperSpec`/`ResolveBaseURL`/`UsedRecipeCommands` (Tasks 2,3,6), `Options.GeneratedAgents` (Task 7).
- Удаляет зависимость `cmd/afm` от `proxy.CreateShim`.

- [ ] **Step 1: Refactor proxy-блок run.go** — замени блок `:129-163` (`var proxyAddr, proxyShimDir string` … `}`) на:

```go
			// Прокси запускается при proxy.enabled + непустом upstream. proxyAddr
			// нужен и для claude-shim (BaseURL), и для executor ProxyURL (mounted
			// не-claude агенты). Generated-агенты bake'ят BaseURL сами (host-match).
			var proxyAddr, proxyUpstream string
			proxyOn := false
			if cfg.Proxy.IsEnabled() {
				upstream := cfg.Proxy.Upstream
				if upstream == "" {
					upstream = os.Getenv("ANTHROPIC_BASE_URL")
				}
				if upstream != "" {
					transforms := proxy.BuildTransforms(upstream, cfg.Proxy.Transforms.ZAI)
					usageLogPath := ""
					if cfg.Accounting.IsEnabled() {
						usageLogPath = filepath.Join(runDir, "usage.jsonl")
					}
					p := proxy.New(upstream, transforms, usageLogPath)
					addr, err := p.Start(cfg.Proxy.Port)
					if err != nil {
						return fmt.Errorf("start proxy: %w", err)
					}
					defer p.Shutdown(context.Background()) //nolint:errcheck
					proxyAddr = addr
					proxyUpstream = upstream
					proxyOn = true
					fmt.Printf("  proxy: %s → %s\n", addr, upstream)
				} else {
					fmt.Println("  proxy: skipped (no upstream) — set proxy.upstream in config or export ANTHROPIC_BASE_URL to enable")
				}
			}

			// Единый wrapper-dir: claude proxy-shim (если proxy on) + generated
			// врапперы (если autoShim on и мы внутри контейнера). На хосте врапперы
			// не генерируются — реальные бинарники используются напрямую.
			var wrapperSpecs []docker.WrapperSpec
			if proxyOn {
				wrapperSpecs = append(wrapperSpecs, docker.WrapperSpec{Command: "claude", BaseURL: proxyAddr})
			}
			generatedAgents := map[string]bool{}
			if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
				if err := cfg.Docker.ValidateAgents(); err != nil {
					return err
				}
				for cmd := range docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents) {
					recipe := cfg.Docker.Agents[cmd]
					generatedAgents[cmd] = true
					wrapperSpecs = append(wrapperSpecs, docker.WrapperSpec{
						Command:      cmd,
						AuthTo:       recipe.Auth.EnvVarName(),
						BaseURL:      docker.ResolveBaseURL(recipe.URL, proxyOn, proxyUpstream, proxyAddr),
						Model:        recipe.Model,
						HasSysPrompt: recipe.SystemPrompt != "",
					})
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

Затем в `orchestrator.New(orchestrator.Options{...})` (`:171`) добавь поле (после `ProxyShimDir`):
```go
				GeneratedAgents: generatedAgents,
```

- [ ] **Step 2: Remove dead proxy.CreateShim**

```bash
git rm pkg/proxy/shim.go pkg/proxy/shim_test.go
```

(После переключения `run.go` на `docker.CreateWrappers` ни один код не вызывает `proxy.CreateShim` — `grep -rn "CreateShim" pkg/ cmd/` должен вернуть пусто.)

- [ ] **Step 3: Build + verify no dangling refs**

```bash
go build ./...
grep -rn "CreateShim" pkg/ cmd/ || echo "OK: no CreateShim refs"
```
Expected: build OK, "OK: no CreateShim refs".

- [ ] **Step 4: Run full test suite**

```bash
go test ./... -race
```
Expected: PASS (включая `pkg/proxy/` — `shim_test.go` удалён, остальные proxy-тесты зелёные).

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add cmd/afm/run.go
git commit -m "feat(run): единый wrapper-dir через docker.CreateWrappers; удалён proxy.CreateShim"
```

---

### Task 9: afm-init — `.afm/secrets.env` в project `.gitignore`

**Files:**
- Modify: `cmd/afm/init.go`

**Interfaces:**
- Produces: side-effect — дописывает `.afm/secrets.env` в `<cwd>/.gitignore`, если отсутствует.

- [ ] **Step 1: Write the failing test** — `cmd/afm/init_test.go` (если файла нет — создай; пакет `main`). Чтобы не запускать интерактив, вынеси логику в тестируемую функцию:

Добавь в `cmd/afm/init.go`:
```go
// ensureGitignoreEntry дописывает entry в .gitignore в repoDir, если его там ещё
// нет. Создаёт .gitignore при отсутствии. Используется afm-init, чтобы
// секреты recipe (.afm/secrets.env) не попали в VCS.
func ensureGitignoreEntry(repoDir, entry string) error {
	giPath := filepath.Join(repoDir, ".gitignore")
	data, _ := os.ReadFile(giPath)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil // уже есть
		}
	}
	content := entry + "\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		content = "\n" + content
	}
	f, err := os.OpenFile(giPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
```

Тест `cmd/afm/init_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureGitignoreEntry(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), ".afm/secrets.env") {
		t.Errorf("entry not written: %s", data)
	}
	// idempotent
	if err := ensureGitignoreEntry(dir, ".afm/secrets.env"); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if got := strings.Count(string(data2), ".afm/secrets.env"); got != 1 {
		t.Errorf("expected 1 entry, got %d: %s", got, data2)
	}
}
```
(добавь импорт `"strings"` в `init_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/afm/ -run TestEnsureGitignoreEntry -race`
Expected: FAIL (`ensureGitignoreEntry undefined`).

- [ ] **Step 3: Call it from afm-init** — в `newInitCmd` `RunE`, после `os.WriteFile(outPath, ...)` (перед финальным `fmt.Printf("\nCreated: ...`), добавь:
```go
				if err := ensureGitignoreEntry(".", ".afm/secrets.env"); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
				}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/afm/ -race`
Expected: PASS.

- [ ] **Step 5: Build + lint + commit**

```bash
go build ./...
make lint
git add cmd/afm/init.go cmd/afm/init_test.go
git commit -m "feat(init): afm-init добавляет .afm/secrets.env в .gitignore"
```

---

### Task 10: Документация (CLAUDE.md) + manual e2e verification

**Files:**
- Modify: `CLAUDE.md` (раздел «Docker Mode» — задокументировать `autoShim`)
- Manual verification (не коммитится, кроме правок CLAUDE.md)

- [ ] **Step 1: Build the binary** (codesign для macOS 26 — см. memory `afm-binary-update`):

```bash
make install
afm --version    # exit 0
```

- [ ] **Step 2: Configure a flow for Docker + autoShim**

В каталоге с тестовым flow создай `.afm/config.yaml`:
```yaml
docker:
  enabled: true
  autoShim: true
  image: akopichin/afm:latest
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      system_prompt: "file:~/.ai-free/claude-glm/system-prompt.md"
      auth:
        from: "file:~/.ai-free/claude-glm/token"
        to: "env:ANTHROPIC_AUTH_TOKEN"
```
(Связано с memory `docker-mode-stale-image`: образ должен быть свежим — `docker.image: akopichin/afm:latest` или `AFM_DOCKER_IMAGE`.)

- [ ] **Step 3: Run a flow that uses glm51 and verify**

```bash
afm run <flow.yaml>
# в отдельном терминале во время прогона:
docker ps   # найти контейнер afm
docker exec <container> sh -c 'which glm51; head -5 "$(which glm51)"'
```
Expected:
- `which glm51` → путь внутри wrapper-dir (`/tmp/fm-wrappers-…/glm51`), НЕ `/usr/local/bin/glm51`.
- В `docker run` argv (`docker inspect`/логи launcher) НЕТ `-v …/glm51:/usr/local/bin/glm51:ro` для glm51 (есть только transient `-e AFM_SECRET_GLM51` без значения).
- Агент glm51 отрабатывает стадию (DONE), модель в usage — `glm-5.1`.

- [ ] **Step 4: Verify backward compat** — прогони тот же flow БЕЗ `autoShim` (или с `autoShim: false`):
Expected: glm51 монтируется `:ro` как раньше (`docker run … -v …/glm51:/usr/local/bin/glm51:ro`), агент работает.

- [ ] **Step 5: Document in CLAUDE.md** — в раздел «Docker Mode» добавь подраздел (после «Нестандартные агенты»):

```markdown
### autoShim: генерируемые врапперы без монтирования

По `docker.autoShim: true` afm генерирует claude-совместимые врапперы для агентов,
описанных в `docker.agents.<cmd>` (recipe: `model`/`url`/`system_prompt`/`auth`),
прямо в контейнере — без `-v` монтирования хост-бинарника и без `extra_mounts`
для токенов. Секрет и контент system_prompt читаются на хосте и передаются в
контейнер как transient env (`AFM_SECRET_<CMD>`, `AFM_SYSPROMPT_<CMD>`); `url`/`model`
контейнер берёт из смонтированного `config.yaml`.

```yaml
docker:
  autoShim: true
  agents:
    glm51:
      model: glm-5.1
      url: https://api.z.ai/api/anthropic
      auth: { from: "file:~/.ai-free/claude-glm/token", to: "env:ANTHROPIC_AUTH_TOKEN" }
```

- `auth.to` ∈ {`env:CLAUDE_CODE_OAUTH_TOKEN`, `env:ANTHROPIC_API_KEY`, `env:ANTHROPIC_AUTH_TOKEN`}.
- Без recipe (при `autoShim: true`) команда монтируется `:ro` как раньше.
- z.ai-агенты идут через прокси (host-match), deepseek-агенты — напрямую.
- См. спек `docs/superpowers/specs/2026-07-14-docker-autoshim-design.md`.
```

- [ ] **Step 6: Final full suite + lint + commit docs**

```bash
go test ./... -race
make lint
git add CLAUDE.md
git commit -m "docs: autoShim — автогенерация врапперов в Docker (CLAUDE.md)"
```

---

## Self-Review (выполнено автором плана)

**Спек-покрытие:**
- Recipe schema + валидация → Task 1. ✓
- `autoShim` мастер-флаг + Docker-only + fallback на mount → Tasks 1 (`IsAutoShim`), 6 (`generatedForMount`), 8 (`AFM_IN_DOCKER` gate). ✓
- secrets_file (global+project merge, env:/file:) → Task 4 (`loadSecretLayers`, `ResolveAuthValue`). ✓
- Data flow (url/model из cfg; transient `AFM_SECRET`+`AFM_SYSPROMPT`; нет `AFM_URL`) → Tasks 4, 6, 8. ✓
- Wrapper template (auth export+unset, baked BASE_URL, 3 model vars, sysprompt 0600, exec abs realClaude) → Task 3. ✓
- claude-shim поглощён CreateWrappers → Tasks 3, 8. ✓
- Proxy host-match P2′ → Task 2 (`ResolveBaseURL`), используется в Task 8. ✓
- `proxyForCmd` generated-aware + `Options.GeneratedAgents` → Task 7. ✓
- `ScanCommands(skip generated)` → Task 5. ✓
- Built-in claude auth: хардкод `claudeAuthEnvVars` оставлен (functionalне нужен getter — см. ниже); список переиспользован как `config.ClaudeAuthEnvVars` для валидации `auth.to` → Task 1. ✓ (см. отклонение)
- afm-init `.gitignore` → Task 9. ✓
- Edge-cases (нет секрета → fail fast; claude не на PATH → hard error; sysprompt отсутствует → warn+skip; url пуст → inherits env; backward compat) → Tasks 3, 4, 6, 8, 10. ✓

**Отклонения от спека (смысловые, зафиксированы в плане):**
1. **`HostOf` локальный в `pkg/docker`, без рефактора `ZAITransform.Match`.** Спек предполагал, что `Match` извлекает host и его можно переиспользовать; на деле `Match` делает `strings.Contains(upstreamURL, "api.z.ai")` (substring, не host-extraction). Локальный `hostOf` (3 строки, `net/url`) проще и не создаёт coupling `pkg/docker`→`pkg/proxy`. `ZAITransform.Match` не трогается.
2. **Getter для `claudeAuthEnvVars` НЕ вводится.** Спек предлагал заменить хардкод getter'ом «из recipe-слоя». Functionalно это не нужно: `auth.to` для generated-агентов выставляется самим враппером (`export <to>=$AFM_SECRET_…`), launcher никогда не передаёт `auth.to` как `-e`. Список нужен только для валидации `auth.to` — он вынесен в `config.ClaudeAuthEnvVars` (Task 1), а `launcher.claudeAuthEnvVars` (хардкод для `-e` passthrough built-in claude, `launcher.go:225`) оставлен как есть.
3. **`WrapperSpec` без поля `RealClaude`.** `CreateWrappers` сам резолвит `claude` через `exec.LookPath` (один раз), поле избыточно.

**Placeholder scan:** плейсхолдеров нет; каждый шаг содержит исполняемый код или точную команду. ✓

**Type consistency:** `WrapperSpec{Command,AuthTo,BaseURL,Model,HasSysPrompt}` единый across Tasks 3/8; `proxyForCmd(cmd,proxyURL,shimDir,generated)` across Task 7; `ScanCommands(f,globalCmd,generated)` across Tasks 5/6/8; `ReExecConfig.Recipes/SecretsFile` across Tasks 6/8; `ResolveBaseURL(agentURL,proxyOn,proxyUpstream,proxyAddr)` across Tasks 2/8. ✓
