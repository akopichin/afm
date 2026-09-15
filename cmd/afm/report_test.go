package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/state"
)

// TestReportRendersCostForRunWithUsage verifies `afm report` (no args, so it
// picks the latest run — same rule `afm check` uses) resolves the run
// directory, loads status via state.LoadRunState and usage via
// accounting.Load, and prints a markdown report to stdout carrying the
// stage, its cost, and the run total.
func TestReportRendersCostForRunWithUsage(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
	writeUsageLog(t, runDir, []accounting.UsageRecord{
		{
			RecordVersion: 1,
			StageID:       cmdInit,
			Phase:         "implementation",
			Model:         "claude-sonnet-4-5",
			Metered:       true,
			Priced:        true,
			Tokens: accounting.Tokens{
				UncachedInput: 10_000,
				CacheRead:     47_600,
				CacheWrite5m:  18_600,
				Output:        9_300,
			},
			EstimatedCostUSD: 1.2345,
		},
	})

	out := captureStdout(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("report: %v", err)
		}
	})

	if !strings.Contains(out, "# Cost report:") {
		t.Errorf("expected a report title, got:\n%s", out)
	}
	if !strings.Contains(out, cmdInit) {
		t.Errorf("expected the stage %q to appear, got:\n%s", cmdInit, out)
	}
	if !strings.Contains(out, "$1.2345") {
		t.Errorf("expected the priced cost $1.2345 to appear, got:\n%s", out)
	}
	if !strings.Contains(out, "## Total") {
		t.Errorf("expected a Total section, got:\n%s", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Errorf("must never render a bogus $0.00, got:\n%s", out)
	}
}

// TestReportNoUsageData covers a run with no usage.jsonl at all (accounting
// disabled, or a run predating this feature): `afm report` must still
// produce a valid report that plainly says so, not crash and not show a
// bogus $0.00.
func TestReportNoUsageData(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

	out := captureStdout(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("report: %v", err)
		}
	})

	if !strings.Contains(out, "No usage data") {
		t.Errorf("expected a clean \"No usage data\" report, got:\n%s", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Errorf("must never render a bogus $0.00, got:\n%s", out)
	}
}

// TestReportExplicitRunID verifies the positional [run] argument accepts a
// bare run id (a directory name under .afm/runs), not just "latest".
func TestReportExplicitRunID(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
	// A second, newer run must NOT be picked when an explicit id names the
	// first one — otherwise "explicit id" would be indistinguishable from
	// "latest" in this test.
	makeRunState(t, "flow-20260202-120000", cmdInit, state.StatusRunning)

	out := captureStdout(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"flow-20260101-120000"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("report: %v", err)
		}
	})

	if !strings.Contains(out, "flow-20260101-120000") {
		t.Errorf("expected the report title to name the explicitly requested run, got:\n%s", out)
	}
}

// TestReportExplicitPath verifies the positional [run] argument also accepts
// a filesystem path to the run directory, not only a bare id.
func TestReportExplicitPath(t *testing.T) {
	dir := chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

	out := captureStdout(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{filepath.Join(dir, runDir)})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("report: %v", err)
		}
	})

	if !strings.Contains(out, "flow-20260101-120000") {
		t.Errorf("expected the report title to name the run at the given path, got:\n%s", out)
	}
}

// TestReportNoRuns verifies the error path when no run directories exist —
// mirroring TestCheckNoRuns.
func TestReportNoRuns(t *testing.T) {
	dir := chdirTemp(t)
	_ = dir

	cmd := newReportCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no runs exist")
	}
}

