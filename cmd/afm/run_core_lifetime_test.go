package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
	"github.com/akopichin/afm/contracts/state"
	"github.com/gorilla/websocket"
)

// Время жизни core: после результата процесс держит API до SIGINT и выходит с кодом завершённого рана,
// repeat не переписывает этот исход, а Docker получает тот же режим и порт. Сигнал и коды выхода
// проверяются дочерним процессом (startAfmChild); агентов и LLM нет — стадии script и фейковые команды.

const holdingFinalState = "api: holding the final state until interrupted"

// coreFixture — afm-root с flow.yaml и свободным портом для дочернего `afm run --mode core`.
type coreFixture struct {
	*cycleFixture
	port int
}

func newCoreFixture(t *testing.T) *coreFixture {
	t.Helper()
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	return &coreFixture{cycleFixture: newExitOnFailFixture(t), port: port}
}

// writeProjectConfig пишет .afm/config.yaml afm-root.
func (fx *coreFixture) writeProjectConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(fx.root, config.AfmDir), 0o755); err != nil { //nolint:gosec // test fixture directory
		t.Fatal(err)
	}
	fx.write(filepath.Join(config.AfmDir, "config.yaml"), body)
}

func (fx *coreFixture) base() string { return fmt.Sprintf("http://localhost:%d", fx.port) }

// startCore запускает `afm run flow.yaml --mode core` дочерним процессом.
func (fx *coreFixture) startCore(t *testing.T, env []string, extraArgs ...string) *afmChild {
	t.Helper()
	args := append([]string{"run", fx.flowPath, "--dir", fx.root, "--mode", "core", "--port", strconv.Itoa(fx.port)}, extraArgs...)
	return startAfmChild(t, fx.root, env, args...)
}

// runtimeRunID читает run_id из /api/runtime, пока API ещё держится.
func (fx *coreFixture) runtimeRunID(t *testing.T) string {
	t.Helper()
	var rt struct {
		RunID string `json:"run_id"`
		Ready bool   `json:"ready"`
	}
	decodeDashboardJSON(t, fx.base()+"/api/runtime", &rt)
	if !rt.Ready || rt.RunID == "" {
		t.Fatalf("/api/runtime = %+v, want a ready run", rt)
	}
	return rt.RunID
}

func (fx *coreFixture) runDir(runID string) string {
	return filepath.Join(fx.root, ".afm", "runs", runID)
}

// dialWS подключается к /ws так же, как вкладка dashboard.
func dialWS(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	return conn
}

// requirePortFree проверяет, что процесс отпустил порт API.
func requirePortFree(t *testing.T, port int) {
	t.Helper()
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("port %d is still held after exit: %v", port, err)
	}
	_ = l.Close()
}

// backgroundFlow — executeFlow в горутине, которую владелец обязан остановить до восстановления os.Stdout,
// иначе startServer пишет в уже подменённый файл.
type backgroundFlow struct {
	finished chan struct{}
	err      error // результат executeFlow, виден после закрытия finished
}

func startBackgroundFlow(ctx context.Context, f *flow.Flow, cfg config.Config, opts runOptions) *backgroundFlow {
	b := &backgroundFlow{finished: make(chan struct{})}
	go func() {
		defer close(b.finished)
		_, b.err = executeFlow(ctx, f, cfg, nil, opts)
	}()
	return b
}

// stop отменяет контекст и ждёт возврата executeFlow; вызывай через defer, чтобы это шло и при Fatal.
func (b *backgroundFlow) stop(t *testing.T, cancel context.CancelFunc) {
	t.Helper()
	cancel()
	select {
	case <-b.finished:
	case <-time.After(loopTestTimeout):
		t.Error("executeFlow did not return after cancel")
	}
}

// eventsSoFar читает журнал, который ран ещё пишет: нет файла, пустой или оборванный хвост — не ошибка.
func eventsSoFar(runDir string) []state.Transition {
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return nil
	}
	var out []state.Transition
	for _, line := range strings.Split(string(data), "\n") {
		var tr state.Transition
		if json.Unmarshal([]byte(line), &tr) == nil {
			out = append(out, tr)
		}
	}
	return out
}

// lastRunLevelEvent возвращает последнее событие уровня рана (не стадии) из завершённого журнала.
func lastRunLevelEvent(t *testing.T, runDir string) string {
	t.Helper()
	readEvents(t, runDir) // журнал обязан читаться целиком
	return runLevelEventSoFar(runDir)
}

