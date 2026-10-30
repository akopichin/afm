package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator/concurrency"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func TestStaleOwner_ReproStaleCompletionEvent(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.writeSummary("done")
	h.run(func(int, string) error { return nil }, nil)
	ev := <-h.o.critical.Recv()
	h.pauseContinue()
	err := h.o.onAgentCompleted(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("stale completion changed successor status to %s", got)
	}
}

func TestStaleOwner_ReproStaleInterruptedAuthorDoesNotRestartNewRevise(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.allowInterrupt = true
	h.run(func(int, string) error {
		h.pauseContinue()
		h.o.interruptChans.Store("s1", make(chan struct{}, 1))
		h.o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "new owner feedback")
		return executor.ErrUserInterrupted
	}, nil)
	if h.interrupted != 0 {
		t.Fatalf("stale author requested %d feedback restarts", h.interrupted)
	}
}

func TestStaleOwner_ReproStaleVerifyRejectionDoesNotRetry(t *testing.T) {
	h := newSummaryHarness(t, 2)
	h.allowInterrupt = true
	checks := 0
	h.run(func(call int, _ string) error {
		if call == 1 {
			h.writeSummary("done")
			return nil
		}
		return &executor.TerminalResultError{Message: "stale second call"}
	}, func() error {
		checks++
		if checks == 1 {
			h.pauseContinue()
			return &VerifyRejectedError{IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "needs changes"}, Step: 1}
		}
		return nil
	})
	if h.calls != 1 {
		t.Fatalf("stale author launched %d calls, want 1", h.calls)
	}
}

func TestStaleOwner_ReproAnsweredQuestionBeforeReserveDoesNotKeepOldOwner(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.interactive = true
	// Keep the answer's successor queued until the old turn has attempted its reserve.
	ctx, cancel := context.WithCancel(context.Background())
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	h.o.concurrency = concurrency.NewWithSemaphores(h.o.critical, map[string]concurrency.Semaphore{"": sem}, "")
	lease, err := h.o.concurrency.AcquireLease(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); lease.Release(); h.o.concurrency.WaitAgents() })
	h.o.summaryDecisionHook = func(string) {
		h.writeQuestion()
		if _, move := h.o.surfaceQuestion("s1", phaseAutonomous, "q1"); move != questionAsked {
			t.Fatalf("surfaceQuestion move = %v", move)
		}
		h.answer()
		if got := criticalOfType(h.o, bus.EventUserAnswered); got != 1 {
			t.Fatalf("queued answer continuations = %d, want 1", got)
		}
		if err := h.o.onUserAnswered(ctx, bus.Event{Type: bus.EventUserAnswered, StageID: "s1", Data: map[string]any{keyPhase: phaseAutonomous}}); err != nil {
			t.Fatal(err)
		}
	}
	h.run(func(call int, _ string) error {
		if call == 2 {
			h.writeSummary("done")
		}
		return nil
	}, nil)
	if h.calls != 1 || h.used() != 0 {
		t.Fatalf("old author made %d calls and spent %d recovery reservations after answer handoff", h.calls, h.used())
	}
}

func TestStaleOwner_ReproStaleVerifyRejectionCannotFailSuccessor(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	h.run(func(int, string) error { return nil }, func() error {
		h.pauseContinue()
		return &VerifyRejectedError{IncompleteWorkError: &stagefiles.IncompleteWorkError{Reason: "needs changes"}, Step: 1}
	})
	if got := h.o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("stale verify rejection changed successor status to %s", got)
	}
}

// G5: stale turn returns nil after Pause→Continue; the successor's question is already surfaced.
// The "abandoned question" branch runs completionCheck and publishes completion without an owner check.
func TestStaleOwner_DiagnosisStaleTurnAbandonedQuestionBranchCompletesSuccessor(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.run(func(int, string) error {
		h.writeSummary("done")
		h.pauseContinue() // successor owns the stage now
		h.writeQuestion()
		if _, move := h.o.surfaceQuestion("s1", phaseAutonomous, "q1"); move != questionAsked {
			t.Fatalf("surface move=%v", move)
		}
		return nil
	}, nil)
	if got := h.completions(); got != 0 {
		t.Fatalf("stale turn published %d completion(s) for the successor's stage", got)
	}
	if got := criticalOfType(h.o, bus.EventAgentCompleted); got != 0 {
		t.Fatalf("stale turn queued %d completion event(s)", got)
	}
}

