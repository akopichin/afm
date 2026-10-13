package orchestrator

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestVerifyAgentStep_WarnsWhenVerifierModifiedTree — an AI-verify agent is supposed to be read-only; if the
// working-tree fingerprint taken right before the subprocess differs from the one taken right after, afm must
// emit a WARN-only verify_tree_modified notice, live AND durable, while the step's own verdict (and the stage
// outcome) stays untouched.
func TestVerifyAgentStep_WarnsWhenVerifierModifiedTree(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	var calls int
	o.fingerprintFn = func(string) (string, bool) {
		calls++
		if calls == 1 {
			return "before", true
		}
		return "after", true
	}
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "all good",
		}}, nil
	}

	subID, ch := o.ui.Subscribe(16)
	defer o.ui.Unsubscribe(subID)

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected fingerprintFn called exactly twice (before/after), got %d", calls)
	}

	var liveModified, livePass int
	drainUIBus(ch, func(ev bus.Event) {
		switch ev.Type {
		case bus.EventVerifyTreeModified:
			liveModified++
		case bus.EventVerifyResult:
			if data, ok := ev.Data.(map[string]any); ok && data[keyVerdict] == string(verify.VerdictPass) {
				livePass++
			}
		default:
		}
	})
	if liveModified != 1 {
		t.Fatalf("expected exactly one live verify_tree_modified event, got %d", liveModified)
	}
	if livePass != 1 {
		t.Fatalf("expected the step's own verdict to still be pass, got %d matching pass results", livePass)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	modified := noticesOfType(notices, bus.EventVerifyTreeModified)
	if len(modified) != 1 {
		t.Fatalf("expected exactly one durable verify_tree_modified notice, got %d", len(modified))
	}
	d := modified[0].Data
	if d[keyVerificationID] == "" || d[keyVerificationID] == nil {
		t.Error("verify_tree_modified notice missing verification_id")
	}
	if got := d[keyStep]; got != float64(1) {
		t.Errorf("verify_tree_modified step = %v, want 1", got)
	}
	if d[keyVerifyCommand] != "codex" {
		t.Errorf("verify_tree_modified command = %v, want codex", d[keyVerifyCommand])
	}
	if modified[0].StageID != verifyTestStageID {
		t.Error("verify_tree_modified notice must carry the stage id")
	}

	// The step's own verdict must be unaffected: exactly one accepted pass result, no rejection, no exec error.
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 || results[0].Data[keyVerdict] != string(verify.VerdictPass) {
		t.Fatalf("expected the step to still resolve as a clean pass, got %+v", results)
	}
}

// TestVerifyAgentStep_NoWarnWhenTreeUnchanged — identical fingerprints before/after must never publish
// verify_tree_modified.
func TestVerifyAgentStep_NoWarnWhenTreeUnchanged(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "all good",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	if modified := noticesOfType(notices, bus.EventVerifyTreeModified); len(modified) != 0 {
		t.Fatalf("expected zero verify_tree_modified notices for an unchanged tree, got %d", len(modified))
	}
}

// TestVerifyAgentStep_NoWarnWhenFingerprintUnavailable — if either fingerprint call comes back !ok (no git
// repo, oversized diff, …), afm must stay fail-open: no event, and the step must not error out because of it.
func TestVerifyAgentStep_NoWarnWhenFingerprintUnavailable(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	var calls int
	o.fingerprintFn = func(string) (string, bool) {
		calls++
		if calls == 1 {
			return "", false // "before" snapshot unavailable
		}
		return "after", true
	}
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "all good",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification must not fail just because a fingerprint is unavailable: %v", err)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	if modified := noticesOfType(notices, bus.EventVerifyTreeModified); len(modified) != 0 {
		t.Fatalf("expected zero verify_tree_modified notices when a fingerprint is unavailable, got %d", len(modified))
	}
}

// TestVerifyTreeModified_IsDurable — the warn notice must land in notices.jsonl on disk, not just on the live
// in-memory bus (a client that connects after the fact must still see it via reconstructNotices).
func TestVerifyTreeModified_IsDurable(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex-as-claude"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	var calls int
	o.fingerprintFn = func(string) (string, bool) {
		calls++
		if calls == 1 {
			return "before", true
		}
		return "after", true
	}
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "all good",
		}}, nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	modified := noticesOfType(notices, bus.EventVerifyTreeModified)
	if len(modified) != 1 {
		t.Fatalf("expected exactly one durable verify_tree_modified notice in notices.jsonl, got %d", len(modified))
	}
	if modified[0].Data[keyVerifyCommand] != "codex-as-claude" {
		t.Errorf("durable notice command = %v, want codex-as-claude", modified[0].Data[keyVerifyCommand])
	}
}
