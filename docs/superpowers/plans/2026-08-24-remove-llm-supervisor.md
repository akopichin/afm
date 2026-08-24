# Remove LLM Supervisor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the LLM-supervisor subsystem (per-stage `supervisor: true` in flow.yaml, which called an LLM to decide autonomous-vs-standard execution) from afm entirely, while leaving the static `agents: [auto]` track — which shares the `autonomous.flag` file mechanism but never involved an LLM decision — fully intact and untouched.

**Architecture:** This is a removal, not a feature. Work proceeds package-by-package in dependency order (orchestrator core → flow → config → cmd/afm wiring → server → dashboard frontend → docs), so `go build ./...`/`go test ./...` stays green after every task. A small new behavior is added along the way: `pkg/flow.ParseFile` prints a non-fatal warning if it sees the now-removed `supervisor`/`supervisor_prompt`/`supervisor_command` keys in a flow.yaml, since Go's non-strict YAML unmarshal would otherwise silently drop them with no signal to the user.

**Tech Stack:** Go (backend/orchestrator), TypeScript/React/Vitest (dashboard frontend), YAML (flow.yaml/config.yaml parsing via gopkg.in/yaml.v3).

**Spec:** `docs/superpowers/specs/2026-08-24-remove-llm-supervisor-design.md`

## Global Constraints

