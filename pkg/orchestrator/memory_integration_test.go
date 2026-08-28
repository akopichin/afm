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
			ProjectFile:     proj,
			MaxBytes:        1000,
			CompressRetries: 2,
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
			return os.WriteFile(spec.datasetOut, []byte("project_level: []\nsession_level: []\n"), 0644)
		case memoryKindUpdater:
			if err := os.WriteFile(spec.projectPath, []byte("# PROJECT MEMORY\n\n🟩 Best Practices\n- Run tests with -race.\n"), 0644); err != nil {
				return err
			}
			return os.WriteFile(spec.sessionPath, []byte("# SESSION MEMORY\n\nStage s1 completed cleanly.\n"), 0644)
		default:
			return nil
		}
	}

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	if got := strings.Join(order, ","); !strings.HasPrefix(got, "reflect,updater") {
		t.Fatalf("pipeline order = %q, want reflect,updater,…", got)
	}

	// (1) reflect_dataset.yaml exists in the stage dir.
	datasetPath := filepath.Join(stageDir, "reflect_dataset.yaml")
	if data, err := os.ReadFile(datasetPath); err != nil {
		t.Errorf("reflect_dataset.yaml not written: %v", err)
	} else if len(data) == 0 {
		t.Error("reflect_dataset.yaml is empty")
	}

	// (2) both memory files exist and are non-empty.
	projData, err := os.ReadFile(proj)
	if err != nil {
		t.Fatalf("PROJECT_MEMORY.md not written: %v", err)
	}
	if len(projData) == 0 {
		t.Error("PROJECT_MEMORY.md is empty")
	}
	sessData, err := os.ReadFile(sess)
	if err != nil {
		t.Fatalf("SESSION_MEMORY.md not written: %v", err)
	}
	if len(sessData) == 0 {
		t.Error("SESSION_MEMORY.md is empty")
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
