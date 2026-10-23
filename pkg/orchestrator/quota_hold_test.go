package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// holdStages — две независимые стадии одного провайдера (дефолтная команда claude).
func holdStages() []flow.Stage {
	return []flow.Stage{
		{ID: "a", Name: "A", Agents: []flow.AgentType{flow.AgentImplementation}},
		{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentImplementation}},
	}
}

// holdPair ждёт, пока обе стадии уйдут в awaiting_quota. Возвращает id и состояние стадии с реальным отказом и
// стадии, ждущей hold: кто из двух первым взял единственный слот команды (max_parallel=1), не определено.
func holdPair(t *testing.T, rig *quotaRig) (refusedID, heldID string, refused, held state.StageState) {
	t.Helper()
	waitFor(t, "both stages parked", func() bool {
		return rig.stage("a").Status == state.StatusAwaitingQuota && rig.stage("b").Status == state.StatusAwaitingQuota
	})
	if rig.stage("a").Quota.Source == state.QuotaSourceProviderHold {
		return "b", "a", rig.stage("b"), rig.stage("a")
	}
	return "a", "b", rig.stage("a"), rig.stage("b")
}

func TestQuotaHold_SecondStageOfTheProviderWaitsForTheParkUntil(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			return limitRefusal(clk.Now().Add(2 * time.Hour)) // отказ получает ту стадию, что первой взяла слот
		}
		return nil
	}}
	cfg := quotaConfig()
	cfg.Executor.MaxParallel = 1
	rig := startQuotaRig(t, t.TempDir(), holdStages(), cfg, runner, clk, true)

	_, _, refused, held := holdPair(t, rig)

	if refused.Quota.Waits != 1 {
		t.Errorf("refused stage waits = %d, want 1", refused.Quota.Waits)
	}
	if !held.Quota.Until.Equal(refused.Quota.Until) {
		t.Errorf("hold until = %s, want exactly the park's %s (no own jitter)", held.Quota.Until, refused.Quota.Until)
	}
	if held.Quota.Waits != 0 || held.Quota.PauseReason != "" || held.Quota.Provider != "claude" {
		t.Errorf("held quota = %+v, want waits=0 (a hold is not a refusal), no pause, provider claude", *held.Quota)
	}
	if got := runner.callCount(); got != 1 {
		t.Errorf("agent calls under the hold = %d, want 1 (only the refused one)", got)
	}

	rig.clk.BlockUntil(3) // таймеры двух парков и тикер dialog-поллера
	clk.Advance(2*time.Hour + 2*time.Minute)
	rig.waitStatus("a", state.StatusDone)
	rig.waitStatus("b", state.StatusDone)
	if got := runner.callCount(); got != 3 {
		t.Errorf("agent calls = %d, want 3 (refusal, retry of it, the held stage)", got)
	}
}

func TestQuotaHold_ManualProbeBypassesHoldOnceAndRefusalSetsNewUntil(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(call int, _ string) error {
		switch call {
		case 1:
			return limitRefusal(clk.Now().Add(2 * time.Hour))
		case 2:
			return limitRefusal(clk.Now().Add(3 * time.Hour)) // проба упёрлась в лимит повторно
		}
		return nil
	}}
	cfg := quotaConfig()
	cfg.Executor.MaxParallel = 1
	rig := startQuotaRig(t, t.TempDir(), holdStages(), cfg, runner, clk, true)
	_, heldID, refused, held := holdPair(t, rig)
	ctx := context.Background()

	if err := rig.orch.Pause(ctx, heldID); err != nil {
		t.Fatal(err)
	}
	if err := rig.orch.Continue(ctx, heldID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "manual probe despite the hold", func() bool { return runner.callCount() == 2 })

	q := rig.waitParkedAgain(heldID, held.Quota.Until)
	if q.Source == state.QuotaSourceProviderHold || !q.Until.After(refused.Quota.Until) || q.Waits != 1 || q.ManualProbe {
		t.Errorf("quota after the refused probe = %+v, want a fresh refusal (waits=1) with a later until than %s", *q, refused.Quota.Until)
	}
	neverHappens(t, "a second probe", func() bool { return runner.callCount() > 2 })
}

