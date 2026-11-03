package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// An on_completion helper starts only after its stage has produced its result, so a stage agent that waits for it
// before finishing its own work waits for a start its own wait prevents: admission refuses such a wait.

// completionWaitFixture is the running test stage with one on_completion helper attached to it. Helper turns are
// counted and held, so a test that breaks the rule cannot start a real process.
type completionWaitFixture struct {
	o      *Orchestrator
	store  *state.Store
	turns  *turnCounter
	helper assistants.TaskID
}

func newCompletionWaitFixture(t *testing.T, mode assistants.Mode, extraStages ...flow.Stage) *completionWaitFixture {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true, extraStages...)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, make(chan struct{}))
	helper := attachWatcherTask(t, o, "op-completion-helper", assistantsTestStageID, assistants.TriggerOnCompletion, mode)
	return &completionWaitFixture{o: o, store: store, turns: turns, helper: helper}
}

func (f *completionWaitFixture) helperID() agentcomm.ParticipantID {
	return meshHelperParticipantID(f.helper)
}

// requestTo sends a request from the stage to to and reports the message and its thread.
func (f *completionWaitFixture) requestTo(t *testing.T, opID string, to agentcomm.ParticipantID) SendReceipt {
	t.Helper()
	sent, err := agentSend(f.o, opID, string(spawnStage), []string{string(to)}, "ready?")
	if err != nil {
		t.Fatalf("send %s: %v", opID, err)
	}
	return sent
}

func (f *completionWaitFixture) wait(opID string, waiter agentcomm.ParticipantID, cond agentcomm.WaitCondition) (WaitReceipt, error) {
	return f.o.Wait(context.Background(), WaitRequest{OperationID: opID, Waiter: waiter, Actor: helperActor(waiter), Condition: cond})
}

func (f *completionWaitFixture) waitFor(opID string, waiter agentcomm.ParticipantID, cond agentcomm.WaitCondition) error {
	_, err := f.wait(opID, waiter, cond)
	return err
}

func taskResultOf(id assistants.TaskID) agentcomm.WaitCondition {
	return agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: string(id)}
}

// cycleReason is the reason of a typed wait_cycle rejection; any other error fails the test.
func cycleReason(t *testing.T, err error) string {
	t.Helper()
	requireRejected(t, err, agentcomm.CodeWaitCycle)
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want a rejection", err)
	}
	return rej.Reason
}

// requireNothingRegistered fails if a wait was recorded or the stage left its status.
func requireNothingRegistered(t *testing.T, store *state.Store, want state.StageStatus) {
	t.Helper()
	rs := store.Snapshot()
	if n := len(rs.Assistants.Mesh.Waits); n != 0 {
		t.Fatalf("waits recorded = %d, want none", n)
	}
	if got := rs.Stages[assistantsTestStageID].Status; got != want {
		t.Fatalf("stage status = %s, want %s", got, want)
	}
}

// The stage's own wait on its not yet started on_completion helper is refused whatever its shape and the helper's
// mode, before anything is recorded.
func TestWaitCompletion_OwnHelperIsACycle(t *testing.T) {
	shapes := map[string]func(t *testing.T, f *completionWaitFixture) error{
		"task_result": func(t *testing.T, f *completionWaitFixture) error {
			return f.waitFor("op-wait", spawnStage, taskResultOf(f.helper))
		},
		"reply_to": func(t *testing.T, f *completionWaitFixture) error {
			sent := f.requestTo(t, "op-ask", f.helperID())
			return f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID})
		},
		"addressed_message in a thread with the helper alone": func(t *testing.T, f *completionWaitFixture) error {
			thread := openThread(t, f.o, "op-thread", 0, spawnStage, f.helperID())
			return f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread})
		},
		"batch item": func(t *testing.T, f *completionWaitFixture) error {
			sent := f.requestTo(t, "op-ask", f.helperID())
			_, err := f.o.Wait(context.Background(), WaitRequest{
				OperationID: "op-wait", Waiter: spawnStage, Actor: spawnStageActor,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{sent.MessageID},
			})
			return err
		},
	}
	for name, wait := range shapes {
		for _, mode := range []assistants.Mode{assistants.ModeRequired, assistants.ModeAdvisory} {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				f := newCompletionWaitFixture(t, mode)

				err := wait(t, f)

				cycleReason(t, err)
				requireNothingRegistered(t, f.store, state.StatusRunning)
				if got := f.turns.of(f.helper); got != 0 {
					t.Fatalf("helper turns = %d, want none", got)
				}
			})
		}
	}
}

// A batch is refused as a whole when one of its requests goes to the stage's own helper, even if another goes to
// someone who can answer.
func TestWaitCompletion_BatchWithOneOwnHelperRequestIsRefused(t *testing.T) {
	f := newCompletionWaitFixture(t, assistants.ModeRequired)
	qa := addTestRunAgent(t, f.o, "op-qa", nil).ParticipantID
	free := f.requestTo(t, "op-ask-qa", qa)
	blocked := f.requestTo(t, "op-ask-helper", f.helperID())

	_, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{free.MessageID, blocked.MessageID},
	})

	cycleReason(t, err)
	requireNothingRegistered(t, f.store, state.StatusRunning)
}

// The cycle is found in the wait graph, not by comparing a helper with its stage: the stage waits for someone who
// itself waits for the stage's helper.
func TestWaitCompletion_CycleThroughIntermediary(t *testing.T) {
	t.Run("another helper", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		x := newRunningHelperForMesh(t, f.o, f.store, "op-x")
		if err := f.waitFor("op-x-waits", meshHelperParticipantID(x), taskResultOf(f.helper)); err != nil {
			t.Fatalf("the other helper's wait for the completion helper blocks nothing yet, but was refused: %v", err)
		}
		t.Cleanup(func() { // the helper must end for the fixture's own cleanup
			for _, id := range pendingWaitsOf(f.store.Snapshot().Assistants.Mesh, meshHelperParticipantID(x)) {
				_ = f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cleanup-" + string(id), WaitID: id, Actor: helperActor(meshHelperParticipantID(x))})
			}
		})
		ask := f.requestTo(t, "op-ask-x", meshHelperParticipantID(x))

		err := f.waitFor("op-stage-waits", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: ask.MessageID})

		cycleReason(t, err)
		if n := len(pendingWaitsOf(f.store.Snapshot().Assistants.Mesh, spawnStage)); n != 0 {
			t.Fatalf("pending waits of the stage = %d, want none", n)
		}
	})
	t.Run("another stage", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeAdvisory, flow.Stage{ID: "other", Command: "claude"})
		activateStage(t, f.o, "other")
		if err := f.waitFor("op-other-waits", meshStageParticipantID("other"), taskResultOf(f.helper)); err != nil {
			t.Fatalf("the other stage's wait for the completion helper blocks nothing yet, but was refused: %v", err)
		}

		err := waitForStageDone(f.o, "op-stage-waits", spawnStage, "other")

		cycleReason(t, err)
	})
}

