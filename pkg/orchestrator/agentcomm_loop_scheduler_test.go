package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// One fake clock anchors both journal commits and scheduler deadlines.
// Only attempted slots are observed, without replacing production timing with wall-clock sleeps.

// recordedTick is one delivery the scheduler attempted: the loop's short id and its slot.
type recordedTick struct {
	loop string
	slot uint64
}

type schedFixture struct {
	loopFixture
	clk   *clock.FakeClock
	ticks chan recordedTick
}

func newSchedFixture(t *testing.T) *schedFixture { return newSchedFixtureWith(t, true) }

// newSchedFixtureWith builds the run; withHelper adds a stage helper that stays mid-turn until the test ends.
func newSchedFixtureWith(t *testing.T, withHelper bool) *schedFixture {
	t.Helper()
	fc := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	state.SetClockForTest(fc)
	t.Cleanup(func() { state.SetClockForTest(nil) })

	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: loopOtherStageID, Command: "claude"})
	o.clock = fc
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, loopOtherStageID)
	stubPassingHelper(o)
	msg := "start"
	added, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: "op-loop-qa", ProfileID: "qa", Title: "My QA", Instruction: "review", FirstMessage: &msg, Actor: loopUser,
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	waitForRunAgentTurn(t, store, added.TaskID, 10*time.Second)
	var helper agentcomm.ParticipantID
	if withHelper {
		helper = meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-loop-helper"))
	}

	f := &schedFixture{
		loopFixture: loopFixture{o: o, store: store, agent: added.ParticipantID, helper: helper},
		clk:         fc,
		ticks:       make(chan recordedTick, 64),
	}
	o.loopTick = f.record
	return f
}

// record is the fake delivery: it notes the attempt and commits nothing.
func (f *schedFixture) record(_ context.Context, key string, slot uint64) error {
	f.ticks <- recordedTick{loop: f.store.Snapshot().Assistants.Loops[key].ShortID, slot: slot}
	return nil
}

// start runs the scheduler under the test's context and stops it at the end of the test.
func (f *schedFixture) start(t *testing.T) (stop func()) {
	t.Helper()
	stop = f.o.startLoopScheduler(t.Context())
	t.Cleanup(stop)
	return stop
}

