package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// maxSummaryRecoveries — сколько post-exit продолжений автора разрешено на один пользовательский цикл
// (до Revise/Manual Retry). Отдельный бюджет: не делит счётчики с transport и verify.
const maxSummaryRecoveries = 2

// summaryRecovery — продолжение автора, которое сейчас выполняется или восстановлено после рестарта.
// Хранится в runWithRetry, чтобы transport-повтор внутри recovery не терял адресную инструкцию.
type summaryRecovery struct {
	n      int    // 1..maxSummaryRecoveries
	reason string // stagefiles.SummaryMissing | stagefiles.SummaryEmpty
}

// attemptOwner — права одной горутины runWithRetry на стадию: её interrupt-канал и per-stage epoch.
// epoch двигается только собственными успешными переходами (резерв возвращает новый seq).
type attemptOwner struct {
	interruptCh chan struct{}
	epoch       uint64
}

// epochIsCurrent — не сменился ли владелец стадии с момента, когда его последний раз подтверждали.
func (o *Orchestrator) epochIsCurrent(stageID string, a *attemptOwner) bool {
	return o.opts.Store.Stage(stageID).OwnerEpoch == a.epoch
}

// restoreSummaryRecovery возвращает инструкцию уже зарезервированного продолжения, если автор
// перезапущен (restart, Continue, ответ на вопрос), а summary всё ещё отсутствует/пуст. Бюджет не тратится.
func (o *Orchestrator) restoreSummaryRecovery(stageID, stageDir string) *summaryRecovery {
	used := o.opts.Store.Stage(stageID).SummaryRecoveryUsed
	if used == 0 {
		return nil
	}
	var se *SummaryError
	if !errors.As(stagefiles.CheckAutonomousCompletion(stageDir), &se) {
		return nil
	}
	return &summaryRecovery{n: used, reason: se.Reason}
}

// summaryDecision — исход решения о отсутствующем/пустом summary.
type summaryDecision int

const (
	summaryReserved      summaryDecision = iota // продолжение зарезервировано
	summaryExhausted                            // бюджет исчерпан, стадия провалена
	summaryLeftWithUser                         // открыт вопрос: стадия остаётся у пользователя, бюджет не тронут
	summaryOwnershipLost                        // владельца сменили до durable-перехода
)

// handleMissingSummary решает судьбу отсутствующего/пустого summary: резервирует очередное продолжение
// (возвращает его и true) либо завершает попытку (false: бюджет исчерпан, открыт вопрос или владение потеряно).
func (o *Orchestrator) handleMissingSummary(stageID string, se *SummaryError, owner *attemptOwner,
	onUserInterrupted func()) (*summaryRecovery, bool) {
	if o.summaryDecisionHook != nil {
		o.summaryDecisionHook(stageID)
	}
	next, decision := o.decideMissingSummary(stageID, se, owner)
	// Notice — только после успешного durable-перехода: проигравший гонку не объявляет то, чего не произошло.
	switch decision {
	case summaryReserved:
		o.publishNotice(bus.Event{Type: bus.EventRetryScheduled, StageID: stageID,
			Data: fmt.Sprintf("%s (%s)", reserveReason(next.n, se), se.Path)})
		return next, true
	case summaryExhausted:
		o.publishNotice(bus.Event{Type: bus.EventRetryExhausted, StageID: stageID,
			Data: fmt.Sprintf("summary recovery exhausted: %s (%s)", exhaustedReason(se), se.Path)})
		o.failBlockedStages()
	case summaryOwnershipLost:
		o.yieldIfSuperseded(stageID, phaseAutonomous, owner, onUserInterrupted)
	default: // summaryLeftWithUser: ответ пользователя запустит продолжение, recovery продолжится с ним
	}
	return nil, false
}

func reserveReason(n int, se *SummaryError) string {
	return fmt.Sprintf("summary recovery %d/%d: %s", n, maxSummaryRecoveries, se)
}

func exhaustedReason(se *SummaryError) string {
	return fmt.Sprintf("%s after %d recovery attempts", se, maxSummaryRecoveries)
}

