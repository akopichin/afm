package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// Pollers and loops start only after recovery has settled what a crash left: a producer running earlier would act on
// half-restored state.
func TestRun_CollaborationProducersStartAfterRecovery(t *testing.T) {
	o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	var hookRuns atomic.Int32
	o.startupReleaseHook = func() {
		hookRuns.Add(1)
		if o.pollerDone != nil {
			t.Error("the question poller was already running inside startup recovery")
		}
	}

	runToEnd(t, o)
	o.concurrency.WaitAgents()

	if got := hookRuns.Load(); got != 1 {
		t.Fatalf("startup recovery reached the release step %d times, want 1", got)
	}
	if o.pollerDone == nil {
		t.Fatal("the question poller never started after recovery")
	}
}

// A poisoned review-pause round runs no recovery at all, yet the run stays up for inspection until cancelled.
func TestRun_PoisonedReviewPauseSkipsCollaborationRecovery(t *testing.T) {
	o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, &scriptedStageRunner{turn: func(int32) error { return nil }})
	marker := filepath.Join(o.opts.RunDir, "notes-pause.json")
	if err := os.WriteFile(marker, []byte(`{"version":1,"state":"resuming",`), 0o644); err != nil {
		t.Fatal(err)
	}
	var hookRuns atomic.Int32
	o.startupReleaseHook = func() { hookRuns.Add(1) }

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := o.Run(ctx)
	o.concurrency.WaitAgents()

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want the run held open until its context ended", err)
	}
	if got := hookRuns.Load(); got != 0 {
		t.Fatalf("recovery ran %d times on a poisoned run, want none", got)
	}
}

// The loop scheduler is stopped, and waited for, before Run returns: a tick still in flight when the run ends
// must have finished by then.
func TestRun_WaitsForAnInFlightLoopTickBeforeReturning(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	state.SetClockForTest(fc)
	t.Cleanup(func() { state.SetClockForTest(nil) })
	runner := &scriptedStageRunner{}
	o, _ := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	o.clock = fc
	ticking, tickEnded := make(chan struct{}), make(chan struct{})
	o.loopTick = func(ctx context.Context, _ string, _ uint64) error {
		close(ticking)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond) // a tick that needs a moment to wind down
		close(tickEnded)
		return nil
	}
	runner.turn = func(int32) error {
		if _, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: "add-qa", ProfileID: "qa", Title: "QA", Instruction: "test the app", Actor: loopUser,
		}); err != nil {
			return err
		}
		if _, err := o.CreateLoop(context.Background(), loopReq("op-run-loop", "qa", time.Minute, "ping")); err != nil {
			return err
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			fc.Advance(time.Minute)
			select {
			case <-ticking:
				return nil
			default:
			}
		}
		return errors.New("the loop never ticked")
	}

	runToEnd(t, o)

	select {
	case <-tickEnded:
	default:
		t.Fatal("Run returned while a loop tick was still in flight")
	}
	o.concurrency.WaitAgents()
}

// The loop scheduler runs only for an assistants run that is not poisoned; absence is observed as no timer armed
// within a short bound, where a running scheduler arms one at once.
func TestStartCollaborationLoops_SchedulerOnlyForAnAssistantsRunThatIsNotPoisoned(t *testing.T) {
	tests := []struct {
		name       string
		assistants bool
		poisoned   bool
		wantLoops  bool
	}{
		{"assistants run", true, false, true},
		{"poisoned run", true, true, false},
		{"assistants disabled", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newSchedFixture(t)
			f.create(t, loopReq("op-1", "my-qa", time.Minute, "ping"))
			f.o.opts.AssistantsEnabled = tc.assistants
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stop := f.o.startCollaborationLoops(ctx, tc.poisoned)
			defer stop()

			armed := make(chan struct{})
			go func() {
				f.clk.BlockUntil(1)
				close(armed)
			}()
			// BlockUntil cannot be cancelled: arm a timer ourselves so the probe always returns.
			defer func() {
				f.clk.NewTimer(time.Hour)
				<-armed
			}()
			select {
			case <-armed:
				if !tc.wantLoops {
					t.Fatal("the loop scheduler started")
				}
			case <-time.After(200 * time.Millisecond):
				if tc.wantLoops {
					t.Fatal("the loop scheduler never armed its timer")
				}
			}
		})
	}
}
