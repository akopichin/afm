package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// Ticks reuse the helper task and saved role without replacing the stage main agent.

// helperRig is the delivery fixture with scripted stage-helper turns.
type helperRig struct {
	deliveryFixture
	mu      sync.Mutex
	turns   map[string]int      // task id → turns started
	prompts map[string][]string // task id → the prompt of each turn
}

// helperScript is the outcome of turn (1-based) of the task taskID.
type helperScript func(taskID string, turn int) (executor.HelperOutcome, error)

func newHelperRig(t *testing.T, script helperScript) *helperRig {
	t.Helper()
	r := &helperRig{deliveryFixture: newDeliveryFixture(t), turns: map[string]int{}, prompts: map[string][]string{}}
	r.o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		r.mu.Lock()
		r.turns[taskID]++
		turn := r.turns[taskID]
		r.prompts[taskID] = append(r.prompts[taskID], prompt)
		r.mu.Unlock()
		return script(taskID, turn)
	}
	return r
}

// passes is the script of a helper whose every turn ends with a passing result.
func passes(string, int) (executor.HelperOutcome, error) { return passResultOutcome(), nil }

// holdTurns makes each of the given turns of every task wait until release(turn) is called (or the test ends);
// the other turns pass at once.
func holdTurns(t *testing.T, turns ...int) (script helperScript, release func(turn int)) {
	gates := map[int]chan struct{}{}
	for _, n := range turns {
		gates[n] = make(chan struct{})
	}
	var once sync.Map
	release = func(turn int) {
		if _, done := once.LoadOrStore(turn, true); !done {
			close(gates[turn])
		}
	}
	t.Cleanup(func() {
		for n := range gates {
			release(n)
		}
	})
	script = func(_ string, n int) (executor.HelperOutcome, error) {
		if gate, held := gates[n]; held {
			awaitRelease(t, gate)
		}
		return passResultOutcome(), nil
	}
	return script, release
}

func (r *helperRig) turnsOf(id assistants.TaskID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[string(id)]
}

func (r *helperRig) promptsOf(id assistants.TaskID) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts[string(id)]...)
}

func (r *helperRig) task(id assistants.TaskID) *state.TaskState {
	return r.store.Snapshot().Assistants.Tasks[id]
}

// attach starts a helper on the run's stage and waits until its first turn is under way.
func (r *helperRig) attach(t *testing.T, opID string, mode assistants.Mode) assistants.TaskID {
	t.Helper()
	id, err := r.o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "look at this",
		Mode: mode, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitUntil(t, "the first turn of the helper", func() bool { return r.turnsOf(id) == 1 })
	return id
}

// waitRested waits until the task has run n turns and rests in status.
func (r *helperRig) waitRested(t *testing.T, id assistants.TaskID, n uint64, status assistants.TaskStatus) *state.TaskState {
	t.Helper()
	waitUntil(t, "the helper rested after its turn", func() bool {
		tk := r.task(id)
		return tk.Turn == n && tk.Status == status
	})
	return r.task(id)
}

// waitWatcherIdle waits until the watcher's collector is not busy with a turn or a fire.
func (r *helperRig) waitWatcherIdle(t *testing.T, id assistants.TaskID) {
	t.Helper()
	waitUntil(t, "the watcher's collector was released", func() bool { return !r.o.watcherFireInFlight(id) })
}

func (r *helperRig) loopFor(t *testing.T, opID string, id assistants.TaskID) {
	t.Helper()
	r.create(t, loopReq(opID, string(meshHelperParticipantID(id)), time.Minute, "status?"))
}