// TestReport_LoadErrorMatrix mirrors TestCheck_LoadErrorMatrix for `afm
// report`: missing/corrupt/other/normal, asserting stdout, stderr and exit
// code separately. The corrupt/other-error stdout must be self-contained —
// distinguishing the two causes entirely within the markdown, since a
// caller doing `afm report run > out.md` never sees stderr.
func TestReport_LoadErrorMatrix(t *testing.T) {
	exitCodeOf := func(t *testing.T, err error) int {
		t.Helper()
		if err == nil {
			return 0
		}
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			return exitErr.Code
		}
		t.Fatalf("expected either nil or *ExitError, got %v (%T)", err, err)
		return -1
	}

	t.Run("missing usage.jsonl", func(t *testing.T) {
		chdirTemp(t)
		makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

		var runErr error
		stdout, stderr := captureOutput(t, func() {
			cmd := newReportCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 0 {
			t.Errorf("missing usage.jsonl: exit code = %d, want 0", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, "No usage data") {
			t.Errorf("missing usage.jsonl: expected stdout \"No usage data\", got:\n%s", stdout)
		}
		if !strings.Contains(stdout, cmdInit) {
			t.Errorf("missing usage.jsonl: expected the FSM stage rows on stdout, got:\n%s", stdout)
		}
		if stderr != "" {
			t.Errorf("missing usage.jsonl: expected empty stderr, got:\n%s", stderr)
		}
	})

	t.Run("corrupt usage.jsonl", func(t *testing.T) {
		chdirTemp(t)
		runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
		if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not valid json\n"), 0644); err != nil {
			t.Fatal(err)
		}

		var runErr error
		stdout, stderr := captureOutput(t, func() {
			cmd := newReportCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 3 {
			t.Errorf("corrupt usage.jsonl: exit code = %d, want 3", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, cmdInit) {
			t.Errorf("corrupt usage.jsonl: expected the FSM stage rows on stdout, got:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Cost unavailable") || !strings.Contains(stdout, "corrupt") {
			t.Errorf("corrupt usage.jsonl: expected a self-contained \"Cost unavailable ... corrupt\" note on stdout, got:\n%s", stdout)
		}
		if strings.Contains(stdout, "No usage data") {
			t.Errorf("corrupt usage.jsonl: stdout must not read as plain \"No usage data\", got:\n%s", stdout)
		}
		if strings.Contains(stdout, "## Total") {
			t.Errorf("corrupt usage.jsonl: must not invent any totals, got:\n%s", stdout)
		}
		if !strings.Contains(stderr, "corrupt") {
			t.Errorf("corrupt usage.jsonl: expected stderr to mention \"corrupt\", got:\n%s", stderr)
		}
	})

	t.Run("other read error", func(t *testing.T) {
		chdirTemp(t)
		runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
		if err := os.MkdirAll(filepath.Join(runDir, "usage.jsonl"), 0755); err != nil {
			t.Fatal(err)
		}

		var runErr error
		stdout, stderr := captureOutput(t, func() {
			cmd := newReportCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 3 {
			t.Errorf("other read error: exit code = %d, want 3", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, cmdInit) {
			t.Errorf("other read error: expected the FSM stage rows on stdout, got:\n%s", stdout)
		}
		if !strings.Contains(stdout, "Cost unavailable") || !strings.Contains(stdout, "cannot read") {
			t.Errorf("other read error: expected a self-contained \"Cost unavailable ... cannot read\" note on stdout, got:\n%s", stdout)
		}
		if strings.Contains(stdout, "corrupt") {
			t.Errorf("other read error: stdout must never say \"corrupt\" for a non-parse failure, got:\n%s", stdout)
		}
		if strings.Contains(stdout, "## Total") {
			t.Errorf("other read error: must not invent any totals, got:\n%s", stdout)
		}
		if strings.Contains(stderr, "corrupt") {
			t.Errorf("other read error: stderr must never say \"corrupt\", got:\n%s", stderr)
		}
		if !strings.Contains(stderr, "cannot read") {
			t.Errorf("other read error: expected stderr to mention \"cannot read\", got:\n%s", stderr)
		}
	})

	t.Run("normal", func(t *testing.T) {
		chdirTemp(t)
		runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
		writeUsageLog(t, runDir, []accounting.UsageRecord{
			{
				RecordVersion: 1, StageID: cmdInit, Phase: "implementation", Model: "claude-sonnet-4-5",
				Metered: true, Priced: true,
				Tokens:           accounting.Tokens{UncachedInput: 1000, Output: 200},
				EstimatedCostUSD: 0.05,
			},
		})

		var runErr error
		stdout, stderr := captureOutput(t, func() {
			cmd := newReportCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 0 {
			t.Errorf("normal: exit code = %d, want 0", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, "## Total") {
			t.Errorf("normal: expected a Total section on stdout, got:\n%s", stdout)
		}
		if stderr != "" {
			t.Errorf("normal: expected empty stderr, got:\n%s", stderr)
		}
	})
}

// TestReportCmd_Registered verifies `afm report` is wired into the root
// command (main.go's root.AddCommand block).
func TestReportCmd_Registered(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "report" {
			return
		}
	}
	t.Fatal("expected \"report\" to be registered as a root subcommand")
}
