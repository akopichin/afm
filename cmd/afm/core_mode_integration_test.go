package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/state"
)

// Сквозные проверки режимов запуска: реальный `afm run` дочерним процессом (startAfmChild) и только HTTP снаружи.
// Общая часть идёт в обоих режимах и сравнивает журнал; остальное — только core. Агентов и LLM нет: flows и
// фейковые скрипты лежат в testdata/core-mode.

const (
	coreModeData = "testdata/core-mode"
)

var bothModes = []string{modeDashboard, modeCore}

// modeFixture — afm-root с flow.yaml из testdata и свободным портом для одного `afm run --mode <mode>`.
type modeFixture struct {
	t          *testing.T
	mode       string
	root       string
	flowPath   string
	agentPath  string // команда стадий flow: динамическая стадия обязана использовать уже подготовленную команду
	port       int
	runsLog    string
	releaseDir string
	env        []string
}

// newModeFixture подставляет в flows/<flowFile> абсолютные пути фейковых агентов; flowTail дописывается в конец
// flow.yaml (корневые ключи вроде hooks).
func newModeFixture(t *testing.T, mode, flowFile, flowTail string) *modeFixture {
	t.Helper()
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	fx := &modeFixture{t: t, mode: mode, root: t.TempDir(), port: port}
	fx.runsLog = filepath.Join(fx.root, "agent-runs.log")
	fx.releaseDir = filepath.Join(fx.root, "release")
	fx.agentPath = testdataAbs(t, "agents/auto_agent.sh")
	text := readTestdata(t, "flows/"+flowFile)
	for placeholder, rel := range map[string]string{
		"__AGENT__":        "agents/agent.sh",
		"__ASK_AGENT__":    "agents/ask_agent.sh",
		"__AUTO_AGENT__":   "agents/auto_agent.sh",
		"__BOARD_AGENT__":  "agents/board_agent.sh",
		"__BUDGET_AGENT__": "agents/budget_agent.sh",
		"__PLAN__":         "plans/plan.md",
	} {
		text = strings.ReplaceAll(text, placeholder, testdataAbs(t, rel))
	}
	fx.flowPath = filepath.Join(fx.root, "flow.yaml")
	writeFile(t, fx.flowPath, text+flowTail)
	writeFile(t, filepath.Join(fx.root, ".afm", "config.yaml"), "server:\n  open_browser: false\n")
	fx.env = []string{"AFM_IT_RUNS_LOG=" + fx.runsLog}
	return fx
}

// gateImplementation заставляет фейковых агентов держать реализацию, пока тест не вызовет release.
func (fx *modeFixture) gateImplementation() {
	fx.t.Helper()
	if err := os.MkdirAll(fx.releaseDir, 0o755); err != nil { //nolint:gosec // test fixture directory
		fx.t.Fatal(err)
	}
	fx.env = append(fx.env, "AFM_IT_RELEASE_DIR="+fx.releaseDir)
}

func testdataAbs(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(coreModeData, rel))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func readTestdata(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(coreModeData, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test fixture directory
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // test fixture file
		t.Fatal(err)
	}
}

// useAfmBinary собирает afm во временный каталог: фейковые агенты question/board вызывают `afm stage|agent`.
func (fx *modeFixture) useAfmBinary() {
	fx.t.Helper()
	bin := filepath.Join(fx.t.TempDir(), "afm")
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	out, err := exec.CommandContext(context.Background(), goBin, "build", "-o", bin, ".").CombinedOutput() //nolint:gosec // G204: the toolchain of this very test run
	if err != nil {
		fx.t.Fatalf("go build afm: %v\n%s", err, out)
	}
	fx.env = append(fx.env, "AFM_IT_BIN="+bin)
}

func (fx *modeFixture) args(extra ...string) []string {
	return append([]string{"run", fx.flowPath, "--dir", fx.root, "--mode", fx.mode, "--port", strconv.Itoa(fx.port)}, extra...)
}

