package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// Real spawn plumbing carries the invocation's lease; stage maps deliberately point at a successor.
// Both an ordinary two-step pass and supersession during step one must leave the successor untouched.
func TestStaleOwner_VerifyUsesTurnResources(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			author := concurrency.ChannelSemaphore(make(chan struct{}, 2))
			reviewer := concurrency.ChannelSemaphore(make(chan struct{}, 1))
			h.o.concurrency = concurrency.NewWithSemaphores(h.o.critical, map[string]concurrency.Semaphore{"author": author, "reviewer": reviewer}, "author")
			s := flow.Stage{ID: "s1", Command: "author", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyAgent, Command: "reviewer"}, {Kind: flow.VerifyAgent, Command: "reviewer"},
			}}}
			successorLease, err := h.o.concurrency.AcquireLease(context.Background(), "author")
			if err != nil {
				t.Fatal(err)
			}
			defer successorLease.Release()
			successorCh := make(chan struct{}, 1)
			successorCh <- struct{}{}
			var steps int
			h.o.runVerifyAgent = func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
				steps++
				if len(author) != 1 || len(reviewer) != 1 {
					t.Errorf("wrong lease: author=%d reviewer=%d, want 1/1", len(author), len(reviewer))
				}
				if supersede {
					h.pauseContinue()
				}
				return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{SchemaVersion: 1, Verdict: verify.VerdictPass, Summary: "ok"}}, nil
			}
			returned := make(chan error, 1)
			h.o.spawnAgentLeased(context.Background(), s, func(ctx context.Context, s flow.Stage) {
				owner := &attemptOwner{epoch: h.o.opts.Store.Stage(s.ID).OwnerEpoch, interruptCh: make(chan struct{}, 1)}
				ctx = context.WithValue(ctx, turnOwnerKey{}, owner)
				h.o.stageLeases.Store(s.ID, successorLease)
				h.o.interruptChans.Store(s.ID, successorCh)
				returned <- h.o.RunVerification(ctx, s, phaseAutonomous)
			})
			h.o.concurrency.WaitAgents()
			err = <-returned
			if supersede {
				if !errors.Is(err, executor.ErrUserInterrupted) || steps != 1 {
					t.Errorf("superseded verify: steps=%d err=%v", steps, err)
				}
				if n := h.noticeCount("verify_result"); n != 0 {
					t.Errorf("stale step published %d results", n)
				}
			} else if err != nil || steps != 2 {
				t.Errorf("current verify: steps=%d err=%v", steps, err)
			}
			if len(author) != 1 || len(reviewer) != 0 {
				t.Errorf("successor lease changed: author=%d reviewer=%d, want 1/0", len(author), len(reviewer))
			}
			if len(successorCh) != 1 {
				t.Error("old verify consumed successor interrupt")
			}
			if got, ok := h.o.stageLeases.Load(s.ID); !ok || got != successorLease {
				t.Error("old cleanup removed successor lease")
			}
		})
	}
}

func TestStaleOwner_VerifyInterruptStaysWithTurn(t *testing.T) {
	h := newSummaryHarness(t, 0)
	own, successor := make(chan struct{}, 1), make(chan struct{}, 1)
	owner := &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch, interruptCh: own}
	ctx := context.WithValue(context.Background(), turnOwnerKey{}, owner)
	h.o.interruptChans.Store("s1", successor)
	own <- struct{}{}
	waitCtx, stop := h.o.pauseAwareVerifyCtx(ctx, "s1")
	// stop joins the observer and must still see the already-buffered owner signal.
	if !stop() || waitCtx.Err() == nil {
		t.Fatal("verify ignored its own buffered interrupt")
	}
	own <- struct{}{}
	if !h.o.verifyShellInterruptPending(ctx, "s1") {
		t.Error("shell verify ignored its own interrupt")
	}
	r := h.o.runnerForVerify(ctx, flow.Stage{ID: "s1"}, "reviewer")
	got := reflect.ValueOf(r).Elem().FieldByName("cfg").FieldByName("InterruptCh").Pointer()
	if got != reflect.ValueOf(own).Pointer() {
		t.Error("verify executor was given a successor interrupt channel")
	}
	successor <- struct{}{}
	_, stop = h.o.pauseAwareVerifyCtx(ctx, "s1")
	if stop() || len(successor) != 1 {
		t.Error("verify consumed successor interrupt")
	}
}
