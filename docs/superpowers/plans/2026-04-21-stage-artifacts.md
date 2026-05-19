# Stage Artifacts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable passing context (plans and named file artifacts) between stages via new `artifacts` and `inputs` YAML fields.

**Architecture:** Add `Artifact` and `Input` types to the flow package with custom YAML unmarshalling for `Input` (supports both string and object forms). Extend orchestrator prompt builders to inject dependent stage plans and artifact contents. Add runtime validation of artifact files before stage launch.

**Tech Stack:** Go, gopkg.in/yaml.v3

---

### Task 1: Artifact and Input types + YAML parsing

**Files:**
- Modify: `pkg/flow/flow.go:10-34` (add types and fields to Stage)
- Test: `pkg/flow/flow_test.go`

- [ ] **Step 1: Write failing test for Artifact parsing**

Add to `pkg/flow/flow_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/flow/ -run TestParseArtifacts -v`
Expected: compilation error — `Artifact`, `Input`, `IsInline` undefined.

- [ ] **Step 3: Add Artifact type, Input type with custom UnmarshalYAML, and Stage fields**

Add to `pkg/flow/flow.go` before the `Stage` struct:

```go
// Artifact describes a file that a stage produces for other stages.
type Artifact struct {
	Name        string `yaml:"name"`
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
	Inline      *bool  `yaml:"inline"`
}

// IsInline returns whether the artifact content should be inlined into the prompt.
// Defaults to true when Inline is nil.
func (a Artifact) IsInline() bool {
	return a.Inline == nil || *a.Inline
}

// Input describes an artifact that a stage consumes from a dependency.
// Supports unmarshalling from a plain string "stage.artifact" or an object {ref, optional}.
type Input struct {
	Ref      string `yaml:"ref"`
	Optional bool   `yaml:"optional"`
}

// UnmarshalYAML allows Input to be parsed from a string or an object.
func (inp *Input) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		inp.Ref = value.Value
		return nil
	}
	type plain Input
	return value.Decode((*plain)(inp))
}
```

Add two new fields to the `Stage` struct:

```go
Artifacts []Artifact `yaml:"artifacts"`
Inputs    []Input    `yaml:"inputs"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/flow/ -run TestParseArtifacts -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat: добавить типы Artifact и Input с YAML-парсингом"
```

---

### Task 2: Flow validation for artifacts and inputs

**Files:**
- Modify: `pkg/flow/flow.go:75-98` (extend `validate()`)
- Test: `pkg/flow/flow_test.go`

- [ ] **Step 1: Write failing tests for validation rules**

Add to `pkg/flow/flow_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/flow/ -run "TestValidate(Duplicate|InputRef|InputNot)" -v`
Expected: all 4 tests FAIL (validation not implemented yet).

- [ ] **Step 3: Implement validation in `validate()`**

Add to the `validate()` function in `pkg/flow/flow.go`, after the existing `detectCycles` call:

```go
// Build artifact index: stageID -> artifactName -> true
artifactIndex := make(map[string]map[string]bool, len(f.Stages))
for _, s := range f.Stages {
	names := make(map[string]bool, len(s.Artifacts))
	for _, a := range s.Artifacts {
		if names[a.Name] {
			return fmt.Errorf("stage %q: duplicate artifact name %q", s.ID, a.Name)
		}
		names[a.Name] = true
	}
	artifactIndex[s.ID] = names
}

