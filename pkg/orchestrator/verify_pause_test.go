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

// TestRunVerification_LeaseWaitAbortsOnPause_VerifierNeverStarts — V5a.1(b):
// pause/revise во время ОЖИДАНИЯ занятого командного слота верификатора
// (лизинг переходит автор->верификатор через Lease.SwapTo) должно прервать
// само ожидание, а не оставлять стадию висеть до тех пор, пока слот не
// освободится сам по себе. Верификатор при этом вообще не должен успеть
// запуститься.
func TestRunVerification_LeaseWaitAbortsOnPause_VerifierNeverStarts(t *testing.T) {
	// codex-слот занят кем-то посторонним — Lease.SwapTo("codex") будет ждать.
	codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	codexSem <- struct{}{}
	claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgr := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{
		"claude": claudeSem,
		"codex":  codexSem,
	}, "claude")

	lease, err := mgr.AcquireLease(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)

	var verifierCalled bool
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		verifierCalled = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	done := make(chan error, 1)
	go func() { done <- o.RunVerification(context.Background(), s, phaseImplementation) }()

	// Дать горутине реально дойти до ожидания занятого семафора (тот же
	// приём, что TestSpawnAgent_BlocksOnFullSemaphore/
	// TestSpawnAgent_SkipsRunWhenPausedWhileQueuedBehindSemaphore в
	// pkg/orchestrator/concurrency).
	time.Sleep(20 * time.Millisecond)

	select {
	case interruptCh <- struct{}{}:
	default:
		t.Fatal("interrupt channel should have room for one signal")
	}

	var gotErr error
	select {
	case gotErr = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunVerification did not return after the interrupt signal — lease wait was not aborted")
	}

	if !errors.Is(gotErr, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", gotErr, gotErr)
	}
	if verifierCalled {
		t.Error("verify agent step must never run — the wait for its command slot should have aborted first")
	}
	if len(codexSem) != 1 {
		t.Errorf("codex slot must remain held by its original owner, len=%d", len(codexSem))
	}
}