func (f *schedFixture) requireTick(t *testing.T, loop string, slot uint64) {
	t.Helper()
	select {
	case got := <-f.ticks:
		if got != (recordedTick{loop, slot}) {
			t.Fatalf("tick = %+v, want %s slot %d", got, loop, slot)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no tick for %s slot %d", loop, slot)
	}
}

// requireNoTick is valid only after the fake-clock timer or an explicit barrier let the scheduler run.
func (f *schedFixture) requireNoTick(t *testing.T) {
	t.Helper()
	select {
	case got := <-f.ticks:
		t.Fatalf("unexpected tick %+v", got)
	default:
	}
}

// advanceAndSettle moves the clock and waits until the scheduler is idle again with its next timer armed.
func (f *schedFixture) advanceAndSettle(d time.Duration) {
	f.clk.Advance(d)
	f.clk.BlockUntil(1)
}

func failStage(t *testing.T, o *Orchestrator, stageID string) {
	t.Helper()
	if _, ok := o.Trigger(stageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatalf("EvFail failed for %s", stageID)
	}
}

func TestLoopScheduler_TicksOnSlotsFixedToCreation(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(1)

	f.clk.Advance(59 * time.Second)
	f.requireNoTick(t) // the first tick is one interval after creation, never earlier
	f.clk.Advance(time.Second)
	f.requireTick(t, "L1", 1)
	f.clk.BlockUntil(1)

	// A tick that is served late does not move the grid: slot 2 stays at creation + 2m.
	f.clk.Advance(30 * time.Second)
	f.requireNoTick(t)
	f.clk.Advance(30 * time.Second)
	f.requireTick(t, "L1", 2)
}

func TestLoopScheduler_LateWakeServesTheLatestSlotOnly(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(1)

	f.clk.Advance(5*time.Minute + 30*time.Second) // the scheduler overslept slots 1..4
	f.requireTick(t, "L1", 5)
	f.clk.BlockUntil(1)
	f.requireNoTick(t) // no burst for the slots that passed

	f.clk.Advance(29 * time.Second)
	f.requireNoTick(t)
	f.clk.Advance(time.Second)
	f.requireTick(t, "L1", 6)
}

func TestLoopScheduler_SkipsABusyTargetWithoutJournalWrite(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-busy", string(f.helper), time.Minute, "to the helper, which is mid-turn"))
	f.create(t, loopReq("op-idle", "my-qa", time.Minute, "to the idle agent"))
	f.start(t)
	f.clk.BlockUntil(1)
	seq := f.store.Snapshot().LastSeq

	f.clk.Advance(time.Minute)
	f.requireTick(t, "L2", 1) // both are due in one pass; only the idle agent is served
	f.clk.BlockUntil(1)
	f.requireNoTick(t)
	if got := f.store.Snapshot().LastSeq; got != seq {
		t.Fatalf("journal seq %d -> %d: a skipped slot must write nothing", seq, got)
	}
}

func TestLoopScheduler_AHelperParkedOnAMeshWaitStillGetsTheTick(t *testing.T) {
	f := newSchedFixture(t)
	parked := deriveTaskID("op-parked")
	runningHelper := f.o.runHelperAgent
	f.o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if taskID != string(parked) {
			return runningHelper(ctx, taskID, profileID, cmd, prompt, logFile, resultFile)
		}
		deadline := f.clk.Now().Add(24 * time.Hour)
		if _, err := f.o.Wait(context.Background(), WaitRequest{
			OperationID: "op-parked-wait", Waiter: meshHelperParticipantID(parked), Deadline: &deadline,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody-writes-here"},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	}
	attachTestTask(context.Background(), t, f.o, "op-parked")
	waitForTaskStatus(t, f.store, parked, assistants.StatusWaitingPeer, 2*time.Second)

	f.create(t, loopReq("op-1", string(meshHelperParticipantID(parked)), time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(2) // the loop's slot and the wait's own 24h deadline
	f.clk.Advance(time.Minute)
	f.requireTick(t, "L1", 1) // a pending mesh wait is not "busy": the tick is a message that resumes it
}

func TestLoopScheduler_CreateWakesTheScheduler(t *testing.T) {
	f := newSchedFixture(t)
	f.start(t) // no loop yet: nothing to wait for
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.clk.BlockUntil(1) // the new loop's first slot is armed

	f.clk.Advance(time.Minute)
	f.requireTick(t, "L1", 1)
}

func TestLoopScheduler_StopEndsTicks(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "stopped"))
	f.create(t, loopReq("op-2", "my-qa", time.Minute, "kept"))
	// A loop stopped before scheduler startup must never arm a delivery timer.
	if _, err := f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: "op-stop", LoopID: "L1", Actor: loopUser}); err != nil {
		t.Fatalf("StopLoop: %v", err)
	}
	f.start(t)
	f.clk.BlockUntil(1)

	f.clk.Advance(time.Minute)
	f.requireTick(t, "L2", 1) // the same pass evaluated L1 and found it stopped
	f.advanceAndSettle(0)
	f.requireNoTick(t)
}

func TestLoopScheduler_StageEndEndsABoundLoop(t *testing.T) {
	f := newSchedFixture(t)
	bound := loopReq("op-bound", "my-qa", time.Minute, "until docs ends")
	bound.UntilStageID = loopOtherStageID
	f.create(t, bound)
	f.create(t, loopReq("op-free", "my-qa", time.Minute, "flow-wide"))
	f.start(t)
	f.clk.BlockUntil(1)
	finishStage(t, f.o, loopOtherStageID)

	f.clk.Advance(time.Minute)
	f.requireTick(t, "L2", 1)
	f.advanceAndSettle(0)
	f.requireNoTick(t)
}

// A loop that is over for good has no timer: the scheduler sleeps until a create gives it something to wait for.
func TestLoopScheduler_ALoopOverForGoodLeavesNoTimer(t *testing.T) {
	f := newSchedFixture(t)
	bound := loopReq("op-bound", "my-qa", time.Minute, "until docs ends")
	bound.UntilStageID = loopOtherStageID
	f.create(t, bound)
	f.start(t)
	f.clk.BlockUntil(1)
	finishStage(t, f.o, loopOtherStageID)

	f.clk.Advance(time.Minute) // the armed slot passes; the scheduler finds the loop over and arms nothing
	armed := make(chan struct{})
	go func() {
		f.clk.BlockUntil(1)
		close(armed)
	}()
	select {
	case <-armed:
		t.Fatal("a timer is armed for a loop that can never tick again")
	case <-time.After(100 * time.Millisecond):
	}
	f.requireNoTick(t)

	f.create(t, loopReq("op-new", "my-qa", time.Minute, "a live loop"))
	select {
	case <-armed:
	case <-time.After(2 * time.Second):
		t.Fatal("creating a loop did not wake the scheduler")
	}
}

// Every stage terminal: a finished flow and a failed run left open in the dashboard alike get no tick, and the
// scheduler stays idle without a log or a journal write per slot.
func TestLoopScheduler_NoTickOnceEveryStageIsTerminal(t *testing.T) {
	for name, end := range map[string]func(*testing.T, *Orchestrator, string){
		"done":   finishStage,
		"failed": func(t *testing.T, o *Orchestrator, id string) { t.Helper(); failStage(t, o, id) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSchedFixture(t)
			f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
			f.start(t)
			f.clk.BlockUntil(1)
			end(t, f.o, assistantsTestStageID)
			end(t, f.o, loopOtherStageID)
			seq := f.store.Snapshot().LastSeq

			for range 3 {
				f.advanceAndSettle(time.Minute) // BlockUntil: the scheduler evaluated the slot and went back to sleep
				f.requireNoTick(t)
			}
			if got := f.store.Snapshot().LastSeq; got != seq {
				t.Fatalf("journal seq %d -> %d with every stage terminal", seq, got)
			}
		})
	}
}