// A completion helper of a stage that cannot finish before the waiter does is no escape either: the waiter is one
// of the unfinished depends_on of that stage.
func TestWaitCompletion_DependentStageHelper(t *testing.T) {
	docs := flow.Stage{ID: "docs", Command: "claude", DependsOn: []string{assistantsTestStageID}}
	attachToDocs := func(t *testing.T, o *Orchestrator) assistants.TaskID {
		return attachWatcherTask(t, o, "op-docs-helper", "docs", assistants.TriggerOnCompletion, assistants.ModeRequired)
	}
	t.Run("pending dependent", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true, docs)
		activateStage(t, o, assistantsTestStageID)
		countTurns(t, o, make(chan struct{}))
		helper := attachToDocs(t, o)

		err := (&completionWaitFixture{o: o, store: store}).waitFor("op-wait", spawnStage, taskResultOf(helper))

		cycleReason(t, err)
	})
	t.Run("dependent that plans eagerly", func(t *testing.T) {
		eager := eagerDocs(flow.AgentPlanning, flow.AgentImplementation)
		o, store := newAssistantsTestOrchestrator(t, true, eager)
		activateStage(t, o, assistantsTestStageID)
		countTurns(t, o, make(chan struct{}))
		if _, ok := o.Trigger("docs", bus.EvStartPlanning, bus.GuardCtx{Stage: eager}, ""); !ok {
			t.Fatal("setup: planning refused")
		}
		helper := attachToDocs(t, o)

		err := (&completionWaitFixture{o: o, store: store}).waitFor("op-wait", spawnStage, taskResultOf(helper))

		cycleReason(t, err)
	})
	t.Run("dependency added at run time", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "docs", Command: "claude"})
		activateStage(t, o, assistantsTestStageID)
		activateStage(t, o, "docs")
		countTurns(t, o, make(chan struct{}))
		helper := attachToDocs(t, o)
		o.graph.Replace([]flow.Stage{{ID: assistantsTestStageID, Command: "claude"}, docs})

		err := (&completionWaitFixture{o: o, store: store}).waitFor("op-wait", spawnStage, taskResultOf(helper))

		cycleReason(t, err)
	})
}

// What is not a cycle stays admitted: the helper's pass can already run, or the waiter has another way out.
func TestWaitCompletion_NoFalseCycle(t *testing.T) {
	t.Run("stage already has its result", func(t *testing.T) {
		for name, events := range map[string][]bus.FSMEvent{
			"awaiting_assistants":             {bus.EvAwaitAssistants},
			"done":                            {bus.EvComplete},
			"paused from awaiting_assistants": {bus.EvAwaitAssistants, bus.EvPause},
		} {
			t.Run(name, func(t *testing.T) {
				f := newCompletionWaitFixture(t, assistants.ModeRequired)
				for _, ev := range events {
					commitStageEvent(t, f.o, assistantsTestStageID, ev)
				}

				if err := f.waitFor("op-wait", spawnStage, taskResultOf(f.helper)); err != nil {
					t.Fatalf("wait for the helper of a stage that has its result: %v", err)
				}
			})
		}
	})
	t.Run("a stage paused before its result still waits for nothing", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		commitStageEvent(t, f.o, assistantsTestStageID, bus.EvPause)

		cycleReason(t, f.waitFor("op-wait", spawnStage, taskResultOf(f.helper)))
	})
	t.Run("helper already started", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeAdvisory)
		commitTaskStatus(t, f.store, f.helper, assistants.StatusRunning)

		if err := f.waitFor("op-wait", spawnStage, taskResultOf(f.helper)); err != nil {
			t.Fatalf("wait for a running helper: %v", err)
		}
	})
	t.Run("helper of an independent stage", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "other", Command: "claude"})
		activateStage(t, o, assistantsTestStageID)
		activateStage(t, o, "other")
		countTurns(t, o, make(chan struct{}))
		helper := attachWatcherTask(t, o, "op-other-helper", "other", assistants.TriggerOnCompletion, assistants.ModeRequired)

		if err := (&completionWaitFixture{o: o, store: store}).waitFor("op-wait", spawnStage, taskResultOf(helper)); err != nil {
			t.Fatalf("wait for the helper of another stage: %v", err)
		}
	})
	t.Run("another recipient can answer", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		qa := addTestRunAgent(t, f.o, "op-qa", nil).ParticipantID
		sent, err := agentSend(f.o, "op-ask-both", string(spawnStage), []string{string(f.helperID()), string(qa)}, "who is ready?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		err = f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID})

		if err != nil {
			t.Fatalf("wait for an answer a run agent can give: %v", err)
		}
	})
	t.Run("checkpoints watcher and one_shot helper are not gated", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		watcher := attachWatcherTask(t, f.o, "op-checkpoints", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)
		oneShot := attachWatcherTask(t, f.o, "op-one-shot", assistantsTestStageID, assistants.TriggerOneShot, assistants.ModeAdvisory)

		for _, id := range []assistants.TaskID{watcher, oneShot} {
			if err := f.waitFor("op-wait-"+string(id), spawnStage, taskResultOf(id)); err != nil {
				t.Fatalf("wait for task %s: %v", id, err)
			}
		}
	})
	t.Run("callback of a one-shot helper while a completion helper waits for the stage", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		activateStage(t, o, assistantsTestStageID)
		gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
		attachWatcherTask(t, o, "op-completion-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
		h := mustSpawn(t, o, "op-spawn", "review the change")
		if _, err := waitReplyTo(o, "op-stage-waits", spawnStage, *h.MessageID); err != nil {
			t.Fatalf("stage wait for its one-shot helper: %v", err)
		}
		callback := helperCallback(t, o, h, "op-callback")

		if _, err := waitReplyTo(o, "op-helper-waits", h.ParticipantID, callback); err != nil {
			t.Fatalf("the one-shot helper's wait for the stage's answer: %v", err)
		}
		gates.release(h.TaskID)
	})
}

