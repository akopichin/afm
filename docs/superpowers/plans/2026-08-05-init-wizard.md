# afm init v2: Wizard by Archetypes + afm validate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bare-bones `afm init` prompt loop with a deterministic archetype-driven wizard covering the real `flow.yaml` schema (agent modes, plan-vs-planning, artifacts/inputs, verify, interactive, custom command), and add a new `afm validate` command so the wizard (and any hand-edited flow.yaml) can be checked before a real `afm run`.

**Architecture:** Pure, unit-testable question/answer functions (`bufio.Scanner` in, `io.Writer` out) build a `*flow.Flow` in memory; a thin `cobra.Command` layer wires stdin/stdout, writes the YAML (`yaml.Marshal`, not manual string concatenation), and calls the shared `flow.ParseFile` validation path — both from the new `afm validate` command and from the wizard's own post-generation check.

**Tech Stack:** Go, `github.com/spf13/cobra`, `gopkg.in/yaml.v3` (already a module dependency via `pkg/flow`).

**Design doc:** `docs/superpowers/specs/2026-08-05-init-wizard-design.md`

## Global Constraints

- Do not change the Go version in `go.mod`.
- Run `make lint` after every code change in every task; it must report `0 issues` before committing (repo's `golangci-lint run --fix`).
- Every commit message must be in Russian.
- Never add `Co-Authored-By` to any commit.
- Write code that is maximally readable to a human (no clever one-liners, name things plainly) — this repo's stated house rule.
- No backwards-compatibility shims: the old `afm init` prompt loop (`stageInput` type, old `prompt()` helper, old YAML string-building) is fully replaced, not kept alongside the new wizard.
- The pre-commit hook in this repo runs `make lint && make build && make test`, which also rebuilds `pkg/web/dashboard` assets as an unrelated side effect. After each commit, run `git status --short` — if dashboard asset files (`pkg/web/dashboard/...`) show as modified/deleted/untracked, revert them with `git checkout -- <paths>` / `rm` before moving to the next task; they are not part of this work.

---

### Task 1: Clean YAML re-marshaling for `flow.Flow`/`flow.Stage`

**Files:**
- Modify: `pkg/flow/flow.go`
- Test: `pkg/flow/marshal_test.go` (new)

**Interfaces:**
- Produces: no new functions — only YAML struct tag changes on the existing `Stage`, `Flow`, `Artifact`, `Input` types in `pkg/flow/flow.go`. Later tasks rely on `yaml.Marshal(&flow.Flow{...})` producing minimal, human-readable output (no zero-value fields, `agents`/`skills`/`depends_on` rendered as inline `[a, b]` lists).

The project has never marshaled a `Flow` back to YAML before (only `yaml.Unmarshal` is used, in `flow.ParseFile`), so today's struct tags have no `omitempty` on almost anything — a straight `yaml.Marshal` would currently print every unset field (`command: ""`, `verify: ""`, `max_parallel: 0`, `supervisor: false`, …) for every stage. This task makes marshaling safe to rely on, without changing any parsing/unmarshaling behavior (`omitempty` and the `flow` style hint only affect marshaling).

- [ ] **Step 1: Write the failing test**

Create `pkg/flow/marshal_test.go`:

```go
package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStageMarshal_OmitsZeroValueFields(t *testing.T) {
	s := Stage{
		ID:     "implementation",
		Name:   "Implementation",
		Agents: []AgentType{AgentPlanning, AgentImplementation},
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	for _, unwanted := range []string{
		"command:", "verify:", "interactive:", "script:", "max_parallel:",
		"supervisor:", "auto_approve:", "eager_planning:", "plan:",
		"description:",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output contains zero-value field %q:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "agents:") {
		t.Errorf("expected agents field present:\n%s", out)
	}
}

func TestStageMarshal_AgentsRenderInlineFlowStyle(t *testing.T) {
	s := Stage{ID: "x", Agents: []AgentType{AgentPlanning, AgentImplementation, AgentReview}}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "agents: [planning, implementation, review]") {
		t.Errorf("expected inline flow-style agents, got:\n%s", data)
	}
}

func TestArtifactAndInputMarshal_OmitZeroValueFields(t *testing.T) {
	s := Stage{
		ID:        "check",
		Artifacts: []Artifact{{Name: "summary", Path: "summary.md"}},
		Inputs:    []Input{{Ref: "build.binary"}},
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	for _, unwanted := range []string{"inline:", "optional:", "description: \"\""} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output contains zero-value field %q:\n%s", unwanted, out)
		}
	}
}

func TestFlowMarshal_RoundTripsThroughParseFile(t *testing.T) {
	f := Flow{
		Name:        "roundtrip-test",
		Description: "d",
		Stages: []Stage{
			{ID: "implementation", Name: "Implementation", Description: "do it",
				Agents: []AgentType{AgentPlanning, AgentImplementation}},
		},
	}
	data, err := yaml.Marshal(&f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	parsed, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v\n---\n%s", err, data)
	}
	if parsed.Name != f.Name || len(parsed.Stages) != 1 {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/flow/... -run TestStageMarshal -v` and `go test ./pkg/flow/... -run TestArtifactAndInputMarshal -v`
Expected: FAIL — output contains `command:`, `verify:`, `inline:`, etc., and agents render as a block list, not `[planning, implementation, review]`.

- [ ] **Step 3: Add `omitempty`/`flow` tags**

In `pkg/flow/flow.go`, change the `Stage` struct's tags:

```go
// Stage represents a single stage in a flow.
type Stage struct {
	ID          string      `yaml:"id"`
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	Agents      []AgentType `yaml:"agents,omitempty,flow"`
	Skills      []string    `yaml:"skills,omitempty,flow"`
	DependsOn   []string    `yaml:"depends_on,omitempty,flow"`
	// EagerPlanning starts the planning agent immediately without
	// waiting for depends_on stages to finish (the default before
	// the planning-by-deps gate was introduced).
	EagerPlanning bool `yaml:"eager_planning,omitempty"`
	// Plan is an optional path to an existing plan file.
	// If set, the planning agent is skipped.
	Plan string `yaml:"plan,omitempty"`
	// Command overrides the global client command for this stage.
	Command string `yaml:"command,omitempty"`
	// MaxParallel limits concurrent stages using the same command.
	MaxParallel int        `yaml:"max_parallel,omitempty"`
	Artifacts   []Artifact `yaml:"artifacts,omitempty"`
	Inputs      []Input    `yaml:"inputs,omitempty"`
	Interactive bool       `yaml:"interactive,omitempty"`
	// Verify is an optional shell command executed in the project directory
	// after the stage reports completion. Non-zero exit means the stage is
	// not actually done, regardless of what the agent claims.
	Verify string `yaml:"verify,omitempty"`
	// Prompt is an optional explicit instruction delivered to the agent
	// after the <stage> context block.
	Prompt string `yaml:"prompt,omitempty"`
	// Supervisor включает оценку стадии агентом-супервизором перед запуском.
	// Стадия обязана содержать AgentPlanning в Agents.
	Supervisor       bool   `yaml:"supervisor,omitempty"`
	SupervisorPrompt string `yaml:"supervisor_prompt,omitempty"`
	// Script, if set, makes this a script-only stage: it runs the given shell
	// script (via sh -c) instead of any agent, with no planning/supervisor/
	// approval gate. Mutually exclusive with Agents/Command/Interactive/Plan/
	// Verify/Supervisor.
	Script        string        `yaml:"script,omitempty"`
	ScriptTimeout time.Duration `yaml:"script_timeout,omitempty"`
	// ScriptBefore/ScriptAfter run a shell script immediately before/after this
	// stage's own main content (agent, script, or interactive). Legal on any
	// stage type, alongside its other fields.
	ScriptBefore        string        `yaml:"script_before,omitempty"`
	ScriptBeforeTimeout time.Duration `yaml:"script_before_timeout,omitempty"`
	ScriptAfter         string        `yaml:"script_after,omitempty"`
	ScriptAfterTimeout  time.Duration `yaml:"script_after_timeout,omitempty"`
	// AutoApprove, if true, approves this stage's plan automatically the
	// instant it's ready (awaiting_approval), with no human interaction —
	// regardless of whether a dashboard is attached and regardless of
	// --require-approval. Default false. Intended for CI runs where some
	// stages need human review and others don't.
	AutoApprove bool `yaml:"auto_approve,omitempty"`
}
```

Change `Artifact`:

```go
// Artifact describes a file that a stage produces for other stages.
type Artifact struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Description string `yaml:"description,omitempty"`
	Inline      *bool  `yaml:"inline,omitempty"`
}
```

Change `Input`:

```go
// Input describes an artifact that a stage consumes from a dependency.
// Supports unmarshalling from a plain string "stage.artifact" or an object {ref, optional}.
type Input struct {
	Ref      string `yaml:"ref"`
	Optional bool   `yaml:"optional,omitempty"`
}
```

Change the `Flow` struct's `Prompt` and `MaxParallel` tags (leave `Name`/`Description`/`RootDir`/`SupervisorCommand`/`Stages` as-is — `RootDir`/`SupervisorCommand` already have `omitempty`):

```go
type Flow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Prompt is shared text added to the system prompt of every stage and
	// every phase (planning/implementation/review). Empty value does not
	// change behavior.
	Prompt      string `yaml:"prompt,omitempty"`
	MaxParallel int    `yaml:"max_parallel,omitempty"`
	...
```

(Leave the rest of the `Flow` struct body — `RootDir`, `SupervisorCommand`, `Stages` — unchanged; only `Prompt`'s and `MaxParallel`'s tags change.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/flow/... -v`
Expected: PASS for all four new tests, and no existing test in `pkg/flow` breaks (run `go test ./pkg/flow/...` with no `-run` filter to confirm).

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add pkg/flow/flow.go pkg/flow/marshal_test.go
git commit -m "$(cat <<'EOF'
fix(flow): чистый YAML при обратной сериализации Flow/Stage

Добавляем omitempty ко всем необязательным полям и flow-стиль
спискам (agents/skills/depends_on), чтобы yaml.Marshal(&flow.Flow{})
давал читаемый минимальный YAML — до сих пор Marshal нигде не
использовался (только Unmarshal в ParseFile), нужно новому wizard'у
afm init.
EOF
)"
```

---

### Task 2: `afm validate` command

**Files:**
- Create: `cmd/afm/validate.go`
- Create: `cmd/afm/validate_test.go`
- Modify: `cmd/afm/main.go`

**Interfaces:**
- Consumes: `flow.ParseFile(path string) (*flow.Flow, error)` (existing, `pkg/flow`).
- Produces: `func validateFlowFile(path string) (string, error)` and `func newValidateCmd() *cobra.Command` in `cmd/afm/validate.go` — later tasks do not depend on these (the wizard's own validation, Task 7, calls `flow.ParseFile` directly rather than reusing this CLI-message-formatting function).

- [ ] **Step 1: Write the failing tests**

Create `cmd/afm/validate_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFlow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write flow.yaml: %v", err)
	}
	return path
}

func TestValidateFlowFile_ValidFlowReturnsOK(t *testing.T) {
	path := writeTestFlow(t, `name: test-flow
description: "a test flow"
stages:
  - id: implementation
    name: "Implementation"
    description: "do the thing"
    agents: [planning, implementation, review]
`)
	msg, err := validateFlowFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "test-flow") || !strings.Contains(msg, "1 stage") {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestValidateFlowFile_CycleReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: cyclic-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [b]
  - id: b
    description: "b"
    agents: [planning, implementation]
    depends_on: [a]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for cyclic depends_on")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestValidateFlowFile_UnknownDependsOnReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: bad-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [nonexistent]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for unknown depends_on")
	}
	if !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected unknown-stage error, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/afm/... -run TestValidateFlowFile -v`
Expected: FAIL — `validateFlowFile` undefined.

- [ ] **Step 3: Implement**

Create `cmd/afm/validate.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/flow"
)

// validateFlowFile parses and validates a flow.yaml without running any
// agents. Returns a human-readable success message, or an error wrapping
// flow.ParseFile's validation failure.
func validateFlowFile(path string) (string, error) {
	f, err := flow.ParseFile(path)
	if err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return fmt.Sprintf("OK: %q — %d stage(s), valid", f.Name, len(f.Stages)), nil
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <flow.yaml>",
		Short: "Validate a flow.yaml without running it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := validateFlowFile(args[0])
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		},
	}
}
```

In `cmd/afm/main.go`, register the command (add `newValidateCmd()` to the existing `root.AddCommand(...)` list):

```go
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newValidateCmd(),
		newListCmd(),
		newInstallSkillsCmd(),
	)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/afm/... -run TestValidateFlowFile -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add cmd/afm/validate.go cmd/afm/validate_test.go cmd/afm/main.go
git commit -m "$(cat <<'EOF'
feat(cli): добавляем afm validate — проверка flow.yaml без запуска

Сейчас ошибки в flow.yaml (циклы, битые depends_on/inputs) всплывают
только на реальном afm run, когда уже подняты агенты. Новая команда
переиспользует flow.ParseFile — dry-run, без побочных эффектов.
EOF
)"
```

(Then follow the dashboard-asset cleanup step from Global Constraints if the pre-commit hook modified `pkg/web/dashboard/...`.)

---

### Task 3: Wizard input helpers

**Files:**
- Create: `cmd/afm/init_prompts.go`
- Create: `cmd/afm/init_prompts_test.go`

**Interfaces:**
- Produces:
  - `func promptLine(scanner *bufio.Scanner, w io.Writer, label string) string`
  - `func promptChoice(scanner *bufio.Scanner, w io.Writer, label string, options []string, defaultIdx int) int`
  - `func promptYesNo(scanner *bufio.Scanner, w io.Writer, label string, def bool) bool`
  - `func promptInt(scanner *bufio.Scanner, w io.Writer, label string, def int) int`
  - `func parsePhaseSelection(raw string, numOptions int, defaults []int) []int`
- Consumes: nothing new (existing `splitComma` in `cmd/afm/init.go` is left untouched and reused as-is by later tasks — do not redefine it here).

- [ ] **Step 1: Write the failing tests**

Create `cmd/afm/init_prompts_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestPromptLine_ReturnsTrimmedInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("  hello world  \n"))
	var out bytes.Buffer
	got := promptLine(scanner, &out, "Name: ")
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
	if !strings.Contains(out.String(), "Name: ") {
		t.Errorf("label not printed: %q", out.String())
	}
}

func TestPromptChoice_DefaultOnEmptyInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 1)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestPromptChoice_ValidSelection(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("3\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 0)
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestPromptChoice_RepromptsOnInvalidInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("bogus\n5\n2\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 0)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestPromptYesNo_Default(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	if got := promptYesNo(scanner, &out, "OK? ", true); !got {
		t.Errorf("expected default true")
	}
}

func TestPromptYesNo_ExplicitNo(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("n\n"))
	var out bytes.Buffer
	if got := promptYesNo(scanner, &out, "OK? ", true); got {
		t.Errorf("expected false")
	}
}

func TestPromptInt_Default(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	if got := promptInt(scanner, &out, "N? ", 2); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestPromptInt_Explicit(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("5\n"))
	var out bytes.Buffer
	if got := promptInt(scanner, &out, "N? ", 2); got != 5 {
		t.Errorf("got %d, want 5", got)
	}
}

func TestParsePhaseSelection_EmptyReturnsDefaults(t *testing.T) {
	got := parsePhaseSelection("", 2, []int{0})
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("got %v, want [0]", got)
	}
}

func TestParsePhaseSelection_ParsesValidIndices(t *testing.T) {
	got := parsePhaseSelection("1,2", 2, []int{0})
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("got %v, want [0 1]", got)
	}
}

func TestParsePhaseSelection_IgnoresInvalidIndices(t *testing.T) {
	got := parsePhaseSelection("1,9,bogus", 2, []int{0})
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("got %v, want [0]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/afm/... -run 'TestPrompt|TestParsePhaseSelection' -v`
Expected: FAIL — none of the functions exist yet.

- [ ] **Step 3: Implement**

Create `cmd/afm/init_prompts.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// promptLine prints label to w, reads one line from scanner, trims whitespace.
func promptLine(scanner *bufio.Scanner, w io.Writer, label string) string {
	fmt.Fprint(w, label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

// promptChoice prints a numbered menu (1-based) and reads a selection.
// Empty input selects defaultIdx (0-based). Out-of-range or non-numeric
// input re-prompts until a valid choice is entered.
func promptChoice(scanner *bufio.Scanner, w io.Writer, label string, options []string, defaultIdx int) int {
	fmt.Fprintln(w, label)
	for i, opt := range options {
		marker := ""
		if i == defaultIdx {
			marker = " [default]"
		}
		fmt.Fprintf(w, "  %d. %s%s\n", i+1, opt, marker)
	}
	for {
		raw := promptLine(scanner, w, "> ")
		if raw == "" {
			return defaultIdx
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Fprintf(w, "Please enter a number between 1 and %d.\n", len(options))
	}
}

// promptYesNo asks a yes/no question. Empty or unrecognized input returns def.
func promptYesNo(scanner *bufio.Scanner, w io.Writer, label string, def bool) bool {
	raw := strings.ToLower(promptLine(scanner, w, label))
	switch raw {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// promptInt reads an integer. Empty or unparseable input returns def.
func promptInt(scanner *bufio.Scanner, w io.Writer, label string, def int) int {
	raw := promptLine(scanner, w, label)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// parsePhaseSelection parses a comma-separated list of 1-based option
// indices (e.g. "1,2"). Empty input returns defaults unchanged. Invalid
// or out-of-range indices are silently skipped.
func parsePhaseSelection(raw string, numOptions int, defaults []int) []int {
	if strings.TrimSpace(raw) == "" {
		return defaults
	}
	var result []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > numOptions {
			continue
		}
		result = append(result, n-1)
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/afm/... -run 'TestPrompt|TestParsePhaseSelection' -v`
Expected: PASS for all eleven tests.

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add cmd/afm/init_prompts.go cmd/afm/init_prompts_test.go
git commit -m "$(cat <<'EOF'
feat(cli): базовые input-хелперы для нового afm init

promptLine/promptChoice/promptYesNo/promptInt/parsePhaseSelection —
чистые функции поверх bufio.Scanner + io.Writer, без прямой
зависимости от os.Stdin/os.Stdout, чтобы будущий wizard был
тестируем без реального терминала.
EOF
)"
```

---

### Task 4: Per-stage question flow (`askStageDetails` / `askAdvanced`)

**Files:**
- Create: `cmd/afm/init_stage.go`
- Create: `cmd/afm/init_stage_test.go`

**Interfaces:**
- Consumes: `promptLine`, `promptChoice`, `promptYesNo`, `parsePhaseSelection` (Task 3); `splitComma` (existing, `cmd/afm/init.go`); `flow.Stage`, `flow.AgentType`, `flow.AgentPlanning`, `flow.AgentImplementation`, `flow.AgentReview`, `flow.AgentAuto`, `flow.Artifact`, `flow.Input` (existing, `pkg/flow`).
- Produces:
  - `const stageModeStandard, stageModeAuto, stageModeScript = 0, 1, 2`
  - `type stageDefaults struct { FixedID, SuggestedID, SuggestedName string; DefaultMode int; AskDeps bool; DependsOn []string; ForceVerify bool }`
  - `func askStageDetails(scanner *bufio.Scanner, w io.Writer, d stageDefaults, priorStages []flow.Stage) flow.Stage`
  - `func askAdvanced(scanner *bufio.Scanner, w io.Writer, stage *flow.Stage, priorStages []flow.Stage)` (called internally by `askStageDetails`; exposed at package level for direct testing)

This is the core of the wizard: one round of questions produces one fully-formed `flow.Stage`. `FixedID` lets the custom archetype (Task 5) supply an ID it already read itself (its own "empty ID = stop looping" outer control), skipping the internal ID prompt; all other archetypes leave `FixedID` empty and get an Enter-to-accept default via `SuggestedID`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/afm/init_stage_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestAskStageDetails_StandardModePlanningAgentDefaults(t *testing.T) {
	lines := []string{
		"", // id -> default "implementation"
		"", // name -> default "Implementation"
		"", // mode -> default standard
		"build the thing", // description
		"",                // plan mode -> default: plan with an agent
		"",                // phases -> default: implementation only
		"n",               // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "implementation", SuggestedName: "Implementation"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.ID != "implementation" || stage.Name != "Implementation" {
		t.Fatalf("got ID=%q Name=%q", stage.ID, stage.Name)
	}
	if stage.Description != "build the thing" {
		t.Errorf("Description = %q", stage.Description)
	}
	want := []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v", stage.Agents, want)
	}
	if stage.Verify != "" || stage.Interactive || len(stage.Artifacts) != 0 || stage.Plan != "" {
		t.Errorf("unexpected fields set: %+v", stage)
	}
}

func TestAskStageDetails_PlanFileWithReviewOnlyPhase(t *testing.T) {
	lines := []string{
		"",          // id -> default "check"
		"",          // name -> default "Check"
		"",          // mode -> default standard
		"check X",   // description
		"2",         // plan mode -> "I already have a plan file"
		"plans/x.md", // plan path
		"2",         // phases -> review only
		"",          // advanced? -> default no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Plan != "plans/x.md" {
		t.Errorf("Plan = %q", stage.Plan)
	}
	want := []flow.AgentType{flow.AgentReview}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v (no planning agent — plan file was supplied)", stage.Agents, want)
	}
}

func TestAskStageDetails_AutoMode(t *testing.T) {
	lines := []string{
		"",  // id -> default
		"",  // name -> default
		"2", // mode -> auto (2nd menu option)
		"n", // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "watch", SuggestedName: "Watch"}
	stage := askStageDetails(scanner, &out, d, nil)

	want := []flow.AgentType{flow.AgentAuto}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v", stage.Agents, want)
	}
	if stage.Description != "" || stage.Plan != "" {
		t.Errorf("auto mode should skip description/plan questions: %+v", stage)
	}
}

func TestAskStageDetails_ScriptMode(t *testing.T) {
	lines := []string{
		"",            // id -> default
		"",            // name -> default
		"3",           // mode -> script (3rd menu option)
		"echo hello",  // shell command
		"n",           // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Script != "echo hello" {
		t.Errorf("Script = %q", stage.Script)
	}
	if len(stage.Agents) != 0 {
		t.Errorf("Agents = %v, want empty for a script stage", stage.Agents)
	}
}

func TestAskStageDetails_ForceVerifyAsksRegardlessOfAdvanced(t *testing.T) {
	lines := []string{
		"",              // id -> default
		"",              // name -> default
		"",              // mode -> default standard
		"go test suite", // description
		"",              // plan mode -> default agent
		"",              // phases -> default implementation
		"go test ./...", // forced verify command
		"n",             // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "build", SuggestedName: "Build", ForceVerify: true}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Verify != "go test ./..." {
		t.Errorf("Verify = %q", stage.Verify)
	}
}

func TestAskStageDetails_AskDepsForCustomArchetype(t *testing.T) {
	lines := []string{
		"",       // name -> default "My Stage" (FixedID set, id not asked)
		"",       // mode -> default standard
		"do x",   // description
		"",       // plan mode -> default agent
		"",       // phases -> default implementation
		"a,b",    // depends_on
		"n",      // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{FixedID: "mystage", SuggestedName: "My Stage", AskDeps: true}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.ID != "mystage" {
		t.Errorf("ID = %q, want mystage (FixedID must be used verbatim)", stage.ID)
	}
	if !reflect.DeepEqual(stage.DependsOn, []string{"a", "b"}) {
		t.Errorf("DependsOn = %v", stage.DependsOn)
	}
}

func TestAskStageDetails_AdvancedBlockArtifactsInputsVerifyInteractiveCommand(t *testing.T) {
	priorStages := []flow.Stage{
		{ID: "build", Artifacts: []flow.Artifact{{Name: "binary", Path: "bin/app"}}},
	}
	lines := []string{
		"",              // id -> default "check"
		"",              // name -> default "Check"
		"",              // mode -> default standard
		"verify build",  // description
		"",              // plan mode -> default agent
		"",              // phases -> default implementation
		"y",             // advanced? -> yes
		"",              // artifact name -> empty, stop (no artifacts of its own)
		"build.binary",  // inputs -> consume build's artifact
		"some check",    // verify command
		"y",             // interactive -> yes
		"",              // custom command -> empty (skip)
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check", DependsOn: []string{"build"}}
	stage := askStageDetails(scanner, &out, d, priorStages)

	if !reflect.DeepEqual(stage.Inputs, []flow.Input{{Ref: "build.binary"}}) {
		t.Errorf("Inputs = %+v", stage.Inputs)
	}
	if stage.Verify != "some check" {
		t.Errorf("Verify = %q", stage.Verify)
	}
	if !stage.Interactive {
		t.Errorf("Interactive = false, want true")
	}
	if stage.Command != "" {
		t.Errorf("Command = %q, want empty", stage.Command)
	}
	if len(stage.Artifacts) != 0 {
		t.Errorf("Artifacts = %+v, want none", stage.Artifacts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/afm/... -run TestAskStageDetails -v`
Expected: FAIL — `askStageDetails`/`stageDefaults` undefined.

- [ ] **Step 3: Implement**

Create `cmd/afm/init_stage.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

const (
	stageModeStandard = 0
	stageModeAuto     = 1
	stageModeScript   = 2
)

var stageModeOptions = []string{
	"Standard agent stage (planning/implementation/review — choose phases)",
	"auto (fully autonomous agent, no planning/supervisor)",
	"script (shell command instead of an agent)",
}

const (
	planModeAgent = 0
	planModeFile  = 1
)

var planModeOptions = []string{
	"Plan with an agent (this stage writes its own plan)",
	"I already have a plan file (I'll provide the path)",
}

var phaseOptions = []string{"implementation", "review"}

// stageDefaults configures the suggested defaults for one round of
// askStageDetails. FixedID, when non-empty, is used as the stage ID
// directly (the custom archetype's own loop already read it as the
// "empty ID = stop" signal) — the ID question is skipped entirely. When
// FixedID is empty, SuggestedID is offered as the Enter-to-accept
// default for a normal ID prompt. DependsOn is used verbatim unless
// AskDeps is true, in which case it's read interactively instead.
type stageDefaults struct {
	FixedID       string
	SuggestedID   string
	SuggestedName string
	DefaultMode   int
	AskDeps       bool
	DependsOn     []string
	ForceVerify   bool
}

// askStageDetails runs the full per-stage question sequence and returns
// the resulting Stage. priorStages is used to offer artifacts of
// dependency stages as candidate `inputs` in the advanced block.
func askStageDetails(scanner *bufio.Scanner, w io.Writer, d stageDefaults, priorStages []flow.Stage) flow.Stage {
	id := d.FixedID
	if id == "" {
		id = promptLine(scanner, w, fmt.Sprintf("Stage ID [%s]: ", d.SuggestedID))
		if id == "" {
			id = d.SuggestedID
		}
	}
	name := promptLine(scanner, w, fmt.Sprintf("Stage name [%s]: ", d.SuggestedName))
	if name == "" {
		name = d.SuggestedName
	}

	stage := flow.Stage{ID: id, Name: name}

	mode := promptChoice(scanner, w, "Stage mode:", stageModeOptions, d.DefaultMode)

	switch mode {
	case stageModeAuto:
		stage.Agents = []flow.AgentType{flow.AgentAuto}
	case stageModeScript:
		stage.Script = promptLine(scanner, w, "Shell command to run: ")
	default: // stageModeStandard
		stage.Description = promptLine(scanner, w, "Description: ")

		planMode := promptChoice(scanner, w, "Does this stage already have a plan?", planModeOptions, planModeAgent)
		if planMode == planModeFile {
			stage.Plan = promptLine(scanner, w, "Path to plan file: ")
		} else {
			stage.Agents = append(stage.Agents, flow.AgentPlanning)
		}

		selected := parsePhaseSelection(
			promptLine(scanner, w, "Which phases to include? [1] implementation [2] review (default: implementation): "),
			len(phaseOptions),
			[]int{0},
		)
		for _, idx := range selected {
			switch phaseOptions[idx] {
			case "implementation":
				stage.Agents = append(stage.Agents, flow.AgentImplementation)
			case "review":
				stage.Agents = append(stage.Agents, flow.AgentReview)
			}
		}
	}

	if d.AskDeps {
		raw := promptLine(scanner, w, "Depends on (comma-separated IDs, or empty): ")
		stage.DependsOn = splitComma(raw)
	} else {
		stage.DependsOn = d.DependsOn
	}

	if d.ForceVerify {
		stage.Verify = promptLine(scanner, w, "Verify shell command (runs after the stage reports done; non-zero triggers one retry): ")
	}

	if promptYesNo(scanner, w, "Advanced settings for this stage? (artifacts/inputs/verify/interactive/custom command) [y/N]: ", false) {
		askAdvanced(scanner, w, &stage, priorStages)
	}

	return stage
}

// askAdvanced collects the optional advanced fields: artifacts, inputs
// (offered from priorStages' declared artifacts, restricted to the
// stage's own DependsOn), a verify command (skipped if already set by
// ForceVerify), interactive mode, and a custom agent command.
func askAdvanced(scanner *bufio.Scanner, w io.Writer, stage *flow.Stage, priorStages []flow.Stage) {
	for {
		name := promptLine(scanner, w, "  Artifact name (empty to stop): ")
		if name == "" {
			break
		}
		path := promptLine(scanner, w, "  Artifact path: ")
		desc := promptLine(scanner, w, "  Artifact description: ")
		stage.Artifacts = append(stage.Artifacts, flow.Artifact{Name: name, Path: path, Description: desc})
	}

	depsSet := make(map[string]bool, len(stage.DependsOn))
	for _, dep := range stage.DependsOn {
		depsSet[dep] = true
	}
	var candidates []string
	for _, prior := range priorStages {
		if !depsSet[prior.ID] {
			continue
		}
		for _, a := range prior.Artifacts {
			candidates = append(candidates, prior.ID+"."+a.Name)
		}
	}
	if len(candidates) > 0 {
		fmt.Fprintf(w, "  Available inputs from dependencies: %s\n", strings.Join(candidates, ", "))
		chosen := promptLine(scanner, w, "  Which to consume as inputs? (comma-separated, empty for none): ")
		for _, c := range splitComma(chosen) {
			stage.Inputs = append(stage.Inputs, flow.Input{Ref: c})
		}
	}

	if stage.Verify == "" {
		v := promptLine(scanner, w, "  Verify shell command (empty to skip): ")
		if v != "" {
			stage.Verify = v
		}
	}

	stage.Interactive = promptYesNo(scanner, w, "  Interactive (agent can ask the user questions)? [y/N]: ", false)

	cmd := promptLine(scanner, w, "  Custom agent command (empty for default): ")
	if cmd != "" {
		stage.Command = cmd
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/afm/... -run TestAskStageDetails -v`
Expected: PASS for all seven tests.

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add cmd/afm/init_stage.go cmd/afm/init_stage_test.go
git commit -m "$(cat <<'EOF'
feat(cli): вопросы по стадии для нового afm init

askStageDetails — режим стадии (standard/auto/script), для standard —
план агентом vs готовый plan: путь + multi-select фаз
(implementation/review); опциональный advanced-блок
(artifacts/inputs/verify/interactive/custom command).
EOF
)"
```

---

### Task 5: Archetype builders

**Files:**
- Create: `cmd/afm/init_archetype.go`
- Create: `cmd/afm/init_archetype_test.go`

**Interfaces:**
- Consumes: `askStageDetails`, `stageDefaults`, `stageModeStandard`, `stageModeScript` (Task 4); `promptLine`, `promptInt`, `promptChoice` (Task 3); `flow.Stage`, `flow.Flow` (existing).
- Produces:
  - `const archetypeSingleChange, archetypeVerifyLoop, archetypeParallelTracks, archetypeCustom = 0, 1, 2, 3`
  - `var archetypeOptions []string`
  - `func buildArchetypeStages(archetype int, scanner *bufio.Scanner, w io.Writer) []flow.Stage`
  - `func buildSingleChangeStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage`
  - `func buildVerifyLoopStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage`
  - `func buildParallelTracksStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage`
  - `func buildCustomStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage`
  - Test helper `parseGeneratedFlow(t *testing.T, f *flow.Flow) *flow.Flow` — defined in `cmd/afm/init_archetype_test.go`; Tasks 6 and 7's tests reuse it directly (same package `main`), do not redefine it.

- [ ] **Step 1: Write the failing tests**

Create `cmd/afm/init_archetype_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"gopkg.in/yaml.v3"
)

