package orchestrator

import (
	"context"
	"fmt"
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
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/state"
)

// At the boundaries of its stage an on_message helper is closed by the system, never by a model call: an idle one
// when the stage is done, a required one whose last request passed once the stage awaits it. Accepted work finishes
// first, a paused stage starts nothing, and a restart picks every one of these up where the crash left it.

// verdictAnswer is a required helper's answer with verdict.
func verdictAnswer(verdict assistants.Verdict) executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: fmt.Sprintf(`{"schema_version":1,"kind":"result","verdict":%q,"summary":"%s"}`, verdict, verdict)}
}

// passEveryRequest is a required helper that passes whatever it is asked.
func passEveryRequest(context.Context, int) (executor.HelperOutcome, error) {
	return verdictAnswer(assistants.VerdictPass), nil
}

// finishMain is the stage's agent finishing its own work: the stage awaits its required helpers, or is done.
func (r *onMessageRig) finishMain() {
	r.t.Helper()
	r.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
}

func (r *onMessageRig) stageStatus() state.StageStatus {
	return r.store.Get(assistantsTestStageID)
}

// requireClosed fails unless the helper was closed by the system with no turn of its own after turns, keeping the
// verdict it had.
func (r *onMessageRig) requireClosed(turns int, verdict assistants.Verdict) {
	r.t.Helper()
	tk := waitForTaskStatus(r.t, r.store, r.task, assistants.StatusCompleted, 3*time.Second)
	if !strings.HasPrefix(tk.StatusReason, onMessageClosedReasonPrefix) {
		r.t.Fatalf("closed with reason %q, want it to say why the system closed it", tk.StatusReason)
	}
	if tk.LastVerdict != verdict {
		r.t.Fatalf("closed with verdict %q, want %q", tk.LastVerdict, verdict)
	}
	r.o.concurrency.WaitAgents()
	if got := r.turns(); got != turns {
		r.t.Fatalf("turns = %d, want %d: closing calls no model", got, turns)
	}
}

// refused fails unless a new request to the helper is refused as invalid_status.
func (r *onMessageRig) refused(id string) {
	r.t.Helper()
	_, err := r.o.Send(context.Background(), SendRequest{
		OperationID: "send-" + id, ThreadID: r.thread, Sender: r.stage, Recipients: []agentcomm.ParticipantID{r.helper},
		Kind: agentcomm.MessageKindRequest, Body: "request " + id,
	})
	assertRejected(r.t, err, assistants.CodeInvalidStatus)
}

// requireReplayAgrees fails unless replaying the journal gives what the live projection holds for the stage's
// attention and timers and for the helper's requests.
func (r *onMessageRig) requireReplayAgrees() {
	r.t.Helper()
	live := r.store.Snapshot()
	replayed, err := state.LoadRunState(r.o.opts.RunDir)
	if err != nil {
		r.t.Fatalf("LoadRunState: %v", err)
	}
	if l, p := live.Assistants.NeedsHumanDecision(assistantsTestStageID), replayed.Assistants.NeedsHumanDecision(assistantsTestStageID); l != p {
		r.t.Fatalf("NeedsHumanDecision live %v, replayed %v", l, p)
	}
	if l, p := live.IdleSince(), replayed.IdleSince(); (l == nil) != (p == nil) || (l != nil && !l.Equal(*p)) || live.IdleAccumulatedMs != replayed.IdleAccumulatedMs {
		r.t.Fatalf("idle live %v/%d ms, replayed %v/%d ms", l, live.IdleAccumulatedMs, p, replayed.IdleAccumulatedMs)
	}
	lt, pt := live.Assistants.Tasks[r.task], replayed.Assistants.Tasks[r.task]
	if lt.Status != pt.Status || lt.ActiveRequest != pt.ActiveRequest || len(lt.PendingRequests) != len(pt.PendingRequests) ||
		lt.LastRequest != pt.LastRequest || lt.LastOutcome != pt.LastOutcome || lt.LastVerdict != pt.LastVerdict {
		r.t.Fatalf("helper live %+v, replayed %+v", lt, pt)
	}
}

// A helper that never got a request is closed when its stage is done, with a reason and no result or verdict, and
// takes no request after that.
func TestOnMessage_StageDoneClosesAnUnusedHelper(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusDone {
		t.Fatalf("stage = %s, want done: an advisory helper does not hold it", got)
	}
	r.requireClosed(0, "")
	if len(r.results()) != 0 || len(taskStartRecords(t, r.o, r.task)) != 0 {
		t.Fatalf("closing recorded a result or a start: results %v", r.results())
	}
	r.refused("late")
	requireRunNotHeld(t, r.store)
	r.requireReplayAgrees()
}

// A helper that answered requests is closed the same way: its results stay history, closing adds none.
func TestOnMessage_StageDoneClosesAUsedHelperWithoutANewResult(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	m1, w1 := r.ask("m1")
	r.requireAnswered(m1, 1, "answer 1", w1)
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)

	r.finishMain()
	r.requireClosed(1, "")
	if tk := r.taskState(); tk.LastRequest != m1 || tk.LastOutcome != state.RequestOutcomeResult || len(r.results()) != 1 {
		t.Fatalf("closed helper: last %s/%s, %d results; want m1's result kept and no new one", tk.LastRequest, tk.LastOutcome, len(r.results()))
	}
}