// Validate inputs
for _, s := range f.Stages {
	depsSet := make(map[string]bool, len(s.DependsOn))
	for _, d := range s.DependsOn {
		depsSet[d] = true
	}

	for _, inp := range s.Inputs {
		parts := strings.SplitN(inp.Ref, ".", 2)
		if len(parts) != 2 {
			return fmt.Errorf("stage %q: invalid input ref %q (expected stage.artifact)", s.ID, inp.Ref)
		}
		stageID, artName := parts[0], parts[1]

		if !ids[stageID] {
			return fmt.Errorf("stage %q: input ref %q references unknown stage %q", s.ID, inp.Ref, stageID)
		}
		if !depsSet[stageID] {
			return fmt.Errorf("stage %q: input ref %q references stage %q which is not in depends_on", s.ID, inp.Ref, stageID)
		}
		arts, ok := artifactIndex[stageID]
		if !ok || !arts[artName] {
			return fmt.Errorf("stage %q: input ref %q references unknown artifact %q in stage %q", s.ID, inp.Ref, artName, stageID)
		}
	}
}
```

Note: add `"strings"` to the imports of `pkg/flow/flow.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/flow/ -run "TestValidate(Duplicate|InputRef|InputNot)" -v`
Expected: all PASS

- [ ] **Step 5: Run all flow tests to check for regressions**

Run: `go test ./pkg/flow/ -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/flow/flow.go pkg/flow/flow_test.go
git commit -m "feat: валидация artifacts/inputs — дубликаты, ссылки, depends_on"
```

---

### Task 3: Collect stage context (plans + artifacts) for prompts

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:431-481` (add `collectStageContext`, modify prompt builders)
- Test: `pkg/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write failing test for `collectDependencyPlans`**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestCollectDependencyPlans(t *testing.T) {
	runDir := t.TempDir()

	// Create a plan for the dependency stage
	depDir := filepath.Join(runDir, "backend")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("# Backend Plan\n\nDo stuff"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "backend", Name: "Backend API", Description: "backend"},
		{ID: "frontend", Name: "Frontend", Description: "frontend", DependsOn: []string{"backend"}},
	}

	result := orchestrator.CollectDependencyPlans(runDir, stages[1], stages)
	if result == "" {
		t.Fatal("expected non-empty dependency plans")
	}
	if !strings.Contains(result, "Backend API") {
		t.Error("should contain dependency stage name")
	}
	if !strings.Contains(result, "# Backend Plan") {
		t.Error("should contain dependency plan content")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestCollectDependencyPlans -v`
Expected: compilation error — `CollectDependencyPlans` undefined.

- [ ] **Step 3: Implement `CollectDependencyPlans`**

Add to `pkg/orchestrator/orchestrator.go`:

```go
// CollectDependencyPlans reads plan.md from each stage in DependsOn
// and returns a formatted prompt section. Missing plans produce a warning comment.
func CollectDependencyPlans(runDir string, stage flow.Stage, allStages []flow.Stage) string {
	if len(stage.DependsOn) == 0 {
		return ""
	}

	nameIndex := make(map[string]string, len(allStages))
	for _, s := range allStages {
		nameIndex[s.ID] = s.Name
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Context from dependent stages\n")

	for _, depID := range stage.DependsOn {
		planPath := filepath.Join(runDir, depID, "plan.md")
		data, err := os.ReadFile(planPath)
		name := nameIndex[depID]
		if name == "" {
			name = depID
		}
		buf.WriteString(fmt.Sprintf("\n### Stage: %s (%s)\n\n", name, depID))
		if err != nil {
			buf.WriteString("(plan not available)\n")
			continue
		}
		buf.WriteString(string(data))
		buf.WriteString("\n")
	}

	return buf.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestCollectDependencyPlans -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go
git commit -m "feat: CollectDependencyPlans — собирает планы зависимых стадий"
```

---

### Task 4: Collect artifact contents for prompts

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go`
- Test: `pkg/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write failing test for `CollectArtifacts` — inline artifact**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestCollectArtifacts_Inline(t *testing.T) {
	projectDir := t.TempDir()

	// Create artifact file in project dir
	if err := os.MkdirAll(filepath.Join(projectDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docs/api.yaml"), []byte("openapi: 3.0.0"), 0644); err != nil {
		t.Fatal(err)
	}

	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "api-contract", Path: "docs/api.yaml", Description: "OpenAPI schema"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.api-contract"}},
		},
	}

	result, err := orchestrator.CollectArtifacts(projectDir, "", allStages[1], allStages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "api-contract") {
		t.Error("should contain artifact name")
	}
	if !strings.Contains(result, "openapi: 3.0.0") {
		t.Error("should contain inlined artifact content")
	}
}
```

- [ ] **Step 2: Write failing test for `CollectArtifacts` — non-inline artifact**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestCollectArtifacts_NonInline(t *testing.T) {
	runDir := t.TempDir()

	// Create artifact in stage dir (path starts with ./)
	stageDir := filepath.Join(runDir, "backend")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "schema.sql"), []byte("CREATE TABLE t(id int)"), 0644); err != nil {
		t.Fatal(err)
	}

	inlineFalse := false
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "db-schema", Path: "./schema.sql", Description: "SQL migration", Inline: &inlineFalse},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.db-schema"}},
		},
	}

	result, err := orchestrator.CollectArtifacts("", runDir, allStages[1], allStages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "File path:") {
		t.Error("non-inline artifact should contain file path reference")
	}
	if strings.Contains(result, "CREATE TABLE") {
		t.Error("non-inline artifact should NOT contain file content")
	}
}
```

