package main

// Цикл repeat (S3.T1): проход флоу повторяется только после успешного рана, flow.yaml перечитывается перед
// каждым новым циклом, а несовместимые правки останавливают процесс до создания run dir. Тесты гоняют настоящий
// executeFlow на script-стадиях — без агентов и без Docker.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/state"
)

const loopTestTimeout = 60 * time.Second

// cycleFixture — изолированный afm-root: flow.yaml, журнал hook-событий и счётчик запусков стадии.
type cycleFixture struct {
	t        *testing.T
	root     string
	flowPath string
	hookLog  string
	tickLog  string
	created  []string // run IDs, которые вернул последний fx.run
	// forceNewRun — как clear-флаги CLI: первый цикл не продолжает найденный ран.
	forceNewRun bool
}

func newCycleFixture(t *testing.T) *cycleFixture {
	t.Helper()
	root := t.TempDir()
	prev := rootDir
	rootDir = root
	t.Cleanup(func() { rootDir = prev })
	t.Setenv("AFM_SIDEAGENT", "0")
	t.Setenv("AFM_IN_DOCKER", "")
	return &cycleFixture{
		t:        t,
		root:     root,
		flowPath: filepath.Join(root, "flow.yaml"),
		hookLog:  filepath.Join(root, "hooks.jsonl"),
		tickLog:  filepath.Join(root, "ticks.txt"),
	}
}

// flowSpec описывает тестовый флоу из одной script-стадии tick и flow-хука, пишущего payload в hookLog.
type flowSpec struct {
	name        string // по умолчанию "rp"
	repeat      string // значение после "repeat:"; "" — ключ не пишется
	rootDir     string
	script      string // по умолчанию — дописать строку в tickLog
	hookEnv     string // YAML-значение env у хука: `{TOKEN: "env:X"}`; "" — без env
	hookExtra   string // shell-хвост команды хука, например "; printenv TOKEN >> log"
	extraStages string // сырые YAML-элементы списка stages
}

func (fx *cycleFixture) flowYAML(s flowSpec) string {
	var b strings.Builder
	name := s.name
	if name == "" {
		name = "rp"
	}
	fmt.Fprintf(&b, "name: %s\n", name)
	if s.repeat != "" {
		fmt.Fprintf(&b, "repeat: %s\n", s.repeat)
	}
	if s.rootDir != "" {
		fmt.Fprintf(&b, "root_dir: %s\n", s.rootDir)
	}
	b.WriteString("hooks:\n  - id: cycle-log\n    events: [flow_started, flow_resumed, flow_repeated, flow_finished, flow_failed, flow_interrupted]\n")
	fmt.Fprintf(&b, "    command: \"{ cat; echo; } >> %s%s\"\n", fx.hookLog, s.hookExtra)
	if s.hookEnv != "" {
		fmt.Fprintf(&b, "    env: %s\n", s.hookEnv)
	}
	script := s.script
	if script == "" {
		script = "echo tick >> " + fx.tickLog
	}
	b.WriteString("stages:\n  - id: tick\n    script: |\n")
	for _, line := range strings.Split(script, "\n") {
		fmt.Fprintf(&b, "      %s\n", line)
	}
	b.WriteString(s.extraStages)
	return b.String()
}

func (fx *cycleFixture) write(name, body string) string {
	fx.t.Helper()
	p := filepath.Join(fx.root, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		fx.t.Fatal(err)
	}
	return p
}

// replaceFlowScript — скрипт стадии tick, подменяющий flow.yaml содержимым next.yaml.
func (fx *cycleFixture) swapFlowScript() string {
	return fmt.Sprintf("echo tick >> %s\ncp %s %s", fx.tickLog, filepath.Join(fx.root, "next.yaml"), fx.flowPath)
}

func testCycleConfig() config.Config {
	noDashboard := 0
	return config.Config{Server: config.ServerConfig{Port: &noDashboard}}
}

func (fx *cycleFixture) loop(cfg config.Config) cycleLoop {
	return cycleLoop{flowPath: fx.flowPath, cfg: cfg, forceNewRun: fx.forceNewRun}
}

func (fx *cycleFixture) run(ctx context.Context, cfg config.Config) error {
	fx.t.Helper()
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		fx.t.Fatal(err)
	}
	created, err := fx.loop(cfg).run(ctx, f, combineRunHooks(cfg, f))
	fx.created = created
	return err
}

// runWithTimeout защищает тест от зависшего цикла.
func (fx *cycleFixture) runWithTimeout(cfg config.Config) error {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	return fx.run(ctx, cfg)
}

func (fx *cycleFixture) payloads() []lifecyclehooks.Payload {
	fx.t.Helper()
	data, err := os.ReadFile(fx.hookLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		fx.t.Fatal(err)
	}
	var out []lifecyclehooks.Payload
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var p lifecyclehooks.Payload
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			fx.t.Fatalf("hook payload %q: %v", line, err)
		}
		out = append(out, p)
	}
	return out
}

func (fx *cycleFixture) eventTypes() []string {
	var types []string
	for _, p := range fx.payloads() {
		types = append(types, string(p.Event))
	}
	return types
}

