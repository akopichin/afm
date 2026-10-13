package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// createSession creates stageDir and persists a session file for phase — the fixture state a
// resumed agent start requires.
func createSession(t *testing.T, stageDir, phase string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := stagefiles.LoadOrCreateSession(stageDir, phase); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyReject_DropsSessionWhenFreshCorrectionOn — default config (fresh_correction defaults
// true): a verify-reject drops the session file so the correction attempt starts fresh.
func TestVerifyReject_DropsSessionWhenFreshCorrectionOn(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stage := flow.Stage{ID: "s1", Interactive: true}
	stageDir := filepath.Join(runDir, stage.ID)
	createSession(t, stageDir, phaseImplementation)

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
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
	if stagefiles.SessionExists(stageDir, phaseImplementation) {
		t.Fatal("expected session file to be dropped after verify-reject with fresh_correction=true (default)")
	}
}

// TestVerifyReject_KeepsSessionWhenFreshCorrectionOff — per-stage fresh_correction: false keeps the
// resumed session across a verify-reject correction.
func TestVerifyReject_KeepsSessionWhenFreshCorrectionOff(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	off := false
	stage := flow.Stage{
		ID:          "s1",
		Interactive: true,
		Verify: flow.VerifySpec{
			Steps:           []flow.VerifyStep{{Kind: flow.VerifyShell, Run: "true"}},
			FreshCorrection: &off,
		},
	}
	stageDir := filepath.Join(runDir, stage.ID)
	createSession(t, stageDir, phaseImplementation)

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
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
	if !stagefiles.SessionExists(stageDir, phaseImplementation) {
		t.Fatal("expected session file to be preserved when fresh_correction=false")
	}
}

// TestIncompleteNonVerify_KeepsSession — the session drop is strictly scoped to the verify-reject
// branch; a plain (non-verify) incomplete-work retry must not touch the session file.
func TestIncompleteNonVerify_KeepsSession(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stage := flow.Stage{ID: "s1", Interactive: true}
	stageDir := filepath.Join(runDir, stage.ID)
	createSession(t, stageDir, phaseImplementation)

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error {
			if attempts == 1 {
				return &stagefiles.IncompleteWorkError{Reason: "plan not finished"}
			}
			return nil
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !stagefiles.SessionExists(stageDir, phaseImplementation) {
		t.Fatal("expected session file to be preserved for non-verify incomplete-work retries")
	}
}

// TestVerifyReject_DropSessionIsNoopWithoutSessionFile — non-interactive stage, no session file ever
// created: the unconditional os.Remove on verify-reject must be a harmless no-op, not a panic/hang.
func TestVerifyReject_DropSessionIsNoopWithoutSessionFile(t *testing.T) {
	o, _ := setupHookOrch(t, "s1")
	o.maxRetries = 15
	stage := flow.Stage{ID: "s1"} // Interactive: false, no session ever created

	var attempts int
	o.runWithRetry(context.Background(), stage, phaseImplementation,
		func(string) error { attempts++; return nil },
		func() error {
			if attempts == 1 {
				return newVerifyRejected(1)
			}
			return nil
		},
		func() { t.Error("onUserInterrupted should not be called") },
	)

	if attempts != 2 {
		t.Fatalf("expected 2 attempts (retry still happens normally), got %d", attempts)
	}
}
