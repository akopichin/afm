package flow_test

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
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

func TestValidateStageIDCharset(t *testing.T) {
	// Reject unsafe stage IDs before they reach paths, session URLs, or Chrome profile names.
	for _, bad := range []string{"a:b", "a@b", "ui#one", "has space", "кирилл"} {
		yaml := "name: f\nstages:\n  - id: \"" + bad + "\"\n    name: s\n    agents: [auto]\n"
		if _, err := flow.ParseFile(writeTemp(t, yaml)); err == nil {
			t.Fatalf("stage id %q must be rejected", bad)
		}
	}
	// Normal ids remain valid.
	for _, ok := range []string{"build", "live-run", "step.2", "a_b"} {
		yaml := "name: f\nstages:\n  - id: \"" + ok + "\"\n    name: s\n    agents: [auto]\n"
		if _, err := flow.ParseFile(writeTemp(t, yaml)); err != nil {
			t.Fatalf("stage id %q must be accepted, got %v", ok, err)
		}
	}
}

func TestValidateStageIDCaseInsensitiveCollision(t *testing.T) {
	// "Build" and "build" differ only in case: on a case-insensitive FS (macOS)
	// they map to one Chrome profile dir, so afm must reject the pair.
	yaml := "name: f\nstages:\n" +
		"  - id: Build\n    name: a\n    agents: [auto]\n" +
		"  - id: build\n    name: b\n    agents: [auto]\n"
	if _, err := flow.ParseFile(writeTemp(t, yaml)); err == nil {
		t.Fatal("case-insensitive id collision (Build/build) must be rejected")
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
	if len(f.Stages[0].Verify.Steps) != 1 || f.Stages[0].Verify.Steps[0].Run != ".venv/bin/python -m pytest tests/ -q" {
		t.Errorf("verify command not parsed, got %+v", f.Stages[0].Verify.Steps)
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

func TestValidateVerify_TableAtParseFileLevel(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "script with object verify rejected",
			yaml: `
name: f
stages:
  - id: s1
    script: "echo hi"
    verify:
      run: "true"
`,
			wantErr:   true,
			errSubstr: "script",
		},
		{
			name: "script with list verify rejected",
			yaml: `
name: f
stages:
  - id: s1
    script: "echo hi"
    verify:
      - run: "true"
`,
			wantErr:   true,
			errSubstr: "script",
		},
		{
			name: "planning-only stage with verify rejected",
			yaml: `
name: f
stages:
  - id: s1
    agents: [planning]
    verify: "true"
`,
			wantErr:   true,
			errSubstr: "planning-only",
		},
		{
			name: "normal stage with verify parses",
			yaml: `
name: f
stages:
  - id: s1
    agents: [planning, implementation]
    verify: "true"
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, tt.yaml))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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

func TestStage_BrowserDisabled(t *testing.T) {
	enabled := true
	disabled := false

	if (flow.Stage{}).BrowserDisabled() {
		t.Error("nil Browser should not be disabled (inherits global policy)")
	}
	if (flow.Stage{Browser: &enabled}).BrowserDisabled() {
		t.Error("browser=true should not be disabled")
	}
	if !(flow.Stage{Browser: &disabled}).BrowserDisabled() {
		t.Error("browser=false should be disabled")
	}
}

func TestParseFile_BrowserFalse(t *testing.T) {
	yaml := `
name: test-flow
stages:
  - id: s1
    name: "S1"
    agents: [planning, implementation]
    browser: false
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Stages[0].BrowserDisabled() {
		t.Error("expected browser: false to parse as disabled")
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

func TestParseMemory_FieldsAndDefaults(t *testing.T) {
	yaml := `
name: f
memory:
  path: .goga/memory
stages:
  - id: build
    name: build
    agents: [planning, implementation]
    reflect:
      file: build.md
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.MemoryEnabled() {
		t.Fatal("expected memory enabled")
	}
	if f.Memory.MaxRules != 25 {
		t.Errorf("MaxRules default = %d, want 25", f.Memory.MaxRules)
	}
	if f.Stages[0].Reflect == nil || f.Stages[0].Reflect.File != "build.md" {
		t.Error("expected stage.Reflect to have file")
	}
	if f.Stages[0].Reflect.Mode != "rw" {
		t.Errorf("Mode default = %q, want rw", f.Stages[0].Reflect.Mode)
	}
}

func TestParseMemory_ExplicitFields(t *testing.T) {
	yaml := `
name: f
memory:
  path: .goga/memory
  max_rules: 100
  commit: true
stages:
  - id: build
    name: build
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Memory.MaxRules != 100 {
		t.Errorf("MaxRules = %d, want 100", f.Memory.MaxRules)
	}
	if !f.Memory.Commit {
		t.Error("Commit should be true")
	}
}

func TestParseMemory_ReflectDefaultsNil(t *testing.T) {
	yaml := `
name: f
memory:
  path: .goga/memory
stages:
  - id: build
    name: build
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Stages[0].Reflect != nil {
		t.Error("Reflect must default to nil")
	}
}

func TestValidateMemory_ReflectRequiresPath(t *testing.T) {
	yaml := `
name: f
stages:
  - id: build
    name: build
    reflect:
      file: build.md
    agents: [planning, implementation]
`
	// The stage needs a valid id: here — without one, validate's mandatory safePathComponent check fires
	// first and masks the assertion this test claims to verify.
	_, err := flow.ParseFile(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("expected error: reflect without memory.path")
	}
	if !strings.Contains(err.Error(), "reflect requires memory.path") {
		t.Fatalf("expected the reflect-requires-memory.path error, got: %v", err)
	}
}

func TestValidateMemory_ScriptStageReflectAllowed(t *testing.T) {
	yaml := `
name: f
memory:
  path: .goga/memory
stages:
  - id: gen
    name: gen
    reflect:
      file: gen.md
    script: "echo hi"
`
	if _, err := flow.ParseFile(writeTemp(t, yaml)); err != nil {
		t.Fatalf("script stage with reflect must parse: %v", err)
	}
}

func TestParseMemoryV3_FieldsAndDefaults(t *testing.T) {
	yaml := `
name: f
memory:
  path: .goga/memory
stages:
  - id: build
    name: build
    agents: [planning, implementation]
    reflect:
      file: build.md
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.MemoryEnabled() || f.Memory.Path != ".goga/memory" {
		t.Fatalf("path not parsed: %+v", f.Memory)
	}
	if f.Memory.MaxRules != 25 {
		t.Errorf("MaxRules default = %d, want 25", f.Memory.MaxRules)
	}
	r := f.Stages[0].Reflect
	if r == nil || r.File != "build.md" {
		t.Fatalf("reflect not parsed: %+v", r)
	}
	if r.Mode != "rw" {
		t.Errorf("mode default = %q, want rw", r.Mode)
	}
	if !r.CanRead() || !r.CanWrite() {
		t.Error("rw must read and write")
	}
}

func TestParseMemoryV3_ModeGates(t *testing.T) {
	r := flow.Reflect{Mode: "r"}
	if !r.CanRead() || r.CanWrite() {
		t.Error("r: read-only")
	}
	w := flow.Reflect{Mode: "w"}
	if w.CanRead() || !w.CanWrite() {
		t.Error("w: write-only")
	}
}

func TestValidateMemoryV3(t *testing.T) {
	write := func(body string) error {
		path := writeTemp(t, body)
		_, err := flow.ParseFile(path)
		return err
	}
	// reflect without memory.path → error
	if err := write("name: f\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      file: s.md\n"); err == nil {
		t.Error("reflect without memory.path must error")
	}
	// empty file → error
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      mode: rw\n"); err == nil {
		t.Error("empty reflect.file must error")
	}
	// bad mode → error
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - name: s\n    agents: [planning]\n    reflect:\n      file: s.md\n      mode: x\n"); err == nil {
		t.Error("bad mode must error")
	}
	// script stage with reflect parses OK
	if err := write("name: f\nmemory:\n  path: m\nstages:\n  - id: s\n    name: s\n    script: \"echo hi\"\n    reflect:\n      file: s.md\n"); err != nil {
		t.Errorf("script+reflect must parse: %v", err)
	}
}

func TestParseHooks_FlowLevel(t *testing.T) {
	f := writeTemp(t, `
name: h
hooks:
  - id: notify
    events: [flow_started, stage_failed]
    command: ./notify.sh
stages:
  - id: s1
    script: "true"
`)
	parsed, err := flow.ParseFile(f)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(parsed.Hooks) != 1 || parsed.Hooks[0].ID != "notify" {
		t.Fatalf("got %+v", parsed.Hooks)
	}
}

func TestParseHooks_StageLevel(t *testing.T) {
	f := writeTemp(t, `
name: h
stages:
  - id: s1
    script: "true"
    hooks:
      - id: dep
        events: all
        skip_events: [stage_question_answered]
        command: ./dep.sh
        timeout: 15s
        retries: 1
`)
	parsed, err := flow.ParseFile(f)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(parsed.Stages[0].Hooks) != 1 {
		t.Fatalf("got %+v", parsed.Stages[0].Hooks)
	}
	h := parsed.Stages[0].Hooks[0]
	if h.Timeout != 15*time.Second || h.Retries != 1 || len(h.SkipEvents) != 1 {
		t.Fatalf("hook fields: %+v", h)
	}
}

func TestParseHooks_EnvAndInheritEnv(t *testing.T) {
	f := writeTemp(t, `
name: h
stages:
  - id: s1
    script: "true"
    hooks:
      - id: dep
        events: all
        command: ./dep.sh
        env:
          TOK: "env:TELEGRAM_TOKEN"
          SECRET: "file:~/.afm/secrets/x"
        inherit_env: true
`)
	parsed, err := flow.ParseFile(f)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	h := parsed.Stages[0].Hooks[0]
	if !h.InheritEnv {
		t.Fatalf("InheritEnv: got %v, want true", h.InheritEnv)
	}
	if len(h.Env) != 2 || h.Env["TOK"] != "env:TELEGRAM_TOKEN" || h.Env["SECRET"] != "file:~/.afm/secrets/x" {
		t.Fatalf("Env: got %+v", h.Env)
	}
}

func TestParseHooks_Errors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"unknown event", `
name: h
hooks: [{id: x, events: [stage_strted], command: "true"}]
stages: [{id: s1, script: "true"}]
`},
		{"dup id flow layer", `
name: h
hooks: [{id: x, events: all, command: "true"}, {id: x, events: all, command: "true"}]
stages: [{id: s1, script: "true"}]
`},
		{"flow event in stage hook", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: [flow_finished], command: "true"}]}]