func (fx *cycleFixture) runDirs() []string {
	fx.t.Helper()
	entries, err := os.ReadDir(filepath.Join(fx.root, ".afm", "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		fx.t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

func (fx *cycleFixture) runState(runID string) state.RunState {
	fx.t.Helper()
	rs, err := state.LoadRunState(filepath.Join(fx.root, ".afm", "runs", runID))
	if err != nil {
		fx.t.Fatal(err)
	}
	return rs
}

func (fx *cycleFixture) ticks() int {
	data, err := os.ReadFile(fx.tickLog)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func (fx *cycleFixture) startRunIDs() []string {
	var ids []string
	for _, p := range fx.payloads() {
		switch p.Event {
		case lifecyclehooks.EventFlowStarted, lifecyclehooks.EventFlowResumed, lifecyclehooks.EventFlowRepeated:
			ids = append(ids, p.Flow.RunID)
		default:
		}
	}
	return ids
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(loopTestTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func wantEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestRepeatLoop_ThreeCyclesDistinctRuns(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3, keep_runs: 0}"}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatalf("loop: %v", err)
	}

	wantEvents(t, fx.eventTypes(),
		"flow_started", "flow_finished",
		"flow_repeated", "flow_finished",
		"flow_repeated", "flow_finished")
	ids := fx.startRunIDs()
	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("run IDs = %v, want three distinct", ids)
	}
	payloads := fx.payloads()
	for i, p := range payloads {
		cycle := i/2 + 1
		if p.Flow.Cycle != cycle || p.Flow.MaxCycles == nil || *p.Flow.MaxCycles != 3 {
			t.Errorf("payload %d (%s): cycle=%d max_cycles=%v, want cycle %d max_cycles 3", i, p.Event, p.Flow.Cycle, p.Flow.MaxCycles, cycle)
		}
	}
	if payloads[2].Flow.PreviousRunID != ids[0] || payloads[4].Flow.PreviousRunID != ids[1] || payloads[0].Flow.PreviousRunID != "" {
		t.Errorf("previous_run_id chain wrong: %q %q %q", payloads[0].Flow.PreviousRunID, payloads[2].Flow.PreviousRunID, payloads[4].Flow.PreviousRunID)
	}
	if got := len(fx.runDirs()); got != 3 {
		t.Fatalf("run dirs = %d, want 3 (no fourth cycle)", got)
	}
	if strings.Join(fx.created, ",") != strings.Join(ids, ",") {
		t.Errorf("created runs = %v, want the three cycle runs %v in order", fx.created, ids)
	}
	for _, id := range ids {
		if rs := fx.runState(id); rs.RunStatus != state.RunStatusFinished || !rs.AllDone() {
			t.Errorf("run %s: status %q all_done=%v, want finished and done", id, rs.RunStatus, rs.AllDone())
		}
	}
	if got := fx.ticks(); got != 3 {
		t.Errorf("stage ran %d times, want 3", got)
	}
}

func TestRepeatLoop_WithoutRepeatRunsOnce(t *testing.T) {
	for name, repeat := range map[string]string{"missing": "", "false": "false"} {
		t.Run(name, func(t *testing.T) {
			fx := newCycleFixture(t)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: repeat}))
			if err := fx.runWithTimeout(testCycleConfig()); err != nil {
				t.Fatalf("loop: %v", err)
			}
			wantEvents(t, fx.eventTypes(), "flow_started", "flow_finished")
			if p := fx.payloads()[0]; p.Flow.Cycle != 0 || p.Flow.MaxCycles != nil {
				t.Errorf("non-repeat payload carries repeat fields: %+v", p.Flow)
			}
			if got := len(fx.runDirs()); got != 1 {
				t.Fatalf("run dirs = %d, want 1", got)
			}
		})
	}
}

// failingSecondStage — флоу, второй стадии которого не удаётся создать каталог: ран падает сразу, без трёх
// повторов script-стадии с паузами 1/2/3 с.
func (fx *cycleFixture) failingSecondStage(repeat string) string {
	return fx.flowYAML(flowSpec{
		repeat:      repeat,
		script:      "echo tick >> " + fx.tickLog + "\ntouch \"$(dirname \"$AFM_STAGE_DIR\")/broken\"",
		extraStages: "  - id: broken\n    script: \"true\"\n    depends_on: [tick]\n",
	})
}

func TestRepeatLoop_FailedCycleStopsWithError(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.failingSecondStage("true"))

	err := fx.runWithTimeout(testCycleConfig())
	if err == nil {
		t.Fatal("failed cycle in repeat mode must give a non-zero exit")
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == interruptedExitCode {
		t.Fatalf("a failed cycle is not an interruption: %v", err)
	}
	wantEvents(t, fx.eventTypes(), "flow_started", "flow_failed")
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1 (a failed cycle is not repeated)", got)
	}
}

func TestRepeatLoop_FailedRunWithoutRepeatKeepsExitSemantics(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.failingSecondStage(""))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatalf("a failed stage outside repeat must keep the old exit code 0, got %v", err)
	}
	wantEvents(t, fx.eventTypes(), "flow_started", "flow_failed")
}

