package server

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	implgraph "github.com/akopichin/afm-engine/testkit/orchestrator/graph"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
)

// Display order follows the recovered graph, including dynamically added dependencies.

func TestRestart_DisplayOrderRecomputedFromSnapshot(t *testing.T) {
	runDir := t.TempDir()
	frozen := []flow.Stage{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}

	dynamic := []state.DynamicStageAdded{
		{Stage: flow.Stage{ID: "n", DependsOn: []string{"a"}}, ExtraDeps: map[string][]string{"b": {"n"}}},
	}
	// The run's restored StageOrder (state.RunState.StageOrder), exactly as Store.AddStageToGraph's own
	// Payload.Order would have recorded it durably.
	restoredOrder := []string{"a", "n", "b"}

	materialized := implgraph.Materialize(frozen, dynamic, restoredOrder)
	for _, s := range materialized {
		if err := os.MkdirAll(filepath.Join(runDir, s.ID), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Same trivial DependsOn mapping cmd/afm's dashboardStageConfig does — buildStageViews/topoOrder only
	// ever reads StageConfig.DependsOn, not the full flow.Stage.
	stages := make(map[string]StageConfig, len(materialized))
	for _, s := range materialized {
		stages[s.ID] = StageConfig{DependsOn: s.DependsOn}
	}

	rs := state.RunState{
		StageOrder: restoredOrder,
		Stages: map[string]state.StageState{
			"a": {Status: state.StatusDone},
			"n": {Status: state.StatusPending},
			"b": {Status: state.StatusPending},
		},
	}

	views := buildStageViews(rs, runDir, stages, nil)
	got := make([]string, len(views))
	for i, v := range views {
		got[i] = v.ID
	}
	if want := []string{"a", "n", "b"}; !slices.Equal(got, want) {
		t.Fatalf("display order after restart = %v, want %v (n must render between its dependency a and its dependent b, reflecting the RESTORED registry, not a stale pre-addition order)", got, want)
	}
}
