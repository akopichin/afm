package orchestrator

import (
	"context"
	"sync"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A stage turn owns the execution epoch of the transition that started it (its grant), never the epoch it finds
// when it gets a command slot: a takeover committed meanwhile belongs to whoever made it.

type turnEpochKey struct{}

func withTurnEpoch(ctx context.Context, epoch uint64) context.Context {
	return context.WithValue(ctx, turnEpochKey{}, epoch)
}

func turnEpochFrom(ctx context.Context) (uint64, bool) {
	e, ok := ctx.Value(turnEpochKey{}).(uint64)
	return e, ok
}

func (o *Orchestrator) stageEpoch(stageID string) uint64 {
	return o.opts.Store.Snapshot().Stages[stageID].Epoch
}

// grantTurn commits ev as the start of a turn and returns the epoch that turn owns: the commit is fenced to the
// epoch read before it, so no takeover can fall between the two.
func (o *Orchestrator) grantTurn(stageID string, ev bus.FSMEvent, guard bus.GuardCtx, reason string) (uint64, bool) {
	_, epoch, ok := o.grantTurnTo(stageID, ev, guard, reason)
	return epoch, ok
}

func (o *Orchestrator) grantTurnTo(stageID string, ev bus.FSMEvent, guard bus.GuardCtx, reason string) (state.StageStatus, uint64, bool) {
	return o.grantTurnAt(stageID, o.stageEpoch(stageID), ev, guard, reason)
}

func (o *Orchestrator) grantTurnAt(stageID string, epoch uint64, ev bus.FSMEvent, guard bus.GuardCtx, reason string) (state.StageStatus, uint64, bool) {
	guard.ExpectEpoch = &epoch
	to, ok := o.Trigger(stageID, ev, guard, reason)
	if !ok {
		return to, 0, false
	}
	if ev == bus.EvContinue {
		epoch++ // a takeover itself starts the epoch its turn owns
	}
	return to, epoch, true
}

// turnRecord is the live turn of a stage, read by the token mint of its invocations and by the Stop hook policy of
// its author process.
type turnRecord struct {
	epoch uint64

	mu          sync.Mutex
	waitsBefore map[agentcomm.WaitID]agentcomm.WaitStatus // the author's waits before its current run started
}

// startRun records the author's waits as they stood before the run that is about to start, the baseline a yield
// (turnEpisode) is told from.
func (t *turnRecord) startRun(waitsBefore map[agentcomm.WaitID]agentcomm.WaitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waitsBefore = waitsBefore
}

func (t *turnRecord) currentWaitsBefore() map[agentcomm.WaitID]agentcomm.WaitStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.waitsBefore
}

func (o *Orchestrator) mintEpochOf(stageID string) uint64 {
	if t, ok := o.liveTurns.Load(stageID); ok {
		return t.(*turnRecord).epoch
	}
	return o.stageEpoch(stageID)
}
