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

	views := buildStageViews(rs, runDir, map[string]bool{"a": true}, map[string]bool{"a": true})

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
