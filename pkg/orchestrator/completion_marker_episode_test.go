package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// The tests here pin one invariant: one completion_marker_pending_wait notice, durable and live, per episode of
// still-open waits of a stage, however its turns overlap, restart or fail to write the journal.

func countLiveEvents(live <-chan bus.Event, typ bus.EventType) int {
	n := 0
	for {
		select {
		case ev := <-live:
			if ev.Type == typ {
				n++
			}
		default:
			return n
		}
	}
}

// runStageTurn is one author turn of stage s1 whose agent does body.
func runStageTurn(o *Orchestrator, body func()) {
	o.runWithRetry(context.Background(), flow.Stage{ID: "s1"}, phaseImplementation,
		func(string) error { body(); return nil },
		func() error { return nil },
		func() {},
	)
}

func transitionS1(t *testing.T, o *Orchestrator, from, to state.StageStatus, event string) {
	t.Helper()
	if err := o.opts.Store.Apply(&state.Transition{StageID: "s1", From: from, To: to, Event: event}); err != nil {
		t.Fatal(err)
	}
}

func waitOnThread(t *testing.T, o *Orchestrator, operationID, thread string) agentcomm.WaitID {
	t.Helper()
	receipt, err := o.Wait(context.Background(), WaitRequest{
		OperationID: operationID, Waiter: meshStageParticipantID("s1"), Deadline: testFarDeadline(),
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: agentcomm.ThreadID(thread)},
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return receipt.WaitID
}

func cancelStageWait(t *testing.T, o *Orchestrator, operationID string, id agentcomm.WaitID) {
	t.Helper()
	err := o.CancelWait(context.Background(), CancelWaitRequest{
		OperationID: operationID, WaitIDs: []agentcomm.WaitID{id}, StageID: "s1", Reason: "test", Actor: ownerActor,
	})
	if err != nil {
		t.Fatalf("CancelWait: %v", err)
	}
}

// markerEpisodeFixture is stage s1 with a stage directory, a subscribed UI and a way to rewrite its marker.
type markerEpisodeFixture struct {
	t      *testing.T
	o      *Orchestrator
	runDir string
	live   <-chan bus.Event
	marker string
}

func newMarkerEpisodeFixture(t *testing.T) *markerEpisodeFixture {
	t.Helper()
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, live := o.ui.Subscribe(64)
	return &markerEpisodeFixture{t: t, o: o, runDir: runDir, live: live, marker: filepath.Join(runDir, "s1", ".done")}
}

func (f *markerEpisodeFixture) writeMarker(content string) { writeFile(f.t, f.marker, content) }

func (f *markerEpisodeFixture) resume() {
	f.t.Helper()
	transitionS1(f.t, f.o, state.StatusAwaitingPeer, state.StatusRunning, "test_resume")
}

func (f *markerEpisodeFixture) requireNotices(want int) {
	f.t.Helper()
	if n := readMarkerNotices(f.t, f.runDir); len(n) != want {
		f.t.Fatalf("durable notices = %d, want %d: %+v", len(n), want, n)
	}
}

func (f *markerEpisodeFixture) requireLive(want int) {
	f.t.Helper()
	if n := countLiveEvents(f.live, eventCompletionMarkerPendingWait); n != want {
		f.t.Fatalf("live notices = %d, want %d", n, want)
	}
}

// holdTurnAInPublisher runs turn A of s1 (marker written, wait registered) and holds it in the notice publisher,
// between its decision and its write, while Pause and Continue hand the stage over. The test runs the callback
// turn B, then calls finishA to let A go on.
func (f *markerEpisodeFixture) holdTurnAInPublisher() (finishA func()) {
	entered, release, aDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.o.markerNoticeDecidedHook = func(string) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	go func() {
		defer close(aDone)
		runStageTurn(f.o, func() {
			f.writeMarker("first")
			waitOnThread(f.t, f.o, "op-w1", "t-a")
		})
	}()
	select {
	case <-entered:
	case <-aDone:
		f.t.Fatal("turn A ended without reaching the notice publisher")
	}
	transitionS1(f.t, f.o, state.StatusRunning, state.StatusPaused, "pause")
	transitionS1(f.t, f.o, state.StatusPaused, state.StatusRunning, "continue")
	return func() {
		close(release)
		<-aDone
	}
}

// Turn A is overtaken by a callback turn B of the same pending episode: whichever publishes, the run gets one
// notice, and B's park is the stage's state.
func TestCompletionMarker_OverlappingTurnsPublishOneNotice(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	finishA := f.holdTurnAInPublisher()

	runStageTurn(f.o, func() { f.writeMarker("second, longer") })
	finishA()

	f.requireNotices(1)
	f.requireLive(1)
	if got := f.o.opts.Store.Get("s1"); got != state.StatusAwaitingPeer {
		t.Fatalf("status = %s, want awaiting_peer (B's park)", got)
	}
}

// B leaves the marker alone: A's notice is the only one, and it is not lost because Pause won the exit window.
func TestCompletionMarker_OvertakenTurnStillPublishesItsNotice(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	finishA := f.holdTurnAInPublisher()

	runStageTurn(f.o, func() {})
	finishA()

	f.requireNotices(1)
	f.requireLive(1)
}

// The episode is the waits still open: a wait that ended inside the turn must neither change the key nor appear
// in the notice, or the callback turn of the same pending wait would be reported again.
func TestCompletionMarker_EpisodeKeyIgnoresWaitsThatEndedInTheTurn(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	runStageTurn(f.o, func() {
		f.writeMarker("first")
		waitOnThread(f.t, f.o, "op-w1", "t-a")
		cancelStageWait(f.t, f.o, "op-cancel-w2", waitOnThread(f.t, f.o, "op-w2", "t-b"))
	})
	f.resume()
	runStageTurn(f.o, func() { f.writeMarker("second, longer") })

	f.requireNotices(1)
	notice := readMarkerNotices(t, f.runDir)[0]
	if len(notice.Data.Waits) != 1 || notice.Data.Waits[0].Status != string(agentcomm.WaitPending) {
		t.Fatalf("notice waits = %+v, want only the pending wait", notice.Data.Waits)
	}
}

// A wait that ended before the turn exited leaves nothing pending: no notice, and the park is the ordinary one.
func TestCompletionMarker_EpisodeEndedBeforeTheTurnExitedIsNotReported(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		f := newMarkerEpisodeFixture(t)
		runStageTurn(f.o, func() {
			f.writeMarker("first")
			cancelStageWait(t, f.o, "op-cancel-w1", waitOnThread(t, f.o, "op-w1", "t-a"))
		})

		f.requireNotices(0)
		f.requireLive(0)
	})
	t.Run("answered", func(t *testing.T) {
		batch := newBatchFixture(t, 1)
		batch.o.absRunDir = batch.o.opts.RunDir
		stageDir := filepath.Join(batch.o.opts.RunDir, "s1")
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		runStageTurn(batch.o, func() {
			writeFile(t, filepath.Join(stageDir, ".done"), "first")
			batch.mustWait("op-w1", nil, 0)
			batch.reply(0)
		})

		if n := readMarkerNotices(t, batch.o.opts.RunDir); len(n) != 0 {
			t.Fatalf("durable notices = %d, want 0: %+v", len(n), n)
		}
	})
}

