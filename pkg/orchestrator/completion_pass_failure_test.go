package orchestrator

import (
	"context"
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
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A failed stage never produces the result its on_completion helpers wait for. Their passes that have not started
// are cancelled with a system reason, so nothing waits for them forever, and a Retry of the stage gives them back
// to the new attempt.

// failedPassReasonPrefix opens the reason of a pass cancelled by its stage's failure; a Retry restores only those.
const failedPassReasonPrefix = "parent stage failed before completion: "

// requirePassCancelled fails unless id was cancelled by its stage's latest failure before any turn started: no
// start record either.
func requirePassCancelled(t *testing.T, o *Orchestrator, turns *turnCounter, id assistants.TaskID) {
	t.Helper()
	requireCancelledByFailure(t, o, turns, id)
	if got := taskStartRecords(t, o, id); len(got) != 0 {
		t.Fatalf("helper start recorded for a stage without a result: %q", got)
	}
}

// requireCancelledByFailure fails unless id was cancelled by its stage's latest failure without running: no
// process, no result or waiver, and no watcher binding left. turns nil: the process count is not checked.
func requireCancelledByFailure(t *testing.T, o *Orchestrator, turns *turnCounter, id assistants.TaskID) {
	t.Helper()
	rs := o.opts.Store.Snapshot()
	tk := rs.Assistants.Tasks[id]
	want := fmt.Sprintf("%s%s, seq=%d", failedPassReasonPrefix, tk.RootStageID, rs.Stages[tk.RootStageID].Seq)
	if tk.Status != assistants.StatusCancelled || tk.StatusReason != want {
		t.Fatalf("helper = %s (%q), want cancelled (%q)", tk.Status, tk.StatusReason, want)
	}
	if turns != nil && turns.of(id) != 0 {
		t.Fatalf("helper ran %d turn(s) for a stage without a result", turns.of(id))
	}
	if tk.LastVerdict != "" || tk.Waived {
		t.Fatalf("helper verdict %q, waived %t: a cancelled pass records neither", tk.LastVerdict, tk.Waived)
	}
	// The binding is dropped in the commit's callback, just after the new status becomes visible.
	waitForTrue(t, func() bool {
		_, _, _, bound := o.WatcherStatus(id)
		return !bound
	}, 2*time.Second, "a cancelled pass is still bound as a watcher")
}

// requireRunNotHeld fails if a helper task still keeps the run from finishing.
func requireRunNotHeld(t *testing.T, store *state.Store) {
	t.Helper()
	rs := store.Snapshot()
	if blocker, held := rs.Assistants.RunBlocker(); held {
		t.Fatalf("run held by task %s (%s)", blocker.ID, blocker.Status)
	}
	if queuedTurnDue(rs) {
		t.Fatal("a queued turn still holds the run")
	}
}

// requirePassRestored fails unless id waits again for its stage's result in a new task generation, bound as a
// watcher like a freshly attached helper.
func requirePassRestored(t *testing.T, o *Orchestrator, id assistants.TaskID, generationBefore uint64) {
	t.Helper()
	tk := o.opts.Store.Snapshot().Assistants.Tasks[id]
	if tk.Status != assistants.StatusWatching || tk.TaskGeneration != generationBefore+1 {
		t.Fatalf("helper = %s (%q), generation %d; want watching in generation %d", tk.Status, tk.StatusReason, tk.TaskGeneration, generationBefore+1)
	}
	if _, _, _, bound := o.WatcherStatus(id); !bound {
		t.Fatal("a restored pass is not bound as a watcher")
	}
}

// failTestStage triggers event on the test stage as its live path does, with everything triggerWithSeq runs after it.
func failTestStage(t *testing.T, o *Orchestrator, event bus.FSMEvent) {
	t.Helper()
	if _, ok := o.Trigger(assistantsTestStageID, event, bus.GuardCtx{}, "boom"); !ok {
		t.Fatalf("%s refused (stage %s)", event, o.currentStatus(assistantsTestStageID))
	}
}

// requireStillCancelledWhenAsked fails unless id stays cancelled by the failure, without a turn, when the paths that
// start passes ask for it after the failure.
func requireStillCancelledWhenAsked(t *testing.T, o *Orchestrator, turns *turnCounter, id assistants.TaskID) {
	t.Helper()
	ctx := context.Background()
	o.issueCompletionPasses(ctx, assistantsTestStageID)
	o.maybeScheduleTask(ctx, id)
	requirePassCancelled(t, o, turns, id)
	requireRunNotHeld(t, o.opts.Store)
}

// Whatever the stage's status and the failure, the pass that has not started is cancelled by the failure itself,
// before anything else asks for it, and stays so. Rows the FSM refuses are absent: EvVerifyFail from pending,
// planning, revising, ready, paused, hook_failed or awaiting_quota, and EvBlockedByDep from anything but pending.
func TestCompletionHelper_ParentFailureCancelsThePendingPass(t *testing.T) {
	afterStart := func(events ...bus.FSMEvent) []bus.FSMEvent {
		return append([]bus.FSMEvent{bus.EvReady, bus.EvStartRun}, events...)
	}
	planned := []bus.FSMEvent{bus.EvStartPlanning, bus.EvPlanReady}
	for _, src := range []struct {
		status state.StageStatus
		path   []bus.FSMEvent // what brings a pending stage to status
		fails  []bus.FSMEvent
	}{
		{state.StatusPending, nil, []bus.FSMEvent{bus.EvFail, bus.EvBlockedByDep}},
		{state.StatusPlanning, planned[:1], []bus.FSMEvent{bus.EvFail}},
		{state.StatusAwaitingApproval, planned, []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail}},
		{state.StatusRevising, append(slices.Clone(planned), bus.EvRevise), []bus.FSMEvent{bus.EvFail}},
		{state.StatusReady, []bus.FSMEvent{bus.EvReady}, []bus.FSMEvent{bus.EvFail}},
		{state.StatusRunning, afterStart(), []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail}},
		{state.StatusRetrying, afterStart(bus.EvScheduleRetry), []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail}},
		{state.StatusPaused, afterStart(bus.EvPause), []bus.FSMEvent{bus.EvFail}},
		{state.StatusAwaitingUserInput, afterStart(bus.EvAskUser), []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail}},
		{state.StatusHookFailed, afterStart(bus.EvHookFailed), []bus.FSMEvent{bus.EvFail}},
		{state.StatusAwaitingPeer, afterStart(bus.EvAwaitPeer), []bus.FSMEvent{bus.EvFail, bus.EvVerifyFail}},
		{state.StatusAwaitingQuota, afterStart(bus.EvAwaitQuota), []bus.FSMEvent{bus.EvFail}},
	} {
		for _, event := range src.fails {
			for _, mode := range []assistants.Mode{assistants.ModeRequired, assistants.ModeAdvisory} {
				t.Run(fmt.Sprintf("%s/%s/%s", src.status, event, mode), func(t *testing.T) {
					o, store := newAssistantsTestOrchestrator(t, true)
					turns := countTurns(t, o, nil)
					for _, ev := range src.path {
						commitStageEvent(t, o, assistantsTestStageID, ev)
					}
					if got := store.Get(assistantsTestStageID); got != src.status {
						t.Fatalf("stage = %s, want %s", got, src.status)
					}
					helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, mode)

					failTestStage(t, o, event)

					requirePassCancelled(t, o, turns, helper)
					requireStillCancelledWhenAsked(t, o, turns, helper)
				})
			}
		}
	}
}

