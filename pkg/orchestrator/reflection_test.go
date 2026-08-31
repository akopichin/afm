package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/state"
)

// newReflectionTestOrchestrator builds an *Orchestrator over a real
// *state.Store on a temp run dir with the given stage — reflection_test.go
// needs a stage carrying a specific Reflect config, unlike the generic
// newTestOrchestrator (memory_agent_test.go), which hardcodes a plain stage.
func newReflectionTestOrchestrator(t *testing.T, stage flow.Stage) (*Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	return o, runDir
}

// stubMemoryAgentByKind wires runMemoryAgent to simulate the v3 chain's file
// effects for each step, and records the call order.
func stubMemoryAgentByKind(o *Orchestrator, order *[]string) {
	o.runMemoryAgent = func(_ context.Context, spec memoryAgentSpec) error {
		*order = append(*order, spec.kind)
		switch spec.kind {
		case memoryKindReflect:
			return os.WriteFile(spec.datasetOut, []byte("project_level: []\nsession_level: []\n"), 0o644)
		case memoryKindAggregate:
			return os.WriteFile(spec.out, []byte("1. P — desc\n"), 0o644)
		case memoryKindPrioritize:
			return os.WriteFile(spec.out, []byte("## High\n1. P — desc\n\n## Medium\n1. m\n\n## Low\n1. l\n"), 0o644)
		case memoryKindUpdate:
			return os.WriteFile(spec.targetFile, []byte("# Project rules\n\n## P\n\ndesc\n"), 0o644)
		default:
			return nil
		}
	}
}

func TestMaybeRunReflection_NoOpWhenDisabled(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = "" // disabled
	if err := os.MkdirAll(filepath.Join(runDir, stage.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	o.runMemoryAgent = func(_ context.Context, _ memoryAgentSpec) error { called = true; return nil }

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if called {
		t.Error("must not run any memory agent when memory disabled")
	}
}

func TestMaybeRunReflection_WritesStageFile(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, stage.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	targetFile := memory.StageFile(o.opts.MemoryDir, "s.md")
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("stage memory file not written: %v", err)
	}
	if !strings.Contains(string(data), "# Project rules") {
		t.Errorf("stage memory file missing expected content: %q", string(data))
	}
	if got := strings.Join(order, ","); got != "reflect,aggregate,prioritize,update" {
		t.Errorf("order = %q, want reflect,aggregate,prioritize,update", got)
	}
}

func TestMaybeRunReflection_ModeR_DoesNotWrite(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeR}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, stage.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	o.runMemoryAgent = func(_ context.Context, _ memoryAgentSpec) error { called = true; return nil }

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if called {
		t.Error("mode:r must not run the write chain")
	}
}

func TestMaybeRunReflection_ScriptSkipped(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Script: "echo hi", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, stage.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	o.runMemoryAgent = func(_ context.Context, _ memoryAgentSpec) error { called = true; return nil }

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if called {
		t.Error("script stages have no agent session — must not run reflection")
	}
}

func TestEndOfRunMemory_WritesProjectFile(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage"}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW}

	for _, id := range []string{"s1", "s2"} {
		dir := filepath.Join(runDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte("project_level: []\nsession_level: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.runEndOfRunMemory(context.Background())

	projFile := memory.ProjectFile(o.opts.MemoryDir)
	data, err := os.ReadFile(projFile)
	if err != nil {
		t.Fatalf("project memory file not written: %v", err)
	}
	if !strings.Contains(string(data), "# Project rules") {
		t.Errorf("project memory file missing expected content: %q", string(data))
	}
	if got := strings.Join(order, ","); got != "aggregate,prioritize,update" {
		t.Errorf("order = %q, want aggregate,prioritize,update (no reflect at end-of-run)", got)
	}

	// A second call is a no-op (finalReflectDone).
	before := len(order)
	o.runEndOfRunMemory(context.Background())
	if len(order) != before {
		t.Error("end-of-run memory ran twice; finalReflectDone not honored")
	}
}

// TestEndOfRunMemory_ModeReadOnlyDoesNotWriteProject — при memory.mode "r"
// глобальная память только для чтения: конец рана НЕ пишет memory.md, даже
// если датасеты стадий есть на диске.
func TestEndOfRunMemory_ModeReadOnlyDoesNotWriteProject(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage"}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeR} // read-only global memory

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte("project_level: []\nsession_level: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.runEndOfRunMemory(context.Background())

	if _, err := os.Stat(memory.ProjectFile(o.opts.MemoryDir)); err == nil {
		t.Error("memory.mode:r must NOT write memory.md at end of run")
	}
	if len(order) != 0 {
		t.Errorf("no distill agents should run for read-only global memory; got %v", order)
	}
}

func TestEndOfRunMemory_NoOpWithoutDatasets(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage"}
	o, _ := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	called := false
	o.runMemoryAgent = func(_ context.Context, _ memoryAgentSpec) error { called = true; return nil }

	o.runEndOfRunMemory(context.Background())
	if called {
		t.Error("must not run the chain when no stage left a dataset")
	}
	if !o.finalReflectDone {
		t.Error("finalReflectDone must still be set even on the no-dataset no-op path")
	}
}

func TestEndOfRunMemory_CommitsWhenEnabled(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage"}
	o, runDir := newReflectionTestOrchestrator(t, stage)

	memDir := t.TempDir()
	runGit(t, memDir, "init")
	runGit(t, memDir, "config", "user.email", "test@example.com")
	runGit(t, memDir, "config", "user.name", "test")
	o.opts.MemoryDir = memDir
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW, Commit: true}

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte("project_level: []\nsession_level: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	stubMemoryAgentByKind(o, &order)

	o.runEndOfRunMemory(context.Background())

	out := runGit(t, memDir, "log", "--oneline")
	if strings.TrimSpace(out) == "" {
		t.Error("expected a commit in memory dir, git log is empty")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}