// A restarted run decides the same: the decision depends only on the journal and the stage list.
func TestWaitCompletion_ReplayParity(t *testing.T) {
	f := newCompletionWaitFixture(t, assistants.ModeRequired)
	cycleReason(t, f.waitFor("op-before-restart", spawnStage, taskResultOf(f.helper)))
	live := buildMeshWaitGraph(f.store.Snapshot(), nil, nil)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, resumedStore := reopenAssistantsTestOrchestrator(t, f.o)
	replayed := buildMeshWaitGraph(resumedStore.Snapshot(), nil, nil)

	if len(live[f.helperID()]) != 1 || len(replayed[f.helperID()]) != len(live[f.helperID()]) {
		t.Fatalf("helper edges: live %d, replayed %d, want one in both", len(live[f.helperID()]), len(replayed[f.helperID()]))
	}
	cycleReason(t, (&completionWaitFixture{o: resumed, store: resumedStore}).waitFor("op-after-restart", spawnStage, taskResultOf(f.helper)))
}

// The reason names the helper and its stage, says why the wait can not end, and tells the stage's own agent what
// to do; a cycle that has nothing to do with the start condition keeps the generic reason.
func TestWaitCompletion_SpecificReason(t *testing.T) {
	t.Run("stage waiting for its own helper", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)

		reason := cycleReason(t, f.waitFor("op-wait", spawnStage, taskResultOf(f.helper)))

		for _, want := range []string{string(f.helper), "on_completion", assistantsTestStageID, "finish"} {
			if !strings.Contains(reason, want) {
				t.Errorf("reason %q lacks %q", reason, want)
			}
		}
		if strings.Contains(reason, "everyone who could satisfy") {
			t.Errorf("reason %q is the generic one", reason)
		}
	})
	t.Run("only the helpers the cycle needs are named, in a stable order", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		second := attachWatcherTask(t, f.o, "op-second-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)

		first := cycleReason(t, f.waitFor("op-wait-1", spawnStage, taskResultOf(second)))
		again := cycleReason(t, f.waitFor("op-wait-2", spawnStage, taskResultOf(second)))

		if first != again {
			t.Errorf("reasons differ between identical waits: %q vs %q", first, again)
		}
		if !strings.Contains(first, string(second)) || strings.Contains(first, string(f.helper)) {
			t.Errorf("reason %q should name %s and not the unrelated helper %s", first, second, f.helper)
		}
	})
	t.Run("a waiter that is not the stage is not told to finish the stage's work", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		qa := addTestRunAgent(t, f.o, "op-qa", nil).ParticipantID
		ask := f.requestTo(t, "op-ask-qa", qa)
		if err := f.waitFor("op-stage-waits", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: ask.MessageID}); err != nil {
			t.Fatalf("stage wait for QA: %v", err)
		}

		reason := cycleReason(t, f.waitFor("op-qa-waits", qa, taskResultOf(f.helper)))

		if !strings.Contains(reason, string(f.helper)) || !strings.Contains(reason, assistantsTestStageID) {
			t.Errorf("reason %q should name the helper and its stage", reason)
		}
		if strings.Contains(reason, "finish") {
			t.Errorf("reason %q tells a run agent to finish the stage's work", reason)
		}
	})
	t.Run("a cycle without the start condition keeps the generic reason", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		foreign := meshHelperParticipantID(newRunningHelperForMesh(t, f.o, f.store, "op-foreign"))
		ask := f.requestTo(t, "op-ask-foreign", foreign)
		if err := f.waitFor("op-stage-waits", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: ask.MessageID}); err != nil {
			t.Fatalf("stage wait for the foreign helper: %v", err)
		}
		callback, err := f.o.Send(context.Background(), SendRequest{
			OperationID: "op-foreign-callback", ThreadID: ask.ThreadID, Sender: foreign, Recipients: []agentcomm.ParticipantID{spawnStage},
			Kind: agentcomm.MessageKindRequest, Body: "q", Actor: helperActor(foreign),
		})
		if err != nil {
			t.Fatalf("callback: %v", err)
		}

		reason := cycleReason(t, f.waitFor("op-foreign-waits", foreign, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: callback.MessageID}))

		if !strings.Contains(reason, "everyone who could satisfy") || strings.Contains(reason, "on_completion") {
			t.Errorf("reason %q should be the generic cycle reason", reason)
		}
	})
}

// The refusal costs the agent nothing but its wait: it finishes its work, AFM sees the result, starts the helper
// once, and the stage ends done.
func TestWaitCompletion_RejectedThenNormalCompletion(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	var helperTurns atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		helperTurns.Add(1)
		return passResultOutcome(), nil
	}
	var helper assistants.TaskID
	var waitErr error
	runner.turn = func(int32) error {
		id, err := o.Attach(context.Background(), AttachRequest{
			OperationID: "op-completion-helper", StageID: unreadTestStageID, ProfileID: "qa", Instruction: "check the result",
			Trigger: assistants.TriggerOnCompletion, Mode: assistants.ModeRequired, Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			return err
		}
		helper = id
		_, waitErr = o.Wait(context.Background(), WaitRequest{
			OperationID: "op-author-waits", Waiter: unreadTestStage, Actor: state.Actor{Kind: "agent", ID: string(unreadTestStage)},
			Condition: taskResultOf(id),
		})
		return nil
	}

	// A stage parked on the cyclic wait never ends the run: the timeout turns that into the failures below.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_ = o.Run(ctx)
	o.concurrency.WaitAgents()

	cycleReason(t, waitErr)
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	if got := helperTurns.Load(); got != 1 {
		t.Fatalf("helper turns = %d, want exactly 1", got)
	}
	if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusCompleted {
		t.Fatalf("helper status = %s, want completed", got)
	}
}

