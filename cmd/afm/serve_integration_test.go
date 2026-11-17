package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/pkg/runregistry"
)

// `afm serve` on real processes: the built afm as the host, host-started and hand-started `afm run` children
// with the fake agents of testdata/core-mode, and a go-sdk MCP client. HOME is a temp dir everywhere, so the
// real ~/.afm is never touched.

const (
	serveData     = "testdata/serve"
	serveFlow     = "prompt.yaml"
	serveFlowLoop = "prompt-repeat.yaml"
	// serveStopWait bounds how long cleanup lets a process exit before killing it.
	serveStopWait = 15 * time.Second
)

var serveEndpointLine = regexp.MustCompile(`MCP endpoint (http://\S+)`)

// serveProc is an afm process started by the test: the host or a hand-started run.
type serveProc struct {
	cmd  *exec.Cmd
	out  *childOutput
	done chan struct{}
}

func startServeProc(t *testing.T, dir string, env []string, bin string, args ...string) *serveProc {
	t.Helper()
	cmd := exec.Command(bin, args...) //nolint:gosec // G204: the afm binary this test built
	cmd.Dir = dir
	cmd.Env = env
	p := &serveProc{cmd: cmd, out: &childOutput{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() { p.stop(t) })
	return p
}

// stop interrupts the process and kills it when it does not exit in time.
func (p *serveProc) stop(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(serveStopWait):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Errorf("process %d ignored SIGINT; output:\n%s", p.cmd.Process.Pid, p.out.String())
	}
}

func (p *serveProc) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(loopTestTimeout):
		t.Fatalf("process did not exit; output:\n%s", p.out.String())
	}
	return p.cmd.ProcessState.ExitCode()
}

// endpoint waits for the host to print where its MCP listens.
func (p *serveProc) endpoint(t *testing.T) string {
	t.Helper()
	var url string
	waitFor(t, "the host MCP endpoint", func() bool {
		select {
		case <-p.done:
			t.Fatalf("host exited before it served: %s", p.out.String())
		default:
		}
		m := serveEndpointLine.FindStringSubmatch(p.out.String())
		if m == nil {
			return false
		}
		url = m[1]
		return true
	})
	return url
}

// pidAlive reports whether pid is a live process; a zombie nobody reaped counts as gone.
func pidAlive(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	return err == nil && stat != "" && !strings.HasPrefix(stat, "Z")
}

type serveFixture struct {
	t          *testing.T
	bin        string
	home       string
	alpha      string
	beta       string
	cfgPath    string
	releaseDir string
	promptLog  string
	env        []string
}

func newServeFixture(t *testing.T, bin string) *serveFixture {
	t.Helper()
	root := t.TempDir()
	fx := &serveFixture{t: t, bin: bin, home: filepath.Join(root, "home"), alpha: filepath.Join(root, "alpha"), beta: filepath.Join(root, "beta")}
	fx.releaseDir = filepath.Join(root, "release")
	fx.promptLog = filepath.Join(root, "prompts.log")
	fx.cfgPath = filepath.Join(root, "serve.yaml")
	for _, dir := range []string{fx.home, fx.releaseDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // test fixture directory
			t.Fatal(err)
		}
	}
	for _, project := range []string{fx.alpha, fx.beta} {
		for _, flow := range []string{serveFlow, serveFlowLoop} {
			text := readTestdata(t, "flows/"+flow)
			text = strings.ReplaceAll(text, "__PROMPT_AGENT__", testdataAbs(t, "agents/prompt_agent.sh"))
			writeFile(t, filepath.Join(project, ".afm", "flows", flow), text)
		}
		writeFile(t, filepath.Join(project, ".afm", "config.yaml"), "server:\n  open_browser: false\n")
	}
	cfg, err := os.ReadFile(filepath.Join(serveData, "serve.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgText := strings.NewReplacer("__ALPHA__", fx.alpha, "__BETA__", fx.beta).Replace(string(cfg))
	writeFile(t, fx.cfgPath, cfgText)
	// Agents hold their implementation until the test releases the stage; AFM_IT_PROMPT_LOG records prompts.
	fx.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fx.home,
		"AFM_WORKTREES=0",
		"AFM_USE_DOCKER=0",
		"AFM_BROWSER=0",
		"AFM_SIDEAGENT=0",
		"AFM_IT_RELEASE_DIR=" + fx.releaseDir,
		"AFM_IT_PROMPT_LOG=" + fx.promptLog,
	}
	// Registered first, so it runs after every host is stopped: nothing may outlive the test.
	t.Cleanup(fx.stopRuns)
	return fx
}

