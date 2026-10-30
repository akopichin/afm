package bus

import (
	"errors"

	"github.com/akopichin/afm/pkg/agentcomm"
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
	// EvAwaitPeer/EvPeerReplied — Agent Mesh Phase 5 (T5.1): the peer twin of EvAskUser/EvUserAnswered. The
	// stage's executor registered a mesh Wait on a colleague and exited; EvPeerReplied dispatches back to the
	// phase it was in (phaseDispatch), exactly like EvUserAnswered does for a human answer.
	EvAwaitPeer   FSMEvent = "await_peer"
	EvPeerReplied FSMEvent = "peer_replied"
	// EvPeerQuestion — Agent Mesh Phase 5 (T5.5): fired instead of EvPeerReplied when the stage is resumed
	// because a colleague asked it a counter-question while its OWN wait was still pending, not because that
	// wait resolved. Deliberately an exact twin of EvPeerReplied's transition row (same From/To) — the
	// resume mechanics (CAS-protected exactly-once dispatch via phaseDispatch, automatic re-park if the
	// original wait is still open when the resumed phase next exits — see retry.go's hasOpenMeshWait check)
	// do not need to differ at all; only the recorded EVENT differs, for observability and tests.
	EvPeerQuestion FSMEvent = "peer_question"
	// EvPeerWaitEnded — Agent Mesh Phase 5 (T5.6): fired instead of EvPeerReplied when the stage is resumed
	// because its own wait resolved to a terminal-but-unsatisfied outcome (cancelled or expired), not because
	// it was actually answered. An exact twin of EvPeerReplied/EvPeerQuestion's transition row (same From/To)
	// for the same reason EvPeerQuestion already is — only the recorded event differs.
	EvPeerWaitEnded FSMEvent = "peer_wait_ended"
	// EvAwaitQuota — the stage's agent hit a provider usage limit: park durably (GuardCtx.Quota carries until/reason)
	// and let the process exit. Reachable from the statuses EvScheduleRetry is, plus retrying itself.
	EvAwaitQuota FSMEvent = "await_quota"
	// EvQuotaReset — the wait is over: dispatch back to the phase the stage was in (phaseDispatch), like
	// EvUserAnswered. Callers fence it with GuardCtx.ExpectStageSeq of the park they decided it for.
	EvQuotaReset FSMEvent = "quota_reset"
	// EvReserveSummaryHook — a self-transition of a running stage that records a summary step before it happens:
	// the Stop hook reminder, or the launch of a post-exit correction. Fenced by the turn's epoch; the stage
	// does not move, so it resolves no wait and completes nothing.
	EvReserveSummaryHook FSMEvent = "reserve_summary_hook"
)

