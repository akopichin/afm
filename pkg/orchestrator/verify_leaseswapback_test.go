package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

func TestRunVerification_SwapBackToAuthorAbortsOnPause_ReturnsPromptly(t *testing.T) {
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

	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	interruptCh := make(chan struct{}, 1)
	o.interruptChans.Store(s.ID, interruptCh)

	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		// Слот "claude" в этот момент уже release'нут SwapTo при переходе на верификатора — занимаем его чужим
		// токеном И сигналим прерывание ДО того, как шаг вернётся.
		claudeSem <- struct{}{}
		interruptCh <- struct{}{}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	done := make(chan error, 1)
	go func() { done <- o.RunVerification(context.Background(), s, phaseImplementation) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunVerification: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunVerification did not return promptly — swap-back to the author slot blocked on the busy semaphore despite the pause signal")
	}

	// Верификаторский слот должен быть освобождён, а слот автора должен остаться занятым ровно "чужим"
	// токеном — lease не мог его захватить и не держит теперь НИЧЕГО.
	if len(codexSem) != 0 {
		t.Errorf("verifier slot must be released, len=%d", len(codexSem))
	}
	if len(claudeSem) != 1 {
		t.Errorf("author slot must remain held only by the external holder (lease must not have sneaked in), len=%d", len(claudeSem))
	}
}
