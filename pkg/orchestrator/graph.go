package orchestrator

import (
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

// Graph is a dependency graph of stages.
type Graph struct {
	stages map[string]*flow.Stage
	deps   map[string][]string // id → depends_on
}

// NewGraph builds a graph from a slice of stages.
func NewGraph(stages []flow.Stage) *Graph {
	g := &Graph{
		stages: make(map[string]*flow.Stage, len(stages)),
		deps:   make(map[string][]string, len(stages)),
	}
	for i := range stages {
		s := &stages[i]
		g.stages[s.ID] = s
		g.deps[s.ID] = s.DependsOn
	}
	return g
}

// ReadyStages returns the IDs of stages that are in StatusReady and whose
// all dependencies are in StatusDone.
func (g *Graph) ReadyStages(statuses map[string]state.StageStatus) []string {
	var ready []string
	for id, deps := range g.deps {
		if statuses[id] != state.StatusReady {
			continue
		}
		allDone := true
		for _, dep := range deps {
			if statuses[dep] != state.StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, id)
		}
	}
	return ready
}

// Stage returns the Stage for a given ID.
func (g *Graph) Stage(id string) *flow.Stage {
	return g.stages[id]
}

// AllIDs returns all stage IDs.
func (g *Graph) AllIDs() []string {
	ids := make([]string, 0, len(g.stages))
	for id := range g.stages {
		ids = append(ids, id)
	}
	return ids
}
