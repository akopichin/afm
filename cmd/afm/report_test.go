package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/runreport"
)

func TestReportRendersCostForRunWithUsage(t *testing.T) {
	chdirTemp(t)
	// Деньги показываются только при show_money (дефолт — скрыто); эта проверка
	// именно про денежный вывод, поэтому явно включаем флаг.
	t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "1")

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

func TestReport_AccountingDisabledPrintsTimeOnlyReportExit0(t *testing.T) {
	chdirTemp(t)
	t.Setenv("AFM_ACCOUNTING", "0")

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

	if runErr != nil {
		t.Fatalf("accounting disabled: expected nil error (exit 0), got %v", runErr)
	}
	if !strings.Contains(stdout, "## Stages") || strings.Contains(stdout, "Total Tokens") || strings.Contains(stdout, "$") {
		t.Errorf("accounting disabled: want the stage time report without tokens and cost, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "accounting display disabled") {
		t.Errorf("accounting disabled: expected a hint on stderr, got:\n%s", stderr)
	}
}

func TestReport_AccountingDisabledStillErrorsOnBadRun(t *testing.T) {
	chdirTemp(t)
	t.Setenv("AFM_ACCOUNTING", "0")

	// A runs dir exists but is empty, and we ask for a specific missing id.
	if err := os.MkdirAll(filepath.Join(".afm", "runs"), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := newReportCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"does-not-exist"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("accounting disabled must still error on a nonexistent run id, not succeed silently")
	}
}

// TestReportExplicitRunID verifies the positional [run] argument accepts a
// bare run id (a directory name under .afm/runs), not just "latest".
func TestReportExplicitRunID(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
	// A second, newer run must NOT be picked when an explicit id names the first one — otherwise "explicit
	// id" would be indistinguishable from "latest" in this test.
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

// A run without budgets adds no Budgets section, with accounting displayed or not.
func TestReport_NoBudgetsNoSection(t *testing.T) {
	chdirTemp(t)
	t.Setenv("AFM_ACCOUNTING", "0")
	makeRunState(t, "flow-20260101-120000", cmdInit, state.StatusDone)
	stdout, _ := captureOutput(t, func() {
		cmd := newReportCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatalf("report: %v", err)
		}
	})
	if strings.Contains(stdout, "Budgets") {
		t.Errorf("a run without budgets printed a Budgets section:\n%s", stdout)
	}
}

// --- fixture run with fixed times: CLI matrix, goldens and the read-only guarantee ---

var updateReportGolden = flag.Bool("update-report-golden", false, "rewrite cmd/afm/testdata/report/*.golden")

const reportFixtureRun = "flow-20261101-100000"

var reportT0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// reportFixture is an afm root with one finished run: two stages, a stage with two attempts (priced and
// unpriced), a reported/estimated mismatch, and an agent-jobs journal. The mode is set by the caller.
type reportFixture struct {
	root   string
	runDir string
}

func newReportFixture(t *testing.T) reportFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AFM_ACCOUNTING", "")
	t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "")
	t.Setenv("AFM_DIR", "")
	root := t.TempDir()
	runDir := filepath.Join(root, ".afm", "runs", reportFixtureRun)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}

	at := func(sec int) time.Time { return reportT0.Add(time.Duration(sec) * time.Second) }
	var events []state.Transition
	add := func(sec int, stage string, from, to state.StageStatus, event string) {
		events = append(events, state.Transition{Seq: uint64(len(events) + 1), Time: at(sec), StageID: stage, From: from, To: to, Event: event})
	}
	add(0, "", "", "", "run_started")
	add(1, "build", state.StatusPending, state.StatusRunning, "start")
	add(61, "build", state.StatusRunning, state.StatusDone, "done")
	add(61, "test", state.StatusPending, state.StatusRunning, "start")
	add(121, "test", state.StatusRunning, state.StatusDone, "done")
	add(121, "", "", "", "run_finished")
	writeEventsLog(t, runDir, events)

	reported := 0.9
	priced := func(stage, phase, job string, mut ...func(*accounting.UsageRecord)) accounting.UsageRecord {
		r := accounting.UsageRecord{
			RecordVersion: 1, StageID: stage, Phase: phase, JobID: job, Channel: "claude", Model: "claude-sonnet-4-5",
			Metered: true, Priced: true, Tokens: accounting.Tokens{UncachedInput: 1000, CacheRead: 500, Output: 200},
			EstimatedCostUSD: 0.25, Rate: &accounting.RateSnapshot{Key: "claude/claude-sonnet-4-5", Source: "builtin", AsOf: "2026-01-01"},
		}
		for _, m := range mut {
			m(&r)
		}
		return r
	}
	writeUsageLog(t, runDir, []accounting.UsageRecord{
		priced("build", "implementation", "j1"),
		priced("build", "review", "j2", func(r *accounting.UsageRecord) {
			r.Model, r.Priced, r.EstimatedCostUSD, r.Rate = "internal-model", false, 0, nil
		}),
		priced("test", "implementation", "j3", func(r *accounting.UsageRecord) {
			r.ReportedCostUSD, r.Warnings = &reported, []string{"reported_cost_differs_from_estimate"}
		}),
	})

	store, err := agentjobs.Open(runDir, agentjobs.Header{RecordingStartedAt: reportT0})
	if err != nil {
		t.Fatal(err)
	}
	for i, j := range []struct{ id, stage, phase string }{{"j1", "build", "implementation"}, {"j2", "build", "review"}, {"j3", "test", "implementation"}} {
		meta := agentjobs.JobMeta{JobID: j.id, StageID: j.stage, AgentID: "stage:" + j.stage, AgentKind: accounting.AgentKindStage, Phase: j.phase}
		start := at(10 + 40*i)
		for _, rec := range []agentjobs.Record{
			{Type: agentjobs.TypeStarted, JobMeta: meta, At: start},
			{Type: agentjobs.TypeFinished, JobMeta: agentjobs.JobMeta{JobID: j.id}, At: start.Add(30 * time.Second), Outcome: agentjobs.OutcomeOK, DurationMS: 30_000},
		} {
			if err := store.Append(rec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return reportFixture{root: root, runDir: runDir}
}

func (fx reportFixture) mode(t *testing.T, mode string) {
	t.Helper()
	switch mode {
	case "full":
		t.Setenv("AFM_ACCOUNTING_SHOW_MONEY", "1")
	case "no_money":
	case "time_only":
		t.Setenv("AFM_ACCOUNTING", "0")
	default:
		t.Fatalf("unknown mode %q", mode)
	}
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return -1
}

type idOnly struct{ ID string }

// stageReportDoc is the part of a stage report the flags test reads.
type stageReportDoc struct {
	SchemaVersion int `json:"schema_version"`
	Scope         scopeDoc
	Stages        []idOnly
	Analytics     jobsDoc
}

type scopeDoc struct {
	StageID string `json:"stage_id"`
}

type jobsDoc struct{ Jobs []idOnly }

type runIDDoc struct{ Run idOnly }

type codeOnly struct{ Code string }

type diagnosticsDoc struct {
	Diagnostics []codeOnly `json:"diagnostics"`
}

func TestReportFormatAndStageFlags(t *testing.T) {
	fx := newReportFixture(t)
	fx.mode(t, "full")

	md, _, err := runReportCmd(t, fx.root)
	if err != nil || !strings.HasPrefix(md, "# Cost report: "+reportFixtureRun+"\n") {
		t.Fatalf("default format must be markdown, err %v:\n%s", err, md)
	}
	js, _, err := runReportCmd(t, fx.root, "--format", "json", "--stage", "build")
	if err != nil {
		t.Fatal(err)
	}
	var doc stageReportDoc
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, js)
	}
	if doc.SchemaVersion != 1 || doc.Scope.StageID != "build" || len(doc.Stages) != 1 || len(doc.Analytics.Jobs) != 2 {
		t.Errorf("stage report = %+v, want schema 1, the build stage and its two attempts", doc)
	}
	if !strings.HasSuffix(js, "}\n") {
		t.Errorf("JSON must end with a newline: %q", js[len(js)-5:])
	}

	for name, args := range map[string][]string{
		"unknown format": {"--format", "yaml"},
		"unknown stage":  {"--stage", "nope"},
		"unknown run":    {"no-such-run"},
	} {
		stdout, _, err := runReportCmd(t, fx.root, args...)
		if err == nil || stdout != "" {
			t.Errorf("%s: err %v stdout %q, want an error and nothing on stdout", name, err, stdout)
		}
	}
}

