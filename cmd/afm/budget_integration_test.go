package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/contracts/budget"
)

// The AFM composition with the real engine: budgets decided by the engine, served by the HTTP API, held without
// ending the process and kept across a restart. The agent is a script with scripted token usage, no LLM.

const (
	// 85 tokens warn, 30 more pass the limit of 100, then a small turn completes the stage.
	budgetScript       = "85 30 20"
	budgetStage        = "stage:build"
	budgetStillLimited = "budget_still_limited"
)

// budgetFixture is a core-mode fixture whose stage build declares a hard limit of 100 tokens.
type budgetFixture struct {
	*modeFixture
}

func newBudgetFixture(t *testing.T, flowFile, tokens string, doneAt int) *budgetFixture {
	t.Helper()
	fx := newModeFixture(t, modeCore, flowFile, "")
	fx.env = append(fx.env, "AFM_IT_BUDGET_TOKENS="+tokens, "AFM_IT_BUDGET_DONE_AT="+strconv.Itoa(doneAt))
	return &budgetFixture{fx}
}

// startWithoutAPI runs afm like `afm run --port 0`: no dashboard, nobody to raise a limit.
func (fx *budgetFixture) startWithoutAPI() *afmChild {
	fx.t.Helper()
	child := startAfmChild(fx.t, fx.root, fx.env, "run", fx.flowPath, "--dir", fx.root, "--port", "0")
	interruptOnCleanup(fx.t, child)
	return child
}

type budgetYAMLBody struct {
	Available bool         `json:"available"`
	File      string       `json:"file"`
	Declared  *budget.Spec `json:"declared"`
}

type budgetViewBody struct {
	View budget.View     `json:"view"`
	YAML *budgetYAMLBody `json:"yaml"`
}

func (fx *budgetFixture) view(scope string) budgetViewBody {
	fx.t.Helper()
	code, body, _ := fx.call(http.MethodGet, "/api/budgets/view?scope="+scope, "", nil)
	if code != http.StatusOK {
		fx.t.Fatalf("GET /api/budgets/view?scope=%s = %d: %s", scope, code, body)
	}
	var out budgetViewBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		fx.t.Fatalf("decode view: %v\n%s", err, body)
	}
	return out
}

type budgetResumeErrorBody struct {
	Code string `json:"code"`
}

type budgetUpdateBody struct {
	View        budget.View            `json:"view"`
	Export      *budget.ExportResult   `json:"export"`
	ResumeError *budgetResumeErrorBody `json:"resume_error"`
}

// noticeRow is a journal row of notices.jsonl.
type noticeRow struct {
	Type string     `json:"type"`
	Data noticeData `json:"data"`
}

type noticeData struct {
	Kind string `json:"kind"`
}

func (fx *budgetFixture) update(body string) budgetUpdateBody {
	fx.t.Helper()
	var out budgetUpdateBody
	if err := json.Unmarshal([]byte(fx.mustPost("/api/budgets/update", body)), &out); err != nil {
		fx.t.Fatalf("decode update: %v", err)
	}
	return out
}

func limitUpdate(scope string, revision int64, tokens int, flags string) string {
	return `{"scope":"` + scope + `","expected_revision":` + strconv.FormatInt(revision, 10) +
		`,"override":{"kind":"limit","spec":{"tokens":` + strconv.Itoa(tokens) + `,"stop_policy":"hard"}}` + flags + `}`
}

func (fx *budgetFixture) waitHeld(scope string) budget.View {
	fx.t.Helper()
	var view budget.View
	waitFor(fx.t, scope+" to be held by its budget", func() bool {
		view = fx.view(scope).View
		return view.Hold != nil
	})
	return view
}

// notices returns the "<type>/<data.kind>" of every budget notice of the run, in order.
func noticeKinds(t *testing.T, runDir string) []string {
	t.Helper()
	file, err := os.Open(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row noticeRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil || !strings.HasPrefix(row.Type, "budget_") {
			continue
		}
		out = append(out, row.Type+"/"+row.Data.Kind)
	}
	return out
}