// A pass already in the queue is cancelled by the failure too, also when the task is paused or its stage had its
// result before failing.
func TestCompletionHelper_ParentFailureCancelsAQueuedPass(t *testing.T) {
	pauseTask := func(t *testing.T, o *Orchestrator, helper assistants.TaskID) {
		t.Helper()
		if err := o.PauseTask(context.Background(), helper); err != nil {
			t.Fatalf("PauseTask: %v", err)
		}
	}
	for _, tc := range []struct {
		name string
		// setup brings the queued pass of the running test stage where the failure finds it.
		setup func(t *testing.T, o *Orchestrator, helper assistants.TaskID)
	}{
		{"queued pass of a running stage", func(*testing.T, *Orchestrator, assistants.TaskID) {}},
		{"paused pass of a stage awaiting its assistants", func(t *testing.T, o *Orchestrator, helper assistants.TaskID) {
			pauseTask(t, o, helper)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
		}},
		{"paused pass of a stage paused while awaiting its assistants", func(t *testing.T, o *Orchestrator, helper assistants.TaskID) {
			pauseTask(t, o, helper)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvPause)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			turns := countTurns(t, o, nil)
			helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
			tc.setup(t, o, helper)

			failTestStage(t, o, bus.EvFail)

			requirePassCancelled(t, o, turns, helper)
			requireStillCancelledWhenAsked(t, o, turns, helper)
		})
	}
}

// Only a pass that has not started is the failure's to cancel: a helper already running finishes on its own, and a
// checkpoints watcher does not wait for the stage's result.
func TestCompletionHelper_ParentFailureLeavesOtherHelpers(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	turns := countTurns(t, o, release)
	running := queueCompletionHelper(t, o, store, "op-running", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	o.maybeScheduleTask(context.Background(), running)
	waitUntil(t, "the helper's process started", func() bool { return turns.of(running) == 1 })
	watcher := attachWatcherTask(t, o, "op-watcher", assistantsTestStageID, assistants.TriggerCheckpoints, assistants.ModeAdvisory)

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail refused")
	}
	close(release)

	waitForTaskStatus(t, store, running, assistants.StatusCompleted, 2*time.Second)
	if got := turns.of(running); got != 1 {
		t.Fatalf("running helper ran %d turns, want its one turn to finish", got)
	}
	if got := store.Snapshot().Assistants.Tasks[watcher].Status; got != assistants.StatusWatching {
		t.Fatalf("checkpoints watcher = %s, want watching", got)
	}
}

// Whoever waits for the result of such a helper learns that its task ended: the wait does not outlive the
// cancelled pass.
func TestCompletionHelper_ParentFailureEndsTheWaitForItsResult(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "other", Command: "claude"})
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, "other")
	turns := countTurns(t, o, nil)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	other := meshStageParticipantID("other")
	wait, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-other-waits", Waiter: other, Actor: helperActor(other), Condition: taskResultOf(helper),
	})
	if err != nil || wait.Status != agentcomm.WaitPending {
		t.Fatalf("wait of another stage for the helper: %+v, %v", wait, err)
	}

	if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail refused")
	}

	requirePassCancelled(t, o, turns, helper)
	if got := store.Snapshot().Assistants.Mesh.Waits[wait.WaitID].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait for the cancelled helper's result = %s, want ready", got)
	}
}

// attachCompletionPass attaches a required on_completion helper to the test stage as a user would.
func attachCompletionPass(o *Orchestrator, opID string) (assistants.TaskID, error) {
	return o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "check",
		Trigger: assistants.TriggerOnCompletion, Mode: assistants.ModeRequired, Actor: state.Actor{Kind: "user"},
	})
}

// holdFirstBindingChange makes the first watcher binding change after the call wait, once it read the task, until
// the returned release is called; decided is closed when it waits. Later changes go through.
func holdFirstBindingChange(t *testing.T, o *Orchestrator) (decided <-chan struct{}, release func()) {
	t.Helper()
	reached, released := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	o.watcherBindingDecidedHook = func(assistants.TaskID) {
		if first.CompareAndSwap(false, true) {
			close(reached)
			awaitRelease(t, released)
		}
	}
	var once sync.Once
	release = func() { once.Do(func() { close(released) }) }
	t.Cleanup(release)
	return reached, release
}

// awaitSignal waits for ch, failing the test if it does not come in time.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting: %s", what)
	}
}