// resumableScript держит стадию на месте, пока нет файла-маркера: первый процесс прерывается посреди рана, а
// продолженный ран (граф и скрипт заморожены в его registry) проходит сразу.
func (fx *cycleFixture) resumableScript() string {
	return fmt.Sprintf("echo tick >> %s\n[ -f %s ] || exec sleep 60", fx.tickLog, filepath.Join(fx.root, "go"))
}

func (fx *cycleFixture) interruptFirstRun() string {
	fx.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fx.run(ctx, testCycleConfig()) }()
	waitFor(fx.t, "stage start", func() bool { return fx.ticks() == 1 })
	cancel()
	<-done
	ids := fx.startRunIDs()
	if len(ids) != 1 {
		fx.t.Fatalf("first process runs = %v, want exactly one", ids)
	}
	fx.write("go", "")
	return ids[0]
}

func TestRepeatLoop_ResumesInterruptedRunOnFirstEntryOnly(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", script: fx.resumableScript()}))
	interrupted := fx.interruptFirstRun()

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	wantEvents(t, fx.eventTypes(),
		"flow_started", "flow_interrupted",
		"flow_resumed", "flow_finished",
		"flow_repeated", "flow_finished")
	ids := fx.startRunIDs()
	if len(ids) != 3 || ids[1] != interrupted || ids[2] == interrupted {
		t.Fatalf("run IDs = %v, want the interrupted run resumed first and a new run second", ids)
	}
	if got := len(fx.runDirs()); got != 2 {
		t.Fatalf("run dirs = %d, want 2", got)
	}
	if len(fx.created) != 1 || fx.created[0] != ids[2] {
		t.Fatalf("created runs = %v, want only the new cycle run %s (a resumed run is not ours to prune)", fx.created, ids[2])
	}
	if p := fx.payloads()[2]; p.Flow.Cycle != 1 || p.Flow.PreviousRunID != "" {
		t.Errorf("resumed run counts as cycle 1 without previous run, got cycle=%d previous=%q", p.Flow.Cycle, p.Flow.PreviousRunID)
	}
}

func TestRepeatLoop_ClearFlagStartsNewFirstRun(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 1}", script: fx.resumableScript()}))
	interrupted := fx.interruptFirstRun()
	fx.forceNewRun = true

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	wantEvents(t, fx.eventTypes(), "flow_started", "flow_interrupted", "flow_started", "flow_finished")
	if ids := fx.startRunIDs(); ids[1] == interrupted {
		t.Fatalf("clear flag must start a new run, resumed %s instead", interrupted)
	}
	if got := len(fx.runDirs()); got != 2 {
		t.Fatalf("run dirs = %d, want 2", got)
	}
}

func TestRepeatLoop_InterruptedCycleDoesNotRepeat(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true", script: "echo tick >> " + fx.tickLog + "\nexec sleep 60"}))

	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fx.run(ctx, testCycleConfig()) }()
	waitFor(t, "stage start", func() bool { return fx.ticks() == 1 })
	cancel()

	var exitErr *ExitError
	if err := <-done; !errors.As(err, &exitErr) || exitErr.Code != interruptedExitCode {
		t.Fatalf("interrupted cycle error = %v, want exit code %d", err, interruptedExitCode)
	}
	wantEvents(t, fx.eventTypes(), "flow_started", "flow_interrupted")
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1", got)
	}
}

func TestRepeatLoop_CancelDuringDrainDoesNotCreateRun(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true"}))
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCycleConfig()
	cfg.Server.Port = &port
	prevGrace := interCycleDrainGrace
	interCycleDrainGrace = time.Minute
	t.Cleanup(func() { interCycleDrainGrace = prevGrace })

	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fx.run(ctx, cfg) }()
	waitFor(t, "first cycle to finish", func() bool { return len(fx.eventTypes()) == 2 })
	cancel()

	var exitErr *ExitError
	if err := <-done; !errors.As(err, &exitErr) || exitErr.Code != interruptedExitCode {
		t.Fatalf("cancel during drain error = %v, want exit code %d", err, interruptedExitCode)
	}
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1: Ctrl+C before the next cycle must not leave an empty run", got)
	}
}

func TestRepeatLoop_DashboardOpensTabOncePerProcess(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}"}))
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	prevGrace := interCycleDrainGrace
	interCycleDrainGrace = 10 * time.Millisecond
	t.Cleanup(func() { interCycleDrainGrace = prevGrace })
	// Финальная пауза dashboard (5 с) после последнего цикла тесту не нужна; настоящий drainDashboard
	// тест не ждёт, в отличие от отмены контекста, после фикса S3.T1 означающей Ctrl+C.
	prevDrain := drainDashboard
	drainDashboard = func(context.Context, func() int) {}
	t.Cleanup(func() { drainDashboard = prevDrain })

	t.Run("open_browser", func(t *testing.T) {
		cfg := testCycleConfig()
		cfg.Server.Port = &port
		openTab := true
		cfg.Server.OpenBrowser = &openTab
		var opened atomic.Int32
		prevOpen := openBrowser
		openBrowser = func(string) { opened.Add(1) }
		t.Cleanup(func() { openBrowser = prevOpen })

		if err := fx.runWithTimeout(cfg); err != nil {
			t.Fatal(err)
		}
		if got := opened.Load(); got != 1 {
			t.Fatalf("browser opened %d times over 2 cycles, want 1", got)
		}
	})

	t.Run("hint", func(t *testing.T) {
		fx := newCycleFixture(t)
		fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}"}))
		cfg := testCycleConfig()
		cfg.Server.Port = &port
		out := captureStdout(t, func() {
			if err := fx.runWithTimeout(cfg); err != nil {
				t.Error(err)
			}
		})
		if got := strings.Count(out, "open this URL in your browser"); got != 1 {
			t.Fatalf("URL hint printed %d times over 2 cycles, want 1\n%s", got, out)
		}
	})
}

