package orchestrator

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// StageFailedError — первая закоммиченная стадия failed при Options.ExitOnFail; Reason уже очищен от секретов.
type StageFailedError struct {
	StageID string
	Reason  string
	Seq     uint64
}

func (e *StageFailedError) Error() string {
	return fmt.Sprintf("stage %s failed: %s", e.StageID, e.Reason)
}

// requiredAssistantFailedReason открывает причину перехода в failed из-за мёртвого required-помощника: по ней
// Retry стадии узнаёт, что дерево задач прежнего поколения нужно заменить новым.
const requiredAssistantFailedReason = "required assistant failed: "

// exitOnFail — состояние политики Options.ExitOnFail.
type exitOnFail struct {
	// mu сериализует каждый FSM-коммит с финализацией рана: коммит до финализации выбирает failure, а
	// финализация до коммита отвергает поздний переход.
	mu sync.Mutex
	// failure — первая закоммиченная стадия failed при живом run context; пишется только под mu.
	failure atomic.Pointer[StageFailedError]
}

// applyTransition применяет FSM-событие; при ExitOnFail коммит, запись первой причины и проверка финализации
// идут под одним mutex.
func (o *Orchestrator) applyTransition(stageID string, ev bus.FSMEvent, ctx bus.GuardCtx, reason string) (from, to state.StageStatus, seq uint64, ok bool, err error) {
	if !o.opts.ExitOnFail {
		return o.fsm.Apply(stageID, ev, ctx, reason)
	}
	o.exit.mu.Lock()
	defer o.exit.mu.Unlock()
	if o.finalizing.Load() {
		current := o.currentStatus(stageID)
		return current, current, 0, false, nil
	}
	// Ctrl+C после коммита не должен отнимать у failed право быть причиной остановки, поэтому «ран жив»
	// фиксируется в момент коммита, а не после него.
	aliveAtCommit := true
	decision := ctx.Fence
	ctx.Fence = func(rs *state.RunState) bool {
		if decision != nil && !decision(rs) {
			return false
		}
		aliveAtCommit = o.runContextAlive()
		return true
	}
	from, to, seq, ok, err = o.fsm.Apply(stageID, ev, ctx, reason)
	if err == nil && ok && to == state.StatusFailed {
		if o.exitOnFailCommittedHook != nil {
			o.exitOnFailCommittedHook(stageID)
		}
		o.recordStageFailure(stageID, reason, seq, aliveAtCommit)
	}
	return from, to, seq, ok, err
}

// recordStageFailure запоминает первую failed-стадию и отменяет ран. Стадии, закоммиченные уже после отмены
// рана (Ctrl+C, соседи первичного провала), причину не меняют. Вызывать под o.exit.mu.
func (o *Orchestrator) recordStageFailure(stageID, reason string, seq uint64, aliveAtCommit bool) {
	if o.exit.failure.Load() != nil || !aliveAtCommit {
		return
	}
	if reason == "" {
		reason = noReasonRecorded
	}
	o.exit.failure.Store(&StageFailedError{StageID: stageID, Reason: o.RedactSecrets(reason), Seq: seq})
	o.cancelRunContext()
}

// installRunContext публикует контекст рана для HTTP-путей и отмены. Провал, закоммиченный раньше Run,
// отменяет ран сразу, до bootstrap.
func (o *Orchestrator) installRunContext(ctx context.Context, cancel context.CancelFunc) {
	o.runMu.Lock()
	o.runCtx = ctx
	o.cancelRun = cancel
	o.runMu.Unlock()
	if o.exit.failure.Load() != nil {
		cancel()
	}
}

// runContextAlive — ран ещё не остановлен; до установки контекста ран считается живым.
func (o *Orchestrator) runContextAlive() bool {
	return o.runContext(context.Background()).Err() == nil
}

