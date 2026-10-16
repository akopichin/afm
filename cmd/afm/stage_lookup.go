package main

import (
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
)

// findStageDefinition looks up a stage's STATIC definition (name, buttons, is-script, depends_on, ...) for an
// existing run directory via that run's own durably persisted stage registry (graph_registry.json,
// pkg/orchestrator/graph.LoadRegistry) — never by re-parsing flow.yaml (T8.4/T8.6: a run's stage definitions
// are frozen at the run's start; flow.yaml may have changed or moved since).
//
// found is false when runDir has no persisted registry yet (e.g. a run directory from before Stage 8, or one
// `afm run` never actually started in) or when stageID is not part of the stored graph.
func findStageDefinition(runDir, stageID string) (flow.Stage, bool, error) {
	stages, found, err := graph.LoadRegistry(runDir)
	if err != nil {
		return flow.Stage{}, false, err
	}
	if !found {
		return flow.Stage{}, false, nil
	}
	s, ok := graph.NewRegistry(stages).StageByID(stageID)
	return s, ok, nil
}
