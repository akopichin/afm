package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

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
	if len(claudeSem) != 0 {
		t.Errorf("G1: a pass outcome must NOT reacquire the author slot (no in-loop retry is coming), len=%d", len(claudeSem))
	}
	if len(codexSem) != 0 {
		t.Errorf("expected the verifier slot to be released after RunVerification returns, len=%d", len(codexSem))
	}
}

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
	if len(sem) != 0 {
		t.Errorf("G1: a pass outcome must release the slot after RunVerification returns (no in-loop retry is coming), len=%d", len(sem))
	}
}

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
