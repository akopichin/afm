package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func TestBuildStageViews_OrdersAndComputesCapabilities(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// "b" is autonomous and failed → plan panel must still show.
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

	views := buildStageViews(rs, runDir, map[string]StageConfig{"a": {Interactive: true, AutoApprove: true}}, nil)

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
	// interactive:true, но pending и без диалоговой истории → строку под диалог НЕ резервируем.
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

	views := buildStageViews(rs, runDir, map[string]StageConfig{
		"script-running": {IsScript: true},
		"script-done":    {IsScript: true},
		"script-failed":  {IsScript: true},
		"script-paused":  {IsScript: true},
	}, nil)
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

	views := buildStageViews(rs, runDir, nil, nil)
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
	deps := map[string]StageConfig{"child": {DependsOn: []string{"parent"}}}
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
	deps := map[string]StageConfig{
		"stage1": {DependsOn: []string{"stage2"}},
		"stage6": {DependsOn: []string{"stage2", "stage3", "stage4", "stage5"}},
	}
	got := topoOrder(ids, deps)
	want := []string{"stage2", "stage3", "stage4", "stage5", "stage1", "stage6"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTopoOrder_UnknownDepIgnored(t *testing.T) {
	ids := []string{"a", "b"}
	deps := map[string]StageConfig{"a": {DependsOn: []string{"does-not-exist"}}}
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

	views := buildStageViews(rs, runDir, map[string]StageConfig{"a": {IsScript: true}}, nil)

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

	views := buildStageViews(rs, runDir, nil, nil)

	if !views[0].ShowPlan {
		t.Errorf("autonomous stage paused: ShowPlan should be true (Continue button lives in PlanPanel), got %+v", views[0])
	}
}

// An autonomous stage that hit a usage limit keeps its plan panel: the quota section and Pause → Continue live there.
func TestBuildStageViews_AutonomousQuotaWaitShowsPlan(t *testing.T) {
	cases := map[string]state.StageState{
		"awaiting_quota":         {Status: state.StatusAwaitingQuota},
		"paused from quota":      {Status: state.StatusPaused, PausedFrom: state.StatusAwaitingQuota},
		"autonomous but running": {Status: state.StatusRunning},
		"autonomous but done":    {Status: state.StatusDone},
	}
	wantPlan := map[string]bool{"awaiting_quota": true, "paused from quota": true}
	for name, stage := range cases {
		t.Run(name, func(t *testing.T) {
			runDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(runDir, "a"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, "a", "autonomous.flag"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			rs := state.RunState{StageOrder: []string{"a"}, Stages: map[string]state.StageState{"a": stage}}

			views := buildStageViews(rs, runDir, nil, nil)

			if views[0].ShowPlan != wantPlan[name] {
				t.Errorf("ShowPlan = %v, want %v for %+v", views[0].ShowPlan, wantPlan[name], views[0])
			}
		})
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

	views := buildStageViews(rs, runDir, map[string]StageConfig{"a": {Buttons: []string{"Run linter", "Rebuild"}}}, nil)

	if !equalSlices(views[0].Buttons, []string{"Run linter", "Rebuild"}) {
		t.Errorf("Buttons = %v, want [Run linter Rebuild]", views[0].Buttons)
	}
}

// A stage added during the run is missing from the start-time StageNames; its name comes from the runtime
// registry's StageConfig, and a flow stage without a registry name keeps its StageNames entry.
func TestBuildStageViews_NameFromRuntimeConfig(t *testing.T) {
	runDir := t.TempDir()
	rs := state.RunState{
		StageOrder: []string{"flow", "added"},
		StageNames: map[string]string{"flow": "From flow"},
		Stages: map[string]state.StageState{
			"flow":  {Status: state.StatusDone},
			"added": {Status: state.StatusRunning},
		},
	}

	views := buildStageViews(rs, runDir, map[string]StageConfig{"added": {Name: "Hardening"}}, nil)

	if views[0].Name != "From flow" || views[1].Name != "Hardening" {
		t.Errorf("names = %q, %q; want From flow, Hardening", views[0].Name, views[1].Name)
	}
}

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

	views := buildStageViews(rs, runDir, nil, stageCosts)
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

func TestStageView_JSONShape_VerifyFieldOmittedOrPresent(t *testing.T) {
	base := StageView{
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

	t.Run("nil Verify omits the key", func(t *testing.T) {
		data, err := json.Marshal(base)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if _, ok := m["verify"]; ok {
			t.Fatalf("StageView JSON must omit \"verify\" when nil, got keys: %v", sortedKeys(m))
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
	})

	t.Run("set Verify serializes as step/command/phase", func(t *testing.T) {
		view := base
		view.Verify = &VerifyView{Step: 2, Command: "codex", Phase: "needs_changes"}
		data, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		raw, ok := m["verify"]
		if !ok {
			t.Fatalf("StageView JSON must carry \"verify\" when set, got keys: %v", sortedKeys(m))
		}
		var got VerifyView
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal verify: %v", err)
		}
		want := VerifyView{Step: 2, Command: "codex", Phase: "needs_changes"}
		if got != want {
			t.Fatalf("verify = %+v, want %+v", got, want)
		}
	})
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

// appendVerifyNotice writes one verify_started/verify_result line to <runDir>/notices.jsonl via the exact
// production call, so the test fixture matches the real on-disk shape byte-for-byte.
func appendVerifyNotice(t *testing.T, runDir, stageID, eventType string, data map[string]any) {
	t.Helper()
	stagefiles.AppendNotice(runDir, stageID, eventType, data)
}

func findStage(views []StageView, id string) *StageView {
	for i := range views {
		if views[i].ID == id {
			return &views[i]
		}
	}
	return nil
}

func TestBuildStageViews_VerifyIndicatorFromNotices(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyStarted), map[string]any{"step": 1, "command": "codex"})
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 1, "command": "codex", "verdict": "needs_changes"})

	rs := state.RunState{
		StageOrder: []string{"build"},
		Stages: map[string]state.StageState{
			"build": {Status: state.StatusFailed},
		},
	}
	views := buildStageViews(rs, runDir, nil, nil)
	v := findStage(views, "build").Verify
	if v == nil || v.Phase != "needs_changes" || v.Step != 1 || v.Command != "codex" {
		t.Fatalf("verify view = %+v, want {1 codex needs_changes}", v)
	}
}

func TestBuildStageViews_VerifyAcceptedStalemate(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyStarted), map[string]any{"step": 1, "command": "codex"})
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 1, "command": "codex", "verdict": "needs_changes"})
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 1, "command": "codex", "verdict": "accepted_stalemate", "reason": "accepted after 2 unchanged round(s) (reviewer/author stalemate)"})

	rs := state.RunState{
		StageOrder: []string{"build"},
		Stages: map[string]state.StageState{
			"build": {Status: state.StatusDone},
		},
	}
	views := buildStageViews(rs, runDir, nil, nil)
	v := findStage(views, "build").Verify
	if v == nil || v.Phase != "accepted_stalemate" || v.Step != 1 || v.Command != "codex" {
		t.Fatalf("verify view = %+v, want {1 codex accepted_stalemate}", v)
	}
}