// parseGeneratedFlow marshals f, writes it to a temp file, and parses it
// back through flow.ParseFile — failing the test if the generated
// flow.yaml doesn't pass real schema validation. Shared by later tasks'
// tests in this package.
func parseGeneratedFlow(t *testing.T, f *flow.Flow) *flow.Flow {
	t.Helper()
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("marshal flow: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write flow: %v", err)
	}
	parsed, err := flow.ParseFile(path)
	if err != nil {
		t.Fatalf("generated flow.yaml is invalid: %v\n---\n%s", err, data)
	}
	return parsed
}

func TestBuildSingleChangeStages_ProducesValidFlow(t *testing.T) {
	lines := []string{
		"", "", "", "ship the feature", "", "", "n",
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	stages := buildSingleChangeStages(scanner, &out)
	if len(stages) != 1 || stages[0].ID != "implementation" {
		t.Fatalf("got %+v", stages)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildVerifyLoopStages_CheckDefaultsToScriptAndIsValid(t *testing.T) {
	// build: id/name defaults, mode default (standard), description,
	// plan mode default (agent), phases default (implementation), forced
	// verify command, advanced -> no.
	buildLines := []string{"", "", "", "implement the feature", "", "", "go test ./...", "n"}
	// check: id/name defaults, mode default (script, per DefaultMode),
	// script command, advanced -> no.
	checkLines := []string{"", "", "go vet ./...", "n"}
	full := strings.Join(buildLines, "\n") + "\n" + strings.Join(checkLines, "\n") + "\n"
	scanner := bufio.NewScanner(strings.NewReader(full))
	var out bytes.Buffer
	stages := buildVerifyLoopStages(scanner, &out)
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	build, check := stages[0], stages[1]
	if build.Verify != "go test ./..." {
		t.Errorf("build.Verify = %q", build.Verify)
	}
	if check.Script != "go vet ./..." {
		t.Errorf("check.Script = %q, want the default script mode to be used", check.Script)
	}
	if len(check.Agents) != 0 {
		t.Errorf("check.Agents = %v, want empty (script stage)", check.Agents)
	}
	if len(check.DependsOn) != 1 || check.DependsOn[0] != build.ID {
		t.Errorf("check.DependsOn = %v", check.DependsOn)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f) // fails the test if the generated flow.yaml doesn't validate
}

func TestBuildParallelTracksStages_DefaultTwoTracksPlusIntegration(t *testing.T) {
	trackAnswers := []string{"", "", "", "track work", "", "", "n"}
	full := "\n" + // "How many parallel tracks?" -> default 2
		strings.Join(trackAnswers, "\n") + "\n" + // track-1
		strings.Join(trackAnswers, "\n") + "\n" + // track-2
		strings.Join(trackAnswers, "\n") + "\n" // integration
	scanner := bufio.NewScanner(strings.NewReader(full))
	var out bytes.Buffer
	stages := buildParallelTracksStages(scanner, &out)
	if len(stages) != 3 {
		t.Fatalf("got %d stages, want 3 (2 tracks + integration)", len(stages))
	}
	integration := stages[2]
	if len(integration.DependsOn) != 2 {
		t.Errorf("integration.DependsOn = %v, want 2 entries", integration.DependsOn)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildCustomStages_StopsOnEmptyID(t *testing.T) {
	stageLines := []string{
		"alpha",   // outer loop: stage ID
		"",        // name -> default "alpha"
		"",        // mode -> default standard
		"do alpha", // description
		"",        // plan mode -> default agent
		"",        // phases -> default implementation
		"",        // depends_on (AskDeps=true) -> empty
		"n",       // advanced? -> no
		"",        // outer loop: empty ID -> stop
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(stageLines, "\n") + "\n"))
	var out bytes.Buffer
	stages := buildCustomStages(scanner, &out)
	if len(stages) != 1 || stages[0].ID != "alpha" {
		t.Fatalf("got %+v", stages)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/afm/... -run 'TestBuild.*Stages' -v`
Expected: FAIL — archetype builder functions undefined.

- [ ] **Step 3: Implement**

Create `cmd/afm/init_archetype.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"io"

	"github.com/akopichin/afm/pkg/flow"
)

const (
	archetypeSingleChange   = 0
	archetypeVerifyLoop     = 1
	archetypeParallelTracks = 2
	archetypeCustom         = 3
)

var archetypeOptions = []string{
	"Single change (planning → implementation → review)",
	"Build + verify loop (build → automated check, one retry on failure)",
	"Parallel tracks → integration (independent stages merge into one)",
	"Custom (build stage-by-stage from scratch)",
}

// buildArchetypeStages dispatches to the stage-graph builder for the
// chosen archetype.
func buildArchetypeStages(archetype int, scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	switch archetype {
	case archetypeVerifyLoop:
		return buildVerifyLoopStages(scanner, w)
	case archetypeParallelTracks:
		return buildParallelTracksStages(scanner, w)
	case archetypeCustom:
		return buildCustomStages(scanner, w)
	default: // archetypeSingleChange
		return buildSingleChangeStages(scanner, w)
	}
}

func buildSingleChangeStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	s := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "implementation",
		SuggestedName: "Implementation",
		DefaultMode:   stageModeStandard,
	}, nil)
	return []flow.Stage{s}
}

func buildVerifyLoopStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	fmt.Fprintln(w, "\nThis is one automatic retry on failure, not a full cycle — a real review-loop (goto) is still in development.")
	build := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "build",
		SuggestedName: "Build",
		DefaultMode:   stageModeStandard,
		ForceVerify:   true,
	}, nil)
	check := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "check",
		SuggestedName: "Check",
		DefaultMode:   stageModeScript,
		DependsOn:     []string{build.ID},
	}, []flow.Stage{build})
	return []flow.Stage{build, check}
}

func buildParallelTracksStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	n := promptInt(scanner, w, "How many parallel tracks? [2]: ", 2)
	tracks := make([]flow.Stage, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("track-%d", i)
		s := askStageDetails(scanner, w, stageDefaults{
			SuggestedID:   id,
			SuggestedName: fmt.Sprintf("Track %d", i),
			DefaultMode:   stageModeStandard,
		}, nil)
		tracks = append(tracks, s)
	}
	trackIDs := make([]string, len(tracks))
	for i, tr := range tracks {
		trackIDs[i] = tr.ID
	}
	integration := askStageDetails(scanner, w, stageDefaults{
		SuggestedID:   "integration",
		SuggestedName: "Integration",
		DefaultMode:   stageModeStandard,
		DependsOn:     trackIDs,
	}, tracks)
	return append(tracks, integration)
}

func buildCustomStages(scanner *bufio.Scanner, w io.Writer) []flow.Stage {
	var stages []flow.Stage
	for {
		fmt.Fprintln(w, "\nAdd a stage (leave ID empty to finish):")
		id := promptLine(scanner, w, "  Stage ID: ")
		if id == "" {
			break
		}
		s := askStageDetails(scanner, w, stageDefaults{
			FixedID:       id,
			SuggestedName: id,
			DefaultMode:   stageModeStandard,
			AskDeps:       true,
		}, stages)
		stages = append(stages, s)
	}
	return stages
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/afm/... -run 'TestBuild.*Stages' -v`
Expected: PASS for all four tests — in particular `TestBuildVerifyLoopStages_CheckDefaultsToScriptAndIsValid` is the regression guard for the schema-validity issue found during design review (a `review`-only check stage with no planning agent and no `plan:` fails `flow.Flow.validate()`'s "must have planning agent, a plan path, or script" rule).

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add cmd/afm/init_archetype.go cmd/afm/init_archetype_test.go
git commit -m "$(cat <<'EOF'
feat(cli): архетипы флоу для нового afm init

4 архетипа (single change / build+verify loop / parallel
tracks→integration / custom) поверх askStageDetails — каждый архетип
задаёт граф зависимостей и дефолты, per-stage вопросы одни и те же.
Тест на verify-loop проверяет, что дефолтная check-стадия (script)
реально проходит flow.ParseFile — предыдущий вариант дефолта
(review-only) не проходил бы валидацию схемы.
EOF
)"
```

---

### Task 6: Wizard orchestration (`runInitWizard`)

**Files:**
- Modify: `cmd/afm/init.go` (add function; do not remove the old `newInitCmd`/`stageInput`/`prompt` yet — that happens in Task 7)
- Modify: `cmd/afm/init_test.go` (extend)

**Interfaces:**
- Consumes: `archetypeOptions`, `archetypeSingleChange`, `buildArchetypeStages` (Task 5); `promptLine`, `promptChoice` (Task 3); `parseGeneratedFlow` (Task 5's test helper, reused here — do not redefine).
- Produces: `func runInitWizard(scanner *bufio.Scanner, w io.Writer) *flow.Flow`

- [ ] **Step 1: Write the failing test**

Add to `cmd/afm/init_test.go` (append; keep the existing `TestEnsureGitignoreEntry` test as-is):

```go
func TestRunInitWizard_SingleChangeArchetype(t *testing.T) {
	lines := []string{
		"my-feature",   // flow name
		"does a thing", // flow description
		"",             // archetype -> default (single change)
		"",             // stage id -> default "implementation"
		"",             // stage name -> default
		"",             // mode -> default standard
		"ship it",      // description
		"",             // plan mode -> default agent
		"",             // phases -> default implementation
		"n",            // advanced -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	f := runInitWizard(scanner, &out)

	if f.Name != "my-feature" || f.Description != "does a thing" {
		t.Errorf("got name=%q description=%q", f.Name, f.Description)
	}
	if len(f.Stages) != 1 || f.Stages[0].ID != "implementation" {
		t.Fatalf("got stages: %+v", f.Stages)
	}
	parseGeneratedFlow(t, f)
}
```

Add the needed imports (`bytes`, `strings` — `bufio` and `testing` are already imported by the existing file) to `cmd/afm/init_test.go`'s import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/afm/... -run TestRunInitWizard -v`
Expected: FAIL — `runInitWizard` undefined.

- [ ] **Step 3: Implement**

Add to `cmd/afm/init.go` (append the function; the old `newInitCmd`/`stageInput`/`prompt`/`splitComma`/`ensureGitignoreEntry` in this file are untouched in this task):

```go
// runInitWizard runs the full interactive flow: archetype selection,
// flow-level name/description, and per-stage questions. Returns the
// fully built Flow — nothing is written to disk yet.
func runInitWizard(scanner *bufio.Scanner, w io.Writer) *flow.Flow {
	name := promptLine(scanner, w, "Flow name (e.g. my-feature): ")
	description := promptLine(scanner, w, "Flow description: ")

	archetype := promptChoice(scanner, w, "\nWhat kind of flow are you building?", archetypeOptions, archetypeSingleChange)
	stages := buildArchetypeStages(archetype, scanner, w)

	return &flow.Flow{Name: name, Description: description, Stages: stages}
}
```

Add `"github.com/akopichin/afm/pkg/flow"` and `"io"` to `cmd/afm/init.go`'s import block if not already present (they are not, in the current file).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/afm/... -run TestRunInitWizard -v`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`.

```bash
git add cmd/afm/init.go cmd/afm/init_test.go
git commit -m "$(cat <<'EOF'
feat(cli): оркестрация мастера afm init (архетип + имя/описание)

runInitWizard связывает выбор архетипа (Task 5) и per-stage вопросы
в единый Flow. Старый RunE и его вспомогательные типы пока не
трогаем — заменяются отдельным коммитом.
EOF
)"
```

---

### Task 7: Rewire `newInitCmd` — write, validate, repair loop

**Files:**
- Modify: `cmd/afm/init.go` (replace `newInitCmd`'s body; delete now-dead `stageInput` type and `prompt` function)
- Modify: `cmd/afm/init_test.go` (extend)

**Interfaces:**
- Consumes: `runInitWizard` (Task 6); `promptChoice`, `promptLine` (Task 3); `flowsDir()` (existing, `cmd/afm/main.go`); `ensureGitignoreEntry` (existing, `cmd/afm/init.go` — unchanged).
- Produces: `func generateAndValidateFlow(f *flow.Flow, outPath string) (string, error)`; rewritten `newInitCmd()`.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/afm/init_test.go`:

```go
func TestGenerateAndValidateFlow_ValidFlowWritesAndValidates(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "flow.yaml")
	f := &flow.Flow{
		Name:        "test-flow",
		Description: "desc",
		Stages: []flow.Stage{
			{ID: "implementation", Name: "Implementation", Description: "do it",
				Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		},
	}
	if _, err := generateAndValidateFlow(f, outPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Errorf("file not written: %v", statErr)
	}
}

func TestGenerateAndValidateFlow_InvalidDependsOnReturnsErrorButStillWritesFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "flow.yaml")
	f := &flow.Flow{
		Name: "test-flow",
		Stages: []flow.Stage{
			{ID: "a", Description: "d", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
				DependsOn: []string{"nonexistent"}},
		},
	}
	_, err := generateAndValidateFlow(f, outPath)
	if err == nil {
		t.Fatal("expected error for bad depends_on")
	}
	if !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("error = %v", err)
	}
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Errorf("file should still be written even when invalid, for manual repair: %v", statErr)
	}
}
```

Add `"os"`, `"path/filepath"` to `cmd/afm/init_test.go`'s import block if not already present (`os` already is, from `TestEnsureGitignoreEntry`; `path/filepath` already is too — check before adding to avoid a duplicate-import compile error).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/afm/... -run TestGenerateAndValidateFlow -v`
Expected: FAIL — `generateAndValidateFlow` undefined.

