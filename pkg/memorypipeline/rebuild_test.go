package memorypipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// newRulesMD is a candidate distinct from validRulesMD (distill_test.go's
// seed) so a target seeded from validRulesMD is "changed" when update writes
// this instead.
const newRulesMD = "# Project rules\n\n## New Pattern\n\nNew description.\n"

// --- shared helpers --------------------------------------------------------

func writeManifestFile(t *testing.T, path, status string) {
	t.Helper()
	m := NewRunningManifest(testMeta())
	m.Status = status
	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("writeManifestFile: %v", err)
	}
}

// reflectRunner writes nonEmptyDatasetYAML to spec.DatasetOut for KindReflect.
func reflectRunner(t *testing.T) AgentRunner {
	t.Helper()
	return func(_ context.Context, spec AgentSpec) error {
		if spec.Kind != KindReflect {
			t.Fatalf("reflectRunner got unexpected kind %q", spec.Kind)
		}
		return os.WriteFile(spec.DatasetOut, []byte(nonEmptyDatasetYAML), 0644)
	}
}

// makeStageDir creates a stage dir with an agent-session file so
// HasAgentSession() is true.
func makeStageDir(t *testing.T, base, id string) string {
	t.Helper()
	dir := filepath.Join(base, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "plan.md"), "a plan\n")
	return dir
}

func rwStage(id, file string) flow.Stage {
	return flow.Stage{ID: id, Name: id, Reflect: &flow.Reflect{File: file, Mode: flow.ReflectModeRW}}
}

// --- CaptureStage ----------------------------------------------------------

func TestCaptureStage_ReuseValidCanonical(t *testing.T) {
	run := t.TempDir()
	stageDir := makeStageDir(t, run, "s1")
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
	writeFile(t, canonical, nonEmptyDatasetYAML)

	// empty behavior map => any agent call fails the test
	var calls []string
	p := New(Prompts{}, AgentConfig{}, WithRunner(recordingRunner(t, &calls, map[string]func(AgentSpec) error{})))

	res, err := p.CaptureStage(context.Background(), rwStage("s1", "s1.md"), stageDir, canonical, stageDir, false)
	if err != nil {
		t.Fatalf("CaptureStage: %v", err)
	}
	if res.Source != "reused" {
		t.Errorf("Source=%q want reused", res.Source)
	}
	if res.DatasetPath != canonical || res.CanonicalPath != canonical {
		t.Errorf("paths: DatasetPath=%q CanonicalPath=%q canonical=%q", res.DatasetPath, res.CanonicalPath, canonical)
	}
	if res.DatasetSHA256 == "" {
		t.Error("DatasetSHA256 empty")
	}
	if len(calls) != 0 {
		t.Errorf("expected no agent calls, got %v", calls)
	}
}

func TestCaptureStage_RegeneratesInvalidCanonical(t *testing.T) {
	run := t.TempDir()
	stageDir := makeStageDir(t, run, "s1")
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
	writeFile(t, canonical, "this is not a valid dataset\n")

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	res, err := p.CaptureStage(context.Background(), rwStage("s1", "s1.md"), stageDir, canonical, stageDir, false)
	if err != nil {
		t.Fatalf("CaptureStage: %v", err)
	}
	if res.Source != "generated" {
		t.Errorf("Source=%q want generated", res.Source)
	}
	if res.DatasetPath != canonical {
		t.Errorf("DatasetPath=%q want %q", res.DatasetPath, canonical)
	}
	if len(res.SourceFiles) == 0 || len(res.SourceHashes) != len(res.SourceFiles) {
		t.Errorf("SourceFiles=%v SourceHashes=%v", res.SourceFiles, res.SourceHashes)
	}
}