// A batch is one episode: answered items stay in the notice while the batch has an item pending, the callback
// turn does not repeat it, and once the whole batch has ended a fresh marker is not reported.
func TestCompletionMarker_BatchIsOneEpisode(t *testing.T) {
	batch := newBatchFixture(t, 2)
	o, runDir := batch.o, batch.o.opts.RunDir
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var episode []agentcomm.WaitID
	runStageTurn(o, func() {
		writeFile(t, filepath.Join(stageDir, ".done"), "first")
		episode = receiptWaitIDs(batch.mustWait("op-batch", nil, 0, 1))
		batch.reply(0)
	})
	if n := readMarkerNotices(t, runDir); len(n) != 1 || len(n[0].Data.Waits) != 2 {
		t.Fatalf("notices = %+v, want one naming both items of the batch", n)
	}

	transitionS1(t, o, state.StatusAwaitingPeer, state.StatusRunning, "test_resume")
	runStageTurn(o, func() { writeFile(t, filepath.Join(stageDir, ".done"), "second, longer") })
	if n := readMarkerNotices(t, runDir); len(n) != 1 {
		t.Fatalf("callback turn: %d notices, want 1", len(n))
	}

	batch.reply(1)
	o.publishCompletionMarkerNotice("s1", ".done", episode)
	if n := readMarkerNotices(t, runDir); len(n) != 1 {
		t.Fatalf("after the batch ended: %d notices, want 1", len(n))
	}
}

