package main

import (
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
