package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

func TestStaleOwner_RecoveryCompletionEpochFence(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	h.o.beforeCompleteHook = func(string) { h.pauseContinue() }
	h.o.resumeExecutionStage(context.Background(), flow.Stage{ID: "s1", Agents: []flow.AgentType{flow.AgentAuto}}, state.StatusRunning, h.o.opts.Store.Stage("s1").OwnerEpoch)
	h.wantStatus(state.StatusRunning)
}

// Recovery must retain its dispatch epoch even when ownership changes before
// the completion probe; rereading the epoch would adopt the successor's work.
func TestStaleOwner_RecoveryUsesDispatchEpochNotReread(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	dispatch := h.o.opts.Store.Stage("s1").OwnerEpoch
	h.pauseContinue()
	successor := h.o.opts.Store.Stage("s1").OwnerEpoch
	if successor == dispatch {
		t.Fatal("Pause/Continue did not replace the dispatch owner")
	}
	seq := h.o.opts.Store.Snapshot().LastSeq
	h.o.resumeExecutionStage(context.Background(), flow.Stage{ID: "s1", Agents: []flow.AgentType{flow.AgentAuto}}, state.StatusRunning, dispatch)
	h.wantStatus(state.StatusRunning)
	if h.o.opts.Store.Stage("s1").OwnerEpoch != successor || h.o.opts.Store.Snapshot().LastSeq != seq {
		t.Fatal("stale recovery changed successor state")
	}
	if h.completions() != 0 || h.noticeCount(string(bus.EventAgentCompleted)) != 0 {
		t.Fatal("stale recovery published a completion")
	}
}

// The marker can arrive after a successor left its question. Delivery and the
// queued consumer must both reject it; a registration-only check is insufficient.
func TestStaleOwner_LiveMarkerDoesNotSuppressAnswerRestart(t *testing.T) {
	for _, deliveryRace := range []bool{false, true} {
		name := "stale_marker"
		if deliveryRace {
			name = "delivery_CAS"
		}
		t.Run(name, func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			owner := &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch}
			successorQuestion := func(string) {
				h.pauseContinue()
				h.writeQuestion()
				if _, ok := h.o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
					t.Fatal("successor ask failed")
				}
			}
			if deliveryRace {
				h.o.beforeLiveAnswerHook = successorQuestion
			} else {
				successorQuestion("s1")
			}
			h.o.liveAgents.Store("s1", owner)
			h.answer()
			if n := criticalOfType(h.o, bus.EventUserAnswered); n != 1 {
				t.Fatalf("answer restart events=%d, want 1", n)
			}
			h.wantStatus(state.StatusAwaitingUserInput)
			// Reinsert an old marker after NotifyAnswer to exercise the independent consumer fence.
			h.o.liveAgents.Store("s1", owner)
			h.o.beforeLiveAnswerHook = nil
			h.writeSummary("done")
			// Use the real restart consumer with a fake runner; join it before Store cleanup.
			r := &fakeAgentRunner{onAgent: func(string) {}}
			h.o.runner, h.o.opts.Runner = r, r
			h.o.concurrency = concurrency.New(h.o.critical, nil, "", 0, nil)
			if err := h.o.onUserAnswered(context.Background(), bus.Event{StageID: "s1", Data: map[string]any{keyPhase: phaseAutonomous}}); err != nil {
				t.Fatal(err)
			}
			h.o.concurrency.WaitAgents()
			if h.o.opts.Store.Stage("s1").OwnerEpoch == owner.epoch || h.o.currentStatus("s1") == state.StatusAwaitingUserInput {
				t.Fatal("stale marker suppressed queued restart")
			}
		})
	}
}

func TestStaleOwner_VerifySharedFeedback(t *testing.T) {
	for _, operation := range []string{"feedback_write", "feedback_clear"} {
		for _, supersede := range []bool{false, true} {
			name := operation + "/current"
			if supersede {
				name = operation + "/superseded"
			}
			t.Run(name, func(t *testing.T) {
				h := newSummaryHarness(t, 0)
				oldResult := shellRejectionResult("old", "")
				if operation == "feedback_clear" {
					oldResult = verify.ModelResult{SchemaVersion: 1, Verdict: verify.VerdictPass, Summary: "ok"}
				}
				if err := stagefiles.WriteActiveFeedback(h.stageDir, "previous", shellRejectionResult("previous", ""), "reviewer", 1); err != nil {
					t.Fatal(err)
				}
				h.o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
					return verify.RunOutcome{ProcessOK: true, Result: &oldResult}, nil
				}
				s := flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "reviewer"}}}}
				owner := &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch, interruptCh: make(chan struct{}, 1)}
				ctx := context.WithValue(context.Background(), turnOwnerKey{}, owner)
				calls := 0
				h.o.beforeOwnerFileHook = func(_ string, op string) {
					if op != operation {
						t.Fatalf("operation=%s want %s", op, operation)
					}
					calls++
					if supersede {
						h.pauseContinue()
						if err := stagefiles.WriteActiveFeedback(h.stageDir, "successor", shellRejectionResult("new", ""), "reviewer", 1); err != nil {
							t.Fatal(err)
						}
					}
				}
				err := h.o.RunVerification(ctx, s, phaseAutonomous)
				_, id, ok := stagefiles.LoadActiveFeedback(h.stageDir)
				if calls != 1 {
					t.Fatalf("file operations=%d", calls)
				}
				if supersede {
					if !ok || id != "successor" {
						t.Fatalf("stale verify changed successor feedback: id=%q ok=%v", id, ok)
					}
					if !errors.Is(err, executor.ErrUserInterrupted) {
						t.Fatalf("stale verify err=%v", err)
					}
				} else if operation == "feedback_clear" {
					if err != nil || ok {
						t.Fatalf("current pass did not clear: ok=%v err=%v", ok, err)
					}
				} else {
					var rejected *VerifyRejectedError
					if !errors.As(err, &rejected) || !ok || id == "previous" {
						t.Fatalf("current rejection did not publish: id=%q err=%v", id, err)
					}
				}
			})
		}
	}
}