// An Attach and the stage's failure leave no pass behind in whatever order they meet. Committed first, the pass is
// cancelled, also when the failure commits between the Attach and its watcher's binding. Planned on a snapshot from
// before the failure, the Attach is refused at its commit; after the failure, at once.
func TestCompletionHelper_ParentFailureAndAttach(t *testing.T) {
	t.Run("failure between the Attach's commit and its watcher's binding", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true)
		activateStage(t, o, assistantsTestStageID)
		turns := countTurns(t, o, nil)
		decided, release := holdFirstBindingChange(t, o)
		var helper assistants.TaskID
		attached := make(chan error, 1)
		go func() {
			var err error
			helper, err = attachCompletionPass(o, "op-helper")
			attached <- err
		}()
		awaitSignal(t, decided, "the Attach read its task to bind it")

		failed := make(chan struct{})
		go func() {
			if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
				t.Error("EvFail refused")
			}
			close(failed)
		}()
		waitUntil(t, "the failure cancelled the attached pass", func() bool {
			for _, tk := range store.Snapshot().Assistants.Tasks {
				if tk.Status == assistants.StatusCancelled {
					return true
				}
			}
			return false
		})
		release()
		if err := <-attached; err != nil {
			t.Fatalf("Attach: %v", err)
		}
		awaitSignal(t, failed, "the failure finished")

		requirePassCancelled(t, o, turns, helper)
		requireRunNotHeld(t, store)
	})

	t.Run("failure between the Attach's snapshot and its commit", func(t *testing.T) {
		o, _ := newAssistantsTestOrchestrator(t, true)
		activateStage(t, o, assistantsTestStageID)
		o.testAfterAttachSnapshot = func() { failTestStage(t, o, bus.EvFail) }

		_, err := attachCompletionPass(o, "op-helper")

		requireRejected(t, err, assistants.CodeInvalidStatus)
		requireNoPassOfTheFailedStage(t, o)
	})

	t.Run("Attach after the failure", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true)
		activateStage(t, o, assistantsTestStageID)
		turns := countTurns(t, o, nil)
		before, err := attachCompletionPass(o, "op-before")
		if err != nil {
			t.Fatalf("Attach: %v", err)
		}
		failTestStage(t, o, bus.EvFail)

		_, err = attachCompletionPass(o, "op-after")

		requirePassCancelled(t, o, turns, before)
		if err == nil {
			t.Fatal("Attach to a failed stage was accepted")
		}
		if n := len(store.Snapshot().Assistants.ByRootStage[assistantsTestStageID]); n != 1 {
			t.Fatalf("stage has %d helpers, want only the one attached before the failure", n)
		}
		requireRunNotHeld(t, store)
	})
}

// requireNoPassOfTheFailedStage fails if the test stage has a helper, a watcher binding, or a task holding the run.
func requireNoPassOfTheFailedStage(t *testing.T, o *Orchestrator) {
	t.Helper()
	if n := len(o.opts.Store.Snapshot().Assistants.ByRootStage[assistantsTestStageID]); n != 0 {
		t.Fatalf("failed stage has %d helpers, want none", n)
	}
	o.watchMu.Lock()
	bound := len(o.watchByTask)
	o.watchMu.Unlock()
	if bound != 0 {
		t.Fatalf("%d watcher(s) bound, want none", bound)
	}
	requireRunNotHeld(t, o.opts.Store)
}

// The failure's cancellation and the Retry that restores the pass leave the watcher bound as the journal has the task
// last: a cancellation that reaches the binding late does not unbind the restored pass.
func TestCompletionHelper_ParentFailureRetryBindsTheRestoredPassLast(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	declStageSetup(t, o, assistantsTestStageID)
	o.activationHeld.Store(true)
	activateStage(t, o, assistantsTestStageID)
	turns := countTurns(t, o, nil)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration
	decided, release := holdFirstBindingChange(t, o)
	failed := make(chan struct{})
	go func() {
		if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
			t.Error("EvFail refused")
		}
		close(failed)
	}()
	awaitSignal(t, decided, "the cancellation read its task to unbind it")
	if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusCancelled {
		t.Fatalf("helper = %s, want cancelled before the Retry", got)
	}

	retried := make(chan struct{})
	go func() {
		o.retryStage(context.Background(), assistantsTestStageID)
		close(retried)
	}()
	waitUntil(t, "the Retry restored the pass", func() bool {
		return store.Snapshot().Assistants.Tasks[helper].TaskGeneration == generation+1
	})
	release()
	awaitSignal(t, failed, "the failure finished")
	awaitSignal(t, retried, "the Retry finished")

	requirePassRestored(t, o, helper, generation)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 0 {
		t.Fatalf("restored helper ran %d turn(s) before the new attempt's result", got)
	}
}

// restartHeld recovers the run in a new process as restartAfterCrash does, with stage activation held: the restart
// settles stages and helpers and starts no stage agent. configure (nil: none) adjusts the new process first.
func restartHeld(t *testing.T, o *Orchestrator, store *state.Store, configure func(*Orchestrator)) (*Orchestrator, *state.Store, *turnCounter) {
	t.Helper()
	store.Close()
	o2, store2 := reopenAssistantsTestOrchestrator(t, o)
	o2.activationHeld.Store(true)
	if configure != nil {
		configure(o2)
	}
	turns := countTurns(t, o2, nil)
	ctx := context.Background()
	o2.startPlanningForPending(ctx)
	o2.recoverCollaboration(ctx)
	return o2, store2, turns
}

// cancelRecords are the journal lines that cancelled id.
func cancelRecords(t *testing.T, o *Orchestrator, id assistants.TaskID) []string {
	t.Helper()
	var out []string
	for _, line := range operationRecords(t, o.opts.RunDir, "finish_task") {
		if strings.Contains(line, string(id)) && strings.Contains(line, string(assistants.StatusCancelled)) {
			out = append(out, line)
		}
	}
	return out
}

// A failure the cancellation did not follow — a crash right after it, or a journal of an older binary — is settled
// when the run resumes, before any helper starts, and only once: a second restart records nothing new and reads the
// same outcome.
func TestCompletionHelper_ParentFailureRecoveredAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name string
		// journal records the helper's and the stage's last steps before the crash, without what follows them live.
		journal func(t *testing.T, o *Orchestrator, store *state.Store, helper assistants.TaskID)
	}{
		{"helper watching", func(t *testing.T, o *Orchestrator, _ *state.Store, _ assistants.TaskID) {
			commitStageEvent(t, o, assistantsTestStageID, bus.EvFail)
		}},
		{"pass queued", func(t *testing.T, o *Orchestrator, store *state.Store, helper assistants.TaskID) {
			commitTaskStatus(t, store, helper, assistants.StatusQueued)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvFail)
		}},
		{"turn cut short by the crash", func(t *testing.T, o *Orchestrator, store *state.Store, helper assistants.TaskID) {
			commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
			commitTaskStatus(t, store, helper, assistants.StatusQueued)
			commitTaskStatus(t, store, helper, assistants.StatusRunning)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvFail)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			o.opts.Config.AutoRecover = new(false)
			activateStage(t, o, assistantsTestStageID)
			helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
			tc.journal(t, o, store, helper)

			o2, store2, turns := restartHeld(t, o, store, nil)

			requirePassCancelled(t, o2, turns, helper)
			requireRunNotHeld(t, store2)
			if got := cancelRecords(t, o2, helper); len(got) != 1 {
				t.Fatalf("cancel records after the restart = %d, want 1", len(got))
			}

			o3, store3, turns := restartHeld(t, o2, store2, nil)

			requirePassCancelled(t, o3, turns, helper)
			requireRunNotHeld(t, store3)
			if got := cancelRecords(t, o3, helper); len(got) != 1 {
				t.Fatalf("cancel records after a second restart = %d, want still 1", len(got))
			}
		})
	}
}

