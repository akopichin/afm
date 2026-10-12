package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// planningTrackingRunner counts planning calls to prove retry recovery resumes execution directly.
type planningTrackingRunner struct {
	delegate       executor.Runner
	planningCalled int32
}

func (r *planningTrackingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	atomic.AddInt32(&r.planningCalled, 1)
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *planningTrackingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func (r *planningTrackingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
}

var _ executor.Runner = (*planningTrackingRunner)(nil)

func TestIntegration_ResumeRunningWithDoneFile_RunsFreshVerifyGate(t *testing.T) {
	stages := []flow.Stage{{
		ID: "impl1", Name: "Impl1",
		Agents: []flow.AgentType{flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	runner := &doneCreatingRunner{delegate: mockRunner(t, mockImplementationScript)}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, runner)

	stageDir := filepath.Join(runDir, "impl1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// plan.md must already be on disk — the resumed implementation agent reads it unconditionally, same as a
	// real post-approval crash would leave it.
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- [ ] x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("completed work summary"), 0644); err != nil {
		t.Fatal(err)
	}

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "impl1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var verifyCalls int32
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		atomic.AddInt32(&verifyCalls, 1)
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl1"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["impl1"].Status)
	}
	if atomic.LoadInt32(&verifyCalls) == 0 {
		t.Error("expected a fresh verify gate to run on resume — a pre-existing .done must not be treated as an already-passed verdict")
	}
}

func TestIntegration_ResumeRetryingAutonomousWithSummary_RunsFreshVerifyGate(t *testing.T) {
	stages := []flow.Stage{{
		ID: "auto1", Name: "Auto1",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, &autonomousSummaryRunner{})

	stageDir := filepath.Join(runDir, "auto1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "auto1", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("expected done after the fresh gate's correction cycle, got %v", final.Stages["auto1"].Status)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected verify invoked exactly twice (fresh gate rejects the recovered summary, correction pass follows), got %d", got)
	}
}

func TestIntegration_ResumeRunningAutonomousWithSummary_RunsFreshVerifyGate(t *testing.T) {
	stages := []flow.Stage{{
		ID: "auto1", Name: "Auto1",
		Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, &autonomousSummaryRunner{})

	stageDir := filepath.Join(runDir, "auto1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "auto1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var verifyCalls int32
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		atomic.AddInt32(&verifyCalls, 1)
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["auto1"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["auto1"].Status)
	}
	if atomic.LoadInt32(&verifyCalls) == 0 {
		t.Error("expected a fresh verify gate to run on resume — a pre-existing execution_summary.md must not be treated as an already-passed verdict")
	}
}

func TestIntegration_ResumeAfterCrashDuringVerifyCorrection_InjectsActiveFeedback(t *testing.T) {
	stages := []flow.Stage{{
		ID: "impl1", Name: "Impl1",
		Agents: []flow.AgentType{flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	var prompts []string
	capturing := &implAgentPromptCapturingRunner{
		delegate: &doneCreatingRunner{delegate: mockRunner(t, mockImplementationScript)},
		onAgent:  func(p string) { prompts = append(prompts, p) },
	}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, capturing)

	stageDir := filepath.Join(runDir, "impl1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- [ ] x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("completed work summary"), 0644); err != nil {
		t.Fatal(err)
	}
	// Machine feedback persisted before rejection survives a crash independently of FSM status.
	if err := stagefiles.WriteActiveFeedback(stageDir, "ver-stale", verify.ModelResult{
		SchemaVersion: 1,
		Verdict:       verify.VerdictNeedsChanges,
		Summary:       "found a blocking issue",
		Findings: []verify.Finding{{
			Blocking: true, Title: "edge case bug", Requirement: "handle nil", Evidence: "panic on nil", MinimalFix: "add nil check",
		}},
	}, "codex", 1); err != nil {
		t.Fatal(err)
	}

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "impl1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl1"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["impl1"].Status)
	}
	if len(prompts) == 0 {
		t.Fatal("expected at least one RunAgent prompt on resume")
	}
	if !strings.Contains(prompts[0], "edge case bug") || !strings.Contains(prompts[0], "add nil check") {
		t.Errorf("resumed prompt should contain the pre-crash active verify feedback, got:\n%s", prompts[0])
	}
}

func TestIntegration_ResumeDoneStage_NeverReVerifies(t *testing.T) {
	stages := []flow.Stage{{
		ID: "impl1", Name: "Impl1",
		Agents: []flow.AgentType{flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	// A runner that fails loudly if ever invoked — proves the stage is truly
	// left untouched, not just "happens to still look done".
	orch, _, stateFile := setupOrchestratorWithRunner(t, stages, mockRunner(t, mockFailScript))

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "impl1", From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "impl1", From: state.StatusRunning, To: state.StatusDone, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var verifyCalled bool
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		verifyCalled = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)
	time.Sleep(300 * time.Millisecond)

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl1"].Status != state.StatusDone {
		t.Fatalf("expected done to remain unchanged, got %v", final.Stages["impl1"].Status)
	}
	if verifyCalled {
		t.Error("a stage already durably done must never be re-verified on resume")
	}
}

func TestIntegration_ResumeRetryingPlainWithDoneFile_RunsFreshVerifyGateNotPlanningHeuristic(t *testing.T) {
	stages := []flow.Stage{{
		ID: "impl1", Name: "Impl1",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}}
	tracker := &planningTrackingRunner{delegate: &doneCreatingRunner{delegate: mockRunner(t, mockImplementationScript)}}
	orch, runDir, stateFile := setupOrchestratorWithRunner(t, stages, tracker)

	stageDir := filepath.Join(runDir, "impl1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	// plan.md already on disk AND .done already on disk — status StatusRetrying.
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- [ ] x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, ".done"), []byte("completed work summary"), 0644); err != nil {
		t.Fatal(err)
	}

	store := orchestrator.StoreFromOrch(orch)
	if err := store.Apply(&state.Transition{StageID: "impl1", From: state.StatusPending, To: state.StatusRetrying, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	var verifyCalls int32
	orchestrator.SetVerifyAgentRunnerForTest(orch, func(ctx context.Context, s flow.Stage, cmd, prompt, logFile, resultFile string) (verify.RunOutcome, error) {
		atomic.AddInt32(&verifyCalls, 1)
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	})

	if err := orch.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	final := loadStateJSON(t, stateFile)
	if final.Stages["impl1"].Status != state.StatusDone {
		t.Fatalf("expected done, got %v", final.Stages["impl1"].Status)
	}
	if atomic.LoadInt32(&verifyCalls) == 0 {
		t.Error("expected a fresh verify gate to run on resume — a pre-existing .done must not be treated as an already-passed verdict")
	}
	if got := atomic.LoadInt32(&tracker.planningCalled); got != 0 {
		t.Errorf("expected RunPlanning to never be invoked (the .done+Verify short-circuit must skip the CheckPlanCompletion/NeedsPlanning planning-retry heuristic entirely), got %d calls", got)
	}
}