func TestReportSelectsRunByPathIDLatestDirAndEnv(t *testing.T) {
	fx := newReportFixture(t)
	older := filepath.Join(fx.root, ".afm", "runs", "flow-20260101-000000")
	if err := os.Rename(fx.runDir, older); err != nil {
		t.Fatal(err)
	}
	// The newest run (lexicographically last) is the empty fixture copy.
	newest := filepath.Join(fx.root, ".afm", "runs", "flow-20270101-000000")
	writeEventsLog(t, newest, []state.Transition{{Seq: 1, Time: reportT0, StageID: "only", From: state.StatusPending, To: state.StatusDone, Event: "x"}})

	runID := func(args ...string) string {
		t.Helper()
		other := chdirTemp(t) // the afm root comes from --dir or AFM_DIR, not from the cwd
		_ = other
		cmd := newRootCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(append([]string{"report", "--format", "json"}, args...))
		prev := rootDir
		t.Cleanup(func() { rootDir = prev })
		var runErr error
		stdout, _ := captureOutput(t, func() { runErr = cmd.Execute() })
		if runErr != nil {
			t.Fatalf("report %v: %v", args, runErr)
		}
		var doc runIDDoc
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Run.ID
	}

	if got := runID("--dir", fx.root); got != "flow-20270101-000000" {
		t.Errorf("latest run = %q", got)
	}
	if got := runID("--dir", fx.root, "flow-20260101-000000"); got != "flow-20260101-000000" {
		t.Errorf("run by id = %q", got)
	}
	if got := runID("--dir", fx.root, older); got != "flow-20260101-000000" {
		t.Errorf("run by path = %q", got)
	}
	t.Setenv("AFM_DIR", fx.root)
	if got := runID("flow-20260101-000000"); got != "flow-20260101-000000" {
		t.Errorf("run through AFM_DIR = %q", got)
	}
	// --dir wins over AFM_DIR: an empty root has no run to report.
	cmd := newRootCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--dir", t.TempDir(), "report"})
	if err := cmd.Execute(); err == nil {
		t.Error("--dir must win over AFM_DIR")
	}
}

