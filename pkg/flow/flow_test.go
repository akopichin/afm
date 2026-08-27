package flow_test

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

const validYAML = `
name: test-flow
description: "test"
stages:
  - id: backend
    name: "Backend"
    description: "do backend stuff"
    agents: [planning, implementation, review]
  - id: frontend
    name: "Frontend"
    description: "do frontend stuff"
    depends_on: [backend]
    agents: [planning, implementation]
`

const cycleYAML = `
name: cycle-flow
description: "has cycle"
stages:
  - id: a
    name: "A"
    description: "a"
    depends_on: [b]
    agents: [planning]
  - id: b
    name: "B"
    description: "b"
    depends_on: [a]
    agents: [planning]
`

const noPlanningYAML = `
name: bad-flow
description: "bad"
stages:
  - id: stage1
    name: "S1"
    description: "no planning and no plan"
    agents: [implementation]
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestParseValidFlow(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Name != "test-flow" {
		t.Errorf("name: got %q want %q", f.Name, "test-flow")
	}
	if len(f.Stages) != 2 {
		t.Errorf("stages: got %d want 2", len(f.Stages))
	}
	if f.Stages[1].DependsOn[0] != "backend" {
		t.Errorf("depends_on: got %v", f.Stages[1].DependsOn)
	}
}

func TestParseRootDir(t *testing.T) {
	const yaml = `
name: rooted-flow
description: "flow with root_dir"
root_dir: /workspace
stages:
  - id: backend
    name: "Backend"
    description: "do backend"
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.RootDir != "/workspace" {
		t.Errorf("root_dir: got %q want %q", f.RootDir, "/workspace")
	}
}

func TestValidateCycle(t *testing.T) {
	path := writeTemp(t, cycleYAML)
	_, err := flow.ParseFile(path)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestValidateMissingPlan(t *testing.T) {
	path := writeTemp(t, noPlanningYAML)
	_, err := flow.ParseFile(path)
	if err == nil {
		t.Fatal("expected validation error for missing planning+plan")
	}
}

const artifactsYAML = `
name: artifact-flow
description: "flow with artifacts"
stages:
  - id: backend
    name: "Backend"
    description: "do backend"
    agents: [planning, implementation]
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema"
      - name: db-schema
        path: ./schema.sql
        description: "SQL migration"
        inline: false
  - id: frontend
    name: "Frontend"
    description: "do frontend"
    depends_on: [backend]
    agents: [planning, implementation]
    inputs:
      - backend.api-contract
      - ref: backend.db-schema
        optional: true
`

func TestParseArtifacts(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, artifactsYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	backend := f.Stages[0]
	if len(backend.Artifacts) != 2 {
		t.Fatalf("backend artifacts: got %d want 2", len(backend.Artifacts))
	}
	if backend.Artifacts[0].Name != "api-contract" {
		t.Errorf("artifact[0].Name: got %q want %q", backend.Artifacts[0].Name, "api-contract")
	}
	if backend.Artifacts[0].Path != "docs/api-contract.yaml" {
		t.Errorf("artifact[0].Path: got %q want %q", backend.Artifacts[0].Path, "docs/api-contract.yaml")
	}
	if !backend.Artifacts[0].IsInline() {
		t.Error("artifact[0] should be inline by default")
	}
	if backend.Artifacts[1].IsInline() {
		t.Error("artifact[1] should not be inline")
	}

	frontend := f.Stages[1]
	if len(frontend.Inputs) != 2 {
		t.Fatalf("frontend inputs: got %d want 2", len(frontend.Inputs))
	}
	if frontend.Inputs[0].Ref != "backend.api-contract" {
		t.Errorf("input[0].Ref: got %q", frontend.Inputs[0].Ref)
	}
	if frontend.Inputs[0].Optional {
		t.Error("input[0] should not be optional")
	}
	if frontend.Inputs[1].Ref != "backend.db-schema" {
		t.Errorf("input[1].Ref: got %q", frontend.Inputs[1].Ref)
	}
	if !frontend.Inputs[1].Optional {
		t.Error("input[1] should be optional")
	}
}

func TestCustomAgentType(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    plan: docs/plan.md
    agents: [senior-go-architect]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := f.Stages[0]

	if !s.HasAgent(flow.AgentImplementation) {
		t.Error("custom agent should count as implementation")
	}
	if s.HasAgent(flow.AgentPlanning) {
		t.Error("custom agent should not count as planning")
	}
	if s.HasAgent(flow.AgentReview) {
		t.Error("custom agent should not count as review")
	}
	if s.ImplAgent() != "senior-go-architect" {
		t.Errorf("ImplAgent: got %q want %q", s.ImplAgent(), "senior-go-architect")
	}
}

func TestValidateDuplicateArtifactName(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [planning]
    artifacts:
      - name: dup
        path: a.txt
        description: d1
      - name: dup
        path: b.txt
        description: d2
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected duplicate artifact name error")
	}
}

