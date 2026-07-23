# Configurable executor.truncate_output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded 100/80-char truncation of agent tool-action output (`pkg/executor/executor.go`'s `contentToAction`) with a single configurable `executor.truncate_output` setting, defaulting to no truncation.

**Architecture:** Three layers, each independently testable: (1) `pkg/config` gains the new YAML field and merge rule; (2) `pkg/executor` gains a `limit int` parameter threaded through `contentToAction`/`ParseToolAction` and a matching `executor.Config` field; (3) the 4 real call sites that construct `executor.Config` (in `pkg/orchestrator`) are wired to pass the configured value through, plus docs.

**Tech Stack:** Go, `go test`, YAML config (`gopkg.in/yaml.v3`), table-driven tests (existing project convention).

## Global Constraints

- Default is `0` = no truncation (Go zero value — no change needed to `config.Default()`).
- ONE config value (`truncate_output`) governs both the text-block cap (currently hardcoded 100) and the Bash/other-tool-fallback cap (currently hardcoded 80) — not two separate knobs.
- `Write`/`Edit`/`Read`/`Glob`/`Grep` log only a file path and are never truncated, before or after this change — no change to that branch.
- Merge/override between global and project config follows the exact existing pattern used for `max_parallel`: `if overlay.Executor.TruncateOutput != 0 { dst.Executor.TruncateOutput = overlay.Executor.TruncateOutput }` (same known limitation as `max_parallel` — accepted, not fixed here).
- `cmd/afm/run.go:201`'s `supervisorRunner` is NOT wired to this setting — its `RunJSONQuery` method never calls `contentToAction` (its own doc comment says "без логирования действий" / "without action logging"), so threading `TruncateOutput` there would be dead code.
- Commit messages in Russian.

---

## File Structure

- `pkg/config/config.go` — `ExecutorConfig.TruncateOutput` field + `mergeFile` override rule.
- `pkg/config/config_test.go` — default value + override tests.
- `pkg/executor/executor.go` — `Config.TruncateOutput` field, `contentToAction(c, limit)`, `ParseToolAction(line, limit)`, the two internal streaming call sites.
- `pkg/executor/executor_test.go` — update the two existing truncation sub-tests to pass an explicit limit, add sub-tests proving `limit == 0` never truncates.
- `pkg/orchestrator/runner_factory.go` — wire `o.opts.Config.Executor.TruncateOutput` into 3 `executor.Config{}` literals.
- `pkg/orchestrator/orchestrator.go` — wire `opts.Config.Executor.TruncateOutput` into 1 `executor.Config{}` literal.
- `config.example.yaml`, `README.md`, `release-notes.md` — documentation.

No new files.

---

### Task 1: `ExecutorConfig.TruncateOutput` field + merge rule

**Files:**
- Modify: `pkg/config/config.go`
- Test: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.ExecutorConfig.TruncateOutput int` (YAML key `truncate_output`), readable as `cfg.Executor.TruncateOutput` on any `config.Config` returned by `config.Default()` or `config.LoadFrom(...)`. Task 3 consumes this field.

- [ ] **Step 1: Write the failing tests**

In `pkg/config/config_test.go`, extend `TestDefaultConfig` (around the existing `MaxParallel` check):

```go
func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Client.Command != defaultCommand {
		t.Errorf("default command: got %q want %q", cfg.Client.Command, "claude")
	}
	if cfg.Executor.IdleTimeout != 30*time.Minute {
		t.Errorf("default idle timeout: got %v", cfg.Executor.IdleTimeout)
	}
	if cfg.Executor.MaxParallel != 0 {
		t.Errorf("default max_parallel: got %d", cfg.Executor.MaxParallel)
	}
	if cfg.Executor.TruncateOutput != 0 {
		t.Errorf("default truncate_output: got %d", cfg.Executor.TruncateOutput)
	}
}
```

Add two new test functions right after `TestLoadProjectOverridesGlobal`:

```go
func TestTruncateOutputCarriesFromGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
executor:
  truncate_output: 50
`)
	writeYAML(t, projectDir, "config.yaml", `
client:
  command: gemini
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Executor.TruncateOutput != 50 {
		t.Errorf("global truncate_output should carry over: got %d", cfg.Executor.TruncateOutput)
	}
}

func TestTruncateOutputProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, globalDir, "config.yaml", `
executor:
  truncate_output: 50
`)
	writeYAML(t, projectDir, "config.yaml", `
executor:
  truncate_output: 200
`)

	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Executor.TruncateOutput != 200 {
		t.Errorf("project truncate_output should override global: got %d", cfg.Executor.TruncateOutput)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/config/... -run 'TestDefaultConfig|TestTruncateOutput' -v`
Expected: FAIL — compile error, `cfg.Executor.TruncateOutput` (and `config.ExecutorConfig{... TruncateOutput ...}` if referenced) undefined, since the field doesn't exist yet.

- [ ] **Step 3: Implement the field and merge rule**

In `pkg/config/config.go`, add the field to `ExecutorConfig` (around line 37-40):

```go
// ExecutorConfig controls agent execution parameters.
type ExecutorConfig struct {
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	MaxParallel    int           `yaml:"max_parallel"`
	TruncateOutput int           `yaml:"truncate_output"`
}
```

In `mergeFile` (around line 277-282), add the override rule right after the existing `MaxParallel` block:

```go
	if overlay.Executor.IdleTimeout != 0 {
		dst.Executor.IdleTimeout = overlay.Executor.IdleTimeout
	}
	if overlay.Executor.MaxParallel != 0 {
		dst.Executor.MaxParallel = overlay.Executor.MaxParallel
	}
	if overlay.Executor.TruncateOutput != 0 {
		dst.Executor.TruncateOutput = overlay.Executor.TruncateOutput
	}
```

`config.Default()` needs no change — the zero value already means "no truncation."

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/config/... -v`
Expected: PASS, all tests including the 3 new/modified ones.

- [ ] **Step 5: Commit**

```bash
git add pkg/config/config.go pkg/config/config_test.go
git commit -m "$(cat <<'EOF'
feat: добавить конфиг executor.truncate_output (0 = без обрезки)
EOF
)"
```

---

### Task 2: parameterize `contentToAction`/`ParseToolAction`, add `executor.Config.TruncateOutput`

**Files:**
- Modify: `pkg/executor/executor.go`
- Test: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 directly (this task only touches `pkg/executor`, which doesn't import `pkg/config` — the config value is threaded in by the caller as a plain `int`).
- Produces: `executor.Config.TruncateOutput int` field; `executor.ParseToolAction(line string, limit int) (tool, detail string, ok bool)` — the exported signature Task 3's callers and any future caller must use. `limit <= 0` means unlimited.

- [ ] **Step 1: Write the failing tests**

In `pkg/executor/executor_test.go`, update `TestParseToolAction`'s table to add a `limit int` field (defaults to `0` for the zero-value cases, which is fine since none of those strings exceed 80/100 chars anyway), and change the two existing truncation cases plus add two new "no truncation when limit is 0" cases:

```go
func TestParseToolAction(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		limit      int
		wantTool   string
		wantDetail string
		wantOK     bool
	}{
		// ... all existing cases unchanged, just add `limit: 0` is implicit
		// (zero value) for every case that doesn't set it explicitly ...
		{
			name:       "text truncation over 100 chars when limit is 100",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, strings.Repeat("x", 120)),
			limit:      100,
			wantTool:   testTypeText,
			wantDetail: strings.Repeat("x", 100) + "...",
			wantOK:     true,
		},
		{
			name:       "bash truncation over 80 chars when limit is 80",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, strings.Repeat("echo ", 20)),
			limit:      80,
			wantTool:   testToolBash,
			wantDetail: strings.Repeat("echo ", 16), // 80 chars
			wantOK:     true,
		},
		{
			name:       "text NOT truncated when limit is 0 (default)",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, strings.Repeat("x", 120)),
			limit:      0,
			wantTool:   testTypeText,
			wantDetail: strings.Repeat("x", 120),
			wantOK:     true,
		},
		{
			name:       "bash NOT truncated when limit is 0 (default)",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, strings.Repeat("echo ", 20)),
			limit:      0,
			wantTool:   testToolBash,
			wantDetail: strings.Repeat("echo ", 20), // full 100 chars, not cut
			wantOK:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, detail, ok := executor.ParseToolAction(tc.line, tc.limit)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tool != tc.wantTool {
				t.Errorf("tool=%q, want %q", tool, tc.wantTool)
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail=%q, want to contain %q", detail, tc.wantDetail)
			}
		})
	}
}
```

Note the call site changes from `executor.ParseToolAction(tc.line)` to `executor.ParseToolAction(tc.line, tc.limit)` — every existing case in the table needs `limit: 0` added implicitly (Go zero value, no literal needed) or explicitly for clarity; only the two renamed truncation cases and the two new cases need a nonzero-or-explicit-zero `limit`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/executor/... -run TestParseToolAction -v`
Expected: FAIL — compile error, `executor.ParseToolAction` called with 2 arguments but declared to take 1 (until Step 3).

- [ ] **Step 3: Implement the parameter threading**

In `pkg/executor/executor.go`:

1. Add `TruncateOutput int` to the `Config` struct (around line 20-30), e.g. right after `IdleTimeout`:
```go
type Config struct {
	Command        string
	ExtraArgs      []string
	IdleTimeout    time.Duration
	TruncateOutput int // 0 = no truncation; max chars for logged agent text/Bash-command detail
	OnAction       func(tool, detail string)
	SessionID      string
	Resume         bool
	StageDir       string
	WrapperDir     string
	Dir            string
}
```

2. Change `contentToAction`'s signature and body (around line 162) from:
```go
func contentToAction(c streamContent) (toolName, detail string, ok bool) {
	switch c.Type {
	case contentTypeText:
		if c.Text == "" {
			return "", "", false
		}
		d := c.Text
		if len(d) > 100 {
			d = d[:100] + "..."
		}
		return contentTypeText, d, true
```
to:
```go
func contentToAction(c streamContent, limit int) (toolName, detail string, ok bool) {
	switch c.Type {
	case contentTypeText:
		if c.Text == "" {
			return "", "", false
		}
		d := c.Text
		if limit > 0 && len(d) > limit {
			d = d[:limit] + "..."
		}
		return contentTypeText, d, true
```

And the Bash case from:
```go
		case toolNameBash:
			cmd := inp.Command
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			return toolNameBash, cmd, true
```
to:
```go
		case toolNameBash:
			cmd := inp.Command
			if limit > 0 && len(cmd) > limit {
				cmd = cmd[:limit] + "..."
			}
			return toolNameBash, cmd, true
```

And the `default:` fallback branch's cap from `if len(d) > 80 { d = d[:80] + "..." }` to `if limit > 0 && len(d) > limit { d = d[:limit] + "..." }` (same substitution — `80` → `limit`, with the added `limit > 0 &&` guard).

3. Change `ParseToolAction`'s signature (around line 117) from `func ParseToolAction(line string) (toolName, detail string, ok bool)` to `func ParseToolAction(line string, limit int) (toolName, detail string, ok bool)`, and its internal call from `contentToAction(c)` to `contentToAction(c, limit)`.

4. Update the two internal streaming closures that call `contentToAction(c)` directly (search for `if tool, detail, actionOK := contentToAction(c); actionOK {` — there are 2 remaining occurrences besides the one inside `ParseToolAction`, both inside methods on `*Executor` where `e.cfg` is in scope). Change both to `contentToAction(c, e.cfg.TruncateOutput)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/executor/... -v`
Expected: PASS, all tests including the 4 truncation-related cases in `TestParseToolAction`.

- [ ] **Step 5: Commit**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go
git commit -m "$(cat <<'EOF'
feat: параметризовать обрезку agent-вывода через executor.Config.TruncateOutput
EOF
)"
```

---

### Task 3: wire config through orchestrator call sites + docs

**Files:**
- Modify: `pkg/orchestrator/runner_factory.go`
- Modify: `pkg/orchestrator/orchestrator.go`
- Modify: `config.example.yaml`
- Modify: `README.md`
- Modify: `release-notes.md`

**Interfaces:**
- Consumes: `config.ExecutorConfig.TruncateOutput` (Task 1), `executor.Config.TruncateOutput` (Task 2).

- [ ] **Step 1: Wire `pkg/orchestrator/runner_factory.go`**

This file has 3 `executor.Config{...}` literals, all inside `runnerFor` and `runnerForFallback`. In EACH of the 3, add a `TruncateOutput: o.opts.Config.Executor.TruncateOutput,` line immediately after the existing `IdleTimeout: o.opts.Config.Executor.IdleTimeout,` line:

1. In `runnerFor`'s non-interactive branch (the `cfg := executor.Config{...}` literal with `Command`, `ExtraArgs`, `IdleTimeout`, `OnAction`, `WrapperDir`, `Dir` fields).
2. In `runnerFor`'s interactive branch (the `executor.New(executor.Config{...})` literal with `Command`, `ExtraArgs`, `IdleTimeout`, `OnAction`, `SessionID`, `Resume`, `StageDir`, `WrapperDir`, `Dir` fields).
3. In `runnerForFallback` (the `executor.New(executor.Config{...})` literal with `Command`, `IdleTimeout`, `OnAction`, `WrapperDir` fields — no `ExtraArgs`/`Dir`).

Run `gofmt -l pkg/orchestrator/runner_factory.go` after editing — expect no output (already formatted); if it lists the file, run `gofmt -w pkg/orchestrator/runner_factory.go`.

- [ ] **Step 2: Wire `pkg/orchestrator/orchestrator.go`**

In `New`, the one `executor.Config{...}` literal (fields `Command`, `ExtraArgs`, `IdleTimeout`, `OnAction`, `WrapperDir`) gets a `TruncateOutput: opts.Config.Executor.TruncateOutput,` line right after `IdleTimeout: opts.Config.Executor.IdleTimeout,` (note: `opts.Config...`, no `o.` prefix — this literal is inside the package-level `New` function, not a method).

Run `gofmt -l pkg/orchestrator/orchestrator.go` — expect no output.

- [ ] **Step 3: Verify the wiring compiles and existing tests still pass**

Run: `go build ./... && go test ./pkg/orchestrator/... -v`
Expected: build succeeds; all existing orchestrator tests still pass (no test currently asserts on `TruncateOutput`, so this just confirms the wiring didn't break anything).

- [ ] **Step 4: Document in `config.example.yaml`**

The `executor:` block (starting at line 42) currently reads:

```yaml
executor:
  # Maximum idle time with no output from the agent before killing the process.
  # Default: 30m
  # idle_timeout: 30m

  # Maximum number of stages running in parallel (0 = unlimited).
  # Default: 0
  # max_parallel: 4
```

Add a matching third entry, in the same two-comment-lines + commented-example style, right after the `max_parallel` entry (before the blank line that precedes `# Directory with custom prompt templates...`):

```yaml
  # Max chars for agent text/Bash-command detail logged to <phase>.log and the
  # agent_action event feed (0 = no limit — the full text/command is kept).
  # Default: 0
  # truncate_output: 100
```

- [ ] **Step 5: Document in `README.md`**

In the `## Configuration` section's YAML example (the `executor:` block), add a line matching the existing inline-comment style used for `idle_timeout`/`max_parallel`:

```yaml
executor:
  idle_timeout: 30m         # agent idle timeout
  max_parallel: 4           # max parallel stages (0 = unlimited)
  truncate_output: 0        # max chars for logged agent text/Bash commands (0 = no limit, default)
```

- [ ] **Step 6: Add a `release-notes.md` entry**

`release-notes.md` already has a `## 2026-07-23` section (from earlier work today) with `### Fix: empty stage badge in dashboard event feed` as its first entry. Add this as a NEW `###` entry at the TOP of that existing section — immediately after the `## 2026-07-23` heading line, before `### Fix: empty stage badge...` — not a new date section:

```markdown
## 2026-07-23

### New config: `executor.truncate_output` (default: no truncation)
- Agent tool-action output (text blocks, Bash commands) logged to `<phase>.log` and the `agent_action` event feed was previously always truncated at hardcoded lengths (100 chars for text, 80 for Bash/other tool details) — permanently, not just a display convenience (the full-screen dashboard view and the API don't recover it; only the raw `<phase>.jsonl` stream kept the untruncated original).
- New `executor.truncate_output` config (default `0` = no truncation; set to `N` to cap logged text/Bash-command detail at `N` chars, matching the old hardcoded behavior when set to 100 or 80).

### Fix: empty stage badge in dashboard event feed
```

- [ ] **Step 7: Full-suite verification**

Run: `go test ./...`
Expected: all packages pass, 0 failures.

Run: `grep -c "truncate_output" config.example.yaml README.md release-notes.md`
Expected: each file prints at least `1`.

- [ ] **Step 8: Commit**

```bash
git add pkg/orchestrator/runner_factory.go pkg/orchestrator/orchestrator.go config.example.yaml README.md release-notes.md
git commit -m "$(cat <<'EOF'
feat: прокинуть executor.truncate_output в orchestrator + документация
EOF
)"
```