func (fx *modeFixture) start(extra ...string) *afmChild {
	fx.t.Helper()
	child := startAfmChild(fx.t, fx.root, fx.env, fx.args(extra...)...)
	interruptOnCleanup(fx.t, child)
	return child
}

// interruptOnCleanup завершает ещё живой core через SIGINT и ждёт его выхода: это чистый путь, сохраняющий
// состояние рана. Kill из startAfmChild — аварийный, он лишь зачищает группы агентов. Регистрируется позже,
// поэтому выполняется раньше.
func interruptOnCleanup(t *testing.T, c *afmChild) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-c.done:
			return
		default:
		}
		_ = c.cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(loopTestTimeout):
			t.Errorf("core did not shut down after SIGINT; output:\n%s", c.out.String())
		}
	})
}

func (fx *modeFixture) base() string { return fmt.Sprintf("http://localhost:%d", fx.port) }

func (fx *modeFixture) runDir(runID string) string {
	return filepath.Join(fx.root, ".afm", "runs", runID)
}

// agentRuns — строки `<stage>:<phase>`, которые фейковые агенты дописали за всё время.
func (fx *modeFixture) agentRuns() []string {
	raw, err := os.ReadFile(fx.runsLog)
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// release открывает стадию, на которой фейковый агент ждёт файл в releaseDir.
func (fx *modeFixture) release(stage string) {
	fx.t.Helper()
	writeFile(fx.t, filepath.Join(fx.releaseDir, stage), "")
}

// call отправляет запрос с Origin своего порта; ответ — код, тело и заголовки.
func (fx *modeFixture) call(method, path, body string, header map[string]string) (int, string, http.Header) {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, fx.base()+path, strings.NewReader(body))
	if err != nil {
		fx.t.Fatal(err)
	}
	req.Header.Set("Origin", fx.base())
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fx.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fx.t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Header
}

// mustPost — POST, который обязан вернуть 200.
func (fx *modeFixture) mustPost(path, body string) string {
	fx.t.Helper()
	code, got, _ := fx.call(http.MethodPost, path, body, nil)
	if code != http.StatusOK {
		fx.t.Fatalf("POST %s = %d, want 200: %s", path, code, got)
	}
	return got
}

type stageInfo struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	PausedFrom string `json:"paused_from"`
}

type statusInfo struct {
	RunID        string          `json:"run_id"`
	RunStatus    string          `json:"run_status"`
	Stages       []stageInfo     `json:"stages"`
	Capabilities json.RawMessage `json:"capabilities"`
}

func (fx *modeFixture) status() statusInfo {
	fx.t.Helper()
	code, body, _ := fx.call(http.MethodGet, "/api/status", "", nil)
	if code != http.StatusOK {
		fx.t.Fatalf("GET /api/status = %d: %s", code, body)
	}
	var st statusInfo
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		fx.t.Fatalf("decode status: %v", err)
	}
	return st
}

func (st statusInfo) stage(id string) (stageInfo, bool) {
	for _, s := range st.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return stageInfo{}, false
}

