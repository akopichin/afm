package bus

import (
	"errors"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// phasePlanning — локальная копия orchestrator.phasePlanning (= string(flow.PhasePlanning)).
// Нужна только phaseDispatch ниже; не экспортируется и не алиасится — это
// derived string-константа, а не тип с identity, так что дублирование
// значения в двух пакетах безопасно (в отличие от StorageError ниже, где
// identity типа важна для errors.As).
const phasePlanning = string(flow.PhasePlanning)

type FSMEvent string

const (
	EvStartPlanning      FSMEvent = "start_planning"
	EvPlanReady          FSMEvent = "plan_ready"
	EvApprove            FSMEvent = "approve"
	EvRevise             FSMEvent = "revise"
	EvStartRun           FSMEvent = "start_run"
	EvComplete           FSMEvent = "complete"
	EvFail               FSMEvent = "fail"
	EvAskUser            FSMEvent = "ask_user"
	EvUserAnswered       FSMEvent = "user_answered"
	EvScheduleRetry      FSMEvent = "schedule_retry"
	EvResumeAfterRetry   FSMEvent = "resume_after_retry"
	EvManualRetry        FSMEvent = "manual_retry"
	EvBlockedByDep       FSMEvent = "blocked_by_dep"
	EvReady              FSMEvent = "ready"
	EvSupervisorApproved FSMEvent = "supervisor_approved"
	// EvHookFailed: script_before exhausted its retries — blocks the stage.
	// Only fired for the blocking "before" hook; "after" hook failures do not
	// go through the FSM (the stage is already done and must stay done).
	EvHookFailed FSMEvent = "hook_failed"
	// EvHookResolved: user retried (succeeded) or skipped a failed before-hook.
	EvHookResolved FSMEvent = "hook_resolved"
	// EvPause — стадия приостанавливается: либо auto_run:false не дал ей
	// начать первую активацию из Pending (Task 5's shouldGateAutoRun), либо
	// пользователь вручную поставил на паузу уже бегущую/ожидающую-повтора
	// стадию (Running/Planning/Revising/Retrying).
	EvPause FSMEvent = "pause"
	// EvContinue возвращает стадию из paused туда, откуда она была
	// приостановлена (ctx.PausedFrom) — реальный перезапуск агента делает
	// вызывающий код (Orchestrator.Continue), а не сама FSM-таблица.
	EvContinue FSMEvent = "continue"
)

type GuardCtx struct {
	Stage flow.Stage
	Phase string
	// PausedFrom используется только EvContinue — вызывающий код читает его
	// заранее из Store.PausedFrom(stageID) (см. Orchestrator.Continue).
	PausedFrom state.StageStatus
}

type Rule struct {
	From []state.StageStatus
	To   func(GuardCtx) state.StageStatus
}

type FSM struct {
	rules map[FSMEvent]Rule
	store *state.Store
}

var ErrNoRule = errors.New("no rule for event")

// StorageError signals that FSM.Apply failed because the underlying log
// write failed (authoritative event log append error) — as opposed to a
// benign state.ErrConcurrentChange (CAS mismatch, silently dropped) or
// ErrNoRule (unknown event, logged and dropped without failing the run).
// Callers (orchestrator.Trigger) use errors.As(err, &se *StorageError) to
// decide whether to setFatal() and stop the run. Lives here (not in
// orchestrator/errors.go) because it's constructed only by FSM.Apply below;
// orchestrator/errors.go keeps a `type StorageError = bus.StorageError`
// alias so existing errors.As call sites there and in orchestrator.go don't
// need a bus. prefix — same pattern as the stagefiles.IncompleteWorkError/
// MissingArtifactError aliases from Task 3.
type StorageError struct{ Inner error }

func (e *StorageError) Error() string { return "storage failure: " + e.Inner.Error() }
func (e *StorageError) Unwrap() error { return e.Inner }

func NewFSM(store *state.Store) *FSM {
	to := func(s state.StageStatus) func(GuardCtx) state.StageStatus {
		return func(GuardCtx) state.StageStatus { return s }
	}
	return &FSM{
		store: store,
		rules: map[FSMEvent]Rule{
			EvStartPlanning: {From: []state.StageStatus{state.StatusPending, state.StatusRetrying, state.StatusRevising}, To: to(state.StatusPlanning)},
			EvPlanReady:     {From: []state.StageStatus{state.StatusPending, state.StatusPlanning, state.StatusRetrying}, To: to(state.StatusAwaitingApproval)},
			EvApprove:       {From: []state.StageStatus{state.StatusAwaitingApproval}, To: to(state.StatusReady)},
			EvRevise:        {From: []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning}, To: to(state.StatusRevising)},
			// Revising тоже разрешён здесь: run<Phase>WithFeedback (кроме
			// planning-варианта, у которого свой EvStartPlanning) переводит
			// стадию обратно в Running этим же событием ПЕРЕД повторным
			// runWithRetry — без этого стадия застревала бы в Revising
			// навсегда (onAgentCompleted и EvComplete ждут Running/Retrying).
			EvStartRun: {From: []state.StageStatus{state.StatusReady, state.StatusRevising}, To: to(state.StatusRunning)},
			EvComplete: {From: []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusAwaitingApproval, state.StatusRetrying}, To: to(state.StatusDone)},
			EvFail:     {From: nil, To: to(state.StatusFailed)},
			// EvAskUser must be reachable from any state the question poller scans
			// (planning, running, plus the retry/revision cycles where an agent can
			// ask mid-flight). Without retrying/revising here the transition is
			// silently rejected and the stage never reaches awaiting_user_input,
			// which breaks the cancel path and the onUserAnswered restart path.
			EvAskUser:            {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusRetrying, state.StatusRevising}, To: to(state.StatusAwaitingUserInput)},
			EvUserAnswered:       {From: []state.StageStatus{state.StatusAwaitingUserInput}, To: phaseDispatch},
			EvScheduleRetry:      {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning}, To: to(state.StatusRetrying)},
			EvResumeAfterRetry:   {From: []state.StageStatus{state.StatusRetrying}, To: phaseDispatch},
			EvManualRetry:        {From: []state.StageStatus{state.StatusFailed}, To: to(state.StatusPending)},
			EvBlockedByDep:       {From: []state.StageStatus{state.StatusPending}, To: to(state.StatusFailed)},
			EvReady:              {From: []state.StageStatus{state.StatusPending, state.StatusRetrying}, To: to(state.StatusReady)},
			EvSupervisorApproved: {From: []state.StageStatus{state.StatusPlanning}, To: to(state.StatusReady)},
			EvHookFailed:         {From: []state.StageStatus{state.StatusRunning}, To: to(state.StatusHookFailed)},
			EvHookResolved:       {From: []state.StageStatus{state.StatusHookFailed}, To: to(state.StatusRunning)},
			EvPause:              {From: []state.StageStatus{state.StatusPending, state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying}, To: to(state.StatusPaused)},
			EvContinue:           {From: []state.StageStatus{state.StatusPaused}, To: func(ctx GuardCtx) state.StageStatus { return ctx.PausedFrom }},
		},
	}
}