// runLevelEventSoFar — то же для журнала, который ран ещё пишет.
func runLevelEventSoFar(runDir string) string {
	last := ""
	for _, tr := range eventsSoFar(runDir) {
		switch tr.Event {
		case "run_started", "run_finished", "run_failed", "run_interrupted":
			last = tr.Event
		default:
		}
	}
	return last
}

// --- core держит результат до отмены ---

// Удержание после результата не зависит от WS и таймера: подмена drainDashboard мгновенным вызовом не даёт
// executeFlow вернуться, а подключение и отключение клиента ничего не меняет.
func TestExecuteFlow_CoreHoldsUntilContextCancel(t *testing.T) {
	fx := newExitOnFailFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Server: config.ServerConfig{Port: &port}}

	var drains atomic.Int32
	prevDrain := drainDashboard
	drainDashboard = func(context.Context, func() int) { drains.Add(1) }
	t.Cleanup(func() { drainDashboard = prevDrain })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout string
	stdout, _ = captureOutput(t, func() {
		flowRun := startBackgroundFlow(ctx, f, cfg, runOptions{launch: launchCore, runName: f.Name})
		defer flowRun.stop(t, cancel)
		base := fmt.Sprintf("http://localhost:%d", port)
		waitFor(t, "the run to finish", func() bool {
			dirs := fx.runDirs()
			return len(dirs) == 1 && runLevelEventSoFar(filepath.Join(fx.root, ".afm", "runs", dirs[0])) == "run_finished"
		})

		// Клиент пришёл и ушёл: у dashboard это сдвигало бы выход, у core API просто остаётся.
		ws := dialWS(t, base)
		_ = ws.Close()
		time.Sleep(300 * time.Millisecond)
		select {
		case <-flowRun.finished:
			t.Fatalf("executeFlow returned %v before the owner interrupted", flowRun.err)
		default:
		}
		if code, _ := getBody(t, base+"/api/runtime"); code != http.StatusOK {
			t.Errorf("GET /api/runtime during the hold = %d, want 200", code)
		}

		flowRun.stop(t, cancel)
		select {
		case <-flowRun.finished:
			if flowRun.err != nil {
				t.Errorf("executeFlow after the owner's interrupt = %v, want nil (exit 0)", flowRun.err)
			}
		default:
		}
	})

	if !strings.Contains(stdout, holdingFinalState) {
		t.Errorf("stdout lacks the hold line: %q", stdout)
	}
	if got := drains.Load(); got != 0 {
		t.Errorf("drainDashboard called %d times in core, want 0: the hold has no timer or WS counter", got)
	}
}

// --exit-on-fail выходит сразу: провал — исход, а не результат, который клиенту нужно успеть прочитать.
func TestExecuteFlow_CoreExitOnFailDoesNotHold(t *testing.T) {
	fx := newExitOnFailFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7"}))
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Server: config.ServerConfig{Port: &port}}

	var drains atomic.Int32
	prevDrain := drainDashboard
	drainDashboard = func(context.Context, func() int) { drains.Add(1) }
	t.Cleanup(func() { drainDashboard = prevDrain })

	var runErr error
	stdout, _ := captureOutput(t, func() {
		_, runErr = executeFlow(context.Background(), f, cfg, nil, runOptions{launch: launchCore, exitOnFail: true, runName: f.Name})
	})

	var exitErr *ExitError
	if !errors.As(runErr, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("executeFlow = %v, want ExitError{Code: 1}", runErr)
	}
	if drains.Load() != 0 || strings.Contains(stdout, "holding") {
		t.Errorf("a failed run was held: drains=%d stdout=%q", drains.Load(), stdout)
	}
}

func TestCoreChild_ExitOnFailExitsOneWithoutHold(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7"}))

	child := fx.startCore(t, nil, "--exit-on-fail")

	if code := child.exitCode(t); code != 1 {
		t.Fatalf("exit code = %d, want 1; output:\n%s", code, child.out.String())
	}
	if strings.Contains(child.out.String(), "holding") {
		t.Errorf("--exit-on-fail held the API: %s", child.out.String())
	}
	requirePortFree(t, fx.port)
}

