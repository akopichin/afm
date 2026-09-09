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
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/state"
)

// nonEmptyReflectDataset/emptyReflectDataset — canonical reflect_dataset.yaml
// bodies used across this file's tests: the new engine (DistillTarget, see
// pkg/memorypipeline/distill.go) actually PARSES dataset content and skips
// the whole aggregate/prioritize/update chain when every dataset is empty
// (datasetsAllEmpty) — unlike the old per-stage `distill` helper this task
// replaces, which called aggregate unconditionally. Tests exercising the real
// distill chain must feed it a non-empty dataset; tests exercising the
// NoHigh/no-op path use the empty one on purpose.
const nonEmptyReflectDataset = `project_level:
  - prompt: "p1"
    chosen: "c1"
    rejected: "r1"
    source: log
session_level: []
`

const emptyReflectDataset = "project_level: []\nsession_level: []\n"

// newReflectionTestOrchestrator builds an *Orchestrator over a real
// *state.Store on a temp run dir with the given stage — reflection_test.go
// needs a stage carrying a specific Reflect config, unlike the generic
// newTestOrchestrator (memory_agent_test.go), which hardcodes a plain stage.
func newReflectionTestOrchestrator(t *testing.T, stage flow.Stage) (*Orchestrator, string) {
	t.Helper()
	return newReflectionTestOrchestratorMulti(t, []flow.Stage{stage})
}

// newReflectionTestOrchestratorMulti is the multi-stage variant — needed by
// tests exercising the project-wide aggregate (which spans several stages)
// and stages sharing one reflect.File (chained per-group distill).
func newReflectionTestOrchestratorMulti(t *testing.T, stages []flow.Stage) (*Orchestrator, string) {
	t.Helper()
	runDir := t.TempDir()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := New(Options{RunDir: runDir, Stages: stages, Store: store, Config: config.Default()})
	return o, runDir
}

// stubMemoryPipeline builds a memorypipeline.Pipeline whose agent runner
// simulates the v3 chain's file effects for each step and records the call
// order — the o.mem injection point tests use instead of the removed
// o.memRunner field (Task 10: o.mem is now a *memorypipeline.Pipeline, and
// tests override its runner via memorypipeline.WithRunner).
func stubMemoryPipeline(order *[]string) *memorypipeline.Pipeline {
	return memorypipeline.New(memorypipeline.Prompts{}, memorypipeline.AgentConfig{}, memorypipeline.WithRunner(
		func(_ context.Context, spec memorypipeline.AgentSpec) error {
			*order = append(*order, spec.Kind)
			switch spec.Kind {
			case memorypipeline.KindReflect:
				return os.WriteFile(spec.DatasetOut, []byte(nonEmptyReflectDataset), 0o644)
			case memorypipeline.KindAggregate:
				return os.WriteFile(spec.Out, []byte("1. P — desc\n"), 0o644)
			case memorypipeline.KindPrioritize:
				return os.WriteFile(spec.Out, []byte("## High\n1. P — desc\n\n## Medium\n1. m\n\n## Low\n1. l\n"), 0o644)
			case memorypipeline.KindUpdate:
				return os.WriteFile(spec.TargetFile, []byte("# Project rules\n\n## P\n\ndesc\n"), 0o644)
			default:
				return nil
			}
		}))
}

// writeAgentSession drops a minimal recognizable agent-session artifact into
// stageDir so memorypipeline.SourceInventory's HasAgentSession() is true —
// CaptureStage StepErrors on a stage dir with nothing but a script log or
// nothing at all (see rebuild.go's isWriteReflect/HasAgentSession gate).
func writeAgentSession(t *testing.T, stageDir string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("a plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeRunReflection_NoOpWhenDisabled(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = "" // disabled
	writeAgentSession(t, filepath.Join(runDir, stage.ID))
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if len(order) != 0 {
		t.Errorf("must not run any memory agent when memory disabled, got %v", order)
	}
}

// Per-stage теперь ТОЛЬКО каптурит датасет (reflect_dataset.yaml) через
// memorypipeline.Pipeline.CaptureStage. Вся сборка (aggregate/prioritize/
// update) перенесена в end-of-run — после каждой стадии ничего не строится,
// поэтому память видит все стадии вместе, а сборка бежит один раз в конце
// (см. runEndOfRunMemory).
func TestMaybeRunReflection_CapturesDatasetOnly(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	stageDir := filepath.Join(runDir, stage.ID)
	writeAgentSession(t, stageDir)
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()

	if got := strings.Join(order, ","); got != "reflect" {
		t.Errorf("order = %q, want just reflect (build moved to end-of-run)", got)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "reflect_dataset.yaml")); err != nil {
		t.Errorf("reflect_dataset.yaml must be captured per-stage: %v", err)
	}
	if _, err := os.Stat(memory.StageFile(o.opts.MemoryDir, "s.md")); err == nil {
		t.Error("stage memory file must NOT be built per-stage anymore (moved to end-of-run)")
	}
}

