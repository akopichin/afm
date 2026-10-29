package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A stage that waits for the result of its own spawned helper is woken by the helper's callback requests, so the
// helper may wait for the stage's answer without that being a deadlock.

func helperActor(p agentcomm.ParticipantID) state.Actor {
	return state.Actor{Kind: "agent", ID: string(p)}
}

// sendHelperCallback sends a request from helper to the stage in the helper's own thread. It reports an error
// instead of failing the test, so a helper turn running on an orchestrator goroutine can use it.
func sendHelperCallback(o *Orchestrator, h AttachAndSendResult, stage agentcomm.ParticipantID, opID string) (agentcomm.MessageID, error) {
	sent, err := o.Send(context.Background(), SendRequest{
		OperationID: opID, ThreadID: h.ThreadID, Sender: h.ParticipantID, Recipients: []agentcomm.ParticipantID{stage},
		Kind: agentcomm.MessageKindRequest, Body: "which branch?", Actor: helperActor(h.ParticipantID),
	})
	return sent.MessageID, err
}

func helperCallback(t *testing.T, o *Orchestrator, h AttachAndSendResult, opID string) agentcomm.MessageID {
	t.Helper()
	id, err := sendHelperCallback(o, h, spawnStage, opID)
	if err != nil {
		t.Fatalf("callback %s: %v", opID, err)
	}
	return id
}

// stageAnswersAs sends the answer of stage (and its actor) to a helper's callback.
func stageAnswersAs(o *Orchestrator, h AttachAndSendResult, opID string, callback agentcomm.MessageID, stage agentcomm.ParticipantID, actor state.Actor) error {
	_, err := o.Send(context.Background(), SendRequest{
		OperationID: opID, ThreadID: h.ThreadID, Sender: stage, Recipients: []agentcomm.ParticipantID{h.ParticipantID},
		Kind: agentcomm.MessageKindReply, InReplyTo: &callback, Body: "main", Actor: actor,
	})
	return err
}

func stageAnswers(o *Orchestrator, h AttachAndSendResult, opID string, callback agentcomm.MessageID) error {
	return stageAnswersAs(o, h, opID, callback, spawnStage, spawnStageActor)
}

func waitReplyTo(o *Orchestrator, opID string, waiter agentcomm.ParticipantID, msg agentcomm.MessageID) (WaitReceipt, error) {
	return o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: waiter, Actor: helperActor(waiter),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg},
	})
}

// helperTurnOutcome is what a fake helper turn reports to the test goroutine.
type helperTurnOutcome struct {
	callback agentcomm.MessageID
	wait     WaitReceipt
	err      error
}

func receiveHelperTurn(t *testing.T, ch <-chan helperTurnOutcome) helperTurnOutcome {
	t.Helper()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("helper callback or wait was refused: %v", out.err)
		}
		return out
	case <-time.After(5 * time.Second):
		t.Fatal("the helper turn did not report")
		return helperTurnOutcome{}
	}
}