- [ ] **Step 3: Write failing test for `CollectArtifacts` — missing required artifact**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestCollectArtifacts_MissingRequired(t *testing.T) {
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "missing", Path: "nonexistent.txt", Description: "gone"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.missing"}},
		},
	}

	_, err := orchestrator.CollectArtifacts("/tmp", "", allStages[1], allStages)
	if err == nil {
		t.Fatal("expected error for missing required artifact")
	}
}
```

- [ ] **Step 4: Write failing test for `CollectArtifacts` — missing optional artifact**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestCollectArtifacts_MissingOptional(t *testing.T) {
	allStages := []flow.Stage{
		{
			ID: "backend", Name: "Backend",
			Artifacts: []flow.Artifact{
				{Name: "missing", Path: "nonexistent.txt", Description: "gone"},
			},
		},
		{
			ID: "frontend", Name: "Frontend",
			DependsOn: []string{"backend"},
			Inputs:    []flow.Input{{Ref: "backend.missing", Optional: true}},
		},
	}

	result, err := orchestrator.CollectArtifacts("/tmp", "", allStages[1], allStages)
	if err != nil {
		t.Fatalf("optional artifact should not cause error: %v", err)
	}
	// Result may be empty or have no content for this artifact — that's fine
	_ = result
}
```

- [ ] **Step 5: Run all tests to verify they fail**

Run: `go test ./pkg/orchestrator/ -run "TestCollectArtifacts" -v`
Expected: compilation error — `CollectArtifacts` undefined.

- [ ] **Step 6: Implement `CollectArtifacts`**

Add to `pkg/orchestrator/orchestrator.go`:

```go
// resolveArtifactPath resolves an artifact path to an absolute file path.
// Paths starting with "./" are relative to the stage's run directory.
// All other paths are relative to the project directory.
func resolveArtifactPath(projectDir, runDir, stageID, artifactPath string) string {
	if strings.HasPrefix(artifactPath, "./") {
		return filepath.Join(runDir, stageID, artifactPath[2:])
	}
	return filepath.Join(projectDir, artifactPath)
}

// CollectArtifacts reads artifact files referenced by a stage's Inputs
// and returns a formatted prompt section. Returns an error if a required
// artifact file is missing.
func CollectArtifacts(projectDir, runDir string, stage flow.Stage, allStages []flow.Stage) (string, error) {
	if len(stage.Inputs) == 0 {
		return "", nil
	}

	// Build index: stageID -> artifactName -> Artifact
	artIndex := make(map[string]map[string]flow.Artifact, len(allStages))
	for _, s := range allStages {
		m := make(map[string]flow.Artifact, len(s.Artifacts))
		for _, a := range s.Artifacts {
			m[a.Name] = a
		}
		artIndex[s.ID] = m
	}

	// Build name index for stage names
	nameIndex := make(map[string]string, len(allStages))
	for _, s := range allStages {
		nameIndex[s.ID] = s.Name
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Artifacts\n")
	hasContent := false

	for _, inp := range stage.Inputs {
		parts := strings.SplitN(inp.Ref, ".", 2)
		stageID, artName := parts[0], parts[1]
		art := artIndex[stageID][artName]

		resolved := resolveArtifactPath(projectDir, runDir, stageID, art.Path)

		if art.IsInline() {
			data, err := os.ReadFile(resolved)
			if err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			buf.WriteString(fmt.Sprintf("\n### %s (from %s): %s\n\n", artName, stageID, art.Description))
			buf.Write(data)
			buf.WriteString("\n")
			hasContent = true
		} else {
			if _, err := os.Stat(resolved); err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			buf.WriteString(fmt.Sprintf("\n### %s (from %s): %s\n\nFile path: %s\n(Use Read tool to access this file)\n", artName, stageID, art.Description, resolved))
			hasContent = true
		}
	}

	if !hasContent {
		return "", nil
	}
	return buf.String(), nil
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./pkg/orchestrator/ -run "TestCollectArtifacts" -v`
Expected: all PASS

