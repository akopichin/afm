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
// with memory v3 enabled end-to-end (RunDir/Memory/MemoryDir) for a single
// reflect:{mode:rw} stage, exactly the shape production wires in
// cmd/afm/run.go. Returns the orchestrator plus the resolved paths/dirs the
// test needs.
func newMemoryIntegrationOrchestrator(t *testing.T) (o *Orchestrator, stage flow.Stage, runDir, stageDir, memDir string) {
	t.Helper()
	stage = flow.Stage{
		ID:      "s1",
		Name:    "Build",
		Reflect: &flow.Reflect{File: "build.md", Mode: flow.ReflectModeRW},
		Agents:  []flow.AgentType{flow.AgentImplementation},
	}

	runDir = t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// The memory directory lives outside the run dir (cross-run, in the
	// repo), same split as production.
	memDir = t.TempDir()

	o = New(Options{
		RunDir:    runDir,
		Stages:    []flow.Stage{stage},
		Store:     store,
		Config:    config.Default(),
		Memory:    flow.MemoryConfig{MaxRules: 25},
		MemoryDir: memDir,
	})

	// completeStage only calls maybeRunReflection once the stage's own agent
	// has already created its directory (see runAutonomousAgent/
	// runImplementationAgent — MkdirAll happens before the first RunAgent
	// call). Reproduce that precondition by hand since no real agent runs
	// here — production dirs exist, tests must create them.
	stageDir = filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return o, stage, runDir, stageDir, memDir
}

// TestIntegration_MemoryV3_PipelineWritesStageFile drives a reflect:{mode:rw}
// stage through maybeRunReflection (the real trigger point completeStage
// calls, see reflection.go) with a stubbed runMemoryAgent, then checks the
// full v3 write path: reflect_dataset.yaml/patterns.md/prioritized.md/high.md
// land in the stage dir, and the stage's own memory file is rewritten with
// the merged High patterns.
func TestIntegration_MemoryV3_PipelineWritesStageFile(t *testing.T) {
	o, stage, _, stageDir, memDir := newMemoryIntegrationOrchestrator(t)
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	if got := strings.Join(order, ","); got != "reflect,aggregate,prioritize,update" {
		t.Fatalf("pipeline order = %q, want reflect,aggregate,prioritize,update", got)
	}

	for _, f := range []string{"reflect_dataset.yaml", "patterns.md", "prioritized.md", "high.md"} {
		if data, err := os.ReadFile(filepath.Join(stageDir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		} else if len(data) == 0 {
			t.Errorf("%s is empty", f)
		}
	}

	target := memory.StageFile(memDir, stage.Reflect.File)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("stage memory file not written: %v", err)
	}
	if !strings.Contains(string(data), "# Project rules") {
		t.Errorf("stage memory file missing expected content: %q", string(data))
	}
}

// TestIntegration_MemoryV3_PointerReachesLaterStagePrompt runs the full
// pipeline for stage s1 (mode:rw — both writes AND reads its own file), then
// verifies a later stage's prompt carries the memory pointer via the actual
// prompts.Build path every runXxxAgent uses (agents.go: MemoryBlock:
// o.memoryBlockForStage(s)) — the project file always, the stage's own file
// only when its mode allows reading.
func TestIntegration_MemoryV3_PointerReachesLaterStagePrompt(t *testing.T) {
	o, stage, _, _, memDir := newMemoryIntegrationOrchestrator(t)
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	// s1 itself (mode:rw) must see its own file pointed at.
	blockForSelf := o.memoryBlockForStage(stage)
	if blockForSelf == "" {
		t.Fatal("memoryBlockForStage(s1) must not be empty after s1 wrote its own file")
	}
	if !strings.Contains(blockForSelf, memory.StageFile(memDir, stage.Reflect.File)) {
		t.Errorf("block for s1 must name its own memory file:\n%s", blockForSelf)
	}

	// A later, unrelated stage without its own reflect config must NOT see
	// s1's file, but the project memory.md doesn't exist yet (no end-of-run
	// pass has happened), so the block is empty.
	laterStage := flow.Stage{ID: "s2", Name: "Deploy"}
	blockForLater := o.memoryBlockForStage(laterStage)
	if strings.Contains(blockForLater, memory.StageFile(memDir, stage.Reflect.File)) {
		t.Errorf("later stage without reflect must not see s1's own file:\n%s", blockForLater)
	}

	// Now run the end-of-run pass — memory.md is created — and confirm it
	// reaches the later stage's built prompt.
	o.runEndOfRunMemory(context.Background())
	blockForLater = o.memoryBlockForStage(laterStage)
	if blockForLater == "" {
		t.Fatal("memoryBlockForStage(later) must not be empty once memory.md exists")
	}
	projFile := memory.ProjectFile(memDir)
	if !strings.Contains(blockForLater, projFile) {
		t.Errorf("block for later stage must name the project memory file:\n%s", blockForLater)
	}
	built := prompts.Build(prompts.Inputs{Stage: laterStage, MemoryBlock: blockForLater})
	if !strings.Contains(built, projFile) {
		t.Errorf("built prompt missing project memory path:\n%s", built)
	}
}
