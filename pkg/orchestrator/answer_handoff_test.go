package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// A turn that ends with a question for the user and a wait on a colleague leaves the stage with the user. An answer
// that lands after the turn's last look at the stage and before its goroutine returns must still start one
// continuation — never leave the stage running with no executor.

// askTheUser is turn 1 of impl: it yields on a wait, writes a question, and, unless pollerMovesFirst is false,
// moves the stage to the user as the question poller does.
func askTheUser(rig *recoveryRig, pollerMovesFirst bool) func(string, int) error {
	return func(stage string, turn int) error {
		if err := rig.yieldOnce(stage, turn); err != nil || stage != recoveryImpl || turn != 1 {
			return err
		}
		question := filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution.q1.question.json")
		if err := os.WriteFile(question, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
			return err
		}
		if pollerMovesFirst {
			rig.o.Trigger(recoveryImpl, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
		}
		return nil
	}
}

// freezePoller keeps the question poller from ticking: it would auto-answer the non-interactive stage's question and
// race the test's own answer.
func freezePoller(rig *recoveryRig) {
	rig.o.clock = clock.NewFake(time.Now())
}

// answerTheQuestion is what the dashboard does: the answer file first, then the notification.
func answerTheQuestion(rig *recoveryRig) error {
	if err := mcp.WriteAnswerFile(filepath.Join(rig.runDir, recoveryImpl), phaseAutonomous, "q1", "the first", false); err != nil {
		return err
	}
	return rig.o.NotifyAnswer(recoveryImpl, phaseAutonomous, "q1", "the first", false)
}

// answerWhen is the dialog seam that answers the question at one point of the turn's exit: before its dialog decision
// (left false) or after it has left (left true). requireAnsweredOnce checks the seam fired exactly once and that the
// answer on disk is the seam's own, not an automatic one.
func answerWhen(t *testing.T, rig *recoveryRig, atLeft bool) (seam func(string, bool), requireAnsweredOnce func()) {
	var answers atomic.Int32
	seam = func(_ string, left bool) {
		if left != atLeft {
			return
		}
		answers.Add(1)
		if err := answerTheQuestion(rig); err != nil {
			t.Errorf("answer: %v", err)
		}
	}
	requireAnsweredOnce = func() {
		t.Helper()
		if n := answers.Load(); n != 1 {
			t.Fatalf("the seam answered %d times, want 1", n)
		}
		raw, err := os.ReadFile(filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution.q1.answer.json"))
		if err != nil || !strings.Contains(string(raw), `"answer":"the first"`) {
			t.Fatalf("answer.json = %q, %v; want the seam's answer", raw, err)
		}
	}
	return seam, requireAnsweredOnce
}

// requireOneContinuation checks the stage ended up with exactly one new turn after the answer, parked again on its
// still-pending wait, and that ending the wait lets it finish normally.
func requireOneContinuation(t *testing.T, rig *recoveryRig, runner *stageScriptRunner) {
	t.Helper()
	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d, want 2: the turn and one continuation after the answer", got)
	}
	rig.ownerCancels("req-cancel-after-answer")
	rig.waitStage(recoveryDocs, state.StatusDone)
	if got, want := fmt.Sprint(runner.started()), fmt.Sprint([]string{"impl#1", "impl#2", "impl#3", "docs#1"}); got != want {
		t.Fatalf("turn order = %s, want %s", got, want)
	}
}

// The question poller had already moved the stage to the user: the answer lands in the window between the turn's
// status check and the return of its goroutine.
func TestAnswerHandoff_AnswerAfterTheStatusCheckStartsOneContinuation(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	runner.script = askTheUser(rig, true)
	var requireAnsweredOnce func()
	rig.o.dialogHook, requireAnsweredOnce = answerWhen(t, rig, true)
	rig.start()

	requireOneContinuation(t, rig, runner)
	requireAnsweredOnce()
}

// The first detection of the question: the turn moves the stage to the user itself, and the answer lands before it
// returns.
func TestAnswerHandoff_AnswerAfterTheTurnAsksTheUserStartsOneContinuation(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	runner.script = askTheUser(rig, false)
	var requireAnsweredOnce func()
	rig.o.dialogHook, requireAnsweredOnce = answerWhen(t, rig, true)
	rig.start()

	requireOneContinuation(t, rig, runner)
	requireAnsweredOnce()
}

// An answer applied before the turn's last look is seen by it: the same turn goes on and parks, no second turn.
func TestAnswerHandoff_AnswerBeforeTheStatusCheckIsHandledByTheTurn(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	runner.script = askTheUser(rig, true)
	var requireAnsweredOnce func()
	rig.o.dialogHook, requireAnsweredOnce = answerWhen(t, rig, false)
	rig.start()

	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: the turn that saw the answer parks itself", got)
	}
	requireAnsweredOnce()
}