// waitReady ждёт /api/runtime с ready=true и возвращает run_id; ребёнок за это время не должен выйти.
func (fx *modeFixture) waitReady(child *afmChild) string {
	fx.t.Helper()
	var runID string
	waitFor(fx.t, "/api/runtime ready", func() bool {
		select {
		case <-child.done:
			fx.t.Fatalf("child exited before ready: %v\n%s", child.err, child.out.String())
		default:
		}
		resp, err := http.Get(fx.base() + "/api/runtime") //nolint:gosec // loopback address of the test child
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var rt struct {
			Mode  string `json:"mode"`
			RunID string `json:"run_id"`
			Ready bool   `json:"ready"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rt) != nil || !rt.Ready {
			return false
		}
		if rt.Mode != fx.mode {
			fx.t.Fatalf("/api/runtime mode = %q, want %q", rt.Mode, fx.mode)
		}
		runID = rt.RunID
		return true
	})
	return runID
}

func (fx *modeFixture) waitStage(id, want string) {
	fx.t.Helper()
	waitFor(fx.t, fmt.Sprintf("stage %s to be %s", id, want), func() bool {
		s, _ := fx.status().stage(id)
		return s.Status == want
	})
}

func (fx *modeFixture) waitRunStatus(want string) {
	fx.t.Helper()
	waitFor(fx.t, "run_status "+want, func() bool { return fx.status().RunStatus == want })
}

// driveToFinish одобряет каждый план, который ждёт approval, пока ран не завершится.
func (fx *modeFixture) driveToFinish() {
	fx.t.Helper()
	waitFor(fx.t, "the run to finish", func() bool {
		st := fx.status()
		for _, s := range st.Stages {
			if s.Status == "awaiting_approval" {
				fx.call(http.MethodPost, "/api/stages/"+s.ID+"/approve", "{}", nil)
			}
		}
		return st.RunStatus == "finished"
	})
}

// transitions — журнал рана без seq и времени: именно такие последовательности должны совпасть между режимами.
func transitions(t *testing.T, runDir string) []string {
	t.Helper()
	var out []string
	for _, tr := range readEvents(t, runDir) {
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", tr.StageID, tr.From, tr.To, tr.Event))
	}
	return out
}

// runToFinish проходит весь сценарий в одном режиме и возвращает переходы и capabilities.
func runToFinish(t *testing.T, mode, flowFile, flowTail string) (seq []string, capabilities string) {
	t.Helper()
	fx := newModeFixture(t, mode, flowFile, flowTail)
	child := fx.start()
	runID := fx.waitReady(child)
	fx.driveToFinish()
	capabilities = string(fx.status().Capabilities)
	if mode == modeCore {
		child.waitOutput(t, holdingFinalState)
		if code := child.interrupt(t); code != 0 {
			t.Fatalf("core exit code after the final hold = %d, want 0\n%s", code, child.out.String())
		}
	}
	return transitions(t, fx.runDir(runID)), capabilities
}

// --- оба режима ---

func TestCoreMode_SameTransitionsInBothModes(t *testing.T) {
	for _, scenario := range []string{"auto", "manual", "preplanned", "script", "autonomous"} {
		t.Run(scenario, func(t *testing.T) {
			byMode := map[string][]string{}
			for _, mode := range bothModes {
				byMode[mode], _ = runToFinish(t, mode, scenario+".yaml", "")
			}
			if len(byMode[modeCore]) == 0 {
				t.Fatal("no transitions recorded")
			}
			if !slices.Equal(byMode[modeDashboard], byMode[modeCore]) {
				t.Errorf("transitions differ\ndashboard: %q\ncore:      %q", byMode[modeDashboard], byMode[modeCore])
			}
			if last := byMode[modeCore][len(byMode[modeCore])-1]; !strings.Contains(last, "run_finished") {
				t.Errorf("last transition = %q, want run_finished", last)
			}
		})
	}
}

func TestCoreMode_SameCapabilitiesInBothModes(t *testing.T) {
	got := map[string]string{}
	for _, mode := range bothModes {
		_, got[mode] = runToFinish(t, mode, "auto.yaml", "")
	}
	if got[modeCore] == "" || got[modeDashboard] != got[modeCore] {
		t.Errorf("capabilities differ\ndashboard: %s\ncore:      %s", got[modeDashboard], got[modeCore])
	}
}

// Observer-hook, упавший на flow_started, не меняет ни одного перехода.
func TestCoreMode_FailingObserverHookKeepsTransitions(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			want, _ := runToFinish(t, mode, "auto.yaml", "")

			marker := filepath.Join(t.TempDir(), "hook.ran")
			hook := fmt.Sprintf("\nhooks:\n  - id: failing\n    events: [flow_started]\n    command: \"touch %s; exit 1\"\n", marker)
			got, _ := runToFinish(t, mode, "auto.yaml", hook)

			if _, err := os.Stat(marker); err != nil {
				t.Errorf("the failing hook never ran: %v", err)
			}
			if !slices.Equal(want, got) {
				t.Errorf("transitions changed by a failing hook\nwithout: %q\nwith:    %q", want, got)
			}
		})
	}
}

// --- только core ---

// Вопрос и approval приходят только по HTTP: без человека ран стоит, а не одобряет себя сам.
func TestCoreMode_QuestionAndManualApprovalOverHTTP(t *testing.T) {
	fx := newModeFixture(t, modeCore, "question.yaml", "")
	fx.useAfmBinary()
	child := fx.start()
	runID := fx.waitReady(child)

	fx.waitStage("build", "awaiting_user_input")
	var dialog []struct {
		ID       string `json:"id"`
		Phase    string `json:"phase"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	code, body, _ := fx.call(http.MethodGet, "/api/stages/build/dialog", "", nil)
	if code != http.StatusOK || json.Unmarshal([]byte(body), &dialog) != nil {
		t.Fatalf("GET dialog = %d: %s", code, body)
	}
	if len(dialog) != 1 || dialog[0].Question != "which database?" || dialog[0].Answer != "" {
		t.Fatalf("dialog = %s, want one unanswered question", body)
	}
	q := dialog[0]

	fx.mustPost("/api/stages/build/dialog/answer",
		fmt.Sprintf(`{"id":%q,"phase":%q,"answer":"sqlite","from_options":true}`, q.ID, q.Phase))
	fx.waitStage("build", "awaiting_approval")
	time.Sleep(500 * time.Millisecond) // выдержка: одобрения от автоматики быть не должно
	if s, _ := fx.status().stage("build"); s.Status != "awaiting_approval" {
		t.Fatalf("stage left awaiting_approval without a human approval: %q", s.Status)
	}

	fx.mustPost("/api/stages/build/approve", "{}")
	fx.waitRunStatus("finished")
	if code := child.interrupt(t); code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, child.out.String())
	}
	if got := lastRunLevelEvent(t, fx.runDir(runID)); got != "run_finished" {
		t.Errorf("last run-level event = %q, want run_finished", got)
	}
}

// Failed-стадия не завершает ран: core держит API, и Retry запускает её заново.
func TestCoreMode_RetryFailedStage(t *testing.T) {
	fx := newModeFixture(t, modeCore, "script-failed.yaml", "")
	child := fx.start()
	runID := fx.waitReady(child)
	fx.waitStage("glue", "failed")

	fx.mustPost("/api/stages/glue/retry", "{}")
	waitFor(t, "the retry to fail again", func() bool {
		failures := 0
		for _, tr := range readEvents(t, fx.runDir(runID)) {
			if tr.StageID == "glue" && tr.To == state.StatusFailed {
				failures++
			}
		}
		return failures == 2
	})
	fx.waitStage("glue", "failed")
	select {
	case <-child.done:
		t.Fatalf("core exited with a failed stage held for Retry: %v\n%s", child.err, child.out.String())
	default:
	}
}

func TestCoreMode_PauseAndContinue(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	fx.gateImplementation()
	child := fx.start()
	fx.waitReady(child)
	// Стадия дошла до реализации и ждёт release: агент жив, пауза ставится поверх него.
	waitFor(t, "the implementation agent to start", func() bool { return slices.Contains(fx.agentRuns(), "build:implementation") })

	fx.mustPost("/api/stages/build/pause", "{}")
	fx.waitStage("build", "paused")
	fx.mustPost("/api/stages/build/continue", "{}")
	fx.waitStage("build", "running")

	fx.release("build")
	fx.waitRunStatus("finished")
}

// Стадия, добавленная через graph commit в идущем ране, исполняется как обычная.
func TestCoreMode_GraphCommitAddsAndRunsStage(t *testing.T) {
	fx := newModeFixture(t, modeCore, "autonomous.yaml", "")
	fx.gateImplementation()
	child := fx.start()
	fx.waitReady(child)
	waitFor(t, "the implementation agent to start", func() bool { return slices.Contains(fx.agentRuns(), "build:implementation") })

	fx.commitExtraStage()
	fx.waitStage("extra", "pending")
	fx.release("build")
	fx.release("extra")
	fx.waitStage("extra", "done")
	fx.waitRunStatus("finished")
}

// commitExtraStage добавляет стадию extra после build с командой build: других команд динамическая стадия не принимает.
func (fx *modeFixture) commitExtraStage() {
	fx.t.Helper()
	body, err := json.Marshal(map[string]any{
		"kind": "append", "id": "extra", "command": fx.agentPath, "prompt": "extra",
		"depends_on": []string{"build"}, "request_id": "commit-extra",
	})
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.mustPost("/api/run/graph/commit", string(body))
}

// Перезапуск над раном с открытым вопросом: тот же ран, вопрос на месте.
func TestCoreMode_RestartKeepsOpenQuestion(t *testing.T) {
	fx := newModeFixture(t, modeCore, "question.yaml", "")
	fx.useAfmBinary()
	child := fx.start()
	runID := fx.waitReady(child)
	fx.waitStage("build", "awaiting_user_input")
	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}

	again := fx.start()
	if got := fx.waitReady(again); got != runID {
		t.Fatalf("restart opened run %s, want the interrupted %s", got, runID)
	}
	fx.waitStage("build", "awaiting_user_input")
	code, body, _ := fx.call(http.MethodGet, "/api/stages/build/dialog", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "which database?") {
		t.Fatalf("dialog after restart = %d: %s", code, body)
	}
	if dirs := (&cycleFixture{t: t, root: fx.root}).runDirs(); !slices.Equal(dirs, []string{runID}) {
		t.Errorf("run dirs = %v, want only %s", dirs, runID)
	}
}