// waitParkedAgain ждёт, что стадия снова в awaiting_quota уже с другой парковкой (until отличается от old).
func (r *quotaRig) waitParkedAgain(id string, old time.Time) *state.QuotaWait {
	r.t.Helper()
	waitFor(r.t, "re-park of "+id, func() bool {
		st := r.stage(id)
		return st.Status == state.StatusAwaitingQuota && st.Quota != nil && !st.Quota.Until.Equal(old)
	})
	return r.stage(id).Quota
}

// --- серия отказов и прогресс, durable между рестартами ---

const progressAction = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"make real-progress"}}]}}` + "\n"

// reopenAfterRefusal: один отказ, остановка рана, (опционально) запись прогресса в лог фазы «другим процессом»,
// переоткрытие и второй отказ без limit.Progress. Возвращает серию второго отказа.
func secondRefusalWaits(t *testing.T, progressBetween bool) int {
	t.Helper()
	runDir := t.TempDir()
	clk := clock.NewFake(quotaStart)
	runner := &quotaRunner{script: func(int, string) error { return limitRefusal(clk.Now().Add(time.Hour)) }}
	rig := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clk, true)
	if q := rig.waitParked("s1"); q.Waits != 1 {
		t.Fatalf("first refusal waits = %d, want 1", q.Waits)
	}
	rig.stop()

	if progressBetween {
		path := filepath.Join(runDir, "s1", flow.PhaseJSONL(flow.PhaseImplementation))
		if err := os.WriteFile(path, []byte(progressAction), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	clk2 := clock.NewFake(quotaStart.Add(time.Minute))
	runner2 := &quotaRunner{script: func(int, string) error { return limitRefusal(clk2.Now().Add(time.Hour)) }}
	rig2 := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner2, clk2, false)
	rig2.waitStatus("s1", state.StatusAwaitingQuota)
	clk2.BlockUntil(2)
	clk2.Advance(2 * time.Hour)
	waitFor(t, "second refusal", func() bool { return runner2.callCount() == 1 })
	waitFor(t, "second park", func() bool { return rig2.stage("s1").Quota != nil && rig2.stage("s1").Quota.Until.After(clk2.Now()) })
	return rig2.stage("s1").Quota.Waits
}

func TestQuotaSeries_RefusalsWithoutProgressContinueTheSeriesAcrossRestart(t *testing.T) {
	if got := secondRefusalWaits(t, false); got != 2 {
		t.Errorf("second refusal without progress: waits = %d, want 2", got)
	}
}

func TestQuotaSeries_RecordedProgressBetweenRefusalsRestartsTheSeriesAcrossRestart(t *testing.T) {
	if got := secondRefusalWaits(t, true); got != 1 {
		t.Errorf("second refusal after recorded progress: waits = %d, want 1 (the series restarts durably)", got)
	}
}

// --- Revise-ход, припаркованный по hold'у или отказу, не теряет фидбек пользователя ---

// stageRunner — runner с исходом по стадии и номеру её вызова. prompts копит промпты стадии по порядку.
type stageRunner struct {
	mu      sync.Mutex
	calls   map[string]int
	prompts map[string][]string
	script  func(ctx context.Context, stageID string, call int) error
	// incomplete: ход завершается без .done (агент «забыл» закончить) — completion check находит незавершённую работу.
	incomplete func(stageID string, call int) bool
	// planText — текст plan.md, который пишет RunPlanning; по умолчанию валидный quotaPlan.
	planText func(stageID string, call int) string
	total    int
}

func newStageRunner(script func(ctx context.Context, stageID string, call int) error) *stageRunner {
	return &stageRunner{calls: map[string]int{}, prompts: map[string][]string{}, script: script}
}

func (r *stageRunner) enter(stageID, prompt string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[stageID]++
	r.total++
	r.prompts[stageID] = append(r.prompts[stageID], prompt)
	return r.calls[stageID]
}

func (r *stageRunner) totalCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

func (r *stageRunner) promptsOf(stageID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts[stageID]...)
}

func (r *stageRunner) RunPlanning(ctx context.Context, _, prompt, outFile, _ string) error {
	stageDir := filepath.Dir(outFile)
	n := r.enter(filepath.Base(stageDir), prompt)
	if err := r.script(ctx, filepath.Base(stageDir), n); err != nil {
		return err
	}
	text := quotaPlan
	if r.planText != nil {
		text = r.planText(filepath.Base(stageDir), n)
	}
	return os.WriteFile(outFile, []byte(text), 0o644)
}

func (r *stageRunner) RunAgent(ctx context.Context, _, _, prompt, logFile string) error {
	stageDir := filepath.Dir(logFile)
	n := r.enter(filepath.Base(stageDir), prompt)
	if err := r.script(ctx, filepath.Base(stageDir), n); err != nil {
		return err
	}
	if r.incomplete != nil && r.incomplete(filepath.Base(stageDir), n) {
		return nil
	}
	// .done — маркер implementation/review, execution_summary.md — autonomous.
	if err := os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("summary"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, ".done"), []byte("done"), 0o644)
}

func (r *stageRunner) RunJSONQuery(context.Context, string) ([]byte, error) { return nil, nil }

const reviseFeedbackText = "FEEDBACK-MARKER: please handle the edge case"

// feedbackCase — стадия, чей Revise паркуется: implementation и autonomous ревизуются из running, planning — из
// awaiting_approval (после первого плана).
type feedbackCase struct {
	name     string
	stage    flow.Stage
	planning bool
}

func feedbackCases() []feedbackCase {
	return []feedbackCase{
		{name: "implementation", stage: flow.Stage{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentImplementation}}},
		{name: "autonomous", stage: flow.Stage{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentAuto}}},
		{name: "planning", stage: flow.Stage{ID: "b", Name: "B", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}, planning: true},
	}
}

// feedbackRig — ран с revise-стадией b и стадией a того же провайдера (claude), которая даёт hold: её первый
// вызов отказывает, но только когда b уже стоит там, откуда её можно ревизовать (aMayRefuse).
type feedbackRig struct {
	*quotaRig
	runner      *stageRunner
	aMayRefuse  chan struct{}
	bEntered    chan struct{}
	clk         *clock.FakeClock
	bFirstCall  sync.Once
	refuseAfter time.Duration
}

// startFeedbackRig запускает ран. withHold: стадия a создаёт hold; иначе b одна, и отказывает сам feedback-ход.
func startFeedbackRig(t *testing.T, c feedbackCase, withHold bool) *feedbackRig {
	t.Helper()
	clk := clock.NewFake(quotaStart)
	f := &feedbackRig{clk: clk, aMayRefuse: make(chan struct{}), bEntered: make(chan struct{}), refuseAfter: 2 * time.Hour}
	var orch *orchestrator.Orchestrator
	f.runner = newStageRunner(func(ctx context.Context, id string, call int) error {
		switch {
		case id == "a" && call == 1:
			<-f.aMayRefuse
			return limitRefusal(clk.Now().Add(f.refuseAfter))
		case id == "b" && call == 1 && !c.planning:
			f.bFirstCall.Do(func() { close(f.bEntered) })
			ch, ok := orchestrator.InterruptChanForTest(orch, "b")
			if !ok {
				return errors.New("no interrupt channel for b")
			}
			select {
			case <-ch:
				return executor.ErrUserInterrupted
			case <-ctx.Done():
				return ctx.Err()
			}
		case id == "b" && !withHold && call == 2:
			return limitRefusal(clk.Now().Add(f.refuseAfter)) // отказ самого feedback-хода
		default:
		}
		return nil
	})
	stages := []flow.Stage{c.stage}
	if withHold {
		stages = []flow.Stage{{ID: "a", Name: "A", Agents: []flow.AgentType{flow.AgentAuto}}, c.stage}
	}
	cfg := quotaConfig()
	cfg.Executor.MaxParallel = 4
	// Planning-стадии без готового plan.md стартуют с planning; остальные — сразу с исполнения по seed-плану.
	f.quotaRig = startQuotaRig(t, t.TempDir(), stages, cfg, f.runner, clk, !c.planning, func(o *orchestrator.Orchestrator) {
		orch = o
		o.SetDashboardURL("http://127.0.0.1:0") // план ждёт ревью вместо авто-апрува
	})
	return f
}

// reviseB доводит b до состояния, из которого Revise применим, при необходимости создаёт hold стадией a и
// ревизует b.
func (f *feedbackRig) reviseB(c feedbackCase, withHold bool) {
	f.t.Helper()
	if c.planning {
		f.waitStatus("b", state.StatusAwaitingApproval)
	} else {
		<-f.bEntered
	}
	if withHold {
		close(f.aMayRefuse)
		f.waitStatus("a", state.StatusAwaitingQuota)
	}
	applied, _, err := f.orch.Revise(context.Background(), "b", reviseFeedbackText)
	if err != nil || !applied {
		f.t.Fatalf("Revise = %v, %v; want applied", applied, err)
	}
	f.waitStatus("b", state.StatusAwaitingQuota)
}

// assertFeedbackReaches проверяет, что последний ход b после пробуждения видит фидбек (и прежний план у planning).
func (f *feedbackRig) assertFeedbackReaches(c feedbackCase, parkedCalls int) {
	f.t.Helper()
	waitFor(f.t, "b's turn after the park", func() bool { return len(f.runner.promptsOf("b")) > parkedCalls })
	prompt := f.runner.promptsOf("b")[parkedCalls]
	if !strings.Contains(prompt, reviseFeedbackText) {
		f.t.Errorf("the turn after the park lost the Revise feedback; prompt:\n%s", prompt)
	}
	if c.planning && !strings.Contains(prompt, "Step 1") {
		f.t.Errorf("the planning turn after the park lost the previous plan; prompt:\n%s", prompt)
	}
}

func TestQuotaFeedback_ReviseTurnHeldByProviderKeepsFeedbackOnWake(t *testing.T) {
	for _, c := range feedbackCases() {
		t.Run(c.name, func(t *testing.T) {
			f := startFeedbackRig(t, c, true)
			f.reviseB(c, true)
			if q := f.stage("b").Quota; q == nil || q.Source != state.QuotaSourceProviderHold {
				t.Fatalf("b quota = %+v, want a provider_hold park", q)
			}
			parked := len(f.runner.promptsOf("b"))

			f.clk.BlockUntil(3) // таймеры парков a и b и тикер dialog-поллера
			f.clk.Advance(2*time.Hour + 2*time.Minute)

			f.assertFeedbackReaches(c, parked)
		})
	}
}

func TestQuotaFeedback_RefusedReviseTurnKeepsFeedbackOnWake(t *testing.T) {
	for _, c := range feedbackCases() {
		t.Run(c.name, func(t *testing.T) {
			f := startFeedbackRig(t, c, false)
			f.reviseB(c, false)
			parked := len(f.runner.promptsOf("b"))

			f.waitParked("b")
			f.clk.Advance(2*time.Hour + 2*time.Minute)

			f.assertFeedbackReaches(c, parked)
		})
	}
}

func TestQuotaFeedback_ManualProbeOfParkedReviseTurnKeepsFeedback(t *testing.T) {
	for _, c := range feedbackCases() {
		t.Run(c.name, func(t *testing.T) {
			f := startFeedbackRig(t, c, true)
			f.reviseB(c, true)
			parked := len(f.runner.promptsOf("b"))

			if err := f.orch.Pause(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}
			if err := f.orch.Continue(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}

			f.assertFeedbackReaches(c, parked)
		})
	}
}

// Review-ход Revise достижим только через review-pause (withFeedbackRunner), а не через Revise API, поэтому его
// парк готовится прямо в журнале: пробуждение после рестарта обязано возобновить feedback-запуск review.
func TestQuotaFeedback_ParkedReviewFeedbackTurnKeepsFeedbackAcrossRestart(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "r", Name: "R", Agents: []flow.AgentType{flow.AgentReview}}}
	store, err := state.Open(runDir, []string{"r"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "r", "feedback.md"), []byte(reviseFeedbackText), 0o644); err != nil {
		t.Fatal(err)
	}
	park := &state.QuotaWait{
		Provider: "claude", Source: state.QuotaSourceStructured, Target: "review", Waits: 1, Feedback: true,
		Until: quotaStart.Add(time.Hour),
	}
	for _, tr := range []state.Transition{
		{From: state.StatusPending, To: state.StatusReady, Event: "test_setup"},
		{From: state.StatusReady, To: state.StatusRunning, Event: "test_setup"},
		{From: state.StatusRunning, To: state.StatusAwaitingQuota, Event: string(bus.EvAwaitQuota), Quota: park},
	} {
		tr.StageID = "r"
		if err := store.Apply(&tr); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	clk := clock.NewFake(quotaStart.Add(2 * time.Hour)) // until уже прошёл: рестарт будит стадию сразу
	runner := newStageRunner(func(context.Context, string, int) error { return nil })
	rig := startQuotaRig(t, runDir, stages, quotaConfig(), runner, clk, false)

	rig.waitStatus("r", state.StatusDone)
	prompts := runner.promptsOf("r")
	if len(prompts) != 1 || !strings.Contains(prompts[0], reviseFeedbackText) {
		t.Errorf("review turn after the restart lost the Revise feedback; prompts = %q", prompts)
	}
}

// --- ручная проба обходит hold ровно один раз ---

// Проба снимает hold только для первого запуска после Continue: если тот вернул незавершённую работу, повторная
// попытка без перехода FSM снова проверяет hold (его держит другая стадия провайдера).
func TestQuotaHold_ManualProbeBypassesHoldForTheFirstAttemptOnly(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	var runner *stageRunner
	runner = newStageRunner(func(_ context.Context, _ string, _ int) error {
		if runner.totalCalls() == 1 {
			return limitRefusal(clk.Now().Add(2 * time.Hour)) // первая из двух стадий упирается в лимит
		}
		return nil
	})
	runner.incomplete = func(string, int) bool { return runner.totalCalls() == 2 } // проба не доделала работу
	cfg := quotaConfig()
	cfg.Executor.MaxParallel = 1
	rig := startQuotaRig(t, t.TempDir(), holdStages(), cfg, runner, clk, true)
	_, heldID, _, _ := holdPair(t, rig)
	ctx := context.Background()

	if err := rig.orch.Pause(ctx, heldID); err != nil {
		t.Fatal(err)
	}
	if err := rig.orch.Continue(ctx, heldID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the manual probe's attempt", func() bool { return runner.totalCalls() >= 2 })

	neverHappens(t, "a second author attempt bypassing the hold", func() bool { return runner.totalCalls() > 2 })
	waitFor(t, "the second attempt held by the provider", func() bool {
		q := rig.stage(heldID).Quota
		return rig.stage(heldID).Status == state.StatusAwaitingQuota && q != nil && q.Source == state.QuotaSourceProviderHold && !q.ManualProbe
	})
}

// --- вложенные процессы одного хода тоже проходят admission ---

// Второй процесс внутри хода автора (re-prompt недостающих секций плана, встроенный review) стартует после
// единственной проверки перед ходом; hold, появившийся между двумя процессами, обязан его остановить, а стадия —
// припарковаться как provider_hold тем же путём, что и перед ходом.
func TestQuotaHold_NestedProcessIsAdmittedAgain(t *testing.T) {
	cases := []struct {
		name  string
		stage flow.Stage
		// planning: первый процесс пишет план без обязательных секций, и тот требует re-prompt.
		planning bool
	}{
		{name: "planning re-prompt", stage: flow.Stage{ID: "n", Name: "N", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}, planning: true},
		{name: "embedded review", stage: flow.Stage{ID: "n", Name: "N", Agents: []flow.AgentType{flow.AgentImplementation, flow.AgentReview}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := clock.NewFake(quotaStart)
			firstDone := make(chan struct{}) // первый процесс n уже начался
			holdReady := make(chan struct{}) // hold создан: первый процесс n может вернуться
			var once sync.Once
			runner := newStageRunner(func(_ context.Context, id string, call int) error {
				switch {
				case id == "a" && call == 1:
					<-firstDone
					return limitRefusal(clk.Now().Add(2 * time.Hour))
				case id == "n" && call == 1:
					once.Do(func() { close(firstDone) })
					<-holdReady
				default:
				}
				return nil
			})
			runner.planText = func(string, int) string { return "# no required sections" }
			cfg := quotaConfig()
			cfg.Executor.MaxParallel = 4
			stages := []flow.Stage{{ID: "a", Name: "A", Agents: []flow.AgentType{flow.AgentAuto}}, c.stage}
			rig := startQuotaRig(t, t.TempDir(), stages, cfg, runner, clk, !c.planning, func(o *orchestrator.Orchestrator) {
				o.SetDashboardURL("http://127.0.0.1:0")
			})

			<-firstDone
			rig.waitStatus("a", state.StatusAwaitingQuota)
			close(holdReady)

			rig.waitStatus("n", state.StatusAwaitingQuota)
			if q := rig.stage("n").Quota; q == nil || q.Source != state.QuotaSourceProviderHold {
				t.Errorf("n quota = %+v, want a provider_hold park", q)
			}
			if got := len(runner.promptsOf("n")); got != 1 {
				t.Errorf("n processes started = %d, want 1 (the nested process must not start under the hold)", got)
			}
		})
	}
}
