package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/akopichin/afm/pkg/state"
)

// Regression: a stage agent finished its turn while a teammate's question to it was still unread, the stage
// went to done, and the teammate waited for the answer forever, holding the run open.

const unreadTestStageID = "frontend"

var unreadTestStage = meshStageParticipantID(unreadTestStageID)

// scriptedStageRunner is a fake executor.Runner for the stage: turn runs the scripted part of each turn, then
// the runner writes the completion markers a real agent would.
type scriptedStageRunner struct {
	turns   atomic.Int32
	mu      sync.Mutex
	prompts []string
	turn    func(n int32) error
}

func (r *scriptedStageRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *scriptedStageRunner) RunAgent(_ context.Context, _, _, prompt, logFile string) error {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	if err := r.turn(r.turns.Add(1)); err != nil {
		return err
	}
	stageDir := filepath.Dir(logFile)
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644)
}

func (r *scriptedStageRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func (r *scriptedStageRunner) prompt(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.prompts) {
		return ""
	}
	return r.prompts[n-1]
}

// newUnreadTestOrchestrator builds a production-wired Orchestrator (New) with one stage run by runner and a
// "qa" run-agent profile.
func newUnreadTestOrchestrator(t *testing.T, agent flow.AgentType, runner executor.Runner) (*Orchestrator, *state.Store) {
	t.Helper()
	return newUnreadTestOrchestratorAt(t, t.TempDir(), agent, runner)
}

// newUnreadTestOrchestratorAt is newUnreadTestOrchestrator over runDir — a second call on the same directory is
// a restarted process replaying the first one's journal (close the first store before).
func newUnreadTestOrchestratorAt(t *testing.T, runDir string, agent flow.AgentType, runner executor.Runner) (*Orchestrator, *state.Store) {
	t.Helper()
	store, err := state.Open(runDir, []string{unreadTestStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatal(err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "qa", Title: "QA", Command: "helper-cmd"}}, nil, "claude",
		func(string) (bool, string, assistants.ReadonlyEnforcement) {
			return true, "", assistants.ReadonlyEnforcementOS
		})
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(runDir, unreadTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- build the page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := New(Options{
		RunDir: runDir, Store: store, Config: config.Default(), Runner: runner,
		Stages:            []flow.Stage{{ID: unreadTestStageID, Name: "Frontend", Agents: []flow.AgentType{agent}}},
		AssistantsEnabled: true, AssistantsRegistry: reg,
	})
	return o, store
}

// runToEnd runs o until the run finishes; a run still open after the timeout fails the test.
func runToEnd(t *testing.T, o *Orchestrator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := o.Run(ctx); err != nil {
		t.Fatalf("Run: %v (the run did not finish)", err)
	}
}

// sendFromUser opens a thread between the human and the stage (once) and sends body to the stage.
func sendFromUser(t *testing.T, o *Orchestrator, opID, body string) agentcomm.MessageID {
	t.Helper()
	ctx := context.Background()
	thread := deriveMeshThreadID("unread-thread")
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "unread-thread", Members: []agentcomm.ParticipantID{meshUserParticipantID, unreadTestStage}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Errorf("OpenThread: %v", err)
	}
	receipt, err := o.Send(ctx, SendRequest{
		OperationID: opID, ThreadID: thread, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{unreadTestStage}, Kind: agentcomm.MessageKindMessage, Body: body,
	})
	if err != nil {
		t.Errorf("Send: %v", err)
	}
	return receipt.MessageID
}