// cancelRunContext отменяет ран, если он уже запущен.
func (o *Orchestrator) cancelRunContext() {
	o.runMu.Lock()
	cancel := o.cancelRun
	o.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// awaitInFlightCommit ждёт FSM-коммит, который держит o.exit.mu: он либо уже записал причину провала, либо
// увидит отменённый ран. Без него отмена рана в окне «коммит failed → запись причины» читает пустой stopError.
func (o *Orchestrator) awaitInFlightCommit() {
	if !o.opts.ExitOnFail {
		return
	}
	o.exit.mu.Lock()
	defer o.exit.mu.Unlock()
}

// runAliveFence не даёт переходу, ждавшему exit.mu за коммитом failed, пережить остановку рана этим провалом.
// Nil без ExitOnFail: поведение по умолчанию не меняется.
func (o *Orchestrator) runAliveFence() func(*state.RunState) bool {
	if !o.opts.ExitOnFail {
		return nil
	}
	return func(*state.RunState) bool { return o.runContextAlive() }
}

// stopError — причина остановки рана: сначала storage-fatal, затем первая закоммиченная failed-стадия.
func (o *Orchestrator) stopError() error {
	if err := o.loadFatal(); err != nil {
		return err
	}
	failure := o.exit.failure.Load()
	if failure == nil {
		return nil // типизированный nil-указатель не должен стать непустым error
	}
	return failure
}

// finalizeRun решает естественное завершение рана. При ExitOnFail любая failed-стадия запрещает его, даже если
// причина ещё не записана, а успешная финализация закрывает путь поздним переходам.
func (o *Orchestrator) finalizeRun() bool {
	canFinish := o.runStateAllowsExit
	if o.opts.ExitOnFail {
		o.exit.mu.Lock()
		defer o.exit.mu.Unlock()
		canFinish = func(rs *state.RunState) bool { return !anyStageFailed(rs) && o.runStateAllowsExit(rs) }
	}
	if !o.opts.Store.FinalizeIf(canFinish) {
		return false
	}
	if o.finalizeDecidedHook != nil {
		o.finalizeDecidedHook()
	}
	o.finalizing.Store(true)
	return true
}

// anyStageFailed — есть ли в проекции стадия failed.
func anyStageFailed(rs *state.RunState) bool {
	for _, st := range rs.Stages {
		if st.Status == state.StatusFailed {
			return true
		}
	}
	return false
}

// startupStop решает, нужно ли остановить ран до bootstrap: провал, закоммиченный раньше Run, или (без
// auto_recover) самая ранняя по seq failed-стадия журнала — до запуска любого агента.
func (o *Orchestrator) startupStop() error {
	o.awaitInFlightCommit()
	if err := o.stopError(); err != nil {
		return err
	}
	if !o.opts.ExitOnFail || o.opts.Config.IsAutoRecover() {
		return nil
	}
	rs := o.opts.Store.Snapshot()
	var first *StageFailedError
	for _, s := range o.reg().Stages() {
		st := rs.Stages[s.ID]
		if st.Status == state.StatusFailed && (first == nil || st.Seq < first.Seq) {
			first = &StageFailedError{StageID: s.ID, Seq: st.Seq}
		}
	}
	if first == nil {
		return nil
	}
	if history, err := o.opts.Store.History(); err == nil {
		for _, t := range history {
			if t.Seq == first.Seq {
				first.Reason = t.Reason
			}
		}
	}
	o.exit.mu.Lock()
	defer o.exit.mu.Unlock()
	o.recordStageFailure(first.StageID, first.Reason, first.Seq, o.runContextAlive())
	return o.stopError()
}

// failOnDeadRequiredAssistant переводит стадию с завершившимся автором в failed, если в её required-дереве есть
// failed/cancelled задача без waiver, даже когда более ранний blocker жив. Fence подтверждает решение в момент
// коммита: та же задача мертва, ран жив. true — стадия переведена в failed.
func (o *Orchestrator) failOnDeadRequiredAssistant(stageID string, fence *uint64) bool {
	runCtx := o.runContext(context.Background())
	if !o.opts.ExitOnFail || runCtx.Err() != nil {
		return false
	}
	rs := o.opts.Store.Snapshot()
	st := rs.Stages[stageID]
	switch st.Status {
	case state.StatusRunning, state.StatusRetrying, state.StatusAwaitingUserInput, state.StatusAwaitingAssistants:
	default:
		return false
	}
	if fence != nil && st.Epoch != *fence {
		return false
	}
	dead, found := rs.Assistants.DeadRequiredTask(stageID)
	if !found {
		return false
	}
	if o.requiredFailureDecidedHook != nil {
		o.requiredFailureDecidedHook(stageID)
	}
	stillDead := func(rs *state.RunState) bool {
		current, found := rs.Assistants.DeadRequiredTask(stageID)
		return runCtx.Err() == nil && found && current.ID == dead.ID && current.TaskGeneration == dead.TaskGeneration
	}
	guard := bus.GuardCtx{ExpectFrom: st.Status, ExpectEpoch: &st.Epoch, ExpectStageSeq: &st.Seq, Fence: stillDead}
	reason := fmt.Sprintf("%s%s (%s)", requiredAssistantFailedReason, dead.ID, dead.Status)
	if _, ok := o.Trigger(stageID, bus.EvFail, guard, reason); !ok {
		return false
	}
	o.failBlockedStages()
	return true
}

// finishBarrierFailureBump даёт стадии, повторённой после падения required-помощника, новое поколение задач:
// иначе мёртвая задача прежнего дерева валила бы каждую новую попытку. Ключ — seq перехода в failed, поэтому
// повтор после краша между Retry и bump не двигает поколение дважды; другие причины failed дерево не меняют.
func (o *Orchestrator) finishBarrierFailureBump(stageID string) error {
	if o.opts.Store.Get(stageID) == state.StatusFailed {
		return nil // Retry ещё не было
	}
	seq, ok, err := o.latestBarrierFailure(stageID)
	if err != nil || !ok {
		return err
	}
	opID := barrierFailureBumpOpID(stageID, seq)
	_, committed, err := o.opts.Store.CommittedOperation(opID, opKindBumpRootGeneration)
	if err != nil {
		return err
	}
	if committed {
		o.cancelSupersededTasks(stageID) // краш мог прийтись и на отмену задач
		return nil
	}
	return o.bumpRootGenerationKeyed(stageID, opID)
}

// latestBarrierFailure возвращает seq последнего перехода стадии в failed, если он вызван мёртвым required-помощником.
func (o *Orchestrator) latestBarrierFailure(stageID string) (seq uint64, ok bool, err error) {
	history, err := o.opts.Store.History()
	if err != nil {
		return 0, false, err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if t := history[i]; t.StageID == stageID && t.To == state.StatusFailed {
			return t.Seq, strings.HasPrefix(t.Reason, requiredAssistantFailedReason), nil
		}
	}
	return 0, false, nil
}

// barrierFailureBumpOpID — bump поколения для перехода в failed с номером failSeq.
func barrierFailureBumpOpID(stageID string, failSeq uint64) string {
	return rootBumpOpID(stageID, "barrier-failed-"+strconv.FormatUint(failSeq, 10))
}
