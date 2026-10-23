package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// The orchestrator and journal share a fake clock so slot and commit times agree.

type deliveryFixture struct{ *schedFixture }

func newDeliveryFixture(t *testing.T) deliveryFixture {
	t.Helper()
	f := newSchedFixtureWith(t, false)
	f.o.loopTick = nil // the production delivery, not the scheduler tests' recorder
	return deliveryFixture{f}
}

// loopKey is the internal key of the loop with the short id shortID.
func (f deliveryFixture) loopKey(t *testing.T, shortID string) string {
	t.Helper()
	l, ok := f.store.Snapshot().Assistants.LoopByShortID(shortID)
	if !ok {
		t.Fatalf("no loop %s", shortID)
	}
	return l.Key
}

func (f deliveryFixture) tick(t *testing.T, shortID string, slot uint64) error {
	t.Helper()
	return f.o.commitLoopTick(context.Background(), f.loopKey(t, shortID), slot)
}

func (f deliveryFixture) mustTick(t *testing.T, shortID string, slot uint64) {
	t.Helper()
	if err := f.tick(t, shortID, slot); err != nil {
		t.Fatalf("commitLoopTick(%s, %d): %v", shortID, slot, err)
	}
}

// loopMessages is every message of the loop's own thread, in order.
func (f deliveryFixture) loopMessages(t *testing.T, shortID string) []*agentcomm.Message {
	t.Helper()
	rs := f.store.Snapshot()
	l, ok := rs.Assistants.LoopByShortID(shortID)
	if !ok {
		t.Fatalf("no loop %s", shortID)
	}
	var out []*agentcomm.Message
	for _, id := range rs.Assistants.Mesh.MessagesByThread[l.ThreadID] {
		out = append(out, rs.Assistants.Mesh.Messages[id])
	}
	return out
}

func (f deliveryFixture) lastSlot(t *testing.T, shortID string) uint64 {
	t.Helper()
	l, _ := f.store.Snapshot().Assistants.LoopByShortID(shortID)
	return l.LastSlot
}

// agentTask is the run agent's task.
func (f deliveryFixture) agentTask() *state.TaskState {
	rs := f.store.Snapshot()
	id, _ := runTaskForParticipant(rs, f.agent)
	return rs.Assistants.Tasks[id]
}

// waitForAgentTurns waits until the run agent has started n turns and is idle again.
func (f deliveryFixture) waitForAgentTurns(t *testing.T, n uint64) {
	t.Helper()
	waitUntil(t, "the run agent finished its turn", func() bool {
		tk := f.agentTask()
		return tk.Turn >= n && tk.Status == assistants.StatusQueued
	})
}

func (f deliveryFixture) agentLoop(t *testing.T, opID string, actor state.Actor, target string) LoopView {
	t.Helper()
	req := loopReq(opID, target, time.Minute, "check the logs")
	req.Actor = actor
	req.OriginStage = assistantsTestStageID
	return f.create(t, req)
}

// gatedHelper attaches a stage helper whose turn stays open until release is called; every other task ends its
// turn at once. The helper is reachable meanwhile, so a loop can be made with it, and finished after release.
func (f deliveryFixture) gatedHelper(t *testing.T, opID string) (helper agentcomm.ParticipantID, release func()) {
	t.Helper()
	taskID := deriveTaskID(opID)
	gate := make(chan struct{})
	f.o.runHelperAgent = func(ctx context.Context, id, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if id == string(taskID) {
			awaitRelease(t, gate)
		}
		return passResultOutcome(), nil
	}
	attachTestTask(context.Background(), t, f.o, opID)
	waitForTaskStatus(t, f.store, taskID, assistants.StatusRunning, 2*time.Second)
	var released bool
	release = func() {
		if !released {
			released = true
			close(gate)
		}
		waitForTaskStatus(t, f.store, taskID, assistants.StatusCompleted, 2*time.Second)
	}
	t.Cleanup(release)
	return meshHelperParticipantID(taskID), release
}

