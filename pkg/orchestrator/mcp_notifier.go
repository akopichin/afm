package orchestrator

import (
	"log"

	"github.com/akopichin/afm/pkg/state"
)

const (
	dataID          = "id"
	dataPhase       = "phase"
	dataQuestion    = "question"
	dataOptions     = "options"
	dataAllowCustom = "allow_custom"
	dataAnswer      = "answer"
)

// McpNotifier bridges pkg/mcp to the orchestrator's EventBus and state.
// It satisfies mcp.Notifier without pkg/mcp importing pkg/orchestrator.
type McpNotifier struct {
	bus *EventBus
	o   *Orchestrator
}

// NewMcpNotifier returns a Notifier that publishes events to bus and
// transitions stage status via the orchestrator.
func NewMcpNotifier(o *Orchestrator) *McpNotifier {
	return &McpNotifier{bus: o.bus, o: o}
}

func (n *McpNotifier) PublishAskUser(stageID, phase, qID, question string, options []string, allowCustom bool) {
	n.bus.Publish(Event{
		Type:    EventAskUser,
		StageID: stageID,
		Data: map[string]any{
			dataID:          qID,
			dataPhase:       phase,
			dataQuestion:    question,
			dataOptions:     options,
			dataAllowCustom: allowCustom,
		},
	})
}

func (n *McpNotifier) PublishUserAnswered(stageID, phase, qID, answer string) {
	n.bus.Publish(Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data: map[string]any{
			dataID:     qID,
			dataPhase:  phase,
			dataAnswer: answer,
		},
	})
}

// SetStageStatus transitions a stage to/from awaiting_user_input.
func (n *McpNotifier) SetStageStatus(stageID string, awaitingInput bool, phase string) {
	var target state.StageStatus
	if awaitingInput {
		target = state.StatusAwaitingUserInput
	} else if phase == phasePlanning {
		target = state.StatusPlanning
	} else {
		target = state.StatusRunning
	}

	current := n.o.currentStatus(stageID)
	if !ValidTransition(current, target) {
		log.Printf("mcp_notifier: invalid transition %s -> %s for stage %s, skipping", current, target, stageID)
		return
	}
	n.o.setStatus(stageID, target)
}