- [ ] **Step 8: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go
git commit -m "feat: CollectArtifacts — сбор артефактов для промптов с inline/path режимами"
```

---

### Task 5: Wire context into prompt builders and orchestrator loop

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:336-460` (prompt builders + runner calls)
- Test: `pkg/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write failing test for planning prompt with dependency context**

Add to `pkg/orchestrator/orchestrator_test.go`:

```go
func TestIntegration_PlanningPromptIncludesDependencyPlan(t *testing.T) {
	runDir := t.TempDir()

	// Create dependency plan
	depDir := filepath.Join(runDir, "first")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("# First Plan"), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "first", Name: "First", Description: "runs first", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{ID: "second", Name: "Second", Description: "runs after", DependsOn: []string{"first"}, Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
	}

	// Capture the prompt passed to the runner
	var capturedPrompt string
	capturingRunner := &promptCapturingRunner{
		delegate: mockRunner(t, mockPlanningScript),
		onPlanning: func(prompt string) {
			capturedPrompt = prompt
		},
	}

	stageIDs := []string{"first", "second"}
	rs := state.NewRunState(stageIDs)
	// Mark first stage as done so second starts planning
	rs.SetStageStatus("first", state.StatusDone)
	stateFile := filepath.Join(runDir, "state.json")
	if err := rs.Save(stateFile); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:    runDir,
		Stages:    stages,
		State:     rs,
		StateFile: stateFile,
		Config:    cfg,
		Prompts:   orchestrator.DefaultPrompts(),
		Runner:    capturingRunner,
	})

	cancel := autoApprove(orch)
	defer cancel()

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(capturedPrompt, "# First Plan") {
		t.Errorf("planning prompt should contain dependency plan, got:\n%s", capturedPrompt)
	}
}
```

Also add `promptCapturingRunner` helper to `pkg/orchestrator/integration_test.go`:

```go
// promptCapturingRunner wraps a Runner and captures the prompt passed to RunPlanning.
type promptCapturingRunner struct {
	delegate   executor.Runner
	onPlanning func(prompt string)
}