// A flow that was over can get a stage again (a failed stage retried): the scheduler never decided "this loop is
// finished" for good, so ticks resume from the next future slot.
func TestLoopScheduler_ResumesAfterAFailedStageIsRetried(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(1)
	failStage(t, f.o, assistantsTestStageID)
	failStage(t, f.o, loopOtherStageID)
	f.advanceAndSettle(time.Minute)
	f.requireNoTick(t)

	if _, ok := f.o.Trigger(loopOtherStageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
		t.Fatal("retry of the failed stage was refused")
	}
	f.advanceAndSettle(time.Minute)
	f.requireTick(t, "L1", 2)
}

func TestLoopScheduler_ReviewPauseSuspendsTicks(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(1)

	f.o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStatePaused})
	f.advanceAndSettle(time.Minute)
	f.requireNoTick(t) // slot 1 passed during the pause: skipped, not queued

	f.o.reviewMarker.Store(nil)
	f.advanceAndSettle(30 * time.Second)
	f.requireNoTick(t) // and no catch-up when the pause ends
	f.clk.Advance(30 * time.Second)
	f.requireTick(t, "L1", 2)
}

func TestLoopScheduler_NoOverlapOfAnUnfinishedTick(t *testing.T) {
	f := newSchedFixture(t)
	var inFlight, maxInFlight atomic.Int32
	started, release := make(chan uint64, 8), make(chan struct{})
	f.o.loopTick = func(_ context.Context, _ string, slot uint64) error {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		maxInFlight.Store(max(maxInFlight.Load(), n))
		started <- slot
		awaitRelease(t, release)
		return nil
	}
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "slow"))
	f.start(t)
	f.clk.BlockUntil(1)

	f.clk.Advance(time.Minute)
	if got := <-started; got != 1 {
		t.Fatalf("first tick slot = %d", got)
	}
	f.clk.Advance(3 * time.Minute) // slots 2..4 come due while tick 1 is still being delivered
	close(release)
	if got := <-started; got != 4 {
		t.Fatalf("tick after the slow one = slot %d, want only the latest, 4", got)
	}
	f.clk.BlockUntil(1)
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("%d ticks of one loop in flight at once", got)
	}
	select {
	case got := <-started:
		t.Fatalf("extra tick for slot %d", got)
	default:
	}
}