// G2 (success path): completionCheck passes, but meanwhile Pause→Continue→Revise targeted the successor.
// The post-check status switch sees Revising and restarts feedback on the successor's behalf.
func TestStaleOwner_DiagnosisStaleTurnSuccessPathSpawnsFeedbackForSuccessorsRevise(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.allowInterrupt = true
	h.writeSummary("done")
	h.run(func(int, string) error { return nil }, func() error {
		h.pauseContinue()
		h.o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "feedback for the successor")
		return nil
	})
	if h.interrupted != 0 {
		t.Fatalf("stale turn requested %d feedback run(s) for the successor's Revise", h.interrupted)
	}
}

// G2 (Revise→Pause→Continue): Revise interrupted this turn, then Pause and Continue happened before the turn
// returned. Continue (resumeStageAtStatus(revising)) spawns the feedback run itself; the turn must not spawn a second.
func TestStaleOwner_DiagnosisRevisePauseContinueDoubleFeedback(t *testing.T) {
	h := newSummaryHarness(t, 15)
	h.allowInterrupt = true
	h.run(func(int, string) error {
		h.o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "fb")
		h.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "")
		h.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRevising}, "")
		return executor.ErrUserInterrupted
	}, nil)
	if h.interrupted != 0 {
		t.Fatalf("turn spawned %d feedback run(s) although Continue already owns the revising stage", h.interrupted)
	}
}

// Feedback claim: run*WithFeedback ignores a failed EvStartRun and runs anyway (second claimant of one Revise).
func TestStaleOwner_DiagnosisFeedbackClaimNotChecked(t *testing.T) {
	for _, phase := range []string{phaseAutonomous, phaseImplementation, phaseReview} {
		t.Run(phase, func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			h.writeSummary("done")
			for _, name := range []string{"plan.md", ".done", "review.md"} {
				if err := os.WriteFile(filepath.Join(h.stageDir, name), []byte("done"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := &fakeAgentRunner{onAgent: func(string) { h.calls++ }}
			h.o.runner, h.o.opts.Runner = runner, runner
			h.o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "fb")
			run := h.o.runAutonomousWithFeedback
			switch phase {
			case phaseImplementation:
				run = h.o.runImplementationWithFeedback
			case phaseReview:
				run = h.o.runReviewWithFeedback
			default:
			}
			s := flow.Stage{ID: "s1"}
			run(context.Background(), s)
			run(context.Background(), s)
			if h.calls != 1 {
				t.Fatalf("two feedback claimants made %d calls, want 1", h.calls)
			}
		})
	}
}

func TestStaleOwner_ConcurrentFeedbackClaimRunsOneAuthor(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	h.o.concurrency = concurrency.New(h.o.critical, nil, "", 0, nil)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); h.o.concurrency.WaitAgents() })
	r := &fakeAgentRunner{onAgent: func(string) { entered <- struct{}{}; <-release }}
	h.o.runner, h.o.opts.Runner = r, r
	h.o.Trigger("s1", bus.EvRevise, bus.GuardCtx{}, "feedback")
	returned := make(chan struct{}, 2)
	for range 2 {
		h.o.spawnAgentLeased(context.Background(), flow.Stage{ID: "s1"}, func(ctx context.Context, s flow.Stage) {
			h.o.runAutonomousWithFeedback(ctx, s)
			returned <- struct{}{}
		})
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("winning feedback never ran")
	}
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("losing feedback did not return while winner was blocked")
	}
	unblock()
	h.o.concurrency.WaitAgents()
	if len(entered) != 0 {
		t.Fatal("both feedback claimants invoked the author")
	}
}