// A stage whose result never comes starts no helper, after the refused wait as before it.
func TestWaitCompletion_RejectedWaitThenFailureStartsNoHelper(t *testing.T) {
	f := newCompletionWaitFixture(t, assistants.ModeRequired)
	cycleReason(t, f.waitFor("op-wait", spawnStage, taskResultOf(f.helper)))

	if _, ok := f.o.Trigger(assistantsTestStageID, bus.EvVerifyFail, bus.GuardCtx{}, "verify failed"); !ok {
		t.Fatal("EvVerifyFail refused")
	}
	f.o.issueCompletionPasses(context.Background(), assistantsTestStageID)

	f.o.concurrency.WaitAgents()
	if got := f.turns.of(f.helper); got != 0 {
		t.Fatalf("helper turns = %d, want none for a stage without a result", got)
	}
	if got := taskStartRecords(t, f.o, f.helper); len(got) != 0 {
		t.Fatalf("helper start recorded for a stage without a result: %q", got)
	}
}

// completionPassPending is true only for a completion helper of the stage's current attempt that has not started
// while its stage has no result — the one rule the wait graph and the cancellation of a failed stage's pass share.
func TestWaitCompletion_PassPendingPredicate(t *testing.T) {
	parent := assistants.TaskID("parent")
	stage := func(status, pausedFrom state.StageStatus) state.StageState {
		return state.StageState{Status: status, PausedFrom: pausedFrom}
	}
	helper := func(mutate func(*state.TaskState)) *state.TaskState {
		task := &state.TaskState{
			Task: assistants.Task{ID: "h", Scope: assistants.ScopeStage, Trigger: assistants.TriggerOnCompletion, RootStageID: "s", RootGeneration: 1, Status: assistants.StatusWatching},
		}
		mutate(task)
		return task
	}
	keep := func(*state.TaskState) {}
	for _, tc := range []struct {
		name  string
		stage state.StageState
		task  *state.TaskState
		want  bool
	}{
		{"watching, stage running", stage(state.StatusRunning, ""), helper(keep), true},
		{"queued", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Status = assistants.StatusQueued }), true},
		{"waiting for a pending parent", stage(state.StatusPending, ""), helper(func(t *state.TaskState) { t.Status = assistants.StatusWaitingParent }), true},
		{"stage failed", stage(state.StatusFailed, ""), helper(keep), true},
		{"stage paused from running", stage(state.StatusPaused, state.StatusRunning), helper(keep), true},
		{"stage running again after an earlier pause from awaiting_assistants", stage(state.StatusRunning, state.StatusAwaitingAssistants), helper(keep), true},
		{"stage awaiting_assistants", stage(state.StatusAwaitingAssistants, ""), helper(keep), false},
		{"stage done", stage(state.StatusDone, ""), helper(keep), false},
		{"stage paused from awaiting_assistants", stage(state.StatusPaused, state.StatusAwaitingAssistants), helper(keep), false},
		{"already running", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Status = assistants.StatusRunning }), false},
		{"waiting for a peer", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Status = assistants.StatusWaitingPeer }), false},
		{"finished", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Status = assistants.StatusCancelled }), false},
		{"older generation", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.RootGeneration = 0 }), false},
		{"delegated child", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.ParentTaskID = &parent }), false},
		{"run agent", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Scope = assistants.ScopeRun }), false},
		{"checkpoints watcher", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Trigger = assistants.TriggerCheckpoints }), false},
		{"one_shot helper", stage(state.StatusRunning, ""), helper(func(t *state.TaskState) { t.Trigger = assistants.TriggerOneShot }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := state.RunState{Stages: map[string]state.StageState{"s": tc.stage}}
			rs.Assistants.RootGenerations = map[string]uint64{"s": 1}

			if got := completionPassPending(rs, tc.task); got != tc.want {
				t.Fatalf("completionPassPending = %t, want %t", got, tc.want)
			}
		})
	}
}

// Someone who can no longer answer is no way out of a cycle: with the stage's request addressed to the gated
// helper and to a finished stage, only the helper could answer, and it needs the stage's result.
func TestWaitCompletion_FinishedAlternativeDoesNotHideTheCycle(t *testing.T) {
	t.Run("reply_to", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired, flow.Stage{ID: "other", Command: "claude"})
		activateStage(t, f.o, "other")
		sent, err := agentSend(f.o, "op-ask-both", string(spawnStage), []string{string(f.helperID()), "stage:other"}, "who can answer?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		finishStage(t, f.o, "other")

		err = f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID})

		cycleReason(t, err)
		requireNothingRegistered(t, f.store, state.StatusRunning)
	})
	t.Run("addressed_message", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeAdvisory, flow.Stage{ID: "other", Command: "claude"})
		activateStage(t, f.o, "other")
		thread := openThread(t, f.o, "op-thread", 0, spawnStage, f.helperID(), "stage:other")
		finishStage(t, f.o, "other")

		err := f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread})

		cycleReason(t, err)
		requireNothingRegistered(t, f.store, state.StatusRunning)
	})
	t.Run("another recipient that can still answer", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired, flow.Stage{ID: "other", Command: "claude"})
		activateStage(t, f.o, "other")
		qa := addTestRunAgent(t, f.o, "op-qa", nil).ParticipantID
		sent, err := agentSend(f.o, "op-ask-all", string(spawnStage), []string{string(f.helperID()), "stage:other", string(qa)}, "who can answer?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		finishStage(t, f.o, "other")

		err = f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID})

		if err != nil {
			t.Fatalf("wait with a run agent that can answer: %v", err)
		}
	})
}

