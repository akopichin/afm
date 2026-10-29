package orchestrator

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// The release of waits for requests a task agent left unanswered, end to end: through Run and a restart, the
// Board, Pause and watchers.

// crashedRun is a run whose process stopped while stage impl waited for QA's answer to request B and QA had
// read B in the middle of its turn.
type crashedRun struct {
	runDir string
	qa     AttachAndSendResult
	b      agentcomm.MessageID
	waitB  agentcomm.WaitID
	c      agentcomm.MessageID // a request QA has not read ("" if none was sent)
}

// crashAfterQAReadB runs a first process: impl hands QA request A, asks it B and parks; QA reads B mid-turn and
// the process stops. With sendC, request C reaches QA unread. With idle, QA's turn is committed as ended the way
// production writes it: an idle agent gets no turn without an unread message.
func crashAfterQAReadB(t *testing.T, idle, sendC bool) crashedRun {
	t.Helper()
	runDir := t.TempDir()
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, runDir, runner)
	impl := meshStageParticipantID(recoveryImpl)
	var run crashedRun
	started, ready, bRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	rig.o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		close(started) // the turn's input is fixed from here: B is not part of it
		select {
		case <-ready:
		case <-ctx.Done():
			return executor.HelperOutcome{}, ctx.Err()
		}
		rig.o.deliverRenderedInbox(run.qa.ParticipantID)()
		readInbox(t, rig.o, run.qa.ParticipantID)
		close(bRead)
		<-ctx.Done() // the process stops mid-turn
		return executor.HelperOutcome{}, ctx.Err()
	}
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl || turn != 1 {
			return nil
		}
		ctx := context.Background()
		msg := "task A"
		qa, err := rig.o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-qa", ProfileID: "qa", Title: "QA", Instruction: "review", FirstMessage: &msg, Actor: state.Actor{Kind: "user"}})
		if err != nil {
			return err
		}
		run.qa = qa
		<-started
		th, err := rig.o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-th", Members: []agentcomm.ParticipantID{impl, qa.ParticipantID}, Scope: agentcomm.ScopeRun})
		if err != nil {
			return err
		}
		send := func(id string) (agentcomm.MessageID, error) {
			receipt, err := rig.o.Send(ctx, SendRequest{
				OperationID: "send-" + id, ThreadID: th.ID, Sender: impl, Recipients: []agentcomm.ParticipantID{qa.ParticipantID},
				Kind: agentcomm.MessageKindRequest, Body: "request " + id, Actor: state.Actor{Kind: "assistant", ID: recoveryImpl},
			})
			return receipt.MessageID, err
		}
		if run.b, err = send("b"); err != nil {
			return err
		}
		w, err := rig.o.Wait(ctx, WaitRequest{
			OperationID: "wait-b", Waiter: impl, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: run.b},
		})
		if err != nil {
			return err
		}
		run.waitB = w.WaitID
		close(ready)
		<-bRead
		if sendC {
			run.c, err = send("c")
		}
		return err
	}
	rig.start()
	rig.waitStage(recoveryImpl, state.StatusAwaitingPeer)
	rig.stop()

	if idle {
		tk := rig.store.Snapshot().Assistants.Tasks[run.qa.TaskID]
		eff, err := taskStatusChangeEffect(run.qa.TaskID, state.TaskStatusChangedPayload{Status: string(assistants.StatusQueued), Reason: runAgentIdleReason, Turn: tk.Turn})
		if err != nil {
			t.Fatal(err)
		}
		op := state.Operation{ID: "legacy-turn-end", Kind: "finish_task", Effects: []state.Effect{eff}}
		if _, err := rig.store.ApplyRunOperation(op, nil); err != nil {
			t.Fatal(err)
		}
	}
	run.runDir = runDir
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	return run
}

// restartRun runs a second process over the journal of run; qaTurn is what each QA turn does, after the
// inbox it was handed is delivered.
func restartRun(t *testing.T, run crashedRun, qaTurn func(rig *recoveryRig, n int32) executor.HelperOutcome) (*recoveryRig, *stageScriptRunner) {
	t.Helper()
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, run.runDir, runner)
	var turns atomic.Int32
	rig.o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		rig.o.deliverRenderedInbox(run.qa.ParticipantID)()
		return qaTurn(rig, turns.Add(1)), nil
	}
	rig.start()
	return rig, runner
}