func TestCaptureStage_ForceRegenerates(t *testing.T) {
	run := t.TempDir()
	stageDir := makeStageDir(t, run, "s1")
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
	writeFile(t, canonical, nonEmptyDatasetYAML) // valid, but force overrides

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	res, err := p.CaptureStage(context.Background(), rwStage("s1", "s1.md"), stageDir, canonical, stageDir, true)
	if err != nil {
		t.Fatalf("CaptureStage: %v", err)
	}
	if res.Source != "generated" {
		t.Errorf("Source=%q want generated (force)", res.Source)
	}
}

func TestCaptureStage_WritesToStagingPathLeavesCanonicalUntouched(t *testing.T) {
	run := t.TempDir()
	work := t.TempDir()
	stageDir := makeStageDir(t, run, "s1")
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")
	writeFile(t, canonical, nonEmptyDatasetYAML) // valid canonical present
	origCanon, _ := os.ReadFile(canonical)

	staging := filepath.Join(work, "stages", "s1", "reflect_dataset.yaml")

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	// force=true so reuse is skipped even though canonical is valid — proves
	// the primitive writes to the caller's chosen staging path, canonical left
	// untouched.
	res, err := p.CaptureStage(context.Background(), rwStage("s1", "s1.md"), stageDir, staging, filepath.Dir(staging), true)
	if err != nil {
		t.Fatalf("CaptureStage: %v", err)
	}
	if res.Source != "generated" {
		t.Errorf("Source=%q want generated", res.Source)
	}
	if res.DatasetPath != staging {
		t.Errorf("DatasetPath=%q want staging %q", res.DatasetPath, staging)
	}
	if res.CanonicalPath != canonical {
		t.Errorf("CanonicalPath=%q want %q", res.CanonicalPath, canonical)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging dataset not written: %v", err)
	}
	after, _ := os.ReadFile(canonical)
	if string(after) != string(origCanon) {
		t.Error("canonical was modified when writing to a staging path")
	}
}

func TestCaptureStage_ScriptOnlyNoAgentSessionStepError(t *testing.T) {
	run := t.TempDir()
	stageDir := filepath.Join(run, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// only a script log — not an agent session.
	writeFile(t, filepath.Join(stageDir, "script.log"), "ran\n")
	canonical := filepath.Join(stageDir, "reflect_dataset.yaml")

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	_, err := p.CaptureStage(context.Background(), rwStage("s1", "s1.md"), stageDir, canonical, stageDir, false)
	if err == nil {
		t.Fatal("expected StepError, got nil")
	}
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a *StepError", err)
	}
	if se.StageID != "s1" || se.Step != KindReflect {
		t.Errorf("StepError StageID=%q Step=%q", se.StageID, se.Step)
	}
}

// --- CaptureAll ------------------------------------------------------------

func TestCaptureAll_WritesStagingAndAdvancesManifest(t *testing.T) {
	run := t.TempDir()
	work := t.TempDir()
	makeStageDir(t, run, "s1")
	makeStageDir(t, run, "s2")

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusRunning)

	stages := []flow.Stage{
		rwStage("s1", "s1.md"),
		{ID: "script", Name: "script", Reflect: &flow.Reflect{File: "x.md", Mode: flow.ReflectModeRW}, Script: "echo hi"},
		{ID: "noreflect", Name: "noreflect"},
		rwStage("s2", "s2.md"),
	}

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	results, err := p.CaptureAll(context.Background(), CaptureRequest{
		RunDir: run, WorkDir: work, Stages: stages, ManifestPath: manifestPath,
	})
	if err != nil {
		t.Fatalf("CaptureAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len=%d want 2 (%+v)", len(results), results)
	}
	for i, id := range []string{"s1", "s2"} {
		wantStaging := filepath.Join(work, "stages", id, "reflect_dataset.yaml")
		if results[i].DatasetPath != wantStaging {
			t.Errorf("[%s] DatasetPath=%q want %q", id, results[i].DatasetPath, wantStaging)
		}
		wantCanon := filepath.Join(run, id, "reflect_dataset.yaml")
		if results[i].CanonicalPath != wantCanon {
			t.Errorf("[%s] CanonicalPath=%q want %q", id, results[i].CanonicalPath, wantCanon)
		}
		if _, err := os.Stat(results[i].CanonicalPath); !os.IsNotExist(err) {
			t.Errorf("[%s] canonical should be untouched, stat err=%v", id, err)
		}
		if _, err := os.Stat(wantStaging); err != nil {
			t.Errorf("[%s] staging not written: %v", id, err)
		}
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != StatusCapturing {
		t.Errorf("manifest status=%q want capturing", m.Status)
	}
	if len(m.Datasets) != 2 {
		t.Errorf("manifest datasets=%d want 2", len(m.Datasets))
	}
}