// A held run keeps the API up, an update with resume continues the same run, and the four notice types are
// what the dashboard keys on.
func TestBudgetIntegration_HoldKeepsAPIUpAndUpdateResumesTheSameRun(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	child := fx.start()
	runID := fx.waitReady(child)

	held := fx.waitHeld(budgetStage)
	if held.Hold.Reason != budget.HoldLimitReached || held.Spent.Tokens != 115 || held.Revision != 0 {
		t.Fatalf("held view = %+v", held)
	}
	select {
	case <-child.done:
		t.Fatalf("a held run ended the process:\n%s", child.out.String())
	default:
	}
	// The budget view gets its hold before the stage FSM reaches paused, so wait for the status.
	fx.waitStage("build", "paused")
	if !strings.Contains(string(fx.status().Capabilities), `"budgets":true`) {
		t.Errorf("capabilities = %s, want budgets on", fx.status().Capabilities)
	}

	// Raising the limit below the spend saves it but cannot continue: the partial result is a 200.
	partial := fx.update(limitUpdate(budgetStage, 0, 50, `,"resume":true`))
	if partial.View.Revision != 1 || partial.View.Override.Spec.Tokens != 50 || partial.ResumeError == nil || partial.ResumeError.Code != budgetStillLimited {
		t.Fatalf("partial update = %+v, resume error %+v", partial, partial.ResumeError)
	}
	if code, _, _ := fx.call(http.MethodPost, "/api/budgets/update", limitUpdate(budgetStage, 0, 900, ""), nil); code != http.StatusConflict {
		t.Errorf("update on a stale revision = %d, want 409", code)
	}

	done := fx.update(limitUpdate(budgetStage, 1, 1000, `,"resume":true`))
	if done.View.Hold != nil || done.ResumeError != nil {
		t.Fatalf("update with resume = %+v", done)
	}
	fx.waitRunStatus("finished")
	if runs := fx.agentRuns(); len(runs) != 3 {
		t.Errorf("agent invocations = %v, want 3 in the one run", runs)
	}
	if got := fx.status().RunID; got != runID {
		t.Errorf("run id changed from %s to %s", runID, got)
	}

	// The lowered limit of 50 is reached at once, which is a second limit_reached before the final update.
	want := []string{
		"budget_warning/warning", "budget_limit_reached/limit_reached", "budget_updated/updated",
		"budget_limit_reached/limit_reached", "budget_updated/updated", "budget_resumed/resumed",
	}
	if got := noticeKinds(t, fx.runDir(runID)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("notices = %v, want %v", got, want)
	}

	child.waitOutput(t, holdingFinalState)
	if code := child.interrupt(t); code != 0 {
		t.Errorf("exit code after the final hold = %d, want 0\n%s", code, child.out.String())
	}
}

// Ctrl+C while held drains cleanly; a restart continues the same run with the spend and the hold it had.
func TestBudgetIntegration_InterruptWhileHeldThenReopenKeepsSpendAndLimit(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	child := fx.start()
	runID := fx.waitReady(child)
	fx.waitHeld(budgetStage)

	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}
	if out := child.out.String(); strings.Contains(out, "write events.jsonl") || strings.Contains(out, "invalid argument") {
		t.Errorf("shutdown wrote to a closed journal:\n%s", out)
	}

	second := fx.start()
	if got := fx.waitReady(second); got != runID {
		t.Fatalf("restart opened run %s, want the same %s", got, runID)
	}
	view := fx.waitHeld(budgetStage)
	if view.Spent.Tokens != 115 || view.Effective == nil || view.Effective.Tokens != 100 {
		t.Errorf("reopened view = %+v, want 115 spent of 100", view)
	}
	if code := second.interrupt(t); code != interruptedExitCode {
		t.Errorf("second exit code = %d, want %d", code, interruptedExitCode)
	}
}

