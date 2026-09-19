package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestRunVerification_SwapsLeaseToVerifierCommandAndBackToAuthor — V4b: when a
// lease exists for the stage (the normal path after spawnKind), RunVerification
// must temporarily move the single held command-semaphore slot to the verify
// agent step's command (SwapTo, release-before-acquire — never two slots at
// once) and return it to the author's command when it returns, regardless of
// outcome (pass here; VerifyRejectedError/VerifyExecError are checked by the
// deferred-swap code path itself, not by the verdict).
func TestRunVerification_SwapsLeaseToVerifierCommandAndBackToAuthor(t *testing.T) {
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
	if len(claudeSem) != 1 {
		t.Fatalf("sanity: author slot should be held before verify runs, len=%d", len(claudeSem))
	}

	s := flow.Stage{ID: "s1", Command: "claude",
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.stageLeases.Store(s.ID, lease)

	var sawVerifierSlotHeld, sawAuthorSlotReleased bool
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		sawVerifierSlotHeld = len(codexSem) == 1
		sawAuthorSlotReleased = len(claudeSem) == 0
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), s, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}

	if !sawVerifierSlotHeld {
		t.Error("expected the codex slot to be held while the verify agent step runs")
	}
	if !sawAuthorSlotReleased {
		t.Error("expected the claude slot to be released while the verify agent step runs (never two slots at once)")
	}
	if len(claudeSem) != 1 {
		t.Errorf("expected the author slot to be re-acquired after RunVerification returns, len=%d", len(claudeSem))
	}
	if len(codexSem) != 0 {
		t.Errorf("expected the verifier slot to be released after RunVerification returns, len=%d", len(codexSem))
	}
}

// TestRunVerification_SameCommandAsAuthorNeverReleasesLease — the author and
// the verify agent step share the same command: SwapTo must be a no-op (the
// slot stays held throughout, never released-then-reacquired) — a real
// release+reacquire on max_parallel=1 could self-deadlock racing another
// waiter for the very slot the lease already owns.
func TestRunVerification_SameCommandAsAuthorNeverReleasesLease(t *testing.T) {
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

	var heldDuringStep bool
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		heldDuringStep = len(sem) == 1
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), s, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	if !heldDuringStep {
		t.Error("same-command swap must be a no-op — the slot should stay held throughout the agent step")
	}
	if len(sem) != 1 {
		t.Errorf("author slot should remain held after RunVerification returns, len=%d", len(sem))
	}
}

// TestRunVerification_NoLeaseIsSafeNoop — a caller that never registered a
// lease for the stage (recovery paths bypassing spawnKind, e.g.
// resumeInteractiveAgent) must not crash — RunVerification simply doesn't
// participate in slot transfer, same as before V3/V4b.
func TestRunVerification_NoLeaseIsSafeNoop(t *testing.T) {
	s := flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, s)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}
	if err := o.RunVerification(context.Background(), s, phaseImplementation); err != nil {
		t.Fatalf("RunVerification without a lease should behave exactly as before V4b, got %v", err)
	}
}
