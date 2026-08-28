package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// newMemoryIntegrationOrchestrator wires a real *state.Store + *Orchestrator
// with memory enabled end-to-end (RunDir/Memory/MemoryProjectPath/
// MemorySessionPath) for a single reflect:true stage, exactly the shape
// production wires in cmd/afm/run.go. Returns the orchestrator plus the
// resolved paths/dirs the test needs.
func newMemoryIntegrationOrchestrator(t *testing.T, retrievalThreshold, coreConfirmCount int) (o *Orchestrator, stage flow.Stage, runDir, stageDir, projPath, sessPath string) {
	t.Helper()
	stage = flow.Stage{
		ID:      "s1",
		Name:    "Build",
		Reflect: true,
		Agents:  []flow.AgentType{flow.AgentImplementation},
	}

	runDir = t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// PROJECT_MEMORY.yaml lives outside the run dir (cross-run, in the repo);
	// SESSION_MEMORY.yaml lives inside it (per-run) — same split as production.
	projPath = filepath.Join(t.TempDir(), "PROJECT_MEMORY.yaml")
	sessPath = filepath.Join(runDir, "SESSION_MEMORY.yaml")

	o = New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: config.Default(),
		Memory: flow.MemoryConfig{
			ProjectFile:        projPath,
			MaxFindings:        60,
			RetrievalThreshold: retrievalThreshold,
			CoreConfirmCount:   coreConfirmCount,
		},
		MemoryProjectPath: projPath,
		MemorySessionPath: sessPath,
	})

	// completeStage only calls maybeRunReflection once the stage's own agent
	// has already created its directory (see runAutonomousAgent/
	// runImplementationAgent — MkdirAll happens before the first RunAgent
	// call). Reproduce that precondition by hand since no real agent runs
	// here — production dirs exist, tests must create them.
	stageDir = filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	return o, stage, runDir, stageDir, projPath, sessPath
}

// consolidatedFindingYAML builds a MergedStore YAML (as the consolidator
// agent would emit) for a single brand-new project-scope finding with the
// given statement/topic — real evidence attached, status: new so Reconcile
// stamps first_seen=last_seen=<run-id> and confirm_count=1.
func consolidatedFindingYAML(statement, topic string) string {
	return "findings:\n" +
		"  - scope: project\n" +
		"    kind: best_practice\n" +
		"    topic: [" + topic + "]\n" +
		"    statement: " + statement + "\n" +
		"    evidence: s1/autonomous.log:1\n" +
		"    status: new\n"
}

// stubReflectThenConsolidate wires runMemoryAgent to simulate the two v2
// agents' file effects exactly like reflection_test.go's pipelineHarness:
// reflect writes an empty candidate dataset (afm never reads it back —
// only the consolidator's output feeds reconcileAndSave), consolidator
// writes consolidatedYAML verbatim to its outPath.
func stubReflectThenConsolidate(o *Orchestrator, consolidatedYAML string) *[]string {
	order := &[]string{}
	o.runMemoryAgent = func(_ context.Context, spec memoryAgentSpec) error {
		*order = append(*order, spec.kind)
		switch spec.kind {
		case memoryKindReflect:
			return os.WriteFile(spec.datasetOut, []byte("findings: []\n"), 0644)
		case memoryKindConsolidator:
			return os.WriteFile(spec.outPath, []byte(consolidatedYAML), 0644)
		default:
			return nil
		}
	}
	return order
}

