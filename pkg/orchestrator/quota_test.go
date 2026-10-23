package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// quotaStart — «сейчас» в тестах парковки; фиксированная дата в UTC, чтобы тесты не зависели от TZ машины.
var quotaStart = time.Date(2026, 10, 23, 23, 48, 0, 0, time.UTC)

const quotaPlan = "## Tasks\n\n- [ ] Step 1\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] works\n"

// quotaRunner — runner с программируемым исходом каждого вызова: script(call, stageDir) == nil — успех (агент пишет
// .done или plan.md), иначе возвращается ошибка как есть.
type quotaRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	kinds   []string // "planning" или "agent" на каждый вызов
	script  func(call int, stageDir string) error
}

func (r *quotaRunner) next(kind, prompt string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.prompts = append(r.prompts, prompt)
	r.kinds = append(r.kinds, kind)
	return r.calls
}

func (r *quotaRunner) kindsOf() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.kinds...)
}

func (r *quotaRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *quotaRunner) promptOf(call int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts[call-1]
}

func (r *quotaRunner) RunPlanning(_ context.Context, _, prompt, outFile, _ string) error {
	n := r.next("planning", prompt)
	if err := r.script(n, filepath.Dir(outFile)); err != nil {
		return err
	}
	return os.WriteFile(outFile, []byte(quotaPlan), 0o644)
}

func (r *quotaRunner) RunAgent(_ context.Context, _, _, prompt, logFile string) error {
	n := r.next("agent", prompt)
	stageDir := filepath.Dir(logFile)
	if err := r.script(n, stageDir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644)
}

