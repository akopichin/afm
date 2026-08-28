package orchestrator

import (
	"context"
	"fmt"
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

// testMemoryPointer mirrors the shape of cmd/afm/memory_pointer.go's
// buildMemoryPointer — that helper lives in package main and can't be
// imported from pkg/orchestrator, so this test reconstructs the same text
// (real project/session paths, not stand-ins) to check that prompts.Build
// actually surfaces both absolute paths when they are present in
// GlobalPrompt. See the "pointer assertion" note in the task report for the
// cross-package limitation this works around.
func testMemoryPointer(projectPath, sessionPath string) string {
	return fmt.Sprintf(`Project memory — accumulated findings from earlier stages and runs — lives at:
  %s
Session memory — this run's short-term context — lives at:
  %s
Before you start, read both files and take their Best Practices (🟩) and Anti-Patterns (🟥) into account.`, projectPath, sessionPath)
}

// TestIntegration_MemoryPipelineWritesFilesAndPointerReachesPrompt drives a
// reflect:true stage through maybeRunReflection (the real trigger point
// completeStage calls, see reflection.go) with a stubbed runMemoryAgent that
// simulates reflect/updater file effects, then checks:
//
//  1. reflect_dataset.yaml lands in the stage dir;
//  2. PROJECT_MEMORY.md and SESSION_MEMORY.md exist and are non-empty;
//  3. the memory pointer (both absolute paths) survives a prompts.Build pass,
//     which is how it actually reaches a later stage's assembled prompt in
//     production (cmd/afm/run.go appends it to Options.GlobalPrompt, which
//     every runXxxAgent forwards into prompts.Build as Inputs.GlobalPrompt).
func TestIntegration_MemoryPipelineWritesFilesAndPointerReachesPrompt(t *testing.T) {
	stage := flow.Stage{
		ID:      "s1",
		Name:    "Build",
		Reflect: true,
		Agents:  []flow.AgentType{flow.AgentImplementation},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// PROJECT_MEMORY.md lives outside the run dir (cross-run, in the repo);
	// SESSION_MEMORY.md lives inside it (per-run) — same split as production.
	proj := filepath.Join(t.TempDir(), "PROJECT_MEMORY.md")
	sess := filepath.Join(runDir, "SESSION_MEMORY.md")

	o := New(Options{
		RunDir: runDir,
		Stages: []flow.Stage{stage},
		Store:  store,
		Config: config.Default(),
		Memory: flow.MemoryConfig{
			ProjectFile: proj,
			MaxFindings: 60,
		},
		MemoryProjectPath: proj,
		MemorySessionPath: sess,
	})

	// completeStage only calls maybeRunReflection once the stage's own agent
	// has already created its directory (see runAutonomousAgent/
	// runImplementationAgent — MkdirAll happens before the first RunAgent
	// call). Reproduce that precondition by hand since no real agent runs here.
	stageDir := filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	var order []string
	o.runMemoryAgent = func(_ context.Context, spec memoryAgentSpec) error {
		order = append(order, spec.kind)
		switch spec.kind {
		case memoryKindReflect:
			return os.WriteFile(spec.datasetOut, []byte("findings: []\n"), 0644)
		case memoryKindConsolidator:
			consolidated := "findings:\n" +
				"  - scope: project\n" +
				"    kind: best_practice\n" +
				"    statement: Run tests with -race.\n" +
				"    evidence: s1/autonomous.log:1\n" +
				"    status: new\n" +
				"  - scope: session\n" +
				"    kind: fact\n" +
				"    statement: Stage s1 completed cleanly.\n" +
				"    evidence: s1/execution_summary.md:1\n" +
				"    status: new\n"
			return os.WriteFile(spec.outPath, []byte(consolidated), 0644)
		default:
			return nil
		}
	}

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	if got := strings.Join(order, ","); got != "reflect,consolidator" {
		t.Fatalf("pipeline order = %q, want exactly reflect,consolidator", got)
	}

	// (1) reflect_dataset.yaml exists in the stage dir.
	datasetPath := filepath.Join(stageDir, "reflect_dataset.yaml")
	if data, err := os.ReadFile(datasetPath); err != nil {
		t.Errorf("reflect_dataset.yaml not written: %v", err)
	} else if len(data) == 0 {
		t.Error("reflect_dataset.yaml is empty")
	}

	// (2) both memory stores exist, parse, and hold the reconciled finding.
	projStore, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("PROJECT_MEMORY store did not parse: %v", err)
	}
	if len(projStore.Findings) != 1 {
		t.Errorf("PROJECT_MEMORY store: want 1 finding, got %d", len(projStore.Findings))
	}
	sessStore, err := memory.Load(sess)
	if err != nil {
		t.Fatalf("SESSION_MEMORY store did not parse: %v", err)
	}
	if len(sessStore.Findings) != 1 {
		t.Errorf("SESSION_MEMORY store: want 1 finding, got %d", len(sessStore.Findings))
	}

	// (3) the memory pointer, once placed into GlobalPrompt exactly the way
	// cmd/afm/run.go does, reaches a prompts.Build output verbatim with both
	// absolute paths intact.
	pointer := testMemoryPointer(proj, sess)
	built := prompts.Build(prompts.Inputs{GlobalPrompt: pointer})
	if !strings.Contains(built, proj) {
		t.Errorf("built prompt missing project memory path %q:\n%s", proj, built)
	}
	if !strings.Contains(built, sess) {
		t.Errorf("built prompt missing session memory path %q:\n%s", sess, built)
	}
}
