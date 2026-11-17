package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/pkg/pipeline"
)

func binderFixture(t *testing.T, source string, resume *pipeline.Binding) (*pipelineFixture, *pipelineBinder) {
	t.Helper()
	pf := newPipelineFixture(t)
	f := mustParseFlow(t, pf.packageFlow("pkgflow"))
	setup := pipelineSetup{
		source: pipeline.Source{Kind: pipeline.SourceLocal, Location: source},
		loaded: pipeline.Loaded{
			Flow:     f,
			Manifest: pipeline.Manifest{Name: "tpkg", Version: "1"},
			Snapshot: pipeline.Snapshot{Dir: "/snap/new", SHA256: "new"},
		},
		cfg: testCycleConfig(),
	}
	return pf, newPipelineBinder(setup, "pkgflow", "", resume)
}

func foreignBinding(runID string) pipeline.Binding {
	return pipeline.Binding{
		RunID: runID, RunName: "pkgflow", Package: pipeline.PackageRef{Name: "other", Version: "9"},
		Source: pipeline.Source{Kind: pipeline.SourceLocal, Location: "/foreign"}, ContentSHA256: "foreign", SnapshotDir: "/snap/foreign",
	}
}

func readBindingOf(t *testing.T, b *pipelineBinder, runID string) pipeline.Binding {
	t.Helper()
	got, ok, err := pipeline.ReadBinding(b.dir, runID)
	if err != nil || !ok {
		t.Fatalf("binding of %s: ok=%v err=%v", runID, ok, err)
	}
	return got
}

// A newer run of another pipeline can appear between preflight and Open; its binding must survive the mismatch.
func TestBinderKeepsForeignBindingWhenPredictedResumeOpensAnotherRun(t *testing.T) {
	predicted := foreignBinding("pkgflow-predicted")
	predicted.Source = pipeline.Source{Kind: pipeline.SourceLocal, Location: "/mine"}
	_, b := binderFixture(t, "/mine", &predicted)
	b.dir = t.TempDir()
	foreign := foreignBinding("pkgflow-actual")
	if err := pipeline.WriteBinding(b.dir, foreign); err != nil {
		t.Fatal(err)
	}
	err := b.runCreated("pkgflow-actual", 1)
	if err == nil || !strings.Contains(err.Error(), "refusing to rebind") {
		t.Fatalf("err = %v", err)
	}
	if got := readBindingOf(t, b, "pkgflow-actual"); got.Source != foreign.Source || got.ContentSHA256 != "foreign" {
		t.Fatalf("foreign binding was overwritten: %+v", got)
	}
}

func TestBinderRefusesBindingSwappedToAnotherPipelineAfterPreflight(t *testing.T) {
	predicted := foreignBinding("pkgflow-run")
	predicted.Source = pipeline.Source{Kind: pipeline.SourceLocal, Location: "/mine"}
	_, b := binderFixture(t, "/mine", &predicted)
	b.dir = t.TempDir()
	foreign := foreignBinding("pkgflow-run")
	if err := pipeline.WriteBinding(b.dir, foreign); err != nil {
		t.Fatal(err)
	}
	if err := b.runCreated("pkgflow-run", 1); err == nil || !strings.Contains(err.Error(), "refusing to rebind") {
		t.Fatalf("err = %v", err)
	}
	if got := readBindingOf(t, b, "pkgflow-run"); got.Source != foreign.Source {
		t.Fatalf("binding was overwritten: %+v", got)
	}
}

func TestBinderRefusesToRebindExistingRunOfNewStart(t *testing.T) {
	_, b := binderFixture(t, "/mine", nil)
	b.dir = t.TempDir()
	foreign := foreignBinding("pkgflow-run")
	if err := pipeline.WriteBinding(b.dir, foreign); err != nil {
		t.Fatal(err)
	}
	if err := b.runCreated("pkgflow-run", 1); err == nil || !strings.Contains(err.Error(), "refusing to rebind") {
		t.Fatalf("err = %v", err)
	}
	if got := readBindingOf(t, b, "pkgflow-run"); got.Source != foreign.Source {
		t.Fatalf("binding was overwritten: %+v", got)
	}
}

func TestBinderRefusesCorruptExistingBinding(t *testing.T) {
	_, b := binderFixture(t, "/mine", nil)
	b.dir = t.TempDir()
	if err := os.WriteFile(b.dir+"/pkgflow-run.json", []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.runCreated("pkgflow-run", 1); err == nil {
		t.Fatal("a damaged binding must not be silently replaced")
	}
}

// A run with progress but no binding (a plain `afm run` of the same flow) is not this pipeline's: binding it would
// make the next start continue it as this pipeline's run.
func TestBinderRefusesUnboundRunWithProgressOfNewStart(t *testing.T) {
	pf, b := binderFixture(t, "/mine", nil)
	b.dir = pipeline.BindingsDir(pf.root)
	if code, _, stderr := pf.runAfm("--pipeline", pf.writePackage("tpkg", "pkgflow", nil)); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	runs := pf.runDirs()
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	if err := os.Remove(filepath.Join(b.dir, runs[0]+".json")); err != nil {
		t.Fatal(err)
	}
	if err := b.runCreated(runs[0], 1); err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("err = %v", err)
	}
	if _, bound, err := pipeline.ReadBinding(b.dir, runs[0]); bound || err != nil {
		t.Fatalf("the unbound run was bound: bound=%v err=%v", bound, err)
	}
}

