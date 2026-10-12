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

// newMemoryIntegrationOrchestrator wires one reflect-enabled stage with a real Store and memory pipeline.
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
		Memory:    flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW, MemoryUse: true},
		MemoryDir: memDir,
	})

	// completeStage only calls maybeRunReflection once the stage's own agent has already created its
	// directory.
	stageDir = filepath.Join(runDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return o, stage, runDir, stageDir, memDir
}

func TestIntegration_MemoryV3_PipelineWritesStageFile(t *testing.T) {
	o, stage, runDir, stageDir, memDir := newMemoryIntegrationOrchestrator(t)
	var order []string
	o.mem = stubMemoryPipeline(&order)

	// Per-stage: только каптура датасета (сборка перенесена в end-of-run).
	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if got := strings.Join(order, ","); got != "reflect" {
		t.Fatalf("per-stage order = %q, want just reflect (build deferred to end-of-run)", got)
	}

	// End-of-run строит файл стадии из её датасета (+ memory.md).
	o.runEndOfRunMemory(context.Background())

	if data, err := os.ReadFile(filepath.Join(stageDir, "reflect_dataset.yaml")); err != nil {
		t.Errorf("reflect_dataset.yaml not written: %v", err)
	} else if len(data) == 0 {
		t.Error("reflect_dataset.yaml is empty")
	}

	// Finalize intermediates live under .memory-finalize attempts so promotion can remain atomic.
	for _, f := range []string{"patterns.md", "prioritized.md", "high.md"} {
		matches, err := filepath.Glob(filepath.Join(runDir, ".memory-finalize", "*", "stages", stage.ID, "distill", f))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Errorf("%s not found in the finalize staging dir: %v", f, matches)
			continue
		}
		if data, err := os.ReadFile(matches[0]); err != nil || len(data) == 0 {
			t.Errorf("%s empty or unreadable: %v", f, err)
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

func TestIntegration_MemoryV3_PointerReachesLaterStagePrompt(t *testing.T) {
	o, stage, _, _, memDir := newMemoryIntegrationOrchestrator(t)
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	// Per-stage память строится в КОНЦЕ рана: до end-of-run ни файла стадии, ни memory.md ещё нет, инъекция
	// пуста.
	if b := o.memoryBlockForStage(stage); b != "" {
		t.Fatalf("before end-of-run nothing is built yet, block must be empty:\n%s", b)
	}

	// End-of-run строит и собственный файл стадии, и memory.md.
	o.runEndOfRunMemory(context.Background())

	// Теперь s1 (mode:rw) видит указатель на свой файл.
	blockForSelf := o.memoryBlockForStage(stage)
	if !strings.Contains(blockForSelf, memory.StageFile(memDir, stage.Reflect.File)) {
		t.Errorf("after end-of-run, block for s1 must name its own memory file:\n%s", blockForSelf)
	}

	// Более поздняя стадия без своего reflect НЕ видит файл s1, но видит memory.md.
	laterStage := flow.Stage{ID: "s2", Name: "Deploy"}
	blockForLater := o.memoryBlockForStage(laterStage)
	if strings.Contains(blockForLater, memory.StageFile(memDir, stage.Reflect.File)) {
		t.Errorf("later stage without reflect must not see s1's own file:\n%s", blockForLater)
	}
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
