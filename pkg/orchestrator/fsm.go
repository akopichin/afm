package orchestrator

import (
	"errors"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

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
)

type GuardCtx struct {
	Stage flow.Stage
	Phase string
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