func (run crashedRun) requireWaitBCancelled(t *testing.T, rig *recoveryRig) {
	t.Helper()
	if w := rig.store.Snapshot().Assistants.Mesh.Waits[run.waitB]; w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, leftReason) || !strings.Contains(w.Reason, "QA") {
		t.Fatalf("wait for B = %s %q, want cancelled: QA %s", w.Status, w.Reason, leftReason)
	}
}

// QA went idle after reading B without replying (an old journal, or a release lost to a crash) and a request C
// is waiting for it. The restart must end B's wait before QA starts the turn for C: a turn that then waits for a
// colleague is never swept, and B would hold its stage forever.
func TestLeftUnanswered_RestartDecidesBeforeTheAgentStartsItsNextTurn(t *testing.T) {
	run := crashAfterQAReadB(t, true, true)
	rig, runner := restartRun(t, run, func(rig *recoveryRig, n int32) executor.HelperOutcome {
		if n > 1 {
			return passResultOutcome()
		}
		asked := sendAs(t, rig.o, "op-q", run.qa.ThreadID, run.qa.ParticipantID, meshUserParticipantID, "a question", nil, false)
		if _, err := rig.o.Wait(context.Background(), WaitRequest{
			OperationID: "op-q-wait", Waiter: run.qa.ParticipantID, Deadline: testFarDeadline(),
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: asked},
		}); err != nil {
			t.Errorf("Wait: %v", err)
		}
		return executor.HelperOutcome{ProcessOK: true}
	})

	rig.waitStage(recoveryDocs, state.StatusDone)
	run.requireWaitBCancelled(t, rig)
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns after the restart = %d, want the single continuation", got)
	}
	waitForTaskStatus(t, rig.store, run.qa.TaskID, assistants.StatusWaitingPeer, taskWait) // it did start the turn for C
}

// QA went idle after reading B and nothing is waiting for it: no turn starts, so only the startup release can
// end B's wait and let impl continue.
func TestLeftUnanswered_RestartOfAnIdleAgentWithNothingToReadEndsTheWait(t *testing.T) {
	run := crashAfterQAReadB(t, true, false)
	var qaTurns atomic.Int32
	rig, runner := restartRun(t, run, func(*recoveryRig, int32) executor.HelperOutcome {
		qaTurns.Add(1)
		return passResultOutcome()
	})

	rig.waitStage(recoveryDocs, state.StatusDone)
	run.requireWaitBCancelled(t, rig)
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns after the restart = %d, want the single continuation", got)
	}
	if got := qaTurns.Load(); got != 0 {
		t.Fatalf("QA turns after the restart = %d, want none: it had nothing to read", got)
	}
}

// QA was cut short in the middle of the turn in which it read B. The restart puts it back in the queue and its
// retry is handed only the turn's input: B is closed with the reason and impl continues once.
func TestLeftUnanswered_RestartAfterACutShortTurnClosesTheRequestItRead(t *testing.T) {
	run := crashAfterQAReadB(t, false, false)
	rig, runner := restartRun(t, run, func(*recoveryRig, int32) executor.HelperOutcome { return passResultOutcome() })

	rig.waitStage(recoveryDocs, state.StatusDone)
	run.requireWaitBCancelled(t, rig)
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns after the restart = %d, want the single continuation", got)
	}
	if got := repliesTo(rig.store, run.b); len(got) != 0 {
		t.Fatalf("replies to B = %+v, want none: the retry answers A", got)
	}
	waitUntilFor(t, 10*time.Second, "the retry answers A", func() bool { return len(repliesTo(rig.store, *run.qa.MessageID)) == 1 })
}

// Several waits for one request, of one waiter: they all end first and the waiter is woken once, so it cannot
// resume on the first and park again before the others are cancelled.
func TestLeftUnanswered_WaitersAreWokenOnceAfterEveryWaitIsCancelled(t *testing.T) {
	f := newLeftFixture(t)
	b := f.send("b")
	f.readInboxMidTurn()
	f.finish()
	for _, op := range []string{"legacy-1", "legacy-2"} {
		registerLegacyWait(t, f.store, op, unreadTestStage, b)
	}
	registerLegacyWait(t, f.store, "legacy-user", meshUserParticipantID, b)

	var woken []agentcomm.ParticipantID
	f.o.releaseUnanswerableWaits(context.Background(), func(_ context.Context, waiter agentcomm.ParticipantID) {
		for id, w := range f.store.Snapshot().Assistants.Mesh.Waits {
			if w.Waiter == waiter && w.Status == agentcomm.WaitPending {
				t.Errorf("waiter %s woken while its wait %s is still pending: it could park again", waiter, id)
			}
		}
		woken = append(woken, waiter)
	})

	want := []agentcomm.ParticipantID{unreadTestStage, meshUserParticipantID}
	slices.Sort(want)
	slices.Sort(woken)
	if !slices.Equal(woken, want) {
		t.Fatalf("woken = %v, want each waiter once", woken)
	}
}