// stop returns only after an in-flight delivery has: Run stops the scheduler before the agents and the store, so
// a tick never writes to a closed journal.
func TestLoopScheduler_StopWaitsForAnInFlightTick(t *testing.T) {
	f := newSchedFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	f.o.loopTick = func(context.Context, string, uint64) error {
		close(started)
		awaitRelease(t, release)
		return nil
	}
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "slow"))
	stop := f.start(t)
	f.clk.BlockUntil(1)
	f.clk.Advance(time.Minute)
	<-started

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned while a tick was still being delivered")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return after the tick finished")
	}
}

// After a restart the scheduler has no memory of what was due while the process was down: it starts at the first
// slot still in the future.
func TestLoopScheduler_RestartSkipsDowntimeAndWaitsForTheNextFutureSlot(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	stop := f.start(t)
	f.clk.BlockUntil(1)
	f.clk.Advance(time.Minute)
	f.requireTick(t, "L1", 1)
	stop()

	f.clk.Advance(9*time.Minute + 30*time.Second) // "down" from 1m to 10m30s
	f.start(t)
	f.clk.BlockUntil(1)
	f.requireNoTick(t) // nothing is due at once: no catch-up for slots 2..10
	f.clk.Advance(29 * time.Second)
	f.requireNoTick(t)
	f.clk.Advance(time.Second)
	f.requireTick(t, "L1", 11)
}

func TestLoopScheduler_TimersEndWithTheRunNotTheRequest(t *testing.T) {
	f := newSchedFixture(t)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if _, err := f.o.CreateLoop(requestCtx, loopReq("op-1", "my-qa", time.Minute, "ping")); err != nil {
		t.Fatalf("CreateLoop: %v", err)
	}
	cancelRequest() // the HTTP handler returned

	runCtx, cancelRun := context.WithCancel(t.Context())
	stop := f.o.startLoopScheduler(runCtx)
	f.clk.BlockUntil(1)
	f.clk.Advance(time.Minute)
	f.requireTick(t, "L1", 1)
	f.clk.BlockUntil(1)

	cancelRun() // the run ends: the timer is released and the goroutine is gone
	stop()
	f.clk.Advance(time.Hour)
	f.requireNoTick(t)
}

// Run starts the scheduler once the team and recovery are ready and stops it, waiting for it, before the run
// ends: a loop ticks while the run is going, never keeps a finished flow open, and nothing ticks afterwards.
func TestLoopScheduler_RunOwnsTheScheduler(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	state.SetClockForTest(fc)
	t.Cleanup(func() { state.SetClockForTest(nil) })
	runner := &scriptedStageRunner{}
	o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	o.clock = fc
	ticks := make(chan uint64, 64)
	o.loopTick = func(_ context.Context, _ string, slot uint64) error {
		ticks <- slot
		return nil
	}
	ticked, finishTurn := make(chan struct{}), make(chan struct{})
	runner.turn = func(int32) error {
		if _, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: "add-qa", ProfileID: "qa", Title: "QA", Instruction: "test the app", Actor: loopUser,
		}); err != nil {
			t.Errorf("AddRunAgent: %v", err)
			return err
		}
		if _, err := o.CreateLoop(context.Background(), loopReq("op-run-loop", "qa", time.Minute, "ping")); err != nil {
			t.Errorf("CreateLoop: %v", err)
			return err
		}
		// The scheduler may not have armed its timer yet: keep the clock moving until a slot is served.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			fc.Advance(time.Minute)
			if len(ticks) > 0 {
				close(ticked)
				break
			}
		}
		<-finishTurn
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(ctx) }()
	select {
	case <-ticked:
	case <-time.After(10 * time.Second):
		t.Fatal("the run's scheduler never ticked the loop")
	}
	close(finishTurn)
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v — the dormant loop must not hold a finished flow open", err)
	}

	for len(ticks) > 0 { // what was served while the run was going
		<-ticks
	}
	fc.Advance(time.Hour)
	time.Sleep(50 * time.Millisecond)
	if len(ticks) != 0 {
		t.Fatal("a loop ticked after the run ended")
	}
}