// A turn that waited for its command slot while its stage failed goes back to the queue once it gets the slot, and
// there its pass ends: no start, and no turn left in the queue forever — also when the task was paused meanwhile.
func TestCompletionHelper_ParentFailureLateRequeue(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("paused=%t", paused), func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			activateStage(t, o, assistantsTestStageID)
			turns := countTurns(t, o, nil)
			helper, slot := startCompletionHelperBehindASlot(t, o, store)
			if paused {
				if err := o.PauseTask(context.Background(), helper); err != nil {
					t.Fatalf("PauseTask: %v", err)
				}
			}
			if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
				t.Fatal("EvFail refused")
			}
			<-slot

			// The start was committed while the stage still had its result; no process ran.
			waitForTaskStatus(t, store, helper, assistants.StatusCancelled, 2*time.Second)
			o.concurrency.WaitAgents()
			requireCancelledByFailure(t, o, turns, helper)
			requireRunNotHeld(t, store)
		})
	}
}

// The scheduler cancels a pass of a failed stage before anything that would only defer it: a pause of the task, a
// provider hold, or a resumed run that has not settled its helpers yet.
func TestCompletionHelper_ParentFailureLateSchedule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) (*Orchestrator, *state.Store)
		// defer makes the scheduler hold the queued pass back, as it would for a stage that has its result.
		deferPass func(t *testing.T, o *Orchestrator, helper assistants.TaskID)
	}{
		{"paused task", func(t *testing.T) (*Orchestrator, *state.Store) {
			return newAssistantsTestOrchestrator(t, true)
		}, func(t *testing.T, o *Orchestrator, helper assistants.TaskID) {
			if err := o.PauseTask(context.Background(), helper); err != nil {
				t.Fatalf("PauseTask: %v", err)
			}
		}},
		{"provider hold", func(t *testing.T) (*Orchestrator, *state.Store) {
			o, store, _ := newHelperHoldOrch(t)
			return o, store
		}, func(t *testing.T, o *Orchestrator, _ assistants.TaskID) {
			parkStageForTest(t, o, "other", holdHelperPark(holdNow.Add(time.Hour)))
		}},
		{"resumed run before its helpers settle", func(t *testing.T) (*Orchestrator, *state.Store) {
			return newAssistantsTestOrchestrator(t, true)
		}, func(_ *testing.T, o *Orchestrator, _ assistants.TaskID) {
			o.opts.Resumed = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := tc.build(t)
			turns := countTurns(t, o, nil)
			activateStage(t, o, assistantsTestStageID)
			helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
			tc.deferPass(t, o, helper)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvFail)

			o.maybeScheduleTask(context.Background(), helper)

			requirePassCancelled(t, o, turns, helper)
			requireRunNotHeld(t, store)
		})
	}
}

// A turn that got its command slot under a provider hold goes back to the queue. A stage that failed after the
// turn's start was committed ends that pass right there, not when the hold lifts an hour later.
func TestCompletionHelper_ParentFailureBeforeAHoldRequeue(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	turns := countTurns(t, o, nil)
	activateStage(t, o, assistantsTestStageID)
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	commitTaskStatus(t, store, helper, assistants.StatusRunning) // its start, committed while the stage had its result
	parkStageForTest(t, o, "other", holdHelperPark(holdNow.Add(time.Hour)))
	o.taskRequeueBarrier = func(assistants.TaskID) {
		if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
			t.Error("EvFail refused")
		}
	}

	o.spawnTask(context.Background(), helper, store.Snapshot().Assistants.Tasks[helper].Task)

	waitForTaskStatus(t, store, helper, assistants.StatusCancelled, 2*time.Second)
	requireCancelledByFailure(t, o, turns, helper)
	requireRunNotHeld(t, store)
}

// A pass that was running when its stage failed finishes on its own. Cut short back into the queue — paused, or
// parked on a usage limit — it is a pass that never produced its result: it ends there, instead of waiting for a
// Continue or for the limit to reset.
func TestCompletionHelper_ParentFailureThenTheRunningPassRequeues(t *testing.T) {
	for _, tc := range []struct {
		name string
		// cut ends the running pass's turn, whose process returns what the fake returns.
		cut func(t *testing.T, o *Orchestrator, helper assistants.TaskID, release func())
		run func(ctx context.Context, release <-chan struct{}) (executor.HelperOutcome, error)
	}{
		{"paused", func(t *testing.T, o *Orchestrator, helper assistants.TaskID, _ func()) {
			if err := o.PauseTask(context.Background(), helper); err != nil {
				t.Fatalf("PauseTask: %v", err)
			}
		}, func(ctx context.Context, _ <-chan struct{}) (executor.HelperOutcome, error) {
			<-ctx.Done()
			return executor.HelperOutcome{}, nil
		}},
		{"usage limit", func(_ *testing.T, _ *Orchestrator, _ assistants.TaskID, release func()) {
			release()
		}, func(_ context.Context, release <-chan struct{}) (executor.HelperOutcome, error) {
			<-release
			return limitOutcome(helperLimit(holdNow.Add(time.Hour)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store, _ := newQuotaOrch(t)
			activateStage(t, o, assistantsTestStageID)
			started, released := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(released) }) }
			t.Cleanup(release)
			o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
				close(started)
				return tc.run(ctx, released)
			}
			helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
			commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
			o.maybeScheduleTask(context.Background(), helper)
			awaitSignal(t, started, "the pass started")
			failTestStage(t, o, bus.EvFail)
			if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusRunning {
				t.Fatalf("running pass = %s after its stage failed, want still running", got)
			}

			tc.cut(t, o, helper, release)

			waitForTaskStatus(t, store, helper, assistants.StatusCancelled, 2*time.Second)
			requireCancelledByFailure(t, o, nil, helper)
			requireRunNotHeld(t, store)
		})
	}
}