// Перезапуск над раном с закоммиченной, ещё не выполненной динамической стадией: стадия на месте и выполняется.
func TestCoreMode_RestartKeepsCommittedDynamicStage(t *testing.T) {
	fx := newModeFixture(t, modeCore, "autonomous.yaml", "")
	fx.gateImplementation()
	child := fx.start()
	runID := fx.waitReady(child)
	waitFor(t, "the implementation agent to start", func() bool { return slices.Contains(fx.agentRuns(), "build:implementation") })
	fx.commitExtraStage()
	fx.waitStage("extra", "pending")
	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}

	again := fx.start()
	if got := fx.waitReady(again); got != runID {
		t.Fatalf("restart opened run %s, want the interrupted %s", got, runID)
	}
	// Автовосстановление после прерывания идёт после ready: сначала стадия ещё failed от каскада прерванного рана.
	fx.waitStage("extra", "pending")
	fx.release("build")
	fx.release("extra")
	fx.waitStage("extra", "done")
	fx.waitRunStatus("finished")
}

// Занятый порт — отказ до запуска агентов, а run directory остаётся свободной для следующего запуска.
func TestCoreMode_BusyPortFailsBeforeAgentsStart(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", fx.port))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	child := fx.start()
	code := child.exitCode(t)
	if code == 0 {
		t.Fatalf("exit code = 0 with the port busy; output:\n%s", child.out.String())
	}
	if runs := fx.agentRuns(); len(runs) != 0 {
		t.Errorf("agents started despite the busy port: %v", runs)
	}
	dirs := (&cycleFixture{t: t, root: fx.root}).runDirs()
	if len(dirs) != 1 {
		t.Fatalf("run dirs = %v, want the one the failed start created", dirs)
	}
	if !strings.Contains(child.out.String(), "start API server") {
		t.Errorf("output does not name the API server start failure:\n%s", child.out.String())
	}
	for _, dir := range dirs {
		lock, err := implstate.TryLockRun(fx.runDir(dir))
		if err != nil {
			t.Fatalf("run lock of %s still held after exit: %v", dir, err)
		}
		_ = lock.Close()
	}
}