// SIGINT в удержании после успеха — штатный конец: код 0, порт свободен, журнал не переписан в interrupted.
func TestCoreChild_InterruptInFinalHoldExitsZero(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))

	child := fx.startCore(t, nil)
	child.waitOutput(t, holdingFinalState)
	runID := fx.runtimeRunID(t)

	if code := child.interrupt(t); code != 0 {
		t.Fatalf("exit code after SIGINT in the hold = %d, want 0; output:\n%s", code, child.out.String())
	}
	if got := lastRunLevelEvent(t, fx.runDir(runID)); got != "run_finished" {
		t.Errorf("last run-level event = %q, want run_finished", got)
	}
	requirePortFree(t, fx.port)
}

// Ход Assistant, активный в момент SIGINT, доводится до конца shutdown'а раньше закрытия Store: после выхода
// run directory свободна, итог рана остался run_finished, процесс Assistant завершён.
func TestCoreChild_InterruptDrainsActiveAssistantTurn(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
	pidFile := filepath.Join(t.TempDir(), "assistant.pid")
	// exec: pid в файле — pid самого блокирующегося процесса, а не оболочки над ним.
	assistant := fx.write("assistant.sh", "#!/bin/sh\necho $$ > "+pidFile+"\nexec sleep 600\n")
	if err := os.Chmod(assistant, 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	fx.writeProjectConfig(t, "client:\n  command: "+assistant+"\n")

	child := fx.startCore(t, []string{"AFM_SIDEAGENT=1"})
	child.waitOutput(t, holdingFinalState)
	runID := fx.runtimeRunID(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, fx.base()+"/api/side-agent/messages",
		strings.NewReader(`{"client_message_id":"c19","text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", fx.base())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/side-agent/messages = %d, want 202", resp.StatusCode)
	}
	var pid int
	waitFor(t, "the Assistant turn to start its process", func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if code := child.interrupt(t); code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, child.out.String())
	}
	lock, err := implstate.TryLockRun(fx.runDir(runID))
	if err != nil {
		t.Fatalf("run lock still held after exit: Store was not closed: %v", err)
	}
	_ = lock.Close()
	if got := lastRunLevelEvent(t, fx.runDir(runID)); got != "run_finished" {
		t.Errorf("last run-level event = %q, want run_finished", got)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Assistant process %d is still alive after exit (kill -0: %v)", pid, err)
	}
}

// --- repeat в core ---

const interCycleHold = "api: holding 5s before the next cycle"

// Два цикла на одном порту: между ними API держится без WS, затем следующий ран; финальное удержание и SIGINT
// дают 0, а keep_runs оставляет только последний ран.
func TestCoreChild_RepeatTwoCyclesThenFinalHold(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2, keep_runs: 1}"}))

	child := fx.startCore(t, nil)
	child.waitOutput(t, interCycleHold)
	first := fx.runtimeRunID(t)
	child.waitOutput(t, holdingFinalState)
	second := fx.runtimeRunID(t)
	if first == second {
		t.Fatalf("both cycles served run %s, want two runs on one port", first)
	}

	if code := child.interrupt(t); code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, child.out.String())
	}
	if dirs := fx.runDirs(); !slices.Equal(dirs, []string{second}) {
		t.Errorf("run dirs = %v, want only the last run %s (keep_runs: 1)", dirs, second)
	}
	requirePortFree(t, fx.port)
}

func TestCoreChild_InterruptBetweenCyclesExits130(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3}"}))

	child := fx.startCore(t, nil)
	child.waitOutput(t, interCycleHold)

	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d; output:\n%s", code, interruptedExitCode, child.out.String())
	}
}

// flow.yaml, сломанный к последнему циклу, — ошибка, которую удержание не прячет: SIGINT даёт не 0 и не 130.
func TestCoreChild_BrokenFlowAtLastCycleIsAnErrorAfterTheHold(t *testing.T) {
	fx := newCoreFixture(t)
	fx.write("next.yaml", "name: rp\nstages: [unclosed")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 0}", script: fx.swapFlowScript()}))

	child := fx.startCore(t, nil)
	child.waitOutput(t, holdingFinalState)

	code := child.interrupt(t)
	if code == 0 || code == interruptedExitCode {
		t.Fatalf("exit code = %d, want an error (neither 0 nor %d); output:\n%s", code, interruptedExitCode, child.out.String())
	}
	if !strings.Contains(child.out.String(), "parse flow") {
		t.Errorf("output does not name the broken flow.yaml:\n%s", child.out.String())
	}
}

// SIGINT приходит после успешного Wait, но до ранней оценки afterRun: отмена не должна давать пустому решению
// дойти до следующего цикла (паника), а исход core с продолжением repeat — прерывание, а не удержание.
func TestCycleLoop_CancelBeforeEarlyDecision(t *testing.T) {
	cases := []struct {
		name       string
		launch     launchMode
		repeat     string
		want130    bool
		wantCycles int32
	}{
		{"core, more cycles pending", launchCore, "{max_cycles: 3}", true, 1},
		{"core, last cycle", launchCore, "{max_cycles: 1}", false, 1},
		{"dashboard, more cycles pending", launchDashboard, "{max_cycles: 3}", true, 1},
		{"dashboard, last cycle", launchDashboard, "{max_cycles: 1}", true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: tc.repeat}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			prev := executeCycle
			executeCycle = func(_ context.Context, _ *flow.Flow, _ config.Config, _ []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
				calls.Add(1)
				cancel()
				opts.afterRun()
				return cycleResult{Completed: true}, nil
			}
			t.Cleanup(func() { executeCycle = prev })

			f, err := flow.ParseFile(fx.flowPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg := testCycleConfig()
			l := fx.loop(cfg)
			l.launch = tc.launch
			var runErr error
			captureOutput(t, func() {
				_, runErr = l.run(ctx, f, combineRunHooks(cfg, f))
			})

			var exitErr *ExitError
			switch {
			case tc.want130 && (!errors.As(runErr, &exitErr) || exitErr.Code != interruptedExitCode):
				t.Fatalf("run = %v, want ExitError{Code: %d}", runErr, interruptedExitCode)
			case !tc.want130 && runErr != nil:
				t.Fatalf("run = %v, want nil (the final result outranks the interrupt)", runErr)
			default:
			}
			if got := calls.Load(); got != tc.wantCycles {
				t.Errorf("executeCycle called %d times, want %d", got, tc.wantCycles)
			}
		})
	}
}

// --- Docker ---

// Core едет в контейнер тем же argv с портом, опубликованным на loopback хоста; вкладку хоста открывает только
// dashboard.
func TestDockerReExec_CarriesModeAndPort(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		wantOpener int
	}{
		{"core", 0},
		{"dashboard", 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			fx := &coreFixture{cycleFixture: newExitOnFailFixture(t)}
			// Хост, а не контейнер: re-exec случается только на хосте, даже если сам набор тестов идёт в Docker.
			t.Setenv("AFM_IN_DOCKER", "")
			t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
			t.Setenv("AFM_USE_DOCKER", "1")
			t.Setenv("ANTHROPIC_API_KEY", "test-key") // preflight Docker-режима требует auth для claude
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture: an executable stub
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			port, err := pickFreeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
			fx.writeProjectConfig(t, "server:\n  open_browser: true\n")

			var argv []string
			docker.SetExecFunc(func(_ string, got []string, _ []string) error {
				argv = got
				return nil
			})
			t.Cleanup(docker.ResetExecFunc)
			var openedPorts []int
			prevOpener := hostBrowserOpener
			hostBrowserOpener = func(p int) { openedPorts = append(openedPorts, p) }
			t.Cleanup(func() { hostBrowserOpener = prevOpener })

			args := []string{"run", fx.flowPath, "--dir", fx.root, "--mode", tc.mode, "--port", strconv.Itoa(port)}
			prevArgs := os.Args
			os.Args = append([]string{"afm"}, args...)
			t.Cleanup(func() { os.Args = prevArgs })

			var code int
			captureOutput(t, func() { code = runMain(args) })

			if code != 0 {
				t.Fatalf("runMain = %d, want 0", code)
			}
			joined := strings.Join(argv, " ")
			for _, want := range []string{"--mode " + tc.mode, fmt.Sprintf("-p 127.0.0.1:%d:%d", port, port)} {
				if !strings.Contains(joined, want) {
					t.Errorf("docker argv lacks %q: %v", want, argv)
				}
			}
			if len(openedPorts) != tc.wantOpener || (tc.wantOpener == 1 && openedPorts[0] != port) {
				t.Errorf("host browser opener calls = %v, want %d call(s) for port %d", openedPorts, tc.wantOpener, port)
			}
		})
	}
}