func TestCaptureAll_ReusesValidCanonical(t *testing.T) {
	run := t.TempDir()
	work := t.TempDir()
	s1 := makeStageDir(t, run, "s1")
	canonical := filepath.Join(s1, "reflect_dataset.yaml")
	writeFile(t, canonical, nonEmptyDatasetYAML) // valid canonical present

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusRunning)

	// empty behavior map => any reflect call fails the test
	var calls []string
	p := New(Prompts{}, AgentConfig{}, WithRunner(recordingRunner(t, &calls, map[string]func(AgentSpec) error{})))
	results, err := p.CaptureAll(context.Background(), CaptureRequest{
		RunDir: run, WorkDir: work, Stages: []flow.Stage{rwStage("s1", "s1.md")}, ManifestPath: manifestPath,
	})
	if err != nil {
		t.Fatalf("CaptureAll: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("reuse must not run reflect, got calls %v", calls)
	}
	if len(results) != 1 || results[0].Source != "reused" {
		t.Fatalf("results=%+v want one reused", results)
	}
	if results[0].DatasetPath != canonical {
		t.Errorf("DatasetPath=%q want canonical %q", results[0].DatasetPath, canonical)
	}
}

func TestCaptureAll_ForceReflectRegeneratesIntoStaging(t *testing.T) {
	run := t.TempDir()
	work := t.TempDir()
	s1 := makeStageDir(t, run, "s1")
	canonical := filepath.Join(s1, "reflect_dataset.yaml")
	writeFile(t, canonical, nonEmptyDatasetYAML) // valid canonical — but force overrides
	orig, _ := os.ReadFile(canonical)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusRunning)

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	results, err := p.CaptureAll(context.Background(), CaptureRequest{
		RunDir: run, WorkDir: work, Stages: []flow.Stage{rwStage("s1", "s1.md")},
		ForceReflect: true, ManifestPath: manifestPath,
	})
	if err != nil {
		t.Fatalf("CaptureAll: %v", err)
	}
	if results[0].Source != "generated" {
		t.Errorf("Source=%q want generated (force)", results[0].Source)
	}
	wantStaging := filepath.Join(work, "stages", "s1", "reflect_dataset.yaml")
	if results[0].DatasetPath != wantStaging {
		t.Errorf("DatasetPath=%q want staging %q", results[0].DatasetPath, wantStaging)
	}
	if after, _ := os.ReadFile(canonical); string(after) != string(orig) {
		t.Error("canonical modified during forced staging regen")
	}
}

func TestCaptureAll_InvalidCanonicalRegenerates(t *testing.T) {
	run := t.TempDir()
	work := t.TempDir()
	s1 := makeStageDir(t, run, "s1")
	writeFile(t, filepath.Join(s1, "reflect_dataset.yaml"), "not a valid dataset\n")

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusRunning)

	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	results, err := p.CaptureAll(context.Background(), CaptureRequest{
		RunDir: run, WorkDir: work, Stages: []flow.Stage{rwStage("s1", "s1.md")}, ManifestPath: manifestPath,
	})
	if err != nil {
		t.Fatalf("CaptureAll: %v", err)
	}
	if results[0].Source != "generated" {
		t.Errorf("Source=%q want generated (invalid canonical)", results[0].Source)
	}
	wantStaging := filepath.Join(work, "stages", "s1", "reflect_dataset.yaml")
	if results[0].DatasetPath != wantStaging {
		t.Errorf("DatasetPath=%q want staging %q", results[0].DatasetPath, wantStaging)
	}
}