// Work accepted before the stage was done is finished — the request under way and the one waiting behind it — and
// holds the run until then; a new request is refused at once, and the helper is closed once its queue is empty.
func TestOnMessage_AcceptedWorkFinishesAfterTheStageIsDone(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	release := make(chan struct{})
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, release)
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1 := r.send("m1")
	waitUntil(t, "turn 1 under way", func() bool { return r.turns() == 1 })
	m2 := r.send("m2")

	r.finishMain()
	if got := r.stageStatus(); got != state.StatusDone {
		t.Fatalf("stage = %s, want done", got)
	}
	r.refused("m3")
	if _, held := r.store.Snapshot().Assistants.RunBlocker(); !held {
		t.Fatal("accepted work does not hold the run")
	}
	close(release)

	for _, m := range []agentcomm.MessageID{m1, m2} {
		if got := r.waitOutcome(m); got != state.RequestOutcomeResult {
			t.Fatalf("%s ended %q, want result", m, got)
		}
	}
	r.requireClosed(2, "")
	requireRunNotHeld(t, r.store)
}

// A required helper with no request yet keeps its stage awaiting it and asks a person for a request, without a
// turn; the request it then passes closes it and completes the stage.
func TestOnMessage_RequiredHelperWithoutARequestWaitsForOne(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage = %s, want awaiting_assistants", got)
	}
	neverTrue(t, "a turn started without a request", func() bool { return r.turns() > 0 })
	if tk := r.taskState(); tk.Status != assistants.StatusWatching || !r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID) {
		t.Fatalf("helper = %s, decision needed %v; want it waiting for a request a person must give", tk.Status, r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID))
	}
	r.requireReplayAgrees()

	m1, _ := r.ask("m1")
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	r.requireClosed(1, assistants.VerdictPass)
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
	r.requireReplayAgrees()
}

// A pass given while the stage's agent still works closes nothing; once the stage awaits its helpers, the system
// closes the helper with that pass and the stage completes, with no further turn.
func TestOnMessage_RequiredPassClosesTheHelperWhenTheStageAwaitsIt(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	m1, _ := r.ask("m1")
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	pass := waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)

	r.finishMain()
	r.requireClosed(1, assistants.VerdictPass)
	if tk := r.taskState(); tk.ResultSeq != pass.ResultSeq {
		t.Fatalf("result seq %d, want the pass at %d kept", tk.ResultSeq, pass.ResultSeq)
	}
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
}

// needs_changes keeps the stage awaiting the helper with a decision asked for; the pass of a later request closes
// it.
func TestOnMessage_RequiredNeedsChangesThenPass(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			return verdictAnswer(assistants.VerdictNeedsChanges), nil
		}
		return verdictAnswer(assistants.VerdictPass), nil
	})
	m1, _ := r.ask("m1")
	r.waitOutcome(m1)
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage = %s, want awaiting_assistants after needs_changes", got)
	}
	neverTrue(t, "the helper was closed on needs_changes", func() bool { return r.taskState().Status.Terminal() })
	if !r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID) {
		t.Fatal("needs_changes asks no decision")
	}
	r.requireReplayAgrees()

	m2, _ := r.ask("m2")
	r.waitOutcome(m2)
	r.requireClosed(2, assistants.VerdictPass)
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
}

// A new request makes the earlier pass not enough: when that request fails, the stage awaits the helper and a
// decision, and the old pass closes nothing.
func TestOnMessage_RequiredPassThenFailedRequestKeepsTheGateOpen(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			return verdictAnswer(assistants.VerdictPass), nil
		}
		return onMessageAnswer("no verdict"), nil // a required helper must give one
	})
	m1, _ := r.ask("m1")
	r.waitOutcome(m1)
	m2, _ := r.ask("m2")
	if got := r.waitOutcome(m2); got != state.RequestOutcomeFailed {
		t.Fatalf("m2 ended %q, want failed", got)
	}
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)

	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage = %s, want awaiting_assistants: the last request failed", got)
	}
	neverTrue(t, "the old pass closed the helper", func() bool { return r.taskState().Status.Terminal() })
	if !r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID) {
		t.Fatal("a failed last request asks no decision")
	}
	r.requireReplayAgrees()
}

// A waiver completes the stage, and the idle required helper is then closed with it — no turn.
func TestOnMessage_WaiverCompletesTheStageAndClosesTheHelper(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage = %s, want awaiting_assistants", got)
	}
	if err := r.o.WaiveTask(context.Background(), r.task, state.Actor{Kind: string(assistants.OriginUser)}, "not needed"); err != nil {
		t.Fatalf("WaiveTask: %v", err)
	}
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
	r.requireClosed(0, "")
}

// A request that arrives while the system is about to close the helper is either taken — the close does not
// happen and the request gets its turn — or refused; it is never accepted and then dropped.
func TestOnMessage_RequestRacingTheCloseIsTakenOrRefused(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	m1, _ := r.ask("m1")
	r.waitOutcome(m1)
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)

	raced := make(chan agentcomm.MessageID, 1)
	var once sync.Once
	r.o.settlePlannedHook = func() {
		tk := r.taskState()
		if r.stageStatus() == state.StatusAwaitingAssistants && tk.Status == assistants.StatusWatching && state.OnMessageIdle(*tk) {
			once.Do(func() { raced <- r.send("m2") })
		}
	}
	r.finishMain()
	m2 := receive(t, raced)
	if m2 == "" {
		t.Fatal("the request racing the close was refused before the close committed")
	}
	if got := r.waitOutcome(m2); got != state.RequestOutcomeResult {
		t.Fatalf("m2 ended %q, want result: it was accepted, so it must be worked", got)
	}
	r.requireClosed(2, assistants.VerdictPass)
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
	r.refused("m3")
}

