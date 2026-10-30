package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

func commitQueuedCompletion(t *testing.T, o *Orchestrator) bool {
	t.Helper()
	for {
		select {
		case ev := <-o.critical.Recv():
			if ev.Type != bus.EventAgentCompleted {
				continue
			}
			if err := o.onAgentCompleted(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			return true
		default:
			return false
		}
	}
}

// Each cell stops the old turn at the boundary relevant to its outcome. No timing race or sleeps:
// hooks model a supersession finishing before the old turn is allowed to continue.
func TestStaleOwner_OutcomeMatrix(t *testing.T) {
	kinds := []string{"pause_continue", "pause_continue_revise", "revise_pause_continue", "answer_restart", "own_revise"}
	outcomes := []string{"summary_success", "async_completion", "summary_missing", "verify_retry", "verify_retry_no_fresh", "verify_exhausted", "verify_interrupted", "agent_interrupted", "nonretryable", "retry_resume", "retries_exhausted", "abandoned_question", "stalemate", "question_left", "async_stalemate", "recovery_fast", "stale_marker", "feedback_write", "feedback_clear", "session_cleanup", "session_nonretryable", "session_retryable"}
	for _, kind := range kinds {
		for _, outcome := range outcomes {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				h := newSummaryHarness(t, 3)
				h.allowInterrupt = true
				h.o.retryBackoff = 0
				h.writeSummary("done")
				owner := &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch}
				var successorMarker *attemptOwner
				sessionPath := stagefiles.SessionFile(h.stageDir, phaseAutonomous)
				h.interactive = (outcome == "verify_retry" || outcome == "verify_retry_no_fresh") || (outcome == "session_cleanup" || outcome == "session_nonretryable" || outcome == "session_retryable")
				var superseded bool
				var seq uint64
				var used, callsAtSupersession int
				apply := func(ev bus.FSMEvent, guard bus.GuardCtx) {
					t.Helper()
					if _, ok := h.o.Trigger("s1", ev, guard, "matrix"); !ok {
						t.Fatalf("supersession %s failed", ev)
					}
				}
				supersede := func() {
					t.Helper()
					if superseded {
						t.Fatal("supersession barrier entered twice")
					}
					superseded = true
					switch kind {
					case "pause_continue", "pause_continue_revise":
						apply(bus.EvPause, bus.GuardCtx{})
						apply(bus.EvContinue, bus.GuardCtx{PausedFrom: h.o.opts.Store.PausedFrom("s1")})
						if kind == "pause_continue_revise" {
							if h.o.currentStatus("s1") == state.StatusRetrying {
								apply(bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phaseAutonomous})
							}
							apply(bus.EvRevise, bus.GuardCtx{})
						}
					case "revise_pause_continue":
						// Revise is unavailable in retrying: the test restores the owner as a real backoff would.
						if h.o.currentStatus("s1") == state.StatusRetrying {
							apply(bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phaseAutonomous})
						}
						apply(bus.EvRevise, bus.GuardCtx{})
						apply(bus.EvPause, bus.GuardCtx{})
						apply(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRevising})
					case "answer_restart":
						apply(bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous})
						apply(bus.EvUserAnswered, bus.GuardCtx{Phase: phaseAutonomous, NewOwner: true})
					case "own_revise":
						if h.o.currentStatus("s1") == state.StatusRetrying {
							apply(bus.EvResumeAfterRetry, bus.GuardCtx{Phase: phaseAutonomous})
						}
						apply(bus.EvRevise, bus.GuardCtx{})
					default:
						t.Fatalf("unknown supersession %q", kind)
					}
					// A successor may already have its own channel; old outcomes cannot consult it as identity.
					if kind != "own_revise" {
						h.o.interruptChans.Store("s1", make(chan struct{}, 1))
					}
					if (outcome == "verify_retry" || outcome == "verify_retry_no_fresh") || (outcome == "session_cleanup" || outcome == "session_nonretryable" || outcome == "session_retryable") {
						if err := os.WriteFile(sessionPath, []byte("successor-session"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if outcome == "stale_marker" {
						successorMarker = &attemptOwner{epoch: h.o.opts.Store.Stage("s1").OwnerEpoch}
						h.o.liveAgents.Store("s1", successorMarker)
					}
					seq, used, callsAtSupersession = h.o.opts.Store.Snapshot().LastSeq, h.used(), h.calls
				}
				var checks int
				check := func() error { checks++; supersede(); return nil }
				agent := func(int, string) error { return nil }
				switch outcome {
				case "summary_success":
				case "async_completion":
					check = func() error { return nil }
				case "summary_missing":
					if err := os.Remove(h.summaryPath()); err != nil {
						t.Fatal(err)
					}
					h.o.summaryDecisionHook = func(string) { supersede() }
					check = nil
				case "verify_retry", "verify_retry_no_fresh", "verify_exhausted":
					if outcome == "verify_retry_no_fresh" {
						fresh := false
						h.o.opts.Config.Verify.FreshCorrection = &fresh
					}
					if outcome == "verify_exhausted" {
						h.o.maxRetries = 0
					}
					check = func() error { return newVerifyRejected(1) }
					h.o.verifyOutcomeGuardHook = func(string) { supersede() }
				case "verify_interrupted":
					check = func() error { supersede(); return executor.ErrUserInterrupted }
				case "agent_interrupted":
					agent = func(int, string) error { supersede(); return executor.ErrUserInterrupted }
				case "nonretryable":
					agent = func(int, string) error { supersede(); return &executor.TerminalResultError{Message: "denied"} }
				case "retry_resume":
					agent = func(int, string) error { return errors.New("overloaded") }
					h.o.beforeRetryResumeHook = func(string) { supersede() }
				case "retries_exhausted":
					h.o.maxRetries = 0
					agent = func(int, string) error { supersede(); return errors.New("overloaded") }
				case "abandoned_question":
					agent = func(int, string) error {
						supersede()
						h.writeQuestion()
						// Only a running successor can have its own question surfaced.
						if h.o.currentStatus("s1") == state.StatusRunning {
							apply(bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous})
							seq = h.o.opts.Store.Snapshot().LastSeq
						}
						return nil
					}
					check = func() error { checks++; return nil }
				case "stalemate", "async_stalemate":
					h.o.opts.Config.Verify.ReviewPatience = intPtr(1)
					var fingerprints int
					h.o.fingerprintFn = func(string) (string, bool) {
						fingerprints++
						if fingerprints == 2 && outcome == "stalemate" {
							supersede()
						}
						return "unchanged", true
					}
					check = func() error { return newVerifyRejected(1) }
				case "recovery_fast":
					h.o.beforeCompleteHook = func(string) { supersede() }
					check = func() error {
						h.o.resumeExecutionStage(context.Background(), flow.Stage{ID: "s1", Agents: []flow.AgentType{flow.AgentAuto}}, state.StatusRunning, owner.epoch)
						return executor.ErrUserInterrupted
					}
				case "stale_marker":
					// Place a successor's marker after the last launch check but before registration.
					h.o.beforeLiveMarkerHook = func(string) { supersede(); callsAtSupersession++ }
					agent = func(int, string) error {
						if marker, _ := h.o.liveAgents.Load("s1"); marker != successorMarker {
							t.Error("stale registration replaced successor marker")
						}
						return executor.ErrUserInterrupted
					}
				case "feedback_write", "feedback_clear":
					result := shellRejectionResult("old", "")
					if outcome == "feedback_clear" {
						result = verify.ModelResult{SchemaVersion: 1, Verdict: verify.VerdictPass, Summary: "ok"}
					}
					h.o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
						return verify.RunOutcome{ProcessOK: true, Result: &result}, nil
					}
					h.o.beforeOwnerFileHook = func(_ string, op string) {
						if op != outcome {
							t.Fatalf("operation=%s want %s", op, outcome)
						}
						supersede()
						if err := stagefiles.WriteActiveFeedback(h.stageDir, "successor", shellRejectionResult("new", ""), "reviewer", 1); err != nil {
							t.Fatal(err)
						}
					}
					check = func() error {
						ctx := context.WithValue(context.Background(), turnOwnerKey{}, owner)
						err := h.o.RunVerification(ctx, flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "reviewer"}}}}, phaseAutonomous)
						if _, id, ok := stagefiles.LoadActiveFeedback(h.stageDir); !ok || id != "successor" {
							t.Errorf("stale feedback mutation: id=%q ok=%v", id, ok)
						}
						return err
					}
				case "session_cleanup", "session_nonretryable", "session_retryable":
					if outcome == "session_nonretryable" {
						agent = func(int, string) error { return &executor.TerminalResultError{Message: "denied"} }
					}
					if outcome == "session_retryable" {
						agent = func(int, string) error { return errors.New("overloaded") }
					}
					check = func() error { return newVerifyRejected(1) }
					h.o.beforeOwnerFileHook = func(_ string, op string) {
						if op != "session_clear" {
							t.Fatalf("operation=%s", op)
						}
						supersede()
					}
				case "question_left":
					agent = func(int, string) error { h.writeQuestion(); return nil }
					check = func() error { checks++; return nil }
					h.o.dialogHook = func(_ string, left bool) {
						if !left {
							supersede()
						}
					}
				default:
					t.Fatalf("unknown outcome %q", outcome)
				}
				if outcome == "stalemate" || outcome == "async_stalemate" {
					s := agentVerifyStage(1, 3, agentStep())
					h.o.runWithRetry(context.Background(), s, phaseAutonomous, func(rc string) error { h.calls++; return agent(h.calls, rc) }, check, func() { h.interrupted++ })
				} else {
					h.run(agent, check)
				}
				if outcome == "async_completion" || outcome == "async_stalemate" {
					ev := <-h.o.critical.Recv()
					supersede()
					var feedbackCalls atomic.Int32
					r := &fakeAgentRunner{onAgent: func(string) { feedbackCalls.Add(1) }}
					h.o.runner, h.o.opts.Runner = r, r
					h.o.concurrency = concurrency.New(h.o.critical, nil, "", 0, nil)
					if err := h.o.onAgentCompleted(context.Background(), ev); err != nil {
						t.Fatal(err)
					}
					h.o.concurrency.WaitAgents()
					h.interrupted = int(feedbackCalls.Load())
				}
				if !superseded {
					t.Fatal("supersession point was not reached")
				}
				if (outcome == "verify_retry" || outcome == "verify_retry_no_fresh") || (outcome == "session_cleanup" || outcome == "session_nonretryable" || outcome == "session_retryable") {
					if data, err := os.ReadFile(sessionPath); err != nil || string(data) != "successor-session" {
						t.Errorf("successor session lost: data=%q err=%v", data, err)
					}
					if n := h.noticeCount(string(bus.EventRetryScheduled)); n != 0 {
						t.Errorf("stale retry notices=%d", n)
					}
				}
				if kind == "own_revise" {
					if h.interrupted != 1 {
						t.Fatalf("own Revise handed off %d times, want exactly 1", h.interrupted)
					}
					return
				}
				if h.calls != callsAtSupersession || h.interrupted != 0 || h.used() != used {
					t.Errorf("stale outcome: calls %d->%d, feedback=%d, budget %d->%d", callsAtSupersession, h.calls, h.interrupted, used, h.used())
				}
				history, err := h.o.opts.Store.History()
				if err != nil {
					t.Fatal(err)
				}
				for _, tr := range history {
					if tr.Seq > seq {
						t.Errorf("stale transition after supersession seq %d: %+v", seq, tr)
					}
				}
				if n := h.noticeCount(string(bus.EventAgentCompleted)); n != 0 {
					t.Errorf("stale durable completion notices=%d", n)
				}
				if n := criticalOfType(h.o, bus.EventAgentCompleted); n != 0 {
					t.Errorf("stale completion events=%d", n)
				}
				if n := countStalemateNotices(t, h.runDir); n != 0 {
					t.Errorf("stale stalemate notices=%d", n)
				}
				if outcome == "abandoned_question" && checks != 0 {
					t.Errorf("stale abandoned question ran verify %d times", checks)
				}
			})
		}
	}
}

