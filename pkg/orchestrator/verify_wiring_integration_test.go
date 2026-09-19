package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// autonomousSummaryRunner writes execution_summary.md on every RunAgent call
// (idempotent — a correction attempt just rewrites the same file) and
// otherwise does nothing; it models an [auto] agent that always claims it
// finished, so completion is decided purely by AI-verify (V4b.2).
type autonomousSummaryRunner struct{}

func (r *autonomousSummaryRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return nil
}

func (r *autonomousSummaryRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *autonomousSummaryRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return nil, nil
}

// TestIntegration_AutonomousVerifyRejectionHoldsThenCorrects is V4b.2's
// "[auto]+verify" scenario: an [auto] stage's own completion probe
// (execution_summary.md) is satisfied on every attempt, but AI-verify is now
// actually invoked (new behavior — see runAutonomousAgent's doc comment) and
// its rejection holds completion — the stage does NOT reach done after the
// first attempt. On the second attempt verify passes and the stage completes.
func TestIntegration_AutonomousVerifyRejectionHoldsThenCorrects(t *testing.T) {
	stages := []flow.Stage{{
		ID: "auto1", Name: "Auto1",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}

	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, &autonomousSummaryRunner{})

	var calls int32
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
				SchemaVersion: verify.SupportedSchemaVersion,
				Verdict:       verify.VerdictNeedsChanges,
				Summary:       "missing tests",
				Findings: []verify.Finding{{
					Blocking: true, Title: "no tests", Requirement: "add tests", Evidence: "none found", MinimalFix: "write tests",
				}},
			}}, nil
		}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["auto1"].Status != state.StatusDone {
		t.Fatalf("expected done after the correction attempt, got %v", final.Stages["auto1"].Status)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected verify invoked exactly twice (reject then pass), got %d", got)
	}
}

// implAgentPromptCapturingRunner wraps a Runner and records every prompt
// passed to RunAgent (not RunPlanning) — needed to inspect the RetryContext
// text V4b.5 injects into the correction attempt.
type implAgentPromptCapturingRunner struct {
	delegate executor.Runner
	onAgent  func(prompt string)
}

func (r *implAgentPromptCapturingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *implAgentPromptCapturingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if r.onAgent != nil {
		r.onAgent(prompt)
	}
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func (r *implAgentPromptCapturingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

// TestIntegration_VerifyFeedbackInjectedIntoCorrectionPrompt is V4b.5's core
// scenario: after a needs_changes verdict, the SAME (fresh) runner's
// correction attempt (attempt 1, still inside runImplementationAgent) must
// see the blocking findings and the report path in its prompt, read via
// stagefiles.LoadActiveFeedback — not a new write path.
func TestIntegration_VerifyFeedbackInjectedIntoCorrectionPrompt(t *testing.T) {
	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan\n- step 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{{
		ID: "impl1", Name: "Impl1", Plan: planFile,
		Agents: []flow.AgentType{flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}

	var prompts []string
	capturing := &implAgentPromptCapturingRunner{
		delegate: &doneCreatingRunner{delegate: mockRunner(t, mockImplementationScript)},
		onAgent:  func(p string) { prompts = append(prompts, p) },
	}
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, capturing)

	var calls int32
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
				SchemaVersion: verify.SupportedSchemaVersion,
				Verdict:       verify.VerdictNeedsChanges,
				Summary:       "missing edge case handling",
				Findings: []verify.Finding{{
					Blocking: true, Title: "edge case bug", Requirement: "handle nil", Evidence: "panic on nil", MinimalFix: "add nil check",
				}},
			}}, nil
		}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl1"].Status != state.StatusDone {
		t.Fatalf("expected done after the correction, got %v", final.Stages["impl1"].Status)
	}
	if len(prompts) < 2 {
		t.Fatalf("expected at least 2 RunAgent prompts (initial + correction), got %d", len(prompts))
	}
	correction := prompts[len(prompts)-1]
	if !strings.Contains(correction, "edge case bug") || !strings.Contains(correction, "add nil check") {
		t.Errorf("correction prompt should contain the blocking findings, got:\n%s", correction)
	}
	if !strings.Contains(correction, "report.md") {
		t.Errorf("correction prompt should reference the verify report path, got:\n%s", correction)
	}
}

// TestIntegration_PlanningOnlyStageNeverInvokesVerify is V4b.2's
// belt-and-suspenders check: a planning-only stage (no implementation agent,
// so it completes right on approval — see approveStage's
// !HasAgent(AgentImplementation) fast path) constructed with a non-empty
// agent-step Verify (bypassing flow.ParseFile's own "verify requires an
// execution stage" validation, which is the real, primary guard) must still
// never invoke the verify agent — proving the wrapper genuinely isn't
// attached to the planning runners, not just that YAML validation happens to
// forbid the combination.
func TestIntegration_PlanningOnlyStageNeverInvokesVerify(t *testing.T) {
	stages := []flow.Stage{{
		ID: "p", Name: "P", AutoApprove: true,
		Agents: []flow.AgentType{flow.AgentPlanning},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}

	runner := mockRunner(t, mockPlanningScript)
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	verifyCalled := false
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		verifyCalled = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["p"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["p"].Status)
	}
	if verifyCalled {
		t.Error("verify must never run for a planning-only stage, even with a declared verify agent step")
	}
}