func TestBuildStageViews_VerifyReRunReturnsRunning(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyStarted), map[string]any{"step": 1, "command": "codex"})
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 1, "command": "codex", "verdict": "pass"})
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyStarted), map[string]any{"step": 2, "command": "codex"})

	rs := state.RunState{
		StageOrder: []string{"build"},
		Stages: map[string]state.StageState{
			"build": {Status: state.StatusRunning},
		},
	}
	views := buildStageViews(rs, runDir, nil, nil)
	v := findStage(views, "build").Verify
	if v == nil || v.Phase != "running" || v.Step != 2 || v.Command != "codex" {
		t.Fatalf("verify view = %+v, want {2 codex running}", v)
	}
}

// TestBuildStageViews_NoVerifyNotices_NilVerify: a stage with no verify
// notices ⇒ Verify == nil (omitempty on the wire, see the JSON-shape test).
func TestBuildStageViews_NoVerifyNotices_NilVerify(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	rs := state.RunState{
		StageOrder: []string{"build"},
		Stages: map[string]state.StageState{
			"build": {Status: state.StatusDone},
		},
	}
	views := buildStageViews(rs, runDir, nil, nil)
	if v := findStage(views, "build").Verify; v != nil {
		t.Fatalf("verify view = %+v, want nil", v)
	}
}

