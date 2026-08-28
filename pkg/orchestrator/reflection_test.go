package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// pipelineHarness wires an orchestrator with memory enabled and a scripted
// runMemoryAgent stub that simulates the two v2 agents' file effects: reflect
// writes an (unread-by-code) candidate dataset, consolidator writes
// consolidatedYAML verbatim to its outPath — afm code (reconcileAndSave) then
// reconciles/evicts/saves for real, exactly as in production. Returns a
// ready-made, already-created stageDir (<runDir>/s1) — in production the
// stage's own agent always creates its directory before reflection ever
// runs (see maybeRunReflection's caller); tests must reproduce that
// precondition by hand since no real agent runs here.
func pipelineHarness(t *testing.T, maxFindings int, consolidatedYAML string) (o *Orchestrator, proj, sess, stageDir string, order *[]string) {
	t.Helper()
	o = newTestOrchestrator(t)
	runDir := t.TempDir()
	proj = filepath.Join(t.TempDir(), "PROJECT_MEMORY.yaml")
	sess = filepath.Join(runDir, "SESSION_MEMORY.yaml")
	o.opts.RunDir = runDir
	o.opts.Memory = flow.MemoryConfig{ProjectFile: proj, MaxFindings: maxFindings}
	o.opts.MemoryProjectPath = proj
	o.opts.MemorySessionPath = sess

	stageDir = filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	order = &[]string{}
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
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
	return o, proj, sess, stageDir, order
}

func TestReflectionPipeline_ReflectThenConsolidatorThenReconcile(t *testing.T) {
	consolidated := "findings:\n" +
		"  - scope: project\n" +
		"    kind: fact\n" +
		"    statement: uses sqlite\n" +
		"    evidence: config.json:1\n" +
		"    status: new\n"
	o, proj, _, stageDir, order := pipelineHarness(t, 60, consolidated)
	runID := filepath.Base(o.opts.RunDir)

	o.runReflectionPipeline(context.Background(), "s1", []string{filepath.Join(stageDir, "autonomous.log")}, stageDir)

	if got := strings.Join(*order, ","); got != "reflect,consolidator" {
		t.Errorf("order = %q, want exactly reflect,consolidator (no compressor)", got)
	}

	out, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("project memory did not parse: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(out.Findings), out.Findings)
	}
	f := out.Findings[0]
	if f.Statement != "uses sqlite" {
		t.Errorf("unexpected statement: %q", f.Statement)
	}
	if f.ConfirmCount != 1 {
		t.Errorf("ConfirmCount = %d, want 1", f.ConfirmCount)
	}
	if f.FirstSeen != runID || f.LastSeen != runID {
		t.Errorf("FirstSeen/LastSeen = %q/%q, want both %q", f.FirstSeen, f.LastSeen, runID)
	}
}

// TestReflectionPipeline_RetainsExistingFindingNotEchoedByConsolidator is the
// data-loss-guard regression: a prev PROJECT store finding A exists; the
// consolidator stub emits ONLY a brand-new finding B (the LLM failed to echo
// A back at all, despite consolidator.md's "do not silently drop existing
// findings" instruction). reconcileAndSave must retain A unchanged alongside
// B — eviction is the only removal path. Must fail before the fix (A would be
// dropped) and pass after.
func TestReflectionPipeline_RetainsExistingFindingNotEchoedByConsolidator(t *testing.T) {
	consolidatedOnlyB := "findings:\n" +
		"  - scope: project\n" +
		"    kind: fact\n" +
		"    statement: new finding B\n" +
		"    evidence: e-b:1\n" +
		"    status: new\n"
	o, proj, _, stageDir, _ := pipelineHarness(t, 60, consolidatedOnlyB)

	seedA := memory.Finding{
		ID: "finding-a", Scope: memory.ScopeProject, Kind: memory.KindFact,
		Statement: "existing finding A", Evidence: "e-a:1",
		FirstSeen: "r0", LastSeen: "r0", ConfirmCount: 3,
	}
	if err := memory.Save(proj, memory.Store{Findings: []memory.Finding{seedA}}); err != nil {
		t.Fatalf("seed prev project store: %v", err)
	}

	o.runReflectionPipeline(context.Background(), "s1", nil, stageDir)

	out, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("project memory did not parse: %v", err)
	}
	if len(out.Findings) != 2 {
		t.Fatalf("want 2 findings (A retained + B new), got %d: %+v", len(out.Findings), out.Findings)
	}
	var gotA *memory.Finding
	var gotB *memory.Finding
	for i := range out.Findings {
		f := &out.Findings[i]
		if f.ID == seedA.ID {
			gotA = f
		}
		if f.Statement == "new finding B" {
			gotB = f
		}
	}
	if gotA == nil {
		t.Fatal("existing finding A was lost — consolidator omitted it, must be retained unchanged")
	}
	if !reflect.DeepEqual(*gotA, seedA) {
		t.Errorf("retained finding A must be unchanged: got %+v, want %+v", *gotA, seedA)
	}
	if gotB == nil {
		t.Error("new finding B must also be present")
	}
}