// TestIntegration_MemoryV2_PipelineWritesValidatedFinding drives a
// reflect:true stage through maybeRunReflection (the real trigger point
// completeStage calls, see reflection.go) with a stubbed runMemoryAgent, then
// checks the full v2 write path: (a) reflect_dataset.yaml + consolidated.yaml
// land in the stage dir; (b) memory.Load(projectPath) returns exactly one
// validated finding with afm-owned metadata (ConfirmCount==1,
// FirstSeen==LastSeen==<run-id>).
func TestIntegration_MemoryV2_PipelineWritesValidatedFinding(t *testing.T) {
	o, stage, runDir, stageDir, projPath, sessPath := newMemoryIntegrationOrchestrator(t, 25, 3)
	consolidated := consolidatedFindingYAML(
		"the build stage always runs go vet before go test",
		"build",
	)
	order := stubReflectThenConsolidate(o, consolidated)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	if got := strings.Join(*order, ","); got != "reflect,consolidator" {
		t.Fatalf("pipeline order = %q, want exactly reflect,consolidator (no compressor in v2)", got)
	}

	// (a) both per-stage byproduct files exist in the stage dir.
	datasetPath := filepath.Join(stageDir, "reflect_dataset.yaml")
	if data, err := os.ReadFile(datasetPath); err != nil {
		t.Errorf("reflect_dataset.yaml not written: %v", err)
	} else if len(data) == 0 {
		t.Error("reflect_dataset.yaml is empty")
	}
	consolidatedPath := filepath.Join(stageDir, "consolidated.yaml")
	if data, err := os.ReadFile(consolidatedPath); err != nil {
		t.Errorf("consolidated.yaml not written: %v", err)
	} else if len(data) == 0 {
		t.Error("consolidated.yaml is empty")
	}

	// (b) the reconciled, validated finding landed in PROJECT_MEMORY.yaml
	// with afm-assigned metadata — not whatever the (stubbed) agent said.
	projStore, err := memory.Load(projPath)
	if err != nil {
		t.Fatalf("PROJECT_MEMORY store did not parse: %v", err)
	}
	if len(projStore.Findings) != 1 {
		t.Fatalf("PROJECT_MEMORY store: want 1 finding, got %d: %+v", len(projStore.Findings), projStore.Findings)
	}
	f := projStore.Findings[0]
	runID := filepath.Base(runDir)
	if f.ConfirmCount != 1 {
		t.Errorf("ConfirmCount = %d, want 1 (brand-new finding)", f.ConfirmCount)
	}
	if f.FirstSeen != runID {
		t.Errorf("FirstSeen = %q, want run-id %q", f.FirstSeen, runID)
	}
	if f.LastSeen != runID {
		t.Errorf("LastSeen = %q, want run-id %q", f.LastSeen, runID)
	}
	if !f.Valid() {
		t.Errorf("reconciled finding must be Valid(): %+v", f)
	}
	if f.Evidence == "" {
		t.Error("finding must carry non-empty evidence (P1 requirement)")
	}

	// SESSION_MEMORY.yaml must also exist (reset at run start) even though
	// this test's finding is project-scoped only.
	if _, err := memory.Load(sessPath); err != nil {
		t.Errorf("SESSION_MEMORY store did not parse: %v", err)
	}
}