// Without a dashboard nobody can raise a limit: the process lists the held scope and exits 75.
func TestBudgetIntegration_HeldRunWithoutAPIExits75(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	child := fx.startWithoutAPI()
	if code := child.exitCode(t); code != budgetHeldExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, budgetHeldExitCode, child.out.String())
	}
	out := child.out.String()
	for _, want := range []string{"held by a budget limit", "Stage build", "tokens 115 / 100", "held: limit reached", "raise the limit in flow.yaml", "a restarted run continues by itself"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The stopped run is readable without a writer lock: check and report name the scope, the figures and the hold.
func TestBudgetIntegration_CheckAndReportShowTheHeldRun(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	if code := fx.startWithoutAPI().exitCode(t); code != budgetHeldExitCode {
		t.Fatalf("exit code = %d, want %d", code, budgetHeldExitCode)
	}
	prev := rootDir
	rootDir = fx.root
	t.Cleanup(func() { rootDir = prev })

	for name, cmd := range map[string]func() *cobra.Command{"check": newCheckCmd, "report": newReportCmd} {
		var runErr error
		stdout, _ := captureOutput(t, func() {
			c := cmd()
			c.SilenceErrors, c.SilenceUsage = true, true
			runErr = c.Execute()
		})
		if runErr != nil {
			t.Fatalf("%s: %v", name, runErr)
		}
		for _, want := range []string{"Budgets", "Stage build", "tokens 115 / 100", "hard", "held: limit reached"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s output lacks %q:\n%s", name, want, stdout)
			}
		}
		if strings.Contains(stdout, "snapshot of a live run") {
			t.Errorf("%s labels a stopped run as live:\n%s", name, stdout)
		}
	}
}

// A medium limit lets the active turn finish and ends the run; repeat must not start another cycle.
func TestBudgetIntegration_RepeatStopsAfterTheLimitIsReached(t *testing.T) {
	fx := newBudgetFixture(t, "budget-medium-repeat.yaml", "150", 1)
	child := fx.startWithoutAPI()
	if code := child.exitCode(t); code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, child.out.String())
	}
	if !strings.Contains(child.out.String(), "repeat stopped: budget limit reached") {
		t.Errorf("output does not name the budget as the reason:\n%s", child.out.String())
	}
	if runs := fx.agentRuns(); len(runs) != 1 {
		t.Errorf("agent invocations = %v, want one cycle", runs)
	}
	if entries, _ := filepath.Glob(filepath.Join(fx.root, ".afm", "runs", "*")); len(entries) != 1 {
		t.Errorf("runs = %v, want one", entries)
	}
}

// Saving to flow.yaml writes the base the file declares, but the engine does not carry that base across a
// restart (engine gap): a runtime change made after the export is replaced by the file's value on reopen.
func TestBudgetIntegration_RuntimeChangeAfterExportIsReplacedByTheFileOnReopen(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	child := fx.start()
	fx.waitReady(child)
	fx.waitHeld(budgetStage)

	if v := fx.view(budgetStage); v.YAML == nil || !v.YAML.Available || v.YAML.File != "flow.yaml" || v.YAML.Declared == nil || v.YAML.Declared.Tokens != 100 {
		t.Fatalf("yaml block = %+v", v.YAML)
	}
	exported := fx.update(limitUpdate(budgetStage, 0, 400, `,"save_to_yaml":true`))
	if exported.Export == nil || exported.Export.Status != budget.ExportSaved {
		t.Fatalf("export = %+v", exported.Export)
	}
	raw, err := os.ReadFile(fx.flowPath)
	if err != nil || !strings.Contains(string(raw), "tokens: 400") {
		t.Fatalf("flow.yaml after the export (%v):\n%s", err, raw)
	}
	fx.update(limitUpdate(budgetStage, exported.View.Revision, 800, ""))
	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d", code)
	}

	// The file's 400 is above the spend of 115, so the reopened run releases the hold and finishes by itself.
	second := fx.start()
	fx.waitReady(second)
	fx.waitRunStatus("finished")
	view := fx.view(budgetStage).View
	if view.Effective == nil || view.Effective.Tokens != 400 {
		t.Errorf("effective limit after reopen = %+v, want the exported 400 (not the runtime 800)", view.Effective)
	}
	second.waitOutput(t, holdingFinalState)
	if code := second.interrupt(t); code != 0 {
		t.Errorf("second exit code = %d, want 0\n%s", code, second.out.String())
	}
}