func TestMaybeRunReflection_ModeR_DoesNotWrite(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeR}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	writeAgentSession(t, filepath.Join(runDir, stage.ID))
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if len(order) != 0 {
		t.Errorf("mode:r must not run the write chain, got %v", order)
	}
}

func TestMaybeRunReflection_ScriptSkipped(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Script: "echo hi", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	writeAgentSession(t, filepath.Join(runDir, stage.ID))
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.maybeRunReflection(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if len(order) != 0 {
		t.Errorf("script stages have no agent session — must not run reflection, got %v", order)
	}
}

// TestEndOfRunMemory_BuildsPerStageFiles — собственный файл стадии с
// reflect:write и общий memory.md оба строятся В КОНЦЕ рана (через
// Pipeline.Finalize под memory-lock'ом), из уже накопленного на диске
// датасета.
func TestEndOfRunMemory_BuildsPerStageFiles(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW}

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.runEndOfRunMemory(context.Background())

	if _, err := os.Stat(memory.StageFile(o.opts.MemoryDir, "s.md")); err != nil {
		t.Errorf("per-stage reflect file must be built at end-of-run: %v", err)
	}
	if _, err := os.Stat(memory.ProjectFile(o.opts.MemoryDir)); err != nil {
		t.Errorf("shared memory.md must be built at end-of-run: %v", err)
	}
	// per-stage distill (aggregate,prioritize,update) + project distill (то же).
	if got := strings.Join(order, ","); got != "aggregate,prioritize,update,aggregate,prioritize,update" {
		t.Errorf("order = %q, want two distill passes (stage file + project memory.md)", got)
	}

	// A second call is a no-op (finalReflectDone).
	before := len(order)
	o.runEndOfRunMemory(context.Background())
	if len(order) != before {
		t.Error("end-of-run memory ran twice; finalReflectDone not honored")
	}
}

