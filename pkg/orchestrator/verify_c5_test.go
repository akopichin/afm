package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestRunVerification_PreSignalledInterrupt_NoopSemaphore_VerifierNeverStarts —
// C5 код-ревью: с семафором без лимита (noopSemaphore, max_parallel не
// задан) Lease.SwapTo на команду верификатора раньше репортовал успех даже
// когда сигнал паузы/ревизии уже лежал в interruptChans ДО вызова
// RunVerification — верификатор запускался бы на стадии, уже ушедшей в
// паузу. Fake-раннер считает вызовы: он не должен быть вызван ни разу.
func TestRunVerification_PreSignalledInterrupt_NoopSemaphore_VerifierNeverStarts(t *testing.T) {
	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}

	// globalMaxParallel=0 → New предсоздаёт noopSemaphore для обеих команд.
	mgr := concurrency.New(bus.NewCriticalBus(1), []flow.Stage{s}, "claude", 0, nil)
	lease, err := mgr.AcquireLease(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	interruptCh := make(chan struct{}, 1)
	interruptCh <- struct{}{} // сигнал уже лежит в канале ДО вызова RunVerification
	o.interruptChans.Store(s.ID, interruptCh)

	var calls int
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	err = o.RunVerification(context.Background(), s, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
	if calls != 0 {
		t.Errorf("verify runner must never be invoked, got %d calls", calls)
	}
}

// TestRunVerification_PreSignalledInterrupt_SameCommandAsAuthor_VerifierNeverStarts —
// та же гонка, что и выше, для второго fast path'а Lease.SwapTo: author ==
// verifier command (та же команда — no-op, не трогает семафор вовсе), пока
// верификатор вообще не стартовал. G1 (5-е код-ревью): исход этого вызова —
// executor.ErrUserInterrupted, НЕ *VerifyRejectedError, так что деферренная
// очистка (verify.go) безусловно Release()ит lease (run(ctx,s) вот-вот
// вернётся, в этом же вызове повторного запуска автора НЕ будет — см.
// runWithRetry's onUserInterrupted, которая спавнит НОВОЕ поколение со своим
// собственным lease) — слот освобождается немедленно, а не остаётся висеть
// held до финального Release() в SpawnAgentLease.
func TestRunVerification_PreSignalledInterrupt_SameCommandAsAuthor_VerifierNeverStarts(t *testing.T) {
	sem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	mgr := concurrency.NewWithSemaphores(bus.NewCriticalBus(1), map[string]concurrency.Semaphore{"claude": sem}, "claude")
	lease, err := mgr.AcquireLease(context.Background(), "claude")
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "claude"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	interruptCh := make(chan struct{}, 1)
	interruptCh <- struct{}{}
	o.interruptChans.Store(s.ID, interruptCh)

	var calls int
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	err = o.RunVerification(context.Background(), s, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
	if calls != 0 {
		t.Errorf("verify runner must never be invoked, got %d calls", calls)
	}
	if len(sem) != 0 {
		t.Errorf("G1: an interrupted outcome must release the lease promptly (no in-loop retry is coming), len=%d", len(sem))
	}
}