// The stage's wait for the gated helper and its own one-shot helper keeps the callback escape: the one-shot helper
// can wake the stage with a request, so its own wait for the stage's answer is no deadlock.
func TestWaitCompletion_CallbackOfOneShotHelperBesideCompletionHelper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	completion := attachWatcherTask(t, o, "op-completion-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	h := mustSpawn(t, o, "op-spawn", "review the change")
	defer gates.release(h.TaskID)
	sent, err := agentSend(o, "op-ask-both", string(spawnStage), []string{string(meshHelperParticipantID(completion)), string(h.ParticipantID)}, "who is ready?")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	stageWait, err := waitReplyTo(o, "op-stage-waits", spawnStage, sent.MessageID)
	if err != nil || stageWait.Status != agentcomm.WaitPending {
		t.Fatalf("stage wait = %+v, err = %v, want pending", stageWait, err)
	}
	park := bus.GuardCtx{Phase: phaseImplementation, PeerWaits: []agentcomm.WaitID{stageWait.WaitID}}
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvAwaitPeer, park, "park on the wait"); !ok {
		t.Fatal("setup: parking refused")
	}
	callback := helperCallback(t, o, h, "op-callback")
	if ev, resumes := peerContinuation(store.Snapshot(), assistantsTestStageID, ""); !resumes || ev != bus.EventPeerQuestion {
		t.Fatalf("setup: callback does not wake the stage: %q %t", ev, resumes)
	}

	if _, err := waitReplyTo(o, "op-helper-waits", h.ParticipantID, callback); err != nil {
		t.Fatalf("the one-shot helper's wait for the stage's answer: %v", err)
	}
}

// The callback escape holds only while the stage waits for nobody but its own one-shot helpers: a foreign recipient
// keeps the stage's edge, with or without a gated completion helper beside it, and a wait recorded on the gated
// helper alone keeps it too. A foreign agent that then waits for the stage closes the cycle.
func TestWaitCompletion_CallbackEscapeOnlyForOwnHelpers(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		askCompletion, askOwn, askForeign bool
	}{
		{name: "own one-shot helper and a foreign agent", askOwn: true, askForeign: true},
		{name: "completion helper, own one-shot helper and a foreign agent", askCompletion: true, askOwn: true, askForeign: true},
		{name: "completion helper alone", askCompletion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
			completion := attachWatcherTask(t, o, "op-completion-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
			own := mustSpawn(t, o, "op-spawn", "review the change")
			defer gates.release(own.TaskID)
			foreign := addTestRunAgent(t, o, "op-foreign", nil).ParticipantID
			var to []string
			if tc.askCompletion {
				to = append(to, string(meshHelperParticipantID(completion)))
			}
			if tc.askOwn {
				to = append(to, string(own.ParticipantID))
			}
			if tc.askForeign {
				to = append(to, string(foreign))
			}
			sent, err := agentSend(o, "op-ask", string(spawnStage), to, "who is ready?")
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			_, err = waitReplyTo(o, "op-stage-waits", spawnStage, sent.MessageID)
			if onlyGated := !tc.askOwn && !tc.askForeign; onlyGated {
				cycleReason(t, err)
				// What an older afm could have recorded: the same wait without admission.
				recordWaitWithoutAdmission(t, store, "op-legacy-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID})
			} else if err != nil {
				t.Fatalf("stage wait: %v", err)
			}
			if tc.askOwn {
				callback := helperCallback(t, o, own, "op-callback")
				if _, err := waitReplyTo(o, "op-own-waits", own.ParticipantID, callback); err != nil {
					t.Fatalf("the one-shot helper's wait for the stage's answer: %v", err)
				}
			}

			_, err = o.Wait(context.Background(), WaitRequest{
				OperationID: "op-foreign-waits", Waiter: foreign, Actor: helperActor(foreign),
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(state.StatusDone)},
			})

			cycleReason(t, err)
		})
	}
}

// recordWaitWithoutAdmission commits a pending wait as an afm without the admission check would have.
func recordWaitWithoutAdmission(t *testing.T, store *state.Store, opID string, waiter agentcomm.ParticipantID, cond agentcomm.WaitCondition) {
	t.Helper()
	effect, err := marshalEffect("mesh_wait_registered", state.Target{WaitID: string(deriveMeshWaitID(opID))},
		state.MeshWaitRegisteredPayload{Waiter: string(waiter), Condition: cond})
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actor{Kind: "agent", ID: string(waiter)}
	if _, err := store.ApplyRunOperation(state.Operation{ID: opID, Kind: "mesh_wait", Actor: actor, Effects: []state.Effect{effect}}, nil); err != nil {
		t.Fatal(err)
	}
}

// A wait whose condition already holds is ready, not a cycle, even if its producer is again gated: the helper posted
// a message before its pass failed and the stage retried.
func TestWaitCompletion_SatisfiedAddressedMessageAfterHelperRetry(t *testing.T) {
	f := newCompletionWaitFixture(t, assistants.ModeRequired)
	commitStageEvent(t, f.o, assistantsTestStageID, bus.EvAwaitAssistants)
	commitTaskStatus(t, f.store, f.helper, assistants.StatusRunning)
	thread := openThread(t, f.o, "op-thread", 0, spawnStage, f.helperID())
	_, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "op-helper-message", ThreadID: thread, Sender: f.helperID(), Recipients: []agentcomm.ParticipantID{spawnStage},
		Kind: agentcomm.MessageKindMessage, Body: "recorded before the retry", Actor: helperActor(f.helperID()),
	})
	if err != nil {
		t.Fatalf("helper message: %v", err)
	}
	commitTaskStatus(t, f.store, f.helper, assistants.StatusFailed)
	commitStageEvent(t, f.o, assistantsTestStageID, bus.EvFail)
	commitStageEvent(t, f.o, assistantsTestStageID, bus.EvManualRetry)
	activateStage(t, f.o, assistantsTestStageID)
	if err := f.o.RetryTask(context.Background(), f.helper); err != nil {
		t.Fatalf("retry the helper: %v", err)
	}

	receipt, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-wait", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread},
	})

	if err != nil || receipt.Status != agentcomm.WaitReady {
		t.Fatalf("receipt = %+v, err = %v, want a ready wait", receipt, err)
	}
}