// A message that arrives mid-turn earns the stage one more turn with it in <inbox> and the continuation note;
// the agent answers and the stage then completes — for each track whose turn completes the stage.
func TestUnreadMessage_ExtraTurnAnswersThenCompletes(t *testing.T) {
	for _, agent := range []flow.AgentType{flow.AgentAuto, flow.AgentImplementation} {
		t.Run(string(agent), func(t *testing.T) {
			runner := &scriptedStageRunner{}
			o, store := newUnreadTestOrchestrator(t, agent, runner)
			var question agentcomm.MessageID
			runner.turn = func(n int32) error {
				switch n {
				case 1:
					question = sendFromUser(t, o, "unread-q", "which port does the dev server use?")
				case 2:
					if _, err := o.Send(context.Background(), SendRequest{
						OperationID: "unread-a", ThreadID: deriveMeshThreadID("unread-thread"), Sender: unreadTestStage,
						Recipients: []agentcomm.ParticipantID{meshUserParticipantID}, Kind: agentcomm.MessageKindReply,
						InReplyTo: &question, Body: "5173",
					}); err != nil {
						t.Errorf("reply: %v", err)
					}
				default:
				}
				return nil
			}

			runToEnd(t, o)

			if got := runner.turns.Load(); got != 2 {
				t.Fatalf("stage turns = %d, want 2 (the turn the question arrived in, then one to answer it)", got)
			}
			second := runner.prompt(2)
			if !strings.Contains(second, "which port does the dev server use?") || !strings.Contains(second, strings.TrimSpace(unreadTurnNoteText)) {
				t.Fatalf("extra turn prompt must carry the question in <inbox> and the continuation note, got:\n%s", second)
			}
			if strings.Contains(runner.prompt(1), strings.TrimSpace(unreadTurnNoteText)) {
				t.Fatal("the first turn must not carry the continuation note")
			}
			if got := store.Get(unreadTestStageID); got != state.StatusDone {
				t.Fatalf("stage status = %s, want done", got)
			}
			if got := receiptOf(store, question, unreadTestStage); got != agentcomm.DeliveryDelivered {
				t.Fatalf("question receipt = %s, want delivered", got)
			}
			if !store.Snapshot().Assistants.Mesh.MessageAnswered(question) {
				t.Fatal("the question must be answered")
			}
		})
	}
}

// A teammate who keeps writing cannot keep the stage open: after maxUnreadTurns extra turns the stage
// completes, and the message it never read becomes undeliverable instead of staying unread forever.
func TestUnreadMessage_ExtraTurnsAreCapped(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	var sent []agentcomm.MessageID
	runner.turn = func(n int32) error {
		sent = append(sent, sendFromUser(t, o, fmt.Sprintf("unread-cap-%d", n), fmt.Sprintf("one more thing #%d", n)))
		return nil
	}

	runToEnd(t, o)

	if got, want := runner.turns.Load(), int32(1+maxUnreadTurns); got != want {
		t.Fatalf("stage turns = %d, want %d (one turn plus the extra-turn limit)", got, want)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	last := sent[len(sent)-1]
	if got := receiptOf(store, last, unreadTestStage); got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("last message receipt = %s, want undeliverable (the stage finished without reading it)", got)
	}
	if got := receiptOf(store, sent[0], unreadTestStage); got != agentcomm.DeliveryDelivered {
		t.Fatalf("first message receipt = %s, want delivered", got)
	}
}

// qaAsksFrontend is a run agent's first turn in the live bug: ask the stage a question, register a wait for
// any message addressed to it, end the turn.
func qaAsksFrontend(t *testing.T, o *Orchestrator, qa agentcomm.ParticipantID) agentcomm.MessageID {
	t.Helper()
	ctx := context.Background()
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "qa-thread", Members: []agentcomm.ParticipantID{qa, unreadTestStage}, Scope: agentcomm.ScopeRun,
	})
	if err != nil {
		t.Errorf("OpenThread: %v", err)
		return ""
	}
	receipt, err := o.Send(ctx, SendRequest{
		OperationID: "qa-question", ThreadID: thread.ID, Sender: qa,
		Recipients: []agentcomm.ParticipantID{unreadTestStage}, Kind: agentcomm.MessageKindMessage, Body: "is the login form done?",
	})
	if err != nil {
		t.Errorf("Send: %v", err)
	}
	if _, err := o.Wait(ctx, WaitRequest{
		OperationID: "qa-wait", Waiter: qa, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage},
	}); err != nil {
		t.Errorf("Wait: %v", err)
	}
	return receipt.MessageID
}