`},
		{"empty command", `
name: h
hooks: [{id: x, events: all, command: ""}]
stages: [{id: s1, script: "true"}]
`},
		{"dup id stage layer", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: all, command: "a"}, {id: x, events: all, command: "b"}]}]
`},
		{"dup id across stages", `
name: h
stages: [{id: s1, script: "true", hooks: [{id: x, events: all, command: "a"}]}, {id: s2, script: "true", hooks: [{id: x, events: all, command: "b"}]}]
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := writeTemp(t, tc.yaml)
			if _, err := flow.ParseFile(f); err == nil {
				t.Fatal("want parse error, got nil")
			}
		})
	}
}

func TestParseHooks_DifferentIDsAcrossLayersOK(t *testing.T) {
	f := writeTemp(t, `
name: h
hooks: [{id: flowhook, events: all, command: "true"}]
stages: [{id: s1, script: "true", hooks: [{id: stagehook, events: all, command: "true"}]}]
`)
	if _, err := flow.ParseFile(f); err != nil {
		t.Fatalf("different ids must coexist: %v", err)
	}
}

func TestParse_SideAgent_Absent(t *testing.T) {
	const yaml = `
name: f
description: "no side_agent"
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.SideAgent != nil {
		t.Errorf("side_agent absent: got %v, want nil", *f.SideAgent)
	}
}