// An answer applied while the agent still runs is the ordinary live one: the turn finds the question answered.
func TestAnswerHandoff_AnswerWhileTheAgentRunsIsHandledByTheTurn(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	ask := askTheUser(rig, true)
	runner.script = func(stage string, turn int) error {
		if err := ask(stage, turn); err != nil || stage != recoveryImpl || turn != 1 {
			return err
		}
		return answerTheQuestion(rig)
	}
	rig.start()

	rig.requireParked()
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: the turn that saw the answer parks itself", got)
	}
}

// The poller moved the stage to the user, the agent's work is done and the question it left is stale: the turn
// completes the stage. An answer arriving while verify runs belongs to that turn; it must not start a continuation
// that would run an agent on the finished stage.
func TestAnswerHandoff_AnswerDuringVerifyOfAStaleQuestionCompletesOnce(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRig(t, t.TempDir(), runner)
	freezePoller(rig)
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl {
			return nil
		}
		question := filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution.q1.question.json")
		if err := os.WriteFile(question, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
			return err
		}
		rig.o.Trigger(recoveryImpl, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
		return nil
	}
	verifyAgent := rig.o.runVerifyAgent
	var answered atomic.Int32
	rig.o.runVerifyAgent = func(ctx context.Context, s flow.Stage, a, b, c, d string) (verify.RunOutcome, error) {
		if answered.Add(1) == 1 {
			if err := answerTheQuestion(rig); err != nil {
				t.Errorf("answer: %v", err)
			}
		}
		return verifyAgent(ctx, s, a, b, c, d)
	}
	rig.start()

	rig.waitStage(recoveryDocs, state.StatusDone)
	rig.stop()
	if n := answered.Load(); n != 1 {
		t.Fatalf("verify ran %d times through the seam, want 1: the answer was never sent during verify", n)
	}
	if got := runner.turnsOf(recoveryImpl); got != 1 {
		t.Fatalf("impl turns = %d, want 1: the answer started a turn on the finished stage", got)
	}
	if got := rig.store.Get(recoveryImpl); got != state.StatusDone {
		t.Fatalf("impl = %s, want done", got)
	}
	if got := rig.verifyCalls.Load(); got != 1 {
		t.Fatalf("verify ran %d times, want 1", got)
	}
}

// The continuation after an answer is a live agent like any other: a question it asks and the user answers while it
// runs goes to it, not to a new continuation (nothing remembers that the previous turn left for an answer).
func TestAnswerHandoff_ContinuationAfterAnAnswerIsALiveAgent(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	ask := askTheUser(rig, true)
	var statusAfterLiveAnswer atomic.Value
	runner.script = func(stage string, turn int) error {
		if err := ask(stage, turn); err != nil || stage != recoveryImpl || turn != 2 {
			return err
		}
		second := filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution.q2.question.json")
		if err := os.WriteFile(second, []byte(`{"id":"q2","question":"and then?"}`), 0o644); err != nil {
			return err
		}
		rig.o.Trigger(recoveryImpl, bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
		if err := mcp.WriteAnswerFile(filepath.Join(rig.runDir, recoveryImpl), phaseAutonomous, "q2", "the second", false); err != nil {
			return err
		}
		if err := rig.o.NotifyAnswer(recoveryImpl, phaseAutonomous, "q2", "the second", false); err != nil {
			return err
		}
		statusAfterLiveAnswer.Store(rig.store.Get(recoveryImpl))
		return nil
	}
	var left <-chan struct{}
	rig.o.dialogHook, left = leftForAnswer()
	rig.start()

	closedWithin(t, left, "the turn to leave the stage with the user")
	if err := answerTheQuestion(rig); err != nil {
		t.Fatal(err)
	}
	rig.requireParked()
	if got := statusAfterLiveAnswer.Load(); got != state.StatusRunning {
		t.Fatalf("stage status right after the live answer = %v, want running: the answer did not reach the live agent", got)
	}
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d, want 2", got)
	}
}