type GuardCtx struct {
	Stage flow.Stage
	Phase string
	// PausedFrom используется только EvContinue — вызывающий код читает его
	// заранее из Store.PausedFrom(stageID) (см. Orchestrator.Continue).
	PausedFrom state.StageStatus
	// ExpectFrom, when set, narrows the event's From-set to exactly this status: the transition happens only
	// if the stage is STILL in the status the caller decided on. The store's CAS on From makes the read and
	// the write one atomic step, so e.g. a Revise decided for a running agent never moves a stage that a
	// concurrent Pause has just paused.
	ExpectFrom state.StageStatus
	// ExpectEpoch, when set, fences the transition to the stage's execution epoch (state.Transition.ExpectEpoch):
	// a process that exits after someone else took the stage over (Pause, Continue, Revise) cannot move it.
	ExpectEpoch *uint64
	// PeerWaits is recorded on an EvAwaitPeer park: the waits its continuation depends on.
	PeerWaits []agentcomm.WaitID
	// ExpectStageSeq, when set, fences the transition to the stage's latest transition (state.Transition.
	// ExpectStageSeq): a resume decided for one park cannot move a later park.
	ExpectStageSeq *uint64
	// Quota is recorded by EvAwaitQuota, and by EvContinue back into a park (the one-shot manual probe).
	Quota *state.QuotaWait
	// SummaryStep is recorded by the transition that reserves a correction of a missing execution_summary.md:
	// EvScheduleRetry for a post-exit continuation, EvReserveSummaryHook for the Stop hook reminder.
	SummaryStep *state.SummaryStep
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
			EvComplete: {From: []state.StageStatus{state.StatusRunning, state.StatusPlanning, state.StatusAwaitingApproval, state.StatusRetrying, state.StatusAwaitingUserInput, state.StatusAwaitingAssistants, state.StatusAwaitingPeer}, To: to(state.StatusDone)},
			EvFail:     {From: nil, To: to(state.StatusFailed)},
			// EvAwaitAssistants мирно повторяет подмножество From у EvComplete, из которого реально стартует
			// completeStage (orchestrator.go) — см. его собственный doc comment.
			EvAwaitAssistants: {From: []state.StageStatus{state.StatusRunning, state.StatusRetrying, state.StatusAwaitingUserInput}, To: to(state.StatusAwaitingAssistants)},
			// EvVerifyFail — намеренно ЗЕРКАЛИТ From-набор EvComplete, но НЕ включает Paused/Revising: если стадия
			// уже там, её владеет конкурентный Pause/Revise, а не устаревший verify-исход.
			EvVerifyFail: {From: []state.StageStatus{state.StatusRunning, state.StatusAwaitingApproval, state.StatusRetrying, state.StatusAwaitingUserInput, state.StatusAwaitingPeer}, To: to(state.StatusFailed)},
			// EvAskUser must be reachable from any state the question poller scans.
			// Not from revising: a question left open there belongs to the turn the Revise interrupted, and handing the
			// stage to the user would take it from the Revise's feedback run.
			EvAskUser:      {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusRetrying}, To: to(state.StatusAwaitingUserInput)},
			EvUserAnswered: {From: []state.StageStatus{state.StatusAwaitingUserInput}, To: phaseDispatch},
			// EvAwaitPeer — T5.1: the peer twin of EvAskUser, reachable from the same set of live-executor
			// statuses (the executor registers a mesh Wait and exits from whatever phase it was running).
			EvAwaitPeer: {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusRetrying, state.StatusRevising}, To: to(state.StatusAwaitingPeer)},
			// EvPeerReplied — T5.1: the peer twin of EvUserAnswered. phaseDispatch resolves back to
			// Planning/Running exactly as EvUserAnswered does, driven by the SAME ctx.Phase convention.
			EvPeerReplied: {From: []state.StageStatus{state.StatusAwaitingPeer}, To: phaseDispatch},
			// EvPeerQuestion — T5.5: an exact twin of EvPeerReplied's row (see its own doc comment above) used
			// to resume a stage for a bounded counter-question turn instead of a genuine reply.
			EvPeerQuestion: {From: []state.StageStatus{state.StatusAwaitingPeer}, To: phaseDispatch},
			// EvPeerWaitEnded — T5.6: another exact twin, used when the stage's own wait ended WITHOUT being
			// satisfied (cancelled or expired) — see its own doc comment above.
			EvPeerWaitEnded: {From: []state.StageStatus{state.StatusAwaitingPeer}, To: phaseDispatch},
			// EvAwaitQuota — the same live-executor statuses EvScheduleRetry covers, plus Retrying: a retry attempt
			// can hit the limit too. EvQuotaReset returns to the phase; Pause/Fail leave the wait like any other.
			EvAwaitQuota: {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusRetrying, state.StatusAwaitingUserInput, state.StatusAwaitingPeer}, To: to(state.StatusAwaitingQuota)},
			EvQuotaReset: {From: []state.StageStatus{state.StatusAwaitingQuota}, To: phaseDispatch},
			// EvReserveSummaryHook stays in running: it only records the reminder (see its doc comment).
			EvReserveSummaryHook: {From: []state.StageStatus{state.StatusRunning}, To: to(state.StatusRunning)},
			// Разрешаем retry из AwaitingUserInput/AwaitingPeer: поллер мог сменить статус до возврата
			// ретраибельной ошибки агента.
			EvScheduleRetry:    {From: []state.StageStatus{state.StatusPlanning, state.StatusRunning, state.StatusAwaitingUserInput, state.StatusAwaitingPeer}, To: to(state.StatusRetrying)},
			EvResumeAfterRetry: {From: []state.StageStatus{state.StatusRetrying, state.StatusAwaitingUserInput}, To: phaseDispatch},
			EvManualRetry:      {From: []state.StageStatus{state.StatusFailed}, To: to(state.StatusPending)},
			EvBlockedByDep:     {From: []state.StageStatus{state.StatusPending}, To: to(state.StatusFailed)},
			EvReady:            {From: []state.StageStatus{state.StatusPending, state.StatusRetrying}, To: to(state.StatusReady)},
			EvHookFailed:       {From: []state.StageStatus{state.StatusRunning}, To: to(state.StatusHookFailed)},
			EvHookResolved:     {From: []state.StageStatus{state.StatusHookFailed}, To: to(state.StatusRunning)},
			// awaiting_assistants/awaiting_peer/awaiting_quota допускают выход в paused: пользователь может вручную
			// поставить на паузу стадию, пока она ждёт помощников/коллегу по mesh (T5.1: "awaiting_peer...
			// is pausable").
			EvPause:    {From: []state.StageStatus{state.StatusPending, state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying, state.StatusAwaitingAssistants, state.StatusAwaitingPeer, state.StatusAwaitingQuota}, To: to(state.StatusPaused)},
			EvContinue: {From: []state.StageStatus{state.StatusPaused}, To: func(ctx GuardCtx) state.StageStatus { return ctx.PausedFrom }},
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
	if ctx.ExpectFrom != "" && from != ctx.ExpectFrom {
		return from, from, 0, false, nil
	}
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

		PeerWaits:      ctx.PeerWaits,
		Quota:          ctx.Quota,
		SummaryStep:    ctx.SummaryStep,
		ExpectEpoch:    ctx.ExpectEpoch,
		ExpectStageSeq: ctx.ExpectStageSeq,
	}
	if err := f.store.Apply(tr); err != nil {
		if errors.Is(err, state.ErrConcurrentChange) {
			return from, from, 0, false, nil // доброкачественный CAS-mismatch, не storage-fatal
		}
		if errors.Is(err, state.ErrStoreClosed) {
			// Этап 10 (T10.5): run уже штатно закрывается (Store.Close() обнулил eventsLog) — та же
			// доброкачественная классификация, что CAS-mismatch, НЕ fatal StorageError.
			return from, from, 0, false, nil
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