// stopRuns interrupts every run left in the process registry, then kills those that ignore it.
func (fx *serveFixture) stopRuns() {
	records, err := runregistry.List(runregistry.Dir(fx.home))
	if err != nil {
		fx.t.Errorf("list leftover runs: %v", err)
		return
	}
	for _, rec := range records {
		if !pidAlive(rec.PID) {
			continue
		}
		_ = syscall.Kill(rec.PID, syscall.SIGINT)
		deadline := time.Now().Add(serveStopWait)
		for pidAlive(rec.PID) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if pidAlive(rec.PID) {
			_ = syscall.Kill(rec.PID, syscall.SIGKILL)
			fx.t.Errorf("run %d ignored SIGINT", rec.PID)
		}
	}
}

func (fx *serveFixture) startHost() (*serveProc, *sdkmcp.ClientSession) {
	fx.t.Helper()
	host := startServeProc(fx.t, fx.home, fx.env, fx.bin, "serve", "--port", "0", "--config", fx.cfgPath)
	endpoint := host.endpoint(fx.t)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "serve-integration", Version: "0"}, nil)
	session, err := client.Connect(fx.t.Context(), &sdkmcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		fx.t.Fatalf("connect %s: %v", endpoint, err)
	}
	fx.t.Cleanup(func() { _ = session.Close() })
	return host, session
}

// startManual starts `afm run` by hand in a project, as a user would.
func (fx *serveFixture) startManual(project string) *serveProc {
	fx.t.Helper()
	port, err := pickFreeLoopbackPort()
	if err != nil {
		fx.t.Fatal(err)
	}
	flow := filepath.Join(project, ".afm", "flows", serveFlow)
	return startServeProc(fx.t, project, fx.env, fx.bin, "run", flow, "--dir", project, "--mode", "core", "--port", strconv.Itoa(port))
}

// releaseAgents lets the fake agents of the stage finish.
func (fx *serveFixture) releaseAgents() { writeFile(fx.t, filepath.Join(fx.releaseDir, "build"), "") }

func (fx *serveFixture) prompts() []string { return promptsIn(fx.promptLog) }

type serveLaunch struct {
	LaunchID   string `json:"launch_id"`
	State      string `json:"state"`
	RunID      string `json:"run_id"`
	InstanceID string `json:"instance_id"`
	Error      string `json:"error"`
}

// serveReport is the part of afm_run_report that tells how the workflow ended.
type serveReport struct {
	Run    serveReportRun     `json:"run"`
	Stages []serveReportStage `json:"stages"`
}

type serveReportRun struct {
	Status string `json:"status"`
}

type serveReportStage struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type serveRow struct {
	RunID      string       `json:"run_id"`
	InstanceID string       `json:"instance_id"`
	Ownership  string       `json:"ownership"`
	Liveness   string       `json:"liveness"`
	Mode       string       `json:"mode"`
	APIURL     string       `json:"api_url"`
	Launch     *serveLaunch `json:"launch"`
	RunStatus  string       `json:"run_status"`
	Actions    []string     `json:"actions"`
}

type serveFailure struct {
	Code         string `json:"code"`
	CurrentRunID string `json:"current_run_id"`
}

func (r serveRow) can(action string) bool {
	for _, a := range r.Actions {
		if a == action {
			return true
		}
	}
	return false
}

