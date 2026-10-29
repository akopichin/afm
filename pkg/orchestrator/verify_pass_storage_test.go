package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

func TestRunVerification_QuotaManifestStorageOutcomes(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken=%t", broken), func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyAgent, Command: "codex"},
			}}}
			o, stageDir := newVerifyTestOrchestrator(t, stage)
			limit := &executor.UsageLimitError{Provider: "codex", Message: "quota exhausted"}
			var verID, manifestPath string
			o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, _, rawPath string) (verify.RunOutcome, error) {
				passDir := filepath.Dir(filepath.Dir(rawPath))
				verID = filepath.Base(passDir)
				manifestPath = filepath.Join(passDir, "manifest.json")
				if broken {
					if err := os.Mkdir(manifestPath, 0755); err != nil {
						t.Fatal(err)
					}
				}
				return verify.RunOutcome{}, limit
			}
			err := o.RunVerification(context.Background(), stage, phaseImplementation)
			results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
			var quotaErr *verifyLimitError
			if !broken {
				if !errors.As(err, &quotaErr) || quotaErr.Limit != limit || quotaErr.Step != 1 || quotaErr.VerifyID != verID {
					t.Fatalf("quota error = %v (%T)", err, err)
				}
				if len(results) != 0 {
					t.Fatalf("quota park emitted result notices: %+v", results)
				}
				if m := readManifest(t, stageDir, verID); m.Steps[0].Outcome != verifyOutcomeQuotaWait {
					t.Fatalf("quota manifest = %+v", m.Steps)
				}
				return
			}
			if errors.As(err, &quotaErr) || errors.Is(err, limit) {
				t.Fatalf("storage failure must not remain a quota error: %v", err)
			}
			execErr := assertVerifyStorageError(t, err, 1, "rename "+manifestPath+": ")
			if len(results) != 1 {
				t.Fatalf("result notices = %+v, want one storage failure", results)
			}
			assertVerifyStorageNotice(t, results[0], execErr, verifyKindAgent, "codex")
			// The failed rename leaves the attempted manifest available without another write seam.
			data, readErr := os.ReadFile(manifestPath + ".tmp")
			if readErr != nil || !strings.Contains(string(data), `"outcome": "awaiting_quota"`) {
				t.Fatalf("attempted quota manifest = %s, error %v", data, readErr)
			}
		})
	}
}

func TestRunVerification_QuotaResumeStorageFailure(t *testing.T) {
	stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
		{Kind: flow.VerifyAgent, Command: "codex"},
		{Kind: flow.VerifyAgent, Command: "codex"},
	}}}
	o, stageDir := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		t.Fatal("corrupt resume must fail before starting an agent")
		return verify.RunOutcome{}, nil
	}
	quota := state.QuotaWait{VerifyID: "v-parked", VerifyStep: 2}
	ctx := withVerifyResume(context.Background(), quota, 0)
	_, loadErr := loadVerifyManifest(stageDir, quota.VerifyID)
	err := o.RunVerification(ctx, stage, phaseImplementation)
	execErr := assertVerifyStorageError(t, err, 2, loadErr.Error())
	if execErr.Reason != "verify storage failure: "+loadErr.Error() {
		t.Fatalf("resume reason = %q", execErr.Reason)
	}
	notices := readVerifyNotices(t, o.opts.RunDir)
	if len(notices) != 1 || notices[0].Type != string(bus.EventVerifyResult) {
		t.Fatalf("resume notices = %+v", notices)
	}
	assertVerifyStorageNotice(t, notices[0], execErr, "", "")
	if notices[0].Data[keyVerificationID] != quota.VerifyID {
		t.Errorf("resume verification id = %v", notices[0].Data[keyVerificationID])
	}
}

