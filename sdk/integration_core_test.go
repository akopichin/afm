package afmsdk

// Core-mode scenarios against the real afm binary: no mock HTTP and no LLM, only the fake agents of
// testdata/core. Each test owns its processes and stops them through Shutdown or a cleanup.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const coreTestdata = "testdata/core"

// coreFixture is a workDir with a flow from testdata/core and the environment its fake agents read.
type coreFixture struct {
	t          *testing.T
	bin        string
	workDir    string
	flowPath   string
	runsLog    string
	releaseDir string
	client     *Client
}

// newCoreFixture writes flows/<flowFile> with its placeholders replaced by absolute agent paths and subst.
// The flow lives outside workDir; HOME and the process environment are isolated from the developer's.
func newCoreFixture(t *testing.T, flowFile string, subst map[string]string) *coreFixture {
	t.Helper()
	bin, err := filepath.Abs(resolveAfmBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	fx := &coreFixture{t: t, bin: bin, workDir: t.TempDir(), releaseDir: filepath.Join(t.TempDir(), "release")}
	fx.runsLog = filepath.Join(t.TempDir(), "agent-runs.log")
	if err := os.MkdirAll(fx.releaseDir, 0o755); err != nil { //nolint:gosec // test fixture directory
		t.Fatal(err)
	}
	isolateHome(t)
	for k, v := range map[string]string{
		"AFM_WORKTREES": "0", "AFM_USE_DOCKER": "0", "AFM_BROWSER": "0", "AFM_SIDEAGENT": "0",
		"AFM_IT_BIN": bin, "AFM_IT_RUNS_LOG": fx.runsLog,
	} {
		t.Setenv(k, v)
	}

	text := readCoreTestdata(t, "flows/"+flowFile)
	for placeholder, rel := range map[string]string{
		"__AGENT__": "agents/agent.sh", "__AUTO_AGENT__": "agents/auto_agent.sh", "__ASK_AGENT__": "agents/ask_agent.sh",
	} {
		abs, err := filepath.Abs(filepath.Join(coreTestdata, rel))
		if err != nil {
			t.Fatal(err)
		}
		text = strings.ReplaceAll(text, placeholder, abs)
	}
	for placeholder, value := range subst {
		text = strings.ReplaceAll(text, placeholder, value)
	}
	fx.flowPath = filepath.Join(t.TempDir(), "flow.yaml")
	writeCoreFile(t, fx.flowPath, text)
	writeCoreFile(t, filepath.Join(fx.workDir, ".afm", "config.yaml"), "server:\n  open_browser: false\n")

	fx.client, err = New(Config{Binary: bin, Mode: ModeCore})
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

func readCoreTestdata(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(coreTestdata, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeCoreFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test fixture directory
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // test fixture file
		t.Fatal(err)
	}
}

// gateImplementation holds the fake implementation agents until release is called for their stage.
func (fx *coreFixture) gateImplementation() {
	fx.t.Setenv("AFM_IT_RELEASE_DIR", fx.releaseDir)
}

func (fx *coreFixture) release(stage string) {
	fx.t.Helper()
	writeCoreFile(fx.t, filepath.Join(fx.releaseDir, stage), "")
}

// agentRuns counts the `<stage>:<phase>` lines the fake agents have written so far.
func (fx *coreFixture) agentRuns(line string) int {
	raw, err := os.ReadFile(fx.runsLog)
	if err != nil {
		return 0
	}
	n := 0
	for _, got := range strings.Fields(string(raw)) {
		if got == line {
			n++
		}
	}
	return n
}

// start launches the flow with fx.client and stops the process at the end of the test.
func (fx *coreFixture) start(ctx context.Context) *Run {
	fx.t.Helper()
	run, err := fx.client.Start(ctx, fx.flowPath, fx.workDir)
	if err != nil {
		fx.t.Fatalf("Start: %v", err)
	}
	fx.t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := run.Shutdown(stopCtx); errors.Is(err, ErrKilled) {
			fx.t.Errorf("afm ignored SIGINT during cleanup: %v", err)
		}
	})
	return run
}

