# openai-agent Tool-Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new autoShim recipe type, `openai-agent`, that gives OpenAI-compatible function-calling providers (starting with IdeaLab) a real single-tool (`bash`) ReAct loop, so afm's `agents: [auto]` / `interactive: true` stages can actually run through them — not just planning/review stages.

**Architecture:** A new adapter script (`scripts/openai-agent-as-claude.sh`) implements the loop itself (call the API with `stream: true` + a `tools` schema, reassemble the SSE deltas with `jq`, execute any `tool_calls` for real via `bash -c`, feed results back, repeat). The existing `docker.agents.<cmd>` recipe/wrapper-generation machinery (`pkg/config`, `pkg/docker/wrapper.go`, `cmd/afm/run.go`) is extended with one more recipe type, exactly parallel to how `openai`/`cursor`/`codex` already work.

**Tech Stack:** Go 1.x (existing `pkg/config`, `pkg/docker`, `cmd/afm`), bash + `curl` + `jq` (existing adapter-script convention).

## Global Constraints

- Do not change the Go version in `go.mod` (repo-wide user instruction).
- Every step that touches `.go` files must leave `make lint` clean (`golangci-lint run --fix ./...` via the Makefile) before that task's commit.
- No new tool surface beyond the single generic `bash` command — do not add distinct read/write/skill tools.
- No new sandboxing beyond the existing Docker container isolation.
- Commit messages must be in Russian, no `Co-Authored-By` trailer (repo-wide user instruction).
- Spec: `docs/superpowers/specs/2026-08-12-openai-agent-tool-loop-design.md`.

---

### Task 1: `openai-agent` recipe type in `pkg/config`

**Files:**
- Modify: `pkg/config/config.go:80-166`
- Test: `pkg/config/config_test.go` (new `TestAgentRecipe_OpenAIAgentType`, alongside the existing `TestAgentRecipe_OpenAIType` at line 462)

**Interfaces:**
- Produces: `config.RecipeTypeOpenAIAgent` (string constant, value `"openai-agent"`); `config.AgentRecipe.MaxTurns int` (new field, `yaml:"max_turns"`); `AgentRecipe.Validate()` accepts `openai-agent` recipes under the same rules as `openai`/`cursor` (model + url required, `auth.to` any `env:VAR`).

- [ ] **Step 1: Write the failing tests**

Add to `pkg/config/config_test.go` (anywhere after `TestAgentRecipe_OpenAIType`, e.g. right before `func TestAgentRecipe_CursorType`):

```go
func TestAgentRecipe_OpenAIAgentType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name: "valid openai-agent recipe",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
		},
		{
			name: "valid openai-agent recipe with max_turns",
			recipe: config.AgentRecipe{
				Type:     "openai-agent",
				Model:    "qwen3-max",
				URL:      "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:     config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
				MaxTurns: 10,
			},
		},
		{
			name: "openai-agent: missing model",
			recipe: config.AgentRecipe{
				Type: "openai-agent",
				URL:  "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth: config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "model is required",
		},
		{
			name: "openai-agent: missing url",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "env:OPENAI_API_KEY"},
			},
			errSub: "url is required",
		},
		{
			name: "openai-agent: auth.to not env:",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:IDEALAB_TOKEN", To: "OPENAI_API_KEY"},
			},
			errSub: "must be an env:",
		},
		{
			name: "openai-agent: any env: var allowed (not restricted to ClaudeAuthEnvVars)",
			recipe: config.AgentRecipe{
				Type:  "openai-agent",
				Model: "qwen3-max",
				URL:   "https://idealab.alibaba-inc.com/api/openai/v1",
				Auth:  config.RecipeAuth{From: "env:MY_CUSTOM_KEY", To: "env:MY_TARGET_KEY"},
			},
			// env: любой — ошибки нет
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
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/config/... -run TestAgentRecipe_OpenAIAgentType -v`
Expected: FAIL — `Type: "openai-agent"` is rejected by the current allow-list switch (`"recipe: type must be ..."`), so every non-error-expecting subtest fails.

- [ ] **Step 3: Implement the recipe type**

In `pkg/config/config.go`, change:

```go
const (
	RecipeTypeOpenAI = "openai"
	RecipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
	RecipeTypeCodex  = "codex"  // codex CLI через codex-as-claude (см. scripts/codex-as-claude.sh)
)
```

to:

```go
const (
	RecipeTypeOpenAI      = "openai"
	RecipeTypeOpenAIAgent = "openai-agent" // OpenAI-совместимый function calling + реальный tool-loop (см. scripts/openai-agent-as-claude.sh)
	RecipeTypeCursor      = "cursor"       // Cursor Cloud Agents API (async run-based, не chat completions)
	RecipeTypeCodex       = "codex"        // codex CLI через codex-as-claude (см. scripts/codex-as-claude.sh)
)
```

Change the `AgentRecipe` struct:

```go
type AgentRecipe struct {
	Type         string     `yaml:"type"`          // "" | "claude" = claude (default); "openai" = OpenAI-compatible; "cursor" = Cursor Cloud Agents API
	Model        string     `yaml:"model"`         // required → ANTHROPIC_DEFAULT_*_MODEL (claude) / OPENAI_MODEL (openai) / CURSOR_MODEL (cursor)
	URL          string     `yaml:"url"`           // optional (claude); required (openai, cursor) — agent gateway
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
}
```

to:

```go
type AgentRecipe struct {
	Type         string     `yaml:"type"`          // "" | "claude" = claude (default); "openai" = OpenAI-compatible; "openai-agent" = OpenAI-compatible + real tool-loop; "cursor" = Cursor Cloud Agents API
	Model        string     `yaml:"model"`         // required → ANTHROPIC_DEFAULT_*_MODEL (claude) / OPENAI_MODEL (openai, openai-agent) / CURSOR_MODEL (cursor)
	URL          string     `yaml:"url"`           // optional (claude); required (openai, openai-agent, cursor) — agent gateway
	SystemPrompt string     `yaml:"system_prompt"` // optional; "file:<path>" → --append-system-prompt-file content
	Auth         RecipeAuth `yaml:"auth"`          // required
	MaxTurns     int        `yaml:"max_turns"`     // openai-agent only: max tool-call iterations per stage invocation; 0 → script default (40)
}
```

Change the `Validate()` switch statement:

```go
	switch r.Type {
	case "", ClaudeCommand, RecipeTypeOpenAI, RecipeTypeCursor, RecipeTypeCodex:
	default:
		return fmt.Errorf("recipe: type must be \"\", \"claude\", \"openai\", \"cursor\", or \"codex\"; got %q", r.Type)
	}
```

to:

```go
	switch r.Type {
	case "", ClaudeCommand, RecipeTypeOpenAI, RecipeTypeOpenAIAgent, RecipeTypeCursor, RecipeTypeCodex:
	default:
		return fmt.Errorf("recipe: type must be \"\", \"claude\", \"openai\", \"openai-agent\", \"cursor\", or \"codex\"; got %q", r.Type)
	}
```

And change the url-required branch:

```go
	// openai и cursor — внешние шлюзы: url обязателен, auth.to не ограничен ClaudeAuthEnvVars
	// (используют свои env vars: OPENAI_API_KEY, CURSOR_API_KEY и т.д.).
	if r.Type == RecipeTypeOpenAI || r.Type == RecipeTypeCursor {
		if r.URL == "" {
			return fmt.Errorf("recipe: url is required for type: %s", r.Type)
		}
		return nil
	}
```

to:

```go
	// openai, openai-agent и cursor — внешние шлюзы: url обязателен, auth.to не
	// ограничен ClaudeAuthEnvVars (используют свои env vars: OPENAI_API_KEY, CURSOR_API_KEY и т.д.).
	if r.Type == RecipeTypeOpenAI || r.Type == RecipeTypeOpenAIAgent || r.Type == RecipeTypeCursor {
		if r.URL == "" {
			return fmt.Errorf("recipe: url is required for type: %s", r.Type)
		}
		return nil
	}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/config/... -v`
Expected: PASS (all of `TestAgentRecipe_OpenAIAgentType` plus every pre-existing test in the package, unaffected).

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): добавляем recipe type openai-agent для tool-loop автошима

Параллельно существующим openai/cursor/codex — модель+url обязательны,
auth.to не ограничен ClaudeAuthEnvVars. Новое поле MaxTurns — предел
tool-вызовов за стадию, используется только этим типом.
EOF
)"
```

---

### Task 2: `openai-agent` wrapper generation in `pkg/docker`

**Files:**
- Modify: `pkg/docker/wrapper.go:35-43` (WrapperSpec), `pkg/docker/wrapper.go:60-83` (CreateWrappers claude/codex LookPath gating), `pkg/docker/wrapper.go:102-125` (generateWrapper)
- Test: `pkg/docker/wrapper_test.go` (new `TestCreateWrappers_OpenAIAgentTemplate`, `TestCreateWrappers_OpenAIAgentNoClaudeRequired`, alongside the existing `TestCreateWrappers_OpenAITemplate` at line 143)

**Interfaces:**
- Consumes: `config.RecipeTypeOpenAIAgent` (from Task 1).
- Produces: `docker.WrapperSpec.MaxTurns int` (new field); a wrapper script body for `Type: config.RecipeTypeOpenAIAgent` that exports `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL`/`OPENAI_AGENT_MAX_TURNS` (the last only when `MaxTurns != 0`) and execs `/usr/local/bin/openai-agent-as-claude "$@"`.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/docker/wrapper_test.go`, right after `TestCreateWrappers_OpenAINoClaudeRequired` (before `TestCreateWrappers_MixedTypes_RequiresClaude`):

```go
func TestCreateWrappers_OpenAIAgentTemplate(t *testing.T) {
	// openai-agent-тип не требует claude в PATH
	t.Setenv("PATH", t.TempDir())

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:     config.RecipeTypeOpenAIAgent,
		Command:  "idealab",
		AuthTo:   "OPENAI_API_KEY",
		BaseURL:  "https://idealab.alibaba-inc.com/api/openai/v1",
		Model:    "qwen3-max",
		MaxTurns: 25,
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (openai-agent): %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "idealab"))
	s := string(script)

	wantSubstrings := []string{
		"#!/bin/sh",
		`export OPENAI_API_KEY="$AFM_SECRET_IDEALAB"`,
		"unset AFM_SECRET_IDEALAB",
		`export OPENAI_BASE_URL="https://idealab.alibaba-inc.com/api/openai/v1"`,
		`export OPENAI_MODEL="qwen3-max"`,
		`export OPENAI_AGENT_MAX_TURNS="25"`,
		`exec /usr/local/bin/openai-agent-as-claude "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("openai-agent wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	if strings.Contains(s, "openai-as-claude") && !strings.Contains(s, "openai-agent-as-claude") {
		t.Errorf("openai-agent wrapper must exec openai-agent-as-claude, not openai-as-claude:\n%s", s)
	}
}

func TestCreateWrappers_OpenAIAgentTemplate_NoMaxTurns(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:    config.RecipeTypeOpenAIAgent,
		Command: "idealab",
		AuthTo:  "OPENAI_API_KEY",
		BaseURL: "https://idealab.alibaba-inc.com/api/openai/v1",
		Model:   "qwen3-max",
		// MaxTurns не задан (0) — переменная не экспортируется, скрипт берёт свой дефолт.
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (openai-agent): %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "idealab"))
	s := string(script)
	if strings.Contains(s, "OPENAI_AGENT_MAX_TURNS") {
		t.Errorf("wrapper must not export OPENAI_AGENT_MAX_TURNS when MaxTurns is 0:\n%s", s)
	}
}

