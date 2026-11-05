package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// An on_message helper rests until a request is addressed to it, answers that one request in a turn of its own —
// result, outcome and reply in one commit — and rests again; requests that arrive meanwhile wait their turn.

// onMessageRig is a running stage with one on_message helper and a conversation between the stage's agent and the
// helper. Each helper turn hands over its inbox as the real executor does, records its prompt and ends as script
// says — with an answer when script is nil. A child the helper delegates to passes at once.
type onMessageRig struct {
	t      *testing.T
	o      *Orchestrator
	store  *state.Store
	task   assistants.TaskID
	helper agentcomm.ParticipantID
	stage  agentcomm.ParticipantID
	thread agentcomm.ThreadID

	mu      sync.Mutex
	prompts []string
	script  func(ctx context.Context, turn int) (executor.HelperOutcome, error)
}

func newOnMessageRig(t *testing.T, mode assistants.Mode) *onMessageRig {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	return newOnMessageRigOn(t, o, store, mode)
}

func newOnMessageRigOn(t *testing.T, o *Orchestrator, store *state.Store, mode assistants.Mode) *onMessageRig {
	t.Helper()
	activateStage(t, o, assistantsTestStageID)
	r := &onMessageRig{t: t, o: o, store: store, stage: meshStageParticipantID(assistantsTestStageID)}
	o.runHelperAgent = r.run
	req := onMessageAttachRequest("op-on-message")
	req.Mode = mode
	id, err := o.Attach(context.Background(), req)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	r.task, r.helper = id, meshHelperParticipantID(id)
	r.thread = openThread(t, o, "op-thread", 0, r.stage, r.helper)
	return r
}

func (r *onMessageRig) run(ctx context.Context, taskID, _, _, prompt, _, _ string) (executor.HelperOutcome, error) {
	if assistants.TaskID(taskID) != r.task {
		return passResultOutcome(), nil
	}
	r.o.deliverRenderedInbox(r.helper)()
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	turn, script := len(r.prompts), r.script
	r.mu.Unlock()
	if script == nil {
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	}
	return script(ctx, turn)
}

func (r *onMessageRig) setScript(script func(ctx context.Context, turn int) (executor.HelperOutcome, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.script = script
}

// onMessageAnswer is an advisory helper's answer: a result without a verdict.
func onMessageAnswer(summary string) executor.HelperOutcome {
	return executor.HelperOutcome{ProcessOK: true, RawText: fmt.Sprintf(`{"schema_version":1,"kind":"result","summary":%q}`, summary)}
}

func (r *onMessageRig) turns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func (r *onMessageRig) prompt(turn int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if turn > len(r.prompts) {
		return ""
	}
	return r.prompts[turn-1]
}

func (r *onMessageRig) taskState() *state.TaskState {
	return r.store.Snapshot().Assistants.Tasks[r.task]
}

// send is a request from the stage's agent to the helper ("" when it was refused, which fails the test).
func (r *onMessageRig) send(id string) agentcomm.MessageID {
	return r.sendIn(r.thread, id)
}

func (r *onMessageRig) sendIn(thread agentcomm.ThreadID, id string) agentcomm.MessageID {
	receipt, err := r.o.Send(context.Background(), SendRequest{
		OperationID: "send-" + id, ThreadID: thread, Sender: r.stage, Recipients: []agentcomm.ParticipantID{r.helper},
		Kind: agentcomm.MessageKindRequest, Body: "request " + id,
	})
	if err != nil {
		r.t.Errorf("Send %s: %v", id, err)
	}
	return receipt.MessageID
}

// waitFor registers the stage's reply_to wait on request.
func (r *onMessageRig) waitFor(request agentcomm.MessageID) agentcomm.WaitID {
	r.t.Helper()
	w, err := r.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-" + string(request), Waiter: r.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: request},
	})
	if err != nil {
		r.t.Fatalf("Wait on %s: %v", request, err)
	}
	return w.WaitID
}