// countingClock counts the waits armed on its fake clock, each once it is registered: an Advance after the count
// reaches the wait.
type countingClock struct {
	*clock.FakeClock
	armed atomic.Int32
}

func (c *countingClock) After(d time.Duration) <-chan time.Time {
	ch := c.FakeClock.After(d)
	c.armed.Add(1)
	return ch
}

// A pass of a stage that keeps its result parks on a usage limit as before: one wake timer, and once the limit resets
// the pass runs again and its verdict completes the stage.
func TestCompletionHelper_UsageLimitParkArmsOneWake(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	counting := &countingClock{FakeClock: clk}
	o.clock = counting
	h := limitFirst(holdNow.Add(2 * time.Minute))
	o.runHelperAgent = h.run
	activateStage(t, o, assistantsTestStageID)
	helper := queueCompletionHelper(t, o, store, "op-helper", assistants.ModeRequired)
	commitStageEvent(t, o, assistantsTestStageID, bus.EvAwaitAssistants)
	o.maybeScheduleTask(context.Background(), helper)

	waitForQuotaPark(t, store, helper)
	// Other waits share the fake clock, so the wake timer is awaited by its own count, not BlockUntil.
	waitUntil(t, "the park armed its wake timer", func() bool { return counting.armed.Load() >= 1 })
	neverTrue(t, "a second wake timer armed for one park", func() bool { return counting.armed.Load() > 1 })
	clk.Advance(4 * time.Minute) // past the reset (2 min) and its margin (1 min ±6 s)

	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 5*time.Second)
	calledExactly(t, h, 2, "the parked pass ran again once")
}

// failedStageWithCancelledPass is the test stage with an on_completion helper whose pass the stage's failure
// cancelled. The stage has its plan and its activation is held, so a Retry leaves the new attempt ready without
// starting an agent.
func failedStageWithCancelledPass(t *testing.T, mode assistants.Mode) (*Orchestrator, *state.Store, *turnCounter, assistants.TaskID) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	declStageSetup(t, o, assistantsTestStageID)
	o.activationHeld.Store(true)
	turns := countTurns(t, o, nil)
	activateStage(t, o, assistantsTestStageID)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, mode)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail refused")
	}
	requirePassCancelled(t, o, turns, helper)
	return o, store, turns, helper
}

// completeRetriedAttempt starts the attempt a Retry left ready and finishes its author's work, as its agent would.
func completeRetriedAttempt(t *testing.T, o *Orchestrator) {
	t.Helper()
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
		t.Fatalf("start of the retried attempt refused (stage %s)", o.currentStatus(assistantsTestStageID))
	}
	o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
}

// A Retry of the stage gives the cancelled pass back to the new attempt: the helper waits for the new result, starts
// once when it comes, and its verdict completes the stage — no waiver and no new Attach needed.
func TestCompletionHelper_ParentFailureRetryRestoresThePass(t *testing.T) {
	for _, mode := range []assistants.Mode{assistants.ModeRequired, assistants.ModeAdvisory} {
		t.Run(string(mode), func(t *testing.T) {
			o, store, turns, helper := failedStageWithCancelledPass(t, mode)
			generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration

			o.retryStage(context.Background(), assistantsTestStageID)

			requirePassRestored(t, o, helper, generation)
			o.concurrency.WaitAgents()
			if got := turns.of(helper); got != 0 {
				t.Fatalf("restored helper ran %d turn(s) before the new attempt's result", got)
			}
			completeRetriedAttempt(t, o)
			waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
			waitStageStatus(t, store, assistantsTestStageID, state.StatusDone, 2*time.Second)
			o.concurrency.WaitAgents()
			if got := turns.of(helper); got != 1 {
				t.Fatalf("restored helper ran %d turns, want 1", got)
			}
		})
	}
}

// A helper declared in flow.yaml is the same task on every attempt of one generation (its Attach replays), so the
// Retry's restore is what lets the new attempt have its pass at all.
func TestCompletionHelper_ParentFailureRetryRestoresADeclaredHelper(t *testing.T) {
	stage := flow.Stage{ID: "decl", Agents: []flow.AgentType{flow.AgentAuto}, Assistants: flow.StageAssistants{{
		Profile: "reviewer", Mode: assistants.ModeRequired, Trigger: assistants.TriggerOnCompletion,
	}}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	declStageSetup(t, o, stage.ID)
	runner := &scriptedStageRunner{turn: func(n int32) error {
		if n == 1 {
			o.Trigger(stage.ID, bus.EvFail, bus.GuardCtx{}, "boom")
			return errors.New("the first attempt failed")
		}
		return nil
	}}
	o.opts.Runner, o.runner = runner, runner
	turns := countTurns(t, o, nil)
	readyAndStart(t, o, stage.ID, stage)
	waitStageStatus(t, store, stage.ID, state.StatusFailed, 2*time.Second)
	o.concurrency.WaitAgents()
	helper := waitForAssistantCount(t, store, stage.ID, 1, 2*time.Second)[0]
	requirePassCancelled(t, o, turns, helper)
	generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration

	o.retryStage(context.Background(), stage.ID)
	o.concurrency.WaitAgents()
	if got := runner.turns.Load(); got != 2 {
		t.Fatalf("stage turns = %d, want the retried attempt's turn", got)
	}
	// The run loop completes the stage on its agent's result; this test has no loop.
	o.completeStage(context.Background(), stage.ID, state.StatusRunning, "main done")

	waitForTaskStatus(t, store, helper, assistants.StatusCompleted, 2*time.Second)
	waitStageStatus(t, store, stage.ID, state.StatusDone, 2*time.Second)
	o.concurrency.WaitAgents()
	if got := turns.of(helper); got != 1 {
		t.Fatalf("declared helper ran %d turns, want 1 after the Retry", got)
	}
	if got := store.Snapshot().Assistants.Tasks[helper].TaskGeneration; got != generation+1 {
		t.Fatalf("declared helper generation = %d, want %d", got, generation+1)
	}
	if n := len(store.Snapshot().Assistants.ByRootStage[stage.ID]); n != 1 {
		t.Fatalf("stage has %d helpers after the Retry, want the declared one only", n)
	}
}

// A Retry the run takes on its own or that a crash interrupted restores the pass too: auto_recover at startup, a
// Retry committed right before a crash, and the latter with collaboration switched off for the resumed run.
func TestCompletionHelper_ParentFailureRetryRestoredAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		before    func(t *testing.T, o *Orchestrator) // what the journal holds after the failure, before the crash
		configure func(o *Orchestrator)
	}{
		{"auto_recover", func(*testing.T, *Orchestrator) {}, nil},
		{"crash between the Retry and the restore", func(t *testing.T, o *Orchestrator) {
			commitStageEvent(t, o, assistantsTestStageID, bus.EvManualRetry)
		}, func(o *Orchestrator) { o.opts.Config.AutoRecover = new(false) }},
		{"collaboration off after the crash", func(t *testing.T, o *Orchestrator) {
			commitStageEvent(t, o, assistantsTestStageID, bus.EvManualRetry)
		}, func(o *Orchestrator) {
			o.opts.Config.AutoRecover = new(false)
			o.opts.AssistantsEnabled = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store, _, helper := failedStageWithCancelledPass(t, assistants.ModeRequired)
			generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration
			tc.before(t, o)

			o2, store2, turns := restartHeld(t, o, store, tc.configure)

			requirePassRestored(t, o2, helper, generation)
			if got := store2.Snapshot().Stages[assistantsTestStageID].Status; got == state.StatusFailed {
				t.Fatal("the stage is still failed after its Retry")
			}
			o2.concurrency.WaitAgents()
			if got := turns.of(helper); got != 0 {
				t.Fatalf("restored helper ran %d turn(s) before the new attempt's result", got)
			}
		})
	}
}

