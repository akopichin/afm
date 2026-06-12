package orchestrator

import "context"

const (
	dataID          = "id"
	dataPhase       = "phase"
	dataQuestion    = "question"
	dataOptions     = "options"
	dataAllowCustom = "allow_custom"
	dataAnswer      = "answer"
)

// McpNotifier bridges pkg/mcp to the orchestrator's buses and state.
// It satisfies mcp.Notifier without pkg/mcp importing pkg/orchestrator.
type McpNotifier struct {
	ui       *UIBus
	critical *CriticalBus
	o        *Orchestrator
}

// NewMcpNotifier returns a Notifier that publishes events to the buses and
// transitions stage status via the orchestrator.
func NewMcpNotifier(o *Orchestrator) *McpNotifier {
	return &McpNotifier{ui: o.ui, critical: o.critical, o: o}
}

func (n *McpNotifier) PublishAskUser(stageID, phase, qID, question string, options []string, allowCustom bool) {
	n.ui.Publish(Event{
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
	_ = n.critical.Publish(context.Background(), Event{
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
	if awaitingInput {
		n.o.Trigger(stageID, EvAskUser, GuardCtx{Phase: phase}, "")
	} else {
		n.o.Trigger(stageID, EvUserAnswered, GuardCtx{Phase: phase}, "")
	}
}