func TestWait_HelperMayWaitForCallbackAnswerWhileItsStageWaitsForIt(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	// Turn 1 of the helper asks the stage and yields on the answer; turn 2 (woken by the answer) finishes.
	var (
		turns        atomic.Int32
		spawned      AttachAndSendResult
		stageWaiting = make(chan struct{})
		turn1        = make(chan helperTurnOutcome, 1)
	)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if turns.Add(1) > 1 {
			return passResultOutcome(), nil
		}
		awaitRelease(t, stageWaiting)
		var out helperTurnOutcome
		out.callback, out.err = sendHelperCallback(o, spawned, spawnStage, "op-edge-callback")
		if out.err == nil {
			out.wait, out.err = waitReplyTo(o, "op-edge-helper-wait", spawned.ParticipantID, out.callback)
		}
		turn1 <- out
		return executor.HelperOutcome{ProcessOK: true}, nil
	}

	spawned = mustSpawn(t, o, "op-edge-spawn", "review the change")
	stageWait, err := waitReplyTo(o, "op-edge-stage-wait", spawnStage, *spawned.MessageID)
	if err != nil || stageWait.Status != agentcomm.WaitPending {
		t.Fatalf("stage wait = %+v, %v; want pending", stageWait, err)
	}
	close(stageWaiting)

	// The callback wait is admitted although the stage already waits for this helper: the helper parks on it.
	out := receiveHelperTurn(t, turn1)
	waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusWaitingPeer, 2*time.Second)

	// The journal replays to the same graph: the stage's wait adds no edge, the helper's wait adds one.
	replayed, err := state.LoadRunState(o.opts.RunDir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	live := buildMeshWaitGraph(store.Snapshot(), nil, nil)
	replay := buildMeshWaitGraph(replayed, nil, nil)
	for _, p := range []agentcomm.ParticipantID{spawnStage, spawned.ParticipantID} {
		if len(live[p]) != len(replay[p]) {
			t.Fatalf("edges of %s: live %d, replayed %d, want the same", p, len(live[p]), len(replay[p]))
		}
	}
	if len(replay[spawnStage]) != 0 || len(replay[spawned.ParticipantID]) != 1 {
		t.Fatalf("replayed edges: stage %d, helper %d, want 0 and 1", len(replay[spawnStage]), len(replay[spawned.ParticipantID]))
	}

	// The stage answers the callback: the helper's second turn runs, the stage's own wait stays pending on the same id.
	if err := stageAnswers(o, spawned, "op-edge-answer", out.callback); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if st := waitStatus(store, out.wait.WaitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("helper wait = %s after the answer, want ready", st)
	}

	// The helper's result resolves the stage's wait.
	waitUntil(t, "the stage wait is ready", func() bool { return waitStatus(store, stageWait.WaitID).Status == agentcomm.WaitReady })
	if got := store.Snapshot().Assistants.Tasks[spawned.TaskID].Status; got != assistants.StatusCompleted {
		t.Fatalf("helper = %s, want completed", got)
	}
	o.concurrency.WaitAgents()
}

// A stage that registers its wait for the helper while the helper already waits for the stage is a true cycle:
// the callback is already pending, so the exemption does not apply to the reverse order.
func TestWait_StageWaitForHelperThatAlreadyWaitsForItIsACycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })

	h := mustSpawn(t, o, "op-edge-rev-spawn", "review the change")
	callback := helperCallback(t, o, h, "op-edge-rev-callback")
	helperWait, err := waitReplyTo(o, "op-edge-rev-helper-wait", h.ParticipantID, callback)
	if err != nil {
		t.Fatalf("helper wait: %v", err)
	}

	_, err = waitReplyTo(o, "op-edge-rev-stage-wait", spawnStage, *h.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)

	// Let the helper finish: cancel its open wait first, as it would be resolved before the turn ends.
	if err := o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-edge-rev-cancel", WaitID: helperWait.WaitID, Actor: helperActor(h.ParticipantID)}); err != nil {
		t.Fatal(err)
	}
	gates.release(h.TaskID)
	waitUntil(t, "the helper completed", func() bool { return store.Snapshot().Assistants.Tasks[h.TaskID].Status == assistants.StatusCompleted })
	o.concurrency.WaitAgents()
}