func TestCreateWrappers_OpenAIAgentNoClaudeRequired(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := CreateWrappers([]WrapperSpec{
		{Type: config.RecipeTypeOpenAIAgent, Command: "idealab", Model: "m", BaseURL: "http://x", AuthTo: "OPENAI_API_KEY"},
	})
	if err != nil {
		t.Errorf("openai-agent-only wrappers must not fail when claude absent: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/docker/... -run TestCreateWrappers_OpenAIAgent -v`
Expected: FAIL — `WrapperSpec` has no `MaxTurns` field yet (compile error) and `generateWrapper` has no `RecipeTypeOpenAIAgent` branch, so it falls through to the claude template and produces `ANTHROPIC_*` output instead.

- [ ] **Step 3: Implement wrapper generation**

In `pkg/docker/wrapper.go`, add a field to `WrapperSpec`:

```go
type WrapperSpec struct {
	Type         string // "" | "claude" = claude template; "openai" = openai-compatible; "cursor" = Cursor Cloud Agents
	Command      string
	AuthTo       string // auth env var name ("" for claude proxy-shim)
	BaseURL      string // baked gateway URL; "" → omit
	Model        string // model string; "" → claude proxy-shim for claude type
	HasSysPrompt bool   // emit sysprompt block (claude type only)
	Bare         bool   // prepend --bare to claude exec (skip CLAUDE.md/hooks/skills auto-context)
}
```

to:

```go
type WrapperSpec struct {
	Type         string // "" | "claude" = claude template; "openai" = openai-compatible; "openai-agent" = openai-compatible tool-loop; "cursor" = Cursor Cloud Agents
	Command      string
	AuthTo       string // auth env var name ("" for claude proxy-shim)
	BaseURL      string // baked gateway URL; "" → omit
	Model        string // model string; "" → claude proxy-shim for claude type
	HasSysPrompt bool   // emit sysprompt block (claude type only)
	Bare         bool   // prepend --bare to claude exec (skip CLAUDE.md/hooks/skills auto-context)
	MaxTurns     int    // openai-agent only: OPENAI_AGENT_MAX_TURNS; 0 → omit (script default)
}
```

In `CreateWrappers`, the claude-LookPath gate currently reads:

```go
	var realClaude string
	for _, s := range specs {
		if s.Type != config.RecipeTypeOpenAI && s.Type != config.RecipeTypeCursor && s.Type != config.RecipeTypeCodex {
```

Change to:

```go
	var realClaude string
	for _, s := range specs {
		if s.Type != config.RecipeTypeOpenAI && s.Type != config.RecipeTypeOpenAIAgent && s.Type != config.RecipeTypeCursor && s.Type != config.RecipeTypeCodex {
```

In `generateWrapper`, add a new branch right after the existing `RecipeTypeOpenAI` block (before the `RecipeTypeCursor` block):

```go
	if s.Type == config.RecipeTypeOpenAIAgent {
		// openai-compatible tool-loop: OPENAI_* vars + exec openai-agent-as-claude.
		// realClaude не нужен — openai-agent-as-claude не вызывает claude.
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
		if s.MaxTurns != 0 {
			fmt.Fprintf(&b, "export OPENAI_AGENT_MAX_TURNS=%q\n", strconv.Itoa(s.MaxTurns))
		}
		b.WriteString("exec /usr/local/bin/openai-agent-as-claude \"$@\"\n")
		return b.String(), nil
	}
```

Add `"strconv"` to the import block at the top of `pkg/docker/wrapper.go` (alongside the existing `"errors"`, `"fmt"`, `"os"`, `"os/exec"`, `"path/filepath"`, `"strings"`).

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/docker/... -v`
Expected: PASS (all of the new tests plus every pre-existing test in the package).

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "$(cat <<'EOF'
feat(docker): генерация враппера для recipe type openai-agent

Экспортирует OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL(+OPENAI_AGENT_MAX_TURNS)
и exec'ает openai-agent-as-claude — параллельно уже существующей ветке для
type: openai.
EOF
)"
```

---

### Task 3: Thread `MaxTurns` through `cmd/afm/run.go`

**Files:**
- Modify: `cmd/afm/run.go:506-516` (`buildWrapperSpec`)
- Test: `cmd/afm/run_test.go` (extend `TestBuildWrapperSpec`)

**Interfaces:**
- Consumes: `config.AgentRecipe.MaxTurns` (Task 1), `docker.WrapperSpec.MaxTurns` (Task 2).
- Produces: `buildWrapperSpec` copies `recipe.MaxTurns` into the returned `WrapperSpec.MaxTurns` unconditionally (0 stays 0, matching Task 2's "omit when zero" behavior).

- [ ] **Step 1: Write the failing test**

Add a new subtest inside `TestBuildWrapperSpec` in `cmd/afm/run_test.go`, after the existing `"BaseURL is recipe URL"` subtest:

```go
	t.Run("MaxTurns passed through", func(t *testing.T) {
		withMaxTurns := openaiRecipe
		withMaxTurns.MaxTurns = 15
		spec := buildWrapperSpec("idealab", withMaxTurns, true)
		if spec.MaxTurns != 15 {
			t.Errorf("MaxTurns: got %d, want 15", spec.MaxTurns)
		}
	})

	t.Run("MaxTurns zero when recipe doesn't set it", func(t *testing.T) {
		spec := buildWrapperSpec("idealab", openaiRecipe, true)
		if spec.MaxTurns != 0 {
			t.Errorf("MaxTurns: got %d, want 0", spec.MaxTurns)
		}
	})
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./cmd/afm/... -run TestBuildWrapperSpec -v`
Expected: FAIL — compile error, `docker.WrapperSpec` literal in `buildWrapperSpec` doesn't set `MaxTurns` yet but the test references `spec.MaxTurns`, which only fails at the assertion (not a compile error, since the field exists from Task 2) — the "MaxTurns passed through" subtest fails because `buildWrapperSpec` never copies `recipe.MaxTurns`, so `spec.MaxTurns` is always 0.

- [ ] **Step 3: Implement**

In `cmd/afm/run.go`, change:

```go
func buildWrapperSpec(cmd string, recipe config.AgentRecipe, bare bool) docker.WrapperSpec {
	return docker.WrapperSpec{
		Type:         recipe.Type,
		Command:      cmd,
		AuthTo:       recipe.Auth.EnvVarName(),
		BaseURL:      recipe.URL,
		Model:        recipe.Model,
		HasSysPrompt: recipe.SystemPrompt != "",
		Bare:         bare,
	}
}
```

to:

```go
func buildWrapperSpec(cmd string, recipe config.AgentRecipe, bare bool) docker.WrapperSpec {
	return docker.WrapperSpec{
		Type:         recipe.Type,
		Command:      cmd,
		AuthTo:       recipe.Auth.EnvVarName(),
		BaseURL:      recipe.URL,
		Model:        recipe.Model,
		HasSysPrompt: recipe.SystemPrompt != "",
		Bare:         bare,
		MaxTurns:     recipe.MaxTurns,
	}
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./cmd/afm/... -run TestBuildWrapperSpec -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add cmd/afm/run.go cmd/afm/run_test.go
git commit -m "$(cat <<'EOF'
feat(run): пробрасываем recipe.MaxTurns в WrapperSpec

Завершает проводку max_turns от config.yaml до генерируемого враппера.
EOF
)"
```

---

### Task 4: The `openai-agent-as-claude.sh` adapter script

**Files:**
- Create: `scripts/openai-agent-as-claude.sh`
- Test: `pkg/executor/openai_agent_translator_test.go` (new file)

**Interfaces:**
- Consumes: env vars `OPENAI_API_KEY` (required), `OPENAI_BASE_URL` (default `https://api.openai.com/v1`), `OPENAI_MODEL` (default `gpt-4o`), `OPENAI_AGENT_MAX_TURNS` (default `40`); prompt via stdin.
- Produces on stdout: zero or more `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"..."}}]}}` lines (one per executed tool call, printed live), then exactly one `{"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}` line, then `{"type":"result","subtype":"success"}`. Exits 0 on any normal completion (including hitting `OPENAI_AGENT_MAX_TURNS`); exits 1 with a diagnostic on stderr if the API request itself fails (non-2xx or curl error).

This task has no dependency on Tasks 1–3 (pure bash/curl/jq, no Go config plumbing) and can run in parallel with them.

- [ ] **Step 1: Write the failing tests**

Create `pkg/executor/openai_agent_translator_test.go`:

```go
package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentScriptPath returns the absolute path to scripts/openai-agent-as-claude.sh.
func agentScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	p := filepath.Join(root, "scripts", "openai-agent-as-claude.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("script not found: %s: %v", p, err)
	}
	return p
}

// writeStatefulFakeCurl creates a fake curl on PATH that returns responses[n]
// (clamped to the last entry once exhausted) on its (n+1)-th invocation —
// the real script calls curl once per loop turn, so this lets a test control
// what each successive turn sees. Each invocation's full argument list is
// captured to <captureDir>/call_<n>.args so a test can inspect exactly what
// request body a given turn sent (proving real tool output made it back into
// history, not just that the script printed something plausible). Each
// response body must be complete SSE text (including "data: [DONE]"); this
// helper appends the "\n<httpCode>" suffix mirroring the script's own
// `curl -w '\n%{http_code}'` usage.
func writeStatefulFakeCurl(t *testing.T, responses []string, httpCode string) (fakeCurlDir, captureDir string) {
	t.Helper()
	fakeCurlDir = t.TempDir()
	respDir := t.TempDir()
	captureDir = t.TempDir()
	for i, r := range responses {
		if err := os.WriteFile(filepath.Join(respDir, fmt.Sprintf("%d", i)), []byte(r), 0o644); err != nil {
			t.Fatalf("write response %d: %v", i, err)
		}
	}
	counterFile := filepath.Join(fakeCurlDir, "counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	script := fmt.Sprintf(`#!/usr/bin/env bash
n=$(cat %q)
printf '%%s' "$*" > %q/"call_$n.args"
idx=$n
max=%d
if [ "$idx" -gt "$max" ]; then idx=$max; fi
cat %q/"$idx"
printf '\n%s'
echo $((n + 1)) > %q
`, counterFile, captureDir, len(responses)-1, respDir, httpCode, counterFile)
	curlPath := filepath.Join(fakeCurlDir, "curl")
	if err := os.WriteFile(curlPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return fakeCurlDir, captureDir
}

func runAgentScript(t *testing.T, fakeCurlDir, prompt string, extraEnv ...string) (stdout string, err error) {
	t.Helper()
	cmd := exec.Command("bash", agentScriptPath(t))
	env := append(os.Environ(),
		"PATH="+fakeCurlDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OPENAI_API_KEY=test",
		"OPENAI_BASE_URL=http://fake/v1",
		"OPENAI_MODEL=m",
	)
	cmd.Env = append(env, extraEnv...)
	cmd.Stdin = strings.NewReader(prompt)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func countCaptures(t *testing.T, captureDir string) int {
	t.Helper()
	entries, err := os.ReadDir(captureDir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	return len(entries)
}

// TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer запускает РЕАЛЬНЫЙ
// scripts/openai-agent-as-claude.sh с поддельным curl (без сети). Первый
// ответ — стримингом (по чанкам, как настоящий IdeaLab) переданный tool_call
// для bash("echo hello-from-tool"), второй — финальный текст без tool_calls.
// Проверяет: скрипт реально выполняет команду (не просто печатает текст),
// передаёт её вывод обратно в историю следующего запроса, эмитит live
// tool_use конверт формы, которую понимает ParseToolAction/дашборд, и
// завершается success-конвертом.
func TestOpenAIAgentAsClaude_SingleToolCallThenFinalAnswer(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	turn1 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"bash","arguments":""},"type":"function","index":0}]},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"","function":{"arguments":"{\"command\": \"echo hello-from-tool\"}"},"type":"function","index":0}]},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"","function":{"arguments":""},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	turn2 := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"All done"},"finish_reason":""}]}
data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{turn1, turn2}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "do the thing\n")
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 2 {
		t.Fatalf("expected exactly 2 curl invocations, got %d", got)
	}

	call1, readErr := os.ReadFile(filepath.Join(captureDir, "call_1.args"))
	if readErr != nil {
		t.Fatalf("read call_1.args: %v", readErr)
	}
	if !strings.Contains(string(call1), "hello-from-tool") {
		t.Errorf("second request did not include real tool output:\n%s", call1)
	}
	if !strings.Contains(string(call1), `"tool_call_id":"call_1"`) {
		t.Errorf("second request missing tool_call_id linking back to call_1:\n%s", call1)
	}
	if !strings.Contains(string(call1), `"role":"tool"`) {
		t.Errorf("second request missing role:tool message:\n%s", call1)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var toolUseLine, textLine, resultLine string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, `"type":"tool_use"`) && toolUseLine == "" {
			toolUseLine = l
		}
		if strings.Contains(l, `"type":"text"`) && textLine == "" {
			textLine = l
		}
		if strings.Contains(l, `"type":"result"`) && resultLine == "" {
			resultLine = l
		}
	}
	if toolUseLine == "" {
		t.Fatalf("no tool_use envelope in output:\n%s", out)
	}
	toolName, detail, ok := ParseToolAction(toolUseLine, 0)
	if !ok || toolName != "Bash" || detail != "echo hello-from-tool" {
		t.Errorf("ParseToolAction(toolUseLine) = (%q, %q, %v), want (\"Bash\", \"echo hello-from-tool\", true)", toolName, detail, ok)
	}
	if textLine == "" {
		t.Fatalf("no final text envelope in output:\n%s", out)
	}
	ev, ok := parseStreamEvent(textLine)
	if !ok || len(ev.Message.Content) == 0 || ev.Message.Content[0].Text != "All done" {
		t.Errorf("final envelope wrong: %s", textLine)
	}
	if resultLine == "" || !strings.Contains(resultLine, `"subtype":"success"`) {
		t.Errorf("missing/bad result line:\n%s", out)
	}
}

// TestOpenAIAgentAsClaude_MaxTurnsReached: поддельный curl всегда возвращает
// новый tool_call и никогда не завершает работу — проверяет, что скрипт
// останавливается ровно после OPENAI_AGENT_MAX_TURNS обращений (не крутится
// бесконечно), и что это не считается ошибкой скрипта (exit 0, а не 1) —
// afm's существующий retry для незавершённой autonomous-стадии уже это покрывает.
func TestOpenAIAgentAsClaude_MaxTurnsReached(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	alwaysToolCall := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_x","function":{"name":"bash","arguments":"{\"command\": \"true\"}"},"type":"function","index":0}]},"finish_reason":"tool_calls"}]}
data: [DONE]
`
	fakeCurlDir, captureDir := writeStatefulFakeCurl(t, []string{alwaysToolCall}, "200")

	out, err := runAgentScript(t, fakeCurlDir, "loop forever\n", "OPENAI_AGENT_MAX_TURNS=2")
	if err != nil {
		t.Fatalf("script should exit 0 on max-turns cap, got error: %v\noutput:\n%s", err, out)
	}

	if got := countCaptures(t, captureDir); got != 2 {
		t.Fatalf("expected exactly OPENAI_AGENT_MAX_TURNS=2 curl invocations, got %d", got)
	}
	if !strings.Contains(out, "max turns reached") {
		t.Errorf("expected max-turns note in output, got:\n%s", out)
	}
	if !strings.Contains(out, `"subtype":"success"`) {
		t.Errorf("expected a success result line even after hitting max turns, got:\n%s", out)
	}
}

// TestOpenAIAgentAsClaude_APIFailureExitsNonZero: поддельный curl возвращает
// HTTP 500 на первом же обращении — скрипт обязан завершиться с ошибкой (в
// отличие от openai-as-claude.sh, который проглатывает сбой curl в пустой
// success: здесь "тихий успех" означал бы, что afm принимает пустой/
// незавершённый tool-loop за штатный незавершённый прогон вместо немедленно
// видимого сбоя).
func TestOpenAIAgentAsClaude_APIFailureExitsNonZero(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	fakeCurlDir, _ := writeStatefulFakeCurl(t, []string{`data: {"error":"server error"}` + "\n"}, "500")

	out, err := runAgentScript(t, fakeCurlDir, "x\n")
	if err == nil {
		t.Fatalf("expected script to exit non-zero on HTTP 500, got success. output:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./pkg/executor/... -run TestOpenAIAgentAsClaude -v`
Expected: FAIL — `scripts/openai-agent-as-claude.sh` doesn't exist yet, so `agentScriptPath` calls `t.Fatalf` in every subtest.

- [ ] **Step 3: Implement the script**

Create `scripts/openai-agent-as-claude.sh` with exactly this content (already validated by hand against a stateful fake `curl` for all three scenarios above, and against the live IdeaLab API for a real multi-tool-call task):

```bash
#!/usr/bin/env bash
# openai-agent-as-claude.sh — real tool-loop translator for OpenAI-compatible
# providers with function calling (IdeaLab, and any other /chat/completions
# gateway that supports `tools`). Unlike openai-as-claude.sh (one-shot text),
# this gives the model exactly one tool — `bash` — and loops: call the API,
# execute any tool_calls for real, feed results back, repeat until the model
# answers with plain text or OPENAI_AGENT_MAX_TURNS is reached.
#
# environment variables:
#   OPENAI_API_KEY         — токен авторизации (обязателен)
#   OPENAI_BASE_URL        — базовый URL API (дефолт: https://api.openai.com/v1)
#   OPENAI_MODEL           — модель (дефолт: gpt-4o)
#   OPENAI_AGENT_MAX_TURNS — макс. число tool-вызовов за стадию (дефолт: 40)

set -euo pipefail

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# игнорируем все claude CLI флаги (--model, --effort, --dangerously-skip-permissions и т.д.)
while [[ $# -gt 0 ]]; do
    shift
done

if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (openai-agent-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)

if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
OPENAI_MODEL="${OPENAI_MODEL:-gpt-4o}"
OPENAI_AGENT_MAX_TURNS="${OPENAI_AGENT_MAX_TURNS:-40}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

messages_file=$(mktemp)
trap 'rm -f "$messages_file" "${messages_file}.tmp"' EXIT

jq -nc --arg sys "$system_prompt" --arg user "$prompt" \
    '[{role:"system", content:$sys}, {role:"user", content:$user}]' > "$messages_file"

final_text=""
turn=0
max_turns_reached=0

while :; do
    turn=$((turn + 1))
    if [[ "$turn" -gt "$OPENAI_AGENT_MAX_TURNS" ]]; then
        max_turns_reached=1
        break
    fi

    request_body=$(jq -nc --slurpfile msgs "$messages_file" --arg model "$OPENAI_MODEL" --argjson tools "$tools_json" \
        '{model: $model, stream: true, tool_choice: "auto", tools: $tools, messages: $msgs[0]}')

    set +e
    response=$(curl -sS -w '\n%{http_code}' \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $OPENAI_API_KEY" \
        -d "$request_body" \
        "${OPENAI_BASE_URL}/chat/completions")
    curl_exit=$?
    set -e

    http_code=$(printf '%s' "$response" | tail -n1)
    body=$(printf '%s' "$response" | sed '$d')

    if [[ "$curl_exit" -ne 0 || "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "error: request to $OPENAI_BASE_URL failed (curl exit $curl_exit, http $http_code): $body" >&2
        exit 1
    fi

    reassembled=$(printf '%s' "$body" | jq -R -s '
        split("\n")
        | map(select(startswith("data: ")) | sub("^data: ";""))
        | map(select(test("^\\{")))
        | map(fromjson? // empty)
        | map(select(.choices != null and (.choices | length) > 0))
        | map(.choices[0]) as $choices
        | {
            content: ($choices | map(.delta.content // "") | join("")),
            tool_calls: (
                $choices
                | map(.delta.tool_calls // [])
                | flatten
                | group_by(.index)
                | map({
                    index: .[0].index,
                    id: ([.[] | .id // "" | select(. != "")] | first // ""),
                    name: ([.[] | .function.name // "" | select(. != "")] | first // ""),
                    arguments: ([.[] | .function.arguments // ""] | join(""))
                  })
            ),
            finish_reason: ([$choices[] | .finish_reason // "" | select(. != "")] | last // "")
          }
    ')

    tool_call_count=$(printf '%s' "$reassembled" | jq '.tool_calls | length')

    if [[ "$tool_call_count" -eq 0 ]]; then
        final_text=$(printf '%s' "$reassembled" | jq -r '.content')
        break
    fi

    assistant_msg=$(printf '%s' "$reassembled" | jq -c \
        '{role:"assistant", content: (.content // ""), tool_calls: [.tool_calls[] | {id: .id, type: "function", function: {name: .name, arguments: .arguments}}]}')
    jq -c --argjson m "$assistant_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"

    for i in $(seq 0 $((tool_call_count - 1))); do
        call=$(printf '%s' "$reassembled" | jq -c ".tool_calls[$i]")
        call_id=$(printf '%s' "$call" | jq -r '.id')
        command=$(printf '%s' "$call" | jq -r '.arguments | fromjson? | .command // empty')

        # живой tool_use конверт — сразу в stdout: сбрасывает idle-timer и рисуется
        # в event feed дашборда (та же форма, что и реальный Bash tool_use у claude).
        jq -nc --arg cmd "$command" '{type:"assistant", message:{content:[{type:"tool_use", name:"Bash", input:{command:$cmd}}]}}'

        set +e
        tool_output=$(bash -c "$command" 2>&1)
        tool_exit=$?
        set -e
        if [[ ${#tool_output} -gt 15000 ]]; then
            tool_output="${tool_output:0:15000}
[...truncated]"
        fi
        tool_output="${tool_output}
[exit code: ${tool_exit}]"

        tool_msg=$(jq -nc --arg id "$call_id" --arg out "$tool_output" '{role:"tool", tool_call_id:$id, content:$out}')
        jq -c --argjson m "$tool_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"
    done
done

if [[ "$max_turns_reached" -eq 1 ]]; then
    final_text="${final_text}
[openai-agent: max turns reached, stopping]"
fi

jq -nc --arg t "$final_text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'
echo '{"type":"result","subtype":"success"}'
```

Make it executable: `chmod +x scripts/openai-agent-as-claude.sh`.

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./pkg/executor/... -v`
Expected: PASS (all three new tests, plus every pre-existing test in the package — in particular `TestOpenAIAsClaude_*` in `openai_translator_test.go` must be unaffected since this is a new file, not a modification of the existing one).

- [ ] **Step 5: Lint and commit**

```bash
make lint
git add scripts/openai-agent-as-claude.sh pkg/executor/openai_agent_translator_test.go
git commit -m "$(cat <<'EOF'
feat(scripts): openai-agent-as-claude — реальный tool-loop поверх function calling

Один инструмент bash: модель сама читает/пишет файлы, гоняет скрипты,
поллит диалоговые файлы и подгружает skill-инструкции через него же.
Реассемблинг стримингового tool_calls по index, max_turns с мягкой
остановкой, hard fail при сбое запроса к API.
EOF
)"
```

---

### Task 5: Ship the adapter in the Docker image

**Files:**
- Modify: `Dockerfile.runtime:76-78` (right after the existing `openai-as-claude` COPY block)

**Interfaces:**
- Consumes: `scripts/openai-agent-as-claude.sh` (Task 4).
- Produces: `/usr/local/bin/openai-agent-as-claude`, executable, present in `akopichin/afm:latest` after `make docker-build`.

- [ ] **Step 1: Add the COPY block**

In `Dockerfile.runtime`, right after:

```dockerfile
# openai-as-claude: транслятор для OpenAI-совместимых провайдеров (cursor, deepseek, и т.д.)
COPY scripts/openai-as-claude.sh /usr/local/bin/openai-as-claude
RUN chmod +x /usr/local/bin/openai-as-claude
```

insert:

```dockerfile
# openai-agent-as-claude: реальный tool-loop (bash tool + function calling) поверх
# OpenAI-совместимых провайдеров с tool-calling (IdeaLab и т.п.)
COPY scripts/openai-agent-as-claude.sh /usr/local/bin/openai-agent-as-claude
RUN chmod +x /usr/local/bin/openai-agent-as-claude
```

- [ ] **Step 2: Verify the Dockerfile change is syntactically sound**

`Dockerfile.runtime`'s final stage (`FROM ubuntu:24.04`, no stage name) is the
default build target, so a plain build is enough — no `--target` needed.

Run: `make docker-build 2>&1 | tail -40`
Expected: build succeeds (tags `akopichin/afm:latest`); near the end of the
log, two new lines appear copying and `chmod`-ing `openai-agent-as-claude`,
mirroring the existing `openai-as-claude`/`cursor-as-claude`/`codex-as-claude`
lines.

- [ ] **Step 3: Confirm the binary exists in the built image**

The image's `ENTRYPOINT` is `/usr/local/bin/docker-entrypoint.sh` (which
execs `afm`), so checking a plain file needs `--entrypoint` to bypass it:

Run: `docker run --rm --entrypoint ls akopichin/afm:latest -la /usr/local/bin/openai-agent-as-claude`
Expected: file listed, executable bit set (`-rwxr-xr-x`).

- [ ] **Step 4: Commit**

```bash
git add Dockerfile.runtime
git commit -m "$(cat <<'EOF'
build(docker): добавляем openai-agent-as-claude в образ

Параллельно уже существующим openai-as-claude/cursor-as-claude/codex-as-claude.
EOF
)"
```

---

### Task 6: Document the `openai-agent` type in `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md` (in the "autoShim" section, right after the existing `#### Тип \`cursor\`: Cursor Cloud Agents API` subsection ends and before whatever comes next — locate it by searching for `#### Тип \`cursor\`` and finding where that subsection's content stops, e.g. just before `#### Тип \`codex\`` if it appears there, or before "Известные грабли" if `cursor` is currently the last documented type)

**Interfaces:** None (documentation only).

- [ ] **Step 1: Write the docs section**

Insert a new subsection (find the exact insertion point by grepping `CLAUDE.md` for `#### Тип`; place this one in type-declaration order, i.e. right after the `openai` subsection and before `cursor`, since `openai-agent` is a variant of `openai`):

```markdown
#### Тип `openai-agent`: OpenAI-совместимые провайдеры с реальным tool-loop

`type: openai` (выше) даёт модели только текст — годится для planning/review
стадий, но не годится для `agents: [auto]`/`interactive: true` стадий, которым
нужно реально писать файлы, гонять скрипты и отвечать на диалоговые вопросы.
`type: openai-agent` — для провайдеров, у которых `/chat/completions`
поддерживает настоящий OpenAI-style function calling (`tools`/`tool_choice`,
включая потоковые `tool_calls` со стандартной index-адресацией фрагментов).
Сгенерированный враппер использует `/usr/local/bin/openai-agent-as-claude`:

```yaml
docker:
  autoShim: true
  agents:
    idealab:
      type: openai-agent
      model: qwen3-max
      url: https://idealab.alibaba-inc.com/api/openai/v1
      max_turns: 40          # опционально; дефолт скрипта — 40
      auth:
        from: "file:~/.ai-free/claude-glm/token-idealab"
        to: "env:OPENAI_API_KEY"
```

Модели даётся ровно один инструмент — `bash` (команда → stdout+stderr+exit
code). Никаких отдельных read/write/skill-инструментов: чтение и запись
файлов, запуск `./scripts/*.sh`, поллинг диалоговых файлов
(`<phase>.<id>.answer.json`) — всё это модель делает сама через `bash`,
ровно как обычный shell-скрипт делал бы. Skill-конвенция (`<skills>name</skills>`
в промпте, см. раздел "File-Based Dialog Protocol" выше) не поддержана нативно
у стороннего провайдера — системный промпт адаптера явно учит модель при
упоминании skill'а самой прочитать `.claude/skills/<name>/SKILL.md` через `bash`.

Каждый tool-вызов сразу печатается в stdout как
`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"..."}}]}}`
— та же форма, что и настоящий Claude `Bash`-tool_use, поэтому дашборд
показывает живой action feed, а не тишину до самого конца стадии; это же
сбрасывает 30-минутный `idle_timeout` между ходами. `max_turns` (дефолт 40)
ограничивает число обращений к API за одну стадию; при достижении лимита
скрипт завершается штатно (exit 0) с пометкой в тексте — afm обрабатывает
это как обычный незавершённый autonomous-прогон (нет `execution_summary.md` →
retry), а не как отдельную ошибку. Сбой самого запроса к API (сеть, не-2xx) —
это `exit 1`, стадия падает сразу, в отличие от `openai-as-claude.sh`
(который на сбое `curl` проглатывает ошибку в пустой success — там это
безопасно для одноразового текста, здесь тихий "успех" замаскировал бы
реально незавершённый tool-loop).

Известное (не новое) ограничение: если модель зависает на диалоговом поллинге
дольше 30 минут (человек долго не отвечает), сработает тот же `idle_timeout`,
что уже документирован для файлового диалогового протокола выше — это
свойство самого механизма, не специфика этого типа.

Требования в образе: `jq`, `curl` (оба уже есть в `Dockerfile.runtime`).
```

- [ ] **Step 2: Verify placement**

Run: `grep -n '^#### Тип' CLAUDE.md`
Expected: the subsections appear in this order: `openai`, `openai-agent`, `cursor`, `codex` (or whatever the pre-existing order was, with `openai-agent` inserted directly after `openai`).

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: описываем recipe type openai-agent в CLAUDE.md

Рядом с openai/cursor/codex — единственный bash-инструмент, skill-конвенция
через системный промпт, max_turns, семантика ошибок.
EOF
)"
```

---

### Task 7: End-to-end verification with the real `lt-flow` project

**Files (outside this repo, not committed here):**
- Modify: `~/.afm/config.yaml` — `docker.agents.idealab.type`: `openai` → `openai-agent`.
- Modify: `/Users/alexander.kopichin/work/lt-flow/flow.yaml` — add `command: idealab` to the five agent-driving stages: `get-uri`, `logs`, `categorize`, `add-auth-rule`, `fill-rule-params`. The four `script:`-only stages (`prepare-rule`, `add-mandatory-rules`, `strip-mixer-prefix`, `load-collector-data`) are untouched.

This task has no automated test — it is the real-world validation that Tasks 1–6 actually solve the original problem. Do this after Tasks 1–6 are all merged, with a freshly built image.

- [ ] **Step 1: Rebuild the image with all changes**

```bash
cd /Users/alexander.kopichin/work/personal/afm
make docker-build
docker run --rm akopichin/afm:latest --version
```

Expected: build succeeds; version command runs.

- [ ] **Step 2: Update the global recipe**

Edit `~/.afm/config.yaml`, change the `idealab` entry under `docker.agents`:

```yaml
    idealab:
      type: openai-agent
      model: qwen3-max
      url: https://idealab.alibaba-inc.com/api/openai/v1
      auth:
        from: "file:~/.ai-free/claude-glm/token-idealab"
        to: "env:OPENAI_API_KEY"
```

(just the `type:` value changes, from `openai` to `openai-agent`; everything else stays).

- [ ] **Step 3: Wire `lt-flow` to use it**

In `/Users/alexander.kopichin/work/lt-flow/flow.yaml`, add `command: idealab` to each of these five stage blocks (alongside their existing `agents:`/`interactive:`/`prompt:` keys — exact placement within the block doesn't matter, YAML key order is free):
- `get-uri`
- `logs`
- `categorize`
- `add-auth-rule`
- `fill-rule-params`

- [ ] **Step 4: Run the real flow and drive the interactive stage**

```bash
cd /Users/alexander.kopichin/work/lt-flow
afm run flow.yaml
```

Open the dashboard URL printed by afm. When the `get-uri` stage reaches `awaiting_user_input`, answer its question through the dashboard (or `curl -X POST localhost:<port>/api/stages/get-uri/dialog/answer -d '{"phase":"...", "id":"...", "answer":"..."}'` if driving headlessly — check `GET /api/stages/get-uri/dialog` first for the exact `phase`/`id` to answer).

Expected: `get-uri` stage completes after being answered — `./result/uri.txt` and `./result/handler-type.txt` exist with real content written by the model's own `bash` tool calls (not empty, not placeholder text).

- [ ] **Step 5: Confirm an autonomous+skill stage actually executes**

Watch the `logs` stage (depends on `get-uri`, uses the `lt-logs` skill, `agents: [auto]`). Once it reaches `done`:

```bash
RUNDIR=$(ls -td /Users/alexander.kopichin/work/lt-flow/.afm/runs/*/ | head -1)
cat "$RUNDIR/logs/execution_summary.md"
ls -la /Users/alexander.kopichin/work/lt-flow/result/
```

Expected: `execution_summary.md` exists and is non-empty; `./result/handle-response.json` (or whatever the `lt-logs` skill actually produces) exists with real content — proving the model used `bash` to load the skill instructions (`cat .claude/skills/lt-logs/SKILL.md`) and then actually acted on them, not just described what it would do.

- [ ] **Step 6: Report back**

No commit for this task (nothing in this repo changes). If any stage stalls or fails, capture `$RUNDIR/<stage>/<phase>.log` and `$RUNDIR/<stage>/<phase>.stderr.log` before treating this task as done — these are the same log files the "Debugging Interactive Stages" section of `CLAUDE.md` already documents.
