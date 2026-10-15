package bus

import (
	"errors"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// phasePlanning — локальная копия orchestrator.phasePlanning.
const phasePlanning = string(flow.PhasePlanning)

type FSMEvent string

const (
	EvStartPlanning    FSMEvent = "start_planning"
	EvPlanReady        FSMEvent = "plan_ready"
	EvApprove          FSMEvent = "approve"
	EvRevise           FSMEvent = "revise"
	EvStartRun         FSMEvent = "start_run"
	EvComplete         FSMEvent = "complete"
	EvFail             FSMEvent = "fail"
	EvAskUser          FSMEvent = "ask_user"
	EvUserAnswered     FSMEvent = "user_answered"
	EvScheduleRetry    FSMEvent = "schedule_retry"
	EvResumeAfterRetry FSMEvent = "resume_after_retry"
	EvManualRetry      FSMEvent = "manual_retry"
	EvBlockedByDep     FSMEvent = "blocked_by_dep"
	EvReady            FSMEvent = "ready"
	// EvHookFailed: script_before exhausted its retries — blocks the stage.
	EvHookFailed FSMEvent = "hook_failed"
	// EvHookResolved: user retried (succeeded) or skipped a failed before-hook.
	EvHookResolved FSMEvent = "hook_resolved"
	// EvPause — стадия приостанавливается: либо auto_run:false не дал ей начать первую активацию из Pending,
	// либо пользователь вручную поставил на паузу уже бегущую/ожидающую-повтора стадию.
	EvPause FSMEvent = "pause"
	// EvContinue возвращает стадию из paused туда, откуда она была приостановлена — реальный перезапуск
	// агента делает вызывающий код, а не сама FSM-таблица.
	EvContinue FSMEvent = "continue"
	// EvVerifyFail — H1: АТОМАРНЫЙ аналог EvFail для verify-driven исходов. Не трогает существующий From:nil
	// у EvFail — другие call site'ы EvFail сознательно должны срабатывать из любого статуса.
	EvVerifyFail FSMEvent = "verify_fail"
	// EvAwaitAssistants — Этап 7 (T7.1/T7.2): основной исполнитель стадии закончил свою часть (file-проба и,
	// если задан, независимый verify уже пройдены), но durable completion barrier обнаружил хотя бы одну
	// незакрытую required-задачу помощника. Намеренно зеркалит ТОЛЬКО ту часть From-набора EvComplete, из
	// которой реально стартует completeStage (Running/Retrying/AwaitingUserInput) — Planning/AwaitingApproval
	// принадлежат planning-only завершению, которое барьер не трогает (TestPlanningOnlyStageUnaffected).
	EvAwaitAssistants FSMEvent = "await_assistants"
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

// StorageError signals that FSM.Apply failed because the underlying log write failed — as opposed to a
// benign state.ErrConcurrentChange or ErrNoRule.
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
			// Поллер может перевести ещё живого planning-агента в AwaitingUserInput; валидный план всё равно должен
			// завершить planning.
			EvPlanReady: {From: []state.StageStatus{state.StatusPending, state.StatusPlanning, state.StatusRetrying, state.StatusAwaitingUserInput}, To: to(state.StatusAwaitingApproval)},
			EvApprove:   {From: []state.StageStatus{state.StatusAwaitingApproval}, To: to(state.StatusReady)},
			// awaiting_assistants добавлен для T7.8: старый процесс УЖЕ вышел (main executor done), Revise там —
			// явный запуск исполнения с feedback, а не прерывание живого агента.
			EvRevise: {From: []state.StageStatus{state.StatusAwaitingApproval, state.StatusRunning, state.StatusPaused, state.StatusAwaitingAssistants}, To: to(state.StatusRevising)},
			// Revising тоже разрешён здесь: run<Phase>WithFeedback переводит стадию обратно в Running этим же
			// событием ПЕРЕД повторным runWithRetry — без этого стадия застревала бы в Revising навсегда.
			EvStartRun: {From: []state.StageStatus{state.StatusReady, state.StatusRevising}, To: to(state.StatusRunning)},
			// Разрешаем completion из AwaitingUserInput: поллер мог обнаружить брошенный вопрос до завершения
			// живого агента. AwaitingAssistants добавлен для T7.2: единственный путь ТУДА — через EvAwaitAssistants
			// ниже, единственный путь ОБРАТНО в Done — сюда, после того как барьер подтвердит гейт закрытым.
			EvComplete: {From: []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusAwaitingApproval, state.StatusRetrying, state.StatusAwaitingUserInput, state.StatusAwaitingAssistants}, To: to(state.StatusDone)},
			EvFail:     {From: nil, To: to(state.StatusFailed)},
			// EvAwaitAssistants мирно повторяет подмножество From у EvComplete, из которого реально стартует
			// completeStage (orchestrator.go) — см. его собственный doc comment.
			EvAwaitAssistants: {From: []state.StageStatus{state.StatusRunning, state.StatusRetrying, state.StatusAwaitingUserInput}, To: to(state.StatusAwaitingAssistants)},
			// EvVerifyFail — намеренно ЗЕРКАЛИТ From-набор EvComplete, но НЕ включает Paused/Revising: если стадия
			// уже там, её владеет конкурентный Pause/Revise, а не устаревший verify-исход.
			EvVerifyFail: {From: []state.StageStatus{state.StatusRunning, state.StatusAwaitingApproval, state.StatusRetrying, state.StatusAwaitingUserInput}, To: to(state.StatusFailed)},
			// EvAskUser must be reachable from any state the question poller scans.
			EvAskUser:      {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusRetrying, state.StatusRevising}, To: to(state.StatusAwaitingUserInput)},
			EvUserAnswered: {From: []state.StageStatus{state.StatusAwaitingUserInput}, To: phaseDispatch},
			// Разрешаем retry из AwaitingUserInput: поллер мог сменить статус до возврата ретраибельной ошибки
			// агента.
			EvScheduleRetry:    {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusAwaitingUserInput}, To: to(state.StatusRetrying)},
			EvResumeAfterRetry: {From: []state.StageStatus{state.StatusRetrying, state.StatusAwaitingUserInput}, To: phaseDispatch},
			EvManualRetry:      {From: []state.StageStatus{state.StatusFailed}, To: to(state.StatusPending)},
			EvBlockedByDep:     {From: []state.StageStatus{state.StatusPending}, To: to(state.StatusFailed)},
			EvReady:            {From: []state.StageStatus{state.StatusPending, state.StatusRetrying}, To: to(state.StatusReady)},
			EvHookFailed:       {From: []state.StageStatus{state.StatusRunning}, To: to(state.StatusHookFailed)},
			EvHookResolved:     {From: []state.StageStatus{state.StatusHookFailed}, To: to(state.StatusRunning)},
			// awaiting_assistants добавлен в T7.1's чеклист (выход в paused допустим: пользователь может
			// вручную поставить на паузу стадию, пока она ждёт помощников).
			EvPause: {From: []state.StageStatus{state.StatusPending, state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying, state.StatusAwaitingAssistants}, To: to(state.StatusPaused)},
			EvContinue:         {From: []state.StageStatus{state.StatusPaused}, To: func(ctx GuardCtx) state.StageStatus { return ctx.PausedFrom }},
		},
	}
}

