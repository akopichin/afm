package orchestrator

import (
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// fsmLifecycleEvents мапит FSM-переходы на публичные lifecycle-события.
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
	// Ожидание usage-limit провайдера: reason несёт момент пробуждения с явной UTC (QuotaWaitMessage).
	bus.EvAwaitQuota: lifecyclehooks.EventStageAwaitingQuota,
	bus.EvQuotaReset: lifecyclehooks.EventStageQuotaReset,
	// EvVerifyFail — атомарный verify-driven аналог EvFail: тот же публичный исход stage_failed для
	// потребителей lifecycle-хуков, просто с другим FSM-событием внутри.
	bus.EvVerifyFail: lifecyclehooks.EventStageFailed,
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
	if !ok || o.hooks == nil {
		return
	}
	event := lifecyclehooks.Event{
		Type:      le,
		StageID:   stageID,
		StageName: o.stageName(stageID),
		From:      string(from),
		To:        string(to),
		Phase:     phaseForStatus(from),
		Reason:    reason,
		Seq:       seq,
	}
	if le == lifecyclehooks.EventStageFinished || le == lifecyclehooks.EventStageFailed {
		analytics := o.stageAnalytics(stageID, seq)
		event.Analytics, event.Time = &analytics, analytics.AsOf
	}
	o.emitLifecycle(event)
}

// Why a terminal snapshot's timing is unknown.
const (
	timingReasonStageAdvanced = "stage_advanced_before_snapshot"
	timingReasonUnrecorded    = "started_before_recording"
)

// stageAnalytics freezes the stage's cumulative time, jobs and tokens at its terminal transition seq. It runs
// after the CAS, so a Retry may already have moved the stage: everything is cut at the transition's own time.
func (o *Orchestrator) stageAnalytics(stageID string, seq uint64) lifecyclehooks.StageAnalytics {
	at, timing, current, runningTurns := o.terminalMoment(stageID, seq)
	snap := o.stageSnapshot(stageID, at, runningTurns)
	return lifecyclehooks.StageAnalytics{
		Version:       lifecyclehooks.AnalyticsVersion,
		AsOf:          at.UTC(),
		StageEventSeq: seq,
		UsageRevision: snap.UsageRevision,
		JobsRevision:  snap.JobsRevision,
		Scope:         lifecyclehooks.AnalyticsScopeStageCumulative,
		Timing:        analyticsTiming(timing, current),
		AgentJobs:     analyticsJobs(snap.Jobs),
		Usage:         analyticsUsage(snap.Usage),
	}
}

// terminalMoment finds when transition seq happened. While the stage still rests in it, its timing is exact: a
// terminal status accumulates nothing. runningTurns are the turns its helpers run now, by task ID.
func (o *Orchestrator) terminalMoment(stageID string, seq uint64) (at time.Time, timing state.StageTiming, current bool, runningTurns map[string]int) {
	runningTurns = map[string]int{}
	o.opts.Store.View(func(rs *state.RunState) {
		if st := rs.Stages[stageID]; st.Seq == seq {
			at, timing, current = st.UpdatedAt, st.Timing, true
		}
		for id, t := range rs.Assistants.Tasks {
			if t.RootStageID == stageID && t.Status == assistants.StatusRunning {
				runningTurns[string(id)] = int(t.Turn)
			}
		}
	})
	if current {
		return at, timing, true, runningTurns
	}
	history, _ := o.opts.Store.History()
	for _, tr := range slices.Backward(history) {
		if tr.Seq == seq {
			return tr.Time, state.StageTiming{}, false, runningTurns
		}
	}
	return o.clk().Now(), state.StageTiming{}, false, runningTurns
}