func (r *quotaRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

func limitRefusal(resetAt time.Time) *executor.UsageLimitError {
	return &executor.UsageLimitError{
		Provider: "claude", ResetAt: resetAt, Window: "five_hour", Message: "usage limit",
		Source: executor.UsageLimitSourceStructured,
	}
}

// quotaRig — один запуск оркестратора поверх каталога рана; reopen поднимает новый поверх того же каталога.
type quotaRig struct {
	t      *testing.T
	runDir string
	store  *state.Store
	orch   *orchestrator.Orchestrator
	clk    *clock.FakeClock
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

func quotaStages() []flow.Stage {
	return []flow.Stage{{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}}}
}

func quotaConfig() config.Config { return config.Default() }

// startQuotaRig открывает (или переоткрывает) ран в runDir и запускает Run. seed: подготовить стадию к первому
// запуску (plan.md + ready) — при переоткрытии журнал уже хранит состояние. configure вызывается до Run
// (например, SetDashboardURL или захват оркестратора раннером).
func startQuotaRig(t *testing.T, runDir string, stages []flow.Stage, cfg config.Config, runner executor.Runner, clk *clock.FakeClock, seed bool, configure ...func(*orchestrator.Orchestrator)) *quotaRig {
	t.Helper()
	ids := make([]string, len(stages))
	for i, s := range stages {
		ids[i] = s.ID
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatal(err)
	}
	if seed {
		for _, id := range ids {
			if err := os.MkdirAll(filepath.Join(runDir, id), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runDir, id, "plan.md"), []byte("# plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: state.StatusReady, Event: "test_setup"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg,
		Prompts: orchestrator.DefaultPrompts(), Runner: runner, Clock: clk,
	})
	for _, fn := range configure {
		fn(orch)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rig := &quotaRig{t: t, runDir: runDir, store: store, orch: orch, clk: clk, cancel: cancel, done: make(chan struct{})}
	go func() { _ = orch.Run(ctx); close(rig.done) }()
	t.Cleanup(rig.stop)
	return rig
}

// stop отменяет контекст рана, ждёт возврата Run и закрывает журнал — штатное завершение процесса, как Ctrl+C.
// Повторный вызов (cleanup после явного stop) ничего не делает.
func (r *quotaRig) stop() {
	if r.closed {
		return
	}
	r.closed = true
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.t.Error("orch.Run did not return within 10s of cancellation")
	}
	r.store.Close()
}

func (r *quotaRig) stage(id string) state.StageState { return r.store.Snapshot().Stages[id] }

// waitFor опрашивает условие по реальному времени (само ожидание сброса идёт на fake-часах).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *quotaRig) waitStatus(id string, want state.StageStatus) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.stage(id).Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("stage %s did not become %s (now %s)", id, want, r.stage(id).Status)
}

// waitParked ждёт парковку и подписку её таймера на fake-часах: помимо таймера парковки на часах всегда висит
// тикер dialog-поллера.
func (r *quotaRig) waitParked(id string) *state.QuotaWait {
	r.t.Helper()
	r.waitStatus(id, state.StatusAwaitingQuota)
	r.clk.BlockUntil(2)
	return r.stage(id).Quota
}

// neverHappens проверяет отрицательное утверждение: условие остаётся ложным короткое реальное окно.
func neverHappens(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// journalEvents возвращает имена событий-переходов из events.jsonl в порядке записи.
func journalEvents(t *testing.T, runDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Event != "" {
			out = append(out, rec.Event)
		}
	}
	return out
}

func countEvent(events []string, name bus.FSMEvent) int {
	n := 0
	for _, e := range events {
		if e == string(name) {
			n++
		}
	}
	return n
}

func TestQuota_ParkWaitsUntilResetPlusMargin_ThenCompletes(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	reset := quotaStart.Add(2 * time.Minute) // 23:50
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(reset)
		}
		return nil
	}}
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clk, true)

	q := rig.waitParked("s1")
	if q == nil {
		t.Fatal("awaiting_quota stage has no quota")
	}
	wantUntil := reset.Add(time.Minute) // 23:51
	if d := q.Until.Sub(wantUntil); d < -6*time.Second || d > 6*time.Second {
		t.Errorf("until = %s, want %s ±6s (resume_margin jitter)", q.Until, wantUntil)
	}
	if q.Provider != "claude" || q.Source != state.QuotaSourceStructured || q.Window != "five_hour" ||
		q.Target != "implementation" || q.Waits != 1 || !q.ResetAt.Equal(reset) || q.PauseReason != "" {
		t.Errorf("quota = %+v", *q)
	}

	// До срока агент не стартует: после промотки на 23:50 таймер парковки всё ещё висит на часах.
	clk.Advance(2 * time.Minute)
	clk.BlockUntil(2)
	if got := runner.callCount(); got != 1 {
		t.Fatalf("agent spawned before until: %d calls", got)
	}
	if rig.stage("s1").Status != state.StatusAwaitingQuota {
		t.Fatalf("status = %s before until", rig.stage("s1").Status)
	}

	clk.Advance(2 * time.Minute) // 23:52 > until
	rig.waitStatus("s1", state.StatusDone)
	if got := runner.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2 (refusal + resumed turn)", got)
	}
	if rig.stage("s1").Quota != nil {
		t.Errorf("quota must be cleared on done, got %+v", *rig.stage("s1").Quota)
	}
	events := journalEvents(t, rig.runDir)
	if n := countEvent(events, bus.EvFail) + countEvent(events, bus.EvScheduleRetry); n != 0 {
		t.Errorf("usage limit must not fail or consume a retry: events = %v", events)
	}
	if countEvent(events, bus.EvAwaitQuota) != 1 || countEvent(events, bus.EvQuotaReset) != 1 {
		t.Errorf("want exactly one park and one wake: events = %v", events)
	}
}

func TestQuota_UnknownResetProbesAfterProbeInterval(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(time.Time{})
		}
		return nil
	}}
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clk, true)

	q := rig.waitParked("s1")
	if want := quotaStart.Add(15 * time.Minute); !q.Until.Equal(want) {
		t.Errorf("until = %s, want %s (probe_interval)", q.Until, want)
	}
	if q.Source != state.QuotaSourceProbe || !q.ResetAt.IsZero() {
		t.Errorf("quota = %+v, want source=probe and unknown reset", *q)
	}
	clk.Advance(14 * time.Minute)
	clk.BlockUntil(2)
	if got := runner.callCount(); got != 1 {
		t.Fatalf("probed before probe_interval: %d calls", got)
	}
	clk.Advance(time.Minute)
	rig.waitStatus("s1", state.StatusDone)
}