// ask is send with the stage's reply_to wait on the request.
func (r *onMessageRig) ask(id string) (agentcomm.MessageID, agentcomm.WaitID) {
	r.t.Helper()
	m := r.send(id)
	return m, r.waitFor(m)
}

func (r *onMessageRig) pause() {
	r.t.Helper()
	if err := r.o.PauseTask(context.Background(), r.task); err != nil {
		r.t.Fatalf("PauseTask: %v", err)
	}
}

func (r *onMessageRig) resume() {
	r.t.Helper()
	if err := r.o.ContinueTask(context.Background(), r.task); err != nil {
		r.t.Fatalf("ContinueTask: %v", err)
	}
}

func (r *onMessageRig) waitStatus(id agentcomm.WaitID) *state.MeshWait {
	return r.store.Snapshot().Assistants.Mesh.Waits[id]
}

// outcome is how the helper ended request, "" while it has not.
func (r *onMessageRig) outcome(request agentcomm.MessageID) state.RequestOutcome {
	for _, e := range r.store.TaskEvents(r.task) {
		var p state.TaskStatusChangedPayload
		if e.Kind == taskStatusChangedEffectKind && json.Unmarshal(e.Payload, &p) == nil && p.RequestID == string(request) && p.RequestOutcome != "" {
			return p.RequestOutcome
		}
	}
	return ""
}

// waitOutcome waits until the helper ended request and returns how.
func (r *onMessageRig) waitOutcome(request agentcomm.MessageID) state.RequestOutcome {
	r.t.Helper()
	waitUntil(r.t, "the helper ended request "+string(request), func() bool { return r.outcome(request) != "" })
	return r.outcome(request)
}

// results are the helper's committed results by the request they answer.
func (r *onMessageRig) results() map[agentcomm.MessageID]state.TaskResultPayload {
	out := map[agentcomm.MessageID]state.TaskResultPayload{}
	for _, e := range r.store.TaskEvents(r.task) {
		var p state.TaskResultPayload
		if e.Kind == taskResultEffectKind && json.Unmarshal(e.Payload, &p) == nil {
			out[agentcomm.MessageID(p.RequestID)] = p
		}
	}
	return out
}

// requireAnswered checks that request got its result in turn — with its summary — and one passive reply to the
// stage, and that its wait (if any) is ready.
func (r *onMessageRig) requireAnswered(request agentcomm.MessageID, turn uint64, summary string, wait agentcomm.WaitID) {
	r.t.Helper()
	if got := r.waitOutcome(request); got != state.RequestOutcomeResult {
		r.t.Fatalf("request %s ended %q, want result", request, got)
	}
	res, ok := r.results()[request]
	if !ok || res.Turn != turn || res.Summary != summary {
		r.t.Fatalf("result of %s = %+v (found %v), want turn %d with summary %q", request, res, ok, turn, summary)
	}
	replies := repliesTo(r.store, request)
	if len(replies) != 1 || replies[0].ID != turnReplyMessageID(r.task, turn, request) || replies[0].Sender != r.helper ||
		!replies[0].AutoResult || !slices.Equal(replies[0].Recipients, []agentcomm.ParticipantID{r.stage}) {
		r.t.Fatalf("replies to %s = %+v, want one passive reply of turn %d to the stage", request, replies, turn)
	}
	if wait != "" {
		if w := r.waitStatus(wait); w.Status != agentcomm.WaitReady {
			r.t.Fatalf("wait on %s = %s %q, want ready", request, w.Status, w.Reason)
		}
	}
}