func TestLoopScheduler_StorageFatalDeliveryEndsTheRun(t *testing.T) {
	f := newSchedFixture(t)
	runCtx, cancelRun := context.WithCancel(t.Context())
	f.o.cancelRun = cancelRun
	f.o.loopTick = func(context.Context, string, uint64) error {
		return &state.StorageError{Inner: errors.New("write events.jsonl: disk full")}
	}
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	stop := f.o.startLoopScheduler(runCtx)
	t.Cleanup(stop)
	f.clk.BlockUntil(1)

	f.clk.Advance(time.Minute)
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a storage-fatal tick did not cancel the run")
	}
	var se *state.StorageError
	if err := f.o.loadFatal(); !errors.As(err, &se) {
		t.Fatalf("fatal error = %v, want the storage error", err)
	}
}

// A tick that lost a race (the commit-time check refused it) is nothing to end the run for: the next slot is
// tried as usual.
func TestLoopScheduler_ARefusedTickIsNotFatal(t *testing.T) {
	f := newSchedFixture(t)
	var attempts atomic.Int32
	f.o.loopTick = func(_ context.Context, key string, slot uint64) error {
		if attempts.Add(1) == 1 {
			return errors.New("the target became busy")
		}
		return f.record(context.Background(), key, slot)
	}
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	f.start(t)
	f.clk.BlockUntil(1)

	f.advanceAndSettle(time.Minute)
	f.requireNoTick(t)
	f.clk.Advance(time.Minute)
	f.requireTick(t, "L1", 2)
	if err := f.o.loadFatal(); err != nil {
		t.Fatalf("fatal error = %v, want none", err)
	}
}

// Loops never keep a finished flow open: with every stage terminal the run may exit whether or not a loop
// exists, and a loop created while the flow was still going does not change that decision.
func TestLoopScheduler_DormantLoopsDoNotHoldTheFlowOpen(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
	finishStage(t, f.o, assistantsTestStageID)
	finishStage(t, f.o, loopOtherStageID)

	rs := f.store.Snapshot()
	withLoop := f.o.runStateAllowsExitCore(&rs)
	rs.Assistants.Loops = nil
	rs.Assistants.LoopOrder = nil
	if withoutLoop := f.o.runStateAllowsExitCore(&rs); withLoop != withoutLoop {
		t.Fatalf("exit allowed = %v with a loop, %v without: a loop must not influence it", withLoop, withoutLoop)
	}
}

// Create and run finalization are ordered by the store lock: a loop either is in the journal before the run
// finalizes, or its create is refused. Nothing is created in a finalized run.
func TestLoopScheduler_CreateRacesRunFinalization(t *testing.T) {
	f := newSchedFixtureWith(t, false) // a running helper would block finalization
	finishStage(t, f.o, assistantsTestStageID)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, _ = f.o.CreateLoop(context.Background(), loopReq("op-race-"+string(rune('a'+i)), "my-qa", time.Minute, "ping"))
		})
	}
	finishStage(t, f.o, loopOtherStageID)
	finalized := f.store.FinalizeIf(func(*state.RunState) bool { return true })
	loopsAtFinalize := len(f.loops())
	wg.Wait()
	if !finalized {
		t.Fatal("FinalizeIf refused")
	}
	if got := len(f.loops()); got != loopsAtFinalize {
		t.Fatalf("%d loops at finalization, %d after: a loop was created in a finalized run", loopsAtFinalize, got)
	}
	if _, err := f.o.CreateLoop(context.Background(), loopReq("op-after", "my-qa", time.Minute, "late")); err == nil {
		t.Fatal("CreateLoop succeeded after the run was finalized")
	}
}

