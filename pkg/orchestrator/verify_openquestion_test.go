package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// TestRunWithRetry_VerifyGatedStaleOpenQuestion_RejectionNotSwallowed is the
// V4b.4 counterpart of TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion
// (retry_test.go) with a verify step present: the stale-open-question branch
// (stage ALREADY in awaiting_user_input, agent exited cleanly) must still run
// the gateWithVerify-wrapped completionCheck exactly ONCE, and a rejection
// from it must NOT be swallowed into a fake EventAgentCompleted — the "do not
// change the live/stale question rule" contract (see CLAUDE.md) means the
// function just returns without publishing anything when completionCheck()
// is non-nil, exactly as it did before verify existed.
func TestRunWithRetry_VerifyGatedStaleOpenQuestion_RejectionNotSwallowed(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	stalePath := filepath.Join(stageDir, "autonomous_execution.q2.question.json")
	if err := os.WriteFile(stalePath, []byte(`{"id":"q2","question":"long-abandoned question"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Симулирует независимый поллер вопросов, уже переведший стадию в
	// awaiting_user_input, ПОКА агент ещё выполнялся (см. оригинальный тест).
	events := o.critical.Recv()
	o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
	<-events // drain this Trigger's own notification

	s := flow.Stage{ID: "s1", Interactive: true,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	var verifyCalls int
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		verifyCalls++
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "still broken",
			Findings: []verify.Finding{{
				Blocking: true, Title: "t", Requirement: "r", Evidence: "e", MinimalFix: "f",
			}},
		}}, nil
	}

	check := o.gateWithVerify(context.Background(), s, phaseAutonomous, func() error {
		return stagefiles.CheckAutonomousCompletion(stageDir)
	})

	o.runWithRetry(context.Background(), s, phaseAutonomous,
		func(retryContext string) error { return nil }, // agent exited cleanly, wrote its artifact
		check,
		func() { t.Error("onUserInterrupted should not be called") },
	)

	select {
	case ev := <-events:
		t.Fatalf("no event should be published when verify rejects a stale-question stage, got %v", ev)
	default:
	}

	if verifyCalls != 1 {
		t.Errorf("expected verify invoked exactly once, got %d", verifyCalls)
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingUserInput {
		t.Errorf("stage status = %v, want still awaiting_user_input (a verify rejection must not fake a completion)", got)
	}
}

// TestRunWithRetry_VerifyGatedLiveOpenQuestion_NeverInvokesVerify is the
// V4b.4 counterpart for a GENUINELY live question (just written by this same
// agent call, stage not yet in awaiting_user_input): runWithRetry must hold
// the stage via EvAskUser and return WITHOUT ever calling completionCheck —
// so verify must never run while a live question is still unanswered.
func TestRunWithRetry_VerifyGatedLiveOpenQuestion_NeverInvokesVerify(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.question.json"),
		[]byte(`{"id":"q1","question":"need input"}`), 0644); err != nil {
		t.Fatal(err)
	}

	s := flow.Stage{ID: "s1", Interactive: true,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		t.Fatal("verify must never run while a live question is still open")
		return verify.RunOutcome{}, errors.New("unreachable")
	}

	check := o.gateWithVerify(context.Background(), s, phaseAutonomous, func() error {
		return stagefiles.CheckAutonomousCompletion(stageDir)
	})

	o.runWithRetry(context.Background(), s, phaseAutonomous,
		func(retryContext string) error { return nil },
		check,
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingUserInput {
		t.Errorf("status = %v, want awaiting_user_input (a genuinely-open live question must still wait)", got)
	}
}