- [ ] **Step 3: Implement**

In `cmd/afm/init.go`, delete the old `newInitCmd()` function body, the `stageInput` struct, and the `prompt` function (they are fully superseded — `ensureGitignoreEntry` and `splitComma` stay, still used). Replace with:

```go
// generateAndValidateFlow marshals f to YAML, writes it to outPath, and
// validates the result via flow.ParseFile. Returns the rendered YAML and
// a validation error (if any) — the file is written regardless of
// validity, so the user (or the wizard's own repair loop) can inspect
// or edit it.
func generateAndValidateFlow(f *flow.Flow, outPath string) (string, error) {
	data, err := yaml.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("render flow.yaml: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return "", fmt.Errorf("create flows dir: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return "", fmt.Errorf("write flow file: %w", err)
	}
	if _, err := flow.ParseFile(outPath); err != nil {
		return string(data), fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return string(data), nil
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a flow.yaml interactively",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)
			f := runInitWizard(scanner, os.Stdout)
			outPath := filepath.Join(flowsDir(), f.Name+".yaml")

			for {
				_, err := generateAndValidateFlow(f, outPath)
				if err == nil {
					break
				}
				fmt.Printf("\n✗ %s — %v\n", outPath, err)
				choice := promptChoice(scanner, os.Stdout, "What next?", []string{
					"Edit the file manually, then re-validate",
					"Restart the wizard from scratch",
					"Exit (file stays on disk, but invalid)",
				}, 0)
				switch choice {
				case 0:
					promptLine(scanner, os.Stdout, "Press Enter once you've fixed the file: ")
				case 1:
					f = runInitWizard(scanner, os.Stdout)
					outPath = filepath.Join(flowsDir(), f.Name+".yaml")
				default:
					return fmt.Errorf("flow.yaml left invalid at %s: %w", outPath, err)
				}
			}

			if err := ensureGitignoreEntry(".", ".afm/secrets.env"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
			}

			fmt.Printf("\n✓ Created: %s\n✓ Validated: %d stage(s), valid\n", outPath, len(f.Stages))
			fmt.Printf("\nRun it with:\n  afm run %s\n", outPath)
			return nil
		},
	}
}
```