// A new attempt of the stage cancels the helper of the old one mid-request; the late answer of that turn changes
// nothing.
func TestOnMessage_NewStageAttemptCancelsTheHelperAndFencesItsLateResult(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	release := make(chan struct{})
	r.setScript(func(context.Context, int) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return verdictAnswer(assistants.VerdictPass), nil
	})
	m1 := r.send("m1")
	waitUntil(t, "turn 1 under way", func() bool { return r.turns() == 1 })
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants {
		t.Fatalf("stage = %s, want awaiting_assistants", got)
	}
	if err := r.o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	waitForTaskStatus(t, r.store, r.task, assistants.StatusCancelled, 2*time.Second)
	close(release)
	r.o.concurrency.WaitAgents()

	if got := r.outcome(m1); got != "" {
		t.Fatalf("m1 ended %q after the cancel, want no outcome", got)
	}
	if tk := r.taskState(); tk.Status != assistants.StatusCancelled || tk.LastVerdict != "" || len(r.results()) != 0 {
		t.Fatalf("old helper = %s verdict %q, %d results; want cancelled with nothing from the late turn", tk.Status, tk.LastVerdict, len(r.results()))
	}
	if blocker, open := r.store.Snapshot().Assistants.RequiredBlocker(assistantsTestStageID); open {
		t.Fatalf("the new attempt is blocked by %s", blocker.ID)
	}
}

// A failed stage cancels nothing: the request under way is finished and holds the run until then, a new request is
// refused, the idle helper does not hold the run, and a Retry of the stage lets it take requests again.
func TestOnMessage_FailedStageFinishesAcceptedWorkAndRetryReopens(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	release := make(chan struct{})
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, release)
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	var exitAllowed []bool
	var mu sync.Mutex
	r.o.exitDecidedHook = func(_ state.RunState, allowed bool) {
		mu.Lock()
		defer mu.Unlock()
		exitAllowed = append(exitAllowed, allowed)
	}
	lastExitDecision := func() bool {
		r.o.shouldExit()
		mu.Lock()
		defer mu.Unlock()
		return exitAllowed[len(exitAllowed)-1]
	}
	m1 := r.send("m1")
	waitUntil(t, "turn 1 under way", func() bool { return r.turns() == 1 })
	if _, ok := r.o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail refused")
	}
	r.refused("m2")
	if tk := r.taskState(); tk.Status != assistants.StatusRunning || tk.ActiveRequest != m1 {
		t.Fatalf("helper = %s active %q, want still working on m1", tk.Status, tk.ActiveRequest)
	}
	if lastExitDecision() {
		t.Fatal("the run may finish under a request still being worked")
	}
	close(release)
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)
	if !lastExitDecision() {
		t.Fatal("an idle helper of a failed stage holds the run")
	}

	if _, ok := r.o.Trigger(assistantsTestStageID, bus.EvManualRetry, bus.GuardCtx{}, "retry"); !ok {
		t.Fatal("EvManualRetry refused")
	}
	m3, w3 := r.ask("m3")
	r.requireAnswered(m3, 2, "answer 2", w3)
}

// While its stage is paused the helper keeps the requests it is sent and starts nothing; the Continue of the stage
// starts one turn per request.
func TestOnMessage_StagePauseHoldsRequestsUntilContinue(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	r.finishMain()
	ctx := context.Background()
	if err := r.o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	m1, _ := r.ask("m1")
	neverTrue(t, "a turn started while the stage is paused", func() bool { return r.turns() > 0 })
	if tk := r.taskState(); tk.Status.Terminal() || tk.ActiveRequest != "" || len(tk.PendingRequests) != 1 {
		t.Fatalf("helper = %s active %q pending %v, want m1 kept waiting", tk.Status, tk.ActiveRequest, tk.PendingRequests)
	}

	if err := r.o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	r.requireClosed(1, assistants.VerdictPass)
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
}

