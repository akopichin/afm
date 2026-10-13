package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// writeAnsweredQA writes one answered Q&A pair to <stageDir>/<phase>.dialog.jsonl — the fixture
// dialogDigestBlock renders into the retry prompt for a fresh (non-resumed) agent start.
func writeAnsweredQA(t *testing.T, stageDir, phase string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := mcp.DialogPath(stageDir, phase)
	if err := mcp.AppendQuestion(path, mcp.Question{ID: "q1", Question: "Which approach?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(path, mcp.Answer{ID: "q1", Answer: "Use approach A."}); err != nil {
		t.Fatal(err)
	}
}

// TestRetryContext_DigestForNonInteractive — non-interactive stage never resumes (willResume is
// always false for it), so a plain (non-verify) incomplete-work retry must carry the Q&A digest.
func TestRetryContext_DigestForNonInteractive(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stage := flow.Stage{ID: "s1"} // Interactive: false (default)
	stageDir := filepath.Join(runDir, stage.ID)
	writeAnsweredQA(t, stageDir, phaseImplementation)

	var contexts []string
	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(retryContext string) error {
			attempts++
			contexts = append(contexts, retryContext)
			return nil
		},
		func() error {
			if attempts == 1 {
				return &stagefiles.IncompleteWorkError{Reason: "not done yet"}
			}
			return nil
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !strings.Contains(contexts[1], dialogDigestHeading) {
		t.Fatalf("expected digest heading in second attempt's retryContext, got %q", contexts[1])
	}
}

// TestRetryContext_NoDigestWhenResuming — a genuinely resumed agent (session preserved across the
// verify-reject because fresh_correction is off) already has the Q&A in its own conversation; the
// digest must NOT be injected, to avoid duplicating it.
func TestRetryContext_NoDigestWhenResuming(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	off := false
	o.opts.Config.Verify = config.VerifyConfig{FreshCorrection: &off}
	stage := flow.Stage{ID: "s1", Interactive: true}
	stageDir := filepath.Join(runDir, stage.ID)
	writeAnsweredQA(t, stageDir, phaseImplementation)
	if _, err := stagefiles.LoadOrCreateSession(stageDir, phaseImplementation); err != nil {
		t.Fatal(err)
	}

	var contexts []string
	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(retryContext string) error {
			attempts++
			contexts = append(contexts, retryContext)
			return nil
		},
		func() error {
			if attempts == 1 {
				return newVerifyRejected(1)
			}
			return nil
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if strings.Contains(contexts[1], dialogDigestHeading) {
		t.Fatalf("expected NO digest heading when genuinely resuming, got %q", contexts[1])
	}
	if !stagefiles.SessionExists(stageDir, phaseImplementation) {
		t.Fatal("expected session file to be preserved with fresh_correction=false")
	}
}

// TestRetryContext_DigestWhenSessionDropped — interactive stage WITHOUT a prior session file:
// willResume is false (nothing to resume) even though Interactive==true, so the corrector's fresh
// start must get the digest.
func TestRetryContext_DigestWhenSessionDropped(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stage := flow.Stage{ID: "s1", Interactive: true}
	stageDir := filepath.Join(runDir, stage.ID)
	writeAnsweredQA(t, stageDir, phaseImplementation)
	// No session file created beforehand.

	var contexts []string
	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(retryContext string) error {
			attempts++
			contexts = append(contexts, retryContext)
			return nil
		},
		func() error {
			if attempts == 1 {
				return newVerifyRejected(1)
			}
			return nil
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !strings.Contains(contexts[1], dialogDigestHeading) {
		t.Fatalf("expected digest heading on second attempt, got %q", contexts[1])
	}
}