// Each request gets a turn of its own, handed that request alone: two results of one task, each bound to its request
// and turn, each answered to the stage; the helper goes back to watching in between and after.
func TestOnMessage_EachRequestGetsItsOwnTurnResultAndReply(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	// Paused, the helper keeps its requests and starts nothing; both are waiting when it continues.
	r.pause()
	m1, w1 := r.ask("m1")
	m2, w2 := r.ask("m2")
	if n := r.turns(); n != 0 {
		t.Fatalf("turns while paused = %d, want 0", n)
	}
	if tk := r.taskState(); !slices.Equal(tk.PendingRequests, []agentcomm.MessageID{m1, m2}) {
		t.Fatalf("pending = %v, want both requests in arrival order", tk.PendingRequests)
	}
	r.resume()

	r.requireAnswered(m1, 1, "answer 1", w1)
	r.requireAnswered(m2, 2, "answer 2", w2)
	r.o.concurrency.WaitAgents()
	if p := r.prompt(1); !strings.Contains(p, "request m1") || strings.Contains(p, "request m2") {
		t.Fatalf("turn 1 prompt must carry m1 and not the request waiting behind it:\n%s", p)
	}
	if p := r.prompt(2); !strings.Contains(p, "request m2") || strings.Contains(p, "request m1") {
		t.Fatalf("turn 2 prompt must carry m2 alone:\n%s", p)
	}
	tk := r.taskState()
	if tk.Status != assistants.StatusWatching || tk.Turn != 2 || tk.ActiveRequest != "" || len(tk.PendingRequests) != 0 ||
		tk.LastRequest != m2 || tk.LastOutcome != state.RequestOutcomeResult || tk.LastVerdict != "" {
		t.Fatalf("task = %s turn %d, queue active=%q pending=%v last=%s/%s verdict %q; want watching after turn 2 with m2's result and no verdict",
			tk.Status, tk.Turn, tk.ActiveRequest, tk.PendingRequests, tk.LastRequest, tk.LastOutcome, tk.LastVerdict)
	}
	want := []string{"watching", "queued", "running", "running", "watching", "queued", "running", "running", "watching"}
	if got := journaledTaskStatuses(t, r.store, r.task); !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
}

// Nothing to do, nothing starts: a passive turn result does not wake the helper, and neither does a Continue.
func TestOnMessage_NoRequestNoTurn(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	sendAs(t, r.o, "op-passive", r.thread, r.stage, r.helper, "fyi", nil, true)
	r.pause()
	r.resume()
	neverTrue(t, "a turn started without a request", func() bool { return r.turns() > 0 })
	if got := journaledTaskStatuses(t, r.store, r.task); !slices.Equal(got, []string{"watching"}) {
		t.Fatalf("statuses = %v, want the helper still resting", got)
	}
}

// A failed turn fails its request only: the requester is told why, the helper waits for the next request, and the
// earlier pass of a required helper no longer counts.
func TestOnMessage_FailedTurnFailsOnlyItsRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeRequired)
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		switch turn {
		case 1:
			return passResultOutcome(), nil
		case 2: // a required helper must give a verdict
			return onMessageAnswer("no verdict"), nil
		default:
			return executor.HelperOutcome{ProcessErr: errors.New("exit status 2")}, nil
		}
	})
	m1, _ := r.ask("m1")
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	pass := r.taskState()
	if pass.LastVerdict != assistants.VerdictPass {
		t.Fatalf("verdict = %q, want pass", pass.LastVerdict)
	}

	for _, tc := range []struct{ id, why string }{{"m2", "result contract not met"}, {"m3", "exit status 2"}} {
		request, wait := r.ask(tc.id)
		if got := r.waitOutcome(request); got != state.RequestOutcomeFailed {
			t.Fatalf("%s ended %q, want failed", tc.id, got)
		}
		waitUntil(t, "the helper rests after "+tc.id, func() bool { return r.taskState().Status == assistants.StatusWatching })
		tk := r.taskState()
		if tk.LastRequest != request || tk.LastOutcome != state.RequestOutcomeFailed || !strings.Contains(tk.StatusReason, tc.why) {
			t.Fatalf("after %s: last %s/%s reason %q, want failed with %q", tc.id, tk.LastRequest, tk.LastOutcome, tk.StatusReason, tc.why)
		}
		if tk.LastVerdict != assistants.VerdictPass || tk.ResultSeq != pass.ResultSeq {
			t.Fatalf("after %s: verdict %q at %d, want the earlier pass kept as history", tc.id, tk.LastVerdict, tk.ResultSeq)
		}
		if _, ok := r.results()[request]; ok {
			t.Fatalf("%s has a result, want none", tc.id)
		}
		replies := repliesTo(r.store, request)
		if len(replies) != 1 || !replies[0].AutoResult || !strings.Contains(replies[0].Body, "could not produce a valid result") || !strings.Contains(replies[0].Body, tc.why) {
			t.Fatalf("replies to %s = %+v, want one passive reply saying why", tc.id, replies)
		}
		if w := r.waitStatus(wait); w.Status != agentcomm.WaitReady {
			t.Fatalf("wait on %s = %s, want ready with the failure reply", tc.id, w.Status)
		}
		if !r.store.Snapshot().Assistants.NeedsHumanDecision(assistantsTestStageID) {
			t.Fatalf("after %s: the old pass still closes the required helper's gate", tc.id)
		}
	}
	neverTrue(t, "a failed request was tried again", func() bool { return r.turns() > 3 })
}