func TestRepeatLoop_ReloadAppliesNewStages(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", script: fx.swapFlowScript()}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", extraStages: "  - id: added\n    script: \"true\"\n    depends_on: [tick]\n"}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	wantEvents(t, fx.eventTypes(), "flow_started", "flow_finished", "flow_repeated", "flow_finished")
	ids := fx.startRunIDs()
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("run IDs = %v, want two distinct", ids)
	}
	if _, ok := fx.runState(ids[0]).Stages["added"]; ok {
		t.Error("first run must not contain the stage added between cycles")
	}
	second := fx.runState(ids[1])
	if st, ok := second.Stages["added"]; !ok || st.Status != state.StatusDone {
		t.Errorf("second run stage 'added' = %+v (present=%v), want done", st, ok)
	}
}

func TestRepeatLoop_ReloadAppliesNewHook(t *testing.T) {
	fx := newCycleFixture(t)
	marker := filepath.Join(fx.root, "hook-version.txt")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", script: fx.swapFlowScript(), hookExtra: "; echo old >> " + marker}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", hookExtra: "; echo new >> " + marker}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	// Каждый цикл доставляет хуку два события: старт и финиш.
	if got, want := strings.Fields(string(data)), []string{"old", "old", "new", "new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hook version per delivery = %v, want %v: cycle 2 must run the reloaded hook", got, want)
	}
}

func TestRepeatLoop_ReloadRepeatDisabledStopsAfterCurrentCycle(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true", script: fx.swapFlowScript()}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "false"}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, fx.eventTypes(), "flow_started", "flow_finished")
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1", got)
	}
}

func TestRepeatLoop_ReloadedMaxCyclesAlreadyReachedStops(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true", script: fx.swapFlowScript()}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 1}"}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1: the reloaded limit is already reached", got)
	}
}

func TestRepeatLoop_LimitReachedDoesNotReadFlow(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 1}", script: fx.swapFlowScript()}))
	fx.write("next.yaml", "name: [broken\n")

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatalf("an invalid edit after the final cycle must not be read: %v", err)
	}
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1", got)
	}
}

func TestRepeatLoop_ReloadFailureStopsBeforeNewRun(t *testing.T) {
	invalidVerify := "name: rp\nrepeat: true\nstages:\n  - id: tick\n    agents: [planning, implementation]\n    verify:\n      command: mytextonly\n"
	tests := []struct {
		name    string
		next    string
		wantErr string
	}{
		{"invalid yaml", "name: [broken\n", "parse flow"},
		{"invalid verify", invalidVerify, "verify"},
		{"flow name changed", "", "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true", script: fx.swapFlowScript()}))
			next := tt.next
			if next == "" {
				next = fx.flowYAML(flowSpec{name: "renamed", repeat: "true"})
			}
			fx.write("next.yaml", next)
			cfg := testCycleConfig()
			cfg.Docker.Agents = map[string]config.AgentRecipe{
				"mytextonly": {Type: config.RecipeTypeOpenAI, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
			}

			err := fx.runWithTimeout(cfg)
			if err == nil {
				t.Fatal("reload failure must end the command with an error")
			}
			for _, want := range []string{"cycle 2", "1 completed", tt.wantErr} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if got := len(fx.runDirs()); got != 1 {
				t.Fatalf("run dirs = %d, want 1: the error must come before the new run dir", got)
			}
		})
	}
}

func TestRepeatLoop_RootDirChangeAppliesToNextRun(t *testing.T) {
	fx := newCycleFixture(t)
	for _, dir := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(fx.root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pwdLog := filepath.Join(fx.root, "pwd.txt")
	script := "pwd -P >> " + pwdLog + "\ncp " + filepath.Join(fx.root, "next.yaml") + " " + fx.flowPath
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", rootDir: "first", script: script}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 2}", rootDir: "second", script: script}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pwdLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	root, _ := filepath.EvalSymlinks(fx.root)
	want := []string{filepath.Join(root, "first"), filepath.Join(root, "second")}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("stage working directories = %v, want %v", lines, want)
	}
}

