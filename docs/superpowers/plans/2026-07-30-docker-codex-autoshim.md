# Docker: codex + `type: codex` autoShim Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `command: codex-as-claude` works inside afm's Docker mode exactly as it does locally, and a new `type: codex` autoShim recipe lets `docker.agents.codex` be configured (model only — auth comes from a copied, writable `~/.codex` OAuth state, not from afm's env-secret pipeline).

**Architecture:** Bake the real `codex` CLI and a vendored `codex-as-claude.sh` translator (claude stream-json ← codex JSONL) into `Dockerfile.runtime`, same as `openai-as-claude`/`cursor-as-claude` already are. `~/.codex` (ChatGPT-plan OAuth state) is mounted read-only into a scratch container path and *copied* (not bind-mounted) into `$HOME/.codex` by `docker-entrypoint.sh` before the privilege drop, so codex can refresh its own OAuth token inside the disposable container without ever touching the host's real `~/.codex`. A new `type: codex` recipe generates a wrapper named `codex` that resolves the real `codex` binary's absolute path once (before the wrapper directory is prepended to `PATH`) and exports it as `CODEX_BIN`, so the vendored adapter — which itself calls `codex` by bare name — never recurses into its own wrapper.

**Tech Stack:** Go 1.26 (afm core), POSIX `sh` (generated wrappers, `docker-entrypoint.sh`), bash (`codex-as-claude.sh`, matches upstream), Docker (Ubuntu 24.04 runtime image).

**Reference spec:** `docs/superpowers/specs/2026-07-30-docker-codex-autoshim-design.md` — read it before starting; this plan implements it task-by-task.

## Global Constraints