func TestBuildStageViews_VerifySurvivesFarPastNoticeHorizon(t *testing.T) {
	runDir := t.TempDir()
	for _, id := range []string{"build", "other"} {
		if err := os.MkdirAll(filepath.Join(runDir, id), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// The ONLY verify notice for each stage — written FIRST, before the
	// 2000-notice horizon that a capped per-stage scan would enforce.
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyResult), map[string]any{"step": 1, "command": "codex", "verdict": "pass"})
	appendVerifyNotice(t, runDir, "other", string(bus.EventVerifyResult), map[string]any{"step": 3, "command": "codex", "verdict": "needs_changes"})

	// 2100 later notices for EACH stage — comfortably past maxStageReplayEvents=2000 — so a per-stage
	// reconstructNotices ring would have evicted the verify_result above from its window by now.
	writeNotices(t, runDir, "build", 2100)
	writeNotices(t, runDir, "other", 2100)

	rs := state.RunState{
		StageOrder: []string{"build", "other"},
		Stages: map[string]state.StageState{
			"build": {Status: state.StatusDone},
			"other": {Status: state.StatusFailed},
		},
	}
	views := buildStageViews(rs, runDir, nil, nil)

	v := findStage(views, "build").Verify
	if v == nil || v.Phase != "pass" || v.Step != 1 || v.Command != "codex" {
		t.Fatalf("build verify view = %+v, want {1 codex pass} — must survive 2100 later notices past the 2000-notice horizon", v)
	}

	other := findStage(views, "other").Verify
	if other == nil || other.Phase != "needs_changes" || other.Step != 3 || other.Command != "codex" {
		t.Fatalf("other verify view = %+v, want {3 codex needs_changes} — must survive 2100 later notices past the 2000-notice horizon", other)
	}
}

// --- usage-limit wait: StageView.quota -----------------------------------------------------------------------