func TestCaptureAll_RequiresManifestPath(t *testing.T) {
	p := New(Prompts{}, AgentConfig{}, WithRunner(reflectRunner(t)))
	_, err := p.CaptureAll(context.Background(), CaptureRequest{RunDir: t.TempDir(), WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected error for empty ManifestPath")
	}
}

// --- Finalize helpers ------------------------------------------------------

// distillRunner produces High for every stage except those named in noHigh,
// where prioritize emits no High section (NoHigh => candidate stays == seed).
// update writes newRulesMD to the candidate.
func distillRunner(noHigh map[string]bool) AgentRunner {
	return func(_ context.Context, spec AgentSpec) error {
		switch spec.Kind {
		case KindAggregate:
			return os.WriteFile(spec.Out, []byte("1. Pattern — a description\n"), 0644)
		case KindPrioritize:
			if noHigh[spec.StageName] {
				return os.WriteFile(spec.Out, []byte("## Medium\n\n1. Pattern — a description\n\n## Low\n"), 0644)
			}
			return os.WriteFile(spec.Out, []byte("## High\n\n1. Pattern — a description\n\n## Medium\n\n## Low\n"), 0644)
		case KindUpdate:
			return os.WriteFile(spec.TargetFile, []byte(newRulesMD), 0644)
		default:
			return errors.New("unexpected kind: " + spec.Kind)
		}
	}
}

type writeRec struct {
	path string
	data []byte
}

// recordingWriter records every promotion write and actually persists it.
func recordingWriter(writes *[]writeRec) AtomicWriter {
	return func(path string, data []byte) error {
		*writes = append(*writes, writeRec{path, append([]byte(nil), data...)})
		return memory.AtomicWrite(path, data)
	}
}

func hasWrite(writes []writeRec, path string) bool {
	for _, w := range writes {
		if w.path == path {
			return true
		}
	}
	return false
}

// stagingDataset writes a non-empty dataset at <work>/stages/<id>/reflect_dataset.yaml
// and returns a generated DatasetResult whose canonical is under <run>.
func stagingDataset(t *testing.T, work, run, id string) DatasetResult {
	t.Helper()
	ds := filepath.Join(work, "stages", id, "reflect_dataset.yaml")
	if err := os.MkdirAll(filepath.Dir(ds), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ds, nonEmptyDatasetYAML)
	return DatasetResult{
		StageID:       id,
		Source:        "generated",
		DatasetPath:   ds,
		CanonicalPath: filepath.Join(run, id, "reflect_dataset.yaml"),
		DatasetSHA256: sha256Hex([]byte(nonEmptyDatasetYAML)),
	}
}

// --- Finalize --------------------------------------------------------------

func TestFinalize_DryRunLeavesTargetsUntouched(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	writeFile(t, memory.StageFile(mem, "s1.md"), validRulesMD)
	writeFile(t, memory.ProjectFile(mem), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	ds := stagingDataset(t, work, run, "s1")
	var writes []writeRec

	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(nil)))
	rep, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "s1.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{ds},
		DryRun:       true,
		ManifestPath: manifestPath,
		writer:       recordingWriter(&writes),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(writes) != 0 {
		t.Errorf("dry run wrote %d files: %+v", len(writes), writes)
	}
	// final targets untouched
	if got, _ := os.ReadFile(memory.StageFile(mem, "s1.md")); string(got) != validRulesMD {
		t.Error("stage target modified during dry run")
	}
	if got, _ := os.ReadFile(memory.ProjectFile(mem)); string(got) != validRulesMD {
		t.Error("memory.md modified during dry run")
	}
	// canonical dataset not published
	if _, err := os.Stat(ds.CanonicalPath); !os.IsNotExist(err) {
		t.Error("canonical dataset published during dry run")
	}
	if len(rep.Targets) != 2 {
		t.Fatalf("report targets=%d want 2", len(rep.Targets))
	}
	for _, tr := range rep.Targets {
		if !tr.Changed || tr.Diff == "" {
			t.Errorf("target %q expected changed with diff", tr.Label)
		}
		if tr.Published {
			t.Errorf("target %q must not be published in dry run", tr.Label)
		}
	}
	m, _ := LoadManifest(manifestPath)
	if m.Status != StatusDryRun {
		t.Errorf("manifest status=%q want dry_run", m.Status)
	}
}

