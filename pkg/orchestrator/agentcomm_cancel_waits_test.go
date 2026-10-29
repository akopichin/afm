package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// CancelWait with an explicit list of wait IDs: the run owner ends several waits of one stage in one operation.

var ownerActor = state.Actor{Kind: "user"}

// listFixture is two stages, each with its own thread to the human, so that waits (one reply_to per request)
// and the replies that answer them are real.
type listFixture struct {
	t       *testing.T
	o       *Orchestrator
	events  <-chan bus.Event
	threads map[string]agentcomm.ThreadID
	ops     atomic.Int32
}

func newListFixture(t *testing.T) *listFixture {
	t.Helper()
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for _, id := range []string{"s1", "s2"} {
		if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
			t.Fatal(err)
		}
	}
	stages := []flow.Stage{{ID: "s1"}, {ID: "s2"}}
	o := &Orchestrator{
		opts:      Options{RootDir: t.TempDir(), RunDir: runDir, Store: store, Stages: stages},
		ui:        bus.NewUIBus(),
		critical:  bus.NewCriticalBus(64),
		fsm:       bus.NewFSM(store),
		graph:     graph.NewStore(graph.NewRegistry(stages)),
		clock:     newFakeClockWithPump(t),
		absRunDir: runDir,
	}
	return &listFixture{t: t, o: o, events: o.critical.Recv(), threads: map[string]agentcomm.ThreadID{}}
}

func (f *listFixture) opID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, f.ops.Add(1))
}

// waitOnRequest sends a request from stage to the human and registers a reply_to wait on it.
func (f *listFixture) waitOnRequest(stage string) agentcomm.WaitID {
	f.t.Helper()
	ctx := context.Background()
	self := meshStageParticipantID(stage)
	if _, ok := f.threads[stage]; !ok {
		th, err := f.o.OpenThread(ctx, OpenThreadRequest{OperationID: "thread-" + stage, Members: []agentcomm.ParticipantID{self, meshUserParticipantID}, Scope: agentcomm.ScopeStage})
		if err != nil {
			f.t.Fatalf("OpenThread: %v", err)
		}
		f.threads[stage] = th.ID
	}
	sent, err := f.o.Send(ctx, SendRequest{
		OperationID: f.opID("ask-" + stage), ThreadID: f.threads[stage], Sender: self, Recipients: []agentcomm.ParticipantID{meshUserParticipantID},
		Kind: agentcomm.MessageKindRequest, Body: "please review", Actor: state.Actor{Kind: "assistant", ID: string(self)},
	})
	if err != nil {
		f.t.Fatalf("Send: %v", err)
	}
	receipt, err := f.o.Wait(ctx, WaitRequest{
		OperationID: f.opID("wait-" + stage), Waiter: self,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: sent.MessageID},
	})
	if err != nil {
		f.t.Fatalf("Wait: %v", err)
	}
	return receipt.WaitID
}

// park puts stage into awaiting_peer with ids as the episode its turn yielded on.
func (f *listFixture) park(stage string, ids ...agentcomm.WaitID) {
	f.t.Helper()
	if _, ok := f.o.Trigger(stage, bus.EvAwaitPeer, bus.GuardCtx{Phase: phaseImplementation, PeerWaits: ids}, ""); !ok {
		f.t.Fatalf("EvAwaitPeer failed for %s", stage)
	}
	f.o.prePeerPhase.Store(stage, phaseImplementation)
	saveMeshPeerWaitPhase(f.o.stageDir(stage), phaseImplementation)
	f.drain()
}

func (f *listFixture) drain() {
	for {
		select {
		case <-f.events:
		default:
			return
		}
	}
}

func (f *listFixture) cancel(stage string, ids ...agentcomm.WaitID) CancelWaitRequest {
	return CancelWaitRequest{OperationID: f.opID("cancel"), WaitIDs: ids, StageID: stage, Reason: "stale after a callback", Actor: ownerActor}
}

func (f *listFixture) status(id agentcomm.WaitID) agentcomm.WaitStatus {
	return f.o.opts.Store.Snapshot().Assistants.Mesh.Waits[id].Status
}