// The helper's own reply to the request replaces the automatic one; the result is still committed for the request.
func TestOnMessage_ExplicitReplyReplacesTheAutomaticOne(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	r.setScript(func(ctx context.Context, turn int) (executor.HelperOutcome, error) {
		request := r.taskState().ActiveRequest
		if _, err := r.o.Send(ctx, SendRequest{
			OperationID: "op-explicit", ThreadID: r.thread, Sender: r.helper, Recipients: []agentcomm.ParticipantID{r.stage},
			Kind: agentcomm.MessageKindReply, InReplyTo: &request, Body: "my own answer",
		}); err != nil {
			t.Errorf("explicit reply: %v", err)
		}
		return onMessageAnswer("answer 1"), nil
	})
	m1, w1 := r.ask("m1")
	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	if replies := repliesTo(r.store, m1); len(replies) != 1 || replies[0].AutoResult || replies[0].Body != "my own answer" {
		t.Fatalf("replies to m1 = %+v, want only the helper's own", replies)
	}
	if res, ok := r.results()[m1]; !ok || res.Turn != 1 {
		t.Fatalf("result of m1 = %+v (found %v), want turn 1", res, ok)
	}
	if w := r.waitStatus(w1); w.Status != agentcomm.WaitReady {
		t.Fatalf("wait on m1 = %s, want ready", w.Status)
	}
}

// A reply that cannot be delivered does not cost the result: it is committed for its request, and the wait for the
// answer ends with the reason.
func TestOnMessage_UndeliveredReplyKeepsTheResult(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	full := openThread(t, r.o, "op-full", 1, r.stage, r.helper)
	r.pause()
	m1 := r.sendIn(full, "m1")
	w1 := r.waitFor(m1)
	r.resume()

	if got := r.waitOutcome(m1); got != state.RequestOutcomeResult {
		t.Fatalf("m1 ended %q, want result", got)
	}
	if res, ok := r.results()[m1]; !ok || res.Summary != "answer 1" {
		t.Fatalf("result of m1 = %+v (found %v), want it committed", res, ok)
	}
	if replies := repliesTo(r.store, m1); len(replies) != 0 {
		t.Fatalf("replies = %+v, want none: the conversation is full", replies)
	}
	waitUntil(t, "the wait on m1 ends", func() bool { return r.waitStatus(w1).Status != agentcomm.WaitPending })
	if w := r.waitStatus(w1); w.Status != agentcomm.WaitCancelled || !strings.Contains(w.Reason, "could not be delivered") {
		t.Fatalf("wait on m1 = %s %q, want cancelled: the answer could not be delivered", w.Status, w.Reason)
	}
	if tk := r.taskState(); tk.Status != assistants.StatusWatching || tk.LastOutcome != state.RequestOutcomeResult {
		t.Fatalf("task = %s/%s, want watching with m1's result", tk.Status, tk.LastOutcome)
	}
}