// Транспортные отказы core те же, что у dashboard: Origin, bearer агента и owner token.
func TestCoreMode_TransportRefusals(t *testing.T) {
	fx := newModeFixture(t, modeCore, "auto.yaml", "")
	child, ownerToken := startCoreWithOwnerToken(t, fx)
	fx.waitReady(child)
	fx.driveToFinish()

	t.Run("forbidden_origin", func(t *testing.T) {
		body := `{"request_id":"r1","key":"k","status":"s"}`
		code, got, _ := fx.call(http.MethodPost, "/api/mesh/board", body, map[string]string{"Origin": "http://evil.example"})
		if code != http.StatusForbidden || !strings.Contains(got, "forbidden_origin") {
			t.Errorf("foreign Origin = %d %s, want 403 forbidden_origin", code, got)
		}
	})
	t.Run("wrong_agent_bearer", func(t *testing.T) {
		code, got, _ := fx.call(http.MethodGet, "/api/agent-mesh/inbox", "", map[string]string{"Authorization": "Bearer not-the-token"})
		if code != http.StatusUnauthorized {
			t.Errorf("wrong bearer = %d %s, want 401", code, got)
		}
	})
	t.Run("owner_wait_without_token", func(t *testing.T) {
		code, got, header := fx.call(http.MethodGet, "/api/mesh/stages/build/waits", "", nil)
		if code != http.StatusUnauthorized || header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("no owner token = %d %s (WWW-Authenticate %q), want 401 Bearer", code, got, header.Get("WWW-Authenticate"))
		}
	})
	t.Run("owner_wait_with_token", func(t *testing.T) {
		code, got, _ := fx.call(http.MethodGet, "/api/mesh/stages/build/waits", "", map[string]string{"Authorization": "Bearer " + ownerToken})
		if code != http.StatusOK {
			t.Errorf("valid owner token = %d %s, want 200", code, got)
		}
	})
}