// The consumer's early epoch check is insufficient: Pause/Continue may finish just before Store.Apply.
func TestStaleOwner_CompletionCASRejectsSupersession(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	h.run(func(int, string) error { return nil }, nil)
	var seq uint64
	h.o.beforeCompleteHook = func(string) { h.pauseContinue(); seq = h.o.opts.Store.Snapshot().LastSeq }
	if !commitQueuedCompletion(t, h.o) {
		t.Fatal("completion was not queued")
	}
	h.wantStatus(state.StatusRunning)
	if h.o.opts.Store.Snapshot().LastSeq != seq || h.completions() != 0 || h.noticeCount(string(bus.EventAgentCompleted)) != 0 {
		t.Fatal("completion crossed the CAS fence")
	}
}

func TestStaleOwner_VerifyCorrectionLaunchIsFenced(t *testing.T) {
	h := newSummaryHarness(t, 2)
	h.writeSummary("done")
	var launches int
	h.o.beforeLaunchHook = func(string) {
		launches++
		if launches == 2 {
			h.pauseContinue()
		}
	}
	h.run(func(int, string) error { return nil }, func() error { return newVerifyRejected(1) })
	if launches != 2 || h.calls != 1 {
		t.Fatalf("launch checks=%d author calls=%d, want 2/1", launches, h.calls)
	}
	h.wantStatus(state.StatusRunning)
}