func TestStaleOwner_CorrectionKeepsSuccessorSession(t *testing.T) {
	h := newSummaryHarness(t, 2)
	h.interactive = true
	h.writeSummary("done")
	path := stagefiles.SessionFile(h.stageDir, phaseAutonomous)
	h.o.beforeOwnerFileHook = func(_ string, op string) {
		if op != "session_clear" {
			t.Fatalf("operation=%s", op)
		}
		h.pauseContinue()
		if err := os.WriteFile(path, []byte("successor-session"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h.run(func(int, string) error { return nil }, func() error { return newVerifyRejected(1) })
	if data, err := os.ReadFile(path); err != nil || string(data) != "successor-session" {
		t.Fatalf("stale correction removed successor session: data=%q err=%v", data, err)
	}
	if n := h.noticeCount(string(bus.EventRetryScheduled)); n != 0 {
		t.Fatalf("stale correction notices=%d", n)
	}
}

// Устаревший автор, потерявший стадию до создания session-файла, не должен его создавать: иначе преемник
// пойдёт в --resume по сессии, которую CLI никогда не запускал.
func TestStaleOwner_SessionCreationFencedByOwner(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprintf("supersede=%t", supersede), func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			marker := filepath.Join(h.runDir, "cli-started")
			script := filepath.Join(h.runDir, "agent.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			operations := 0
			h.o.beforeOwnerFileHook = func(_ string, op string) {
				if op != "session_create" {
					return
				}
				operations++
				if supersede {
					h.pauseContinue()
				}
			}
			stage := flow.Stage{ID: "s1", Command: script, Interactive: true}
			h.o.runAutonomousAttempt(context.Background(), stage, h.stageDir, "", "autonomous.log", "autonomous")

			_, sessionErr := os.Stat(stagefiles.SessionFile(h.stageDir, phaseAutonomous))
			_, cliErr := os.Stat(marker)
			if operations != 1 {
				t.Fatalf("session_create operations=%d, want 1", operations)
			}
			if supersede {
				if !os.IsNotExist(sessionErr) || !os.IsNotExist(cliErr) {
					t.Fatalf("stale author left session (err=%v) or started CLI (err=%v)", sessionErr, cliErr)
				}
				h.wantStatus(state.StatusRunning)
			} else if sessionErr != nil {
				t.Fatalf("current owner did not create session: %v", sessionErr)
			}
		})
	}
}

// Первый запуск интерактивного автора создаёт сессию сам и идёт с --session-id; --resume только когда сессия уже была.
func TestEnsureTurnSession_FirstLaunchIsNotResume(t *testing.T) {
	h := newSummaryHarness(t, 0)
	stage := flow.Stage{ID: "s1", Interactive: true}
	owner := &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch}
	runnerResume := func(resume bool) bool {
		r := h.o.runnerForTurnResume(stage, phaseAutonomous, nil, resume)
		return reflect.ValueOf(r).Elem().FieldByName("cfg").FieldByName("Resume").Bool()
	}

	first, err := h.o.ensureTurnSession(stage, phaseAutonomous, owner)
	if err != nil || first {
		t.Fatalf("first launch: resume=%t err=%v, want fresh session", first, err)
	}
	if !stagefiles.SessionExists(h.stageDir, phaseAutonomous) {
		t.Fatal("session file was not created for the first launch")
	}
	if runnerResume(first) {
		t.Fatal("first launch runner uses --resume although the session was just created")
	}

	second, err := h.o.ensureTurnSession(stage, phaseAutonomous, owner)
	if err != nil || !second {
		t.Fatalf("second launch: resume=%t err=%v, want resume", second, err)
	}
	if !runnerResume(second) {
		t.Fatal("second launch runner does not use --resume")
	}
}

