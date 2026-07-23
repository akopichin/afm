package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestCollectDependencyPlans_WarnsOnMissingPlan(t *testing.T) {
	runDir := t.TempDir()
	depDir := filepath.Join(runDir, "dep-stage")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	// No plan.md written — simulates a dependency that hasn't produced one.

	stage := flow.Stage{ID: "s2", DependsOn: []string{"dep-stage"}}
	allStages := []flow.Stage{{ID: "dep-stage", Name: "Dep Stage"}, stage}

	var warned []string
	got := CollectDependencyPlans(runDir, stage, allStages, func(depID, msg string) {
		warned = append(warned, depID+": "+msg)
	})

	if !strings.Contains(got, "(plan not available)") {
		t.Errorf("prompt text should still contain placeholder, got %q", got)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "dep-stage") {
		t.Fatalf("expected exactly one warn() call naming dep-stage, got %v", warned)
	}
}

func TestCollectDependencyPlans_NoWarnWhenPlanPresent(t *testing.T) {
	runDir := t.TempDir()
	depDir := filepath.Join(runDir, "dep-stage")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("## Tasks\n- do thing\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stage := flow.Stage{ID: "s2", DependsOn: []string{"dep-stage"}}
	allStages := []flow.Stage{{ID: "dep-stage", Name: "Dep Stage"}, stage}

	var warned []string
	CollectDependencyPlans(runDir, stage, allStages, func(depID, msg string) {
		warned = append(warned, depID)
	})
	if len(warned) != 0 {
		t.Errorf("expected no warn() calls when plan is present, got %v", warned)
	}
}

func TestCollectDependencyPlans_NilWarnIsSafe(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s2", DependsOn: []string{"missing-dep"}}
	allStages := []flow.Stage{stage}

	got := CollectDependencyPlans(runDir, stage, allStages, nil)
	if !strings.Contains(got, "(plan not available)") {
		t.Errorf("expected placeholder text with nil warn callback, got %q", got)
	}
}