func TestValidateInputRefUnknownStage(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [planning]
  - id: s2
    name: S2
    description: d
    depends_on: [s1]
    agents: [planning]
    inputs:
      - nope.artifact
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected unknown stage error in input ref")
	}
}

func TestValidateInputRefUnknownArtifact(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [planning]
    artifacts:
      - name: real
        path: a.txt
        description: d
  - id: s2
    name: S2
    description: d
    depends_on: [s1]
    agents: [planning]
    inputs:
      - s1.fake
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected unknown artifact error in input ref")
	}
}

func TestValidateInputNotInDependsOn(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [planning]
    artifacts:
      - name: art
        path: a.txt
        description: d
  - id: s2
    name: S2
    description: d
    agents: [planning]
    inputs:
      - s1.art
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error: input stage not in depends_on")
	}
}

const interactiveYAML = `
name: interactive-flow
description: "test interactive"
stages:
  - id: discovery
    name: "Discovery"
    description: "ask user"
    agents: [planning]
    interactive: true
`

func TestParseInteractive(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, interactiveYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.Stages[0].Interactive {
		t.Error("Interactive should be true")
	}
}

func TestInteractiveDefaultsFalse(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range f.Stages {
		if s.Interactive {
			t.Errorf("stage %q: Interactive should default to false", s.ID)
		}
	}
}

func TestStageWithReadyPlan(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [implementation]
    plan: docs/plans/existing.md
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error for stage with plan path: %v", err)
	}
	if f.Stages[0].Plan != "docs/plans/existing.md" {
		t.Error("plan path not set")
	}
}

const eagerPlanningYAML = `
name: eager-flow
description: "eager planning"
stages:
  - id: base
    name: "Base"
    description: "base stage"
    agents: [planning, implementation]
  - id: dependent
    name: "Dependent"
    description: "plans eagerly"
    depends_on: [base]
    eager_planning: true
    agents: [planning, implementation]
`

func TestParseEagerPlanning(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, eagerPlanningYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.Stages[1].EagerPlanning {
		t.Error("eager_planning: got false, want true")
	}
	if f.Stages[0].EagerPlanning {
		t.Error("eager_planning default: got true, want false")
	}
}

func TestParseVerify(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [implementation]
    plan: docs/plans/existing.md
    verify: ".venv/bin/python -m pytest tests/ -q"
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Stages[0].Verify != ".venv/bin/python -m pytest tests/ -q" {
		t.Errorf("verify command not parsed, got %q", f.Stages[0].Verify)
	}
}

func TestParsePrompt(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    agents: [implementation]
    plan: docs/plans/existing.md
    prompt: |
      Focus on the error-handling path and keep changes minimal.
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(f.Stages[0].Prompt, "Focus on the error-handling path") {
		t.Errorf("prompt not parsed, got %q", f.Stages[0].Prompt)
	}
}