// TestEndOfRunMemory_WritesProjectFile — memory.md aggregates ALL eligible
// stage datasets of the run (not just one), built explicitly from
// o.opts.Stages (review #13) rather than a filesystem Glob.
func TestEndOfRunMemory_WritesProjectFile(t *testing.T) {
	s1 := flow.Stage{ID: "s1", Name: "Stage1", Reflect: &flow.Reflect{File: "s1.md", Mode: flow.ReflectModeRW}}
	s2 := flow.Stage{ID: "s2", Name: "Stage2", Reflect: &flow.Reflect{File: "s2.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestratorMulti(t, []flow.Stage{s1, s2})
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW}

	for _, id := range []string{"s1", "s2"} {
		dir := filepath.Join(runDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var calls []string
	var projectAggInputs []string
	o.mem = memorypipeline.New(memorypipeline.Prompts{}, memorypipeline.AgentConfig{}, memorypipeline.WithRunner(
		func(_ context.Context, spec memorypipeline.AgentSpec) error {
			calls = append(calls, spec.Kind)
			if spec.Kind == memorypipeline.KindAggregate && spec.StageName == "flow-memory" {
				projectAggInputs = append([]string{}, spec.InPaths...)
			}
			switch spec.Kind {
			case memorypipeline.KindAggregate:
				return os.WriteFile(spec.Out, []byte("1. P — desc\n"), 0o644)
			case memorypipeline.KindPrioritize:
				return os.WriteFile(spec.Out, []byte("## High\n1. P — desc\n\n## Medium\n\n## Low\n"), 0o644)
			case memorypipeline.KindUpdate:
				return os.WriteFile(spec.TargetFile, []byte("# Project rules\n\n## P\n\ndesc\n"), 0o644)
			}
			return nil
		}))

	o.runEndOfRunMemory(context.Background())

	projFile := memory.ProjectFile(o.opts.MemoryDir)
	data, err := os.ReadFile(projFile)
	if err != nil {
		t.Fatalf("project memory file not written: %v", err)
	}
	if !strings.Contains(string(data), "# Project rules") {
		t.Errorf("project memory file missing expected content: %q", string(data))
	}
	if len(projectAggInputs) != 2 {
		t.Errorf("project aggregate inputs=%v want both stage datasets", projectAggInputs)
	}

	// A second call is a no-op (finalReflectDone).
	before := len(calls)
	o.runEndOfRunMemory(context.Background())
	if len(calls) != before {
		t.Error("end-of-run memory ran twice; finalReflectDone not honored")
	}
}

// TestEndOfRunMemory_ModeReadOnlyDoesNotWriteProject — memory.mode:r is a
// READ-only gate on the shared project file specifically: a stage's OWN
// reflect file is still governed by its own reflect.mode (independent axis,
// see flow.Reflect/MemoryConfig doc comments) and is still built.
func TestEndOfRunMemory_ModeReadOnlyDoesNotWriteProject(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeR} // read-only GLOBAL memory

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.runEndOfRunMemory(context.Background())

	if _, err := os.Stat(memory.StageFile(o.opts.MemoryDir, "s.md")); err != nil {
		t.Errorf("per-stage reflect file must still be built (governed by reflect.mode, not memory.mode): %v", err)
	}
	if _, err := os.Stat(memory.ProjectFile(o.opts.MemoryDir)); err == nil {
		t.Error("memory.mode:r must NOT write memory.md at end of run")
	}
	// Only ONE distill pass (the stage's own file) — no second pass for the
	// read-only project file.
	if got := strings.Join(order, ","); got != "aggregate,prioritize,update" {
		t.Errorf("order = %q, want a single distill pass (stage file only)", got)
	}
}

func TestEndOfRunMemory_NoOpWithoutDatasets(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, _ := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.runEndOfRunMemory(context.Background())
	if len(order) != 0 {
		t.Errorf("must not run the chain when no stage left a dataset, got %v", order)
	}
	if !o.finalReflectDone {
		t.Error("finalReflectDone must still be set even on the no-dataset no-op path")
	}
}

// TestEndOfRunMemory_EmptyDatasetKeepsPriorRules — a stage whose captured
// dataset has empty project_level AND session_level sections is a genuine
// no-op through the new engine's datasetsAllEmpty short-circuit (see
// DistillTarget in pkg/memorypipeline/distill.go): no aggregate/prioritize/
// update call happens at all, and a pre-existing target file is left
// byte-for-byte unchanged (Finalize only publishes Changed targets).
func TestEndOfRunMemory_EmptyDatasetKeepsPriorRules(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeR} // isolate: no project file involved

	target := memory.StageFile(o.opts.MemoryDir, "s.md")
	const prior = "# Project rules\n\n## Old Pattern\n\nDescription of the old pattern.\n"
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(emptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	o.mem = stubMemoryPipeline(&order)

	o.runEndOfRunMemory(context.Background())

	if len(order) != 0 {
		t.Errorf("an empty dataset must not call any distill agent, got %v", order)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("stage memory file disappeared: %v", err)
	}
	if string(got) != prior {
		t.Errorf("empty dataset must keep prior rules unchanged:\ngot:  %q\nwant: %q", got, prior)
	}
}

// TestEndOfRunMemory_SharedReflectFile_FirstNoHighPreservesPriorRules —
// two stages chained onto the SAME reflect.File (see buildStageGroups in
// pkg/memorypipeline/rebuild.go): the FIRST stage produces no High patterns
// (NoHigh — its candidate stays == the seed unchanged), the SECOND does and
// its update appends to the (chained) candidate. The shared target must end
// up with BOTH the original prior rules (never wiped by the NoHigh stage)
// AND the second stage's contribution.
func TestEndOfRunMemory_SharedReflectFile_FirstNoHighPreservesPriorRules(t *testing.T) {
	s1 := flow.Stage{ID: "s1", Name: "Stage1", Reflect: &flow.Reflect{File: "shared.md", Mode: flow.ReflectModeRW}}
	s2 := flow.Stage{ID: "s2", Name: "Stage2", Reflect: &flow.Reflect{File: "shared.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestratorMulti(t, []flow.Stage{s1, s2})
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeR} // isolate: no project file involved

	target := memory.StageFile(o.opts.MemoryDir, "shared.md")
	const prior = "# Project rules\n\n## Old Pattern\n\nDescription of the old pattern.\n"
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"s1", "s2"} {
		dir := filepath.Join(runDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	o.mem = memorypipeline.New(memorypipeline.Prompts{}, memorypipeline.AgentConfig{}, memorypipeline.WithRunner(
		func(_ context.Context, spec memorypipeline.AgentSpec) error {
			switch spec.Kind {
			case memorypipeline.KindAggregate:
				return os.WriteFile(spec.Out, []byte("1. P — d\n"), 0o644)
			case memorypipeline.KindPrioritize:
				if spec.StageName == "s1" {
					// no "## High" section at all -> SelectHigh returns "" -> NoHigh.
					return os.WriteFile(spec.Out, []byte("## Medium\n\n1. P — d\n\n## Low\n"), 0o644)
				}
				return os.WriteFile(spec.Out, []byte("## High\n\n1. P — d\n\n## Medium\n\n## Low\n"), 0o644)
			case memorypipeline.KindUpdate:
				cur, _ := os.ReadFile(spec.TargetFile)
				return os.WriteFile(spec.TargetFile, []byte(string(cur)+"\n## Appended "+spec.StageName+"\n\nblk\n"), 0o644)
			}
			return nil
		}))

	o.runEndOfRunMemory(context.Background())

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read shared.md: %v", err)
	}
	if !strings.Contains(string(got), "## Old Pattern") {
		t.Errorf("NoHigh first stage wiped the shared seed:\n%s", got)
	}
	if !strings.Contains(string(got), "## Appended s2") {
		t.Errorf("s2 did not chain onto the shared candidate:\n%s", got)
	}
}

// TestEndOfRunMemory_GhostDatasetExcludedFromProjectAggregate — a leftover
// reflect_dataset.yaml under a stage id no longer present in o.opts.Stages
// (e.g. removed from flow.yaml since a previous run) must NOT enter the
// project aggregate. Task 10 brief review #13: this replaces the old
// filepath.Glob(<run>/*/reflect_dataset.yaml), which picked up exactly this
// kind of stale directory.
func TestEndOfRunMemory_GhostDatasetExcludedFromProjectAggregate(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
	o, runDir := newReflectionTestOrchestrator(t, stage)
	o.opts.MemoryDir = t.TempDir()
	o.opts.Memory = flow.MemoryConfig{MaxRules: 25, Mode: flow.ReflectModeRW}

	dir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stray leftover dataset for a stage id NOT in o.opts.Stages at all.
	ghostDir := filepath.Join(runDir, "ghost")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghostDir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}

	var projectAggInputs []string
	o.mem = memorypipeline.New(memorypipeline.Prompts{}, memorypipeline.AgentConfig{}, memorypipeline.WithRunner(
		func(_ context.Context, spec memorypipeline.AgentSpec) error {
			if spec.Kind == memorypipeline.KindAggregate && spec.StageName == "flow-memory" {
				projectAggInputs = append([]string{}, spec.InPaths...)
			}
			switch spec.Kind {
			case memorypipeline.KindAggregate:
				return os.WriteFile(spec.Out, []byte("1. P — desc\n"), 0o644)
			case memorypipeline.KindPrioritize:
				return os.WriteFile(spec.Out, []byte("## High\n1. P — desc\n\n## Medium\n\n## Low\n"), 0o644)
			case memorypipeline.KindUpdate:
				return os.WriteFile(spec.TargetFile, []byte("# Project rules\n\n## P\n\ndesc\n"), 0o644)
			}
			return nil
		}))

	o.runEndOfRunMemory(context.Background())

	for _, p := range projectAggInputs {
		if strings.Contains(p, "ghost") {
			t.Errorf("project aggregate included the ghost dataset: %v", projectAggInputs)
		}
	}
	if len(projectAggInputs) != 1 {
		t.Errorf("project aggregate inputs=%v want exactly 1 (the real stage only)", projectAggInputs)
	}
}

func TestEndOfRunMemory_CommitsWhenEnabled(t *testing.T) {
	stage := flow.Stage{ID: "s1", Name: "Stage", Reflect: &flow.Reflect{File: "s.md", Mode: flow.ReflectModeRW}}
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
	if err := os.WriteFile(filepath.Join(dir, "reflect_dataset.yaml"), []byte(nonEmptyReflectDataset), 0o644); err != nil {
		t.Fatal(err)
	}
	var order []string
	o.mem = stubMemoryPipeline(&order)

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