func TestLoopTick_DeliversOneLabelledMessageAndALinkedTurn(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.clk.Advance(time.Minute)

	f.mustTick(t, "L1", 1)

	msgs := f.loopMessages(t, "L1")
	if len(msgs) != 1 {
		t.Fatalf("loop thread holds %d messages, want exactly the tick", len(msgs))
	}
	tick := msgs[0]
	if tick.Sender != meshUserParticipantID || len(tick.Recipients) != 1 || tick.Recipients[0] != f.agent || tick.AutoResult {
		t.Fatalf("tick = %+v, want a waking message from the creator to the target", tick)
	}
	if !strings.HasPrefix(tick.Body, "⟳ Loop L1 · every 1m") || !strings.Contains(tick.Body, "check the logs") || !strings.Contains(tick.Body, "stop") {
		t.Fatalf("tick body = %q, want the visible L1 header, the way to stop it and the prompt", tick.Body)
	}
	if tick.LoopKey != f.loopKey(t, "L1") {
		t.Fatalf("tick carries loop key %q, want the loop's own", tick.LoopKey)
	}
	if got := f.lastSlot(t, "L1"); got != 1 {
		t.Fatalf("last slot = %d, want 1 committed with the message", got)
	}

	// A normal turn follows, and its result is answered back to the creator, linked to the tick.
	f.waitForAgentTurns(t, 2)
	waitUntil(t, "the agent's reply to the tick", func() bool { return len(f.loopMessages(t, "L1")) == 2 })
	reply := f.loopMessages(t, "L1")[1]
	if reply.Sender != f.agent || reply.InReplyTo == nil || *reply.InReplyTo != tick.ID || !reply.AutoResult ||
		len(reply.Recipients) != 1 || reply.Recipients[0] != meshUserParticipantID {
		t.Fatalf("reply = %+v, want the agent's result linked to the tick, back to the creator", reply)
	}
}

func TestLoopTick_FirstTickOpensTheLoopsOwnThread(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	l, _ := f.store.Snapshot().Assistants.LoopByShortID("L1")
	if _, open := f.store.Snapshot().Assistants.Mesh.Threads[l.ThreadID]; open {
		t.Fatal("the thread exists before the first tick")
	}

	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)

	th := f.store.Snapshot().Assistants.Mesh.Threads[l.ThreadID]
	if th == nil || th.OriginStage != assistantsTestStageID || th.Status != agentcomm.ThreadOpen {
		t.Fatalf("thread = %+v, want opened by the first tick from the loop's origin Feed", th)
	}
	for _, member := range []agentcomm.ParticipantID{meshUserParticipantID, f.agent} {
		if !threadHasMember(th, member) {
			t.Fatalf("thread members %v lack %s", th.Members, member)
		}
	}
	threads := len(f.store.Snapshot().Assistants.Mesh.ThreadOrder)

	f.mustTick(t, "L1", 2) // the second tick goes to the same thread and opens nothing
	if got := len(f.store.Snapshot().Assistants.Mesh.ThreadOrder); got != threads {
		t.Fatalf("threads %d -> %d: only the first tick opens the thread", threads, got)
	}
}

// Ordinary traffic must not spend a loop thread budget or look like an unread tick.
func TestLoopTick_OwnThreadIsNeverReusedForAnOrdinarySend(t *testing.T) {
	f := newDeliveryFixture(t)
	helper, release := f.gatedHelper(t, "op-c1-helper")
	t.Cleanup(release)

	view := f.agentLoop(t, "op-c1-loop", state.Actor{Kind: "agent", ID: string(helper)}, "my-qa")
	f.clk.Advance(time.Minute)
	f.mustTick(t, view.ID, 1)

	l, ok := f.store.Snapshot().Assistants.LoopByShortID(view.ID)
	if !ok || l.ThreadID == "" {
		t.Fatalf("loop %s has no thread yet", view.ID)
	}

	receipt, err := f.o.SendFromAgent(context.Background(), SendRequest{
		OperationID: "op-c1-reply", Sender: helper, Recipients: []agentcomm.ParticipantID{f.agent},
		Kind: agentcomm.MessageKindMessage, Body: "status update", Actor: state.Actor{Kind: "agent", ID: string(helper)},
	})
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if receipt.ThreadID == l.ThreadID {
		t.Fatalf("ordinary send between the loop's own creator and target reused its thread %s", l.ThreadID)
	}
}

// A run agent that created a loop for itself gets its next turn from the tick, and nobody owes that tick an
// answer: the result is not mailed back to the agent itself.
func TestLoopTick_RunAgentSelfLoopGetsATurnWithoutAReply(t *testing.T) {
	f := newDeliveryFixture(t)
	agentActor := state.Actor{Kind: "agent", ID: string(f.agent)}
	view := f.agentLoop(t, "op-self", agentActor, "")
	if view.TargetID != f.agent || view.CreatorID != f.agent {
		t.Fatalf("view = %+v, want a self-loop", view)
	}

	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)

	msgs := f.loopMessages(t, "L1")
	if len(msgs) != 1 || msgs[0].Sender != f.agent || len(msgs[0].Recipients) != 1 || msgs[0].Recipients[0] != f.agent {
		t.Fatalf("loop thread = %+v, want the self-addressed tick alone: no automatic self-reply", msgs)
	}
	if got := f.store.Snapshot().Assistants.Mesh.Receipts[msgs[0].ID][f.agent]; got == agentcomm.DeliveryAccepted {
		t.Fatalf("receipt = %s: the agent's turn was woken but never handed its tick", got)
	}
	if strings.Contains(noticesText(t, f.o), "not delivered") {
		t.Fatal("the self-loop's turn reported an undelivered result")
	}
}

