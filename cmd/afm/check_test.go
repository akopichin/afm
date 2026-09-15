package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/state"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. check.go/list.go print via fmt.Printf directly
// (not cmd.OutOrStdout()), so capturing the real os.Stdout is required.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// writeEventsLog writes a sequence of transitions to <runDir>/events.jsonl,
// the authoritative source state.LoadRunState (and thus `afm check`) reads
// from — mirroring makeRunState but allowing several transitions per stage.
func writeEventsLog(t *testing.T, runDir string, transitions []state.Transition) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, tr := range transitions {
		data, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckReadsStatusFromLogNotSnapshot verifies that `afm check` derives
// stage status from events.jsonl (via state.LoadRunState), not from the
// cached state.json snapshot. A couple of transitions are seeded in the log
// (pending -> running -> done), while a deliberately stale state.json claims
// the stage is "failed" — check must report the log's truth ("done"), never
// the stale snapshot's "failed".
func TestCheckReadsStatusFromLogNotSnapshot(t *testing.T) {
	chdirTemp(t)

	runDir := filepath.Join(".afm", "runs", "flow-20260101-120000")
	writeEventsLog(t, runDir, []state.Transition{
		{Seq: 1, Time: time.Now(), StageID: cmdInit, From: state.StatusPending, To: state.StatusRunning, Event: "start"},
		{Seq: 2, Time: time.Now(), StageID: cmdInit, From: state.StatusRunning, To: state.StatusDone, Event: "finish"},
	})

	staleSnapshot := state.RunState{Stages: map[string]state.StageState{
		cmdInit: {Status: state.StatusFailed, UpdatedAt: time.Now()},
	}}
	data, err := json.Marshal(staleSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if !strings.Contains(out, cmdInit) {
		t.Errorf("output should mention stage %q, got:\n%s", cmdInit, out)
	}
	if !strings.Contains(out, string(state.StatusDone)) {
		t.Errorf("output should show status %q from the log, got:\n%s", state.StatusDone, out)
	}
	if strings.Contains(out, string(state.StatusFailed)) {
		t.Errorf("output should NOT reflect the stale state.json snapshot (%q), got:\n%s", state.StatusFailed, out)
	}
}

// TestCheckDoesNotBlockOnActiveRunLock is the load-bearing guarantee from
// CLAUDE.md: `afm check` is read-only and must NOT take the run's flock, so
// it keeps working while a live `afm run` (or any process holding state.Open)
// has the lock. We hold the lock ourselves via state.Open for the whole test
// and assert check still succeeds and reports the correct status.
func TestCheckDoesNotBlockOnActiveRunLock(t *testing.T) {
	chdirTemp(t)

	runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusRunning)

	store, err := state.Open(runDir, []string{cmdInit})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	out := captureStdout(t, func() {
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check must not block or fail while the run is locked: %v", err)
		}
	})

	if !strings.Contains(out, string(state.StatusRunning)) {
		t.Errorf("expected status %q in output, got:\n%s", state.StatusRunning, out)
	}
}

// TestCheckNoRuns verifies the error path when no run directories exist.
func TestCheckNoRuns(t *testing.T) {
	chdirTemp(t)

	if err := os.MkdirAll(filepath.Join(".afm", "runs"), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := newCheckCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no runs exist")
	}
}

func TestLastLogAction_OnlyPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("line one\nplanning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "planning last line" {
		t.Errorf("got %q, want %q", got, "planning last line")
	}
}

func TestLastLogAction_ImplementationBeatsPlanning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planning.log"), []byte("planning last line"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "implementation.log"), []byte("implementation last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "implementation last line" {
		t.Errorf("got %q, want %q — later phase must win", got, "implementation last line")
	}
}

func TestLastLogAction_ReviewOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.log"), []byte("review last line"), 0644); err != nil {
		t.Fatal(err)
	}
	got := lastLogAction(dir)
	if got != "review last line" {
		t.Errorf("got %q, want %q — review.log was not covered before this fix", got, "review last line")
	}
}