func TestCycleConfig_FlowMaxParallelDoesNotLeakAcrossCycles(t *testing.T) {
	base := config.Config{Executor: config.ExecutorConfig{MaxParallel: 5}}

	withFlow := cycleConfig(base, &flow.Flow{MaxParallel: 2}, false)
	if withFlow.Executor.MaxParallel != 2 {
		t.Fatalf("flow max_parallel: got %d, want 2", withFlow.Executor.MaxParallel)
	}
	if got := cycleConfig(base, &flow.Flow{}, false).Executor.MaxParallel; got != 5 {
		t.Fatalf("flow max_parallel removed: got %d, want the config value 5", got)
	}
	if got := cycleConfig(base, &flow.Flow{MaxParallel: 2}, true).Executor.MaxParallel; got != 5 {
		t.Fatalf("--max-parallel must outrank the flow: got %d, want 5", got)
	}
	if base.Executor.MaxParallel != 5 {
		t.Fatalf("base config mutated: %d", base.Executor.MaxParallel)
	}
}

func TestRepeatContinues(t *testing.T) {
	tests := []struct {
		name      string
		repeat    flow.RepeatConfig
		completed int
		want      bool
	}{
		{"disabled", flow.RepeatConfig{}, 1, false},
		{"infinite", flow.RepeatConfig{Enabled: true}, 100, true},
		{"below limit", flow.RepeatConfig{Enabled: true, MaxCycles: 3}, 2, true},
		{"limit reached", flow.RepeatConfig{Enabled: true, MaxCycles: 3}, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repeatContinues(tt.repeat, tt.completed); got != tt.want {
				t.Fatalf("repeatContinues(%+v, %d) = %v, want %v", tt.repeat, tt.completed, got, tt.want)
			}
		})
	}
}

// --- reload compatibility ---

func compatLoop(cfg config.Config) cycleLoop {
	return cycleLoop{cfg: cfg}
}

func stageWithCommand(id, command string) flow.Stage {
	return flow.Stage{ID: id, Command: command}
}

func TestCheckReloadCompatible_Native(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	on, off := true, false
	tests := []struct {
		name         string
		worktreeName string
		prev, next   flow.Flow
		wantErr      string // "" — совместимо
	}{
		{name: "same flow", prev: flow.Flow{Name: "a"}, next: flow.Flow{Name: "a"}},
		{name: "stages and command may change", prev: flow.Flow{Name: "a"},
			next: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "codex-as-claude")}}},
		{name: "name changed", prev: flow.Flow{Name: "a"}, next: flow.Flow{Name: "b"}, wantErr: "name"},
		{name: "worktrees turned on", prev: flow.Flow{Name: "a", Worktrees: &off}, next: flow.Flow{Name: "a", Worktrees: &on}, wantErr: "worktrees"},
		{name: "worktrees base changed", prev: flow.Flow{Name: "a", Worktrees: &on, RootDir: "x"}, next: flow.Flow{Name: "a", Worktrees: &on, RootDir: "y"}, wantErr: "root_dir"},
		{name: "root_dir changes without worktrees", prev: flow.Flow{Name: "a", Worktrees: &off, RootDir: "x"}, next: flow.Flow{Name: "a", Worktrees: &off, RootDir: "y"}},
		{name: "explicit --worktree keeps worktrees on", worktreeName: "wt", prev: flow.Flow{Name: "a", Worktrees: &off}, next: flow.Flow{Name: "a", Worktrees: &on}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := compatLoop(config.Config{})
			l.worktreeName = tt.worktreeName
			err := l.checkReloadCompatible(&tt.prev, &tt.next)
			assertCompat(t, err, tt.wantErr)
		})
	}
}

func assertCompat(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("want error mentioning %q, got nil", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) || !strings.Contains(err.Error(), "restart `afm run`") {
		t.Fatalf("error %q must mention %q and ask to restart `afm run`", err, wantErr)
	}
}

func TestCheckReloadCompatible_Container(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	autoShim := true
	cfg := config.Config{Docker: config.DockerConfig{
		AutoShim: &autoShim,
		Agents: map[string]config.AgentRecipe{
			"glm": {Type: config.ClaudeCommand, Model: "m", URL: "https://x", Auth: config.RecipeAuth{To: "env:ANTHROPIC_API_KEY"}},
			"cdx": {Type: config.RecipeTypeCodex, Auth: config.RecipeAuth{To: "env:OPENAI_API_KEY"}},
		},
	}}
	project := absRootDir()
	inside := filepath.Join(project, "sub")
	outside := filepath.Join(filepath.Dir(project), "elsewhere")

	tests := []struct {
		name       string
		prev, next flow.Flow
		wantErr    string
	}{
		{name: "same flow", prev: flow.Flow{Name: "a"}, next: flow.Flow{Name: "a"}},
		{name: "root_dir inside the mounted project", prev: flow.Flow{Name: "a", RootDir: "x"}, next: flow.Flow{Name: "a", RootDir: inside}},
		{name: "root_dir outside the mounted project", prev: flow.Flow{Name: "a", RootDir: "x"}, next: flow.Flow{Name: "a", RootDir: outside}, wantErr: "mount"},
		{name: "unchanged root_dir outside the project", prev: flow.Flow{Name: "a", RootDir: outside}, next: flow.Flow{Name: "a", RootDir: outside}},
		{name: "stage command added", prev: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "")}},
			next: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "mytool")}}, wantErr: "docker"},
		{name: "recipe used by next flow", prev: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "")}},
			next: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "glm")}}, wantErr: "docker"},
		{name: "codex state needed", prev: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "")}},
			next: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "cdx")}}, wantErr: "docker"},
		{name: "same commands reordered", prev: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "glm"), stageWithCommand("t", "mytool")}},
			next: flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("t", "mytool"), stageWithCommand("s", "glm")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCompat(t, compatLoop(cfg).checkReloadCompatible(&tt.prev, &tt.next), tt.wantErr)
		})
	}
}