// TestFinalize_DryRunDoesNotCreateMemoryDir is the regression guard for the
// final-review Fix 1: a dry run against a FRESH (non-existent) memory.path
// must leave no trace on disk at all — not even an empty directory. Before
// the fix, Finalize unconditionally MkdirAll'd req.MemoryDir near the top,
// so a dry run on a brand-new project silently created memory.path even
// though the documented contract is "dry run writes nothing".
func TestFinalize_DryRunDoesNotCreateMemoryDir(t *testing.T) {
	base := t.TempDir()
	mem := filepath.Join(base, "memory") // deliberately never created
	run := t.TempDir()
	work := t.TempDir()

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	ds := stagingDataset(t, work, run, "s1")
	var writes []writeRec

	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(nil)))
	rep, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "s1.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{ds},
		DryRun:       true,
		ManifestPath: manifestPath,
		writer:       recordingWriter(&writes),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(writes) != 0 {
		t.Errorf("dry run wrote %d files: %+v", len(writes), writes)
	}
	if _, err := os.Stat(mem); !os.IsNotExist(err) {
		t.Errorf("dry run must not create memory dir %s, stat err=%v", mem, err)
	}
	if len(rep.Targets) != 2 {
		t.Fatalf("report targets=%d want 2", len(rep.Targets))
	}
	for _, tr := range rep.Targets {
		// A missing final target is treated as empty, so a fresh target
		// candidate still counts as "changed" against it.
		if !tr.Changed || tr.Diff == "" {
			t.Errorf("target %q expected changed with diff", tr.Label)
		}
		if tr.Published {
			t.Errorf("target %q must not be published in dry run", tr.Label)
		}
	}
	m, _ := LoadManifest(manifestPath)
	if m.Status != StatusDryRun {
		t.Errorf("manifest status=%q want dry_run", m.Status)
	}
}

func TestFinalize_PublishOnlyChanged(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	writeFile(t, memory.StageFile(mem, "a.md"), validRulesMD)
	writeFile(t, memory.StageFile(mem, "b.md"), validRulesMD)
	writeFile(t, memory.ProjectFile(mem), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	dsA := stagingDataset(t, work, run, "stage_a")
	dsB := stagingDataset(t, work, run, "stage_b")

	var writes []writeRec
	meta := testMeta()
	meta.EffectiveCommit = false

	// stage_b => NoHigh => candidate stays == seed => unchanged.
	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(map[string]bool{"stage_b": true})))
	rep, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         meta,
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("stage_a", "a.md"), rwStage("stage_b", "b.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{dsA, dsB},
		ManifestPath: manifestPath,
		writer:       recordingWriter(&writes),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	aFinal := memory.StageFile(mem, "a.md")
	bFinal := memory.StageFile(mem, "b.md")
	projFinal := memory.ProjectFile(mem)

	if !hasWrite(writes, aFinal) {
		t.Error("changed a.md not published")
	}
	if !hasWrite(writes, projFinal) {
		t.Error("changed memory.md not published")
	}
	if hasWrite(writes, bFinal) {
		t.Error("unchanged b.md was rewritten")
	}
	if !hasWrite(writes, dsA.CanonicalPath) || !hasWrite(writes, dsB.CanonicalPath) {
		t.Error("generated canonical datasets not published")
	}
	if got, _ := os.ReadFile(aFinal); string(got) != newRulesMD {
		t.Error("a.md content not durably updated")
	}
	if got, _ := os.ReadFile(bFinal); string(got) != validRulesMD {
		t.Error("b.md should be unchanged")
	}

	// per-target Published flags
	byLabel := map[string]TargetResult{}
	for _, tr := range rep.Targets {
		byLabel[tr.Label] = tr
	}
	if !byLabel["a.md"].Published || !byLabel["a.md"].Changed {
		t.Errorf("a.md target=%+v", byLabel["a.md"])
	}
	if byLabel["b.md"].Published || byLabel["b.md"].Changed {
		t.Errorf("b.md target=%+v", byLabel["b.md"])
	}

	m, _ := LoadManifest(manifestPath)
	if m.Status != StatusPublished {
		t.Errorf("manifest status=%q want published", m.Status)
	}
}