// A turn whose start committed but which waited for its command slot goes back to the queue when its stage paused
// meanwhile; the Continue starts it once.
func TestOnMessage_StagePausedWhileTheTurnWaitsForItsSlot(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	slot := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	slot <- struct{}{}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), map[string]concurrency.Semaphore{"helper-cmd": slot}, "claude")
	r := newOnMessageRigOn(t, o, store, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	r.finishMain()
	m1 := r.send("m1")
	waitForTaskStatus(t, store, r.task, assistants.StatusRunning, 2*time.Second)
	ctx := context.Background()
	if err := o.Pause(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	requeued := make(chan struct{})
	o.taskRequeueBarrier = func(assistants.TaskID) { close(requeued) }
	<-slot

	receive(t, requeued)
	tk := waitForTaskStatus(t, store, r.task, assistants.StatusQueued, 2*time.Second)
	if tk.StatusReason != onMessageStagePausedReason {
		t.Fatalf("requeue reason = %q, want %q", tk.StatusReason, onMessageStagePausedReason)
	}
	neverTrue(t, "a turn started while the stage is paused", func() bool { return r.turns() > 0 })

	if err := o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	r.requireClosed(1, assistants.VerdictPass)
}

// restart ends the rig's process as a crash does and recovers the run in a new one in Run's startup order — the
// stage's own recovery left out while it runs, as its agent is not under test. The new rig serves the same helper.
func (r *onMessageRig) restart() *onMessageRig {
	r.t.Helper()
	return r.restartWith(func(*Orchestrator) {})
}

// restartWith is restart with configure applied to the new process before it recovers.
func (r *onMessageRig) restartWith(configure func(*Orchestrator)) *onMessageRig {
	r.t.Helper()
	r.o.concurrency.WaitAgents()
	r.store.Close()
	o2, store2 := reopenAssistantsTestOrchestrator(r.t, r.o)
	configure(o2)
	r2 := &onMessageRig{t: r.t, o: o2, store: store2, task: r.task, helper: r.helper, stage: r.stage, thread: r.thread, script: r.script}
	o2.runHelperAgent = r2.run
	ctx := context.Background()
	if store2.Get(assistantsTestStageID) != state.StatusRunning {
		o2.startPlanningForPending(ctx)
	}
	o2.recoverCollaboration(ctx)
	return r2
}

// commitLostWake records a request to the helper as Send does, without the wake that follows the commit — a crash
// in between.
func (r *onMessageRig) commitLostWake(id string) agentcomm.MessageID {
	r.t.Helper()
	msgID := deriveMeshMessageID("lost-wake:" + id)
	req := SendRequest{Sender: r.stage, ThreadID: r.thread, Recipients: []agentcomm.ParticipantID{r.helper}, Kind: agentcomm.MessageKindRequest, Body: "request " + id}
	planned, err := r.o.planMessage(r.store.Snapshot(), req, msgID, nil)
	if err != nil {
		r.t.Fatalf("planMessage: %v", err)
	}
	op := state.Operation{ID: "test-send:" + id, Kind: "send_message", Effects: planned.effects}
	if _, err := r.store.ApplyRunOperation(op, nil); err != nil {
		r.t.Fatalf("commit %s: %v", id, err)
	}
	return msgID
}

// commitTaskStart records the wake and the start of the helper's task as scheduling does — separate commits, before
// any turn is recorded — with no process behind them.
func (r *onMessageRig) commitTaskStart() {
	r.t.Helper()
	commitTaskStatus(r.t, r.store, r.task, assistants.StatusQueued)
	commitTaskStatus(r.t, r.store, r.task, assistants.StatusRunning)
}

// commitTurnStart records, after commitTaskStart, the start of a turn on request with no process behind it — one the
// crash cut short.
func (r *onMessageRig) commitTurnStart(request agentcomm.MessageID) {
	r.t.Helper()
	eff, err := taskStatusChangeEffect(r.task, state.TaskStatusChangedPayload{Status: string(assistants.StatusRunning), Turn: r.taskState().Turn + 1, RequestID: string(request)})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.store.ApplyRunOperation(state.Operation{ID: "test-turn:" + string(request), Kind: "start_turn", Effects: []state.Effect{eff}}, nil); err != nil {
		r.t.Fatalf("commit the turn on %s: %v", request, err)
	}
}

// requireAnsweredOnceAfterRestart fails unless request got its result and reply in turn 1, the only turn the helper
// ran after the restart.
func (r *onMessageRig) requireAnsweredOnceAfterRestart(request agentcomm.MessageID) {
	r.t.Helper()
	r.requireAnswered(request, 1, "answer 1", "")
	r.o.concurrency.WaitAgents()
	if got := r.turns(); got != 1 {
		r.t.Fatalf("turns after the restart = %d, want the request worked on once", got)
	}
}

// A restart picks the helper up at whatever boundary the crash came: an idle helper stays idle, a request whose wake,
// start or turn the crash cut off gets its turn once, a stage paused keeps its requests waiting, and a close the crash
// came before is made — with collaboration disabled too, as it needs no model.
func TestOnMessage_RestartAtEachBoundary(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory).restart()
		neverTrue(t, "an idle helper started after the restart", func() bool { return r.turns() > 0 })
		if tk := r.taskState(); tk.Status != assistants.StatusWatching {
			t.Fatalf("helper = %s, want watching", tk.Status)
		}
	})
	t.Run("request committed, wake lost", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		m1 := r.commitLostWake("m1")
		if tk := r.taskState(); tk.Status != assistants.StatusWatching || len(tk.PendingRequests) != 1 {
			t.Fatalf("setup: helper = %s pending %v, want the request waiting with no wake", tk.Status, tk.PendingRequests)
		}
		r = r.restart()
		r.requireAnswered(m1, 1, "answer 1", "")
	})
	t.Run("woken, task not started", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		m1 := r.commitLostWake("m1")
		commitTaskStatus(t, r.store, r.task, assistants.StatusQueued)
		r = r.restart()
		r.requireAnsweredOnceAfterRestart(m1)
	})
	t.Run("task started, turn not recorded", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		m1 := r.commitLostWake("m1")
		r.commitTaskStart()
		if tk := r.taskState(); tk.Status != assistants.StatusRunning || tk.ActiveRequest != "" || tk.Turn != 0 {
			t.Fatalf("setup: helper = %s active %q turn %d, want running with no turn bound to m1 yet", tk.Status, tk.ActiveRequest, tk.Turn)
		}
		r = r.restart()
		r.requireAnsweredOnceAfterRestart(m1)
	})
	t.Run("turn cut short", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		m1 := r.commitLostWake("m1")
		r.commitTaskStart()
		r.commitTurnStart(m1)
		r = r.restart()
		r.requireAnswered(m1, 2, "answer 1", "")
		r.o.concurrency.WaitAgents()
		if got := r.turns(); got != 1 {
			t.Fatalf("turns after the restart = %d, want the cut-short turn run again once", got)
		}
		if p := r.prompt(1); !strings.Contains(p, "request m1") {
			t.Fatalf("the repeated turn must carry m1:\n%s", p)
		}
	})
	t.Run("stage paused", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeRequired)
		r.finishMain()
		if err := r.o.Pause(context.Background(), assistantsTestStageID); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		r.commitLostWake("m1")
		r = r.restart()
		neverTrue(t, "a turn started under the paused stage", func() bool { return r.turns() > 0 })
	})
	t.Run("stage done before the close", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		commitStageEvent(t, r.o, assistantsTestStageID, bus.EvComplete)
		r = r.restart()
		r.requireClosed(0, "")
	})
	t.Run("required pass, stage awaiting before the close", func(t *testing.T) {
		r := passedThenAwaitingBeforeTheClose(t)
		r = r.restart()
		r.requireClosed(0, assistants.VerdictPass)
		waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
	})
	t.Run("required pass, stage awaiting before the close, collaboration disabled", func(t *testing.T) {
		r := passedThenAwaitingBeforeTheClose(t)
		r = r.restartWith(func(o *Orchestrator) { o.opts.AssistantsEnabled = false })
		r.requireClosed(0, assistants.VerdictPass)
		waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
	})
}