// A finished helper is not left behind by its loop: each delivered tick puts the same task back in the queue and
// runs another turn, with the tick as its input, and its earlier results stay in the journal.
func TestLoopHelper_FinishedHelperRunsAgainOnEachEligibleSlot(t *testing.T) {
	script, release := holdTurns(t, 1, 2)
	r := newHelperRig(t, script)
	id := r.attach(t, "op-helper", assistants.ModeAdvisory)
	r.loopFor(t, "op-loop", id)
	release(1)
	first := r.waitRested(t, id, 1, assistants.StatusCompleted)

	r.mustTick(t, "L1", 1)

	waitUntil(t, "turn 2 under way", func() bool {
		tk := r.task(id)
		return tk.Turn == 2 && tk.Status == assistants.StatusRunning && r.turnsOf(id) == 2
	})
	running := r.task(id)
	if running.Turn != 2 || !running.FinishedAt.IsZero() {
		t.Fatalf("task = turn %d finished at %v, want turn 2 of the same task with no stale finish time", running.Turn, running.FinishedAt)
	}
	if running.ProfileID != first.ProfileID || running.ProfileSnapshot.Command != first.ProfileSnapshot.Command {
		t.Fatalf("profile = %q/%q, want the helper's saved profile %q/%q", running.ProfileID, running.ProfileSnapshot.Command, first.ProfileID, first.ProfileSnapshot.Command)
	}
	if got := r.promptsOf(id); len(got) != 2 || !strings.Contains(got[1], "⟳ Loop L1") || !strings.Contains(got[1], "status?") {
		t.Fatalf("prompts = %q, want turn 2 to carry the loop's tick", got)
	}
	release(2)
	second := r.waitRested(t, id, 2, assistants.StatusCompleted)
	if second.ResultSeq <= first.ResultSeq || second.FinishedAt.IsZero() {
		t.Fatalf("result seq %d→%d finished %v, want a new result and a finish time after turn 2", first.ResultSeq, second.ResultSeq, second.FinishedAt)
	}

	r.mustTick(t, "L1", 2)

	r.waitRested(t, id, 3, assistants.StatusCompleted)
	if got := len(r.loopMessages(t, "L1")); got != 2 {
		t.Fatalf("loop thread holds %d messages, want one per slot", got)
	}
}

// A watcher resting between checkpoints is restarted by a tick, and rests again afterwards.
func TestLoopHelper_RestingWatcherRunsAgainOnEachEligibleSlot(t *testing.T) {
	r := newHelperRig(t, passes)
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.mustTick(t, "L1", 1)
	r.waitRested(t, id, 1, assistants.StatusWatching)
	// The collector is released just after the task is committed as watching, and a tick in that gap is skipped
	// (loopMayTick) — and, being a no-op, would not fail mustTick.
	r.waitWatcherIdle(t, id)
	r.mustTick(t, "L1", 2)
	r.waitRested(t, id, 2, assistants.StatusWatching)

	if got := r.promptsOf(id); len(got) != 2 || !strings.Contains(got[1], "⟳ Loop L1") {
		t.Fatalf("prompts = %q, want both turns to carry a tick", got)
	}
}

// A watcher whose final pass is over is completed, not watching: a tick runs one more turn and it ends completed
// again, rather than resting as a watcher nothing will ever fire (which would hold a required stage open).
func TestLoopHelper_CompletedWatcherEndsCompletedAfterATickTurn(t *testing.T) {
	r := newHelperRig(t, passes)
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeRequired)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)
	r.o.taskFinalPass.Store(id, true)
	r.o.fireWatcherTask(context.Background(), id)
	r.waitRested(t, id, 1, assistants.StatusCompleted)

	r.mustTick(t, "L1", 1)

	r.waitRested(t, id, 2, assistants.StatusCompleted)
	if _, blocked := r.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); blocked {
		t.Fatal("the stage is held open by a watcher that is done")
	}
}

// A helper parked on a mesh wait is resumed by the tick (the existing path), and the next slot reaches it again.
func TestLoopHelper_HelperParkedOnAMeshWaitRunsAgainOnEachEligibleSlot(t *testing.T) {
	var r *helperRig
	r = newHelperRig(t, func(taskID string, turn int) (executor.HelperOutcome, error) {
		if turn > 1 {
			return passResultOutcome(), nil
		}
		deadline := r.clk.Now().Add(24 * time.Hour)
		_, err := r.o.Wait(context.Background(), WaitRequest{
			OperationID: "op-parked-wait", Waiter: meshHelperParticipantID(assistants.TaskID(taskID)), Deadline: &deadline,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody-writes-here"},
		})
		if err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}, nil
	})
	id := r.attach(t, "op-helper", assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWaitingPeer, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.mustTick(t, "L1", 1)
	r.waitRested(t, id, 2, assistants.StatusWaitingPeer) // its wait is still open: the turn parks again
	r.mustTick(t, "L1", 2)

	waitUntil(t, "the second tick's turn", func() bool { return r.turnsOf(id) == 3 })
	if got := r.promptsOf(id); !strings.Contains(got[1], "⟳ Loop L1") || !strings.Contains(got[2], "⟳ Loop L1") {
		t.Fatalf("prompts = %q, want turns 2 and 3 to carry a tick", got)
	}
}