// Self-addressing is the loop's alone: an agent's own send to itself is still refused, and nothing a client can
// send carries the loop marker (SendRequest has no such field).
func TestLoopTick_OrdinarySelfSendStaysRejected(t *testing.T) {
	f := newDeliveryFixture(t)
	me := string(f.agent)

	if _, err := agentSend(f.o, "op-self-send", me, []string{me}, "note to self"); err == nil {
		t.Fatal("an ordinary self-send was accepted")
	}
	for _, m := range f.store.Snapshot().Assistants.Mesh.Messages {
		if m.LoopKey != "" {
			t.Fatalf("message %s carries a loop marker though no loop ticked", m.ID)
		}
	}
}

// A slot is committed once: asking again for it — a restart's retry, a repeated attempt — adds nothing.
func TestLoopTick_RetryNeverDuplicates(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)
	waitUntil(t, "the reply", func() bool { return len(f.loopMessages(t, "L1")) == 2 })
	seq := f.store.Snapshot().LastSeq

	f.mustTick(t, "L1", 1)

	if got := len(f.loopMessages(t, "L1")); got != 2 {
		t.Fatalf("loop thread holds %d messages after a repeated slot, want 2", got)
	}
	if got := f.store.Snapshot().LastSeq; got != seq {
		t.Fatalf("journal seq %d -> %d: a repeated slot must write nothing", seq, got)
	}
	f.mustTick(t, "L1", 0) // a slot that is not after the last one is as dead
	if got := f.store.Snapshot().LastSeq; got != seq {
		t.Fatalf("journal seq %d -> %d for slot 0", seq, got)
	}
}

// A tick is journaled under an id derived from its loop and slot, so a retry after a restart is the same
// operation, and carries the loop's marker.
func TestLoopTick_IsRecordedUnderItsDeterministicOperationID(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)
	waitUntil(t, "the reply", func() bool { return len(f.loopMessages(t, "L1")) == 2 })

	op, committed, err := f.store.CommittedOperation(loopTickOperationID(f.loopKey(t, "L1"), 1), opKindLoopTick)
	if err != nil || !committed {
		t.Fatalf("CommittedOperation = (%v, %v), want the tick recorded under its deterministic id", committed, err)
	}
	var p state.MeshMessageSentPayload
	if _, found, err := op.Effect("mesh_message_sent", &p); err != nil || !found || p.LoopKey != f.loopKey(t, "L1") {
		t.Fatalf("committed tick effect = (%+v, %v, %v), want the message marked with the loop key", p, found, err)
	}
}

// Whatever keeps the target from taking a message — or ends the loop — yields no message and no cursor move.
func TestLoopTick_SkippedSlotsLeaveNoTrace(t *testing.T) {
	cases := map[string]func(t *testing.T, f deliveryFixture){
		"stopped": func(t *testing.T, f deliveryFixture) {
			if _, err := f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: "op-stop", LoopID: "L1", Actor: loopUser}); err != nil {
				t.Fatalf("StopLoop: %v", err)
			}
		},
		"target held": func(t *testing.T, f deliveryFixture) {
			taskID, _ := runTaskForParticipant(f.store.Snapshot(), f.agent)
			if err := f.o.PauseTask(context.Background(), taskID); err != nil {
				t.Fatalf("PauseTask: %v", err)
			}
		},
		"target cancelled": func(t *testing.T, f deliveryFixture) {
			taskID, _ := runTaskForParticipant(f.store.Snapshot(), f.agent)
			if err := f.o.CancelTask(context.Background(), taskID); err != nil {
				t.Fatalf("CancelTask: %v", err)
			}
		},
		"review pause": func(t *testing.T, f deliveryFixture) {
			f.o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStatePaused})
		},
		"every stage terminal": func(t *testing.T, f deliveryFixture) {
			finishStage(t, f.o, assistantsTestStageID)
			finishStage(t, f.o, loopOtherStageID)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeliveryFixture(t)
			f.agentLoop(t, "op-1", loopUser, "my-qa")
			setup(t, f)
			seq := f.store.Snapshot().LastSeq

			if err := f.tick(t, "L1", 1); err != nil {
				t.Fatalf("commitLoopTick = %v, want nil: a skipped slot is not an error", err)
			}

			if got := f.store.Snapshot().LastSeq; got != seq {
				t.Fatalf("journal seq %d -> %d: a skipped slot must write nothing", seq, got)
			}
			if f.lastSlot(t, "L1") != 0 || len(f.loopMessages(t, "L1")) != 0 {
				t.Fatal("a skipped slot moved the cursor or left a message")
			}
		})
	}
}

