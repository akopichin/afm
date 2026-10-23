package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// holdNow — «сейчас» в тестах hold'а: фиксированная дата в UTC, чтобы тесты не зависели от TZ машины.
var holdNow = time.Date(2026, 10, 23, 23, 48, 0, 0, time.UTC)

// newHoldTestOrch собирает оркестратор без Run поверх реального журнала, с fake-часами.
func newHoldTestOrch(t *testing.T, stages ...flow.Stage) (*Orchestrator, *clock.FakeClock) {
	t.Helper()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	dir := t.TempDir()
	store, err := state.Open(dir, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o := assembleAssistantsTestOrchestrator(t, Options{RunDir: dir, Stages: stages, Store: store, RunID: "run1", Config: config.Default()})
	clk := clock.NewFake(holdNow)
	o.clock = clk
	return o, clk
}

// parkStageForTest durably parks a running stage with q — как это делает parkForQuota, но без агента.
func parkStageForTest(t *testing.T, o *Orchestrator, id string, q state.QuotaWait) {
	t.Helper()
	activateStage(t, o, id)
	if _, ok := o.Trigger(id, bus.EvAwaitQuota, bus.GuardCtx{Quota: &q}, "test park"); !ok {
		t.Fatalf("EvAwaitQuota failed for %s", id)
	}
}

func claudePark(until time.Time) state.QuotaWait {
	return state.QuotaWait{Provider: "claude", Until: until, ResetAt: until.Add(-time.Minute), Window: "five_hour",
		Source: state.QuotaSourceStructured, Target: phaseImplementation, Waits: 1}
}

func TestAdmitProvider_ActiveParkHoldsSameProviderUntilItsUntil(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"}, flow.Stage{ID: "c"})
	until := holdNow.Add(time.Hour)
	parkStageForTest(t, o, "a", claudePark(until))

	hold := o.admitProvider("claude", "b")
	if hold == nil {
		t.Fatal("b/claude admitted while a/claude is parked")
	}
	if hold.Source != state.QuotaSourceProviderHold || hold.Provider != "claude" || !hold.ResetAt.Equal(until) {
		t.Errorf("hold = %+v, want provider_hold on claude until exactly %s (no jitter)", hold, until)
	}
	if got := o.admitProvider("codex", "c"); got != nil {
		t.Errorf("c/codex held by a claude park: %+v", got)
	}
	if got := o.admitProvider("claude", "a"); got != nil {
		t.Errorf("the parked stage itself is held by its own park: %+v", got)
	}
}

func TestAdmitProvider_ExpiredHoldNoLongerBlocks(t *testing.T) {
	o, clk := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))

	clk.Advance(time.Hour) // until == now: уже не «в будущем»
	if got := o.admitProvider("claude", "b"); got != nil {
		t.Errorf("hold at its until still blocks: %+v", got)
	}
}

func TestAdmitProvider_LongestParkOfTheProviderWins(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "d"}, flow.Stage{ID: "b"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	parkStageForTest(t, o, "d", claudePark(holdNow.Add(2*time.Hour)))

	hold := o.admitProvider("claude", "b")
	if hold == nil || !hold.ResetAt.Equal(holdNow.Add(2*time.Hour)) {
		t.Errorf("hold = %+v, want the longest park (until +2h)", hold)
	}
}

func TestAdmitProvider_ProviderHoldParkDoesNotCreateHold(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"}, flow.Stage{ID: "c"})
	held := claudePark(holdNow.Add(3 * time.Hour))
	held.Source = state.QuotaSourceProviderHold
	parkStageForTest(t, o, "b", held)

	if got := o.admitProvider("claude", "c"); got != nil {
		t.Errorf("a provider_hold park created a hold: %+v", got)
	}
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	if hold := o.admitProvider("claude", "c"); hold == nil || !hold.ResetAt.Equal(holdNow.Add(time.Hour)) {
		t.Errorf("hold = %+v, want a's real park (+1h), not the provider_hold's +3h", hold)
	}
}

