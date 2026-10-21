package stagefiles

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

// Project artifacts live in the agents' project root (a worktree when the run uses one), not in afm's CWD.
func TestCollectArtifacts_ProjectPathsResolveFromProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "docs", "api.yaml"), []byte("worktree contract"), 0644); err != nil {
		t.Fatal(err)
	}
	notInline := false
	producer := flow.Stage{ID: "design", Artifacts: []flow.Artifact{
		{Name: "api", Path: "docs/api.yaml"},
		{Name: "ref", Path: "docs/api.yaml", Inline: &notInline},
	}}
	consumer := flow.Stage{ID: "build", Inputs: []flow.Input{{Ref: "design.api"}, {Ref: "design.ref"}}}

	got, err := CollectArtifacts(projectDir, t.TempDir(), consumer, []flow.Stage{producer, consumer})
	if err != nil {
		t.Fatalf("CollectArtifacts: %v", err)
	}
	if !strings.Contains(got, "worktree contract") {
		t.Errorf("inline artifact must come from the project root, got:\n%s", got)
	}
	if !strings.Contains(got, "File path: "+filepath.Join(projectDir, "docs", "api.yaml")) {
		t.Errorf("a non-inline artifact must be handed over as an absolute path, got:\n%s", got)
	}
	if err := CheckCompletion(writeDone(t), projectDir, producer); err != nil {
		t.Errorf("CheckCompletion must find the artifact in the project root: %v", err)
	}
}

func TestResolveArtifactPath_KeepsAbsolutePath(t *testing.T) {
	if got := resolveArtifactPath("/wt", "/run", "s", "/abs/out.md"); got != "/abs/out.md" {
		t.Errorf("resolveArtifactPath = %q, want the absolute path unchanged", got)
	}
}

// writeDone returns a stage dir that already has its .done marker.
func writeDone(t *testing.T) string {
	t.Helper()
	stageDir := filepath.Join(t.TempDir(), "design")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0644); err != nil {
		t.Fatal(err)
	}
	return stageDir
}