- Do not change the Go version in `go.mod` (currently `go 1.26.4`) without explicitly flagging it first.
- After every code change, run `make lint` and fix anything it flags — zero tolerance for lint warnings.
- No deprecated Go constructs; write code a newcomer to this codebase can read without extra explanation.
- All git commit messages are in Russian, one commit per task (or per logical step group, per the task's own commit step). Never add `Co-Authored-By` to any commit.
- Follow existing code patterns exactly: `pkg/docker/wrapper.go`'s `openai`/`cursor` branches, `pkg/config/config.go`'s `AgentRecipe.Validate()` structure, and the test styles in `wrapper_test.go`/`config_test.go`/`launcher_test.go` are the templates for every new test in this plan.
- Comments explain *why*, never *what* — match the existing Russian-language inline comment style used throughout `pkg/docker` and `pkg/config`.

---

### Task 1: `type: codex` recipe — config schema and validation

**Files:**
- Modify: `pkg/config/config.go:80-83` (const block), `pkg/config/config.go:124-151` (`Validate()`)
- Test: `pkg/config/config_test.go` (new `TestAgentRecipe_CodexType`, add near `TestAgentRecipe_CursorType` at line 510)

**Interfaces:**
- Produces: `config.RecipeTypeCodex = "codex"` (exported const) — consumed by Task 2 (`pkg/docker/wrapper.go`) and Task 3 (`pkg/docker/launcher.go`).
- Consumes: nothing new — reuses existing `AgentRecipe`, `RecipeAuth` types.

- [ ] **Step 1: Write the failing test**

Add to `pkg/config/config_test.go`, right after `TestAgentRecipe_CursorType` (ends at line 568):

```go
func TestAgentRecipe_CodexType(t *testing.T) {
	cases := []struct {
		name   string
		recipe config.AgentRecipe
		errSub string // пустая строка → ожидаем PASS
	}{
		{
			name:   "valid codex recipe, no auth, no model",
			recipe: config.AgentRecipe{Type: "codex"},
		},
		{
			name:   "valid codex recipe with model",
			recipe: config.AgentRecipe{Type: "codex", Model: "gpt-5.1-codex"},
		},
		{
			name:   "codex: auth optional but validated if present",
			recipe: config.AgentRecipe{Type: "codex", Auth: config.RecipeAuth{From: "env:OPENAI_API_KEY", To: "env:OPENAI_API_KEY"}},
		},
		{
			name:   "codex: auth.to must be env: if present",
			recipe: config.AgentRecipe{Type: "codex", Auth: config.RecipeAuth{From: "env:X", To: "OPENAI_API_KEY"}},
			errSub: "auth.to must be an env: reference",
		},
		{
			name:   "codex: url not required",
			recipe: config.AgentRecipe{Type: "codex", Model: "gpt-5.1-codex"},
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

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/... -run TestAgentRecipe_CodexType -v`
Expected: FAIL — `Validate()` rejects `Type: "codex"` with `recipe: type must be "", "claude", "openai", or "cursor"; got "codex"`.

- [ ] **Step 3: Write minimal implementation**

In `pkg/config/config.go`, change the const block at lines 80-83 from:

```go
const (
	RecipeTypeOpenAI = "openai"
	RecipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
)
```

to:

```go
const (
	RecipeTypeOpenAI = "openai"
	RecipeTypeCursor = "cursor" // Cursor Cloud Agents API (async run-based, не chat completions)
	RecipeTypeCodex  = "codex"  // codex CLI через codex-as-claude (см. scripts/codex-as-claude.sh)
)
```

Then replace `Validate()` (lines 124-151):

```go
func (r AgentRecipe) Validate() error {
	// type — allow-list; неизвестное значение (напр. опечатка "openapi") молча
	// трактовалось бы как claude, что ведёт к некорректной генерации обёртки.
	switch r.Type {
	case "", ClaudeCommand, RecipeTypeOpenAI, RecipeTypeCursor, RecipeTypeCodex:
	default:
		return fmt.Errorf("recipe: type must be \"\", \"claude\", \"openai\", \"cursor\", or \"codex\"; got %q", r.Type)
	}
	// codex: model опционален ("" / "default" → CODEX_MODEL не выставляется, решает
	// сам codex/~/.codex/config.toml); auth опционален — авторизация идёт через
	// смонтированную ~/.codex (ChatGPT-plan OAuth), а не через AFM_SECRET_<CMD>.
	// Если auth всё же задан (задел на будущий API-key режим) — валидируем как
	// openai/cursor (env:, без ограничения ClaudeAuthEnvVars).
	if r.Type == RecipeTypeCodex {
		if r.Auth == (RecipeAuth{}) {
			return nil
		}
		if !strings.HasPrefix(r.Auth.To, "env:") {
			return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
		}
		return nil
	}
	if r.Model == "" {
		return errors.New("recipe: model is required")
	}
	if !strings.HasPrefix(r.Auth.To, "env:") {
		return errors.New("recipe: auth.to must be an env: reference (e.g. env:OPENAI_API_KEY)")
	}
	// openai и cursor — внешние шлюзы: url обязателен, auth.to не ограничен ClaudeAuthEnvVars
	// (используют свои env vars: OPENAI_API_KEY, CURSOR_API_KEY и т.д.).
	if r.Type == RecipeTypeOpenAI || r.Type == RecipeTypeCursor {
		if r.URL == "" {
			return fmt.Errorf("recipe: url is required for type: %s", r.Type)
		}
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

Run: `go test ./pkg/config/... -run TestAgentRecipe_CodexType -v`
Expected: PASS (all 5 subtests)

Also run the full package to confirm no regressions: `go test ./pkg/config/... -v`
Expected: PASS (existing `TestAgentRecipe_OpenAIType`, `TestAgentRecipe_CursorType`, `TestAgentRecipe_ClaudeType`, `TestDockerAutoShim_ParseAndValidate` all still pass — the "openapi" typo test at line ~478 asserts substring `"type must be"`, which the new error message still starts with).

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "feat(config): recipe-тип codex — model и auth опциональны"
```

---

### Task 2: codex autoShim wrapper generation

**Files:**
- Modify: `pkg/docker/wrapper.go:29-42` (`WrapperSpec` doc comment), `pkg/docker/wrapper.go:44-83` (`CreateWrappers`), `pkg/docker/wrapper.go:85-173` (`generateWrapper`)
- Test: `pkg/docker/wrapper_test.go` (new tests after `TestCreateWrappers_CursorNoClaudeRequired` at line 259)

**Interfaces:**
- Consumes: `config.RecipeTypeCodex` (Task 1).
- Produces: nothing new consumed by later tasks — `generateWrapper`/`CreateWrappers` signatures change internally but their exported contract (`CreateWrappers([]WrapperSpec) (string, error)`) is unchanged, so `cmd/afm/run.go` (Task 4) needs no signature-level changes here.

- [ ] **Step 1: Write the failing tests**

Add to `pkg/docker/wrapper_test.go`, after `TestCreateWrappers_CursorNoClaudeRequired` (ends at line 259):

```go
// stubCodexOnPATH кладёт fake-codex в temp-dir и prepend'ит его к PATH.
// Возвращает абсолютный путь к fake-codex.
func stubCodexOnPATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho fake-codex\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return bin
}

func TestCreateWrappers_CodexTemplate(t *testing.T) {
	realCodex := stubCodexOnPATH(t)

	dir, err := CreateWrappers([]WrapperSpec{{
		Type:    config.RecipeTypeCodex,
		Command: "codex",
		Model:   "gpt-5.1-codex",
	}})
	if err != nil {
		t.Fatalf("CreateWrappers (codex): %v", err)
	}
	defer cleanup(dir)

	script, _ := os.ReadFile(filepath.Join(dir, "codex"))
	s := string(script)

	wantSubstrings := []string{
		"#!/bin/sh",
		`export CODEX_BIN="` + realCodex + `"`,
		`export CODEX_MODEL="gpt-5.1-codex"`,
		`exec /usr/local/bin/codex-as-claude "$@"`,
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(s, w) {
			t.Errorf("codex wrapper missing %q\n--- script ---\n%s", w, s)
		}
	}
	for _, bad := range []string{"ANTHROPIC_", "OPENAI_", "CURSOR_"} {
		if strings.Contains(s, bad) {
			t.Errorf("codex wrapper must not contain %q:\n%s", bad, s)
		}
	}
}

func TestCreateWrappers_CodexModelOptional(t *testing.T) {
	stubCodexOnPATH(t)
	for _, model := range []string{"", "default"} {
		dir, err := CreateWrappers([]WrapperSpec{{Type: config.RecipeTypeCodex, Command: "codex", Model: model}})
		if err != nil {
			t.Fatalf("CreateWrappers (codex, model=%q): %v", model, err)
		}
		s := string(mustRead(t, filepath.Join(dir, "codex")))
		cleanup(dir)
		if strings.Contains(s, "CODEX_MODEL") {
			t.Errorf("model=%q should omit CODEX_MODEL:\n%s", model, s)
		}
	}
}

func TestCreateWrappers_CodexNoClaudeRequired(t *testing.T) {
	// codex-тип не требует claude в PATH
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	if err := os.WriteFile(filepath.Join(emptyDir, "codex"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := CreateWrappers([]WrapperSpec{
		{Type: config.RecipeTypeCodex, Command: "codex", Model: "m"},
	})
	if err != nil {
		t.Errorf("codex-only wrappers must not fail when claude absent: %v", err)
	}
}

func TestCreateWrappers_CodexNoBinaryHardError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // ни claude, ни codex
	_, err := CreateWrappers([]WrapperSpec{{Type: config.RecipeTypeCodex, Command: "codex"}})
	if err == nil {
		t.Fatal("expected error when codex not in PATH")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/docker/... -run TestCreateWrappers_Codex -v`
Expected: FAIL — `config.RecipeTypeCodex` branch doesn't exist yet in `generateWrapper`, so the codex spec falls through to the claude-agent template (wrong output) or `CreateWrappers` errors trying to resolve `claude` in `TestCreateWrappers_CodexNoClaudeRequired`/`_CodexNoBinaryHardError` (since the codex-exclusion isn't in the `realClaude` lookup condition yet).

