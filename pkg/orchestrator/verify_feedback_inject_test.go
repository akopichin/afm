package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
)

// TestVerifyFeedbackBlock_ReadsActiveFeedback — unit coverage for the small
// helper itself: empty when there is no active verify feedback, otherwise the
// heading + the full stagefiles.WriteActiveFeedback text.
func TestVerifyFeedbackBlock_ReadsActiveFeedback(t *testing.T) {
	o := &Orchestrator{}
	stageDir := t.TempDir()

	if got := o.verifyFeedbackBlock(stageDir); got != "" {
		t.Errorf("expected empty block when no active feedback exists, got %q", got)
	}

	result := verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictNeedsChanges, Summary: "s",
		Findings: []verify.Finding{{Blocking: true, Title: "nil deref", Requirement: "handle nil", Evidence: "panic", MinimalFix: "add check"}},
	}
	if err := stagefiles.WriteActiveFeedback(stageDir, "ver1", result, "codex", 1); err != nil {
		t.Fatal(err)
	}

	got := o.verifyFeedbackBlock(stageDir)
	if !strings.Contains(got, verifyFeedbackHeading) {
		t.Errorf("expected heading %q in block, got %q", verifyFeedbackHeading, got)
	}
	if !strings.Contains(got, "nil deref") || !strings.Contains(got, "add check") {
		t.Errorf("expected the blocking finding in block, got %q", got)
	}
}

// fakeAgentRunner is a minimal executor.Runner stub that just records the
// prompt passed to RunAgent and returns nil (success) without touching disk —
// enough to inspect the RetryContext assembly of a single runner call without
// spinning up a real subprocess.
type fakeAgentRunner struct {
	onAgent func(prompt string)
}

func (r *fakeAgentRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return nil
}

func (r *fakeAgentRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	if r.onAgent != nil {
		r.onAgent(prompt)
	}
	return nil
}

func (r *fakeAgentRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return nil, nil
}

// TestRunImplementationWithFeedback_InjectsVerifyFeedbackAlongsideHumanNote —
// V4b.5: the manual-retry/WithFeedback path must inject BOTH the human note
// (feedback.md, agent_suggest/Revise) and the machine verify feedback
// (verify/feedback.md) into the same prompt, and must NEVER overwrite the
// human file — only stagefiles.LoadActiveFeedback (read), never SaveFeedback.
func TestRunImplementationWithFeedback_InjectsVerifyFeedbackAlongsideHumanNote(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	const humanNote = "please also update docs"
	if err := os.WriteFile(filepath.Join(stageDir, "feedback.md"), []byte(humanNote), 0644); err != nil {
		t.Fatal(err)
	}
	result := verify.ModelResult{
		SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictNeedsChanges, Summary: "s",
		Findings: []verify.Finding{{Blocking: true, Title: "nil deref", Requirement: "handle nil", Evidence: "panic", MinimalFix: "add check"}},
	}
	if err := stagefiles.WriteActiveFeedback(stageDir, "ver9", result, "codex", 1); err != nil {
		t.Fatal(err)
	}

	var capturedPrompt string
	fake := &fakeAgentRunner{onAgent: func(prompt string) { capturedPrompt = prompt }}
	o.opts.Runner = fake
	o.runner = fake

	o.runImplementationWithFeedback(context.Background(), flow.Stage{ID: "s1"})

	if !strings.Contains(capturedPrompt, humanNote) {
		t.Errorf("expected the human feedback note in the prompt, got:\n%s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "nil deref") || !strings.Contains(capturedPrompt, "add check") {
		t.Errorf("expected the verify feedback in the prompt, got:\n%s", capturedPrompt)
	}

	data, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != humanNote {
		t.Errorf("human feedback.md must not be modified by verify injection, got %q", string(data))
	}
}