// A helper the stage did not spawn (here: attached by the human) keeps the cycle check: nothing says the stage
// is woken by its callbacks.
func TestWait_ForeignHelperWaitingForTheStageStillClosesACycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	helper := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-edge-foreign"))
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-edge-foreign-open", Members: []agentcomm.ParticipantID{spawnStage, helper}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := o.Send(ctx, SendRequest{OperationID: "op-edge-foreign-ask", ThreadID: thread.ID, Sender: spawnStage, Recipients: []agentcomm.ParticipantID{helper}, Kind: agentcomm.MessageKindRequest, Body: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitReplyTo(o, "op-edge-foreign-stage-wait", spawnStage, ask.MessageID); err != nil {
		t.Fatalf("stage wait: %v", err)
	}
	callback, err := o.Send(ctx, SendRequest{OperationID: "op-edge-foreign-callback", ThreadID: thread.ID, Sender: helper, Recipients: []agentcomm.ParticipantID{spawnStage}, Kind: agentcomm.MessageKindRequest, Body: "q2"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = waitReplyTo(o, "op-edge-foreign-helper-wait", helper, callback.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)
}

// The exemption lives in the wait graph only: what the graph holds for a stage wait is checked on a hand-built
// projection so each ownership rule is pinned on its own.
func TestBuildMeshWaitGraph_StageWaitOnItsOwnCurrentOneShotHelpersAddsNoEdge(t *testing.T) {
	const stageID = "impl"
	stage := meshStageParticipantID(stageID)
	ownTask := func(id string, mutate func(*state.TaskState)) *state.TaskState {
		ts := &state.TaskState{Task: assistants.Task{
			ID: assistants.TaskID(id), Scope: assistants.ScopeStage, RootStageID: stageID, CreatedBy: assistants.OriginStageAgent,
			Trigger: assistants.TriggerOneShot, RootGeneration: 2, Status: assistants.StatusRunning,
		}}
		if mutate != nil {
			mutate(ts)
		}
		return ts
	}
	cases := []struct {
		name      string
		task      *state.TaskState
		wantEdges int
	}{
		{"own current one-shot helper", ownTask("h1", nil), 0},
		{"own helper with the default trigger", ownTask("h1", func(ts *state.TaskState) { ts.Trigger = "" }), 0},
		{"helper of an old root generation", ownTask("h1", func(ts *state.TaskState) { ts.RootGeneration = 1 }), 1},
		{"helper of another stage", ownTask("h1", func(ts *state.TaskState) { ts.RootStageID = "other" }), 1},
		{"human-attached helper", ownTask("h1", func(ts *state.TaskState) { ts.CreatedBy = assistants.OriginUser }), 1},
		{"watcher helper", ownTask("h1", func(ts *state.TaskState) { ts.Trigger = assistants.TriggerCheckpoints }), 1},
		{"run-scoped agent", ownTask("h1", func(ts *state.TaskState) { ts.Scope = assistants.ScopeRun; ts.RootStageID = "" }), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := *state.NewRunState([]string{stageID, "other"})
			rs.Assistants.RootGenerations[stageID] = 2
			rs.Assistants.Tasks[tc.task.ID] = tc.task
			target := meshHelperParticipantID(tc.task.ID)
			rs.Assistants.Mesh.Messages["m1"] = &agentcomm.Message{ID: "m1", Sender: stage, Recipients: []agentcomm.ParticipantID{target}}
			rs.Assistants.Mesh.Waits["w1"] = &state.MeshWait{Wait: agentcomm.Wait{
				ID: "w1", Waiter: stage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"},
			}}

			if got := len(buildMeshWaitGraph(rs, nil, nil)[stage]); got != tc.wantEdges {
				t.Fatalf("edges of the stage = %d, want %d", got, tc.wantEdges)
			}
		})
	}

	t.Run("a foreign recipient next to the helper keeps the edge", func(t *testing.T) {
		rs := *state.NewRunState([]string{stageID})
		rs.Assistants.RootGenerations[stageID] = 2
		task := ownTask("h1", nil)
		rs.Assistants.Tasks[task.ID] = task
		rs.Assistants.Mesh.Messages["m1"] = &agentcomm.Message{ID: "m1", Sender: stage, Recipients: []agentcomm.ParticipantID{meshHelperParticipantID(task.ID), "helper:foreign"}}
		rs.Assistants.Mesh.Waits["w1"] = &state.MeshWait{Wait: agentcomm.Wait{
			ID: "w1", Waiter: stage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"},
		}}
		if got := len(buildMeshWaitGraph(rs, nil, nil)[stage]); got != 1 {
			t.Fatalf("edges of the stage = %d, want 1", got)
		}
	})

	// The skipped edge is only valid while every pending wait of the helper is an answer to a request addressed to
	// the stage: a wait the stage's callback turn cannot satisfy keeps the edge, whatever else the helper waits for.
	helperWaits := func(t *testing.T, conds ...agentcomm.WaitCondition) int {
		rs := *state.NewRunState([]string{stageID})
		rs.Assistants.RootGenerations[stageID] = 2
		task := ownTask("h1", nil)
		rs.Assistants.Tasks[task.ID] = task
		helper := meshHelperParticipantID(task.ID)
		rs.Assistants.Mesh.Messages["m-stage"] = &agentcomm.Message{ID: "m-stage", Sender: stage, Recipients: []agentcomm.ParticipantID{helper}}
		rs.Assistants.Mesh.Messages["m-callback"] = &agentcomm.Message{ID: "m-callback", Sender: helper, Recipients: []agentcomm.ParticipantID{stage}}
		rs.Assistants.Mesh.Messages["m-other"] = &agentcomm.Message{ID: "m-other", Sender: helper, Recipients: []agentcomm.ParticipantID{"run:other"}}
		rs.Assistants.Mesh.Waits["w-stage"] = &state.MeshWait{Wait: agentcomm.Wait{
			ID: "w-stage", Waiter: stage, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m-stage"},
		}}
		for i, cond := range conds {
			id := agentcomm.WaitID(fmt.Sprintf("w-helper-%d", i))
			rs.Assistants.Mesh.Waits[id] = &state.MeshWait{Wait: agentcomm.Wait{ID: id, Waiter: helper, Status: agentcomm.WaitPending, Condition: cond}}
		}
		return len(buildMeshWaitGraph(rs, nil, nil)[stage])
	}
	callbackWait := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m-callback"}
	otherWait := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m-other"}
	stageDone := agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: stageID, Status: "done"}
	for _, tc := range []struct {
		name      string
		conds     []agentcomm.WaitCondition
		wantEdges int
	}{
		{"helper without waits", nil, 0},
		{"helper waiting for the stage's answer", []agentcomm.WaitCondition{callbackWait}, 0},
		{"helper waiting for an answer of someone else", []agentcomm.WaitCondition{otherWait}, 1},
		{"helper waiting for the stage to finish", []agentcomm.WaitCondition{stageDone}, 1},
		{"callback wait next to a stage-status wait", []agentcomm.WaitCondition{callbackWait, stageDone}, 1},
		{"stage-status wait that also names the callback", []agentcomm.WaitCondition{{Kind: agentcomm.WaitOnStageStatus, StageID: stageID, Status: "done", MessageID: "m-callback"}}, 1},
	} {
		t.Run("stage wait edge: "+tc.name, func(t *testing.T) {
			if got := helperWaits(t, tc.conds...); got != tc.wantEdges {
				t.Fatalf("edges of the stage = %d, want %d", got, tc.wantEdges)
			}
		})
	}

	t.Run("a helper waiting for something never gets the exemption for its own edges", func(t *testing.T) {
		rs := *state.NewRunState([]string{stageID})
		rs.Assistants.RootGenerations[stageID] = 2
		task := ownTask("h1", nil)
		rs.Assistants.Tasks[task.ID] = task
		helper := meshHelperParticipantID(task.ID)
		rs.Assistants.Mesh.Messages["m1"] = &agentcomm.Message{ID: "m1", Sender: helper, Recipients: []agentcomm.ParticipantID{stage}}
		rs.Assistants.Mesh.Waits["w1"] = &state.MeshWait{Wait: agentcomm.Wait{
			ID: "w1", Waiter: helper, Status: agentcomm.WaitPending, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"},
		}}
		if got := len(buildMeshWaitGraph(rs, nil, nil)[helper]); got != 1 {
			t.Fatalf("edges of the helper = %d, want 1", got)
		}
	})
}