func TestAdmitProvider_PausedParkIsNotAHold(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	if _, ok := o.Trigger("a", bus.EvPause, bus.GuardCtx{}, "user"); !ok {
		t.Fatal("pause failed")
	}
	if got := o.admitProvider("claude", "b"); got != nil {
		t.Errorf("a paused stage holds the provider: %+v", got)
	}
}

func TestAdmitProvider_ManualProbeBypassesHoldOfItsStageOnly(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"}, flow.Stage{ID: "c"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	probe := claudePark(holdNow.Add(time.Hour))
	probe.ManualProbe = true
	activateStage(t, o, "b")
	if _, ok := o.Trigger("b", bus.EvAwaitQuota, bus.GuardCtx{Quota: &probe}, "test"); !ok {
		t.Fatal("park failed")
	}

	if got := o.admitProvider("claude", "b"); got != nil {
		t.Errorf("manual probe held by the provider hold: %+v", got)
	}
	if got := o.admitProvider("claude", "c"); got == nil {
		t.Error("the manual probe of b opened the hold for c")
	}
}

// Проба — одна допуск-проверка: первая (автор) проходит, вторая того же состояния парка (повторная попытка автора
// или verify-агент) снова проверяет hold.
func TestAdmitProvider_ManualProbeIsSpentByTheFirstAdmission(t *testing.T) {
	o, _ := newHoldTestOrch(t, flow.Stage{ID: "a"}, flow.Stage{ID: "b"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	probe := claudePark(holdNow.Add(time.Hour))
	probe.ManualProbe = true
	activateStage(t, o, "b")
	if _, ok := o.Trigger("b", bus.EvAwaitQuota, bus.GuardCtx{Quota: &probe}, "test"); !ok {
		t.Fatal("park failed")
	}

	if got := o.admitProvider("claude", "b"); got != nil {
		t.Fatalf("first admission after the Continue held: %+v", got)
	}
	if got := o.admitProvider("claude", "b"); got == nil {
		t.Error("second admission still bypasses the hold: the probe was not spent")
	}
}

func TestVerifyAdmission_ManualProbeSpentByAuthorAdmissionDoesNotBypassHold(t *testing.T) {
	stage := flow.Stage{ID: "main", Command: "claude"}
	o, _ := newHoldTestOrch(t, stage, flow.Stage{ID: "a"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	probe := claudePark(holdNow.Add(time.Hour))
	probe.ManualProbe = true
	activateStage(t, o, "main")
	if _, ok := o.Trigger("main", bus.EvAwaitQuota, bus.GuardCtx{Quota: &probe}, "test"); !ok {
		t.Fatal("park failed")
	}
	var calls atomic.Int32
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls.Add(1)
		return verify.RunOutcome{}, nil
	}
	if got := o.admitProvider(o.stageProvider(stage), stage.ID); got != nil {
		t.Fatalf("author admission held: %+v", got)
	}

	_, _, err := o.runVerifyAgentStep(context.Background(), stage, flow.VerifyStep{Command: "claude"}, 1, "ver-1", prompts.VerifyInputs{})

	var limit *executor.UsageLimitError
	if !errors.As(err, &limit) || limit.Source != state.QuotaSourceProviderHold {
		t.Fatalf("verify admission err = %v, want a provider_hold: the probe was spent by the author's admission", err)
	}
	if calls.Load() != 0 {
		t.Errorf("verify agent started %d times under the hold", calls.Load())
	}
}

func TestAfterOnRunContext_FiresWhenTheClockReachesTheTimeAndNotBefore(t *testing.T) {
	o, clk := newHoldTestOrch(t, flow.Stage{ID: "a"})
	fired := make(chan string, 2)
	o.afterOnRunContext(context.Background(), holdNow.Add(time.Hour), func(context.Context) { fired <- "later" })
	o.afterOnRunContext(context.Background(), holdNow.Add(-time.Hour), func(context.Context) { fired <- "past" }) // уже наступило: ждёт ноль
	clk.BlockUntil(2)

	clk.Advance(0)
	waitForFired(t, fired, "past")
	neverTrue(t, "the later timer firing before its time", func() bool { return len(fired) > 0 })

	clk.Advance(time.Hour)
	waitForFired(t, fired, "later")
}

func waitForFired(t *testing.T, fired <-chan string, want string) {
	t.Helper()
	select {
	case got := <-fired:
		if got != want {
			t.Fatalf("fired = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timer %q did not fire", want)
	}
}

func TestQuotaSeries(t *testing.T) {
	prev := &state.QuotaWait{Target: phaseImplementation, Waits: 3}
	for name, c := range map[string]struct {
		prev       *state.QuotaWait
		phase      string
		progressed bool
		want       int
	}{
		"no earlier park":                {nil, phaseImplementation, false, 0},
		"same phase continues the run":   {prev, phaseImplementation, false, 3},
		"another phase starts a series":  {prev, phasePlanning, false, 0},
		"progress starts a series":       {prev, phaseImplementation, true, 0},
		"manual probe zeroed the series": {&state.QuotaWait{Target: phaseImplementation}, phaseImplementation, false, 0},
	} {
		if got := quotaSeries(c.prev, c.phase, c.progressed); got != c.want {
			t.Errorf("%s: series = %d, want %d", name, got, c.want)
		}
	}
}

func TestPlanQuotaWait_ProviderHoldTakesHoldUntilWithoutJitterOrCount(t *testing.T) {
	cfg := config.Default().UsageLimit
	until := holdNow.Add(40 * time.Minute)
	hold := &executor.UsageLimitError{Provider: "claude", Window: "five_hour", ResetAt: until, Source: state.QuotaSourceProviderHold}
	for i := 0; i < 20; i++ { // джиттер случаен: проверяем многократно, что его нет
		q := planQuotaWait(cfg, hold, phaseImplementation, 4, holdNow) // 4 == max_waits: ни паузы, ни счёта
		if !q.Until.Equal(until) || q.Source != state.QuotaSourceProviderHold || q.PauseReason != "" || q.Waits != 4 {
			t.Fatalf("plan = %+v, want provider_hold until exactly %s, no pause, waits unchanged", q, until)
		}
	}
	// Hold дальше max_wait всё равно ждётся: он вычислен из чужого парка, который уже прошёл проверку.
	far := &executor.UsageLimitError{Provider: "claude", ResetAt: holdNow.Add(11 * time.Hour), Source: state.QuotaSourceProviderHold}
	if q := planQuotaWait(cfg, far, phaseImplementation, 0, holdNow); q.PauseReason != "" || q.Until.IsZero() {
		t.Errorf("far hold plan = %+v, want a plain wait", q)
	}
}

// --- helper site ---

// newHelperHoldOrch — оркестратор помощников с командой helper-cmd (профиль reviewer), где слот команды один, и fake-часами.
func newHelperHoldOrch(t *testing.T) (*Orchestrator, *state.Store, *clock.FakeClock) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "other", Command: "helper-cmd"})
	clk := clock.NewFake(holdNow)
	o.clock = clk
	sems := map[string]concurrency.Semaphore{
		"claude":     concurrency.ChannelSemaphore(make(chan struct{}, 4)),
		"helper-cmd": concurrency.ChannelSemaphore(make(chan struct{}, 1)),
	}
	o.concurrency = concurrency.NewWithSemaphores(bus.NewCriticalBus(16), sems, "claude")
	o.concurrency.Clock = clk
	t.Cleanup(o.concurrency.WaitAgents) // раньше закрытия журнала: cleanup'ы идут LIFO
	return o, store, clk
}

func holdHelperPark(until time.Time) state.QuotaWait {
	q := claudePark(until)
	q.Provider = "helper-cmd"
	return q
}

func TestHelperAdmission_HoldDefersSpawnUntilHoldEnds(t *testing.T) {
	o, store, clk := newHelperHoldOrch(t)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		calls.Add(1)
		return passResultOutcome(), nil
	}
	activateStage(t, o, assistantsTestStageID)
	parkStageForTest(t, o, "other", holdHelperPark(holdNow.Add(time.Hour)))

	taskID := attachTestTask(context.Background(), t, o, "op-1")

	neverTrue(t, "helper process started under the provider hold", func() bool { return calls.Load() > 0 })
	if tk := store.Snapshot().Assistants.Tasks[taskID]; tk == nil || tk.Status != assistants.StatusQueued {
		t.Fatalf("task = %+v, want queued while the provider is held", tk)
	}

	// Парк-держатель кончается вместе со своим until (его собственный таймер здесь не взведён): проверяем,
	// что помощник сам просыпается к концу hold'а.
	clk.BlockUntil(1)
	if _, ok := o.Trigger("other", bus.EvPause, bus.GuardCtx{}, "release the hold"); !ok {
		t.Fatal("pause failed")
	}
	clk.Advance(time.Hour)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	if calls.Load() != 1 {
		t.Errorf("helper calls = %d, want exactly 1 after the hold", calls.Load())
	}
}