func phaseDispatch(ctx GuardCtx) state.StageStatus {
	if ctx.Phase == phasePlanning {
		return state.StatusPlanning
	}
	return state.StatusRunning
}

// Apply возвращает вместе со статусом и seq применённой transition (0, если
// переход не применился) — единственный надёжный источник seq для Trigger,
// который прикладывает его к live-событию (дедуп истории /api/events с
// live-потоком на фронте по стабильному ключу, а не по содержимому).
func (f *FSM) Apply(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (state.StageStatus, uint64, bool, error) {
	rule, ok := f.rules[ev]
	if !ok {
		return "", 0, false, ErrNoRule
	}
	from := f.store.Get(stageID)
	if !ruleAllowsFrom(rule.From, from) {
		return from, 0, false, nil
	}
	to := rule.To(ctx)
	tr := &state.Transition{
		StageID: stageID,
		From:    from,
		To:      to,
		Event:   string(ev),
		Reason:  reason,
	}
	if err := f.store.Apply(tr); err != nil {
		if errors.Is(err, state.ErrConcurrentChange) {
			return from, 0, false, nil // доброкачественный CAS-mismatch, не storage-fatal
		}
		return from, 0, false, &StorageError{Inner: err}
	}
	return to, tr.Seq, true, nil
}

func ruleAllowsFrom(allowed []state.StageStatus, from state.StageStatus) bool {
	if len(allowed) == 0 {
		return from != state.StatusDone && from != state.StatusFailed
	}
	for _, a := range allowed {
		if a == from {
			return true
		}
	}
	return false
}

func IsTerminal(s state.StageStatus) bool {
	return s == state.StatusDone || s == state.StatusFailed
}