// The incident, end to end: the coordinator assigns a Board task to QA (request A), sends a separate detailed
// request B and waits for it; QA reads B mid-turn, marks the task done and finishes. The result answers A only,
// B's wait is cancelled, the done notice resolves nothing, and the cancellation wakes the coordinator once.
func TestLeftUnanswered_BoardTaskIncident(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner, func(store *state.Store) {
		if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, state.Actor{Kind: "system", ID: "afm"}); err != nil {
			t.Fatal(err)
		}
	})
	impl := meshStageParticipantID(recoveryImpl)
	qa := addTestRunAgent(t, rig.o, "op-qa", nil)
	const key = "task/b3-incident"
	var (
		assigned    BoardTaskReceipt
		b           agentcomm.MessageID
		waitB       agentcomm.WaitID
		afterCancel atomic.Value // what the coordinator's last turn saw: B's wait status and the verify calls so far
	)
	started, release := make(chan struct{}), make(chan struct{})
	rig.o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		rig.o.deliverRenderedInbox(qa.ParticipantID)()
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return executor.HelperOutcome{}, ctx.Err()
		}
		readInbox(t, rig.o, qa.ParticipantID)
		mustBoardTask(t, rig.o, BoardTaskRequest{
			OperationID: "done", Action: BoardTaskDone, Actor: qa.ParticipantID, Key: key, ExpectedVersion: 2, AssignmentID: assigned.AssignmentID, Text: "shipped",
		})
		// The done notice woke the coordinator for a turn of its own, which parks again on B: the cancellation
		// comes after it, so the turn it causes is told apart.
		waitUntilFor(t, 10*time.Second, "the notice turn parks the coordinator again", func() bool {
			return runner.turnsOf(recoveryImpl) >= 2 && rig.store.Get(recoveryImpl) == state.StatusAwaitingPeer
		})
		return passResultOutcome(), nil
	}
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl {
			return nil
		}
		if turn == 3 {
			afterCancel.Store(verifyAndWait{waitB: rig.store.Snapshot().Assistants.Mesh.Waits[waitB].Status, verifies: rig.verifyCalls.Load()})
			return nil
		}
		if turn == 2 {
			return nil
		}
		ctx := context.Background()
		var err error
		if assigned, err = rig.o.BoardTask(ctx, BoardTaskRequest{
			OperationID: "create", Action: BoardTaskCreate, Actor: impl, Key: key, Title: new("B3"), To: string(qa.ParticipantID),
		}); err != nil {
			return err
		}
		<-started
		receipt, err := rig.o.Send(ctx, SendRequest{
			OperationID: "send-b", ThreadID: assigned.ThreadID, Sender: impl, Recipients: []agentcomm.ParticipantID{qa.ParticipantID},
			Kind: agentcomm.MessageKindRequest, Body: "detailed request B", Actor: state.Actor{Kind: "assistant", ID: recoveryImpl},
		})
		if err != nil {
			return err
		}
		b = receipt.MessageID
		w, err := rig.o.Wait(ctx, WaitRequest{
			OperationID: "wait-b", Waiter: impl, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: b},
		})
		if err != nil {
			return err
		}
		waitB = w.WaitID
		go func() {
			waitUntilFor(t, 10*time.Second, "the coordinator parks", func() bool { return rig.store.Get(recoveryImpl) == state.StatusAwaitingPeer })
			close(release)
		}()
		return nil
	}
	rig.start()

	rig.waitStage(recoveryDocs, state.StatusDone)
	snap := rig.store.Snapshot()
	toA := repliesTo(rig.store, assigned.MessageID)
	if len(toA) != 1 || toA[0].Sender != qa.ParticipantID {
		t.Fatalf("replies to A = %+v, want QA's single turn result", toA)
	}
	if w := snap.Assistants.Mesh.Waits[waitB]; w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, leftReason) || !strings.Contains(w.Reason, string(toA[0].ID)) {
		t.Fatalf("wait for B = %s %q, want cancelled: %s, naming the turn result %s", w.Status, w.Reason, leftReason, toA[0].ID)
	}
	if got := repliesTo(rig.store, b); len(got) != 0 {
		t.Fatalf("replies to B = %+v, want none", got)
	}
	if got := runner.turnsOf(recoveryImpl); got != 3 {
		t.Fatalf("impl turns = %d, want the asking turn, the turn for the done notice and one continuation after the cancellation", got)
	}
	last := afterCancel.Load().(verifyAndWait)
	if last.waitB != agentcomm.WaitCancelled || last.verifies != 0 {
		t.Fatalf("the continuation saw B %s after %d verify calls, want cancelled and none: the cancellation is not a completion", last.waitB, last.verifies)
	}
	if got := rig.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify calls = %d, want the one of the completed stage", got)
	}
}