// startCoreWithOwnerToken запускает core с AFM_OWNER_TOKEN_FD и читает выданный токен. startAfmChild не умеет
// передавать дескрипторы, поэтому окружение и ожидание повторены здесь.
func startCoreWithOwnerToken(t *testing.T, fx *modeFixture) (*afmChild, string) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	raw, err := json.Marshal(fx.args())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0]) //nolint:gosec // G702: os.Args[0] here is the test binary itself
	cmd.Dir = fx.root
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"AFM_WORKTREES=0",
		"AFM_USE_DOCKER=0",
		"AFM_BROWSER=0",
		"AFM_SIDEAGENT=0",
		afmRunTestChildArgsEnv + "=" + string(raw),
		ownerTokenFDEnv + "=3", // первый ExtraFiles
	}, fx.env...)
	cmd.ExtraFiles = []*os.File{pw}
	c := &afmChild{cmd: cmd, out: &childOutput{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = c.out, c.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close() // у ребёнка своя копия; без этого чтение не увидит EOF после его выхода
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
		default:
			_ = cmd.Process.Kill()
			<-c.done
		}
	})

	interruptOnCleanup(t, c)

	tokenCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		tokenCh <- strings.TrimSpace(line)
	}()
	select {
	case token := <-tokenCh:
		if token == "" {
			t.Fatalf("no owner token delivered; output:\n%s", c.out.String())
		}
		return c, token
	case <-time.After(loopTestTimeout):
		t.Fatalf("owner token was not delivered; output:\n%s", c.out.String())
		return nil, ""
	}
}