func (f *listFixture) requireStatuses(want map[agentcomm.WaitID]agentcomm.WaitStatus) {
	f.t.Helper()
	for id, status := range want {
		if got := f.status(id); got != status {
			f.t.Errorf("wait %s is %s, want %s", id, got, status)
		}
	}
}

// sendReply is safe to call from a goroutine of the test: it reports the error instead of failing the test.
func (f *listFixture) sendReply(stage string, to agentcomm.MessageID) error {
	_, err := f.o.Send(context.Background(), SendRequest{
		OperationID: f.opID("reply"), ThreadID: f.threads[stage], Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{meshStageParticipantID(stage)},
		Kind: agentcomm.MessageKindReply, InReplyTo: &to, Body: "done",
	})
	return err
}

func (f *listFixture) reply(stage string, to agentcomm.MessageID) {
	f.t.Helper()
	if err := f.sendReply(stage, to); err != nil {
		f.t.Fatalf("reply: %v", err)
	}
}

// wakeParks collects the park each wake event of stage (an answer ending the park, or a wait ending) is bound to
// within grace. A reply and a cancel racing may both publish one; they carry the same park, so the consumer
// (resumeAwaitingPeerPhase) continues the stage once and skips the rest.
func (f *listFixture) wakeParks(stage string, grace time.Duration) map[uint64]int {
	parks := map[uint64]int{}
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		select {
		case ev := <-f.events:
			if ev.StageID == stage && (ev.Type == bus.EventPeerWaitEnded || ev.Type == bus.EventPeerReplied) {
				parks[ev.ParkSeq]++
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	return parks
}

func (f *listFixture) requestOf(id agentcomm.WaitID) agentcomm.MessageID {
	return f.o.opts.Store.Snapshot().Assistants.Mesh.Waits[id].Condition.MessageID
}

func (f *listFixture) requireOneContinuation() {
	f.t.Helper()
	waitForCriticalEvent(f.t, f.events, bus.EventPeerWaitEnded, "s1", 2*time.Second)
	requireNoCriticalEvent(f.t, f.events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)
}

func TestCancelWaitList_EndsSelectedWaitsInOneOperationWithOneContinuation(t *testing.T) {
	f := newListFixture(t)
	w0, w1, w2 := f.waitOnRequest("s1"), f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1, w2)

	req := f.cancel("s1", w0, w2)
	if err := f.o.CancelWait(context.Background(), req); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}

	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w0: agentcomm.WaitCancelled, w1: agentcomm.WaitPending, w2: agentcomm.WaitCancelled})
	waits := f.o.opts.Store.Snapshot().Assistants.Mesh.Waits
	if waits[w0].Reason != req.Reason || waits[w2].Reason != req.Reason {
		t.Errorf("reasons = %q/%q, want %q", waits[w0].Reason, waits[w2].Reason, req.Reason)
	}
	if _, committed := f.o.opts.Store.Snapshot().Assistants.OperationIndex[req.OperationID]; !committed {
		t.Error("the whole list must be one committed operation under the request id")
	}
	f.requireOneContinuation()
}

func TestCancelWaitList_RejectsMalformedAndMixedSelectionsWithoutChangingAnything(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1)

	cases := []struct {
		name string
		edit func(*CancelWaitRequest)
		want assistants.ErrorCode
	}{
		{"with a single wait id", func(r *CancelWaitRequest) { r.WaitID = w1 }, assistants.CodeInvalidRequest},
		{"with a batch id", func(r *CancelWaitRequest) { r.BatchID = "mb-x" }, assistants.CodeInvalidRequest},
		{"empty id", func(r *CancelWaitRequest) { r.WaitIDs = []agentcomm.WaitID{w0, ""} }, assistants.CodeInvalidRequest},
		{"duplicate id", func(r *CancelWaitRequest) { r.WaitIDs = []agentcomm.WaitID{w0, w0} }, assistants.CodeInvalidRequest},
		{"unknown id next to a valid one", func(r *CancelWaitRequest) { r.WaitIDs = []agentcomm.WaitID{w0, "mw-unknown"} }, assistants.CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := f.cancel("s1", w0, w1)
			tc.edit(&req)
			requireRejected(t, f.o.CancelWait(context.Background(), req), tc.want)
			f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w0: agentcomm.WaitPending, w1: agentcomm.WaitPending})
		})
	}
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 100*time.Millisecond)
}

