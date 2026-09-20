package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

const verifyTestStageID = "s1"

// newVerifyTestOrchestrator строит минимальный Orchestrator для тестов
// RunVerification напрямую (без New()) — движок не трогает FSM/store/UI,
// ему достаточно opts.RunDir/Stages и инъектируемого o.runVerifyAgent
// (тот же приём, что approve_test.go применяет для approveStage).
func newVerifyTestOrchestrator(t *testing.T, stage flow.Stage) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	stageDir := filepath.Join(dir, verifyTestStageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{
		opts:           Options{RunDir: dir, Stages: []flow.Stage{stage}, RunID: "run1"},
		ui:             bus.NewUIBus(),
		runVerifyShell: runVerifyShellCommand,
	}
	return o, stageDir
}

func readManifest(t *testing.T, stageDir, verID string) stagefiles.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stagefiles.VerifyDir(stageDir), verID, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m stagefiles.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

// TestRunVerification_ShellRejectedCarriesLegacyTail — relocated legacy shell
// exec/exit-code/output coverage (see TestCheckCompletion_Verify in
// pkg/orchestrator/stagefiles/completion_test.go, which no longer runs
// verify at all): первый shell-шаг падает, второй не запускается вовсе.
func TestRunVerification_ShellRejectedCarriesLegacyTail(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "echo '3 tests failed'; exit 1"},
			{Kind: flow.VerifyShell, Run: "true"},
		}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var rejected *VerifyRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *VerifyRejectedError, got %v (%T)", err, err)
	}
	if rejected.Step != 1 {
		t.Errorf("want failing step 1, got %d", rejected.Step)
	}
	if !stagefiles.IsIncompleteWorkError(err) {
		t.Error("verify rejection must route through the incomplete-work policy")
	}
	if want := "3 tests failed"; !strings.Contains(err.Error(), want) {
		t.Errorf("expected legacy output tail in error, got %q", err.Error())
	}

	m := readManifest(t, stageDir, rejected.ReportID)
	if len(m.Steps) != 2 {
		t.Fatalf("expected 2 manifest steps, got %d", len(m.Steps))
	}
	if m.Steps[0].Outcome != "needs_changes" {
		t.Errorf("step 1 outcome = %q, want needs_changes", m.Steps[0].Outcome)
	}
	if m.Steps[1].Outcome != "not-run" {
		t.Errorf("step 2 outcome = %q, want not-run (fail-fast must stop the pass)", m.Steps[1].Outcome)
	}
}

// TestRunVerification_ShellPassThenAgentNeedsChanges — commit order (V2a
// §6.3): accepted result + report + manifest persisted BEFORE feedback.md,
// feedback.md written BEFORE the typed rejection is returned.
func TestRunVerification_ShellPassThenAgentNeedsChanges(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyShell, Run: "true"},
			{Kind: flow.VerifyAgent, Command: "codex"},
		}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(_ context.Context, _ flow.Stage, cmd, _, _, _ string) (verify.RunOutcome, error) {
		if cmd != "codex" {
			t.Errorf("unexpected agent command %q", cmd)
		}
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "found a blocking issue",
			Findings: []verify.Finding{{
				Blocking: true, Title: "bug", Requirement: "req", Evidence: "ev", MinimalFix: "fix",
			}},
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var rejected *VerifyRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *VerifyRejectedError, got %v (%T)", err, err)
	}
	if rejected.Step != 2 {
		t.Errorf("want failing step 2 (agent), got %d", rejected.Step)
	}

	// Commit order: accepted result + report + manifest already on disk...
	stepDir := stagefiles.StepDir(stageDir, rejected.ReportID, 2)
	if _, err := os.Stat(filepath.Join(stepDir, "result.json")); err != nil {
		t.Errorf("expected accepted result.json to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagefiles.VerifyDir(stageDir), rejected.ReportID, "report.md")); err != nil {
		t.Errorf("expected report.md to exist: %v", err)
	}
	m := readManifest(t, stageDir, rejected.ReportID)
	if m.Steps[0].Outcome != "pass" {
		t.Errorf("step 1 (shell) outcome = %q, want pass", m.Steps[0].Outcome)
	}
	if m.Steps[1].Outcome != "needs_changes" {
		t.Errorf("step 2 (agent) outcome = %q, want needs_changes", m.Steps[1].Outcome)
	}

	// ...and durable feedback.md is active with matching provenance.
	text, verID, ok := stagefiles.LoadActiveFeedback(stageDir)
	if !ok {
		t.Fatal("expected active feedback.md to exist")
	}
	if verID != rejected.ReportID {
		t.Errorf("feedback provenance verID = %q, want %q", verID, rejected.ReportID)
	}
	if !strings.Contains(text, "bug") {
		t.Errorf("expected blocking finding title in active feedback, got %q", text)
	}
}

// TestRunVerification_AgentPassAllSteps_ClearsFeedback — full pass returns
// nil and clears any stale active feedback from a previous rejected pass.
func TestRunVerification_AgentPassAllSteps_ClearsFeedback(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex"},
		}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	if err := stagefiles.WriteActiveFeedback(stageDir, "stale-ver", verify.ModelResult{
		SchemaVersion: 1, Verdict: verify.VerdictNeedsChanges, Summary: "old",
		Findings: []verify.Finding{{Blocking: true, Title: "t", Requirement: "r", Evidence: "e", MinimalFix: "f"}},
	}, "codex", 1); err != nil {
		t.Fatal(err)
	}

	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictPass,
			Summary:       "all good",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if err != nil {
		t.Fatalf("expected nil on full pass, got %v", err)
	}
	if _, _, ok := stagefiles.LoadActiveFeedback(stageDir); ok {
		t.Error("expected active feedback to be cleared after a full pass")
	}
}