func TestCarryHookEnv(t *testing.T) {
	envHook := func(id, stage string, env map[string]lifecyclehooks.SecretRef, resolved map[string]string, command string) lifecyclehooks.RegisteredHook {
		return lifecyclehooks.RegisteredHook{StageID: stage, Hook: lifecyclehooks.Hook{ID: id, Command: command, Env: env, ResolvedEnv: resolved}}
	}
	token := map[string]lifecyclehooks.SecretRef{"TOKEN": "env:HOST_TOKEN"} //nolint:gosec // reference to a test environment variable name
	resolved := map[string]string{"TOKEN": "s3cret-value"}
	prev := []lifecyclehooks.RegisteredHook{
		envHook("plain", "", nil, nil, "true"),
		envHook("notify", "", token, resolved, "old"),
	}

	t.Run("same env layout copies resolved values", func(t *testing.T) {
		next := []lifecyclehooks.RegisteredHook{
			envHook("added-plain", "", nil, nil, "true"),
			envHook("plain", "", nil, nil, "true"),
			envHook("notify", "", token, nil, "new-command"),
		}
		if err := carryHookEnv(prev, next); err != nil {
			t.Fatal(err)
		}
		if got := next[2].Hook.ResolvedEnv["TOKEN"]; got != "s3cret-value" {
			t.Fatalf("resolved TOKEN = %q, want it carried over", got)
		}
		if next[2].Hook.Command != "new-command" {
			t.Errorf("hook command must come from the reloaded flow, got %q", next[2].Hook.Command)
		}
	})

	for name, next := range map[string][]lifecyclehooks.RegisteredHook{
		"env hook removed":    {envHook("plain", "", nil, nil, "true")},
		"env hook added":      {envHook("notify", "", token, nil, ""), envHook("audit", "", token, nil, "")},
		"env spec changed":    {envHook("notify", "", map[string]lifecyclehooks.SecretRef{"TOKEN": "env:OTHER"}, nil, "")},
		"stage layer changed": {envHook("notify", "tick", token, nil, "")},
		"hook id changed":     {envHook("renamed", "", token, nil, "")},
	} {
		t.Run(name, func(t *testing.T) {
			err := carryHookEnv(prev, next)
			if err == nil || !strings.Contains(err.Error(), "restart `afm run`") {
				t.Fatalf("want a restart error, got %v", err)
			}
		})
	}
}

// --- Docker: transport читается один раз ---

func TestRepeatLoop_ContainerReusesTransportAcrossCycles(t *testing.T) {
	fx := newCycleFixture(t)
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv(lifecyclehooks.TransportName(0, 0), "hook-secret-value")
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:1")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "browser-token")
	t.Setenv("AFM_BROWSER_EPOCH", "epoch-1")
	envLog := filepath.Join(fx.root, "hook-env.txt")
	// Команда хука читает свой env: без перенесённого значения второй цикл увидел бы пустую строку.
	fx.write("flow.yaml", fx.flowYAML(flowSpec{
		repeat:    "{max_cycles: 2}",
		hookEnv:   `{TOKEN: "env:HOST_ONLY_TOKEN"}`,
		hookExtra: "; printenv TOKEN >> " + envLog,
	}))

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	wantEvents(t, fx.eventTypes(), "flow_started", "flow_finished", "flow_repeated", "flow_finished")
	data, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(data)); len(got) != 4 || got[0] != "hook-secret-value" || got[3] != "hook-secret-value" {
		t.Fatalf("hook TOKEN per event = %v, want hook-secret-value for all 4 deliveries", got)
	}
	for _, name := range []string{lifecyclehooks.TransportName(0, 0), "AFM_BROWSER_TRANSPORT_TOKEN"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("transport variable %s must be removed from the process environment", name)
		}
	}
}

func TestRepeatLoop_ContainerHookEnvChangeFailsBeforeNewRun(t *testing.T) {
	fx := newCycleFixture(t)
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv(lifecyclehooks.TransportName(0, 0), "hook-secret-value")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{
		repeat: "true", hookEnv: `{TOKEN: "env:HOST_ONLY_TOKEN"}`, script: fx.swapFlowScript(),
	}))
	fx.write("next.yaml", fx.flowYAML(flowSpec{repeat: "true", hookEnv: `{TOKEN: "env:OTHER_TOKEN"}`}))

	err := fx.runWithTimeout(testCycleConfig())
	if err == nil || !strings.Contains(err.Error(), "restart `afm run`") || !strings.Contains(err.Error(), "cycle 2") {
		t.Fatalf("error = %v, want a restart error for cycle 2", err)
	}
	if got := len(fx.runDirs()); got != 1 {
		t.Fatalf("run dirs = %d, want 1: the error must come before the new run dir", got)
	}
}