// decideMissingSummary — единственное решение о reserve/fail, целиком под answerMu: тем же замком поллер вопросов
// отдаёт стадию пользователю, поэтому открытый вопрос либо виден здесь (бюджет не тратится, стадия не проваливается),
// либо появится уже после durable-перехода.
func (o *Orchestrator) decideMissingSummary(stageID string, se *SummaryError, owner *attemptOwner) (*summaryRecovery, summaryDecision) {
	o.answerMu.Lock()
	defer o.answerMu.Unlock()
	switch o.leaveForOpenQuestionLocked(stageID, phaseAutonomous, owner) {
	case dialogLeft:
		return nil, summaryLeftWithUser
	case dialogTakenOver:
		return nil, summaryOwnershipLost
	default: // dialogNone: открытого вопроса нет, решаем о бюджете
	}
	used := o.opts.Store.Stage(stageID).SummaryRecoveryUsed
	if used >= maxSummaryRecoveries {
		if _, _, ok := o.ownerTrigger(owner, phaseAutonomous, stageID, bus.EvFail, bus.GuardCtx{}, exhaustedReason(se)); !ok {
			return nil, summaryOwnershipLost
		}
		return nil, summaryExhausted
	}
	next := summaryRecovery{n: used + 1, reason: se.Reason}
	_, seq, ok := o.ownerTrigger(owner, phaseAutonomous, stageID, bus.EvSummaryRecover, bus.GuardCtx{
		SummaryRecovery: &state.SummaryRecovery{Used: next.n, Reason: next.reason},
	}, reserveReason(next.n, se))
	if !ok {
		return nil, summaryOwnershipLost
	}
	owner.epoch = seq
	return &next, summaryReserved
}

// ownerFence — epoch-guard для переходов автономной стадии; у остальных фаз владение не отслеживается.
func ownerFence(phase string, owner *attemptOwner) *uint64 {
	if phase != phaseAutonomous {
		return nil
	}
	return &owner.epoch
}

// failAsOwner — fenced EvFail: устаревший автор не валит стадию, которой уже владеет другой.
func (o *Orchestrator) failAsOwner(stageID string, owner *attemptOwner, reason string, onUserInterrupted func()) {
	if _, _, ok := o.ownerTrigger(owner, phaseAutonomous, stageID, bus.EvFail, bus.GuardCtx{}, reason); !ok {
		o.yieldIfSuperseded(stageID, phaseAutonomous, owner, onUserInterrupted)
		return
	}
	o.failBlockedStages()
}

// yieldIfSuperseded is the single turn-exit decision. Only the turn displaced by THIS Revise
// hands it to feedback; a successor's Revising status is not ownership (status ABA).
// Other phases retain their legacy status-based handling.
func (o *Orchestrator) yieldIfSuperseded(stageID, phase string, owner *attemptOwner, onUserInterrupted func()) bool {
	st := o.opts.Store.Stage(stageID)
	if phase == phaseAutonomous {
		if st.OwnerEpoch == owner.epoch {
			return false
		}
		if st.Status == state.StatusRevising && st.PrevOwnerEpoch == owner.epoch {
			onUserInterrupted()
		}
		return true
	}
	switch st.Status {
	case state.StatusPaused:
		return true
	case state.StatusRevising:
		onUserInterrupted()
		return true
	default:
		return false
	}
}

// ownerTrigger carries the autonomous turn's fence to every durable transition, including failures.
func (o *Orchestrator) ownerTrigger(owner *attemptOwner, phase, stageID string, ev bus.FSMEvent, guard bus.GuardCtx, reason string) (state.StageStatus, uint64, bool) {
	guard.OwnerEpoch = ownerFence(phase, owner)
	return o.triggerWithSeq(stageID, ev, guard, reason)
}

// withOwnerFiles protects shared artifact writes at their actual publication
// boundary. Unique per-pass reports need no gate. Legacy non-autonomous callers
// have no owner token and retain their existing file semantics (residual R5).
func (o *Orchestrator) withOwnerFiles(stageID string, owner *attemptOwner, operation string, write func() error) error {
	if o.beforeOwnerFileHook != nil {
		o.beforeOwnerFileHook(stageID, operation)
	}
	if owner == nil {
		return write()
	}
	err := o.opts.Store.WithStageOwner(stageID, owner.epoch, write)
	if errors.Is(err, state.ErrConcurrentChange) {
		return executor.ErrUserInterrupted
	}
	return err
}

type turnOwnerKey struct{}
type turnLeaseKey struct{}

func turnOwner(ctx context.Context) *attemptOwner {
	owner, _ := ctx.Value(turnOwnerKey{}).(*attemptOwner)
	return owner
}

// summaryRecoveryBlock — адресная инструкция продолжения для автора. Английский: это prompt для агента.
func summaryRecoveryBlock(stageDir string, r *summaryRecovery) string {
	problem := "does not exist"
	if r.reason == stagefiles.SummaryEmpty {
		problem = "exists but is empty"
	}
	path := stagefiles.SummaryPath(stageDir)
	return fmt.Sprintf(`

## Execution summary recovery (%d/%d)

Your previous run ended, but the execution summary file %s %s.
You have %d recovery run(s) left after this one.

Do this now:
1. Inspect the working tree and the real results of your earlier work and checks. Do not assume that anything succeeded — verify it.
2. If the task is complete, write a non-empty execution summary to %s. Say honestly what was done and what was verified.
3. If work remains, finish it first, then write the summary.

Do NOT redo work that is already done, and do not repeat external actions that have side effects.
Background tasks started in an earlier session may have ended without notice; check their actual outcome instead of waiting for a notification.
`, r.n, maxSummaryRecoveries, path, problem, maxSummaryRecoveries-r.n, path)
}