// writeUsageLog writes the given accounting.UsageRecords as usage.jsonl in
// runDir, mirroring what accounting.Store.Append persists during a real run
// — `afm check` reads this file read-only via accounting.Load.
func writeUsageLog(t *testing.T, runDir string, recs []accounting.UsageRecord) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, rec := range recs {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckShowsCostColumnsAndTotal verifies that when a run has recorded
// usage (usage.jsonl present with priced records), `afm check` renders the
// TOKENS/CACHE/EST. COST columns for the stage and a TOTAL summary line
// derived from accounting.Ledger.RunSummary — never computed by hand in cmd.
func TestCheckShowsCostColumnsAndTotal(t *testing.T) {
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
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if !strings.Contains(out, "EST. COST") {
		t.Errorf("expected an EST. COST column header, got:\n%s", out)
	}
	if !strings.Contains(out, "$1.2345") {
		t.Errorf("expected the stage row to show the priced cost $1.2345, got:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("expected a TOTAL summary line, got:\n%s", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Errorf("must never render a bogus $0.00, got:\n%s", out)
	}
}

// TestCheckWithoutUsageDataRendersCleanly verifies the backward-compat path:
// a run with no usage.jsonl at all (accounting.Load returns an empty ledger,
// nil error) must render without the cost columns and without a misleading
// $0.00 anywhere — just a plain "No usage data" note.
func TestCheckWithoutUsageDataRendersCleanly(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)

	out := captureStdout(t, func() {
		cmd := newCheckCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if strings.Contains(out, "EST. COST") {
		t.Errorf("no usage.jsonl: cost columns should be omitted entirely, got:\n%s", out)
	}
	if strings.Contains(out, "$0.00") {
		t.Errorf("no usage.jsonl: must never render a bogus $0.00, got:\n%s", out)
	}
	if !strings.Contains(out, "No usage data") {
		t.Errorf("no usage.jsonl: expected a clean \"No usage data\" note, got:\n%s", out)
	}
	if !strings.Contains(out, cmdInit) {
		t.Errorf("the stage table itself must still render, got:\n%s", out)
	}
}

// captureOutput redirects both os.Stdout and os.Stderr for the duration of
// fn and returns everything written to each SEPARATELY — the load-error
// matrix below must assert stdout and stderr independently (check.go prints
// via fmt.Printf/os.Stderr directly, not cmd.OutOrStdout()/ErrOrStderr()).
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	fn()

	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errW.Close(); err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	if _, err := io.Copy(&outBuf, outR); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&errBuf, errR); err != nil {
		t.Fatal(err)
	}
	return outBuf.String(), errBuf.String()
}

// TestCheck_LoadErrorMatrix exercises accounting.Load's four outcomes for
// `afm check`, asserting stdout, stderr and the exit code (via the RunE's
// returned *ExitError, translated to a real process exit by cmd/afm/exit.go
// — see TestExitError_ProcessExitCode for the process-level proof) totally
// independently, per the CLI-consistency contract in the design spec.
func TestCheck_LoadErrorMatrix(t *testing.T) {
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
			cmd := newCheckCmd()
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
			cmd := newCheckCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 3 {
			t.Errorf("corrupt usage.jsonl: exit code = %d, want 3", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, cmdInit) {
			t.Errorf("corrupt usage.jsonl: expected the stage table to still render on stdout, got:\n%s", stdout)
		}
		if strings.Contains(stdout, "EST. COST") {
			t.Errorf("corrupt usage.jsonl: no cost columns should render, got:\n%s", stdout)
		}
		if !strings.Contains(stderr, "corrupt") {
			t.Errorf("corrupt usage.jsonl: expected stderr to mention \"corrupt\", got:\n%s", stderr)
		}
	})

	t.Run("other read error", func(t *testing.T) {
		chdirTemp(t)
		runDir := makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
		// usage.jsonl as a DIRECTORY is a portable way to force a read
		// error (EISDIR) without chmod, which is unstable across users/CI
		// (e.g. root always bypasses permission bits).
		if err := os.MkdirAll(filepath.Join(runDir, "usage.jsonl"), 0755); err != nil {
			t.Fatal(err)
		}

		var runErr error
		stdout, stderr := captureOutput(t, func() {
			cmd := newCheckCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 3 {
			t.Errorf("other read error: exit code = %d, want 3", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, cmdInit) {
			t.Errorf("other read error: expected the stage table to still render on stdout, got:\n%s", stdout)
		}
		if strings.Contains(stderr, "corrupt") {
			t.Errorf("other read error: stderr must never say \"corrupt\" for a non-parse failure, got:\n%s", stderr)
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
			cmd := newCheckCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			runErr = cmd.Execute()
		})

		if exitCodeOf(t, runErr) != 0 {
			t.Errorf("normal: exit code = %d, want 0", exitCodeOf(t, runErr))
		}
		if !strings.Contains(stdout, "TOTAL") {
			t.Errorf("normal: expected a TOTAL line on stdout, got:\n%s", stdout)
		}
		if stderr != "" {
			t.Errorf("normal: expected empty stderr, got:\n%s", stderr)
		}
	})
}

// TestCoverageNote_SumsCountNotGroups is the CLI-level companion to
// pkg/accounting's TestLedger_CoverageIssues_SumsCountAcrossIdenticalRecords:
// two identical unpriced records must render as "2 unpriced" in `afm
// check`'s coverage note, not "1" (which a naive len(issues) would produce,
// since aggregateIssues groups identical gaps into one issue with Count==2).
func TestCoverageNote_SumsCountNotGroups(t *testing.T) {
	led := &accounting.Ledger{}
	rec := accounting.UsageRecord{
		StageID: "docs", Phase: "implementation",
		Metered: true, Priced: false, Channel: "claude-cli", Model: "some-unknown-model",
	}
	led.Add(rec)
	led.Add(rec)

	got := coverageNote(led)
	if got != "2 unpriced" {
		t.Errorf("coverageNote = %q, want %q", got, "2 unpriced")
	}
}

func TestStatusColor_PausedGetsItsOwnColor(t *testing.T) {
	// paused needs a human decision (Continue), same class as
	// awaiting_approval — it must not fall into the same colorGray bucket as
	// genuinely inert statuses like pending/ready.
	if got := statusColor(state.StatusPaused); got == colorGray {
		t.Error("statusColor(paused) = colorGray, want a distinct attention color")
	}
}