Update `cmd/afm/init.go`'s import block to the full set actually used across the file now: `bufio`, `fmt`, `io`, `os`, `path/filepath`, `strings`, `github.com/spf13/cobra`, `github.com/akopichin/afm/pkg/flow`, `gopkg.in/yaml.v3`. Remove any import that's no longer used after deleting the old `prompt`/`stageInput` code (check with `go vet ./cmd/afm/...` or let `make lint` catch unused imports).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/afm/... -v`
Expected: PASS for every test in the package (all tasks' tests together — this is the first point where the whole rewritten `cmd/afm/init.go` compiles as a single unit, so also run the full package test, not just the two new tests).

- [ ] **Step 5: Lint and commit**

Run: `make lint` — expect `0 issues`. Run `make build` to confirm `cmd/afm` still compiles into a working binary.

```bash
git add cmd/afm/init.go cmd/afm/init_test.go
git commit -m "$(cat <<'EOF'
feat(cli): переключаем afm init на новый wizard + самопроверку

newInitCmd теперь: runInitWizard -> yaml.Marshal -> запись файла ->
flow.ParseFile на своём же результате. При ошибке — меню
исправить-вручную/начать-заново/выйти вместо молчаливой записи
невалидного flow.yaml. Старый построчный prompt-луп и stageInput
удалены — полностью заменены.
EOF
)"
```

(Then follow the dashboard-asset cleanup step from Global Constraints if the pre-commit hook modified `pkg/web/dashboard/...`.)

---

### Task 8: README — document the new wizard and `afm validate`

**Files:**
- Modify: `README.md`

No tests (documentation-only). Still gets its own commit.

- [ ] **Step 1: Update the Quick Start section**

In `README.md`, replace the "### 1. Create a flow" section (currently at line 127):

```markdown
### 1. Create a flow

