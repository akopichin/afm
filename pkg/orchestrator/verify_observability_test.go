package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// verifyNotice mirrors the on-disk shape stagefiles.AppendNotice writes (see
// TestExecScript_PersistsOutputToNotices in hooks_test.go for the established pattern this file follows).
type verifyNotice struct {
	Type    string         `json:"type"`
	StageID string         `json:"stage_id"`
	Data    map[string]any `json:"data"`
}

// readVerifyNotices reads every line of <runDir>/notices.jsonl and parses it — "" (no file yet) returns an
// empty slice, matching "a stage that never verifies emits none".
func readVerifyNotices(t *testing.T, runDir string) []verifyNotice {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read notices.jsonl: %v", err)
	}
	var out []verifyNotice
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var n verifyNotice
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("unmarshal notice %q: %v", line, err)
		}
		out = append(out, n)
	}
	return out
}

// drainUIBus consumes buffered events; RunVerification publishes them synchronously before returning.
func drainUIBus(ch <-chan bus.Event, fn func(bus.Event)) {
	for {
		select {
		case ev := <-ch:
			fn(ev)
		default:
			return
		}
	}
}

func noticesOfType(notices []verifyNotice, typ bus.EventType) []verifyNotice {
	var out []verifyNotice
	for _, n := range notices {
		if n.Type == string(typ) {
			out = append(out, n)
		}
	}
	return out
}

func TestRunVerification_EmitsStartAndResultNotices_AgentPass(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)
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

	var liveStarted, liveResult int
	drainUIBus(ch, func(ev bus.Event) {
		switch ev.Type {
		case bus.EventVerifyStarted:
			liveStarted++
		case bus.EventVerifyResult:
			liveResult++
		default:
		}
	})
	if liveStarted != 1 || liveResult != 1 {
		t.Fatalf("expected exactly one live verify_started and one verify_result, got started=%d result=%d", liveStarted, liveResult)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	started := noticesOfType(notices, bus.EventVerifyStarted)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(started) != 1 {
		t.Fatalf("expected exactly one durable verify_started notice, got %d", len(started))
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly one durable verify_result notice, got %d", len(results))
	}

	sd := started[0].Data
	if sd["verification_id"] == "" || sd["verification_id"] == nil {
		t.Error("verify_started notice missing verification_id")
	}
	if got := sd["step"]; got != float64(1) {
		t.Errorf("verify_started step = %v, want 1", got)
	}
	if sd["kind"] != verifyKindAgent || sd["command"] != "codex" {
		t.Errorf("verify_started kind/command = %v/%v, want agent/codex", sd["kind"], sd["command"])
	}

	rd := results[0].Data
	if rd["verdict"] != "pass" {
		t.Errorf("verify_result verdict = %v, want pass", rd["verdict"])
	}
	if _, hasExecErr := rd["exec_error"]; hasExecErr {
		t.Error("a real verdict must not carry exec_error")
	}
	path, _ := rd["report_path"].(string)
	if path == "" {
		t.Error("expected a non-empty report_path for a pass")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("report_path %q does not exist: %v", path, err)
	}
	if results[0].StageID != verifyTestStageID || started[0].StageID != verifyTestStageID {
		t.Error("notices must carry the stage id")
	}
}

func TestRunVerification_EmitsResultNotice_AgentRejected(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion,
			Verdict:       verify.VerdictNeedsChanges,
			Summary:       "missing tests",
			Findings: []verify.Finding{{
				Blocking: true, Title: "no tests", Requirement: "add tests", Evidence: "none", MinimalFix: "write tests",
			}},
		}}, nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err == nil {
		t.Fatal("expected a rejection error")
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected exactly one verify_result notice, got %d", len(results))
	}
	if results[0].Data["verdict"] != "needs_changes" {
		t.Errorf("verdict = %v, want needs_changes", results[0].Data["verdict"])
	}
	if path, _ := results[0].Data["report_path"].(string); path == "" {
		t.Error("expected a non-empty report_path for a rejection")
	}
}

// TestRunVerification_EmitsResultNotice_ProtocolError covers the
// exec-error (no verdict at all) path.
func TestRunVerification_EmitsResultNotice_ProtocolError(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{ProcessOK: true, ProtocolErr: errUnparsable}, nil
	}

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err == nil {
		t.Fatal("expected a *VerifyExecError")
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected exactly one verify_result notice, got %d", len(results))
	}
	if _, hasVerdict := results[0].Data["verdict"]; hasVerdict {
		t.Error("an exec error must not carry a verdict")
	}
	if results[0].Data["exec_error"] == "" || results[0].Data["exec_error"] == nil {
		t.Error("expected a non-empty exec_error kind")
	}
	if reason, _ := results[0].Data["reason"].(string); !strings.Contains(reason, "protocol error") {
		t.Errorf("expected the reason to mention the protocol error, got %q", reason)
	}
	if _, hasPath := results[0].Data["report_path"]; hasPath {
		t.Error("no report.md is written for an exec error — report_path must be omitted")
	}
}

