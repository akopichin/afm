package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// Slots stay anchored to creation and skipped slots never queue up; recovery starts in the future.
// The run owns and joins this scheduler before agent and Store cleanup.

// loopTickOperationPrefix starts the operation id of every delivered tick. The id is computable from a loop's
// key, so it is reserved (rejectReservedRequestID): a client cannot occupy it before the scheduler uses it.
const loopTickOperationPrefix = "loop-tick:"

// opKindLoopTick is the kind of the operation that delivers a loop tick.
const opKindLoopTick = "loop_tick"

// loopTickOperationID is the deterministic id of the operation that delivers slot of the loop with loopKey, so
// a tick retried after a restart commits at most once.
func loopTickOperationID(loopKey string, slot uint64) string {
	return fmt.Sprintf("%s%s:%d", loopTickOperationPrefix, loopKey, slot)
}

// loopTickFunc treats admission races as skipped slots; only storage failure ends the run.
type loopTickFunc func(ctx context.Context, loopKey string, slot uint64) error

// wakeSignal wakes one waiting goroutine. The zero value is ready to use, which Orchestrators built by a struct
// literal in tests rely on. A notify while nobody waits is remembered once.
type wakeSignal struct {
	once sync.Once
	ch   chan struct{}
}

func (w *wakeSignal) c() chan struct{} {
	w.once.Do(func() { w.ch = make(chan struct{}, 1) })
	return w.ch
}

func (w *wakeSignal) notify() {
	select {
	case w.c() <- struct{}{}:
	default:
	}
}

// commitLoopTick is the production loopTickFunc.
func (o *Orchestrator) commitLoopTick(ctx context.Context, loopKey string, slot uint64) error {
	return o.commitLoopTickFrom(ctx, o.opts.Store.Snapshot(), loopKey, slot)
}

// commitLoopTickFrom commits the message, slot and optional restart atomically with deterministic IDs.
// flowPauseMu covers admission and commit so PauseFlow cannot overlap that decision.
func (o *Orchestrator) commitLoopTickFrom(ctx context.Context, rs state.RunState, loopKey string, slot uint64) error {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()
	if o.checkLoopTick(rs, loopKey, slot) != nil {
		return nil
	}
	l := rs.Assistants.Loops[loopKey]
	msg, err := o.planLoopTick(rs, *l, slot)
	if err != nil {
		return err
	}
	ticked, err := marshalEffect("loop_ticked", state.Target{LoopKey: loopKey}, state.LoopTickedPayload{Slot: slot})
	if err != nil {
		return err
	}
	effects := append(slices.Clone(msg.effects), ticked)
	helperID, restart := loopRestartStatus(rs, l.TargetID)
	if restart != "" {
		queued, err := taskStatusEffect(helperID, assistants.StatusQueued, loopRestartReason)
		if err != nil {
			return err
		}
		effects = append([]state.Effect{queued}, effects...)
	}
	runCtx := o.runContext(ctx) // the turn this tick starts must not die with the scheduler's pass
	op := state.Operation{
		ID: loopTickOperationID(loopKey, slot), Kind: opKindLoopTick, Actor: state.Actor{Kind: "system", ID: "loop"},
		Effects: effects,
		Expect: state.Preconditions{Check: func(cur *state.RunState) error {
			if err := o.checkLoopTick(*cur, loopKey, slot); err != nil {
				return err
			}
			if _, now := loopRestartStatus(*cur, l.TargetID); now != restart {
				return attachRejected(assistants.CodeInvalidStatus, "helper %s changed from %q to %q since the tick was planned", helperID, restart, now)
			}
			return nil
		}},
	}
	_, err = o.opts.Store.ApplyRunOperation(op, func(res state.OperationResult) {
		if restart != "" {
			if o.testAfterLoopRestartCommit != nil {
				o.testAfterLoopRestartCommit()
			}
			o.afterLoopRestart(helperID, rs.Assistants.Tasks[helperID].Trigger, restart)
		}
		o.deliverCommittedMessages(runCtx, []plannedMessage{msg}, res.ReadyWaits) // starts the queued helper
	})
	var rejected *state.OperationRejectedError
	if errors.As(err, &rejected) {
		return nil // lost the race: a stop, the end of the loop or a busy target got there first
	}
	return err
}

// loopRestartReason is the status reason of a helper a tick put back in the queue.
const loopRestartReason = "loop tick"

// loopRestartStatus requeues only successful resting helpers; running and waiting tasks use normal delivery.
func loopRestartStatus(rs state.RunState, target agentcomm.ParticipantID) (assistants.TaskID, assistants.TaskStatus) {
	if !strings.HasPrefix(string(target), meshHelperPrefix) {
		return "", ""
	}
	t, ok := loopTargetTask(rs, target)
	if !ok || (t.Status != assistants.StatusCompleted && t.Status != assistants.StatusWatching) {
		return "", ""
	}
	return t.ID, t.Status
}