func TestRunVerification_PassStorageOutcomes(t *testing.T) {
	for _, target := range []string{"result.json", "report.md", "manifest.json"} {
		t.Run(target, func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyAgent, Command: "codex"},
			}}}
			o, stageDir := newVerifyTestOrchestrator(t, stage)
			var verID, blockedPath string
			o.runVerifyAgent = func(_ context.Context, _ flow.Stage, _, _, _, rawPath string) (verify.RunOutcome, error) {
				stepDir := filepath.Dir(rawPath)
				passDir := filepath.Dir(stepDir)
				verID = filepath.Base(passDir)
				blockedPath = filepath.Join(passDir, target)
				if target == "result.json" {
					blockedPath = filepath.Join(stepDir, target)
				}
				if err := os.Mkdir(blockedPath, 0755); err != nil {
					t.Fatal(err)
				}
				return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
					SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "accepted",
				}}, nil
			}
			err := o.RunVerification(context.Background(), stage, phaseImplementation)
			step := 1
			if target == "manifest.json" {
				step = 0
			}
			execErr := assertVerifyStorageError(t, err, step, "rename "+blockedPath+": ")
			results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
			if len(results) != 1 {
				t.Fatalf("result notices = %+v, want one", results)
			}
			if target == "manifest.json" {
				if results[0].Data[keyVerdict] != string(verify.VerdictPass) || results[0].Data[keyVerifyReason] != "accepted" {
					t.Fatalf("final manifest failure must retain the earlier pass notice: %+v", results[0])
				}
				if _, exists := results[0].Data[keyExecErrorKind]; exists {
					t.Fatal("the pass notice must not be rewritten as an execution failure")
				}
				return
			}
			assertVerifyStorageNotice(t, results[0], execErr, verifyKindAgent, "codex")
			if m := readManifest(t, stageDir, verID); m.Steps[0].Outcome != verifyOutcomeError {
				t.Errorf("storage failure manifest = %+v", m.Steps)
			}
		})
	}
}

func assertVerifyStorageError(t *testing.T, err error, step int, detail string) *VerifyExecError {
	t.Helper()
	var execErr *VerifyExecError
	if !errors.As(err, &execErr) {
		t.Fatalf("expected *VerifyExecError, got %v (%T)", err, err)
	}
	if execErr.Step != step || execErr.Transient || !strings.HasPrefix(execErr.Reason, "verify storage failure: "+detail) {
		t.Fatalf("storage error = %+v, want step %d and reason prefix %q", execErr, step, detail)
	}
	want := "verify execution failed: " + execErr.Reason
	if step > 0 {
		want = fmt.Sprintf("verify execution failed at step %d: %s", step, execErr.Reason)
	}
	if err.Error() != want {
		t.Errorf("storage error text = %q, want %q", err.Error(), want)
	}
	return execErr
}

func TestRunVerification_ShellStorageOutcomes(t *testing.T) {
	for _, target := range []string{"step-dir", "command.log", "manifest.json"} {
		t.Run(target, func(t *testing.T) {
			stage := flow.Stage{ID: verifyTestStageID, Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
				{Kind: flow.VerifyShell, Run: "check"},
			}}}
			o, stageDir := newVerifyTestOrchestrator(t, stage)
			var verID string
			o.runVerifyShell = func(context.Context, string, string) (string, error) {
				started := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyStarted)
				verID = started[0].Data[keyVerificationID].(string)
				stepDir := stagefiles.StepDir(stageDir, verID, 1)
				if target == "step-dir" {
					if err := os.MkdirAll(filepath.Dir(stepDir), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(stepDir, nil, 0644); err != nil {
						t.Fatal(err)
					}
				} else {
					path := filepath.Join(stepDir, target)
					if target == "manifest.json" {
						path = filepath.Join(filepath.Dir(stepDir), target)
					}
					if err := os.MkdirAll(path, 0755); err != nil {
						t.Fatal(err)
					}
				}
				return "output", nil
			}
			err := o.RunVerification(context.Background(), stage, phaseImplementation)
			step := 1
			if target == "manifest.json" {
				step = 0
			}
			execErr := assertVerifyStorageError(t, err, step, "")
			results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
			if len(results) != 1 {
				t.Fatalf("result notices = %+v, want one", results)
			}
			if target == "manifest.json" {
				if results[0].Data[keyVerdict] != string(verify.VerdictPass) || results[0].Data[keyVerifyReason] != "passed" {
					t.Fatalf("final manifest failure must retain the earlier shell pass notice: %+v", results[0])
				}
				return
			}
			assertVerifyStorageNotice(t, results[0], execErr, verifyKindShell, "check")
			if m := readManifest(t, stageDir, verID); m.Steps[0].Outcome != verifyOutcomeError {
				t.Errorf("storage failure manifest = %+v", m.Steps)
			}
		})
	}
}

func assertVerifyStorageNotice(t *testing.T, notice verifyNotice, execErr *VerifyExecError, kind, command string) {
	t.Helper()
	d := notice.Data
	if notice.StageID != verifyTestStageID || d[keyVerifyReason] != execErr.Reason ||
		d[keyExecErrorKind] != execErrorKindExecFailure || d[keyStep] != float64(execErr.Step) ||
		d[keyVerifyKind] != kind || d[keyVerifyCommand] != command {
		t.Errorf("storage failure notice = %+v, error = %+v", notice, execErr)
	}
	if _, exists := d[keyVerdict]; exists {
		t.Error("storage failure notice must not carry a verdict")
	}
}