func (fx *coreFixture) runDir(runID string) string {
	return filepath.Join(fx.workDir, ".afm", "runs", runID)
}

func coreTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustStatus(ctx context.Context, t *testing.T, run *Run) RunStatus {
	t.Helper()
	status, err := run.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// lastRunLevelEvent returns the newest run-level event of the run's journal, which only the test reads.
func lastRunLevelEvent(t *testing.T, runDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	last := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad journal line %q: %v", line, err)
		}
		switch ev.Event {
		case "run_started", "run_finished", "run_failed", "run_interrupted":
			last = ev.Event
		default:
		}
	}
	return last
}

func processArgs(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: pid of a process of this test
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return string(out)
}

func processAliveForTest(run *Run) bool {
	select {
	case <-run.exited:
		return false
	default:
		return true
	}
}

// exitCode returns the exit code an owned afm ended with, or fails when it did not end by an exit status.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v is not an *exec.ExitError", err)
	}
	return exitErr.ExitCode()
}

// requireResult waits for the workflow outcome and requires it to be state with a matching end time.
func requireResult(ctx context.Context, t *testing.T, run *Run, state string) RunStatus {
	t.Helper()
	result, err := run.WaitResult(ctx)
	if err != nil {
		t.Fatalf("WaitResult: %v", err)
	}
	if result.RunState != state || result.EndedAt == nil {
		t.Fatalf("WaitResult = state %q ended_at %v, want %q with an end time", result.RunState, result.EndedAt, state)
	}
	return result
}

func getStatusCode(ctx context.Context, t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// S1/S6: without Mode (or with ModeDashboard) afm starts as before, with the Web UI and no --mode flag.
func TestIntegration_CoreDashboardKeepsUI(t *testing.T) {
	for _, mode := range []Mode{"", ModeDashboard} {
		t.Run(fmt.Sprintf("mode=%q", mode), func(t *testing.T) {
			fx := newCoreFixture(t, "manual.yaml", nil)
			var err error
			if fx.client, err = New(Config{Binary: fx.bin, Mode: mode}); err != nil {
				t.Fatal(err)
			}
			ctx := coreTestContext(t)
			run := fx.start(ctx)

			if got := run.Mode(); got != ModeDashboard {
				t.Errorf("Mode() = %q, want %q", got, ModeDashboard)
			}
			if args := processArgs(t, run.PID()); strings.Contains(args, "--mode") {
				t.Errorf("argv %q carries --mode, want the pre-core command line", args)
			}
			if code := getStatusCode(ctx, t, run.baseURL+"/"); code != http.StatusOK {
				t.Errorf("GET / = %d, want 200 (the Web UI)", code)
			}
			waitForStageStatus(ctx, t, run, "build", StageAwaitingApproval, 30*time.Second)
		})
	}
}

// S7: core serves the API only, reports itself ready with the run it executes, and passes --mode core.
func TestIntegration_CoreStartServesAPIOnly(t *testing.T) {
	fx := newCoreFixture(t, "manual.yaml", nil)
	ctx := coreTestContext(t)
	run := fx.start(ctx)

	if run.Mode() != ModeCore {
		t.Errorf("Mode() = %q, want core", run.Mode())
	}
	if args := processArgs(t, run.PID()); !strings.Contains(args, "--mode core") {
		t.Errorf("argv %q lacks --mode core", args)
	}
	info, found, err := run.fetchRuntime(ctx)
	if err != nil || !found {
		t.Fatalf("fetchRuntime = found %v, err %v", found, err)
	}
	status := mustStatus(ctx, t, run)
	if info.Mode != "core" || info.ProtocolVersion != 1 || !info.Ready || info.RunID == "" || info.RunID != status.RunID {
		t.Errorf("runtime = %+v with status run_id %q, want a ready core of the same run", info, status.RunID)
	}
	if code := getStatusCode(ctx, t, run.baseURL+"/"); code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404 (no Web UI in core)", code)
	}
	if _, err := os.Stat(fx.runDir(info.RunID)); err != nil {
		t.Errorf("run directory of the ready run: %v", err)
	}
}