// afterLoopRestart preserves completed watchers' final-pass behavior and claims resting collectors.
// A competing checkpoint fire remains responsible for its committed debt.
func (o *Orchestrator) afterLoopRestart(taskID assistants.TaskID, trigger assistants.Trigger, was assistants.TaskStatus) {
	switch {
	case was == assistants.StatusCompleted && isWatcherTrigger(trigger):
		o.taskFinalPass.Store(taskID, true)
	case was == assistants.StatusWatching:
		o.claimWatcherForRestart(taskID)
	default:
	}
	o.publishTaskQueued(taskID, loopRestartReason)
}

// claimWatcherForRestart marks taskID's collector busy, if it is bound to one, for a turn this restart is about
// to start rather than one the collector's own Ready() decided on.
func (o *Orchestrator) claimWatcherForRestart(taskID assistants.TaskID) {
	o.watchMu.Lock()
	b, ok := o.watchByTask[taskID]
	o.watchMu.Unlock()
	if ok {
		b.coll.SetBusy(true)
	}
}

// watcherFireInFlight excludes a loop restart once the collector has already claimed a checkpoint turn.
func (o *Orchestrator) watcherFireInFlight(taskID assistants.TaskID) bool {
	o.watchMu.Lock()
	b, ok := o.watchByTask[taskID]
	o.watchMu.Unlock()
	return ok && b.coll.Busy()
}

// checkLoopTick revalidates eligibility and cursor under the store lock to reject stale slot plans.
func (o *Orchestrator) checkLoopTick(rs state.RunState, loopKey string, slot uint64) error {
	l, ok := rs.Assistants.Loops[loopKey]
	switch {
	case !ok || l.Stopped():
		return attachRejected(assistants.CodeInvalidStatus, "loop %s is not running", loopKey)
	case slot <= l.LastSlot:
		return attachRejected(assistants.CodeInvalidStatus, "slot %d of loop %s is not after the last delivered slot %d", slot, l.ShortID, l.LastSlot)
	case !o.loopMayTick(rs, *l):
		return attachRejected(assistants.CodeInvalidStatus, "loop %s cannot tick now", l.ShortID)
	}
	if _, restart := loopRestartStatus(rs, l.TargetID); restart != "" {
		return nil // a finished helper is "unreachable", but this very tick restarts it
	}
	return o.rejectUnreachableRecipients(rs, []agentcomm.ParticipantID{l.TargetID})
}

// planLoopTick isolates scheduled messages in their own thread and marks permitted self-delivery.
func (o *Orchestrator) planLoopTick(rs state.RunState, l state.LoopState, slot uint64) (plannedMessage, error) {
	var open []state.Effect
	if _, exists := rs.Assistants.Mesh.Threads[l.ThreadID]; !exists {
		members := conversationMembers(l.CreatorID, []agentcomm.ParticipantID{l.TargetID})
		var err error
		if open, err = threadOpenEffects(rs, l.ThreadID, "Loop "+l.ShortID, agentcomm.ScopeRun, members, 0, l.OriginStageID); err != nil {
			return plannedMessage{}, err
		}
	}
	return o.planMarkedMessage(rs, SendRequest{
		ThreadID: l.ThreadID, Sender: l.CreatorID, Recipients: []agentcomm.ParticipantID{l.TargetID},
		Kind: agentcomm.MessageKindMessage, Body: loopTickBody(l), OriginStage: l.OriginStageID,
	}, l.Key, deriveMeshMessageID(loopTickOperationID(l.Key, slot)), open)
}

type loopScheduler struct {
	o       *Orchestrator
	deliver loopTickFunc
	// next is, per loop key, the first slot not evaluated yet. Never persisted.
	next map[string]uint64
}