// The whole stage flow of the live incident: the stage spawns a helper and waits once for its result; the helper
// asks the stage and parks on the answer; the callback wakes the stage, which answers and parks again on the SAME
// wait; the helper's result then resumes it once and the stage finishes.
func TestStageWaitOnSpawnedHelperSurvivesCallbackAndFinishesOnTheResult(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	stageActor := state.Actor{Kind: "agent", ID: string(unreadTestStage)}

	var (
		spawned      AttachAndSendResult
		stageWaitID  agentcomm.WaitID
		stageWaiting = make(chan struct{})
		helperTurns  atomic.Int32
		callbackSent = make(chan agentcomm.MessageID, 1)
		turn1        = make(chan helperTurnOutcome, 1)
	)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if helperTurns.Add(1) > 1 {
			return passResultOutcome(), nil
		}
		awaitRelease(t, stageWaiting)
		var out helperTurnOutcome
		out.callback, out.err = sendHelperCallback(o, spawned, unreadTestStage, "op-flow-callback")
		if out.err == nil {
			callbackSent <- out.callback
			out.wait, out.err = waitReplyTo(o, "op-flow-helper-wait", spawned.ParticipantID, out.callback)
		}
		turn1 <- out
		return executor.HelperOutcome{ProcessOK: true}, nil
	}

	runner.turn = func(n int32) error {
		switch n {
		case 1:
			var err error
			spawned, err = o.SpawnHelper(context.Background(), SpawnHelperRequest{
				OperationID: "op-flow-spawn", ProfileID: "qa", Title: "Review", Instruction: "review the change", Actor: stageActor,
			})
			if err != nil {
				return err
			}
			receipt, err := waitReplyTo(o, "op-flow-stage-wait", unreadTestStage, *spawned.MessageID)
			if err != nil {
				return err
			}
			stageWaitID = receipt.WaitID
			close(stageWaiting)
		case 2: // woken by the callback while the wait is still pending
			if st := waitStatus(store, stageWaitID).Status; st != agentcomm.WaitPending {
				return fmt.Errorf("stage wait = %s in the callback turn, want pending", st)
			}
			select {
			case callback := <-callbackSent:
				return stageAnswersAs(o, spawned, "op-flow-answer", callback, unreadTestStage, stageActor)
			case <-time.After(5 * time.Second):
				return errors.New("the helper's callback never arrived")
			}
		default:
		}
		return nil
	}

	runToEnd(t, o)

	receiveHelperTurn(t, turn1)
	if got := runner.turns.Load(); got != 3 {
		t.Fatalf("stage turns = %d, want 3 (spawn+wait, callback answer, result)", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	var stageWaits int
	for _, w := range store.Snapshot().Assistants.Mesh.Waits {
		if w.Waiter == unreadTestStage {
			stageWaits++
		}
	}
	if stageWaits != 1 {
		t.Fatalf("waits registered by the stage = %d, want exactly 1 (the same wait through the callback turn)", stageWaits)
	}
	if st := waitStatus(store, stageWaitID).Status; st != agentcomm.WaitReady {
		t.Fatalf("stage wait = %s at the end, want ready", st)
	}
}

// A helper that finished before the stage registered its wait: the result reply is already there, so the wait is
// ready at once and the stage does not yield.
func TestWait_OnAHelperThatAlreadyFinishedIsReadyAtRegistration(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil }).release(deriveTaskID("op-edge-done-spawn"))
	h := mustSpawn(t, o, "op-edge-done-spawn", "review the change")
	waitForTaskStatus(t, store, h.TaskID, assistants.StatusCompleted, 2*time.Second)

	receipt, err := waitReplyTo(o, "op-edge-done-wait", spawnStage, *h.MessageID)
	if err != nil || receipt.Status != agentcomm.WaitReady || receipt.AlreadyPending {
		t.Fatalf("wait = %+v, %v; want ready at registration", receipt, err)
	}
	o.concurrency.WaitAgents()
}

