package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/pkg/pipeline"
)

func (pf *pipelineFixture) repeatFlow(repeat string) string {
	return fmt.Sprintf(`name: pkgflow
repeat: %s
skills_dir: skills
stages:
  - id: only
    name: Only
    script: |
      echo "$AFM_PIPELINE_DIR" >> %s
`, repeat, pf.log)
}

// Every cycle of a repeating package is a run of its own with its own binding, and the flow is re-read from the
// same snapshot, so relative skills_dir values keep resolving inside it.
func TestPipelineRepeatBindsEveryCycleToTheSnapshot(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nresources: [skills]\n",
		"flow.yaml":           pf.repeatFlow("{max_cycles: 2, keep_runs: 0}"),
		"skills/a/SKILL.md":   "---\nname: a\ndescription: d\n---\nbody\n",
	})
	code, stdout, stderr := pf.runAfm("--pipeline", dir)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	runs := pf.runDirs()
	bindings := pf.bindings()
	if len(runs) != 2 || len(bindings) != 2 {
		t.Fatalf("runs=%v bindings=%+v", runs, bindings)
	}
	byRun := map[string]pipeline.Binding{}
	for _, b := range bindings {
		byRun[b.RunID] = b
	}
	for _, id := range runs {
		b, ok := byRun[id]
		if !ok || b.SnapshotDir != bindings[0].SnapshotDir || b.RunName != "pkgflow" {
			t.Fatalf("run %s has binding %+v", id, b)
		}
	}
	if lines := pf.logLines(); len(lines) != 2 || lines[0] != lines[1] || !strings.HasPrefix(lines[0], bindings[0].SnapshotDir) {
		t.Fatalf("both cycles must see the same snapshot: %q", lines)
	}
}

func TestPipelineRepeatReloadsFlowFromTheSnapshot(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nresources: [skills]\n",
		"flow.yaml":           pf.repeatFlow("{max_cycles: 3}"),
		"skills/a/SKILL.md":   "x",
	})
	loaded, _, err := loadFreshPipeline(t.Context(), pipeline.Source{Kind: pipeline.SourceLocal, Location: dir})
	if err != nil {
		t.Fatal(err)
	}
	loop := cycleLoop{
		cfg:      testCycleConfig(),
		loadFlow: func() (*flow.Flow, error) { return pipeline.LoadFlow(loaded.Snapshot, loaded.Manifest) },
	}
	next, _, stop, err := loop.reload(loaded.Flow, nil, 1)
	if err != nil || stop != "" {
		t.Fatalf("reload: stop=%q err=%v", stop, err)
	}
	if want := filepath.Join(loaded.Snapshot.Dir, "skills"); string(next.SkillsDir) != want {
		t.Fatalf("skills_dir after reload = %q, want %q", next.SkillsDir, want)
	}
}

// Without a package nothing changes: the loop still reads its flow file.
func TestPlainRepeatStillReloadsFromTheFlowFile(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}"}))
	f, err := fx.loop(testCycleConfig()).readFlow()
	if err != nil || f.Name != "rp" {
		t.Fatalf("flow = %+v, err = %v", f, err)
	}
}

// A binding goes away with the run that repeat cleanup removes; the surviving run keeps its own.
func TestPipelineRepeatCleanupRemovesBindingsOfRemovedRuns(t *testing.T) {
	pf := newPipelineFixture(t)
	dir := pf.writePackage("tpkg", "pkgflow", map[string]string{
		pipeline.ManifestFile: "schema_version: 2\nname: tpkg\nversion: 1\nentrypoint: flow.yaml\nresources: [skills]\n",
		"flow.yaml":           pf.repeatFlow("{max_cycles: 3, keep_runs: 1}"),
		"skills/a/SKILL.md":   "x",
	})
	if code, stdout, stderr := pf.runAfm("--pipeline", dir); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	runs, bindings := pf.runDirs(), pf.bindings()
	if len(runs) != 1 || len(bindings) != 1 || bindings[0].RunID != runs[0] {
		t.Fatalf("runs=%v bindings=%+v: removed runs must lose their binding", runs, bindings)
	}
}