// S11 and S21: a flow that ends at once leaves the API up with the final run, and Shutdown does not rewrite it.
func TestIntegration_CoreInstantFlowResultAndShutdown(t *testing.T) {
	fx := newCoreFixture(t, "script.yaml", nil)
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	startedID := mustStatus(ctx, t, run).RunID

	result := requireResult(ctx, t, run, RunStateFinished)
	if result.RunID != startedID {
		t.Errorf("result run_id %q, want the run %q Start returned", result.RunID, startedID)
	}
	if got := mustStatus(ctx, t, run); got.RunState != RunStateFinished {
		t.Errorf("Status after the result: %q, want the API to keep serving finished", got.RunState)
	}
	if !processAliveForTest(run) {
		t.Fatal("core exited by itself after the workflow ended, want it to hold the API")
	}

	if err := run.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := run.Wait(ctx); err != nil {
		t.Errorf("Wait after Shutdown: %v", err)
	}
	if _, err := run.Status(ctx); err == nil {
		t.Error("Status succeeded after Shutdown, want the API closed")
	}
	if got := lastRunLevelEvent(t, fx.runDir(startedID)); got != "run_finished" {
		t.Errorf("last run-level event = %q, want run_finished", got)
	}
}

// S12: a flow outside workDir, with root_dir set, still gives the agent the project cwd and an absolute stage dir.
func TestIntegration_CorePathsFlowOutsideWorkDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // test fixture directory
		t.Fatal(err)
	}
	fx := newCoreFixture(t, "paths.yaml", map[string]string{"__ROOT_DIR__": root})
	records := t.TempDir()
	t.Setenv("AFM_IT_RECORD_DIR", records)
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	runID := requireResult(ctx, t, run, RunStateFinished).RunID

	if filepath.Dir(fx.flowPath) == fx.workDir {
		t.Fatal("fixture flow must live outside workDir")
	}
	wantCwd, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	gotCwd := strings.TrimSpace(readCoreTestdataFile(t, filepath.Join(records, "build.cwd")))
	if gotCwd != wantCwd {
		t.Errorf("agent cwd = %q, want root_dir %q", gotCwd, wantCwd)
	}
	stageDir := strings.TrimSpace(readCoreTestdataFile(t, filepath.Join(records, "build.stagedir")))
	if !filepath.IsAbs(stageDir) || !strings.HasPrefix(stageDir, fx.runDir(runID)) {
		t.Errorf("AFM_STAGE_DIR = %q, want an absolute path inside %s", stageDir, fx.runDir(runID))
	}
}

func readCoreTestdataFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// dialogQuestions returns the question entries of a stage dialog, without the agent's text entries.
func dialogQuestions(ctx context.Context, run *Run, stageID string) ([]DialogEntry, error) {
	entries, err := run.Dialog(ctx, stageID)
	var questions []DialogEntry
	for _, entry := range entries {
		if entry.Question != "" {
			questions = append(questions, entry)
		}
	}
	return questions, err
}