// passedThenAwaitingBeforeTheClose is a required helper resting after the pass of its only request, with its stage
// awaiting it and no close made: the crash came right after the stage's transition. The helper's turn has ended
// before the transition, so only the restart can close it.
func passedThenAwaitingBeforeTheClose(t *testing.T) *onMessageRig {
	t.Helper()
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	m1, _ := r.ask("m1")
	r.waitOutcome(m1)
	r.o.concurrency.WaitAgents()
	commitStageEvent(t, r.o, assistantsTestStageID, bus.EvAwaitAssistants)
	if tk := r.taskState(); tk.Status != assistants.StatusWatching || r.stageStatus() != state.StatusAwaitingAssistants {
		t.Fatalf("setup: helper = %s, stage %s; want the helper resting and the stage awaiting it", tk.Status, r.stageStatus())
	}
	return r
}

// A board notice to a helper whose request queue is full is skipped like one to a full inbox — the publish itself
// goes through — and an assignment it could not take is refused before anything is recorded.
func TestOnMessage_FullRequestQueueSkipsABoardNotice(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	r := newOnMessageRigOn(t, o, store, assistants.ModeAdvisory)
	r.pause()
	for i := range agentcomm.MaxInboxUnread {
		thread := r.thread
		if i >= agentcomm.MaxThreadMessages/2 {
			thread = openThread(t, o, "op-thread-2", 0, r.stage, r.helper)
		}
		sendAs(t, o, fmt.Sprintf("op-req-%d", i), thread, r.stage, r.helper, fmt.Sprintf("request %d", i), nil, false)
	}
	readInbox(t, o, r.helper)
	if tk := r.taskState(); len(tk.PendingRequests) != agentcomm.MaxInboxUnread {
		t.Fatalf("setup: %d requests waiting, want %d", len(tk.PendingRequests), agentcomm.MaxInboxUnread)
	}
	messagesBefore := len(store.Snapshot().Assistants.Mesh.Messages)

	if _, err := o.Publish(context.Background(), PublishRequest{
		OperationID: "op-publish", Author: agentcomm.UserParticipantID, Key: "status/build", Status: "green",
		Notify: []agentcomm.ParticipantID{r.helper}, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Publish with a notice the helper has no room for: %v", err)
	}
	if got := len(store.Snapshot().Assistants.Mesh.Messages); got != messagesBefore {
		t.Fatalf("messages %d → %d, want the notice skipped", messagesBefore, got)
	}

	_, err := o.BoardTask(context.Background(), BoardTaskRequest{
		OperationID: "op-assign", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/review",
		Title: new("Review"), To: string(r.helper),
	})
	requireRejection(t, err, assistants.CodeInvalidStatus)
	if !strings.Contains(err.Error(), "requests waiting") {
		t.Fatalf("refusal %q, want it to name the full request queue", err)
	}
	if _, exists := store.Snapshot().Assistants.Mesh.BoardTasks["task/review"]; exists {
		t.Fatal("the refused assignment left a board task behind")
	}
}

// A delegation refused again and again fails the request it was for, not the helper: it takes the next request.
func TestOnMessage_RepeatedDelegationRejectionFailsOnlyTheRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	if _, err := r.store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "rev-shallow"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn <= maxConsecutiveDelegationRejections {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil // depth 2 > max_depth 1
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1, w1 := r.ask("m1")
	if got := r.waitOutcome(m1); got != state.RequestOutcomeFailed {
		t.Fatalf("m1 ended %q, want failed", got)
	}
	tk := waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)
	if !strings.Contains(tk.StatusReason, "delegation_rejected") {
		t.Fatalf("reason = %q, want the rejections named", tk.StatusReason)
	}
	if w := r.waitStatus(w1); w.Status != agentcomm.WaitReady {
		t.Fatalf("wait on m1 = %s, want ready with the failure reply", w.Status)
	}
	m2, w2 := r.ask("m2")
	r.requireAnswered(m2, uint64(maxConsecutiveDelegationRejections)+1, fmt.Sprintf("answer %d", maxConsecutiveDelegationRejections+1), w2)
}