func phaseDispatch(ctx GuardCtx) state.StageStatus {
	if ctx.Phase == phasePlanning {
		return state.StatusPlanning
	}
	return state.StatusRunning
}

// Apply возвращает вместе со статусом и seq применённой transition — единственный надёжный источник seq
// для Trigger, который прикладывает его к live-событию.
func (f *FSM) Apply(stageID string, ev FSMEvent, ctx GuardCtx, reason string) (from, to state.StageStatus, seq uint64, ok bool, err error) {
	current := f.store.Get(stageID)
	rule, ruleOK := f.rules[ev]
	if !ruleOK {
		return current, current, 0, false, ErrNoRule
	}
	from = current
	if !ruleAllowsFrom(rule.From, from) {
		return from, from, 0, false, nil
	}
	to = rule.To(ctx)
	tr := &state.Transition{
		StageID: stageID,
		From:    from,
		To:      to,
		Event:   string(ev),
		Reason:  reason,
	}
	if err := f.store.Apply(tr); err != nil {
		if errors.Is(err, state.ErrConcurrentChange) {
			return from, from, 0, false, nil // доброкачественный CAS-mismatch, не storage-fatal
		}
		return from, from, 0, false, &StorageError{Inner: err}
	}
	return from, to, tr.Seq, true, nil
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