// TestRunVerification_EmitsResultNotice_Interrupted checks that an external
// interruption is classified distinctly (exec_error="interrupted").
func TestRunVerification_EmitsResultNotice_Interrupted(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{Interrupted: true}, nil
	}

	_ = o.RunVerification(context.Background(), stage, phaseImplementation)

	notices := readVerifyNotices(t, o.opts.RunDir)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected exactly one verify_result notice, got %d", len(results))
	}
	if results[0].Data["exec_error"] != "interrupted" {
		t.Errorf("exec_error = %v, want interrupted", results[0].Data["exec_error"])
	}
}

func TestRunVerification_EmitsNoticesForShellSteps(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "exit 1"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err == nil {
		t.Fatal("expected a rejection error")
	}
	notices := readVerifyNotices(t, o.opts.RunDir)
	started := noticesOfType(notices, bus.EventVerifyStarted)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(started) != 1 || len(results) != 1 {
		t.Fatalf("expected exactly one started/result pair for the shell step, got started=%d result=%d", len(started), len(results))
	}
	if started[0].Data["kind"] != verifyKindShell || started[0].Data["command"] != "exit 1" {
		t.Errorf("verify_started kind/command = %v/%v, want shell/'exit 1'", started[0].Data["kind"], started[0].Data["command"])
	}
	if results[0].Data["verdict"] != "needs_changes" {
		t.Errorf("verdict = %v, want needs_changes", results[0].Data["verdict"])
	}
}

func TestRunVerification_EmitsNoticeForPassingShellStep(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID,
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "true"}}}}
	o, _ := newVerifyTestOrchestrator(t, stage)

	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	notices := readVerifyNotices(t, o.opts.RunDir)
	results := noticesOfType(notices, bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("expected exactly one verify_result notice, got %d", len(results))
	}
	if results[0].Data["verdict"] != "pass" {
		t.Errorf("verdict = %v, want pass", results[0].Data["verdict"])
	}
	if _, hasPath := results[0].Data["report_path"]; hasPath {
		t.Error("no report.md is written for a passing shell step — report_path must be omitted")
	}
}

// TestGateWithVerify_EmptyVerifyEmitsNoNotices — a stage that never verifies
// (no Verify spec) must never touch notices.jsonl at all.
func TestGateWithVerify_EmptyVerifyEmitsNoNotices(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID} // Verify is empty
	o, _ := newVerifyTestOrchestrator(t, stage)

	check := o.gateWithVerify(context.Background(), stage, phaseImplementation, func() error { return nil })
	if err := check(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	if notices := readVerifyNotices(t, o.opts.RunDir); len(notices) != 0 {
		t.Fatalf("expected zero notices for a stage without verify, got %d", len(notices))
	}
}

func TestRunVerification_NonPassingVerdictObservations(t *testing.T) {
	for _, tc := range []struct {
		name, reason, noticeReason, outcome string
		verdict                             verify.Verdict
	}{
		{"inconclusive", "verify inconclusive: uncertain", "uncertain", "inconclusive", verify.VerdictInconclusive},
		{"unknown", `verify produced unknown verdict "unknown"`, `verify produced unknown verdict "unknown"`, verifyOutcomeError, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyAgent, Command: "codex"},
			}}}
			o, stageDir := newVerifyTestOrchestrator(t, stage)
			o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
				return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
					SchemaVersion: verify.SupportedSchemaVersion, Verdict: tc.verdict, Summary: "uncertain",
				}}, nil
			}
			err := o.RunVerification(context.Background(), stage, phaseImplementation)
			var execErr *VerifyExecError
			if !errors.As(err, &execErr) || execErr.Reason != tc.reason || execErr.Step != 1 || execErr.Transient {
				t.Fatalf("verdict error = %v (%T), want %q at step 1", err, err, tc.reason)
			}
			notices := readVerifyNotices(t, o.opts.RunDir)
			if len(notices) != 2 || notices[0].Type != string(bus.EventVerifyStarted) || notices[1].Type != string(bus.EventVerifyResult) {
				t.Fatalf("verdict notices = %+v", notices)
			}
			d := notices[1].Data
			if d[keyVerifyReason] != tc.noticeReason {
				t.Errorf("notice reason = %v, want %q", d[keyVerifyReason], tc.noticeReason)
			}
			if tc.verdict == verify.VerdictInconclusive {
				if d[keyVerdict] != string(tc.verdict) || d[keyExecErrorKind] != nil {
					t.Errorf("inconclusive notice = %+v", d)
				}
			} else if d[keyVerdict] != nil || d[keyExecErrorKind] != execErrorKindExecFailure {
				t.Errorf("unknown verdict notice = %+v", d)
			}
			if m := readManifest(t, stageDir, d[keyVerificationID].(string)); m.Steps[0].Outcome != tc.outcome {
				t.Errorf("verdict manifest = %+v", m.Steps)
			}
		})
	}
}

// errUnparsable is a fixed sentinel used only to build a ProtocolErr in the
// tests above — its text is asserted against, so it must stay stable.
var errUnparsable = protocolErr{}

type protocolErr struct{}

func (protocolErr) Error() string { return "protocol error: truncated JSON" }
