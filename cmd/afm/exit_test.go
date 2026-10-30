package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

// afmExitTestChildEnv selects a fixture and runs check in a child test process, exiting with runMain's
// code.
const afmExitTestChildEnv = "AFM_EXIT_TEST_CHILD"

func TestMain(m *testing.M) {
	if dir := os.Getenv(afmExitTestChildEnv); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(9) //nolint:revive // deliberate custom exit code, see comment above
		}
		os.Exit(runMain([]string{"check"})) //nolint:revive // deliberate custom exit code, see comment above
		return
	}
	// No explicit os.Exit(m.Run()) needed: the `go test` runner applies
	// m.Run()'s exit code automatically once TestMain returns (Go 1.15+).
	m.Run()
}

func TestExitError_ProcessExitCode(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, ".afm", "runs", "flow-20260101-120000")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}

	tr := state.Transition{
		Seq: 1, Time: time.Now(), StageID: cmdInit,
		From: state.StatusPending, To: state.StatusDone, Event: "test_setup",
	}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), data, 0644); err != nil {
		t.Fatal(err)
	}
	// A complete (newline-terminated) line that isn't valid JSON: Load
	// returns ErrCorruptUsage for this, never a torn-tail false positive.
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not valid json\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// os.Args[0] is the path to this very test binary (compiled by `go test`), not user/network input — the
	// standard os/exec "helper process" re-exec pattern.
	cmd := exec.Command(os.Args[0]) //nolint:gosec // G702: os.Args[0] here is the test binary itself
	cmd.Env = append(os.Environ(), afmExitTestChildEnv+"="+dir)
	runErr := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("expected the subprocess to exit non-zero via *exec.ExitError, got err=%v", runErr)
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Fatalf("subprocess exit code = %d, want 3", got)
	}
}

func TestRunMain_ExitErrorSilentSuppressesPrinting(t *testing.T) {
	prev := rootDir
	t.Cleanup(func() { rootDir = prev })

	dir := t.TempDir()
	runDir := filepath.Join(dir, ".afm", "runs", "flow-20260101-120000")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not valid json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tr := state.Transition{
		Seq: 1, Time: time.Now(), StageID: cmdInit,
		From: state.StatusPending, To: state.StatusDone, Event: "test_setup",
	}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if got := runMain([]string{"check", "--dir", dir}); got != 3 {
			t.Errorf("runMain([\"check\"]) with a corrupt ledger = %d, want 3", got)
		}
	})
	if got := out; got == "" {
		t.Error("expected check's own stdout table even on a corrupt ledger, got empty output")
	}
}

// TestRunExitError_InterruptIsNotAUsageError: Ctrl+C ends the run with a silent exit 130 (main prints no usage);
// any other run error stays an ordinary error.
func TestRunExitError_InterruptIsNotAUsageError(t *testing.T) {
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	var exitErr *ExitError
	if err := runExitError(interrupted, context.Canceled, ""); !errors.As(err, &exitErr) || !exitErr.Silent || exitErr.Code != interruptedExitCode {
		t.Fatalf("interrupted run error = %v, want a silent ExitError with code %d", err, interruptedExitCode)
	}

	boom := errors.New("boom")
	if err := runExitError(context.Background(), boom, ""); !errors.Is(err, boom) || errors.As(err, &exitErr) {
		t.Fatalf("failed run error = %v, want the wrapped run error", err)
	}
}
