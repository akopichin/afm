package orchestrator

import (
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// fsmLifecycleEvents мапит FSM-переходы на публичные lifecycle-события.
// EvReady/EvBlockedByDep — внутренняя планировка (pending→ready и т.п.), им
// нет публичного аналога; EvHookFailed/EvHookResolved относятся к
// script_before/after, чьи события эмитятся на своих границах (Task 10) —
// здесь их нет, чтобы не задваивать.
var fsmLifecycleEvents = map[bus.FSMEvent]lifecyclehooks.EventType{
	bus.EvStartPlanning:    lifecyclehooks.EventStagePlanningStarted,
	bus.EvPlanReady:        lifecyclehooks.EventStagePlanReady,
	bus.EvApprove:          lifecyclehooks.EventStageApproved,
	bus.EvRevise:           lifecyclehooks.EventStageRevisionStarted,
	bus.EvStartRun:         lifecyclehooks.EventStageExecutionStarted,
	bus.EvAskUser:          lifecyclehooks.EventStageQuestionAsked,
	bus.EvUserAnswered:     lifecyclehooks.EventStageQuestionAnswered,
	bus.EvScheduleRetry:    lifecyclehooks.EventStageRetryScheduled,
	bus.EvResumeAfterRetry: lifecyclehooks.EventStageRetryStarted,
	bus.EvManualRetry:      lifecyclehooks.EventStageRetryStarted,
	bus.EvPause:            lifecyclehooks.EventStagePaused,
	bus.EvContinue:         lifecyclehooks.EventStageResumed,
	bus.EvComplete:         lifecyclehooks.EventStageFinished,
	bus.EvFail:             lifecyclehooks.EventStageFailed,
}

// emitLifecycle — точка отправки; nil-safe (хуки не настроены — обычный путь
// тестов и ранов без hooks).
func (o *Orchestrator) emitLifecycle(ev lifecyclehooks.Event) {
	if o.hooks == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	o.hooks.Emit(ev)
}

// emitLifecycleTransition вызывается из triggerWithSeq ТОЛЬКО для успешно
// применённых переходов (seq > 0) — не применённый CAS событие не порождает.
func (o *Orchestrator) emitLifecycleTransition(stageID string, from, to state.StageStatus, seq uint64, ev bus.FSMEvent, reason string) {
	le, ok := fsmLifecycleEvents[ev]
	if !ok {
		return
	}
	o.emitLifecycle(lifecyclehooks.Event{
		Type:      le,
		StageID:   stageID,
		StageName: o.stageName(stageID),
		From:      string(from),
		To:        string(to),
		Phase:     phaseForStatus(from),
		Reason:    reason,
		Seq:       seq,
	})
}

// emitStageEvent — не-FSM границы (script-стадии, script_before/after,
// авто-ответы): без seq, event_id строит dispatcher.
func (o *Orchestrator) emitStageEvent(ev lifecyclehooks.EventType, stageID, reason string) {
	o.emitLifecycle(lifecyclehooks.Event{
		Type:      ev,
		StageID:   stageID,
		StageName: o.stageName(stageID),
		Reason:    reason,
	})
}

// stageName — отображаемое имя стадии (Name, иначе ID).
func (o *Orchestrator) stageName(id string) string {
	if s := o.graph.Stage(id); s != nil {
		if s.Name != "" {
			return s.Name
		}
		return s.ID
	}
	return ""
}

// phaseForStatus — грубая фаза по исходному статусу перехода: planning/
// revising → planning, running/retrying → implementation, остальное "".
func phaseForStatus(st state.StageStatus) string {
	switch st {
	case state.StatusPlanning, state.StatusRevising:
		return phasePlanning
	case state.StatusRunning, state.StatusRetrying:
		return phaseImplementation
	}
	return ""
}