// S14: the question is asked once, nothing approves the plan but the client, and verify runs once before done.
func TestIntegration_CoreQuestionApproveVerify(t *testing.T) {
	fx := newCoreFixture(t, "question.yaml", nil)
	verifyLog := filepath.Join(t.TempDir(), "verify.log")
	t.Setenv("AFM_IT_VERIFY_LOG", verifyLog)
	ctx := coreTestContext(t)
	run := fx.start(ctx)

	waitForStageStatus(ctx, t, run, "build", StageAwaitingUserInput, 30*time.Second)
	entries, err := dialogQuestions(ctx, run, "build")
	if err != nil {
		t.Fatalf("Dialog: %v", err)
	}
	if len(entries) != 1 || entries[0].Question != "which database?" || entries[0].Answer != nil {
		t.Fatalf("Dialog = %+v, want one unanswered question", entries)
	}
	if err := run.Answer(ctx, "build", DialogAnswer{ID: entries[0].ID, Phase: entries[0].Phase, Answer: "sqlite", FromOptions: true}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	waitForStageStatus(ctx, t, run, "build", StageAwaitingApproval, 30*time.Second)
	time.Sleep(time.Second) // nothing but the client may approve the plan
	if got := mustStatus(ctx, t, run).Stages["build"]; got != StageAwaitingApproval {
		t.Fatalf("stage left awaiting_approval without Approve: %q", got)
	}
	if err := run.Approve(ctx, "build"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	result := requireResult(ctx, t, run, RunStateFinished)
	if result.Stages["build"] != StageDone {
		t.Errorf("stage build = %q, want done", result.Stages["build"])
	}
	entries, err = dialogQuestions(ctx, run, "build")
	if err != nil || len(entries) != 1 || entries[0].Answer == nil || *entries[0].Answer != "sqlite" {
		t.Errorf("Dialog after the run = %+v (err %v), want the one question answered once", entries, err)
	}
	if got := strings.Fields(readCoreTestdataFile(t, verifyLog)); len(got) != 1 {
		t.Errorf("verify ran %d times, want once", len(got))
	}
	if n := fx.agentRuns("build:planning"); n != 1 {
		t.Errorf("planning agent ran %d times, want 1", n)
	}
	summary := filepath.Join(fx.runDir(result.RunID), "build", "execution_summary.md")
	if _, err := os.Stat(summary); err != nil {
		t.Errorf("implementation artifact: %v", err)
	}
}

// S15: Pause/Continue, Revise and a repeated command behave as for any HTTP client; Retry is in the S18 test.
func TestIntegration_CoreStageControls(t *testing.T) {
	t.Run("pause and continue", func(t *testing.T) {
		fx := newCoreFixture(t, "auto.yaml", nil)
		fx.gateImplementation()
		ctx := coreTestContext(t)
		run := fx.start(ctx)
		waitUntil(t, "the implementation agent", func() bool { return fx.agentRuns("build:implementation") == 1 })

		if err := run.Pause(ctx, "build"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		waitForStageStatus(ctx, t, run, "build", StagePaused, 30*time.Second)
		if err := run.Continue(ctx, "build"); err != nil {
			t.Fatalf("Continue: %v", err)
		}
		waitForStageStatus(ctx, t, run, "build", StageRunning, 30*time.Second)
		fx.release("build")
		requireResult(ctx, t, run, RunStateFinished)
	})

	t.Run("revise replans", func(t *testing.T) {
		fx := newCoreFixture(t, "manual.yaml", nil)
		ctx := coreTestContext(t)
		run := fx.start(ctx)
		waitForStageStatus(ctx, t, run, "build", StageAwaitingApproval, 30*time.Second)

		if err := run.Revise(ctx, "build", "add more tests"); err != nil {
			t.Fatalf("Revise: %v", err)
		}
		waitUntil(t, "the second planning run", func() bool { return fx.agentRuns("build:planning") == 2 })
		waitForStageStatus(ctx, t, run, "build", StageAwaitingApproval, 30*time.Second)
	})

	t.Run("repeated approve is refused", func(t *testing.T) {
		fx := newCoreFixture(t, "manual.yaml", nil)
		fx.gateImplementation()
		ctx := coreTestContext(t)
		run := fx.start(ctx)
		waitForStageStatus(ctx, t, run, "build", StageAwaitingApproval, 30*time.Second)

		if err := run.Approve(ctx, "build"); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		waitUntil(t, "the implementation agent", func() bool { return fx.agentRuns("build:implementation") == 1 })
		var httpErr *HTTPError
		if err := run.Approve(ctx, "build"); !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
			t.Errorf("second Approve error = %v, want the FSM refusal as an *HTTPError with 400", err)
		}
		fx.release("build")
		requireResult(ctx, t, run, RunStateFinished)
	})
}

// S18: a failed stage is retryable, so WaitResult keeps waiting through it and returns only after Retry succeeds.
func TestIntegration_CoreWaitResultThroughRetry(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	fx := newCoreFixture(t, "script-gate.yaml", map[string]string{"__GATE__": gate})
	ctx := coreTestContext(t)
	run := fx.start(ctx)

	type outcome struct {
		result RunStatus
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := run.WaitResult(ctx)
		done <- outcome{result, err}
	}()
	waitForStageStatus(ctx, t, run, "glue", StageFailed, 30*time.Second)
	select {
	case got := <-done:
		t.Fatalf("WaitResult returned %+v / %v for a failed but retryable stage", got.result, got.err)
	case <-time.After(time.Second):
	}

	writeCoreFile(t, gate, "")
	if err := run.Retry(ctx, "glue"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.RunState != RunStateFinished {
			t.Fatalf("WaitResult = %+v / %v, want finished after Retry", got.result, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("WaitResult did not return after Retry")
	}
}

// S19: with --exit-on-fail the process leaves on the first failure; the result is failed, or unavailable with
// exit code 1, and never a made-up success.
func TestIntegration_CoreExitOnFail(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "never-created")
	fx := newCoreFixture(t, "script-gate.yaml", map[string]string{"__GATE__": gate})
	t.Setenv("AFM_EXIT_ON_FAIL", "1")
	ctx := coreTestContext(t)
	run := fx.start(ctx)

	result, err := run.WaitResult(ctx)
	switch {
	case err == nil:
		if result.RunState != RunStateFailed {
			t.Fatalf("WaitResult state = %q, want failed", result.RunState)
		}
	case errors.Is(err, ErrResultUnavailable):
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("WaitResult = %v, want ErrResultUnavailable wrapping exit code 1", err)
		}
	default:
		t.Fatalf("WaitResult: %v", err)
	}
	if code := exitCode(t, run.Wait(ctx)); code != 1 {
		t.Errorf("afm exit code = %d, want 1", code)
	}
}

// S18 and S22: SIGINT while an agent works records an interrupted run (exit 130); the result is never finished.
func TestIntegration_CoreShutdownDuringAgent(t *testing.T) {
	fx := newCoreFixture(t, "auto.yaml", nil)
	fx.gateImplementation()
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	runID := mustStatus(ctx, t, run).RunID
	waitUntil(t, "the implementation agent", func() bool { return fx.agentRuns("build:implementation") == 1 })

	resultErr := make(chan error, 1)
	var resultState string
	go func() {
		result, err := run.WaitResult(ctx)
		resultState = result.RunState
		resultErr <- err
	}()

	var wg sync.WaitGroup
	shutdownErrs := make([]error, 2)
	for i := range shutdownErrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shutdownErrs[i] = run.Shutdown(ctx)
		}()
	}
	waitErr := run.Wait(ctx)
	wg.Wait()

	if code := exitCode(t, shutdownErrs[0]); code != 130 {
		t.Errorf("Shutdown exit code = %d, want 130 (interrupted workflow)", code)
	}
	if shutdownErrs[0].Error() != shutdownErrs[1].Error() || shutdownErrs[0].Error() != waitErr.Error() {
		t.Errorf("results differ: %v / %v / Wait %v, want one shared result", shutdownErrs[0], shutdownErrs[1], waitErr)
	}
	if err := <-resultErr; err != nil && !errors.Is(err, ErrResultUnavailable) {
		t.Errorf("WaitResult error = %v, want a result or ErrResultUnavailable", err)
	} else if err == nil && resultState != RunStateInterrupted {
		t.Errorf("WaitResult state = %q, want interrupted", resultState)
	}
	if got := lastRunLevelEvent(t, fx.runDir(runID)); got != "run_interrupted" {
		t.Errorf("last run-level event = %q, want run_interrupted", got)
	}
	if _, err := run.Status(ctx); err == nil {
		t.Error("Status succeeded after Shutdown, want the API closed")
	}
}