// A helper that is not waiting for its stage's result is no cycle, whatever its trigger says: it runs, waits for
// someone else, belongs to an old attempt, or is delegated work whose parent may wait for it.
func TestWaitCompletion_HelpersThatDoNotWaitForTheStageResult(t *testing.T) {
	waitsFor := func(t *testing.T, f *completionWaitFixture, id assistants.TaskID) WaitReceipt {
		t.Helper()
		receipt, err := f.wait("op-wait", spawnStage, taskResultOf(id))
		if err != nil {
			t.Fatalf("wait for the result of %s: %v", id, err)
		}
		return receipt
	}
	for _, status := range []assistants.TaskStatus{assistants.StatusWaitingPeer, assistants.StatusWaitingChildren} {
		t.Run("completion helper in "+string(status), func(t *testing.T) {
			f := newCompletionWaitFixture(t, assistants.ModeRequired)
			commitTaskStatus(t, f.store, f.helper, status)

			if got := waitsFor(t, f, f.helper).Status; got != agentcomm.WaitPending {
				t.Fatalf("receipt status = %s, want pending", got)
			}
		})
	}
	t.Run("completion helper of an earlier attempt", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		if err := f.o.BumpRootGeneration(assistantsTestStageID); err != nil {
			t.Fatalf("BumpRootGeneration: %v", err)
		}
		commitTaskStatus(t, f.store, f.helper, assistants.StatusQueued) // the new attempt cancelled it; make it due again

		if got := waitsFor(t, f, f.helper).Status; got != agentcomm.WaitPending {
			t.Fatalf("receipt status = %s, want pending", got)
		}
	})
	t.Run("delegated on_completion child", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		parent := string(attachTestTask(context.Background(), t, f.o, "op-parent"))
		child := deriveTaskID("op-child:0")
		created, err := taskCreatedEffect(child, assistantsTestStageID, state.TaskCreatedPayload{
			ParentTaskID: &parent, ProfileID: "reviewer", Title: "child", Instruction: "do it",
			Mode: "advisory", Trigger: string(assistants.TriggerOnCompletion), CreatedBy: "assistant",
		})
		if err != nil {
			t.Fatal(err)
		}
		quota, err := quotaReservedEffect(1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.ApplyRunOperation(state.Operation{ID: "op-child", Kind: "attach_children", Effects: []state.Effect{created, quota}}, nil); err != nil {
			t.Fatalf("commit child: %v", err)
		}
		commitTaskStatus(t, f.store, child, assistants.StatusQueued)

		if got := waitsFor(t, f, child).Status; got != agentcomm.WaitPending {
			t.Fatalf("receipt status = %s, want pending", got)
		}
	})
	t.Run("finished completion helper", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		commitTaskStatus(t, f.store, f.helper, assistants.StatusCompleted)

		if got := waitsFor(t, f, f.helper).Status; got != agentcomm.WaitReady {
			t.Fatalf("receipt status = %s, want ready", got)
		}
	})
	t.Run("request the helper already answered", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		ask := f.requestTo(t, "op-ask", f.helperID())
		commitTaskStatus(t, f.store, f.helper, assistants.StatusRunning)
		_, err := f.o.Send(context.Background(), SendRequest{
			OperationID: "op-answer", ThreadID: ask.ThreadID, Sender: f.helperID(), Recipients: []agentcomm.ParticipantID{spawnStage},
			Kind: agentcomm.MessageKindReply, InReplyTo: &ask.MessageID, Body: "done", Actor: helperActor(f.helperID()),
		})
		if err != nil {
			t.Fatalf("answer: %v", err)
		}
		commitTaskStatus(t, f.store, f.helper, assistants.StatusQueued)

		receipt, err := f.wait("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: ask.MessageID})

		if err != nil || receipt.Status != agentcomm.WaitReady {
			t.Fatalf("receipt = %+v, err = %v, want a ready wait", receipt, err)
		}
	})
}

// decision registers a wait and reports its outcome in a comparable form; an accepted pending wait is cancelled,
// so the same probe can be repeated on the same journal.
func (f *completionWaitFixture) decision(t *testing.T, opID string, waiter agentcomm.ParticipantID, cond agentcomm.WaitCondition) string {
	t.Helper()
	receipt, err := f.wait(opID, waiter, cond)
	if err != nil {
		return "rejected: " + cycleReason(t, err)
	}
	if receipt.Status == agentcomm.WaitPending {
		if err := f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: opID + "-cancel", WaitID: receipt.WaitID, Actor: helperActor(waiter)}); err != nil {
			t.Fatalf("cancel the probe: %v", err)
		}
	}
	return string(receipt.Status)
}

// What admission decides after a restart is what it decided before it: an accepted wait, and a refusal that needs a
// stage the journal added at run time.
func TestWaitCompletion_AdmissionDecisionSurvivesRestart(t *testing.T) {
	t.Run("accepted wait for a helper that is already working", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		commitTaskStatus(t, f.store, f.helper, assistants.StatusWaitingPeer)
		before := f.decision(t, "op-before-restart", spawnStage, taskResultOf(f.helper))
		if err := f.store.Close(); err != nil {
			t.Fatal(err)
		}

		resumed, resumedStore := reopenAssistantsTestOrchestrator(t, f.o)
		after := (&completionWaitFixture{o: resumed, store: resumedStore}).decision(t, "op-after-restart", spawnStage, taskResultOf(f.helper))

		if before != string(agentcomm.WaitPending) || after != before {
			t.Fatalf("decision before restart %q, after %q, want pending in both", before, after)
		}
	})
	t.Run("refused wait for the helper of a stage added at run time", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		docs := flow.Stage{ID: "docs", Command: "claude", DependsOn: []string{assistantsTestStageID}}
		_, err := f.store.AddStageToGraph(state.StageGraphOperation{
			ID:      "op-add-docs",
			Payload: state.StageGraphPayload{Stage: docs, Order: []string{assistantsTestStageID, "docs"}},
			Expect:  state.StageGraphExpect{GraphRevision: f.o.reg().Revision()},
		})
		if err != nil {
			t.Fatalf("add the stage: %v", err)
		}
		f.o.graph.Replace([]flow.Stage{{ID: assistantsTestStageID, Command: "claude"}, docs})
		helper := attachWatcherTask(t, f.o, "op-docs-helper", "docs", assistants.TriggerOnCompletion, assistants.ModeRequired)
		before := f.decision(t, "op-before-restart", spawnStage, taskResultOf(helper))
		if err := f.store.Close(); err != nil {
			t.Fatal(err)
		}

		resumed, resumedStore := reopenAssistantsTestOrchestrator(t, f.o)
		after := (&completionWaitFixture{o: resumed, store: resumedStore}).decision(t, "op-after-restart", spawnStage, taskResultOf(helper))

		if !strings.HasPrefix(before, "rejected: ") || after != before {
			t.Fatalf("decision before restart %q, after %q, want the same refusal", before, after)
		}
	})
}