// A helper that is not at rest gets no extra turn and no message: one still working, one that is held, one that
// failed and one that was cancelled. The slot is lost without a trace.
func TestLoopHelper_OnlyAHelperAtRestGetsATurn(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, r *helperRig, id assistants.TaskID, release func(int))
		want  assistants.TaskStatus
	}{
		{"still working", func(*testing.T, *helperRig, assistants.TaskID, func(int)) {}, assistants.StatusRunning},
		{"held", func(t *testing.T, r *helperRig, id assistants.TaskID, release func(int)) {
			release(1)
			waitForTaskStatus(t, r.store, id, assistants.StatusCompleted, 2*time.Second)
			if err := r.o.PauseTask(context.Background(), id); err != nil {
				t.Fatalf("PauseTask: %v", err)
			}
		}, assistants.StatusCompleted},
		{"cancelled", func(t *testing.T, r *helperRig, id assistants.TaskID, release func(int)) {
			if err := r.o.CancelTask(context.Background(), id); err != nil {
				t.Fatalf("CancelTask: %v", err)
			}
			release(1)
		}, assistants.StatusCancelled},
	} {
		t.Run(c.name, func(t *testing.T) {
			script, release := holdTurns(t, 1)
			r := newHelperRig(t, script)
			id := r.attach(t, "op-helper", assistants.ModeAdvisory)
			r.loopFor(t, "op-loop", id)
			c.setup(t, r, id, release)
			before, seq := r.task(id), r.store.Snapshot().LastSeq

			if err := r.tick(t, "L1", 1); err != nil {
				t.Fatalf("commitLoopTick = %v, want nil", err)
			}

			after := r.task(id)
			if after.Status != c.want || after.Turn != before.Turn || r.turnsOf(id) != 1 {
				t.Fatalf("task = %s turn %d (%d runs), want %s turn %d and no extra run", after.Status, after.Turn, r.turnsOf(id), c.want, before.Turn)
			}
			if r.lastSlot(t, "L1") != 0 || len(r.loopMessages(t, "L1")) != 0 || r.store.Snapshot().LastSeq != seq {
				t.Fatal("the slot left a trace in the journal")
			}
		})
	}
}

// A helper whose turn failed is not messaged either, and is not restarted by the loop: Retry is the human's call.
func TestLoopHelper_FailedHelperIsNotRestarted(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(string, int) (executor.HelperOutcome, error) {
		awaitRelease(t, gate)
		return executor.HelperOutcome{}, errors.New("boom")
	})
	id := r.attach(t, "op-helper", assistants.ModeAdvisory)
	r.loopFor(t, "op-loop", id)
	close(gate)
	waitForTaskStatus(t, r.store, id, assistants.StatusFailed, 2*time.Second)
	seq := r.store.Snapshot().LastSeq

	if err := r.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}

	if r.task(id).Status != assistants.StatusFailed || r.turnsOf(id) != 1 || r.store.Snapshot().LastSeq != seq {
		t.Fatalf("task = %s after %d runs: a failed helper was restarted", r.task(id).Status, r.turnsOf(id))
	}
}

// A required helper's completed result closes its gate as before, with a loop pointed at it: only a delivered
// tick re-opens the gate, for exactly as long as the extra turn runs.
func TestLoopHelper_RequiredHelperGateFollowsItsTurns(t *testing.T) {
	script, release := holdTurns(t, 1, 2)
	r := newHelperRig(t, script)
	id := r.attach(t, "op-helper", assistants.ModeRequired)
	r.loopFor(t, "op-loop", id)
	release(1)
	r.waitRested(t, id, 1, assistants.StatusCompleted)
	blocked := func() bool {
		_, ok := r.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID)
		return ok
	}
	if blocked() {
		t.Fatal("a required helper's passing result does not release the stage while a loop exists")
	}

	r.mustTick(t, "L1", 1)
	waitUntil(t, "the extra turn", func() bool { return r.task(id).Status == assistants.StatusRunning })
	if !blocked() {
		t.Fatal("the stage is not held while the required helper runs its extra turn")
	}
	release(2)
	r.waitRested(t, id, 2, assistants.StatusCompleted)

	if blocked() {
		t.Fatal("the stage stays held after the extra turn passed")
	}
}