// One reply_to_all wait over two requests ends once each request has its own answer.
func TestOnMessage_ReplyToAllWaitGetsEachRequestsAnswer(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	r.pause()
	m1, m2 := r.send("m1"), r.send("m2")
	batch, err := r.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-batch", Waiter: r.stage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo},
		MessageIDs: []agentcomm.MessageID{m1, m2},
	})
	if err != nil || batch.Status != agentcomm.WaitPending || len(batch.Items) != 2 {
		t.Fatalf("batch wait = %+v, %v; want two pending items", batch, err)
	}
	r.resume()

	r.requireAnswered(m1, 1, "answer 1", batch.Items[0].WaitID)
	r.requireAnswered(m2, 2, "answer 2", batch.Items[1].WaitID)
}

// A request sent while the turn is committing its result is not lost: the helper takes it right after.
func TestOnMessage_RequestDuringTheFinishStartsTheNextTurn(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	sent := make(chan agentcomm.MessageID, 1)
	var once sync.Once
	r.o.helperOutputCommitHook = func() { once.Do(func() { sent <- r.send("m2") }) }

	m1 := r.send("m1")
	m2 := receive(t, sent)
	r.requireAnswered(m1, 1, "answer 1", "")
	r.requireAnswered(m2, 2, "answer 2", "")
}

// A request read ahead in the middle of another request's turn is still owed its own turn: the wait on it survives
// the idle moment between the two turns — the sweep of unanswerable waits included — and its turn is handed it.
func TestOnMessage_RequestReadAheadKeepsItsWaitAndGetsItsTurn(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	release := make(chan struct{})
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			awaitRelease(t, release)
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1, w1 := r.ask("m1")
	waitUntil(t, "turn 1 under way", func() bool { return r.turns() == 1 })
	m2, w2 := r.ask("m2")
	readInbox(t, r.o, r.helper)
	if got := r.store.Snapshot().Assistants.Mesh.Receipts[m2][r.helper]; got != agentcomm.DeliveryDelivered {
		t.Fatalf("receipt of m2 = %s, want delivered: the helper read it ahead", got)
	}
	// Right before the turn for m2 starts, a sweep of unanswerable waits runs while the helper is between turns.
	var swept atomic.Bool
	r.o.settlePlannedHook = func() {
		if slices.Contains(r.taskState().PendingRequests, m2) && swept.CompareAndSwap(false, true) {
			r.o.releaseUnanswerableWaits(context.Background(), func(context.Context, agentcomm.ParticipantID) {})
		}
	}
	close(release)

	r.requireAnswered(m1, 1, "answer 1", w1)
	r.requireAnswered(m2, 2, "answer 2", w2)
	if !swept.Load() {
		t.Fatal("the sweep between the turns did not run")
	}
	if p := r.prompt(2); !strings.Contains(p, "request m2") {
		t.Fatalf("turn 2 prompt must carry m2, read ahead or not:\n%s", p)
	}
}