// Rejections count within one request: a request answered after some of them does not leave the next one fewer.
func TestOnMessage_DelegationRejectionsCountPerRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	if _, err := r.store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 1, Source: assistants.DepthSourceDefault, Revision: "rev-shallow"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	// m1: rejected twice, then answered; m2: rejected once, then answered.
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		switch turn {
		case 1, 2, 4:
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil // depth 2 > max_depth 1
		default:
			return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
		}
	})
	m1, w1 := r.ask("m1")
	r.requireAnswered(m1, 3, "answer 3", w1)
	m2, w2 := r.ask("m2")
	r.requireAnswered(m2, 5, "answer 5", w2)
}

// failFirstTurnToStart makes the helper's executable fail to start on its first turn only.
func failFirstTurnToStart(r *onMessageRig) (repair func()) {
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			return executor.HelperOutcome{}, fmt.Errorf("spawn helper-cmd: %w", executor.ErrAgentStart)
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	return func() {}
}

// blockTheWorkspace makes the preparation of the helper's turns fail before any turn is recorded, until repair: the
// scratch directory each turn creates is a file.
func blockTheWorkspace(r *onMessageRig) (repair func()) {
	scratch := r.o.taskScratchDir(r.task)
	if err := os.RemoveAll(scratch); err != nil {
		r.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(scratch), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(scratch, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		if err := os.Remove(scratch); err != nil {
			r.t.Fatal(err)
		}
	}
}

// requestQueue is t's accepted requests in the order it works them off: the active one, then those waiting.
func requestQueue(t *state.TaskState) []agentcomm.MessageID {
	if t.ActiveRequest == "" {
		return t.PendingRequests
	}
	return append([]agentcomm.MessageID{t.ActiveRequest}, t.PendingRequests...)
}

// A helper that failed before a process worked on its request keeps the requests it accepted, unanswered: its Retry
// works them off in order under their original ids, each answered once, without anybody sending them again. One whose
// stage is done meanwhile finishes them, then is closed.
func TestOnMessage_RetryOfAFailedHelperResumesItsRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sabotage  func(*onMessageRig) (repair func())
		firstTurn uint64 // the turn the retry answers the first request in
	}{
		{"process did not start", failFirstTurnToStart, 2},
		{"preparation failed", blockTheWorkspace, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newOnMessageRig(t, assistants.ModeAdvisory)
			repair := tc.sabotage(r)
			r.pause()
			m1, m2 := r.send("m1"), r.send("m2")
			r.resume()
			tk := waitForTaskStatus(t, r.store, r.task, assistants.StatusFailed, 2*time.Second)
			if got := requestQueue(tk); !slices.Equal(got, []agentcomm.MessageID{m1, m2}) {
				t.Fatalf("failed helper's requests = %v, want both kept in order", got)
			}
			if r.outcome(m1) != "" || len(repliesTo(r.store, m1)) != 0 {
				t.Fatal("the failure ended m1, which no process worked on")
			}
			repair()
			if err := r.o.RetryTask(context.Background(), r.task); err != nil {
				t.Fatalf("RetryTask: %v", err)
			}
			r.requireAnswered(m1, tc.firstTurn, fmt.Sprintf("answer %d", tc.firstTurn), "")
			r.requireAnswered(m2, tc.firstTurn+1, fmt.Sprintf("answer %d", tc.firstTurn+1), "")
			if tk := waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second); !state.OnMessageIdle(*tk) {
				t.Fatalf("helper after its requests: active %q pending %v, want resting", tk.ActiveRequest, tk.PendingRequests)
			}
		})
	}
	t.Run("stage done meanwhile", func(t *testing.T) {
		r := newOnMessageRig(t, assistants.ModeAdvisory)
		failFirstTurnToStart(r)
		m1 := r.send("m1")
		waitForTaskStatus(t, r.store, r.task, assistants.StatusFailed, 2*time.Second)
		r.finishMain()
		if err := r.o.RetryTask(context.Background(), r.task); err != nil {
			t.Fatalf("RetryTask: %v", err)
		}
		// The done stage reads no more replies; the result is committed for m1 all the same.
		if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
			t.Fatalf("m1 ended %q, want result", got)
		}
		if res := r.results()[m1]; res.Turn != 2 || res.Summary != "answer 2" {
			t.Fatalf("result of m1 = turn %d %q, want the retried turn 2", res.Turn, res.Summary)
		}
		r.requireClosed(2, "")
		requireRunNotHeld(t, r.store)
	})
}