func TestFinalize_ProjectAggregateOnlyEligibleDatasets(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	writeFile(t, memory.StageFile(mem, "s1.md"), validRulesMD)
	writeFile(t, memory.ProjectFile(mem), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	ds := stagingDataset(t, work, run, "s1")
	// a stray ghost dataset on disk under run — NOT in req.Datasets
	ghost := filepath.Join(run, "ghost", "reflect_dataset.yaml")
	if err := os.MkdirAll(filepath.Dir(ghost), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ghost, nonEmptyDatasetYAML)

	var projectAggInputs []string
	runner := func(_ context.Context, spec AgentSpec) error {
		if spec.Kind == KindAggregate && spec.StageName == "flow-memory" {
			projectAggInputs = append([]string{}, spec.InPaths...)
		}
		return distillRunner(nil)(context.Background(), spec)
	}

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	_, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "s1.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{ds},
		DryRun:       true,
		ManifestPath: manifestPath,
		writer:       recordingWriter(new([]writeRec)),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	for _, in := range projectAggInputs {
		if strings.Contains(in, "ghost") {
			t.Errorf("project aggregate included the ghost dataset: %v", projectAggInputs)
		}
	}
	if len(projectAggInputs) != 1 || projectAggInputs[0] != ds.DatasetPath {
		t.Errorf("project aggregate inputs=%v want [%s]", projectAggInputs, ds.DatasetPath)
	}
}

func TestFinalize_SharedReflectFileChainsThroughCandidate(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	// no existing shared.md — brand new target
	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	dsA := stagingDataset(t, work, run, "s1")
	dsB := stagingDataset(t, work, run, "s2")

	// update appends this stage's block to the (seeded) candidate — so if
	// stage2 chains from stage1's candidate, the final file carries both.
	runner := func(_ context.Context, spec AgentSpec) error {
		switch spec.Kind {
		case KindAggregate:
			return os.WriteFile(spec.Out, []byte("1. P — d\n"), 0644)
		case KindPrioritize:
			return os.WriteFile(spec.Out, []byte("## High\n\n1. P — d\n\n## Medium\n\n## Low\n"), 0644)
		case KindUpdate:
			cur, _ := os.ReadFile(spec.TargetFile)
			content := string(cur)
			if !strings.Contains(content, "# Project rules") {
				content = "# Project rules\n"
			}
			content += "\n## " + spec.StageName + "\n\nblock\n"
			return os.WriteFile(spec.TargetFile, []byte(content), 0644)
		}
		return errors.New("unexpected kind")
	}

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	_, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "shared.md"), rwStage("s2", "shared.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeR, MaxRules: 25}, // R => no project write
		Datasets:     []DatasetResult{dsA, dsB},
		ManifestPath: manifestPath,
		writer:       recordingWriter(new([]writeRec)),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err := os.ReadFile(memory.StageFile(mem, "shared.md"))
	if err != nil {
		t.Fatalf("read shared.md: %v", err)
	}
	if !strings.Contains(string(got), "## s1") || !strings.Contains(string(got), "## s2") {
		t.Errorf("shared.md did not chain both stages:\n%s", got)
	}
}

func TestFinalize_NoHighFirstStagePreservesSeedInSharedGroup(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	// prior rules in the shared target ("## Old Pattern")
	writeFile(t, memory.StageFile(mem, "shared.md"), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	dsA := stagingDataset(t, work, run, "s1")
	dsB := stagingDataset(t, work, run, "s2")

	// s1 => NoHigh (candidate stays == seed, prior rules preserved); s2 => High
	// and its update APPENDS a block to the (chained) candidate.
	runner := func(_ context.Context, spec AgentSpec) error {
		switch spec.Kind {
		case KindAggregate:
			return os.WriteFile(spec.Out, []byte("1. P — d\n"), 0644)
		case KindPrioritize:
			if spec.StageName == "s1" {
				return os.WriteFile(spec.Out, []byte("## Medium\n\n1. P — d\n\n## Low\n"), 0644)
			}
			return os.WriteFile(spec.Out, []byte("## High\n\n1. P — d\n\n## Medium\n\n## Low\n"), 0644)
		case KindUpdate:
			cur, _ := os.ReadFile(spec.TargetFile)
			return os.WriteFile(spec.TargetFile, []byte(string(cur)+"\n## Appended "+spec.StageName+"\n\nblk\n"), 0644)
		}
		return errors.New("unexpected kind")
	}

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	_, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "shared.md"), rwStage("s2", "shared.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeR, MaxRules: 25}, // R => no project write
		Datasets:     []DatasetResult{dsA, dsB},
		ManifestPath: manifestPath,
		writer:       recordingWriter(new([]writeRec)),
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err := os.ReadFile(memory.StageFile(mem, "shared.md"))
	if err != nil {
		t.Fatalf("read shared.md: %v", err)
	}
	// The NoHigh first stage must NOT wipe the shared target: the seed's prior
	// "## Old Pattern" survives, and s2's appended block is chained on top.
	if !strings.Contains(string(got), "## Old Pattern") {
		t.Errorf("NoHigh first stage wiped the shared seed:\n%s", got)
	}
	if !strings.Contains(string(got), "## Appended s2") {
		t.Errorf("s2 did not chain onto the shared candidate:\n%s", got)
	}
}

func TestFinalize_CtxCancelledBeforePromotionNoWrite(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	writeFile(t, memory.StageFile(mem, "s1.md"), validRulesMD)
	writeFile(t, memory.ProjectFile(mem), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	ds := stagingDataset(t, work, run, "s1")
	var writes []writeRec

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Finalize; stub runner ignores ctx so distill completes

	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(nil)))
	_, err := p.Finalize(ctx, FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "s1.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{ds},
		ManifestPath: manifestPath,
		writer:       recordingWriter(&writes),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if len(writes) != 0 {
		t.Errorf("cancelled Finalize published %d files", len(writes))
	}
	m, _ := LoadManifest(manifestPath)
	if m.Status != StatusCancelled {
		t.Errorf("manifest status=%q want cancelled", m.Status)
	}
}

func TestFinalize_SecondWriteFailsLeavesPromoting(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	writeFile(t, memory.StageFile(mem, "a.md"), validRulesMD)
	writeFile(t, memory.StageFile(mem, "b.md"), validRulesMD)
	writeFile(t, memory.ProjectFile(mem), validRulesMD)

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	dsA := stagingDataset(t, work, run, "stage_a")
	dsB := stagingDataset(t, work, run, "stage_b")

	n := 0
	failWriter := func(path string, data []byte) error {
		n++
		if n == 2 {
			return errors.New("boom on second write")
		}
		return memory.AtomicWrite(path, data)
	}

	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(nil)))
	_, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("stage_a", "a.md"), rwStage("stage_b", "b.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeRW, MaxRules: 25},
		Datasets:     []DatasetResult{dsA, dsB},
		ManifestPath: manifestPath,
		writer:       failWriter,
	})
	if err == nil {
		t.Fatal("expected promotion error")
	}
	m, _ := LoadManifest(manifestPath)
	if m.Status != StatusPromoting {
		t.Errorf("manifest status=%q want promoting", m.Status)
	}
	if len(m.Targets) < 2 {
		t.Fatalf("manifest targets=%d", len(m.Targets))
	}
	if !m.Targets[0].Published {
		t.Error("first target should be published")
	}
	if m.Targets[1].Published {
		t.Error("second target must not be published")
	}
}