// A pass attached while its stage was pending waited for the stage to start; restored, it waits for the stage's
// result like the pass of a started stage.
func TestCompletionHelper_ParentFailureRetryRestoresAWaitingParentPass(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	declStageSetup(t, o, assistantsTestStageID)
	o.activationHeld.Store(true)
	turns := countTurns(t, o, nil)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	if got := store.Snapshot().Assistants.Tasks[helper].Status; got != assistants.StatusWaitingParent {
		t.Fatalf("helper attached to a pending stage = %s, want waiting_parent", got)
	}
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvBlockedByDep, bus.GuardCtx{}, "dep failed"); !ok {
		t.Fatal("EvBlockedByDep refused")
	}
	requirePassCancelled(t, o, turns, helper)
	generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration

	o.retryStage(context.Background(), assistantsTestStageID)

	requirePassRestored(t, o, helper, generation)
}

// The stage may fail again between its Retry and the restore (its dependency failed meanwhile), or after the
// restore: the pass is cancelled again by that failure and the next Retry restores it again.
func TestCompletionHelper_ParentFailureRetryRacesADependencyFailure(t *testing.T) {
	const depID = "dep"
	stages := []flow.Stage{{ID: depID, Command: "claude"}, {ID: assistantsTestStageID, Command: "claude", DependsOn: []string{depID}}}
	o, store := newMultiStageBarrierTestOrchestrator(t, stages)
	declStageSetup(t, o, assistantsTestStageID)
	o.activationHeld.Store(true)
	turns := countTurns(t, o, nil)
	activateStage(t, o, assistantsTestStageID)
	helper := attachWatcherTask(t, o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeRequired)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
		t.Fatal("EvFail refused")
	}
	requirePassCancelled(t, o, turns, helper)
	generation := store.Snapshot().Assistants.Tasks[helper].TaskGeneration

	// The Retry is committed, and the dependency's failure blocks the stage before anything restored the pass.
	commitStageEvent(t, o, assistantsTestStageID, bus.EvManualRetry)
	if _, ok := o.Trigger(assistantsTestStageID, bus.EvBlockedByDep, bus.GuardCtx{}, "dep failed"); !ok {
		t.Fatal("EvBlockedByDep refused")
	}
	o.retryStage(context.Background(), assistantsTestStageID)
	requirePassRestored(t, o, helper, generation)

	// The restored pass waits for the stage, whose dependency still has not run: the dependency fails, and so does
	// the stage again.
	if _, ok := o.Trigger(depID, bus.EvFail, bus.GuardCtx{}, "dep failed"); !ok {
		t.Fatal("EvFail of the dependency refused")
	}
	o.failBlockedStages()
	requirePassCancelled(t, o, turns, helper)

	o.retryStage(context.Background(), assistantsTestStageID)
	requirePassRestored(t, o, helper, generation+1)
}