func TestContainerInputs_ReadsTransportOnce(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:1")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "browser-token")
	t.Setenv("AFM_BROWSER_EPOCH", "epoch-1")
	hooks := []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "n", Env: map[string]lifecyclehooks.SecretRef{"TOKEN": "env:X"}}}}
	t.Setenv(lifecyclehooks.TransportName(0, 0), "v")

	desc, err := takeContainerTransport(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if desc == nil || desc.Token != "browser-token" || desc.Epoch != "epoch-1" {
		t.Fatalf("browser descriptor = %+v, want the transport values", desc)
	}
	if hooks[0].Hook.ResolvedEnv["TOKEN"] != "v" {
		t.Fatalf("hook env not resolved: %+v", hooks[0].Hook.ResolvedEnv)
	}
	if _, ok := os.LookupEnv("AFM_BROWSER_TRANSPORT_TOKEN"); ok {
		t.Error("browser token still in the environment")
	}
}

// --- drain dashboard: решение о следующем цикле принимается до выбора паузы ---

// countDrains подменяет финальное ожидание dashboard счётчиком вызовов.
func countDrains(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	prev := drainDashboard
	drainDashboard = func(context.Context, func() int) { n.Add(1) }
	t.Cleanup(func() { drainDashboard = prev })
	prevGrace := interCycleDrainGrace
	interCycleDrainGrace = time.Millisecond
	t.Cleanup(func() { interCycleDrainGrace = prevGrace })
	return &n
}

func TestRepeatLoop_FinalDrainWhenReloadEndsRepeat(t *testing.T) {
	tests := []struct {
		name    string
		next    string
		wantErr string
	}{
		{"repeat disabled", "repeat: false", ""},
		{"reloaded max_cycles reached", "repeat: {max_cycles: 1}", ""},
		{"invalid yaml", "name: [broken\n", "parse flow"},
		{"restart required", "name: renamed\nrepeat: true", "restart"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drains := countDrains(t)
			fx := newCycleFixture(t)
			port, err := pickFreeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			cfg := testCycleConfig()
			cfg.Server.Port = &port
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true", script: fx.swapFlowScript()}))
			next := tt.next
			if !strings.HasPrefix(next, "name:") {
				next = fx.flowYAML(flowSpec{repeat: strings.TrimPrefix(next, "repeat: ")})
			}
			fx.write("next.yaml", next)

			err = fx.runWithTimeout(cfg)
			if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.wantErr)
			}
			if got := drains.Load(); got != 1 {
				t.Fatalf("full dashboard drains = %d, want 1: the last cycle is the actual exit", got)
			}
		})
	}
}

func TestRepeatLoop_ShortGraceBetweenCyclesFullDrainAtEnd(t *testing.T) {
	drains := countDrains(t)
	fx := newCycleFixture(t)
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCycleConfig()
	cfg.Server.Port = &port
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3}"}))

	out := captureStdout(t, func() {
		if err := fx.runWithTimeout(cfg); err != nil {
			t.Error(err)
		}
	})

	if got := drains.Load(); got != 1 {
		t.Fatalf("full dashboard drains over 3 cycles = %d, want 1 (only the last)", got)
	}
	if got := strings.Count(out, "dashboard: holding 1ms for UI"); got != 2 {
		t.Fatalf("short inter-cycle grace printed %d times, want 2\n%s", got, out)
	}
}

// Ctrl+C во время финального (полного) дренажа dashboard должен прерывать процесс, а не завершаться
// штатно: next.stopReason посчитан ещё до паузы и не должен маскировать отмену контекста.
func TestRepeatLoop_CancelDuringFinalDrainInterrupts(t *testing.T) {
	fx := newCycleFixture(t)
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCycleConfig()
	cfg.Server.Port = &port
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 1}"}))

	draining := make(chan struct{})
	prevDrain := drainDashboard
	drainDashboard = func(ctx context.Context, _ func() int) {
		close(draining)
		<-ctx.Done()
	}
	t.Cleanup(func() { drainDashboard = prevDrain })

	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fx.run(ctx, cfg) }()
	select {
	case <-draining:
	case <-time.After(loopTestTimeout):
		t.Fatal("timed out waiting for the final dashboard drain to start")
	}
	cancel()

	var exitErr *ExitError
	if err := <-done; !errors.As(err, &exitErr) || exitErr.Code != interruptedExitCode {
		t.Fatalf("cancel during final drain error = %v, want exit code %d", err, interruptedExitCode)
	}
}

// --- правка flow.yaml во время паузы dashboard действует: новый ран решает свежее чтение ---