// The reason names every helper the cycle needs: the wait's own targets first, then the others, each group by id.
func TestWaitCompletion_ReasonOrdersTheHelpersOfTheCycle(t *testing.T) {
	named := func(reason string, ids ...assistants.TaskID) []int {
		at := make([]int, len(ids))
		for i, id := range ids {
			at[i] = strings.Index(reason, string(id))
			if at[i] < 0 {
				t.Fatalf("reason %q lacks helper %s", reason, id)
			}
		}
		return at
	}
	t.Run("several helpers the request goes to", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		second := attachWatcherTask(t, f.o, "op-second-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		third := attachWatcherTask(t, f.o, "op-third-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		ids := []assistants.TaskID{f.helper, second, third}
		slices.Sort(ids)
		to := []string{string(meshHelperParticipantID(ids[2])), string(meshHelperParticipantID(ids[0])), string(meshHelperParticipantID(ids[1]))}
		sent, err := agentSend(f.o, "op-ask-all", string(spawnStage), to, "ready?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		reason := cycleReason(t, f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID}))

		if at := named(reason, ids...); !slices.IsSorted(at) {
			t.Fatalf("helpers %v are not named by id in %q", ids, reason)
		}
	})
	t.Run("a direct helper before one the cycle only reaches through another", func(t *testing.T) {
		f := newCompletionWaitFixture(t, assistants.ModeRequired)
		other := attachWatcherTask(t, f.o, "op-other-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		direct, indirect := f.helper, other
		if direct < indirect { // the order by id must differ from the order the cycle is reached in
			direct, indirect = indirect, direct
		}
		x := newRunningHelperForMesh(t, f.o, f.store, "op-x")
		if err := f.waitFor("op-x-waits", meshHelperParticipantID(x), taskResultOf(indirect)); err != nil {
			t.Fatalf("the helper's wait for the completion helper blocks nothing yet, but was refused: %v", err)
		}
		t.Cleanup(func() { // the helper must end for the fixture's own cleanup
			for _, id := range pendingWaitsOf(f.store.Snapshot().Assistants.Mesh, meshHelperParticipantID(x)) {
				_ = f.o.CancelWait(context.Background(), CancelWaitRequest{OperationID: "op-cleanup-" + string(id), WaitID: id, Actor: helperActor(meshHelperParticipantID(x))})
			}
		})
		sent, err := agentSend(f.o, "op-ask-both", string(spawnStage), []string{string(meshHelperParticipantID(direct)), string(meshHelperParticipantID(x))}, "ready?")
		if err != nil {
			t.Fatalf("send: %v", err)
		}

		reason := cycleReason(t, f.waitFor("op-wait", spawnStage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID}))

		if at := named(reason, direct, indirect); at[0] > at[1] {
			t.Fatalf("direct helper %s is not named before %s in %q", direct, indirect, reason)
		}
	})
}

// A refused batch stays refused when the agent sends it again, under the same operation id or a new one, and leaves
// no wait of its own behind.
func TestWaitCompletion_RefusedBatchStaysRefused(t *testing.T) {
	f := newCompletionWaitFixture(t, assistants.ModeRequired)
	qa := addTestRunAgent(t, f.o, "op-qa", nil).ParticipantID
	free := f.requestTo(t, "op-ask-qa", qa)
	blocked := f.requestTo(t, "op-ask-helper", f.helperID())
	batch := func(opID string) error {
		_, err := f.o.Wait(context.Background(), WaitRequest{
			OperationID: opID, Waiter: spawnStage, Actor: spawnStageActor,
			Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}, MessageIDs: []agentcomm.MessageID{free.MessageID, blocked.MessageID},
		})
		return err
	}

	for _, opID := range []string{"op-batch", "op-batch", "op-batch-again"} {
		cycleReason(t, batch(opID))
		requireNothingRegistered(t, f.store, state.StatusRunning)
	}
}

// perStageRunner counts the turns of each stage and ends each by declaring the stage done, as a real agent does.
type perStageRunner struct {
	mu     sync.Mutex
	turns  map[string]int
	onTurn func(stageID string, n int) error
}

func (r *perStageRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *perStageRunner) RunAgent(_ context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	stageID := filepath.Base(stageDir)
	r.mu.Lock()
	if r.turns == nil {
		r.turns = map[string]int{}
	}
	r.turns[stageID]++
	n := r.turns[stageID]
	r.mu.Unlock()
	if err := r.onTurn(stageID, n); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644)
}

func (r *perStageRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *perStageRunner) turnsOf(stageID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[stageID]
}