func stageViewsByID(t *testing.T, stages map[string]state.StageState) map[string]StageView {
	t.Helper()
	ids := make([]string, 0, len(stages))
	for id := range stages {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	rs := state.RunState{StageOrder: ids, Stages: stages}
	out := map[string]StageView{}
	for _, v := range buildStageViews(rs, t.TempDir(), nil, nil) {
		out[v.ID] = v
	}
	return out
}

func TestBuildStageViews_QuotaVisibleOnlyWhileWaitingOrPausedFromIt(t *testing.T) {
	until := time.Date(2026, 10, 23, 23, 51, 3, 123456789, time.UTC)
	reset := time.Date(2026, 10, 23, 23, 50, 0, 0, time.UTC)
	park := &state.QuotaWait{
		Provider: "claude", Window: "five_hour", Source: state.QuotaSourceStructured, ResetAt: reset, Until: until,
		Target: "implementation", Waits: 2, ProgressMark: 7, Feedback: true, ManualProbe: true, VerifyID: "v1", VerifyStep: 3,
	}
	paused := &state.QuotaWait{Provider: "codex", Source: state.QuotaSourceText, ResetAt: reset, PauseReason: state.QuotaPauseExceedsMaxWait}
	views := stageViewsByID(t, map[string]state.StageState{
		"waiting":       {Status: state.StatusAwaitingQuota, Quota: park},
		"paused-reason": {Status: state.StatusPaused, PausedFrom: state.StatusAwaitingQuota, Quota: paused},
		"paused-manual": {Status: state.StatusPaused, PausedFrom: state.StatusAwaitingQuota, Quota: park},
		"paused-other":  {Status: state.StatusPaused, PausedFrom: state.StatusRunning, Quota: park}, // carried series
		"running":       {Status: state.StatusRunning, Quota: park},
		"done":          {Status: state.StatusDone},
		"legacy":        {Status: state.StatusAwaitingQuota}, // journal without quota metadata
	})

	w := views["waiting"].Quota
	if w == nil || w.Provider != "claude" || w.Window != "five_hour" || w.Source != "structured" || w.PauseReason != "" {
		t.Fatalf("waiting quota = %+v", w)
	}
	if !w.Until.Equal(until.Truncate(time.Second)) || !w.ResetAt.Equal(reset) {
		t.Errorf("until/reset = %s / %s", w.Until, w.ResetAt)
	}
	if p := views["paused-reason"].Quota; p == nil || p.PauseReason != "exceeds_max_wait" || p.Provider != "codex" || !p.Until.IsZero() {
		t.Errorf("paused-with-reason quota = %+v", p)
	}
	if p := views["paused-manual"].Quota; p == nil || p.Provider != "claude" || p.PauseReason != "" {
		t.Errorf("manually paused park quota = %+v", p)
	}
	for _, id := range []string{"paused-other", "running", "done", "legacy"} {
		if q := views[id].Quota; q != nil {
			t.Errorf("%s must not carry quota, got %+v", id, q)
		}
	}
}

// The view is a small shared type: RFC3339 UTC, and none of the journal-only bookkeeping.
func TestStageView_QuotaJSONShape(t *testing.T) {
	views := stageViewsByID(t, map[string]state.StageState{"a": {Status: state.StatusAwaitingQuota, Quota: &state.QuotaWait{
		Provider: "claude", Window: "five_hour", Source: state.QuotaSourceProviderHold,
		ResetAt: time.Date(2026, 10, 23, 23, 50, 0, 0, time.FixedZone("UTC+3", 3*3600)),
		Until:   time.Date(2026, 10, 23, 23, 51, 3, 999, time.UTC),
		Target:  "verify", VerifyID: "v1", VerifyStep: 2, Waits: 3, ProgressMark: 9, Feedback: true, ManualProbe: true,
	}}})
	raw, err := json.Marshal(views["a"])
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var quota map[string]any
	if err := json.Unmarshal(top["quota"], &quota); err != nil {
		t.Fatalf("quota: %v (%s)", err, top["quota"])
	}
	want := map[string]any{
		"provider": "claude", "window": "five_hour", "source": "provider_hold",
		"until": "2026-10-23T23:51:03Z", "reset_at": "2026-10-23T20:50:00Z",
	}
	if len(quota) != len(want) {
		t.Errorf("quota keys = %v, want only %v", quota, want)
	}
	for k, v := range want {
		if quota[k] != v {
			t.Errorf("quota[%s] = %v, want %v", k, quota[k], v)
		}
	}
	// A stage without a wait omits the field entirely.
	raw, _ = json.Marshal(StageView{ID: "x", Status: state.StatusRunning})
	if strings.Contains(string(raw), "quota") {
		t.Errorf("quota must be omitted outside a wait: %s", raw)
	}
}

// After a restart the view comes from the journal, not from memory.
func TestBuildStageViews_QuotaSurvivesReplayFromJournal(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 10, 23, 23, 51, 0, 0, time.UTC)
	for _, tr := range []*state.Transition{
		{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "start_run"},
		{StageID: "s1", From: state.StatusRunning, To: state.StatusAwaitingQuota, Event: "await_quota",
			Quota: &state.QuotaWait{Provider: "claude", Source: state.QuotaSourceText, Until: until, Target: "implementation"}},
	} {
		if err := store.Apply(tr); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	reopened, err := state.Open(runDir, []string{"s1"}) // the restart: nothing but events.jsonl carries over
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	views := buildStageViews(reopened.Snapshot(), runDir, nil, nil)
	if len(views) != 1 || views[0].Quota == nil || views[0].Quota.Provider != "claude" || !views[0].Quota.Until.Equal(until) || views[0].Quota.Source != "text" {
		t.Fatalf("replayed view = %+v", views)
	}
}