// leaveQuestions is turn 1 of impl without a wait: its agent exits leaving the questions ids open, so the turn hands
// the stage to the user.
func leaveQuestions(rig *recoveryRig, ids ...string) error {
	for _, id := range ids {
		question := filepath.Join(rig.runDir, recoveryImpl, "autonomous_execution."+id+".question.json")
		if err := os.WriteFile(question, []byte(`{"id":"`+id+`","question":"which one?"}`), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// closedWithin waits for ch to close, failing the test after a bound instead of hanging it.
func closedWithin(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// leftForAnswer is the dialog seam that reports when the turn has left the stage with the user.
func leftForAnswer() (seam func(string, bool), left <-chan struct{}) {
	ch := make(chan struct{})
	var once sync.Once
	return func(_ string, isLeft bool) {
		if isLeft {
			once.Do(func() { close(ch) })
		}
	}, ch
}

// The agent left two questions: an answer to one of them starts no continuation, the answer to the last one starts one.
func TestAnswerHandoff_AnswerWhileAnotherQuestionIsOpenStartsNothing(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl || turn != 1 {
			return nil
		}
		return leaveQuestions(rig, "q1", "q2")
	}
	var left <-chan struct{}
	rig.o.dialogHook, left = leftForAnswer()
	rig.start()
	closedWithin(t, left, "the turn to leave the stage with the user")

	stageDir := filepath.Join(rig.runDir, recoveryImpl)
	if err := mcp.WriteAnswerFile(stageDir, phaseAutonomous, "q1", "the first", false); err != nil {
		t.Fatal(err)
	}
	// The event loop's handling of that answer, run here so its outcome is known when it returns.
	if err := rig.o.onUserAnswered(context.Background(), bus.Event{
		Type: bus.EventUserAnswered, StageID: recoveryImpl, Data: map[string]any{keyID: "q1", keyPhase: phaseAutonomous},
	}); err != nil {
		t.Fatal(err)
	}
	if got := rig.store.Get(recoveryImpl); got != state.StatusAwaitingUserInput {
		t.Fatalf("impl = %s after an answer to q1 alone, want awaiting_user_input: q2 is still open", got)
	}

	if err := mcp.WriteAnswerFile(stageDir, phaseAutonomous, "q2", "the second", false); err != nil {
		t.Fatal(err)
	}
	if err := rig.o.NotifyAnswer(recoveryImpl, phaseAutonomous, "q2", "the second", false); err != nil {
		t.Fatal(err)
	}
	rig.waitStage(recoveryImpl, state.StatusDone)
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d, want 2: the turn and one continuation after the last answer", got)
	}
}

// The startup resume of a stage left with the user runs after the answer has started the continuation (the resume's
// runner was queued for a command slot meanwhile): its grant fails and it starts no second agent. The queueing itself
// is the concurrency manager's; here the resume runs at the moment it would get its slot.
func TestAnswerHandoff_ResumeAfterTheAnswersContinuationStartsNoAgent(t *testing.T) {
	runner := &stageScriptRunner{}
	rig := newRecoveryRigVerifying(t, t.TempDir(), runner, flow.VerifySpec{})
	freezePoller(rig)
	// The resume falls back to the implementation agent, which reads the plan.
	if err := os.WriteFile(filepath.Join(rig.runDir, recoveryImpl, "plan.md"), []byte("## Tasks\n- build it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	continuing, release := make(chan struct{}), make(chan struct{})
	runner.script = func(stage string, turn int) error {
		if stage != recoveryImpl {
			return nil
		}
		if turn == 1 {
			return leaveQuestions(rig, "q1")
		}
		if turn == 2 {
			close(continuing)
			<-release
		}
		return nil
	}
	var left <-chan struct{}
	rig.o.dialogHook, left = leftForAnswer()
	rig.start()
	closedWithin(t, left, "the turn to leave the stage with the user")

	if err := answerTheQuestion(rig); err != nil {
		t.Fatal(err)
	}
	closedWithin(t, continuing, "the answer's continuation")
	rig.o.resumeInteractiveAgent(context.Background(), *rig.o.reg().Stage(recoveryImpl))
	close(release)

	rig.waitStage(recoveryImpl, state.StatusDone)
	if got := runner.turnsOf(recoveryImpl); got != 2 {
		t.Fatalf("impl turns = %d, want 2: the late resume ran an agent beside the answer's continuation", got)
	}
}