func TestQuota_UnwaitableLimitParksThenPausesWithReason(t *testing.T) {
	cases := []struct {
		name   string
		limit  func() *executor.UsageLimitError
		reason string
	}{
		{"reset beyond max_wait", func() *executor.UsageLimitError { return limitRefusal(quotaStart.Add(13 * time.Hour)) }, state.QuotaPauseExceedsMaxWait},
		{"no reset", func() *executor.UsageLimitError {
			l := limitRefusal(time.Time{})
			l.NoReset = true
			return l
		}, state.QuotaPauseNoReset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &quotaRunner{script: func(int, string) error { return tc.limit() }}
			rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clock.NewFake(quotaStart), true)
			rig.waitStatus("s1", state.StatusPaused)

			st := rig.stage("s1")
			if st.PausedFrom != state.StatusAwaitingQuota || st.Quota == nil || st.Quota.PauseReason != tc.reason || !st.Quota.Until.IsZero() {
				t.Fatalf("stage = %+v quota = %+v, want paused from awaiting_quota with reason %s", st, st.Quota, tc.reason)
			}
			events := journalEvents(t, rig.runDir)
			if countEvent(events, bus.EvFail) != 0 || countEvent(events, bus.EvAwaitQuota) != 1 || countEvent(events, bus.EvPause) != 1 {
				t.Errorf("want park then pause, no fail: events = %v", events)
			}
			// Причина и PausedFrom переживают replay журнала.
			rs, err := state.LoadRunState(rig.runDir)
			if err != nil {
				t.Fatal(err)
			}
			got := rs.Stages["s1"]
			if got.Status != state.StatusPaused || got.PausedFrom != state.StatusAwaitingQuota || got.Quota == nil || got.Quota.PauseReason != tc.reason {
				t.Errorf("replayed stage = %+v quota = %+v", got, got.Quota)
			}
			if runner.callCount() != 1 {
				t.Errorf("agent calls = %d, want 1", runner.callCount())
			}
		})
	}
}

// refusalsEveryTime — агент всегда упирается в лимит со сбросом через минуту от «сейчас».
func refusalsEveryTime(clk *clock.FakeClock, progress bool) *quotaRunner {
	return &quotaRunner{script: func(int, string) error {
		l := limitRefusal(clk.Now().Add(time.Minute))
		l.Progress = progress
		return l
	}}
}

func TestQuota_FifthConsecutiveRefusalPauses(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := refusalsEveryTime(clk, false)
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clk, true)

	for want := 1; want <= 4; want++ { // max_waits=4: четыре отказа ждём
		q := rig.waitParked("s1")
		if q.Waits != want {
			t.Fatalf("waits after refusal %d = %d", want, q.Waits)
		}
		clk.Advance(3 * time.Minute)
		waitFor(t, "wake", func() bool { return runner.callCount() > want })
	}
	rig.waitStatus("s1", state.StatusPaused)
	st := rig.stage("s1")
	if st.PausedFrom != state.StatusAwaitingQuota || st.Quota == nil || st.Quota.PauseReason != state.QuotaPauseMaxWaits || st.Quota.Waits != 5 {
		t.Errorf("stage = %+v quota = %+v, want paused with max_waits at the 5th refusal", st, st.Quota)
	}
	if got := runner.callCount(); got != 5 {
		t.Errorf("agent calls = %d, want 5", got)
	}
}

func TestQuota_RefusalWithProgressRestartsSeries(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := refusalsEveryTime(clk, true)
	cfg := quotaConfig()
	one := 1
	cfg.UsageLimit.MaxWaits = &one
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), cfg, runner, clk, true)

	for round := 1; round <= 3; round++ { // с max_waits=1 без прогресса вторая же попытка ушла бы в паузу
		q := rig.waitParked("s1")
		if q.Waits != 1 {
			t.Fatalf("round %d: waits = %d, want 1 (progress restarts the series)", round, q.Waits)
		}
		clk.Advance(3 * time.Minute)
		waitFor(t, "wake", func() bool { return runner.callCount() > round })
	}
	if rig.stage("s1").Status == state.StatusPaused {
		t.Fatal("refusals with progress must not exhaust max_waits")
	}
}

