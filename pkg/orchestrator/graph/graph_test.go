package graph_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func makeStages(specs []struct {
	id   string
	deps []string
}) []flow.Stage {
	stages := make([]flow.Stage, len(specs))
	for i, s := range specs {
		stages[i] = flow.Stage{ID: s.id, DependsOn: s.deps, Agents: []flow.AgentType{flow.AgentPlanning}}
	}
	return stages
}

func TestReadyStagesNoDeps(t *testing.T) {
	stages := makeStages([]struct {
		id   string
		deps []string
	}{
		{"a", nil}, {"b", nil}, {"c", nil},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusReady,
		"b": state.StatusReady,
		"c": state.StatusDone,
	}
	g := graph.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready stages, got %d: %v", len(ready), ready)
	}
}

func TestReadyStagesBlockedByDep(t *testing.T) {
	stages := makeStages([]struct {
		id   string
		deps []string
	}{
		{"a", nil},
		{"b", []string{"a"}},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusRunning,
		"b": state.StatusReady,
	}
	g := graph.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 0 {
		t.Errorf("b should be blocked: got %v", ready)
	}
}

func TestReadyStagesDepDone(t *testing.T) {
	stages := makeStages([]struct {
		id   string
		deps []string
	}{
		{"a", nil},
		{"b", []string{"a"}},
	})
	statuses := map[string]state.StageStatus{
		"a": state.StatusDone,
		"b": state.StatusReady,
	}
	g := graph.NewGraph(stages)
	ready := g.ReadyStages(statuses)
	if len(ready) != 1 || ready[0] != "b" {
		t.Errorf("b should be ready when a is done: got %v", ready)
	}
}