// The stage's edge to its own helper is skipped only because the helper's waits are answers to the stage's own
// callbacks. Any other wait of the helper — here on the stage finishing — is not something a callback turn of the
// stage can satisfy, so the edge stays and the cycle is seen.
func TestWait_HelperWaitThatNeedsTheStageToFinishIsACycle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-edge-done-spawn", "review the change")
	if _, err := waitReplyTo(o, "op-edge-done-stage-wait", spawnStage, *h.MessageID); err != nil {
		t.Fatalf("stage wait: %v", err)
	}
	stageDone := func(opID string) error {
		_, err := o.Wait(context.Background(), WaitRequest{
			OperationID: opID, Waiter: h.ParticipantID, Actor: helperActor(h.ParticipantID),
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(state.StatusDone)},
		})
		return err
	}

	requireRejected(t, stageDone("op-edge-done-wait"), agentcomm.CodeWaitCycle)

	// The helper's callback wait is admitted, and a stage-status wait next to it still is not.
	callback := helperCallback(t, o, h, "op-edge-done-callback")
	if _, err := waitReplyTo(o, "op-edge-done-callback-wait", h.ParticipantID, callback); err != nil {
		t.Fatalf("callback wait: %v", err)
	}
	requireRejected(t, stageDone("op-edge-done-wait-2"), agentcomm.CodeWaitCycle)

	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 2 {
		t.Fatalf("waits = %d, want the stage's and the helper's callback wait only", n)
	}
}