// A Retry gives back only what the failure took: a helper the user cancelled, a helper that failed on its own, and
// the helpers of an attempt whose generation a dead required helper ended stay as they are.
func TestCompletionHelper_ParentFailureRetryRestoresNothingElse(t *testing.T) {
	t.Run("cancelled by the user, failed on its own", func(t *testing.T) {
		o, store := newAssistantsTestOrchestrator(t, true)
		declStageSetup(t, o, assistantsTestStageID)
		o.activationHeld.Store(true)
		countTurns(t, o, nil)
		activateStage(t, o, assistantsTestStageID)
		cancelled := attachWatcherTask(t, o, "op-cancelled", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		failed := attachWatcherTask(t, o, "op-failed", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		if err := o.CancelTask(context.Background(), cancelled); err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
		commitTaskStatus(t, store, failed, assistants.StatusFailed)
		before := store.Snapshot().Assistants.Tasks
		if _, ok := o.Trigger(assistantsTestStageID, bus.EvFail, bus.GuardCtx{}, "boom"); !ok {
			t.Fatal("EvFail refused")
		}

		o.retryStage(context.Background(), assistantsTestStageID)

		after := store.Snapshot().Assistants.Tasks
		for _, id := range []assistants.TaskID{cancelled, failed} {
			if after[id].Status != before[id].Status || after[id].StatusReason != before[id].StatusReason || after[id].TaskGeneration != before[id].TaskGeneration {
				t.Fatalf("task %s = %s (%q) gen %d after the Retry, want it untouched: %s (%q) gen %d", id,
					after[id].Status, after[id].StatusReason, after[id].TaskGeneration, before[id].Status, before[id].StatusReason, before[id].TaskGeneration)
			}
		}
	})
	t.Run("generation ended by a dead required helper", func(t *testing.T) {
		g := newRequiredGate(t)
		g.failAll()
		declStageSetup(t, g.o, assistantsTestStageID)
		g.o.activationHeld.Store(true)
		dead := g.attach(t, "op-dead", assistants.ModeRequired)
		waitForTaskStatus(t, g.store, dead, assistants.StatusFailed, 2*time.Second)
		g.o.concurrency.WaitAgents()
		helper := attachWatcherTask(t, g.o, "op-helper", assistantsTestStageID, assistants.TriggerOnCompletion, assistants.ModeAdvisory)
		g.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
		g.requireStageFailedByHelper(t)
		requirePassCancelled(t, g.o, nil, helper)
		reason := g.store.Snapshot().Assistants.Tasks[helper].StatusReason

		g.o.retryStage(context.Background(), assistantsTestStageID)

		rs := g.store.Snapshot()
		if got := rs.Assistants.RootGenerations[assistantsTestStageID]; got != 1 {
			t.Fatalf("root generation after the Retry = %d, want 1", got)
		}
		if tk := rs.Assistants.Tasks[helper]; tk.Status != assistants.StatusCancelled || tk.StatusReason != reason {
			t.Fatalf("helper of the ended generation = %s (%q), want still cancelled (%q)", tk.Status, tk.StatusReason, reason)
		}
		if dead, found := rs.Assistants.DeadRequiredTask(assistantsTestStageID); found {
			t.Fatalf("dead task %s of the previous generation gates the new attempt", dead.ID)
		}
	})
}

// resultlessStageRunner is a stage agent that never writes its result: each turn runs turn and exits.
type resultlessStageRunner struct {
	turns atomic.Int32
	turn  func(n int32)
}

func (r *resultlessStageRunner) RunPlanning(context.Context, string, string, string, string) error {
	return errors.New("unexpected planning turn")
}

func (r *resultlessStageRunner) RunAgent(context.Context, string, string, string, string) error {
	r.turn(r.turns.Add(1))
	return nil
}

func (r *resultlessStageRunner) RunJSONQuery(context.Context, string) ([]byte, error) {
	return nil, nil
}

// attachCompletionHelper attaches a required on_completion helper to the unread-test stage, as its agent's turn
// would.
func attachCompletionHelper(o *Orchestrator, opID string) (assistants.TaskID, error) {
	return o.Attach(context.Background(), AttachRequest{
		OperationID: opID, StageID: unreadTestStageID, ProfileID: "qa", Instruction: "check the result",
		Trigger: assistants.TriggerOnCompletion, Mode: assistants.ModeRequired, Actor: state.Actor{Kind: "user"},
	})
}

// backgroundRun is o.Run in its own goroutine: done is closed when Run returns, with err set.
type backgroundRun struct {
	done chan struct{}
	err  error
}

// startRun runs o in the background. The run is cancelled and awaited at the end of the test, before its store
// closes.
func startRun(t *testing.T, o *Orchestrator, timeout time.Duration) *backgroundRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	r := &backgroundRun{done: make(chan struct{})}
	go func() {
		r.err = o.Run(ctx)
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-r.done
	})
	return r
}

// requireFinishedItself fails unless Run returned on its own, before its deadline.
func (r *backgroundRun) requireFinishedItself(t *testing.T) {
	t.Helper()
	<-r.done
	if r.err != nil {
		t.Fatalf("Run = %v, want the run to finish by itself", r.err)
	}
}

// exitDecision is one run-state exit decision of the run loop and the snapshot it was taken on.
type exitDecision struct {
	rs      state.RunState
	allowed bool
}

// firstExitDecisionAfterCancel hands over the first exit decision the run loop takes on a snapshot in which a
// completion pass is already cancelled.
func firstExitDecisionAfterCancel(o *Orchestrator) <-chan exitDecision {
	decisions := make(chan exitDecision, 1)
	var sent atomic.Bool
	o.exitDecidedHook = func(rs state.RunState, allowed bool) {
		for _, tk := range rs.Assistants.Tasks {
			if isCompletionHelper(tk.Task) && tk.Status == assistants.StatusCancelled && sent.CompareAndSwap(false, true) {
				decisions <- exitDecision{rs: rs, allowed: allowed}
				return
			}
		}
	}
	return decisions
}

// awaitExitDecision waits for the run loop's decision, failing the test if none comes in time.
func awaitExitDecision(t *testing.T, decisions <-chan exitDecision) exitDecision {
	t.Helper()
	select {
	case d := <-decisions:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the run loop took no exit decision after the pass was cancelled")
		return exitDecision{}
	}
}