func TestCancelWaitList_RequiresOneWaiterThatIsTheNamedStage(t *testing.T) {
	f := newListFixture(t)
	mine, theirs := f.waitOnRequest("s1"), f.waitOnRequest("s2")
	f.park("s1", mine)
	f.park("s2", theirs)

	requireRejected(t, f.o.CancelWait(context.Background(), f.cancel("", mine, theirs)), assistants.CodeInvalidRequest)
	requireRejected(t, f.o.CancelWait(context.Background(), f.cancel("s1", mine, theirs)), assistants.CodeNotFound)
	requireRejected(t, f.o.CancelWait(context.Background(), f.cancel("s1", theirs)), assistants.CodeNotFound)
	requireRejected(t, f.o.CancelWait(context.Background(), f.cancel("s3", mine)), assistants.CodeNotFound)
	// Another participant may not cancel a list of someone else's waits.
	stranger := f.cancel("", mine)
	stranger.Actor = state.Actor{Kind: "agent", ID: string(meshStageParticipantID("s2"))}
	requireRejected(t, f.o.CancelWait(context.Background(), stranger), assistants.CodeActorNotAllowed)

	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{mine: agentcomm.WaitPending, theirs: agentcomm.WaitPending})
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 100*time.Millisecond)

	// The waiter itself may cancel its own list.
	own := f.cancel("", mine)
	own.Actor = state.Actor{Kind: "agent", ID: string(meshStageParticipantID("s1"))}
	if err := f.o.CancelWait(context.Background(), own); err != nil {
		t.Fatalf("a stage cancelling its own waits: %v", err)
	}
	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{mine: agentcomm.WaitCancelled})
}

// A result that arrives first keeps its win: the cancel only ends what is still pending, and an all-ended list
// is a no-op that wakes nobody.
func TestCancelWaitList_AnswerThatWonTheRaceIsKept(t *testing.T) {
	f := newListFixture(t)
	answered, pending := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", answered, pending)
	f.reply("s1", f.requestOf(answered))
	f.drain()

	if err := f.o.CancelWait(context.Background(), f.cancel("s1", answered, pending)); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{answered: agentcomm.WaitReady, pending: agentcomm.WaitCancelled})

	f.drain()
	if err := f.o.CancelWait(context.Background(), f.cancel("s1", answered, pending)); err != nil {
		t.Fatalf("CancelWait of ended waits: %v", err)
	}
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)
	requireNoCriticalEvent(t, f.events, bus.EventPeerReplied, "s1", 50*time.Millisecond)
}

func TestCancelWaitList_RepeatOfARequestIdIsANoOp(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1)

	req := f.cancel("s1", w0, w1)
	if err := f.o.CancelWait(context.Background(), req); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	f.requireOneContinuation()
	if err := f.o.CancelWait(context.Background(), req); err != nil {
		t.Fatalf("repeat of the same request: %v", err)
	}
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)

	other := req
	other.WaitIDs = []agentcomm.WaitID{w0}
	err := f.o.CancelWait(context.Background(), other)
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("the same request id for another selection: err = %v, want an operation id conflict", err)
	}
}

// Two owners cancelling the same waits at once under different request ids: one wins, one wake.
func TestCancelWaitList_ConcurrentCancelsWakeTheStageOnce(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1)

	var wg sync.WaitGroup
	for range 4 {
		req := f.cancel("s1", w0, w1)
		wg.Go(func() {
			if err := f.o.CancelWait(context.Background(), req); err != nil {
				t.Errorf("CancelWait: %v", err)
			}
		})
	}
	wg.Wait()

	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w0: agentcomm.WaitCancelled, w1: agentcomm.WaitCancelled})
	f.requireOneContinuation()
}