// What notices.jsonl remembers survives a real restart: a new Orchestrator over the same run directory does not
// repeat the notice, and publishes nothing live.
func TestCompletionMarker_EpisodeIsRememberedAcrossARestart(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	runStageTurn(f.o, func() {
		f.writeMarker("first")
		waitOnThread(t, f.o, "op-w1", "t-a")
	})

	o2 := reopenOrchestrator(t, f.o)
	_, live2 := o2.ui.Subscribe(64)
	transitionS1(t, o2, state.StatusAwaitingPeer, state.StatusRunning, "test_resume")
	runStageTurn(o2, func() { f.writeMarker("second, longer") })

	f.requireNotices(1)
	if n := countLiveEvents(live2, eventCompletionMarkerPendingWait); n != 0 {
		t.Fatalf("live notices after the restart = %d, want 0", n)
	}
}

// A notice the journal refused is not remembered: the process that restarts publishes it, live and durable.
func TestCompletionMarker_FailedAppendIsRetriedAfterARestart(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	notices := filepath.Join(f.runDir, "notices.jsonl")
	if err := os.Mkdir(notices, 0o755); err != nil { // an append to a directory fails even as root
		t.Fatal(err)
	}
	runStageTurn(f.o, func() {
		f.writeMarker("first")
		waitOnThread(t, f.o, "op-w1", "t-a")
	})
	if err := os.Remove(notices); err != nil {
		t.Fatal(err)
	}

	o2 := reopenOrchestrator(t, f.o)
	_, live2 := o2.ui.Subscribe(64)
	transitionS1(t, o2, state.StatusAwaitingPeer, state.StatusRunning, "test_resume")
	runStageTurn(o2, func() { f.writeMarker("second, longer") })

	f.requireNotices(1)
	if n := countLiveEvents(live2, eventCompletionMarkerPendingWait); n != 1 {
		t.Fatalf("live notices after the restart = %d, want 1", n)
	}
}

// Durable first: a failed append publishes nothing live and leaves the episode to the next turn with a fresh
// marker, so the live feed and the journal never disagree.
func TestCompletionMarker_FailedAppendPublishesNothingAndIsRetriedByTheNextTurn(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	notices := filepath.Join(f.runDir, "notices.jsonl")
	if err := os.Mkdir(notices, 0o755); err != nil {
		t.Fatal(err)
	}
	runStageTurn(f.o, func() {
		f.writeMarker("first")
		waitOnThread(t, f.o, "op-w1", "t-a")
	})
	f.requireLive(0)
	f.requireNotices(0)

	if err := os.Remove(notices); err != nil {
		t.Fatal(err)
	}
	f.resume()
	runStageTurn(f.o, func() { f.writeMarker("second, longer") })

	f.requireNotices(1)
	f.requireLive(1)
}

// Publishers racing for one episode — the turns of a stage, however they overlap — produce one notice.
func TestCompletionMarker_ConcurrentPublishersOfOneEpisodeWriteOneNotice(t *testing.T) {
	f := newMarkerEpisodeFixture(t)
	waitID := registerTestStageWait(t, f.o, "s1", "op-stress")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			<-start
			f.o.publishCompletionMarkerNotice("s1", ".done", []agentcomm.WaitID{waitID})
		})
	}
	close(start)
	wg.Wait()

	f.requireNotices(1)
	f.requireLive(1)
}
