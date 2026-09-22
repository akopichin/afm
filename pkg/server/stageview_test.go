package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
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
	// still show (Retry lives there). The dialog panel is NOT reserved here:
	// без диалоговой истории и не в awaiting_user_input DialogChannel рендерит
	// пусто, а зарезервированная строка давала пустую дыру (см. showDialog в
	// stageview.go).
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

	views := buildStageViews(rs, runDir, map[string]bool{"a": true}, map[string]bool{"a": true}, map[string]bool{"a": false}, nil, nil, nil)

	if len(views) != 2 || views[0].ID != "b" || views[1].ID != "a" {
		t.Fatalf("order not preserved: %+v", views)
	}

	a, b := views[1], views[0]

	if a.Name != "Stage A" || !a.Interactive || !a.AutoApprove {
		t.Errorf("stage a view wrong: %+v", a)
	}
	if a.ShowPlan {
		t.Errorf("stage a (pending): ShowPlan should be false, got %+v", a)
	}
	// interactive:true, но pending и без диалоговой истории → строку под диалог
	// НЕ резервируем (иначе пустая дыра). Панель появится, когда стадия реально
	// задаст вопрос (awaiting_user_input) или запишет dialog.jsonl (hasDialog).
	if a.ShowDialog {
		t.Errorf("stage a (interactive, pending, no dialog): ShowDialog should be false, got %+v", a)
	}

	if !b.Autonomous {
		t.Errorf("stage b: Autonomous should be true, got %+v", b)
	}
	if !b.ShowPlan {
		t.Errorf("stage b (autonomous but failed): ShowPlan should still be true, got %+v", b)
	}
	// autonomous, но failed и без диалоговой истории → тоже не резервируем.
	if b.ShowDialog {
		t.Errorf("stage b (autonomous, failed, no dialog): ShowDialog should be false, got %+v", b)
	}
}

func TestBuildStageViews_HidesPlanForPendingAndScriptStages(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"pending", "script-running", "script-done", "script-failed", "script-paused"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	rs := state.RunState{
		StageOrder: []string{"pending", "script-running", "script-done", "script-failed", "script-paused"},
		Stages: map[string]state.StageState{
			"pending":        {Status: state.StatusPending},
			"script-running": {Status: state.StatusRunning},
			"script-done":    {Status: state.StatusDone},
			"script-failed":  {Status: state.StatusFailed},
			"script-paused":  {Status: state.StatusPaused},
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, map[string]bool{
		"script-running": true,
		"script-done":    true,
		"script-failed":  true,
		"script-paused":  true,
	}, nil, nil, nil)
	byID := make(map[string]StageView, len(views))
	for _, view := range views {
		byID[view.ID] = view
	}

	for _, id := range []string{"pending", "script-running", "script-done"} {
		if byID[id].ShowPlan {
			t.Errorf("%s: ShowPlan = true, want false", id)
		}
	}
	for _, id := range []string{"script-failed", "script-paused"} {
		if !byID[id].ShowPlan {
			t.Errorf("%s: ShowPlan = false, want true for recovery action", id)
		}
	}
}

// showDialog должен резервировать строку под диалог только когда есть что
// показать: awaiting_user_input (живой вопрос) или уже записанный dialog.jsonl.
func TestBuildStageViews_ShowDialogOnlyWithContent(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"asking", "answered"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// "answered" уже писал диалог на диск → hasDialog=true.
	if err := os.WriteFile(filepath.Join(runDir, "answered", "planning.dialog.jsonl"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rs := state.RunState{
		StageOrder: []string{"asking", "answered"},
		Stages: map[string]state.StageState{
			"asking":   {Status: state.StatusAwaitingUserInput},
			"answered": {Status: state.StatusDone},
		},
	}

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, nil, nil)
	byID := map[string]StageView{}
	for _, v := range views {
		byID[v.ID] = v
	}

	if !byID["asking"].ShowDialog {
		t.Errorf("asking (awaiting_user_input): ShowDialog should be true, got %+v", byID["asking"])
	}
	if !byID["answered"].ShowDialog {
		t.Errorf("answered (hasDialog): ShowDialog should be true, got %+v", byID["answered"])
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

	views := buildStageViews(rs, runDir, nil, nil, map[string]bool{"a": true}, nil, nil, nil)

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

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, nil, nil)

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

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, map[string][]string{"a": {"Run linter", "Rebuild"}}, nil)

	if !equalSlices(views[0].Buttons, []string{"Run linter", "Rebuild"}) {
		t.Errorf("Buttons = %v, want [Run linter Rebuild]", views[0].Buttons)
	}
}

// TestBuildStageViews_SetsCostFromBundle проверяет проброс stageCosts:
// стадия, для которой в bundle.Stages есть запись, получает её в Cost;
// стадия без записи (не участвовавшая в accounting) получает nil, не пустой
// *CostView — фронт отличает "нет данных" от "данные нулевые".
func TestBuildStageViews_SetsCostFromBundle(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	rs := state.RunState{
		StageOrder: []string{"a", "b"},
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusDone},
			"b": {Status: state.StatusDone},
		},
	}
	stageCosts := map[string]*accounting.CostView{
		"a": {DisplayCost: "$0.12", Coverage: "full"},
	}

	views := buildStageViews(rs, runDir, nil, nil, nil, nil, nil, stageCosts)
	byID := map[string]StageView{}
	for _, v := range views {
		byID[v.ID] = v
	}

	if byID["a"].Cost == nil || byID["a"].Cost.DisplayCost != "$0.12" {
		t.Errorf("stage a: Cost = %+v, want DisplayCost=$0.12", byID["a"].Cost)
	}
	if byID["b"].Cost != nil {
		t.Errorf("stage b: Cost = %+v, want nil (no bundle entry)", byID["b"].Cost)
	}
}

// TestStageView_JSONShape_NoVerifyField locks the exact set of JSON keys
// StageView serializes (V5b.4): AI-verify (flow.Stage.Verify becoming a
// struct, V1-V5a) added NO field here — the dashboard renders verify
// progress/results purely from feed events/notices (see feed-view-model.ts),
// not from a DTO field. Any accidental future "verify"-ish addition to
// StageView, and any accidental removal/rename of an existing field, fails
// this test — it's the single guard for "the /api/status shape for a stage
// is unchanged by AI-verify".
func TestStageView_JSONShape_NoVerifyField(t *testing.T) {
	view := StageView{
		ID:          "a",
		Name:        "A",
		Status:      state.StatusPaused,
		Interactive: true,
		Autonomous:  true,
		AutoApprove: true,
		HasDialog:   true,
		IsScript:    true,
		PausedFrom:  state.StatusRunning,
		ShowPlan:    true,
		ShowDialog:  true,
		PreNote:     "note",
		Buttons:     []string{"Run linter"},
		Cost:        &accounting.CostView{DisplayCost: "$0.01"},
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := m["verify"]; ok {
		t.Fatalf("StageView JSON must not carry a \"verify\" field, got keys: %v", sortedKeys(m))
	}

	want := []string{
		"auto_approve", "autonomous", "buttons", "cost", "has_dialog", "id",
		"interactive", "is_script", "name", "paused_from", "pre_note",
		"show_dialog", "show_plan", "status", "updated_at",
	}
	got := sortedKeys(m)
	if !equalSlices(got, want) {
		t.Fatalf("StageView JSON keys changed:\n got  %v\n want %v", got, want)
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
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