func callServe(t *testing.T, s *sdkmcp.ClientSession, name string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// mustCall calls a tool that must succeed and decodes its structured result into out (nil: ignore it).
func mustCall(t *testing.T, s *sdkmcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := callServe(t, s, name, args)
	if res.IsError {
		t.Fatalf("%s failed: %v", name, res.StructuredContent)
	}
	if out != nil {
		decodeMCP(t, res, out)
	}
}

// refusalOf calls a tool that must be refused and returns the failure.
func refusalOf(t *testing.T, s *sdkmcp.ClientSession, name string, args map[string]any) serveFailure {
	t.Helper()
	res := callServe(t, s, name, args)
	if !res.IsError {
		t.Fatalf("%s succeeded, want a refusal: %v", name, res.StructuredContent)
	}
	var f serveFailure
	decodeMCP(t, res, &f)
	return f
}

func listRuns(t *testing.T, s *sdkmcp.ClientSession) []serveRow {
	t.Helper()
	var out struct {
		Runs []serveRow `json:"runs"`
	}
	mustCall(t, s, "afm_runs_list", nil, &out)
	return out.Runs
}

// waitRow waits for the first row accepted by match.
func waitRow(t *testing.T, s *sdkmcp.ClientSession, what string, match func(serveRow) bool) serveRow {
	t.Helper()
	var found serveRow
	waitFor(t, what, func() bool {
		for _, row := range listRuns(t, s) {
			if match(row) {
				found = row
				return true
			}
		}
		return false
	})
	return found
}

func launchRow(launchID string) func(serveRow) bool {
	return func(r serveRow) bool { return r.Launch != nil && r.Launch.LaunchID == launchID }
}

// launchExited matches a launch whose process is gone; its registry record went with it, so the row comes from
// the receipt alone.
func launchExited(launchID string) func(serveRow) bool {
	return func(r serveRow) bool { return launchRow(launchID)(r) && r.Launch.State == "exited" }
}

func startArgs(requestID, project, flow, mode, task string) map[string]any {
	return map[string]any{"request_id": requestID, "project": project, "flow": flow, "mode": mode, "task": task}
}

// startReady starts a run through the host and waits until its launch is ready and the run is known.
func startReady(t *testing.T, s *sdkmcp.ClientSession, args map[string]any) (serveLaunch, serveRow) {
	t.Helper()
	var launch serveLaunch
	mustCall(t, s, "afm_run_start", args, &launch)
	row := waitRow(t, s, "launch "+launch.LaunchID[:8]+" to be ready", func(r serveRow) bool {
		return launchRow(launch.LaunchID)(r) && r.Launch.State == "ready" && r.RunID != "" && r.Liveness == "active"
	})
	return launch, row
}

func probeRuntime(t *testing.T, apiURL string) (mode string, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/runtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var rt struct {
		Mode string `json:"mode"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rt) != nil {
		return "", false
	}
	return rt.Mode, true
}

func buildServeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "afm")
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	out, err := exec.CommandContext(context.Background(), goBin, "build", "-o", bin, ".").CombinedOutput() //nolint:gosec // G204: the toolchain of this very test run
	if err != nil {
		t.Fatalf("go build afm: %v\n%s", err, out)
	}
	return bin
}

// TestServe runs every scenario against one built binary; each scenario has its own HOME, projects and host.
func TestServe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("afm serve needs POSIX process control")
	}
	bin := buildServeBinary(t)
	scenarios := []struct {
		name string
		run  func(t *testing.T, fx *serveFixture)
	}{
		{"lists a verified manual run as attached", serveManualRun},
		{"start with a task, duplicates and admission", serveStartTask},
		{"dashboard start", serveDashboardStart},
		{"host SIGKILL and restart reconnects", serveHostKilled},
		{"host shutdown leaves the run", serveShutdownLeavesRun},
		{"cancel, release and report after exit", serveCancelRelease},
		{"run_changed after a repeat cycle", serveRunChanged},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { sc.run(t, newServeFixture(t, bin)) })
	}
}

func serveManualRun(t *testing.T, fx *serveFixture) {
	manual := fx.startManual(fx.alpha)
	_, session := fx.startHost()

	// Its endpoint proves its identity, so the listing alone shows it attached; no afm_run_attach is needed.
	attached := waitRow(t, session, "the manual run to be listed as attached", func(r serveRow) bool {
		return r.Ownership == "attached" && r.Liveness == "active" && r.RunID != "" && r.InstanceID != "" && r.APIURL != ""
	})
	if attached.can("cancel") || attached.can("release") {
		t.Fatalf("attached run actions = %v, want none that signal its process", attached.Actions)
	}
	var status struct {
		RunID string `json:"run_id"`
	}
	mustCall(t, session, "afm_run_status", map[string]any{"run_id": attached.RunID}, &status)
	if status.RunID != attached.RunID {
		t.Fatalf("forwarded afm_run_status run_id = %q, want %q", status.RunID, attached.RunID)
	}
	if f := refusalOf(t, session, "afm_run_cancel", map[string]any{"run_id": attached.RunID}); f.Code != "not_managed" {
		t.Fatalf("cancel of an attached run = %q, want not_managed", f.Code)
	}
	select {
	case <-manual.done:
		t.Fatalf("the host stopped the attached run:\n%s", manual.out.String())
	default:
	}
}

func serveStartTask(t *testing.T, fx *serveFixture) {
	_, session := fx.startHost()
	args := startArgs("req-task", "alpha", serveFlow, "core", "serve task text")
	first, row := startReady(t, session, args)

	// Readiness of the runtime says nothing about the agent: wait until it logged its whole prompt.
	waitFor(t, "the agent to receive its prompt", func() bool { return fx.agentStarted(row.RunID) })
	prompts := fx.prompts()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "serve task text") || !strings.Contains(prompts[0], "flow instructions") {
		t.Fatalf("agent prompts = %q, want one with the flow prompt and the task", prompts)
	}
	if row.Mode != "core" || row.Ownership != "managed" {
		t.Fatalf("started row = %+v, want a managed core run", row)
	}

	var again serveLaunch
	mustCall(t, session, "afm_run_start", args, &again)
	if again.LaunchID != first.LaunchID {
		t.Fatalf("repeated request_id gave launch %s, want %s", again.LaunchID, first.LaunchID)
	}
	different := startArgs("req-task", "alpha", serveFlow, "core", "another task")
	if f := refusalOf(t, session, "afm_run_start", different); f.Code != "request_conflict" {
		t.Fatalf("same request_id with other parameters = %q, want request_conflict", f.Code)
	}
	if f := refusalOf(t, session, "afm_run_start", startArgs("req-second", "alpha", serveFlow, "core", "")); f.Code != "conflict" {
		t.Fatalf("second run of the same flow in the same checkout = %q, want conflict", f.Code)
	}
	if n := len(fx.prompts()); n != 1 {
		t.Fatalf("%d agent prompts after duplicates, want still 1", n)
	}
	count := 0
	for _, r := range listRuns(t, session) {
		if r.Launch != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d host-started runs listed, want 1", count)
	}
}

func serveDashboardStart(t *testing.T, fx *serveFixture) {
	_, session := fx.startHost()
	_, row := startReady(t, session, startArgs("req-dash", "beta", serveFlow, "dashboard", ""))
	if row.Mode != "dashboard" {
		t.Fatalf("row mode = %q, want dashboard", row.Mode)
	}
	if mode, ok := probeRuntime(t, row.APIURL); !ok || mode != "dashboard" {
		t.Fatalf("/api/runtime of %s = %q, %v; want dashboard", row.APIURL, mode, ok)
	}
}

func serveHostKilled(t *testing.T, fx *serveFixture) {
	host, session := fx.startHost()
	args := startArgs("req-survive", "alpha", serveFlow, "core", "survive the host")
	launch, before := startReady(t, session, args)
	// The prompt is counted after the restart: it must be there before, so a missing one is not taken for a
	// launch the restart did not repeat.
	waitFor(t, "the agent to receive its prompt", func() bool { return fx.agentStarted(before.RunID) })

	if err := host.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-host.done
	if _, ok := probeRuntime(t, before.APIURL); !ok {
		t.Fatal("the run stopped with the host")
	}

	_, session = fx.startHost()
	after := waitRow(t, session, "the restarted host to find the run", func(r serveRow) bool {
		return launchRow(launch.LaunchID)(r) && r.Liveness == "active" && r.Ownership == "managed"
	})
	if after.InstanceID != before.InstanceID || after.RunID != before.RunID {
		t.Fatalf("after restart %s/%s, want the same run %s/%s", after.InstanceID, after.RunID, before.InstanceID, before.RunID)
	}
	var again serveLaunch
	mustCall(t, session, "afm_run_start", args, &again)
	if again.LaunchID != launch.LaunchID {
		t.Fatalf("repeated request_id after restart gave launch %s, want %s", again.LaunchID, launch.LaunchID)
	}
	matching := 0
	for _, r := range listRuns(t, session) {
		if r.RunID == before.RunID {
			matching++
		}
	}
	if matching != 1 || len(fx.prompts()) != 1 {
		t.Fatalf("%d rows for the run and %d prompts after restart, want 1 and 1", matching, len(fx.prompts()))
	}
	var status struct {
		RunID string `json:"run_id"`
	}
	mustCall(t, session, "afm_run_status", map[string]any{"run_id": before.RunID}, &status)
	if status.RunID != before.RunID {
		t.Fatalf("status through the restarted host = %q, want %q", status.RunID, before.RunID)
	}
}

func serveShutdownLeavesRun(t *testing.T, fx *serveFixture) {
	host, session := fx.startHost()
	_, row := startReady(t, session, startArgs("req-leave", "alpha", serveFlow, "core", ""))

	if err := host.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := host.exitCode(t); code != 0 {
		t.Fatalf("host exit code = %d, want 0; output:\n%s", code, host.out.String())
	}
	if _, ok := probeRuntime(t, row.APIURL); !ok {
		t.Fatal("the run stopped when the host shut down")
	}
}

func serveCancelRelease(t *testing.T, fx *serveFixture) {
	_, session := fx.startHost()

	// Cancel: the agent is held, so the workflow is active.
	heldLaunch, held := startReady(t, session, startArgs("req-cancel", "alpha", serveFlow, "core", ""))
	if !held.can("cancel") {
		t.Fatalf("active run actions = %v, want cancel", held.Actions)
	}
	var stopped struct {
		State string `json:"state"`
	}
	// The agent has read its whole prompt, so the cancel meets the stage in a known phase.
	waitFor(t, "the held agent to receive its prompt", func() bool { return fx.agentStarted(held.RunID) })
	mustCall(t, session, "afm_run_cancel", map[string]any{"run_id": held.RunID}, &stopped)
	if stopped.State != "stopped" && stopped.State != "killed" {
		t.Fatalf("cancel state = %q, want stopped or killed", stopped.State)
	}
	cancelled := waitRow(t, session, "the cancelled launch to exit", launchExited(heldLaunch.LaunchID))
	requireCleanExit(t, cancelled, "interrupted")
	requireReportStatus(t, session, held.RunID, "interrupted", "failed")

	// Release: the workflow finishes, the core runtime holds its API until released.
	fx.releaseAgents()
	doneLaunch, done := startReady(t, session, startArgs("req-release", "beta", serveFlow, "core", ""))
	finished := waitRow(t, session, "the finished run to be releasable", func(r serveRow) bool { return r.RunID == done.RunID && r.can("release") })
	if finished.RunStatus != "finished" {
		t.Fatalf("releasable run status = %q, want finished", finished.RunStatus)
	}
	mustCall(t, session, "afm_run_release", map[string]any{"run_id": done.RunID}, nil)
	released := waitRow(t, session, "the released launch to exit", launchExited(doneLaunch.LaunchID))
	requireCleanExit(t, released, "finished")
	requireReportStatus(t, session, done.RunID, "finished", "done")
}

// agentStarted reports whether the agent of runID logged its complete prompt.
func (fx *serveFixture) agentStarted(runID string) bool {
	return slices.ContainsFunc(fx.prompts(), func(record string) bool {
		header, _, _ := strings.Cut(record, "\n")
		return strings.Contains(header, runID)
	})
}

// requireCleanExit fails when an exited launch carries an error, a stop the user asked for is not a failure, or
// the row lost the status the workflow ended with.
func requireCleanExit(t *testing.T, row serveRow, runStatus string) {
	t.Helper()
	if row.Launch.State != "exited" || row.Launch.Error != "" {
		t.Fatalf("launch after the stop = %+v, want exited without an error", *row.Launch)
	}
	if row.RunStatus != runStatus {
		t.Errorf("exited row run_status = %q, want %q", row.RunStatus, runStatus)
	}
}

// requireReportStatus reads the report from the run's files and checks how the workflow and its only stage,
// build, ended.
func requireReportStatus(t *testing.T, s *sdkmcp.ClientSession, runID, want, buildStatus string) {
	t.Helper()
	var report serveReport
	mustCall(t, s, "afm_run_report", map[string]any{"run_id": runID}, &report)
	if report.Run.Status != want {
		t.Fatalf("report status of %s = %q, want %q", runID, report.Run.Status, want)
	}
	if len(report.Stages) != 1 || report.Stages[0].ID != "build" || report.Stages[0].Status != buildStatus {
		t.Errorf("report stages of %s = %+v, want build %s", runID, report.Stages, buildStatus)
	}
}

func serveRunChanged(t *testing.T, fx *serveFixture) {
	_, session := fx.startHost()
	_, first := startReady(t, session, startArgs("req-repeat", "alpha", serveFlowLoop, "core", ""))

	fx.releaseAgents()
	next := waitRow(t, session, "the repeat to open a new run", func(r serveRow) bool {
		// The record names the new run before its endpoint answers; only an active row can be asked for status.
		return r.Launch != nil && r.RunID != "" && r.RunID != first.RunID && r.Liveness == "active"
	})
	f := refusalOf(t, session, "afm_run_status", map[string]any{"run_id": first.RunID})
	if f.Code != "run_changed" || f.CurrentRunID != next.RunID {
		t.Fatalf("status of the previous cycle = %+v, want run_changed with current_run_id %s", f, next.RunID)
	}
	var status struct {
		RunID string `json:"run_id"`
	}
	mustCall(t, session, "afm_run_status", map[string]any{"run_id": next.RunID}, &status)
	if status.RunID != next.RunID {
		t.Fatalf("status of the current cycle = %q, want %q", status.RunID, next.RunID)
	}
}