func TestParseRootPrompt(t *testing.T) {
	yaml := `
name: f
description: d
prompt: |
  Always write commit messages in Russian.
stages:
  - id: s1
    name: S1
    description: d
    agents: [implementation]
    plan: docs/plans/existing.md
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(f.Prompt, "Always write commit messages in Russian") {
		t.Errorf("root prompt not parsed, got %q", f.Prompt)
	}
}

func TestParseRootPromptEmpty(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Prompt != "" {
		t.Errorf("root prompt: got %q want empty", f.Prompt)
	}
}

func TestParseScriptStageFields(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: notify
    name: N
    description: d
    script: |
      echo "hello"
    script_timeout: 45s
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.Script != "echo \"hello\"\n" {
		t.Errorf("Script = %q", st.Script)
	}
	if st.ScriptTimeout != 45*time.Second {
		t.Errorf("ScriptTimeout = %v, want 45s", st.ScriptTimeout)
	}
	if !st.IsScript() {
		t.Error("IsScript() should be true")
	}
}

func TestParseScriptBeforeAfterFields(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: build
    name: B
    description: d
    agents: [implementation]
    plan: docs/plan.md
    script_before: |
      echo "before"
    script_before_timeout: 10s
    script_after: |
      echo "after"
    script_after_timeout: 20s
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.ScriptBefore != "echo \"before\"\n" || st.ScriptBeforeTimeout != 10*time.Second {
		t.Errorf("ScriptBefore = %q / %v", st.ScriptBefore, st.ScriptBeforeTimeout)
	}
	if st.ScriptAfter != "echo \"after\"\n" || st.ScriptAfterTimeout != 20*time.Second {
		t.Errorf("ScriptAfter = %q / %v", st.ScriptAfter, st.ScriptAfterTimeout)
	}
	if st.IsScript() {
		t.Error("IsScript() should be false for an agent stage with hooks")
	}
}

func TestScriptTimeoutDefaultsWhenUnset(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: notify
    name: N
    description: d
    script: |
      echo "hello"
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.ScriptTimeout != 5*time.Minute {
		t.Errorf("ScriptTimeout = %v, want 5m default", st.ScriptTimeout)
	}
}

func TestScriptBeforeAfterTimeoutsDefaultWhenUnset(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: build
    name: B
    description: d
    agents: [implementation]
    plan: docs/plan.md
    script_before: |
      echo "before"
    script_after: |
      echo "after"
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := f.Stages[0]
	if st.ScriptBeforeTimeout != 5*time.Minute {
		t.Errorf("ScriptBeforeTimeout = %v, want 5m default", st.ScriptBeforeTimeout)
	}
	if st.ScriptAfterTimeout != 5*time.Minute {
		t.Errorf("ScriptAfterTimeout = %v, want 5m default", st.ScriptAfterTimeout)
	}
}

func TestValidateScriptCannotCombineWithAgents(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
    agents: [implementation]
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("expected script-combination error, got %v", err)
	}
}

func TestValidateScriptCannotCombineWithVerify(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
    verify: "true"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("expected script-combination error, got %v", err)
	}
}

func TestValidateScriptStageNeedsNoOtherWorkField(t *testing.T) {
	yaml := `
name: f
description: d
stages:
  - id: s1
    name: S1
    description: d
    script: "echo hi"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error for valid script-only stage: %v", err)
	}
}

func TestStage_AutoRunDisabled(t *testing.T) {
	enabled := true
	disabled := false

	if (flow.Stage{}).AutoRunDisabled() {
		t.Error("nil AutoRun should not be disabled (default: enabled)")
	}
	if (flow.Stage{AutoRun: &enabled}).AutoRunDisabled() {
		t.Error("AutoRun=true should not be disabled")
	}
	if !(flow.Stage{AutoRun: &disabled}).AutoRunDisabled() {
		t.Error("AutoRun=false should be disabled")
	}
}