func TestParse_SideAgent_False(t *testing.T) {
	const yaml = `
name: f
description: "side_agent false"
side_agent: false
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.SideAgent == nil || *f.SideAgent {
		t.Errorf("side_agent false: got %v, want pointer to false", f.SideAgent)
	}
}

func TestParse_SideAgent_True(t *testing.T) {
	const yaml = `
name: f
description: "side_agent true"
side_agent: true
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`
	f, err := flow.ParseFile(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.SideAgent == nil || !*f.SideAgent {
		t.Errorf("side_agent true: got %v, want pointer to true", f.SideAgent)
	}
}

func TestParseFile_WorktreesTriState(t *testing.T) {
	const base = `
name: f
description: "worktrees tri-state"
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`
	t.Run("absent", func(t *testing.T) {
		f, err := flow.ParseFile(writeTemp(t, base))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.Worktrees != nil {
			t.Errorf("worktrees absent: got %v, want nil", *f.Worktrees)
		}
	})

	t.Run("true", func(t *testing.T) {
		f, err := flow.ParseFile(writeTemp(t, "worktrees: true\n"+base))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.Worktrees == nil || !*f.Worktrees {
			t.Errorf("worktrees true: got %v, want pointer to true", f.Worktrees)
		}
	})

	t.Run("false", func(t *testing.T) {
		f, err := flow.ParseFile(writeTemp(t, "worktrees: false\n"+base))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.Worktrees == nil || *f.Worktrees {
			t.Errorf("worktrees false: got %v, want pointer to false", f.Worktrees)
		}
	})
}

// --- T1.3: collaboration.max_depth in flow.yaml -------------------------------------------------------

const collabFlowBase = `
name: f
description: "collaboration max_depth"
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`

func TestParseFile_CollaborationMaxDepth(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "collaboration:\n  max_depth: 2\n"+collabFlowBase))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Collaboration == nil || f.Collaboration.MaxDepth == nil || *f.Collaboration.MaxDepth != 2 {
		t.Fatalf("Collaboration = %+v, want MaxDepth pointing to 2", f.Collaboration)
	}
}

func TestParseFile_CollaborationMaxDepthZero(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "collaboration:\n  max_depth: 0\n"+collabFlowBase))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Collaboration == nil || f.Collaboration.MaxDepth == nil || *f.Collaboration.MaxDepth != 0 {
		t.Fatalf("Collaboration = %+v, want explicit MaxDepth 0, not lost/nil", f.Collaboration)
	}
}

func TestParseFile_CollaborationAbsent(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, collabFlowBase))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Collaboration != nil {
		t.Errorf("Collaboration = %+v, want nil when the key is absent", f.Collaboration)
	}
}

func TestParseFile_RejectsNegativeMaxDepth(t *testing.T) {
	_, err := flow.ParseFile(writeTemp(t, "collaboration:\n  max_depth: -1\n"+collabFlowBase))
	if err == nil {
		t.Fatal("expected ParseFile to reject a negative collaboration.max_depth")
	}
	if !strings.Contains(err.Error(), "collaboration.max_depth") {
		t.Errorf("error = %q, want it to name collaboration.max_depth", err.Error())
	}
}

func TestParseFile_RejectsUnknownCollaborationField(t *testing.T) {
	_, err := flow.ParseFile(writeTemp(t, "collaboration:\n  enabled: true\n"+collabFlowBase))
	if err == nil {
		t.Fatal("expected ParseFile to reject collaboration.enabled at the flow level (no per-flow enable)")
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Errorf("error = %q, want it to name the rejected field \"enabled\"", err.Error())
	}
}

func TestParseFile_RejectsUnknownCollaborationField_Other(t *testing.T) {
	_, err := flow.ParseFile(writeTemp(t, "collaboration:\n  bogus: 1\n"+collabFlowBase))
	if err == nil {
		t.Fatal("expected ParseFile to reject an unknown collaboration field")
	}
}

func TestParseFile_CollaborationMode(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want assistants.CollaborationMode
	}{
		{"team", "collaboration:\n  mode: team\n", assistants.CollaborationModeTeam},
		{"assistants", "collaboration:\n  mode: assistants\n", assistants.CollaborationModeAssistants},
		{"with max_depth", "collaboration:\n  max_depth: 2\n  mode: assistants\n", assistants.CollaborationModeAssistants},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := flow.ParseFile(writeTemp(t, tt.yaml+collabFlowBase))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Collaboration == nil || f.Collaboration.Mode == nil || *f.Collaboration.Mode != tt.want {
				t.Fatalf("Collaboration = %+v, want Mode pointing to %q", f.Collaboration, tt.want)
			}
		})
	}
}

func TestParseFile_CollaborationModeKeepsMaxDepth(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "collaboration:\n  max_depth: 2\n  mode: assistants\n"+collabFlowBase))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Collaboration.MaxDepth == nil || *f.Collaboration.MaxDepth != 2 {
		t.Errorf("MaxDepth = %v, want 2 next to mode", f.Collaboration.MaxDepth)
	}
}

func TestParseFile_CollaborationModeAbsent(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "collaboration:\n  max_depth: 1\n"+collabFlowBase))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Collaboration.Mode != nil {
		t.Errorf("Mode = %q, want nil when the key is absent", *f.Collaboration.Mode)
	}
}

func TestParseFile_RejectsInvalidCollaborationMode(t *testing.T) {
	for name, value := range map[string]string{
		"empty string": `""`,
		"null":         "null",
		"bare key":     "",
		"number":       "1",
		"bool":         "true",
		"list":         "[team]",
		"object":       "{a: b}",
		"unknown":      "solo",
		"wrong case":   "Team",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, "collaboration:\n  mode: "+value+"\n"+collabFlowBase))
			if err == nil {
				t.Fatalf("expected ParseFile to reject collaboration.mode: %s", value)
			}
			if !strings.Contains(err.Error(), "collaboration.mode") {
				t.Errorf("error = %q, want it to name collaboration.mode", err.Error())
			}
		})
	}
}

func TestParseFile_RejectsEnabledNextToCollaborationMode(t *testing.T) {
	_, err := flow.ParseFile(writeTemp(t, "collaboration:\n  mode: team\n  enabled: true\n"+collabFlowBase))
	if err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("error = %v, want collaboration.enabled still rejected at the flow level", err)
	}
}

// TestParseFile_CollaborationModeAlias: алиас на корректную строку принимается так же, как сама строка.
func TestParseFile_CollaborationModeAlias(t *testing.T) {
	f, err := flow.ParseFile(writeTemp(t, "x-mode: &m assistants\ncollaboration:\n  mode: *m\n"+collabFlowBase))
	if err != nil {
		t.Fatal(err)
	}
	if f.Collaboration == nil || f.Collaboration.Mode == nil || *f.Collaboration.Mode != assistants.CollaborationModeAssistants {
		t.Fatalf("Collaboration = %+v, want Mode pointing to assistants", f.Collaboration)
	}
}

// --- repeat: re-run the flow after each successful pass ---------------------------------------------------

const repeatFlowBase = `
name: f
description: "repeat"
stages:
  - id: s
    name: "S"
    description: "d"
    agents: [planning, implementation]
`

func TestParseFile_Repeat(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want flow.RepeatConfig
	}{
		{"missing key", "", flow.RepeatConfig{}},
		{"false", "repeat: false\n", flow.RepeatConfig{}},
		{"true", "repeat: true\n", flow.RepeatConfig{Enabled: true, MaxCycles: 0, KeepRuns: 2}},
		{"mapping defaults", "repeat:\n  max_cycles: 3\n", flow.RepeatConfig{Enabled: true, MaxCycles: 3, KeepRuns: 2}},
		{"empty mapping", "repeat: {}\n", flow.RepeatConfig{Enabled: true, MaxCycles: 0, KeepRuns: 2}},
		{"explicit keep_runs zero", "repeat:\n  keep_runs: 0\n", flow.RepeatConfig{Enabled: true, MaxCycles: 0, KeepRuns: 0}},
		{"explicit max_cycles zero", "repeat:\n  max_cycles: 0\n  keep_runs: 5\n", flow.RepeatConfig{Enabled: true, MaxCycles: 0, KeepRuns: 5}},
		{"full mapping", "repeat:\n  max_cycles: 4\n  keep_runs: 1\n", flow.RepeatConfig{Enabled: true, MaxCycles: 4, KeepRuns: 1}},
		{"integral float", "repeat:\n  max_cycles: 1.0\n", flow.RepeatConfig{Enabled: true, MaxCycles: 1, KeepRuns: 2}},
		{"exponent float", "repeat:\n  max_cycles: 1e0\n", flow.RepeatConfig{Enabled: true, MaxCycles: 1, KeepRuns: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := flow.ParseFile(writeTemp(t, tt.yaml+repeatFlowBase))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Repeat != tt.want {
				t.Errorf("Repeat = %+v, want %+v", f.Repeat, tt.want)
			}
		})
	}
}

func TestParseFile_RepeatRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"negative max_cycles", "repeat:\n  max_cycles: -1\n", "repeat.max_cycles"},
		{"negative keep_runs", "repeat:\n  keep_runs: -1\n", "repeat.keep_runs"},
		{"unknown key", "repeat:\n  max_cycle: 3\n", `unknown field "max_cycle"`},
		{"string instead of bool", "repeat: \"true\"\n", "repeat"},
		{"number", "repeat: 3\n", "repeat"},
		{"yaml 1.1 bool yes", "repeat: yes\n", "repeat"},
		{"yaml 1.1 bool on", "repeat: on\n", "repeat"},
		{"quoted yaml 1.1 bool", "repeat: \"yes\"\n", "repeat"},
		{"list", "repeat: [1]\n", "repeat"},
		{"max_cycles not a number", "repeat:\n  max_cycles: many\n", "repeat.max_cycles"},
		{"keep_runs not a number", "repeat:\n  keep_runs: 1.5\n", "repeat.keep_runs"},
		{"fractional max_cycles", "repeat:\n  max_cycles: 1.5\n", "repeat.max_cycles"},
		{"empty value", "repeat:\n", "repeat"},
		{"null", "repeat: null\n", "repeat"},
		{"tilde null", "repeat: ~\n", "repeat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, tt.yaml+repeatFlowBase))
			if err == nil {
				t.Fatal("expected ParseFile to reject the repeat setting")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