// A loop cannot bypass a required helper decision that still needs Revise, Retry or waiver.
func TestLoopHelper_RequiredHelperWithOpenGateIsNotRestarted(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(string, int) (executor.HelperOutcome, error) {
		awaitRelease(t, gate)
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	})
	id := r.attach(t, "op-helper", assistants.ModeRequired)
	r.loopFor(t, "op-loop", id) // the target is still reachable (mid-turn): the loop can be made
	close(gate)
	r.waitRested(t, id, 1, assistants.StatusCompleted)
	if _, blocked := r.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); !blocked {
		t.Fatal("a needs_changes verdict must leave the stage's required gate open")
	}
	seq := r.store.Snapshot().LastSeq

	if err := r.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}

	if r.task(id).Status != assistants.StatusCompleted || r.turnsOf(id) != 1 || r.store.Snapshot().LastSeq != seq {
		t.Fatalf("task = %s after %d runs: a human-decision-pending required helper was restarted", r.task(id).Status, r.turnsOf(id))
	}
}

// Each required helper gate is checked independently, even if another helper blocks first.
func TestLoopHelper_SecondRequiredHelperWithOpenGateIsAlsoNotRestarted(t *testing.T) {
	secondID := deriveTaskID("op-helper-b")
	gate := make(chan struct{})
	r := newHelperRig(t, func(taskID string, _ int) (executor.HelperOutcome, error) {
		if taskID == string(secondID) {
			awaitRelease(t, gate)
		}
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"fix it"}`}, nil
	})
	first := r.attach(t, "op-helper-a", assistants.ModeRequired)
	r.waitRested(t, first, 1, assistants.StatusCompleted)

	second := r.attach(t, "op-helper-b", assistants.ModeRequired) // its first turn is gated above
	r.loopFor(t, "op-loop", second)                               // still reachable (mid-turn): the loop can be made
	close(gate)
	r.waitRested(t, second, 1, assistants.StatusCompleted)

	blocker, blocked := r.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID)
	if !blocked || blocker.ID != first {
		t.Fatalf("RequiredBlocker = %+v (blocked=%v), want the FIRST helper %s reported (the scenario this fix guards against)", blocker, blocked, first)
	}
	seq := r.store.Snapshot().LastSeq

	if err := r.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}

	if r.task(second).Status != assistants.StatusCompleted || r.turnsOf(second) != 1 || r.store.Snapshot().LastSeq != seq {
		t.Fatalf("second helper = %s after %d runs: restarted even though its OWN gate is open too, just masked by the first required helper", r.task(second).Status, r.turnsOf(second))
	}
}

// Once the helper's stage has ended no tick is delivered, and nothing is restarted.
func TestLoopHelper_StageEndPreventsAnyLaterTick(t *testing.T) {
	script, release := holdTurns(t, 1)
	r := newHelperRig(t, script)
	id := r.attach(t, "op-helper", assistants.ModeAdvisory)
	r.loopFor(t, "op-loop", id)
	release(1)
	r.waitRested(t, id, 1, assistants.StatusCompleted)
	finishStage(t, r.o, assistantsTestStageID)
	seq := r.store.Snapshot().LastSeq

	if err := r.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}

	if r.task(id).Status != assistants.StatusCompleted || r.turnsOf(id) != 1 || r.store.Snapshot().LastSeq != seq {
		t.Fatalf("task = %s after %d runs: a tick was delivered after the stage ended", r.task(id).Status, r.turnsOf(id))
	}
}

// A helper's loop for itself wakes its creator once per eligible slot — the creator is the target, and one tick
// is one message to it and one turn.
func TestLoopHelper_SelfLoopWakesItsCreatorOncePerSlot(t *testing.T) {
	script, release := holdTurns(t, 1)
	r := newHelperRig(t, script)
	id := r.attach(t, "op-helper", assistants.ModeAdvisory)
	helper := meshHelperParticipantID(id)
	req := loopReq("op-self-loop", "", time.Minute, "keep going")
	req.Actor, req.OriginStage = state.Actor{Kind: "agent", ID: string(helper)}, assistantsTestStageID
	r.create(t, req)
	release(1)
	r.waitRested(t, id, 1, assistants.StatusCompleted)

	r.mustTick(t, "L1", 1)
	r.waitRested(t, id, 2, assistants.StatusCompleted)
	r.mustTick(t, "L1", 2)
	r.waitRested(t, id, 3, assistants.StatusCompleted)

	msgs := r.loopMessages(t, "L1")
	if len(msgs) != 2 || r.turnsOf(id) != 3 {
		t.Fatalf("%d messages and %d turns, want one message and one turn per slot", len(msgs), r.turnsOf(id))
	}
	for _, m := range msgs {
		if m.Sender != helper || len(m.Recipients) != 1 || m.Recipients[0] != helper {
			t.Fatalf("message %+v, want the helper's tick to itself", m)
		}
	}
}

// A final watcher pass winning the CAS must invalidate a tick planned from its resting state.
func TestLoopHelper_TickPlannedForAWatcherLosesTheRaceToItsFinalPass(t *testing.T) {
	r := newHelperRig(t, passes)
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeRequired)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)
	planned := r.store.Snapshot()
	r.o.taskFinalPass.Store(id, true)
	r.o.fireWatcherTask(context.Background(), id)
	r.waitRested(t, id, 1, assistants.StatusCompleted)
	seq := r.store.Snapshot().LastSeq

	if err := r.o.commitLoopTickFrom(context.Background(), planned, r.loopKey(t, "L1"), 1); err != nil {
		t.Fatalf("commitLoopTickFrom = %v, want the lost race to be quiet", err)
	}

	if r.store.Snapshot().LastSeq != seq || r.lastSlot(t, "L1") != 0 {
		t.Fatal("a tick planned for a resting watcher was committed to a completed one")
	}
}

// A collector already claiming a checkpoint owns its turn, so a loop must skip that slot.
func TestLoopHelper_TickSkipsARestingWatcherWhoseCheckpointFireIsAlreadyInFlight(t *testing.T) {
	r := newHelperRig(t, passes)
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)
	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()
	if _, ready := b.coll.Ready(); !ready {
		t.Fatal("collector should be ready to fire before the tick is attempted")
	}
	seq := r.store.Snapshot().LastSeq

	if err := r.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want the lost race to be quiet", err)
	}

	if after := r.task(id); after.Status != assistants.StatusWatching || after.Turn != 0 || r.turnsOf(id) != 0 {
		t.Fatalf("task = %s turn %d (%d runs), want unchanged: the tick restarted a watcher mid-fire instead of yielding the slot", after.Status, after.Turn, r.turnsOf(id))
	}
	if r.store.Snapshot().LastSeq != seq || r.lastSlot(t, "L1") != 0 {
		t.Fatal("a tick skipped for an in-flight checkpoint fire still left a trace in the journal")
	}
}

// A loop restart claims the collector for the whole turn to exclude a concurrent checkpoint fire.
func TestLoopHelper_RestartClaimsTheWatcherSoAConcurrentPollCannotAlsoFire(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(_ string, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, gate) // the loop's own restarted turn: block so a concurrent poll can race it
		}
		return passResultOutcome(), nil
	})
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)

	r.mustTick(t, "L1", 1)
	waitForTaskStatus(t, r.store, id, assistants.StatusRunning, 2*time.Second)

	r.o.pollWatchersOnce(context.Background()) // must be a no-op while the restarted turn is in flight

	close(gate)
	r.waitRested(t, id, 1, assistants.StatusWatching)

	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()
	if r.turnsOf(id) != 1 {
		t.Fatalf("turnsOf = %d, want exactly 1: a concurrent poll fired a second turn on top of the loop's restart", r.turnsOf(id))
	}
	if passes := b.coll.Passes(); passes != 0 {
		t.Fatalf("Passes = %d, want 0: the poll during the restarted turn must not count a pass", passes)
	}
	// The status can become watching before deferred collector settlement; wait for both before advancing.
	waitForTrue(t, func() bool { return !b.coll.Busy() }, 2*time.Second, "collector left busy after the loop-started turn rested back to watching")

	clk.Advance(10 * time.Second) // past the collector's own cooldown, which started when turn 1 rested
	r.o.pollWatchersOnce(context.Background())
	r.waitRested(t, id, 2, assistants.StatusWatching)
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes = %d, want 1: the content was preserved for the next real poll", passes)
	}
}

// A checkpoint committed during a loop restart must get its owed turn without recommitting its cursor.
func TestLoopHelper_RestartWinningAfterTheCheckpointCommitsDoesNotStrandIt(t *testing.T) {
	r := newHelperRig(t, passes)
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)

	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()

	r.o.testAfterLoopRestartCommit = func() {
		r.o.testAfterLoopRestartCommit = nil // the restarted turn's own later completion must not re-enter this
		r.o.pollWatchersOnce(context.Background())
	}
	r.mustTick(t, "L1", 1)

	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor = (%d, %v), want (1, true): the checkpoint committed before the restart's CAS ran", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes = %d, want 1: a committed checkpoint is not rolled back by a lost CAS", passes)
	}
	if after := r.task(id); after.Status != assistants.StatusQueued && after.Status != assistants.StatusRunning {
		t.Fatalf("task status = %s, want the loop's own restart still under way", after.Status)
	}

	// The restarted turn must settle and discharge the already committed checkpoint debt.
	r.waitRested(t, id, 2, assistants.StatusWatching)
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes after the debt retry rested = %d, want still 1: the retry carries no new bundle", passes)
	}
	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor after the debt retry rested = (%d, %v), want still (1, true)", seq, ok)
	}
	waitForTrue(t, func() bool { return !b.coll.Busy() }, 2*time.Second, "collector left busy after the debt retry rested back to watching")

	// Unrelated new content must still fire normally: the raced bundle must not have left this collector
	// unable to ever commit again.
	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 2))
	clk.Advance(10 * time.Second)
	r.o.pollWatchersOnce(context.Background())
	r.waitRested(t, id, 3, assistants.StatusWatching)
	if seq, ok := b.store.Cursor(); !ok || seq != 2 {
		t.Fatalf("Cursor after the next real checkpoint = (%d, %v), want (2, true)", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 2 {
		t.Fatalf("Passes after the next real checkpoint = %d, want 2", passes)
	}
}

// A checkpoint fire losing to a running loop turn must preserve the same debt as a queued restart.
func TestLoopHelper_RestartRunningByTheTimeOfTheCASDoesNotStrandIt(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(_ string, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, gate) // the loop's own restarted turn: stay "running" long enough to prove the race
		}
		return passResultOutcome(), nil
	})
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)

	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()

	checkpointCommitted := make(chan struct{})
	runningReached := make(chan struct{})
	fireDone := make(chan struct{})

	r.o.testAfterLoopRestartCommit = func() {
		r.o.testAfterLoopRestartCommit = nil // the restarted turn's own later completion must not re-enter this
		go func() {
			defer close(fireDone)
			r.o.testAfterCheckpointCommit = func() {
				r.o.testAfterCheckpointCommit = nil
				close(checkpointCommitted)
				<-runningReached // hold the CAS check until the restart has durably reached "running"
			}
			r.o.maybeFireWatcher(context.Background(), b)
		}()
		<-checkpointCommitted // the checkpoint is durably committed before letting the restart reach "running"
	}
	r.mustTick(t, "L1", 1)
	waitForTaskStatus(t, r.store, id, assistants.StatusRunning, 2*time.Second)
	close(runningReached)
	<-fireDone

	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor = (%d, %v), want (1, true): the checkpoint committed before the restart reached running", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes = %d, want 1: a committed checkpoint is not rolled back by a CAS lost to \"running\"", passes)
	}

	close(gate)
	// The restarted turn must settle and discharge the already committed checkpoint debt.
	r.waitRested(t, id, 2, assistants.StatusWatching)
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes after the debt retry rested = %d, want still 1: the retry carries no new bundle", passes)
	}
	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor after the debt retry rested = (%d, %v), want still (1, true)", seq, ok)
	}
	waitForTrue(t, func() bool { return !b.coll.Busy() }, 2*time.Second, "collector left busy after the debt retry rested back to watching")

	// Unrelated new content must still fire normally: the raced bundle must not have left this collector
	// unable to ever commit again.
	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 2))
	clk.Advance(10 * time.Second)
	r.o.pollWatchersOnce(context.Background())
	r.waitRested(t, id, 3, assistants.StatusWatching)
	if seq, ok := b.store.Cursor(); !ok || seq != 2 {
		t.Fatalf("Cursor after the next real checkpoint = (%d, %v), want (2, true)", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 2 {
		t.Fatalf("Passes after the next real checkpoint = %d, want 2", passes)
	}
}

// A competing turn can finish before the lost CAS is handled; its checkpoint still needs a retry.
func TestLoopHelper_ConcurrentRestartFinishesBeforeTheLostCASIsHandled(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(_ string, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, gate) // the loop's own restarted turn: hold "running" until released
		}
		return passResultOutcome(), nil
	})
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)

	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()

	checkpointCommitted := make(chan struct{})
	runningReached := make(chan struct{})
	casLost := make(chan struct{})
	fireDone := make(chan struct{})

	r.o.testAfterLoopRestartCommit = func() {
		r.o.testAfterLoopRestartCommit = nil // the restarted turn's own later completion must not re-enter this
		go func() {
			defer close(fireDone)
			r.o.testAfterCheckpointCommit = func() {
				r.o.testAfterCheckpointCommit = nil
				close(checkpointCommitted)
				<-runningReached // hold the CAS attempt until the restart is durably "running"
			}
			r.o.testAfterFireCASLost = func() {
				r.o.testAfterFireCASLost = nil
				close(casLost) // the CAS has already failed against "running"
				close(gate)    // now let the restarted turn run all the way back to resting "watching"...
				for i := 0; i < 500 && r.task(id).Status != assistants.StatusWatching; i++ {
					time.Sleep(5 * time.Millisecond)
				}
				// ...BEFORE commitAndFire does anything else with the lost CAS — the whole point of this test.
			}
			r.o.maybeFireWatcher(context.Background(), b)
		}()
		<-checkpointCommitted // the checkpoint pops/commits before afterLoopRestart claims the collector busy
	}
	r.mustTick(t, "L1", 1)
	waitForTaskStatus(t, r.store, id, assistants.StatusRunning, 2*time.Second)
	close(runningReached)
	<-casLost
	<-fireDone
	// An already settled competing turn leaves the fire path responsible for retrying its debt.
	r.waitRested(t, id, 2, assistants.StatusWatching)
	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor = (%d, %v), want (1, true): no new bundle, just the retried fire for the one already committed", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes = %d, want 1: a committed checkpoint is not rolled back once the restart's own turn already rested again", passes)
	}
	waitForTrue(t, func() bool { return !b.coll.Busy() }, 2*time.Second, "collector left busy after the retried fire's own turn rested")

	// Unrelated new content must still fire normally: the raced bundle must not have left this collector unable
	// to ever commit again.
	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 2))
	clk.Advance(10 * time.Second)
	r.o.pollWatchersOnce(context.Background())
	r.waitRested(t, id, 3, assistants.StatusWatching)
	if seq, ok := b.store.Cursor(); !ok || seq != 2 {
		t.Fatalf("Cursor after the next real checkpoint = (%d, %v), want (2, true)", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 2 {
		t.Fatalf("Passes after the next real checkpoint = %d, want 2", passes)
	}
}

// Registering debt before the status read lets a concurrent settlement observe and discharge it.
func TestLoopHelper_CompetingTurnSettlesAfterLostFireStatusRead(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(_ string, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, gate)
		}
		return passResultOutcome(), nil
	})
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)
	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()

	checkpointCommitted := make(chan struct{})
	runningReached := make(chan struct{})
	statusRead := make(chan struct{})
	settled := make(chan struct{})
	fireDone := make(chan struct{})
	r.o.testAfterLoopRestartCommit = func() {
		r.o.testAfterLoopRestartCommit = nil
		go func() {
			defer close(fireDone)
			r.o.testAfterCheckpointCommit = func() {
				r.o.testAfterCheckpointCommit = nil
				close(checkpointCommitted)
				<-runningReached
			}
			r.o.testAfterLostFireStatusRead = func(status assistants.TaskStatus) {
				r.o.testAfterLostFireStatusRead = nil
				if status != assistants.StatusRunning {
					t.Errorf("status read after lost fire CAS = %s, want running", status)
				}
				close(statusRead)
				<-settled
			}
			r.o.maybeFireWatcher(context.Background(), b)
		}()
		<-checkpointCommitted
	}
	r.mustTick(t, "L1", 1)
	waitForTaskStatus(t, r.store, id, assistants.StatusRunning, 2*time.Second)
	close(runningReached)
	<-statusRead
	close(gate)
	// commitAndFire is still paused after its stale "running" read. Only settleWatcherBinding can start
	// checkpoint 1's turn here; if the debt was recorded too late, this wait times out at turn 1.
	r.waitRested(t, id, 2, assistants.StatusWatching)
	r.waitWatcherIdle(t, id)
	close(settled)
	<-fireDone
	if n := r.turnsOf(id); n != 2 {
		t.Fatalf("turnsOf = %d, want 2: the later status check must not start a duplicate turn", n)
	}
	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor = (%d, %v), want (1, true)", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes = %d, want 1", passes)
	}
}

// Losing a checkpoint CAS must not release Busy while the competing loop turn is still running.
// Otherwise another checkpoint can consume its cursor and pass without receiving a turn.
func TestLoopHelper_LostCASDuringARunningLoopTurnDoesNotReleaseItsClaim(t *testing.T) {
	gate := make(chan struct{})
	r := newHelperRig(t, func(_ string, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, gate) // the loop's own restarted turn: stay "running" for the whole test
		}
		return passResultOutcome(), nil
	})
	clk := newWatchFakeClock()
	r.o.watchClock = clk
	id := attachWatcherTask(t, r.o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, r.store, id, assistants.StatusWatching, 2*time.Second)
	r.loopFor(t, "op-loop", id)

	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 1))
	clk.Advance(10 * time.Second)

	r.o.watchMu.Lock()
	b := r.o.watchByTask[id]
	r.o.watchMu.Unlock()

	checkpointCommitted := make(chan struct{})
	runningReached := make(chan struct{})
	fireDone := make(chan struct{})

	r.o.testAfterLoopRestartCommit = func() {
		r.o.testAfterLoopRestartCommit = nil // the restarted turn's own later completion must not re-enter this
		go func() {
			defer close(fireDone)
			r.o.testAfterCheckpointCommit = func() {
				r.o.testAfterCheckpointCommit = nil
				close(checkpointCommitted)
				<-runningReached // hold the CAS attempt until the restart has durably reached "running"
			}
			r.o.maybeFireWatcher(context.Background(), b)
		}()
		<-checkpointCommitted // the checkpoint is durably committed before letting the restart reach "running"
	}
	r.mustTick(t, "L1", 1)
	waitForTaskStatus(t, r.store, id, assistants.StatusRunning, 2*time.Second)
	close(runningReached)
	<-fireDone // the first fire's CAS has now lost against the loop-started turn, which is still Running
	// startTurn commits "running" slightly before runHelperAgent is actually invoked (buildTaskContext,
	// persisting context.md, etc. run in between) — wait for the call itself, not just the status.
	waitUntil(t, "the loop-started turn's own runHelperAgent call", func() bool { return r.turnsOf(id) == 1 })

	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes after the first lost CAS = %d, want 1", passes)
	}

	// A second, unrelated observation arrives while the loop-started turn is STILL running. It must not be
	// allowed to fire: the collector is still legitimately busy with the turn the loop started.
	r.o.feedWatchCollectors(assistantsTestStageID, substantialObservation(assistantsTestStageID, 2))
	clk.Advance(10 * time.Second)
	r.o.pollWatchersOnce(context.Background())
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes while the loop-started turn is still running = %d, want still 1: a losing fire must not have released its claim", passes)
	}
	if got := r.task(id).Status; got != assistants.StatusRunning {
		t.Fatalf("task status = %s, want still running: a second fire must not have spawned a turn on top of it", got)
	}
	if n := r.turnsOf(id); n != 1 {
		t.Fatalf("turnsOf = %d, want 1: only the loop-started turn has run so far", n)
	}

	close(gate)
	// Keep Busy claimed until the restarted turn settles and retries the owed checkpoint.
	r.waitRested(t, id, 2, assistants.StatusWatching)
	if passes := b.coll.Passes(); passes != 1 {
		t.Fatalf("Passes after the debt retry rested = %d, want still 1: the retry carries no new bundle", passes)
	}
	if seq, ok := b.store.Cursor(); !ok || seq != 1 {
		t.Fatalf("Cursor after the debt retry rested = (%d, %v), want still (1, true)", seq, ok)
	}
	waitForTrue(t, func() bool { return !b.coll.Busy() }, 2*time.Second, "collector left busy after the debt retry rested")

	// Once the debt retry rests, the pending second observation — fed earlier while the loop-started turn was
	// still running, and never consumed since — fires normally as the very next pass.
	clk.Advance(10 * time.Second) // past the collector's own cooldown, which started when turn 2 rested
	r.o.pollWatchersOnce(context.Background())
	r.waitRested(t, id, 3, assistants.StatusWatching)
	if seq, ok := b.store.Cursor(); !ok || seq != 2 {
		t.Fatalf("Cursor after the pending observation finally fires = (%d, %v), want (2, true)", seq, ok)
	}
	if passes := b.coll.Passes(); passes != 2 {
		t.Fatalf("Passes after the pending observation finally fires = %d, want 2", passes)
	}
}