// Pinned: the stage re-registers its wait while the helper still waits for the answer to its callback. The
// duplicate goes through admission first, so it is the same wait_cycle as a fresh wait: parking again without
// answering would leave both waiting for each other. Once the stage has answered, the same call returns the first wait.
func TestWait_StageRepeatsItsWaitWhileHelperAwaitsItsAnswerIsACycleUntilAnswered(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-edge-rep-spawn", "review the change")
	first, err := waitReplyTo(o, "op-edge-rep-stage-wait", spawnStage, *h.MessageID)
	if err != nil {
		t.Fatalf("stage wait: %v", err)
	}
	callback := helperCallback(t, o, h, "op-edge-rep-callback")
	if _, err := waitReplyTo(o, "op-edge-rep-helper-wait", h.ParticipantID, callback); err != nil {
		t.Fatalf("helper callback wait: %v", err)
	}

	_, err = waitReplyTo(o, "op-edge-rep-stage-again", spawnStage, *h.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)

	if err := stageAnswers(o, h, "op-edge-rep-answer", callback); err != nil {
		t.Fatalf("answer: %v", err)
	}
	again, err := waitReplyTo(o, "op-edge-rep-stage-again-2", spawnStage, *h.MessageID)
	if err != nil || again.WaitID != first.WaitID || !again.AlreadyPending {
		t.Fatalf("after the answer = %+v, %v; want the first wait %s, already pending", again, err, first.WaitID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 2 {
		t.Fatalf("waits = %d, want 2 (the stage's and the helper's)", n)
	}
}

// A helper woken by a message of the stage while its wait for the stage's answer is still pending repeats that
// wait (it does not remember its first call) and gets already_pending. If the answer lands before the process
// exits, the empty exit is still a yield: the wait was pending when the turn started and ended during it.
func TestHelperTurn_AlreadyPendingWaitThatEndsBeforeExitIsStillAYield(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var (
		turns     atomic.Int32
		spawned   AttachAndSendResult
		known     = make(chan struct{})
		started   = make(chan agentcomm.WaitID, 1)
		repeated  = make(chan helperTurnOutcome, 1)
		turn1Done = make(chan helperTurnOutcome, 1)
	)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		switch turns.Add(1) {
		case 1: // asks the stage and yields on the answer
			awaitRelease(t, known)
			var out helperTurnOutcome
			out.callback, out.err = sendHelperCallback(o, spawned, spawnStage, "op-race-callback")
			if out.err == nil {
				out.wait, out.err = waitReplyTo(o, "op-race-wait", spawned.ParticipantID, out.callback)
			}
			turn1Done <- out
			return executor.HelperOutcome{ProcessOK: true}, nil
		case 2: // woken by another message: the first wait is pending, repeats it, then the answer lands, then exits empty
			first := <-started
			var out helperTurnOutcome
			out.wait, out.err = waitReplyTo(o, "op-race-wait-again", spawned.ParticipantID, firstCallback(o, spawned))
			if out.err == nil && (!out.wait.AlreadyPending || out.wait.WaitID != first) {
				out.err = fmt.Errorf("repeated wait = %+v, want already pending on %s", out.wait, first)
			}
			if out.err == nil {
				out.err = stageAnswers(o, spawned, "op-race-answer", firstCallback(o, spawned))
			}
			repeated <- out
			return executor.HelperOutcome{ProcessOK: true}, nil
		default:
			return passResultOutcome(), nil
		}
	}

	spawned = mustSpawn(t, o, "op-race-spawn", "review the change")
	close(known)
	out := receiveHelperTurn(t, turn1Done)
	waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
	started <- out.wait.WaitID

	if _, err := o.Send(context.Background(), SendRequest{
		OperationID: "op-race-nudge", ThreadID: spawned.ThreadID, Sender: spawnStage, Recipients: []agentcomm.ParticipantID{spawned.ParticipantID},
		Kind: agentcomm.MessageKindMessage, Body: "also check the docs", Actor: spawnStageActor,
	}); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	receiveHelperTurn(t, repeated)

	task := waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusCompleted, 5*time.Second)
	if turns.Load() != 3 {
		t.Fatalf("helper turns = %d, want 3 (ask, repeated wait, result after the answer)", turns.Load())
	}
	if task.StatusReason != "" {
		t.Fatalf("helper reason = %q, want a clean completion", task.StatusReason)
	}
	o.concurrency.WaitAgents()
}

