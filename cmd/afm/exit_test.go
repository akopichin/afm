package main

import (
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

// afmExitTestChildEnv triggers this file's TestMain child mode: when set (to
// a fixture directory), the test binary skips the normal test suite,
// chdirs into the fixture and runs `runMain(["check"])` directly, then
// os.Exits with its result. Only TestExitError_ProcessExitCode ever sets
// this, by re-execing the test binary itself — never a normal `go test`
// invocation.
const afmExitTestChildEnv = "AFM_EXIT_TEST_CHILD"

// TestMain intercepts the child-mode re-exec (see afmExitTestChildEnv)
// before any *testing.T machinery runs, so the exit code below reflects a
// real OS-level process exit rather than a value handed back through Go
// function calls — exactly what TestExitError_ProcessExitCode needs to
// prove main() actually translates *ExitError into a process exit code, not
// just that some function returns the right int. The child-mode branch
// deliberately calls os.Exit itself instead of returning (which would let
// the normal `go test` runner exit with m.Run()'s result instead of the
// child's own exit code) — the explicit calls below are the whole point of
// this test, not the redundant `os.Exit(m.Run())` idiom revive's
// redundant-test-main-exit rule targets.
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

// TestExitError_ProcessExitCode proves the real OS process exit code is 3
// for a corrupt ledger, not merely that check's RunE *returns*
// ExitError{Code:3} — a single in-process cmd.Execute() call (as the other
// check_test.go/report_test.go matrix tests use) never exercises main()'s
// os.Exit translation at all. This re-execs the test binary itself (the
// standard os/exec "helper process" pattern): the child, driven by TestMain
// above, runs `afm check` for real against a fixture run directory whose
// usage.jsonl is a complete-but-unparseable line (accounting.ErrCorruptUsage),
// and the parent asserts the child process's actual exit code.
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

	// os.Args[0] is the path to this very test binary (compiled by `go
	// test`), not user/network input — the standard os/exec "helper
	// process" re-exec pattern.
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

// TestRunMain_ExitErrorSilentSuppressesPrinting is the fast, in-process
// companion to TestExitError_ProcessExitCode: it checks runMain's own
// translation logic (the code TestMain calls in the child) without paying
// for a subprocess, by feeding it a fake command that returns *ExitError
// directly.
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
