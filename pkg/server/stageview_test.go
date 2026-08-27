package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestBuildStageViews_OrdersAndComputesCapabilities(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// "b" is autonomous (has autonomous.flag) and failed → plan panel must
	// still show (Retry lives there), dialog panel shows too (autonomous track
	// is always dialog-capable).
	if err := os.WriteFile(filepath.Join(runDir, "b", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	rs := state.RunState{
		StageOrder: []string{"b", "a"}, // deliberately not alphabetical — order must be preserved
		StageNames: map[string]string{"a": "Stage A"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusPending},
			"b": {Status: state.StatusFailed},
		},
	}

	views := buildStageViews(rs, runDir, map[string]bool{"a": true}, map[string]bool{"a": true}, map[string]bool{"a": false}, nil, nil)

	if len(views) != 2 || views[0].ID != "b" || views[1].ID != "a" {
		t.Fatalf("order not preserved: %+v", views)
	}

	a, b := views[1], views[0]

	if a.Name != "Stage A" || !a.Interactive || !a.AutoApprove {
		t.Errorf("stage a view wrong: %+v", a)
	}
	if !a.ShowPlan {
		t.Errorf("stage a (not autonomous): ShowPlan should be true, got %+v", a)
	}
	if !a.ShowDialog { // interactive:true → dialog shown
		t.Errorf("stage a: ShowDialog should be true (interactive), got %+v", a)
	}

	if !b.Autonomous {
		t.Errorf("stage b: Autonomous should be true, got %+v", b)
	}
	if !b.ShowPlan {
		t.Errorf("stage b (autonomous but failed): ShowPlan should still be true, got %+v", b)
	}
	if !b.ShowDialog {
		t.Errorf("stage b (autonomous): ShowDialog should be true, got %+v", b)
	}
}

func TestTopoOrder_NoDeps_PreservesDeclarationOrder(t *testing.T) {
	ids := []string{"b", "a", "c"}
	got := topoOrder(ids, nil)
	if !equalSlices(got, ids) {
		t.Fatalf("got %v, want %v (unchanged)", got, ids)
	}
}

func TestTopoOrder_DependencyRendersAfterItsDep(t *testing.T) {
	// "child" declared BEFORE its dependency "parent" — must be reordered.
	ids := []string{"child", "parent"}
	deps := map[string][]string{"child": {"parent"}}
	got := topoOrder(ids, deps)
	want := []string{"parent", "child"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTopoOrder_UnrelatedSiblingsKeepDeclarationOrderRelativeToEachOther(t *testing.T) {
	// stage1 depends on stage2; stage3/4/5 have no deps at all; stage6
	// depends on stage2,3,4,5. Declared as 1,2,3,4,5,6 (1 before its own dep).
	ids := []string{"stage1", "stage2", "stage3", "stage4", "stage5", "stage6"}
	deps := map[string][]string{
		"stage1": {"stage2"},
		"stage6": {"stage2", "stage3", "stage4", "stage5"},
	}
	got := topoOrder(ids, deps)
	want := []string{"stage2", "stage3", "stage4", "stage5", "stage1", "stage6"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTopoOrder_UnknownDepIgnored(t *testing.T) {
	ids := []string{"a", "b"}
	deps := map[string][]string{"a": {"does-not-exist"}}
	got := topoOrder(ids, deps)
	if !equalSlices(got, ids) {
		t.Fatalf("got %v, want %v (unknown dep should be ignored, not block ordering)", got, ids)
	}
}

func TestBuildStageViews_IsScriptAndPausedFrom(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	rs := state.RunState{
		StageOrder: []string{"a", "b"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusPaused, PausedFrom: state.StatusRunning},
			"b": {Status: state.StatusRunning}, // never paused — PausedFrom must not leak into the view
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, map[string]bool{"a": true}, nil, nil)

	a, b := views[0], views[1]
	if !a.IsScript {
		t.Error("stage a: IsScript = false, want true")
	}
	if a.PausedFrom != state.StatusRunning {
		t.Errorf("stage a: PausedFrom = %q, want %q", a.PausedFrom, state.StatusRunning)
	}
	if b.IsScript {
		t.Error("stage b: IsScript = true, want false")
	}
	if b.PausedFrom != "" {
		t.Errorf("stage b: PausedFrom = %q, want empty (never paused)", b.PausedFrom)
	}
}

// TestBuildStageViews_AutonomousPausedShowsPlan is a regression test for a
// bug found live: an autonomous stage (agents: [auto], no plan.md) that gets
// paused had ShowPlan=false — same as any other non-failed autonomous
// status — so PlanPanel (where the paused section + Continue button live)
// never rendered at all. Only DialogChannel showed, with no way to resume.
// paused must be treated like failed: both need PlanPanel's action button
// regardless of the stage being autonomous.
func TestBuildStageViews_AutonomousPausedShowsPlan(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "a", "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	rs := state.RunState{
		StageOrder: []string{"a"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusPaused, PausedFrom: state.StatusRunning},
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, nil)

	if !views[0].ShowPlan {
		t.Errorf("autonomous stage paused: ShowPlan should be true (Continue button lives in PlanPanel), got %+v", views[0])
	}
}

func TestBuildStageViews_IncludesButtons(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	rs := state.RunState{
		StageOrder: []string{"a"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusRunning},
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, map[string][]string{"a": {"Run linter", "Rebuild"}})

	if !equalSlices(views[0].Buttons, []string{"Run linter", "Rebuild"}) {
		t.Errorf("Buttons = %v, want [Run linter Rebuild]", views[0].Buttons)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