// firstCallback is the one request the helper sent to the stage.
func firstCallback(o *Orchestrator, h AttachAndSendResult) agentcomm.MessageID {
	for id, m := range o.opts.Store.Snapshot().Assistants.Mesh.Messages {
		if m.Sender == h.ParticipantID && m.Kind == agentcomm.MessageKindRequest {
			return id
		}
	}
	return ""
}

// After a restart the journal replays the stage's wait and the helper's callback wait: a second callback wait is
// still admitted, and a repeated stage wait, once the stage has answered, is still the first wait.
func TestWait_AdmissionAndDedupSurviveARestart(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)

	var (
		turns   atomic.Int32
		spawned AttachAndSendResult
		known   = make(chan struct{})
		turn1   = make(chan helperTurnOutcome, 1)
		release = make(chan struct{})
	)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if turns.Add(1) > 1 {
			awaitRelease(t, release)
			return passResultOutcome(), nil
		}
		awaitRelease(t, known)
		var out helperTurnOutcome
		out.callback, out.err = sendHelperCallback(o, spawned, spawnStage, "op-restart-callback")
		if out.err == nil {
			out.wait, out.err = waitReplyTo(o, "op-restart-helper-wait", spawned.ParticipantID, out.callback)
		}
		turn1 <- out
		return executor.HelperOutcome{ProcessOK: true}, nil
	}
	spawned = mustSpawn(t, o, "op-restart-spawn", "review the change")
	close(known)
	first, err := waitReplyTo(o, "op-restart-stage-wait", spawnStage, *spawned.MessageID)
	if err != nil {
		t.Fatalf("stage wait: %v", err)
	}
	out := receiveHelperTurn(t, turn1)
	waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
	o.concurrency.WaitAgents()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, resumedStore := reopenAssistantsTestOrchestrator(t, o)
	callback2, err := resumed.Send(context.Background(), SendRequest{
		OperationID: "op-restart-callback-2", ThreadID: spawned.ThreadID, Sender: spawned.ParticipantID, Recipients: []agentcomm.ParticipantID{spawnStage},
		Kind: agentcomm.MessageKindRequest, Body: "and the tests?", Actor: helperActor(spawned.ParticipantID),
	})
	if err != nil {
		t.Fatalf("second callback: %v", err)
	}
	if _, err := waitReplyTo(resumed, "op-restart-helper-wait-2", spawned.ParticipantID, callback2.MessageID); err != nil {
		t.Fatalf("second callback wait after the restart was refused: %v", err)
	}
	_, err = waitReplyTo(resumed, "op-restart-stage-again", spawnStage, *spawned.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)

	for i, callback := range []agentcomm.MessageID{out.callback, callback2.MessageID} {
		if err := stageAnswers(resumed, spawned, fmt.Sprintf("op-restart-answer-%d", i), callback); err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
	}
	again, err := waitReplyTo(resumed, "op-restart-stage-again-2", spawnStage, *spawned.MessageID)
	if err != nil || again.WaitID != first.WaitID || !again.AlreadyPending {
		t.Fatalf("repeated stage wait after the restart = %+v, %v; want the first wait %s, already pending", again, err, first.WaitID)
	}
	if n := len(resumedStore.Snapshot().Assistants.Mesh.WaitOrder); n != 3 {
		t.Fatalf("waits = %d, want 3 (stage, two callback waits)", n)
	}
	close(release)
	resumed.concurrency.WaitAgents()
}