func TestQuota_PauseThenContinueRunsExactlyOneManualProbe(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call <= 2 {
			return limitRefusal(clk.Now().Add(time.Hour))
		}
		return nil
	}}
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clk, true)
	rig.waitParked("s1")
	ctx := context.Background()

	// Continue прямо из ожидания не принимается.
	if err := rig.orch.Continue(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	neverHappens(t, "direct Continue left awaiting_quota", func() bool { return rig.stage("s1").Status != state.StatusAwaitingQuota })

	if err := rig.orch.Pause(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if st := rig.stage("s1"); st.Status != state.StatusPaused || st.PausedFrom != state.StatusAwaitingQuota || st.Quota == nil {
		t.Fatalf("after Pause: %+v quota=%+v", st, st.Quota)
	}
	clk.Advance(2 * time.Hour)
	neverHappens(t, "paused quota stage woke by the old timer", func() bool { return runner.callCount() > 1 })

	// Continue из quota-паузы — одна ручная проба сразу, без ожидания таймера.
	if err := rig.orch.Continue(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "manual probe", func() bool { return runner.callCount() == 2 })
	q := rig.waitParked("s1")
	if q.Waits != 1 || q.ManualProbe || q.PauseReason != "" {
		t.Errorf("quota after the refused probe = %+v, want a new series (waits=1), no leftover probe intent", *q)
	}
	neverHappens(t, "a second probe before the new until", func() bool { return runner.callCount() > 2 })

	clk.Advance(2 * time.Hour)
	rig.waitStatus("s1", state.StatusDone)
	if got := runner.callCount(); got != 3 {
		t.Errorf("agent calls = %d, want 3", got)
	}
}

// Crash между Continue и spawn: журнал хранит awaiting_quota с намерением пробы — старт обязан запустить пробу, не
// дожидаясь until.
func TestQuota_ManualProbeSurvivesCrashBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	stages := quotaStages()
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "s1", "plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	far := quotaStart.Add(10 * time.Hour)
	apply := func(from, to state.StageStatus, ev string, q *state.QuotaWait) {
		t.Helper()
		if err := store.Apply(&state.Transition{StageID: "s1", From: from, To: to, Event: ev, Quota: q}); err != nil {
			t.Fatal(err)
		}
	}
	apply(state.StatusPending, state.StatusRunning, "test_setup", nil)
	apply(state.StatusRunning, state.StatusAwaitingQuota, string(bus.EvAwaitQuota), &state.QuotaWait{Provider: "claude", Until: far, Target: "implementation", Waits: 2})
	apply(state.StatusAwaitingQuota, state.StatusPaused, string(bus.EvPause), nil)
	apply(state.StatusPaused, state.StatusAwaitingQuota, string(bus.EvContinue), &state.QuotaWait{Provider: "claude", Until: far, Target: "implementation", ManualProbe: true})
	store.Close() // процесс «упал» до записи quota_reset и spawn

	runner := &quotaRunner{script: func(int, string) error { return nil }}
	rig := startQuotaRig(t, runDir, stages, quotaConfig(), runner, clock.NewFake(quotaStart), false)
	rig.waitStatus("s1", state.StatusDone)
	if got := runner.callCount(); got != 1 {
		t.Errorf("agent calls = %d, want exactly one probe", got)
	}
}