func TestFinalize_SymlinkedTargetParentRejected(t *testing.T) {
	mem := t.TempDir()
	run := t.TempDir()
	work := t.TempDir()
	outside := t.TempDir()

	// mem/sub -> outside (a symlinked parent for the final target)
	if err := os.Symlink(outside, filepath.Join(mem, "sub")); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(work, "manifest.json")
	writeManifestFile(t, manifestPath, StatusCapturing)

	ds := stagingDataset(t, work, run, "s1")
	var writes []writeRec

	p := New(Prompts{}, AgentConfig{}, WithRunner(distillRunner(nil)))
	_, err := p.Finalize(context.Background(), FinalizeRequest{
		Meta:         testMeta(),
		RunDir:       run,
		WorkDir:      work,
		MemoryDir:    mem,
		Stages:       []flow.Stage{rwStage("s1", "sub/f.md")},
		Memory:       flow.MemoryConfig{Mode: flow.ReflectModeR, MaxRules: 25},
		Datasets:     []DatasetResult{ds},
		ManifestPath: manifestPath,
		writer:       recordingWriter(&writes),
	})
	if err == nil {
		t.Fatal("expected containment rejection")
	}
	if !strings.Contains(err.Error(), "escapes") && !strings.Contains(err.Error(), "symlink") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(writes) != 0 {
		t.Errorf("rejected target still published %d files", len(writes))
	}
}

