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

const verifyNameKey = "name"

func namedVerifyStage(id string, steps ...flow.VerifyStep) flow.Stage {
	return flow.Stage{ID: id, Verify: flow.VerifySpec{Steps: steps}}
}

func nameOf(n verifyNotice) (string, bool) {
	name, ok := n.Data[verifyNameKey].(string)
	return name, ok
}

func TestVerifyNotices_StartedAndResultCarryStepName(t *testing.T) {
	stage := namedVerifyStage(verifyTestStageID,
		flow.VerifyStep{Kind: flow.VerifyShell, Run: "true", Name: "Unit tests"},
		flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex", Name: "Code review"},
		flow.VerifyStep{Kind: flow.VerifyShell, Run: "true"},
	)
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return passOutcome(), nil
	}
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}

	notices := readVerifyNotices(t, o.opts.RunDir)
	for _, typ := range []bus.EventType{bus.EventVerifyStarted, bus.EventVerifyResult} {
		got := noticesOfType(notices, typ)
		if len(got) != 3 {
			t.Fatalf("%s notices = %d, want 3", typ, len(got))
		}
		for i, want := range []string{"Unit tests", "Code review", ""} {
			name, present := nameOf(got[i])
			if name != want || present != (want != "") {
				t.Errorf("%s step %d: name = %q (present=%v), want %q", typ, i+1, name, present, want)
			}
		}
	}
}

func TestVerifyNotices_RejectionCarriesStepName(t *testing.T) {
	stage := namedVerifyStage(verifyTestStageID, flow.VerifyStep{Kind: flow.VerifyShell, Run: "false", Name: "Unit tests"})
	o, _ := newVerifyTestOrchestrator(t, stage)
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err == nil {
		t.Fatal("expected a verify rejection")
	}
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("result notices = %+v, want one", results)
	}
	if name, _ := nameOf(results[0]); name != "Unit tests" {
		t.Errorf("rejection name = %q, want Unit tests", name)
	}
}

func TestVerifyNotices_RetryCarriesStepName(t *testing.T) {
	shrinkVerifyBackoff(t)
	stage := namedVerifyStage(verifyTestStageID, flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex", Name: "Code review"})
	o, _ := newVerifyTestOrchestrator(t, stage)
	calls := 0
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls++
		if calls == 1 {
			return verify.RunOutcome{ProcessOK: false}, nil
		}
		return passOutcome(), nil
	}
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	retries := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyRetry)
	if len(retries) != 1 {
		t.Fatalf("retry notices = %+v, want one", retries)
	}
	if name, _ := nameOf(retries[0]); name != "Code review" {
		t.Errorf("retry name = %q, want Code review", name)
	}
}

func TestVerifyNotices_TreeModifiedCarriesStepName(t *testing.T) {
	stage := namedVerifyStage(verifyTestStageID, flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex", Name: "Code review"})
	o, _ := newVerifyTestOrchestrator(t, stage)
	calls := 0
	o.fingerprintFn = func(string) (string, bool) {
		calls++
		return string(rune('a' + calls)), true
	}
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return passOutcome(), nil
	}
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err != nil {
		t.Fatalf("RunVerification: %v", err)
	}
	modified := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyTreeModified)
	if len(modified) != 1 {
		t.Fatalf("tree_modified notices = %+v, want one", modified)
	}
	if name, _ := nameOf(modified[0]); name != "Code review" {
		t.Errorf("tree_modified name = %q, want Code review", name)
	}
}

func TestVerifyNotices_ExecFailureCarriesStepName(t *testing.T) {
	stage := namedVerifyStage(verifyTestStageID, flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex", Name: "Code review"})
	o, _ := newVerifyTestOrchestrator(t, stage)
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		return verify.RunOutcome{}, errors.New("boom")
	}
	if err := o.RunVerification(context.Background(), stage, phaseImplementation); err == nil {
		t.Fatal("expected an exec failure")
	}
	results := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult)
	if len(results) != 1 {
		t.Fatalf("result notices = %+v, want one", results)
	}
	if name, _ := nameOf(results[0]); name != "Code review" {
		t.Errorf("exec-failure name = %q, want Code review", name)
	}
}

// verifyResultNotices читает только verify_result: в журнале retry-цикла есть notices со строковым data.
func verifyResultNotices(t *testing.T, runDir string) []verifyNotice {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices.jsonl: %v", err)
	}
	var out []verifyNotice
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, `"type":"`+string(bus.EventVerifyResult)+`"`) {
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

func TestVerifyNotices_StalemateAcceptCarriesStepName(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	o.fingerprintFn = func(string) (string, bool) { return "frozen", true }
	step := flow.VerifyStep{Kind: flow.VerifyAgent, Command: "codex", Name: "Code review"}
	stage := agentVerifyStage(1, 6, step)

	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { return nil },
		func() error { return newVerifyRejected(1) },
		func() { t.Error("onUserInterrupted should not be called") },
	)

	var stalemate []verifyNotice
	for _, n := range verifyResultNotices(t, runDir) {
		if n.Data[keyVerdict] == string(verify.VerdictAcceptedStalemate) {
			stalemate = append(stalemate, n)
		}
	}
	if len(stalemate) != 1 {
		t.Fatalf("stalemate notices = %+v, want one", stalemate)
	}
	if name, _ := nameOf(stalemate[0]); name != "Code review" {
		t.Errorf("stalemate name = %q, want Code review", name)
	}
}

// У двух стадий имена независимы: notice адресуется stage_id, а не именем шага.
func TestVerifyNotices_StepNamesAreScopedToTheirStage(t *testing.T) {
	first := namedVerifyStage("s1", flow.VerifyStep{Kind: flow.VerifyShell, Run: "true", Name: "Unit tests"})
	second := namedVerifyStage("s2", flow.VerifyStep{Kind: flow.VerifyShell, Run: "true", Name: "Smoke"})
	o, _ := newVerifyTestOrchestrator(t, first)
	o.opts.Stages = []flow.Stage{first, second}
	if err := os.MkdirAll(filepath.Join(o.opts.RunDir, "s2"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []flow.Stage{first, second} {
		if err := o.RunVerification(context.Background(), s, phaseImplementation); err != nil {
			t.Fatalf("RunVerification %s: %v", s.ID, err)
		}
	}

	got := map[string]string{}
	for _, n := range noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventVerifyResult) {
		got[n.StageID], _ = nameOf(n)
	}
	if got["s1"] != "Unit tests" || got["s2"] != "Smoke" {
		t.Errorf("names by stage = %v, want s1=Unit tests, s2=Smoke", got)
	}
}