// A question the helper asks its stage in the middle of a request is a callback, not a wait cycle; the answer
// resumes the same request, and a new request sent meanwhile waits for its own turn.
func TestOnMessage_CallbackContinuesTheRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	questions := make(chan agentcomm.MessageID, 1)
	r.setScript(func(ctx context.Context, turn int) (executor.HelperOutcome, error) {
		if turn > 1 {
			return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
		}
		q, err := r.o.Send(ctx, SendRequest{
			OperationID: "op-question", ThreadID: r.thread, Sender: r.helper, Recipients: []agentcomm.ParticipantID{r.stage},
			Kind: agentcomm.MessageKindRequest, Body: "which module?",
		})
		if err != nil {
			t.Errorf("question: %v", err)
		}
		if _, err := r.o.Wait(ctx, WaitRequest{
			OperationID: "op-wait-question", Waiter: r.helper, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: q.MessageID},
		}); err != nil {
			t.Errorf("the helper's wait for the answer to its question: %v", err)
		}
		questions <- q.MessageID
		return executor.HelperOutcome{ProcessOK: true}, nil // yields into the wait
	})
	m1, w1 := r.ask("m1")
	q := receive(t, questions)
	waitForTaskStatus(t, r.store, r.task, assistants.StatusWaitingPeer, 3*time.Second)
	m2 := r.send("m2")
	if tk := r.taskState(); tk.Status != assistants.StatusWaitingPeer || tk.ActiveRequest != m1 || r.turns() != 1 {
		t.Fatalf("after m2: %s active %q turns %d, want m1 still waiting for its answer", tk.Status, tk.ActiveRequest, r.turns())
	}
	sendAs(t, r.o, "op-answer", r.thread, r.stage, r.helper, "the billing module", &q, false)
	w2 := r.waitFor(m2)

	r.requireAnswered(m1, 2, "answer 2", w1)
	r.requireAnswered(m2, 3, "answer 3", w2)
	if p := r.prompt(2); !strings.Contains(p, "the billing module") || !strings.Contains(p, "request m1") || strings.Contains(p, "request m2") {
		t.Fatalf("turn 2 prompt must carry m1 and its answer, not m2:\n%s", p)
	}
}

// A delegation is part of the request: the turn after the children finish answers it.
func TestOnMessage_DelegationContinuesTheRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1, w1 := r.ask("m1")
	r.requireAnswered(m1, 2, "answer 2", w1)
}

// A usage limit parks the turn and keeps its request: the retry is handed the same request, the next one waits.
func TestOnMessage_QuotaParkKeepsTheRequest(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	r := newOnMessageRigOn(t, o, store, assistants.ModeAdvisory)
	r.setScript(func(_ context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			return limitOutcome(helperLimit(holdNow.Add(2 * time.Minute)))
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1, w1 := r.ask("m1")
	parked := waitForQuotaPark(t, store, r.task)
	if parked.ActiveRequest != m1 {
		t.Fatalf("parked with active %q, want m1 kept", parked.ActiveRequest)
	}
	m2, w2 := r.ask("m2")

	advanceUntil(t, clk, time.Minute, "the retry of m1", func() bool { return r.turns() >= 2 })
	r.requireAnswered(m1, 2, "answer 2", w1)
	r.requireAnswered(m2, 3, "answer 3", w2)
	if p := r.prompt(2); !strings.Contains(p, "request m1") || strings.Contains(p, "request m2") {
		t.Fatalf("the retry's prompt must carry m1 alone:\n%s", p)
	}
}

// A Pause of the helper in the middle of a request keeps that request active: nothing ends it — no outcome, result
// or reply — and the request behind it keeps waiting. The Continue works on the same request first, then on the next.
func TestOnMessage_PauseOfTheActiveTurnKeepsItsRequest(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	r.setScript(func(ctx context.Context, turn int) (executor.HelperOutcome, error) {
		if turn == 1 {
			<-ctx.Done() // stopped by the Pause, as the executor's process is
			return executor.HelperOutcome{ProcessErr: ctx.Err()}, nil
		}
		return onMessageAnswer(fmt.Sprintf("answer %d", turn)), nil
	})
	m1, w1 := r.ask("m1")
	waitUntil(t, "turn 1 under way", func() bool { return r.turns() == 1 })
	m2, w2 := r.ask("m2")
	r.pause()

	tk := waitForTaskStatus(t, r.store, r.task, assistants.StatusQueued, 2*time.Second)
	if tk.ActiveRequest != m1 || !slices.Equal(tk.PendingRequests, []agentcomm.MessageID{m2}) || tk.LastRequest != "" {
		t.Fatalf("paused helper: active %q pending %v last %q, want m1 still active and m2 waiting", tk.ActiveRequest, tk.PendingRequests, tk.LastRequest)
	}
	if r.outcome(m1) != "" || len(r.results()) != 0 || len(repliesTo(r.store, m1)) != 0 {
		t.Fatal("the Pause ended m1")
	}
	if w := r.waitStatus(w1); w.Status != agentcomm.WaitPending {
		t.Fatalf("wait on m1 = %s %q, want still pending", w.Status, w.Reason)
	}
	neverTrue(t, "a turn started while the helper is paused", func() bool { return r.turns() > 1 })

	r.resume()
	r.requireAnswered(m1, 2, "answer 2", w1)
	r.requireAnswered(m2, 3, "answer 3", w2)
	if p := r.prompt(2); !strings.Contains(p, "request m1") || strings.Contains(p, "request m2") {
		t.Fatalf("the turn after the Continue must carry m1 alone:\n%s", p)
	}
}

// A board task assigned to the helper is a request like any other.
func TestOnMessage_BoardAssignmentIsARequest(t *testing.T) {
	o, store := newBoardTaskTestOrchestrator(t)
	r := newOnMessageRigOn(t, o, store, assistants.ModeAdvisory)
	assigned := mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "op-create", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: "task/review",
		Title: new("Review"), To: string(r.helper),
	})
	if got := r.waitOutcome(assigned.MessageID); got != state.RequestOutcomeResult {
		t.Fatalf("the assignment ended %q, want result", got)
	}
	if p := r.prompt(1); !strings.Contains(p, "task/review") {
		t.Fatalf("turn 1 prompt must carry the assignment:\n%s", p)
	}
}