// A failed helper of a superseded attempt of its stage is not retried: nothing would ever work its requests off, and
// they would hold the run.
func TestOnMessage_RetryRefusesAHelperOfASupersededAttempt(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	failFirstTurnToStart(r)
	r.send("m1")
	waitForTaskStatus(t, r.store, r.task, assistants.StatusFailed, 2*time.Second)
	if err := r.o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	requireRejection(t, r.o.RetryTask(context.Background(), r.task), assistants.CodeInvalidStatus)
	if tk := r.taskState(); tk.Status != assistants.StatusFailed {
		t.Fatalf("helper = %s, want still failed", tk.Status)
	}
	requireRunNotHeld(t, r.store)
}

// A new attempt of the stage that comes while a Retry of its failed helper is under way — after the Retry read the
// helper as current, before its commit — supersedes the helper all the same: the Retry is refused and the requests
// the helper kept do not hold the run.
func TestOnMessage_RetryRacingANewStageAttemptIsRefused(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	failFirstTurnToStart(r)
	r.send("m1")
	waitForTaskStatus(t, r.store, r.task, assistants.StatusFailed, 2*time.Second)
	// The Retry cancels a child the helper left unfinished between its read and its commit; the new attempt comes
	// right there, and its own sweep cancels the child again.
	child := r.commitUnfinishedChild()
	var newAttemptCame atomic.Bool
	r.o.taskCancels.Store(child, &taskCancelEntry{cancel: func() {
		if newAttemptCame.CompareAndSwap(false, true) {
			if err := r.o.BumpRootGeneration(assistantsTestStageID); err != nil {
				t.Errorf("BumpRootGeneration: %v", err)
			}
		}
	}})

	requireRejection(t, r.o.RetryTask(context.Background(), r.task), assistants.CodeInvalidStatus)
	if gen := r.store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]; gen != 1 {
		t.Fatalf("stage generation = %d, want the new attempt to have come inside the Retry", gen)
	}
	if tk := r.taskState(); tk.Status != assistants.StatusFailed {
		t.Fatalf("helper = %s, want still failed", tk.Status)
	}
	requireRunNotHeld(t, r.store)
}

// commitUnfinishedChild records a queued child of the helper that nothing runs.
func (r *onMessageRig) commitUnfinishedChild() assistants.TaskID {
	r.t.Helper()
	child, parent := deriveTaskID("op-unfinished-child"), string(r.task)
	created, err := taskCreatedEffect(child, assistantsTestStageID, state.TaskCreatedPayload{
		ParentTaskID: &parent, ProfileID: "reviewer", Title: "child", Instruction: "do it",
		Mode: string(assistants.ModeAdvisory), Trigger: string(assistants.TriggerOneShot), CreatedBy: "assistant",
	})
	if err != nil {
		r.t.Fatal(err)
	}
	queued, err := taskStatusEffect(child, assistants.StatusQueued, "")
	if err != nil {
		r.t.Fatal(err)
	}
	quota, err := quotaReservedEffect(1)
	if err != nil {
		r.t.Fatal(err)
	}
	op := state.Operation{ID: "op-unfinished-child", Kind: "attach_children", Effects: []state.Effect{created, queued, quota}}
	if _, err := r.store.ApplyRunOperation(op, nil); err != nil {
		r.t.Fatalf("commit the child: %v", err)
	}
	return child
}

// A required helper whose process failed to start on a later request is not closed with the pass of an earlier one
// once retried: the retry works on that very request, and only its own pass closes the helper and the stage.
func TestOnMessage_RetryAfterAStartFailureWorksOnTheStoppedRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	release := make(chan struct{})
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		switch turn {
		case 2:
			return executor.HelperOutcome{}, fmt.Errorf("spawn helper-cmd: %w", executor.ErrAgentStart)
		case 3:
			awaitRelease(t, release)
		default:
		}
		return verdictAnswer(assistants.VerdictPass), nil
	})
	m1, _ := r.ask("m1")
	r.waitOutcome(m1)
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWatching, 2*time.Second)
	m2 := r.send("m2")
	waitForTaskStatus(t, r.store, r.task, assistants.StatusFailed, 2*time.Second)
	r.finishMain()
	if got := r.stageStatus(); got != state.StatusAwaitingAssistants || !r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID) {
		t.Fatalf("stage = %s, decision needed %v; want it awaiting a decision on the failed helper", got, r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID))
	}
	if err := r.o.RetryTask(context.Background(), r.task); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}

	waitUntil(t, "the retry works on m2", func() bool { return r.turns() == 3 })
	if tk := r.taskState(); tk.Status.Terminal() || tk.ActiveRequest != m2 || r.stageStatus() != state.StatusAwaitingAssistants {
		t.Fatalf("helper = %s active %q, stage %s; want m2 under way and the stage still awaiting it", tk.Status, tk.ActiveRequest, r.stageStatus())
	}
	r.requireReplayAgrees()
	close(release)
	if got := r.waitOutcome(m2); got != state.RequestOutcomeResult {
		t.Fatalf("m2 ended %q, want result", got)
	}
	if res := r.results()[m2]; res.Turn != 3 || res.Verdict != assistants.VerdictPass {
		t.Fatalf("result of m2 = turn %d verdict %q, want the retried turn 3's pass", res.Turn, res.Verdict)
	}
	r.requireClosed(3, assistants.VerdictPass)
	waitStageStatus(t, r.store, assistantsTestStageID, state.StatusDone, 3*time.Second)
}