- [ ] **Step 3: Write minimal implementation**

In `pkg/docker/wrapper.go`, update the `WrapperSpec` doc comment (lines 29-33) from:

```go
// WrapperSpec describes one wrapper script to generate in the wrapper-dir.
// Type "" or "claude" selects the claude template (auth + ANTHROPIC_* vars + exec claude).
// Type "openai" selects the OpenAI-compatible template (OPENAI_* vars + exec openai-as-claude).
// Type "cursor" selects the Cursor Cloud Agents template (CURSOR_* vars + exec cursor-as-claude).
// Model == "" with Type "" selects the claude proxy-shim (BASE_URL only, no model vars).
```

to:

```go
// WrapperSpec describes one wrapper script to generate in the wrapper-dir.
// Type "" or "claude" selects the claude template (auth + ANTHROPIC_* vars + exec claude).
// Type "openai" selects the OpenAI-compatible template (OPENAI_* vars + exec openai-as-claude).
// Type "cursor" selects the Cursor Cloud Agents template (CURSOR_* vars + exec cursor-as-claude).
// Type "codex" selects the codex template (CODEX_BIN + optional CODEX_MODEL + exec codex-as-claude).
// Model == "" with Type "" selects the claude proxy-shim (BASE_URL only, no model vars).
```

Replace `CreateWrappers` (lines 44-83) with:

```go
// CreateWrappers creates a temp dir with one executable script per spec, named
// after spec.Command, and returns its path. realClaude is resolved once via
// exec.LookPath("claude") (absolute path → bypasses the wrapper-dir on PATH,
// avoiding recursion) — but only when at least one spec is a claude-family type
// (not openai/cursor/codex, which exec their own adapters, never claude).
// realCodexBin is resolved once via exec.LookPath("codex") — only when at least
// one spec is type "codex" — for the same PATH-recursion reason: the generated
// "codex" wrapper shadows the real codex binary on PATH, so the wrapper bakes
// in the absolute path via CODEX_BIN instead of letting the adapter script
// resolve "codex" through the (now-shadowed) PATH itself.
// Caller must defer os.RemoveAll(dir). Empty specs → ("", nil).
func CreateWrappers(specs []WrapperSpec) (string, error) {
	if len(specs) == 0 {
		return "", nil
	}
	// LookPath claude только если есть хотя бы один claude-тип (не openai, не cursor,
	// не codex — все три используют собственные адаптеры, не вызывающие claude).
	var realClaude string
	for _, s := range specs {
		if s.Type != config.RecipeTypeOpenAI && s.Type != config.RecipeTypeCursor && s.Type != config.RecipeTypeCodex {
			p, err := exec.LookPath(config.ClaudeCommand)
			if err != nil {
				return "", fmt.Errorf("claude not found in PATH (required for wrapper generation): %w", err)
			}
			realClaude = p
			break
		}
	}
	var realCodexBin string
	for _, s := range specs {
		if s.Type == config.RecipeTypeCodex {
			p, err := exec.LookPath("codex")
			if err != nil {
				return "", fmt.Errorf("codex not found in PATH (required for wrapper generation): %w", err)
			}
			realCodexBin = p
			break
		}
	}
	dir, err := os.MkdirTemp("", "fm-wrappers-*")
	if err != nil {
		return "", fmt.Errorf("create wrapper dir: %w", err)
	}
	for _, s := range specs {
		script, gErr := generateWrapper(s, realClaude, realCodexBin)
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

In `generateWrapper`, change the signature (line 85) from `func generateWrapper(s WrapperSpec, realClaude string) (string, error) {` to `func generateWrapper(s WrapperSpec, realClaude, realCodexBin string) (string, error) {`, and insert a new branch right after the cursor branch (after line 125, before `if s.Model == "" {`):

```go
	if s.Type == config.RecipeTypeCodex {
		// CODEX_BIN — abs-путь к реальному codex CLI, резолвлен ДО того как
		// wrapper-dir (где лежит ЭТОТ же файл с именем "codex") попал в PATH —
		// иначе codex-as-claude (внутри себя вызывающий голый `codex`) поймал бы
		// сам этот враппер и ушёл в бесконечную рекурсию. CODEX_MODEL опускается
		// для ""/"default" — тогда решает сам codex/~/.codex/config.toml.
		fmt.Fprintf(&b, "export CODEX_BIN=%q\n", realCodexBin)
		if s.Model != "" && s.Model != "default" {
			fmt.Fprintf(&b, "export CODEX_MODEL=%q\n", s.Model)
		}
		b.WriteString("exec /usr/local/bin/codex-as-claude \"$@\"\n")
		return b.String(), nil
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/docker/... -run TestCreateWrappers -v`
Expected: PASS — all codex tests plus every existing `TestCreateWrappers_*` test (claude, openai, cursor, mixed-types, bare-flag) unaffected.

Run: `go test ./pkg/docker/... -v` (full package) to catch anything else touching `generateWrapper`'s signature.
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/docker/wrapper.go pkg/docker/wrapper_test.go
git commit -m "feat(docker): autoshim-враппер для type: codex — CODEX_BIN против рекурсии по PATH"
```

---

### Task 3: `UsesCodex` gating + `~/.codex` state mount in `ReExec`

**Files:**
- Modify: `pkg/docker/launcher.go:34-44` (`ReExecConfig`), `pkg/docker/launcher.go` (new `UsesCodex` near `UsedRecipes`, ~line 217), `pkg/docker/launcher.go:269-274` (new mount block in `ReExec`, right after the `ExtraMounts` loop), `pkg/docker/launcher.go`'s existing recipe-secrets loop in `ReExec` (the `for cmd, recipe := range cfg.Recipes { ... }` block — see amendment below, added after Task 2's review surfaced a real bug this task must also close)
- Test: `pkg/docker/launcher_test.go` (new tests)

**Interfaces:**
- Consumes: `config.RecipeTypeCodex`, `config.AgentRecipe` (Task 1); `flow.Flow`, `flow.Stage` (existing).
- Produces: `docker.UsesCodex(f *flow.Flow, globalCmd string, usedRecipes map[string]config.AgentRecipe) bool` and `ReExecConfig.MountCodexState bool` — both consumed by Task 4 (`cmd/afm/run.go`).

**Amendment (discovered during Task 2's review, must be fixed in this task):** `config.AgentRecipe.Validate()` (Task 1) is the first place that allows `Auth` to be entirely unset (`RecipeAuth{}`) — true for `type: codex`'s primary, no-auth (ChatGPT-plan OAuth) use case. But `ReExec`'s existing recipe-secrets loop calls `ResolveAuthValue(recipe.Auth.From, secrets)` **unconditionally** for every entry in `cfg.Recipes`, and `ResolveAuthValue("", secrets)` returns an error (`"auth.from must be env:VAR or file:PATH, got \"\""`) — which `ReExec` then propagates, aborting the entire Docker run. Since a used `type: codex` recipe with no `auth` block is the expected, primary configuration, this bug would break codex's whole intended use case. Fix it as part of this task's `ReExec` edit (see Step 3 below).

- [ ] **Step 1: Write the failing tests**

Add to `pkg/docker/launcher_test.go` (near the other `UsedRecipes` tests, e.g. after `TestUsedRecipes_EmptyAndClaude`):

```go
func TestUsesCodex_DirectCommand(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{{ID: "s1", Command: "codex-as-claude"}}}
	if !docker.UsesCodex(f, "", nil) {
		t.Error("expected true for direct codex-as-claude stage command")
	}
}

func TestUsesCodex_GlobalCommand(t *testing.T) {
	if !docker.UsesCodex(nil, "codex-as-claude", nil) {
		t.Error("expected true for global codex-as-claude client command")
	}
}

func TestUsesCodex_RecipeType(t *testing.T) {
	recipes := map[string]config.AgentRecipe{"codex": {Type: config.RecipeTypeCodex}}
	if !docker.UsesCodex(nil, "", recipes) {
		t.Error("expected true when a used recipe has type codex")
	}
}

func TestUsesCodex_False(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{{ID: "s1", Command: "glm51"}}}
	recipes := map[string]config.AgentRecipe{"glm51": {Type: ""}}
	if docker.UsesCodex(f, "claude", recipes) {
		t.Error("expected false when codex is not used")
	}
}

func TestReExec_CodexStateMount_WhenPresentAndFlagged(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: true,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	argsStr := strings.Join(capturedArgs, " ")
	want := homeDir + "/.codex:/tmp/host-codex:ro"
	if !strings.Contains(argsStr, want) {
		t.Errorf("missing codex state mount %q: %s", want, argsStr)
	}
}

func TestReExec_CodexStateMount_SkippedWhenFlagFalse(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: false,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "host-codex") {
		t.Error("codex state must not be mounted when MountCodexState=false")
	}
}

func TestReExec_CodexStateMount_SkippedWhenDirMissing(t *testing.T) {
	homeDir := t.TempDir() // нет .codex внутри
	t.Setenv("HOME", homeDir)

	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, MountCodexState: true,
	})
	if err != nil {
		t.Fatalf("ReExec: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "host-codex") {
		t.Error("codex state must not be mounted when ~/.codex does not exist")
	}
}

// TestReExec_CodexRecipeNoAuth_DoesNotFail проверяет фикс бага, найденного в
// ревью Task 2: recipe типа codex может законно иметь пустой Auth (Validate()
// это разрешает — авторизация идёт через смонтированную ~/.codex, а не через
// секрет). Раньше ResolveAuthValue("", secrets) фейлил на пустом auth.from и
// валил ВЕСЬ ReExec — это ломало главный (безсекретный) сценарий codex.
func TestReExec_CodexRecipeNoAuth_DoesNotFail(t *testing.T) {
	var capturedArgs []string
	docker.SetExecFunc(func(argv0 string, argv []string, envv []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	recipes := map[string]config.AgentRecipe{
		"codex": {Type: config.RecipeTypeCodex}, // Auth умышленно не задан
	}
	err := docker.ReExec(docker.ReExecConfig{
		Image: "akopichin/afm:latest", ProjectDir: "/tmp/proj",
		ExtraArgs: []string{"run", "flow.yaml"}, Recipes: recipes,
	})
	if err != nil {
		t.Fatalf("ReExec must not fail for a no-auth codex recipe: %v", err)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "AFM_SECRET_CODEX") {
		t.Error("no-auth codex recipe must not emit an AFM_SECRET_ env var")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/docker/... -run 'TestUsesCodex|TestReExec_CodexStateMount|TestReExec_CodexRecipeNoAuth' -v`
Expected: FAIL — `docker.UsesCodex` doesn't exist and `ReExecConfig.MountCodexState` is an unknown field (compile failure covers the first two groups); `TestReExec_CodexRecipeNoAuth_DoesNotFail` fails at runtime once the package compiles (after Step 3's `ReExecConfig`/`UsesCodex` additions alone, without the Step 3 secrets-loop fix) with `agent codex: auth.from must be env:VAR or file:PATH, got ""`.

- [ ] **Step 3: Write minimal implementation**

In `pkg/docker/launcher.go`, add `MountCodexState` to `ReExecConfig` (lines 34-44):

```go
// ReExecConfig параметры для перезапуска afm в Docker.
type ReExecConfig struct {
	Image           string
	ProjectDir      string                        // абсолютный путь к директории проекта
	Commands        []CommandMount
	DashboardPort   int                           // порт dashboard; при >0 пробрасывается на хост через -p
	ExtraMounts     []string                      // доп. хост-директории (могут начинаться с ~) → монтируются :ro
	ExtraArgs       []string                      // os.Args[1:]
	ClientCommand   string                        // имя агента из config (для проверки auth при command: claude)
	Recipes         map[string]config.AgentRecipe // autoShim: команды с recipe → генерируются, секрет → transient env
	SecretsFile     string                        // опц. override для default-слоёв secrets.env
	MountCodexState bool                          // codex использует ~/.codex (OAuth) → монтировать read-only в /tmp/host-codex (entrypoint копирует в $HOME/.codex, см. UsesCodex)
}
```

Add `UsesCodex` right after `UsedRecipes` (which ends at line 217, before `ReExec` at line 219):

```go
// codexAdapterCommand — имя команды-адаптера codex CLI → claude stream-json
// (см. scripts/codex-as-claude.sh), baked в образ рядом с openai-as-claude/
// cursor-as-claude. Используется и напрямую (command: codex-as-claude, без
// recipe), и как exec-цель generated-враппера recipe-типа "codex".
const codexAdapterCommand = "codex-as-claude"

// UsesCodex reports whether the flow (a stage command or the global client
// command) invokes codex — either directly via codexAdapterCommand, or
// indirectly via a used recipe of type "codex" — used to gate mounting the
// host's ~/.codex OAuth state (see ReExec). usedRecipes should already be
// filtered to commands the flow actually references (UsedRecipes), consistent
// with the least-privilege rule already applied to recipe secrets.
func UsesCodex(f *flow.Flow, globalCmd string, usedRecipes map[string]config.AgentRecipe) bool {
	if globalCmd == codexAdapterCommand {
		return true
	}
	if f != nil {
		for _, s := range f.Stages {
			if s.Command == codexAdapterCommand {
				return true
			}
		}
	}
	for _, r := range usedRecipes {
		if r.Type == config.RecipeTypeCodex {
			return true
		}
	}
	return false
}
```

In `ReExec`, add the mount block right after the `ExtraMounts` loop (after line 273, before the `// Окружение внутри контейнера.` comment at line 275):

```go
	// Codex OAuth-состояние (~/.codex): монтируем read-only во временный путь
	// контейнера — entrypoint (root, до gosu) копирует его в $HOME/.codex
	// (writable), чтобы codex мог обновлять auth.json (refresh token), не задевая
	// хостовый ~/.codex. Гейтим MountCodexState, чтобы не тащить OAuth-состояние
	// во флоу, которые codex не используют (см. UsesCodex).
	if cfg.MountCodexState {
		codexHostDir := filepath.Join(home, ".codex")
		if info, statErr := os.Stat(codexHostDir); statErr == nil && info.IsDir() {
			args = append(args, "-v", codexHostDir+":/tmp/host-codex:ro")
		}
	}
```

Also fix the bug described in this task's "Amendment" above: in the existing recipe-secrets loop further down in `ReExec` (unchanged since before this plan — the `if len(cfg.Recipes) > 0 { ... for cmd, recipe := range cfg.Recipes { ... } }` block), change:

```go
		for cmd, recipe := range cfg.Recipes {
			name := envName(cmd)
			val, vErr := ResolveAuthValue(recipe.Auth.From, secrets)
			if vErr != nil {
				return fmt.Errorf("agent %s: %w", cmd, vErr)
			}
```

to:

```go
		for cmd, recipe := range cfg.Recipes {
			name := envName(cmd)
			// codex — единственный тип, для которого Validate() допускает пустой
			// Auth (авторизация идёт через смонтированную ~/.codex, не через
			// секрет). Пропускаем резолв, иначе ResolveAuthValue("", ...) фейлит
			// на пустом auth.from и валит весь запуск даже когда секрет не нужен.
			if recipe.Auth.From == "" {
				continue
			}
			val, vErr := ResolveAuthValue(recipe.Auth.From, secrets)
			if vErr != nil {
				return fmt.Errorf("agent %s: %w", cmd, vErr)
			}
```

(The rest of the loop body — `os.Setenv`, the `-e AFM_SECRET_...` append, and the `SystemPrompt` block — stays exactly as it is, just now skipped entirely for a no-auth recipe via the new `continue`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/docker/... -run 'TestUsesCodex|TestReExec_CodexStateMount|TestReExec_CodexRecipeNoAuth' -v`
Expected: PASS (8 new tests).

Run: `go test ./pkg/docker/... -v` (full package)
Expected: PASS — existing `TestReExec_*` tests don't set `MountCodexState`, so it defaults `false` and the new mount block never fires for them.

- [ ] **Step 5: Commit**

```bash
git add pkg/docker/launcher.go pkg/docker/launcher_test.go
git commit -m "feat(docker): UsesCodex-гейтинг + copy-not-mount ~/.codex в ReExec"
```

---

### Task 4: wire `UsesCodex` into `cmd/afm/run.go`

**Files:**
- Modify: `cmd/afm/run.go:105-128` (docker re-exec branch)

**Interfaces:**
- Consumes: `docker.UsesCodex` (Task 3), the existing `recipes`/`cmds` locals already built in this function.
- Produces: nothing new for later tasks — this is the final host-side wiring point.

- [ ] **Step 1: (no new automated test)**

This change lives inside the `RunE` closure of `newRunCmd()`, which calls `docker.ReExec` directly and (on success) never returns — the surrounding docker-branch code has no existing unit test today (only the extracted pure helpers `buildWrapperSpec`/`resolveSupervisorCommand` are tested, in `cmd/afm/run_test.go`). This task follows that existing precedent: no new test file, verified instead by Task 7's manual end-to-end run. `docker.UsesCodex` itself is already fully unit-tested in Task 3.

- [ ] **Step 2: Implement the wiring**

In `cmd/afm/run.go`, inside the `if cfg.Docker.IsDockerEnabled() {` block, change:

```go
				cmds := docker.ScanCommands(f, cfg.Client.Command, generatedForMount)
				port := cfg.Server.GetPort()
```

to:

```go
				cmds := docker.ScanCommands(f, cfg.Client.Command, generatedForMount)
				mountCodexState := docker.UsesCodex(f, cfg.Client.Command, recipes)
				port := cfg.Server.GetPort()
```

Then, in the `docker.ReExec(docker.ReExecConfig{...})` call a few lines below, add the new field:

```go
				return docker.ReExec(docker.ReExecConfig{
					Image:           cfg.Docker.GetImage(),
					ProjectDir:      absDir,
					Commands:        cmds,
					DashboardPort:   port,
					ExtraMounts:     cfg.Docker.ExtraMounts,
					ExtraArgs:       append(os.Args[1:], "--dir="+absDir),
					ClientCommand:   cfg.Client.Command,
					Recipes:         recipes,
					SecretsFile:     cfg.Docker.SecretsFile,
					MountCodexState: mountCodexState,
				})
```

Note: `recipes` is declared as `var recipes map[string]config.AgentRecipe` earlier in the same block and stays `nil` when `cfg.Docker.IsAutoShim()` is false — `docker.UsesCodex` handles a `nil` map by simply not finding a codex-type recipe (its `for _, r := range usedRecipes` loop is a no-op on `nil`), so direct `command: codex-as-claude` usage (no recipe, no autoShim) is still detected correctly through the `globalCmd`/stage-command checks.

- [ ] **Step 3: Verify the whole module still builds and lints**

Run: `go build ./...`
Expected: success.

Run: `make lint`
Expected: `0 issues.`

- [ ] **Step 4: Commit**

```bash
git add cmd/afm/run.go
git commit -m "feat(cli): пробросить UsesCodex в ReExec — монтировать ~/.codex только когда нужно"
```

---

### Task 5: vendor `scripts/codex-as-claude.sh`

**Files:**
- Create: `scripts/codex-as-claude.sh`

**Interfaces:**
- Consumes: `CODEX_BIN` env var (set by the Task 2 wrapper when running under autoShim; falls back to bare `codex` when unset, e.g. local non-Docker use).
- Produces: the file Task 6's `Dockerfile.runtime` `COPY`s to `/usr/local/bin/codex-as-claude`.

- [ ] **Step 1: Create the file**

Source: `~/work/ralphex/scripts/codex-as-claude/codex-as-claude.sh`. Two changes from upstream: (a) the ralphex-specific `<<<RALPHEX:REVIEW_DONE>>>` signal-injection block is removed (afm has no equivalent signal), (b) the `codex` invocation uses `"${CODEX_BIN:-codex}"` instead of bare `codex`, so the autoShim wrapper (Task 2) can hand it an absolute path and avoid PATH-recursion.

```sh
#!/usr/bin/env bash
# codex-as-claude.sh — wraps the codex CLI to produce Claude-compatible
# stream-json output, so afm's executor (pkg/executor, claude stream-json only)
# can drive codex the same way it drives claude.
#
# environment variables:
#   CODEX_BIN      — abs path to the real codex binary (set by the autoShim
#                    "codex" wrapper, see pkg/docker/wrapper.go, to avoid PATH
#                    recursion — the wrapper shadows the bare "codex" name).
#                    Falls back to bare "codex" when unset (local, non-Docker use).
#   CODEX_MODEL    — codex model to use (default: codex's own default)
#   CODEX_SANDBOX  — sandbox mode (default: danger-full-access — the container
#                    is already isolated)
#   CODEX_VERBOSE  — set to 1 to include command execution output (default: 0)

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# prompt via stdin (primary path — matches how afm's executor pipes the prompt
# to claude). also accept -p for direct invocations; all other flags
# (--dangerously-skip-permissions etc., unconditionally added by afm's executor
# for claude-compatible commands) are ignored.
prompt=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p) prompt="${2:-}"; shift; shift 2>/dev/null || true ;;
        *)  shift ;;
    esac
done

if [[ -z "$prompt" ]]; then
    if [[ ! -t 0 ]]; then
        prompt=$(cat)
    fi
fi

if [[ -z "$prompt" ]]; then
    echo "error: no prompt provided (expected -p flag or stdin)" >&2
    exit 1
fi

CODEX_MODEL="${CODEX_MODEL:-}"
CODEX_SANDBOX="${CODEX_SANDBOX:-danger-full-access}"

codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox -s "$CODEX_SANDBOX")
[[ -n "$CODEX_MODEL" ]] && codex_args+=(-m "$CODEX_MODEL")

# run codex with JSON output, translate events to claude stream-json format.
# only agent messages are emitted by default — command executions and file
# reads produce excessive noise; set CODEX_VERBOSE=1 to include them.
#
# event mapping:
#   item.completed + agent_message     -> content_block_delta (text_delta)
#   item.completed + command_execution -> skipped (or included if CODEX_VERBOSE=1)
#   item.completed + reasoning         -> skipped
#   turn.completed                     -> result (end of execution)
#   everything else                    -> skipped
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" 2>/dev/null | while IFS= read -r line; do
    echo "$line" | jq -c --argjson verbose "$CODEX_VERBOSE" '
        if .type == "item.completed" then
            if .item.type == "agent_message" then
                {type: "content_block_delta", delta: {type: "text_delta", text: (.item.text + "\n")}}
            elif .item.type == "command_execution" and $verbose == 1 then
                {type: "content_block_delta", delta: {type: "text_delta",
                    text: ("$ " + .item.command + "\n" + (.item.aggregated_output // "") + "\n")}}
            else empty
            end
        elif .type == "turn.completed" then
            {type: "result", result: ""}
        else empty
        end
    ' 2>/dev/null || true
done || true

echo '{"type":"result","result":""}'
```

Make it executable: `chmod +x scripts/codex-as-claude.sh`

- [ ] **Step 2: Ad-hoc verification (no committed test — matches existing `openai-as-claude.sh`/`cursor-as-claude.sh` precedent, neither of which has a test file; their behavior is exercised via `wrapper_test.go`'s generation-output assertions plus real usage)**

Run this from the repo root to confirm the translation logic and the `CODEX_BIN` override both work, using a mock `codex`:

```bash
tmp=$(mktemp -d)
cat > "$tmp/codex" <<'EOF'
#!/bin/sh
cat <<'JSONL'
{"type":"item.completed","item":{"type":"agent_message","text":"hello from mock codex"}}
{"type":"turn.completed"}
JSONL
EOF
chmod +x "$tmp/codex"

# 1) bare "codex" (CODEX_BIN unset) — resolved via PATH
echo "test prompt" | PATH="$tmp:$PATH" bash scripts/codex-as-claude.sh
# 2) CODEX_BIN set to the mock's absolute path — must behave identically
echo "test prompt" | CODEX_BIN="$tmp/codex" bash scripts/codex-as-claude.sh
rm -rf "$tmp"
```

Expected (both invocations): a `content_block_delta` line containing `"hello from mock codex"` followed by a `{"type":"result","result":""}` line.

- [ ] **Step 3: Commit**

```bash
git add scripts/codex-as-claude.sh
git commit -m "feat(docker): вендорим codex-as-claude.sh (адаптер codex JSONL -> claude stream-json)"
```

---

### Task 6: bake codex into the runtime image + copy `~/.codex` in the entrypoint

**Files:**
- Modify: `Dockerfile.runtime:55-74` (install codex CLI, copy the adapter), `docker-entrypoint.sh` (copy block for `/tmp/host-codex`)

**Interfaces:**
- Consumes: `scripts/codex-as-claude.sh` (Task 5); `/tmp/host-codex` mount path (Task 3, matching the literal path used in `ReExec`'s new mount block — keep both in sync).
- Produces: `/usr/local/bin/codex-as-claude` and a real `codex` binary on `PATH` inside the image (consumed at runtime by Task 2's generated wrapper and by direct `command: codex-as-claude` usage).

- [ ] **Step 1: (manual verification only — Dockerfile/shell changes aren't covered by `go test`; Task 7 builds and exercises the image end-to-end)**

- [ ] **Step 2: Edit `Dockerfile.runtime`**

Change:

```dockerfile
# claude CLI
RUN npm install -g @anthropic-ai/claude-code
```

to:

```dockerfile
# claude CLI
RUN npm install -g @anthropic-ai/claude-code

# codex CLI (нативный бинарник — не портируется в контейнер простым mount с
# хоста, поэтому ставим его так же, как claude, npm-пакетом внутри образа)
RUN npm install -g @openai/codex
```

And change:

```dockerfile
# cursor-as-claude: адаптер Cursor Cloud Agents API (асинхронный run-based → claude stream-json)
COPY scripts/cursor-as-claude.sh /usr/local/bin/cursor-as-claude
RUN chmod +x /usr/local/bin/cursor-as-claude

WORKDIR /project
```

to:

```dockerfile
# cursor-as-claude: адаптер Cursor Cloud Agents API (асинхронный run-based → claude stream-json)
COPY scripts/cursor-as-claude.sh /usr/local/bin/cursor-as-claude
RUN chmod +x /usr/local/bin/cursor-as-claude

# codex-as-claude: адаптер codex JSONL (`codex exec --json`) → claude stream-json
COPY scripts/codex-as-claude.sh /usr/local/bin/codex-as-claude
RUN chmod +x /usr/local/bin/codex-as-claude

WORKDIR /project
```

- [ ] **Step 3: Edit `docker-entrypoint.sh`**

Change:

```sh
mkdir -p /home/afm
chown "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm

# gosu сбрасывает HOME по /etc/passwd; для uid без записи там он ставит HOME=/.
```

to:

```sh
mkdir -p /home/afm
chown "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm

# Codex OAuth-состояние (~/.codex): смонтировано read-only в /tmp/host-codex
# (см. pkg/docker/launcher.go ReExec — MountCodexState), здесь копируется
# (не bind-mount) в $HOME/.codex — codex может обновлять auth.json (refresh
# token) внутри контейнера, не задевая хостовый ~/.codex. Контейнер эфемерный
# (--rm), копия исчезает вместе с ним. Копируем и chown'им ДО gosu — после
# сброса привилегий процесс уже не root и не сможет chown'ить.
if [ -d /tmp/host-codex ]; then
  mkdir -p /home/afm/.codex
  cp -a /tmp/host-codex/. /home/afm/.codex/
  chown -R "$AFM_HOST_UID:$AFM_HOST_GID" /home/afm/.codex
fi

# gosu сбрасывает HOME по /etc/passwd; для uid без записи там он ставит HOME=/.
```

- [ ] **Step 4: Syntax-check the entrypoint**

Run: `sh -n docker-entrypoint.sh`
Expected: no output, exit code 0.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile.runtime docker-entrypoint.sh
git commit -m "feat(docker): baked codex CLI + codex-as-claude в образ, copy ~/.codex в entrypoint"
```

---

### Task 7: manual end-to-end verification (build + run)

**Files:** none (verification only — no code changes)

**Interfaces:** exercises everything produced by Tasks 1-6 together.

This task is executed directly (not delegated to a TDD subagent) since it's exploratory manual QA, not a scripted test. Run every step yourself and read the actual output before declaring success — per `superpowers:verification-before-completion`, do not report this task done without having actually run it.

- [ ] **Step 1: Build the binary and the Docker image**

```bash
cd /Users/alexander.kopichin/work/personal/afm
make build
make docker-build
docker images akopichin/afm:latest   # confirm a fresh image was just built
```

Expected: `make build` succeeds, produces `./bin/afm`. `make docker-build` succeeds and tags `akopichin/afm:latest` locally (matches `config.DockerConfig.GetImage()`'s default, so no `AFM_DOCKER_IMAGE` override is needed — the freshly built local image is what `docker run` picks up).

- [ ] **Step 2: Create an isolated scratch project for the smoke test**

```bash
scratch=/private/tmp/claude-502/-Users-alexander-kopichin-work-personal-afm/ce504f01-1608-4ae2-9b73-6ed00cb4474b/scratchpad/codex-smoketest
mkdir -p "$scratch/.afm"
cat > "$scratch/flow.yaml" <<'EOF'
name: codex-docker-smoketest
description: Manual smoke test for codex support in Docker mode (autoShim recipe)
stages:
  - id: hello
    name: "Codex hello"
    description: |
      Write the exact text "hello from codex" (no quotes, no trailing newline
      content beyond a single trailing newline) to a file named
      codex-hello.txt in the project root. Do nothing else — no other files,
      no explanations.
    agents: [planning, implementation]
    command: codex
EOF
cat > "$scratch/.afm/config.yaml" <<'EOF'
docker:
  enabled: true
  autoShim: true
  agents:
    codex:
      type: codex
      model: default
EOF
```

- [ ] **Step 3: Run the flow in Docker and watch it execute**

```bash
cd "$scratch"
/Users/alexander.kopichin/work/personal/afm/bin/afm run flow.yaml
```

Expected: afm re-execs itself into the `akopichin/afm:latest` container (log line printing the dashboard URL). Open the printed dashboard URL and watch the `hello` stage go through `planning` → `implementation` using the `codex` command. Wait for the stage to reach `done`.

- [ ] **Step 4: Verify the actual output**

```bash
cat "$scratch/codex-hello.txt"
```

Expected: file exists, contains `hello from codex`.

- [ ] **Step 5: Verify the host's real `~/.codex` was not mutated**

Before step 3, note `stat -f '%m' ~/.codex/auth.json` (or `ls -la ~/.codex/auth.json`); after the run completes, compare again.

```bash
ls -la ~/.codex/auth.json
```

Expected: mtime unchanged from before the run — proves the container wrote to its own disposable copy, not the host file, even if codex refreshed its token during the run.

- [ ] **Step 6: Verify the direct (non-recipe) path too — `command: codex-as-claude` without any recipe**

```bash
cat > "$scratch/flow2.yaml" <<'EOF'
name: codex-docker-smoketest-direct
description: Manual smoke test for codex-as-claude used directly, no autoShim recipe
stages:
  - id: hello2
    name: "Codex hello (direct)"
    description: |
      Write the exact text "hello from codex direct" to a file named
      codex-hello-direct.txt in the project root. Do nothing else.
    agents: [planning, implementation]
    command: codex-as-claude
EOF
cat > "$scratch/.afm/config.yaml" <<'EOF'
docker:
  enabled: true
EOF
"$scratch/../../bin/afm" run "$scratch/flow2.yaml" --dir "$scratch"
```

(Adjust the `afm` binary path if the relative path above doesn't resolve — use the same absolute path as Step 3.)

Expected: same success pattern — `codex-hello-direct.txt` appears with the expected content, this time via the image-baked `codex-as-claude` mounted/exec'd directly (no `docker.agents` recipe involved), proving Part 1 of the design works independently of autoShim.

- [ ] **Step 7: Report results**

Summarize what was observed (both flows' final state, file contents, `~/.codex` mtime comparison) back to the user — do not mark this task complete without having actually executed steps 1-6 and inspected real output.

---

## Post-plan

- [ ] After all 7 tasks are committed and Task 7's manual verification passes, use `superpowers:requesting-code-review` to review the full diff before considering this feature complete.