func TestReportExitCodeMatrix(t *testing.T) {
	appendTo := func(path, text string) func(*testing.T, reportFixture) {
		return func(t *testing.T, fx reportFixture) {
			t.Helper()
			f, err := os.OpenFile(filepath.Join(fx.runDir, path), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(text); err != nil {
				t.Fatal(err)
			}
		}
	}
	replace := func(path, text string) func(*testing.T, reportFixture) {
		return func(t *testing.T, fx reportFixture) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(fx.runDir, path), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	sideJournal := func(t *testing.T, fx reportFixture) {
		t.Helper()
		writeFile(t, filepath.Join(fx.root, ".afm", "side-agent", reportFixtureRun, "agent-jobs.jsonl"), "garbage\n")
	}
	tests := []struct {
		name     string
		mode     string
		damage   func(*testing.T, reportFixture)
		wantCode int
		stderr   string // part of the reason on stderr when the code is not 0
		code     string // diagnostic code that must appear
	}{
		{"healthy", "full", func(*testing.T, reportFixture) {}, 0, "", ""},
		{"corrupt ledger", "full", appendTo("usage.jsonl", "{broken}\n"), 3, "corrupt", "usage_corrupt"},
		{"corrupt ledger without money", "no_money", appendTo("usage.jsonl", "{broken}\n"), 3, "corrupt", "usage_corrupt"},
		{"corrupt ledger, time only", "time_only", appendTo("usage.jsonl", "{broken}\n"), 0, "", ""},
		{"unreadable ledger", "full", func(t *testing.T, fx reportFixture) {
			t.Helper()
			if err := os.Remove(filepath.Join(fx.runDir, "usage.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(fx.runDir, "usage.jsonl"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, 3, "cannot read", "usage_unavailable"},
		{"jobs journal without a header", "full", replace("agent-jobs.jsonl", "garbage\n"), 3, "agent jobs unavailable", "jobs_unavailable"},
		{"jobs journal with a broken line", "full", appendTo("agent-jobs.jsonl", "{broken}\n"), 0, "", "jobs_degraded"},
		{"no jobs journal", "full", func(t *testing.T, fx reportFixture) {
			t.Helper()
			if err := os.Remove(filepath.Join(fx.runDir, "agent-jobs.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, 0, "", "jobs_absent"},
		{"broken Assistant journal", "full", sideJournal, 0, "", "assistant_jobs_unavailable"},
	}
	for _, tc := range tests {
		for _, format := range []string{"json", "markdown"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				fx := newReportFixture(t)
				fx.mode(t, tc.mode)
				tc.damage(t, fx)

				stdout, stderr, err := runReportCmd(t, fx.root, "--format", format)

				if got := exitCodeOf(t, err); got != tc.wantCode {
					t.Fatalf("exit code = %d (%v), want %d", got, err, tc.wantCode)
				}
				if format == "json" {
					var doc diagnosticsDoc
					if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
						t.Fatalf("stdout must be one JSON document even on a damaged input: %v\n%s", err, stdout)
					}
					if tc.code != "" && !slices.ContainsFunc(doc.Diagnostics, func(d codeOnly) bool { return d.Code == tc.code }) {
						t.Errorf("diagnostics = %+v, want %q", doc.Diagnostics, tc.code)
					}
				} else if !strings.HasPrefix(stdout, "# Cost report:") {
					t.Errorf("markdown stdout = %q", stdout)
				}
				if tc.wantCode != 0 && !strings.Contains(stderr, tc.stderr) {
					t.Errorf("stderr = %q, want the reason %q", stderr, tc.stderr)
				}
				if strings.Contains(stdout, "accounting display disabled") {
					t.Error("diagnostics belong on stderr, not in the document")
				}
			})
		}
	}
}

// The report only reads: every file of the run and of the Assistant keeps its name, size, mtime and content,
// also when the logs end in a half-written line.
func TestReportDoesNotWriteAnything(t *testing.T) {
	fx := newReportFixture(t)
	fx.mode(t, "full")
	writeFile(t, filepath.Join(fx.root, ".afm", "side-agent", reportFixtureRun, "usage.jsonl"), "")
	for _, name := range []string{"usage.jsonl", "agent-jobs.jsonl", "events.jsonl"} {
		f, err := os.OpenFile(filepath.Join(fx.runDir, name), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"torn":`); err != nil { // no trailing newline: an in-flight write
			t.Fatal(err)
		}
		_ = f.Close()
	}
	before := snapshotTree(t, filepath.Join(fx.root, ".afm"))

	for _, format := range []string{"markdown", "json"} {
		if _, stderr, err := runReportCmd(t, fx.root, "--format", format); err != nil {
			t.Fatalf("%s: %v\n%s", format, err, stderr)
		}
	}

	if after := snapshotTree(t, filepath.Join(fx.root, ".afm")); !reflect.DeepEqual(before, after) {
		t.Errorf("the report changed the run directory\nbefore: %v\nafter:  %v", before, after)
	}
}

type fileStamp struct {
	Size  int64
	MTime time.Time
	SHA   [sha256.Size]byte
}

func snapshotTree(t *testing.T, root string) map[string]fileStamp {
	t.Helper()
	out := map[string]fileStamp{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		stamp := fileStamp{Size: info.Size(), MTime: info.ModTime()}
		if !d.IsDir() {
			data, err := os.ReadFile(path) //nolint:gosec // read-only walk of a test temp dir
			if err != nil {
				return err
			}
			stamp.SHA = sha256.Sum256(data)
		}
		out[rel] = stamp
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReportModesHideTheirData(t *testing.T) {
	fx := newReportFixture(t)

	t.Run("no_money has no dollar sign outside budgets", func(t *testing.T) {
		fx.mode(t, "no_money")
		md, _, err := runReportCmd(t, fx.root)
		if err != nil {
			t.Fatal(err)
		}
		if body, _, _ := strings.Cut(md, "## Budgets"); strings.Contains(body, "$") || strings.Contains(body, "Est. cost") || strings.Contains(body, "Est. Cost") {
			t.Errorf("no_money leaks money:\n%s", md)
		}
		if !strings.Contains(md, "Total Tokens") {
			t.Errorf("no_money must keep the tokens:\n%s", md)
		}
	})
	t.Run("time_only has no tokens outside budgets", func(t *testing.T) {
		fx.mode(t, "time_only")
		md, _, err := runReportCmd(t, fx.root)
		if err != nil {
			t.Fatal(err)
		}
		body, _, _ := strings.Cut(md, "## Budgets")
		for _, banned := range []string{"Tokens", "tokens", "$", "Coverage and pricing", "## Total"} {
			if strings.Contains(body, banned) {
				t.Errorf("time_only shows %q:\n%s", banned, md)
			}
		}
		if !strings.Contains(md, "Agent time") || !strings.Contains(md, "## Agent jobs") {
			t.Errorf("time_only must keep the time figures:\n%s", md)
		}
	})
}

func goldenCompare(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "report", name)
	if *updateReportGolden {
		writeFile(t, path, got)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-report-golden to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file (run with -update-report-golden after checking the change)\n--- got ---\n%s", name, got)
	}
}

func TestReportGoldenDocuments(t *testing.T) {
	for _, tc := range []struct{ mode, format, file string }{
		{"full", "json", "full.json.golden"},
		{"full", "markdown", "full.md.golden"},
		{"no_money", "markdown", "no_money.md.golden"},
		{"time_only", "markdown", "time_only.md.golden"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			fx := newReportFixture(t)
			fx.mode(t, tc.mode)
			stdout, _, err := runReportCmd(t, fx.root, "--format", tc.format)
			if err != nil {
				t.Fatal(err)
			}
			goldenCompare(t, tc.file, stdout)
		})
	}
}

func TestReportMarkdownDiagnosticsSection(t *testing.T) {
	var clean strings.Builder
	reportMarkdown{w: &clean, r: &runreport.Report{}}.diagnostics()
	if want := "## Diagnostics\n\nNone.\n\n"; clean.String() != want {
		t.Errorf("clean run = %q, want %q", clean.String(), want)
	}

	var damaged strings.Builder
	r := &runreport.Report{Diagnostics: []runreport.Diagnostic{{Code: "x_code", Message: "boom"}}}
	reportMarkdown{w: &damaged, r: r}.diagnostics()
	if want := "## Diagnostics\n\n- `x_code`: boom\n\n"; damaged.String() != want {
		t.Errorf("with diagnostics = %q, want %q", damaged.String(), want)
	}
}