// startQAWhileStageWorks adds the QA run agent from inside the stage's first turn and returns once QA has
// asked its question and parked in waiting_peer — the moment the live bug's stage turn ended.
func startQAWhileStageWorks(t *testing.T, o *Orchestrator, store *state.Store) assistants.TaskID {
	t.Helper()
	first := "ask frontend whether the login form is done"
	res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: "add-qa", ProfileID: "qa", Title: "QA", Instruction: "test the app", FirstMessage: &first, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Errorf("AddRunAgent: %v", err)
		return ""
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.Snapshot().Assistants.Tasks[res.TaskID].Status == assistants.StatusWaitingPeer {
			return res.TaskID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("QA did not park in waiting_peer")
	return res.TaskID
}

// The live bug end to end: QA asks the stage mid-turn and waits; the stage gets an extra turn, answers, QA
// is resumed by the answer, and the run finishes.
func TestUnreadMessage_RunAgentQuestionIsAnsweredAndRunFinishes(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	qa := meshRunParticipantID(deriveTaskID("add-qa"))
	var question agentcomm.MessageID
	var qaTurns atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		o.deliverRenderedInbox(qa)() // what the real executor does once the process starts
		if qaTurns.Add(1) == 1 {
			question = qaAsksFrontend(t, o, qa)
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}
	var qaTask assistants.TaskID
	runner.turn = func(n int32) error {
		switch n {
		case 1:
			qaTask = startQAWhileStageWorks(t, o, store)
		case 2:
			th := store.Snapshot().Assistants.Mesh.Messages[question].ThreadID
			if _, err := o.Send(context.Background(), SendRequest{
				OperationID: "frontend-answer", ThreadID: th, Sender: unreadTestStage,
				Recipients: []agentcomm.ParticipantID{qa}, Kind: agentcomm.MessageKindReply, InReplyTo: &question, Body: "yes, merged",
			}); err != nil {
				t.Errorf("answer: %v", err)
			}
		default:
		}
		return nil
	}

	runToEnd(t, o)

	if got := runner.turns.Load(); got != 2 {
		t.Fatalf("stage turns = %d, want 2", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	if got := qaTurns.Load(); got != 2 {
		t.Fatalf("QA turns = %d, want 2 (asked, then resumed by the answer)", got)
	}
	if got := store.Snapshot().Assistants.Tasks[qaTask].Status; got == assistants.StatusWaitingPeer {
		t.Fatal("QA is still waiting for an answer")
	}
}

// A stage that fails with a teammate's question unread will never answer it: the question becomes
// undeliverable, QA's wait for the answer is cancelled, QA is resumed, and the run finishes.
func TestUnreadMessage_WaiterIsReleasedWhenStageFinishesUnread(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	qa := meshRunParticipantID(deriveTaskID("add-qa"))
	var question agentcomm.MessageID
	var qaTurns atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		o.deliverRenderedInbox(qa)() // what the real executor does once the process starts
		if qaTurns.Add(1) == 1 {
			question = qaAsksFrontend(t, o, qa)
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		return passResultOutcome(), nil
	}
	runner.turn = func(int32) error {
		startQAWhileStageWorks(t, o, store)
		return fmt.Errorf("agent crashed: %w", executor.ErrAgentStart)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = o.Run(ctx) // a failed stage ends the run; only that it ends at all matters here
	if ctx.Err() != nil {
		t.Fatal("the run did not finish: QA is still waiting for an answer from a failed stage")
	}

	if got := store.Get(unreadTestStageID); got != state.StatusFailed {
		t.Fatalf("stage status = %s, want failed", got)
	}
	if got := receiptOf(store, question, unreadTestStage); got != agentcomm.DeliveryUndeliverable {
		t.Fatalf("question receipt = %s, want undeliverable", got)
	}
	rs := store.Snapshot()
	w := rs.Assistants.Mesh.Waits[deriveMeshWaitID("qa-wait")]
	if w == nil || w.Status != agentcomm.WaitCancelled {
		t.Fatalf("QA wait = %+v, want cancelled", w)
	}
	if got := qaTurns.Load(); got != 2 {
		t.Fatalf("QA turns = %d, want 2 (asked, then resumed when its wait ended)", got)
	}
}

// A restart between parking a stage for unread messages and starting its extra turn resumes that turn.
func TestUnreadMessage_RecoverParkedStage(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	parkStageAwaitingPeer(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "rec-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "rec-send", ThreadID: deriveMeshThreadID("rec-open"), Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{stage}, Kind: agentcomm.MessageKindMessage, Body: "still there?",
	}); err != nil {
		t.Fatal(err)
	}

	o.recoverMeshWaits(ctx)

	if got := o.currentStatus(assistantsTestStageID); got == state.StatusAwaitingPeer {
		t.Fatal("a stage parked for unread messages must be resumed on recovery, not left in awaiting_peer")
	}
	o.concurrency.WaitAgents()
}

// A restart after a stage's wait was cancelled (its colleague finished without reading the question) but
// before the stage was woken resumes the stage through the terminal-wait path.
func TestUnreadMessage_RecoverStageWhoseWaitWasCancelled(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "ended-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := o.Wait(ctx, WaitRequest{
		OperationID: "ended-wait", Waiter: stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	parkStageAwaitingPeer(t, o, assistantsTestStageID)
	// Committed, but the process stopped before waking the stage.
	if err := o.resolveMeshWaitDurable("ended-cancel", receipt.WaitID, state.Actor{Kind: "system"}, agentcomm.WaitCancelled, "nobody will answer"); err != nil {
		t.Fatal(err)
	}

	o.recoverMeshWaits(ctx)

	if got := store.Get(assistantsTestStageID); got == state.StatusAwaitingPeer {
		t.Fatal("a stage whose wait was cancelled must be resumed on recovery, not left in awaiting_peer")
	}
	o.concurrency.WaitAgents()
}

// Two recipients finishing at once: a release decided while the other recipient could still answer must not
// commit after that recipient gave the message up too — it is decided again, and the sender's wait is cancelled.
func TestUnreadMessage_ReleaseDecidesAgainstCurrentState(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "backend", Command: "claude"}, flow.Stage{ID: "qa", Command: "claude"})
	ctx := context.Background()
	for _, id := range []string{assistantsTestStageID, "backend", "qa"} {
		activateStage(t, o, id)
	}
	front, back, qa := meshStageParticipantID(assistantsTestStageID), meshStageParticipantID("backend"), meshStageParticipantID("qa")
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "race-open", Members: []agentcomm.ParticipantID{qa, front, back}, Scope: agentcomm.ScopeRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := o.Send(ctx, SendRequest{
		OperationID: "race-q", ThreadID: thread.ID, Sender: qa,
		Recipients: []agentcomm.ParticipantID{front, back}, Kind: agentcomm.MessageKindMessage, Body: "is the API stable?",
	})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := o.Wait(ctx, WaitRequest{
		OperationID: "race-wait", Waiter: qa, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
	})
	if err != nil {
		t.Fatal(err)
	}

	stale := store.Snapshot() // frontend's release looks while backend can still answer
	if _, err := o.markUnreadUndeliverable(sent.MessageID, back, "backend"); err != nil {
		t.Fatal(err)
	}
	op, ended, err := unreadReleaseOp(stale, sent.MessageID, front, "frontend finished")
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 0 {
		t.Fatalf("stale decision cancelled %v, want nothing (backend could still answer then)", ended)
	}
	if _, err := store.ApplyRunOperation(*op, nil); !errors.Is(err, errUnreadReleaseStale) {
		t.Fatalf("stale release: err = %v, want errUnreadReleaseStale", err)
	}

	cancelled, err := o.markUnreadUndeliverable(sent.MessageID, front, assistantsTestStageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 1 || cancelled[0] != wait.WaitID {
		t.Fatalf("cancelled = %v, want [%s]", cancelled, wait.WaitID)
	}
	if got := store.Snapshot().Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitCancelled {
		t.Fatalf("sender wait = %s, want cancelled", got)
	}
}

// A wait for the answer to a message nobody can answer any more (its only recipient gave it up) is refused.
func TestUnreadMessage_WaitOnMessageWithoutReaderIsRejected(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "qa", Command: "claude"})
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, "qa")
	front, qa := meshStageParticipantID(assistantsTestStageID), meshStageParticipantID("qa")
	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "late-open", Members: []agentcomm.ParticipantID{qa, front}, Scope: agentcomm.ScopeRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := o.Send(ctx, SendRequest{
		OperationID: "late-q", ThreadID: thread.ID, Sender: qa,
		Recipients: []agentcomm.ParticipantID{front}, Kind: agentcomm.MessageKindMessage, Body: "done yet?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.markUnreadUndeliverable(sent.MessageID, front, assistantsTestStageID); err != nil {
		t.Fatal(err)
	}

	_, err = o.Wait(ctx, WaitRequest{
		OperationID: "late-wait", Waiter: qa, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
	})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeInvalidStatus {
		t.Fatalf("wait on a message nobody can answer: err = %v, want an invalid_status rejection", err)
	}
}

// A restart after the stage agent wrote its completion marker but before its completion was handled gives the
// extra turn for a colleague's unread question instead of completing the stage and dropping the question.
func TestUnreadMessage_RecoveryCompletionGivesExtraTurn(t *testing.T) {
	runner := &scriptedStageRunner{turn: func(int32) error { return nil }}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	activateStage(t, o, unreadTestStageID)
	stageDir := filepath.Join(o.opts.RunDir, unreadTestStageID)
	for _, name := range []string{".done", "execution_summary.md"} {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("done"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	question := sendFromUser(t, o, "recovered-q", "did you update the README?")

	runToEnd(t, o)

	if got := runner.turns.Load(); got != 1 {
		t.Fatalf("stage turns = %d, want 1 (the extra turn for the unread question)", got)
	}
	if !strings.Contains(runner.prompt(1), "did you update the README?") {
		t.Fatalf("extra turn prompt must carry the question, got:\n%s", runner.prompt(1))
	}
	if got := receiptOf(store, question, unreadTestStage); got != agentcomm.DeliveryDelivered {
		t.Fatalf("question receipt = %s, want delivered", got)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
}