func analyticsTiming(tm state.StageTiming, current bool) lifecyclehooks.AnalyticsTiming {
	switch {
	case !current:
		return lifecyclehooks.AnalyticsTiming{Reason: timingReasonStageAdvanced}
	case tm.Incomplete:
		return lifecyclehooks.AnalyticsTiming{Reason: timingReasonUnrecorded}
	}
	waiting := tm.ApprovalMs + tm.HelpersMs + tm.BackoffMs + tm.SchedulingMs
	return lifecyclehooks.AnalyticsTiming{
		TotalMS:      lifecyclehooks.Known(tm.WorkMs + waiting + tm.PausedMs),
		WorkMS:       lifecyclehooks.Known(tm.WorkMs),
		WaitingMS:    lifecyclehooks.Known(waiting),
		PausedMS:     lifecyclehooks.Known(tm.PausedMs),
		ApprovalMS:   lifecyclehooks.Known(tm.ApprovalMs),
		HelpersMS:    lifecyclehooks.Known(tm.HelpersMs),
		BackoffMS:    lifecyclehooks.Known(tm.BackoffMs),
		SchedulingMS: lifecyclehooks.Known(tm.SchedulingMs),
		Complete:     true,
	}
}

func analyticsJobs(j accounting.StageJobs) lifecyclehooks.AnalyticsAgentJobs {
	out := lifecyclehooks.AnalyticsAgentJobs{Complete: j.Complete, Reason: primaryReason(j.Reasons)}
	if j.Known {
		out.Count, out.Active, out.Measured = lifecyclehooks.Known(j.Count), lifecyclehooks.Known(j.Active), lifecyclehooks.Known(j.Measured)
		out.UnknownTime, out.DurationMS = lifecyclehooks.Known(j.UnknownTime), lifecyclehooks.Known(j.DurationMS)
	}
	return out
}

func analyticsUsage(u accounting.StageUsage) lifecyclehooks.AnalyticsUsage {
	out := lifecyclehooks.AnalyticsUsage{Health: string(u.Health), Coverage: string(u.Coverage), Reason: primaryReason(u.Reasons)}
	if u.Tokens == nil {
		return out
	}
	out.MeteredJobs, out.UnmeteredJobs, out.PendingJobs = lifecyclehooks.Known(u.Metered), lifecyclehooks.Known(u.Unmetered), lifecyclehooks.Known(u.Pending)
	out.Tokens = lifecyclehooks.Known(lifecyclehooks.AnalyticsTokens{
		UncachedInput:   u.Tokens.UncachedInput,
		CacheRead:       u.Tokens.CacheRead,
		CacheWrite5m:    u.Tokens.CacheWrite5m,
		CacheWrite1h:    u.Tokens.CacheWrite1h,
		CacheWriteOther: u.Tokens.CacheWriteOther,
		Output:          u.Tokens.Output,
		Total:           u.Tokens.Total(),
	})
	return out
}

// countedReasons are already visible in the snapshot's own counts (active, unknown_time, pending, unmetered).
var countedReasons = map[string]bool{
	accounting.ReasonActiveJobs: true, accounting.ReasonInterruptedJob: true,
	accounting.ReasonPendingUsage: true, accounting.ReasonUnmeteredJob: true,
}

// primaryReason picks the one reason the DTO has room for, preferring one that no count shows.
func primaryReason(reasons []string) string {
	for _, r := range reasons {
		if !countedReasons[r] {
			return r
		}
	}
	if len(reasons) > 0 {
		return reasons[0]
	}
	return ""
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
	if s := o.reg().Stage(id); s != nil {
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

// PublishLifecycleHookFailure — dashboard-warning о финальном сбое lifecycle хука: live в ui-шину +
// durable в notices.jsonl.
func (o *Orchestrator) PublishLifecycleHookFailure(hookID, eventID string, err error) {
	const keyError = "error" // matches the existing "error" key used by publishHookNotice (hooks.go)
	data := map[string]string{"hook_id": hookID, "event_id": eventID, keyError: err.Error()}
	o.publishNotice(bus.Event{Type: bus.EventLifecycleHookFailed, Data: data})
}