// TestReflectionPipeline_TolerantOfMarkdownFencedConsolidatedYAML — the
// consolidator wraps consolidated.yaml in a ```yaml ... ``` markdown fence
// (LLMs do this despite being asked for raw YAML). reconcileAndSave must
// strip the fence before yaml.Unmarshal, not fail the whole pipeline. Must
// fail before the fix (yaml.Unmarshal errors on the fence lines → no finding
// saved) and pass after.
func TestReflectionPipeline_TolerantOfMarkdownFencedConsolidatedYAML(t *testing.T) {
	fenced := "```yaml\n" +
		"findings:\n" +
		"  - scope: project\n" +
		"    kind: fact\n" +
		"    statement: fenced finding\n" +
		"    evidence: e:1\n" +
		"    status: new\n" +
		"```\n"
	o, proj, _, stageDir, _ := pipelineHarness(t, 60, fenced)

	o.runReflectionPipeline(context.Background(), "s1", nil, stageDir)

	out, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("project memory did not parse: %v", err)
	}
	if len(out.Findings) != 1 {
		t.Fatalf("want 1 finding despite markdown fence, got %d: %+v", len(out.Findings), out.Findings)
	}
	if out.Findings[0].Statement != "fenced finding" {
		t.Errorf("unexpected statement: %q", out.Findings[0].Statement)
	}
}

func TestReflectionPipeline_EmptyEvidenceDropped(t *testing.T) {
	consolidated := "findings:\n" +
		"  - scope: project\n" +
		"    kind: fact\n" +
		"    statement: no evidence here\n" +
		"    status: new\n"
	o, proj, _, stageDir, _ := pipelineHarness(t, 60, consolidated)

	o.runReflectionPipeline(context.Background(), "s1", nil, stageDir)

	out, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("project memory did not parse: %v", err)
	}
	if len(out.Findings) != 0 {
		t.Errorf("finding without evidence must not land, got %+v", out.Findings)
	}
}

func TestReflectionPipeline_EvictionCapsStoreSize(t *testing.T) {
	const maxFindings = 2
	proj := filepath.Join(t.TempDir(), "PROJECT_MEMORY.yaml")
	seed := memory.Store{Findings: []memory.Finding{
		{ID: "keep-hi", Scope: memory.ScopeProject, Kind: memory.KindFact, Statement: "a", Evidence: "e1", FirstSeen: "r0", LastSeen: "r0", ConfirmCount: 5},
		{ID: "drop-lo", Scope: memory.ScopeProject, Kind: memory.KindFact, Statement: "b", Evidence: "e2", FirstSeen: "r0", LastSeen: "r0", ConfirmCount: 1},
	}}
	if err := memory.Save(proj, seed); err != nil {
		t.Fatal(err)
	}

	consolidated := "findings:\n" +
		"  - id: keep-hi\n" +
		"    scope: project\n" +
		"    kind: fact\n" +
		"    statement: a\n" +
		"    evidence: e1\n" +
		"    status: unchanged\n" +
		"  - id: drop-lo\n" +
		"    scope: project\n" +
		"    kind: fact\n" +
		"    statement: b\n" +
		"    evidence: e2\n" +
		"    status: unchanged\n" +
		"  - scope: project\n" +
		"    kind: fact\n" +
		"    statement: brand new fact\n" +
		"    evidence: e3\n" +
		"    status: new\n"

	o := newTestOrchestrator(t)
	runDir := t.TempDir()
	sess := filepath.Join(runDir, "SESSION_MEMORY.yaml")
	o.opts.RunDir = runDir
	o.opts.Memory = flow.MemoryConfig{ProjectFile: proj, MaxFindings: maxFindings}
	o.opts.MemoryProjectPath = proj
	o.opts.MemorySessionPath = sess
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		switch spec.kind {
		case memoryKindReflect:
			return os.WriteFile(spec.datasetOut, []byte("findings: []\n"), 0644)
		case memoryKindConsolidator:
			return os.WriteFile(spec.outPath, []byte(consolidated), 0644)
		default:
			return nil
		}
	}

	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	o.runReflectionPipeline(context.Background(), "s1", nil, stageDir)

	out, err := memory.Load(proj)
	if err != nil {
		t.Fatalf("project memory did not parse: %v", err)
	}
	if len(out.Findings) > maxFindings {
		t.Errorf("store not evicted: len=%d, want <= %d", len(out.Findings), maxFindings)
	}
}