// A helper that delegated waits for its children, which no callback turn of the stage can resolve: the stage's edge
// to it stays, so a child waiting for the stage to finish is a cycle — also after the helper's callback was answered.
func TestWait_ChildOfDelegatingHelperWaitingForStageDoneIsACycle(t *testing.T) {
	for _, afterCallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("after callback %v", afterCallback), func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			var (
				spawned    AttachAndSendResult
				helperTurn atomic.Int32
				delegated  atomic.Bool
				callbackCh = make(chan helperTurnOutcome, 1)
				gate       = make(chan struct{})
				release    = make(chan struct{})
			)
			o.runHelperAgent = func(ctx context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
				if assistants.TaskID(taskID) != spawned.TaskID {
					select {
					case <-release:
						return passResultOutcome(), nil
					case <-ctx.Done():
						return executor.HelperOutcome{}, ctx.Err()
					}
				}
				awaitRelease(t, gate)
				turn := helperTurn.Add(1)
				if afterCallback && turn == 1 {
					var out helperTurnOutcome
					out.callback, out.err = sendHelperCallback(o, spawned, spawnStage, "op-del-callback")
					if out.err == nil {
						out.wait, out.err = waitReplyTo(o, "op-del-helper-wait", spawned.ParticipantID, out.callback)
					}
					callbackCh <- out
					return executor.HelperOutcome{ProcessOK: true}, nil
				}
				// Delegating once is the scenario; the helper resumed after its child finished ends normally.
				if !delegated.CompareAndSwap(false, true) {
					return passResultOutcome(), nil
				}
				return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
			}

			spawned = mustSpawn(t, o, "op-del-spawn", "review the change")
			if _, err := waitReplyTo(o, "op-del-stage-wait", spawnStage, *spawned.MessageID); err != nil {
				t.Fatalf("stage wait: %v", err)
			}
			close(gate)
			if afterCallback {
				out := receiveHelperTurn(t, callbackCh)
				waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusWaitingPeer, 2*time.Second)
				if err := stageAnswers(o, spawned, "op-del-answer", out.callback); err != nil {
					t.Fatalf("answer: %v", err)
				}
			}
			waitForTaskStatus(t, store, spawned.TaskID, assistants.StatusWaitingChildren, 3*time.Second)

			rs := store.Snapshot()
			var child *state.TaskState
			for _, tk := range rs.Assistants.Tasks {
				if tk.ParentTaskID != nil && *tk.ParentTaskID == spawned.TaskID {
					child = tk
				}
			}
			if child == nil {
				t.Fatal("the delegating helper has no child")
			}
			childParticipant := meshParticipantForTask(rs, child.Task)
			_, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-del-child-wait", Waiter: childParticipant, Actor: helperActor(childParticipant),
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(state.StatusDone)},
			})
			close(release)
			requireRejected(t, err, agentcomm.CodeWaitCycle)
			o.concurrency.WaitAgents()
		})
	}
}

// Pinned limit: the stage read the helper's callback, left it unanswered and parked again. The helper's wait for
// the answer is still admitted, because the exemption does not depend on delivery state. That is the stage
// ignoring its own callback, and its next repeat of the wait is the clear wait_cycle.
func TestWait_HelperWaitForACallbackTheStageAlreadyReadIsAdmitted(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-read-spawn", "review the change")
	if _, err := waitReplyTo(o, "op-read-stage-wait", spawnStage, *h.MessageID); err != nil {
		t.Fatalf("stage wait: %v", err)
	}
	callback := helperCallback(t, o, h, "op-read-callback")
	if err := o.MarkMessageDelivered(context.Background(), callback, spawnStage); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	if _, err := waitReplyTo(o, "op-read-helper-wait", h.ParticipantID, callback); err != nil {
		t.Fatalf("helper wait for the read callback: %v", err)
	}

	_, err := waitReplyTo(o, "op-read-stage-again", spawnStage, *h.MessageID)
	requireRejected(t, err, agentcomm.CodeWaitCycle)
}