// S20: cancelling WaitResult stops only the waiting; core keeps running and the stage does not change.
func TestIntegration_CoreCancelledWaitResultLeavesCore(t *testing.T) {
	fx := newCoreFixture(t, "auto.yaml", nil)
	fx.gateImplementation()
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	waitUntil(t, "the implementation agent", func() bool { return fx.agentRuns("build:implementation") == 1 })
	waitForStageStatus(ctx, t, run, "build", StageRunning, 30*time.Second)

	waitCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := run.WaitResult(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitResult error = %v, want the context deadline", err)
	}
	if !processAliveForTest(run) {
		t.Fatal("core exited after the wait was cancelled")
	}
	if got := mustStatus(ctx, t, run).Stages["build"]; got != StageRunning {
		t.Errorf("stage after the cancelled wait = %q, want running", got)
	}

	fx.release("build")
	requireResult(ctx, t, run, RunStateFinished)
}

// S24: a Connect handle reads the result but owns nothing; the process and its directory stay untouched.
func TestIntegration_CoreConnectHandleOwnsNothing(t *testing.T) {
	fx := newCoreFixture(t, "script.yaml", nil)
	ctx := coreTestContext(t)
	owner := fx.start(ctx)

	conn, err := Connect(ctx, owner.Port())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Mode() != ModeCore {
		t.Errorf("Mode() = %q, want core", conn.Mode())
	}
	requireResult(ctx, t, conn, RunStateFinished)

	if err := conn.Shutdown(ctx); !errors.Is(err, ErrNotOwned) {
		t.Errorf("Shutdown error = %v, want ErrNotOwned", err)
	}
	if err := conn.Wait(ctx); !errors.Is(err, ErrNotOwned) {
		t.Errorf("Wait error = %v, want ErrNotOwned", err)
	}
	if err := conn.Cleanup(); !errors.Is(err, ErrNotOwned) {
		t.Errorf("Cleanup error = %v, want ErrNotOwned", err)
	}
	if !processAliveForTest(owner) {
		t.Error("the owner process was stopped through a Connect handle")
	}
	if _, err := os.Stat(filepath.Join(fx.workDir, ".afm")); err != nil {
		t.Errorf("work directory touched through a Connect handle: %v", err)
	}
}

