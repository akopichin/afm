package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

func TestRunVerification_NonRetryOutcomesReleaseLeasePromptly(t *testing.T) {
	// fake verify занимает слот claude только после SwapTo release; до RunVerification он ещё занят lease
	// автора.
	steal := func(claudeSem concurrency.ChannelSemaphore) { claudeSem <- struct{}{} }

	cases := []struct {
		name       string
		buildStage func() flow.Stage
		runAgent   func(claudeSem concurrency.ChannelSemaphore) func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error)
		wantErrIs  error // non-nil: проверяем errors.Is; nil+wantNilErr: err==nil; иначе просто "err != nil"
		wantNilErr bool
	}{
		{
			name:       "pass",
			buildStage: func() flow.Stage { return verifyLeaseTestStage(0) },
			runAgent: func(claudeSem concurrency.ChannelSemaphore) func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
				return func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
					steal(claudeSem)
					return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
						SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
					}}, nil
				}
			},
			wantNilErr: true,
		},
		{
			// Детерминированный exec-сбой (ненулевой err от runVerifyAgent — executor-storage): НЕ транзиент,
			// не ретраится — поэтому проверяет именно «немедленный релиз lease на не-retry исходе».
			name:       "exec-error",
			buildStage: func() flow.Stage { return verifyLeaseTestStage(0) },
			runAgent: func(claudeSem concurrency.ChannelSemaphore) func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
				return func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
					steal(claudeSem)
					return verify.RunOutcome{}, errors.New("verify execution failed: storage")
				}
			},
		},
		{
			name:       "timeout",
			buildStage: func() flow.Stage { return verifyLeaseTestStage(20 * time.Millisecond) },
			runAgent: func(claudeSem concurrency.ChannelSemaphore) func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
				return func(ctx context.Context, _ flow.Stage, _, _, _, _ string) (verify.RunOutcome, error) {
					steal(claudeSem)
					<-ctx.Done() // дожидаемся истечения st.Timeout (stepCtx)
					return verify.RunOutcome{}, ctx.Err()
				}
			},
		},
		{
			name:       "interrupt",
			buildStage: func() flow.Stage { return verifyLeaseTestStage(0) },
			runAgent: func(claudeSem concurrency.ChannelSemaphore) func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
				return func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
					steal(claudeSem)
					return verify.RunOutcome{Interrupted: true}, nil
				}
			},
			wantErrIs: executor.ErrUserInterrupted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
			codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
			mgr := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{
				"claude": claudeSem,
				"codex":  codexSem,
			}, "claude")

			lease, err := mgr.AcquireLease(context.Background(), "claude")
			if err != nil {
				t.Fatalf("AcquireLease: %v", err)
			}

			s := tc.buildStage()
			o, _ := newVerifyTestOrchestrator(t, s)
			o.stageLeases.Store(s.ID, lease)
			o.runVerifyAgent = tc.runAgent(claudeSem)

			done := make(chan error, 1)
			go func() { done <- o.RunVerification(context.Background(), s, phaseImplementation) }()

			select {
			case err := <-done:
				switch {
				case tc.wantErrIs != nil:
					if !errors.Is(err, tc.wantErrIs) {
						t.Fatalf("RunVerification error = %v, want errors.Is(%v)", err, tc.wantErrIs)
					}
				case tc.wantNilErr:
					if err != nil {
						t.Fatalf("RunVerification: expected nil error for outcome %q, got %v", tc.name, err)
					}
				default:
					if err == nil {
						t.Fatalf("RunVerification: expected a non-nil error for outcome %q", tc.name)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("RunVerification did not return promptly — deferred lease handling blocked on the busy author slot")
			}

			if len(codexSem) != 0 {
				t.Errorf("verifier slot must be released, len=%d", len(codexSem))
			}
			if len(claudeSem) != 1 {
				t.Errorf("author slot must remain held only by the external holder, len=%d", len(claudeSem))
			}
		})
	}
}

func TestRunVerification_NeedsChangesReacquiresAuthorLease(t *testing.T) {
	claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgr := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{
		"claude": claudeSem,
		"codex":  codexSem,
	}, "claude")

	lease, err := mgr.AcquireLease(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	s := verifyLeaseTestStage(0)
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "fix it",
			Findings: []verify.Finding{{
				Blocking: true, Title: "x", Requirement: "y", Evidence: "z", MinimalFix: "w",
			}},
		}}, nil
	}

	done := make(chan error, 1)
	go func() { done <- o.RunVerification(context.Background(), s, phaseImplementation) }()

	select {
	case err := <-done:
		var rejected *VerifyRejectedError
		if !errors.As(err, &rejected) {
			t.Fatalf("RunVerification error = %v, want *VerifyRejectedError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunVerification did not return promptly")
	}

	if len(codexSem) != 0 {
		t.Errorf("verifier slot must be released, len=%d", len(codexSem))
	}
	if len(claudeSem) != 1 {
		t.Errorf("needs_changes must reacquire the author slot for the coming in-loop retry, len=%d", len(claudeSem))
	}
}

// verifyLeaseTestStage строит однoшаговую agent-verify стадию с командой автора "claude" и командой
// верификатора "codex" — общий fixture для тестов этого файла.
func verifyLeaseTestStage(timeout time.Duration) flow.Stage {
	return flow.Stage{
		ID:      verifyTestStageID,
		Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex", Timeout: timeout},
		}},
	}
}