// A run without a dashboard ends by itself when its stage failed and only such a pass is left: the cancelled pass
// no longer holds it. A helper that still works holds it, without the run finalizing or stopping the helper, until
// it finishes. A dashboard run waits for a Retry as before, with nothing but its failed stage holding it.
func TestCompletionHelper_ParentFailureRunExits(t *testing.T) {
	t.Run("only the pass is left", func(t *testing.T) {
		var helper assistants.TaskID
		runner := &resultlessStageRunner{}
		o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
		runner.turn = func(n int32) {
			if n == 1 {
				helper, _ = attachCompletionHelper(o, "op-completion-helper")
			}
		}
		var helperTurns atomic.Int32
		o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
			helperTurns.Add(1)
			return passResultOutcome(), nil
		}
		startRun(t, o, 10*time.Second).requireFinishedItself(t)

		if got := store.Get(unreadTestStageID); got != state.StatusFailed {
			t.Fatalf("stage = %s, want failed", got)
		}
		requirePassCancelled(t, o, nil, helper)
		if got := helperTurns.Load(); got != 0 {
			t.Fatalf("helper turns = %d, want none", got)
		}
		if got := store.Snapshot().RunStatus; got != state.RunStatusFailed {
			t.Fatalf("run status = %q, want failed", got)
		}
	})

	t.Run("another helper still works", func(t *testing.T) {
		var completion assistants.TaskID
		runner := &resultlessStageRunner{}
		o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
		release := make(chan struct{})
		runner.turn = func(n int32) {
			if n == 1 {
				completion, _ = attachCompletionHelper(o, "op-completion-helper")
				if _, err := o.Attach(context.Background(), AttachRequest{
					OperationID: "op-working", StageID: unreadTestStageID, ProfileID: "qa", Instruction: "review",
					Actor: state.Actor{Kind: "user"},
				}); err != nil {
					t.Errorf("Attach of the working helper: %v", err)
				}
			}
		}
		working := make(chan context.Context, 1)
		o.runHelperAgent = func(ctx context.Context, _, _, _, _, _, _ string) (executor.HelperOutcome, error) {
			working <- ctx
			awaitRelease(t, release)
			return passResultOutcome(), nil
		}
		decisions := firstExitDecisionAfterCancel(o)
		run := startRun(t, o, 10*time.Second)

		d := awaitExitDecision(t, decisions)
		if d.allowed {
			t.Fatal("the run decided to finish while another helper still worked")
		}
		if blocker, held := d.rs.Assistants.RunBlocker(); !held || blocker.Trigger != assistants.TriggerOneShot || blocker.Status != assistants.StatusRunning {
			t.Fatalf("run blocker = %s %s (held %t), want the working helper running", blocker.Trigger, blocker.Status, held)
		}
		helperCtx := <-working
		if o.finalizing.Load() {
			t.Fatal("the run finalized while another helper still worked")
		}
		if err := helperCtx.Err(); err != nil {
			t.Fatalf("the working helper was stopped (%v) before it finished", err)
		}
		close(release)

		run.requireFinishedItself(t)
		requirePassCancelled(t, o, nil, completion)
	})

	t.Run("dashboard run", func(t *testing.T) {
		runner := &resultlessStageRunner{}
		o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
		o.SetDashboardURL("http://127.0.0.1:1")
		runner.turn = func(n int32) {
			if n == 1 {
				if _, err := attachCompletionHelper(o, "op-completion-helper"); err != nil {
					t.Errorf("Attach: %v", err)
				}
			}
		}
		decisions := firstExitDecisionAfterCancel(o)
		startRun(t, o, 10*time.Second)

		d := awaitExitDecision(t, decisions)
		requireOnlyTheFailedStageHolds(t, d)
		if o.finalizing.Load() {
			t.Fatal("a dashboard run with a failed stage finalized")
		}
	})
}

// requireOnlyTheFailedStageHolds fails unless the decision kept the run open for its failed stage alone, as a
// dashboard run waiting for a Retry: every stage is terminal, no helper or due turn holds it, nothing is unread.
func requireOnlyTheFailedStageHolds(t *testing.T, d exitDecision) {
	t.Helper()
	if d.allowed {
		t.Fatal("the run decided to finish with a failed stage")
	}
	for id, st := range d.rs.Stages {
		if !bus.IsTerminal(st.Status) {
			t.Fatalf("stage %s = %s, want every stage terminal", id, st.Status)
		}
	}
	if blocker, held := d.rs.Assistants.RunBlocker(); held {
		t.Fatalf("run held by task %s (%s)", blocker.ID, blocker.Status)
	}
	if queuedTurnDue(d.rs) {
		t.Fatal("a queued turn still holds the run")
	}
	if finishedStageHasUnread(d.rs) {
		t.Fatal("an unread message still holds the run")
	}
	if d.rs.AllDone() {
		t.Fatal("the run holds no failed stage")
	}
}

// A stage agent that ignores the refusal — repeats the same waits, alone and as a batch, then exits without a result —
// never gets a cyclic wait recorded: each repeat is refused at once, the stage never parks for a colleague, fails as
// usual without its result, and the helper's pass is cancelled with it, so the run ends.
func TestWaitCompletion_AgentIgnoresRejection(t *testing.T) {
	runner := &resultlessStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	var helperTurns atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		helperTurns.Add(1)
		return passResultOutcome(), nil
	}
	actor := state.Actor{Kind: "agent", ID: string(unreadTestStage)}
	var (
		helper   assistants.TaskID
		question agentcomm.MessageID // the stage's request to the helper
		waitErrs []error
	)
	wait := func(opID string, cond agentcomm.WaitCondition, batch []agentcomm.MessageID) {
		_, err := o.Wait(context.Background(), WaitRequest{OperationID: opID, Waiter: unreadTestStage, Actor: actor, Condition: cond, MessageIDs: batch})
		waitErrs = append(waitErrs, err)
	}
	replies := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo}
	runner.turn = func(n int32) {
		if n == 1 {
			var err error
			if helper, err = attachCompletionHelper(o, "op-completion-helper"); err != nil {
				waitErrs = append(waitErrs, err)
				return
			}
			sent, err := agentSend(o, "op-ask-helper", string(unreadTestStage), []string{string(meshHelperParticipantID(helper))}, "is it fine?")
			if err != nil {
				waitErrs = append(waitErrs, err)
				return
			}
			question = sent.MessageID
			for _, opID := range []string{"op-wait-1", "op-wait-1-again"} {
				wait(opID, taskResultOf(helper), nil)
			}
			for _, opID := range []string{"op-reply-1", "op-reply-1-again"} {
				wait(opID, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: question}, nil)
			}
			for _, opID := range []string{"op-batch-1", "op-batch-1", "op-batch-1-again"} {
				wait(opID, replies, []agentcomm.MessageID{question})
			}
			return
		}
		wait(fmt.Sprintf("op-wait-turn-%d", n), taskResultOf(helper), nil)
		wait(fmt.Sprintf("op-batch-turn-%d", n), replies, []agentcomm.MessageID{question})
	}
	startRun(t, o, 15*time.Second).requireFinishedItself(t)

	if want := 7 + 2*maxSummaryPostExit; len(waitErrs) != want {
		t.Fatalf("waits tried = %d (%v), want %d", len(waitErrs), waitErrs, want)
	}
	for _, err := range waitErrs {
		cycleReason(t, err)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 0 {
		t.Fatalf("waits recorded = %d, want none", n)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range history {
		if tr.StageID == unreadTestStageID && tr.To == state.StatusAwaitingPeer {
			t.Fatalf("stage parked in awaiting_peer at seq %d", tr.Seq)
		}
	}
	if got := runner.turns.Load(); got != 1+maxSummaryPostExit {
		t.Fatalf("stage turns = %d, want the first and %d summary corrections", got, maxSummaryPostExit)
	}
	if got := store.Get(unreadTestStageID); got != state.StatusFailed {
		t.Fatalf("stage = %s, want failed", got)
	}
	requirePassCancelled(t, o, nil, helper)
	if got := helperTurns.Load(); got != 0 {
		t.Fatalf("helper turns = %d, want none", got)
	}
}