// startLoopScheduler runs the scheduler until ctx is done or stop is called; stop waits for the goroutine, so
// that nothing is delivered after it returns.
func (o *Orchestrator) startLoopScheduler(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	deliver := o.loopTick
	if deliver == nil {
		deliver = o.commitLoopTick
	}
	s := &loopScheduler{o: o, deliver: deliver, next: map[string]uint64{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *loopScheduler) run(ctx context.Context) {
	wake := s.o.loopWake.c()
	for ctx.Err() == nil {
		s.deliverDue(ctx)
		delay, armed := s.untilNextSlot()
		if armed && delay <= 0 {
			continue
		}
		s.sleep(ctx, wake, delay, armed)
	}
}

// sleep waits for the next slot (when armed), a create or stop, or the end of the run. The timer is released
// whichever of them comes first.
func (s *loopScheduler) sleep(ctx context.Context, wake <-chan struct{}, delay time.Duration, armed bool) {
	var timerC <-chan time.Time
	if armed {
		timer := s.o.clk().NewTimer(delay)
		defer timer.Stop()
		timerC = timer.C()
	}
	select {
	case <-timerC:
	case <-wake:
	case <-ctx.Done():
	}
}

// deliverDue attempts the tick of every loop that has a slot due now.
func (s *loopScheduler) deliverDue(ctx context.Context) {
	now := s.o.clk().Now()
	for _, l := range s.o.opts.Store.Snapshot().Assistants.OrderedLoops() {
		slot, due := s.dueSlot(l, now)
		if !due || ctx.Err() != nil {
			continue
		}
		// An earlier delivery of this pass, a stop or the end of a stage may have changed the answer since.
		rs := s.o.opts.Store.Snapshot()
		current, known := rs.Assistants.Loops[l.Key]
		if !known || current.Stopped() || !s.o.loopMayTick(rs, *current) {
			continue
		}
		if err := s.deliver(ctx, l.Key, slot); err != nil {
			var storageErr *state.StorageError
			if errors.As(err, &storageErr) {
				log.Printf("FATAL: storage failure delivering a loop tick: %v", err)
				s.o.setFatal(err)
			}
		}
	}
}

// dueSlot reports the slot to attempt for l at now, and moves l's next slot past it. A loop seen for the first
// time starts at its first future slot. When several slots have come due the latest one is the one served.
func (s *loopScheduler) dueSlot(l state.LoopState, now time.Time) (slot uint64, due bool) {
	next, seen := s.next[l.Key]
	switch {
	case l.Stopped():
		delete(s.next, l.Key)
		return 0, false
	case !seen:
		s.next[l.Key] = nextLoopSlot(l, now)
		return 0, false
	case now.Before(l.SlotTime(next)):
		return 0, false
	}
	slot = uint64(now.Sub(l.CreatedAt) / l.Interval)
	s.next[l.Key] = slot + 1
	return slot, slot > l.LastSlot
}

// untilNextSlot is how long until the earliest slot among the loops that may still tick; armed=false when
// there is none and only a create (wake) can give the scheduler something to wait for.
func (s *loopScheduler) untilNextSlot() (delay time.Duration, armed bool) {
	now := s.o.clk().Now()
	rs := s.o.opts.Store.Snapshot()
	for _, l := range rs.Assistants.OrderedLoops() {
		next, seen := s.next[l.Key]
		if !seen || l.Stopped() || loopOverForGood(rs, l) {
			continue
		}
		if d := l.SlotTime(next).Sub(now); !armed || d < delay {
			delay, armed = d, true
		}
	}
	return delay, armed
}

// loopMayTick excludes review pauses, ended loops and unavailable targets before delivery.
// Resting watchers also need an unclaimed checkpoint collector.
func (o *Orchestrator) loopMayTick(rs state.RunState, l state.LoopState) bool {
	if o.reviewTxnActive() {
		return false
	}
	if reason, _ := loopEndReason(rs, l); reason != "" {
		return false
	}
	if !loopTargetReady(rs, l) {
		return false
	}
	t, ok := loopTargetTask(rs, l.TargetID)
	return !ok || t.Status != assistants.StatusWatching || !o.watcherFireInFlight(t.ID)
}

// loopOverForGood reports an end that nothing later can undo, so the loop needs no timer. The end of the flow is
// not one of them: a stage can still be added to it before the run is finalized.
func loopOverForGood(rs state.RunState, l state.LoopState) bool {
	for _, end := range []loopEnd{stageEnd(rs, l.UntilStageID), loopTargetEnd(rs, l.TargetID), loopThreadEnd(rs, l)} {
		if end.final {
			return true
		}
	}
	return false
}

// loopTargetReady skips busy, held and unread targets while allowing durable waits and resting helpers.
// A required helper whose own decision gate remains open cannot restart.
func loopTargetReady(rs state.RunState, l state.LoopState) bool {
	t, ok := loopTargetTask(rs, l.TargetID)
	if !ok {
		return false
	}
	if held, _ := rs.Assistants.EffectivelyHeld(t.ID); held || loopTickUnread(rs, l) {
		return false
	}
	switch t.Status {
	case assistants.StatusWaitingPeer, assistants.StatusWatching:
		return true
	case assistants.StatusCompleted:
		return !taskNeedsHumanDecision(t)
	case assistants.StatusQueued:
		return idleRunAgent(rs, t)
	}
	return false
}

// taskNeedsHumanDecision checks this helper gate independently of whichever blocker appears first.
// A waiver closes that gate and allows later loop turns.
func taskNeedsHumanDecision(t *state.TaskState) bool {
	return t.Mode == assistants.ModeRequired && !t.Waived && !assistants.TaskGateClosed(t.Status, t.LastVerdict, t.LastVerdict != "")
}

// loopTargetTask is the task a loop's target participant runs as.
func loopTargetTask(rs state.RunState, target agentcomm.ParticipantID) (*state.TaskState, bool) {
	if id, isHelper := strings.CutPrefix(string(target), meshHelperPrefix); isHelper {
		t, ok := rs.Assistants.Tasks[assistants.TaskID(id)]
		return t, ok
	}
	id, ok := runTaskForParticipant(rs, target)
	if !ok {
		return nil, false
	}
	t, ok := rs.Assistants.Tasks[id]
	return t, ok
}

// loopTickUnread reports whether the target still has a message of the loop's own thread it has not been
// handed: a new tick would pile up behind it.
func loopTickUnread(rs state.RunState, l state.LoopState) bool {
	mesh := rs.Assistants.Mesh
	for _, id := range mesh.MessagesByThread[l.ThreadID] {
		if mesh.Receipts[id][l.TargetID] == agentcomm.DeliveryAccepted {
			return true
		}
	}
	return false
}
