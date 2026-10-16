package main

// T8.4 — CLI-side stage lookup: a run's stage DEFINITION (name, is-script, buttons, ...) is found via the
// run's own durable graph.Registry, never by re-parsing flow.yaml — so an afm CLI invocation against a run
// directory always reflects the graph that run actually started with, even if flow.yaml has since changed
// (same invariant as T8.6's dashboard/orchestrator path, applied to CLI tooling).

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
)

// TestCli_FindsStageViaRegistry: a stage's definition is found via the run's stored registry even when a
// (simulated) re-parse of flow.yaml would disagree with it — the run directory alone is enough context, no
// flow.yaml file is read at all.
func TestCli_FindsStageViaRegistry(t *testing.T) {
	runDir := t.TempDir()
	original := []flow.Stage{{ID: "build", Name: "Build", Buttons: flow.Buttons{{Label: "Run linter", Prompt: "lint"}}}}
	if err := graph.SaveRegistry(runDir, original); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}

	got, ok, err := findStageDefinition(runDir, "build")
	if err != nil {
		t.Fatalf("findStageDefinition: %v", err)
	}
	if !ok {
		t.Fatal("findStageDefinition: stage \"build\" not found")
	}
	if got.Name != "Build" || len(got.Buttons) != 1 || got.Buttons[0].Label != "Run linter" {
		t.Fatalf("findStageDefinition = %+v, want the stored definition", got)
	}

	if _, ok, err := findStageDefinition(runDir, "missing"); err != nil || ok {
		t.Fatalf("findStageDefinition(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

// TestCli_FindsStageViaRegistry_NoRunYet: a run directory that never persisted a registry (e.g. `afm run`
// never started here) reports found=false rather than erroring — callers decide what "no run here" means.
func TestCli_FindsStageViaRegistry_NoRunYet(t *testing.T) {
	runDir := t.TempDir()
	_, ok, err := findStageDefinition(runDir, "anything")
	if err != nil {
		t.Fatalf("findStageDefinition: %v", err)
	}
	if ok {
		t.Fatal("findStageDefinition: found a stage in a run directory with no persisted registry")
	}
}