func (r *promptCapturingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	if r.onPlanning != nil {
		r.onPlanning(prompt)
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *promptCapturingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_PlanningPromptIncludesDependencyPlan -v`
Expected: FAIL — prompt does not contain dependency plan content.

- [ ] **Step 3: Modify prompt builders and orchestrator to pass stage context**

In `pkg/orchestrator/orchestrator.go`, update `buildPlanningPrompt` signature and body:

```go
func buildPlanningPrompt(template string, s flow.Stage, dependencyContext string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n\n%s%s%s", template, s.Name, s.Description, extra, dependencyContext)
}
```

Update `buildImplementationPrompt`:

```go
func buildImplementationPrompt(template string, s flow.Stage, plan, dependencyContext string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf("%s\n\n## Stage: %s\n%s\n\n## Plan\n\n%s%s", template, s.Name, dependencyContext, plan, extra)
}
```

Update `buildRevisionPrompt`:

```go
func buildRevisionPrompt(template string, s flow.Stage, prevPlan, feedback, dependencyContext string) string {
	extra := ""
	if len(s.Skills) > 0 {
		extra = "\n\nUse these skills: " + joinStrings(s.Skills)
	}
	return fmt.Sprintf(
		"%s\n\n## Stage: %s\n\n%s%s%s\n\n## Previous plan (needs revision)\n\n%s\n\n## Feedback\n\n%s\n\nRevise the plan according to the feedback above.",
		template, s.Name, s.Description, extra, dependencyContext, prevPlan, feedback,
	)
}
```

Add a helper to build full context string:

```go
// buildStageContext collects dependency plans and artifact contents for a stage's prompt.
func (o *Orchestrator) buildStageContext(s flow.Stage) string {
	plans := CollectDependencyPlans(o.opts.RunDir, s, o.opts.Stages)
	artifacts, _ := CollectArtifacts(".", o.opts.RunDir, s, o.opts.Stages)
	return plans + artifacts
}
```

Update callers in `runPlanningAgent`:

```go
func (o *Orchestrator) runPlanningAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	depCtx := o.buildStageContext(s)
	prompt := buildPlanningPrompt(o.opts.Prompts.Planning, s, depCtx)
	outFile := filepath.Join(stageDir, "plan.md")
	logFile := filepath.Join(stageDir, "planning.log")

	r := o.runnerFor(s)
	if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
}
```

Update `runPlanningWithFeedback`:

```go
func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)

	feedbackData, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	var prevPlan string
	entries, _ := os.ReadDir(stageDir)
	for _, e := range entries {
		if matched, _ := filepath.Match("plan.v*.md", e.Name()); matched {
			data, _ := os.ReadFile(filepath.Join(stageDir, e.Name()))
			prevPlan = string(data)
		}
	}

	depCtx := o.buildStageContext(s)
	prompt := buildRevisionPrompt(o.opts.Prompts.Planning, s, prevPlan, string(feedbackData), depCtx)
	outFile := filepath.Join(stageDir, "plan.md")
	logFile := filepath.Join(stageDir, "planning-revision.log")

	r := o.runnerFor(s)
	if err := r.RunPlanning(ctx, s.Name, prompt, outFile, logFile); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "planning"})
}
```

Update `runImplementationAgent`:

```go
func (o *Orchestrator) runImplementationAgent(ctx context.Context, s flow.Stage) {
	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	planData, err := os.ReadFile(filepath.Join(stageDir, "plan.md"))
	if err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	depCtx := o.buildStageContext(s)
	prompt := buildImplementationPrompt(o.opts.Prompts.Implementation, s, string(planData), depCtx)
	logFile := filepath.Join(stageDir, "implementation.log")

	r := o.runnerFor(s)
	if err := r.RunAgent(ctx, "implementation", s.Name, prompt, logFile); err != nil {
		o.setStatus(s.ID, state.StatusFailed)
		return
	}

	if s.HasAgent(flow.AgentReview) {
		reviewPrompt := buildReviewPrompt(o.opts.Prompts.Review, s)
		reviewLog := filepath.Join(stageDir, "review.log")
		if err := r.RunAgent(ctx, "review", s.Name, reviewPrompt, reviewLog); err != nil {
			o.setStatus(s.ID, state.StatusFailed)
			return
		}
	}

	o.bus.Publish(Event{Type: EventAgentCompleted, StageID: s.ID, Data: "implementation"})
}
```

- [ ] **Step 4: Run the integration test to verify it passes**

Run: `go test ./pkg/orchestrator/ -run TestIntegration_PlanningPromptIncludesDependencyPlan -v`
Expected: PASS

- [ ] **Step 5: Run all tests to check for regressions**

Run: `go test ./... -v -race`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/orchestrator_test.go pkg/orchestrator/integration_test.go
git commit -m "feat: передача контекста зависимых стадий (планы + артефакты) в промпты агентов"
```

---

### Task 6: Update README with artifacts/inputs documentation

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add `artifacts` and `inputs` to the Stage fields table**

In `README.md`, add rows to the "Поля стадии" table after `| `max_parallel` |`:

```markdown
| `artifacts` | нет | Файлы, которые стадия производит для других стадий |
| `inputs` | нет | Артефакты из зависимых стадий (`stage.artifact`) |
```

- [ ] **Step 2: Add a section explaining artifacts/inputs usage**

After the "Поля стадии" table, add:

```markdown
### Передача контекста между стадиями

Планы зависимых стадий автоматически добавляются в промпт через `depends_on`. Для передачи файловых артефактов используй `artifacts` + `inputs`:

\```yaml
stages:
  - id: backend
    artifacts:
      - name: api-contract
        path: docs/api-contract.yaml
        description: "OpenAPI schema"
      - name: db-schema
        path: ./schema.sql           # ./ = stage-директория в run
        description: "SQL миграция"
        inline: false                 # передать путь, не содержимое

  - id: frontend
    depends_on: [backend]
    inputs:
      - backend.api-contract          # обязательный артефакт
      - ref: backend.db-schema        # опциональный
        optional: true
\```

- `inline: true` (по умолчанию) — содержимое файла вставляется в промпт
- `inline: false` — в промпт передаётся путь к файлу
- `optional: true` — если файл не найден, стадия запускается без него
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: добавить описание artifacts/inputs в README"
```

---

### Task 7: Run full test suite and lint

**Files:** none (verification only)

- [ ] **Step 1: Run all tests**

Run: `make test`
Expected: all PASS

- [ ] **Step 2: Run linter**

Run: `make lint`
Expected: no errors

- [ ] **Step 3: Fix any lint issues found**

If linter reports issues, fix them in the relevant files.

- [ ] **Step 4: Commit lint fixes if any**

```bash
git add -A
git commit -m "fix: исправить замечания линтера"
```