func TestLoopScheduler_TickOperationIDsAreReserved(t *testing.T) {
	f := newSchedFixture(t)
	id := loopTickOperationID("loop-x", 3)
	_, err := f.o.CreateLoop(context.Background(), loopReq(id, "my-qa", time.Minute, "squatting on a tick id"))
	requireRejected(t, err, assistants.CodeInvalidRequest)
	_, err = f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: id, LoopID: "L1", Actor: loopUser})
	requireRejected(t, err, assistants.CodeInvalidRequest)
}

// Skipping busy, held and unread targets must not advance the durable delivered-slot cursor.
func TestLoopScheduler_TargetReadiness(t *testing.T) {
	const helperTask = assistants.TaskID("t-helper")
	helper := meshHelperParticipantID(helperTask)
	loop := state.LoopState{Key: "loop-1", TargetID: helper, ThreadID: "mt-loop"}
	build := func(status assistants.TaskStatus, mutate func(*state.RunState)) state.RunState {
		rs := state.RunState{
			Stages: map[string]state.StageState{assistantsTestStageID: {Status: state.StatusRunning}},
			Assistants: state.AssistantsState{
				Tasks: map[assistants.TaskID]*state.TaskState{
					helperTask: {Task: assistants.Task{ID: helperTask, RootStageID: assistantsTestStageID, Status: status}},
				},
				Mesh: state.MeshState{
					Messages:         map[agentcomm.MessageID]*agentcomm.Message{},
					MessagesByThread: map[agentcomm.ThreadID][]agentcomm.MessageID{},
					Receipts:         map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{},
				},
			},
		}
		if mutate != nil {
			mutate(&rs)
		}
		return rs
	}
	unreadTick := func(rs *state.RunState) {
		m := agentcomm.MessageID("m-1")
		rs.Assistants.Mesh.Messages[m] = &agentcomm.Message{ID: m}
		rs.Assistants.Mesh.MessagesByThread[loop.ThreadID] = []agentcomm.MessageID{m}
		rs.Assistants.Mesh.Receipts[m] = map[agentcomm.ParticipantID]agentcomm.DeliveryStatus{helper: agentcomm.DeliveryAccepted}
	}
	consumedTick := func(rs *state.RunState) {
		unreadTick(rs)
		rs.Assistants.Mesh.Receipts["m-1"][helper] = agentcomm.DeliveryDelivered
	}
	held := func(rs *state.RunState) {
		rs.Assistants.Tasks[helperTask].Holds = map[string]state.Hold{holdOwnerManual: {}}
	}

	for _, c := range []struct {
		name   string
		status assistants.TaskStatus
		mutate func(*state.RunState)
		want   bool
	}{
		{"running turn", assistants.StatusRunning, nil, false},
		{"about to start", assistants.StatusQueued, nil, false},
		{"paused", assistants.StatusPaused, nil, false},
		{"held", assistants.StatusCompleted, held, false},
		{"waiting for children", assistants.StatusWaitingChildren, nil, false},
		{"previous tick unread", assistants.StatusWaitingPeer, unreadTick, false},
		{"previous tick consumed", assistants.StatusWaitingPeer, consumedTick, true},
		{"parked on a mesh wait", assistants.StatusWaitingPeer, nil, true},
		{"finished one-shot helper", assistants.StatusCompleted, nil, true},
		{"resting watcher", assistants.StatusWatching, nil, true},
		{"failed", assistants.StatusFailed, nil, false},
		{"cancelled", assistants.StatusCancelled, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := loopTargetReady(build(c.status, c.mutate), loop); got != c.want {
				t.Fatalf("loopTargetReady = %v, want %v", got, c.want)
			}
		})
	}
}