// TestRunVerification_AgentNonZeroExitAfterPassText_NeverPasses — the model's
// own text is never authoritative: a non-OK process outcome must reject,
// even if Result somehow ended up non-nil (defensive: real executor never
// does this, but the engine must not trust Result unless ProcessOK).
func TestRunVerification_AgentNonZeroExitAfterPassText_NeverPasses(t *testing.T) {
	stage := flow.Stage{
		ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "codex"},
		}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		// Result is non-nil despite ProcessOK:false — the point of this test
		// is that a non-OK process outcome rejects EVEN IF the model's text
		// somehow decoded to a "pass" (the engine must check ProcessOK
		// before ever looking at Result).
		return verify.RunOutcome{ProcessOK: false, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictPass,
			Summary:       "claims pass but the process itself failed",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if stagefiles.IsIncompleteWorkError(err) {
		t.Error("VerifyExecError must not be classified as incomplete work (no free author retry)")
	}
	if _, _, ok := stagefiles.LoadActiveFeedback(stageDir); ok {
		t.Error("a failed process must never leave a defect feedback behind")
	}
	// Ищем ЛЮБОЙ accepted result.json под настоящим verify-каталогом стадии
	// (verID у движка случайный — фиксированный "whatever" здесь заведомо не
	// существовал бы независимо от корректности кода и ничего не проверял).
	matches, globErr := filepath.Glob(filepath.Join(stagefiles.VerifyDir(stageDir), "*", "step-*", "result.json"))
	if globErr != nil {
		t.Fatalf("glob accepted results: %v", globErr)
	}
	if len(matches) != 0 {
		t.Errorf("no accepted result should have been persisted, found: %v", matches)
	}
}

// TestRunVerification_AgentProtocolError_ExecError — an unparsable/corrupt
// model response must never pass and must never look like a rejection.
func TestRunVerification_AgentProtocolError_ExecError(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, ProtocolErr: errors.New("truncated JSON")}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "protocol error") {
		t.Errorf("expected protocol error text, got %q", err.Error())
	}
}

// TestRunVerification_AgentInconclusive_ExecError — inconclusive is neither
// pass nor a rejection: the engine cannot decide, so it fails closed.
func TestRunVerification_AgentInconclusive_ExecError(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictInconclusive,
			Summary:       "could not determine test coverage",
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if !strings.Contains(err.Error(), "inconclusive") {
		t.Errorf("expected inconclusive text, got %q", err.Error())
	}
}

// TestRunVerification_StorageFailureFailsClosed — a persist failure during
// the needs_changes commit chain must surface as VerifyExecError, never as
// a silent pass and never as a (partially-written) VerifyRejectedError.
func TestRunVerification_StorageFailureFailsClosed(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "blocking",
			Findings: []verify.Finding{{
				Blocking: true, Title: "t", Requirement: "r", Evidence: "e", MinimalFix: "f",
			}},
		}}, nil
	}

	if err := os.Chmod(stageDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stageDir, 0o755) })

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError (storage failure fails closed), got %v (%T)", err, err)
	}
	var rejected *VerifyRejectedError
	if errors.As(err, &rejected) {
		t.Error("a storage failure must never surface as VerifyRejectedError")
	}
}

// TestRunVerification_StaleAcceptedResultNotReused — a NEW invocation must
// never treat an old, on-disk accepted result (from a previous pass) as
// this pass's verdict: it always mints a fresh verification id and decides
// purely from THIS call's outcome.
func TestRunVerification_StaleAcceptedResultNotReused(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}
	o, stageDir := newVerifyTestOrchestrator(t, stage)

	// Seed a stale PASS from a previous verification id.
	staleResult := verify.ModelResult{SchemaVersion: 1, Verdict: verify.VerdictPass, Summary: "old pass"}
	if err := stagefiles.SaveAcceptedResult(stageDir, "old-verid", 1, staleResult); err != nil {
		t.Fatal(err)
	}

	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "actually broken",
			Findings: []verify.Finding{{
				Blocking: true, Title: "t", Requirement: "r", Evidence: "e", MinimalFix: "f",
			}},
		}}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)

	var rejected *VerifyRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected *VerifyRejectedError (fresh verdict, not the stale on-disk pass), got %v (%T)", err, err)
	}
	if rejected.ReportID == "old-verid" {
		t.Error("engine must mint a fresh verification id, not reuse the stale one")
	}
}

// TestRunVerification_Interrupted_PropagatesUserInterrupted — an external
// interruption must be handed to the caller as-is, never converted into a
// needs_changes rejection.
func TestRunVerification_Interrupted_PropagatesUserInterrupted(t *testing.T) {
	stage := flow.Stage{
		ID:     verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{Interrupted: true}, nil
	}

	err := o.RunVerification(context.Background(), stage, phaseImplementation)
	if !errors.Is(err, executor.ErrUserInterrupted) {
		t.Fatalf("expected executor.ErrUserInterrupted, got %v (%T)", err, err)
	}
}