// verifyAndWait is what a coordinator turn saw when it started.
type verifyAndWait struct {
	waitB    agentcomm.WaitStatus
	verifies int32
}

// A Pause of QA in the middle of the turn in which it read B puts the turn back in the queue: B, read and not
// part of the input the retry is handed again, ends with the reason; the retry answers A and no second process
// starts alongside it.
func TestLeftUnanswered_PausedMidTurnClosesTheRequestItRead(t *testing.T) {
	f := newLeftFixture(t)
	b, wait := f.ask("b")
	f.readInboxMidTurn()

	if err := f.o.PauseTask(context.Background(), f.rev.TaskID); err != nil {
		t.Fatalf("PauseTask: %v", err)
	}
	waitUntil(t, "the wait for B ends", func() bool { return f.wait(wait).Status != agentcomm.WaitPending })
	f.requireCancelled(wait)
	if got := f.turns.Load(); got != 1 {
		t.Fatalf("turns = %d while paused, want only the interrupted one", got)
	}

	if err := f.o.ContinueTask(context.Background(), f.rev.TaskID); err != nil {
		t.Fatalf("ContinueTask: %v", err)
	}
	waitForRunAgentTurn(t, f.store, f.rev.TaskID, taskWait)
	f.o.concurrency.WaitAgents()
	if got := f.turns.Load(); got != 2 {
		t.Fatalf("turns = %d, want the interrupted one and a single retry", got)
	}
	if got := repliesTo(f.store, *f.rev.MessageID); len(got) != 1 {
		t.Fatalf("replies to A = %+v, want the retry's result: A was handed again", got)
	}
	if got := repliesTo(f.store, b); len(got) != 0 {
		t.Fatalf("replies to B = %+v, want none", got)
	}
}

// An observer reads a request in a pass and rests again: the wait for the request ends then, as for any agent
// gone idle.
func TestLeftUnanswered_WatcherPassThatReadARequestEndsItsWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	started, release := make(chan struct{}), make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		close(started)
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	id := attachWatcherTask(t, o, "op-watch", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
	waitForTaskStatus(t, store, id, assistants.StatusWatching, 2*time.Second)
	watcher := meshHelperParticipantID(id)
	thread := openThread(t, o, "op-th", 0, stage, watcher)

	o.fireWatcherTask(context.Background(), id)
	<-started
	b := sendAs(t, o, "op-b", thread, stage, watcher, "request B", nil, false)
	readInbox(t, o, watcher)
	w, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-b", Waiter: stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: b},
	})
	if err != nil || w.Status != agentcomm.WaitPending {
		t.Fatalf("Wait = %+v, %v; want pending while the pass runs", w, err)
	}
	close(release)

	waitUntil(t, "the wait for B ends", func() bool { return store.Snapshot().Assistants.Mesh.Waits[w.WaitID].Status != agentcomm.WaitPending })
	if got := store.Snapshot().Assistants.Tasks[id].Status; got != assistants.StatusWatching {
		t.Fatalf("watcher = %s, want it resting again", got)
	}
	if got := store.Snapshot().Assistants.Mesh.Waits[w.WaitID]; got.Status != agentcomm.WaitCancelled || !strings.Contains(got.Reason, leftReason) {
		t.Fatalf("wait for B = %s %q, want cancelled: %s", got.Status, got.Reason, leftReason)
	}
	o.concurrency.WaitAgents()
}