func TestMaybeRunReflection_NoOpWhenDisabled(t *testing.T) {
	o := newTestOrchestrator(t)
	o.opts.MemoryProjectPath = "" // disabled
	called := false
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error { called = true; return nil }
	o.maybeRunReflection(context.Background(), "s1")
	o.concurrency.WaitAgents()
	if called {
		t.Error("must not run any memory agent when memory disabled")
	}
}

func TestReflectionPipeline_Serialized(t *testing.T) {
	o, _, _, _, _ := pipelineHarness(t, 60, "findings: []\n")
	var mu sync.Mutex
	concurrent, maxConcurrent := 0, 0
	o.runMemoryAgent = func(ctx context.Context, spec memoryAgentSpec) error {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
		// small yield to expose overlap if not serialized
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Go(func() { o.runReflectionPipeline(context.Background(), "s", nil, t.TempDir()) })
	}
	wg.Wait()
	if maxConcurrent > 1 {
		t.Errorf("pipelines overlapped: maxConcurrent=%d (reflectMu not held)", maxConcurrent)
	}
}

func TestInitSessionMemory_ResetsEachRun(t *testing.T) {
	o := newTestOrchestrator(t)
	runDir := t.TempDir()
	sess := filepath.Join(runDir, "SESSION_MEMORY.yaml")
	if err := os.WriteFile(sess, []byte("STALE CONTENT FROM A PREVIOUS RUN"), 0644); err != nil {
		t.Fatalf("setup: failed to write stale session file: %v", err)
	}
	o.opts.MemoryProjectPath = filepath.Join(t.TempDir(), "P.yaml")
	o.opts.MemorySessionPath = sess

	o.initSessionMemory()

	data, _ := os.ReadFile(sess)
	if strings.Contains(string(data), "STALE") {
		t.Error("session memory must be reset at run start")
	}
	got, err := memory.Load(sess)
	if err != nil {
		t.Fatalf("session memory must parse as a valid store: %v", err)
	}
	if len(got.Findings) != 0 {
		t.Errorf("session memory must be reset to empty, got %d findings", len(got.Findings))
	}
}

func TestFinalReflection_RunsOnceWhenEnabled(t *testing.T) {
	o, _, _, _, order := pipelineHarness(t, 60, "findings: []\n")
	o.opts.Memory.FinalReflect = true

	o.runFinalReflectionOnce(context.Background())
	first := len(*order)
	if first == 0 {
		t.Fatal("final reflection did not run")
	}
	// A second call must be a no-op (finalReflectDone).
	o.runFinalReflectionOnce(context.Background())
	if len(*order) != first {
		t.Error("final reflection ran twice; finalReflectDone not honored")
	}
}

func TestFinalReflection_NoOpWhenDisabled(t *testing.T) {
	o, _, _, _, order := pipelineHarness(t, 60, "findings: []\n")
	o.opts.Memory.FinalReflect = false
	o.runFinalReflectionOnce(context.Background())
	if len(*order) != 0 {
		t.Error("final reflection must not run when disabled")
	}
}