// A stage whose verification must pass first: its agent's wait for the helper is refused before anything is recorded.
// Passing verification starts the helper once, then the after-hook and the dependent stage once; failing it cancels
// the helper's pass, nothing follows, and the run ends by itself.
func TestWaitCompletion_HelperStartsOnlyAfterVerificationPasses(t *testing.T) {
	const author, dependent = "impl", "docs"
	type outcome struct {
		o                    *Orchestrator
		runner               *perStageRunner
		store                *state.Store
		helper               assistants.TaskID
		waitErr              error // the author's wait for the helper, in its first turn
		verifyRunsAtWait     int
		verifyRuns           int
		authorAtHelperStarts []state.StageStatus
		afterHooks           int
	}
	run := func(t *testing.T, verifyPasses bool) outcome {
		t.Helper()
		runDir := t.TempDir()
		marker := filepath.Join(t.TempDir(), "after-hook")
		stages := []flow.Stage{
			{
				ID: author, Agents: []flow.AgentType{flow.AgentAuto}, ScriptAfter: "echo ran >> " + marker,
				Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "check"}}},
			},
			{ID: dependent, Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{author}},
		}
		store, err := state.Open(runDir, []string{author, dependent})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
			t.Fatal(err)
		}
		registry, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "qa", Title: "QA", Command: "helper-cmd"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{author, dependent} {
			if err := os.MkdirAll(filepath.Join(runDir, id), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		runner := &perStageRunner{}
		var (
			o                  *Orchestrator
			helper             assistants.TaskID
			attachErr, waitErr error
			verifyRuns         atomic.Int32
			verifyRunsAtWait   int32
		)
		runner.onTurn = func(stageID string, n int) error {
			if stageID != author || n != 1 {
				return nil
			}
			helper, attachErr = o.Attach(context.Background(), AttachRequest{
				OperationID: "op-completion-helper", StageID: author, ProfileID: "qa", Instruction: "check the result",
				Trigger: assistants.TriggerOnCompletion, Mode: assistants.ModeRequired, Actor: state.Actor{Kind: "user"},
			})
			if attachErr != nil {
				return attachErr
			}
			verifyRunsAtWait = verifyRuns.Load()
			waiter := meshStageParticipantID(author)
			_, waitErr = o.Wait(context.Background(), WaitRequest{
				OperationID: "op-author-waits", Waiter: waiter, Actor: state.Actor{Kind: "agent", ID: string(waiter)}, Condition: taskResultOf(helper),
			})
			return nil
		}
		o = New(Options{
			RunDir: runDir, Store: store, Config: config.Default(), Runner: runner, Stages: stages,
			AssistantsEnabled: true, AssistantsRegistry: registry,
		})
		o.helperAccess = allowHelpersOS
		o.runVerifyShell = func(context.Context, string, string, string) (string, error) {
			verifyRuns.Add(1)
			if !verifyPasses {
				return "check failed", errors.New("exit status 1")
			}
			return "ok", nil
		}
		var mu sync.Mutex
		var authorAtHelperStarts []state.StageStatus
		o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
			mu.Lock()
			defer mu.Unlock()
			authorAtHelperStarts = append(authorAtHelperStarts, store.Get(author))
			return passResultOutcome(), nil
		}

		startRun(t, o, 15*time.Second).requireFinishedItself(t)
		o.concurrency.WaitAgents()
		if attachErr != nil {
			t.Fatalf("attach the helper: %v", attachErr)
		}
		hooks := 0
		if data, err := os.ReadFile(marker); err == nil {
			hooks = strings.Count(string(data), "ran")
		}
		mu.Lock()
		defer mu.Unlock()
		return outcome{
			o: o, runner: runner, store: store, helper: helper, waitErr: waitErr, verifyRunsAtWait: int(verifyRunsAtWait),
			verifyRuns: int(verifyRuns.Load()), authorAtHelperStarts: slices.Clone(authorAtHelperStarts), afterHooks: hooks,
		}
	}
	// The author's wait is refused with the completion reason: nothing recorded, verification not yet run, and the
	// stage never parked for a colleague.
	requireWaitRefused := func(t *testing.T, got outcome) {
		t.Helper()
		reason := cycleReason(t, got.waitErr)
		if !strings.Contains(reason, "on_completion") || !strings.Contains(reason, "finish this stage's own work") {
			t.Fatalf("reason = %q, want the completion helper named and the stage told to finish its own work", reason)
		}
		if got.verifyRunsAtWait != 0 {
			t.Fatalf("verification runs at the wait = %d, want none", got.verifyRunsAtWait)
		}
		if n := len(got.store.Snapshot().Assistants.Mesh.Waits); n != 0 {
			t.Fatalf("waits recorded = %d, want none", n)
		}
		if n := transitionsTo(t, got.store, author, state.StatusAwaitingPeer); n != 0 {
			t.Fatalf("stage %s parked in awaiting_peer %d time(s)", author, n)
		}
	}

	t.Run("verification passes", func(t *testing.T) {
		got := run(t, true)

		requireWaitRefused(t, got)
		if got.verifyRuns != 1 {
			t.Fatalf("verification runs = %d, want 1", got.verifyRuns)
		}
		if !slices.Equal(got.authorAtHelperStarts, []state.StageStatus{state.StatusAwaitingAssistants}) {
			t.Fatalf("stage %s at each helper start = %v, want one start while it awaits its assistants", author, got.authorAtHelperStarts)
		}
		if starts := taskStartRecords(t, got.o, got.helper); len(starts) != 1 {
			t.Fatalf("helper starts recorded = %q, want one", starts)
		}
		if status := got.store.Snapshot().Assistants.Tasks[got.helper].Status; status != assistants.StatusCompleted {
			t.Fatalf("helper status = %s, want completed", status)
		}
		if status := got.store.Get(author); status != state.StatusDone {
			t.Fatalf("stage %s status = %s, want done", author, status)
		}
		if got.afterHooks != 1 {
			t.Fatalf("after-hook runs = %d, want 1", got.afterHooks)
		}
		if turns, readied := got.runner.turnsOf(dependent), transitionsTo(t, got.store, dependent, state.StatusReady); turns != 1 || readied != 1 {
			t.Fatalf("dependent stage turns = %d, made ready %d time(s), want once each", turns, readied)
		}
	})
	t.Run("verification fails", func(t *testing.T) {
		got := run(t, false)

		requireWaitRefused(t, got)
		if got.verifyRuns < 1 || got.runner.turnsOf(author) < 1 {
			t.Fatalf("verification runs = %d, author turns = %d: the author must produce a result that verification rejects", got.verifyRuns, got.runner.turnsOf(author))
		}
		requirePassCancelled(t, got.o, nil, got.helper)
		if len(got.authorAtHelperStarts) != 0 {
			t.Fatalf("helper turns = %d, want none for a stage that failed verification", len(got.authorAtHelperStarts))
		}
		if status := got.store.Get(author); status != state.StatusFailed {
			t.Fatalf("stage %s status = %s, want failed", author, status)
		}
		if got.afterHooks != 0 {
			t.Fatalf("after-hook runs = %d, want none", got.afterHooks)
		}
		if turns, readied := got.runner.turnsOf(dependent), transitionsTo(t, got.store, dependent, state.StatusReady); turns != 0 || readied != 0 {
			t.Fatalf("dependent stage turns = %d, made ready %d time(s), want none", turns, readied)
		}
	})
}

// transitionsTo counts the journaled transitions of stageID into status.
func transitionsTo(t *testing.T, store *state.Store, stageID string, status state.StageStatus) int {
	t.Helper()
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tr := range history {
		if tr.StageID == stageID && tr.To == status {
			n++
		}
	}
	return n
}
