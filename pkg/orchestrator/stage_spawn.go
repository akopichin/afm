package orchestrator

import (
	"context"
	"path/filepath"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// Значения runnerKind — какой из 8 раннеров (agents.go) сейчас/последним
// активирован для стадии. См. комментарий у поля runnerKind.
const (
	kindPlanning       = "planning"
	kindImplementation = "implementation"
	kindReview         = "review"
	kindAutonomous     = "autonomous"
)

// setRunnerKind фиксирует, каким раннером активирована стадия stageID.
// Вызывается первой строкой в каждом из 8 run*Agent/run*WithFeedback.
func (o *Orchestrator) setRunnerKind(stageID, kind string) {
	o.runnerKind.Store(stageID, kind)
}

// clearRunnerKind удаляет текущий тип раннера при завершении, ошибке или ожидании пользователя.
func (o *Orchestrator) clearRunnerKind(stageID string) {
	o.runnerKind.Delete(stageID)
}

// runnerKindOf возвращает текущий runnerKind стадии либо "" если ни разу не
// выставлялся (или уже снят).
func (o *Orchestrator) runnerKindOf(stageID string) string {
	if v, ok := o.runnerKind.Load(stageID); ok {
		return v.(string)
	}
	return ""
}

// spawnAgentLeased exposes each stage lease to RunVerification so verifier slots count toward max_parallel.
// The callback owns registration and conditional cleanup, including when Revise starts a replacement.
func (o *Orchestrator) spawnAgentLeased(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	if _, ok := turnEpochFrom(ctx); !ok && o.opts.Store != nil {
		ctx = withTurnEpoch(ctx, o.stageEpoch(s.ID)) // the spawn decided now, not when the slot frees
	}
	o.concurrency.SpawnAgentLease(ctx, s, func(ctx context.Context, s flow.Stage, lease *concurrency.Lease) {
		o.stageLeases.Store(s.ID, lease)
		// CompareAndDelete, а не безусловный Delete — на Revise-прерывании раннер-замена может Store'ить
		// свой lease под тем же stageID ДО того, как ЭТОТ callback размотает свой defer.
		defer o.stageLeases.CompareAndDelete(s.ID, lease)
		run(ctx, s)
	})
}

func (o *Orchestrator) spawnKind(ctx context.Context, s flow.Stage, kind string, run func(context.Context, flow.Stage)) {
	o.setRunnerKind(s.ID, kind)
	o.attachDeclaredAssistants(ctx, s, kind) // before the spawn below — see its own doc comment for why
	o.spawnAgentLeased(ctx, s, run)
}

// computeResumeKind determines which of the runner variants a paused stage should be resumed with.
func (o *Orchestrator) computeResumeKind(s flow.Stage, pausedFrom state.StageStatus) string {
	if s.IsAuto() || isAutonomousStage(filepath.Join(o.opts.RunDir, s.ID)) {
		return kindAutonomous
	}
	if k := o.runnerKindOf(s.ID); k != "" {
		return k // authoritative: what was actually spawned
	}
	if pausedFrom == state.StatusPlanning {
		return kindPlanning
	}
	return kindImplementation
}

// withFeedbackRunner maps a resume_kind to the *WithFeedback variant of the matching runner — used by
// resumeOwner when the resumed stage must see a feedback.md.
func (o *Orchestrator) withFeedbackRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningWithFeedback
	case kindReview:
		return o.runReviewWithFeedback
	case kindAutonomous:
		return o.runAutonomousWithFeedback
	default:
		return o.runImplementationWithFeedback
	}
}

// plainRunner maps a resume_kind to the plain (fresh, no feedback.md) variant of the matching runner —
// used by resumeOwner for a non-target owner that wasn't paused mid-revising.
func (o *Orchestrator) plainRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningAgent
	case kindReview:
		return o.runReviewAgent
	case kindAutonomous:
		return o.runAutonomousAgent
	default:
		return o.runImplementationAgent
	}
}

// kindOfPhase is the runner kind that executes a phase (the inverse of executionPhases and phasePlanning).
func kindOfPhase(phase string) (string, bool) {
	switch phase {
	case phasePlanning:
		return kindPlanning, true
	case phaseImplementation:
		return kindImplementation, true
	case phaseReview:
		return kindReview, true
	case phaseAutonomous:
		return kindAutonomous, true
	}
	return "", false
}

// turnRunner is the runner of a resumed turn: the feedback run of a Revise (feedbackTurnRunner) or a fresh agent.
func (o *Orchestrator) turnRunner(kind string, feedback bool) func(context.Context, flow.Stage) {
	if feedback {
		return o.feedbackTurnRunner(kind)
	}
	return o.plainRunner(kind)
}

// feedbackTurnRunner maps a resume_kind to the body of the feedback run of that kind WITHOUT claimRevise — what
// the wake of a parked feedback turn runs: the stage is no longer revising, and the turn must see feedback.md.
func (o *Orchestrator) feedbackTurnRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningFeedbackTurn
	case kindReview:
		return o.runReviewFeedbackTurn
	case kindAutonomous:
		return o.runAutonomousFeedbackTurn
	default:
		return o.runImplementationFeedbackTurn
	}
}

// spawnFeedbackRun hands a Revise that overtook its turn's completion to the feedback run of kind: the run's claimRevise
// lets only one hand-off of that Revise start an agent.
func (o *Orchestrator) spawnFeedbackRun(ctx context.Context, stageID, kind string) {
	if stage := o.reg().Stage(stageID); stage != nil {
		o.spawnKind(ctx, *stage, kind, o.withFeedbackRunner(kind))
	}
}

// feedbackKindOf is the runner kind of the feedback run for a turn of agentType.
func feedbackKindOf(agentType string) string {
	switch agentType {
	case phaseImplementation:
		return kindImplementation
	case phaseAutonomous:
		return kindAutonomous
	default:
		return kindReview
	}
}
