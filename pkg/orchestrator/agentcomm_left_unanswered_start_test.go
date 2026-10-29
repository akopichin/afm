package orchestrator

import (
	"context"
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
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// The start of a task agent's turn ends, in its own commit, the waits on the requests the agent left unanswered:
// no turn begins with such a wait pending, whichever path made the agent idle and whoever starts the turn.

// holdSweepUntil makes the background release of unanswerable waits wait for open (or the test's patience to
// end), so that a turn start can be ordered before it deterministically.
func holdSweepUntil(o *Orchestrator, open <-chan struct{}) {
	o.sweepGate = func() {
		select {
		case <-open:
		case <-time.After(taskWait):
		}
	}
}

// yieldOnQuestion makes QA's turn wait for the user's answer to a question and exit empty, as a yield does.
func (f *leftFixture) yieldOnQuestion(op string) agentcomm.MessageID {
	f.t.Helper()
	asked := sendAs(f.t, f.o, op, f.rev.ThreadID, f.rev.ParticipantID, meshUserParticipantID, "a question", nil, false)
	if _, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: op + "-wait", Waiter: f.rev.ParticipantID, Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: asked},
	}); err != nil {
		f.t.Errorf("Wait: %v", err)
	}
	return asked
}

// operationRecords are the journal lines of the operations of kind opKind.
func operationRecords(t *testing.T, runDir, opKind string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"op_kind":"`+opKind+`"`) {
			out = append(out, line)
		}
	}
	return out
}

// The live finish race: B was read in the middle of turn 1 and request C is unread. The background release is
// held until the turn for C has started, so the start itself must have ended B's wait.
func TestLeftUnanswered_TurnStartEndsTheWaitsOnWhatTheAgentLeft(t *testing.T) {
	f := newLeftFixture(t)
	_, live := f.o.ui.Subscribe(256)
	opened := make(chan struct{})
	var once sync.Once
	holdSweepUntil(f.o, opened)
	_, waitB := f.ask("b")
	f.readInboxMidTurn()
	f.send("c")
	var atStart agentcomm.WaitStatus
	f.turn = func(n int32) executor.HelperOutcome {
		if n == 1 {
			return passResultOutcome()
		}
		atStart = f.wait(waitB).Status
		once.Do(func() { close(opened) })
		f.yieldOnQuestion("op-q")
		return executor.HelperOutcome{ProcessOK: true}
	}
	close(f.release)

	waitForTaskStatus(t, f.store, f.rev.TaskID, assistants.StatusWaitingPeer, taskWait)
	if atStart != agentcomm.WaitCancelled {
		t.Fatalf("B's wait when the turn for C started = %q, want cancelled before any new turn", atStart)
	}
	f.requireCancelled(waitB)
	requireCancelledNotices(t, live, waitB)
	f.o.concurrency.WaitAgents()
}

// parkedScenario is the stage-level race: stage impl asks QA request A and a separate request B, waits on B (and on
// any legacy waits) and parks. QA reads B in the middle of its turn 1, sends itself request C and ends the turn:
// the turn for C is the one whose start must settle B.
type parkedScenario struct {
	t       *testing.T
	runDir  string
	o       *Orchestrator
	store   *state.Store
	runner  *scriptedStageRunner
	b       agentcomm.MessageID
	waits   []agentcomm.WaitID     // the waits on B; the first is the stage's own
	atStart []agentcomm.WaitStatus // their statuses when QA's turn for C started
	qaTurns atomic.Int32
	armed   atomic.Bool // QA's turn 1 has ended: the next turn start is the one for C
	opened  chan struct{}
	once    sync.Once
}

// open lets a held background release (holdSweepUntil) go on.
func (s *parkedScenario) open() { s.once.Do(func() { close(s.opened) }) }

// newParkedScenario builds the scenario; legacyOps are further waits on B the stage registers as an old journal holds them.
func newParkedScenario(t *testing.T, legacyOps ...string) *parkedScenario {
	t.Helper()
	runDir := t.TempDir()
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestratorAt(t, runDir, flow.AgentAuto, runner)
	s := &parkedScenario{t: t, runDir: runDir, o: o, store: store, runner: runner, opened: make(chan struct{})}
	started, release, ready := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var rev AttachAndSendResult
	var thread agentcomm.ThreadID
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		awaitRelease(t, ready)
		o.deliverRenderedInbox(rev.ParticipantID)()
		switch s.qaTurns.Add(1) {
		case 1:
			close(started)
			awaitRelease(t, release)
			readInbox(t, o, rev.ParticipantID)
			sendAs(t, o, "send-c", thread, unreadTestStage, rev.ParticipantID, "request c", nil, false)
			s.armed.Store(true)
		case 2:
			s.atStart = make([]agentcomm.WaitStatus, len(s.waits))
			for i, id := range s.waits {
				s.atStart[i] = store.Snapshot().Assistants.Mesh.Waits[id].Status
			}
			s.open()
		default:
		}
		return passResultOutcome(), nil
	}
	var err error
	rev, err = o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-rev", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	thread = openThread(t, o, "op-th", 0, unreadTestStage, rev.ParticipantID)
	close(ready)

	runner.turn = func(n int32) error {
		if n != 1 {
			return nil
		}
		sendAs(t, o, "send-a", thread, unreadTestStage, rev.ParticipantID, "request a", nil, false)
		<-started
		s.b = sendAs(t, o, "send-b", thread, unreadTestStage, rev.ParticipantID, "request b", nil, false)
		w, err := o.Wait(context.Background(), WaitRequest{
			OperationID: "wait-b", Waiter: unreadTestStage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: s.b},
		})
		if err != nil {
			return err
		}
		s.waits = []agentcomm.WaitID{w.WaitID}
		for _, op := range legacyOps {
			s.waits = append(s.waits, registerLegacyWait(t, store, op, unreadTestStage, s.b))
		}
		go func() {
			waitUntil(t, "the stage parks", func() bool { return store.Get(unreadTestStageID) == state.StatusAwaitingPeer })
			close(release)
		}()
		return nil
	}
	return s
}

// run drives the run to its end: the stage must continue exactly once and QA must run the turn for C.
func (s *parkedScenario) run() {
	s.t.Helper()
	runToEnd(s.t, s.o)
	waitUntil(s.t, "QA's turn for C ran", func() bool { return s.qaTurns.Load() == 2 })
	s.o.concurrency.WaitAgents()
	if got := s.store.Get(unreadTestStageID); got != state.StatusDone {
		s.t.Fatalf("stage = %s, want done", got)
	}
	if got := s.runner.turns.Load(); got != 2 {
		s.t.Fatalf("stage turns = %d, want the asking turn and exactly one continuation", got)
	}
}

// requireCancelledBeforeTheTurn checks every wait on B was cancelled, with the reason, by the time QA's turn for C started.
func (s *parkedScenario) requireCancelledBeforeTheTurn() {
	s.t.Helper()
	for i, id := range s.waits {
		if s.atStart[i] != agentcomm.WaitCancelled {
			s.t.Errorf("wait %d when the turn for C started = %q, want cancelled", i, s.atStart[i])
		}
		if w := s.store.Snapshot().Assistants.Mesh.Waits[id]; !strings.Contains(w.Reason, leftReason) {
			s.t.Errorf("wait %d reason = %q, want %q", i, w.Reason, leftReason)
		}
	}
}

// requireOneSettlingStart checks the start_task records that cancel waits: one, cancelling n waits with the start itself.
func (s *parkedScenario) requireOneSettlingStart(n int) {
	s.t.Helper()
	var settling []string
	for _, rec := range operationRecords(s.t, s.runDir, "start_task") {
		if strings.Contains(rec, "mesh_wait_resolved") {
			settling = append(settling, rec)
		}
	}
	if len(settling) != 1 || strings.Count(settling[0], "mesh_wait_resolved") != n || !strings.Contains(settling[0], "task_status_changed") {
		s.t.Fatalf("start_task records that cancel waits = %d, want one that cancels %d with the start:\n%v", len(settling), n, settling)
	}
}

// requireCancelledOnce checks the journal cancels n waits in all: none twice.
func (s *parkedScenario) requireCancelledOnce(n int) {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.runDir, "events.jsonl"))
	if err != nil {
		s.t.Fatal(err)
	}
	if got := strings.Count(string(raw), "mesh_wait_resolved"); got != n {
		s.t.Fatalf("wait cancellations in the journal = %d, want %d", got, n)
	}
}

// Two waits on B, one of a stage parked on them: the start of the turn for C ends both in its own commit, and the
// stage continues once.
func TestLeftUnanswered_TurnStartSettlesEveryWaitInItsOwnCommit(t *testing.T) {
	s := newParkedScenario(t, "legacy-2")
	holdSweepUntil(s.o, s.opened)

	s.run()

	s.requireCancelledBeforeTheTurn()
	s.requireOneSettlingStart(2)
}

// The background release commits B's cancellation between the start's snapshot and its commit: the start is
// rejected, decides again and starts the turn for C; B is cancelled once and the stage woken once.
func TestLeftUnanswered_StartRetriesWhenTheReleaseSettlesTheWaitFirst(t *testing.T) {
	s := newParkedScenario(t)
	holdSweepUntil(s.o, s.opened)
	s.o.settlePlannedHook = func() {
		if s.armed.CompareAndSwap(true, false) {
			s.o.releaseUnanswerableWaits(context.Background(), s.o.resumeWaiterAfterTerminalWait)
			s.open()
		}
	}

	s.run()

	s.requireCancelledBeforeTheTurn()
	s.requireCancelledOnce(1)
}

// Another wait on B appears between the start's snapshot and its commit: the start is planned again and cancels
// both in its one commit, so no wait is left pending when the turn runs.
func TestLeftUnanswered_StartRePlansWhenAnotherWaitOnTheRequestAppears(t *testing.T) {
	s := newParkedScenario(t)
	holdSweepUntil(s.o, s.opened)
	s.o.settlePlannedHook = func() {
		if s.armed.CompareAndSwap(true, false) {
			s.waits = append(s.waits, registerLegacyWait(t, s.store, "legacy-late", unreadTestStage, s.b))
		}
	}

	s.run()

	s.requireCancelledBeforeTheTurn()
	s.requireOneSettlingStart(2)
}

// requireCancelledNotices checks the live feed announced each wait as cancelled with the reason.
func requireCancelledNotices(t *testing.T, live <-chan bus.Event, ids ...agentcomm.WaitID) {
	t.Helper()
	announced := map[string]string{}
	for drained := false; !drained; {
		select {
		case ev := <-live:
			data, _ := ev.Data.(map[string]any)
			if reason, _ := data["reason"].(string); ev.Type == bus.EventMeshWait && data["status"] == string(agentcomm.WaitCancelled) {
				id, _ := data["wait_id"].(string)
				announced[id] = reason
			}
		default:
			drained = true
		}
	}
	for _, id := range ids {
		if reason, ok := announced[string(id)]; !ok || !strings.Contains(reason, leftReason) {
			t.Errorf("live notice for wait %s = %q (announced: %v), want cancelled: %s", id, reason, ok, leftReason)
		}
	}
}

// The release after a finish ends all the waits it finds in one commit.
func TestLeftUnanswered_ReleaseEndsEveryWaitInOneCommit(t *testing.T) {
	runDir := t.TempDir()
	f := newLeftFixtureAt(t, runDir)
	b := f.send("b")
	f.readInboxMidTurn()
	f.finish()
	_, live := f.o.ui.Subscribe(256)
	var waits []agentcomm.WaitID
	for _, op := range []string{"legacy-1", "legacy-2"} {
		waits = append(waits, registerLegacyWait(t, f.store, op, unreadTestStage, b))
	}
	waits = append(waits, registerLegacyWait(t, f.store, "legacy-3", meshUserParticipantID, b))
	before := len(operationRecords(t, runDir, "mesh_resolve_wait"))

	f.o.releaseUnanswerableWaits(context.Background(), func(context.Context, agentcomm.ParticipantID) {})

	records := operationRecords(t, runDir, "mesh_resolve_wait")
	if len(records) != before+1 || strings.Count(records[len(records)-1], "mesh_wait_resolved") != 3 {
		t.Fatalf("release commits = %d new, want one that cancels all three waits:\n%v", len(records)-before, records[before:])
	}
	requireCancelledNotices(t, live, waits...)
}

// Two waits, each on a request QA left, are unanswerable at the same moment. Between the release's snapshot and
// its commit one of them is answered: the commit is rejected, the release decides again, and the other is
// still cancelled and its waiter woken once.
func TestLeftUnanswered_ReleaseDecidesAgainWhenOneWaitIsAnsweredMeanwhile(t *testing.T) {
	f := newLeftFixture(t)
	b, e := f.send("b"), f.send("e")
	f.readInboxMidTurn()
	f.finish()
	waitB := registerLegacyWait(t, f.store, "legacy-b", unreadTestStage, b)
	waitE := registerLegacyWait(t, f.store, "legacy-e", unreadTestStage, e)
	var armed atomic.Bool
	armed.Store(true)
	f.o.settlePlannedHook = func() {
		if armed.CompareAndSwap(true, false) {
			sendAs(t, f.o, "late-reply-b", f.thread, f.rev.ParticipantID, unreadTestStage, "done B", &b, false)
		}
	}
	var woken []agentcomm.ParticipantID
	f.o.releaseUnanswerableWaits(context.Background(), func(_ context.Context, waiter agentcomm.ParticipantID) {
		woken = append(woken, waiter)
	})

	if w := f.wait(waitB); w.Status != agentcomm.WaitReady {
		t.Fatalf("answered wait = %s %q, want ready", w.Status, w.Reason)
	}
	f.requireCancelled(waitE)
	if !slices.Equal(woken, []agentcomm.ParticipantID{unreadTestStage}) {
		t.Fatalf("woken = %v, want the waiter of the cancelled wait once", woken)
	}
}

// A usage limit parks the agent after it read B: B's wait ends while the agent is queued, not at its next turn.
func TestLeftUnanswered_QuotaParkEndsTheWaitWithoutAnotherTurn(t *testing.T) {
	f := newLeftFixture(t)
	_, waitB := f.ask("b")
	f.readInboxMidTurn()
	f.turn = func(n int32) executor.HelperOutcome {
		if n == 1 {
			return executor.HelperOutcome{ProcessErr: &executor.UsageLimitError{Provider: quotaProvider, NoReset: true, Message: "insufficient_quota"}}
		}
		return passResultOutcome()
	}
	close(f.release)

	waitUntil(t, "B's wait ends during the park", func() bool { return f.wait(waitB).Status != agentcomm.WaitPending })
	f.requireCancelled(waitB)
	tk := f.store.Snapshot().Assistants.Tasks[f.rev.TaskID]
	if tk.Status != assistants.StatusQueued || tk.Quota == nil || f.turns.Load() != 1 {
		t.Fatalf("task = %s quota=%v turns=%d, want parked after one turn", tk.Status, tk.Quota, f.turns.Load())
	}
	f.o.concurrency.WaitAgents()
}

// The same with a reset time: B's wait ends while the park timer is still pending.
func TestLeftUnanswered_QuotaParkUntilEndsTheWaitBeforeTheTimerFires(t *testing.T) {
	f := newLeftFixture(t)
	clk := clock.NewFake(holdNow)
	runCtx, cancel := context.WithCancel(context.Background())
	f.o.runCtx = runCtx
	t.Cleanup(cancel)
	f.o.clock = clk
	_, waitB := f.ask("b")
	f.readInboxMidTurn()
	f.turn = func(n int32) executor.HelperOutcome {
		if n == 1 {
			return executor.HelperOutcome{ProcessErr: helperLimit(holdNow.Add(2 * time.Minute))}
		}
		return passResultOutcome()
	}
	close(f.release)

	waitUntil(t, "B's wait ends during the park", func() bool { return f.wait(waitB).Status != agentcomm.WaitPending })
	f.requireCancelled(waitB)
	if tk := f.store.Snapshot().Assistants.Tasks[f.rev.TaskID]; tk.Status != assistants.StatusQueued || tk.Quota == nil || tk.Quota.Until.IsZero() || f.turns.Load() != 1 {
		t.Fatalf("task = %s quota=%+v turns=%d, want parked until the reset after one turn", tk.Status, tk.Quota, f.turns.Load())
	}
	advanceUntil(t, clk, time.Minute, "the parked turn runs after the reset", func() bool { return f.turns.Load() == 2 })
	f.o.concurrency.WaitAgents() // the turn must not still write while the run directory is removed
}

// A held agent resumed from waiting_peer is idle without a turn: B, read in the turn that yielded, ends now.
func TestLeftUnanswered_ResumeOfAHeldAgentEndsTheWait(t *testing.T) {
	f := newLeftFixture(t)
	_, waitB := f.ask("b")
	f.readInboxMidTurn()
	var asked agentcomm.MessageID
	f.turn = func(n int32) executor.HelperOutcome {
		if n == 1 {
			asked = f.yieldOnQuestion("op-q")
			return executor.HelperOutcome{ProcessOK: true}
		}
		return passResultOutcome()
	}
	close(f.release)
	waitForTaskStatus(t, f.store, f.rev.TaskID, assistants.StatusWaitingPeer, taskWait)

	if err := f.o.PauseTask(context.Background(), f.rev.TaskID); err != nil {
		t.Fatal(err)
	}
	sendAs(t, f.o, "op-q-answer", f.rev.ThreadID, meshUserParticipantID, f.rev.ParticipantID, "the answer", &asked, false)
	waitForTaskStatus(t, f.store, f.rev.TaskID, assistants.StatusQueued, taskWait)

	waitUntil(t, "B's wait ends while the agent is held", func() bool { return f.wait(waitB).Status != agentcomm.WaitPending })
	f.requireCancelled(waitB)
	if f.turns.Load() != 1 {
		t.Fatalf("turns = %d, want none while held", f.turns.Load())
	}
	f.o.concurrency.WaitAgents() // the background release and the park must not still write at cleanup
}

// Restart: a message reaches the idle agent before the startup release (a stage or the human writing to it while
// Run still recovers). The turn it starts must not begin with B's wait pending.
func TestLeftUnanswered_StartupTurnBeforeTheReleaseStillEndsTheWait(t *testing.T) {
	run := crashAfterQAReadB(t, true, false)
	rig := newRecoveryRig(t, run.runDir, &stageScriptRunner{})
	started := make(chan agentcomm.WaitStatus, 1)
	rig.o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		rig.o.deliverRenderedInbox(run.qa.ParticipantID)()
		select {
		case started <- rig.store.Snapshot().Assistants.Mesh.Waits[run.waitB].Status:
		default:
		}
		return passResultOutcome(), nil
	}
	hookDone := make(chan struct{})
	rig.o.startupReleaseHook = func() {
		defer close(hookDone)
		sendAs(t, rig.o, "send-c-at-startup", run.qa.ThreadID, meshUserParticipantID, run.qa.ParticipantID, "request c", nil, false)
		select {
		case got := <-started:
			if got != agentcomm.WaitCancelled {
				t.Errorf("B's wait when the turn for C started = %q, want cancelled", got)
			}
		case <-time.After(taskWait):
			t.Error("the turn for C did not start")
		}
	}
	rig.start()
	<-hookDone

	waitUntilFor(t, taskWait, "B's wait ends", func() bool {
		return rig.store.Snapshot().Assistants.Mesh.Waits[run.waitB].Status != agentcomm.WaitPending
	})
	run.requireWaitBCancelled(t, rig)
}

// PauseTask and an immediate ContinueTask: the paused turn is re-queued after the hold is already gone, so the
// retry starts at once. That start closes B, A is handed again, and never two turns run at once.
func TestLeftUnanswered_PauseWithImmediateContinueStartsASettledTurn(t *testing.T) {
	f := newLeftFixture(t)
	opened := make(chan struct{})
	holdSweepUntil(f.o, opened)
	b, waitB := f.ask("b")
	f.readInboxMidTurn()
	var atStart agentcomm.WaitStatus
	var running, overlapped atomic.Int32
	f.turn = func(int32) executor.HelperOutcome {
		if running.Add(1) > 1 {
			overlapped.Add(1)
		}
		defer running.Add(-1)
		atStart = f.wait(waitB).Status
		close(opened)
		return passResultOutcome()
	}

	if err := f.o.PauseTask(context.Background(), f.rev.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := f.o.ContinueTask(context.Background(), f.rev.TaskID); err != nil {
		t.Fatal(err)
	}
	waitForRunAgentTurn(t, f.store, f.rev.TaskID, taskWait)
	f.o.concurrency.WaitAgents()

	if atStart != agentcomm.WaitCancelled {
		t.Fatalf("B's wait when the retry started = %q, want cancelled", atStart)
	}
	f.requireCancelled(waitB)
	if got := f.turns.Load(); got != 2 || overlapped.Load() != 0 {
		t.Fatalf("turns = %d overlapped = %d, want the interrupted turn and one retry, never two at once", got, overlapped.Load())
	}
	if got := repliesTo(f.store, *f.rev.MessageID); len(got) != 1 {
		t.Fatalf("replies to A = %+v, want the retry's result: A was handed again", got)
	}
	if got := repliesTo(f.store, b); len(got) != 0 {
		t.Fatalf("replies to B = %+v, want none", got)
	}
}

// A watcher fires right after a pass that read B, before the background release ran: the fire's own start settles B.
func TestLeftUnanswered_WatcherFireSettlesWhatItsLastPassLeft(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	stage := meshStageParticipantID(assistantsTestStageID)
	opened := make(chan struct{})
	holdSweepUntil(o, opened)
	started, release := make(chan struct{}), make(chan struct{})
	var passes atomic.Int32
	var waitB agentcomm.WaitID
	var atStart agentcomm.WaitStatus
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if passes.Add(1) == 1 {
			close(started)
			awaitRelease(t, release)
			return passResultOutcome(), nil
		}
		atStart = store.Snapshot().Assistants.Mesh.Waits[waitB].Status
		close(opened)
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
	waitB = w.WaitID
	close(release)
	waitForTaskStatus(t, store, id, assistants.StatusWatching, taskWait)

	o.fireWatcherTask(context.Background(), id)
	waitUntil(t, "the second pass starts", func() bool { return passes.Load() == 2 })
	o.concurrency.WaitAgents()

	if atStart != agentcomm.WaitCancelled {
		t.Fatalf("B's wait when the second pass started = %q, want cancelled", atStart)
	}
}

// B went to QA and QB. QA read it and left, but QB, held, has not seen it: it can still answer, so QA's next
// turn does not end the wait.
func TestLeftUnanswered_StartDoesNotEndAWaitAnotherRecipientCanAnswer(t *testing.T) {
	f := newLeftFixture(t)
	qb, err := f.o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-qb", ProfileID: "qa", Title: "QB", Instruction: "review", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.o.PauseTask(context.Background(), qb.TaskID); err != nil {
		t.Fatal(err)
	}
	both := openThread(t, f.o, "op-th2", 0, unreadTestStage, f.rev.ParticipantID, qb.ParticipantID)
	receipt, err := f.o.Send(context.Background(), SendRequest{
		OperationID: "send-b", ThreadID: both, Sender: unreadTestStage, Recipients: []agentcomm.ParticipantID{f.rev.ParticipantID, qb.ParticipantID},
		Kind: agentcomm.MessageKindRequest, Body: "request b", Actor: state.Actor{Kind: "assistant", ID: unreadTestStageID},
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "wait-b", Waiter: unreadTestStage, Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: receipt.MessageID},
	})
	if err != nil {
		t.Fatal(err)
	}
	readInbox(t, f.o, f.rev.ParticipantID)
	f.send("c") // unread: QA's next turn starts for it
	var atStart agentcomm.WaitStatus
	f.turn = func(int32) executor.HelperOutcome {
		atStart = f.wait(w.WaitID).Status
		return passResultOutcome()
	}
	close(f.release)
	waitUntil(t, "the turn for C ran", func() bool { return f.turns.Load() == 2 })
	f.o.concurrency.WaitAgents()

	if got := f.wait(w.WaitID).Status; atStart != agentcomm.WaitPending || got != agentcomm.WaitPending {
		t.Fatalf("B's wait at the start of the turn for C = %q, after it = %q, want pending: QB can still answer", atStart, got)
	}
}