func TestHelperAdmission_HoldAppearingWhileWaitingForSlotRequeuesTask(t *testing.T) {
	o, store, clk := newHelperHoldOrch(t)
	release := make(chan struct{})
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			awaitRelease(t, release) // первый помощник держит единственный слот команды
		}
		return passResultOutcome(), nil
	}
	activateStage(t, o, assistantsTestStageID)
	first := attachTestTask(context.Background(), t, o, "op-1")
	waitForTaskStatus(t, store, first, assistants.StatusRunning, 5*time.Second)
	second := attachTestTask(context.Background(), t, o, "op-2") // admission прошла: hold'а ещё нет; ждёт слот
	waitForTaskStatus(t, store, second, assistants.StatusRunning, 5*time.Second)

	parkStageForTest(t, o, "other", holdHelperPark(holdNow.Add(time.Hour)))
	close(release) // слот освобождается — второй получает его уже под hold'ом

	waitForTaskStatus(t, store, second, assistants.StatusQueued, 5*time.Second)
	if calls.Load() != 1 {
		t.Fatalf("helper calls = %d, want 1: the second must not start under the hold", calls.Load())
	}
	if _, ok := o.Trigger("other", bus.EvPause, bus.GuardCtx{}, "release the hold"); !ok {
		t.Fatal("pause failed")
	}
	clk.BlockUntil(1)
	clk.Advance(time.Hour)
	waitForTaskStatus(t, store, second, assistants.StatusCompleted, 5*time.Second)
}

// --- verify site ---

func TestVerifyAdmission_HoldReturnsProviderHoldBeforeAgentStart(t *testing.T) {
	stage := flow.Stage{ID: "main", Command: "claude"}
	o, _ := newHoldTestOrch(t, stage, flow.Stage{ID: "a"})
	parkStageForTest(t, o, "a", claudePark(holdNow.Add(time.Hour)))
	var calls atomic.Int32
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		calls.Add(1)
		return verify.RunOutcome{}, nil
	}

	_, _, err := o.runVerifyAgentStep(context.Background(), stage, flow.VerifyStep{Command: "claude"}, 1, "ver-1", prompts.VerifyInputs{})

	var limit *executor.UsageLimitError
	if !errors.As(err, &limit) || limit.Source != state.QuotaSourceProviderHold || !limit.ResetAt.Equal(holdNow.Add(time.Hour)) {
		t.Fatalf("err = %v, want a provider_hold usage limit until +1h", err)
	}
	if calls.Load() != 0 {
		t.Errorf("verify agent started %d times under the hold", calls.Load())
	}
}

// neverTrue проверяет отрицательное утверждение: условие остаётся ложным короткое реальное окно.
func neverTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
