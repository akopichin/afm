package flow

// Phase — имя фазы выполнения стадии в рантайме. В отличие от AgentType
// (агенты, объявляемые в YAML: planning/implementation/review), множество фаз
// включает autonomous_execution — это рантайм-решение супервизора, а НЕ
// допустимое значение поля agents: в YAML. Единый источник правды для всех
// пакетов (orchestrator/server/mcp/prompts).
type Phase string

const (
	PhasePlanning       Phase = "planning"
	PhaseImplementation Phase = "implementation"
	PhaseReview         Phase = "review"
	PhaseAutonomous     Phase = "autonomous_execution"
)

// Phases возвращает все допустимые рантайм-фазы.
func Phases() []Phase {
	return []Phase{PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous}
}

// IsValidPhase сообщает, является ли s допустимым именем фазы.
func IsValidPhase(s string) bool {
	switch Phase(s) {
	case PhasePlanning, PhaseImplementation, PhaseReview, PhaseAutonomous:
		return true
	default:
		return false
	}
}

// PhaseJSONL возвращает имя основного stream-json лога фазы. autonomous-трек
// логируется в autonomous.jsonl (а не autonomous_execution.jsonl).
func PhaseJSONL(p Phase) string {
	if p == PhaseAutonomous {
		return "autonomous.jsonl"
	}
	return string(p) + ".jsonl"
}

// PhaseStreamLogs возвращает все stream-json логи фазы в хронологическом
// порядке (для отображения истории в UI дашборда).
func PhaseStreamLogs(p Phase) []string {
	switch p {
	case PhasePlanning:
		return []string{"planning.jsonl", "planning-reprompt.jsonl", "planning-revision.jsonl"}
	case PhaseImplementation:
		return []string{"implementation.jsonl"}
	case PhaseReview:
		return []string{"review.jsonl"}
	case PhaseAutonomous:
		return []string{"autonomous.jsonl"}
	default:
		return nil
	}
}