// --- validateTargetUnderMemoryDir / canonicalizeExistingAncestor -----------

func TestValidateTargetUnderMemoryDir_SymlinkedFinalFile(t *testing.T) {
	mem := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(mem, "memory.md")
	if err := os.WriteFile(filepath.Join(outside, "real.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "real.md"), target); err != nil {
		t.Fatal(err)
	}
	if err := validateTargetUnderMemoryDir(mem, target); err == nil {
		t.Error("expected rejection of a symlinked final file")
	}
}

func TestValidateTargetUnderMemoryDir_DanglingSymlinkParent(t *testing.T) {
	mem := t.TempDir()
	// mem/sub -> a path that does not exist (dangling)
	if err := os.Symlink(filepath.Join(mem, "does-not-exist"), filepath.Join(mem, "sub")); err != nil {
		t.Fatal(err)
	}
	err := validateTargetUnderMemoryDir(mem, filepath.Join(mem, "sub", "f.md"))
	if err == nil {
		t.Fatal("expected rejection of a dangling-symlink parent")
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "escapes") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateTargetUnderMemoryDir_OK(t *testing.T) {
	mem := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mem, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateTargetUnderMemoryDir(mem, filepath.Join(mem, "sub", "f.md")); err != nil {
		t.Errorf("valid target rejected: %v", err)
	}
}

func TestCanonicalizeExistingAncestor_RejoinsMissingComponents(t *testing.T) {
	base := t.TempDir()
	got := canonicalizeExistingAncestor(filepath.Join(base, "a", "b", "c.md"))
	// base is real; a/b/c.md do not exist yet — resolved base + rejoined tail.
	realBase, _ := filepath.EvalSymlinks(base)
	want := filepath.Join(realBase, "a", "b", "c.md")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