// S25: MaxConcurrent=1 keeps the slot until the first core exits; a cancelled waiting Start changes nothing.
func TestIntegration_CoreMaxConcurrentHoldsSlotUntilShutdown(t *testing.T) {
	first := newCoreFixture(t, "script.yaml", nil)
	client, err := New(Config{Binary: first.bin, Mode: ModeCore, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	first.client = client
	ctx := coreTestContext(t)
	run1 := first.start(ctx)
	requireResult(ctx, t, run1, RunStateFinished)

	second := newCoreFixture(t, "script.yaml", nil)
	type startOutcome struct {
		run *Run
		err error
	}
	startDone := make(chan startOutcome, 1)
	startCtx, cancelStart := context.WithCancel(ctx)
	go func() {
		run, err := client.Start(startCtx, second.flowPath, second.workDir)
		startDone <- startOutcome{run: run, err: err}
	}()
	// Runs on any exit, including Fatal: stop the waiting Start and close whatever it returned.
	var outcome *startOutcome
	t.Cleanup(func() {
		cancelStart()
		if outcome == nil {
			got := <-startDone
			outcome = &got
		}
		if outcome.run != nil {
			_ = outcome.run.Shutdown(context.Background())
		}
	})
	select {
	case got := <-startDone:
		outcome = &got
		t.Fatalf("second Start returned (%v) while the first core still holds its slot", got.err)
	case <-time.After(time.Second):
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Start(cancelCtx, second.flowPath, second.workDir); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled Start error = %v, want context.Canceled", err)
	}
	if _, err := run1.Status(ctx); err != nil {
		t.Errorf("first core after a cancelled Start: %v", err)
	}

	if err := run1.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case got := <-startDone:
		outcome = &got
		if got.err != nil {
			t.Fatalf("second Start after the slot was freed: %v", got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second Start did not continue after Shutdown")
	}
}

// S26: two clients with separate work directories run side by side with their own ids, ports and shutdown.
func TestIntegration_CoreIndependentClients(t *testing.T) {
	ctx := coreTestContext(t)
	fx1 := newCoreFixture(t, "manual.yaml", nil)
	run1 := fx1.start(ctx)
	fx2 := newCoreFixture(t, "auto.yaml", nil)
	run2 := fx2.start(ctx)

	if run1.Port() == run2.Port() || run1.PID() == run2.PID() {
		t.Fatalf("ports %d/%d or pids %d/%d are shared", run1.Port(), run2.Port(), run1.PID(), run2.PID())
	}
	s1, s2 := mustStatus(ctx, t, run1), mustStatus(ctx, t, run2)
	if s1.RunID == s2.RunID || s1.FlowName != "core-manual" || s2.FlowName != "core-auto" {
		t.Fatalf("statuses %q/%q and %q/%q are not independent", s1.FlowName, s1.RunID, s2.FlowName, s2.RunID)
	}

	waitForStageStatus(ctx, t, run1, "build", StageAwaitingApproval, 30*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- run1.Approve(ctx, "build")
	}()
	go func() {
		defer wg.Done()
		_, err := run2.WaitResult(ctx)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent request: %v", err)
		}
	}
	requireResult(ctx, t, run1, RunStateFinished)

	if err := run1.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown first: %v", err)
	}
	if _, err := run2.Status(ctx); err != nil {
		t.Errorf("second core after the first was shut down: %v", err)
	}
}

// S17: after a restart over the same workDir the run id and the open question are the same, the question is not
// asked again, and WaitResult does not hand out the interrupted outcome the previous process left behind.
func TestIntegration_CoreRestartKeepsRunAndQuestion(t *testing.T) {
	fx := newCoreFixture(t, "question.yaml", nil)
	t.Setenv("AFM_IT_VERIFY_LOG", filepath.Join(t.TempDir(), "verify.log"))
	ctx := coreTestContext(t)
	first := fx.start(ctx)
	runID := mustStatus(ctx, t, first).RunID
	waitForStageStatus(ctx, t, first, "build", StageAwaitingUserInput, 30*time.Second)
	if code := exitCode(t, first.Shutdown(ctx)); code != 130 {
		t.Fatalf("first Shutdown exit code = %d, want 130", code)
	}

	second := fx.start(ctx)
	if got := mustStatus(ctx, t, second).RunID; got != runID {
		t.Fatalf("restart opened run %s, want the interrupted %s", got, runID)
	}
	waitForStageStatus(ctx, t, second, "build", StageAwaitingUserInput, 30*time.Second)
	entries, err := dialogQuestions(ctx, second, "build")
	if err != nil || len(entries) != 1 || entries[0].Question != "which database?" {
		t.Fatalf("Dialog after restart = %+v (err %v), want the one open question", entries, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if result, err := second.WaitResult(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitResult = %+v / %v, want to keep waiting for the restarted run", result, err)
	}

	if err := second.Answer(ctx, "build", DialogAnswer{ID: entries[0].ID, Phase: entries[0].Phase, Answer: "sqlite", FromOptions: true}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitForStageStatus(ctx, t, second, "build", StageAwaitingApproval, 30*time.Second)
	if err := second.Approve(ctx, "build"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if got := requireResult(ctx, t, second, RunStateFinished); got.RunID != runID {
		t.Errorf("result run_id %q, want %q", got.RunID, runID)
	}
	if entries, err = dialogQuestions(ctx, second, "build"); err != nil || len(entries) != 1 {
		t.Errorf("Dialog after the run = %+v (err %v), want the question once", entries, err)
	}
}

// S16: AttachAndSend and Send are idempotent for a repeated request_id, and the helper's answer reaches the Inbox.
func TestIntegration_CoreHelperMessagingIsIdempotent(t *testing.T) {
	fx := newCoreFixture(t, "auto.yaml", nil)
	fx.gateImplementation()
	helper, err := filepath.Abs(filepath.Join(coreTestdata, "agents/helper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeCoreFile(t, filepath.Join(fx.workDir, ".afm", "config.yaml"), fmt.Sprintf(`server:
  open_browser: false
docker:
  agents:
    %q:
      type: codex
collaboration:
  enabled: true
  profiles:
    reviewer:
      title: Reviewer
      command: %s
      instructions: Review.
`, helper, helper))
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	waitUntil(t, "the implementation agent", func() bool { return fx.agentRuns("build:implementation") == 1 })

	first := "please review"
	attach := AttachAndSendRequest{RequestID: "attach-1", ProfileID: "reviewer", StageID: "build", OriginStage: "build", Instruction: "look", FirstMessage: &first}
	attached, err := run.AttachAndSend(ctx, attach)
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	replayed, err := run.AttachAndSend(ctx, attach)
	if err != nil {
		t.Fatalf("AttachAndSend replay: %v", err)
	}
	if replayed.TaskID != attached.TaskID || replayed.ThreadID != attached.ThreadID || replayed.ParticipantID != attached.ParticipantID {
		t.Fatalf("replayed attach = %+v, want the same helper as %+v", replayed, attached)
	}

	send := MeshSendRequest{RequestID: "send-1", ThreadID: attached.ThreadID, Recipients: []string{attached.ParticipantID}, Kind: "message", Body: "second note", OriginStage: "build"}
	receipt, err := run.Send(ctx, send)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if again, err := run.Send(ctx, send); err != nil || again.MessageID != receipt.MessageID {
		t.Fatalf("Send replay = %+v (err %v), want message %s again", again, err, receipt.MessageID)
	}

	waitUntil(t, "the helper's answer in the Inbox", func() bool {
		page, err := run.Inbox(ctx, 0, 100)
		if err != nil {
			t.Fatalf("Inbox: %v", err)
		}
		for _, msg := range page.Messages {
			if msg.SenderID == attached.ParticipantID {
				return true
			}
		}
		return false
	})
	page, err := run.Thread(ctx, attached.ThreadID, 0, 100)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	bodies := map[string]int{}
	for _, msg := range page.Messages {
		bodies[msg.Body]++
	}
	if bodies[first] != 1 || bodies["second note"] != 1 {
		t.Errorf("thread bodies = %v, want each user message exactly once", bodies)
	}

	fx.release("build")
	requireResult(ctx, t, run, RunStateFinished)
}

// S13: with repeat, WaitResult answers for the cycle it started on or reports the change; the next cycle runs on.
func TestIntegration_CoreRepeatCycles(t *testing.T) {
	fx := newCoreFixture(t, "repeat.yaml", nil)
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	firstID := mustStatus(ctx, t, run).RunID

	result, err := run.WaitResult(ctx)
	var changed *RunChangedError
	switch {
	case err == nil:
		if result.RunID != firstID {
			t.Errorf("result run_id %q, want the awaited run %q", result.RunID, firstID)
		}
	case errors.As(err, &changed):
		if changed.Expected != firstID || changed.Current == firstID {
			t.Errorf("RunChangedError = %+v, want a change away from %q", changed, firstID)
		}
	default:
		t.Fatalf("WaitResult: %v", err)
	}

	// The API restarts between cycles, so a refused connection is part of the wait.
	waitUntil(t, "the second cycle to finish", func() bool {
		status, err := run.Status(ctx)
		return err == nil && status.RunID != firstID && status.RunState == RunStateFinished && status.EndedAt != nil
	})
	if second := requireResult(ctx, t, run, RunStateFinished); second.RunID == firstID {
		t.Errorf("second WaitResult returned the first cycle's run %q", firstID)
	}
	if n := fx.agentRuns("build:implementation"); n != 2 {
		t.Errorf("implementation ran %d times, want once per cycle", n)
	}
}

// S13: without core, repeat is unchanged: afm runs both cycles and exits by itself.
func TestIntegration_CoreRepeatKeepsDashboardBehavior(t *testing.T) {
	fx := newCoreFixture(t, "repeat.yaml", nil)
	var err error
	if fx.client, err = New(Config{Binary: fx.bin}); err != nil {
		t.Fatal(err)
	}
	ctx := coreTestContext(t)
	run := fx.start(ctx)
	if err := run.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n := fx.agentRuns("build:implementation"); n != 2 {
		t.Errorf("implementation ran %d times, want once per cycle", n)
	}
}