func TestParseFile_AutoRunFalse(t *testing.T) {
	yaml := `
name: test-flow
stages:
  - id: s1
    name: "S1"
    script: "echo hi"
    auto_run: false
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Stages[0].AutoRunDisabled() {
		t.Error("expected auto_run: false to parse as disabled")
	}
}

func TestParseFile_AutoRunOmittedDefaultsEnabled(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].AutoRunDisabled() {
		t.Error("expected omitted auto_run to default to enabled (not disabled)")
	}
}

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
	path := writeTemp(t, yaml)

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

func TestParseButtons_LabelToPrompt(t *testing.T) {
	yaml := `
name: test-flow
stages:
  - id: s1
    name: "S1"
    agents: [planning, implementation]
    buttons:
      Run linter: "Запусти golangci-lint и почини все замечания"
      Rebuild: "Пересобери проект с нуля"
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	btns := f.Stages[0].Buttons
	if len(btns) != 2 {
		t.Fatalf("expected 2 buttons, got %d: %+v", len(btns), btns)
	}
	if got := btns.Prompt("Run linter"); got != "Запусти golangci-lint и почини все замечания" {
		t.Errorf("Prompt(\"Run linter\") = %q", got)
	}
	if got := btns.Prompt("Rebuild"); got != "Пересобери проект с нуля" {
		t.Errorf("Prompt(\"Rebuild\") = %q", got)
	}
	if got := btns.Prompt("does-not-exist"); got != "" {
		t.Errorf("Prompt(unknown) = %q, want empty", got)
	}
	if want := []string{"Run linter", "Rebuild"}; !equalStrs(btns.Labels(), want) {
		t.Errorf("Labels() = %v, want %v", btns.Labels(), want)
	}
}

// TestParseButtons_DeclarationOrderPreserved — YAML-порядок кнопок сохраняется
// (плоский map[string]string итерировался бы случайно, тасуя меню).
func TestParseButtons_DeclarationOrderPreserved(t *testing.T) {
	yaml := `
name: test-flow
stages:
  - id: s1
    name: "S1"
    agents: [planning, implementation]
    buttons:
      Zebra: "z prompt"
      Apple: "a prompt"
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Zebra", "Apple"}; !equalStrs(f.Stages[0].Buttons.Labels(), want) {
		t.Errorf("order not preserved: %v, want %v", f.Stages[0].Buttons.Labels(), want)
	}
}

func TestParseButtons_NoButtonsParsesUnchanged(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if f.Stages[0].Buttons != nil {
		t.Errorf("expected nil Buttons when none declared, got %+v", f.Stages[0].Buttons)
	}
}

func TestValidateButtons_EmptyLabel(t *testing.T) {
	yaml := `
name: f
stages:
  - id: s1
    name: S1
    agents: [planning, implementation]
    buttons:
      "": "some prompt"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "button label cannot be empty") {
		t.Fatalf("expected empty-label error, got %v", err)
	}
}

func TestValidateButtons_EmptyPrompt(t *testing.T) {
	yaml := `
name: f
stages:
  - id: s1
    name: S1
    agents: [planning, implementation]
    buttons:
      Run linter: ""
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "has empty prompt") {
		t.Fatalf("expected empty-prompt error, got %v", err)
	}
}

func TestValidateButtons_DuplicateLabel(t *testing.T) {
	yaml := `
name: f
stages:
  - id: s1
    name: S1
    agents: [planning, implementation]
    buttons:
      Run linter: "first"
      Run linter: "second"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "duplicate button label") {
		t.Fatalf("expected duplicate-label error, got %v", err)
	}
}

func TestValidateButtons_CannotCombineWithScript(t *testing.T) {
	yaml := `
name: f
stages:
  - id: s1
    name: S1
    script: "echo hi"
    buttons:
      Run linter: "lint"
`
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil || !strings.Contains(err.Error(), `"buttons" cannot be combined with script`) {
		t.Fatalf("expected buttons+script error, got %v", err)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