```bash
afm init
```

Interactively asks questions and creates `.afm/flows/<name>.yaml`. Or write it by hand — see the example below.
```

with:

```markdown
### 1. Create a flow

```bash
afm init
```

Walks you through one of four archetypes — a single change
(planning → implementation → review), a build + verify loop, parallel
tracks merging into an integration stage, or fully custom
stage-by-stage — then asks per-stage questions (agent mode, plan vs.
planning agent, which phases to run, and optional artifacts/inputs/
verify/interactive/custom-command settings). The result is validated
before the wizard reports success. Or write `flow.yaml` by hand — see
the example below.

```bash
afm validate flow.yaml
```

Checks a flow.yaml for structural errors (dependency cycles, unknown
`depends_on`/`inputs` references, …) without running any agents. The
wizard runs this automatically after generating a file; run it yourself
after hand-editing a flow.
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: README про новый afm init (архетипы) и afm validate
EOF
)"
```

(Then follow the dashboard-asset cleanup step from Global Constraints if the pre-commit hook modified `pkg/web/dashboard/...`.)

---

## Self-Review Notes

- **Spec coverage:** archetypes (§1) → Tasks 5/6; per-stage questions incl. mode/plan-vs-file/phases/advanced (§2) → Task 4; `afm validate` (§3) → Task 2; wizard+validate integration and repair loop (§4) → Task 7; clean YAML output (implied by §1's rendered examples) → Task 1; README (out of `Вне охвата`, but implicitly expected of any user-facing CLI change) → Task 8. The design doc's "Вне охвата" items (skill update, real goto-based loop, NL inference, multi-error accumulation, per-field jump-back repair) are deliberately not tasked here.
- **Correctness fix carried from spec:** the design doc's self-review already caught that `check`'s original review-only default wouldn't pass `validate()`'s "must have planning agent, a plan path, or script" rule; Task 5 defaults `check` to `script` mode and Task 5's test (`TestBuildVerifyLoopStages_CheckDefaultsToScriptAndIsValid`) asserts the round-trip through real `flow.ParseFile` succeeds, so this can't regress silently.
- **Type consistency check:** `stageDefaults` (Task 4) is used identically in Task 5's four builder functions and nowhere else; `askStageDetails`'s signature `(scanner *bufio.Scanner, w io.Writer, d stageDefaults, priorStages []flow.Stage) flow.Stage` matches every call site in Tasks 5 and (transitively, via `buildArchetypeStages`) Task 6; `parseGeneratedFlow` is defined once (Task 5's test file) and reused verbatim by Tasks 6 and 7's tests, same package.