- Every task must leave `go build ./...` and the full `go test ./...` green (per the spec's acceptance bar: "nothing degraded" in the remaining tracks — static `agents:[auto]`, script stages, pre-planned stages, standard planning→implementation→review).
- Do not touch `autonomous.flag`, `isAutonomousStage`, `clearStaleAutonomousFlag`, `runAutonomousAgent`, `activateAutoStage`, or any `agents:[auto]`-specific code/tests — these are shared with the static auto track and are explicitly out of scope.
- Do not touch `executor.Runner.RunJSONQuery` / `Executor.RunJSONQuery` or its interface-conformance test mocks across the orchestrator test suite — it's a general-purpose interface method, not supervisor-specific; the supervisor was only its one production caller.
- Do not touch `pkg/docker/wrapper_test.go` or `pkg/executor/debug_test.go` — both were checked during planning and only mention the word "supervisor" as an example label in an unrelated general-purpose test, not supervisor feature code.
- Never edit `pkg/web/dashboard/public/skins/` — it's fully regenerated from `pkg/web/dashboard/skins/` by `scripts/sync-skins.js` on every build; edit only the `skins/` source.
- Follow existing code comment language conventions per file (this codebase mixes Russian and English comments; match whatever a given file already uses for comments in the immediate vicinity of an edit).

---

## Task 1: Remove the LLM supervisor from the orchestrator core (Go)

This is one atomic unit: `pkg/orchestrator`'s package-internal files (`orchestrator.go`, `supervisor_track.go`, `scheduling.go`, `recovery.go`) and its `bus` sub-package all reference each other and must compile together. The `pkg/orchestrator/supervisor` package is deleted in the same task since nothing else will import it afterward (an orphaned package would trip the `unused` linter). `scenario_harness_test.go` also constructs `orchestrator.Options{SupervisorRunner: ...}` and must be fixed in this same task to keep `go test ./pkg/orchestrator/...` compiling.

**Files:**
- Delete: `pkg/orchestrator/supervisor/supervisor.go`
- Delete: `pkg/orchestrator/supervisor/supervisor_test.go`
- Delete: `pkg/orchestrator/supervisor_track.go` (will be replaced by a smaller file, see below)
- Create: `pkg/orchestrator/autonomous_flag.go` (the shared bits `supervisor_track.go` used to hold, renamed since nothing in it is about the supervisor anymore)
- Delete: `pkg/orchestrator/supervisor_orchestrator_test.go`
- Delete: `pkg/orchestrator/integration_supervisor_test.go`
- Modify: `pkg/orchestrator/orchestrator.go`
- Modify: `pkg/orchestrator/scheduling.go`
- Modify: `pkg/orchestrator/recovery.go`
- Modify: `pkg/orchestrator/scenario_harness_test.go`
- Modify: `pkg/orchestrator/scenario_test.go`
- Modify: `pkg/orchestrator/bus/fsm.go`
- Modify: `pkg/orchestrator/bus/fsm_test.go`
- Modify: `pkg/orchestrator/bus/bus.go`

**Interfaces:**
- Produces: `pkg/orchestrator.Options` with NO `SupervisorRunner` field. `pkg/orchestrator.Orchestrator` with NO `supervisor` field. `pkg/orchestrator/bus` with NO `EvSupervisorApproved` / `EventSupervisorDecision`. Functions `isAutonomousStage(stageDir string) bool` and `clearStaleAutonomousFlag(stageDir string)` continue to exist (moved to `autonomous_flag.go`, same package, same signatures) — later tasks and existing callers (`dialog_poller.go`, `hooks.go`, `agents.go`) are unaffected.
- Consumes: nothing from other tasks (this is the first task).

- [ ] **Step 1: Delete the supervisor package**

```bash
rm -rf pkg/orchestrator/supervisor
```

- [ ] **Step 2: Replace `supervisor_track.go` with `autonomous_flag.go`, keeping only the shared, non-supervisor helpers**

```bash
git rm pkg/orchestrator/supervisor_track.go
```

Create `pkg/orchestrator/autonomous_flag.go` with exactly this content:

```go
package orchestrator

import (
	"os"
	"path/filepath"
)

// isAutonomousStage возвращает true, если stageDir содержит autonomous.flag —
// маркер того, что стадия уже переведена на автономный трек (agents:[auto]).
func isAutonomousStage(stageDir string) bool {
	_, err := os.Stat(filepath.Join(stageDir, "autonomous.flag"))
	return err == nil
}

// clearStaleAutonomousFlag удаляет autonomous.flag, оставшийся от неудавшейся
// автономной попытки, когда текущая попытка идёт по стандартному треку
// (planning). Без этого isAutonomousStage (и производный от неё
// stage_autonomous в /api/status) навсегда считал бы стадию автономной — даже
// после того, как она реально прошла planning и получила настоящий plan.md,
// ожидающий approve/revise в дашборде.
func clearStaleAutonomousFlag(stageDir string) {
	_ = os.Remove(filepath.Join(stageDir, "autonomous.flag"))
}
```

- [ ] **Step 3: Delete the two orchestrator-level supervisor test files**

```bash
git rm pkg/orchestrator/supervisor_orchestrator_test.go
git rm pkg/orchestrator/integration_supervisor_test.go
```

- [ ] **Step 4: Remove supervisor wiring from `orchestrator.go`**

Remove the import (in the `import (...)` block near the top of the file):

```go
	"github.com/akopichin/afm/pkg/orchestrator/supervisor"
```

Remove the `SupervisorRunner` field and its doc comment from the `Options` struct:

```go
	// SupervisorRunner — runner для вызовов Supervisor.EvaluateStage.
	// nil = Supervisor отключён глобально (DetermineStagePhases всегда вернёт базовые фазы).
	SupervisorRunner executor.Runner
```

Remove the `supervisor` field and its doc comment from the `Orchestrator` struct:

```go
	// supervisor оценивает, можно ли выполнить стадию автономно.
	// nil, если SupervisorRunner не задан в Options.
	supervisor *supervisor.Supervisor
```

In `New()`, remove this block:

```go
	// Supervisor включается только если задан SupervisorRunner; иначе
	// DetermineStagePhases всегда возвращает базовые фазы.
	var sup *supervisor.Supervisor
	if opts.SupervisorRunner != nil {
		sup = supervisor.NewSupervisor(opts.SupervisorRunner)
	}
```

And remove the `supervisor:     sup,` line from the `&Orchestrator{...}` struct literal, so it reads:

```go
	o := &Orchestrator{
		opts:           opts,
		graph:          graph.NewGraph(opts.Stages),
		runner:         r,
		critical:       critical,
		ui:             ui,
		fsm:            bus.NewFSM(opts.Store),
		concurrency:    conc,
		violationCache: make(map[string]violationCacheEntry),
		lastRootScan:   make(map[string]time.Time),
		maxRetries:     MaxRetries,
		retryBackoff:   RetryBackoff,
	}
```

- [ ] **Step 5: Update the two call sites in `scheduling.go` and `recovery.go`**

In `pkg/orchestrator/scheduling.go`, inside `startPlanningForUnblocked`, change:

```go
		o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
```

to:

```go
		o.concurrency.SpawnAgent(ctx, s, o.runPlanningAgent)
```

In `pkg/orchestrator/recovery.go`, there are two occurrences of the same line — one inside `startPlanningForPending`'s default case, one inside `resumePlanningStage`. Change BOTH occurrences of:

```go
			o.concurrency.SpawnAgent(ctx, s, o.startWithSupervisor)
```

(note: the one inside `startPlanningForPending` is indented one tab deeper than the one in `resumePlanningStage` — match indentation as it already exists in each spot) to:

```go
			o.concurrency.SpawnAgent(ctx, s, o.runPlanningAgent)
```

- [ ] **Step 6: Remove the FSM event and its transition-table row**

In `pkg/orchestrator/bus/fsm.go`, remove this line from the `FSMEvent` const block:

```go
	EvSupervisorApproved FSMEvent = "supervisor_approved"
```

Remove this line from the transition table:

```go
			EvSupervisorApproved: {From: []state.StageStatus{state.StatusPlanning}, To: to(state.StatusReady)},
```

In `pkg/orchestrator/bus/fsm_test.go`, remove this line from the table-driven test cases:

```go
		{"planning->ready(supervisor)", state.StatusPlanning, EvSupervisorApproved, state.StatusReady, true},
```

- [ ] **Step 7: Remove the bus event type**

In `pkg/orchestrator/bus/bus.go`, remove:

```go
	EventSupervisorDecision EventType = "supervisor_decision"
```

- [ ] **Step 8: Fix `scenario_harness_test.go` — remove the `Supervisor` scenario field and simplify `scriptedRunner`**

Remove the `Supervisor []byte` field from the `Scenario` struct:

```go
	Supervisor []byte
```

Update the doc comment above `scriptedRunner` (it currently reads):

```go
// scriptedRunner — единственный mock executor.Runner для всех сценариев.
// Поведение конфигурируется картой agents (stageID → AgentSpec) и supervisor
// (ответ RunJSONQuery). Модель поведения — supervisorTestRunner,
// rateLimitThenSuccessRunner, noDoneRunner из соседних *_test.go.
```

to:

```go
// scriptedRunner — единственный mock executor.Runner для всех сценариев.
// Поведение конфигурируется картой agents (stageID → AgentSpec). Модель
// поведения — rateLimitThenSuccessRunner, noDoneRunner из соседних *_test.go.
```

Remove the `supervisor []byte` field from the `scriptedRunner` struct:

```go
	agents     map[string]AgentSpec
	supervisor []byte
```

becomes:

```go
	agents map[string]AgentSpec
```

Change the constructor:

```go
func newScriptedRunner(agents map[string]AgentSpec, supervisor []byte) *scriptedRunner {
	return &scriptedRunner{agents: agents, supervisor: supervisor, calls: map[string]int{}}
}
```

to:

```go
func newScriptedRunner(agents map[string]AgentSpec) *scriptedRunner {
	return &scriptedRunner{agents: agents, calls: map[string]int{}}
}
```

Change `RunJSONQuery` (it's still required to satisfy the `executor.Runner` interface, just returns nothing meaningful now — nothing in production calls it anymore):

```go
func (r *scriptedRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return r.supervisor, nil
}
```

to:

```go
func (r *scriptedRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
```

Update the one call site inside `runScenario`:

```go
	runner := newScriptedRunner(sc.Agents, sc.Supervisor)
	orch := orchestrator.New(orchestrator.Options{
		RunDir:           runDir,
		Stages:           stages,
		Store:            store,
		Config:           config.Default(),
		Prompts:          orchestrator.DefaultPrompts(),
		Runner:           runner,
		SupervisorRunner: runner,
```

to:

```go
	runner := newScriptedRunner(sc.Agents)
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
```

(the alignment of the remaining fields' colons changes because `SupervisorRunner` was the longest field name in that literal — run `gofmt -w pkg/orchestrator/scenario_harness_test.go` after this edit to fix alignment rather than hand-aligning it).

- [ ] **Step 9: Remove the now-redundant `"supervisor-autonomous"` case from `scenario_test.go`**

In `pkg/orchestrator/scenario_test.go`'s `TestScenarios` table, remove this entire case (it tested the LLM-approved autonomous path; the very next case, `"auto-phase"`, already covers the exact same assertions — `autonomous.flag` + `execution_summary.md` present, `plan.md` absent — via the static `agents:[auto]` route that remains):

```go
		{
			Name: "supervisor-autonomous",
			Stages: []flow.Stage{
				{ID: "auto1", Supervisor: true, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
			},
			Supervisor: []byte(`{"can_execute_autonomously":true,"reason":"x","recommended_phases":["autonomous_execution"]}`),
			Agents:     map[string]AgentSpec{"auto1": {}},
			Expect: Expectation{
				Statuses:     map[string]state.StageStatus{"auto1": state.StatusDone},
				FilesPresent: map[string][]string{"auto1": {"autonomous.flag", "execution_summary.md"}},
				FilesAbsent:  map[string][]string{"auto1": {"plan.md"}},
			},
		},
```

- [ ] **Step 10: Update the two stale comment references in `scenario_harness_test.go`'s package doc comment**

Change:

```go
// Идея: вместо ручного написания mock-Runner + setup-кода для каждого нового
// теста (см. supervisorTestRunner, rateLimitThenSuccessRunner, noDoneRunner в
// integration_supervisor_test.go / integration_retry_test.go) — описать сценарий
```

to:

```go
// Идея: вместо ручного написания mock-Runner + setup-кода для каждого нового
// теста (см. rateLimitThenSuccessRunner, noDoneRunner в integration_retry_test.go)
// — описать сценарий
```

- [ ] **Step 11: Build and run the full orchestrator test suite**

```bash
gofmt -w pkg/orchestrator/orchestrator.go pkg/orchestrator/scheduling.go pkg/orchestrator/recovery.go \
  pkg/orchestrator/autonomous_flag.go pkg/orchestrator/scenario_harness_test.go pkg/orchestrator/scenario_test.go \
  pkg/orchestrator/bus/fsm.go pkg/orchestrator/bus/fsm_test.go pkg/orchestrator/bus/bus.go
go build ./...
go vet ./pkg/orchestrator/...
go test ./pkg/orchestrator/... -count=1
```

Expected: clean build, clean vet, all tests pass. If `scenario_test.go`'s `TestScenarios` fails on an unrelated case, stop and investigate before continuing — do not proceed with a red test suite.

- [ ] **Step 12: Commit**

```bash
git add -A -- pkg/orchestrator
git commit -m "убираем LLM-supervisor из orchestrator core: пакет supervisor, supervisor_track.go, FSM/bus события, call sites"
```

---

## Task 2: Remove supervisor fields from `pkg/flow`

**Files:**
- Modify: `pkg/flow/flow.go`
- Modify: `pkg/flow/flow_test.go`
- Modify: `pkg/flow/auto_test.go`
- Modify: `pkg/flow/marshal_test.go`

**Interfaces:**
- Produces: `flow.Stage` with no `Supervisor`/`SupervisorPrompt` fields; `flow.Flow` with no `SupervisorCommand` field. `flow.ParseFile` behavior on an old flow.yaml with these keys: parses successfully, `Stage.Supervisor`/`SupervisorPrompt` and `Flow.SupervisorCommand` simply don't exist to hold the value (no warning yet — that's Task 3).
- Consumes: nothing from Task 1 (independent package).

- [ ] **Step 1: Remove the `Supervisor`/`SupervisorPrompt` fields from `Stage`**

In `pkg/flow/flow.go`, change:

```go
	// Supervisor включает оценку стадии агентом-супервизором перед запуском.
	// Стадия обязана содержать AgentPlanning в Agents.
	Supervisor       bool   `yaml:"supervisor,omitempty"`
	SupervisorPrompt string `yaml:"supervisor_prompt,omitempty"`
	// Script, if set, makes this a script-only stage: it runs the given shell
	// script (via sh -c) instead of any agent, with no planning/supervisor/
	// approval gate. Mutually exclusive with Agents/Command/Interactive/Plan/
	// Verify/Supervisor.
	Script        string        `yaml:"script,omitempty"`
```

to:

```go
	// Script, if set, makes this a script-only stage: it runs the given shell
	// script (via sh -c) instead of any agent, with no planning/approval gate.
	// Mutually exclusive with Agents/Command/Interactive/Plan/Verify.
	Script        string        `yaml:"script,omitempty"`
```

- [ ] **Step 2: Remove `Flow.SupervisorCommand`**

Change:

```go
	// SupervisorCommand задаёт команду для агента-супервизора (как command у стадии).
	// Default: значение из config.Supervisor.Command или config.Client.Command.
	SupervisorCommand string  `yaml:"supervisor_command,omitempty"`
	Stages            []Stage `yaml:"stages"`
```

to:

```go
	Stages []Stage `yaml:"stages"`
```

- [ ] **Step 3: Remove the `auto`-incompatible-with-`supervisor` validation check**

Change:

```go
		if len(s.Agents) != 1 {
			return fmt.Errorf("stage %q: \"auto\" must be the only agent", s.ID)
		}
		if s.Supervisor {
			return fmt.Errorf("stage %q: \"auto\" is incompatible with supervisor: true", s.ID)
		}
	}
```

to:

```go
		if len(s.Agents) != 1 {
			return fmt.Errorf("stage %q: \"auto\" must be the only agent", s.ID)
		}
	}
```

- [ ] **Step 4: Remove the `script`-incompatible-with-`supervisor` validation check**

Change:

```go
		if s.Verify != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with verify", s.ID)
		}
		if s.Supervisor {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with supervisor", s.ID)
		}
	}
```

to:

```go
		if s.Verify != "" {
			return fmt.Errorf("stage %q: \"script\" cannot be combined with verify", s.ID)
		}
	}
```

- [ ] **Step 5: Delete `TestFlow_SupervisorFields` from `flow_test.go`**

Remove this entire test function:

```go
func TestFlow_SupervisorFields(t *testing.T) {
	yaml := `
name: test
supervisor_command: glm51
stages:
  - id: s1
    description: do something
    supervisor: true
    supervisor_prompt: "extra hint"
    agents: [planning, implementation]
    skills: [goga:apply]
`
	f, err := flow.ParseFile(writeTempYAML(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if f.SupervisorCommand != "glm51" {
		t.Errorf("got SupervisorCommand=%q, want glm51", f.SupervisorCommand)
	}
	s := f.Stages[0]
	if !s.Supervisor {
		t.Error("expected Supervisor=true")
	}
	if s.SupervisorPrompt != "extra hint" {
		t.Errorf("got SupervisorPrompt=%q, want 'extra hint'", s.SupervisorPrompt)
	}
}
```

(if the function has more assertions after this in the original file, remove through its closing `}` — check the file for the true end of the function before deleting.)

- [ ] **Step 6: Delete `TestParse_AutoIncompatibleWithSupervisor` from `auto_test.go`**

Remove:

```go
func TestParse_AutoIncompatibleWithSupervisor(t *testing.T) {
	_, err := writeFlow(t, `
name: f
stages:
  - id: a
    agents: [auto]
    supervisor: true
`)
	if err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("auto+supervisor: want 'supervisor' error, got %v", err)
	}
}
```

- [ ] **Step 7: Remove `"supervisor:"` from the zero-value field list in `marshal_test.go`**

Change:

```go
	for _, unwanted := range []string{
		"command:", "verify:", "interactive:", "script:", "max_parallel:",
		"supervisor:", "auto_approve:", "eager_planning:", "plan:",
		"description:",
	} {
```

to:

```go
	for _, unwanted := range []string{
		"command:", "verify:", "interactive:", "script:", "max_parallel:",
		"auto_approve:", "eager_planning:", "plan:",
		"description:",
	} {
```

- [ ] **Step 8: Build and test**

```bash
go build ./...
go vet ./pkg/flow/...
go test ./pkg/flow/... -count=1
```

Expected: clean build/vet, all tests pass.

- [ ] **Step 9: Commit**

```bash
git add -A -- pkg/flow
git commit -m "убираем supervisor/supervisor_prompt/supervisor_command из flow.yaml схемы"
```

---

## Task 3: Add a `ParseFile` warning for deprecated supervisor keys

TDD: write the failing test first, then implement.

**Files:**
- Modify: `pkg/flow/flow.go`
- Modify: `pkg/flow/flow_test.go`

**Interfaces:**
- Consumes: `flow.ParseFile(path string) (*Flow, error)` (existing, from Task 2's state — fields already removed).
- Produces: nothing new consumed by later tasks — this is a leaf behavior.

- [ ] **Step 1: Write the failing test**

Add to `pkg/flow/flow_test.go` (the struct no longer having `Supervisor`/`SupervisorPrompt`/`SupervisorCommand` fields is already guaranteed by Task 2 at compile time, so this test only needs to assert on the printed warning, not on the struct):

```go
func TestParseFile_WarnsOnDeprecatedSupervisorKeys(t *testing.T) {
	yaml := `
name: test
supervisor_command: glm51
stages:
  - id: s1
    description: do something
    supervisor: true
    supervisor_prompt: "extra hint"
    agents: [planning, implementation]
`
	path := writeTempYAML(t, yaml)

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	_, parseErr := flow.ParseFile(path)

	os.Stderr = origStderr
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	stderr := buf.String()

	if parseErr != nil {
		t.Fatalf("ParseFile should succeed despite deprecated keys, got: %v", parseErr)
	}
	if !strings.Contains(stderr, "WARN") || !strings.Contains(stderr, "supervisor_command") {
		t.Errorf("expected a WARN about supervisor_command, got stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "s1") || !strings.Contains(stderr, "supervisor") {
		t.Errorf("expected a WARN naming stage s1 and the supervisor key, got stderr: %q", stderr)
	}
}
```

Check the top of `pkg/flow/flow_test.go` for its existing `import (...)` block — add `"bytes"` and `"os"` to it if not already present (the file already parses YAML from temp files via `writeTempYAML`, so `os` is very likely already imported; `bytes` likely is not).

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./pkg/flow/ -run TestParseFile_WarnsOnDeprecatedSupervisorKeys -v
```

Expected: FAIL (no "WARN" is ever printed today — stderr is empty).

- [ ] **Step 3: Implement the warning in `flow.go`**

Add this function to `pkg/flow/flow.go` (near `ParseFile`, e.g. directly below it):

```go
// warnDeprecatedSupervisorFields best-effort re-parses the raw YAML looking
// for the removed LLM-supervisor keys (supervisor/supervisor_prompt on a
// stage; supervisor_command at the flow root) and prints a non-fatal WARN to
// stderr naming the affected stage id and key. The LLM supervisor was
// removed (agents: [auto] replaces it for autonomous stages); ParseFile's
// primary decode silently drops these now-unknown keys (yaml.Unmarshal isn't
// KnownFields-strict), so a flow.yaml built for the old supervisor keeps
// working exactly as it does today when the supervisor is unconfigured —
// this only makes that behavior change visible instead of silent.
func warnDeprecatedSupervisorFields(data []byte, path string) {
	var probe struct {
		SupervisorCommand *string `yaml:"supervisor_command"`
		Stages            []struct {
			ID               string  `yaml:"id"`
			Supervisor       *bool   `yaml:"supervisor"`
			SupervisorPrompt *string `yaml:"supervisor_prompt"`
		} `yaml:"stages"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return
	}
	if probe.SupervisorCommand != nil {
		fmt.Fprintf(os.Stderr, "WARN: %s: flow-level \"supervisor_command\" is no longer supported and is ignored (the LLM supervisor was removed; use \"agents: [auto]\" for autonomous stages)\n", path)
	}
	for _, s := range probe.Stages {
		if s.Supervisor != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s: stage %q: \"supervisor\" is no longer supported and is ignored (use \"agents: [auto]\" for autonomous stages)\n", path, s.ID)
		}
		if s.SupervisorPrompt != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s: stage %q: \"supervisor_prompt\" is no longer supported and is ignored\n", path, s.ID)
		}
	}
}
```

Call it from `ParseFile`, right after the existing validate+defaults calls:

```go
func ParseFile(path string) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	var f Flow
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	f.applyScriptTimeoutDefaults()
	warnDeprecatedSupervisorFields(data, path)
	return &f, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./pkg/flow/ -run TestParseFile_WarnsOnDeprecatedSupervisorKeys -v
```

Expected: PASS.

- [ ] **Step 5: Run the full flow package suite**

```bash
go build ./...
go vet ./pkg/flow/...
go test ./pkg/flow/... -count=1
```

Expected: clean build/vet, all tests pass (including the ones from Task 2).

- [ ] **Step 6: Commit**

```bash
git add -A -- pkg/flow
git commit -m "добавляем warning в ParseFile на устаревшие supervisor-ключи в flow.yaml"
```

---

## Task 4: Remove `SupervisorConfig` from `pkg/config`

**Files:**
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/config_test.go`

**Interfaces:**
- Produces: `config.Config` with no `Supervisor` field; `config.SupervisorConfig` type no longer exists.
- Consumes: nothing from other tasks (independent package).

- [ ] **Step 1: Remove the `SupervisorConfig` type**

In `pkg/config/config.go`, remove:

```go
// SupervisorConfig настраивает агента-супервизора.
type SupervisorConfig struct {
	Command string `yaml:"command"`
}

```

- [ ] **Step 2: Remove the `Supervisor` field from `Config`**

Change:

```go
type Config struct {
	Client     ClientConfig     `yaml:"client"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Server     ServerConfig     `yaml:"server"`
	Docker     DockerConfig     `yaml:"docker"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	PromptsDir string           `yaml:"prompts_dir"`
	Theme      string           `yaml:"theme"`
	SkinDir    string           `yaml:"skin_dir"`
```

to:

```go
type Config struct {
	Client     ClientConfig   `yaml:"client"`
	Executor   ExecutorConfig `yaml:"executor"`
	Server     ServerConfig   `yaml:"server"`
	Docker     DockerConfig   `yaml:"docker"`
	PromptsDir string         `yaml:"prompts_dir"`
	Theme      string         `yaml:"theme"`
	SkinDir    string         `yaml:"skin_dir"`
```

(the struct tag column alignment shrinks now that `SupervisorConfig` — the longest type name — is gone; run `gofmt -w pkg/config/config.go` after this edit rather than hand-aligning it).

- [ ] **Step 3: Remove the merge logic**

Change:

```go
	if overlay.Docker.Agents != nil {
		if dst.Docker.Agents == nil {
			dst.Docker.Agents = map[string]AgentRecipe{}
		}
		for k, v := range overlay.Docker.Agents {
			dst.Docker.Agents[k] = v // per-key overlay: проектный слой дополняет/переопределяет глобальный
		}
	}
	if overlay.Supervisor.Command != "" {
		dst.Supervisor.Command = overlay.Supervisor.Command
	}
	if overlay.AutoRecover != nil {
```

to:

```go
	if overlay.Docker.Agents != nil {
		if dst.Docker.Agents == nil {
			dst.Docker.Agents = map[string]AgentRecipe{}
		}
		for k, v := range overlay.Docker.Agents {
			dst.Docker.Agents[k] = v // per-key overlay: проектный слой дополняет/переопределяет глобальный
		}
	}
	if overlay.AutoRecover != nil {
```

- [ ] **Step 4: Delete `TestConfig_SupervisorMerge`**

Remove this entire test function (and the blank line immediately after its closing `}`) from `pkg/config/config_test.go`:

```go
func TestConfig_SupervisorMerge(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte("supervisor:\n  command: glm51\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFrom("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.Command != "glm51" {
		t.Errorf("got %q, want glm51", cfg.Supervisor.Command)
	}
}

```

- [ ] **Step 5: Build and test**

```bash
gofmt -w pkg/config/config.go
go build ./...
go vet ./pkg/config/...
go test ./pkg/config/... -count=1
```

Expected: clean build/vet, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add -A -- pkg/config
git commit -m "убираем SupervisorConfig из config.yaml схемы"
```

---

## Task 5: Remove supervisor wiring from `cmd/afm/run.go`

Depends on Tasks 1, 2, and 4 (`Options.SupervisorRunner`, `flow.Flow.SupervisorCommand`, `config.Config.Supervisor` must already be gone for this task's edits to compile against a consistent picture — though in practice each removed reference here simply won't exist to reference anymore, causing a compile error until this task's edits land too).

**Files:**
- Modify: `cmd/afm/run.go`

**Interfaces:**
- Consumes: `pkg/orchestrator.Options` (no `SupervisorRunner` field, per Task 1), `flow.Flow` (no `SupervisorCommand` field, per Task 2), `config.Config` (no `Supervisor` field, per Task 4).
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Remove the `executor` import**

`executor.New(...)` for the supervisor runner (removed in Step 3 below) is the ONLY use of the `executor` package in this file. Remove this line from the `import (...)` block:

```go
	"github.com/akopichin/afm/pkg/executor"
```

- [ ] **Step 2: Simplify the recipe-collection block (removes the supervisor-command recipe lookup)**

Change:

```go
				recipes = docker.UsedRecipes(f, cfg.Client.Command, cfg.Docker.Agents)
				// supervisor_command может быть recipe-агентом вне stage-команд
				// (иначе в контейнере враппер supervisor'а
				// не получит токен → RunJSONQuery упадёт).
				if supCmd := resolveSupervisorCommand(f, cfg); supCmd != "" {
					if recipe, isRecipe := cfg.Docker.Agents[supCmd]; isRecipe {
						recipes[supCmd] = recipe
					}
				}
				// generatedForMount — то же самое множество ключей, чтобы
```

to:

```go
				recipes = docker.UsedRecipes(f, cfg.Client.Command, cfg.Docker.Agents)
				// generatedForMount — то же самое множество ключей, чтобы
```

- [ ] **Step 3: Remove the supervisor command resolution, wrapper, and runner construction**

Change:

```go
			// Единый wrapper-dir: generated-врапперы (autoShim, только внутри
			// контейнера). На хосте врапперы не генерируются — реальные бинарники
			// используются напрямую.
			// Resolve supervisor command (см. resolveSupervisorCommand).
			// Резолвим ДО генерации wrappers: враппер supervisor_command тоже нужен, даже
			// если ни одна стадия эту команду не использует (иначе RunJSONQuery не найдёт
			// бинарник в контейнере → вечный fallback супервизора).
			supervisorCmd := resolveSupervisorCommand(f, cfg)

			var wrapperSpecs []docker.WrapperSpec
			generatedAgents := map[string]bool{}
			if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
				if err := cfg.Docker.ValidateAgents(); err != nil {
					return err
				}
				used := docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents)
				if _, isRecipe := cfg.Docker.Agents[supervisorCmd]; isRecipe {
					used[supervisorCmd] = true // supervisor_command может не быть среди stage-команд
				}
				for cmd := range used {
					generatedAgents[cmd] = true
					wrapperSpecs = append(wrapperSpecs, buildWrapperSpec(cmd, cfg.Docker.Agents[cmd], cfg.Client.IsClaudeBare()))
				}
			}
			var wrapperDir string
			if len(wrapperSpecs) > 0 {
				wd, err := docker.CreateWrappers(wrapperSpecs)
				if err != nil {
					return fmt.Errorf("create wrappers: %w", err)
				}
				wrapperDir = wd
				defer os.RemoveAll(wd) //nolint:errcheck
			}

			supervisorWrapperDir := ""
			if generatedAgents[supervisorCmd] {
				supervisorWrapperDir = wrapperDir
			}
			supervisorRunner := executor.New(executor.Config{
				Command:    supervisorCmd,
				WrapperDir: supervisorWrapperDir,
				Debug:      debugEnabled,
				RunDir:     runDir,
			})

```

to:

```go
			// Единый wrapper-dir: generated-врапперы (autoShim, только внутри
			// контейнера). На хосте врапперы не генерируются — реальные бинарники
			// используются напрямую.
			var wrapperSpecs []docker.WrapperSpec
			generatedAgents := map[string]bool{}
			if os.Getenv("AFM_IN_DOCKER") == "1" && cfg.Docker.IsAutoShim() {
				if err := cfg.Docker.ValidateAgents(); err != nil {
					return err
				}
				used := docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents)
				for cmd := range used {
					generatedAgents[cmd] = true
					wrapperSpecs = append(wrapperSpecs, buildWrapperSpec(cmd, cfg.Docker.Agents[cmd], cfg.Client.IsClaudeBare()))
				}
			}
			var wrapperDir string
			if len(wrapperSpecs) > 0 {
				wd, err := docker.CreateWrappers(wrapperSpecs)
				if err != nil {
					return fmt.Errorf("create wrappers: %w", err)
				}
				wrapperDir = wd
				defer os.RemoveAll(wd) //nolint:errcheck
			}

```

- [ ] **Step 4: Remove `SupervisorRunner` from the `orchestrator.Options` literal**

Change:

```go
			orch := orchestrator.New(orchestrator.Options{
				RunDir:           runDir,
				Stages:           f.Stages,
				Store:            store,
				Config:           cfg,
				Prompts:          prompts,
				WrapperDir:       wrapperDir,
				GeneratedAgents:  generatedAgents,
				GlobalPrompt:     f.Prompt,
				RootDir:          agentRootDir,
				RequireApproval:  requireApproval,
				Debug:            debugEnabled,
				SupervisorRunner: supervisorRunner,
			})
```

to:

```go
			orch := orchestrator.New(orchestrator.Options{
				RunDir:          runDir,
				Stages:          f.Stages,
				Store:           store,
				Config:          cfg,
				Prompts:         prompts,
				WrapperDir:      wrapperDir,
				GeneratedAgents: generatedAgents,
				GlobalPrompt:    f.Prompt,
				RootDir:         agentRootDir,
				RequireApproval: requireApproval,
				Debug:           debugEnabled,
			})
```

- [ ] **Step 5: Delete `resolveSupervisorCommand`**

Remove this function and its doc comment entirely:

```go
// resolveSupervisorCommand определяет команду супервизора с приоритетом:
// flow.supervisor_command > config.supervisor.command > config.client.command.
// Используется и в host-ветке (резолв секрета recipe), и в container-ветке
// (генерация wrapper'а) — чтобы supervisor-only команда (не у stage) тоже получала
// и секрет, и wrapper.
func resolveSupervisorCommand(f *flow.Flow, cfg config.Config) string {
	if f != nil && f.SupervisorCommand != "" {
		return f.SupervisorCommand
	}
	if cfg.Supervisor.Command != "" {
		return cfg.Supervisor.Command
	}
	return cfg.Client.Command
}

```

(this leaves `buildWrapperSpec`'s own doc comment, currently positioned directly above `resolveSupervisorCommand`'s doc comment, correctly adjacent to `func buildWrapperSpec(...)` with no gap).

- [ ] **Step 6: Build**

```bash
gofmt -w cmd/afm/run.go
go build ./...
go vet ./cmd/afm/...
```

Expected: clean build/vet. There is no `cmd/afm/*_test.go` file referencing anything removed here (verified during planning), so no test file changes are needed in this task.

- [ ] **Step 7: Run the full afm test suite so far**

```bash
go test ./... -count=1
```

Expected: all pass (this exercises everything from Tasks 1–5 together for the first time).

- [ ] **Step 8: Commit**

```bash
git add -A -- cmd/afm
git commit -m "убираем resolveSupervisorCommand и supervisorRunner из cmd/afm/run.go"
```

---

## Task 6: Remove the supervisor read path from `pkg/server`

**Files:**
- Modify: `pkg/server/events_handler.go`
- Modify: `pkg/server/handlers.go`
- Modify: `pkg/server/server.go`

**Interfaces:**
- Consumes: nothing from other Go tasks (this reads `supervisor.jsonl` as a plain file by name, not via any Go type from the orchestrator/bus packages — verified during planning: no `pkg/server/*_test.go` file references supervisor at all).
- Produces: `GET /api/stages/<id>/supervisor` no longer exists (404 via the default not-found path, same as any other unknown suffix). `/api/events` / `/ws` no longer emit `supervisor_decision` entries for old runs.

- [ ] **Step 1: Remove `reconstructSupervisorDecisions` and `autonomousLabel` from `events_handler.go`**

Remove:

```go
// autonomousLabel — значение supervisor-решения (Task 3,
// logSupervisorDecision track="autonomous") в can_execute_autonomously.
// Не связано с flow.PhaseAutonomous ("autonomous_execution") — это
// отдельный, случайно совпадающий по подстроке "autonomous" словарь
// (supervisor-track "standard"/"autonomous", а не имя фазы).
const autonomousLabel = "autonomous"

```

Remove this line from `reconstructEventHistory`:

```go
	out = append(out, reconstructSupervisorDecisions(s.runDir)...)
```

Remove the function itself:

```go
// reconstructSupervisorDecisions читает run-level supervisor.jsonl
// (пишется logSupervisorDecision, pkg/orchestrator/supervisor_track.go).
func reconstructSupervisorDecisions(runDir string) []feedEvent {
	path := filepath.Join(runDir, "supervisor.jsonl")
	var out []feedEvent
	for _, line := range readLines(path) {
		var e struct {
			Ts       string `json:"ts"`
			StageID  string `json:"stage_id"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.Ts)
		if err != nil {
			continue
		}
		out = append(out, feedEvent{
			Type: "supervisor_decision", StageID: e.StageID,
			Data:      map[string]any{"can_execute_autonomously": e.Decision == autonomousLabel, "reason": e.Reason},
			Timestamp: ts,
		})
	}
	return out
}

```

Update the now-stale doc comment on `feedEvent` (it lists supervisor_decision as an example of a Seq-less event type):

Change:

```go
// feedEvent — одна запись реплея истории ленты событий. Seq заполняется
// только для событий, производных от реальной FSM-transition (events.jsonl) —
// это стабильный ключ дедупликации на фронте при слиянии с live-потоком
// WebSocket. Для остальных типов (agent_action/supervisor_decision/notices)
// Seq остаётся нулевым.
```

to:

```go
// feedEvent — одна запись реплея истории ленты событий. Seq заполняется
// только для событий, производных от реальной FSM-transition (events.jsonl) —
// это стабильный ключ дедупликации на фронте при слиянии с live-потоком
// WebSocket. Для остальных типов (agent_action/notices) Seq остаётся нулевым.
```

- [ ] **Step 2: Remove `handleSupervisor` from `handlers.go`**

Remove:

```go
// handleSupervisor возвращает последнее решение супервизора для стадии
// (читает <runDir>/supervisor.jsonl). Даёт UI показать резолюцию
// (autonomous/standard + reason) персистентно: событие шины EventSupervisorDecision
// live-only и теряется, если дашборд подключился после старта стадии.
func (s *Server) handleSupervisor(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/supervisor")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.runDir, "supervisor.jsonl"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var latest map[string]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]string
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["stage_id"] == stageID {
			latest = entry
		}
	}
	if latest == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(latest)
}

```

- [ ] **Step 3: Remove the route in `server.go`**

Change:

```go
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/supervisor"):
		s.handleSupervisor(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
```

to:

```go
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
```

- [ ] **Step 4: Build and test**

```bash
go build ./...
go vet ./pkg/server/...
go test ./pkg/server/... -count=1
```

Expected: clean build/vet, all tests pass (no test file needed changes, per the Interfaces note above — this step confirms that).

- [ ] **Step 5: Commit**

```bash
git add -A -- pkg/server
git commit -m "убираем чтение supervisor.jsonl и /api/stages/<id>/supervisor из pkg/server"
```

---

## Task 7: Remove the supervisor UI from the dashboard frontend

**Files:**
- Delete: `pkg/web/dashboard/src/components/supervisor-decision/` (whole directory: `SupervisorDecision.tsx`, `SupervisorDecision.test.tsx`, `index.ts`)
- Modify: `pkg/web/dashboard/src/app/App.tsx`
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.tsx`
- Modify: `pkg/web/dashboard/src/components/event-feed/EventFeedPanel.test.tsx`
- Modify: `pkg/web/dashboard/src/types/afm-event.ts`
- Modify: `pkg/web/dashboard/src/types/afm-event.test.ts`
- Modify: `pkg/web/dashboard/src/hooks/use-event-feed/use-event-feed.ts`
- Modify: `pkg/web/dashboard/skins/base/event-feed.css`
- Modify: `pkg/web/dashboard/skins/coffee/index.css`
- Modify: `pkg/web/dashboard/skins/goga/index.css`
- Modify: `pkg/web/dashboard/skins/novacorps/index.css`

**Interfaces:**
- Consumes: nothing from the Go tasks (frontend fetches `/api/stages/<id>/supervisor`, which now 404s if ever called — but Step 2 below removes the only caller).
- Produces: nothing consumed elsewhere — this is the last code-touching task.

- [ ] **Step 1: Delete the component directory**

```bash
git rm -r pkg/web/dashboard/src/components/supervisor-decision
```

- [ ] **Step 2: Remove the import and usage in `App.tsx`**

Remove this import line:

```tsx
import { SupervisorDecision } from '../components/supervisor-decision'
```

Remove this line (it sits inside a `<span>` alongside a "thinking" indicator — remove only this one line, keep the surrounding `<span className="status-badge-wrap">...</span>` and its other children):

```tsx
                    {selectedStageId != null && <SupervisorDecision stageId={selectedStageId} />}
```

- [ ] **Step 3: Remove the `supervisor_decision` case from `EventFeedPanel.tsx`**

Change:

```tsx
    case 'auto_answered': {
      const obj = isRecord(data) ? data : {}
      const id = typeof obj.id === 'string' ? obj.id : ''
      const answer = typeof obj.answer === 'string' ? obj.answer : ''
      msg = `auto-answered ${id}: ${answer}`
      msgClass = 'feed-msg action'
      break
    }
    case 'supervisor_decision': {
      const obj = isRecord(data) ? data : {}
      const autonomous = obj.can_execute_autonomously === true
      const reason = typeof obj.reason === 'string' ? obj.reason : ''
      msg = `supervisor: ${autonomous ? 'autonomous' : 'standard'}${reason !== '' ? ` — ${reason}` : ''}`
      msgClass = 'feed-msg supervisor'
      entryClass = 'supervisor'
      break
    }
    case 'context_warning':
```

to:

```tsx
    case 'auto_answered': {
      const obj = isRecord(data) ? data : {}
      const id = typeof obj.id === 'string' ? obj.id : ''
      const answer = typeof obj.answer === 'string' ? obj.answer : ''
      msg = `auto-answered ${id}: ${answer}`
      msgClass = 'feed-msg action'
      break
    }
    case 'context_warning':
```

- [ ] **Step 4: Update `EventFeedPanel.test.tsx`**

Change:

```tsx
  test('renders representative feed lines for known event types and falls back to type for unknown ones', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'src/x.ts' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
      {
        type: 'supervisor_decision',
        payload: { can_execute_autonomously: true, reason: 'looks safe' },
        stageId: 's2',
        timestamp: '2026-07-10T10:00:02Z',
      },
      { type: 'custom_unknown_type', payload: null, stageId: '', timestamp: '2026-07-10T10:00:03Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(4)

    expect(entries[0]?.textContent).toContain('→ running')
    expect(entries[1]?.textContent).toContain('read_file: src/x.ts')
    expect(entries[2]?.textContent).toContain('supervisor: autonomous — looks safe')
    expect(entries[2]).toHaveClass('supervisor')
    expect(entries[3]?.textContent).toContain('custom_unknown_type')
  })
```

to:

```tsx
  test('renders representative feed lines for known event types and falls back to type for unknown ones', () => {
    const events: AfmEvent[] = [
      { type: 'stage_status_changed', payload: 'running', stageId: 's1', timestamp: '2026-07-10T10:00:00Z' },
      { type: 'agent_action', payload: { tool: 'read_file', detail: 'src/x.ts' }, stageId: '', timestamp: '2026-07-10T10:00:01Z' },
      { type: 'custom_unknown_type', payload: null, stageId: '', timestamp: '2026-07-10T10:00:03Z' },
    ]

    const { container } = render(<EventFeedPanel events={events} logEntries={[]} />)

    const entries = container.querySelectorAll('.feed-entry')
    expect(entries).toHaveLength(3)

    expect(entries[0]?.textContent).toContain('→ running')
    expect(entries[1]?.textContent).toContain('read_file: src/x.ts')
    expect(entries[2]?.textContent).toContain('custom_unknown_type')
  })
```

- [ ] **Step 5: Remove `'supervisor_decision'` from the event-type union in `afm-event.ts`**

Change:

```ts
export const AFM_EVENT_TYPES = [
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_action',
  'agent_completed',
  'supervisor_decision',
  'script_output',
  'hook_failed',
  'hook_resolved',
  'auto_answered',
] as const
```

to:

```ts
export const AFM_EVENT_TYPES = [
  'stage_status_changed',
  'approved',
  'revised',
  'retry_scheduled',
  'retry_exhausted',
  'manual_retry',
  'ask_user',
  'user_answered',
  'agent_action',
  'agent_completed',
  'script_output',
  'hook_failed',
  'hook_resolved',
  'auto_answered',
] as const
```

- [ ] **Step 6: Update `afm-event.test.ts`**

Change:

```ts
    expect(AFM_EVENT_TYPES).toEqual([
      'stage_status_changed',
      'approved',
      'revised',
      'retry_scheduled',
      'retry_exhausted',
      'manual_retry',
      'ask_user',
      'user_answered',
      'agent_action',
      'agent_completed',
      'supervisor_decision',
      'script_output',
      'hook_failed',
      'hook_resolved',
      'auto_answered',
    ])
```

to:

```ts
    expect(AFM_EVENT_TYPES).toEqual([
      'stage_status_changed',
      'approved',
      'revised',
      'retry_scheduled',
      'retry_exhausted',
      'manual_retry',
      'ask_user',
      'user_answered',
      'agent_action',
      'agent_completed',
      'script_output',
      'hook_failed',
      'hook_resolved',
      'auto_answered',
    ])
```

- [ ] **Step 7: Update the stale comment in `use-event-feed.ts`**

Find the comment mentioning `supervisor_decision` (around the mid-file discussion of which event types never carry a `seq`) and remove `supervisor_decision` from that list, keeping the rest of the sentence intact — e.g. if it currently reads something like `agent_completed, context_warning, supervisor_decision) seq не приходит ни`, change it to `agent_completed, context_warning) seq не приходит ни` (read the exact surrounding sentence in the file first so the edit stays grammatically correct in context).

- [ ] **Step 8: Clean up `pkg/web/dashboard/skins/base/event-feed.css`**

Replace the entire file content with the following (this drops the supervisor-only rule blocks — `.feed-entry.supervisor*`, `.feed-msg.supervisor`, `.supervisor-decision`, `.supervisor-dot*`, `.supervisor-popover*` — while keeping `.status-badge-wrap` intact, since it's also the positioning container for the unrelated "thinking" indicator in `App.tsx`):

```css
/* pkg/web/dashboard/public/skins/base/event-feed.css
   Live event feed. */

#feed-content {
  font-size: 11px;
  line-height: 1.5;
}

.feed-entry {
  display: grid;
  grid-template-columns: 38px 1fr;
  gap: 8px;
  padding: 5px 0;
  border-bottom: 1px solid rgba(111, 212, 204, 0.05);
  word-break: break-word;
}
.feed-entry:hover {
  background: rgba(111, 212, 204, 0.05);
}

.feed-time {
  color: var(--ink-dim);
  font-size: 10px;
  text-align: right;
  padding-right: 2px;
  letter-spacing: 0.05em;
}

.feed-msg {
  color: var(--ink);
  font-size: 11px;
  line-height: 1.5;
}
.feed-msg.action { color: var(--ink-dim); }
.feed-msg.error  { color: var(--coral); }
.feed-msg.warning { color: var(--amber); }

.status-badge-wrap {
  position: relative;
  display: inline-flex;
}

.feed-stage-badge {
  display: inline;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.18em;
  text-transform: uppercase;
  color: var(--amber);
  margin-right: 6px;
}
.feed-stage-badge::after {
  content: " ·";
  color: var(--ink-dim);
  margin: 0 4px 0 2px;
}

.feed-stage-badge.status-pending             { color: var(--c-pending); }
.feed-stage-badge.status-planning            { color: var(--c-planning); }
.feed-stage-badge.status-awaiting_approval   { color: var(--c-awaiting); }
.feed-stage-badge.status-revising            { color: var(--c-revising); }
.feed-stage-badge.status-ready               { color: var(--c-ready); }
.feed-stage-badge.status-running             { color: var(--c-running); }
.feed-stage-badge.status-done                { color: var(--c-done); }
.feed-stage-badge.status-failed              { color: var(--c-failed); }
.feed-stage-badge.status-retrying            { color: var(--c-retrying); }
.feed-stage-badge.status-awaiting_user_input { color: var(--c-awaiting-user); }
.feed-stage-badge.status-hook_failed         { color: var(--c-hook-failed); }
.feed-stage-badge.status-paused              { color: var(--c-paused); }

/* A2 — вход новых строк ленты (только новые монтируются → анимируются) */
.feed-entry { animation: feedIn 0.25s ease; }
@keyframes feedIn {
  from { opacity: 0; transform: translateY(-6px); }
  to   { opacity: 1; transform: none; }
}
```

- [ ] **Step 9: Remove the `--supervisor-accent` variable from `skins/coffee/index.css`**

There are two occurrences (dark theme block and light theme block). In each, change the comment line and remove the variable — e.g. the dark-theme block:

```css
  /* dialog/supervisor accents */
  --qa-answer:              #d8b98a;
  --dialog-option-text:     #c2d6a6;
  --btn-cancel-dialog-text: #ff8a5a;
  --supervisor-accent:      #f0b45e;
```

becomes:

```css
  /* dialog accents */
  --qa-answer:              #d8b98a;
  --dialog-option-text:     #c2d6a6;
  --btn-cancel-dialog-text: #ff8a5a;
```

and the light-theme block:

```css
  /* dialog/supervisor accents */
  --qa-answer:              #7a5320;
  --dialog-option-text:     #5e7f3c;
  --btn-cancel-dialog-text: #b23a1a;
  --supervisor-accent:      #9a6a1e;
```

becomes:

```css
  /* dialog accents */
  --qa-answer:              #7a5320;
  --dialog-option-text:     #5e7f3c;
  --btn-cancel-dialog-text: #b23a1a;
```

- [ ] **Step 10: Remove the `--supervisor-accent` variable from `skins/goga/index.css`**

Dark-theme block — change:

```css
  /* dialog/supervisor accents — goga их не переопределял и сегодня, сохраняем
     дословные значения novacorps (текущее поведение — унаследованные через
     старый @import — не меняется) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
  --supervisor-accent:      #c084fc;
```

to:

```css
  /* dialog accents — goga их не переопределял и сегодня, сохраняем
     дословные значения novacorps (текущее поведение — унаследованные через
     старый @import — не меняется) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
```

Light-theme block — change:

```css
  /* dialog/supervisor accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #2563eb;
  --btn-cancel-dialog-text: #b91c1c;
  --supervisor-accent:      #7e22ce;
```

to:

```css
  /* dialog accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #2563eb;
  --btn-cancel-dialog-text: #b91c1c;
```

- [ ] **Step 11: Remove the `--supervisor-accent` variable from `skins/novacorps/index.css`**

Dark-theme block — change:

```css
  /* dialog/supervisor accents (dark-mode-tuned pale hues, как раньше) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
  --supervisor-accent:      #c084fc;
```

to:

```css
  /* dialog accents (dark-mode-tuned pale hues, как раньше) */
  --qa-answer:              #9fe0d8;
  --dialog-option-text:     #d4baff;
  --btn-cancel-dialog-text: #ff8a7a;
```

Light-theme block — change:

```css
  /* dialog/supervisor accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #6d28d9;
  --btn-cancel-dialog-text: #b91c1c;
  --supervisor-accent:      #9333ea;
```

to:

```css
  /* dialog accents — переподобраны под светлый фон */
  --qa-answer:              #0f766e;
  --dialog-option-text:     #6d28d9;
  --btn-cancel-dialog-text: #b91c1c;
```

- [ ] **Step 12: Run the dashboard test suite and build**

```bash
cd pkg/web/dashboard
npm test -- --run
npm run build
cd ../../..
```

Expected: all tests pass, build succeeds with no TypeScript errors (an unused import or unused `entryClass`-only-set-by-removed-branch would surface here).

- [ ] **Step 13: Commit**

```bash
git add -A -- pkg/web/dashboard
git commit -m "убираем supervisor UI из дашборда: компонент, event feed, типы событий, CSS-акценты"
```

---

## Task 8: Update `AGENTS.md`

**Files:**
- Modify: `AGENTS.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Correct the stale supervisor reference**

Find this sentence inside the "Жёсткий автономный трек: `agents: [auto]`" bullet:

```
Такая стадия идёт `runAutonomousAgent` НАПРЯМУЮ, без вызова `DetermineStagePhases` (без LLM-супервизора и без фолбэка).
```

Change it to:

```
Такая стадия идёт `runAutonomousAgent` НАПРЯМУЮ — это единственный способ попасть на автономный трек (LLM-supervisor, который раньше мог принять то же решение динамически, был убран как избыточный: практика показала, что статического `agents: [auto]` достаточно).
```

Find this sentence in the same bullet:

```
Валидация `ParseFile`: `auto` — единственный агент; `auto`+`supervisor:true` → ошибка.
```

Change it to:

```
Валидация `ParseFile`: `auto` — единственный агент.
```

- [ ] **Step 2: Commit**

```bash
git add AGENTS.md
git commit -m "обновляем AGENTS.md после удаления LLM-supervisor"
```

---

## Task 9: Final full-repository verification

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Full backend build, vet, and test**

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

Expected: everything green. This is the concrete check for the spec's acceptance bar — zero regressions in the static `agents:[auto]` track, script stages, pre-planned stages, and the standard planning→implementation→review cycle.

- [ ] **Step 2: Lint**

```bash
make lint
```

Expected: `0 issues.` (matches the project's existing lint gate — no new `unused`/`goconst`/etc. findings from this removal).

- [ ] **Step 3: Full dashboard build and test**

```bash
cd pkg/web/dashboard
npm test -- --run
npm run build
cd ../../..
```

Expected: all pass, clean build.

- [ ] **Step 4: Confirm no stray references remain**

```bash
grep -rn "supervisor" --include='*.go' --include='*.ts' --include='*.tsx' --include='*.css' --include='*.yaml' --include='*.yml' . \
  | grep -v -i 'goga-supervisor\|supervisor_decision.*deprecated\|node_modules\|\.git/' \
  | grep -iv 'is no longer supported\|deprecated\|removed\b'
```

Review the output by hand: expect it to be empty, or contain only incidental unrelated matches (if any surface, investigate before considering the removal complete — do not assume they're harmless without reading them).

- [ ] **Step 5: Final commit (if Step 4 turned up any stragglers that needed fixing)**

```bash
git add -A
git commit -m "финальная зачистка после удаления LLM-supervisor"
```

If Step 4 found nothing to fix, skip this commit — Task 8's commit is then the last one for this plan.