func TestStaleOwner_BeforeVerifyIsFenced(t *testing.T) {
	h := newSummaryHarness(t, 0)
	h.writeSummary("done")
	h.o.dialogHook = func(_ string, left bool) {
		if !left {
			h.pauseContinue()
		}
	}
	checks := 0
	h.run(func(int, string) error { return nil }, func() error { checks++; return nil })
	if checks != 0 {
		t.Fatalf("superseded turn ran verify %d times", checks)
	}
}

func TestAnswerWindow_ImplementationBeforeAndAfterLeave(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprintf("before=%t", before), func(t *testing.T) {
			h := newSummaryHarness(t, 0)
			h.o.dialogHook = func(_ string, left bool) {
				if left == before {
					return
				}
				if err := os.WriteFile(filepath.Join(h.stageDir, "implementation.q1.answer.json"), []byte(`{"answer":"yes"}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := h.o.NotifyAnswer("s1", phaseImplementation, "q1", "yes", false); err != nil {
					t.Fatal(err)
				}
			}
			h.o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Interactive: true}, phaseImplementation, func(string) error {
				return os.WriteFile(filepath.Join(h.stageDir, "implementation.q1.question.json"), []byte(`{"id":"q1","question":"ok?"}`), 0o644)
			}, func() error { return nil }, func() { t.Error("unexpected interrupt") })
			if before {
				if !commitQueuedCompletion(t, h.o) {
					t.Fatal("answer before leave did not complete")
				}
				h.wantStatus(state.StatusDone)
				if n := criticalOfType(h.o, bus.EventUserAnswered); n != 0 {
					t.Fatalf("extra answer restart=%d", n)
				}
			} else {
				h.wantStatus(state.StatusAwaitingUserInput)
				if n := criticalOfType(h.o, bus.EventUserAnswered); n != 1 {
					t.Fatalf("answer restart=%d, want 1", n)
				}
			}
		})
	}
}
