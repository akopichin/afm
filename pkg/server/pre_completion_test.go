package server

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

func preCompletionStage(status state.StageStatus, decision state.PreCompletionDecision) state.StageState {
	return state.StageState{
		Status: status,
		PreCompletion: &state.PreCompletionRound{
			ID:       "round-7",
			Phase:    "execution",
			TaskIDs:  []assistants.TaskID{"t-1"},
			Decision: decision,
		},
	}
}

func buildOne(t *testing.T, st state.StageState, cfg StageConfig) StageView {
	t.Helper()
	rs := state.RunState{StageOrder: []string{"a"}, Stages: map[string]state.StageState{"a": st}}
	views := BuildStageViews(rs, t.TempDir(), map[string]StageConfig{"a": cfg}, nil)
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	return views[0]
}

func TestBuildStageViews_PreCompletionReviewing(t *testing.T) {
	st := preCompletionStage(state.StatusAwaitingPreCompletion, state.PreCompletionReviewing)
	v := buildOne(t, st, StageConfig{PreCompletionMaxFailures: 1})

	want := PreCompletionView{
		RoundID: "round-7", Phase: "execution", Decision: state.PreCompletionReviewing,
		CorrectionCount: 0, MaxFailures: 1, TaskIDs: []assistants.TaskID{"t-1"},
	}
	if v.PreCompletion == nil || v.PreCompletion.RoundID != want.RoundID || v.PreCompletion.Decision != want.Decision ||
		v.PreCompletion.MaxFailures != 1 || v.PreCompletion.Phase != want.Phase || len(v.PreCompletion.TaskIDs) != 1 {
		t.Fatalf("PreCompletion = %+v, want %+v", v.PreCompletion, want)
	}
	if v.PreCompletionBlocked {
		t.Fatal("a reviewing round must not be blocked")
	}
}

func TestBuildStageViews_PreCompletionNeedsDecisionIsBlocked(t *testing.T) {
	st := preCompletionStage(state.StatusAwaitingPreCompletion, state.PreCompletionNeedsDecision)
	v := buildOne(t, st, StageConfig{})
	if !v.PreCompletionBlocked {
		t.Fatalf("needs_decision while awaiting must be blocked: %+v", v)
	}
}

func TestBuildStageViews_PreCompletionNotBlockedWhenPaused(t *testing.T) {
	st := preCompletionStage(state.StatusPaused, state.PreCompletionNeedsDecision)
	v := buildOne(t, st, StageConfig{})
	if v.PreCompletion == nil || v.PreCompletionBlocked {
		t.Fatalf("paused stage keeps the round but is not blocked: %+v", v)
	}
}

func TestBuildStageViews_ExplicitZeroBudgetIsKept(t *testing.T) {
	st := preCompletionStage(state.StatusAwaitingPreCompletion, state.PreCompletionReviewing)
	v := buildOne(t, st, StageConfig{PreCompletionMaxFailures: 0})
	if v.PreCompletion == nil || v.PreCompletion.MaxFailures != 0 {
		t.Fatalf("max_failures must serialize as 0: %+v", v.PreCompletion)
	}
	raw, err := json.Marshal(v.PreCompletion)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["max_failures"]; !ok {
		t.Fatalf("max_failures key missing in %s", raw)
	}
}

func TestBuildStageViews_NoRoundNoFields(t *testing.T) {
	v := buildOne(t, state.StageState{Status: state.StatusRunning}, StageConfig{PreCompletionMaxFailures: 1})
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pre_completion", "pre_completion_blocked"} {
		if _, ok := keys[k]; ok {
			t.Errorf("key %q must be absent: %s", k, raw)
		}
	}
}