// A tick must wait for the complete PauseFlow activation window before deciding admission.
// Holding flowPauseMu simulates the interval before PauseFlow publishes its marker.
func TestLoopTick_BlocksOnAConcurrentPauseActivationWindow(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	seq := f.store.Snapshot().LastSeq

	f.o.flowPauseMu.Lock()
	tickDone := make(chan error, 1)
	go func() { tickDone <- f.tick(t, "L1", 1) }()

	select {
	case <-tickDone:
		t.Fatal("commitLoopTick returned while flowPauseMu was held: it must wait for PauseFlow's activation window")
	case <-time.After(100 * time.Millisecond):
	}

	// PauseFlow reaches the end of its activation window: the marker now exists, mirroring the moment just
	// before it releases flowPauseMu.
	f.o.reviewMarker.Store(&state.PauseMarker{Version: 1, State: state.PauseStatePaused})
	f.o.flowPauseMu.Unlock()

	if err := <-tickDone; err != nil {
		t.Fatalf("commitLoopTick = %v, want nil: a skipped slot is not an error", err)
	}
	if got := f.store.Snapshot().LastSeq; got != seq {
		t.Fatalf("journal seq %d -> %d: the tick must see the marker set during its wait and skip, not commit during the activation window", seq, got)
	}
	if f.lastSlot(t, "L1") != 0 || len(f.loopMessages(t, "L1")) != 0 {
		t.Fatal("a tick that waited out a pause activation moved the cursor or left a message")
	}
}

// A busy target (a helper in the middle of its turn) is skipped without a message.
func TestLoopTick_BusyTargetGetsNoMessage(t *testing.T) {
	f := newSchedFixtureWith(t, true)
	f.o.loopTick = nil
	d := deliveryFixture{f}
	d.create(t, loopReq("op-busy", string(f.helper), time.Minute, "to the helper, which is mid-turn"))

	if err := d.tick(t, "L1", 1); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}
	if d.lastSlot(t, "L1") != 0 || len(d.loopMessages(t, "L1")) != 0 {
		t.Fatal("a busy target got a message")
	}
}

// The checks run again under the commit's lock: a plan made from an earlier snapshot loses the race to whatever
// committed in between — here the target being held — and costs only its own slot.
func TestLoopTick_CommitTimeCheckLosesTheRaceQuietly(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	key := f.loopKey(t, "L1")
	stale := f.store.Snapshot()
	taskID, _ := runTaskForParticipant(stale, f.agent)
	if err := f.o.PauseTask(context.Background(), taskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	seq := f.store.Snapshot().LastSeq

	if err := f.o.commitLoopTickFrom(context.Background(), stale, key, 1); err != nil {
		t.Fatalf("commitLoopTickFrom = %v, want nil: a lost race is not an error", err)
	}

	if got := f.store.Snapshot().LastSeq; got != seq {
		t.Fatalf("journal seq %d -> %d: the lost race wrote something", seq, got)
	}
}

// Failed or cancelled helpers cannot restart or receive ticks; Retry may restore a failed one.
func TestLoopTick_FailedOrCancelledHelperIsNotMessaged(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(t *testing.T, f deliveryFixture, helper assistants.TaskID)
		want assistants.TaskStatus
	}{
		{"failed", func(t *testing.T, f deliveryFixture, helper assistants.TaskID) {}, assistants.StatusFailed},
		{"cancelled", func(t *testing.T, f deliveryFixture, helper assistants.TaskID) {
			if err := f.o.CancelTask(context.Background(), helper); err != nil {
				t.Fatalf("CancelTask: %v", err)
			}
		}, assistants.StatusCancelled},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDeliveryFixture(t)
			gate := make(chan struct{})
			f.o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
				awaitRelease(t, gate)
				return executor.HelperOutcome{}, errors.New("boom")
			}
			task := attachTestTask(context.Background(), t, f.o, "op-helper")
			waitForTaskStatus(t, f.store, task, assistants.StatusRunning, 2*time.Second)
			f.create(t, loopReq("op-helper-loop", string(meshHelperParticipantID(task)), time.Minute, "status?"))
			c.end(t, f, task)
			close(gate)
			waitForTaskStatus(t, f.store, task, c.want, 2*time.Second)
			seq := f.store.Snapshot().LastSeq

			if err := f.tick(t, "L1", 1); err != nil {
				t.Fatalf("commitLoopTick = %v, want nil", err)
			}
			if f.lastSlot(t, "L1") != 0 || len(f.loopMessages(t, "L1")) != 0 || f.store.Snapshot().LastSeq != seq {
				t.Fatal("a helper that is over was messaged")
			}
		})
	}
}