// A loop tick is a request: it wakes the resting helper through the usual queue, and the helper rests again.
func TestOnMessage_LoopTickIsARequest(t *testing.T) {
	rig := newHelperRig(t, func(string, int) (executor.HelperOutcome, error) { return onMessageAnswer("status ok"), nil })
	id, err := rig.o.Attach(context.Background(), onMessageAttachRequest("op-on-message"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	rig.loopFor(t, "op-loop", id)
	for slot := uint64(1); slot <= 2; slot++ {
		rig.mustTick(t, "L1", slot)
		// The loop's thread also carries the helper's replies to the ticks.
		ticks := slices.DeleteFunc(rig.loopMessages(t, "L1"), func(m *agentcomm.Message) bool { return m.AutoResult })
		tick := ticks[slot-1].ID
		waitUntil(t, "the helper answered the tick", func() bool {
			tk := rig.task(id)
			return tk.LastRequest == tick && tk.Status == assistants.StatusWatching
		})
	}
	if got := rig.promptsOf(id); len(got) != 2 || !strings.Contains(got[1], "⟳ Loop L1") {
		t.Fatalf("prompts = %q, want one turn per tick", got)
	}
}

// A cancelled helper takes no more requests.
func TestOnMessage_CancelledHelperRefusesRequests(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	if err := r.o.CancelTask(context.Background(), r.task); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	_, err := r.o.Send(context.Background(), SendRequest{
		OperationID: "op-late", ThreadID: r.thread, Sender: r.stage, Recipients: []agentcomm.ParticipantID{r.helper},
		Kind: agentcomm.MessageKindRequest, Body: "too late",
	})
	assertRejected(t, err, assistants.CodeInvalidStatus)
}

// The roster shows a resting helper as idle, not as working.
func TestOnMessage_RosterShowsARestingHelperIdle(t *testing.T) {
	r := newOnMessageRig(t, assistants.ModeAdvisory)
	if p, _ := resolveParticipant(r.store.Snapshot(), r.helper); p.Activity != agentcomm.ActivityIdle || !p.Reachable {
		t.Fatalf("resting helper = %s reachable %v, want idle and reachable", p.Activity, p.Reachable)
	}
}
