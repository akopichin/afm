package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// newGateVerifyTestOrchestrator строит минимальный Orchestrator для тестов
// gateWithVerify напрямую — тот же приём, что newVerifyTestOrchestrator в
// verify_test.go (RunVerification не трогает FSM/store/UI).
func newGateVerifyTestOrchestrator(t *testing.T, stages ...flow.Stage) *Orchestrator {
	t.Helper()
	return &Orchestrator{opts: Options{RunDir: t.TempDir(), Stages: stages, RunID: "run1"}}
}

// TestGateWithVerify_ProbeFailsSkipsVerify — файл-проба идёт ПЕРВОЙ: если она
// не прошла, gateWithVerify возвращает её ошибку как есть, вообще не запуская
// verify (GET/status/refresh не должны триггерить AI).
func TestGateWithVerify_ProbeFailsSkipsVerify(t *testing.T) {
	s := flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o := newGateVerifyTestOrchestrator(t, s)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		t.Fatal("verify must not run when the file probe fails")
		return verify.RunOutcome{}, nil
	}

	probeErr := errors.New("probe not satisfied")
	check := o.gateWithVerify(context.Background(), s, phaseImplementation, func() error { return probeErr })
	if err := check(); !errors.Is(err, probeErr) {
		t.Fatalf("expected the probe error unchanged, got %v", err)
	}
}

// TestGateWithVerify_EmptyVerifyNeverRunsVerification — стадия без verify:
// нулевое изменение поведения, completionCheck ведёт себя ровно как baseCheck.
func TestGateWithVerify_EmptyVerifyNeverRunsVerification(t *testing.T) {
	s := flow.Stage{ID: "s1"} // Verify пуст
	o := newGateVerifyTestOrchestrator(t, s)

	check := o.gateWithVerify(context.Background(), s, phaseImplementation, func() error { return nil })
	if err := check(); err != nil {
		t.Fatalf("expected nil (probe passed, no verify configured), got %v", err)
	}
}

// TestGateWithVerify_ProbePassesRunsVerification — проба прошла, verify
// настроен и фаза исполнительская: verify реально вызывается, а его вердикт
// становится итоговой ошибкой completionCheck.
func TestGateWithVerify_ProbePassesRunsVerification(t *testing.T) {
	s := flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o := newGateVerifyTestOrchestrator(t, s)
	called := false
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		called = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	check := o.gateWithVerify(context.Background(), s, phaseImplementation, func() error { return nil })
	if err := check(); err != nil {
		t.Fatalf("expected nil on full pass, got %v", err)
	}
	if !called {
		t.Error("expected RunVerification to invoke the verify agent step")
	}
}

// TestGateWithVerify_AutonomousGatesAfterSummaryProbe — CheckAutonomousCompletion
// (базовая проба для [auto]-стадий) идёт первой; verify запускается только
// после того, как она реально прошла (новое поведение для [auto]+verify).
func TestGateWithVerify_AutonomousGatesAfterSummaryProbe(t *testing.T) {
	s := flow.Stage{ID: "s1", Agents: []flow.AgentType{flow.AgentAuto},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o := newGateVerifyTestOrchestrator(t, s)
	called := false
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		called = true
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	probeErr := errors.New("execution_summary.md missing")
	check := o.gateWithVerify(context.Background(), s, phaseAutonomous, func() error { return probeErr })
	if err := check(); !errors.Is(err, probeErr) {
		t.Fatalf("expected the probe error unchanged when summary is missing, got %v", err)
	}
	if called {
		t.Error("verify must not run before the summary probe passes")
	}
}

// TestGateWithVerify_PlanningPhaseNeverGated — belt-and-suspenders: даже если
// кто-то по ошибке обернёт completionCheck планировочного раннера,
// gateWithVerify не должен запускать verify для фазы planning (в проде этого
// не происходит — раннеры планирования вообще не оборачиваются, см. agents.go;
// V1 к тому же запрещает verify на planning-only стадии на этапе parse).
func TestGateWithVerify_PlanningPhaseNeverGated(t *testing.T) {
	s := flow.Stage{ID: "s1", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o := newGateVerifyTestOrchestrator(t, s)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		t.Fatal("verify must never run for phasePlanning")
		return verify.RunOutcome{}, nil
	}

	check := o.gateWithVerify(context.Background(), s, phasePlanning, func() error { return nil })
	if err := check(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