// A reply racing the cancel leaves each wait either answered or cancelled, never both, and the stage is woken
// for its one park, however many of the two publish the wake.
func TestCancelWaitList_ConcurrentReplyEndsEachWaitOnce(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1)
	request := f.requestOf(w0)

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := f.sendReply("s1", request); err != nil {
			t.Errorf("reply: %v", err)
		}
	})
	wg.Go(func() {
		if err := f.o.CancelWait(context.Background(), f.cancel("s1", w0, w1)); err != nil {
			t.Errorf("CancelWait: %v", err)
		}
	})
	wg.Wait()

	if got := f.status(w0); got != agentcomm.WaitReady && got != agentcomm.WaitCancelled {
		t.Fatalf("answered wait is %s, want ready or cancelled", got)
	}
	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w1: agentcomm.WaitCancelled})
	park := f.o.opts.Store.Snapshot().Stages["s1"].Seq
	if parks := f.wakeParks("s1", 300*time.Millisecond); len(parks) != 1 || parks[park] == 0 {
		t.Fatalf("wake events by park = %v, want the stage woken for its one park (seq %d) only", parks, park)
	}
}

// Pause keeps the park's waits; the cancel is durable meanwhile and Continue decides on it.
func TestCancelWaitList_WhilePausedIsDurableAndContinueResumesOnce(t *testing.T) {
	f := newListFixture(t)
	w0 := f.waitOnRequest("s1")
	f.park("s1", w0)
	if _, ok := f.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
		t.Fatal("EvPause from awaiting_peer should be allowed")
	}
	f.drain()

	if err := f.o.CancelWait(context.Background(), f.cancel("s1", w0)); err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w0: agentcomm.WaitCancelled})
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 150*time.Millisecond)
	if got := f.o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v: a cancel must not lift a manual pause", got)
	}

	if _, ok := f.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusAwaitingPeer}, "continue"); !ok {
		t.Fatal("EvContinue should return to awaiting_peer")
	}
	f.drain()
	f.o.continueParkedStage(context.Background(), "s1")
	f.requireOneContinuation()
}

// After a restart the journal still shows the cancellation and the stage still parked: recovery's decision
// (peerContinuation) is to continue it because its waits ended.
func TestCancelWaitList_SurvivesRestartAndRecoveryDecidesToContinue(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	f.park("s1", w0, w1)
	cancel := f.cancel("s1", w0, w1)
	cancel.Reason = "stale"
	if err := f.o.CancelWait(context.Background(), cancel); err != nil {
		t.Fatal(err)
	}

	rs := reopenOrchestrator(t, f.o).opts.Store.Snapshot()

	if w := rs.Assistants.Mesh.Waits[w0]; w.Status != agentcomm.WaitCancelled || w.Reason != "stale" {
		t.Fatalf("wait after restart: %+v", w)
	}
	if rs.Stages["s1"].Status != state.StatusAwaitingPeer {
		t.Fatalf("the park must survive the restart, status %v", rs.Stages["s1"].Status)
	}
	if ev, ok := peerContinuation(rs, "s1", ""); !ok || ev != bus.EventPeerWaitEnded {
		t.Fatalf("recovery decision = %q, %v; want %s", ev, ok, bus.EventPeerWaitEnded)
	}
}