// Ранняя оценка afterRun делается до паузы dashboard и выбирает только её длину. Правка flow.yaml, пришедшая
// после неё (в паузу), должна остановить повторение до создания следующего run dir.
func TestRepeatLoop_EditAfterEarlyDecisionStopsBeforeNewRun(t *testing.T) {
	tests := []struct {
		name    string
		next    string
		wantErr string
	}{
		{"repeat disabled", "repeat: false", ""},
		{"reloaded max_cycles reached", "repeat: {max_cycles: 1}", ""},
		{"invalid yaml", "name: [broken\n", "parse flow"},
		{"restart required", "name: renamed\nrepeat: true", "restart"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true"}))
			next := tt.next
			if !strings.HasPrefix(next, "name:") {
				next = fx.flowYAML(flowSpec{repeat: strings.TrimPrefix(next, "repeat: ")})
			}
			prev := executeCycle
			executeCycle = func(ctx context.Context, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
				early := opts.afterRun
				opts.afterRun = func() bool {
					more := early()
					fx.write("flow.yaml", next) // правка во время паузы dashboard, уже после ранней оценки
					return more
				}
				return prev(ctx, f, cfg, hooks, opts)
			}
			t.Cleanup(func() { executeCycle = prev })

			err := fx.runWithTimeout(testCycleConfig())
			if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.wantErr)
			}
			if got := len(fx.runDirs()); got != 1 {
				t.Fatalf("run dirs = %d, want 1: the edit landed before cycle 2 was created", got)
			}
			if got := fx.ticks(); got != 1 {
				t.Fatalf("cycles run = %d, want 1", got)
			}
		})
	}
}

// --- порядок: run dir следующего цикла создаётся раньше очистки старых ---

func TestRepeatLoop_NewRunDirExistsBeforeOldRunsAreRemoved(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3, keep_runs: 1}"}))
	var atCreate, afterPrune [][]string
	prev := executeCycle
	executeCycle = func(ctx context.Context, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
		if orig := opts.onRunCreated; orig != nil {
			opts.onRunCreated = func(id string) {
				atCreate = append(atCreate, fx.runDirs())
				orig(id)
				afterPrune = append(afterPrune, fx.runDirs())
			}
		}
		return prev(ctx, f, cfg, hooks, opts)
	}
	t.Cleanup(func() { executeCycle = prev })

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	ids := fx.startRunIDs()
	// Колбэк есть у циклов 2 и 3. В момент вызова новый run dir уже на диске, а старый ещё не удалён.
	if len(atCreate) != 2 || !sameRuns(atCreate[1], ids) {
		t.Fatalf("run dirs when cycle 3 run was created = %v, want all of %v", atCreate, ids)
	}
	if !sameRuns(afterPrune[1], ids[1:]) {
		t.Fatalf("run dirs after cycle 3 cleanup = %v, want %v", afterPrune[1], ids[1:])
	}
}

// --- Docker: дескриптор browser один на процесс ---

func TestRepeatLoop_ContainerGivesEveryCycleTheSameBrowserDescriptor(t *testing.T) {
	fx := newCycleFixture(t)
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv("AFM_BROWSER_ENDPOINT", "host.docker.internal:1")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "browser-token")
	t.Setenv("AFM_BROWSER_EPOCH", "epoch-1")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3}"}))
	var seen []*browser.Descriptor
	prev := executeCycle
	executeCycle = func(ctx context.Context, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
		seen = append(seen, opts.containerBrowser)
		return prev(ctx, f, cfg, hooks, opts)
	}
	t.Cleanup(func() { executeCycle = prev })

	if err := fx.runWithTimeout(testCycleConfig()); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 3 {
		t.Fatalf("cycles = %d, want 3", len(seen))
	}
	for i, d := range seen {
		if d == nil || d.Token != "browser-token" || d.Epoch != "epoch-1" || d != seen[0] {
			t.Fatalf("cycle %d browser descriptor = %+v, want the one read from transport in cycle 1 (%+v)", i+1, d, seen[0])
		}
	}
}

// --- Docker-проекция детерминирована ---

func TestProjectDocker_StableAcrossMapIterationOrder(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{Profiles: map[string]config.ProfileConfig{
		"a": {Command: "cmd-e"}, "b": {Command: "cmd-b"}, "c": {Command: "cmd-d"},
		"d": {Command: "cmd-a"}, "e": {Command: "cmd-c"}, "f": {Command: "cmd-f"},
	}}}
	f := &flow.Flow{Name: "a", Stages: []flow.Stage{stageWithCommand("s", "mytool")}}
	first, err := projectDocker(f, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		got, err := projectDocker(f, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("projection #%d = %+v differs from the first %+v for the same flow", i, got, first)
		}
	}
}

func TestCheckReloadCompatible_ContainerUnchangedFlowWithManyHelperProfiles(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	cfg := config.Config{Collaboration: config.CollaborationConfig{Profiles: map[string]config.ProfileConfig{
		"a": {Command: "cmd-e"}, "b": {Command: "cmd-b"}, "c": {Command: "cmd-d"},
		"d": {Command: "cmd-a"}, "e": {Command: "cmd-c"}, "f": {Command: "cmd-f"},
	}}}
	f := &flow.Flow{Name: "a"}
	for i := range 100 {
		if err := compatLoop(cfg).checkReloadCompatible(f, f); err != nil {
			t.Fatalf("check #%d: an unchanged flow must stay compatible: %v", i, err)
		}
	}
}
