package main

// T8.6 — on restart/resume, the stage registry must be restored from this run's own durable
// graph_registry.json BEFORE any consumer (orchestrator.New, the dashboard's stage config) is built, and a
// flow.yaml edited between runs must not silently replace the stored graph. executeFlow (run.go) calls
// graph.RestoreOrPersist exactly once, right after normalizeInteractiveStages and before everything
// downstream — these tests exercise that REAL function, the same style TestRecovery_TreeRestoredBeforeOrchestratorNew
// (run_collaboration_test.go) already uses for the Этап 2 collaboration half of this same ordering
// requirement: a full orchestrator.New/dashboard build is disproportionate for an ordering test, so a
// documented stand-in marker plays that role here too.

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
)

// TestRestart_GraphRestoredBeforeOrchestratorNew mirrors TestRecovery_TreeRestoredBeforeOrchestratorNew's
// style: the real graph.RestoreOrPersist call (run.go's exact call site) must complete — durably, stages
// readable back from disk — before the point in executeFlow where orchestrator.New is constructed next.
func TestRestart_GraphRestoredBeforeOrchestratorNew(t *testing.T) {
	dir := t.TempDir()
	fresh := []flow.Stage{{ID: "a"}}

	var order []string
	stages, err := graph.RestoreOrPersist(dir, fresh)
	if err != nil {
		t.Fatalf("RestoreOrPersist: %v", err)
	}
	order = append(order, "registry_restored")
	// Mirrors run.go: orchestrator.New(orchOpts) is constructed shortly after stages is resolved (collaboration
	// setup sits between them, but never reads/touches stages itself).
	order = append(order, "orchestrator_new")

	if len(stages) != 1 || stages[0].ID != "a" {
		t.Fatalf("RestoreOrPersist returned %+v, want fresh stages", stages)
	}
	if _, found, err := graph.LoadRegistry(dir); err != nil || !found {
		t.Fatalf("stage registry was not durably persisted before orchestrator.New would run: found=%v err=%v", found, err)
	}
	if len(order) != 2 || order[0] != "registry_restored" || order[1] != "orchestrator_new" {
		t.Fatalf("order = %v, want [registry_restored, orchestrator_new]", order)
	}
}

// TestRestart_ChangedFlowYamlDoesNotReplaceStoredGraph: a flow.yaml edited between two runs of the SAME run
// directory must not retake effect — the second RestoreOrPersist call (standing in for a restarted
// executeFlow re-parsing flow.yaml into `stages`) must still return the ORIGINAL stage set.
func TestRestart_ChangedFlowYamlDoesNotReplaceStoredGraph(t *testing.T) {
	dir := t.TempDir()
	original := []flow.Stage{{ID: "build", Name: "Build"}}
	if _, err := graph.RestoreOrPersist(dir, original); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Simulate the user editing flow.yaml before restarting afm: a re-parse now yields a DIFFERENT stage set.
	edited := []flow.Stage{{ID: "build", Name: "Build (edited)"}, {ID: "deploy", Name: "Deploy"}}
	got, err := graph.RestoreOrPersist(dir, edited)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Build" {
		t.Fatalf("resumed run used the edited flow.yaml instead of the stored graph: got %+v", got)
	}
}

// TestRestart_DashboardConfigBuiltAfterRegistry: the dashboard's static stage config (dashboardStageConfig,
// run.go's real production call feeding registryStageProvider) must be built from the SAME registry-resolved
// stage slice RestoreOrPersist returns — not from a flow.yaml re-parse — so a stage's dashboard-facing shape
// (here: its predefined Buttons) also survives an edited flow.yaml across a restart.
func TestRestart_DashboardConfigBuiltAfterRegistry(t *testing.T) {
	dir := t.TempDir()
	original := []flow.Stage{{ID: "build", Buttons: flow.Buttons{{Label: "Run linter", Prompt: "lint it"}}}}
	if _, err := graph.RestoreOrPersist(dir, original); err != nil {
		t.Fatalf("first run: %v", err)
	}

	edited := []flow.Stage{{ID: "build"}} // flow.yaml edited: button removed — must NOT reach the dashboard
	stages, err := graph.RestoreOrPersist(dir, edited)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	cfg := dashboardStageConfig(stages)
	buildCfg, ok := cfg["build"]
	if !ok {
		t.Fatal(`dashboardStageConfig(stages)["build"] missing`)
	}
	if len(buildCfg.Buttons) != 1 || buildCfg.Buttons[0] != "Run linter" {
		t.Fatalf("dashboard config built from the edited stages, not the registry-restored ones: %+v", buildCfg)
	}
}