func TestStaleOwner_LiveAnswerKeepsOwnerAndCompletes(t *testing.T) {
	h := newSummaryHarness(t, 0)
	epoch := h.o.opts.Store.Stage("s1").OwnerEpoch
	h.run(func(int, string) error {
		h.writeQuestion()
		if _, ok := h.o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, ""); !ok {
			t.Fatal("ask failed")
		}
		h.answer()
		h.wantStatus(state.StatusRunning)
		if got := h.o.opts.Store.Stage("s1").OwnerEpoch; got != epoch {
			t.Fatalf("live answer changed epoch %d -> %d", epoch, got)
		}
		h.writeSummary("done")
		return nil
	}, nil)
	if !commitQueuedCompletion(t, h.o) {
		t.Fatal("live turn did not queue completion")
	}
	h.wantStatus(state.StatusDone)
	if n := h.noticeCount(string(bus.EventAgentCompleted)); n != 1 || h.completions() != 0 {
		t.Fatalf("completion notices=%d pending=%d", n, h.completions())
	}
}

// A signal to the currently registered live turn must reach its actual executor,
// even without a status/epoch change. A stale-turn test alone misses this wiring.
func TestStaleOwner_LiveInterruptReachesExecutor(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%t", interactive), func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			marker := filepath.Join(h.runDir, "cli-started")
			script := filepath.Join(h.runDir, "agent.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			h.writeSummary("done")
			epoch := h.o.opts.Store.Stage("s1").OwnerEpoch
			signals := 0
			h.o.beforeLaunchHook = func(id string) {
				ch, ok := h.o.interruptChans.Load(id)
				if !ok {
					t.Fatal("live turn has no registered channel")
				}
				ch.(chan struct{}) <- struct{}{}
				signals++
			}
			h.o.runAutonomousAttempt(context.Background(), flow.Stage{ID: "s1", Command: script, Interactive: interactive}, h.stageDir, "", "autonomous.log", "autonomous")
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("signalled live executor started CLI: %v", err)
			}
			if signals != 1 || h.o.opts.Store.Stage("s1").OwnerEpoch != epoch || h.completions() != 0 {
				t.Fatalf("signals=%d epoch=%d completions=%d", signals, h.o.opts.Store.Stage("s1").OwnerEpoch, h.completions())
			}
			h.wantStatus(state.StatusRunning)
		})
	}
}

func TestStaleOwner_InteractiveRunnerUsesSuppliedInterrupt(t *testing.T) {
	h := newSummaryHarness(t, 0)
	own, successor := make(chan struct{}, 1), make(chan struct{}, 1)
	h.o.interruptChans.Store("s1", successor)
	r := h.o.runnerForTurn(flow.Stage{ID: "s1", Interactive: true}, phaseAutonomous, own)
	if got := reflect.ValueOf(r).Elem().FieldByName("cfg").FieldByName("InterruptCh").Pointer(); got != reflect.ValueOf(own).Pointer() {
		t.Fatal("interactive runner ignored supplied turn channel")
	}
}

func TestStaleOwner_RecoveryDispatchEpoch(t *testing.T) {
	for _, path := range []string{"continue", "startup_running", "startup_retrying"} {
		for _, supersede := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/supersede=%t", path, supersede), func(t *testing.T) {
				h := newSummaryHarness(t, 0)
				h.o.opts.Stages[0].Agents = []flow.AgentType{flow.AgentAuto}
				h.o.graph = graph.NewGraph(h.o.opts.Stages)
				h.writeSummary("done")
				if path == "startup_retrying" {
					h.o.Trigger("s1", bus.EvScheduleRetry, bus.GuardCtx{Phase: phaseAutonomous}, "")
				}
				probes := 0
				h.o.beforeCompleteHook = func(string) {
					probes++
					if supersede {
						from := h.o.currentStatus("s1")
						h.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "")
						h.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: from}, "")
					}
				}
				if path == "continue" {
					h.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "")
					if err := h.o.Continue(context.Background(), "s1"); err != nil {
						t.Fatal(err)
					}
				} else {
					h.o.startPlanningForPending(context.Background())
				}
				want := state.StatusDone
				if supersede {
					want = state.StatusRunning
					if path == "startup_retrying" {
						want = state.StatusRetrying
					}
				}
				h.wantStatus(want)
				if probes != 1 {
					t.Fatalf("recovery completion probes=%d", probes)
				}
				history, err := h.o.opts.Store.History()
				if err != nil {
					t.Fatal(err)
				}
				completions := 0
				for _, transition := range history {
					if transition.StageID == "s1" && transition.To == state.StatusDone {
						completions++
					}
				}
				wantCompletions := 1
				if supersede {
					wantCompletions = 0
				}
				if completions != wantCompletions {
					t.Fatalf("recovery completion transitions=%d, want %d", completions, wantCompletions)
				}
			})
		}
	}
}