// A closed loop thread ends the loop visibly, and no further tick goes into it.
func TestLoopTick_ClosedThreadEndsTheLoopVisibly(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)
	waitUntil(t, "the reply", func() bool { return len(f.loopMessages(t, "L1")) == 2 })
	l, _ := f.store.Snapshot().Assistants.LoopByShortID("L1")
	if err := f.o.CloseThread(context.Background(), CloseThreadRequest{OperationID: "op-close", ThreadID: l.ThreadID, Actor: loopUser}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	if err := f.tick(t, "L1", 2); err != nil {
		t.Fatalf("commitLoopTick = %v, want nil", err)
	}

	if got := len(f.loopMessages(t, "L1")); got != 2 {
		t.Fatalf("loop thread holds %d messages, want no tick in a closed thread", got)
	}
	page, err := f.o.ListLoops(context.Background(), ListLoopsRequest{Actor: loopUser})
	if err != nil || len(page.Loops) != 1 || page.Loops[0].Status != LoopEnded || !strings.Contains(page.Loops[0].EndReason, "thread") {
		t.Fatalf("list = (%+v, %v), want L1 ended with the thread as the reason", page, err)
	}
}

// A write that fails costs the slot nothing in the projection: the cursor stays, the message does not exist, and
// the error is the storage failure that ends the run.
func TestLoopTick_JournalFailureDoesNotAdvanceTheCursor(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.store.CloseEventsLogForTest()

	err := f.tick(t, "L1", 1)

	var storageErr *state.StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("commitLoopTick = %v, want the *state.StorageError", err)
	}
	if f.lastSlot(t, "L1") != 0 || len(f.loopMessages(t, "L1")) != 0 {
		t.Fatal("a failed write moved the cursor or left a message")
	}
}

// A loop a helper made for a run agent goes on after the helper is done: the agent's result for a tick has nobody
// to go back to, so no reply is attempted and nothing is reported as undelivered, tick after tick.
func TestLoopTick_HelperCreatedLoopOutlivesItsCreator(t *testing.T) {
	f := newDeliveryFixture(t)
	helper, release := f.gatedHelper(t, "op-creator")
	f.agentLoop(t, "op-helper-loop", state.Actor{Kind: "agent", ID: string(helper)}, "my-qa")
	release()
	if p, _ := resolveParticipant(f.store.Snapshot(), helper); p.Reachable {
		t.Fatal("the creating helper is still reachable: the scenario is not set up")
	}

	f.mustTick(t, "L1", 1)
	f.waitForAgentTurns(t, 2)
	f.mustTick(t, "L1", 2)
	f.waitForAgentTurns(t, 3)

	msgs := f.loopMessages(t, "L1")
	if len(msgs) != 2 {
		t.Fatalf("loop thread holds %d messages, want the two ticks and no reply to a finished helper: %+v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.Sender != helper || m.Recipients[0] != f.agent {
			t.Fatalf("message %+v, want the helper's tick to the run agent", m)
		}
	}
	if strings.Contains(noticesText(t, f.o), "not delivered") {
		t.Fatal("a result for a finished creator was reported as undelivered")
	}
}

// The scheduler and the delivery together: each due slot is one message and one turn, each answered once.
func TestLoopTick_SchedulerDeliversEachDueSlotOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	f.agentLoop(t, "op-1", loopUser, "my-qa")
	f.start(t)
	f.clk.BlockUntil(1)

	f.clk.Advance(time.Minute)
	waitUntil(t, "tick 1 and its reply", func() bool { return len(f.loopMessages(t, "L1")) == 2 })
	f.clk.BlockUntil(1)
	f.clk.Advance(time.Minute)
	waitUntil(t, "tick 2 and its reply", func() bool { return len(f.loopMessages(t, "L1")) == 4 })

	if got := f.lastSlot(t, "L1"); got != 2 {
		t.Fatalf("last slot = %d, want 2", got)
	}
	if got := f.agentTask().Turn; got != 3 { // the first message of the agent, then one turn per tick
		t.Fatalf("agent turns = %d, want 3", got)
	}
}
