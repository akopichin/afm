package graph_test

// Этап 9, T9.5 — TestRestart_AddedStageSurvivesWithDependencies: a dynamically-added stage AND the extra
// dependency it gave an existing stage must both survive a durable store close+reopen (replay), not just the
// in-memory WithPlan path dynamic_test.go's TestMaterialize_ReplaysDynamicAdditionsInOrder already covers.
// This exercises the real on-disk round trip: Store.AddStageToGraph -> Close -> Open (replay) ->
// graph.Materialize, the exact sequence cmd/afm/run.go's restart path uses.

import (
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestRestart_AddedStageSurvivesWithDependencies(t *testing.T) {
	dir := t.TempDir()
	frozen := []flow.Stage{
		{ID: "d1"},
		{ID: "d2"},
		{ID: "t", DependsOn: []string{"d1", "d2"}},
	}

	store, err := state.Open(dir, []string{"d1", "d2", "t"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	op := state.StageGraphOperation{
		ID: "op-1",
		Payload: state.StageGraphPayload{
			// n inherits t's CURRENT deps (d1, d2) — the real shape PlanInsertBefore would produce.
			Stage:     flow.Stage{ID: "n", DependsOn: []string{"d1", "d2"}},
			ExtraDeps: map[string][]string{"t": {"n"}},
			Order:     []string{"d1", "d2", "n", "t"},
		},
	}
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	store.Close()

	// Restart: reopen (replay) and re-materialize exactly like cmd/afm/run.go does.
	store2, err := state.Open(dir, []string{"d1", "d2", "t"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer store2.Close()
	snap := store2.Snapshot()

	if len(snap.DynamicStages) != 1 || snap.DynamicStages[0].Stage.ID != "n" {
		t.Fatalf("dynamically added stage did not survive replay: %+v", snap.DynamicStages)
	}

	restored := graph.Materialize(frozen, snap.DynamicStages, snap.StageOrder)
	reg := graph.NewRegistry(restored)

	n, ok := reg.StageByID("n")
	if !ok {
		t.Fatal("restored registry is missing the dynamically added stage n")
	}
	if !slices.Equal(n.DependsOn, []string{"d1", "d2"}) {
		t.Fatalf("n's OWN inherited dependencies did not survive restart: %v, want [d1 d2]", n.DependsOn)
	}

	tgt, ok := reg.StageByID("t")
	if !ok {
		t.Fatal("restored registry is missing t")
	}
	if !slices.Equal(tgt.DependsOn, []string{"d1", "d2", "n"}) {
		t.Fatalf("t's EXTRA dependency on n did not survive restart: %v, want [d1 d2 n]", tgt.DependsOn)
	}

	if _, ok := reg.StageByID("d1"); !ok {
		t.Fatal("frozen stage d1 lost across restart")
	}
	if _, ok := reg.StageByID("d2"); !ok {
		t.Fatal("frozen stage d2 lost across restart")
	}
}