// reopenOrchestrator replays the journal of o into a new Orchestrator over the same run directory.
func reopenOrchestrator(t *testing.T, o *Orchestrator) *Orchestrator {
	t.Helper()
	o.opts.Store.Close()
	store, err := state.Open(o.opts.RunDir, []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stages := []flow.Stage{{ID: "s1"}, {ID: "s2"}}
	return &Orchestrator{
		opts:      Options{RootDir: o.opts.RootDir, RunDir: o.opts.RunDir, Store: store, Stages: stages},
		ui:        bus.NewUIBus(),
		critical:  bus.NewCriticalBus(64),
		fsm:       bus.NewFSM(store),
		graph:     graph.NewStore(graph.NewRegistry(stages)),
		clock:     o.clock,
		absRunDir: o.absRunDir,
	}
}

// The owner's read shows one stage's waits only, in registration order, ended ones with their reason, and
// changes nothing.
func TestStageWaits_ListsOnlyThatStagesWaitsAndIsReadOnly(t *testing.T) {
	f := newListFixture(t)
	w0, w1 := f.waitOnRequest("s1"), f.waitOnRequest("s1")
	other := f.waitOnRequest("s2")
	f.park("s1", w0, w1)
	if err := f.o.CancelWait(context.Background(), f.cancel("s1", w0)); err != nil {
		t.Fatal(err)
	}
	f.drain()
	before := f.o.opts.Store.Snapshot().Assistants.Mesh.Waits

	views, err := f.o.StageWaits("s1")
	if err != nil {
		t.Fatal(err)
	}

	if len(views) != 2 || views[0].WaitID != w0 || views[1].WaitID != w1 {
		t.Fatalf("views = %+v, want s1's %s then %s (and not %s)", views, w0, w1, other)
	}
	if views[0].Status != agentcomm.WaitCancelled || views[0].Reason == "" || views[1].Status != agentcomm.WaitPending {
		t.Errorf("statuses/reasons = %+v", views)
	}
	if views[1].Condition.Kind != agentcomm.WaitOnReplyTo || views[1].Condition.MessageID == "" {
		t.Errorf("condition = %+v, want the reply_to request", views[1].Condition)
	}
	for id, w := range f.o.opts.Store.Snapshot().Assistants.Mesh.Waits {
		if w.Status != before[id].Status || w.Reason != before[id].Reason {
			t.Errorf("reading changed wait %s", id)
		}
	}
	requireNoCriticalEvent(t, f.events, bus.EventPeerWaitEnded, "s1", 50*time.Millisecond)
	_, err = f.o.StageWaits("s9")
	requireRejected(t, err, assistants.CodeNotFound)
}

// The owner's request id is bound to the stage it was first used for: the same id, waits and reason against
// another stage is not a replay of the first result but a request that must pass its own ownership check.
func TestCancelWaitList_ReplayUnderAnotherStageIsNotAReplay(t *testing.T) {
	f := newListFixture(t)
	mine, theirs := f.waitOnRequest("s1"), f.waitOnRequest("s2")
	f.park("s1", mine)
	f.park("s2", theirs)
	first := f.cancel("s1", mine)
	first.OperationID = OwnerCancelOperationID("s1", "req-1")
	if err := f.o.CancelWait(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	replay := first
	replay.StageID, replay.OperationID = "s2", OwnerCancelOperationID("s2", "req-1")
	requireRejected(t, f.o.CancelWait(context.Background(), replay), assistants.CodeNotFound)

	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{mine: agentcomm.WaitCancelled, theirs: agentcomm.WaitPending})
}

func TestOwnerCancelOperationID_IsPerStageAndPerRequest(t *testing.T) {
	a, b := OwnerCancelOperationID("s1", "req"), OwnerCancelOperationID("s2", "req")
	if a == b || a == OwnerCancelOperationID("s1", "other") || a == "req" {
		t.Fatalf("operation ids must differ per stage and per request: %q %q", a, b)
	}
}

// The owner's namespace is not for agents: one could otherwise commit a cancel under an id the owner will use.
func TestCancelWaitList_OwnerOperationIdsAreReservedForTheHuman(t *testing.T) {
	f := newListFixture(t)
	w0 := f.waitOnRequest("s1")
	f.park("s1", w0)
	req := f.cancel("", w0)
	req.OperationID = OwnerCancelOperationID("s1", "x")
	req.Actor = state.Actor{Kind: "agent", ID: string(meshStageParticipantID("s1"))}

	requireRejected(t, f.o.CancelWait(context.Background(), req), assistants.CodeInvalidRequest)

	f.requireStatuses(map[agentcomm.WaitID]agentcomm.WaitStatus{w0: agentcomm.WaitPending})
}