func TestQuota_ReopenKeepsWaitWithoutFailAndWaitsRemainder(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	reset := quotaStart.Add(2 * time.Minute)
	runner := &quotaRunner{script: func(call int, stageDir string) error {
		if call == 1 {
			action := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"make earlier-work"}}]}}` + "\n"
			if err := os.WriteFile(filepath.Join(stageDir, flow.PhaseJSONL(flow.PhaseImplementation)), []byte(action), 0o644); err != nil {
				return err
			}
			return limitRefusal(reset)
		}
		return nil
	}}
	rig := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clk, true)
	parked := rig.waitParked("s1")
	until := parked.Until

	rig.stop() // отмена run context: Ctrl+C
	events := journalEvents(t, runDir)
	if countEvent(events, bus.EvFail) != 0 {
		t.Fatalf("cancelling the run must not fail a waiting stage: events = %v", events)
	}
	rs, err := state.LoadRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s1"]; got.Status != state.StatusAwaitingQuota || got.Quota == nil || !got.Quota.Until.Equal(until) {
		t.Fatalf("journal after cancel = %+v quota=%+v, want awaiting_quota with the same until", got, got.Quota)
	}

	// Переоткрытие в 23:49: до until осталось чуть больше двух минут — таймер ждёт остаток.
	clk2 := clock.NewFake(quotaStart.Add(time.Minute))
	rig2 := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clk2, false)
	rig2.waitStatus("s1", state.StatusAwaitingQuota)
	clk2.BlockUntil(2)
	clk2.Advance(time.Minute + 30*time.Second) // 23:50:30 < until
	clk2.BlockUntil(2)
	if runner.callCount() != 1 {
		t.Fatalf("woke before the remainder elapsed: %d calls", runner.callCount())
	}
	clk2.Advance(time.Minute) // 23:51:30 > until
	rig2.waitStatus("s1", state.StatusDone)
	if got := runner.callCount(); got != 2 {
		t.Fatalf("agent calls = %d, want 2", got)
	}
	if prompt := runner.promptOf(2); !strings.Contains(prompt, "Previously completed actions") || !strings.Contains(prompt, "make earlier-work") {
		t.Errorf("fresh session after the wait must get the earlier actions even after reopen; prompt:\n%s", prompt)
	}
}

func TestQuota_ExpiredUntilAfterRestartStartsOneProcess(t *testing.T) {
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(quotaStart.Add(2 * time.Minute))
		}
		return nil
	}}
	rig := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clk, true)
	rig.waitParked("s1")
	rig.stop()

	rig2 := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clock.NewFake(quotaStart.Add(time.Hour)), false)
	rig2.waitStatus("s1", state.StatusDone)
	orchestrator.WaitAgentsForTest(rig2.orch)
	if got := runner.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2: the expired wait must start exactly one process", got)
	}
	if n := countEvent(journalEvents(t, runDir), bus.EvQuotaReset); n != 1 {
		t.Errorf("quota_reset count = %d, want 1", n)
	}
}

func TestQuota_ReviewPauseBlocksWakeUntilResumed(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(quotaStart.Add(2 * time.Minute))
		}
		return nil
	}}
	rig := startQuotaRig(t, t.TempDir(), quotaStages(), quotaConfig(), runner, clk, true)
	rig.waitParked("s1")
	ctx := context.Background()

	if _, err := rig.orch.PauseFlow(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Minute) // until прошёл, но review-пауза держит активацию
	neverHappens(t, "wake during review pause", func() bool {
		return runner.callCount() > 1 || rig.stage("s1").Status != state.StatusAwaitingQuota
	})

	if err := rig.orch.CancelNotesAndResume(ctx); err != nil {
		t.Fatal(err)
	}
	rig.waitStatus("s1", state.StatusDone)
	if got := runner.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2", got)
	}
}

func TestQuota_PlanningReturnsToPlanning(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(quotaStart.Add(2 * time.Minute))
		}
		return nil
	}}
	stages := []flow.Stage{{ID: "s1", Name: "S1", Description: "plan me", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}}
	rig := startQuotaRig(t, t.TempDir(), stages, quotaConfig(), runner, clk, false)

	q := rig.waitParked("s1")
	if q.Target != "planning" {
		t.Fatalf("target = %q, want planning", q.Target)
	}
	clk.Advance(5 * time.Minute)
	waitFor(t, "the resumed planning turn", func() bool { return runner.callCount() >= 2 })
	if kinds := runner.kindsOf(); kinds[0] != "planning" || kinds[1] != "planning" {
		t.Errorf("turns = %v, want the wake to return to planning, not implementation", kinds)
	}
	if q := rig.stage("s1").Quota; q != nil && q.Target != "planning" {
		t.Errorf("target = %q", q.Target)
	}
}

// Парк возвращает управление и lease сразу: при max_parallel=1 вторая стадия идёт, пока первая ждёт сброса.
func TestQuota_ParkReleasesLease(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(_ int, stageDir string) error {
		if filepath.Base(stageDir) == "s1" {
			// Другой провайдер: сюда проверяется только освобождение слота, а не hold (см. quota_hold_test.go).
			limit := limitRefusal(quotaStart.Add(time.Hour))
			limit.Provider = "codex"
			return limit
		}
		return nil
	}}
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Agents: []flow.AgentType{flow.AgentImplementation}, MaxParallel: 1},
		{ID: "s2", Name: "S2", Agents: []flow.AgentType{flow.AgentImplementation}, MaxParallel: 1},
	}
	rig := startQuotaRig(t, t.TempDir(), stages, quotaConfig(), runner, clk, true)
	rig.waitStatus("s1", state.StatusAwaitingQuota)
	rig.waitStatus("s2", state.StatusDone)
	if rig.stage("s1").Status != state.StatusAwaitingQuota {
		t.Errorf("s1 = %s, want still waiting", rig.stage("s1").Status)
	}
}

// Устаревший парк автора (эпоха ушла вперёд) и старый таймер (другой seq парка) не трогают новый ход/парк.
func TestQuota_StaleParkAndStaleTimerChangeNothing(t *testing.T) {
	runDir := t.TempDir()
	stages := quotaStages()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runner := &quotaRunner{script: func(int, string) error { return nil }}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: quotaConfig(),
		Prompts: orchestrator.DefaultPrompts(), Runner: runner, Clock: clock.NewFake(quotaStart),
	})
	fsm := bus.NewFSM(store)
	must := func(ev bus.FSMEvent, ctx bus.GuardCtx) uint64 {
		t.Helper()
		_, _, seq, ok, err := fsm.Apply("s1", ev, ctx, "")
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", ev, ok, err)
		}
		return seq
	}
	must(bus.EvReady, bus.GuardCtx{})
	must(bus.EvStartRun, bus.GuardCtx{})
	oldEpoch := store.Snapshot().Stages["s1"].Epoch

	// Pause → Continue отдают стадию другому ходу: эпоха выросла, парк старого автора не проходит.
	must(bus.EvPause, bus.GuardCtx{})
	must(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning})
	orchestrator.ParkForQuotaForTest(orch, stages[0], "implementation", oldEpoch, limitRefusal(quotaStart.Add(time.Hour)))
	if st := store.Snapshot().Stages["s1"]; st.Status != state.StatusRunning || st.Quota != nil {
		t.Fatalf("stale author park changed the stage: %+v quota=%+v", st, st.Quota)
	}

	// Настоящий парк, затем Pause → Continue из паузы: новый парк со своим seq.
	curEpoch := store.Snapshot().Stages["s1"].Epoch
	orchestrator.ParkForQuotaForTest(orch, stages[0], "implementation", curEpoch, limitRefusal(quotaStart.Add(time.Hour)))
	first := store.Snapshot().Stages["s1"]
	if first.Status != state.StatusAwaitingQuota {
		t.Fatalf("status = %s, want awaiting_quota", first.Status)
	}
	must(bus.EvPause, bus.GuardCtx{})
	must(bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusAwaitingQuota, Quota: &state.QuotaWait{Provider: "claude", Until: quotaStart.Add(time.Hour), Target: "implementation", ManualProbe: true}})
	second := store.Snapshot().Stages["s1"]
	if second.Seq == first.Seq {
		t.Fatal("new park must have a new seq")
	}
	orchestrator.WakeFromQuotaForTest(orch, "s1", first.Seq) // таймер первого парка
	if st := store.Snapshot().Stages["s1"]; st.Status != state.StatusAwaitingQuota || st.Seq != second.Seq {
		t.Fatalf("old timer moved the newer park: %+v", st)
	}
	if runner.callCount() != 0 {
		t.Errorf("old timer spawned an agent: %d calls", runner.callCount())
	}
}
