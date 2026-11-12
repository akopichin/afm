package main

import (
	"fmt"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/flow"
)

// declaredAssistantLoops lists helper loops in declaration order; the engine creates them on attach, so they
// get its idempotency, redaction and limits. Errors name the stage and profile, never the prompt (user text).
func declaredAssistantLoops(stages []flow.Stage) ([]engine.StageAssistantLoop, error) {
	var loops []engine.StageAssistantLoop
	for _, stage := range stages {
		for _, helper := range stage.Assistants {
			if helper.Loop == nil {
				continue
			}
			every, err := helper.Loop.Interval()
			if err != nil {
				return nil, fmt.Errorf("stage %q: assistant %q: loop.every: %w", stage.ID, helper.Profile, err)
			}
			loops = append(loops, engine.StageAssistantLoop{
				StageID:   stage.ID,
				ProfileID: helper.Profile,
				Every:     every,
				Prompt:    helper.Loop.Prompt,
			})
		}
	}
	return loops, nil
}