// The engine opens a newer unbound run with progress instead of the predicted one: it stays unbound, nothing
// starts, the predicted run is untouched, and the next start refuses the unbound run instead of continuing it.
func TestPipelineResumeLeavesUnboundRunWithProgressUnbound(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	plan, err := preparePipelineRun(t.Context(), pipelineFlags{source: dir}, testCycleConfig(), "", false, false, false)
	if err != nil || plan.resume == nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	predicted := plan.resume.RunID
	before := pf.runFingerprint(predicted)
	foreign := "pkgflow-20990101-000000-ffff" // newer, so the engine continues it rather than the predicted run
	if err := os.CopyFS(filepath.Join(runsDir(), foreign), os.DirFS(filepath.Join(runsDir(), predicted))); err != nil {
		t.Fatal(err)
	}
	stagesBefore := len(pf.logLines())
	binder := newPipelineBinder(plan.setup, plan.runName, "", plan.resume)
	_, err = executeFlow(t.Context(), plan.setup.loaded.Flow, plan.setup.cfg, nil, runOptions{runName: plan.runName, cycle: 1, pipeline: binder})
	if err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("err = %v", err)
	}
	if _, bound, err := pipeline.ReadBinding(pipeline.BindingsDir(pf.root), foreign); bound || err != nil {
		t.Fatalf("the unbound run was bound: bound=%v err=%v", bound, err)
	}
	if got := len(pf.logLines()); got != stagesBefore {
		t.Fatalf("stages started after the refusal: %v", pf.logLines())
	}
	if pf.runFingerprint(predicted) != before {
		t.Fatal("the predicted run's binding or journal changed")
	}
	next, err := preparePipelineRun(t.Context(), pipelineFlags{source: dir}, testCycleConfig(), "", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "was not started from this pipeline source") {
		t.Fatalf("the next start must refuse the unbound run %s: plan=%+v err=%v", foreign, next, err)
	}
}

// The engine opens a run other than the one preflight predicted: the original binding stays, the new run gets a
// truthful binding, the error is returned and no stage starts.
func TestPipelineUnexpectedRunIDKeepsOriginalBindingAndStartsNothing(t *testing.T) {
	predicted := foreignBinding("pkgflow-19700101-000000-aaaa")
	predicted.Source = pipeline.Source{Kind: pipeline.SourceLocal, Location: "/mine"}
	pf, b := binderFixture(t, "/mine", &predicted)
	b.dir = pipeline.BindingsDir(pf.root)
	if err := pipeline.WriteBinding(b.dir, predicted); err != nil {
		t.Fatal(err)
	}
	f := b.setup.loaded.Flow
	_, err := executeFlow(context.Background(), f, b.setup.cfg, nil, runOptions{runName: f.Name, cycle: 1, pipeline: b})
	if err == nil || !strings.Contains(err.Error(), "expected to continue run "+predicted.RunID) {
		t.Fatalf("err = %v", err)
	}
	if lines := pf.logLines(); len(lines) != 0 {
		t.Fatalf("stages started after the mismatch: %v", lines)
	}
	if got := readBindingOf(t, b, predicted.RunID); got.Package.Name != "other" || got.SnapshotDir != "/snap/foreign" {
		t.Fatalf("original binding changed: %+v", got)
	}
	var opened pipeline.Binding
	for _, bd := range pf.bindings() {
		if bd.RunID != predicted.RunID {
			opened = bd
		}
	}
	if opened.RunID == "" || opened.Package.Name != "tpkg" || opened.ContentSHA256 != "new" {
		t.Fatalf("the opened run has no truthful binding: %+v", pf.bindings())
	}
}

// A live afm process holding the run makes preflight refuse before the engine could open a different run.
func TestPipelineResumeRefusesRunHeldByAnotherProcess(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	runs := pf.runDirs()
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	lock, err := implstate.TryLockRun(filepath.Join(runsDir(), runs[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	before := len(pf.logLines())
	code, _, stderr := pf.runAfm("--pipeline", dir)
	if code == 0 || !strings.Contains(stderr, "held by another afm process") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got := pf.runDirs(); len(got) != 1 || len(pf.logLines()) != before {
		t.Fatalf("a refused continuation started something: runs=%v log=%v", got, pf.logLines())
	}
}

// A start with a task is a new run: preflight must not predict the continuation of the recorded run, or the binder
// would reject the new run the engine opens.
func TestPipelinePlanWithTaskStartsNewRun(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", nil)
	if code, _, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("first start: code=%d %s", code, stderr)
	}
	cont, err := preparePipelineRun(t.Context(), pipelineFlags{source: dir}, testCycleConfig(), "", false, false, false)
	if err != nil || cont.resume == nil {
		t.Fatalf("without a task the plan must continue the recorded run: plan=%+v err=%v", cont, err)
	}
	fresh, err := preparePipelineRun(t.Context(), pipelineFlags{source: dir}, testCycleConfig(), "", false, false, true)
	if err != nil || fresh.resume != nil || !fresh.forceNew {
		t.Fatalf("with a task the plan must start a new run: plan=%+v err=%v", fresh, err)
	}
}