// TestIntegration_MemoryV2_RetrievalInlinesFindingIntoPrompt runs the same
// full pipeline, then forces a LARGE store (seeds enough filler findings via
// memory.Save to exceed RetrievalThreshold) so a later stage's retrieval
// takes the "inline relevant slice" path (P2) rather than the pointer path.
// Verifies the finding's statement — not just a file path — survives
// o.memoryBlockForStage + prompts.Build into the actual assembled prompt of
// a later, topically-related stage.
func TestIntegration_MemoryV2_RetrievalInlinesFindingIntoPrompt(t *testing.T) {
	const threshold = 5
	o, stage, _, _, projPath, _ := newMemoryIntegrationOrchestrator(t, threshold, 3)
	consolidated := consolidatedFindingYAML(
		"database migrations must run with the -allow-destructive flag in staging",
		"database",
	)
	stubReflectThenConsolidate(o, consolidated)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	// Push the store size past the threshold with unrelated filler findings,
	// keeping the pipeline-produced finding intact — memory.Save overwrites
	// the whole file, so load-then-append-then-save.
	current, err := memory.Load(projPath)
	if err != nil {
		t.Fatalf("failed to load store before seeding filler: %v", err)
	}
	if len(current.Findings) != 1 {
		t.Fatalf("precondition: want 1 finding from the pipeline, got %d", len(current.Findings))
	}
	for i := 0; i < threshold+3; i++ {
		current.Findings = append(current.Findings, memory.Finding{
			ID:           "filler-" + string(rune('a'+i)),
			Scope:        memory.ScopeProject,
			Kind:         memory.KindFact,
			Topic:        []string{"unrelated"},
			Statement:    "some unrelated low-confidence filler finding",
			Evidence:     "e:1",
			FirstSeen:    "r0",
			LastSeen:     "r0",
			ConfirmCount: 1,
		})
	}
	if err := memory.Save(projPath, current); err != nil {
		t.Fatalf("failed to seed filler findings: %v", err)
	}

	// Sanity: the store is now large enough that Select must filter, not
	// "inject all" (otherwise this test would pass for the wrong reason).
	total := len(current.Findings)
	if total <= threshold {
		t.Fatalf("test setup bug: total findings %d must exceed threshold %d", total, threshold)
	}

	laterStage := flow.Stage{
		ID:          "database-migration",
		Name:        "Database migration",
		Description: "migrate the database schema in staging",
	}
	block := o.memoryBlockForStage(laterStage)
	if block == "" {
		t.Fatal("memoryBlockForStage returned empty for a large, relevant store")
	}
	if !strings.Contains(block, "database migrations must run with the -allow-destructive flag in staging") {
		t.Errorf("inlined memory block missing the relevant finding's statement:\n%s", block)
	}
	if strings.Contains(block, "some unrelated low-confidence filler finding") {
		t.Errorf("inlined memory block must NOT contain the irrelevant filler finding:\n%s", block)
	}

	// The block reaches the actual assembled prompt via prompts.Build, the
	// same path every runXxxAgent uses (agents.go: MemoryBlock: o.memoryBlockForStage(s)).
	built := prompts.Build(prompts.Inputs{Stage: laterStage, MemoryBlock: block})
	if !strings.Contains(built, "database migrations must run with the -allow-destructive flag in staging") {
		t.Errorf("built prompt missing the finding's statement:\n%s", built)
	}
}

// TestIntegration_MemoryV2_RetrievalPointerForSmallStore is the small-store
// counterpart: a single finding, well under RetrievalThreshold, must reach
// the later stage's prompt as a POINTER (both absolute file paths) rather
// than inlined content — the v1 behavior preserved for the "not enough
// findings yet to bother filtering" case.
func TestIntegration_MemoryV2_RetrievalPointerForSmallStore(t *testing.T) {
	o, stage, _, _, projPath, sessPath := newMemoryIntegrationOrchestrator(t, 25, 3)
	consolidated := consolidatedFindingYAML(
		"the build stage always runs go vet before go test",
		"build",
	)
	stubReflectThenConsolidate(o, consolidated)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	laterStage := flow.Stage{ID: "s2", Name: "Deploy", Description: "deploy the build artifact"}
	block := o.memoryBlockForStage(laterStage)
	if block == "" {
		t.Fatal("memoryBlockForStage returned empty for a small, non-empty store")
	}
	if !strings.Contains(block, projPath) || !strings.Contains(block, sessPath) {
		t.Errorf("small-store block must point at both absolute paths:\n%s", block)
	}
	if strings.Contains(block, "go vet before go test") {
		t.Errorf("small-store block must NOT inline finding content, only point at files:\n%s", block)
	}

	built := prompts.Build(prompts.Inputs{Stage: laterStage, MemoryBlock: block})
	if !strings.Contains(built, projPath) || !strings.Contains(built, sessPath) {
		t.Errorf("built prompt missing memory pointer paths:\n%s", built)
	}
}