// A Pause of the stage committed while a turn is being prepared, after its command slot was granted, still holds the
// turn: the recorded turn start is the last step before the process, so the request goes back to the queue for the
// Continue instead of being worked on under the paused stage.
func TestOnMessage_StagePausedRightBeforeTheProcessStarts(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(passEveryRequest)
	r.finishMain()
	ctx := context.Background()
	var pauseOnce sync.Once
	r.o.fingerprintFn = func(string) (string, bool) {
		pauseOnce.Do(func() {
			if err := r.o.Pause(ctx, assistantsTestStageID); err != nil {
				t.Errorf("Pause: %v", err)
			}
		})
		return "", false
	}
	m1 := r.send("m1")
	waitUntil(t, "the turn goes back to the queue", func() bool {
		tk := r.taskState()
		return tk.Status == assistants.StatusQueued && tk.StatusReason == onMessageStagePausedReason
	})
	neverTrue(t, "a process started under the paused stage", func() bool { return r.turns() > 0 })
	if tk := r.taskState(); tk.Turn != 0 || tk.ActiveRequest != "" || !slices.Equal(tk.PendingRequests, []agentcomm.MessageID{m1}) {
		t.Fatalf("held helper: turn %d active %q pending %v, want m1 still waiting with no turn recorded", tk.Turn, tk.ActiveRequest, tk.PendingRequests)
	}

	if err := r.o.Continue(ctx, assistantsTestStageID); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	if res := r.results()[m1]; res.Turn != 1 {
		t.Fatalf("result of m1 in turn %d, want turn 1", res.Turn)
	}
	r.requireClosed(1, assistants.VerdictPass)
}

// A headless run whose stage failed while its on_message helper still had a request ends by itself once that request
// is answered: the request held the run, and its end wakes the run loop to decide again.
func TestOnMessage_FailedStageRunEndsAfterTheLastRequest(t *testing.T) {
	runner := &resultlessStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	stage := meshStageParticipantID(unreadTestStageID)
	attached := make(chan assistants.TaskID, 1)
	runner.turn = func(n int32) {
		if n != 1 {
			return
		}
		id, err := o.Attach(context.Background(), AttachRequest{
			OperationID: "op-on-message", StageID: unreadTestStageID, ProfileID: "qa", Instruction: "answer each request",
			Trigger: assistants.TriggerOnMessage, Actor: state.Actor{Kind: "user"},
		})
		if err != nil {
			t.Errorf("Attach: %v", err)
			return
		}
		helper := meshHelperParticipantID(id)
		th, err := o.OpenThread(context.Background(), OpenThreadRequest{OperationID: "op-thread", Members: []agentcomm.ParticipantID{stage, helper}, Scope: agentcomm.ScopeRun})
		if err == nil {
			_, err = o.Send(context.Background(), SendRequest{
				OperationID: "op-m1", ThreadID: th.ID, Sender: stage, Recipients: []agentcomm.ParticipantID{helper},
				Kind: agentcomm.MessageKindRequest, Body: "request m1",
			})
		}
		if err != nil {
			t.Errorf("the request to the helper: %v", err)
		}
		attached <- id
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		awaitRelease(t, release)
		return onMessageAnswer("answer 1"), nil
	}
	// The helper is released only after the run loop has decided on every event queued before: the stage agent has
	// exited, the helper has started, and the loop took a marker queued after that with nothing left behind it. From
	// then on only the end of the request can wake the loop.
	var markerMu sync.Mutex // queueing the marker and the hook's check of it are one step
	markerQueued, decided := false, false
	drained := make(chan exitDecision, 1)
	o.exitDecidedHook = func(rs state.RunState, allowed bool) {
		markerMu.Lock()
		defer markerMu.Unlock()
		if markerQueued && !decided && len(o.critical.Recv()) == 0 {
			decided = true
			drained <- exitDecision{rs: rs, allowed: allowed}
		}
	}
	queueMarker := func() bool {
		markerMu.Lock()
		defer markerMu.Unlock()
		markerQueued = o.critical.WakeEventLoop()
		return markerQueued
	}
	run := startRun(t, o, 10*time.Second)
	helper := receive(t, attached)
	waitUntil(t, "the stage failed and its agent exited", func() bool {
		return store.Get(unreadTestStageID) == state.StatusFailed && !o.concurrency.IsActive(unreadTestStageID)
	})
	receive(t, started)
	if !queueMarker() {
		t.Fatal("no room for the marker in the event queue")
	}
	if d := receive(t, drained); d.allowed || state.OnMessageIdle(*d.rs.Assistants.Tasks[helper]) {
		t.Fatalf("drained loop: exit allowed %v, helper idle %v; want the run held by the request under way", d.allowed, state.OnMessageIdle(*d.rs.Assistants.Tasks[helper]))
	}
	close(release)

	run.requireFinishedItself(t)
	if tk := store.Snapshot().Assistants.Tasks[helper]; tk.Status != assistants.StatusWatching || tk.LastOutcome != state.RequestOutcomeResult {
		t.Fatalf("helper = %s, last outcome %q; want resting after answering its request", tk.Status, tk.LastOutcome)
	}
}
